package heif

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tasks #348 and #349.
//
// #348: parseIinf sliced every infe box at pos+8, ignoring the 64-bit
// largesize header form (ISO/IEC 14496-12 §4.2: size == 1, the type, then a
// 64-bit size; the header is 16 bytes), so an infe written that way was read
// from the wrong offset and its item was lost.
//
// #349: Extract (first 'meta' in file order, descending into moov, trak and
// udta) and Inject (top-level 'meta' first, then moov) disagreed on a file
// that holds a top-level and a moov-nested meta box. Both now use
// findMetaBoxAbs: the first top-level meta box, and only when the file has none
// the first meta box that is a direct child of moov.

// infeBody returns the content of an infe box built by bmffInfeV2 (the bytes
// after the 8-byte header).
func infeBody(infe []byte) []byte { return infe[8:] }

// TestTask348ParseIinfInfeHeaderForms: an infe box with an 8-byte header and
// the same box with a 16-byte largesize header yield identical items, and
// Extract finds the Exif and XMP payloads either way. HEIF-BOX-LARGESIZE-02.
func TestTask348ParseIinfInfeHeaderForms(t *testing.T) {
	t.Parallel()
	exifInfe := bmffInfeV2(2, "Exif")
	xmpInfe := bmffInfeV2WithContentType(3, "mime", "application/rdf+xml")
	imgInfe := bmffInfeV2(1, "hvc1")
	large := func(infe []byte) []byte { return bmffLargesizeBox("infe", infeBody(infe)) }

	for _, tc := range []struct {
		name  string
		infes [][]byte
	}{
		{"all 8-byte headers", [][]byte{imgInfe, exifInfe, xmpInfe}},
		{"all 16-byte headers", [][]byte{large(imgInfe), large(exifInfe), large(xmpInfe)}},
		{"mixed headers", [][]byte{imgInfe, large(exifInfe), xmpInfe}},
		{"first infe 16-byte", [][]byte{large(imgInfe), exifInfe, large(xmpInfe)}},
	} {
		t.Run("HEIF-BOX-LARGESIZE-02/"+tc.name, func(t *testing.T) {
			t.Parallel()
			iinf := bmffIinf(tc.infes...)
			got := parseIinf(iinf)
			want := map[uint16][4]byte{1: {'h', 'v', 'c', '1'}, 2: itemTypeExif, 3: itemTypeMime}
			if len(got) != len(want) {
				t.Fatalf("parseIinf: %d items %v, want %d %v", len(got), got, len(want), want)
			}
			for id, typ := range want {
				if g := got[id]; g != typ {
					t.Errorf("item %d: type %q, want %q", id, string(g[:]), string(typ[:]))
				}
			}
		})
	}
}

// TestTask348ExtractAndInjectLargesizeInfe: a whole file whose infe boxes use
// the 16-byte header is read by Extract and rewritten by Inject exactly like
// the 8-byte form. HEIF-BOX-LARGESIZE-02.
func TestTask348ExtractAndInjectLargesizeInfe(t *testing.T) {
	t.Parallel()
	exifTIFF := []byte("EXIF-PAYLOAD-OLD")
	xmp := []byte("<x:xmpmeta>old</x:xmpmeta>")
	for _, largeInfe := range []bool{false, true} {
		name := "8-byte infe"
		if largeInfe {
			name = "16-byte infe"
		}
		t.Run("HEIF-BOX-LARGESIZE-02/"+name, func(t *testing.T) {
			t.Parallel()
			file := buildInfeFile(largeInfe, exifTIFF, xmp)
			gotEXIF, _, gotXMP, err := Extract(bytes.NewReader(file))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(gotEXIF, exifTIFF) || !bytes.Equal(gotXMP, xmp) {
				t.Fatalf("Extract = %q / %q, want %q / %q", gotEXIF, gotXMP, exifTIFF, xmp)
			}

			newEXIF := []byte("EXIF-PAYLOAD-NEW-LONGER")
			newXMP := []byte("<x:xmpmeta>new payload</x:xmpmeta>")
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(file), &out, newEXIF, nil, newXMP, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			gotEXIF, _, gotXMP, err = Extract(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Extract after Inject: %v", err)
			}
			if !bytes.Equal(gotEXIF, newEXIF) || !bytes.Equal(gotXMP, newXMP) {
				t.Fatalf("re-read = %q / %q, want %q / %q", gotEXIF, gotXMP, newEXIF, newXMP)
			}
			// The Inject must not have created duplicate items: the original
			// Exif and XMP items were found and rewritten in place.
			items := parseIinf(findMetaContentOrFail(t, out.Bytes()))
			if len(items) != 3 {
				t.Errorf("output has %d items %v, want 3 (image, Exif, XMP)", len(items), items)
			}
		})
	}
}

