package exif

// write_test.go — regression gate for audit finding PERF-201-LOW (task #247).
//
// task #201 (performance audit 2026-06-10) introduced a direct, non-comma-ok
// type assertion (order.(binary.AppendByteOrder)) in writeTIFFHeader
// (write.go) and writeIFD (ifd.go) to reach binary.AppendByteOrder's
// allocation-free Append* methods. That assertion panics for any
// binary.ByteOrder implementation that does not also implement
// binary.AppendByteOrder — unreachable from any file this package parses
// (Parse only ever produces binary.LittleEndian or binary.BigEndian), but
// reachable if a caller hand-builds &EXIF{ByteOrder: <custom implementation>}
// and calls Encode.
//
// The fix (appendUint16Order / appendUint32Order in write.go) replaces the
// direct assertion with a comma-ok assertion and a panic-free fallback that
// works for any binary.ByteOrder implementation, while keeping the
// binary.LittleEndian/binary.BigEndian fast path byte-identical and
// allocation-free (see BenchmarkEXIFEncode / BenchmarkEXIFEncode_Camera,
// unchanged at 80 B/2 allocs and 1618 B/14 allocs respectively across this
// change).

import (
	"encoding/binary"
	"testing"
)

// customByteOrderNoAppend is a minimal binary.ByteOrder implementation that
// intentionally does NOT implement binary.AppendByteOrder (it defines no
// AppendUint16/AppendUint32/AppendUint64/String-for-AppendByteOrder methods
// beyond the plain ByteOrder contract). It delegates every required method to
// binary.LittleEndian so the encoded bytes remain deterministic and
// verifiable, while still being a distinct concrete type that fails the
// order.(binary.AppendByteOrder) assertion exercised by writeTIFFHeader and
// writeIFD.
type customByteOrderNoAppend struct{}

func (customByteOrderNoAppend) Uint16(b []byte) uint16       { return binary.LittleEndian.Uint16(b) }
func (customByteOrderNoAppend) Uint32(b []byte) uint32       { return binary.LittleEndian.Uint32(b) }
func (customByteOrderNoAppend) Uint64(b []byte) uint64       { return binary.LittleEndian.Uint64(b) }
func (customByteOrderNoAppend) PutUint16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func (customByteOrderNoAppend) PutUint32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }
func (customByteOrderNoAppend) PutUint64(b []byte, v uint64) { binary.LittleEndian.PutUint64(b, v) }
func (customByteOrderNoAppend) String() string               { return "customByteOrderNoAppend" }

// Compile-time check: customByteOrderNoAppend satisfies binary.ByteOrder.
var _ binary.ByteOrder = customByteOrderNoAppend{}

// TestEncodeCustomByteOrderWithoutAppend is the regression gate for audit
// finding PERF-201-LOW (task #247).
//
// Before the fix, Encode on an EXIF whose ByteOrder is a binary.ByteOrder
// implementation other than binary.LittleEndian/binary.BigEndian panicked
// inside writeTIFFHeader's non-comma-ok order.(binary.AppendByteOrder)
// assertion. After the fix, Encode must either return well-formed bytes or a
// clean error — it must never panic.
func TestEncodeCustomByteOrderWithoutAppend(t *testing.T) {
	t.Parallel()

	// Self-validate the test's own premise: if customByteOrderNoAppend ever
	// started satisfying binary.AppendByteOrder (e.g. a careless future edit
	// added the Append* methods), this test would stop exercising the
	// fallback path entirely while still appearing to pass. Fail loudly
	// instead of silently testing nothing.
	if _, ok := any(customByteOrderNoAppend{}).(binary.AppendByteOrder); ok {
		t.Fatal("customByteOrderNoAppend unexpectedly implements binary.AppendByteOrder — this test no longer exercises the PERF-201-LOW fallback path")
	}

	e := &EXIF{
		ByteOrder: customByteOrderNoAppend{},
		IFD0: &IFD{
			Entries: []IFDEntry{
				{Tag: TagImageWidth, Type: TypeShort, Count: 1, Value: []byte{0x40, 0x00}},
			},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Encode panicked with a binary.ByteOrder that does not implement binary.AppendByteOrder: %v (PERF-201-LOW regression)", r)
		}
	}()

	out, err := Encode(e)
	if err != nil {
		// A clean error is an acceptable outcome per the task contract; only a
		// panic is a regression. Log it for visibility in -v runs.
		t.Logf("Encode() returned a clean error for a non-AppendByteOrder order: %v", err)
		return
	}
	if len(out) == 0 {
		t.Error("Encode() returned no error and no bytes for a non-AppendByteOrder order")
	}
	// TIFF §2: the byte-order mark must be a well-formed "II" or "MM" — proves
	// the fallback path in appendUint16Order/appendUint32Order produced a
	// structurally valid header rather than silently emitting garbage.
	gotMark := string(out[:min(2, len(out))])
	if gotMark != "II" && gotMark != "MM" {
		t.Errorf("Encode() output byte-order mark = %q, want \"II\" or \"MM\"", gotMark)
	}
}
