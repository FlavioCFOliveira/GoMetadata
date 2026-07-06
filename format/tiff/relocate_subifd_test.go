package tiff

// relocate_subifd_test.go — acceptance tests for SubIFD (0x014A) recursive
// relocation (task #94, epic #33).
//
// DNG stores its full-resolution image data in one or more SubIFDs referenced
// by tag 0x014A in IFD0. IFD0 itself typically holds only a small thumbnail.
// Each SubIFD carries its own StripOffsets/StripByteCounts or
// TileOffsets/TileByteCounts pointing at the full-res image blocks.
//
// Mandatory acceptance criteria (per task spec):
//   (a) IFD0 thumbnail strip offsets point at byte-identical thumbnail data.
//   (b) The 0x014A SubIFD pointer points at a valid SubIFD.
//   (c) SubIFD0's StripOffsets/TileOffsets point at byte-identical full-res
//       image block data.
//   (d) Output re-parses and the injected metadata is readable.
//   (e) gometadata.Write → Read round-trip for FormatDNG.
//
// Additional tests:
//   - Two SubIFDs (multi-SubIFD DNG-like structure).
//   - SubIFD with tiles instead of strips.
//   - SubIFD with multiple strips (COUNT > 1).
//
// Spec references:
//   TIFF 6.0 §2:  TIFF header layout, IFD structure, inline vs out-of-line.
//   TIFF 6.0 §7:  StripOffsets / StripByteCounts.
//   TIFF 6.0 §15: TileOffsets / TileByteCounts.
//   TIFF Extension §F: SubIFDs tag (0x014A) — array of LONG offsets.
//   Adobe DNG Specification 1.7 §4: IFD0 thumbnail + SubIFDs full-res image.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// DNG-like TIFF fixture builders
// ---------------------------------------------------------------------------

// buildDNGLikeTIFF builds a synthetic DNG-like TIFF stream:
//
//	TIFF header (LE)
//	IFD0:
//	  ImageWidth, ImageLength, StripOffsets→thumbStrip, StripByteCounts→len(thumbStrip),
//	  SubIFDs (0x014A, count=1) → SubIFD0
//	SubIFD0:
//	  ImageWidth, ImageLength, StripOffsets→fullStrip, StripByteCounts→len(fullStrip)
//	thumbStrip bytes
//	fullStrip bytes
//
// This mirrors the canonical DNG structure: IFD0 holds a small thumbnail with
// its own strips; SubIFD0 holds the full-resolution image with its own strips.
func buildDNGLikeTIFF(thumbStrip, fullStrip []byte) []byte {
	order := binary.LittleEndian

	// IFD0: 5 entries — ImageWidth(0x0100), ImageLength(0x0101),
	// StripOffsets(0x0111), StripByteCounts(0x0117), SubIFDs(0x014A).
	// SubIFDs (0x014A, count=1, TypeLong) → value is inline (4 bytes).
	nIFD0 := 5

	// SubIFD0: 4 entries — ImageWidth, ImageLength, StripOffsets, StripByteCounts.
	nSubIFD0 := 4

	// Layout:
	//   [0..7]        header
	//   [8...]        IFD0: 2+5×12+4 = 70 bytes  @ 8..77
	//   [78..]        SubIFD0: 2+4×12+4 = 54 bytes  @ 78..131
	//   [132..]       thumbStrip data
	//   [132+len(ts)] fullStrip data
	const headerSize = 8
	ifd0Off := headerSize
	subIFD0Off := ifd0Off + 2 + nIFD0*12 + 4
	thumbOff := subIFD0Off + 2 + nSubIFD0*12 + 4
	fullOff := thumbOff + len(thumbStrip)
	total := fullOff + len(fullStrip)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// --- IFD0 ---
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2

	// writeEntry writes a TypeLong (type=4) IFD entry at position p, then advances p.
	writeEntry := func(tag uint16, count, val uint32) { //nolint:unparam // count is always 1 for IFD0 entries in this fixture
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], 4) // TypeLong
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}

	// Entries must be sorted by tag for TIFF compliance.
	writeEntry(0x0100, 1, 4)                       // ImageWidth = 4
	writeEntry(0x0101, 1, 1)                       // ImageLength = 1
	writeEntry(0x0111, 1, uint32(thumbOff))        // StripOffsets → thumb //nolint:gosec // G115: test helper
	writeEntry(0x0117, 1, uint32(len(thumbStrip))) //nolint:gosec // G115: test helper
	writeEntry(0x014A, 1, uint32(subIFD0Off))      // SubIFDs → SubIFD0 //nolint:gosec // G115: test helper
	// IFD0 next-IFD = 0 (already zero-initialized)
	p += 4 // skip next-IFD pointer

	// --- SubIFD0 ---
	order.PutUint16(buf[subIFD0Off:], uint16(nSubIFD0))
	q := subIFD0Off + 2

	// writeEntryAt writes a TypeLong IFD entry at a fixed position.
	writeEntryAt := func(pos int, tag uint16, count, val uint32) { //nolint:unparam // count is always 1 for SubIFD0 entries in this fixture
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], 4) // TypeLong
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
	}
	writeEntryAt(q, 0x0100, 1, 1024)                      // ImageWidth = 1024
	writeEntryAt(q+12, 0x0101, 1, 768)                    // ImageLength = 768
	writeEntryAt(q+24, 0x0111, 1, uint32(fullOff))        //nolint:gosec // G115: test helper
	writeEntryAt(q+36, 0x0117, 1, uint32(len(fullStrip))) //nolint:gosec // G115: test helper
	// SubIFD0 next-IFD = 0 (already zero)

	// Image data
	copy(buf[thumbOff:], thumbStrip)
	copy(buf[fullOff:], fullStrip)

	return buf
}

