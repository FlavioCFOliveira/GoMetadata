package tiff

// relocate_nef_test.go — regression tests for the Nikon NEF-specific
// copy-and-relocate path (task #102).
//
// These tests validate that relocateTIFFFromParsedNEF correctly handles the two
// Nikon-specific structural challenges:
//
//  1. The Nikon Type-3 MakerNote blob declares a smaller byte count than the
//     actual extent of MakerNote-referenced data.  The PreviewIFD and
//     NikonScanIFD live beyond the declared blob boundary.  relocate_nef.go
//     must extend the blob to cover those structures.
//
//  2. The PreviewIFD (inside the MakerNote embedded TIFF) holds tag 0x0201
//     (PreviewImageStart) as a MakerNote-TIFF-relative offset, not an outer-TIFF
//     absolute offset.  After relocation, this pointer must be updated to the
//     new MakerNote-relative position of the moved preview JPEG.
//
// Synthetic fixture structure
// ───────────────────────────
// The fixture is a big-endian TIFF (MM\0*) with:
//
//   IFD0:
//     StripOffsets → thumbnail bytes ("THUMB")
//     StripByteCounts → 5
//     ExifIFD pointer (0x8769) → ExifIFD
//     SubIFDs (0x014A) × 2 → SubIFD[0] (JPEG preview), SubIFD[1] (raw)
//
//   ExifIFD:
//     MakerNote (0x927C) → Nikon Type-3 blob
//
//   Nikon Type-3 MakerNote blob:
//     [0..5]  "Nikon\x00"   magic
//     [6..7]  version 0x02 0x00
//     [8..9]  0x00 0x00     padding (D70 variant, TIFF header at offset 10)
//     [10..]  embedded TIFF header: MM 0x002A + IFD0 offset
//     IFD0 of embedded TIFF:
//       tag 0x0011 (PreviewIFD)  → offset within embedded TIFF
//     PreviewIFD (inside embedded TIFF):
//       0x0103 (Compression) = 6 (JPEG)
//       0x0201 (PreviewImageStart) = offset relative to MakerNote embedded TIFF base
//       0x0202 (PreviewImageLength) = len(previewJPEG)
//
//   SubIFD[0]: JpgFromRawStart / JpgFromRawLength → jpegFromRaw bytes ("JFRAW")
//   SubIFD[1]: StripOffsets/StripByteCounts → raw bytes ("RAWDAT")
//
//   Image data (after IFD area):
//     thumbnail bytes ("THUMB")
//     previewJPEG bytes ("\xFF\xD8preview")   ← MakerNote PreviewIFD points here
//     jpegFromRaw bytes ("JFRAW")              ← SubIFD[0] 0x0201 points here
//     rawData bytes ("RAWDAT")                 ← SubIFD[1] StripOffsets points here

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// buildNEFLikeTIFF constructs a synthetic big-endian TIFF that mimics the
// Nikon D70 NEF structure used in task #102.
//
// All offsets in the returned buffer are correct for the original layout.
// The test then calls InjectWithEXIFNEF and verifies that the output correctly
// relocates all four image blocks and updates the MakerNote PreviewIFD pointer.
func buildNEFLikeTIFF(t *testing.T, thumbnail, previewJPEG, jpegFromRaw, rawData []byte) []byte {
	t.Helper()

	// We build the file in two passes:
	// Pass 1: assemble the IFD area with placeholder values for offsets.
	// Pass 2: compute final offsets and patch them in.

	// Helper: big-endian uint32 write.
	putBE32 := func(b []byte, off int, v uint32) {
		binary.BigEndian.PutUint32(b[off:], v)
	}
	// Helper: big-endian uint16 write.
	putBE16 := func(b []byte, off int, v uint16) {
		binary.BigEndian.PutUint16(b[off:], v)
	}

	// -----------------------------------------------------------------------
	// Layout plan (all offsets are absolute in the outer TIFF):
	//
	// 0x0000: TIFF header (8 bytes): MM 0x002A + IFD0 offset = 0x0008
	// 0x0008: IFD0 (count + 6 entries + nextIFD)
	//         6 entries × 12 = 72 bytes, + 2 (count) + 4 (nextIFD) = 78 bytes
	//         OOL values: SubIFDs array (2 × 4 = 8 bytes) immediately follows
	// 0x0058: SubIFDs array: [subifd0_off, subifd1_off] (8 bytes)
	// 0x0060: ExifIFD (count + 1 entry + nextIFD)
	//         1 entry × 12 = 12 bytes, + 2 + 4 = 18 bytes
	// 0x0072: MakerNote blob start
	//   mn_hdr [0..9]: "Nikon\x00\x02\x00\x00\x00" (10 bytes, version 0x0200, D70 style)
	//   mn_tiff [10..17]: MM 0x002A + ifd0_off_rel (8 bytes, ifd0 at rel offset 8 within tiff section)
	//   mn_ifd0 [18..]: count(2) + 1 entry × 12 + nextIFD(4) = 18 bytes
	//     entry: tag=0x0011, type=4(LONG), count=1, inline_val = previewIFD_rel_off
	//   previewIFD [36..]: count(2) + 3 entries × 12 + nextIFD(4) = 42 bytes
	//     entry[0]: tag=0x0103 (Compression), type=3, count=1, val=6
	//     entry[1]: tag=0x0201 (PreviewImageStart), type=4, count=1, val=preview_rel_off
	//     entry[2]: tag=0x0202 (PreviewImageLength), type=4, count=1, val=len(previewJPEG)
	// MakerNote blob = all above from mn_hdr to end of previewIFD
	// -----------------------------------------------------------------------

	// MakerNote embedded TIFF layout (relative to mn_tiff_base = blob[10]):
	//   +0x00: MM 0x002A 0x00000008  (TIFF header, IFD0 at relative offset 8)
	//   +0x08: 0x0001 (1 entry)      (MakerNote IFD0 start)
	//     entry: tag 0x0011, type 4, count 1, val = previewIFD_rel (from mn_tiff_base)
	//   +0x1a: 0x00000000            (nextIFD = 0)
	//   +0x1e: (previewIFD start, relative to mn_tiff_base)
	//     count = 3
	//     entry[0]: 0x0103 SHORT  1  0x0006
	//     entry[1]: 0x0201 LONG   1  preview_data_rel (from mn_tiff_base)
	//     entry[2]: 0x0202 LONG   1  len(previewJPEG)
	//   +0x36: (nextIFD = 0)

	const (
		tiffHdrSize       = 8
		ifd0Start         = 8
		ifd0EntryCount    = 6                         // StripOffsets, StripByteCounts, ExifIFD, SubIFDs, + 2 for byte order
		ifd0FixedSize     = 2 + ifd0EntryCount*12 + 4 // count + entries + nextIFD
		subIFDsArraySize  = 8                         // 2 × TypeLong
		exifIFDStart      = ifd0Start + ifd0FixedSize + subIFDsArraySize
		exifIFDEntryCount = 1 // MakerNote only
		exifIFDFixedSize  = 2 + exifIFDEntryCount*12 + 4
		makerNoteStart    = exifIFDStart + exifIFDFixedSize
		mnHeaderBytes     = 10 // "Nikon\x00\x02\x00\x00\x00"
		mnTIFFBase        = makerNoteStart + mnHeaderBytes
		mnIFD0RelOff      = 8 // embedded TIFF IFD0 is 8 bytes into tiff section
		mnIFD0FileOff     = mnTIFFBase + mnIFD0RelOff
		mnIFD0EntryCount  = 1
		mnIFD0FixedSize   = 2 + mnIFD0EntryCount*12 + 4
		previewIFDRelOff  = mnIFD0RelOff + mnIFD0FixedSize // relative to mnTIFFBase
		previewIFDFileOff = mnTIFFBase + previewIFDRelOff
		previewIFDEntries = 3
		previewIFDFixedSz = 2 + previewIFDEntries*12 + 4
		makerNoteEnd      = previewIFDFileOff + previewIFDFixedSz // MakerNote blob ends here
		// SubIFDs
		subIFD0Start = makerNoteEnd
		// SubIFD[0]: 5 entries (SubfileType, Compression, JpgFromRawStart, JpgFromRawLength, YCbCrPos)
		subifd0EntryCount = 3
		subifd0FixedSize  = 2 + subifd0EntryCount*12 + 4
		subIFD1Start      = subIFD0Start + subifd0FixedSize
		subifd1EntryCount = 2
		subifd1FixedSize  = 2 + subifd1EntryCount*12 + 4
		// Image data starts here
		imageDataStart = subIFD1Start + subifd1FixedSize
	)

	// Compute image block offsets.
	// Test fixture sizes are small (< 100 bytes); uint32 conversion is safe.
	thumbOff := uint32(imageDataStart)
	previewOff := thumbOff + uint32(len(thumbnail))     //nolint:gosec // G115: test fixture sizes << 2^32
	jpegRawOff := previewOff + uint32(len(previewJPEG)) //nolint:gosec // G115: same
	rawDataOff := jpegRawOff + uint32(len(jpegFromRaw)) //nolint:gosec // G115: same
	totalSize := rawDataOff + uint32(len(rawData))      //nolint:gosec // G115: test fixture sizes << 2^32

	// MakerNote-relative preview offset (relative to mnTIFFBase)
	previewRelOff := previewOff - uint32(mnTIFFBase)

	// Allocate buffer
	buf := make([]byte, totalSize)

	// TIFF header: MM + 0x002A + IFD0 at 0x0008
	buf[0], buf[1] = 'M', 'M'
	putBE16(buf, 2, 0x002A)
	putBE32(buf, 4, uint32(ifd0Start))

	// IFD0: 6 entries
	pos := ifd0Start
	putBE16(buf, pos, uint16(ifd0EntryCount))
	pos += 2

	// Entry 0: StripOffsets (0x0111) LONG count=1, inline=thumbOff
	putBE16(buf, pos, 0x0111)
	putBE16(buf, pos+2, 4) // TypeLong
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, thumbOff)
	pos += 12

	// Entry 1: StripByteCounts (0x0117) LONG count=1, inline=len(thumbnail)
	putBE16(buf, pos, 0x0117)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, uint32(len(thumbnail))) //nolint:gosec // G115: len(thumbnail) is test-controlled
	pos += 12

	// Entry 2: ExifIFD pointer (0x8769) LONG count=1, inline=exifIFDStart
	putBE16(buf, pos, 0x8769)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, uint32(exifIFDStart))
	pos += 12

	// Entry 3: SubIFDs (0x014A) LONG count=2, OOL pointer to subIFDsArray
	putBE16(buf, pos, 0x014A)
	putBE16(buf, pos+2, 4) // TypeLong
	putBE32(buf, pos+4, 2)
	subIFDsArrayOff := uint32(ifd0Start + ifd0FixedSize)
	putBE32(buf, pos+8, subIFDsArrayOff)
	pos += 12

	// Entry 4: ImageWidth (0x0100) SHORT count=1 (dummy, sorted before 0x014A)
	// ... skip for simplicity; just add two dummy entries to reach count=6
	// Entry 4: NewSubfileType (0x00FE) LONG count=1 = 1 (reduced resolution image)
	putBE16(buf, pos, 0x00FE)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, 1)
	pos += 12

	// Entry 5: Orientation (0x0112) SHORT count=1 = 1
	putBE16(buf, pos, 0x0112)
	putBE16(buf, pos+2, 3) // TypeShort
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, 1<<16) // value 1 in high 2 bytes (big-endian SHORT inline)
	pos += 12

	// nextIFD = 0
	putBE32(buf, pos, 0)
	pos += 4

	// SubIFDs OOL array: [subIFD0Start, subIFD1Start]
	putBE32(buf, pos, uint32(subIFD0Start))
	putBE32(buf, pos+4, uint32(subIFD1Start))

	// ExifIFD: 1 entry (MakerNote)
	pos = exifIFDStart
	putBE16(buf, pos, uint16(exifIFDEntryCount))
	pos += 2

	// MakerNote (0x927C) UNDEFINED count=makerNoteEnd-makerNoteStart, OOL=makerNoteStart
	mnBlobSize := makerNoteEnd - makerNoteStart
	putBE16(buf, pos, 0x927C)
	putBE16(buf, pos+2, 7)                  // TypeUndefined
	putBE32(buf, pos+4, uint32(mnBlobSize)) // mnBlobSize is a compile-time constant (all consts)
	putBE32(buf, pos+8, uint32(makerNoteStart))
	pos += 12
	putBE32(buf, pos, 0) // nextIFD

	// MakerNote blob
	pos = makerNoteStart
	// Header: "Nikon\x00\x02\x00\x00\x00" (D70 variant, TIFF at +10)
	copy(buf[pos:], "Nikon\x00\x02\x00\x00\x00")
	pos += 10
	// Embedded TIFF header at pos = mnTIFFBase
	buf[pos], buf[pos+1] = 'M', 'M'           // byte order
	putBE16(buf, pos+2, 0x002A)               // magic
	putBE32(buf, pos+4, uint32(mnIFD0RelOff)) // IFD0 at relative offset 8
	pos += 8                                  // now at mnIFD0FileOff

	// MakerNote IFD0: 1 entry (PreviewIFD pointer 0x0011)
	putBE16(buf, pos, uint16(mnIFD0EntryCount))
	pos += 2
	putBE16(buf, pos, 0x0011) // tag: PreviewIFD
	putBE16(buf, pos+2, 4)    // TypeLong
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, previewIFDRelOff) // MakerNote-TIFF-relative offset of PreviewIFD
	pos += 12
	putBE32(buf, pos, 0) // nextIFD
	pos += 4

	// PreviewIFD (at previewIFDFileOff)
	if pos != previewIFDFileOff {
		t.Fatalf("previewIFD layout mismatch: pos=%d want=%d", pos, previewIFDFileOff)
	}
	putBE16(buf, pos, uint16(previewIFDEntries))
	pos += 2

	// Entry[0]: Compression (0x0103) SHORT count=1 val=6 (JPEG)
	putBE16(buf, pos, 0x0103)
	putBE16(buf, pos+2, 3) // TypeShort
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, 6<<16) // val=6 big-endian SHORT inline
	pos += 12

	// Entry[1]: PreviewImageStart (0x0201) LONG count=1 val=previewRelOff
	putBE16(buf, pos, 0x0201)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, previewRelOff) // MakerNote-TIFF-relative
	pos += 12

	// Entry[2]: PreviewImageLength (0x0202) LONG count=1 val=len(previewJPEG)
	putBE16(buf, pos, 0x0202)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, uint32(len(previewJPEG))) //nolint:gosec // G115: test-controlled length
	pos += 12
	putBE32(buf, pos, 0) // nextIFD
	pos += 4

	// SubIFD[0]: JpgFromRaw (JPEG-in-TIFF format via 0x0201/0x0202)
	if pos != subIFD0Start {
		t.Fatalf("SubIFD[0] layout mismatch: pos=%d want=%d", pos, subIFD0Start)
	}
	putBE16(buf, pos, uint16(subifd0EntryCount))
	pos += 2

	// Entry[0]: Compression (0x0103) SHORT count=1 val=6
	putBE16(buf, pos, 0x0103)
	putBE16(buf, pos+2, 3)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, 6<<16)
	pos += 12

	// Entry[1]: JpgFromRawStart (0x0201) LONG count=1 val=jpegRawOff
	putBE16(buf, pos, 0x0201)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, jpegRawOff)
	pos += 12

	// Entry[2]: JpgFromRawLength (0x0202) LONG count=1 val=len(jpegFromRaw)
	putBE16(buf, pos, 0x0202)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, uint32(len(jpegFromRaw))) //nolint:gosec // G115: test-controlled
	pos += 12
	putBE32(buf, pos, 0) // nextIFD
	pos += 4

	// SubIFD[1]: raw strip (StripOffsets / StripByteCounts)
	if pos != subIFD1Start {
		t.Fatalf("SubIFD[1] layout mismatch: pos=%d want=%d", pos, subIFD1Start)
	}
	putBE16(buf, pos, uint16(subifd1EntryCount))
	pos += 2

	// Entry[0]: StripOffsets (0x0111) LONG count=1 val=rawDataOff
	putBE16(buf, pos, 0x0111)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, rawDataOff)
	pos += 12

	// Entry[1]: StripByteCounts (0x0117) LONG count=1 val=len(rawData)
	putBE16(buf, pos, 0x0117)
	putBE16(buf, pos+2, 4)
	putBE32(buf, pos+4, 1)
	putBE32(buf, pos+8, uint32(len(rawData))) //nolint:gosec // G115: test-controlled
	pos += 12
	putBE32(buf, pos, 0) // nextIFD
	pos += 4

	// Image data
	if pos != imageDataStart {
		t.Fatalf("image data layout mismatch: pos=%d want=%d", pos, imageDataStart)
	}
	copy(buf[thumbOff:], thumbnail)
	copy(buf[previewOff:], previewJPEG)
	copy(buf[jpegRawOff:], jpegFromRaw)
	copy(buf[rawDataOff:], rawData)

	return buf
}

