package tiff

// relocate_helpers_test.go — targeted unit tests for low-coverage helper functions
// in the format/tiff package (task #194: raise coverage to ≥80%).
//
// Coverage targets:
//   - readUint: size=2 (LE/BE), size=unsupported, truncation
//   - bytecountTagFor: JPEGInterchangeFormat, unknown tag
//   - findNikonBlobInBase: found, not-found, short inputs
//   - rebaseIFDInBlob: OOL rebase, sub-IFD pointer rebase, inline skip
//   - rebaseSonyMakerNote: IFD scan with OOL rebase
//   - rebaseOlympMakerNote: full IFD walk (exercising ExifIFD + MakerNote chain)
//
// Spec references:
//   - TIFF 6.0 §2: IFD entry layout.
//   - ExifTool Nikon.pm: findNikonBlobInBase prefix = "Nikon\x00".
//   - ExifTool Olympus.pm / Sony.pm: rebase conventions.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// readUint
// ---------------------------------------------------------------------------

// TestReadUint_Size2LE verifies readUint with size=2, little-endian.
func TestReadUint_Size2LE(t *testing.T) {
	t.Parallel()

	b := []byte{0x34, 0x12, 0xFF}
	got, err := readUint(b, 2, binary.LittleEndian)
	if err != nil {
		t.Fatalf("readUint size=2 LE: unexpected error: %v", err)
	}
	if got != 0x1234 {
		t.Errorf("readUint size=2 LE: got 0x%X, want 0x1234", got)
	}
}

// TestReadUint_Size2BE verifies readUint with size=2, big-endian.
func TestReadUint_Size2BE(t *testing.T) {
	t.Parallel()

	b := []byte{0x12, 0x34}
	got, err := readUint(b, 2, binary.BigEndian)
	if err != nil {
		t.Fatalf("readUint size=2 BE: unexpected error: %v", err)
	}
	if got != 0x1234 {
		t.Errorf("readUint size=2 BE: got 0x%X, want 0x1234", got)
	}
}

// TestReadUint_Size2Truncated verifies ErrTruncatedOffsetArray for size=2 with < 2 bytes.
func TestReadUint_Size2Truncated(t *testing.T) {
	t.Parallel()

	_, err := readUint([]byte{0x01}, 2, binary.LittleEndian)
	if err == nil {
		t.Fatal("expected ErrTruncatedOffsetArray for size=2 with 1 byte, got nil")
	}
	if !errors.Is(err, ErrTruncatedOffsetArray) {
		t.Errorf("want ErrTruncatedOffsetArray, got %v", err)
	}
}

// TestReadUint_Size4Truncated verifies ErrTruncatedOffsetArray for size=4 with < 4 bytes.
func TestReadUint_Size4Truncated(t *testing.T) {
	t.Parallel()

	_, err := readUint([]byte{0x01, 0x02, 0x03}, 4, binary.LittleEndian)
	if err == nil {
		t.Fatal("expected ErrTruncatedOffsetArray for size=4 with 3 bytes, got nil")
	}
	if !errors.Is(err, ErrTruncatedOffsetArray) {
		t.Errorf("want ErrTruncatedOffsetArray, got %v", err)
	}
}

// TestReadUint_UnsupportedSize verifies ErrUnsupportedElemSize for size=1.
func TestReadUint_UnsupportedSize(t *testing.T) {
	t.Parallel()

	_, err := readUint([]byte{0x01}, 1, binary.LittleEndian)
	if err == nil {
		t.Fatal("expected ErrUnsupportedElemSize for size=1, got nil")
	}
	if !errors.Is(err, ErrUnsupportedElemSize) {
		t.Errorf("want ErrUnsupportedElemSize, got %v", err)
	}
}

// TestReadUint_Size4LE verifies readUint with size=4, little-endian (existing branch).
func TestReadUint_Size4LE(t *testing.T) {
	t.Parallel()

	b := []byte{0x78, 0x56, 0x34, 0x12}
	got, err := readUint(b, 4, binary.LittleEndian)
	if err != nil {
		t.Fatalf("readUint size=4 LE: %v", err)
	}
	if got != 0x12345678 {
		t.Errorf("readUint size=4 LE: got 0x%X, want 0x12345678", got)
	}
}

// ---------------------------------------------------------------------------
// bytecountTagFor
// ---------------------------------------------------------------------------