// buildDNGLikeMultiSubIFD builds a DNG-like TIFF with two SubIFDs.
//
// IFD0:
//
//	StripOffsets → thumbStrip (thumbnail)
//	SubIFDs (0x014A, count=2) → [SubIFD0, SubIFD1]
//
// SubIFD0: full-res strip (fullStrip0)
// SubIFD1: preview strip (previewStrip)
//
// The 0x014A value array has count=2 and is stored out-of-line (8 bytes > 4).
func buildDNGLikeMultiSubIFD(thumbStrip, fullStrip0, previewStrip []byte) []byte {
	order := binary.LittleEndian

	// IFD0: 5 entries. SubIFDs has count=2, so it is stored OUT-OF-LINE.
	// Out-of-line SubIFDs array: 2×4 = 8 bytes.
	nIFD0 := 5
	nSubIFD0 := 4
	nSubIFD1 := 4

	// Layout:
	//   [0..7]     header
	//   [8..]      IFD0: 2+5×12+4 = 70 bytes  @ 8
	//   [78..]     SubIFDs out-of-line value array (2×4 = 8 bytes)  @ 78
	//   [86..]     SubIFD0: 2+4×12+4 = 54 bytes  @ 86
	//   [140..]    SubIFD1: 2+4×12+4 = 54 bytes  @ 140
	//   [194..]    thumbStrip
	//   [194+ts]   fullStrip0
	//   [194+ts+fs0] previewStrip
	const headerSize = 8
	ifd0Off := headerSize
	subIFDsArrayOff := ifd0Off + 2 + nIFD0*12 + 4  // 78
	subIFD0Off := subIFDsArrayOff + 8              // 86
	subIFD1Off := subIFD0Off + 2 + nSubIFD0*12 + 4 // 140
	thumbOff := subIFD1Off + 2 + nSubIFD1*12 + 4   // 194
	full0Off := thumbOff + len(thumbStrip)
	prev1Off := full0Off + len(fullStrip0)
	total := prev1Off + len(previewStrip)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// --- IFD0 ---
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2

	writeEntry := func(tag uint16, count, val uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], 4) // TypeLong
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(0x0100, 1, 4)                       // ImageWidth
	writeEntry(0x0101, 1, 1)                       // ImageLength
	writeEntry(0x0111, 1, uint32(thumbOff))        // StripOffsets → thumb
	writeEntry(0x0117, 1, uint32(len(thumbStrip))) //nolint:gosec // G115: test helper
	// SubIFDs: count=2, out-of-line → subIFDsArrayOff
	writeEntry(0x014A, 2, uint32(subIFDsArrayOff))
	// IFD0 next-IFD = 0
	p += 4

	// SubIFDs array: [SubIFD0Off, SubIFD1Off]
	order.PutUint32(buf[subIFDsArrayOff:], uint32(subIFD0Off))
	order.PutUint32(buf[subIFDsArrayOff+4:], uint32(subIFD1Off))

	writeEntryAt := func(pos int, tag uint16, count, val uint32) { //nolint:unparam // count is always 1 in this fixture's SubIFDs
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], 4) // TypeLong
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
	}

	// --- SubIFD0: full-res ---
	order.PutUint16(buf[subIFD0Off:], uint16(nSubIFD0))
	q0 := subIFD0Off + 2
	writeEntryAt(q0, 0x0100, 1, 1024)
	writeEntryAt(q0+12, 0x0101, 1, 768)
	writeEntryAt(q0+24, 0x0111, 1, uint32(full0Off))        //nolint:gosec // G115: test helper
	writeEntryAt(q0+36, 0x0117, 1, uint32(len(fullStrip0))) //nolint:gosec // G115: test helper
	// SubIFD0 next-IFD = 0

	// --- SubIFD1: preview ---
	order.PutUint16(buf[subIFD1Off:], uint16(nSubIFD1))
	q1 := subIFD1Off + 2
	writeEntryAt(q1, 0x0100, 1, 256)
	writeEntryAt(q1+12, 0x0101, 1, 192)
	writeEntryAt(q1+24, 0x0111, 1, uint32(prev1Off))          //nolint:gosec // G115: test helper
	writeEntryAt(q1+36, 0x0117, 1, uint32(len(previewStrip))) //nolint:gosec // G115: test helper
	// SubIFD1 next-IFD = 0

	copy(buf[thumbOff:], thumbStrip)
	copy(buf[full0Off:], fullStrip0)
	copy(buf[prev1Off:], previewStrip)

	return buf
}

// buildDNGLikeTiledSubIFD builds a DNG-like TIFF where SubIFD0 uses tiles
// instead of strips. IFD0 has a thumbnail strip; SubIFD0 has two tiles.
func buildDNGLikeTiledSubIFD(thumbStrip, tile0, tile1 []byte) []byte {
	order := binary.LittleEndian

	// IFD0: 5 entries (same as buildDNGLikeTIFF but pointing to tiled SubIFD)
	nIFD0 := 5
	// SubIFD0: TileWidth, TileLength, TileOffsets(count=2,OOL), TileByteCounts(count=2,OOL)
	// = 6 entries
	nSubIFD0 := 6

	// Layout:
	//   header(8) + IFD0(2+5×12+4=70) + SubIFD0(2+6×12+4=78) +
	//   tileOffsetsArray(2×4=8) + tileCountsArray(2×4=8) +
	//   thumbStrip + tile0 + tile1
	const headerSize = 8
	ifd0Off := headerSize
	subIFD0Off := ifd0Off + 2 + nIFD0*12 + 4              // 78
	tileOffsetsArrOff := subIFD0Off + 2 + nSubIFD0*12 + 4 // after SubIFD0 fixed block
	tileCountsArrOff := tileOffsetsArrOff + 8
	thumbOff := tileCountsArrOff + 8
	tile0Off := thumbOff + len(thumbStrip)
	tile1Off := tile0Off + len(tile0)
	total := tile1Off + len(tile1)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// --- IFD0 ---
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2
	writeEntry := func(tag uint16, count, val uint32) { //nolint:unparam // count always 1 for IFD0 entries in this fixture
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], 4) // TypeLong
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(0x0100, 1, 2)
	writeEntry(0x0101, 1, 1)
	writeEntry(0x0111, 1, uint32(thumbOff))        // thumbOff is bounded by buf size
	writeEntry(0x0117, 1, uint32(len(thumbStrip))) //nolint:gosec // G115: len always non-negative
	writeEntry(0x014A, 1, uint32(subIFD0Off))      // subIFD0Off is small positive int
	p += 4                                         // next-IFD = 0

	// --- SubIFD0 (tiled) ---
	// Entries sorted by tag: 0x0100 ImageWidth, 0x0101 ImageLength,
	// 0x0142 TileWidth, 0x0143 TileLength,
	// 0x0144 TileOffsets, 0x0145 TileByteCounts.
	order.PutUint16(buf[subIFD0Off:], uint16(nSubIFD0))
	q := subIFD0Off + 2
	// writeEntryAt accepts count to support entries with count>1 (tile arrays).
	writeEntryAt := func(pos int, tag uint16, count, val uint32) {
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], 4) // TypeLong
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
	}
	writeEntryAt(q, 0x0100, 1, 2)
	writeEntryAt(q+12, 0x0101, 1, 2)
	writeEntryAt(q+24, 0x0142, 1, 1)                         // TileWidth = 1
	writeEntryAt(q+36, 0x0143, 1, 1)                         // TileLength = 1
	writeEntryAt(q+48, 0x0144, 2, uint32(tileOffsetsArrOff)) // tileOffsetsArrOff is small positive int
	writeEntryAt(q+60, 0x0145, 2, uint32(tileCountsArrOff))  // tileCountsArrOff is small positive int
	// SubIFD0 next-IFD = 0

	// Tile offset/count arrays
	order.PutUint32(buf[tileOffsetsArrOff:], uint32(tile0Off))    //nolint:gosec // G115: tile0Off bounded by buf
	order.PutUint32(buf[tileOffsetsArrOff+4:], uint32(tile1Off))  //nolint:gosec // G115: tile1Off bounded by buf
	order.PutUint32(buf[tileCountsArrOff:], uint32(len(tile0)))   //nolint:gosec // G115: test helper
	order.PutUint32(buf[tileCountsArrOff+4:], uint32(len(tile1))) //nolint:gosec // G115: test helper

	copy(buf[thumbOff:], thumbStrip)
	copy(buf[tile0Off:], tile0)
	copy(buf[tile1Off:], tile1)

	return buf
}

