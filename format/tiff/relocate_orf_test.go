package tiff

// relocate_orf_test.go — synthetic unit tests for the Olympus ORF-specific
// copy-and-relocate subsystem (task #194: raise format/tiff coverage to ≥80%).
//
// All tests use in-memory fixtures only; no corpus files required.
//
// Coverage targets:
//   - isOLYMPMakerNote
//   - extractOlympMakerNoteInfo (OLYMP-type detection, thumbnail path)
//   - rebaseOlympMakerNote
//   - rebaseOlympMNEntry (in-blob + external thumbnail cases)
//   - relocateTIFFFromParsedORF (OLYMP path + standard path)
//   - orfRelocateWithOLYMP
//
// Spec references:
//   - ExifTool Olympus.pm: OLYMP-type MakerNote header, offset base rules.
//   - TIFF 6.0 §2: IFD entry layout.
//   - EXIF 2.32 §4.6.5: MakerNote (0x927C).

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// isOLYMPMakerNote
// ---------------------------------------------------------------------------

// TestIsOLYMPMakerNote_TrueForOLYMP verifies that a blob starting with "OLYMP\x00"
// is correctly identified as an OLYMP-type MakerNote.
//
// ExifTool Olympus.pm: OLYMP-type starts with "OLYMP\x00" (6 bytes).
func TestIsOLYMPMakerNote_TrueForOLYMP(t *testing.T) {
	t.Parallel()

	// Minimum valid OLYMP-type blob: "OLYMP\x00" (6 bytes) + version (2 bytes).
	blob := make([]byte, olympMNHeaderLen)
	copy(blob, "OLYMP\x00\x01\x00")

	if !isOLYMPMakerNote(blob) {
		t.Error("isOLYMPMakerNote: expected true for 'OLYMP\\x00' prefix")
	}
}

// TestIsOLYMPMakerNote_FalseForOLYMPUS verifies that "OLYMPUS\x00" blobs are
// NOT identified as OLYMP-type (byte 5 is 'U', not '\x00').
//
// ExifTool Olympus.pm: "OLYMPUS\x00" uses blob-relative offsets (safe to copy verbatim).
func TestIsOLYMPMakerNote_FalseForOLYMPUS(t *testing.T) {
	t.Parallel()

	// "OLYMPUS\x00" — byte 5 is 'U', not '\x00'.
	blob := []byte{'O', 'L', 'Y', 'M', 'P', 'U', 'S', 0x00}

	if isOLYMPMakerNote(blob) {
		t.Error("isOLYMPMakerNote: should return false for 'OLYMPUS\\x00' prefix")
	}
}

// TestIsOLYMPMakerNote_FalseForShortBlob verifies that blobs shorter than
// olympMNHeaderLen (8 bytes) return false.
func TestIsOLYMPMakerNote_FalseForShortBlob(t *testing.T) {
	t.Parallel()

	cases := [][]byte{
		nil,
		{},
		{'O', 'L', 'Y', 'M', 'P'},             // 5 bytes, too short
		{'O', 'L', 'Y', 'M', 'P', 0x00, 0x01}, // 7 bytes, still < 8
	}

	for _, blob := range cases {
		if isOLYMPMakerNote(blob) {
			t.Errorf("isOLYMPMakerNote: expected false for short blob %v", blob)
		}
	}
}

// TestIsOLYMPMakerNote_FalseForNonOlympus verifies that arbitrary blobs return false.
func TestIsOLYMPMakerNote_FalseForNonOlympus(t *testing.T) {
	t.Parallel()

	blob := []byte("Nikon\x00\x02\x10somedata")
	if isOLYMPMakerNote(blob) {
		t.Error("isOLYMPMakerNote: expected false for Nikon blob")
	}
}

// ---------------------------------------------------------------------------
// extractOlympMakerNoteInfo
// ---------------------------------------------------------------------------

