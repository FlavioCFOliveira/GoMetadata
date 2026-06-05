package tiff

// task99_word_align_test.go — regression tests for task #99.
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// (Word = 2 bytes.)
//
// The test in this file asserts that:
//  1. Every out-of-line value offset in the output of relocateTIFF /
//     InjectWithEXIF is even (word-aligned).
//  2. Image data is byte-identical to the source (ImageDataHash IN==OUT) —
//     the alignment padding must not corrupt or displace any image bytes.
//
// Fixture: format/tiff/testdata/cramps.tif (same as realfile_test.go).

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// scanAllOOLOffsets walks every IFD reachable from IFD0 in the TIFF stream at
// tiffData and returns a slice of all out-of-line value offsets. Offsets are
// TIFF-stream-relative (i.e. absolute within tiffData).
func scanAllOOLOffsets(t *testing.T, tiffData []byte) []uint32 {
	t.Helper()
	if len(tiffData) < 8 {
		t.Errorf("TIFF stream too short (%d bytes)", len(tiffData))
		return nil
	}
	var order binary.ByteOrder
	switch {
	case tiffData[0] == 'I' && tiffData[1] == 'I':
		order = binary.LittleEndian
	case tiffData[0] == 'M' && tiffData[1] == 'M':
		order = binary.BigEndian
	default:
		t.Errorf("unknown byte-order marker in TIFF stream")
		return nil
	}

	ifd0Off := order.Uint32(tiffData[4:])

	var offsets []uint32
	visited := make(map[uint32]bool)

	var walkIFD func(off uint32)
	walkIFD = func(off uint32) {
		if off == 0 || visited[off] {
			return
		}
		visited[off] = true
		if uint64(off)+2 > uint64(len(tiffData)) {
			return
		}
		count := int(order.Uint16(tiffData[off:]))
		pos := int(off) + 2
		if pos+count*12 > len(tiffData) {
			return
		}

		for i := 0; i < count; i++ { //nolint:intrange // binary parser: loop variable is a byte-slice offset multiplier
			e := pos + i*12
			if e+12 > len(tiffData) {
				break
			}
			tag := order.Uint16(tiffData[e:])
			typ := order.Uint16(tiffData[e+2:])
			cnt := order.Uint32(tiffData[e+4:])
			valOrOff := order.Uint32(tiffData[e+8:])

			sz := typeSize(typ)
			total := uint64(sz) * uint64(cnt)
			if sz > 0 && total > 4 {
				offsets = append(offsets, valOrOff)
			}

			// Follow sub-IFD pointers.
			isPtr := tag == uint16(exif.TagExifIFDPointer) ||
				tag == uint16(exif.TagGPSIFDPointer) ||
				tag == uint16(exif.TagInteropIFDPointer)
			if isPtr && sz > 0 && total <= 4 {
				walkIFD(valOrOff)
			}
		}
		// Follow next-IFD chain.
		nextPtrPos := pos + count*12
		if nextPtrPos+4 <= len(tiffData) {
			next := order.Uint32(tiffData[nextPtrPos:])
			walkIFD(next)
		}
	}
	walkIFD(ifd0Off)
	return offsets
}

// TestWordAlignedRelocateTIFF_CrampsTIF is the regression test for task #99
// on the TIFF relocate path.
//
// It:
//  1. Loads cramps.tif.
//  2. Calls InjectWithEXIF with a modified copyright string.
//  3. Asserts all OOL value offsets in the output are even (word-aligned).
//  4. Asserts strip bytes are byte-identical to the source (ImageDataHash IN==OUT).
func TestWordAlignedRelocateTIFF_CrampsTIF(t *testing.T) {
	t.Parallel()

	original, err := os.ReadFile(crampsFixture)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("testdata/cramps.tif not present; skipping")
		}
		t.Fatalf("read fixture: %v", err)
	}

	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}

	// Snapshot strip bytes before mutation.
	origStrips := snapshotStrips(t, e)

	// Set copyright and inject.
	e.SetCopyright("(c) 2026 task-99-regression")

	var out bytes.Buffer
	if err := InjectWithEXIF(original, e, nil, nil, &out); err != nil {
		t.Fatalf("InjectWithEXIF: %v", err)
	}
	result := out.Bytes()

	// --- Assertion 1: all OOL value offsets must be even (word-aligned). ---
	// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
	oolOffsets := scanAllOOLOffsets(t, result)
	for _, off := range oolOffsets {
		if off%2 != 0 {
			t.Errorf("OOL value at odd offset %d (0x%x) — TIFF 6.0 §2 word-alignment violation", off, off)
		}
	}

	// --- Assertion 2: image data must be byte-identical (ImageDataHash IN==OUT). ---
	e2, err := exif.Parse(result)
	if err != nil {
		t.Fatalf("exif.Parse result: %v", err)
	}
	verifyImageBlocksIdentical(t, original, origStrips, result, e2)
}

// TestWordAlignedRelocateTIFF_WithXMP exercises the path where an XMP blob of
// odd byte length is injected alongside the modified EXIF, causing the IPTC
// and copyright entries to follow an odd-length XMP value area — the exact
// scenario that produced "Odd offset" warnings before the fix.
func TestWordAlignedRelocateTIFF_WithXMP(t *testing.T) {
	t.Parallel()

	original, err := os.ReadFile(crampsFixture)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("testdata/cramps.tif not present; skipping")
		}
		t.Fatalf("read fixture: %v", err)
	}

	e, err := exif.Parse(original)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	origStrips := snapshotStrips(t, e)
	e.SetCopyright("(c) 2026 task-99-regression")

	// Odd-length XMP blob: 101 bytes (odd).
	xmp := make([]byte, 101)
	copy(xmp, `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`)

	// Odd-length IPTC blob: 19 bytes.
	iptc := make([]byte, 19)
	iptc[0] = 0x1C

	var out bytes.Buffer
	if err := InjectWithEXIF(original, e, iptc, xmp, &out); err != nil {
		t.Fatalf("InjectWithEXIF: %v", err)
	}
	result := out.Bytes()

	oolOffsets := scanAllOOLOffsets(t, result)
	for _, off := range oolOffsets {
		if off%2 != 0 {
			t.Errorf("OOL value at odd offset %d (0x%x) — TIFF 6.0 §2 word-alignment violation", off, off)
		}
	}

	// Image data must be preserved (ImageDataHash IN==OUT).
	e2, err := exif.Parse(result)
	if err != nil {
		t.Fatalf("exif.Parse result: %v", err)
	}
	verifyImageBlocksIdentical(t, original, origStrips, result, e2)
}