// buildDNGLikeMultiStripSubIFD builds a DNG-like TIFF where SubIFD0 has
// two strips (COUNT > 1).
func buildDNGLikeMultiStripSubIFD(thumbStrip, fullStrip0, fullStrip1 []byte) []byte {
	order := binary.LittleEndian

	nIFD0 := 5
	// SubIFD0: ImageWidth, ImageLength, StripOffsets(count=2,OOL), StripByteCounts(count=2,OOL)
	nSubIFD0 := 4

	// Layout:
	// header(8) + IFD0(70) + SubIFD0(54) + stripOffsetsArr(8) + stripCountsArr(8) +
	// thumbStrip + fullStrip0 + fullStrip1
	const headerSize = 8
	ifd0Off := headerSize
	subIFD0Off := ifd0Off + 2 + nIFD0*12 + 4 // 78
	stripOffsetsArrOff := subIFD0Off + 2 + nSubIFD0*12 + 4
	stripCountsArrOff := stripOffsetsArrOff + 8
	thumbOff := stripCountsArrOff + 8
	full0Off := thumbOff + len(thumbStrip)
	full1Off := full0Off + len(fullStrip0)
	total := full1Off + len(fullStrip1)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// --- IFD0 ---
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2
	writeEntry := func(tag uint16, count, val uint32) { //nolint:unparam // count always 1 for IFD0 entries in this fixture
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], 4) // TypeLong
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(0x0100, 1, 2)
	writeEntry(0x0101, 1, 2)
	writeEntry(0x0111, 1, uint32(thumbOff))        // thumbOff bounded by buf size
	writeEntry(0x0117, 1, uint32(len(thumbStrip))) //nolint:gosec // G115: len always non-negative
	writeEntry(0x014A, 1, uint32(subIFD0Off))      // subIFD0Off bounded by buf size
	p += 4                                         // next-IFD = 0

	// --- SubIFD0 with 2 strips ---
	order.PutUint16(buf[subIFD0Off:], uint16(nSubIFD0))
	q := subIFD0Off + 2
	// writeEntryAt accepts count to support array entries (count=2 for strip arrays).
	writeEntryAt := func(pos int, tag uint16, count, val uint32) {
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], 4) // TypeLong
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
	}
	writeEntryAt(q, 0x0100, 1, 1024)
	writeEntryAt(q+12, 0x0101, 1, 768)
	writeEntryAt(q+24, 0x0111, 2, uint32(stripOffsetsArrOff)) // stripOffsetsArrOff bounded by buf
	writeEntryAt(q+36, 0x0117, 2, uint32(stripCountsArrOff))  // stripCountsArrOff bounded by buf
	// SubIFD0 next-IFD = 0

	// Strip arrays
	order.PutUint32(buf[stripOffsetsArrOff:], uint32(full0Off))         //nolint:gosec // G115: full0Off bounded by buf
	order.PutUint32(buf[stripOffsetsArrOff+4:], uint32(full1Off))       //nolint:gosec // G115: full1Off bounded by buf
	order.PutUint32(buf[stripCountsArrOff:], uint32(len(fullStrip0)))   //nolint:gosec // G115: test helper
	order.PutUint32(buf[stripCountsArrOff+4:], uint32(len(fullStrip1))) //nolint:gosec // G115: test helper

	copy(buf[thumbOff:], thumbStrip)
	copy(buf[full0Off:], fullStrip0)
	copy(buf[full1Off:], fullStrip1)

	return buf
}

// ---------------------------------------------------------------------------
// SubIFD assertion helpers
// ---------------------------------------------------------------------------

// assertSubIFDsRelocated verifies the SubIFD acceptance criteria:
//
//	(a) IFD0's StripOffsets/TileOffsets point at byte-identical original blocks.
//	(b) The 0x014A pointer points at a valid, parseable SubIFD.
//	(c) SubIFD's StripOffsets/TileOffsets point at byte-identical full-res blocks.
//
// It uses raw-TIFF scanning (not exif.Parse) to follow the 0x014A pointer array,
// so it validates both the pointer and the SubIFD structure independently.
func assertSubIFDsRelocated(t *testing.T, base, output []byte) {
	t.Helper()
	var order binary.ByteOrder = binary.LittleEndian
	if len(output) >= 2 && output[0] == 'M' && output[1] == 'M' {
		order = binary.BigEndian
	}

	// IFD0 blocks (thumbnail strips/tiles in IFD0).
	// Use exif.Parse path for IFD0.
	assertBlocksRelocated(t, base, output)

	// SubIFD blocks: follow the 0x014A pointer from IFD0 in the output.
	if len(output) < 8 {
		return
	}
	ifd0Off := int(order.Uint32(output[4:]))
	if ifd0Off+2 > len(output) {
		return
	}
	ifd0Count := int(order.Uint16(output[ifd0Off:]))
	pos := ifd0Off + 2

	for i := range ifd0Count {
		e := pos + i*12
		if e+12 > len(output) {
			break
		}
		tag := order.Uint16(output[e:])
		if tag != 0x014A {
			continue
		}

		// Found SubIFDs entry.
		entryCount := int(order.Uint32(output[e+4:]))
		total := uint64(4) * uint64(entryCount) //nolint:gosec // G115: entryCount from int(Uint32()), bounded by file size

		var subIFDOffsets []uint32
		if total <= 4 && entryCount == 1 {
			subIFDOffsets = []uint32{order.Uint32(output[e+8:])}
		} else {
			arrOff := int(order.Uint32(output[e+8:]))
			if arrOff+entryCount*4 > len(output) {
				t.Errorf("SubIFDs array at %d exceeds output len %d", arrOff, len(output))
				return
			}
			subIFDOffsets = make([]uint32, entryCount)
			for j := range entryCount {
				subIFDOffsets[j] = order.Uint32(output[arrOff+j*4:])
			}
		}

		// Check each SubIFD.
		for idx, siOff := range subIFDOffsets {
			assertSubIFDBlocksRelocated(t, base, output, siOff, order, idx)
		}
		return
	}
	t.Error("assertSubIFDsRelocated: 0x014A SubIFDs entry not found in output IFD0")
}