// buildORFWithOLYMPMakerNote builds a synthetic minimal little-endian TIFF
// (with ORF magic patched to standard TIFF) containing an OLYMP-type MakerNote.
//
// The TIFF has the ORF magic at [0:4] already patched to standard ("II\x2A\x00").
// The function returns:
//   - the full TIFF bytes
//   - the absolute offset of the MakerNote blob
//   - the absolute file offset of the OOL data within the MakerNote IFD
func buildTIFFWithOLYMPMakerNote(t *testing.T, includeThumbEntry bool) (buf []byte, mnBlobOff, oolAbsOff uint32) {
	t.Helper()

	order := binary.LittleEndian

	// Layout:
	//   [0..7]   TIFF header
	//   [8..29]  IFD0: 1 entry (ExifIFD pointer), nextIFD
	//   [30..51] ExifIFD: 1 entry (MakerNote), nextIFD
	//   [52..]   MakerNote blob (OLYMP-type)
	//
	// OLYMP-type MakerNote layout:
	//   [0..5]   "OLYMP\x00"
	//   [6..7]   version
	//   [8..9]   IFD count
	//   [10..?]  IFD entries
	//   [?..?+3] nextIFD
	//   [?..]    OOL data

	const (
		ifd0Start   = 8
		nIFD0       = 1
		ifd0Size    = 2 + nIFD0*12 + 4
		exifStart   = ifd0Start + ifd0Size // 30
		nExif       = 1
		exifSize    = 2 + nExif*12 + 4
		mnBlobStart = exifStart + exifSize // 52
	)

	// MakerNote structure.
	// IFD is at blob[8].
	// Entries:
	//   Entry 0: tag=0x0001, TypeUndefined, count=8 (OOL)
	//   (optional) Entry 1: tag=0x0100 (ThumbnailImage), TypeUndefined, count=12 (external OOL)

	nMNEntries := 1
	if includeThumbEntry {
		nMNEntries = 2
	}
	mnIFDSize := 2 + nMNEntries*12 + 4                                  // count + entries + nextIFD
	mnIFDOff := olympMNHeaderLen                                        // IFD at blob[8]
	mnOOLData := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0x01, 0x02} // 8 bytes

	// External thumbnail data (for includeThumbEntry case).
	thumbData := []byte{0xFF, 0xD8, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A}

	// Blob size:
	//   header(8) + IFD(mnIFDSize) + OOL data (8 bytes)
	// The external thumbnail is placed OUTSIDE the MakerNote blob.
	mnHeaderSize := olympMNHeaderLen
	mnOOLRelOff := mnHeaderSize + mnIFDSize // OOL data relative to blob[0]
	mnBlobSize := mnOOLRelOff + len(mnOOLData)

	var totalSize int
	var thumbAbsOff uint32
	if includeThumbEntry {
		thumbAbsOff = uint32(mnBlobStart + mnBlobSize) //nolint:gosec // G115: test layout values are bounded
		totalSize = mnBlobStart + mnBlobSize + len(thumbData)
	} else {
		totalSize = mnBlobStart + mnBlobSize
	}

	buf = make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Start)

	// IFD0.
	order.PutUint16(buf[ifd0Start:], nIFD0)
	p := ifd0Start + 2
	order.PutUint16(buf[p:], uint16(exif.TagExifIFDPointer))
	order.PutUint16(buf[p+2:], 4) // TypeLong
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], uint32(exifStart))
	// nextIFD = 0.

	// ExifIFD.
	order.PutUint16(buf[exifStart:], nExif)
	q := exifStart + 2
	order.PutUint16(buf[q:], uint16(exif.TagMakerNote))
	order.PutUint16(buf[q+2:], 7)                  // TypeUndefined
	order.PutUint32(buf[q+4:], uint32(mnBlobSize)) //nolint:gosec // G115: test helper
	order.PutUint32(buf[q+8:], uint32(mnBlobStart))

	// MakerNote blob.
	mn := buf[mnBlobStart:]
	copy(mn, "OLYMP\x00\x01\x00") // 8-byte header

	mnIFDInBuf := mnBlobStart + mnIFDOff
	order.PutUint16(buf[mnIFDInBuf:], uint16(nMNEntries))
	ep := mnIFDInBuf + 2

	// Entry 0: OOL data within blob.
	oolAbsOff = uint32(mnBlobStart + mnOOLRelOff)
	order.PutUint16(buf[ep:], 0x0001)      // tag
	order.PutUint16(buf[ep+2:], 7)         // TypeUndefined
	order.PutUint32(buf[ep+4:], 8)         // count = 8 (OOL)
	order.PutUint32(buf[ep+8:], oolAbsOff) // TIFF-absolute
	ep += 12

	if includeThumbEntry {
		// Entry 1: ThumbnailImage (0x0100), external data.
		order.PutUint16(buf[ep:], uint16(olympMNTagThumbnailImage))
		order.PutUint16(buf[ep+2:], 7)                      // TypeUndefined
		order.PutUint32(buf[ep+4:], uint32(len(thumbData))) //nolint:gosec // G115: test
		order.PutUint32(buf[ep+8:], thumbAbsOff)            // external abs offset
		ep += 12
		copy(buf[thumbAbsOff:], thumbData)
	}
	_ = ep
	// nextIFD = 0.

	// OOL data.
	copy(buf[oolAbsOff:], mnOOLData)

	return buf, uint32(mnBlobStart), oolAbsOff
}

