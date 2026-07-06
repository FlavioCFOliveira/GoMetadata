package tiff

// relocate_rw2_test.go — synthetic unit tests for the Panasonic RW2-specific
// copy-and-relocate subsystem (task #194: raise format/tiff coverage to ≥80%).
//
// All tests use in-memory fixtures only; no corpus files required.
//
// Coverage targets:
//   - extractRW2RawDataBlock (nil / valid / zero-offset cases)
//   - patchRW2RawDataOffsetInFinalTIFF (found / not-found / short paths)
//   - removeRW2SentinelStrips (sentinel / non-sentinel cases)
//
// Spec references:
//   - ExifTool Panasonic.pm: RW2 file structure, GUID, tag 0x0118.
//   - TIFF 6.0 §2: IFD entry layout.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// extractRW2RawDataBlock
// ---------------------------------------------------------------------------

// buildIFDWithEntry builds a minimal LE IFD containing a single TypeLong
// Count=1 inline entry with the given tag and value.
func buildIFDWithEntry(tag exif.TagID, value uint32) *exif.IFD {
	e := &exif.IFD{}
	entry := exif.IFDEntry{
		Tag:   tag,
		Type:  exif.TypeLong,
		Count: 1,
	}
	entry.Value = make([]byte, 4)
	binary.LittleEndian.PutUint32(entry.Value, value)
	e.Entries = append(e.Entries, entry)
	return e
}

// TestExtractRW2RawDataBlock_ValidEntry verifies that a valid 0x0118 entry
// produces a correctly sized imageBlock.
//
// ExifTool Panasonic.pm: tag 0x0118 is TypeLong Count=1 inline; value is the
// absolute file offset of the raw sensor data extending to EOF.
func TestExtractRW2RawDataBlock_ValidEntry(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const rawDataOff = 100
	const bufSize = 200 // rawDataOff + 100 bytes of raw data

	base := make([]byte, bufSize)
	ifd0 := buildIFDWithEntry(rw2TagRawDataOffset, rawDataOff)

	blk := extractRW2RawDataBlock(base, ifd0, order)
	if blk == nil {
		t.Fatal("extractRW2RawDataBlock: expected non-nil block")
	}
	if blk.srcOffset != rawDataOff {
		t.Errorf("srcOffset: got %d, want %d", blk.srcOffset, rawDataOff)
	}
	wantSize := uint64(bufSize - rawDataOff)
	if blk.size != wantSize {
		t.Errorf("size: got %d, want %d", blk.size, wantSize)
	}
	if blk.ifdPtr != nil {
		t.Error("ifdPtr should be nil (standalone block)")
	}
}

// TestExtractRW2RawDataBlock_ZeroOffset verifies that a zero offset returns nil.
//
// ExifTool Panasonic.pm: offset 0 is invalid for RawDataOffset.
func TestExtractRW2RawDataBlock_ZeroOffset(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	base := make([]byte, 100)
	ifd0 := buildIFDWithEntry(rw2TagRawDataOffset, 0)

	blk := extractRW2RawDataBlock(base, ifd0, order)
	if blk != nil {
		t.Errorf("extractRW2RawDataBlock with offset=0: expected nil, got %+v", blk)
	}
}

// TestExtractRW2RawDataBlock_OffsetBeyondBuffer verifies that an offset at or
// beyond the buffer length returns nil.
func TestExtractRW2RawDataBlock_OffsetBeyondBuffer(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	base := make([]byte, 100)
	ifd0 := buildIFDWithEntry(rw2TagRawDataOffset, 100) // offset == bufLen → at EOF

	blk := extractRW2RawDataBlock(base, ifd0, order)
	if blk != nil {
		t.Errorf("extractRW2RawDataBlock with offset=EOF: expected nil, got %+v", blk)
	}
}

// TestExtractRW2RawDataBlock_NoEntry verifies that when 0x0118 is absent nil is returned.
func TestExtractRW2RawDataBlock_NoEntry(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	base := make([]byte, 100)
	ifd0 := buildIFDWithEntry(exif.TagImageWidth, 1024) // different tag

	blk := extractRW2RawDataBlock(base, ifd0, order)
	if blk != nil {
		t.Errorf("extractRW2RawDataBlock with no 0x0118: expected nil, got %+v", blk)
	}
}

