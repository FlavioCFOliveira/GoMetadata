package heif

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Task #282: iloc data_reference_index.
//
// ISO/IEC 14496-12 §8.11.3.2 places a 16-bit data_reference_index after the
// item_ID (and, for versions 1 and 2, after the construction_method field) in
// EVERY iloc version. The former parser omitted it, so every field after the
// first item_ID was read two bytes early: an iPhone HEIC with 53 items
// resolved 1 of them and Extract returned no EXIF/XMP at all.

// ilocTestEntry describes one item entry for buildIlocBody.
type ilocTestEntry struct {
	id         uint32
	method     uint16 // construction_method (iloc version 1 and 2 only)
	dataRefIdx uint16
	baseOffset uint64
	extents    [][2]uint64 // {offset, length}
}

// buildIlocBody serialises an iloc box BODY (no box header) per ISO/IEC
// 14496-12 §8.11.3.2 with the given version and field widths, independently
// of the production encoder.
func buildIlocBody(version uint8, offsetSize, lengthSize, baseOffsetSize, indexSize int, entries []ilocTestEntry) []byte {
	putN := func(b []byte, n int, v uint64) []byte {
		for i := n - 1; i >= 0; i-- {
			b = append(b, byte(v>>(8*uint(i)))) //nolint:gosec // G115: test helper, small loop index
		}
		return b
	}
	b := []byte{version, 0, 0, 0,
		byte(offsetSize<<4 | lengthSize),    //nolint:gosec // G115: test helper, nibble values
		byte(baseOffsetSize<<4 | indexSize)} //nolint:gosec // G115: test helper, nibble values
	if version < 2 {
		b = putN(b, 2, uint64(len(entries)))
	} else {
		b = putN(b, 4, uint64(len(entries)))
	}
	for _, e := range entries {
		if version < 2 {
			b = putN(b, 2, uint64(e.id))
		} else {
			b = putN(b, 4, uint64(e.id))
		}
		if version == 1 || version == 2 {
			b = putN(b, 2, uint64(e.method))
		}
		b = putN(b, 2, uint64(e.dataRefIdx))
		b = putN(b, baseOffsetSize, e.baseOffset)
		b = putN(b, 2, uint64(len(e.extents)))
		for i, ext := range e.extents {
			b = putN(b, indexSize, uint64(i+1))
			b = putN(b, offsetSize, ext[0])
			b = putN(b, lengthSize, ext[1])
		}
	}
	return b
}

// buildTask282File assembles ftyp + meta{hdlr?, dinf?, iinf, iloc} + mdat and
// returns the file. iinfItems lists the infe boxes; mkIloc receives the
// absolute offset of the mdat payload and returns the iloc box body; extra
// holds additional meta children (e.g. a dinf box).
func buildTask282File(infes [][]byte, extra [][]byte, mdatPayload []byte, mkIloc func(mdatData uint64) []byte) []byte {
	build := func(mdatData uint64) []byte {
		ftyp := bmffFtyp("heic", 0, "mif1")
		var metaBody []byte
		metaBody = append(metaBody, 0, 0, 0, 0)
		for _, e := range extra {
			metaBody = append(metaBody, e...)
		}
		metaBody = append(metaBody, bmffIinf(infes...)...)
		metaBody = append(metaBody, boxWithHeader("iloc", mkIloc(mdatData))...)
		out := append([]byte{}, ftyp...)
		out = append(out, bmffBox("meta", metaBody)...)
		out = append(out, bmffBox("mdat", mdatPayload)...)
		return out
	}
	first := build(0)
	// The iloc size does not depend on offset values, so one rebuild with the
	// real mdat payload offset is exact.
	return build(uint64(len(first) - len(mdatPayload))) //nolint:gosec // G115: non-negative test length
}