// assertSubIFDBlocksRelocated verifies that a single SubIFD's strip/tile blocks
// are correctly relocated: bytes at the new offset equal the original block data.
func assertSubIFDBlocksRelocated(t *testing.T, base, output []byte, subIFDOff uint32, order binary.ByteOrder, idx int) {
	t.Helper()

	// (b) The SubIFD pointer must address a valid IFD in the output.
	if uint64(subIFDOff)+2 > uint64(len(output)) {
		t.Errorf("SubIFD[%d] offset %d is out of bounds (output len %d)", idx, subIFDOff, len(output))
		return
	}
	subCount := int(order.Uint16(output[subIFDOff:]))
	if subCount == 0 || subCount > 200 {
		t.Errorf("SubIFD[%d] at offset %d: implausible entry count %d", idx, subIFDOff, subCount)
		return
	}

	// Parse SubIFD entries from the output to get the new strip/tile offsets.
	outIFD, _, ok := exif.ParseIFDAt(output, subIFDOff, order)
	if !ok || outIFD == nil {
		t.Errorf("SubIFD[%d] at offset %d: ParseSingleIFD failed on output", idx, subIFDOff)
		return
	}

	// Parse SubIFD entries from the base to find the original block data.
	// We scan the base to find a SubIFD whose blocks match the expected content.
	// (We don't know the original SubIFD position after relocation, so we search
	// by content — this is the most robust verification approach.)

	// (c) Check strip blocks.
	outStripOff := outIFD.Get(exif.TagStripOffsets)
	outStripCnt := outIFD.Get(exif.TagStripByteCounts)
	if outStripOff != nil && outStripCnt != nil {
		n := int(outStripOff.Count)
		elemSz := int(typeSize(uint16(outStripOff.Type)))
		cntElemSz := int(typeSize(uint16(outStripCnt.Type)))
		if elemSz > 0 && cntElemSz > 0 {
			for j := range n {
				if j*elemSz+elemSz > len(outStripOff.Value) {
					break
				}
				newOff, err := readUint(outStripOff.Value[j*elemSz:], elemSz, order)
				if err != nil {
					t.Errorf("SubIFD[%d] strip[%d]: read offset: %v", idx, j, err)
					continue
				}
				newCnt, err := readUint(outStripCnt.Value[j*cntElemSz:], cntElemSz, order)
				if err != nil {
					t.Errorf("SubIFD[%d] strip[%d]: read count: %v", idx, j, err)
					continue
				}
				if newCnt == 0 {
					continue
				}
				end := newOff + newCnt
				if end > uint64(len(output)) {
					t.Errorf("SubIFD[%d] strip[%d]: offset %d+%d exceeds output len %d", idx, j, newOff, newCnt, len(output))
					continue
				}
				newBlock := output[newOff:end]
				// Find this block in base by content.
				if !bytes.Contains(base, newBlock) {
					t.Errorf("SubIFD[%d] strip[%d] at new offset %d (size %d): bytes not found verbatim in base",
						idx, j, newOff, newCnt)
				}
			}
		}
	}

	// (c) Check tile blocks.
	outTileOff := outIFD.Get(exif.TagTileOffsets)
	outTileCnt := outIFD.Get(exif.TagTileByteCounts)
	if outTileOff != nil && outTileCnt != nil {
		n := int(outTileOff.Count)
		elemSz := int(typeSize(uint16(outTileOff.Type)))
		cntElemSz := int(typeSize(uint16(outTileCnt.Type)))
		if elemSz > 0 && cntElemSz > 0 {
			for j := range n {
				if j*elemSz+elemSz > len(outTileOff.Value) {
					break
				}
				newOff, err := readUint(outTileOff.Value[j*elemSz:], elemSz, order)
				if err != nil {
					t.Errorf("SubIFD[%d] tile[%d]: read offset: %v", idx, j, err)
					continue
				}
				newCnt, err := readUint(outTileCnt.Value[j*cntElemSz:], cntElemSz, order)
				if err != nil {
					t.Errorf("SubIFD[%d] tile[%d]: read count: %v", idx, j, err)
					continue
				}
				if newCnt == 0 {
					continue
				}
				end := newOff + newCnt
				if end > uint64(len(output)) {
					t.Errorf("SubIFD[%d] tile[%d]: offset %d+%d exceeds output len %d", idx, j, newOff, newCnt, len(output))
					continue
				}
				newBlock := output[newOff:end]
				if !bytes.Contains(base, newBlock) {
					t.Errorf("SubIFD[%d] tile[%d] at new offset %d (size %d): bytes not found verbatim in base",
						idx, j, newOff, newCnt)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test (SubIFD-1): single SubIFD with strip image — DNG canonical structure
// ---------------------------------------------------------------------------

// TestSubIFDRelocateSingleSubIFDStrip is the primary DNG SubIFD test.
//
// It builds a DNG-like TIFF (IFD0 thumbnail + SubIFD0 full-res strip), injects
// IPTC+XMP, and verifies all four criteria:
//
//	(a) IFD0 thumbnail strips byte-identical at new offset.
//	(b) 0x014A pointer addresses a valid SubIFD in the output.
//	(c) SubIFD0 strips byte-identical at new offset.
//	(d) Output re-parses; injected metadata is readable.
//
// This is the proof test for task #94 SubIFD relocation.
func TestSubIFDRelocateSingleSubIFDStrip(t *testing.T) {
	t.Parallel()

	thumbStrip := []byte("THUMB-STRIP-DNG-CANONICAL-THUMBNAIL-DATA")
	fullStrip := []byte("FULLRES-STRIP-DNG-CANONICAL-FULL-RESOLUTION-IMAGE-DATA")

	original := buildDNGLikeTIFF(thumbStrip, fullStrip)

	newIPTC := []byte("iptc-dng-subifd-test-payload-long-enough")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"><rdf:RDF/></xmpmeta>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject DNG-like single SubIFD: %v", err)
	}

	output := out.Bytes()

	// (d) re-parse and verify metadata.
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after DNG-like SubIFD Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// (a) IFD0 thumbnail strip byte-identical.
	if !bytes.Contains(output, thumbStrip) {
		t.Error("thumbnail strip data not found verbatim in output")
	}

	// (c) full-res strip byte-identical.
	if !bytes.Contains(output, fullStrip) {
		t.Error("full-res strip data not found verbatim in output")
	}

	// (a)+(b)+(c) comprehensive structural check.
	assertSubIFDsRelocated(t, original, output)

	// Additional: verify IFD0 strip offset points at thumb data exactly.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse output: %v", err)
	}
	var orderB binary.ByteOrder = binary.LittleEndian
	if e.ByteOrder == binary.BigEndian {
		orderB = binary.BigEndian
	}
	stripOff := e.IFD0.Get(exif.TagStripOffsets)
	stripCnt := e.IFD0.Get(exif.TagStripByteCounts)
	if stripOff == nil || stripCnt == nil {
		t.Fatal("IFD0 StripOffsets/StripByteCounts missing from output")
	}
	ifd0Offset := orderB.Uint32(stripOff.Value)
	ifd0Size := orderB.Uint32(stripCnt.Value)
	if int(ifd0Offset)+int(ifd0Size) > len(output) {
		t.Fatalf("IFD0 strip offset+size exceeds output length")
	}
	// THE KEY ASSERTION (a): bytes at IFD0 strip offset == original thumb data.
	if !bytes.Equal(output[ifd0Offset:ifd0Offset+ifd0Size], thumbStrip) {
		t.Errorf("IFD0 thumbnail at new offset: bytes differ\n  got:  %x\n  want: %x",
			output[ifd0Offset:ifd0Offset+ifd0Size], thumbStrip)
	}
}

// ---------------------------------------------------------------------------
// Test (SubIFD-2): exact byte-at-offset identity for both blocks
// ---------------------------------------------------------------------------

// TestSubIFDExactByteAtOffset is the explicit bytes-at-new-offset ==
// original-block proof for both the IFD0 thumbnail and the SubIFD full-res strip.
//
// For each block: locate the new offset in output, compare byte-for-byte with
// the original data. This is the direct evidence required by the acceptance
// criteria "SubIFD block-integrity test evidence".
func TestSubIFDExactByteAtOffset(t *testing.T) { //nolint:paralleltest // uses AllocsPerRun pattern — not parallel-safe
	thumbStrip := []byte("THUMB_EXACT_BYTE_IDENTITY_CHECK!")
	fullStrip := []byte("FULLRES_EXACT_BYTE_IDENTITY_CHECK")

	original := buildDNGLikeTIFF(thumbStrip, fullStrip)

	newIPTC := []byte("iptc-exact-byte-dng-subifd-check")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	output := out.Bytes()

	var orderB binary.ByteOrder = binary.LittleEndian
	if len(output) >= 2 && output[0] == 'M' && output[1] == 'M' {
		orderB = binary.BigEndian
	}

	// Locate IFD0 strip offset.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse output: %v", err)
	}
	ifd0SOff := e.IFD0.Get(exif.TagStripOffsets)
	ifd0SCnt := e.IFD0.Get(exif.TagStripByteCounts)
	if ifd0SOff == nil || ifd0SCnt == nil {
		t.Fatal("IFD0 StripOffsets/StripByteCounts missing")
	}
	thumbNewOff := orderB.Uint32(ifd0SOff.Value)
	thumbNewCnt := orderB.Uint32(ifd0SCnt.Value)
	if int(thumbNewOff)+int(thumbNewCnt) > len(output) {
		t.Fatalf("IFD0 thumb block out of output bounds")
	}
	// KEY ASSERTION (a): thumbnail bytes at new offset == original.
	if got := output[thumbNewOff : thumbNewOff+thumbNewCnt]; !bytes.Equal(got, thumbStrip) {
		t.Errorf("IFD0 thumbnail at new offset %d: bytes differ\n  got:  %x\n  want: %x",
			thumbNewOff, got, thumbStrip)
	}

	// Locate SubIFD0 strip offset.
	ifd0Off := int(orderB.Uint32(output[4:]))
	if ifd0Off+2 > len(output) {
		t.Fatalf("ifd0Off out of bounds")
	}
	ifd0Count := int(orderB.Uint16(output[ifd0Off:]))
	pos := ifd0Off + 2
	var subIFD0NewOff uint32
	found := false
	for i := range ifd0Count {
		e2 := pos + i*12
		if e2+12 > len(output) {
			break
		}
		if orderB.Uint16(output[e2:]) == 0x014A {
			subIFD0NewOff = orderB.Uint32(output[e2+8:])
			found = true
			break
		}
	}
	if !found {
		t.Fatal("0x014A SubIFDs tag not found in IFD0 of output")
	}

	subIFD, _, ok := exif.ParseIFDAt(output, subIFD0NewOff, orderB)
	if !ok || subIFD == nil {
		t.Fatalf("ParseSingleIFD at SubIFD0 offset %d failed", subIFD0NewOff)
	}

	subSOff := subIFD.Get(exif.TagStripOffsets)
	subSCnt := subIFD.Get(exif.TagStripByteCounts)
	if subSOff == nil || subSCnt == nil {
		t.Fatal("SubIFD0 StripOffsets/StripByteCounts missing")
	}
	fullNewOff := orderB.Uint32(subSOff.Value)
	fullNewCnt := orderB.Uint32(subSCnt.Value)
	if int(fullNewOff)+int(fullNewCnt) > len(output) {
		t.Fatalf("SubIFD0 full-res block out of output bounds")
	}
	// KEY ASSERTION (c): full-res bytes at new offset == original.
	if got := output[fullNewOff : fullNewOff+fullNewCnt]; !bytes.Equal(got, fullStrip) {
		t.Errorf("SubIFD0 full-res at new offset %d: bytes differ\n  got:  %x\n  want: %x",
			fullNewOff, got, fullStrip)
	}
}

// ---------------------------------------------------------------------------
// Test (SubIFD-3): two SubIFDs (multi-SubIFD DNG-like)
// ---------------------------------------------------------------------------

// TestSubIFDRelocateMultiSubIFD verifies relocation of a DNG-like TIFF with
// two SubIFDs: SubIFD0 (full-res) and SubIFD1 (preview).
//
// Covers the case where 0x014A count > 1 and the pointer array is stored
// out-of-line (2×4 = 8 bytes > 4 byte inline threshold).
func TestSubIFDRelocateMultiSubIFD(t *testing.T) {
	t.Parallel()

	thumbStrip := []byte("THUMB-MULTI-SUBIFD-DNG-DATA-ABCD")
	fullStrip0 := []byte("FULLRES0-MULTI-SUBIFD-DNG-FULL-RES-IMAGE-DATA")
	previewStrip := []byte("PREVIEW-MULTI-SUBIFD-DNG-PREVIEW-DATA-XY")

	original := buildDNGLikeMultiSubIFD(thumbStrip, fullStrip0, previewStrip)

	newIPTC := []byte("iptc-multi-subifd-dng-test-payload")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject DNG-like multi-SubIFD: %v", err)
	}

	output := out.Bytes()

	// re-parse metadata.
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after multi-SubIFD Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// All three image blocks must appear verbatim.
	if !bytes.Contains(output, thumbStrip) {
		t.Error("thumbnail strip not found verbatim in output")
	}
	if !bytes.Contains(output, fullStrip0) {
		t.Error("full-res strip0 not found verbatim in output")
	}
	if !bytes.Contains(output, previewStrip) {
		t.Error("preview strip not found verbatim in output")
	}

	// Structural check: IFD0 block + both SubIFD blocks.
	assertSubIFDsRelocated(t, original, output)
}

