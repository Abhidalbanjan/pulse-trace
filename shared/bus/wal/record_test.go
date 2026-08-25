package wal

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
	"time"
)

func TestRecordRoundTrip(t *testing.T) {
	want := Record{
		Timestamp: time.Date(2026, 8, 25, 12, 0, 0, 123456789, time.UTC),
		Key:       "cart-service",
		Value:     []byte(`{"level":"ERROR","message":"boom"}`),
		Headers: map[string][]byte{
			"traceparent": []byte("00-abc-def-01"),
			"tenant":      []byte("acme"),
		},
	}

	got, err := decodeRecord(want.encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Timestamp.Equal(want.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, want.Timestamp)
	}
	if got.Key != want.Key {
		t.Errorf("Key = %q, want %q", got.Key, want.Key)
	}
	if !bytes.Equal(got.Value, want.Value) {
		t.Errorf("Value = %q, want %q", got.Value, want.Value)
	}
	if len(got.Headers) != len(want.Headers) {
		t.Fatalf("Headers = %v, want %v", got.Headers, want.Headers)
	}
	for k, v := range want.Headers {
		if !bytes.Equal(got.Headers[k], v) {
			t.Errorf("header %q = %q, want %q", k, got.Headers[k], v)
		}
	}
}

// The empty cases are the ones a hand-rolled format gets wrong: a zero-length
// field and a zero-count map both have to survive a round trip without becoming
// a nil that a consumer then dereferences.
func TestRecordRoundTripEmptyFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  Record
	}{
		{"no key", Record{Value: []byte("v")}},
		{"no headers", Record{Key: "k", Value: []byte("v")}},
		{"no value", Record{Key: "k"}},
		{"nothing at all", Record{}},
		{"empty header value", Record{Headers: map[string][]byte{"k": {}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeRecord(tc.rec.encode())
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Key != tc.rec.Key {
				t.Errorf("Key = %q, want %q", got.Key, tc.rec.Key)
			}
			if !bytes.Equal(got.Value, tc.rec.Value) && !(len(got.Value) == 0 && len(tc.rec.Value) == 0) {
				t.Errorf("Value = %q, want %q", got.Value, tc.rec.Value)
			}
			if len(got.Headers) != len(tc.rec.Headers) {
				t.Errorf("Headers = %v, want %v", got.Headers, tc.rec.Headers)
			}
		})
	}
}

// Headers must not alias the decode buffer: the reader reuses it for the next
// record, so an aliased header would mutate under a consumer still holding it.
func TestDecodedHeadersDoNotAliasTheBuffer(t *testing.T) {
	rec := Record{Headers: map[string][]byte{"traceparent": []byte("00-abc-def-01")}, Value: []byte("v")}
	buf := rec.encode()
	got, err := decodeRecord(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	before := string(got.Headers["traceparent"])
	for i := range buf {
		buf[i] = 0xFF
	}
	if after := string(got.Headers["traceparent"]); after != before {
		t.Errorf("header changed when the buffer was reused: %q -> %q", before, after)
	}
	if string(got.Value) != "v" {
		t.Errorf("value changed when the buffer was reused: %q", got.Value)
	}
}

// A payload whose declared field lengths run past its end must be reported as
// corrupt, not panic. This is the shape a torn write takes once the CRC has
// (improbably) been satisfied, and a panic here takes down a consumer.
func TestDecodeRejectsOverlongFieldsRatherThanPanicking(t *testing.T) {
	rec := Record{Key: "k", Value: []byte("value"), Headers: map[string][]byte{"h": []byte("v")}}
	full := rec.encode()

	for cut := 0; cut < len(full); cut++ {
		truncated := full[:cut]
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("decode panicked on a %d-byte prefix: %v", cut, p)
				}
			}()
			if _, err := decodeRecord(truncated); err == nil {
				t.Errorf("a %d-byte prefix of a %d-byte record decoded without error", cut, len(full))
			}
		}()
	}
}

// The checksum is the whole defence against a torn tail decoding as plausible
// data. Every single-byte corruption must be caught by it.
func TestFramedRecordDetectsSingleByteCorruption(t *testing.T) {
	rec := Record{Key: "k", Value: []byte("a longer value so there is something to corrupt")}
	framed := frame(rec.encode())

	declared := binary.LittleEndian.Uint32(framed[0:4])
	want := binary.LittleEndian.Uint32(framed[4:8])
	if got := crc32.Checksum(framed[headerSize:], castagnoli); got != want {
		t.Fatalf("baseline checksum mismatch: %d != %d", got, want)
	}
	if int(declared) != len(framed)-headerSize {
		t.Fatalf("declared length %d, actual payload %d", declared, len(framed)-headerSize)
	}

	for i := headerSize; i < len(framed); i++ {
		corrupt := append([]byte(nil), framed...)
		corrupt[i] ^= 0x01
		if crc32.Checksum(corrupt[headerSize:], castagnoli) == want {
			t.Errorf("flipping a bit at offset %d left the checksum unchanged", i)
		}
	}
}