// TestTask282DataReferenceIndexAllVersions proves that Extract resolves the
// EXIF and XMP items of iloc boxes of every version when they are preceded by
// other items: every field after the first item_ID is aligned only if the
// data_reference_index of every item is consumed.
func TestTask282DataReferenceIndexAllVersions(t *testing.T) {
	t.Parallel()
	exif := conformanceMinimalTIFF()
	xmp := conformanceXMPPacket()
	exifBlock := append([]byte{0, 0, 0, 0}, exif...)
	imageBytes := []byte("IMAGEDATA-IMAGEDATA")
	mdat := append(append(append([]byte{}, imageBytes...), exifBlock...), xmp...)

	for _, version := range []uint8{0, 1, 2} {
		for _, widths := range [][4]int{{4, 4, 0, 0}, {8, 8, 4, 0}, {4, 4, 0, 4}} {
			if widths[3] != 0 && version == 0 {
				continue // index_size is reserved in version 0
			}
			t.Run("", func(t *testing.T) {
				t.Parallel()
				infes := [][]byte{
					bmffInfeV2(1, "hvc1"),
					bmffInfeV2(2, "hvc1"),
					bmffInfeV2(3, "Exif"),
					bmffInfeV2WithContentType(4, "mime", "application/rdf+xml"),
				}
				data := buildTask282File(infes, nil, mdat, func(mdatData uint64) []byte {
					base := uint64(0)
					if widths[2] > 0 {
						base = mdatData
						mdatData = 0
					}
					return buildIlocBody(version, widths[0], widths[1], widths[2], widths[3], []ilocTestEntry{
						{id: 1, baseOffset: base, extents: [][2]uint64{{mdatData, 9}}},
						{id: 2, baseOffset: base, extents: [][2]uint64{{mdatData + 9, 10}}},
						{id: 3, baseOffset: base, extents: [][2]uint64{{mdatData + uint64(len(imageBytes)), uint64(len(exifBlock))}}},
						{id: 4, baseOffset: base, extents: [][2]uint64{{mdatData + uint64(len(imageBytes)+len(exifBlock)), uint64(len(xmp))}}},
					})
				})
				rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("iloc v%d widths %v: Extract error: %v", version, widths, err)
				}
				if !bytes.Equal(rawEXIF, exif) {
					t.Errorf("iloc v%d widths %v: rawEXIF = %d bytes, want the %d-byte EXIF item", version, widths, len(rawEXIF), len(exif))
				}
				if !bytes.Equal(rawXMP, xmp) {
					t.Errorf("iloc v%d widths %v: rawXMP = %d bytes, want the %d-byte XMP item", version, widths, len(rawXMP), len(xmp))
				}
			})
		}
	}
}

// TestTask282IlocRoundTripPreservesDataReferenceIndex proves that the write
// path serialises data_reference_index for every version, so an iloc box
// that is parsed and rebuilt unchanged is byte-identical, and that
// ilocBoxSize matches the serialised length.
func TestTask282IlocRoundTripPreservesDataReferenceIndex(t *testing.T) {
	t.Parallel()
	for _, version := range []uint8{0, 1, 2} {
		indexSize := 4
		if version == 0 {
			indexSize = 0
		}
		body := buildIlocBody(version, 4, 8, 4, indexSize, []ilocTestEntry{
			{id: 7, dataRefIdx: 0, baseOffset: 100, extents: [][2]uint64{{1, 2}, {3, 4}}},
			{id: 9, dataRefIdx: 3, baseOffset: 200, extents: [][2]uint64{{5, 6}}},
		})
		if version > 0 {
			// construction_method 1 on the second item, written back unchanged.
			body = buildIlocBody(version, 4, 8, 4, indexSize, []ilocTestEntry{
				{id: 7, dataRefIdx: 0, baseOffset: 100, extents: [][2]uint64{{1, 2}, {3, 4}}},
				{id: 9, method: 1, dataRefIdx: 3, baseOffset: 200, extents: [][2]uint64{{5, 6}}},
			})
		}
		box := boxWithHeader("iloc", body)
		info, ok := parseIlocFull(box)
		if !ok {
			t.Fatalf("v%d: parseIlocFull failed", version)
		}
		if len(info.items) != 2 || info.items[1].dataRefIdx != 3 {
			t.Fatalf("v%d: parsed items %+v, want 2 items with item 9 data_reference_index 3", version, info.items)
		}
		rebuilt := buildIlocBox(info, info.items)
		if !bytes.Equal(rebuilt, box) {
			t.Errorf("v%d: rebuilt iloc box differs from the source:\n got %x\nwant %x", version, rebuilt, box)
		}
		if got := ilocBoxSize(info, info.items); got != len(rebuilt) {
			t.Errorf("v%d: ilocBoxSize = %d, serialised length = %d", version, got, len(rebuilt))
		}
	}
}

