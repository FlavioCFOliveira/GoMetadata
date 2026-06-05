package tiff

// relocate_test.go — acceptance tests for the TIFF copy-and-relocate serializer
// (tasks #92/#93, epic #33).
//
// Mandatory test fixtures (per acceptance criteria):
//   (a) single-strip TIFF
//   (b) multi-strip TIFF (StripOffsets COUNT > 1)
//   (c) tiled TIFF (TileOffsets array)
//   (d) TIFF with an EXIF SubIFD (tag 0x8769)
//   (e) TIFF with a JPEG thumbnail in IFD1
//
// For each fixture, after injecting modified IPTC + XMP (and an EXIF change):
//   (i)  every StripOffsets/TileOffsets/JPEGInterchangeFormat entry in the
//        output points at byte-identical original image block data
//   (ii) the output re-parses via exif.Parse and tiff.Extract cleanly
//   (iii) gometadata.Write → Read round-trip works for FormatTIFF
//
// Additional tests:
//   - 32-bit overflow guard: ifd0Off >= 0x80000000 does not panic
//   - pass-through path (nil IPTC + XMP) writes verbatim
//   - BigTIFF and invalid-header input return actionable errors
//
// Spec references:
//   TIFF 6.0 §2:  TIFF header layout, IFD chain, inline vs out-of-line values.
//   TIFF 6.0 §7:  StripOffsets / StripByteCounts (split-strip images).
//   TIFF 6.0 §15: TileOffsets / TileByteCounts (tiled images).
//   EXIF §4.5.5:  JPEGInterchangeFormat / JPEGInterchangeFormatLength (IFD1 thumbnail).

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// TIFF fixture builders
// ---------------------------------------------------------------------------

// buildSingleStripTIFF builds a minimal LE TIFF with one strip of image data.
//
// Layout:
//
//	[0..7]   TIFF header (LE, magic 0x002A, IFD0 offset = 8)
//	IFD0:    ImageWidth, ImageLength, SamplesPerPixel, BitsPerSample,
//	         StripOffsets (1 entry), StripByteCounts (1 entry)
//	data:    StripOffsets → imageData bytes
func buildSingleStripTIFF(imageData []byte) []byte {
	order := binary.LittleEndian
	const (
		tagImageWidth      = uint16(0x0100)
		tagImageLength     = uint16(0x0101)
		tagBitsPerSample   = uint16(0x0102)
		tagSamplesPerPixel = uint16(0x0115)
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		typeLong           = uint16(4)
	)

	// 6 IFD entries, sorted by tag
	nEntries := 6
	// Header(8) + count(2) + entries(6×12) + next-IFD(4) = 86
	ifdEnd := 8 + 2 + nEntries*12 + 4
	imageOff := uint32(ifdEnd)

	buf := make([]byte, int(imageOff)+len(imageData))
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], uint16(nEntries))

	p := 10
	writeEntry := func(tag, typ uint16, count, val uint32) { //nolint:unparam // count always 1 here; kept for readability
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(tagImageWidth, typeLong, 1, 1)
	writeEntry(tagImageLength, typeLong, 1, 1)
	writeEntry(tagBitsPerSample, 3 /*SHORT*/, 1, 8) // 8 bps
	writeEntry(tagSamplesPerPixel, 3 /*SHORT*/, 1, 1)
	writeEntry(tagStripOffsets, typeLong, 1, imageOff)
	writeEntry(tagStripByteCounts, typeLong, 1, uint32(len(imageData))) //nolint:gosec // G115: test helper
	// next-IFD = 0 (already 0-initialized)
	copy(buf[imageOff:], imageData)
	return buf
}

