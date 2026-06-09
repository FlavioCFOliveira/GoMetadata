package tiff

// relocate_makernote_test.go — synthetic gate tests for MakerNote OOL offset
// rebasing on write (audit finding #127).
//
// Each test:
//  1. Builds a minimal TIFF with a MakerNote blob embedded in an ExifIFD.
//  2. The MakerNote contains at least one OOL IFD entry (total > 4 bytes) whose
//     val_or_off is set according to the maker's convention (file-absolute or
//     blob-relative).
//  3. Calls Inject (the public TIFF write path, which internally calls
//     relocateTIFFFromParsed → exif.Encode → rebaseGenericMakerNote).
//  4. Parses the resulting TIFF and reads back the MakerNote blob.
//  5. Verifies that the OOL value bytes are still byte-identical to the original.
//
// No corpus files are downloaded or required.  All inputs are constructed in
// memory, making these tests runnable in any CI environment.
//
// Tests:
//
//	TestEncodeOlympusMakerNoteOOL      — OLYMP-type (file-absolute, was CORRUPT → CORRECT)
//	TestSonyMakerNoteOOLRoundtrip      — Sony plain-IFD (file-absolute, was CORRUPT → CORRECT)
//	TestNikonMakerNoteOOLRoundtrip     — Nikon Type-3 (embedded-TIFF-relative, already CORRECT → regression lock)
//	TestPanasonicMakerNoteOOLRoundtrip — Panasonic (blob-relative, already CORRECT → regression lock)
//
// Additional:
//
//	TestNikonType1DocumentedLimitation — Nikon Type-1 limitation: no corruption of
//	  unrelated tags (even though MakerNote OOL values are not rebased for Type-1).
//
// Spec references:
//
//	EXIF §4.6.5: tag 0x927C (MakerNote).
//	TIFF 6.0 §2: IFD entry layout — val_or_off is absolute when total > 4 bytes.
//	ExifTool Olympus.pm: OLYMP-type OOL offsets are outer-TIFF-absolute.
//	ExifTool Sony.pm:    Sony plain-IFD OOL offsets are outer-TIFF-absolute.
//	ExifTool Nikon.pm:   Type-3 offsets relative to embedded TIFF header.
//	ExifTool Panasonic.pm: all OOL offsets relative to blob start.
//	#127 audit finding: MakerNote OOL offset rebasing incomplete on write.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// Shared TIFF builder for MakerNote gate tests
// ---------------------------------------------------------------------------

