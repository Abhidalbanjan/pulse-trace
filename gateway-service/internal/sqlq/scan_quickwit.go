package sqlq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Quickwit-backed relations (logs).

// tenantIDPattern is what a tenant identifier may contain.
//
// The other two scanners bind the tenant as a parameter, so its contents cannot
// change their statement's meaning. Quickwit 0.8 has no bind parameters — the
// query is a string in its own language — so the tenant is concatenated, and
// that is the one place in this package where a value becomes syntax.
//
// Rather than escape, this refuses. Tenant IDs are VARCHAR(50) drawn from
// account provisioning, not user input, and every real one matches this
// pattern; an escaping routine would be a second thing to get right, on the
// only path where getting it wrong crosses a tenant boundary. If a tenant ID
// ever legitimately needs a character outside this set, that is a conversation,
// not a silent widening.
var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,49}$`)

// QuickwitScanner materialises the `logs` relation.
type QuickwitScanner struct {
	Rel    Relation
	URL    string // Quickwit endpoint, e.g. http://quickwit:7280
	Index  string
	Client *http.Client
}

func (s *QuickwitScanner) Relation() Relation { return s.Rel }

// indexField maps a catalog column to the field name Quickwit knows it by.
//
// Declared columns are stored lowercase and match one-to-one. Attribute columns
// keep the customer's spelling and address a nested field, which the index
// resolves because its dynamic mapping sets expand_dots.
func (s *QuickwitScanner) indexField(column string) string {
	if key := s.Rel.AttrKey(column); key != "" {
		return s.Rel.AttrPrefix + "." + key
	}
	return strings.ToLower(column)
}

// termValuePattern is what a pushed-down filter value may contain.
//
// Quickwit 0.8 has no bind parameters, so a filter value becomes part of the
// query string — the same problem the tenant id has, and it gets the same
// answer: refuse rather than escape. The value is emitted inside double quotes,
// where the only two characters that can end the quoting are `"` and `\`, so
// those are excluded along with control characters. Everything else — spaces,
// colons, the boolean keywords — is inert once quoted and stays allowed,
// because a log message value like `connection refused` is an ordinary thing to
// filter on.
//
// A value outside this set is not silently dropped or silently escaped; the
// scanner errors, and the engine surfaces it. Refusing to answer is recoverable
// for the user. Answering a different question is not.
var termValuePattern = regexp.MustCompile(`^[^"\\\x00-\x1f]{1,256}$`)

// searchQuery returns the Quickwit query string for one tenant and filter set.
// Split out so the isolation property is assertable without a running Quickwit.
func (s *QuickwitScanner) searchQuery(tenantID string, where []Predicate) (string, error) {
	if !tenantIDPattern.MatchString(tenantID) {
		return "", fmt.Errorf("quickwit scanner: refusing tenant id %q: outside the permitted character set", tenantID)
	}
	// The tenant clause is built first and joined with AND, so no filter can
	// widen the set of tenants the query reaches — the worst a hostile value
	// could do, if it escaped its quoting, is narrow or broaden within one
	// tenant. It cannot escape its quoting, but the clause order means the
	// isolation argument does not rest on that being true.
	q := "tenant_id:" + tenantID
	for _, p := range where {
		if !s.Rel.HasColumn(p.Column) {
			return "", fmt.Errorf("quickwit scanner: %q is not a column of %s", p.Column, s.Rel.Name)
		}
		if !termValuePattern.MatchString(p.Value) {
			return "", fmt.Errorf("quickwit scanner: refusing filter value for %q: outside the permitted character set", p.Column)
		}
		q += fmt.Sprintf(` AND %s:"%s"`, s.indexField(p.Column), p.Value)
	}
	return q, nil
}