// TestTask282DataReferenceIndexDref proves the data_reference_index
// semantics of ISO/IEC 14496-12 §8.11.3.3: a non-zero index designates the
// file itself only when the referenced dref entry is self-contained
// (flags bit 0, §8.7.2.3); otherwise the item is external and is not read.
func TestTask282DataReferenceIndexDref(t *testing.T) {
	t.Parallel()
	exif := conformanceMinimalTIFF()
	exifBlock := append([]byte{0, 0, 0, 0}, exif...)
	dinf := func(urlFlags uint32) []byte {
		url := bmffFullBox("url ", 0, urlFlags, nil)
		dref := bmffFullBox("dref", 0, 0, append(binary.BigEndian.AppendUint32(nil, 1), url...))
		return bmffBox("dinf", dref)
	}
	for _, tc := range []struct {
		name     string
		flags    uint32
		dri      uint16
		wantEXIF bool
	}{
		{"index 0 is this file", 0, 0, true},
		{"self-contained entry", 1, 1, true},
		{"external entry", 0, 1, false},
		{"index beyond entry_count", 1, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := buildTask282File([][]byte{bmffInfeV2(1, "Exif")}, [][]byte{dinf(tc.flags)}, exifBlock,
				func(mdatData uint64) []byte {
					return buildIlocBody(1, 4, 4, 0, 0, []ilocTestEntry{
						{id: 1, dataRefIdx: tc.dri, extents: [][2]uint64{{mdatData, uint64(len(exifBlock))}}},
					})
				})
			rawEXIF, _, _, err := Extract(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Extract error: %v", err)
			}
			if got := rawEXIF != nil; got != tc.wantEXIF {
				t.Errorf("EXIF present = %v, want %v", got, tc.wantEXIF)
			}
		})
	}
}