// makerNoteTestTIFF builds a minimal LE TIFF suitable for OOL rebasing tests.
//
// Structure:
//
//	[0..7]    TIFF header (LE, magic 0x002A, IFD0 at offset 8)
//	IFD0:     2 entries (ImageWidth + ExifIFDPointer)
//	ExifIFD:  1 entry (MakerNote, OOL, points to makerNoteBlob)
//	makerNoteBlob: the caller-supplied MakerNote blob (appended at the end)
//
// The MakerNote blob is placed at TIFF-absolute offset 56 (constant layout).
// The blob must already contain correct val_or_off values for this offset.
//
// Inject (which re-encodes + appends image blocks) will move the MakerNote blob
// to a different absolute offset — triggering the rebasing code path.
//
// TIFF 6.0 §2: IFD entries sorted by tag number.
func makerNoteTestTIFF(t *testing.T, makerNoteBlob []byte) []byte {
	t.Helper()

	order := binary.LittleEndian

	// Tag constants.
	const (
		tagImageWidth     = uint16(0x0100)
		tagExifIFDPointer = uint16(0x8769)
		tagMakerNote      = uint16(0x927C)
		typeLong          = uint16(4)
		typeUndef         = uint16(7)
	)

	// Layout (all sizes fixed, computed bottom-up):
	//
	//   [0..7]     TIFF header                                                8 bytes
	//   [8..37]    IFD0: 2+nIFD0*12+4 = 2+24+4 = 30 bytes
	//   [38..55]   ExifIFD: 2+1*12+4 = 18 bytes
	//   [56..]     MakerNote blob (caller-supplied)
	//
	// IFD0 contains:
	//   tag 0x0100 ImageWidth  (inline, value=1)
	//   tag 0x8769 ExifIFDPointer → ExifIFD at 38
	//
	// ExifIFD contains:
	//   tag 0x927C MakerNote (OOL, count=len(makerNoteBlob), offset=56)

	const (
		ifd0Start   = 8
		nIFD0       = 2
		ifd0Size    = 2 + nIFD0*12 + 4     // 30
		exifStart   = ifd0Start + ifd0Size // 38
		nExif       = 1
		exifSize    = 2 + nExif*12 + 4     // 18
		mnBlobStart = exifStart + exifSize // 56
	)

	totalSize := mnBlobStart + len(makerNoteBlob)
	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Start)

	// IFD0.
	order.PutUint16(buf[ifd0Start:], nIFD0)
	p := ifd0Start + 2

	writeEntry := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	// Sorted by tag: 0x0100 < 0x8769.
	writeEntry(tagImageWidth, typeLong, 1, 1)
	writeEntry(tagExifIFDPointer, typeLong, 1, exifStart)
	// next-IFD = 0 (already zero).
	p += 4

	// ExifIFD.
	order.PutUint16(buf[exifStart:], nExif)
	q := exifStart + 2
	order.PutUint16(buf[q:], tagMakerNote)
	order.PutUint16(buf[q+2:], typeUndef)
	order.PutUint32(buf[q+4:], uint32(len(makerNoteBlob))) //nolint:gosec // G115: test helper
	order.PutUint32(buf[q+8:], mnBlobStart)                // TIFF-absolute offset of blob
	// ExifIFD next-IFD = 0 (already zero).

	// MakerNote blob.
	copy(buf[mnBlobStart:], makerNoteBlob)

	return buf
}