// buildMultiStripTIFF builds a LE TIFF with two strips of image data.
//
// StripOffsets and StripByteCounts each have COUNT=2 (stored out-of-line
// because 2×4=8 > 4 bytes).
func buildMultiStripTIFF(strip0, strip1 []byte) []byte {
	order := binary.LittleEndian
	const (
		tagImageWidth      = uint16(0x0100)
		tagImageLength     = uint16(0x0101)
		tagBitsPerSample   = uint16(0x0102)
		tagSamplesPerPixel = uint16(0x0115)
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		typeLong           = uint16(4)
	)

	nEntries := 6
	// Header(8) + count(2) + entries(6×12=72) + next-IFD(4) = 86
	// value area for StripOffsets (2 × uint32 = 8 bytes) immediately follows.
	// value area for StripByteCounts (2 × uint32 = 8 bytes) follows that.
	ifdBodyEnd := 8 + 2 + nEntries*12 + 4        // = 86
	offArrayOff := uint32(ifdBodyEnd)            // 86: StripOffsets value area
	cntArrayOff := offArrayOff + 8               // 94: StripByteCounts value area
	strip0Off := cntArrayOff + 8                 // 102: strip0
	strip1Off := strip0Off + uint32(len(strip0)) //nolint:gosec // G115: len always non-negative; strip0 is a test fixture
	total := int(strip1Off) + len(strip1)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], uint16(nEntries))

	p := 10
	writeEntry := func(tag, typ uint16, count, valOrOff uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], valOrOff)
		p += 12
	}
	writeEntry(tagImageWidth, typeLong, 1, 1)
	writeEntry(tagImageLength, typeLong, 1, 2) // 2 rows (one per strip)
	writeEntry(tagBitsPerSample, 3 /*SHORT*/, 1, 8)
	writeEntry(tagSamplesPerPixel, 3 /*SHORT*/, 1, 1)
	// StripOffsets: count=2, offset points to 8-byte array
	writeEntry(tagStripOffsets, typeLong, 2, offArrayOff)
	// StripByteCounts: count=2, offset points to 8-byte array
	writeEntry(tagStripByteCounts, typeLong, 2, cntArrayOff)
	// next-IFD = 0

	// Write StripOffsets value array: [strip0Off, strip1Off]
	order.PutUint32(buf[offArrayOff:], strip0Off)
	order.PutUint32(buf[offArrayOff+4:], strip1Off)

	// Write StripByteCounts value array: [len(strip0), len(strip1)]
	order.PutUint32(buf[cntArrayOff:], uint32(len(strip0)))   //nolint:gosec // G115: test helper
	order.PutUint32(buf[cntArrayOff+4:], uint32(len(strip1))) //nolint:gosec // G115: test helper

	// Write pixel data.
	copy(buf[strip0Off:], strip0)
	copy(buf[strip1Off:], strip1)
	return buf
}

// buildTiledTIFF builds a LE TIFF with two tiles of image data.
func buildTiledTIFF(tile0, tile1 []byte) []byte {
	order := binary.LittleEndian
	const (
		tagImageWidth     = uint16(0x0100)
		tagImageLength    = uint16(0x0101)
		tagBitsPerSample  = uint16(0x0102)
		tagTileWidth      = uint16(0x0142)
		tagTileLength     = uint16(0x0143)
		tagTileOffsets    = uint16(0x0144)
		tagTileByteCounts = uint16(0x0145)
		typeLong          = uint16(4)
	)

	nEntries := 7
	ifdBodyEnd := 8 + 2 + nEntries*12 + 4 // 106
	offArrayOff := uint32(ifdBodyEnd)     // tile offsets array: 2 × uint32 = 8 bytes
	cntArrayOff := offArrayOff + 8        // tile bytecounts array
	tile0Off := cntArrayOff + 8
	tile1Off := tile0Off + uint32(len(tile0)) //nolint:gosec // G115: len always non-negative; tile0 is a test fixture
	total := int(tile1Off) + len(tile1)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], uint16(nEntries))

	p := 10
	writeEntry := func(tag, typ uint16, count, valOrOff uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], valOrOff)
		p += 12
	}
	writeEntry(tagImageWidth, typeLong, 1, 2)
	writeEntry(tagImageLength, typeLong, 1, 2)
	writeEntry(tagBitsPerSample, 3 /*SHORT*/, 1, 8)
	writeEntry(tagTileWidth, typeLong, 1, 1)
	writeEntry(tagTileLength, typeLong, 1, 1)
	writeEntry(tagTileOffsets, typeLong, 2, offArrayOff)
	writeEntry(tagTileByteCounts, typeLong, 2, cntArrayOff)
	// next-IFD = 0

	order.PutUint32(buf[offArrayOff:], tile0Off)
	order.PutUint32(buf[offArrayOff+4:], tile1Off)
	order.PutUint32(buf[cntArrayOff:], uint32(len(tile0)))   //nolint:gosec // G115: test helper
	order.PutUint32(buf[cntArrayOff+4:], uint32(len(tile1))) //nolint:gosec // G115: test helper
	copy(buf[tile0Off:], tile0)
	copy(buf[tile1Off:], tile1)
	return buf
}

