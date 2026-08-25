package wal

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func fillTo(t *testing.T, l *Log, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%04d", i))}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

// A consumed segment can be dropped whole; the active one never can, because it
// is the append target.
func TestReclaimDropsConsumedSegmentsButKeepsTheActiveOne(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, Options{SegmentBytes: 256, SyncInterval: -1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()

	fillTo(t, l, 60)
	before := l.Segments()
	if before < 3 {
		t.Fatalf("expected several segments, got %d", before)
	}

	freed, err := l.Reclaim(l.NextOffset()) // everything consumed
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if freed <= 0 {
		t.Errorf("freed %d bytes, want > 0", freed)
	}
	if got := l.Segments(); got != 1 {
		t.Errorf("segments after full reclaim = %d, want 1 (the active one survives)", got)
	}

	// The files must actually be gone, not merely dropped from the slice.
	bases, err := existingSegmentBases(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(bases) != 1 {
		t.Errorf("%d segment files on disk after reclaim, want 1", len(bases))
	}

	// And the log stays writable, with numbering intact.
	next := l.NextOffset()
	off, err := l.Append(Record{Value: []byte("after-reclaim")})
	if err != nil {
		t.Fatalf("append after reclaim: %v", err)
	}
	if off != next {
		t.Errorf("offset after reclaim = %d, want %d — reclaiming renumbered the log", off, next)
	}
}

// Reclaim must not drop a segment the slowest consumer still needs.
func TestReclaimKeepsWhatTheSlowestConsumerNeeds(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentBytes: 256, SyncInterval: -1})
	defer l.Close()
	fillTo(t, l, 60)

	// A consumer that has read nothing.
	freed, err := l.Reclaim(0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if freed != 0 {
		t.Errorf("freed %d bytes with a consumer at offset 0, want 0", freed)
	}

	// Everything still readable from the start.
	got, err := l.ReadFrom(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 60 {
		t.Errorf("read %d records, want 60", len(got))
	}
}

// The bound must refuse rather than drop. A record accepted and discarded is a
// count that is wrong downstream with nothing anywhere saying so.
func TestFullLogRefusesRatherThanDropping(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{
		SegmentBytes: 256,
		MaxBytes:     2048,
		FullTimeout:  -1, // refuse immediately
		SyncInterval: -1,
	})
	defer l.Close()

	written := 0
	var lastErr error
	for i := 0; i < 10000; i++ {
		if _, err := l.AppendWithBackpressure(Record{Value: []byte(fmt.Sprintf("v%04d", i))}); err != nil {
			lastErr = err
			break
		}
		written++
	}

	if lastErr == nil {
		t.Fatal("the log never refused despite a 2 KiB bound")
	}
	if !errors.Is(lastErr, ErrLogFull) {
		t.Fatalf("refusal error = %v, want ErrLogFull", lastErr)
	}
	if written == 0 {
		t.Fatal("nothing was written at all")
	}

	// Everything the log *accepted* must still be there. A bound that sheds
	// already-acknowledged records is the silent drop in a different costume.
	got, err := l.ReadFrom(0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != written {
		t.Errorf("log holds %d records but acknowledged %d", len(got), written)
	}
}

// Space freed by a consumer must unblock a waiting publisher, rather than the
// publisher waiting out its whole deadline.
func TestBackpressureReleasesWhenSpaceIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{
		SegmentBytes: 256,
		MaxBytes:     2048,
		FullTimeout:  3 * time.Second,
		SyncInterval: -1,
	})
	defer l.Close()

	// Fill until the next write would block.
	for i := 0; ; i++ {
		if _, err := l.Append(Record{Value: []byte(fmt.Sprintf("v%04d", i))}); err != nil {
			if errors.Is(err, errAtCapacity) {
				break
			}
			t.Fatalf("append: %v", err)
		}
		if i > 10000 {
			t.Fatal("never reached capacity")
		}
	}

	// A consumer catches up in the background, freeing segments.
	go func() {
		time.Sleep(80 * time.Millisecond)
		_, _ = l.Reclaim(l.NextOffset())
	}()

	start := time.Now()
	if _, err := l.AppendWithBackpressure(Record{Value: []byte("unblocked")}); err != nil {
		t.Fatalf("append after reclaim: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waited %v — the publisher did not notice the space being freed", elapsed)
	}
}

// A reclaimed prefix must be visible as such: a consumer committed below it has
// fallen off the log and needs to know rather than silently resuming late.
func TestOldestOffsetTracksReclamation(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentBytes: 256, SyncInterval: -1})
	defer l.Close()
	fillTo(t, l, 60)

	if got := l.OldestOffset(); got != 0 {
		t.Errorf("OldestOffset before reclaim = %d, want 0", got)
	}
	if _, err := l.Reclaim(l.NextOffset()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if got := l.OldestOffset(); got == 0 {
		t.Error("OldestOffset still 0 after reclaiming every full segment")
	}
	if got := l.OldestOffset(); got > l.NextOffset() {
		t.Errorf("OldestOffset %d exceeds NextOffset %d", got, l.NextOffset())
	}
}

// An unbounded log (MaxBytes zero) must never refuse — that is the cluster-mode
// default and changing it would be a behaviour change disguised as a feature.
func TestUnboundedLogNeverRefuses(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, Options{SegmentBytes: 256, SyncInterval: -1})
	defer l.Close()
	for i := 0; i < 500; i++ {
		if _, err := l.AppendWithBackpressure(Record{Value: []byte("v")}); err != nil {
			t.Fatalf("unbounded log refused at %d: %v", i, err)
		}
	}
	_ = os.Getpid()
}
