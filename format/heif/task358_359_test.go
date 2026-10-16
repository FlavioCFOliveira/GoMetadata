package heif

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Tasks #358 and #359.
//
// #358: parseIinf read a 16-bit entry_count for every iinf version below 2;
// ISO/IEC 14496-12 §8.11.6 makes it 16 bits for version 0 and 32 bits
// otherwise, and buildIinfBox already wrote it that way, so Extract could not
// read what Inject wrote for such a file.
//
// #359: Inject appended boxes after a child of meta, iinf or iref whose size
// field is 0 (§4.2: the box extends to the end of its container); readers took
// the appended boxes as part of that child. Inject now closes the open-ended
// child first (internal/bmff).

// task359Layout describes the fixture of buildTask359File.
type task359Layout struct {
	iinfVersion byte
	// open selects the child that gets a size field of 0: "" none, "meta" a
	// last child of meta (the file has no iinf box, so Inject appends it
	// after the open-ended child), "iinf" the last infe box, "iref" the last
	// reference box of an iref box.
	open string
	// largeOpen gives the open-ended box the 64-bit form with largesize 0.
	largeOpen bool
}

// buildTask359File assembles ftyp + meta + mdat. The meta box holds pitm
// (item 1), an iinf box (unless open == "meta") of the given version with the
// infe boxes of items 1 (image) and 2 (Exif), an iref box when open == "iref",
// and an iloc box for the same items.
func buildTask359File(l task359Layout) []byte {
	image := []byte("IMAGE")
	exifItem := append([]byte{0, 0, 0, 0}, conformanceMinimalTIFF()...)
	payload := append(append([]byte{}, image...), exifItem...)

	// open turns the box into an open-ended one.
	open := func(box []byte) []byte {
		out := append([]byte(nil), box...)
		if l.largeOpen {
			// 64-bit header form: size 1, type, largesize 0.
			large := append([]byte{0, 0, 0, 1}, out[4:8]...)
			large = binary.BigEndian.AppendUint64(large, 0)
			return append(large, out[8:]...)
		}
		binary.BigEndian.PutUint32(out, 0)
		return out
	}

	build := func(mdatData uint64) []byte {
		infes := [][]byte{bmffInfeV2(1, "hvc1"), bmffInfeV2(2, "Exif")}
		if l.open == "iinf" {
			infes[len(infes)-1] = open(infes[len(infes)-1])
		}
		entries := []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{mdatData, uint64(len(image))}}},
			{id: 2, extents: [][2]uint64{{mdatData + uint64(len(image)), uint64(len(exifItem))}}},
		}
		body := append([]byte{0, 0, 0, 0}, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		if l.open != "meta" {
			var iinfBody []byte
			if l.iinfVersion == 0 {
				iinfBody = binary.BigEndian.AppendUint16(nil, uint16(len(infes))) //nolint:gosec // G115: two entries
			} else {
				iinfBody = binary.BigEndian.AppendUint32(nil, uint32(len(infes))) //nolint:gosec // G115: two entries
			}
			for _, infe := range infes {
				iinfBody = append(iinfBody, infe...)
			}
			body = append(body, bmffFullBox("iinf", l.iinfVersion, 0, iinfBody)...)
		}
		if l.open == "iref" {
			// Version 0 reference box from item 2 to item 1 ('dimg').
			ref := bmffBox("dimg", []byte{0, 2, 0, 1, 0, 1})
			body = append(body, bmffFullBox("iref", 0, 0, open(ref))...)
		}
		body = append(body, boxWithHeader("iloc", buildIlocBody(0, 4, 4, 0, 0, entries))...)
		if l.open == "meta" {
			body = append(body, open(bmffBox("free", []byte{1, 2, 3, 4}))...)
		}
		out := bmffFtyp("heic", 0, "mif1")
		out = append(out, bmffBox("meta", body)...)
		return append(out, bmffBox("mdat", payload)...)
	}
	first := build(0)
	return build(uint64(len(first) - len(payload))) //nolint:gosec // G115: test fixture sizes are small
}