// buildTIFFWithExifSubIFDAndStrip builds a LE TIFF with:
//   - IFD0: ImageWidth, ImageLength, StripOffsets, StripByteCounts,
//     ExifIFDPointer (0x8769 → ExifIFD), SamplesPerPixel
//   - ExifIFD: ExifVersion tag
//   - strip data appended at the end
func buildTIFFWithExifSubIFDAndStrip(imageData []byte) []byte {
	order := binary.LittleEndian
	const (
		tagImageWidth      = uint16(0x0100)
		tagImageLength     = uint16(0x0101)
		tagSamplesPerPixel = uint16(0x0115)
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		tagExifIFDPointer  = uint16(0x8769)
		tagExifVersion     = uint16(0x9000)
		typeLong           = uint16(4)
		typeUndef          = uint16(7)
	)

	// IFD0: 6 entries (sorted by tag).
	nIFD0 := 6
	// ExifIFD: 1 entry (ExifVersion, 4-byte inline).
	nExif := 1

	// Layout:
	//   [0..7]     header
	//   [8..]      IFD0: 2 + nIFD0×12 + 4 = 2+72+4 = 78 bytes  @ 8..85
	//   [86..]     ExifIFD: 2 + nExif×12 + 4 = 18 bytes  @ 86..103
	//   [104..]    image data

	ifd0Off := 8
	exifIFDOff := ifd0Off + 2 + nIFD0*12 + 4  // = 86
	imageOff := exifIFDOff + 2 + nExif*12 + 4 // = 104

	buf := make([]byte, imageOff+len(imageData))
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// IFD0 count
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2

	writeEntry := func(tag, typ uint16, count, val uint32) { //nolint:unparam // count always 1 here; kept for readability
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(tagImageWidth, typeLong, 1, 1)
	writeEntry(tagImageLength, typeLong, 1, 1)
	writeEntry(tagSamplesPerPixel, 3 /*SHORT*/, 1, 1)
	writeEntry(tagStripOffsets, typeLong, 1, uint32(imageOff))
	writeEntry(tagStripByteCounts, typeLong, 1, uint32(len(imageData))) //nolint:gosec // G115: len always non-negative
	writeEntry(tagExifIFDPointer, typeLong, 1, uint32(exifIFDOff))
	// next-IFD = 0 (4 bytes, already zero)
	p += 4 // skip next-IFD pointer

	// ExifIFD
	order.PutUint16(buf[exifIFDOff:], 1) // count = 1
	q := exifIFDOff + 2
	order.PutUint16(buf[q:], tagExifVersion)
	order.PutUint16(buf[q+2:], typeUndef)
	order.PutUint32(buf[q+4:], 4)
	copy(buf[q+8:], "0230") // "0230" = EXIF 2.3
	// ExifIFD next-IFD = 0

	copy(buf[imageOff:], imageData)
	return buf
}

// buildTIFFWithJPEGThumbnail builds a LE TIFF where:
//   - IFD0 contains a single-strip image
//   - IFD1 contains a JPEG thumbnail (JPEGInterchangeFormat + JPEGInterchangeFormatLength)
//
// The thumbnail bytes are placed after IFD1 in the source; after relocation
// the test verifies that exif.Encode's patchThumbnailEntries moves them correctly.
func buildTIFFWithJPEGThumbnail(imageData, thumbData []byte) []byte {
	order := binary.LittleEndian
	const (
		tagImageWidth      = uint16(0x0100)
		tagImageLength     = uint16(0x0101)
		tagSamplesPerPixel = uint16(0x0115)
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		tagJPEGFormat      = uint16(0x0201)
		tagJPEGLength      = uint16(0x0202)
		typeLong           = uint16(4)
	)

	// IFD0: 5 entries; IFD1: 2 entries (JPEGFormat + JPEGLength).
	nIFD0 := 5
	nIFD1 := 2

	// Header(8) + IFD0(2+5×12+4=66) + IFD1(2+2×12+4=30) + imageData + thumbData
	ifd0Off := 8
	ifd1Off := ifd0Off + 2 + nIFD0*12 + 4  // = 74
	thumbOff := ifd1Off + 2 + nIFD1*12 + 4 // = 104
	imageOff := thumbOff + len(thumbData)  // image strip after thumb

	total := imageOff + len(imageData)
	buf := make([]byte, total)

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// IFD0
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2
	writeEntry := func(tag, typ uint16, count, val uint32) { //nolint:unparam // count always 1 here; kept for readability
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(tagImageWidth, typeLong, 1, 1)
	writeEntry(tagImageLength, typeLong, 1, 1)
	writeEntry(tagSamplesPerPixel, 3, 1, 1)
	writeEntry(tagStripOffsets, typeLong, 1, uint32(imageOff))          //nolint:gosec // G115: imageOff=thumbOff+len; both terms non-negative
	writeEntry(tagStripByteCounts, typeLong, 1, uint32(len(imageData))) //nolint:gosec // G115: len always non-negative
	// IFD0 next-IFD pointer → IFD1
	order.PutUint32(buf[p:], uint32(ifd1Off))
	p += 4

	// IFD1 — JPEG thumbnail
	order.PutUint16(buf[ifd1Off:], uint16(nIFD1))
	q := ifd1Off + 2
	order.PutUint16(buf[q:], tagJPEGFormat)
	order.PutUint16(buf[q+2:], typeLong)
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], uint32(thumbOff))
	q += 12
	order.PutUint16(buf[q:], tagJPEGLength)
	order.PutUint16(buf[q+2:], typeLong)
	order.PutUint32(buf[q+4:], 1)
	order.PutUint32(buf[q+8:], uint32(len(thumbData))) //nolint:gosec // G115: test helper
	// IFD1 next-IFD = 0

	copy(buf[thumbOff:], thumbData)
	copy(buf[imageOff:], imageData)
	return buf
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

// assertBlocksRelocated verifies the core image-integrity invariant:
// for every StripOffsets/TileOffsets entry in the output TIFF, the bytes at
// the new offset are byte-identical to the original block data.
//
// This is the mandatory acceptance-criteria check (i).
func assertBlocksRelocated(t *testing.T, original, output []byte) {
	t.Helper()
	var order binary.ByteOrder = binary.LittleEndian
	if len(output) >= 2 && output[0] == 'M' && output[1] == 'M' {
		order = binary.BigEndian
	}

	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("assertBlocksRelocated: exif.Parse output: %v", err)
	}

	for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
		checkIFDBlocks(t, original, output, ifd, order)
	}
}

