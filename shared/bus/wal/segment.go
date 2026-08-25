package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// segment is one append-only file holding a contiguous run of offsets.
//
// It owns exactly two things: appending framed records, and knowing where every
// record it holds begins. Rotation, offset assignment across files and reader
// fan-out belong to the Log above it — a segment that knew about its successor
// would have to reason about two files at once, which is where log
// implementations usually go wrong.
type segment struct {
	mu sync.RWMutex

	path string
	file *os.File
	// base is the offset of this segment's first record.
	base int64
	// starts[i] is the byte position of the record at offset base+i, so a read
	// from an arbitrary offset is a seek rather than a scan. Rebuilt on open,
	// which is also when recovery happens.
	starts []int64
	// bytes is the size of the intact prefix — everything after it has been
	// truncated away.
	bytes int64
	// dirty records whether anything has been written since the last fsync.
	dirty bool
}

// openSegment opens or creates a segment, recovering a torn tail if it finds
// one.
//
// Recovery is not a special mode: it is what opening *is*. The file is scanned
// once, every record that can be proved whole is indexed, and the first one that
// cannot ends the segment — the file is truncated there. That means the same
// code path runs after a clean shutdown and after a `kill -9`, so the recovery
// path cannot rot from disuse.
func openSegment(path string, base int64) (*segment, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: open segment %s: %w", path, err)
	}
	s := &segment{path: path, file: f, base: base}
	if err := s.recover(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// recover scans the file, indexes every intact record and truncates the rest.
func (s *segment) recover() error {
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("wal: stat %s: %w", s.path, err)
	}
	size := info.Size()

	var (
		pos    int64
		header = make([]byte, headerSize)
	)
	for pos < size {
		if _, err := s.file.ReadAt(header, pos); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break // the length word itself was half written
			}
			return fmt.Errorf("wal: read header at %d in %s: %w", pos, s.path, err)
		}
		length := int64(binary.LittleEndian.Uint32(header[0:4]))
		if length <= 0 || length > maxRecordSize {
			break // a length this size is corruption, not a record
		}
		if pos+headerSize+length > size {
			break // the payload runs past the end: torn write
		}

		payload := make([]byte, length)
		if _, err := s.file.ReadAt(payload, pos+headerSize); err != nil {
			break
		}
		if !checksumOK(payload, binary.LittleEndian.Uint32(header[4:8])) {
			break // written whole but no longer intact
		}
		// Reject a record that passes its checksum but cannot be parsed, rather
		// than indexing something a reader will later fail on.
		if _, err := decodeRecord(payload); err != nil {
			break
		}

		s.starts = append(s.starts, pos)
		pos += headerSize + length
	}

	s.bytes = pos
	// Truncate rather than merely stopping the index here. A torn tail left in
	// place would sit between the last good record and the next append, and
	// every record after it would be unreachable on the next recovery — the log
	// would silently lose everything written after the crash.
	if pos < size {
		if err := s.file.Truncate(pos); err != nil {
			return fmt.Errorf("wal: truncate torn tail of %s: %w", s.path, err)
		}
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("wal: sync after truncate %s: %w", s.path, err)
		}
	}
	if _, err := s.file.Seek(pos, io.SeekStart); err != nil {
		return fmt.Errorf("wal: seek %s: %w", s.path, err)
	}
	return nil
}

// append writes one record and returns the offset it was given.
//
// It does not fsync: durability is the Log's policy (on rotation and on a
// timer), because syncing per record costs an order of magnitude and the
// current Kafka producer does not promise that either.
func (s *segment) append(r Record) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload := r.encode()
	if len(payload) > maxRecordSize {
		return 0, fmt.Errorf("wal: record of %d bytes exceeds the %d limit", len(payload), maxRecordSize)
	}
	buf := frame(payload)

	n, err := s.file.Write(buf)
	if err != nil {
		// A short write leaves a torn record, which is exactly what recovery
		// handles — but the caller must be told the record is not durable.
		if n > 0 {
			s.bytes += int64(n)
		}
		return 0, fmt.Errorf("wal: append to %s: %w", s.path, err)
	}

	off := s.base + int64(len(s.starts))
	s.starts = append(s.starts, s.bytes)
	s.bytes += int64(n)
	s.dirty = true
	return off, nil
}

// sync flushes to disk. A no-op when nothing has been written since the last.
func (s *segment) sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("wal: sync %s: %w", s.path, err)
	}
	s.dirty = false
	return nil
}

// readFrom returns every record at or after the given absolute offset.
func (s *segment) readFrom(offset int64) ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx := offset - s.base
	if idx < 0 {
		idx = 0
	}
	if idx >= int64(len(s.starts)) {
		return nil, nil
	}

	out := make([]Record, 0, int64(len(s.starts))-idx)
	header := make([]byte, headerSize)
	for i := idx; i < int64(len(s.starts)); i++ {
		pos := s.starts[i]
		if _, err := s.file.ReadAt(header, pos); err != nil {
			return out, fmt.Errorf("wal: read header at %d in %s: %w", pos, s.path, err)
		}
		length := int64(binary.LittleEndian.Uint32(header[0:4]))
		payload := make([]byte, length)
		if _, err := s.file.ReadAt(payload, pos+headerSize); err != nil {
			return out, fmt.Errorf("wal: read payload at %d in %s: %w", pos, s.path, err)
		}
		if !checksumOK(payload, binary.LittleEndian.Uint32(header[4:8])) {
			// Indexed as intact at open and not now: the file changed underneath
			// us. Return what is provably good and stop.
			return out, fmt.Errorf("%w: at offset %d in %s", ErrCorrupt, s.base+i, s.path)
		}
		rec, err := decodeRecord(payload)
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// nextOffset is the offset the next appended record will receive.
func (s *segment) nextOffset() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.base + int64(len(s.starts))
}

// count is how many records the segment holds.
func (s *segment) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.starts)
}

// isDirty reports whether anything has been appended since the last sync.
//
// Exists so a caller can observe sync state under the segment's own lock. The
// field is guarded by s.mu, and reading it through the Log's lock instead — as
// a test here originally did — is a data race the race detector catches.
func (s *segment) isDirty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirty
}

// size is the number of intact bytes on disk.
func (s *segment) size() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bytes
}

// offsetOf is the byte position where the record at an absolute offset begins.
// Returns the segment size when the offset is past the end.
func (s *segment) offsetOf(offset int64) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := offset - s.base
	if idx < 0 || idx >= int64(len(s.starts)) {
		return s.bytes
	}
	return s.starts[idx]
}

// Close syncs and closes the underlying file.
func (s *segment) Close() error {
	if err := s.sync(); err != nil {
		s.file.Close()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}