// TestTask358IinfEntryCountWidth: an iinf box of version 0 (16-bit
// entry_count) and of version 1 (32-bit entry_count) holding the same infe
// boxes yield the same items; versions 2 and above read 32 bits as well.
// HEIF-IINF-COUNT-01.
func TestTask358IinfEntryCountWidth(t *testing.T) {
	t.Parallel()
	infes := [][]byte{bmffInfeV2(1, "hvc1"), bmffInfeV2(2, "Exif"), bmffInfeV2WithContentType(3, "mime", "application/rdf+xml")}
	want := map[uint16][4]byte{1: {'h', 'v', 'c', '1'}, 2: itemTypeExif, 3: itemTypeMime}
	for _, version := range []byte{0, 1, 2, 7} {
		t.Run("HEIF-IINF-COUNT-01/version "+string(rune('0'+version)), func(t *testing.T) {
			t.Parallel()
			var body []byte
			if version == 0 {
				body = binary.BigEndian.AppendUint16(nil, uint16(len(infes))) //nolint:gosec // G115: three entries
			} else {
				body = binary.BigEndian.AppendUint32(nil, uint32(len(infes))) //nolint:gosec // G115: three entries
			}
			for _, infe := range infes {
				body = append(body, infe...)
			}
			iinf := bmffFullBox("iinf", version, 0, body)
			got := parseIinf(iinf)
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

// TestTask358ParseIinfItemCountTable covers the field widths of
// parseIinfEntryCount, including the truncated forms.
func TestTask358ParseIinfItemCountTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		data    []byte
		version byte
		count   int
		newPos  int
		ok      bool
	}{
		{"v0 reads 16 bits", []byte{0, 3, 9, 9}, 0, 3, 2, true},
		{"v1 reads 32 bits", []byte{0, 0, 0, 3}, 1, 3, 4, true},
		{"v1 high half is not dropped", []byte{0, 1, 0, 3}, 1, 0x10003, 4, true},
		{"v2 reads 32 bits", []byte{0, 0, 0, 3}, 2, 3, 4, true},
		{"v1 truncated to 16 bits", []byte{0, 3}, 1, 0, 0, false},
		{"v0 truncated", []byte{0}, 0, 0, 0, false},
		{"v1 above int32 is refused", []byte{0x80, 0, 0, 0}, 1, 0, 0, false},
	} {
		t.Run("HEIF-IINF-COUNT-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			count, newPos, ok := parseIinfEntryCount(tc.data, tc.version, 0)
			if ok != tc.ok || count != tc.count || newPos != tc.newPos {
				t.Errorf("got (%d, %d, %v), want (%d, %d, %v)", count, newPos, ok, tc.count, tc.newPos, tc.ok)
			}
		})
	}
}

// TestTask358IlocItemCountWidth: the 'iloc' item_count keeps its own rule
// (ISO/IEC 14496-12 §8.11.3.2: 16 bits below version 2), so version 1 of an
// iloc box is still read with 16 bits. HEIF-IINF-COUNT-01.
func TestTask358IlocItemCountWidth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		data    []byte
		version byte
		count   int
		newPos  int
	}{
		{"v0", []byte{0, 3, 9, 9}, 0, 3, 2},
		{"v1", []byte{0, 3, 9, 9}, 1, 3, 2},
		{"v2", []byte{0, 0, 0, 3}, 2, 3, 4},
	} {
		t.Run("HEIF-IINF-COUNT-01/iloc "+tc.name, func(t *testing.T) {
			t.Parallel()
			count, newPos, ok := parseIlocItemCount(tc.data, tc.version, 0)
			if !ok || count != tc.count || newPos != tc.newPos {
				t.Errorf("got (%d, %d, %v), want (%d, %d, true)", count, newPos, ok, tc.count, tc.newPos)
			}
		})
	}
}