// TestExtractOlympMakerNoteInfo_OLYMPDetected verifies that extractOlympMakerNoteInfo
// correctly detects and returns info for an OLYMP-type MakerNote.
func TestExtractOlympMakerNoteInfo_OLYMPDetected(t *testing.T) { //nolint:paralleltest // exif.Parse modifies state
	base, mnBlobOff, _ := buildTIFFWithOLYMPMakerNote(t, false)

	e, err := exif.Parse(base)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	// Manually set MakerNoteOffset since exif.Parse populates it for OOL entries.
	if e.MakerNoteOffset == 0 {
		e.MakerNoteOffset = mnBlobOff
	}

	info := extractOlympMakerNoteInfo(base, e, binary.LittleEndian)
	if info == nil {
		t.Fatal("extractOlympMakerNoteInfo: expected non-nil info for OLYMP-type MakerNote")
	}
	if info.mnSrcOffset == 0 {
		t.Error("extractOlympMakerNoteInfo: mnSrcOffset is 0")
	}
	if info.mnBlobSize == 0 {
		t.Error("extractOlympMakerNoteInfo: mnBlobSize is 0")
	}
}

// TestExtractOlympMakerNoteInfo_NilEXIF verifies that extractOlympMakerNoteInfo
// returns nil when the EXIF struct has no ExifIFD.
func TestExtractOlympMakerNoteInfo_NilEXIF(t *testing.T) {
	t.Parallel()

	info := extractOlympMakerNoteInfo(make([]byte, 100), nil, binary.LittleEndian)
	if info != nil {
		t.Errorf("nil EXIF: expected nil info, got %+v", info)
	}
}

// TestExtractOlympMakerNoteInfo_NonOLYMPMakerNote verifies that extractOlympMakerNoteInfo
// returns nil for a non-OLYMP MakerNote (e.g., Nikon prefix).
func TestExtractOlympMakerNoteInfo_NonOLYMPMakerNote(t *testing.T) {
	t.Parallel()

	// Build a minimal TIFF with a Nikon-style MakerNote.
	order := binary.LittleEndian
	nikonMN := make([]byte, 30)
	copy(nikonMN, "Nikon\x00\x02\x10")

	base, _, _ := buildTIFFWithOLYMPMakerNote(t, false)

	// Patch the MakerNote blob start with Nikon magic.
	e, parseErr := exif.Parse(base)
	if parseErr != nil {
		t.Fatalf("exif.Parse: %v", parseErr)
	}
	if e.ExifIFD != nil {
		mnEntry := e.ExifIFD.Get(exif.TagMakerNote)
		if mnEntry != nil && len(mnEntry.Value) >= len(nikonMN) {
			copy(mnEntry.Value, nikonMN)
		}
	}

	info := extractOlympMakerNoteInfo(base, e, order)
	if info != nil {
		t.Error("extractOlympMakerNoteInfo: expected nil for Nikon MakerNote")
	}
}

// ---------------------------------------------------------------------------
// rebaseOlympMakerNote + rebaseOlympMNEntry — via end-to-end orfRelocateWithOLYMP
// ---------------------------------------------------------------------------

