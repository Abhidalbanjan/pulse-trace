package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestHandlersUseTenantScopedAlertReads is the F0.3 tenant-isolation ratchet for
// alert-service. Request handlers must read a single alert via GetByIDForTenant,
// never the unscoped GetByID — otherwise an alert ID from one tenant is readable
// by another (service, level, message, trace_id). The unscoped GetByID remains for
// internal callers that have already resolved the tenant, which live outside
// internal/handler, so this scan is scoped to the handler package.
func TestHandlersUseTenantScopedAlertReads(t *testing.T) {
	// `.GetByID(` matches the unscoped call but not `.GetByIDForTenant(`.
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
		t.Fatalf("tenant-isolation ratchet: %d unscoped alert read(s) in handlers:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