func (s *QuickwitScanner) Scan(ctx context.Context, tenantID string, limit int) (*Rows, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("quickwit scanner: refusing to scan with an empty tenant")
	}
	q, err := s.searchQuery(tenantID, nil)
	if err != nil {
		return nil, err
	}

	index := s.Index
	if index == "" {
		index = "pulsetrace-logs"
	}
	params := url.Values{}
	params.Set("query", q)
	params.Set("max_hits", fmt.Sprint(limit))
	endpoint := fmt.Sprintf("%s/api/v1/%s/search?%s", strings.TrimRight(s.URL, "/"), index, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("quickwit scanner: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quickwit scanner: status %d: %s", resp.StatusCode, firstBytes(body, 200))
	}

	var payload struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("quickwit scanner: decode: %w", err)
	}

	// Project onto the catalog's columns rather than returning whatever the
	// index happens to hold. The index is `mode: dynamic`, so a document can
	// carry arbitrary fields — including tenant_id itself. Projecting here means
	// a field nobody designed cannot become queryable by being ingested.
	out := &Rows{Columns: s.Rel.Columns}
	for _, hit := range payload.Hits {
		row := make([]any, len(s.Rel.Columns))
		for i, col := range s.Rel.Columns {
			row[i] = hit[col]
		}
		out.Values = append(out.Values, row)
	}
	return out, nil
}

// ── Aggregator ───────────────────────────────────────────────────────────────
//
// Quickwit answers both of these itself, which is the whole point: a search
// with max_hits=0 returns an exact num_hits over the entire index (5,104,773
// rows in 48 ms on the benchmark corpus), and a terms aggregation returns
// grouped counts. Neither ships a single document.

func (s *QuickwitScanner) CountAll(ctx context.Context, tenantID string, where []Predicate) (int64, error) {
	resp, err := s.searchAgg(ctx, tenantID, where, nil, 0)
	if err != nil {
		return 0, err
	}
	return resp.NumHits, nil
}

func (s *QuickwitScanner) GroupCount(ctx context.Context, tenantID, column string, where []Predicate, limit int) (*Rows, error) {
	if !s.Rel.HasColumn(column) {
		// The column is about to be named in a request to the store. It was
		// checked during planning; checking again here means a future caller
		// cannot skip that step.
		return nil, fmt.Errorf("quickwit scanner: %q is not a column of %s", column, s.Rel.Name)
	}
	aggs := map[string]any{
		"grouped": map[string]any{
			"terms": map[string]any{"field": s.indexField(column), "size": limit},
		},
	}
	resp, err := s.searchAgg(ctx, tenantID, where, aggs, 0)
	if err != nil {
		return nil, err
	}
	out := &Rows{Columns: []string{column, "count"}}
	for _, b := range resp.Aggregations.Grouped.Buckets {
		out.Values = append(out.Values, []any{b.Key, b.DocCount})
	}
	return out, nil
}

type quickwitSearchResponse struct {
	NumHits      int64 `json:"num_hits"`
	Aggregations struct {
		Grouped struct {
			Buckets []struct {
				Key      any   `json:"key"`
				DocCount int64 `json:"doc_count"`
			} `json:"buckets"`
		} `json:"grouped"`
	} `json:"aggregations"`
}

// searchAgg posts a search whose body carries the tenant filter and optional
// aggregations. The tenant still goes through searchQuery, so the same refusal
// of ids that could become syntax applies here.
func (s *QuickwitScanner) searchAgg(ctx context.Context, tenantID string, where []Predicate, aggs map[string]any, maxHits int) (*quickwitSearchResponse, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("quickwit scanner: refusing to aggregate with an empty tenant")
	}
	q, err := s.searchQuery(tenantID, where)
	if err != nil {
		return nil, err
	}

	body := map[string]any{"query": q, "max_hits": maxHits}
	if aggs != nil {
		body["aggs"] = aggs
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	index := s.Index
	if index == "" {
		index = "pulsetrace-logs"
	}
	endpoint := fmt.Sprintf("%s/api/v1/%s/search", strings.TrimRight(s.URL, "/"), index)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("quickwit scanner: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quickwit scanner: status %d: %s", resp.StatusCode, firstBytes(raw, 200))
	}
	var out quickwitSearchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("quickwit scanner: decode aggregation: %w", err)
	}
	return &out, nil
}
