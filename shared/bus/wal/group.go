package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
)

// groupDir is where a topic's consumer positions live, beside its segments.
const groupDir = "groups"

// groupNamePattern is what a consumer group may be called.
//
// The name becomes a filename. Kafka group ids in this codebase come from
// configuration rather than end users, so this is not the front line — but
// "configuration is trusted" is exactly the assumption that turns a stray
// `../../` into a file written outside the data directory, and the check costs
// nothing. Quickwit's own group id embeds a generated ULID and a colon, so the
// permitted set has to be wider than plain identifiers.
var groupNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$`)

// Group is a named consumer's committed position on one topic.
//
// # Why the position is "next to read" and not "last read"
//
// Off-by-one in a committed offset is a bug you discover in production, at the
// worst moment, as either a permanently skipped record or an infinite redelivery
// loop. Storing the *next* offset makes the empty case fall out for free — a
// group that has consumed nothing is at 0, which is also where the log starts —
// and makes the resume rule a plain read rather than an increment someone has to
// remember.
type Group struct {
	mu   sync.Mutex
	name string
	path string
	next int64
	// committed distinguishes "has never consumed" from "consumed up to 0".
	// They look identical in `next`, and they mean opposite things when
	// choosing where a new subscriber starts.
	committed bool
}

// OpenGroup loads a group's committed position, or starts it at zero.
//
// A missing file means a group that has never committed, which must replay from
// the beginning rather than skip to the end: the log exists precisely so a
// consumer that was absent does not lose what happened while it was away.
func OpenGroup(topicDir, name string) (*Group, error) {
	if !groupNamePattern.MatchString(name) {
		return nil, fmt.Errorf("wal: refusing group name %q: outside the permitted character set", name)
	}
	dir := filepath.Join(topicDir, groupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: create %s: %w", dir, err)
	}
	g := &Group{name: name, path: filepath.Join(dir, name+".offset")}

	raw, err := os.ReadFile(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return g, nil // never committed; the caller chooses where to start
		}
		return nil, fmt.Errorf("wal: read offset for group %q: %w", name, err)
	}

	next, ok := decodeOffset(raw)
	if !ok {
		// An unreadable offset file resumes from zero rather than from the end.
		// Both are wrong; only one is recoverable. Replaying redelivers records
		// the consumer may already have processed, which at-least-once delivery
		// already requires it to tolerate. Skipping to the end silently drops
		// everything between here and there, and nothing downstream can tell.
		return g, nil
	}
	g.next = next
	g.committed = true
	return g, nil
}

// HasCommitted reports whether this group has ever recorded a position.
//
// A group that has not is the one case where the two transports could legally
// disagree about where to begin, so the choice is made explicitly by the caller
// rather than defaulted here.
func (g *Group) HasCommitted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed
}

// Seek sets the starting position of a group that has never committed.
//
// Refused once a group has a committed position: moving a live consumer is how
// records get skipped, and it must not be reachable by accident.
func (g *Group) Seek(next int64) error {
	g.mu.Lock()
	if g.committed {
		g.mu.Unlock()
		return fmt.Errorf("wal: refusing to seek group %q, which has already committed at %d", g.name, g.next)
	}
	g.next = next
	g.mu.Unlock()
	return g.Commit(next)
}

// Position is the next offset this group should read.
func (g *Group) Position() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.next
}

// Commit records that everything below next has been processed.
//
// # Why this is a rename and not a write
//
// Overwriting the file in place has a window in which it contains neither the
// old offset nor the new one — a crash there leaves a consumer resuming from a
// truncated number, which is a *valid-looking* offset pointing somewhere
// arbitrary. Writing a temp file, syncing it, and renaming makes the switch
// atomic: a reader sees the old value or the new one, never a mixture.
//
// The parent directory is synced too. Without that the rename itself can be
// lost on power failure even though the file's contents were durable — the
// classic mistake, because it passes every test that does not cut the power.
func (g *Group) Commit(next int64) error {
	if next < 0 {
		return fmt.Errorf("wal: refusing negative offset %d for group %q", next, g.name)
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	// Committing backwards would replay records already acknowledged. It is
	// never what a caller means, and silently allowing it hides the bug.
	if next < g.next {
		return fmt.Errorf("wal: refusing to move group %q backwards, %d -> %d", g.name, g.next, next)
	}
	if next == g.next && g.committed {
		return nil
	}

	tmp := g.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("wal: create offset temp for %q: %w", g.name, err)
	}
	if _, err := f.Write(encodeOffset(next)); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("wal: write offset for %q: %w", g.name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("wal: sync offset for %q: %w", g.name, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("wal: close offset temp for %q: %w", g.name, err)
	}
	if err := os.Rename(tmp, g.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("wal: commit offset for %q: %w", g.name, err)
	}
	if err := syncDir(filepath.Dir(g.path)); err != nil {
		return err
	}

	g.next = next
	g.committed = true
	return nil
}

// Name is the group's identifier.
func (g *Group) Name() string { return g.name }

// encodeOffset writes the offset as decimal text with a checksum.
//
// Text because an operator reading `cat groups/alert-service.offset` mid-incident
// should see a number, not eight bytes of binary. The checksum is there because
// atomic rename protects against a torn write but not against the bytes rotting
// afterwards, and a corrupt offset that still parses is the failure this whole
// file exists to avoid.
func encodeOffset(next int64) []byte {
	body := strconv.FormatInt(next, 10)
	sum := crc32.Checksum([]byte(body), castagnoli)
	return append([]byte(body+"\n"), binary.LittleEndian.AppendUint32(nil, sum)...)
}

// decodeOffset parses an offset file, reporting whether it can be trusted.
func decodeOffset(raw []byte) (int64, bool) {
	if len(raw) < 5 {
		return 0, false
	}
	body, sum := raw[:len(raw)-4], binary.LittleEndian.Uint32(raw[len(raw)-4:])
	trimmed := body
	if n := len(trimmed); n > 0 && trimmed[n-1] == '\n' {
		trimmed = trimmed[:n-1]
	}
	if crc32.Checksum(trimmed, castagnoli) != sum {
		return 0, false
	}
	next, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil || next < 0 {
		return 0, false
	}
	return next, true
}

// syncDir fsyncs a directory so a rename within it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("wal: open dir %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("wal: sync dir %s: %w", dir, err)
	}
	return nil
}
