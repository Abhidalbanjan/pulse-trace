package ingestproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// normMetric is a version-agnostic view of a Datadog metric series, so the v1
// ([[ts,val]] arrays, string type) and v2 ([{timestamp,value}] objects, int
// type) payloads share one OTLP translator.
type normMetric struct {
	name   string
	kind   string // gauge | count | rate
	unit   string
	attrs  []*commonpb.KeyValue
	points []normPoint
}

type normPoint struct {
	tsNanos uint64
	value   float64
}

// DatadogSeries handles /api/v1/series and /api/v2/series (both JSON). The path
// selects the wire version.
func (p *Proxy) DatadogSeries(w http.ResponseWriter, r *http.Request) {
	tenantID, tier, status, ok := p.resolveTenant(r.Context(), datadogKey(r))
	if !ok {
		http.Error(w, "invalid or missing DD-API-KEY", status)
		return
	}
	body, err := readBody(r, 16<<20)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var metrics []normMetric
	if strings.HasPrefix(r.URL.Path, "/api/v2/") {
		metrics, err = parseDDV2Series(body)
	} else {
		metrics, err = parseDDV1Series(body)
	}
	if err != nil {
		http.Error(w, "invalid Datadog series payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req := normMetricsToOTLP(metrics); len(req.GetResourceMetrics()) > 0 {
		if err := p.fwd.ForwardMetrics(r.Context(), tenantID, tier, req); err != nil {
			httpForwardError(w, err)
			return
		}
	}
	// DD's intake replies with a small JSON ack.
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// ── v1 (/api/v1/series) ────────────────────────────────────────────────────

type ddV1Series struct {
	Series []ddV1Metric `json:"series"`
}

type ddV1Metric struct {
	Metric string       `json:"metric"`
	Points [][2]float64 `json:"points"` // [epoch_seconds, value]
	Type   string       `json:"type"`   // gauge | count | rate
	Host   string       `json:"host"`
	Tags   []string     `json:"tags"`
}

func parseDDV1Series(body []byte) ([]normMetric, error) {
	var s ddV1Series
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	out := make([]normMetric, 0, len(s.Series))
	for _, m := range s.Series {
		kind := m.Type
		if kind == "" {
			kind = "gauge"
		}
		pts := make([]normPoint, 0, len(m.Points))
		for _, pt := range m.Points {
			pts = append(pts, normPoint{tsNanos: secondsToNanos(pt[0]), value: pt[1]})
		}
		out = append(out, normMetric{
			name:   m.Metric,
			kind:   kind,
			attrs:  ddTagAttrs(m.Tags, m.Host),
			points: pts,
		})
	}
	return out, nil
}

// ── v2 (/api/v2/series) ────────────────────────────────────────────────────

type ddV2Series struct {
	Series []ddV2Metric `json:"series"`
}

type ddV2Metric struct {
	Metric    string         `json:"metric"`
	Type      int            `json:"type"` // 0 unspec, 1 count, 2 rate, 3 gauge
	Points    []ddV2Point    `json:"points"`
	Resources []ddV2Resource `json:"resources"`
	Tags      []string       `json:"tags"`
	Unit      string         `json:"unit"`
}

type ddV2Point struct {
	Timestamp int64   `json:"timestamp"` // epoch seconds
	Value     float64 `json:"value"`
}

type ddV2Resource struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func parseDDV2Series(body []byte) ([]normMetric, error) {
	var s ddV2Series
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, err
	}
	out := make([]normMetric, 0, len(s.Series))
	for _, m := range s.Series {
		var host string
		for _, res := range m.Resources {
			if res.Type == "host" {
				host = res.Name
			}
		}
		pts := make([]normPoint, 0, len(m.Points))
		for _, pt := range m.Points {
			pts = append(pts, normPoint{tsNanos: secondsToNanos(float64(pt.Timestamp)), value: pt.Value})
		}
		out = append(out, normMetric{
			name:   m.Metric,
			kind:   ddV2Kind(m.Type),
			unit:   m.Unit,
			attrs:  ddTagAttrs(m.Tags, host),
			points: pts,
		})
	}
	return out, nil
}

func ddV2Kind(t int) string {
	switch t {
	case 1:
		return "count"
	case 2:
		return "rate"
	case 3:
		return "gauge"
	default:
		return "gauge"
	}
}

// ── shared translation ─────────────────────────────────────────────────────

// normMetricsToOTLP builds a single OTLP metrics export. A Datadog "count" maps
// to a monotonic delta Sum; "gauge" and "rate" map to a Gauge (a rate is an
// instantaneous per-second value, which is gauge-like). The tenant Resource is
// stamped later by ForwardMetrics.
func normMetricsToOTLP(metrics []normMetric) *colmetricspb.ExportMetricsServiceRequest {
	otlp := make([]*metricspb.Metric, 0, len(metrics))
	for _, m := range metrics {
		dps := make([]*metricspb.NumberDataPoint, 0, len(m.points))
		for _, pt := range m.points {
			dps = append(dps, &metricspb.NumberDataPoint{
				TimeUnixNano: pt.tsNanos,
				Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: pt.value},
				Attributes:   m.attrs,
			})
		}
		metric := &metricspb.Metric{Name: m.name, Unit: m.unit}
		if m.kind == "count" {
			metric.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				IsMonotonic:            true,
				DataPoints:             dps,
			}}
		} else {
			metric.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: dps}}
		}
		otlp = append(otlp, metric)
	}
	if len(otlp) == 0 {
		return &colmetricspb.ExportMetricsServiceRequest{}
	}
	return &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope:   &commonpb.InstrumentationScope{Name: "pulsetrace/datadog"},
				Metrics: otlp,
			}},
		}},
	}
}

// ddTagAttrs turns Datadog "key:value" tags (plus an optional host) into OTLP
// attributes. A tag without a colon becomes a key with an empty value.
func ddTagAttrs(tags []string, host string) []*commonpb.KeyValue {
	var attrs []*commonpb.KeyValue
	if host != "" {
		attrs = append(attrs, strAttr("host.name", host))
	}
	for _, tag := range tags {
		if k, v, found := strings.Cut(tag, ":"); found {
			attrs = append(attrs, strAttr(k, v))
		} else {
			attrs = append(attrs, strAttr(tag, ""))
		}
	}
	return attrs
}

func secondsToNanos(sec float64) uint64 {
	if sec <= 0 {
		return 0
	}
	return uint64(sec * 1e9)
}
