package wal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func logWith(t *testing.T, dir string, n int) *Log {
	t.Helper()
	l, err := Open(dir, Options{SyncInterval: -1})
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%03d", i))}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return l
}

// collect runs a reader until it has seen want records or the deadline passes.
func collect(t *testing.T, r *Reader, want int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var (
		mu   sync.Mutex
		seen []string
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx, func(_ context.Context, _ int64, rec Record) error {
			mu.Lock()
			seen = append(seen, string(rec.Value))
			n := len(seen)
			mu.Unlock()
			if n >= want {
				cancel()
			}
			return nil
		})
	}()
	<-done
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), seen...)
}

func TestReaderDeliversInOrderFromZero(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 5)
	defer l.Close()
	g, _ := OpenGroup(dir, "g")

	got := collect(t, NewReader(l, g), 5)
	if len(got) != 5 {
		t.Fatalf("delivered %d records, want 5: %v", len(got), got)
	}
	for i, v := range got {
		if want := fmt.Sprintf("v%03d", i); v != want {
			t.Errorf("record %d = %q, want %q", i, v, want)
		}
	}
}

// A reader that has committed must resume after those records, not replay them.
func TestReaderResumesFromCommittedPosition(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 5)
	defer l.Close()

	g, _ := OpenGroup(dir, "g")
	if err := g.Commit(3); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := collect(t, NewReader(l, g), 2)
	if len(got) != 2 || got[0] != "v003" || got[1] != "v004" {
		t.Fatalf("resumed with %v, want [v003 v004]", got)
	}
}

// The heart of at-least-once: a handler that fails must NOT have its record
// committed, so a later reader sees it again. Committing before the handler
// would make this a silent drop.
func TestFailedHandlerLeavesTheRecordUncommitted(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 5)
	defer l.Close()
	g, _ := OpenGroup(dir, "g")

	boom := errors.New("handler exploded")
	var delivered []string
	err := NewReader(l, g).Run(context.Background(), func(_ context.Context, _ int64, rec Record) error {
		delivered = append(delivered, string(rec.Value))
		if string(rec.Value) == "v002" {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want the handler's error", err)
	}
	// Two succeeded and were committed; the third failed and must not be.
	if got := g.Position(); got != 2 {
		t.Fatalf("committed position = %d, want 2 (the failing record must not be committed)", got)
	}

	// A fresh reader must see the failed record again.
	reopened, _ := OpenGroup(dir, "g")
	redelivered := collect(t, NewReader(l, reopened), 3)
	if len(redelivered) == 0 || redelivered[0] != "v002" {
		t.Fatalf("redelivery started at %v, want v002 first", redelivered)
	}
}

// A crash between handling and committing redelivers. Simulated by committing
// nothing after a successful handle, then reopening the group from disk.
func TestCrashBetweenHandleAndCommitRedelivers(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 3)
	defer l.Close()

	g, _ := OpenGroup(dir, "g")
	handled := 0
	ctx, cancel := context.WithCancel(context.Background())
	_ = NewReader(l, g).Run(ctx, func(_ context.Context, _ int64, _ Record) error {
		handled++
		if handled == 1 {
			// "Crash" immediately after handling the first record but before
			// its commit lands, by aborting the run.
			cancel()
			return errors.New("power cut")
		}
		return nil
	})

	fresh, _ := OpenGroup(dir, "g")
	if got := fresh.Position(); got != 0 {
		t.Fatalf("position after a crash before commit = %d, want 0 — the record must be redelivered", got)
	}
}

// Two groups on one log consume independently and each see everything.
func TestTwoGroupsEachSeeEveryRecord(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 4)
	defer l.Close()

	a, _ := OpenGroup(dir, "alert-service")
	b, _ := OpenGroup(dir, "topology-service")

	gotA := collect(t, NewReader(l, a), 4)
	gotB := collect(t, NewReader(l, b), 4)
	if len(gotA) != 4 || len(gotB) != 4 {
		t.Fatalf("group a saw %d, group b saw %d, want 4 each", len(gotA), len(gotB))
	}
	if a.Position() != 4 || b.Position() != 4 {
		t.Errorf("positions a=%d b=%d, want 4 each", a.Position(), b.Position())
	}
}

// A reader must pick up records appended while it is already running, not just
// those present when it started.
func TestReaderTailsRecordsAppendedWhileRunning(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 1)
	defer l.Close()
	g, _ := OpenGroup(dir, "g")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var (
		mu   sync.Mutex
		seen []string
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = NewReader(l, g).Run(ctx, func(_ context.Context, _ int64, rec Record) error {
			mu.Lock()
			seen = append(seen, string(rec.Value))
			n := len(seen)
			mu.Unlock()
			if n >= 3 {
				cancel()
			}
			return nil
		})
	}()

	time.Sleep(60 * time.Millisecond) // let it catch up and start polling
	for i := 1; i < 3; i++ {
		if _, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%03d", i))}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("saw %v, want three records including those appended mid-run", seen)
	}
}

// Cancellation is a clean stop, not an error — it is how a service shuts down.
func TestCancelledContextStopsCleanly(t *testing.T) {
	dir := t.TempDir()
	l := logWith(t, dir, 0)
	defer l.Close()
	g, _ := OpenGroup(dir, "g")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewReader(l, g).Run(ctx, func(context.Context, int64, Record) error {
		t.Error("handler ran on an already-cancelled context")
		return nil
	}); err != nil {
		t.Errorf("Run on a cancelled context returned %v, want nil", err)
	}
}
