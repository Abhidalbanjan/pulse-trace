package wal

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestLog(t *testing.T, dir string, segBytes int64) *Log {
	t.Helper()
	// SyncInterval -1 disables the background timer so tests drive durability
	// explicitly and do not race a ticker.
	l, err := Open(dir, Options{SegmentBytes: segBytes, SyncInterval: -1})
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	return l
}

func TestLogAppendReadAndOffsets(t *testing.T) {
	l := openTestLog(t, t.TempDir(), DefaultSegmentBytes)
	defer l.Close()

	for i := 0; i < 10; i++ {
		off, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%d", i))})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if off != int64(i) {
			t.Fatalf("append %d got offset %d", i, off)
		}
	}
	got, err := l.ReadFrom(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("read %d records, want 10", len(got))
	}
	if l.NextOffset() != 10 {
		t.Errorf("NextOffset = %d, want 10", l.NextOffset())
	}
}

// Rotation must not renumber. A consumer's committed offset has to keep meaning
// the same record after the log rolls, which is the whole reason offsets are
// global rather than per-segment.
func TestLogRotatesWithoutRenumbering(t *testing.T) {
	dir := t.TempDir()
	l := openTestLog(t, dir, 256) // tiny segments to force many rotations
	defer l.Close()

	const n = 60
	for i := 0; i < n; i++ {
		off, err := l.Append(Record{Value: []byte(fmt.Sprintf("record-%03d", i))})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if off != int64(i) {
			t.Fatalf("append %d got offset %d — rotation renumbered", i, off)
		}
	}
	if l.Segments() < 2 {
		t.Fatalf("expected several segments at 256 bytes each, got %d", l.Segments())
	}

	got, err := l.ReadFrom(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != n {
		t.Fatalf("read %d records across %d segments, want %d", len(got), l.Segments(), n)
	}
	for i, r := range got {
		if want := fmt.Sprintf("record-%03d", i); string(r.Value) != want {
			t.Fatalf("record %d = %q, want %q — ordering broke across a boundary", i, r.Value, want)
		}
	}

	// And a mid-log read must land on the right record regardless of which
	// segment holds it.
	for _, from := range []int64{0, 1, 17, 31, 59} {
		part, err := l.ReadFrom(from)
		if err != nil {
			t.Fatalf("ReadFrom(%d): %v", from, err)
		}
		if int64(len(part)) != n-from {
			t.Errorf("ReadFrom(%d) returned %d records, want %d", from, len(part), n-from)
		}
		if want := fmt.Sprintf("record-%03d", from); string(part[0].Value) != want {
			t.Errorf("ReadFrom(%d) starts at %q, want %q", from, part[0].Value, want)
		}
	}
}

// Reopening must resume where the log left off, across rotations.
func TestLogReopenResumesAfterRotation(t *testing.T) {
	dir := t.TempDir()

	l := openTestLog(t, dir, 256)
	for i := 0; i < 40; i++ {
		if _, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%03d", i))}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	segs := l.Segments()
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := openTestLog(t, dir, 256)
	defer reopened.Close()

	if got := reopened.NextOffset(); got != 40 {
		t.Errorf("NextOffset after reopen = %d, want 40", got)
	}
	if got := reopened.Segments(); got != segs {
		t.Errorf("segments after reopen = %d, want %d", got, segs)
	}
	got, err := reopened.ReadFrom(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 40 {
		t.Fatalf("read %d records after reopen, want 40", len(got))
	}
	off, err := reopened.Append(Record{Value: []byte("after-reopen")})
	if err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if off != 40 {
		t.Errorf("first offset after reopen = %d, want 40", off)
	}
}

// A crash leaves a torn record in whichever segment was active. Reopening the
// *log* must recover it, not just reopening a segment directly.
func TestLogRecoversTornTailInTheActiveSegment(t *testing.T) {
	dir := t.TempDir()
	l := openTestLog(t, dir, 256)
	for i := 0; i < 30; i++ {
		if _, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%03d", i))}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	l.Close()

	// Tear the last byte off the newest segment, the way a kill mid-write would.
	bases, err := existingSegmentBases(dir)
	if err != nil {
		t.Fatalf("list segments: %v", err)
	}
	last := segmentPath(dir, bases[len(bases)-1])
	info, err := os.Stat(last)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := os.Truncate(last, info.Size()-1); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	reopened := openTestLog(t, dir, 256)
	defer reopened.Close()

	got, err := reopened.ReadFrom(0)
	if err != nil {
		t.Fatalf("read after recovery: %v", err)
	}
	if len(got) != 29 {
		t.Fatalf("recovered %d records, want 29 (the torn one dropped)", len(got))
	}
	if reopened.NextOffset() != 29 {
		t.Errorf("NextOffset after recovery = %d, want 29", reopened.NextOffset())
	}
	// The log must be writable again, with no gap in the numbering.
	off, err := reopened.Append(Record{Value: []byte("resumed")})
	if err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	if off != 29 {
		t.Errorf("post-recovery offset = %d, want 29", off)
	}
}

// Per-topic ordering under concurrent producers: every offset handed out must
// be unique and the log must contain exactly what was written. Ordering between
// concurrent publishers is not promised — only that nothing is lost, duplicated
// or interleaved into a corrupt record.
func TestLogConcurrentAppendsAreDenseAndUnique(t *testing.T) {
	l := openTestLog(t, t.TempDir(), 4096)
	defer l.Close()

	const (
		writers = 8
		each    = 200
	)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[int64]bool, writers*each)
	)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				off, err := l.Append(Record{Value: []byte(fmt.Sprintf("w%d-%d", w, i))})
				if err != nil {
					t.Errorf("append: %v", err)
					return
				}
				mu.Lock()
				if seen[off] {
					t.Errorf("offset %d handed out twice", off)
				}
				seen[off] = true
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	total := writers * each
	if len(seen) != total {
		t.Fatalf("%d distinct offsets, want %d", len(seen), total)
	}
	for i := 0; i < total; i++ {
		if !seen[int64(i)] {
			t.Fatalf("offset %d was never assigned — the numbering has a hole", i)
		}
	}
	got, err := l.ReadFrom(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != total {
		t.Fatalf("log holds %d records, want %d", len(got), total)
	}
}

// A stray file in the log directory must be an error, not silently ignored:
// guessing risks assigning offsets that already exist.
func TestLogRefusesUnrecognisedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notasegment.seg"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(dir, Options{SyncInterval: -1}); err == nil {
		t.Error("expected Open to refuse a directory containing an unrecognised segment file")
	}
}

// The background sync timer must actually fire and must stop cleanly on Close
// (a leaked ticker goroutine per topic is a slow leak in a long-lived process).
func TestLogBackgroundSyncRunsAndStops(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{SyncInterval: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := l.Append(Record{Value: []byte("v")}); err != nil {
		t.Fatalf("append: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	l.mu.RLock()
	active := l.segments[len(l.segments)-1]
	l.mu.RUnlock()
	// Through the segment's own accessor: `dirty` is guarded by the segment's
	// mutex, and reading it under the Log's lock is a race the detector flags.
	if active.isDirty() {
		t.Error("the periodic sync did not fire")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-l.syncDone:
	case <-time.After(time.Second):
		t.Error("the sync goroutine did not exit on Close")
	}
	if err := l.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
}