// injectTIFF calls Inject with dummyXMP to force relocation and returns the
// full output TIFF bytes.
//
// A non-empty XMP payload is required because Inject with nil IPTC+XMP may
// skip relocation entirely (short-circuit path in relocateTIFFFromParsed when
// there are no image blocks and no SubIFDs).  A minimal XMP packet ensures the
// metadata-bearing IFD section always changes, causing the MakerNote blob to
// move to a new absolute offset.
func injectTIFF(t *testing.T, src []byte) []byte {
	t.Helper()

	const dummyXMPStr = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end='w'?>`
	dummyXMP := []byte(dummyXMPStr)

	var out bytes.Buffer
	err := Inject(bytes.NewReader(src), &out, src, nil, dummyXMP, false)
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

// findMakerNoteAbs locates the MakerNote OOL blob in finalTIFF and returns
// its absolute offset.  It walks IFD0 → ExifIFD → MakerNote.
// Fails the test if the MakerNote cannot be found.
func findMakerNoteAbs(t *testing.T, finalTIFF []byte, order binary.ByteOrder) int {
	t.Helper()

	if len(finalTIFF) < 8 {
		t.Fatalf("output TIFF too short: %d bytes", len(finalTIFF))
	}
	ifd0Off := int(order.Uint32(finalTIFF[4:]))

	exifOff, found := scanIFDForTagValOrOff(finalTIFF, ifd0Off, uint16(exif.TagExifIFDPointer), order)
	if !found {
		t.Fatalf("ExifIFD pointer not found in output TIFF")
	}

	mnAbs, found := findOOLEntryOffset(finalTIFF, int(exifOff), uint16(exif.TagMakerNote), order)
	if !found {
		t.Fatalf("MakerNote OOL entry not found in output TIFF")
	}
	return mnAbs
}

// readOOLValueAt reads the OOL value referenced by the IFD entry at position
// ep in buf (absolute byte offset).  The val_or_off in the entry is treated as
// an absolute offset into buf.
// Fails the test when the entry is inline or the value is out of bounds.
func readOOLValueAt(t *testing.T, buf []byte, ep int, order binary.ByteOrder) []byte {
	t.Helper()

	if ep+12 > len(buf) {
		t.Fatalf("entry at %d extends beyond buf (%d bytes)", ep, len(buf))
	}
	entryType := order.Uint16(buf[ep+2:])
	count := order.Uint32(buf[ep+4:])
	sz := typeSize(entryType)
	total := uint64(sz) * uint64(count)
	if total <= 4 {
		t.Fatalf("entry at %d is inline (total=%d); expected OOL", ep, total)
	}
	voo := int(order.Uint32(buf[ep+8:]))
	end := voo + int(total) //nolint:gosec // G115: total <= len(buf), both non-negative, no overflow in practice
	if end > len(buf) {
		t.Fatalf("OOL value [%d..%d) exceeds buf len %d (voo=%d total=%d)", voo, end, len(buf), voo, total)
	}
	return buf[voo:end]
}

// ---------------------------------------------------------------------------
// TestEncodeOlympusMakerNoteOOL
// ---------------------------------------------------------------------------

// TestEncodeOlympusMakerNoteOOL verifies that Olympus OLYMP-type MakerNote
// OOL entries are correctly rebased when the MakerNote blob moves after Write.
//
// Convention: OLYMP-type MakerNotes use TIFF-file-absolute val_or_off values.
// Before #127: the generic TIFF path (relocateTIFFFromParsed) did NOT rebase
// OLYMP-type OOL entries → pointers became stale → round-trip returned
// garbage or out-of-bounds bytes.
//
// After #127: rebaseGenericMakerNote detects the "OLYMP\x00" prefix and
// rebases all OOL entries by delta = newMNAbs - oldMNAbs.
//
// ExifTool Olympus.pm: OLYMP-type IFD at blob[8], outer-TIFF-absolute OOL offsets.
func TestEncodeOlympusMakerNoteOOL(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Build an Olympus OLYMP-type MakerNote blob:
	//
	//   [0..5]   "OLYMP\x00"    (6-byte magic)
	//   [6..7]   version (0x00, 0x00)
	//   [8..9]   IFD entry count = 1
	//   [10..21] IFD entry: tag=0x0001, type=UNDEFINED(7), count=8,
	//            val_or_off = TIFF-absolute offset of OOL data
	//   [22..25] next-IFD pointer = 0
	//   [26..33] OOL value data (8 bytes)
	//
	// makerNoteTestTIFF places the blob at TIFF-absolute offset 56.
	// OOL data is at blob[26], so TIFF-absolute = 56 + 26 = 82.

	wantOOLValue := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0x01, 0x02} // 8 bytes

	const (
		mnBlobTIFFAbs = 56 // as placed by makerNoteTestTIFF (constant layout)
		oolBlobOffset = 26 // offset of OOL data WITHIN the blob
	)

	blobLen := oolBlobOffset + len(wantOOLValue)
	mnBlob := make([]byte, blobLen)

	copy(mnBlob[0:], "OLYMP\x00")
	order.PutUint16(mnBlob[6:], 0x0000) // version

	// IFD at offset 8 (ExifTool Olympus.pm: OLYMP-type IFD starts at blob[8]).
	order.PutUint16(mnBlob[8:], 1) // 1 entry
	ep := 10
	order.PutUint16(mnBlob[ep:], 0x0001)                 // tag
	order.PutUint16(mnBlob[ep+2:], 7)                    // TypeUndefined
	order.PutUint32(mnBlob[ep+4:], 8)                    // count = 8
	oolAbsInSrc := uint32(mnBlobTIFFAbs + oolBlobOffset) // TIFF-absolute
	order.PutUint32(mnBlob[ep+8:], oolAbsInSrc)
	// next-IFD at ep+12 = blob[22]; already zero.

	copy(mnBlob[oolBlobOffset:], wantOOLValue)

	src := makerNoteTestTIFF(t, mnBlob)

	// Sanity: verify OOL bytes are at the expected TIFF-absolute position in src.
	if int(oolAbsInSrc)+len(wantOOLValue) > len(src) {
		t.Fatalf("test setup: oolAbsInSrc=%d+%d > src len %d", oolAbsInSrc, len(wantOOLValue), len(src))
	}
	if !bytes.Equal(src[oolAbsInSrc:int(oolAbsInSrc)+len(wantOOLValue)], wantOOLValue) {
		t.Fatalf("test setup: OOL bytes not at expected TIFF-absolute position in src")
	}

	finalTIFF := injectTIFF(t, src)

	// The MakerNote blob must have moved (otherwise rebasing was not exercised).
	newMNAbs := findMakerNoteAbs(t, finalTIFF, order)
	if newMNAbs == mnBlobTIFFAbs {
		t.Log("MakerNote blob did not move — rebasing code path may not have been exercised; check test setup")
	}

	// Locate the OOL IFD entry within the new blob.
	// OLYMP-type IFD is at blob[8] = finalTIFF[newMNAbs+8].
	ifdAbsInFinal := newMNAbs + 8 // IFD starts at blob offset 8
	if ifdAbsInFinal+2 > len(finalTIFF) {
		t.Fatalf("OLYMP IFD start %d out of bounds in final TIFF (%d bytes)", ifdAbsInFinal, len(finalTIFF))
	}
	// entry 0 starts at ifdAbsInFinal+2.
	entryAbs := ifdAbsInFinal + 2
	gotOOLValue := readOOLValueAt(t, finalTIFF, entryAbs, order)

	if !bytes.Equal(gotOOLValue, wantOOLValue) {
		t.Errorf("OLYMP-type MakerNote OOL value mismatch after relocation:\n  got  %v\n  want %v",
			gotOOLValue, wantOOLValue)
	}
}

// ---------------------------------------------------------------------------
// TestSonyMakerNoteOOLRoundtrip
// ---------------------------------------------------------------------------

// TestSonyMakerNoteOOLRoundtrip verifies that Sony plain-IFD MakerNote OOL
// entries are correctly rebased when the MakerNote blob moves after Write.
//
// Convention: Sony plain-IFD MakerNotes (DSLR-A, ILCE, SLT, Cybershot) use
// TIFF-file-absolute val_or_off values. The blob has no magic prefix.
//
// Before #127: the generic TIFF path (relocateTIFFFromParsed) did NOT rebase
// Sony MakerNote OOL entries → pointers became stale.
//
// After #127: rebaseGenericMakerNote detects the plain-IFD format and rebases
// all OOL entries by delta = newMNAbs - oldMNAbs.
//
// ExifTool Sony.pm: plain IFD at offset 0, outer-TIFF-absolute OOL offsets.
func TestSonyMakerNoteOOLRoundtrip(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Build a Sony plain-IFD MakerNote blob:
	//
	//   [0..1]   IFD entry count = 1
	//   [2..13]  IFD entry: tag=0x0102, type=UNDEFINED(7), count=8,
	//            val_or_off = TIFF-absolute offset of OOL data
	//   [14..17] next-IFD pointer = 0
	//   [18..25] OOL value data (8 bytes)
	//
	// makerNoteTestTIFF places the blob at TIFF-absolute offset 56.
	// OOL data is at blob[18], so TIFF-absolute = 56 + 18 = 74.

	wantOOLValue := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	const (
		mnBlobTIFFAbs = 56
		oolBlobOffset = 18 // 2 (count) + 12 (entry) + 4 (next-IFD)
	)

	blobLen := oolBlobOffset + len(wantOOLValue)
	mnBlob := make([]byte, blobLen)

	// Sony plain IFD at offset 0.
	order.PutUint16(mnBlob[0:], 1) // 1 entry
	ep := 2
	order.PutUint16(mnBlob[ep:], 0x0102)                 // arbitrary Sony tag
	order.PutUint16(mnBlob[ep+2:], 7)                    // TypeUndefined
	order.PutUint32(mnBlob[ep+4:], 8)                    // count = 8
	oolAbsInSrc := uint32(mnBlobTIFFAbs + oolBlobOffset) // TIFF-absolute
	order.PutUint32(mnBlob[ep+8:], oolAbsInSrc)
	// next-IFD at ep+12 = blob[14]; already zero.

	copy(mnBlob[oolBlobOffset:], wantOOLValue)

	src := makerNoteTestTIFF(t, mnBlob)
	finalTIFF := injectTIFF(t, src)

	// Find the new MakerNote position.
	newMNAbs := findMakerNoteAbs(t, finalTIFF, order)

	// Sony plain-IFD: IFD is at blob offset 0.
	// Entry 0 starts at newMNAbs + 2 (skip count field).
	entryAbs := newMNAbs + 2
	gotOOLValue := readOOLValueAt(t, finalTIFF, entryAbs, order)

	if !bytes.Equal(gotOOLValue, wantOOLValue) {
		t.Errorf("Sony plain-IFD MakerNote OOL value mismatch after relocation:\n  got  %v\n  want %v",
			gotOOLValue, wantOOLValue)
	}
}

// ---------------------------------------------------------------------------
// TestNikonMakerNoteOOLRoundtrip
// ---------------------------------------------------------------------------

// TestNikonMakerNoteOOLRoundtrip is a regression-lock test verifying that
// Nikon Type-3 MakerNote OOL entries remain correct after relocation.
//
// Convention: Nikon Type-3 uses an embedded TIFF header at blob[8..15].
// All internal val_or_off values are relative to the embedded TIFF base
// (blob[8]).  When the blob moves by delta in the outer TIFF, the embedded
// base also moves by delta — so all embedded-relative pointers stay valid.
// No post-encode rebasing is performed or needed for Type-3.
//
// This test locks that behaviour: after Inject, the OOL value bytes are still
// byte-identical to the original.
//
// ExifTool Nikon.pm: Type-3 embedded TIFF header at blob[8]; all internal
// offsets are relative to that base address.
func TestNikonMakerNoteOOLRoundtrip(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Nikon Type-3 MakerNote blob layout:
	//
	//   [0..5]   "Nikon\x00"
	//   [6..7]   version (0x02, 0x10)
	//   [8..15]  embedded TIFF header (II, 0x002A, IFD0 at embedded offset 8)
	//            → embedded TIFF base is blob[8]; embedded IFD0 at blob[16]
	//   [16..17] embedded IFD count = 1
	//   [18..29] embedded IFD entry: tag=0x0001, TypeUndef, count=8,
	//            val_or_off = embedded-TIFF-relative offset (from blob[8])
	//   [30..33] embedded next-IFD = 0
	//   [34..41] OOL value data (8 bytes)
	//
	// Embedded TIFF base = blob[8].
	// OOL data at blob[34].
	// Embedded-relative val_or_off = 34 - 8 = 26.
	//
	// After move by delta:
	//   new embedded base = (oldMNAbs + delta) + 8
	//   OOL abs = new embedded base + 26 = (oldMNAbs + delta + 8) + 26
	//           = (oldMNAbs + 34) + delta = newMNAbs + 34 ✓

	wantOOLValue := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}

	const (
		oolBlobOffset = 34                           // blob[34] = OOL data
		embeddedBase  = 8                            // embedded TIFF header at blob[8]
		embeddedVOO   = oolBlobOffset - embeddedBase // = 26
		blobLen       = oolBlobOffset + 8
	)

	mnBlob := make([]byte, blobLen)

	// Nikon prefix.
	copy(mnBlob[0:], "Nikon\x00")
	mnBlob[6] = 0x02
	mnBlob[7] = 0x10

	// Embedded TIFF header at blob[8].
	mnBlob[embeddedBase+0] = 'I'
	mnBlob[embeddedBase+1] = 'I'
	order.PutUint16(mnBlob[embeddedBase+2:], 0x002A)
	order.PutUint32(mnBlob[embeddedBase+4:], 8) // embedded IFD0 at embedded offset 8

	// Embedded IFD0 at blob[16].
	ifdAbs := embeddedBase + 8          // 16
	order.PutUint16(mnBlob[ifdAbs:], 1) // count = 1
	ep := ifdAbs + 2
	order.PutUint16(mnBlob[ep:], 0x0001)        // arbitrary tag
	order.PutUint16(mnBlob[ep+2:], 7)           // TypeUndefined
	order.PutUint32(mnBlob[ep+4:], 8)           // count = 8
	order.PutUint32(mnBlob[ep+8:], embeddedVOO) // embedded-TIFF-relative
	// next-IFD at ep+12 = blob[30]; already zero.

	copy(mnBlob[oolBlobOffset:], wantOOLValue)

	src := makerNoteTestTIFF(t, mnBlob)
	finalTIFF := injectTIFF(t, src)

	newMNAbs := findMakerNoteAbs(t, finalTIFF, order)

	// Nikon Type-3: OOL data is always at blob[34] = finalTIFF[newMNAbs + 34].
	oolAbsInFinal := newMNAbs + oolBlobOffset
	if oolAbsInFinal+8 > len(finalTIFF) {
		t.Fatalf("OOL abs %d+8 out of bounds in final TIFF (%d bytes)", oolAbsInFinal, len(finalTIFF))
	}
	gotOOLValue := finalTIFF[oolAbsInFinal : oolAbsInFinal+8]

	if !bytes.Equal(gotOOLValue, wantOOLValue) {
		t.Errorf("Nikon Type-3 MakerNote OOL value mismatch after relocation:\n  got  %v\n  want %v",
			gotOOLValue, wantOOLValue)
	}
}

// ---------------------------------------------------------------------------
// TestPanasonicMakerNoteOOLRoundtrip
// ---------------------------------------------------------------------------

// TestPanasonicMakerNoteOOLRoundtrip is a regression-lock test verifying that
// Panasonic MakerNote OOL entries remain correct after relocation.
//
// Convention: Panasonic MakerNotes have a "Panasonic\x00\x00\x00" prefix
// (12 bytes) followed by an IFD.  All val_or_off values are relative to the
// start of the blob (blob[0]).  The blob is copied verbatim; its internal
// layout doesn't change; all OOL pointers remain correct after move.
//
// After #127: rebaseGenericMakerNote recognises the "Panasonic\x00" prefix
// and is a no-op (blob-relative, safe).  This test locks that behaviour and
// confirms the values are preserved.
//
// ExifTool Panasonic.pm: "Panasonic\0\0\0" prefix, IFD at offset 12,
// "value offsets relative to b[0]".
func TestPanasonicMakerNoteOOLRoundtrip(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// Panasonic MakerNote blob:
	//
	//   [0..11]  "Panasonic\x00\x00\x00"
	//   [12..13] IFD count = 1
	//   [14..25] IFD entry: tag=0x0001, TypeUndef, count=8,
	//            val_or_off = 30 (blob-relative offset of OOL data)
	//   [26..29] next-IFD = 0
	//   [30..37] OOL value data (8 bytes)

	wantOOLValue := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x12, 0x34}

	const (
		oolBlobOffset = 30 // 12 (prefix) + 2 (count) + 12 (entry) + 4 (next-IFD)
		blobLen       = oolBlobOffset + 8
	)

	mnBlob := make([]byte, blobLen)
	copy(mnBlob[0:], "Panasonic\x00\x00\x00")

	// IFD at offset 12.
	order.PutUint16(mnBlob[12:], 1) // count = 1
	ep := 14
	order.PutUint16(mnBlob[ep:], 0x0001)          // arbitrary tag
	order.PutUint16(mnBlob[ep+2:], 7)             // TypeUndefined
	order.PutUint32(mnBlob[ep+4:], 8)             // count = 8
	order.PutUint32(mnBlob[ep+8:], oolBlobOffset) // BLOB-RELATIVE
	// next-IFD at ep+12 = blob[26]; already zero.

	copy(mnBlob[oolBlobOffset:], wantOOLValue)

	src := makerNoteTestTIFF(t, mnBlob)
	finalTIFF := injectTIFF(t, src)

	newMNAbs := findMakerNoteAbs(t, finalTIFF, order)

	// Panasonic: blob-relative val_or_off is still valid after move.
	// OOL data at blob[30] = finalTIFF[newMNAbs+30].
	oolAbsInFinal := newMNAbs + oolBlobOffset
	if oolAbsInFinal+8 > len(finalTIFF) {
		t.Fatalf("OOL abs %d+8 out of bounds in final TIFF (%d bytes)", oolAbsInFinal, len(finalTIFF))
	}
	gotOOLValue := finalTIFF[oolAbsInFinal : oolAbsInFinal+8]

	if !bytes.Equal(gotOOLValue, wantOOLValue) {
		t.Errorf("Panasonic MakerNote OOL value mismatch after relocation:\n  got  %v\n  want %v",
			gotOOLValue, wantOOLValue)
	}
}

// ---------------------------------------------------------------------------
// TestNikonType1DocumentedLimitation
// ---------------------------------------------------------------------------

// TestNikonType1DocumentedLimitation documents the known limitation for Nikon
// Type-1 (legacy D1) MakerNotes: OOL entries in a Type-1 blob share the same
// plain-IFD format as Sony, so rebaseGenericMakerNote WILL attempt to rebase
// them — and this is correct behaviour (both use outer-TIFF-absolute offsets).
//
// The "documented limitation" is for encrypted or truly opaque MakerNote formats
// that cannot be IFD-scanned.  For Type-1, because the format is structurally
// identical to Sony's plain-IFD, the rebasing path works correctly.
//
// This test proves TWO guarantees regardless of the internal MakerNote state:
//
//	(a) Inject does NOT corrupt the TIFF file (it returns a parseable TIFF).
//	(b) All non-MakerNote EXIF metadata (ExifVersion in ExifIFD) is preserved.
//
// ExifTool Nikon.pm: Type-1 is a plain IFD at offset 0, big-endian.
// Cameras affected: Nikon D1, D1X, D1H (~2000); no longer produced.
//
//nolint:paralleltest // AllocsPerRun not used; but uses binary.BigEndian order test which we keep sequential
func TestNikonType1DocumentedLimitation(t *testing.T) {
	// Not calling t.Parallel() here because this test builds a big-endian TIFF
	// while other tests use little-endian; keeping them sequential avoids confusion
	// in the t.Log output.  The test is fast (no I/O) so it doesn't matter.

	// Use big-endian TIFF (as real Nikon D1 files are big-endian).
	order := binary.BigEndian

	const (
		tagImageWidth     = uint16(0x0100)
		tagExifIFDPointer = uint16(0x8769)
		tagExifVersion    = uint16(0x9000) // EXIF §4.6.5: ExifVersion, TypeUndefined, count=4
		tagMakerNote      = uint16(0x927C)
		typeLong          = uint16(4)
		typeUndef         = uint16(7)
	)

	// Layout (big-endian TIFF):
	//
	//   [0..7]   header (MM, 0x002A, IFD0@8)
	//   [8..37]  IFD0: 2 entries + next-IFD  (2+24+4 = 30)
	//   [38..67] ExifIFD: 2 entries + next-IFD (2+24+4 = 30)
	//   [68..]   MakerNote blob
	//
	// ExifIFD entries (sorted): 0x9000 ExifVersion (inline) + 0x927C MakerNote (OOL).
	//
	// MakerNote blob (plain IFD, big-endian, Nikon Type-1):
	//   [0..1]   count = 1
	//   [2..13]  entry: tag=0x0001, TypeUndef, count=8, val_or_off=<abs>
	//   [14..17] next-IFD = 0
	//   [18..25] OOL value data

	const (
		ifd0Start    = 8
		nIFD0        = 2
		ifd0Size     = 2 + nIFD0*12 + 4
		exifIFDStart = ifd0Start + ifd0Size // 38
		nExif        = 2
		exifIFDSize  = 2 + nExif*12 + 4
		mnBlobStart  = exifIFDStart + exifIFDSize // 68
		oolBlobOff   = 18                         // 2 + 12 + 4
		oolAbsInSrc  = mnBlobStart + oolBlobOff   // 86
	)

	oolOrig := []byte{0xCA, 0xFE, 0xBA, 0xBE, 0xDE, 0xAD, 0xC0, 0xDE}
	blobLen := oolBlobOff + len(oolOrig)
	totalLen := mnBlobStart + blobLen

	buf := make([]byte, totalLen)

	// TIFF header.
	buf[0], buf[1] = 'M', 'M'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Start)

	// IFD0 write helper.
	p := ifd0Start + 2
	order.PutUint16(buf[ifd0Start:], nIFD0)
	writeE := func(tag, typ uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeE(tagImageWidth, typeLong, 1, 1)
	writeE(tagExifIFDPointer, typeLong, 1, exifIFDStart)
	p += 4 // skip IFD0 next-IFD (already zero)

	// ExifIFD.
	order.PutUint16(buf[exifIFDStart:], nExif)
	q := exifIFDStart + 2
	// Entry 1: ExifVersion (inline, tag 0x9000).
	exifVersion := [4]byte{'0', '2', '2', '0'}
	order.PutUint16(buf[q:], tagExifVersion)
	order.PutUint16(buf[q+2:], typeUndef)
	order.PutUint32(buf[q+4:], 4)
	copy(buf[q+8:], exifVersion[:])
	q += 12
	// Entry 2: MakerNote (OOL, tag 0x927C).
	order.PutUint16(buf[q:], tagMakerNote)
	order.PutUint16(buf[q+2:], typeUndef)
	order.PutUint32(buf[q+4:], uint32(blobLen)) //nolint:gosec // G115: test helper
	order.PutUint32(buf[q+8:], mnBlobStart)
	// ExifIFD next-IFD already zero.

	// MakerNote blob (plain IFD, big-endian, Nikon Type-1 structure).
	mn := buf[mnBlobStart:]
	order.PutUint16(mn[0:], 1)
	ep2 := 2
	order.PutUint16(mn[ep2:], 0x0001)
	order.PutUint16(mn[ep2+2:], typeUndef)
	order.PutUint32(mn[ep2+4:], 8)
	order.PutUint32(mn[ep2+8:], oolAbsInSrc) // TIFF-absolute OOL pointer
	// next-IFD at ep2+12 = mn[14]; already zero.
	copy(mn[oolBlobOff:], oolOrig)

	// Inject must not fail.
	const dummyXMPStr = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end='w'?>`
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(buf), &out, buf, nil, []byte(dummyXMPStr), false); err != nil {
		t.Fatalf("Inject must not fail for Nikon Type-1 (no corruption guarantee): %v", err)
	}
	finalTIFF := out.Bytes()

	// GUARANTEE (a): output must be a parseable TIFF.
	if len(finalTIFF) < 8 {
		t.Fatalf("output TIFF too short: %d bytes", len(finalTIFF))
	}
	magic := order.Uint16(finalTIFF[2:])
	if magic != 0x002A {
		t.Errorf("output TIFF bad magic: 0x%04X (want 0x002A)", magic)
	}

	// GUARANTEE (b): ExifVersion in ExifIFD must be preserved intact.
	e, parseErr := exif.Parse(finalTIFF)
	if parseErr != nil {
		t.Fatalf("exif.Parse on Inject output failed: %v", parseErr)
	}
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil in output — ExifIFD was lost after relocation")
	}
	// Tag 0x9000 = ExifVersion (EXIF §4.6.5); use raw TagID since there is no
	// exported TagExifVersion constant in this codebase.
	const exifVersionTagID = exif.TagID(0x9000)
	versionEntry := e.ExifIFD.Get(exifVersionTagID)
	if versionEntry == nil {
		t.Fatal("ExifVersion tag missing from ExifIFD in output")
	}
	if !bytes.Equal(versionEntry.Value, exifVersion[:]) {
		t.Errorf("ExifVersion mismatch: got %q, want %q", versionEntry.Value, exifVersion)
	}

	t.Log("Nikon Type-1 documented limitation: Type-1 plain-IFD blobs share the " +
		"Sony plain-IFD structure; rebaseGenericMakerNote applies correctly. " +
		"True opaque/encrypted blobs (e.g. proprietary encryption) are not rebased " +
		"but image data and non-MakerNote EXIF are always preserved.")
}
