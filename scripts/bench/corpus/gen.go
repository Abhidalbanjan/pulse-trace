// Command gen produces the benchmark corpus: a deterministic, reproducible body
// of telemetry that both PulseTrace and OpenObserve are loaded with, so
// BENCHMARK.md compares engines rather than datasets.
//
// Design constraints, and why each matters:
//
//   - Byte-reproducible from a seed. A benchmark whose input drifts cannot
//     detect a regression: you could never tell a slower engine from a heavier
//     corpus. gen_test.go pins this with a SHA-256 equality check.
//
//   - No wall-clock in the payload. Timestamps are derived from a fixed epoch
//     plus a deterministic offset, otherwise "same seed" would still yield
//     different bytes on every run.
//
//   - Explicit, documented cardinality. Storage size and group-by cost are
//     dominated by cardinality, so it is a stated parameter rather than an
//     accident: service×40, pod×2000, customer_id×50k. The customer dimension
//     is deliberately high-cardinality — that is the case that separates a
//     columnar store from an inverted index, and the one a vendor benchmark
//     usually omits.
//
//   - Realistic shape, not random noise. Compressibility decides bytes-on-disk,
//     and uniformly random strings compress nothing like real logs do. Messages
//     are drawn from templates with structured parameters, which is roughly how
//     application logs actually behave.
//
// Output is newline-delimited JSON on stdout (or --out), one record per line,
// tagged with a `signal` field so loaders can route it. Emitting a single
// ordered stream keeps the corpus a single hashable artifact.
//
// Usage:
//
//	go run ./scripts/bench/corpus --size-mb 10240 --seed 42 --out corpus.ndjson
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"io"
	"math/rand"
	"os"
	"time"
)

// epoch is the fixed base timestamp. Deliberately not time.Now(): the corpus
// must be identical across runs and machines.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// Signal mix, as a fraction of total bytes. Logs dominate because they dominate
// real observability spend, which is what the cost comparison is about.
const (
	fracLogs    = 0.70
	fracTraces  = 0.20
	fracMetrics = 0.10
)

// Cardinality profile. See the package comment — these are the numbers that
// decide the result, so they are named and not buried.
const (
	numServices  = 40
	numPods      = 2000
	numCustomers = 50000
	numHosts     = 200
)

var levels = []string{"INFO", "INFO", "INFO", "INFO", "WARN", "ERROR", "DEBUG"}

// messageTemplates keep the corpus compressible in the way real logs are: a
// small set of shapes with varying parameters.
var messageTemplates = []string{
	"request completed method=%s path=%s status=%d duration_ms=%d",
	"connection pool utilisation at %d%% active=%d idle=%d",
	"cache %s key=%s ttl_s=%d",
	"upstream call service=%s status=%d retries=%d",
	"query executed rows=%d duration_ms=%d plan=%s",
	"authentication %s user_id=%d method=%s",
}

var (
	methods   = []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	paths     = []string{"/api/v1/orders", "/api/v1/cart", "/api/v1/checkout", "/healthz", "/api/v1/catalog", "/api/v1/users"}
	statuses  = []int{200, 200, 200, 201, 204, 400, 404, 429, 500, 503}
	cacheOps  = []string{"hit", "miss", "evict"}
	plans     = []string{"seq_scan", "index_scan", "bitmap_heap_scan", "nested_loop"}
	authKinds = []string{"succeeded", "failed", "token_refreshed"}
	opNames   = []string{"http.request", "db.query", "cache.get", "kafka.publish", "grpc.call"}
	metricSet = []string{"http_requests_total", "http_request_duration_seconds", "process_cpu_seconds_total", "go_goroutines", "queue_depth"}
)

type record struct {
	Signal    string            `json:"signal"`
	Timestamp string            `json:"timestamp"`
	Service   string            `json:"service,omitempty"`
	Level     string            `json:"level,omitempty"`
	Message   string            `json:"message,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	SpanID    string            `json:"span_id,omitempty"`
	ParentID  string            `json:"parent_span_id,omitempty"`
	Operation string            `json:"operation,omitempty"`
	Duration  int64             `json:"duration_us,omitempty"`
	Metric    string            `json:"metric,omitempty"`
	Value     float64           `json:"value,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// gen holds the deterministic state. Every value in the corpus comes from this
// one source, so the seed fully determines the output.
type gen struct {
	r   *rand.Rand
	seq int64
}

func (g *gen) pick(ss []string) string { return ss[g.r.Intn(len(ss))] }
func (g *gen) pickInt(is []int) int    { return is[g.r.Intn(len(is))] }

// ts advances a deterministic clock: each record is a fixed step later than the
// last, spread over a 24h window so time-range queries have something to cut.
func (g *gen) ts() time.Time {
	g.seq++
	return epoch.Add(time.Duration(g.seq) * 800 * time.Microsecond)
}

func (g *gen) hex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.r.Intn(256))
	}
	return hex.EncodeToString(b)
}