// TestBytecountTagFor_JPEGInterchangeFormat verifies the JPEGInterchangeFormat branch.
//
// TIFF 6.0: JPEGInterchangeFormat (0x0201) → JPEGInterchangeFormatLength (0x0202).
func TestBytecountTagFor_JPEGInterchangeFormat(t *testing.T) {
	t.Parallel()

	got := bytecountTagFor(exif.TagJPEGInterchangeFormat)
	if got != exif.TagJPEGInterchangeFormatLength {
		t.Errorf("bytecountTagFor(JPEGInterchangeFormat): got tag 0x%04X, want 0x%04X",
			got, exif.TagJPEGInterchangeFormatLength)
	}
}

// TestBytecountTagFor_StripOffsets verifies the StripOffsets branch.
func TestBytecountTagFor_StripOffsets(t *testing.T) {
	t.Parallel()

	got := bytecountTagFor(exif.TagStripOffsets)
	if got != exif.TagStripByteCounts {
		t.Errorf("bytecountTagFor(StripOffsets): got 0x%04X, want TagStripByteCounts", got)
	}
}

// TestBytecountTagFor_TileOffsets verifies the TileOffsets branch.
func TestBytecountTagFor_TileOffsets(t *testing.T) {
	t.Parallel()

	got := bytecountTagFor(exif.TagTileOffsets)
	if got != exif.TagTileByteCounts {
		t.Errorf("bytecountTagFor(TileOffsets): got 0x%04X, want TagTileByteCounts", got)
	}
}