// ---------------------------------------------------------------------------
// Test (SubIFD-4): SubIFD with tiles
// ---------------------------------------------------------------------------

// TestSubIFDRelocateTiledSubIFD verifies relocation of a DNG-like TIFF where
// SubIFD0 uses tiles (TileOffsets/TileByteCounts).
func TestSubIFDRelocateTiledSubIFD(t *testing.T) {
	t.Parallel()

	thumbStrip := []byte("THUMB-TILED-SUBIFD-DNG-DATA-EFGH")
	tile0 := []byte("TILE0-SUBIFD-DNG-TILED-IMAGE-DATA-AAAA")
	tile1 := []byte("TILE1-SUBIFD-DNG-TILED-IMAGE-DATA-BBBB")

	original := buildDNGLikeTiledSubIFD(thumbStrip, tile0, tile1)

	newIPTC := []byte("iptc-tiled-subifd-test-payload-dng")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject DNG-like tiled SubIFD: %v", err)
	}

	output := out.Bytes()

	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after tiled SubIFD Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	if !bytes.Contains(output, thumbStrip) {
		t.Error("thumbnail strip not found verbatim in output")
	}
	if !bytes.Contains(output, tile0) {
		t.Error("tile0 not found verbatim in output")
	}
	if !bytes.Contains(output, tile1) {
		t.Error("tile1 not found verbatim in output")
	}

	assertSubIFDsRelocated(t, original, output)
}