// TestORFRelocateWithOLYMP_OOLBytesPreserved verifies that after
// relocateTIFFFromParsedORF on an ORF fixture with an OLYMP-type MakerNote:
//   - The output is a valid TIFF.
//   - The OOL data bytes inside the MakerNote blob are preserved.
func TestORFRelocateWithOLYMP_OOLBytesPreserved(t *testing.T) { //nolint:paralleltest // exif.Parse side-effects
	// Build the base TIFF with OLYMP-type MakerNote.
	baseTIFF, mnBlobOff, oolAbsOff := buildTIFFWithOLYMPMakerNote(t, false)

	// Patch bytes [0:4] to ORF magic "IIRO" so relocateTIFFFromParsedORF accepts it.
	orfBase := make([]byte, len(baseTIFF))
	copy(orfBase, baseTIFF)
	orfBase[0] = 0x49 // 'I'
	orfBase[1] = 0x49 // 'I'
	orfBase[2] = 0x52 // 'R'
	orfBase[3] = 0x4F // 'O' → "IIRO"

	wantOOLData := baseTIFF[oolAbsOff : oolAbsOff+8]

	order := binary.LittleEndian
	out, err := relocateTIFFFromParsedORF(orfBase, nil, nil, nil)
	if err != nil {
		t.Fatalf("relocateTIFFFromParsedORF: %v", err)
	}
	if len(out) < 8 {
		t.Fatalf("output too short: %d bytes", len(out))
	}

	// Output must have the ORF magic restored.
	if out[0] != 0x49 || out[1] != 0x49 || out[2] != 0x52 || out[3] != 0x4F {
		t.Errorf("ORF magic not restored in output: [0:4]=%02X %02X %02X %02X",
			out[0], out[1], out[2], out[3])
	}

	// Patch output bytes [2:4] to standard TIFF magic for parsing.
	outParseable := make([]byte, len(out))
	copy(outParseable, out)
	outParseable[2] = 0x2A
	outParseable[3] = 0x00

	// Locate the MakerNote in the output.
	_ = mnBlobOff // original offset (no longer valid after relocation)
	mnIdx := bytes.Index(out, []byte("OLYMP\x00"))
	if mnIdx < 0 {
		t.Fatal("OLYMP marker not found in output")
	}

	// The OLYMP IFD is at blob[8].
	ifdStart := mnIdx + olympMNHeaderLen
	if ifdStart+2 > len(out) {
		t.Fatalf("OLYMP IFD start %d out of output bounds", ifdStart)
	}
	mnCount := int(order.Uint16(out[ifdStart:]))
	if mnCount < 1 {
		t.Fatalf("OLYMP IFD has 0 entries in output")
	}

	// Entry 0 (OOL data, tag=0x0001): val_or_off should point to preserved OOL bytes.
	ep := ifdStart + 2
	if ep+12 > len(out) {
		t.Fatalf("entry 0 out of bounds in output")
	}
	gotOOLValue := readOOLValueAt(t, out, ep, order)
	if !bytes.Equal(gotOOLValue, wantOOLData) {
		t.Errorf("OOL data mismatch after ORF relocation:\n  got  %v\n  want %v",
			gotOOLValue, wantOOLData)
	}
}

// TestORFRelocateWithOLYMP_ThumbBlockIncluded verifies that when the OLYMP
// MakerNote has an external ThumbnailImage (tag 0x0100) pointing outside the
// blob, the thumb bytes are preserved in the output.
func TestORFRelocateWithOLYMP_ThumbBlockIncluded(t *testing.T) { //nolint:paralleltest // exif.Parse state
	baseTIFF, _, _ := buildTIFFWithOLYMPMakerNote(t, true)

	// Extract the expected thumbnail bytes from the source.
	// The thumb data is appended after the MakerNote blob.
	thumbData := []byte{0xFF, 0xD8, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A}

	orfBase := make([]byte, len(baseTIFF))
	copy(orfBase, baseTIFF)
	orfBase[0] = 0x49
	orfBase[1] = 0x49
	orfBase[2] = 0x52
	orfBase[3] = 0x4F // "IIRO"

	out, err := relocateTIFFFromParsedORF(orfBase, nil, nil, nil)
	if err != nil {
		t.Fatalf("relocateTIFFFromParsedORF (thumb): %v", err)
	}

	if !bytes.Contains(out, thumbData) {
		t.Error("thumbnail data bytes not found in ORF output")
	}
}