// TestBytecountTagFor_Unknown verifies that unknown tags return 0.
func TestBytecountTagFor_Unknown(t *testing.T) {
	t.Parallel()

	got := bytecountTagFor(exif.TagID(0x1234)) // unknown
	if got != 0 {
		t.Errorf("bytecountTagFor(unknown 0x1234): got 0x%04X, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// findNikonBlobInBase
// ---------------------------------------------------------------------------

// TestFindNikonBlobInBase_Found verifies that a Nikon blob prefix is found at
// the correct offset.
//
// ExifTool Nikon.pm: Nikon Type-3 MakerNote prefix = "Nikon\x00" (6 bytes).
// findNikonBlobInBase matches the first 6 bytes of blob against base.
func TestFindNikonBlobInBase_Found(t *testing.T) {
	t.Parallel()

	// Build a base buffer with a Nikon MakerNote blob starting at offset 100.
	base := make([]byte, 200)
	blob := []byte{'N', 'i', 'k', 'o', 'n', 0x00, 0x02, 0x00, 0x00, 0x00}
	copy(base[100:], blob)

	got := findNikonBlobInBase(base, blob)
	if got != 100 {
		t.Errorf("findNikonBlobInBase: got %d, want 100", got)
	}
}

// TestFindNikonBlobInBase_NotFound verifies that a missing prefix returns 0.
func TestFindNikonBlobInBase_NotFound(t *testing.T) {
	t.Parallel()

	base := make([]byte, 100)
	blob := []byte{'N', 'i', 'k', 'o', 'n', 0x00, 0x99}

	got := findNikonBlobInBase(base, blob)
	if got != 0 {
		t.Errorf("findNikonBlobInBase not-found: got %d, want 0", got)
	}
}

// TestFindNikonBlobInBase_BlobTooShort verifies that a blob shorter than 6 bytes
// returns 0.
func TestFindNikonBlobInBase_BlobTooShort(t *testing.T) {
	t.Parallel()

	base := make([]byte, 50)
	blob := []byte{'N', 'i', 'k'} // only 3 bytes

	got := findNikonBlobInBase(base, blob)
	if got != 0 {
		t.Errorf("findNikonBlobInBase short blob: got %d, want 0", got)
	}
}

// TestFindNikonBlobInBase_BaseTooShort verifies that a base shorter than 6 bytes
// returns 0.
func TestFindNikonBlobInBase_BaseTooShort(t *testing.T) {
	t.Parallel()

	base := []byte{'N', 'i', 'k'} // only 3 bytes
	blob := []byte{'N', 'i', 'k', 'o', 'n', 0x00}

	got := findNikonBlobInBase(base, blob)
	if got != 0 {
		t.Errorf("findNikonBlobInBase short base: got %d, want 0", got)
	}
}

// TestFindNikonBlobInBase_AtStart verifies detection when the blob starts at offset 0.
func TestFindNikonBlobInBase_AtStart(t *testing.T) {
	t.Parallel()

	blob := []byte{'N', 'i', 'k', 'o', 'n', 0x00, 0x02, 0x00}
	base := make([]byte, 50)
	copy(base, blob)

	got := findNikonBlobInBase(base, blob)
	if got != 0 {
		t.Errorf("findNikonBlobInBase at start: got %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// rebaseIFDInBlob
// ---------------------------------------------------------------------------

// buildIFDBlobWithOOLEntry builds a minimal IFD blob with a single OOL entry.
// The OOL val_or_off field is set to absOOLOff (TIFF-absolute).
//
// Layout: count(2) + entry(12) = 14 bytes.
func buildIFDBlobWithOOLEntry(absOOLOff, count uint32, order binary.ByteOrder) []byte {
	buf := make([]byte, 14)
	order.PutUint16(buf[0:], 1) // 1 entry
	// Entry: TypeByte=1, Count=count, val_or_off=absOOLOff (OOL when count > 4)
	order.PutUint16(buf[2:], 0x0001)     // arbitrary tag
	order.PutUint16(buf[4:], 1)          // TypeByte
	order.PutUint32(buf[6:], count)      // count
	order.PutUint32(buf[10:], absOOLOff) // val_or_off
	return buf
}

// TestRebaseIFDInBlob_OOLEntryRebased verifies that an OOL entry whose val_or_off
// is within [srcOff, srcOff+len(rawSR2)) is rebased correctly.
//
// Simulates the case where the SR2 block moves from srcOff to newOff.
// TIFF 6.0 §2: val_or_off for OOL entries is an absolute file offset.
func TestRebaseIFDInBlob_OOLEntryRebased(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const srcOff = uint32(1000)
	const newOff = uint32(3000)
	const absOOLOff = uint32(1050) // within [srcOff, srcOff+300)

	// rawSR2 = a block of 300 bytes starting at srcOff.
	rawSR2 := make([]byte, 300)

	// Build blob with an OOL entry pointing to absOOLOff.
	// count=8 → TypeByte(1) × 8 = 8 > 4 → OOL.
	blob := buildIFDBlobWithOOLEntry(absOOLOff, 8, order)

	rebaseIFDInBlob(blob, rawSR2, srcOff, newOff, order, false)

	// After rebase: val_or_off should be newOff + (absOOLOff - srcOff) = 3050.
	gotVOO := order.Uint32(blob[10:])
	wantVOO := newOff + (absOOLOff - srcOff) // 3050
	if gotVOO != wantVOO {
		t.Errorf("OOL rebase: got val_or_off 0x%X, want 0x%X", gotVOO, wantVOO)
	}
}

// TestRebaseIFDInBlob_InlineSkipped verifies that an inline entry (count ≤ 4 for
// TypeByte) is NOT modified.
func TestRebaseIFDInBlob_InlineSkipped(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const srcOff = uint32(1000)
	const newOff = uint32(3000)
	const inlineVal = uint32(0xABCDEFFF)

	// TypeByte × 4 = 4 bytes → inline.
	blob := buildIFDBlobWithOOLEntry(inlineVal, 4, order)
	original := make([]byte, len(blob))
	copy(original, blob)

	rawSR2 := make([]byte, 300)
	rebaseIFDInBlob(blob, rawSR2, srcOff, newOff, order, false)

	// val_or_off must be unchanged (it was inline).
	if !bytes.Equal(blob, original) {
		t.Error("inline entry modified by rebaseIFDInBlob (should be no-op)")
	}
}

// TestRebaseIFDInBlob_OutsideSrcOffSkipped verifies that a val_or_off pointing
// before srcOff is not rebased.
func TestRebaseIFDInBlob_OutsideSrcOffSkipped(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const srcOff = uint32(1000)
	const newOff = uint32(3000)
	const absOOLOff = uint32(500) // BEFORE srcOff

	rawSR2 := make([]byte, 300)
	blob := buildIFDBlobWithOOLEntry(absOOLOff, 8, order)
	original := make([]byte, len(blob))
	copy(original, blob)

	rebaseIFDInBlob(blob, rawSR2, srcOff, newOff, order, false)

	// val_or_off points outside [srcOff, srcOff+300) → must be unchanged.
	gotVOO := order.Uint32(blob[10:])
	if gotVOO != absOOLOff {
		t.Errorf("outside-srcOff: val_or_off changed from 0x%X to 0x%X (should be no-op)",
			absOOLOff, gotVOO)
	}
}

// TestRebaseIFDInBlob_TypeLongSubIFDPointerRebased verifies that followSubIFDs=true
// rebases uint32 values in a TypeLong OOL array when they point within the SR2 block.
//
// ExifTool Sony.pm: SR2 IFD tag 0x7241 carries a TypeLong OOL array; each element
// that points within the SR2 block is a sub-IFD offset and must be rebased.
//
// Note: Count=1 TypeLong → total=4 bytes → inline (skipped by rebaseIFDInBlob).
// Use Count=2 so total=8 bytes → OOL entry.
func TestRebaseIFDInBlob_TypeLongSubIFDPointerRebased(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const srcOff = uint32(2000)
	const newOff = uint32(5000)

	// Build rawSR2 with a sub-IFD pointer array at relative offset 40.
	// The array has TWO TypeLong elements (count=2 → total=8 → OOL):
	//   [40..43]: pointer to sub-IFD at srcOff+60 = 2060
	//   [44..47]: pointer to sub-IFD at srcOff+62 = 2062
	rawSR2 := make([]byte, 200)
	const subIFDPtrRelOff = 40
	const subIFDRelOff = 60
	const subIFDRelOff2 = 62
	order.PutUint32(rawSR2[subIFDPtrRelOff:], srcOff+subIFDRelOff)    // first ptr = 2060
	order.PutUint32(rawSR2[subIFDPtrRelOff+4:], srcOff+subIFDRelOff2) // second ptr = 2062

	// Sub-IFDs at rawSR2[60] and rawSR2[62]: 0 entries each.
	order.PutUint16(rawSR2[subIFDRelOff:], 0)
	order.PutUint16(rawSR2[subIFDRelOff2:], 0)

	// Build a blob with a TypeLong OOL array entry (count=2 → total=8 → OOL).
	// val_or_off = srcOff + subIFDPtrRelOff = 2040.
	buf := make([]byte, 14)
	order.PutUint16(buf[0:], 1)      // 1 entry
	order.PutUint16(buf[2:], 0x0001) // tag
	order.PutUint16(buf[4:], 4)      // TypeLong
	order.PutUint32(buf[6:], 2)      // count=2 → total=8 → OOL
	order.PutUint32(buf[10:], srcOff+subIFDPtrRelOff)

	rebaseIFDInBlob(buf, rawSR2, srcOff, newOff, order, true)

	// The OOL pointer in blob entry should be rebased.
	gotVOO := order.Uint32(buf[10:])
	wantVOO := newOff + subIFDPtrRelOff // 5040
	if gotVOO != wantVOO {
		t.Errorf("TypeLong OOL entry: val_or_off got 0x%X, want 0x%X", gotVOO, wantVOO)
	}

	// The first sub-IFD pointer VALUE in rawSR2 should also be rebased.
	gotSubVal := order.Uint32(rawSR2[subIFDPtrRelOff:])
	wantSubVal := newOff + subIFDRelOff // 5060
	if gotSubVal != wantSubVal {
		t.Errorf("sub-IFD pointer[0] in rawSR2: got 0x%X, want 0x%X", gotSubVal, wantSubVal)
	}
}

// ---------------------------------------------------------------------------
// rebaseSonyMakerNote — exercising the IFD scan path
// ---------------------------------------------------------------------------

// buildFinalTIFFWithSonyMNAndOOL builds a synthetic LE finalTIFF that contains:
//   - IFD0 with ExifIFD pointer (tag 0x8769) → ExifIFD
//   - ExifIFD with MakerNote (tag 0x927C) → Sony plain-IFD MakerNote blob
//   - MakerNote blob: plain IFD with 1 OOL entry at val_or_off = oolAbsOff
//
// Returns (finalTIFF, mnAbsOff, oolEntryOff).
// mnAbsOff is the absolute offset of the MakerNote blob in finalTIFF.
// oolEntryOff is the absolute offset of the OOL val_or_off field to check.
func buildFinalTIFFWithSonyMNAndOOL(t *testing.T, oolAbsOff uint32) (finalTIFF []byte, mnAbsOff uint32, oolValOff int) {
	t.Helper()

	order := binary.LittleEndian

	// Layout (all offsets LE, IFD0 at byte 8):
	//
	//   [0:8]    TIFF header: "II" + 0x2A00 + IFD0=8
	//   [8:10]   IFD0 count = 1
	//   [10:22]  IFD0 entry: tag=0x8769 (ExifIFD ptr), TypeLong, count=1, val=exifStart
	//   [22:26]  nextIFD = 0
	//   [26:28]  ExifIFD count = 1
	//   [28:40]  ExifIFD entry: tag=0x927C (MakerNote), TypeUndefined, count=mnSize, val_or_off=mnAbsOff
	//   [40:44]  ExifIFD nextIFD = 0
	//   [44:??]  MakerNote blob: plain IFD with 1 OOL entry

	// MakerNote IFD: 1 entry (OOL, count=8 TypeByte → total=8>4).
	//   mnBlob[0:2]   = count = 1
	//   mnBlob[2:14]  = entry: tag=0x0001, TypeByte, count=8, val_or_off=oolAbsOff
	mnBlobSize := 14
	mnBlobData := make([]byte, mnBlobSize)
	order.PutUint16(mnBlobData[0:], 1)          // 1 entry
	order.PutUint16(mnBlobData[2:], 0x0001)     // tag
	order.PutUint16(mnBlobData[4:], 1)          // TypeByte
	order.PutUint32(mnBlobData[6:], 8)          // count=8 → OOL
	order.PutUint32(mnBlobData[10:], oolAbsOff) // val_or_off

	const (
		ifd0Off   = 8
		exifStart = 26
		mnStart   = 44
	)
	mnAbsOff = mnStart

	totalSize := mnStart + mnBlobSize
	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0.
	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], uint16(exif.TagExifIFDPointer))
	order.PutUint16(buf[ifd0Off+4:], 4) // TypeLong
	order.PutUint32(buf[ifd0Off+6:], 1)
	order.PutUint32(buf[ifd0Off+10:], exifStart)
	// nextIFD = 0.

	// ExifIFD.
	order.PutUint16(buf[exifStart:], 1)
	order.PutUint16(buf[exifStart+2:], uint16(exif.TagMakerNote))
	order.PutUint16(buf[exifStart+4:], 7) // TypeUndefined
	order.PutUint32(buf[exifStart+6:], uint32(mnBlobSize))
	order.PutUint32(buf[exifStart+10:], mnStart)
	// ExifIFD nextIFD = 0.

	// MakerNote blob.
	copy(buf[mnStart:], mnBlobData)

	// OOL val_or_off field is at mnStart + 10.
	oolValOff = mnStart + 10
	return buf, mnAbsOff, oolValOff
}

// TestRebaseSonyMakerNote_OOLEntriesRebased verifies that rebaseSonyMakerNote
// correctly rebases OOL val_or_off entries in the MakerNote IFD.
//
// Sony MakerNote OOL offsets are TIFF-file-absolute (ExifTool Sony.pm).
func TestRebaseSonyMakerNote_OOLEntriesRebased(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Scenario:
	//   - MakerNote blob is at mnAbsOff=44 in the current finalTIFF (58 bytes).
	//   - info.mnSrcOffset = 20 (pretend it was at offset 20 before encoding).
	//   - OOL entry val_or_off stored in the blob = 26.
	//   - relOff = 26 - 20 = 6.
	//   - newVOO = 44 + 6 = 50.
	//   - Bounds check: 50 + 8 (TypeByte count=8) = 58 <= len(finalTIFF)=58 ✓
	//
	// Why oolAbsOff=26 and not larger values:
	//   With oolAbsOff=54: newVOO = 44+(54-20)=78; bounds: 78+8=86 > 58 → guard fires.
	const (
		fakeSrcOff = uint32(20) // pretend the MakerNote "was" at offset 20
		oolAbsOff  = uint32(26) // OOL val_or_off that fits within the 58-byte buffer
	)

	finalTIFF, mnAbsOff, oolValOff := buildFinalTIFFWithSonyMNAndOOL(t, oolAbsOff)

	// Build a minimal sonySR2Info.
	mnEntry := &exif.IFDEntry{
		Tag:   exif.TagMakerNote,
		Type:  exif.TypeUndefined,
		Count: uint32(len(finalTIFF)), //nolint:gosec // G115: test helper
		Value: finalTIFF[mnAbsOff:],
	}
	info := &sonySR2Info{
		mnSrcOffset: fakeSrcOff, // != mnAbsOff(44) → triggers rebase
		mnEntry:     mnEntry,
		sr2RawBytes: nil, // no SR2 block
	}

	if err := rebaseSonyMakerNote(finalTIFF, info, order); err != nil {
		t.Fatalf("rebaseSonyMakerNote: %v", err)
	}

	// Expected: relOff = oolAbsOff - fakeSrcOff = 26 - 20 = 6.
	// newVOO = mnAbsOff + relOff = 44 + 6 = 50.
	gotVOO := order.Uint32(finalTIFF[oolValOff:])
	wantVOO := mnAbsOff + (oolAbsOff - fakeSrcOff) // 44 + 6 = 50
	if gotVOO != wantVOO {
		t.Errorf("rebaseSonyMakerNote OOL: got val_or_off 0x%X, want 0x%X", gotVOO, wantVOO)
	}
}

// TestRebaseSonyMakerNote_NilEntry verifies that a nil mnEntry is a no-op.
func TestRebaseSonyMakerNote_NilEntry(t *testing.T) {
	t.Parallel()

	info := &sonySR2Info{mnEntry: nil}
	finalTIFF := make([]byte, 20)

	if err := rebaseSonyMakerNote(finalTIFF, info, binary.LittleEndian); err != nil {
		t.Fatalf("nil mnEntry: unexpected error: %v", err)
	}
}

// TestRebaseSonyMakerNote_SameOffset verifies that when mnSrcOffset == new position
// the function is a no-op (blob did not move).
func TestRebaseSonyMakerNote_SameOffset(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const oolAbsOff = uint32(54)

	finalTIFF, mnAbsOff, oolValOff := buildFinalTIFFWithSonyMNAndOOL(t, oolAbsOff)

	mnEntry := &exif.IFDEntry{
		Tag:   exif.TagMakerNote,
		Type:  exif.TypeUndefined,
		Count: uint32(len(finalTIFF) - int(mnAbsOff)), //nolint:gosec // G115: test helper
		Value: finalTIFF[mnAbsOff:],
	}
	info := &sonySR2Info{
		mnSrcOffset: mnAbsOff, // SAME as actual position → no rebase
		mnEntry:     mnEntry,
	}

	if err := rebaseSonyMakerNote(finalTIFF, info, order); err != nil {
		t.Fatalf("same-offset: unexpected error: %v", err)
	}

	// val_or_off must be unchanged.
	gotVOO := order.Uint32(finalTIFF[oolValOff:])
	if gotVOO != oolAbsOff {
		t.Errorf("same-offset no-op: got val_or_off 0x%X, want 0x%X", gotVOO, oolAbsOff)
	}
}

// ---------------------------------------------------------------------------
// rebaseOlympMakerNote — exercising the IFD walk path
// ---------------------------------------------------------------------------

// buildFinalTIFFWithOLYMPMNAndOOL builds a synthetic LE finalTIFF that contains:
//   - IFD0 with ExifIFD pointer → ExifIFD
//   - ExifIFD with MakerNote OOL blob
//   - MakerNote blob: OLYMP header + IFD with one OOL entry
//
// Returns (finalTIFF, mnAbsOff, oolValOff in finalTIFF).
func buildFinalTIFFWithOLYMPMNAndOOL(t *testing.T, oolAbsOff uint32) ([]byte, uint32, int) {
	t.Helper()

	order := binary.LittleEndian

	// MakerNote blob layout:
	//   [0:6]   "OLYMP\x00" header prefix
	//   [6:8]   version = 0x01 0x00
	//   [8:10]  IFD count = 1
	//   [10:22] IFD entry: tag=0x0001, TypeByte, count=8, val_or_off=oolAbsOff (OOL)
	mnBlobData := make([]byte, 22)
	copy(mnBlobData[0:], []byte{'O', 'L', 'Y', 'M', 'P', 0x00})
	mnBlobData[6] = 0x01 // version
	mnBlobData[7] = 0x00
	order.PutUint16(mnBlobData[8:], 1)          // 1 IFD entry
	order.PutUint16(mnBlobData[10:], 0x0001)    // tag
	order.PutUint16(mnBlobData[12:], 1)         // TypeByte
	order.PutUint32(mnBlobData[14:], 8)         // count=8 → OOL
	order.PutUint32(mnBlobData[18:], oolAbsOff) // val_or_off

	const (
		ifd0Off   = 8
		exifStart = 26
		mnStart   = 44
	)

	mnSize := len(mnBlobData)
	totalSize := mnStart + mnSize
	buf := make([]byte, totalSize)

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0 with ExifIFD pointer.
	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], uint16(exif.TagExifIFDPointer))
	order.PutUint16(buf[ifd0Off+4:], 4)
	order.PutUint32(buf[ifd0Off+6:], 1)
	order.PutUint32(buf[ifd0Off+10:], exifStart)

	// ExifIFD with MakerNote.
	order.PutUint16(buf[exifStart:], 1)
	order.PutUint16(buf[exifStart+2:], uint16(exif.TagMakerNote))
	order.PutUint16(buf[exifStart+4:], 7)              // TypeUndefined
	order.PutUint32(buf[exifStart+6:], uint32(mnSize)) //nolint:gosec // test helper
	order.PutUint32(buf[exifStart+10:], mnStart)

	// MakerNote blob.
	copy(buf[mnStart:], mnBlobData)

	// OOL val_or_off field is at mnStart + 18.
	oolValOff := mnStart + 18
	return buf, uint32(mnStart), oolValOff
}

// TestRebaseOlympMakerNote_OOLEntryRebased verifies that rebaseOlympMakerNote
// rebases an OOL entry in the OLYMP MakerNote IFD when the blob moves.
//
// OLYMP-type MakerNote OOL offsets are TIFF-file-absolute (ExifTool Olympus.pm).
func TestRebaseOlympMakerNote_OOLEntryRebased(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// Set oolAbsOff to mnStart+18+something to make it within the blob.
	// mnStart = 44, mnBlobSize = 22, so blob range = [44, 66).
	// OOL entry at finalTIFF offset 44+18 = 62. That's within blob range.
	// So: oldVOO = 62, info.mnSrcOffset = 20 (pretend it was at 20 before).
	// relOff = 62 - 20 = 42. newVOO = 44 + 42 = 86.
	// But wait — the blob end = 44+22 = 66, and OOL = 62 < 66 → "in blob" case.
	// rebaseOlympMNEntry Case 2: uint64(oldVOO) >= uint64(info.mnSrcOffset) && < mnBlobEnd
	//   62 >= 20 AND 62 < (20+22)=42? No: 62 >= 42 is false.
	// Hmm. Let us pick different values so OOL is in-blob for the ORIGINAL position.
	// info.mnSrcOffset = 20, mnBlobSize = 22, blob range = [20, 42).
	// oolAbsOff must be in [20, 42) — but it will be stored in finalTIFF at position 44+18=62.
	// That is different from info.mnSrcOffset. The key is that oolAbsOff (the VALUE
	// in the entry) must be in [info.mnSrcOffset, info.mnSrcOffset+info.mnBlobSize).
	// So set oolAbsOff = 25 (within [20, 42)).
	const (
		fakeSrcOff = uint32(20) // blob "was" at 20
		oolAbsOff  = uint32(25) // within [20, 42) = "in-blob" original range
	)

	finalTIFF, mnAbsOff, oolValOff := buildFinalTIFFWithOLYMPMNAndOOL(t, oolAbsOff)

	mnBlobSize := 22
	info := &olympMakerNoteInfo{
		mnEntry: &exif.IFDEntry{
			Tag:   exif.TagMakerNote,
			Value: finalTIFF[mnAbsOff:],
		},
		mnSrcOffset: fakeSrcOff,
		mnBlobSize:  uint32(mnBlobSize),
		order:       order,
	}

	rebaseOlympMakerNote(finalTIFF, info, nil, order)

	// Case 2: in-blob rebase.
	// newVOO = uint32(newMNAbs) + (oldVOO - info.mnSrcOffset) = 44 + (25 - 20) = 49.
	gotVOO := order.Uint32(finalTIFF[oolValOff:])
	wantVOO := mnAbsOff + (oolAbsOff - fakeSrcOff) // 44 + 5 = 49
	if gotVOO != wantVOO {
		t.Errorf("rebaseOlympMakerNote in-blob: got val_or_off 0x%X, want 0x%X", gotVOO, wantVOO)
	}
}

// TestRebaseOlympMakerNote_SameOffset verifies that when the blob did not move
// (info.mnSrcOffset == new position) the function is a no-op.
func TestRebaseOlympMakerNote_SameOffset(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const oolAbsOff = uint32(48)

	finalTIFF, mnAbsOff, oolValOff := buildFinalTIFFWithOLYMPMNAndOOL(t, oolAbsOff)

	info := &olympMakerNoteInfo{
		mnEntry: &exif.IFDEntry{
			Tag:   exif.TagMakerNote,
			Value: finalTIFF[mnAbsOff:],
		},
		mnSrcOffset: mnAbsOff, // SAME → no rebase
		mnBlobSize:  22,
		order:       order,
	}

	rebaseOlympMakerNote(finalTIFF, info, nil, order)

	// val_or_off must be unchanged.
	gotVOO := order.Uint32(finalTIFF[oolValOff:])
	if gotVOO != oolAbsOff {
		t.Errorf("rebaseOlympMakerNote no-rebase: val_or_off changed from 0x%X to 0x%X",
			oolAbsOff, gotVOO)
	}
}

// TestRebaseOlympMakerNote_NilInfo verifies that a nil info is a no-op.
func TestRebaseOlympMakerNote_NilInfo(t *testing.T) {
	t.Parallel()

	finalTIFF := make([]byte, 50)
	// Must not panic.
	rebaseOlympMakerNote(finalTIFF, nil, nil, binary.LittleEndian)
}

// TestRebaseOlympMakerNote_NilMNEntry verifies that nil mnEntry is a no-op.
func TestRebaseOlympMakerNote_NilMNEntry(t *testing.T) {
	t.Parallel()

	info := &olympMakerNoteInfo{mnEntry: nil}
	finalTIFF := make([]byte, 50)
	rebaseOlympMakerNote(finalTIFF, info, nil, binary.LittleEndian)
}

// ---------------------------------------------------------------------------
// enumerateSubIFDsAt — cycle guard and bounds checks
// ---------------------------------------------------------------------------

// TestEnumerateSubIFDs_CycleGuard verifies that enumerateSubIFDs stops when
// the same SubIFD offset is visited twice without panicking.
//
// TIFF Extension §F: SubIFDs are limited by maxSubIFDDepth (8) to prevent
// crafted inputs from exhausting the call stack.
func TestEnumerateSubIFDs_CycleGuard(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Build a TIFF where the 0x014A entry's value points back at IFD0 (offset 8).
	// The cycle guard in enumerateSubIFDsAt must prevent infinite recursion.
	//
	// Layout:
	//   [0:8]   header
	//   [8:10]  IFD0 count = 1
	//   [10:22] IFD0 entry: tag=0x014A, TypeLong, Count=1, val_or_off=valueAreaOff
	//   [22:26] nextIFD = 0
	//   [26:30] value area: uint32 = 8 (points to IFD0 itself → cycle)
	const valueAreaOff = 26
	buf := make([]byte, 30)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)                        // IFD0: 1 entry
	order.PutUint16(buf[10:], uint16(exif.TagSubIFDs)) // tag 0x014A
	order.PutUint16(buf[12:], uint16(exif.TypeLong))   // TypeLong
	order.PutUint32(buf[14:], 1)                       // Count=1
	order.PutUint32(buf[18:], valueAreaOff)            // OOL → value area
	order.PutUint32(buf[22:], 0)                       // nextIFD = 0
	order.PutUint32(buf[valueAreaOff:], 8)             // SubIFD offset = 8 (self-referential)

	// Build a parsed IFD0 with the SubIFDs entry matching the raw bytes.
	ifd0 := &exif.IFD{}
	subEntry := exif.IFDEntry{
		Tag:   exif.TagSubIFDs,
		Type:  exif.TypeLong,
		Count: 1,
		Value: make([]byte, 4),
	}
	order.PutUint32(subEntry.Value, 8) // SubIFD offset = 8 (self-referential cycle)
	ifd0.Entries = append(ifd0.Entries, subEntry)

	// Must not panic or loop.
	subIFDs, subBlocks, err := enumerateSubIFDs(buf, ifd0, order, newImageBlockBudget())
	// Either returns empty or shallow result; no error expected for a valid struct.
	_ = subIFDs
	_ = subBlocks
	_ = err
}