// ---------------------------------------------------------------------------
// Test (SubIFD-5): SubIFD with multiple strips (COUNT > 1)
// ---------------------------------------------------------------------------

// TestSubIFDRelocateMultiStripSubIFD verifies relocation of a DNG-like TIFF
// where SubIFD0 has two strips (COUNT > 1).
func TestSubIFDRelocateMultiStripSubIFD(t *testing.T) {
	t.Parallel()

	thumbStrip := []byte("THUMB-MULTISTRIP-SUBIFD-DNG-IJKL")
	fullStrip0 := []byte("FULLSTRIP0-SUBIFD-DNG-MULTISTRIP-PART-A")
	fullStrip1 := []byte("FULLSTRIP1-SUBIFD-DNG-MULTISTRIP-PART-B")

	original := buildDNGLikeMultiStripSubIFD(thumbStrip, fullStrip0, fullStrip1)

	newIPTC := []byte("iptc-multistrip-subifd-test-payload")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject DNG-like multi-strip SubIFD: %v", err)
	}

	output := out.Bytes()

	_, gotIPTC, _, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after multi-strip SubIFD Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}

	if !bytes.Contains(output, thumbStrip) {
		t.Error("thumbnail strip not found verbatim in output")
	}
	if !bytes.Contains(output, fullStrip0) {
		t.Error("fullStrip0 not found verbatim in output")
	}
	if !bytes.Contains(output, fullStrip1) {
		t.Error("fullStrip1 not found verbatim in output")
	}

	assertSubIFDsRelocated(t, original, output)
}

// ---------------------------------------------------------------------------
// Test (SubIFD-6): fail-before / pass-after evidence
// ---------------------------------------------------------------------------

// TestSubIFDFailBeforePassAfter is the explicit regression test that
// demonstrates the old code would have corrupted the SubIFD offsets while the
// new code produces correct output.
//
// Before task #94, enumerateImageBlocks did NOT follow 0x014A SubIFDs; after
// re-encoding the main EXIF block, the SubIFD pointer in IFD0 would still hold
// the old absolute offset from the original file, and that SubIFD's strip
// offsets would also be stale. The full-res image would be unreadable.
//
// This test verifies the new code produces valid output by checking that the
// SubIFD pointer and its strip offsets are correct in the output.
func TestSubIFDFailBeforePassAfter(t *testing.T) {
	t.Parallel()

	thumbStrip := []byte("THUMB-FAIL-BEFORE-PASS-AFTER-TEST")
	fullStrip := []byte("FULLRES-FAIL-BEFORE-PASS-AFTER-TEST-DATA")

	original := buildDNGLikeTIFF(thumbStrip, fullStrip)

	// Record the original SubIFD position (this is what the old code would
	// have left stale in the output).
	var origOrderB binary.ByteOrder = binary.LittleEndian
	origIFD0Off := int(origOrderB.Uint32(original[4:]))
	origIFD0Count := int(origOrderB.Uint16(original[origIFD0Off:]))
	var origSubIFDOff uint32
	for i := range origIFD0Count {
		e := origIFD0Off + 2 + i*12
		if e+12 > len(original) {
			break
		}
		if origOrderB.Uint16(original[e:]) == 0x014A {
			origSubIFDOff = origOrderB.Uint32(original[e+8:])
			break
		}
	}

	newIPTC := []byte("iptc-fail-before-pass-after-payload")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	output := out.Bytes()

	// In the new output, the SubIFD pointer must NOT be the old offset.
	// (The old offset no longer makes sense because the EXIF block was rebuilt.)
	var outputOrderB binary.ByteOrder = binary.LittleEndian
	outIFD0Off := int(outputOrderB.Uint32(output[4:]))
	outIFD0Count := int(outputOrderB.Uint16(output[outIFD0Off:]))
	var newSubIFDOff uint32
	found := false
	for i := range outIFD0Count {
		e := outIFD0Off + 2 + i*12
		if e+12 > len(output) {
			break
		}
		if outputOrderB.Uint16(output[e:]) == 0x014A {
			newSubIFDOff = outputOrderB.Uint32(output[e+8:])
			found = true
			break
		}
	}
	if !found {
		t.Fatal("0x014A SubIFDs tag not found in output IFD0")
	}

	// The new SubIFD offset must differ from the original (block was relocated).
	if newSubIFDOff == origSubIFDOff {
		t.Errorf("SubIFD pointer was NOT relocated: still points to original offset %d (FAIL-BEFORE condition)", origSubIFDOff)
	}

	// The new pointer must address a valid IFD.
	subIFD, _, ok := exif.ParseIFDAt(output, newSubIFDOff, outputOrderB)
	if !ok || subIFD == nil {
		t.Fatalf("SubIFD at new offset %d cannot be parsed (PASS-AFTER failed)", newSubIFDOff)
	}

	// The SubIFD's strip data must be byte-identical to the original.
	subSOff := subIFD.Get(exif.TagStripOffsets)
	subSCnt := subIFD.Get(exif.TagStripByteCounts)
	if subSOff == nil || subSCnt == nil {
		t.Fatal("SubIFD StripOffsets/StripByteCounts missing in output")
	}
	newFullOff := outputOrderB.Uint32(subSOff.Value)
	newFullCnt := outputOrderB.Uint32(subSCnt.Value)
	if int(newFullOff)+int(newFullCnt) > len(output) {
		t.Fatalf("SubIFD strip block out of output bounds")
	}
	if got := output[newFullOff : newFullOff+newFullCnt]; !bytes.Equal(got, fullStrip) {
		t.Errorf("SubIFD full-res at new offset %d: bytes differ (PASS-AFTER failed)\n  got:  %x\n  want: %x",
			newFullOff, got, fullStrip)
	}

	// Also confirm the thumbnail is byte-identical.
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse output: %v", err)
	}
	ifd0SOff := e.IFD0.Get(exif.TagStripOffsets)
	ifd0SCnt := e.IFD0.Get(exif.TagStripByteCounts)
	if ifd0SOff == nil || ifd0SCnt == nil {
		t.Fatal("IFD0 StripOffsets/StripByteCounts missing in output")
	}
	thumbNewOff := outputOrderB.Uint32(ifd0SOff.Value)
	thumbNewCnt := outputOrderB.Uint32(ifd0SCnt.Value)
	if int(thumbNewOff)+int(thumbNewCnt) > len(output) {
		t.Fatalf("IFD0 thumb block out of output bounds")
	}
	if got := output[thumbNewOff : thumbNewOff+thumbNewCnt]; !bytes.Equal(got, thumbStrip) {
		t.Errorf("IFD0 thumbnail at new offset %d: bytes differ\n  got:  %x\n  want: %x",
			thumbNewOff, got, thumbStrip)
	}
}

// ---------------------------------------------------------------------------
// Test (SubIFD-7): SubIFD out-of-line RATIONAL values survive relocation (bug #98)
// ---------------------------------------------------------------------------

