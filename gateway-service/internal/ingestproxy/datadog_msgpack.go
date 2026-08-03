package ingestproxy

import (
	"bytes"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// Decode-bomb defense for the Datadog msgpack trace paths.
//
// A msgpack array/map header encodes its element count in a few bytes,
// independent of the actual payload size, and the decoder preallocates
// `make([]T, n)` before reading a single element. So a ~10-byte body can declare
// a 4-billion-element array and OOM-kill the process — a capped request body does
// not help. (github.com/vmihailenco/msgpack/v5 *has* a slice-alloc guard but a
// flag-check bug bypasses it for nested slices, which is exactly the [][]span
// shape DD traces use.) We therefore decode these paths with an explicit bound:
// every element consumes at least one wire byte, so no array/map can legitimately
// have more elements than the body has bytes. Anything over that is malformed and
// rejected before it can allocate.

// boundedArrayLen reads an array header and rejects a count above limit (or a
// negative count, msgpack's nil). limit is the body length in bytes.
func boundedArrayLen(d *msgpack.Decoder, limit int) (int, error) {
	n, err := d.DecodeArrayLen()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, nil
	}
	if n > limit {
		return 0, fmt.Errorf("array length %d exceeds body bound %d", n, limit)
	}
	return n, nil
}

// boundedMapLen is boundedArrayLen for maps.
func boundedMapLen(d *msgpack.Decoder, limit int) (int, error) {
	n, err := d.DecodeMapLen()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, nil
	}
	if n > limit {
		return 0, fmt.Errorf("map length %d exceeds body bound %d", n, limit)
	}
	return n, nil
}

// decodeDatadogTracesMsgpack decodes the v0.3/v0.4 [][]ddSpan msgpack shape with
// bounded outer/inner array lengths. Each span is map-keyed, and the library's
// map-alloc guard already bounds a single span's meta/metrics, so decoding the
// span itself via reflection is safe once the trace/span array counts are bounded.
func decodeDatadogTracesMsgpack(body []byte) ([][]ddSpan, error) {
	d := msgpack.NewDecoder(bytes.NewReader(body))
	limit := len(body)

	traceCount, err := boundedArrayLen(d, limit)
	if err != nil {
		return nil, err
	}
	out := make([][]ddSpan, 0, traceCount)
	for t := 0; t < traceCount; t++ {
		spanCount, err := boundedArrayLen(d, limit)
		if err != nil {
			return nil, err
		}
		spans := make([]ddSpan, 0, spanCount)
		for s := 0; s < spanCount; s++ {
			var span ddSpan
			if err := d.Decode(&span); err != nil {
				return nil, err
			}
			spans = append(spans, span)
		}
		out = append(out, spans)
	}
	return out, nil
}

// decodeV05SpanStream decodes one v0.5 span array positionally from the stream,
// bounding its meta/metrics maps and skipping any trailing fields beyond the core
// v05SpanArity (newer agents append e.g. a meta_struct element). get resolves a
// string-table index.
func decodeV05SpanStream(d *msgpack.Decoder, limit int, get func(uint32) string) (ddSpan, error) {
	fieldCount, err := d.DecodeArrayLen()
	if err != nil {
		return ddSpan{}, fmt.Errorf("v0.5 span is not an array: %w", err)
	}
	if fieldCount < v05SpanArity {
		return ddSpan{}, fmt.Errorf("v0.5 span has %d fields, want at least %d", fieldCount, v05SpanArity)
	}

	// Accumulate the first decode error across the scalar reads to keep it legible.
	var derr error
	u32 := func() uint32 {
		if derr != nil {
			return 0
		}
		var v uint32
		v, derr = d.DecodeUint32()
		return v
	}
	u64 := func() uint64 {
		if derr != nil {
			return 0
		}
		var v uint64
		v, derr = d.DecodeUint64()
		return v
	}
	i64 := func() int64 {
		if derr != nil {
			return 0
		}
		var v int64
		v, derr = d.DecodeInt64()
		return v
	}
	i32 := func() int32 {
		if derr != nil {
			return 0
		}
		var v int32
		v, derr = d.DecodeInt32()
		return v
	}

	service, name, resource := u32(), u32(), u32()
	traceID, spanID, parentID := u64(), u64(), u64()
	start, duration := i64(), i64()
	errCode := i32()
	if derr != nil {
		return ddSpan{}, fmt.Errorf("v0.5 span scalar field: %w", derr)
	}

	// meta: map[uint32]uint32 of string-table indices.
	metaLen, err := boundedMapLen(d, limit)
	if err != nil {
		return ddSpan{}, err
	}
	meta := make(map[string]string, metaLen)
	for i := 0; i < metaLen; i++ {
		k, kerr := d.DecodeUint32()
		v, verr := d.DecodeUint32()
		if kerr != nil || verr != nil {
			return ddSpan{}, fmt.Errorf("v0.5 span meta pair: %w", firstErr(kerr, verr))
		}
		meta[get(k)] = get(v)
	}

	// metrics: map[uint32]float64.
	metricsLen, err := boundedMapLen(d, limit)
	if err != nil {
		return ddSpan{}, err
	}
	metrics := make(map[string]float64, metricsLen)
	for i := 0; i < metricsLen; i++ {
		k, kerr := d.DecodeUint32()
		v, verr := d.DecodeFloat64()
		if kerr != nil || verr != nil {
			return ddSpan{}, fmt.Errorf("v0.5 span metrics pair: %w", firstErr(kerr, verr))
		}
		metrics[get(k)] = v
	}

	typ := u32()
	if derr != nil {
		return ddSpan{}, fmt.Errorf("v0.5 span type field: %w", derr)
	}

	// Skip any trailing fields a newer agent added beyond the core layout.
	for i := v05SpanArity; i < fieldCount; i++ {
		if err := d.Skip(); err != nil {
			return ddSpan{}, fmt.Errorf("v0.5 span trailing field %d: %w", i, err)
		}
	}

	return ddSpan{
		Service: get(service), Name: get(name), Resource: get(resource),
		TraceID: traceID, SpanID: spanID, ParentID: parentID,
		Start: start, Duration: duration, Error: errCode, Type: get(typ),
		Meta: meta, Metrics: metrics,
	}, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