// TestNEFRelocateNikonPreviewIFD verifies that relocateTIFFFromParsedNEF
// correctly handles a synthetic Nikon NEF-like TIFF:
//
//  1. All four image blocks (thumbnail, MakerNote preview, SubIFD JPEG, SubIFD raw)
//     are preserved byte-identically.
//  2. The MakerNote PreviewIFD tag 0x0201 is updated with the new
//     MakerNote-relative offset of the relocated preview JPEG.
//  3. The output parses without error.
//  4. The SubIFD[0] JpgFromRaw offset is updated correctly.
//  5. The SubIFD[1] strip offset is updated correctly.
func TestNEFRelocateNikonPreviewIFD(t *testing.T) { //nolint:paralleltest // modifies global EXIF struct; not safe to parallelize
	thumbnail := []byte("THUMBNAIL_DATA")
	previewJPEG := []byte("\xFF\xD8preview_jpeg_data\xFF\xD9")
	jpegFromRaw := []byte("JPEG_FROM_RAW")
	rawData := []byte("NEF_RAW_STRIP_DATA")

	base := buildNEFLikeTIFF(t, thumbnail, previewJPEG, jpegFromRaw, rawData)

	// Parse the base to get the EXIF struct.
	e, err := exif.Parse(base)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	if e.MakerNoteOffset == 0 {
		t.Fatal("MakerNoteOffset is 0 after parse; expected non-zero (MakerNote is OOL)")
	}

	// Set a copyright tag to exercise the write path.
	e.SetCopyright("© NEF regression test")

	// Call the NEF-specific relocator.
	var out bytes.Buffer
	if err := InjectWithEXIFNEF(base, e, nil, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIFNEF: %v", err)
	}

	result := out.Bytes()
	if len(result) == 0 {
		t.Fatal("InjectWithEXIFNEF produced no output")
	}

	// Re-parse the result.
	e2, err := exif.Parse(result)
	if err != nil {
		t.Fatalf("exif.Parse result: %v", err)
	}

	// Verify copyright was written.
	if got := e2.Copyright(); got != "© NEF regression test" {
		t.Errorf("Copyright round-trip: got %q, want %q", got, "© NEF regression test")
	}

	// Verify IFD0 thumbnail bytes are byte-identical.
	checkBlockIdentical(t, "IFD0 thumbnail (StripOffsets)", base, result,
		e2.IFD0, exif.TagStripOffsets, exif.TagStripByteCounts, thumbnail, binary.BigEndian)

	// Verify MakerNote PreviewIFD image block is byte-identical.
	checkNikonPreviewBlock(t, result, previewJPEG, binary.BigEndian)

	// Verify SubIFD[0] JpgFromRaw bytes are byte-identical.
	checkSubIFD0JPEGBlock(t, base, result, e2, jpegFromRaw, binary.BigEndian)

	// Verify SubIFD[1] raw strip bytes are byte-identical.
	checkSubIFD1StripBlock(t, base, result, e2, rawData, binary.BigEndian)
}