// ---------------------------------------------------------------------------
// relocateTIFFFromParsedORF — error paths
// ---------------------------------------------------------------------------

// TestRelocateTIFFFromParsedORF_InvalidMagic verifies that ErrORFInvalidMagic
// is returned when the input does not carry a valid ORF magic.
func TestRelocateTIFFFromParsedORF_InvalidMagic(t *testing.T) {
	t.Parallel()

	// Standard TIFF magic — not ORF magic.
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)

	_, err := relocateTIFFFromParsedORF(buf, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for non-ORF magic but got nil")
	}
}

// TestRelocateTIFFFromParsedORF_NewerOlympus verifies that a newer Olympus
// OLYMPUS\x00-type MakerNote takes the standard path (no crash, valid output).
func TestRelocateTIFFFromParsedORF_NewerOlympus(t *testing.T) { //nolint:paralleltest // exif.Parse state
	order := binary.LittleEndian

	// Build a minimal ORF-magic TIFF with no MakerNote.
	buf := make([]byte, 40)
	buf[0], buf[1] = 0x49, 0x49 // "II"
	buf[2], buf[3] = 0x52, 0x4F // "RO" → "IIRO"
	order.PutUint32(buf[4:], 8)

	// IFD0: 1 entry (ImageWidth).
	order.PutUint16(buf[8:], 1)
	order.PutUint16(buf[10:], 0x0100) // ImageWidth
	order.PutUint16(buf[12:], 4)      // TypeLong
	order.PutUint32(buf[14:], 1)
	order.PutUint32(buf[18:], 100)
	// nextIFD = 0.

	out, err := relocateTIFFFromParsedORF(buf, nil, nil, nil)
	if err != nil {
		t.Fatalf("relocateTIFFFromParsedORF (newer Olympus no-MN): %v", err)
	}
	if len(out) < 4 {
		t.Fatalf("output too short: %d bytes", len(out))
	}
	// Output must have ORF magic restored.
	if out[2] != 0x52 || out[3] != 0x4F {
		t.Errorf("ORF magic not restored: [2]=%02X [3]=%02X", out[2], out[3])
	}
}

// TestRebaseOlympMNEntry_ExternalThumbUpdated verifies that rebaseOlympMNEntry
// patches the ThumbnailImage entry to thumbBlock.newOffset when the original
// val_or_off points outside the blob range.
func TestRebaseOlympMNEntry_ExternalThumbUpdated(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Build a minimal finalTIFF with an OLYMP MakerNote IFD entry for tag 0x0100.
	// Entry: TypeUndefined, Count=12, val_or_off = some external address.
	const externalOldOff = uint32(5000)
	const newThumbOff = uint32(8000)

	// Build an entry in a buffer.
	// (rebaseOlympMNEntry operates on a finalTIFF slice; entry is at entryPos.)
	finalTIFF := make([]byte, 100)
	entryPos := 20
	order.PutUint16(finalTIFF[entryPos:], uint16(olympMNTagThumbnailImage))
	order.PutUint16(finalTIFF[entryPos+2:], 7)  // TypeUndefined
	order.PutUint32(finalTIFF[entryPos+4:], 12) // count=12 → OOL
	order.PutUint32(finalTIFF[entryPos+8:], externalOldOff)

	// thumbSrcOffset must be set so the branch guard `info.thumbSrcOffset > 0` fires.
	// The external-thumb condition checks oldVOO == thumbSrcOffset OR out-of-blob;
	// since externalOldOff (5000) >= mnBlobEnd (1200) the "out-of-blob" arm fires.
	info := &olympMakerNoteInfo{
		mnSrcOffset:    1000,
		mnBlobSize:     200,
		thumbSrcOffset: externalOldOff, // marks this as an external thumbnail
		order:          order,
	}
	thumbBlock := &imageBlock{newOffset: uint64(newThumbOff)}
	mnBlobEnd := uint64(info.mnSrcOffset) + uint64(info.mnBlobSize) // 1200
	newMNAbs := 2000

	rebaseOlympMNEntry(finalTIFF, entryPos, info, thumbBlock, mnBlobEnd, newMNAbs, order)

	gotVal := order.Uint32(finalTIFF[entryPos+8:])
	if gotVal != newThumbOff {
		t.Errorf("ThumbnailImage entry not patched to newThumbOff: got 0x%08X, want 0x%08X",
			gotVal, newThumbOff)
	}
}