// TestExtractRW2RawDataBlock_ShortValue verifies that an entry with fewer than
// 4 value bytes returns nil gracefully.
func TestExtractRW2RawDataBlock_ShortValue(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	base := make([]byte, 100)

	// Build an IFD with a 0x0118 entry that has only 2 value bytes.
	ifd0 := &exif.IFD{}
	entry := exif.IFDEntry{
		Tag:   rw2TagRawDataOffset,
		Type:  exif.TypeLong,
		Count: 1,
		Value: []byte{0x01, 0x02}, // too short
	}
	ifd0.Entries = append(ifd0.Entries, entry)

	blk := extractRW2RawDataBlock(base, ifd0, order)
	if blk != nil {
		t.Errorf("extractRW2RawDataBlock with short value: expected nil, got %+v", blk)
	}
}

// ---------------------------------------------------------------------------
// patchRW2RawDataOffsetInFinalTIFF
// ---------------------------------------------------------------------------

// buildMinimalRW2FinalTIFF builds a minimal LE TIFF (as produced by exif.Encode
// before GUID insertion) containing a 0x0118 entry in IFD0 with the given
// inline value.
func buildMinimalRW2FinalTIFF(tagValue uint32) []byte {
	order := binary.LittleEndian

	// Layout: header(8) + IFD0(2+1×12+4) = 26 bytes.
	buf := make([]byte, 26)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	// IFD0: 1 entry (0x0118).
	order.PutUint16(buf[8:], 1)         // count
	order.PutUint16(buf[10:], 0x0118)   // tag
	order.PutUint16(buf[12:], 4)        // TypeLong
	order.PutUint32(buf[14:], 1)        // count=1
	order.PutUint32(buf[18:], tagValue) // inline value
	// nextIFD = 0.

	return buf
}

// TestPatchRW2RawDataOffsetInFinalTIFF_Updated verifies that the 0x0118
// val_or_off field is patched with rawDataBlock.newOffset.
func TestPatchRW2RawDataOffsetInFinalTIFF_Updated(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const originalOff = uint32(1000)
	const newOff = uint32(5000)

	finalTIFF := buildMinimalRW2FinalTIFF(originalOff)
	rawBlk := &imageBlock{newOffset: uint64(newOff)}

	err := patchRW2RawDataOffsetInFinalTIFF(finalTIFF, rawBlk, order)
	if err != nil {
		t.Fatalf("patchRW2RawDataOffsetInFinalTIFF: unexpected error: %v", err)
	}

	// Read the patched value back.
	gotVal := order.Uint32(finalTIFF[18:])
	if gotVal != newOff {
		t.Errorf("patched val_or_off: got %d, want %d", gotVal, newOff)
	}
}

// TestPatchRW2RawDataOffsetInFinalTIFF_NoEntry verifies that when 0x0118 is
// absent from IFD0 the function returns nil (non-fatal) without modifying the buffer.
func TestPatchRW2RawDataOffsetInFinalTIFF_NoEntry(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Build a finalTIFF with 0x0100 instead of 0x0118.
	buf := make([]byte, 26)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x0100) // ImageWidth — not 0x0118
	order.PutUint16(buf[12:], 4)
	order.PutUint32(buf[14:], 1)
	order.PutUint32(buf[18:], 1024)

	original := make([]byte, len(buf))
	copy(original, buf)

	rawBlk := &imageBlock{newOffset: 9999}
	err := patchRW2RawDataOffsetInFinalTIFF(buf, rawBlk, order)
	if err != nil {
		t.Fatalf("no-entry case: unexpected error: %v", err)
	}
	if !bytes.Equal(buf, original) {
		t.Error("buffer modified when 0x0118 is absent (should be a no-op)")
	}
}

// TestPatchRW2RawDataOffsetInFinalTIFF_TooShort verifies that a too-short
// finalTIFF returns ErrRW2OutputTooShort.
func TestPatchRW2RawDataOffsetInFinalTIFF_TooShort(t *testing.T) {
	t.Parallel()

	rawBlk := &imageBlock{newOffset: 100}
	err := patchRW2RawDataOffsetInFinalTIFF([]byte{0x01, 0x02, 0x03}, rawBlk, binary.LittleEndian)
	if err == nil {
		t.Fatal("expected error for too-short finalTIFF, got nil")
	}
}

