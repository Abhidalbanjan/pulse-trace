package wal

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func tempSeg(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "00000000000000000000.seg")
}

func appendN(t *testing.T, s *segment, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.append(Record{Key: fmt.Sprintf("k%d", i), Value: []byte(fmt.Sprintf("value-%d", i))}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

func readAll(t *testing.T, s *segment) []Record {
	t.Helper()
	recs, err := s.readFrom(0)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	return recs
}

func TestSegmentAppendAndRead(t *testing.T) {
	s, err := openSegment(tempSeg(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	appendN(t, s, 5)
	got := readAll(t, s)
	if len(got) != 5 {
		t.Fatalf("read %d records, want 5", len(got))
	}
	for i, r := range got {
		if want := fmt.Sprintf("value-%d", i); string(r.Value) != want {
			t.Errorf("record %d value = %q, want %q", i, r.Value, want)
		}
	}
}

// Offsets must be assigned densely from the segment's base and must survive a
// reopen — a consumer's committed position means nothing if the numbering
// restarts.
func TestSegmentOffsetsAreDenseAndSurviveReopen(t *testing.T) {
	path := tempSeg(t)
	s, err := openSegment(path, 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for i := 0; i < 3; i++ {
		off, err := s.append(Record{Value: []byte("v")})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if want := int64(100 + i); off != want {
			t.Errorf("append %d returned offset %d, want %d", i, off, want)
		}
	}
	s.Close()

	reopened, err := openSegment(path, 100)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	if got := reopened.nextOffset(); got != 103 {
		t.Errorf("nextOffset after reopen = %d, want 103", got)
	}
	if got := len(readAll(t, reopened)); got != 3 {
		t.Errorf("read %d records after reopen, want 3", got)
	}
}

func TestSegmentReadFromSkipsEarlierOffsets(t *testing.T) {
	s, err := openSegment(tempSeg(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	appendN(t, s, 5)

	got, err := s.readFrom(3)
	if err != nil {
		t.Fatalf("readFrom: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("readFrom(3) returned %d records, want 2", len(got))
	}
	if string(got[0].Value) != "value-3" {
		t.Errorf("first record = %q, want value-3", got[0].Value)
	}
}

// ── The one that matters ─────────────────────────────────────────────────────

// A process killed mid-append leaves a partial record on disk. Reopening must
// recover every record that was written whole and discard the torn one — and it
// must physically truncate, so the next append does not write after garbage and
// permanently corrupt the offset numbering.
//
// Every prefix of the final record is exercised, not just one: the interesting
// failures are the boundaries (nothing written, the length word half written,
// the payload one byte short), and picking a single arbitrary cut point tests
// only whichever case that happens to be.
func TestSegmentRecoversFromTornTail(t *testing.T) {
	for cut := 1; cut <= 24; cut++ {
		t.Run(fmt.Sprintf("torn_by_%d_bytes", cut), func(t *testing.T) {
			path := tempSeg(t)
			s, err := openSegment(path, 0)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			appendN(t, s, 4)
			good := s.size()
			// Simulate the kill: a fifth record starts landing and stops.
			if _, err := s.append(Record{Key: "torn", Value: []byte("this record never finished being written")}); err != nil {
				t.Fatalf("append: %v", err)
			}
			full := s.size()
			s.Close()

			if cut > int(full-good) {
				t.Skipf("cut %d exceeds the final record's %d bytes", cut, full-good)
			}
			if err := os.Truncate(path, full-int64(cut)); err != nil {
				t.Fatalf("truncate: %v", err)
			}

			recovered, err := openSegment(path, 0)
			if err != nil {
				t.Fatalf("reopen after torn write: %v", err)
			}
			defer recovered.Close()

			got := readAll(t, recovered)
			if len(got) != 4 {
				t.Fatalf("recovered %d records, want the 4 that were written whole", len(got))
			}
			for i, r := range got {
				if want := fmt.Sprintf("value-%d", i); string(r.Value) != want {
					t.Errorf("record %d = %q, want %q", i, r.Value, want)
				}
			}
			if got := recovered.nextOffset(); got != 4 {
				t.Errorf("nextOffset after recovery = %d, want 4", got)
			}
			// The torn bytes must be gone from the *file*, not merely excluded
			// from the in-memory index — an earlier version of this assertion
			// read segment.size(), which reports the indexed prefix and is
			// therefore correct whether or not anything was truncated. It
			// passed against an implementation that never called Truncate.
			// Stat the file instead.
			if info, err := os.Stat(path); err != nil {
				t.Fatalf("stat: %v", err)
			} else if info.Size() != good {
				t.Errorf("on-disk size after recovery = %d, want %d (the torn tail was not truncated)",
					info.Size(), good)
			}
			if sz := recovered.size(); sz != good {
				t.Errorf("indexed size after recovery = %d, want %d", sz, good)
			}

			// And the segment must be usable again.
			off, err := recovered.append(Record{Value: []byte("after-recovery")})
			if err != nil {
				t.Fatalf("append after recovery: %v", err)
			}
			if off != 4 {
				t.Errorf("first post-recovery offset = %d, want 4", off)
			}
			if final := readAll(t, recovered); len(final) != 5 {
				t.Errorf("after recovery+append, read %d records, want 5", len(final))
			}
		})
	}
}

// A record whose payload was fully written but whose bytes were then corrupted
// (bit rot, a bad sector) must also stop the log there rather than deliver
// garbage as data.
func TestSegmentStopsAtACorruptRecord(t *testing.T) {
	path := tempSeg(t)
	s, err := openSegment(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	appendN(t, s, 3)
	sizeAfterTwo := int64(0)
	{
		// Find where the third record starts by replaying the first two.
		two, err := openSegment(path, 0)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		recs, _ := two.readFrom(0)
		if len(recs) != 3 {
			t.Fatalf("setup: expected 3 records, got %d", len(recs))
		}
		sizeAfterTwo = two.offsetOf(2)
		two.Close()
	}
	s.Close()

	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open for corruption: %v", err)
	}
	// Flip a bit inside the third record's payload.
	if _, err := f.WriteAt([]byte{0xFF}, sizeAfterTwo+headerSize+2); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	f.Close()

	recovered, err := openSegment(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer recovered.Close()

	if got := len(readAll(t, recovered)); got != 2 {
		t.Errorf("recovered %d records, want 2 (the log must stop at the corrupt one)", got)
	}
}
