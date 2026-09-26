package tiff

// task285_286_test.go — regression battery for tasks #285/#286 (Sprint 44,
// Batch E): the ORF/RW2 relocators must never mutate the "base"/
// "originalBytes" byte slice they are given. Before this fix,
// relocateTIFFFromParsedORF/relocateTIFFFromParsedRW2 patched bytes[2:4] to
// standard TIFF magic (0x2A 0x00) in place before the exif.Parse fallback
// (used whenever the caller passes e == nil), which forced every entry point
// — relocateTIFFAsORF/relocateTIFFAsRW2, and ultimately
// gometadata.writeTIFFORF/writeTIFFRW2 — to defensively clone the whole file
// first. exif.AcceptRAWMagic (exif package, #286) removes the need for that
// mutation entirely: base's real, on-disk magic bytes are passed through to
// exif.Parse unchanged, and the write-path clones this test guards against
// are gone (see write.go's writeTIFFORF/writeTIFFRW2).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestRelocateTIFFFromParsedORF_DoesNotMutateBase proves that base's bytes
// are unchanged after a call that takes the exif.Parse fallback path (e ==
// nil), which is exactly the code path that used to patch base[2:4] in
// place.
func TestRelocateTIFFFromParsedORF_DoesNotMutateBase(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	buf := make([]byte, 40)
	buf[0], buf[1] = 0x49, 0x49 // "II"
	buf[2], buf[3] = 0x52, 0x4F // "RO" -> ORF magic "IIRO"
	order.PutUint32(buf[4:], 8)

	// IFD0: 1 entry (ImageWidth).
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x0100) // ImageWidth
	order.PutUint16(buf[12:], 4)      // TypeLong
	order.PutUint32(buf[14:], 1)
	order.PutUint32(buf[18:], 100)
	// nextIFD = 0.

	before := bytes.Clone(buf)

	if _, _, err := relocateTIFFFromParsedORF(buf, nil, uint64(len(buf)), nil, nil, nil); err != nil {
		t.Fatalf("relocateTIFFFromParsedORF: %v", err)
	}

	if !bytes.Equal(buf, before) {
		t.Errorf("base was mutated by relocateTIFFFromParsedORF (e == nil fallback path):\n"+
			"before: % x\nafter:  % x", before, buf)
	}
	// Sanity: the magic bytes specifically, since those were the exact bytes
	// the old in-place patch overwrote.
	if buf[2] != 0x52 || buf[3] != 0x4F {
		t.Errorf("ORF magic bytes[2:4] changed: got %02X %02X, want 52 4F", buf[2], buf[3])
	}
}

// TestRelocateTIFFFromParsedRW2_DoesNotMutateBase proves that base's bytes
// are unchanged after a call that takes the exif.Parse fallback path (e ==
// nil), which is exactly the code path that used to patch base[2:4] in
// place. Reuses buildMinimalRW2TIFF from relocate_rw2_test.go.
func TestRelocateTIFFFromParsedRW2_DoesNotMutateBase(t *testing.T) { //nolint:paralleltest // exif.Parse global side-effects, mirrors sibling tests in relocate_rw2_test.go
	buf, _ := buildMinimalRW2TIFF(t)
	before := bytes.Clone(buf)

	if _, _, err := relocateTIFFFromParsedRW2(buf, uint64(len(buf)), nil, nil, nil); err != nil {
		t.Fatalf("relocateTIFFFromParsedRW2: %v", err)
	}

	if !bytes.Equal(buf, before) {
		t.Errorf("base was mutated by relocateTIFFFromParsedRW2 (e == nil fallback path):\n"+
			"before: % x\nafter:  % x", before, buf)
	}
	if buf[2] != rw2MagicBytes[2] || buf[3] != rw2MagicBytes[3] {
		t.Errorf("RW2 magic bytes[2:4] changed: got %02X %02X, want %02X %02X",
			buf[2], buf[3], rw2MagicBytes[2], rw2MagicBytes[3])
	}
}

// TestRelocateTIFFAsORF_NoDefensiveCloneNeeded proves relocateTIFFAsORF is a
// pure pass-through to relocateTIFFFromParsedORF (no internal clone): the
// returned output must not alias originalBytes' backing array in a way that
// would break if originalBytes were reused, but more importantly this test
// pins the observable behaviour (no error, valid ORF magic restored) so a
// future re-introduction of the removed clone (which would still pass this
// test) is caught by the non-mutation test above instead — documented here
// for discoverability alongside the other #285/#286 regression tests.
func TestRelocateTIFFAsORF_NoDefensiveCloneNeeded(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	buf := make([]byte, 40)
	buf[0], buf[1] = 0x49, 0x49
	buf[2], buf[3] = 0x52, 0x4F
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x0100)
	order.PutUint16(buf[12:], 4)
	order.PutUint32(buf[14:], 1)
	order.PutUint32(buf[18:], 100)

	before := bytes.Clone(buf)
	out, _, err := relocateTIFFAsORF(buf, nil, uint64(len(buf)), nil, nil, nil)
	if err != nil {
		t.Fatalf("relocateTIFFAsORF: %v", err)
	}
	if !bytes.Equal(buf, before) {
		t.Error("relocateTIFFAsORF mutated its originalBytes argument")
	}
	if len(out) < 4 || out[2] != 0x52 || out[3] != 0x4F {
		t.Errorf("ORF magic not restored in output: % x", out[:min(4, len(out))])
	}
}
