// Package wal is the durable log behind the in-process bus (P1.2).
//
// # Why a log and not a channel
//
// The obvious in-process bus is a buffered channel, and it is wrong. A lite
// deployment that loses its queue on restart is a data-loss bug shipped on
// purpose: telemetry that never arrives is not visible as missing, it is
// visible as a lower number, and every count downstream of it is quietly wrong.
// Kafka's durability is not incidental to what the cluster deployment does —
// it is the property the rest of the system assumes.
//
// So the in-process transport writes to disk with the same guarantee the
// current Kafka producer asks for (`WaitForAll` acks, i.e. the record is
// durable before Publish returns), and recovers what it wrote.
//
// # The framing, and what it is defending against
//
// A record on disk is:
//
//	[u32 length][u32 crc32c][payload]
//
// The length lets a reader skip a record it does not need. The CRC is the part
// that matters: a process killed mid-write leaves a *partial* record, and
// without a checksum a truncated payload is indistinguishable from a short
// valid one — it decodes into plausible garbage and is delivered as real data.
// With one, a torn tail is detectable, and the log can truncate back to the
// last record it can prove was written whole.
package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"time"
)

// castagnoli is the CRC used by every serious log format (and by Kafka itself)
// because it has hardware support on amd64 and arm64 — the checksum must not be
// the reason a durable write is slower than an undurable one.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

const (
	// headerSize is the fixed framing before each payload: length + crc.
	headerSize = 8
	// maxRecordSize bounds a single record. It is a sanity bound, not a policy:
	// a corrupt length field reading as 3 GB must be rejected as corruption
	// rather than attempted as an allocation.
	maxRecordSize = 64 << 20 // 64 MiB
)

var (
	// ErrCorrupt marks a record that failed its checksum or is internally
	// inconsistent. Callers treat it as "the log ends here", never as "skip
	// this one": past a torn record nothing later can be trusted to be aligned.
	ErrCorrupt = errors.New("wal: corrupt record")
	// ErrIncomplete marks a record that is well-formed so far but runs past the
	// end of the file — the signature of a process killed mid-append.
	ErrIncomplete = errors.New("wal: incomplete record")
)

// Record is one message in the log.
type Record struct {
	Timestamp time.Time
	Key       string
	Value     []byte
	Headers   map[string][]byte
}

// encode serialises a record's payload (without the outer length/crc framing).
//
// Field order is fixed and every variable-length field is length-prefixed, so a
// decoder never has to guess where one ends. Written by hand rather than with
// encoding/json because this is the hot path of every publish, and because a
// format that can be decoded by inspection is one an operator can debug with
// xxd when it matters most.
func (r Record) encode() []byte {
	size := 8 + 4 + len(r.Key) + 4 + 4 + len(r.Value)
	for k, v := range r.Headers {
		size += 4 + len(k) + 4 + len(v)
	}
	buf := make([]byte, 0, size)

	buf = binary.LittleEndian.AppendUint64(buf, uint64(r.Timestamp.UnixNano()))

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(r.Key)))
	buf = append(buf, r.Key...)

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(r.Headers)))
	for k, v := range r.Headers {
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(k)))
		buf = append(buf, k...)
		buf = binary.LittleEndian.AppendUint32(buf, uint32(len(v)))
		buf = append(buf, v...)
	}

	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(r.Value)))
	buf = append(buf, r.Value...)
	return buf
}

// frame wraps an encoded payload in its length and checksum.
func frame(payload []byte) []byte {
	out := make([]byte, headerSize, headerSize+len(payload))
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(out[4:8], crc32.Checksum(payload, castagnoli))
	return append(out, payload...)
}

// checksumOK reports whether a payload still matches the checksum it was
// written with.
func checksumOK(payload []byte, want uint32) bool {
	return crc32.Checksum(payload, castagnoli) == want
}

// decodeRecord parses a payload previously produced by encode.
//
// Every read is bounds-checked against the remaining buffer. A corrupt length
// that survived the CRC (possible only in a deliberately crafted file, but the
// check costs nothing) must not become an out-of-range panic inside a consumer.
func decodeRecord(b []byte) (Record, error) {
	var r Record
	cur := b

	take := func(n int) ([]byte, bool) {
		if n < 0 || n > len(cur) {
			return nil, false
		}
		out := cur[:n]
		cur = cur[n:]
		return out, true
	}
	takeU32 := func() (int, bool) {
		raw, ok := take(4)
		if !ok {
			return 0, false
		}
		return int(binary.LittleEndian.Uint32(raw)), true
	}

	ts, ok := take(8)
	if !ok {
		return r, fmt.Errorf("%w: truncated timestamp", ErrCorrupt)
	}
	r.Timestamp = time.Unix(0, int64(binary.LittleEndian.Uint64(ts))).UTC()

	klen, ok := takeU32()
	if !ok {
		return r, fmt.Errorf("%w: truncated key length", ErrCorrupt)
	}
	key, ok := take(klen)
	if !ok {
		return r, fmt.Errorf("%w: key length %d exceeds record", ErrCorrupt, klen)
	}
	r.Key = string(key)

	hcount, ok := takeU32()
	if !ok {
		return r, fmt.Errorf("%w: truncated header count", ErrCorrupt)
	}
	if hcount > 0 {
		r.Headers = make(map[string][]byte, hcount)
		for i := 0; i < hcount; i++ {
			n, ok := takeU32()
			if !ok {
				return r, fmt.Errorf("%w: truncated header key length", ErrCorrupt)
			}
			hk, ok := take(n)
			if !ok {
				return r, fmt.Errorf("%w: header key length %d exceeds record", ErrCorrupt, n)
			}
			n, ok = takeU32()
			if !ok {
				return r, fmt.Errorf("%w: truncated header value length", ErrCorrupt)
			}
			hv, ok := take(n)
			if !ok {
				return r, fmt.Errorf("%w: header value length %d exceeds record", ErrCorrupt, n)
			}
			// Copied, not aliased: the caller owns this map for the lifetime of
			// the message, while the backing buffer belongs to the reader and
			// is reused for the next record.
			r.Headers[string(hk)] = append([]byte(nil), hv...)
		}
	}

	vlen, ok := takeU32()
	if !ok {
		return r, fmt.Errorf("%w: truncated value length", ErrCorrupt)
	}
	val, ok := take(vlen)
	if !ok {
		return r, fmt.Errorf("%w: value length %d exceeds record", ErrCorrupt, vlen)
	}
	r.Value = append([]byte(nil), val...)

	if len(cur) != 0 {
		return r, fmt.Errorf("%w: %d trailing bytes", ErrCorrupt, len(cur))
	}
	return r, nil
}