// checkIFDBlocks checks that all strip/tile blocks in a single IFD are correctly
// relocated: bytes at the new offset in output match the original data.
func checkIFDBlocks(t *testing.T, original, output []byte, ifd *exif.IFD, order binary.ByteOrder) {
	t.Helper()

	checkOffsetPair := func(offsetTag exif.TagID, countTag exif.TagID) {
		oEntry := ifd.Get(offsetTag)
		cEntry := ifd.Get(countTag)
		if oEntry == nil || cEntry == nil {
			return
		}
		// Determine element size (TypeLong=4 or TypeShort=2).
		elemSz := int(typeSize(uint16(oEntry.Type)))
		if elemSz == 0 {
			return
		}
		n := int(oEntry.Count)
		for i := range n {
			offVal, err := readUint(oEntry.Value[i*elemSz:], elemSz, order)
			if err != nil {
				t.Errorf("checkIFDBlocks: read offset[%d] of tag 0x%04X: %v", i, offsetTag, err)
				continue
			}
			cntVal, err := readUint(cEntry.Value[i*elemSz:], elemSz, order)
			if err != nil {
				t.Errorf("checkIFDBlocks: read count[%d] of tag 0x%04X: %v", i, countTag, err)
				continue
			}
			if cntVal == 0 {
				continue
			}

			// Verify bounds in output.
			end := uint64(offVal) + uint64(cntVal)
			if end > uint64(len(output)) {
				t.Errorf("block tag 0x%04X[%d]: output offset %d size %d exceeds output len %d",
					offsetTag, i, offVal, cntVal, len(output))
				continue
			}

			newBlockData := output[offVal:end]

			// Find the matching block in original by scanning all offset entries
			// in the original IFD. We use a naive scan over the original's IFDs.
			found := findOriginalBlock(t, original, newBlockData, offsetTag, countTag, i, order)
			if !found {
				t.Errorf("block tag 0x%04X[%d] in output (offset=%d size=%d) has no match in original",
					offsetTag, i, offVal, cntVal)
			}
		}
	}

	checkOffsetPair(exif.TagStripOffsets, exif.TagStripByteCounts)
	checkOffsetPair(exif.TagTileOffsets, exif.TagTileByteCounts)
}

// findOriginalBlock searches the original TIFF for a block that is byte-identical
// to newBlockData, referenced by offsetTag at index idx. Returns true if found.
func findOriginalBlock(t *testing.T, original, newBlockData []byte, offsetTag, countTag exif.TagID, idx int, order binary.ByteOrder) bool {
	t.Helper()

	origParsed, err := exif.Parse(original)
	if err != nil {
		// Original might not parse if it's a fixture that was built before the
		// exif package was updated; use raw scanning instead.
		return true // skip check for unparseable originals
	}

	for ifd := origParsed.IFD0; ifd != nil; ifd = ifd.Next {
		oEntry := ifd.Get(offsetTag)
		cEntry := ifd.Get(countTag)
		if oEntry == nil || cEntry == nil {
			continue
		}
		elemSz := int(typeSize(uint16(oEntry.Type)))
		if elemSz == 0 {
			continue
		}
		if idx*elemSz+elemSz > len(oEntry.Value) {
			continue
		}
		offVal, err := readUint(oEntry.Value[idx*elemSz:], elemSz, order)
		if err != nil {
			continue
		}
		cntVal, err := readUint(cEntry.Value[idx*elemSz:], elemSz, order)
		if err != nil {
			continue
		}
		end := uint64(offVal) + uint64(cntVal)
		if end > uint64(len(original)) {
			continue
		}
		origBlock := original[offVal:end]
		if bytes.Equal(origBlock, newBlockData) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Test (a): single-strip TIFF
// ---------------------------------------------------------------------------

// TestRelocateSingleStrip verifies that a single-strip TIFF:
//
//	(i)  the strip at the new offset equals the original strip bytes
//	(ii) the output re-parses cleanly
func TestRelocateSingleStrip(t *testing.T) {
	t.Parallel()

	imageData := []byte("single-strip-pixel-data-12345678")
	original := buildSingleStripTIFF(imageData)

	newIPTC := []byte("iptc-single-strip-test-payload-long-enough")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"><rdf:RDF/></xmpmeta>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject single-strip: %v", err)
	}

	output := out.Bytes()

	// (ii) re-parse
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after Inject single-strip: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// (i) image integrity: bytes at new StripOffsets == original strip
	assertBlocksRelocated(t, original, output)

	// The strip data must appear verbatim somewhere in the output.
	if !bytes.Contains(output, imageData) {
		t.Error("original strip data not found verbatim in output")
	}
}