// findMetaContentOrFail returns the selected meta content of file or fails t.
func findMetaContentOrFail(t *testing.T, file []byte) []byte {
	t.Helper()
	c := metaContentInWindow(file, true)
	if c == nil {
		t.Fatal("no meta box selected")
	}
	return c
}

// buildInfeFile assembles ftyp + meta{pitm, iinf, iloc} + mdat with an image
// item (1), an Exif item (2) and an XMP item (3); every infe box has a
// 16-byte header when largeInfe is set.
func buildInfeFile(largeInfe bool, exifTIFF, xmp []byte) []byte {
	exifItem := append([]byte{0, 0, 0, 0}, exifTIFF...)
	image := []byte("IMAGE")
	payload := append(append(append([]byte{}, image...), exifItem...), xmp...)
	mk := func(infe []byte) []byte {
		if largeInfe {
			return bmffLargesizeBox("infe", infeBody(infe))
		}
		return infe
	}
	build := func(mdatData uint64) []byte {
		entries := []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{mdatData, uint64(len(image))}}},
			{id: 2, extents: [][2]uint64{{mdatData + uint64(len(image)), uint64(len(exifItem))}}},
			{id: 3, extents: [][2]uint64{{mdatData + uint64(len(image)+len(exifItem)), uint64(len(xmp))}}},
		}
		body := append([]byte{0, 0, 0, 0}, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		body = append(body, bmffIinf(mk(bmffInfeV2(1, "hvc1")), mk(bmffInfeV2(2, "Exif")),
			mk(bmffInfeV2WithContentType(3, "mime", "application/rdf+xml")))...)
		body = append(body, boxWithHeader("iloc", buildIlocBody(0, 4, 4, 0, 0, entries))...)
		out := bmffFtyp("heic", 0, "mif1")
		out = append(out, bmffBox("meta", body)...)
		return append(out, bmffBox("mdat", payload)...)
	}
	first := build(0)
	return build(uint64(len(first) - len(payload))) //nolint:gosec // G115: test fixture sizes are small
}

// TestTask348NoHardCodedBoxHeaderLength is the audit gate for #348: every box
// content offset in the package's non-test sources comes from
// parseHEIFBoxHeader or flatBoxRangeHdr. A slice such as data[pos+8 ...] is
// legal only inside parseHEIFBoxHeader itself (the header parser), so a new
// hard-coded header length fails here. Box construction (new boxes the
// writer builds) never indexes a source buffer with pos+8. HEIF-BOX-LARGESIZE-02.
func TestTask348NoHardCodedBoxHeaderLength(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(src, []byte("\n")) {
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("//")) || !bytes.Contains(line, []byte("pos+8")) {
				continue
			}
			// parseHEIFBoxHeader: minimum header, type and largesize fields.
			if f == "heif.go" && (bytes.Contains(line, []byte("pos+8 > len(data)")) ||
				bytes.Contains(line, []byte("data[pos+4 : pos+8]")) ||
				bytes.Contains(line, []byte("data[pos+8:]"))) {
				continue
			}
			// patchAncestorSize scanners read a raw header of a box they
			// may not parse (loop guards and in-place size patches).
			if f == "inject.go" && (bytes.Contains(line, []byte("for pos+8 <= len(data)")) ||
				bytes.Contains(line, []byte("data[pos+8:]"))) {
				continue
			}
			t.Errorf("%s:%d: hard-coded box header length: %s", f, i+1, trimmed)
		}
	}
}

// metaFixture describes the items of one meta box of a #349 fixture.
type metaFixture struct {
	exif []byte // payload of the Exif item (id 1) stored in the file's mdat
}

