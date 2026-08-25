package wal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroupStartsAtZeroAndReplays(t *testing.T) {
	dir := t.TempDir()
	g, err := OpenGroup(dir, "alert-service")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A group that has never committed must replay from the start, not skip to
	// the end — the log exists so an absent consumer does not lose what happened
	// while it was away.
	if got := g.Position(); got != 0 {
		t.Errorf("fresh group position = %d, want 0", got)
	}
}

func TestGroupCommitSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	g, err := OpenGroup(dir, "alert-service")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := g.Commit(42); err != nil {
		t.Fatalf("commit: %v", err)
	}

	reopened, err := OpenGroup(dir, "alert-service")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Position(); got != 42 {
		t.Errorf("position after reopen = %d, want 42", got)
	}
}

// Two groups on the same topic are independent positions — one falling behind
// must not move the other.
func TestGroupsAreIndependent(t *testing.T) {
	dir := t.TempDir()
	a, _ := OpenGroup(dir, "alert-service")
	b, _ := OpenGroup(dir, "topology-service")

	if err := a.Commit(100); err != nil {
		t.Fatalf("commit a: %v", err)
	}
	if got := b.Position(); got != 0 {
		t.Errorf("group b moved to %d when a committed", got)
	}
	reopenedB, _ := OpenGroup(dir, "topology-service")
	if got := reopenedB.Position(); got != 0 {
		t.Errorf("group b persisted %d when only a committed", got)
	}
}

// Moving backwards would replay records already acknowledged. It is never what
// a caller means, and allowing it silently hides the bug.
func TestGroupRefusesToMoveBackwards(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGroup(dir, "g")
	if err := g.Commit(10); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := g.Commit(5); err == nil {
		t.Error("expected a refusal when committing backwards")
	}
	if got := g.Position(); got != 10 {
		t.Errorf("position = %d after a refused backwards commit, want 10", got)
	}
	if err := g.Commit(-1); err == nil {
		t.Error("expected a refusal for a negative offset")
	}
}

// A group name becomes a filename. Configuration is trusted, but "trusted" is
// exactly the assumption that turns a stray ../.. into a write outside the data
// directory.
func TestGroupNameIsShapeChecked(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{
		"../escape", "a/b", "..", "", ".hidden", "with space",
		"nul\x00byte", strings.Repeat("a", 129),
	} {
		if _, err := OpenGroup(dir, bad); err == nil {
			t.Errorf("accepted group name %q", bad)
		}
	}
	// Quickwit's group id embeds a ULID and a colon, so the permitted set has to
	// be wider than plain identifiers.
	for _, good := range []string{
		"alert-service", "topology-service",
		"quickwit-pulsetrace-logs:01M09PSPN3891DCRKAV6F6WF30-kafka-logs-source",
		"svc.name", "a@b", "G1",
	} {
		if _, err := OpenGroup(dir, good); err != nil {
			t.Errorf("rejected legitimate group name %q: %v", good, err)
		}
	}
}

// A corrupt offset file must resume from zero, not from the end. Both are wrong;
// only one is recoverable — replay redelivers records the consumer already has
// to tolerate, while skipping to the end silently drops everything in between.
func TestCorruptOffsetFileReplaysRatherThanSkips(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGroup(dir, "g")
	if err := g.Commit(500); err != nil {
		t.Fatalf("commit: %v", err)
	}
	path := filepath.Join(dir, groupDir, "g.offset")

	for _, corruption := range []struct {
		name    string
		content []byte
	}{
		{"truncated", []byte("50")},
		{"empty", nil},
		{"bad checksum", append([]byte("500\n"), 0, 0, 0, 0)},
		{"not a number", append([]byte("abc\n"), encodeOffset(0)[len(encodeOffset(0))-4:]...)},
		{"garbage", []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB}},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			if err := os.WriteFile(path, corruption.content, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			reopened, err := OpenGroup(dir, "g")
			if err != nil {
				t.Fatalf("open with a corrupt offset must not fail: %v", err)
			}
			if got := reopened.Position(); got != 0 {
				t.Errorf("position = %d after corruption, want 0 (replay, never skip)", got)
			}
		})
	}
}

// The committed file must be readable by a human mid-incident: an operator who
// cats it should see a number.
func TestOffsetFileIsHumanReadable(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGroup(dir, "g")
	if err := g.Commit(12345); err != nil {
		t.Fatalf("commit: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, groupDir, "g.offset"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(raw), "12345\n") {
		t.Errorf("offset file starts with %q, want a plain decimal number", string(raw[:min(8, len(raw))]))
	}
}

// No temp file may survive a commit — a directory accumulating .tmp files is a
// leak, and one left behind is indistinguishable from a crash artefact.
func TestCommitLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	g, _ := OpenGroup(dir, "g")
	for i := int64(1); i <= 5; i++ {
		if err := g.Commit(i * 10); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, groupDir))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("group dir holds %d files, want exactly the offset file", len(entries))
	}
}