// checkBlockIdentical reads the StripOffset from ifd in result and verifies
// that the bytes at that offset match want.
func checkBlockIdentical(t *testing.T, name string, _, result []byte, ifd *exif.IFD, offsetTag, countTag exif.TagID, want []byte, order binary.ByteOrder) {
	t.Helper()
	offEntry := ifd.Get(offsetTag)
	cntEntry := ifd.Get(countTag)
	if offEntry == nil || cntEntry == nil {
		t.Errorf("%s: offset or count entry missing from result IFD", name)
		return
	}
	if len(offEntry.Value) < 4 || len(cntEntry.Value) < 4 {
		t.Errorf("%s: offset/count entry value too short", name)
		return
	}
	off := order.Uint32(offEntry.Value)
	size := order.Uint32(cntEntry.Value)
	if uint64(off)+uint64(size) > uint64(len(result)) {
		t.Errorf("%s: offset+size (%d+%d) out of result bounds (%d)", name, off, size, len(result))
		return
	}
	got := result[off : off+size]
	if !bytes.Equal(got, want) {
		t.Errorf("%s: bytes differ\n  got:  %q\n  want: %q", name, got, want)
	}
}

// checkNikonPreviewBlock finds the Nikon Type-3 MakerNote in result, locates
// the PreviewIFD 0x0201 entry, and verifies the bytes at the indicated offset
// match want.
func checkNikonPreviewBlock(t *testing.T, result, wantPreview []byte, order binary.ByteOrder) {
	t.Helper()

	// Find MakerNote blob in result.
	mnOff := bytes.Index(result, []byte("Nikon\x00"))
	if mnOff < 0 {
		t.Error("MakerNote 'Nikon\\x00' prefix not found in result")
		return
	}

	// Find TIFF header offset dynamically.
	mnBlob := result[mnOff:]
	tiffHdrOff, mnOrder := findNikonMNTIFFHeader(mnBlob)
	if mnOrder == nil {
		t.Error("embedded TIFF header not found in MakerNote blob")
		return
	}

	mnTIFFBase := uint32(mnOff) + uint32(tiffHdrOff) //nolint:gosec // G115: test file
	tiffHdr := result[mnTIFFBase:]
	ifd0RelOff := mnOrder.Uint32(tiffHdr[4:])
	mnIFD0FileOff := int(mnTIFFBase) + int(ifd0RelOff)

	// Find PreviewIFD pointer (tag 0x0011) in MakerNote IFD0.
	previewRelOff, found := findInlineIFDPointer(result, uint32(mnIFD0FileOff), exif.TagID(0x0011), order) //nolint:gosec // G115
	if !found {
		t.Error("PreviewIFD pointer (tag 0x0011) not found in MakerNote IFD0")
		return
	}
	previewIFDFileOff := int(mnTIFFBase) + int(previewRelOff)

	// Read 0x0201 from PreviewIFD.
	previewImgRelOff, previewImgSize, _, _, ok := parsePreviewIFDEntries(result, uint32(previewIFDFileOff), order) //nolint:gosec // G115
	if !ok || previewImgRelOff == 0 {
		t.Error("PreviewIFD 0x0201 not found in result")
		return
	}

	absOff := int(mnTIFFBase) + int(previewImgRelOff)
	if uint64(absOff)+uint64(previewImgSize) > uint64(len(result)) { //nolint:gosec // G115: absOff is non-negative int; widening to uint64
		t.Errorf("preview image at abs=%d size=%d out of result bounds (%d)", absOff, previewImgSize, len(result))
		return
	}
	got := result[absOff : absOff+int(previewImgSize)]
	if !bytes.Equal(got, wantPreview) {
		t.Errorf("PreviewIFD image bytes differ\n  got:  %q\n  want: %q", got, wantPreview)
	}
}