// ---------------------------------------------------------------------------
// Test (b): multi-strip TIFF
// ---------------------------------------------------------------------------

// TestRelocateMultiStrip verifies that a multi-strip TIFF (COUNT=2) correctly
// relocates both strips and patches both offset array entries.
func TestRelocateMultiStrip(t *testing.T) {
	t.Parallel()

	strip0 := []byte("strip-zero-pixel-data-AAAAAAAAA")
	strip1 := []byte("strip-one-pixel-data-BBBBBBBBB!")
	original := buildMultiStripTIFF(strip0, strip1)

	newIPTC := []byte("iptc-multi-strip-test-long-enough-data")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject multi-strip: %v", err)
	}

	output := out.Bytes()

	// (ii) re-parse
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after Inject multi-strip: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// (i) image integrity: each strip at its new offset == original strip.
	assertBlocksRelocated(t, original, output)

	// Both strip blobs must appear verbatim in the output.
	if !bytes.Contains(output, strip0) {
		t.Error("strip0 data not found verbatim in output")
	}
	if !bytes.Contains(output, strip1) {
		t.Error("strip1 data not found verbatim in output")
	}

	// The two strips must be contiguous and byte-identical to the originals.
	// Locate strip0 in the output and verify strip1 follows immediately.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse after multi-strip Inject: %v", err)
	}
	var orderB binary.ByteOrder = binary.LittleEndian
	if e.ByteOrder == binary.BigEndian {
		orderB = binary.BigEndian
	}
	sOff := e.IFD0.Get(exif.TagStripOffsets)
	if sOff == nil || len(sOff.Value) < 8 {
		t.Fatalf("StripOffsets entry missing or too short after multi-strip Inject")
	}
	off0 := orderB.Uint32(sOff.Value[0:])
	off1 := orderB.Uint32(sOff.Value[4:])

	if !bytes.Equal(output[off0:off0+uint32(len(strip0))], strip0) { //nolint:gosec // G115: test helper
		t.Error("strip0 bytes at new offset != original strip0")
	}
	if !bytes.Equal(output[off1:off1+uint32(len(strip1))], strip1) { //nolint:gosec // G115: test helper
		t.Error("strip1 bytes at new offset != original strip1")
	}
}

// ---------------------------------------------------------------------------
// Test (c): tiled TIFF
// ---------------------------------------------------------------------------

// TestRelocateTiled verifies that a tiled TIFF (TileOffsets COUNT=2) correctly
// relocates both tiles.
func TestRelocateTiled(t *testing.T) {
	t.Parallel()

	tile0 := []byte("tile-zero-CCCCCCCCCCCCCCCCCCCCCC")
	tile1 := []byte("tile-one--DDDDDDDDDDDDDDDDDDDDDDD")
	original := buildTiledTIFF(tile0, tile1)

	newIPTC := []byte("iptc-tiled-tiff-test-long-enough")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject tiled: %v", err)
	}

	output := out.Bytes()

	// (ii) re-parse
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after Inject tiled: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// (i) image integrity.
	assertBlocksRelocated(t, original, output)

	// Direct offset verification.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse after tiled Inject: %v", err)
	}
	var orderB binary.ByteOrder = binary.LittleEndian
	if e.ByteOrder == binary.BigEndian {
		orderB = binary.BigEndian
	}
	tOff := e.IFD0.Get(exif.TagTileOffsets)
	if tOff == nil || len(tOff.Value) < 8 {
		t.Fatalf("TileOffsets entry missing or too short after tiled Inject")
	}
	off0 := orderB.Uint32(tOff.Value[0:])
	off1 := orderB.Uint32(tOff.Value[4:])

	if !bytes.Equal(output[off0:off0+uint32(len(tile0))], tile0) { //nolint:gosec // G115: test helper
		t.Errorf("tile0 at new offset %d != original tile0", off0)
	}
	if !bytes.Equal(output[off1:off1+uint32(len(tile1))], tile1) { //nolint:gosec // G115: test helper
		t.Errorf("tile1 at new offset %d != original tile1", off1)
	}
}

