package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// Task #309: create Exif and XMP items in HEIF/AVIF files that have none.
//
// testdata/no_metadata.avif and testdata/no_metadata.heic are 64×64 images
// written by macOS sips from an ffmpeg test pattern: one image item (ID 1,
// primary), iloc version 0, no iref box, the image data in an mdat box after
// the meta box. Creating items grows the meta box, so the image item's
// extent offset must be shifted by the growth.

// task309EXIF and task309XMP are the payloads injected by the tests.
var (
	task309EXIF = conformanceMinimalTIFF()                                                                                                                                                                          //nolint:gochecknoglobals // read-only test payload
	task309XMP  = []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="w"?>`) //nolint:gochecknoglobals // read-only test payload
)

// injectBytes runs Inject on src and returns the output.
func injectBytes(t *testing.T, src, rawEXIF, rawXMP []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, rawEXIF, nil, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

// itemBytes returns the payload of item id in file (construction method 0,
// every extent concatenated), resolved independently of the read path
// under test through parseIlocFull.
func itemBytes(t *testing.T, file []byte, id uint16) []byte {
	t.Helper()
	metaContent, err := findBox(file, boxTypeMeta, 0)
	if err != nil || metaContent == nil {
		t.Fatalf("meta box not found: %v", err)
	}
	info, ok := parseIlocFull(metaContent)
	if !ok {
		t.Fatal("iloc box not parsed")
	}
	for _, it := range info.items {
		if it.id != id {
			continue
		}
		var out []byte
		for _, e := range it.extents {
			start := it.baseOffset + e.offset
			if start+e.length > uint64(len(file)) {
				t.Fatalf("item %d extent [%d,+%d) beyond file length %d", id, start, e.length, len(file))
			}
			out = append(out, file[start:start+e.length]...)
		}
		return out
	}
	t.Fatalf("item %d not in iloc", id)
	return nil
}

// cdscTargets returns, for every 'cdsc' reference of the iref box, the
// referencing item and its targets.
func cdscTargets(t *testing.T, file []byte) map[uint32][]uint32 {
	t.Helper()
	metaContent, _ := findBox(file, boxTypeMeta, 0)
	iref := findInnerBox(metaContent, boxTypeIref)
	refs := map[uint32][]uint32{}
	if len(iref) < 4 {
		return refs
	}
	w := 2
	if iref[0] == 1 {
		w = 4
	}
	pos := 4
	for pos < len(iref) {
		size, typ, hl, ok := parseHEIFBoxHeader(iref, pos)
		if !ok {
			t.Fatalf("malformed iref child at %d", pos)
		}
		body := iref[pos+int(hl) : pos+int(size)] //nolint:gosec // G115: box size bounded by len(iref)
		if typ == [4]byte{'c', 'd', 's', 'c'} {
			from := uint32(readUintN(body, w)) //nolint:gosec // G115: item IDs are at most 32 bits wide
			n := int(binary.BigEndian.Uint16(body[w:]))
			for i := range n {
				refs[from] = append(refs[from], uint32(readUintN(body[w+2+i*w:], w))) //nolint:gosec // G115: item IDs are at most 32 bits wide
			}
		}
		pos += int(size) //nolint:gosec // G115: box size bounded by len(iref)
	}
	return refs
}

// iinfEntryCount returns the entry_count field of the iinf box and the
// number of infe boxes it holds.
func iinfEntryCount(t *testing.T, file []byte) (declared, actual int) {
	t.Helper()
	metaContent, _ := findBox(file, boxTypeMeta, 0)
	iinf := findInnerBox(metaContent, boxTypeIinf)
	count, pos, ok := parseIinfItemCount(iinf, iinf[0], 4)
	if !ok {
		t.Fatal("iinf entry_count unreadable")
	}
	for pos < len(iinf) {
		size, typ, _, ok := parseHEIFBoxHeader(iinf, pos)
		if !ok {
			break
		}
		if typ == boxTypeInfe {
			actual++
		}
		pos += int(size) //nolint:gosec // G115: box size bounded by len(iinf)
	}
	return count, actual
}

// TestTask309CreateItems creates Exif and XMP items in the metadata-free
// AVIF and HEIC fixtures and checks every structure the item needs: the
// infe entries and entry_count, the 'cdsc' references to the primary item,
// the iloc extents, and the image item, whose data must still be found at
// its (shifted) location.
func TestTask309CreateItems(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"testdata/no_metadata.avif", "testdata/no_metadata.heic"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			origImage := itemBytes(t, src, 1)
			for _, tc := range []struct {
				name           string
				exif, xmp      []byte
				wantInfeEntrys int
			}{
				{"exif and xmp", task309EXIF, task309XMP, 3},
				{"exif only", task309EXIF, nil, 2},
				{"xmp only", nil, task309XMP, 2},
			} {
				out := injectBytes(t, src, tc.exif, tc.xmp)
				assertContiguousBoxes(t, out)

				rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out))
				if err != nil {
					t.Fatalf("%s: Extract: %v", tc.name, err)
				}
				if !bytes.Equal(rawEXIF, tc.exif) || !bytes.Equal(rawXMP, tc.xmp) {
					t.Errorf("%s: read back exif=%d xmp=%d bytes, want %d and %d", tc.name, len(rawEXIF), len(rawXMP), len(tc.exif), len(tc.xmp))
				}
				if got := itemBytes(t, out, 1); !bytes.Equal(got, origImage) {
					t.Errorf("%s: image item data changed or its offset was not shifted", tc.name)
				}
				declared, actual := iinfEntryCount(t, out)
				if declared != tc.wantInfeEntrys || actual != tc.wantInfeEntrys {
					t.Errorf("%s: iinf entry_count=%d infe boxes=%d, want %d", tc.name, declared, actual, tc.wantInfeEntrys)
				}
				refs := cdscTargets(t, out)
				if len(refs) != tc.wantInfeEntrys-1 {
					t.Errorf("%s: %d cdsc references, want %d", tc.name, len(refs), tc.wantInfeEntrys-1)
				}
				for from, to := range refs {
					if len(to) != 1 || to[0] != 1 {
						t.Errorf("%s: item %d cdsc -> %v, want [1] (the primary item)", tc.name, from, to)
					}
				}
				// The Exif item carries the 4-byte TIFF header offset prefix
				// (ISO/IEC 23008-12 Annex A.2.1).
				if tc.exif != nil {
					block := itemBytes(t, out, 2)
					if binary.BigEndian.Uint32(block) != 0 || !bytes.Equal(block[4:], tc.exif) {
						t.Errorf("%s: Exif item = %x, want 00000000 + TIFF", tc.name, block[:8])
					}
				}
			}
		})
	}
}

// TestTask309RewriteDoesNotDuplicateItems proves that a second write of a
// file whose items were created by Inject replaces them instead of adding
// another pair.
func TestTask309RewriteDoesNotDuplicateItems(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/no_metadata.heic")
	if err != nil {
		t.Fatal(err)
	}
	first := injectBytes(t, src, task309EXIF, task309XMP)
	newXMP := bytes.Replace(task309XMP, []byte("rdf:RDF"), []byte("rdf:RDF "), 1)
	second := injectBytes(t, first, task309EXIF, newXMP)
	if declared, actual := iinfEntryCount(t, second); declared != 3 || actual != 3 {
		t.Errorf("second write: iinf entry_count=%d infe=%d, want 3", declared, actual)
	}
	if refs := cdscTargets(t, second); len(refs) != 2 {
		t.Errorf("second write: %d cdsc references, want 2", len(refs))
	}
	_, _, rawXMP, err := Extract(bytes.NewReader(second))
	if err != nil || !bytes.Equal(rawXMP, newXMP) {
		t.Errorf("second write: XMP = %q (%v), want the new packet", rawXMP, err)
	}
	if !bytes.Equal(itemBytes(t, second, 1), itemBytes(t, src, 1)) {
		t.Error("second write: image item data lost")
	}
}

// TestTask309NonXMPMimeItemIsNotXMP proves that a 'mime' item whose
// content_type is not "application/rdf+xml" is neither read as XMP nor
// overwritten: a separate XMP item is created and the other item is kept.
func TestTask309NonXMPMimeItemIsNotXMP(t *testing.T) {
	t.Parallel()
	other := []byte("not an XMP packet")
	data := buildTask282File([][]byte{
		bmffInfeV2(1, "hvc1"),
		bmffInfeV2WithContentType(2, "mime", "application/octet-stream"),
	}, [][]byte{bmffFullBox("pitm", 0, 0, []byte{0, 1})}, append([]byte("IMAGE"), other...), func(d uint64) []byte {
		return buildIlocBody(0, 4, 4, 0, 0, []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{d, 5}}},
			{id: 2, extents: [][2]uint64{{d + 5, uint64(len(other))}}},
		})
	})
	if _, _, rawXMP, _ := Extract(bytes.NewReader(data)); rawXMP != nil {
		t.Fatalf("a non-XMP mime item was read as XMP: %q", rawXMP)
	}
	out := injectBytes(t, data, nil, task309XMP)
	_, _, rawXMP, err := Extract(bytes.NewReader(out))
	if err != nil || !bytes.Equal(rawXMP, task309XMP) {
		t.Errorf("XMP = %q (%v), want the new packet", rawXMP, err)
	}
	if got := itemBytes(t, out, 2); !bytes.Equal(got, other) {
		t.Errorf("the non-XMP mime item was overwritten: %q", got)
	}
	if got := itemBytes(t, out, 1); !bytes.Equal(got, []byte("IMAGE")) {
		t.Errorf("image item = %q, want %q", got, "IMAGE")
	}
}

// TestTask309ShiftsChunkOffsetsAndBaseOffsets proves that every absolute
// file offset behind the meta box moves with it: stco and co64 chunk offsets
// of a track (ISO/IEC 14496-12 §8.7.5) and an iloc item addressed through
// base_offset, while an offset before the meta box is left unchanged.
func TestTask309ShiftsChunkOffsetsAndBaseOffsets(t *testing.T) {
	t.Parallel()
	payload := []byte("SAMPLE-DATA-AFTER-META")
	build := func(mdatData uint64) []byte {
		ftyp := bmffFtyp("msf1", 0, "mif1")
		metaBody := make([]byte, 0, 256)
		metaBody = append(metaBody, 0, 0, 0, 0)
		metaBody = append(metaBody, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		metaBody = append(metaBody, bmffIinf(bmffInfeV2(1, "hvc1"))...)
		metaBody = append(metaBody, boxWithHeader("iloc", buildIlocBody(1, 4, 4, 4, 0, []ilocTestEntry{
			{id: 1, baseOffset: mdatData, extents: [][2]uint64{{0, uint64(len(payload))}}},
		}))...)
		meta := bmffBox("meta", metaBody)
		stco := bmffFullBox("stco", 0, 0, binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 2), 4), uint32(mdatData))) //nolint:gosec // G115: the test file is a few hundred bytes
		co64 := bmffFullBox("co64", 0, 0, binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint32(nil, 1), mdatData+3))
		moov := bmffBox("moov", bmffBox("trak", bmffBox("mdia", bmffBox("minf", bmffBox("stbl", append(stco, co64...))))))
		out := append([]byte{}, ftyp...)
		out = append(out, meta...)
		out = append(out, moov...)
		return append(out, bmffBox("mdat", payload)...)
	}
	first := build(0)
	src := build(uint64(len(first) - len(payload))) //nolint:gosec // G115: non-negative test length
	mdatData := uint64(len(src) - len(payload))     //nolint:gosec // G115: non-negative test length

	out := injectBytes(t, src, task309EXIF, task309XMP)
	assertContiguousBoxes(t, out)
	if got := itemBytes(t, out, 1); !bytes.Equal(got, payload) {
		t.Errorf("base_offset item = %q, want %q", got, payload)
	}
	stco := findNested(t, out, "stco")
	co64 := findNested(t, out, "co64")
	newMdat := uint64(bytes.Index(out, payload)) //nolint:gosec // G115: the payload is present, so the index is non-negative
	if got := binary.BigEndian.Uint32(stco[8:]); got != 4 {
		t.Errorf("stco entry before the meta box = %d, want 4 (unchanged)", got)
	}
	if got := uint64(binary.BigEndian.Uint32(stco[12:])); got != newMdat {
		t.Errorf("stco entry = %d, want %d (shifted from %d)", got, newMdat, mdatData)
	}
	if got := binary.BigEndian.Uint64(co64[8:]); got != newMdat+3 {
		t.Errorf("co64 entry = %d, want %d", got, newMdat+3)
	}
}

// findNested returns the payload of the first box of type typ under the
// moov/trak/mdia/minf/stbl path of file.
func findNested(t *testing.T, file []byte, typ string) []byte {
	t.Helper()
	start, end, hl, ok := childBoxRange(file, boxTypeMoov)
	if !ok {
		t.Fatal("moov not found")
	}
	body := file[start+hl : end]
	for _, name := range []string{"trak", "mdia", "minf", "stbl", typ} {
		body = findInnerBox(body, fourCC(name))
	}
	if body == nil {
		t.Fatalf("%s not found", typ)
	}
	return body
}

// TestTask309MetaInsideMoovPatchesAncestor proves that when the meta box is
// nested in a moov box, the growth of the meta box is added to the moov
// box size (the former code patched a truncated prefix copy, which can never
// contain the end of the wrapping box, so the size stayed stale).
func TestTask309MetaInsideMoovPatchesAncestor(t *testing.T) {
	t.Parallel()
	build := func(mdatData uint64) []byte {
		ftyp := bmffFtyp("heic", 0, "mif1")
		metaBody := make([]byte, 0, 256)
		metaBody = append(metaBody, 0, 0, 0, 0)
		metaBody = append(metaBody, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		metaBody = append(metaBody, bmffIinf(bmffInfeV2(1, "hvc1"))...)
		metaBody = append(metaBody, boxWithHeader("iloc", buildIlocBody(0, 4, 4, 0, 0, []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{mdatData, 5}}},
		}))...)
		out := append([]byte{}, ftyp...)
		out = append(out, bmffBox("moov", bmffBox("meta", metaBody))...)
		return append(out, bmffBox("mdat", []byte("IMAGE"))...)
	}
	first := build(0)
	src := build(uint64(len(first) - 5)) //nolint:gosec // G115: non-negative test length
	out := injectBytes(t, src, task309EXIF, task309XMP)
	assertContiguousBoxes(t, out)
	if got := itemBytes(t, out, 1); !bytes.Equal(got, []byte("IMAGE")) {
		t.Errorf("image item = %q, want IMAGE", got)
	}
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out))
	if err != nil || !bytes.Equal(rawEXIF, task309EXIF) || !bytes.Equal(rawXMP, task309XMP) {
		t.Errorf("read back: err=%v exif=%d xmp=%d", err, len(rawEXIF), len(rawXMP))
	}
}

// TestTask309IrefVersion1 proves that new references appended to an iref
// box of version 1 use 32-bit item IDs (ISO/IEC 14496-12 §8.11.12.2).
func TestTask309IrefVersion1(t *testing.T) {
	t.Parallel()
	iref := bmffFullBox("iref", 1, 0, nil)
	data := buildTask282File([][]byte{bmffInfeV2(1, "hvc1")},
		[][]byte{bmffFullBox("pitm", 0, 0, []byte{0, 1}), iref}, []byte("IMAGE"), func(d uint64) []byte {
			return buildIlocBody(0, 4, 4, 0, 0, []ilocTestEntry{{id: 1, extents: [][2]uint64{{d, 5}}}})
		})
	out := injectBytes(t, data, task309EXIF, nil)
	refs := cdscTargets(t, out)
	if len(refs[2]) != 1 || refs[2][0] != 1 {
		t.Errorf("cdsc references = %v, want item 2 -> [1]", refs)
	}
}

// TestTask309NoFreeItemID proves that Inject refuses to create an item when
// every 16-bit item ID is in use, instead of reusing an ID.
func TestTask309NoFreeItemID(t *testing.T) {
	t.Parallel()
	data := buildTask282File([][]byte{bmffInfeV2(0xFFFF, "hvc1")},
		[][]byte{bmffFullBox("pitm", 0, 0, []byte{0xFF, 0xFF})}, []byte("IMAGE"), func(d uint64) []byte {
			return buildIlocBody(0, 4, 4, 0, 0, []ilocTestEntry{{id: 0xFFFF, extents: [][2]uint64{{d, 5}}}})
		})
	err := Inject(bytes.NewReader(data), &bytes.Buffer{}, task309EXIF, nil, nil, true)
	if !errors.Is(err, errNoFreeItemID) {
		t.Errorf("Inject error = %v, want errNoFreeItemID", err)
	}
}

// TestTask309TruncatedInfeIsNotRevived is the regression gate for a defect
// FuzzHEIFInject found in the item creation code (corpus entry
// testdata/fuzz/FuzzHEIFInject/73a9cfc5cb454843): the source iinf box ends
// with a truncated infe box (declared size 48, 20 bytes present) that
// parseIinf ignores. Appending the new infe boxes after those bytes let the
// truncated box parse as a complete Exif item in the output, whose garbage
// iloc extent pointed far outside the file. Only complete source children
// are kept now, and entry_count counts the infe boxes actually written.
func TestTask309TruncatedInfeIsNotRevived(t *testing.T) {
	t.Parallel()
	in := []byte("\x00\x00\x00Xmeta0000\x00\x00\x00#iinf\x0000000\x00\x00\x000infe\x02000\x00\x0100Exif0\x00\x00\x00 iloc\x00000D\x00\x000\x00\x01000000000000000000000000000000000000000000")
	out := injectBytes(t, in, []byte("EXIF"), []byte("XMP"))
	assertHEIFExtentsWithinBounds(t, out)
	if declared, actual := iinfEntryCount(t, out); declared != 2 || actual != 2 {
		t.Errorf("iinf entry_count=%d infe boxes=%d, want 2 (the two new items)", declared, actual)
	}
}