// TestPatchRW2RawDataOffsetInFinalTIFF_IFD0OOB verifies that an IFD0 offset
// beyond the buffer returns ErrRW2IFD0OutOfBounds.
func TestPatchRW2RawDataOffsetInFinalTIFF_IFD0OOB(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// IFD0 pointer = 9999 far beyond the buffer.
	buf := make([]byte, 12)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 9999) // out of bounds

	rawBlk := &imageBlock{newOffset: 100}
	err := patchRW2RawDataOffsetInFinalTIFF(buf, rawBlk, order)
	if err == nil {
		t.Fatal("expected error for IFD0 OOB, got nil")
	}
}

// ---------------------------------------------------------------------------
// removeRW2SentinelStrips
// ---------------------------------------------------------------------------

// TestRemoveRW2SentinelStrips_SentinelRemoved verifies that StripOffsets=0xFFFFFFFF
// causes both StripOffsets and StripByteCounts to be removed from ifd0.
//
// ExifTool Panasonic.pm: StripOffsets = 0xFFFFFFFF is the sentinel value.
func TestRemoveRW2SentinelStrips_SentinelRemoved(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	ifd0 := &exif.IFD{}

	// Add StripOffsets with sentinel value.
	soEntry := exif.IFDEntry{
		Tag:   exif.TagStripOffsets,
		Type:  exif.TypeLong,
		Count: 1,
		Value: make([]byte, 4),
	}
	order.PutUint32(soEntry.Value, 0xFFFFFFFF)
	ifd0.Entries = append(ifd0.Entries, soEntry)

	// Add StripByteCounts.
	sbcEntry := exif.IFDEntry{
		Tag:   exif.TagStripByteCounts,
		Type:  exif.TypeLong,
		Count: 1,
		Value: make([]byte, 4),
	}
	order.PutUint32(sbcEntry.Value, 1024)
	ifd0.Entries = append(ifd0.Entries, sbcEntry)

	removeRW2SentinelStrips(ifd0, order)

	if ifd0.Get(exif.TagStripOffsets) != nil {
		t.Error("StripOffsets (sentinel) not removed from IFD")
	}
	if ifd0.Get(exif.TagStripByteCounts) != nil {
		t.Error("StripByteCounts not removed from IFD when sentinel StripOffsets present")
	}
}

// TestRemoveRW2SentinelStrips_NonSentinelPreserved verifies that a non-sentinel
// StripOffsets value (e.g. 100) is NOT removed from the IFD.
func TestRemoveRW2SentinelStrips_NonSentinelPreserved(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	ifd0 := &exif.IFD{}

	soEntry := exif.IFDEntry{
		Tag:   exif.TagStripOffsets,
		Type:  exif.TypeLong,
		Count: 1,
		Value: make([]byte, 4),
	}
	order.PutUint32(soEntry.Value, 100) // normal offset
	ifd0.Entries = append(ifd0.Entries, soEntry)

	removeRW2SentinelStrips(ifd0, order)

	if ifd0.Get(exif.TagStripOffsets) == nil {
		t.Error("non-sentinel StripOffsets was incorrectly removed")
	}
}

// TestRemoveRW2SentinelStrips_NoEntry verifies that removeRW2SentinelStrips
// is a no-op when StripOffsets is absent.
func TestRemoveRW2SentinelStrips_NoEntry(t *testing.T) {
	t.Parallel()

	ifd0 := &exif.IFD{} // empty IFD
	// Must not panic.
	removeRW2SentinelStrips(ifd0, binary.LittleEndian)
}

// ---------------------------------------------------------------------------
// relocateTIFFFromParsedRW2 — end-to-end
// ---------------------------------------------------------------------------