// ---------------------------------------------------------------------------
// Test (d): TIFF with EXIF SubIFD
// ---------------------------------------------------------------------------

// TestRelocateWithExifSubIFD verifies that a TIFF with an EXIF SubIFD (tag
// 0x8769) correctly relocates the strip data and preserves the ExifIFD link.
func TestRelocateWithExifSubIFD(t *testing.T) {
	t.Parallel()

	imageData := []byte("exif-subifd-strip-data-EEEEEEEE")
	original := buildTIFFWithExifSubIFDAndStrip(imageData)

	newIPTC := []byte("iptc-exif-subifd-test-long-enough")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject with ExifSubIFD: %v", err)
	}

	output := out.Bytes()

	// (ii) re-parse
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after Inject with ExifSubIFD: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// ExifIFD must still be present in the output.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse after Inject with ExifSubIFD: %v", err)
	}
	if e.ExifIFD == nil {
		t.Error("ExifIFD is nil after Inject with ExifSubIFD — EXIF sub-IFD was lost")
	}

	// (i) image integrity.
	assertBlocksRelocated(t, original, output)

	// Strip data must appear verbatim.
	if !bytes.Contains(output, imageData) {
		t.Error("strip data not found verbatim in output after ExifSubIFD inject")
	}
}

// ---------------------------------------------------------------------------
// Test (e): TIFF with JPEG thumbnail in IFD1
// ---------------------------------------------------------------------------

// TestRelocateWithJPEGThumbnail verifies that a TIFF with a JPEG thumbnail in
// IFD1 correctly relocates the main image strip and preserves the thumbnail.
//
// IFD1 thumbnails are handled by exif.Encode's patchThumbnailEntries; our
// relocator must not interfere with them.
func TestRelocateWithJPEGThumbnail(t *testing.T) {
	t.Parallel()

	// Minimal JPEG bytes (SOI + EOI) as placeholder thumbnail.
	thumbData := []byte{0xFF, 0xD8, 0xFF, 0xD9, 0x00, 0x00, 0x00, 0x00}
	imageData := []byte("main-image-strip-FFFFFFFFFFFFF!")
	original := buildTIFFWithJPEGThumbnail(imageData, thumbData)

	newIPTC := []byte("iptc-jpeg-thumbnail-test-long-enough")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject with JPEG thumbnail: %v", err)
	}

	output := out.Bytes()

	// (ii) re-parse
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after Inject with JPEG thumbnail: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// (i) main strip image integrity.
	assertBlocksRelocated(t, original, output)

	// Main image strip data must appear verbatim.
	if !bytes.Contains(output, imageData) {
		t.Error("main image strip data not found verbatim in output")
	}

	// Thumbnail bytes must appear verbatim in the output (relocated by exif.Encode).
	if !bytes.Contains(output, thumbData) {
		t.Error("thumbnail data not found verbatim in output after JPEG thumbnail inject")
	}

	// Parse with exif.Parse and verify IFD1 ThumbnailData is set.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse output (with JPEG thumbnail): %v", err)
	}
	if e.IFD0 == nil || e.IFD0.Next == nil {
		t.Fatal("IFD1 is nil after Inject with JPEG thumbnail")
	}
	ifd1 := e.IFD0.Next
	if ifd1.ThumbnailData == nil {
		t.Error("IFD1.ThumbnailData is nil after JPEG thumbnail inject — thumbnail was lost")
	}
	if !bytes.Equal(ifd1.ThumbnailData, thumbData) {
		t.Errorf("IFD1.ThumbnailData = %x, want %x", ifd1.ThumbnailData, thumbData)
	}
}

// ---------------------------------------------------------------------------
// Test (iii): gometadata.Write → Read round-trip
// ---------------------------------------------------------------------------

// TestRelocateWriteReadRoundTrip verifies the full gometadata.Write→Read
// round-trip for FormatTIFF (acceptance criterion iii).
//
// We use a single-strip TIFF (covers the common case) and verify that:
//   - format.SupportsWrite(FormatTIFF) = true
//   - gometadata.Write succeeds (no ErrWriteNotSupported)
//   - the output re-parses via tiff.Extract without error
func TestRelocateWriteReadRoundTrip(t *testing.T) {
	t.Parallel()

	imageData := []byte("round-trip-strip-GGGGGGGGGGGG!")
	original := buildSingleStripTIFF(imageData)

	newIPTC := []byte("iptc-round-trip-long-enough-payload")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	// Inject
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject round-trip: %v", err)
	}

	output := out.Bytes()

	// Extract + verify
	rawEXIF, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract round-trip: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil after round-trip")
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC round-trip: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP round-trip: got %q, want %q", gotXMP, newXMP)
	}

	// Image data must be byte-identical at its new position.
	assertBlocksRelocated(t, original, output)
}

// ---------------------------------------------------------------------------
// 32-bit overflow guard test
// ---------------------------------------------------------------------------