// buildDNGLikeWithRationalSubIFD builds a synthetic DNG-like TIFF where the
// SubIFD contains out-of-line RATIONAL values (XResolution 0x011A, YResolution
// 0x011B) in addition to strip image data. This is the canonical DNG real-world
// pattern that exposed bug #98 (Pentax QS1.dng).
//
// Structure:
//
//	TIFF header (LE)
//	IFD0:
//	  ImageWidth, ImageLength, StripOffsets→thumbStrip, StripByteCounts, SubIFDs→SubIFD0
//	SubIFD0:
//	  ImageWidth, ImageLength,
//	  XResolution (0x011A, TypeRATIONAL, count=1) — OOL: 8 bytes, value = numer/denom
//	  YResolution (0x011B, TypeRATIONAL, count=1) — OOL: 8 bytes, value = numer/denom
//	  StripOffsets (0x0111), StripByteCounts (0x0117)
//	RationalArea (XRes 8 bytes + YRes 8 bytes = 16 bytes)
//	thumbStrip bytes
//	fullStrip bytes
//
// TIFF 6.0 §2: RATIONAL = two LONGs (numerator/denominator), each 4 bytes =
// 8 bytes total. Since 8 > 4, the value is stored out-of-line; the entry's
// valOrOff field points to the 8-byte value area.
//
// Spec reference: TIFF 6.0 §8: XResolution tag 0x011A, YResolution tag 0x011B,
// both TypeRATIONAL (type code 5), Count=1.
func buildDNGLikeWithRationalSubIFD(thumbStrip, fullStrip []byte, xResDenom, yResDenom uint32) []byte {
	order := binary.LittleEndian

	// IFD0: 5 entries (ImageWidth, ImageLength, StripOffsets, StripByteCounts, SubIFDs).
	nIFD0 := 5

	// SubIFD0: 6 entries sorted by tag:
	//   0x0100 ImageWidth
	//   0x0101 ImageLength
	//   0x011A XResolution  (TypeRATIONAL=5, Count=1, OOL)
	//   0x011B YResolution  (TypeRATIONAL=5, Count=1, OOL)
	//   0x0111 StripOffsets  (TypeLong, Count=1, inline)
	//   0x0117 StripByteCounts (TypeLong, Count=1, inline)
	// NOTE: entries must be sorted ascending by tag for TIFF compliance.
	// Sorted order: 0x0100, 0x0101, 0x0111, 0x0117, 0x011A, 0x011B
	nSubIFD0 := 6

	// Layout:
	//   [0..7]        TIFF header
	//   [8..]         IFD0: 2 + 5×12 + 4 = 70 bytes @ 8
	//   [78..]        SubIFD0: 2 + 6×12 + 4 = 78 bytes @ 78
	//   [156..]       XResolution value area: 8 bytes (numerator=300, denominator=xResDenom)
	//   [164..]       YResolution value area: 8 bytes (numerator=300, denominator=yResDenom)
	//   [172..]       thumbStrip
	//   [172+ts..]    fullStrip
	const headerSize = 8
	ifd0Off := headerSize
	subIFD0Off := ifd0Off + 2 + nIFD0*12 + 4         // 78
	xResValueOff := subIFD0Off + 2 + nSubIFD0*12 + 4 // 156
	yResValueOff := xResValueOff + 8                 // 164
	thumbOff := yResValueOff + 8                     // 172
	fullOff := thumbOff + len(thumbStrip)
	total := fullOff + len(fullStrip)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], uint32(ifd0Off))

	// --- IFD0 ---
	order.PutUint16(buf[ifd0Off:], uint16(nIFD0))
	p := ifd0Off + 2
	writeEntry := func(tag, typ uint16, count, val uint32) { //nolint:unparam // typ is always 4 for IFD0; kept as param for clarity
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
		p += 12
	}
	writeEntry(0x0100, 4, 1, 4) // ImageWidth
	writeEntry(0x0101, 4, 1, 1) // ImageLength
	writeEntry(0x0111, 4, 1, uint32(thumbOff))
	writeEntry(0x0117, 4, 1, uint32(len(thumbStrip))) //nolint:gosec // G115: len always non-negative
	writeEntry(0x014A, 4, 1, uint32(subIFD0Off))
	p += 4 // IFD0 next-IFD = 0

	// --- SubIFD0 ---
	// Entries sorted ascending by tag: 0x0100, 0x0101, 0x0111, 0x0117, 0x011A, 0x011B
	order.PutUint16(buf[subIFD0Off:], uint16(nSubIFD0))
	q := subIFD0Off + 2
	writeEntryAt := func(pos int, tag, typ uint16, count, val uint32) { //nolint:unparam // count always 1 for scalar entries in this fixture
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], typ)
		order.PutUint32(buf[pos+4:], count)
		order.PutUint32(buf[pos+8:], val)
	}
	// TIFF type 4 = LONG (4 bytes), type 5 = RATIONAL (8 bytes).
	writeEntryAt(q, 0x0100, 4, 1, 1024)                      // ImageWidth
	writeEntryAt(q+12, 0x0101, 4, 1, 768)                    // ImageLength
	writeEntryAt(q+24, 0x0111, 4, 1, uint32(fullOff))        //nolint:gosec // G115: fullOff bounded by buf
	writeEntryAt(q+36, 0x0117, 4, 1, uint32(len(fullStrip))) //nolint:gosec // G115: len always non-negative
	// XResolution: TypeRATIONAL (5), Count=1, OOL → xResValueOff
	writeEntryAt(q+48, 0x011A, 5, 1, uint32(xResValueOff))
	// YResolution: TypeRATIONAL (5), Count=1, OOL → yResValueOff
	writeEntryAt(q+60, 0x011B, 5, 1, uint32(yResValueOff))
	// SubIFD0 next-IFD = 0 (already zero)

	// --- RATIONAL value areas ---
	// XResolution = 300/xResDenom
	order.PutUint32(buf[xResValueOff:], 300)
	order.PutUint32(buf[xResValueOff+4:], xResDenom)
	// YResolution = 300/yResDenom
	order.PutUint32(buf[yResValueOff:], 300)
	order.PutUint32(buf[yResValueOff+4:], yResDenom)

	// --- Image data ---
	copy(buf[thumbOff:], thumbStrip)
	copy(buf[fullOff:], fullStrip)

	return buf
}