// TestTask282MultiExtentItemIsConcatenated proves that the payload of an
// item stored in several extents is the concatenation of its extents in
// order (ISO/IEC 14496-12 §8.11.3.1), as libavif avifDecoderItemRead merges
// them. The former reader kept only the last extent.
func TestTask282MultiExtentItemIsConcatenated(t *testing.T) {
	t.Parallel()
	exif := conformanceMinimalTIFF()
	exifBlock := append([]byte{0, 0, 0, 0}, exif...)
	// Store the block as two extents in reverse physical order with a gap.
	split := 7
	mdat := append(append(append([]byte{}, exifBlock[split:]...), 0xEE, 0xEE), exifBlock[:split]...)
	data := buildTask282File([][]byte{bmffInfeV2(1, "Exif")}, nil, mdat, func(mdatData uint64) []byte {
		tail, head := uint64(len(exifBlock)-split), uint64(split) //nolint:gosec // G115: small non-negative test lengths
		return buildIlocBody(0, 4, 4, 0, 0, []ilocTestEntry{{id: 1, extents: [][2]uint64{
			{mdatData + tail + 2, head},
			{mdatData, tail},
		}}})
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if !bytes.Equal(rawEXIF, exif) {
		t.Errorf("rawEXIF = %x, want %x", rawEXIF, exif)
	}
}

// TestTask282IlocFieldSizeValidation proves that iloc field widths outside
// {0, 4, 8} (ISO/IEC 14496-12 §8.11.3.3) are rejected, as libavif does.
func TestTask282IlocFieldSizeValidation(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, 3, 5, 6, 7, 9, 15} {
		body := buildIlocBody(1, 4, 4, 0, 0, nil)
		body[4] = byte(n<<4 | 4) //nolint:gosec // G115: test value below 16
		if _, ok := parseIlocHeader(body); ok {
			t.Errorf("offset_size %d accepted, want rejection", n)
		}
	}
	if _, ok := parseIlocHeader(buildIlocBody(3, 4, 4, 0, 0, nil)); ok {
		t.Error("iloc version 3 accepted, want rejection (ISO/IEC 14496-12 defines versions 0-2)")
	}
}

// TestTask282InjectStoresPayloadsInMdatBox proves that Inject stores the new
// payloads inside a new top-level 'mdat' box (ISO/IEC 14496-12 §4.2: a file
// is a sequence of boxes) rather than as raw bytes after the last box, which
// ExifTool reports as "Unknown trailer", and that the rewritten file is a
// contiguous sequence of boxes ending exactly at EOF.
func TestTask282InjectStoresPayloadsInMdatBox(t *testing.T) {
	t.Parallel()
	// A box-structured source: ftyp + meta + mdat (buildConformanceHEIF
	// stores its payloads as raw bytes after meta, which is not a box).
	oldBlock := append([]byte{0, 0, 0, 0}, conformanceMinimalTIFF()...)
	src := buildTask282File([][]byte{bmffInfeV2(1, "Exif"), bmffInfeV2WithContentType(2, "mime", "application/rdf+xml")}, nil,
		oldBlock, func(mdatData uint64) []byte {
			return buildIlocBody(0, 4, 4, 0, 0, []ilocTestEntry{
				{id: 1, extents: [][2]uint64{{mdatData, uint64(len(oldBlock))}}},
				{id: 2, extents: [][2]uint64{{mdatData, 4}}},
			})
		})
	newEXIF := conformanceMinimalTIFF()
	newXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="w"?>`)
	for _, openEnded := range []bool{false, true} {
		in := append([]byte{}, src...)
		if openEnded {
			// Append an mdat box whose size field is 0 ("extends to EOF").
			in = append(in, 0, 0, 0, 0, 'm', 'd', 'a', 't', 1, 2, 3)
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(in), &out, newEXIF, nil, newXMP, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		got := out.Bytes()
		assertContiguousBoxes(t, got)
		// The last top-level box is the new mdat holding EXIF block + XMP.
		lastStart := lastTopLevelBox(got)
		if string(got[lastStart+4:lastStart+8]) != "mdat" {
			t.Fatalf("last box is %q, want the new mdat", got[lastStart+4:lastStart+8])
		}
		rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(got))
		if err != nil || !bytes.Equal(rawEXIF, newEXIF) || !bytes.Equal(rawXMP, newXMP) {
			t.Errorf("round trip: err=%v exif=%d xmp=%d bytes", err, len(rawEXIF), len(rawXMP))
		}
		if openEnded {
			// The formerly open-ended box got an explicit size.
			if bytes.Contains(got[len(src):lastStart], []byte{0, 0, 0, 0, 'm', 'd', 'a', 't'}) {
				t.Error("the size-0 box was not given an explicit size")
			}
		}
	}
}

// assertContiguousBoxes fails t unless data is a sequence of well-formed
// top-level boxes ending exactly at len(data).
func assertContiguousBoxes(t *testing.T, data []byte) {
	t.Helper()
	pos := 0
	for pos < len(data) {
		size, _, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok || binary.BigEndian.Uint32(data[pos:]) == 0 {
			t.Fatalf("malformed or open-ended top-level box at offset %d", pos)
		}
		pos += int(size) //nolint:gosec // G115: bounded by len(data)
	}
}

// lastTopLevelBox returns the offset of the last top-level box of data.
func lastTopLevelBox(data []byte) int {
	pos, last := 0, 0
	for pos < len(data) {
		size, _, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		last = pos
		pos += int(size) //nolint:gosec // G115: bounded by len(data)
	}
	return last
}

// TestTask282PassThroughIsVerbatim is the regression gate for a defect
// FuzzHEIFInject found (corpus entry
// testdata/fuzz/FuzzHEIFInject/e070f83062e82dec): the size field of a last
// box that extends to EOF (size 0) was made explicit before Inject decided
// whether it could rebuild the file, so a pass-through (here: iloc
// length_size 0, which cannot store a relocated length) was no longer
// byte-identical to the input.
func TestTask282PassThroughIsVerbatim(t *testing.T) {
	t.Parallel()
	in := []byte("\x00\x00\x00kmeta0000\x00\x00\x00#iinf\x0000000\x00\x00\x00\x15infe\x02000\x00\x0100Exif0\x00\x00\x000iloc\x00000\x80A\x000\x00\x01000000000000000000000000000000000000000000\x00\x00\x00\x000000")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(in), &out, []byte("EXIF"), nil, []byte("XMP"), true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if !bytes.Equal(out.Bytes(), in) {
		t.Error("a pass-through write changed the file")
	}
}