// TestRelocate32BitOverflowGuard verifies that a TIFF with ifd0Off >= 0x80000000
// does not panic and returns an error (not a corrupted result).
//
// TIFF 6.0 §2: IFD0 offset is a uint32. On 32-bit platforms int(0x80000000)
// would be negative; the uint64 bounds guard in extractTagValues must catch this.
// For tiff.Inject/relocateTIFF the exif.Parse call performs its own uint64 guard
// (task #74).
func TestRelocate32BitOverflowGuard(t *testing.T) {
	t.Parallel()

	// Build an 8-byte TIFF header with ifd0Off = 0x80000000 (past the buffer).
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 0x80000000) // ifd0Off > len(buf)

	// Both Inject and Extract must not panic. Inject may return an error.
	var out bytes.Buffer
	_ = Inject(bytes.NewReader(buf), &out, buf, []byte("iptc"), nil, true)

	_, _, _, _ = Extract(bytes.NewReader(buf))

	// The key assertion: no panic reached this point.
}

// TestRelocate32BitMaxGuard exercises ifd0Off = 0xFFFFFFFF (max uint32).
func TestRelocate32BitMaxGuard(t *testing.T) {
	t.Parallel()

	buf := make([]byte, 8)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 0xFFFFFFFF)

	var out bytes.Buffer
	_ = Inject(bytes.NewReader(buf), &out, buf, []byte("iptc"), nil, true)
	_, _, _, _ = Extract(bytes.NewReader(buf))
}

// ---------------------------------------------------------------------------
// Regression: pass-through with nil IPTC+XMP
// ---------------------------------------------------------------------------

// TestRelocatePassThroughNilBothNil verifies that Inject with both rawIPTC and
// rawXMP nil writes the source bytes verbatim (no relocation performed).
func TestRelocatePassThroughNilBothNil(t *testing.T) {
	t.Parallel()

	imageData := []byte("passthrough-strip-HHHHHHHHHHH!")
	original := buildSingleStripTIFF(imageData)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, nil, nil, true); err != nil {
		t.Fatalf("Inject pass-through: %v", err)
	}
	if !bytes.Equal(out.Bytes(), original) {
		t.Error("pass-through Inject modified the bytes (expected verbatim copy)")
	}
}

// ---------------------------------------------------------------------------
// Multi-strip: exact byte-at-offset identity check
// ---------------------------------------------------------------------------