// buildTask349File assembles ftyp followed by the top-level boxes of layout in
// order, then mdat. layout entries: "meta" (top-level meta, items from top),
// "moov" (moov holding a meta box with the items of nested), "free" (a 70000
// byte free box, which pushes later boxes beyond the Extract header window).
func buildTask349File(layout []string, top, nested metaFixture) []byte {
	topItem := append([]byte{0, 0, 0, 0}, top.exif...)
	nestedItem := append([]byte{0, 0, 0, 0}, nested.exif...)
	mdatPayload := append(append([]byte{}, topItem...), nestedItem...)

	metaBox := func(item []byte, itemOff, mdatData uint64) []byte {
		entries := []ilocTestEntry{{id: 1, extents: [][2]uint64{{mdatData + itemOff, uint64(len(item))}}}}
		body := append([]byte{0, 0, 0, 0}, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		body = append(body, bmffIinf(bmffInfeV2(1, "Exif"))...)
		body = append(body, boxWithHeader("iloc", buildIlocBody(0, 4, 4, 0, 0, entries))...)
		return bmffBox("meta", body)
	}
	build := func(mdatData uint64) []byte {
		out := bmffFtyp("heic", 0, "mif1")
		for _, kind := range layout {
			switch kind {
			case "meta":
				out = append(out, metaBox(topItem, 0, mdatData)...)
			case "moov":
				out = append(out, bmffBox("moov", metaBox(nestedItem, uint64(len(topItem)), mdatData))...)
			case "free":
				out = append(out, bmffBox("free", make([]byte, 70000))...)
			}
		}
		return append(out, bmffBox("mdat", mdatPayload)...)
	}
	first := build(0)
	return build(uint64(len(first) - len(mdatPayload))) //nolint:gosec // G115: test fixture sizes are small
}

// metaPlacements are the layouts of the #349 acceptance table.
var metaPlacements = []struct { //nolint:gochecknoglobals // read-only test table
	name   string
	layout []string
	// wantNested is true when the nested (moov) meta box is the selected one.
	wantNested bool
}{
	{"top-level then moov", []string{"meta", "moov"}, false},
	{"moov then top-level", []string{"moov", "meta"}, false},
	{"moov then top-level beyond the header window", []string{"moov", "free", "meta"}, false},
	{"top-level beyond the header window then moov", []string{"free", "meta", "moov"}, false},
	{"moov only", []string{"moov"}, true},
	{"top-level only", []string{"meta"}, false},
}

// TestTask349ExtractInjectSelectSameMeta: with a top-level and a moov-nested
// meta box in either order (and beyond the 64 KB Extract window), Extract and
// Inject address the SAME box: the file-level one; the moov-nested box is used
// only when the file has no file-level meta box. Inject rewrites the selected
// box's Exif item, the other box stays byte-identical, and Extract re-reads
// the written value. HEIF-META-SELECT-01.
func TestTask349ExtractInjectSelectSameMeta(t *testing.T) {
	t.Parallel()
	top := metaFixture{exif: []byte("TOP-LEVEL-EXIF")}
	nested := metaFixture{exif: []byte("MOOV-NESTED-EXIF")}
	for _, tc := range metaPlacements {
		t.Run("HEIF-META-SELECT-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			file := buildTask349File(tc.layout, top, nested)
			want := top.exif
			if tc.wantNested {
				want = nested.exif
			}

			// Extract and findMetaBoxAbs agree with the rule.
			gotEXIF, _, _, err := Extract(bytes.NewReader(file))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(gotEXIF, want) {
				t.Fatalf("Extract = %q, want %q", gotEXIF, want)
			}

			// Inject writes into the same box.
			newEXIF := []byte("REWRITTEN-EXIF-PAYLOAD")
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(file), &out, newEXIF, nil, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			reEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Extract after Inject: %v", err)
			}
			if !bytes.Equal(reEXIF, newEXIF) {
				t.Fatalf("re-read after Inject = %q, want %q (Inject wrote into a different meta box than Extract reads)", reEXIF, newEXIF)
			}

			// The meta box that was NOT selected keeps its original items.
			if len(tc.layout) > 1 {
				assertUnselectedMetaIntact(t, out.Bytes(), tc.wantNested, top.exif, nested.exif)
			}
		})
	}
}