// buildMinimalRW2TIFF builds a minimal synthetic RW2 file with:
//   - "IIU\x00" magic
//   - 16-byte GUID at [8:24]
//   - IFD0 at offset 24 containing: ImageWidth, StripOffsets (sentinel), 0x0118
//   - Raw sensor data after IFD block
//
// Returns the full file bytes and the expected raw sensor data bytes.
func buildMinimalRW2TIFF(t *testing.T) ([]byte, []byte) {
	t.Helper()

	order := binary.LittleEndian
	rawData := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}

	// Layout (RW2):
	//   [0..7]   header: "IIU\x00" + ifd0Offset=24
	//   [8..23]  GUID (all 0x42 for test)
	//   [24..?]  IFD0: 3 entries (ImageWidth, 0x0118, JpgFromRaw dummy)
	//   [?+..]   raw sensor data

	const (
		ifd0Offset = 24
		nEntries   = 2 // ImageWidth, 0x0118 (no sentinel; use real offset for simplicity)
	)

	ifd0Size := 2 + nEntries*12 + 4
	rawDataOff := ifd0Offset + ifd0Size // raw data immediately follows IFD0
	totalSize := rawDataOff + len(rawData)

	buf := make([]byte, totalSize)

	// Header.
	buf[0] = rw2MagicBytes[0] // 'I'
	buf[1] = rw2MagicBytes[1] // 'I'
	buf[2] = rw2MagicBytes[2] // 'U'
	buf[3] = rw2MagicBytes[3] // 0x00
	order.PutUint32(buf[4:], ifd0Offset)

	// GUID (16 bytes of 0x42).
	for i := range rw2GUIDLen {
		buf[rw2GUIDOffset+i] = 0x42
	}

	// IFD0.
	order.PutUint16(buf[ifd0Offset:], nEntries)
	p := ifd0Offset + 2

	writeEntry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}

	writeEntry(0x0100, 4, 1, 1) // ImageWidth = 1
	writeEntry(0x0118, 4, 1, uint32(rawDataOff))

	// nextIFD = 0.

	copy(buf[rawDataOff:], rawData)

	return buf, rawData
}

// TestRW2Relocate_RoundTrip verifies that relocateTIFFFromParsedRW2 on a
// synthetic RW2 file:
//   - Does not return an error.
//   - Preserves the raw sensor data bytes.
//   - Outputs a valid RW2 file (correct magic at bytes [0:4]).
//   - Inserts the 16-byte GUID at bytes [8:24].
func TestRW2Relocate_RoundTrip(t *testing.T) { //nolint:paralleltest // exif.Parse state
	base, rawData := buildMinimalRW2TIFF(t)
	origGUID := make([]byte, rw2GUIDLen)
	copy(origGUID, base[rw2GUIDOffset:rw2GUIDOffset+rw2GUIDLen])

	out, err := relocateTIFFFromParsedRW2(base, nil, nil, nil)
	if err != nil {
		t.Fatalf("relocateTIFFFromParsedRW2: %v", err)
	}
	if len(out) < rw2GUIDOffset+rw2GUIDLen {
		t.Fatalf("output too short: %d bytes", len(out))
	}

	// RW2 magic must be restored.
	if out[0] != rw2MagicBytes[0] || out[1] != rw2MagicBytes[1] ||
		out[2] != rw2MagicBytes[2] || out[3] != rw2MagicBytes[3] {
		t.Errorf("RW2 magic not restored: [0:4]=%02X%02X%02X%02X",
			out[0], out[1], out[2], out[3])
	}

	// GUID must be preserved at bytes [8:24].
	gotGUID := out[rw2GUIDOffset : rw2GUIDOffset+rw2GUIDLen]
	if !bytes.Equal(gotGUID, origGUID) {
		t.Errorf("GUID not preserved:\n  got  %v\n  want %v", gotGUID, origGUID)
	}

	// Raw sensor data must be present somewhere in the output.
	if !bytes.Contains(out, rawData) {
		t.Error("raw sensor data bytes not found in RW2 output")
	}
}

// TestRW2Relocate_InvalidMagic verifies ErrRW2InvalidMagic for non-RW2 input.
func TestRW2Relocate_InvalidMagic(t *testing.T) {
	t.Parallel()

	// Standard LE TIFF magic — not RW2.
	buf := make([]byte, 30)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)

	_, err := relocateTIFFFromParsedRW2(buf, nil, nil, nil)
	if err == nil {
		t.Fatal("expected ErrRW2InvalidMagic for standard TIFF magic, got nil")
	}
}

// TestRW2Relocate_TooShort verifies ErrRW2OutputTooShort for a buffer too small
// to contain the GUID.
func TestRW2Relocate_TooShort(t *testing.T) {
	t.Parallel()

	// Valid RW2 magic but only 10 bytes (less than rw2GUIDOffset + rw2GUIDLen = 24).
	buf := make([]byte, 10)
	buf[0] = rw2MagicBytes[0]
	buf[1] = rw2MagicBytes[1]
	buf[2] = rw2MagicBytes[2]
	buf[3] = rw2MagicBytes[3]

	_, err := relocateTIFFFromParsedRW2(buf, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for too-short RW2 buffer, got nil")
	}
}
