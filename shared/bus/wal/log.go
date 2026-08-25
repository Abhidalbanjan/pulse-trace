package wal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults matching the plan's durability budget.
const (
	// DefaultSegmentBytes rolls a segment at 64 MiB. Small enough that recovery
	// scans one bounded file, large enough that rotation is rare.
	DefaultSegmentBytes = 64 << 20
	// DefaultSyncInterval is the window in which a crash can lose records —
	// deliberately the same order as the current Kafka producer's, so lite and
	// cluster make the same promise rather than lite quietly making a weaker
	// one.
	DefaultSyncInterval = 100 * time.Millisecond

	segmentSuffix = ".seg"
	// offsetDigits zero-pads segment names so lexical order is numeric order —
	// a directory listing is then already sorted, and an operator reading `ls`
	// sees the log in the order it was written.
	offsetDigits = 20
)

// Options configure a Log.
type Options struct {
	// SegmentBytes is the size at which a segment rolls. Zero uses the default.
	SegmentBytes int64
	// SyncInterval is how often a dirty segment is fsynced. Zero uses the
	// default; negative disables the timer, leaving durability to rotation and
	// Close (used by tests that drive sync explicitly).
	SyncInterval time.Duration
}

func (o Options) segmentBytes() int64 {
	if o.SegmentBytes > 0 {
		return o.SegmentBytes
	}
	return DefaultSegmentBytes
}

// Log is one topic's durable, append-only record log.
//
// # Why offsets are global rather than per-segment
//
// A consumer commits a position and expects to resume there. If offsets
// restarted per segment, a committed position would be ambiguous the moment the
// log rolled — the same number would name a record in every segment. So the Log
// assigns them monotonically across the whole topic and segments merely record
// which range they hold.
type Log struct {
	mu sync.RWMutex

	dir  string
	opts Options

	// segments are ordered oldest-first; the last is the one being appended to.
	segments []*segment

	syncStop chan struct{}
	syncDone chan struct{}
	closed   bool
}

// Open opens or creates the log for one topic under dir.
//
// An existing directory is reopened rather than replaced: every segment is
// scanned, which is where a torn tail from the last crash gets truncated, and
// the highest offset found becomes the resume point.
func Open(dir string, opts Options) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: create %s: %w", dir, err)
	}
	l := &Log{dir: dir, opts: opts}

	bases, err := existingSegmentBases(dir)
	if err != nil {
		return nil, err
	}
	for _, base := range bases {
		s, err := openSegment(segmentPath(dir, base), base)
		if err != nil {
			l.closeSegments()
			return nil, err
		}
		l.segments = append(l.segments, s)
	}

	// An empty log still needs somewhere to write.
	if len(l.segments) == 0 {
		s, err := openSegment(segmentPath(dir, 0), 0)
		if err != nil {
			return nil, err
		}
		l.segments = []*segment{s}
	}

	// A crash between rotating and writing can leave a trailing empty segment
	// whose base is stale relative to the previous one. Dropping it here keeps
	// the invariant that the last segment is always the append target.
	l.dropTrailingEmptyDuplicates()

	if opts.SyncInterval >= 0 {
		l.startSyncLoop()
	}
	return l, nil
}

// Append writes one record durably-eventually and returns its offset.
//
// "Eventually" is the honest word: the record is in the page cache when this
// returns and on disk within SyncInterval. That is the same window the Kafka
// path already accepts, and pretending otherwise would misrepresent it. A
// caller that needs the stronger guarantee calls Sync.
func (l *Log) Append(r Record) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, fmt.Errorf("wal: log is closed")
	}

	active := l.segments[len(l.segments)-1]
	if active.size() >= l.opts.segmentBytes() && active.count() > 0 {
		var err error
		if active, err = l.rotate(); err != nil {
			return 0, err
		}
	}
	return active.append(r)
}

// rotate syncs the active segment and starts a new one. Caller holds l.mu.
//
// The sync is not optional: rotation is the moment the previous segment stops
// being written, so it is the last chance to make it durable without waiting
// for the timer.
func (l *Log) rotate() (*segment, error) {
	active := l.segments[len(l.segments)-1]
	if err := active.sync(); err != nil {
		return nil, err
	}
	base := active.nextOffset()
	s, err := openSegment(segmentPath(l.dir, base), base)
	if err != nil {
		return nil, err
	}
	l.segments = append(l.segments, s)
	return s, nil
}

// ReadFrom returns every record at or after offset, across segment boundaries.
func (l *Log) ReadFrom(offset int64) ([]Record, error) {
	l.mu.RLock()
	segs := append([]*segment(nil), l.segments...)
	l.mu.RUnlock()

	var out []Record
	for _, s := range segs {
		if s.nextOffset() <= offset {
			continue // entirely before the requested position
		}
		recs, err := s.readFrom(offset)
		if err != nil {
			return append(out, recs...), err
		}
		out = append(out, recs...)
	}
	return out, nil
}

// NextOffset is the offset the next appended record will receive.
func (l *Log) NextOffset() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.segments[len(l.segments)-1].nextOffset()
}

// Sync forces the active segment to disk.
func (l *Log) Sync() error {
	l.mu.RLock()
	if l.closed || len(l.segments) == 0 {
		l.mu.RUnlock()
		return nil
	}
	active := l.segments[len(l.segments)-1]
	l.mu.RUnlock()
	return active.sync()
}

// Segments is how many segment files back this log. Exposed for tests and for
// the operational metric that makes rotation visible.
func (l *Log) Segments() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.segments)
}

// Close stops the sync loop and flushes everything.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	stop := l.syncStop
	done := l.syncDone
	l.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}
	return l.closeSegments()
}

func (l *Log) closeSegments() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var firstErr error
	for _, s := range l.segments {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (l *Log) startSyncLoop() {
	interval := l.opts.SyncInterval
	if interval == 0 {
		interval = DefaultSyncInterval
	}
	l.syncStop = make(chan struct{})
	l.syncDone = make(chan struct{})
	go func() {
		defer close(l.syncDone)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-l.syncStop:
				return
			case <-t.C:
				// A failed periodic sync is reported by the next Append or by
				// Close; there is nobody to return it to here.
				_ = l.Sync()
			}
		}
	}()
}

// dropTrailingEmptyDuplicates removes an empty final segment that duplicates
// the previous segment's end, which a crash between rotate and write can leave.
func (l *Log) dropTrailingEmptyDuplicates() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.segments) > 1 {
		last := l.segments[len(l.segments)-1]
		prev := l.segments[len(l.segments)-2]
		if last.count() != 0 || last.base != prev.nextOffset() {
			return
		}
		// Keep it only if it is the sole append target; here prev can serve.
		_ = last.Close()
		_ = os.Remove(last.path)
		l.segments = l.segments[:len(l.segments)-1]
	}
}

func segmentPath(dir string, base int64) string {
	return filepath.Join(dir, fmt.Sprintf("%0*d%s", offsetDigits, base, segmentSuffix))
}

// existingSegmentBases lists the base offsets of the segments already on disk,
// in ascending order.
func existingSegmentBases(dir string) ([]int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("wal: read %s: %w", dir, err)
	}
	var bases []int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), segmentSuffix) {
			continue
		}
		raw := strings.TrimSuffix(e.Name(), segmentSuffix)
		base, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			// A file that looks like a segment but is not named like one is not
			// silently skipped: it means something else is writing here, and
			// guessing would risk assigning duplicate offsets.
			return nil, fmt.Errorf("wal: unrecognised segment file %q in %s", e.Name(), dir)
		}
		bases = append(bases, base)
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i] < bases[j] })
	return bases, nil
}
