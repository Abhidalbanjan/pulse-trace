package sqlq

import (
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

// searchQuery returns the Quickwit query string for one tenant. Split out so
// the isolation property is assertable without a running Quickwit.
func (s *QuickwitScanner) searchQuery(tenantID string) (string, error) {
	if !tenantIDPattern.MatchString(tenantID) {
		return "", fmt.Errorf("quickwit scanner: refusing tenant id %q: outside the permitted character set", tenantID)
	}
	return "tenant_id:" + tenantID, nil
}

func (s *QuickwitScanner) Scan(ctx context.Context, tenantID string, limit int) (*Rows, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("quickwit scanner: refusing to scan with an empty tenant")
	}
	q, err := s.searchQuery(tenantID)
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