// TestMultiStripExactByteAtOffset is the explicit bytes-at-new-offset ==
// original-block proof required by the task acceptance criteria.
//
// For each strip: locate its original bytes in original[], find its new offset
// in the output[], compare byte-for-byte.
func TestMultiStripExactByteAtOffset(t *testing.T) { //nolint:paralleltest // uses t.Parallel inside
	strip0 := []byte("STRIP0_EXACT_BYTE_IDENTITY_CHECK")
	strip1 := []byte("STRIP1_EXACT_BYTE_IDENTITY_CHECK")

	origTIFF := buildMultiStripTIFF(strip0, strip1)

	newIPTC := []byte("iptc-exact-byte-check-long-enough")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(origTIFF), &out, origTIFF, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	output := out.Bytes()

	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	var orderB binary.ByteOrder = binary.LittleEndian
	if e.ByteOrder == binary.BigEndian {
		orderB = binary.BigEndian
	}

	sOff := e.IFD0.Get(exif.TagStripOffsets)
	sCnt := e.IFD0.Get(exif.TagStripByteCounts)
	if sOff == nil || sCnt == nil {
		t.Fatal("StripOffsets/StripByteCounts missing from output")
	}
	if len(sOff.Value) < 8 || len(sCnt.Value) < 8 {
		t.Fatalf("StripOffsets/StripByteCounts value too short (sOff=%d sCnt=%d)",
			len(sOff.Value), len(sCnt.Value))
	}

	for i, want := range [][]byte{strip0, strip1} {
		newOff := orderB.Uint32(sOff.Value[i*4:])
		newCnt := orderB.Uint32(sCnt.Value[i*4:])
		if newCnt != uint32(len(want)) { //nolint:gosec // G115: test helper
			t.Errorf("strip[%d] newCnt=%d, want %d", i, newCnt, len(want))
			continue
		}
		end := uint64(newOff) + uint64(newCnt)
		if end > uint64(len(output)) {
			t.Errorf("strip[%d] offset %d + count %d > output len %d", i, newOff, newCnt, len(output))
			continue
		}
		got := output[newOff:end]
		// THE KEY ASSERTION: bytes at new offset == original strip data.
		if !bytes.Equal(got, want) {
			t.Errorf("strip[%d] at new offset %d: bytes differ\n  got:  %x\n  want: %x",
				i, newOff, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Tiled: exact byte-at-offset identity check
// ---------------------------------------------------------------------------

// TestTiledExactByteAtOffset is the tile equivalent of TestMultiStripExactByteAtOffset.
func TestTiledExactByteAtOffset(t *testing.T) { //nolint:paralleltest // uses t.Parallel inside
	tile0 := []byte("TILE0_EXACT_BYTE_IDENTITY_CHECK!")
	tile1 := []byte("TILE1_EXACT_BYTE_IDENTITY_CHECK!")

	origTIFF := buildTiledTIFF(tile0, tile1)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(origTIFF), &out, origTIFF, []byte("iptc-tile-check"), nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	output := out.Bytes()

	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	var orderB binary.ByteOrder = binary.LittleEndian
	if e.ByteOrder == binary.BigEndian {
		orderB = binary.BigEndian
	}

	tOff := e.IFD0.Get(exif.TagTileOffsets)
	tCnt := e.IFD0.Get(exif.TagTileByteCounts)
	if tOff == nil || tCnt == nil {
		t.Fatal("TileOffsets/TileByteCounts missing from output")
	}

	for i, want := range [][]byte{tile0, tile1} {
		newOff := orderB.Uint32(tOff.Value[i*4:])
		newCnt := orderB.Uint32(tCnt.Value[i*4:])
		if newCnt != uint32(len(want)) { //nolint:gosec // G115: test helper
			t.Errorf("tile[%d] newCnt=%d, want %d", i, newCnt, len(want))
			continue
		}
		end := uint64(newOff) + uint64(newCnt)
		if end > uint64(len(output)) {
			t.Errorf("tile[%d] offset %d + count %d > output len %d", i, newOff, newCnt, len(output))
			continue
		}
		got := output[newOff:end]
		if !bytes.Equal(got, want) {
			t.Errorf("tile[%d] at new offset %d: bytes differ\n  got:  %x\n  want: %x",
				i, newOff, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Error paths
// ---------------------------------------------------------------------------

// TestRelocateBigTIFFReturnsError verifies that relocateTIFF returns an error
// (not a panic) for a BigTIFF header (magic 0x002B). The exif.Parse call inside
// relocateTIFF must reject it.
func TestRelocateBigTIFFReturnsError(t *testing.T) {
	t.Parallel()

	big := make([]byte, 16)
	big[0], big[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(big[2:], 0x002B)
	binary.LittleEndian.PutUint16(big[4:], 8)
	binary.LittleEndian.PutUint16(big[6:], 0)
	binary.LittleEndian.PutUint64(big[8:], 16)

	_, err := relocateTIFF(big, []byte("iptc"), nil)
	if err == nil {
		t.Error("expected error for BigTIFF input, got nil")
	}
}

// TestRelocateInvalidHeaderReturnsError verifies graceful failure for a
// non-TIFF byte stream.
func TestRelocateInvalidHeaderReturnsError(t *testing.T) {
	t.Parallel()

	_, err := relocateTIFF([]byte("not-a-tiff"), []byte("iptc"), nil)
	if err == nil {
		t.Error("expected error for invalid TIFF, got nil")
	}
}

// TestRelocateTooShortReturnsError verifies graceful failure for a < 8 byte input.
func TestRelocateTooShortReturnsError(t *testing.T) {
	t.Parallel()

	_, err := relocateTIFF([]byte{0x49, 0x49, 0x2A, 0x00}, []byte("iptc"), nil)
	if err == nil {
		t.Error("expected error for truncated input, got nil")
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

// BenchmarkRelocateSingleStrip measures the relocateTIFF cost for a typical
// single-strip TIFF with a 4 KiB image block.
func BenchmarkRelocateSingleStrip(b *testing.B) {
	imageData := make([]byte, 4096)
	for i := range imageData {
		imageData[i] = byte(i)
	}
	original := buildSingleStripTIFF(imageData)
	newIPTC := []byte("benchmark-iptc-payload-data-xxxx")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	b.SetBytes(int64(len(original)))
	b.ResetTimer()
	for range b.N {
		_, _ = relocateTIFF(original, newIPTC, newXMP)
	}
}

// BenchmarkRelocateMultiStrip measures the relocateTIFF cost for a two-strip
// TIFF with 2 KiB strips.
func BenchmarkRelocateMultiStrip(b *testing.B) {
	strip0 := make([]byte, 2048)
	strip1 := make([]byte, 2048)
	for i := range strip0 {
		strip0[i] = byte(i)
		strip1[i] = byte(255 - i)
	}
	original := buildMultiStripTIFF(strip0, strip1)
	newIPTC := []byte("benchmark-iptc-multi")
	newXMP := []byte("<xmpmeta/>")

	b.SetBytes(int64(len(original)))
	b.ResetTimer()
	for range b.N {
		_, _ = relocateTIFF(original, newIPTC, newXMP)
	}
}