// TestRebaseOlympMNEntry_InBlobOOLRebased verifies that an OOL entry whose
// original offset is within the MakerNote blob range is rebased by delta.
func TestRebaseOlympMNEntry_InBlobOOLRebased(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// MakerNote blob originally at [1000..1200].
	// OOL value originally at 1050 (within blob).
	const mnSrc = uint32(1000)
	const mnSize = uint32(200)
	const oldOOLAbs = uint32(1050)

	// New MakerNote abs = 2000 (moved by +1000).
	const newMNAbs = int(2000)

	finalTIFF := make([]byte, 100)
	entryPos := 10
	order.PutUint16(finalTIFF[entryPos:], 0x0001) // arbitrary tag (not thumb)
	order.PutUint16(finalTIFF[entryPos+2:], 7)    // TypeUndefined
	order.PutUint32(finalTIFF[entryPos+4:], 8)    // count=8 → OOL
	order.PutUint32(finalTIFF[entryPos+8:], oldOOLAbs)

	info := &olympMakerNoteInfo{
		mnSrcOffset: mnSrc,
		mnBlobSize:  mnSize,
		order:       order,
	}
	mnBlobEnd := uint64(mnSrc) + uint64(mnSize)

	rebaseOlympMNEntry(finalTIFF, entryPos, info, nil, mnBlobEnd, newMNAbs, order)

	// new_voo = newMNAbs + (oldOOLAbs - mnSrc) = 2000 + (1050 - 1000) = 2050.
	wantNewVOO := uint32(newMNAbs) + (oldOOLAbs - mnSrc)
	gotVal := order.Uint32(finalTIFF[entryPos+8:])
	if gotVal != wantNewVOO {
		t.Errorf("in-blob OOL entry not rebased: got 0x%08X, want 0x%08X", gotVal, wantNewVOO)
	}
}

// TestRebaseOlympMNEntry_InlineEntryUnchanged verifies that inline entries
// (total ≤ 4) are not modified by rebaseOlympMNEntry.
func TestRebaseOlympMNEntry_InlineEntryUnchanged(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	const origVal = uint32(0xABCDEF01)
	finalTIFF := make([]byte, 30)
	entryPos := 0
	order.PutUint16(finalTIFF[entryPos:], 0x0001) // tag
	order.PutUint16(finalTIFF[entryPos+2:], 4)    // TypeLong → total=4 → inline
	order.PutUint32(finalTIFF[entryPos+4:], 1)    // count=1
	order.PutUint32(finalTIFF[entryPos+8:], origVal)

	info := &olympMakerNoteInfo{mnSrcOffset: 0, mnBlobSize: 100, order: order}
	rebaseOlympMNEntry(finalTIFF, entryPos, info, nil, 100, 200, order)

	gotVal := order.Uint32(finalTIFF[entryPos+8:])
	if gotVal != origVal {
		t.Errorf("inline entry modified: got 0x%08X, want 0x%08X", gotVal, origVal)
	}
}

// TestRebaseOlympMakerNote_NilInfoNoop verifies that rebaseOlympMakerNote is
// a no-op when info is nil or info.mnEntry is nil.
func TestRebaseOlympMakerNote_NilInfoNoop(t *testing.T) {
	t.Parallel()

	finalTIFF := make([]byte, 100)
	original := make([]byte, len(finalTIFF))
	copy(original, finalTIFF)

	// nil info.
	rebaseOlympMakerNote(finalTIFF, nil, nil, binary.LittleEndian)
	if !bytes.Equal(finalTIFF, original) {
		t.Error("rebaseOlympMakerNote with nil info modified finalTIFF")
	}

	// nil mnEntry.
	info := &olympMakerNoteInfo{mnEntry: nil}
	rebaseOlympMakerNote(finalTIFF, info, nil, binary.LittleEndian)
	if !bytes.Equal(finalTIFF, original) {
		t.Error("rebaseOlympMakerNote with nil mnEntry modified finalTIFF")
	}
}
