package wal

import (
	"errors"
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

	// DefaultFullTimeout is how long a publisher waits for a full log to drain
	// before being refused. Long enough to ride out a consumer's GC pause or a
	// slow batch, short enough that a caller's own request deadline is not
	// silently consumed by it.
	DefaultFullTimeout = 2 * time.Second

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
	// MaxBytes bounds the topic's total on-disk size. Zero means unbounded.
	//
	// The bound is what makes back-pressure possible at all: without it a
	// consumer that stops consuming turns into a disk that fills, and the
	// failure surfaces as the whole host wedging rather than as this topic
	// refusing writes.
	MaxBytes int64
	// FullTimeout is how long Append waits for space before refusing. Zero uses
	// the default; negative refuses immediately.
	FullTimeout time.Duration
}

func (o Options) fullTimeout() time.Duration {
	if o.FullTimeout != 0 {
		return o.FullTimeout
	}
	return DefaultFullTimeout
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

	if l.opts.MaxBytes > 0 && l.totalBytes() >= l.opts.MaxBytes {
		return 0, errAtCapacity
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

// ErrLogFull is returned when the topic is at its size bound and nothing can be
// reclaimed within the deadline.
//
// It is an error rather than a silent drop, and that is the entire point.
// Telemetry discarded quietly does not show up as missing — it shows up as a
// smaller number, and every count, rate and SLO computed downstream of it is
// then wrong with nothing anywhere saying so. A publisher that is refused can
// retry, shed deliberately, or surface a 429; a publisher whose record vanished
// cannot do any of those, because it was told it succeeded.
var ErrLogFull = errors.New("wal: log is full")

// Append writes a record, waiting for space if the log is at its bound.
//
// The wait is bounded and then it refuses. Blocking forever would convert a
// stalled consumer into a stalled *producer* — the gateway's ingest handler
// holding connections open until it runs out of them, which is a worse and much
// less legible failure than a 429.
func (l *Log) AppendWithBackpressure(r Record) (int64, error) {
	deadline := time.Now().Add(l.opts.fullTimeout())
	for {
		off, err := l.Append(r)
		if !errors.Is(err, errAtCapacity) {
			return off, err
		}
		if l.opts.FullTimeout < 0 || !time.Now().Before(deadline) {
			return 0, fmt.Errorf("%w: %s is at its %d-byte bound and the slowest "+
				"consumer has not advanced", ErrLogFull, l.dir, l.opts.MaxBytes)
		}
		// Somebody else's Reclaim may free space while we wait.
		time.Sleep(pollInterval)
	}
}

// errAtCapacity is the internal signal that the bound is reached. Callers see
// ErrLogFull from AppendWithBackpressure, or this from Append if they chose the
// non-blocking path.
var errAtCapacity = errors.New("wal: at capacity")

// totalBytes is the topic's on-disk size across every segment.
func (l *Log) totalBytes() int64 {
	var n int64
	for _, s := range l.segments {
		n += s.size()
	}
	return n
}

// Reclaim deletes whole segments whose records are all below upTo.
//
// Whole segments only. Reclaiming individual records would mean rewriting a
// file that is being appended to and re-indexing every offset after it — the
// reason log-structured storage deletes by segment everywhere it appears.
//
// The active segment is never removed, even when fully consumed: it is the
// append target, and deleting it mid-write is how a log loses the records it is
// in the middle of accepting.
func (l *Log) Reclaim(upTo int64) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var freed int64
	for len(l.segments) > 1 {
		oldest := l.segments[0]
		// Keep it unless every record it holds has been consumed.
		if oldest.nextOffset() > upTo {
			break
		}
		size := oldest.size()
		if err := oldest.Close(); err != nil {
			return freed, err
		}
		if err := os.Remove(oldest.path); err != nil {
			return freed, fmt.Errorf("wal: remove reclaimed segment %s: %w", oldest.path, err)
		}
		l.segments = l.segments[1:]
		freed += size
	}
	return freed, nil
}

// OldestOffset is the lowest offset still readable. Records below it have been
// reclaimed; a consumer committed below this has fallen off the log.
func (l *Log) OldestOffset() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(l.segments) == 0 {
		return 0
	}
	return l.segments[0].base
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