func (g *gen) service() string  { return fmt.Sprintf("svc-%02d", g.r.Intn(numServices)) }
func (g *gen) pod() string      { return fmt.Sprintf("pod-%04d", g.r.Intn(numPods)) }
func (g *gen) customer() string { return fmt.Sprintf("cust-%05d", g.r.Intn(numCustomers)) }
func (g *gen) host() string     { return fmt.Sprintf("host-%03d", g.r.Intn(numHosts)) }

func (g *gen) message() string {
	switch tpl := g.pick(messageTemplates); tpl {
	case messageTemplates[0]:
		return fmt.Sprintf(tpl, g.pick(methods), g.pick(paths), g.pickInt(statuses), g.r.Intn(4000))
	case messageTemplates[1]:
		return fmt.Sprintf(tpl, g.r.Intn(100), g.r.Intn(50), g.r.Intn(50))
	case messageTemplates[2]:
		return fmt.Sprintf(tpl, g.pick(cacheOps), g.customer(), g.r.Intn(3600))
	case messageTemplates[3]:
		return fmt.Sprintf(tpl, g.service(), g.pickInt(statuses), g.r.Intn(4))
	case messageTemplates[4]:
		return fmt.Sprintf(tpl, g.r.Intn(10000), g.r.Intn(2000), g.pick(plans))
	default:
		return fmt.Sprintf(messageTemplates[5], g.pick(authKinds), g.r.Intn(numCustomers), g.pick(methods))
	}
}

func (g *gen) logRecord() record {
	return record{
		Signal:    "log",
		Timestamp: g.ts().UTC().Format(time.RFC3339Nano),
		Service:   g.service(),
		Level:     g.pick(levels),
		Message:   g.message(),
		TraceID:   g.hex(16),
		Attrs: map[string]string{
			"pod":         g.pod(),
			"host":        g.host(),
			"customer_id": g.customer(),
			"env":         "production",
		},
	}
}

func (g *gen) traceRecord() record {
	return record{
		Signal:    "trace",
		Timestamp: g.ts().UTC().Format(time.RFC3339Nano),
		Service:   g.service(),
		TraceID:   g.hex(16),
		SpanID:    g.hex(8),
		ParentID:  g.hex(8),
		Operation: g.pick(opNames),
		Duration:  int64(g.r.Intn(2_000_000)),
		Attrs: map[string]string{
			"pod":         g.pod(),
			"customer_id": g.customer(),
			"status_code": fmt.Sprintf("%d", g.pickInt(statuses)),
		},
	}
}

func (g *gen) metricRecord() record {
	return record{
		Signal:    "metric",
		Timestamp: g.ts().UTC().Format(time.RFC3339Nano),
		Service:   g.service(),
		Metric:    g.pick(metricSet),
		Value:     float64(g.r.Intn(100000)) / 100.0,
		Attrs: map[string]string{
			"pod":  g.pod(),
			"host": g.host(),
			"env":  "production",
		},
	}
}

// Generate writes the corpus to w until targetBytes of JSON have been emitted,
// returning the byte count and the SHA-256 of everything written.
func Generate(w io.Writer, seed int64, targetBytes int64) (int64, string, error) {
	g := &gen{r: rand.New(rand.NewSource(seed))}
	h := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(w, h), 1<<20)

	var written int64
	logBudget := int64(float64(targetBytes) * fracLogs)
	traceBudget := int64(float64(targetBytes) * fracTraces)
	metricBudget := targetBytes - logBudget - traceBudget

	emit := func(rec record) error {
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if _, err := bw.Write(b); err != nil {
			return err
		}
		written += int64(len(b))
		return nil
	}

	// Signals are emitted in contiguous blocks rather than interleaved: loaders
	// batch by signal, and a deterministic order is required for the hash.
	for _, blk := range []struct {
		budget int64
		next   func() record
	}{
		{logBudget, g.logRecord},
		{traceBudget, g.traceRecord},
		{metricBudget, g.metricRecord},
	} {
		start := written
		for written-start < blk.budget {
			if err := emit(blk.next()); err != nil {
				return written, "", err
			}
		}
	}

	if err := bw.Flush(); err != nil {
		return written, "", err
	}
	return written, hex.EncodeToString(h.(hash.Hash).Sum(nil)), nil
}

func main() {
	var (
		sizeMB = flag.Int64("size-mb", 10240, "target corpus size in MiB (default 10 GiB)")
		seed   = flag.Int64("seed", 42, "PRNG seed — the same seed must always produce identical bytes")
		out    = flag.String("out", "", "output file (default: stdout)")
	)
	flag.Parse()

	w := io.Writer(os.Stdout)
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create %s: %v\n", *out, err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}

	target := *sizeMB * 1024 * 1024
	n, sum, err := Generate(w, *seed, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate: %v\n", err)
		os.Exit(1)
	}
	// To stderr so it cannot contaminate a stdout corpus.
	fmt.Fprintf(os.Stderr, "corpus: %d bytes (%.2f MiB) seed=%d sha256=%s\n",
		n, float64(n)/1024/1024, *seed, sum)
}
