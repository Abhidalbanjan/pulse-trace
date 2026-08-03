package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoRawTenantTableReads is the static half of the F0.3 tenant-isolation
// ratchet. The runtime guard (queryScoped) fails closed on an unscoped read, but
// only for reads that actually go through it — a new handler could still call the
// raw clickHouseClient.query() on a tenant-scoped table and bypass it. This test
// scans the handler package source and fails if any raw `.query(` call passes a
// SQL string that references a tenant-scoped ClickHouse table: such reads MUST use
// queryScoped instead. Combined, the two make "forgot to scope by tenant" a build
// failure rather than a silent cross-tenant leak.
func TestNoRawTenantTableReads(t *testing.T) {
	// `.query(` matches the raw call but not `.queryScoped(` (which has "Scoped"
	// between "query" and "(").
	rawCall := regexp.MustCompile(`\.query\(`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var violations []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "clickhouse.go" {
			continue // clickhouse.go defines query/queryScoped themselves
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(src)
		for _, loc := range rawCall.FindAllStringIndex(text, -1) {
			// Look at the SQL that follows the call for a tenant-scoped table name.
			end := loc[0] + 1200
			if end > len(text) {
				end = len(text)
			}
			window := text[loc[0]:end]
			for _, tbl := range tenantScopedCHTables {
				if strings.Contains(window, tbl) {
					violations = append(violations,
						f+": raw .query() reads tenant-scoped table "+tbl+" — use queryScoped()")
					break
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("tenant-isolation ratchet: %d unscoped tenant-table read(s):\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
