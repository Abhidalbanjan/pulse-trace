package handler

// Multi-series metric math (Metrics · E4).
//
// Real dashboards need derived series — error ratio a/b, percentage a/b*100,
// headroom limit-used. This evaluates a user-supplied expression over several
// metric queries aligned by time bucket. The expression is run through a tiny
// hand-written recursive-descent evaluator over a closed grammar (numbers,
// single-letter series vars, + - * / and parentheses) — never eval/reflection —
// so a hostile expression can't do anything but arithmetic. The evaluator is
// pure and heavily unit-tested (precedence, parens, injection, divide-by-zero).

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
)

// ── Safe arithmetic evaluator ────────────────────────────────────────────────

type exprParser struct {
	s    string
	pos  int
	vars map[string]float64
}

// errDivZero is returned for division by zero so the caller can drop that bucket
// (a hole in a derived series) rather than emitting Inf/NaN.
var errDivZero = fmt.Errorf("division by zero")

// evalMetricExpr evaluates expr against the provided single-letter variables.
// Grammar: expr := term (('+'|'-') term)* ; term := factor (('*'|'/') factor)* ;
// factor := number | var | '(' expr ')' | '-' factor. Any other character, an
// unknown variable, or trailing input is an error — the input is arithmetic-only
// by construction. Pure.
func evalMetricExpr(expr string, vars map[string]float64) (float64, error) {
	p := &exprParser{s: expr, vars: vars}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos != len(p.s) {
		return 0, fmt.Errorf("unexpected %q at position %d", string(p.s[p.pos]), p.pos)
	}
	return v, nil
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

func (p *exprParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.s) {
			return v, nil
		}
		op := p.s[p.pos]
		if op != '+' && op != '-' {
			return v, nil
		}
		p.pos++
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

func (p *exprParser) parseTerm() (float64, error) {
	v, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.s) {
			return v, nil
		}
		op := p.s[p.pos]
		if op != '*' && op != '/' {
			return v, nil
		}
		p.pos++
		rhs, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			v *= rhs
		} else {
			if rhs == 0 {
				return 0, errDivZero
			}
			v /= rhs
		}
	}
}

func (p *exprParser) parseFactor() (float64, error) {
	p.skipSpace()
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	c := p.s[p.pos]
	switch {
	case c == '-':
		p.pos++
		v, err := p.parseFactor()
		return -v, err
	case c == '(':
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return v, nil
	case c >= 'a' && c <= 'z':
		p.pos++
		// Reject multi-letter identifiers (functions/keywords) — vars are single letters.
		if p.pos < len(p.s) && p.s[p.pos] >= 'a' && p.s[p.pos] <= 'z' {
			return 0, fmt.Errorf("unknown identifier near position %d", p.pos)
		}
		val, ok := p.vars[string(c)]
		if !ok {
			return 0, fmt.Errorf("undefined series %q", string(c))
		}
		return val, nil
	case (c >= '0' && c <= '9') || c == '.':
		return p.parseNumber()
	default:
		return 0, fmt.Errorf("unexpected character %q at position %d", string(c), p.pos)
	}
}

func (p *exprParser) parseNumber() (float64, error) {
	start := p.pos
	dots := 0
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c >= '0' && c <= '9' {
			p.pos++
		} else if c == '.' {
			dots++
			p.pos++
		} else {
			break
		}
	}
	if dots > 1 {
		return 0, fmt.Errorf("malformed number at position %d", start)
	}
	var v float64
	if _, err := fmt.Sscanf(p.s[start:p.pos], "%g", &v); err != nil {
		return 0, fmt.Errorf("malformed number %q", p.s[start:p.pos])
	}
	return v, nil
}

// referencedVars returns the distinct single-letter variables (a–z) used in expr,
// so the handler fetches exactly the series the formula needs.
func referencedVars(expr string) []string {
	seen := map[byte]bool{}
	out := []string{}
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if c >= 'a' && c <= 'z' && !seen[c] {
			seen[c] = true
			out = append(out, string(c))
		}
	}
	sort.Strings(out)
	return out
}

// ── Formula endpoint ─────────────────────────────────────────────────────────

