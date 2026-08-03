package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestHandlersUseTenantScopedIncidentReads is the F0.3 tenant-isolation ratchet
// for correlation-service. Request handlers must read incidents via the
// tenant-scoped repository method (GetByIDForTenant), never the unscoped GetByID —
// otherwise an incident ID guessed or leaked from one tenant can be read by
// another (title, root cause, causal analysis, timeline).
//
// The unscoped GetByID is legitimate for internal callers (the correlation engine,
// which has already resolved the tenant); those live outside internal/handler, so
// this scan is scoped to the handler package.
func TestHandlersUseTenantScopedIncidentReads(t *testing.T) {
	// `.GetByID(` matches the unscoped call but not `.GetByIDForTenant(` (which has
	// "ForTenant" between "GetByID" and "(").
	unscoped := regexp.MustCompile(`\.GetByID\(`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var violations []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(src)
		for _, loc := range unscoped.FindAllStringIndex(text, -1) {
			line := 1 + strings.Count(text[:loc[0]], "\n")
			violations = append(violations, f+" uses unscoped .GetByID() at line "+strconv.Itoa(line)+" — use GetByIDForTenant()")
		}
	}
	if len(violations) > 0 {
		t.Fatalf("tenant-isolation ratchet: %d unscoped incident read(s) in handlers:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