// assertUnselectedMetaIntact checks that the meta box Inject did not select
// still resolves to its original Exif payload in out.
func assertUnselectedMetaIntact(t *testing.T, out []byte, nestedSelected bool, topEXIF, nestedEXIF []byte) {
	t.Helper()
	var other []byte
	var wantPayload []byte
	if nestedSelected {
		_, e, off, ok := flatBoxRangeHdrTop(out)
		if !ok {
			t.Fatal("top-level meta box lost")
		}
		other, wantPayload = out[off:e], topEXIF
	} else {
		ms, me, mhl, ok := flatBoxRangeHdr(out, boxTypeMoov)
		if !ok {
			t.Fatal("moov box lost")
		}
		s, e, hl, ok := flatBoxRangeHdr(out[ms+mhl:me], boxTypeMeta)
		if !ok {
			t.Fatal("moov-nested meta box lost")
		}
		other, wantPayload = out[ms+mhl+s+hl+4:ms+mhl+e], nestedEXIF
	}
	items := parseIloc(other)
	loc, ok := items[1]
	if !ok {
		t.Fatalf("unselected meta lost its item: %v", items)
	}
	// Offsets of the unselected box may have been shifted by the meta size
	// change; resolve against out and compare with the original payload.
	got := out[loc.offset+4 : loc.offset+loc.length]
	if !bytes.Equal(got, wantPayload) {
		t.Errorf("unselected meta box now resolves to %q, want its original %q", got, wantPayload)
	}
}

// flatBoxRangeHdrTop returns the range and content offset of the first
// top-level meta box of data.
func flatBoxRangeHdrTop(data []byte) (start, end, contentOff int, ok bool) {
	s, e, hl, found := flatBoxRangeHdr(data, boxTypeMeta)
	if !found {
		return 0, 0, 0, false
	}
	return s, e, s + hl + 4, true
}

// TestTask349MalformedTopLevelMetaIsNotSkipped: a top-level meta box that is
// present but too small to be a FullBox is the selected box; the selection
// does not fall through to a moov-nested box, so Extract and Inject keep
// agreeing (Inject returns ErrNoMetadataContainer, Extract returns nothing).
// HEIF-META-SELECT-01.
func TestTask349MalformedTopLevelMetaIsNotSkipped(t *testing.T) {
	t.Parallel()
	good := buildTask349File([]string{"moov"}, metaFixture{}, metaFixture{exif: []byte("NESTED")})
	ftyp := bmffFtyp("heic", 0, "mif1")
	file := append(append([]byte{}, ftyp...), bmffBox("meta", []byte{0, 0})...)
	file = append(file, good[len(ftyp):]...)

	if _, _, _, found := findMetaBoxAbs(file); found {
		t.Fatal("findMetaBoxAbs selected a box although the top-level meta is malformed")
	}
	gotEXIF, _, _, err := Extract(bytes.NewReader(file))
	if err != nil || gotEXIF != nil {
		t.Errorf("Extract = %q, %v, want nothing", gotEXIF, err)
	}
	err = Inject(bytes.NewReader(file), &bytes.Buffer{}, []byte("x"), nil, nil, true)
	if !errors.Is(err, ErrNoMetadataContainer) {
		t.Errorf("Inject err = %v, want ErrNoMetadataContainer", err)
	}
}

// TestTask349NestedDeeperThanMoovIsNotSelected: a meta box inside moov/udta
// or moov/trak is not the file's metadata container.
func TestTask349NestedDeeperThanMoovIsNotSelected(t *testing.T) {
	t.Parallel()
	for _, container := range []string{"udta", "trak"} {
		t.Run("HEIF-META-SELECT-01/"+container, func(t *testing.T) {
			t.Parallel()
			meta := bmffBox("meta", append([]byte{0, 0, 0, 0}, bmffIinf(bmffInfeV2(1, "Exif"))...))
			file := append(bmffFtyp("heic", 0, "mif1"), bmffBox("moov", bmffBox(container, meta))...)
			if _, _, _, found := findMetaBoxAbs(file); found {
				t.Errorf("a meta box nested in moov/%s was selected", container)
			}
		})
	}
}

// TestTask349CorpusSelectsTopLevel records the corpus finding behind the
// moov-fallback decision: every HEIF/AVIF file of the corpus has its meta box
// at the top level, so the rule changes no corpus result.
func TestTask349CorpusSelectsTopLevel(t *testing.T) {
	t.Parallel()
	files, _ := filepath.Glob("../../testdata/corpus/heif/*/*")
	if len(files) == 0 {
		t.Skip("HEIF corpus absent") // allowed: corpus file absent (docs/TESTING.md §2.1)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, ok := flatBoxRangeHdr(data, boxTypeMoov); ok {
			ms, me, mhl, _ := flatBoxRangeHdr(data, boxTypeMoov)
			if _, _, _, nested := flatBoxRangeHdr(data[ms+mhl:me], boxTypeMeta); nested {
				if _, _, _, top := flatBoxRangeHdr(data, boxTypeMeta); !top {
					t.Errorf("%s: only a moov-nested meta box", f)
				}
			}
		}
	}
}