// fetchFormulaSeries runs one variable's metric query aggregated to a single
// value per time bucket (across services), returning bucket→value.
func (h *MetricsHandler) fetchFormulaSeries(tenant, table, metric, valueExpr, service, sqlInterval, bucketExpr string) (map[string]float64, error) {
	params := map[string]string{"metric": stringParam(metric), "tenant": tenant}
	whereService := ""
	if service != "" {
		params["service"] = stringParam(service)
		whereService = "AND ServiceName = {service:String}"
	}
	query := fmt.Sprintf(`
		SELECT %s as time_bucket, %s as value
		FROM pulsetrace.%s
		WHERE ResourceAttributes['tenant.id'] = {tenant:String} AND MetricName = {metric:String} %s AND TimeUnix >= now() - INTERVAL %s
		GROUP BY time_bucket ORDER BY time_bucket ASC FORMAT JSON`,
		bucketExpr, valueExpr, table, whereService, sqlInterval)

	resp, err := h.ch.queryScoped(tenant, query, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return map[string]float64{}, nil
	}
	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(result.Data))
	for _, row := range result.Data {
		out[toStr(row["time_bucket"])] = toFloat(row["value"])
	}
	return out, nil
}

// QueryFormula evaluates a multi-series expression aligned by time bucket.
//
//	GET /api/v1/metrics/formula?expr=a/b*100
//	    &a_metric=&a_type=gauge&a_fn=avg&b_metric=&b_type=sum&b_fn=rate
//	    &service=&interval=1h
func (h *MetricsHandler) QueryFormula(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()

	expr := strings.TrimSpace(q.Get("expr"))
	if expr == "" {
		http.Error(w, "missing required 'expr' query param", http.StatusBadRequest)
		return
	}
	vars := referencedVars(expr)
	if len(vars) == 0 {
		http.Error(w, "expr must reference at least one series variable (a–z)", http.StatusBadRequest)
		return
	}
	// Validate the expression syntax up front (dummy values) so a bad formula is a
	// clean 400 before we run any ClickHouse queries.
	dummy := make(map[string]float64, len(vars))
	for _, v := range vars {
		dummy[v] = 1
	}
	if _, err := evalMetricExpr(expr, dummy); err != nil && err != errDivZero {
		http.Error(w, "invalid expression: "+err.Error(), http.StatusBadRequest)
		return
	}

	interval := q.Get("interval")
	sqlInterval, ok := intervalToSQL[interval]
	if !ok {
		interval = "1h"
		sqlInterval = intervalToSQL[interval]
	}
	bucketExpr := metricIntervalToBucket[interval]
	bucketSeconds := metricIntervalBucketSeconds[interval]
	service := q.Get("service")
	tenant := tenantFromRequest(r)

	// Fetch each referenced series, aggregated to one value per bucket.
	seriesByVar := make(map[string]map[string]float64, len(vars))
	for _, v := range vars {
		metric := q.Get(v + "_metric")
		if metric == "" {
			http.Error(w, fmt.Sprintf("series %q is referenced but %s_metric is missing", v, v), http.StatusBadRequest)
			return
		}
		table, ok := metricTableFor[q.Get(v+"_type")]
		if !ok {
			http.Error(w, fmt.Sprintf("%s_type must be one of: gauge, sum", v), http.StatusBadRequest)
			return
		}
		valueExpr, ok := metricAggExpr(q.Get(v+"_fn"), bucketSeconds)
		if !ok {
			http.Error(w, fmt.Sprintf("%s_fn must be one of: avg, min, max, sum, rate, p50, p90, p95, p99", v), http.StatusBadRequest)
			return
		}
		s, err := h.fetchFormulaSeries(tenant, table, metric, valueExpr, service, sqlInterval, bucketExpr)
		if err != nil {
			log.Printf("[MetricsHandler.QueryFormula] series %q query failed: %v", v, err)
			http.Error(w, "failed to query analytics engine", http.StatusInternalServerError)
			return
		}
		seriesByVar[v] = s
	}

	// Union of buckets across all series; evaluate only where every referenced
	// series has a value (a derived point needs all its inputs).
	bucketSet := map[string]struct{}{}
	for _, s := range seriesByVar {
		for b := range s {
			bucketSet[b] = struct{}{}
		}
	}
	buckets := make([]string, 0, len(bucketSet))
	for b := range bucketSet {
		buckets = append(buckets, b)
	}
	sort.Strings(buckets)

	type point struct {
		TimeBucket string  `json:"time_bucket"`
		Value      float64 `json:"value"`
	}
	out := make([]point, 0, len(buckets))
	for _, b := range buckets {
		vals := make(map[string]float64, len(vars))
		complete := true
		for _, v := range vars {
			val, ok := seriesByVar[v][b]
			if !ok {
				complete = false
				break
			}
			vals[v] = val
		}
		if !complete {
			continue
		}
		res, err := evalMetricExpr(expr, vals)
		if err != nil {
			if err == errDivZero {
				continue // skip the bucket rather than emit Inf
			}
			http.Error(w, "invalid expression: "+err.Error(), http.StatusBadRequest)
			return
		}
		out = append(out, point{TimeBucket: b, Value: res})
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"expr": expr, "series": out})
}