// TestTask358RoundTripIinfVersion1: Inject keeps iinf version 1, writes its
// entry_count in 32 bits, and Extract reads back every payload Inject wrote
// along with the items the source declared. HEIF-IINF-COUNT-01.
func TestTask358RoundTripIinfVersion1(t *testing.T) {
	t.Parallel()
	for _, version := range []byte{0, 1} {
		t.Run("HEIF-IINF-COUNT-01/version "+string(rune('0'+version)), func(t *testing.T) {
			t.Parallel()
			src := buildTask359File(task359Layout{iinfVersion: version})
			if got, _, _, err := Extract(bytes.NewReader(src)); err != nil || got == nil {
				t.Fatalf("Extract(source) = %x, %v; want the source EXIF", got, err)
			}
			newEXIF := conformanceMinimalTIFF()
			out := injectBytes(t, src, newEXIF, task309XMP)

			content := findMetaContentOrFail(t, out)
			iinf := findInnerBox(content, boxTypeIinf)
			if len(iinf) < 8 || iinf[0] != version {
				t.Fatalf("iinf version = %v, want %d kept", iinf[:min(len(iinf), 1)], version)
			}
			declared, actual := iinfEntryCount(t, out)
			if declared != 3 || actual != 3 {
				t.Errorf("iinf entry_count = %d with %d infe boxes, want 3 and 3", declared, actual)
			}
			if items := parseIinf(content); len(items) != 3 {
				t.Errorf("parseIinf read %d items %v, want 3", len(items), items)
			}
			gotEXIF, _, gotXMP, err := Extract(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(gotEXIF, newEXIF) || !bytes.Equal(gotXMP, task309XMP) {
				t.Errorf("Extract after Inject = %q / %q, want %q / %q", gotEXIF, gotXMP, newEXIF, task309XMP)
			}
		})
	}
}

// TestTask359InjectClosesOpenEndedChild: a size-0 last child of meta (file
// without iinf), of iinf or of iref no longer swallows the boxes Inject
// appends: every item re-reads, in the 32-bit and the 64-bit (largesize 0)
// forms. HEIF-BOX-OPENENDED-01.
func TestTask359InjectClosesOpenEndedChild(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		l    task359Layout
	}{
		{"meta child", task359Layout{open: "meta"}},
		{"meta child 64-bit", task359Layout{open: "meta", largeOpen: true}},
		{"iinf v0 last infe", task359Layout{open: "iinf"}},
		{"iinf v1 last infe", task359Layout{open: "iinf", iinfVersion: 1}},
		{"iinf last infe 64-bit", task359Layout{open: "iinf", largeOpen: true}},
		{"iref last reference", task359Layout{open: "iref"}},
		{"iref last reference 64-bit", task359Layout{open: "iref", largeOpen: true}},
	} {
		t.Run("HEIF-BOX-OPENENDED-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := buildTask359File(tc.l)
			newEXIF := conformanceMinimalTIFF()
			out := injectBytes(t, src, newEXIF, task309XMP)

			content := findMetaContentOrFail(t, out)
			items := parseIinf(content)
			// The "meta child" source declares no iinf box: Inject creates it
			// (after the open-ended child) with the Exif and XMP items.
			if tc.l.open != "meta" && (items[1] != [4]byte{'h', 'v', 'c', '1'} || items[2] != itemTypeExif) {
				t.Errorf("source items lost: %v", items)
			}
			xmpID := uint16(0)
			for id, typ := range items {
				if typ == itemTypeMime {
					xmpID = id
				}
			}
			if xmpID == 0 {
				t.Fatalf("the XMP item written by Inject is not readable: items %v", items)
			}
			if _, ok := parseIlocFull(content); !ok {
				t.Fatal("iloc box not readable in the output")
			}
			gotEXIF, _, gotXMP, err := Extract(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if !bytes.Equal(gotEXIF, newEXIF) || !bytes.Equal(gotXMP, task309XMP) {
				t.Errorf("Extract after Inject = %q / %q, want %q / %q", gotEXIF, gotXMP, newEXIF, task309XMP)
			}
			if tc.l.open == "iref" {
				if refs := cdscTargets(t, out); len(refs[uint32(xmpID)]) != 1 {
					t.Errorf("cdsc reference of the XMP item missing: %v", refs)
				}
			}
			// No box of the output is open-ended any more, except the last
			// box of the file the source already had that way (none here).
			assertNoOpenEndedChild(t, content)
		})
	}
}

// assertNoOpenEndedChild fails t when a child of the meta content, of its iinf
// or of its iref box has a size field of 0 (or a largesize of 0).
func assertNoOpenEndedChild(t *testing.T, metaContent []byte) {
	t.Helper()
	check := func(name string, data []byte) {
		for pos := 0; pos < len(data); {
			size, _, _, ok := parseHEIFBoxHeader(data, pos)
			if !ok {
				return
			}
			if bmffOpenEnded(data[pos:]) {
				t.Errorf("%s has an open-ended child at %d", name, pos)
			}
			pos += int(size) //nolint:gosec // G115: bounded by the slice length
		}
	}
	check("meta", metaContent)
	if iinf := findInnerBox(metaContent, boxTypeIinf); len(iinf) >= 4 {
		_, pos, _ := parseIinfEntryCount(iinf, iinf[0], 4)
		check("iinf", iinf[pos:])
	}
	if iref := findInnerBox(metaContent, boxTypeIref); len(iref) >= 4 {
		check("iref", iref[4:])
	}
}

// bmffOpenEnded reports whether the box at the start of b is open-ended.
func bmffOpenEnded(b []byte) bool {
	if binary.BigEndian.Uint32(b) == 0 {
		return true
	}
	return binary.BigEndian.Uint32(b) == 1 && len(b) >= 16 && binary.BigEndian.Uint64(b[8:]) == 0
}

// TestTask359OpenEndedChildContentKept: closing an open-ended child keeps its
// content byte for byte.
func TestTask359OpenEndedChildContentKept(t *testing.T) {
	t.Parallel()
	t.Run("HEIF-BOX-OPENENDED-01", func(t *testing.T) {
		t.Parallel()
		src := buildTask359File(task359Layout{open: "meta"})
		out := injectBytes(t, src, conformanceMinimalTIFF(), task309XMP)
		if !bytes.Contains(out, []byte{'f', 'r', 'e', 'e', 1, 2, 3, 4}) {
			t.Error("the open-ended 'free' child lost its content")
		}
	})
}