// checkSubIFD0JPEGBlock parses SubIFD[0] from the result (following the 0x014A
// pointer in e2.IFD0) and verifies that the JPEG bytes it references match want.
func checkSubIFD0JPEGBlock(t *testing.T, _, result []byte, e2 *exif.EXIF, wantJPEG []byte, order binary.ByteOrder) {
	t.Helper()

	// Get 0x014A SubIFDs pointer array from e2.IFD0.
	subEntry := e2.IFD0.Get(exif.TagSubIFDs)
	if subEntry == nil || subEntry.Count < 1 {
		t.Error("SubIFDs entry (0x014A) missing from result IFD0")
		return
	}
	if len(subEntry.Value) < 4 {
		t.Error("SubIFDs value too short")
		return
	}
	subifd0Off := order.Uint32(subEntry.Value[:4])

	// Parse SubIFD[0] from result.
	subifd0, _, ok := exif.ParseIFDAt(result, subifd0Off, order)
	if !ok || subifd0 == nil {
		t.Error("SubIFD[0] not parseable in result")
		return
	}

	// Find 0x0201 and 0x0202.
	offEntry := subifd0.Get(exif.TagJPEGInterchangeFormat)
	lenEntry := subifd0.Get(exif.TagJPEGInterchangeFormatLength)
	if offEntry == nil || lenEntry == nil {
		// Try ThumbnailData path.
		if subifd0.ThumbnailData != nil {
			if !bytes.Equal(subifd0.ThumbnailData, wantJPEG) {
				t.Errorf("SubIFD[0] ThumbnailData differs\n  got:  %q\n  want: %q",
					subifd0.ThumbnailData, wantJPEG)
			}
			return
		}
		t.Error("SubIFD[0] 0x0201/0x0202 entries missing in result")
		return
	}
	if len(offEntry.Value) < 4 || len(lenEntry.Value) < 4 {
		t.Error("SubIFD[0] 0x0201/0x0202 value too short")
		return
	}
	off := order.Uint32(offEntry.Value)
	size := order.Uint32(lenEntry.Value)
	if uint64(off)+uint64(size) > uint64(len(result)) {
		t.Errorf("SubIFD[0] JPEG at off=%d size=%d out of result bounds (%d)", off, size, len(result))
		return
	}
	got := result[off : off+size]
	if !bytes.Equal(got, wantJPEG) {
		t.Errorf("SubIFD[0] JPEG bytes differ\n  got:  %q\n  want: %q", got, wantJPEG)
	}
}

// checkSubIFD1StripBlock parses SubIFD[1] from the result and verifies that
// the strip bytes it references match want.
func checkSubIFD1StripBlock(t *testing.T, _, result []byte, e2 *exif.EXIF, wantStrip []byte, order binary.ByteOrder) {
	t.Helper()

	subEntry := e2.IFD0.Get(exif.TagSubIFDs)
	if subEntry == nil || subEntry.Count < 2 {
		t.Error("SubIFDs entry (0x014A) has fewer than 2 entries in result IFD0")
		return
	}
	if len(subEntry.Value) < 8 {
		t.Error("SubIFDs value too short for 2 entries")
		return
	}
	subifd1Off := order.Uint32(subEntry.Value[4:8])

	subifd1, _, ok := exif.ParseIFDAt(result, subifd1Off, order)
	if !ok || subifd1 == nil {
		t.Error("SubIFD[1] not parseable in result")
		return
	}

	checkBlockIdentical(t, "SubIFD[1] raw strip", nil, result,
		subifd1, exif.TagStripOffsets, exif.TagStripByteCounts, wantStrip, order)
}