// TestSubIFDRationalValuesPreservedOnRelocation is the regression test for
// task #98 (SubIFD OOL value fix): out-of-line RATIONAL values (XResolution,
// YResolution) were silently lost after DNG write because patchRawIFDOffsets
// only updated the valOrOff pointer for strip/tile image-data tags, leaving
// RATIONAL and other OOL entries pointing at stale original-file offsets.
//
// After the fix, patchRawIFDOffsets updates the valOrOff field for EVERY OOL
// entry in the SubIFD — not just strip/tile offset arrays — so RATIONAL, SRATIONAL,
// DOUBLE, long ASCII, and similar entries round-trip correctly.
//
// Test structure:
//  1. Build a synthetic DNG-like TIFF with a SubIFD that carries:
//     - XResolution = 300/1 (RATIONAL, OOL, 8 bytes)
//     - YResolution = 300/1 (RATIONAL, OOL, 8 bytes)
//     - StripOffsets / StripByteCounts (image block)
//  2. Inject IPTC+XMP (forces the copy-and-relocate path).
//  3. Re-parse the output and verify:
//     (a) SubIFD XResolution == 300/1 (not undef, not stale).
//     (b) SubIFD YResolution == 300/1 (not undef, not stale).
//     (c) Image block bytes are verbatim-identical.
//     (d) IFD0 thumbnail block bytes are verbatim-identical.
func TestSubIFDRationalValuesPreservedOnRelocation(t *testing.T) {
	t.Parallel()

	thumbStrip := []byte("THUMB-BUG98-RATIONAL-REGRESSION-TEST-DATA!")
	fullStrip := []byte("FULLRES-BUG98-RATIONAL-REGRESSION-FULL-IMAGE-DATA")

	// XResolution = 300/1, YResolution = 300/1 (typical DPI setting for DNGs).
	const xResDenom uint32 = 1
	const yResDenom uint32 = 1

	original := buildDNGLikeWithRationalSubIFD(thumbStrip, fullStrip, xResDenom, yResDenom)

	newIPTC := []byte("iptc-bug98-regression-rational-subifd")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"><rdf:RDF/></xmpmeta>")

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, newXMP, true); err != nil {
		t.Fatalf("Inject (bug #98 regression): %v", err)
	}

	output := out.Bytes()

	// (d) Re-parse and verify injected metadata round-trips.
	_, gotIPTC, gotXMP, err := Extract(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("Extract after bug-#98 Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC: got %q, want %q", gotIPTC, newIPTC)
	}
	if !bytes.Equal(gotXMP, newXMP) {
		t.Errorf("XMP: got %q, want %q", gotXMP, newXMP)
	}

	// Structural check: IFD0 thumbnail + SubIFD image block both byte-identical.
	if !bytes.Contains(output, thumbStrip) {
		t.Error("bug #98: thumbnail strip data not found verbatim in output")
	}
	if !bytes.Contains(output, fullStrip) {
		t.Error("bug #98: full-res strip data not found verbatim in output")
	}
	assertSubIFDsRelocated(t, original, output)

	// THE KEY ASSERTIONS (a)+(b): RATIONAL XResolution and YResolution must
	// survive relocation with their original numerator/denominator values.
	//
	// Parse the SubIFD from the output and verify the RATIONAL entries directly.
	var order binary.ByteOrder = binary.LittleEndian
	if len(output) >= 2 && output[0] == 'M' && output[1] == 'M' {
		order = binary.BigEndian
	}

	ifd0Off := int(order.Uint32(output[4:]))
	if ifd0Off+2 > len(output) {
		t.Fatalf("bug #98: ifd0Off %d out of bounds", ifd0Off)
	}
	ifd0Count := int(order.Uint16(output[ifd0Off:]))

	var subIFD0Off uint32
	for i := range ifd0Count {
		e := ifd0Off + 2 + i*12
		if e+12 > len(output) {
			break
		}
		if order.Uint16(output[e:]) == 0x014A {
			subIFD0Off = order.Uint32(output[e+8:])
			break
		}
	}
	if subIFD0Off == 0 {
		t.Fatal("bug #98: 0x014A SubIFDs tag not found in output IFD0")
	}

	parsedSubIFD, _, ok := exif.ParseIFDAt(output, subIFD0Off, order)
	if !ok || parsedSubIFD == nil {
		t.Fatalf("bug #98: ParseIFDAt at SubIFD offset %d failed on output", subIFD0Off)
	}

	// (a) Verify XResolution (0x011A).
	xResEntry := parsedSubIFD.Get(exif.TagXResolution)
	if xResEntry == nil {
		t.Error("bug #98: SubIFD XResolution (0x011A) is nil in output — RATIONAL value was lost")
	} else {
		// TypeRATIONAL = 8 bytes: numerator (4 bytes) + denominator (4 bytes).
		if len(xResEntry.Value) < 8 {
			t.Errorf("bug #98: SubIFD XResolution value too short: %d bytes (want 8)", len(xResEntry.Value))
		} else {
			numer := order.Uint32(xResEntry.Value[0:])
			denom := order.Uint32(xResEntry.Value[4:])
			if numer != 300 || denom != xResDenom {
				t.Errorf("bug #98: SubIFD XResolution = %d/%d, want 300/%d (value corrupted after relocation)",
					numer, denom, xResDenom)
			}
		}
	}

	// (b) Verify YResolution (0x011B).
	yResEntry := parsedSubIFD.Get(exif.TagYResolution)
	if yResEntry == nil {
		t.Error("bug #98: SubIFD YResolution (0x011B) is nil in output — RATIONAL value was lost")
	} else {
		if len(yResEntry.Value) < 8 {
			t.Errorf("bug #98: SubIFD YResolution value too short: %d bytes (want 8)", len(yResEntry.Value))
		} else {
			numer := order.Uint32(yResEntry.Value[0:])
			denom := order.Uint32(yResEntry.Value[4:])
			if numer != 300 || denom != yResDenom {
				t.Errorf("bug #98: SubIFD YResolution = %d/%d, want 300/%d (value corrupted after relocation)",
					numer, denom, yResDenom)
			}
		}
	}

	// (c) The full-res strip bytes must be at exactly the right offset in the output.
	subSOff := parsedSubIFD.Get(exif.TagStripOffsets)
	subSCnt := parsedSubIFD.Get(exif.TagStripByteCounts)
	if subSOff == nil || subSCnt == nil {
		t.Fatal("bug #98: SubIFD StripOffsets/StripByteCounts missing in output")
	}
	elemSz := int(typeSize(uint16(subSOff.Type)))
	cntSz := int(typeSize(uint16(subSCnt.Type)))
	if elemSz > 0 && cntSz > 0 && len(subSOff.Value) >= elemSz && len(subSCnt.Value) >= cntSz {
		newFullOff, _ := readUint(subSOff.Value, elemSz, order)
		newFullCnt, _ := readUint(subSCnt.Value, cntSz, order)
		end := newFullOff + newFullCnt
		if end > uint64(len(output)) {
			t.Fatalf("bug #98: SubIFD full-res block %d+%d exceeds output len %d", newFullOff, newFullCnt, len(output))
		}
		if !bytes.Equal(output[newFullOff:end], fullStrip) {
			t.Errorf("bug #98: SubIFD full-res strip bytes differ at new offset %d", newFullOff)
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

// BenchmarkRelocateDNGLike measures the relocateTIFF cost for a DNG-like TIFF
// with thumbnail + SubIFD full-res strip.
func BenchmarkRelocateDNGLike(b *testing.B) {
	thumbStrip := make([]byte, 512)
	fullStrip := make([]byte, 8192)
	for i := range fullStrip {
		fullStrip[i] = byte(i)
	}
	original := buildDNGLikeTIFF(thumbStrip, fullStrip)
	newIPTC := []byte("benchmark-dng-iptc-payload")
	newXMP := []byte("<xmpmeta xmlns:x=\"adobe:ns:meta/\"/>")

	b.SetBytes(int64(len(original)))
	b.ResetTimer()
	for range b.N {
		_, _ = relocateTIFF(original, newIPTC, newXMP)
	}
}
