package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// Tasks #334 and #335.
//
// #334: the 'meta' (and 'moov') box may use the 64-bit largesize header form
// (ISO/IEC 14496-12 §4.2: size == 1, then the type, then a 64-bit size; the
// header is 16 bytes). findMetaBoxAbs assumed 8-byte headers, so the content
// offset, the FullBox version/flags and every child box were misread.
//
// #335: an iloc box may declare offset_size or length_size 0 (§8.11.3.3: the
// sizes are taken from {0, 4, 8}). Inject used to decline such a file and
// return nil without writing the requested metadata. It now widens the zero
// widths, and every other branch that used to decline returns an error.

// task334Layout describes a synthetic HEIF file for buildTask334File.
type task334Layout struct {
	metaLarge bool // meta box with a 16-byte largesize header
	inMoov    bool // meta box nested in a moov box
	moovLarge bool // moov box with a 16-byte largesize header (inMoov only)
	// withItems adds an Exif item (id 2) and an XMP item (id 3) holding old
	// payloads, so Inject rewrites them instead of creating them.
	withItems bool
	// offsetSize, lengthSize and baseOffsetSize are the iloc field widths. With
	// offsetSize 0 every item is located by its base_offset (the extent
	// offset field is absent).
	offsetSize, lengthSize, baseOffsetSize int
}

// task334Image is the payload of the image item (id 1) of the fixtures.
var task334Image = []byte("IMAGE") //nolint:gochecknoglobals // read-only test payload

// buildTask334File assembles ftyp + [moov{] meta{pitm, iinf, iloc} [, trak
// with an stco box pointing at the image]} + mdat. The file is built twice
// because the iloc extents and the stco entry hold absolute offsets.
func buildTask334File(l task334Layout) []byte {
	oldEXIF := append([]byte{0, 0, 0, 0}, conformanceMinimalTIFF()...)
	oldXMP := []byte("<old/>")
	mdatPayload := append(append(append([]byte{}, task334Image...), oldEXIF...), oldXMP...)

	build := func(mdatData uint64) []byte {
		type item struct {
			id       uint32
			off, len uint64 // relative to the mdat payload
		}
		items := []item{{1, 0, uint64(len(task334Image))}}
		infes := [][]byte{bmffInfeV2(1, "hvc1")}
		if l.withItems {
			items = append(items,
				item{2, uint64(len(task334Image)), uint64(len(oldEXIF))},
				item{3, uint64(len(task334Image) + len(oldEXIF)), uint64(len(oldXMP))})
			infes = append(infes, bmffInfeV2(2, "Exif"), bmffInfeV2WithContentType(3, "mime", "application/rdf+xml"))
		}
		entries := make([]ilocTestEntry, 0, len(items))
		for _, it := range items {
			e := ilocTestEntry{id: it.id}
			ext := [2]uint64{mdatData + it.off, it.len}
			if l.offsetSize == 0 {
				e.baseOffset, ext[0] = mdatData+it.off, 0
			}
			e.extents = [][2]uint64{ext}
			entries = append(entries, e)
		}
		metaBody := append([]byte{0, 0, 0, 0}, bmffFullBox("pitm", 0, 0, []byte{0, 1})...)
		metaBody = append(metaBody, bmffIinf(infes...)...)
		metaBody = append(metaBody, boxWithHeader("iloc", buildIlocBody(0, l.offsetSize, l.lengthSize, l.baseOffsetSize, 0, entries))...)
		mk := bmffBox
		if l.metaLarge {
			mk = bmffLargesizeBox
		}
		meta := mk("meta", metaBody)

		out := bmffFtyp("heic", 0, "mif1")
		if l.inMoov {
			stco := bmffFullBox("stco", 0, 0, binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 1), uint32(mdatData))) //nolint:gosec // G115: small test file
			trak := bmffBox("trak", bmffBox("mdia", bmffBox("minf", bmffBox("stbl", stco))))
			mkMoov := bmffBox
			if l.moovLarge {
				mkMoov = bmffLargesizeBox
			}
			out = append(out, mkMoov("moov", append(meta, trak...))...)
		} else {
			out = append(out, meta...)
		}
		return append(out, bmffBox("mdat", mdatPayload)...)
	}
	first := build(0)
	return build(uint64(len(first) - len(oldEXIF) - len(oldXMP) - len(task334Image))) //nolint:gosec // G115: non-negative test length
}

// task334Meta returns the content of the meta box of file, found through the
// header-length-aware reader, and the header length of that box, failing t when
// it is absent.
func task334Meta(t *testing.T, file []byte) (content []byte, headerLen int) {
	t.Helper()
	start, end, contentOff, found := findMetaBoxAbs(file)
	if !found {
		t.Fatal("meta box not found")
	}
	_, _, hl, _ := flatBoxRangeHdr(file[start:], boxTypeMeta)
	return file[contentOff:end], hl
}

// TestTask334FindMetaBoxAbsHeaderLength: ISO/IEC 14496-12 §4.2 / §8.11.1 —
// the meta content starts after the box header (8 or 16 bytes) plus the 4
// FullBox bytes, at the top level and inside moov, whatever the header form
// of the moov box.
func TestTask334FindMetaBoxAbsHeaderLength(t *testing.T) {
	t.Parallel()
	ftypLen := len(bmffFtyp("heic", 0, "mif1"))
	for _, tc := range []struct {
		name string
		l    task334Layout
		// wantStart is the absolute start of the meta box, wantHdr its header
		// length.
		wantStart, wantHdr int
	}{
		{"top-level meta, 8-byte header", task334Layout{offsetSize: 4, lengthSize: 4}, ftypLen, 8},
		{"top-level meta, 16-byte header", task334Layout{metaLarge: true, offsetSize: 4, lengthSize: 4}, ftypLen, 16},
		{"meta in moov (8), 8-byte header", task334Layout{inMoov: true, offsetSize: 4, lengthSize: 4}, ftypLen + 8, 8},
		{"meta in moov (8), 16-byte header", task334Layout{inMoov: true, metaLarge: true, offsetSize: 4, lengthSize: 4}, ftypLen + 8, 16},
		{"meta in moov (16), 8-byte header", task334Layout{inMoov: true, moovLarge: true, offsetSize: 4, lengthSize: 4}, ftypLen + 16, 8},
		{"meta in moov (16), 16-byte header", task334Layout{inMoov: true, moovLarge: true, metaLarge: true, offsetSize: 4, lengthSize: 4}, ftypLen + 16, 16},
	} {
		t.Run("HEIF-BOX-LARGESIZE-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			file := buildTask334File(tc.l)
			start, end, contentOff, found := findMetaBoxAbs(file)
			if !found {
				t.Fatal("findMetaBoxAbs: not found")
			}
			if start != tc.wantStart || contentOff != tc.wantStart+tc.wantHdr+4 {
				t.Errorf("start=%d contentOff=%d, want start=%d contentOff=%d (header %d + 4 FullBox bytes)",
					start, contentOff, tc.wantStart, tc.wantStart+tc.wantHdr+4, tc.wantHdr)
			}
			// The content must begin with the first child box (pitm), whose
			// type sits 4 bytes into the content.
			if string(file[contentOff+4:contentOff+8]) != "pitm" {
				t.Errorf("content does not start at the first child box: % x", file[contentOff:contentOff+8])
			}
			if end > len(file) || end <= contentOff {
				t.Errorf("end=%d outside (contentOff=%d, len=%d]", end, contentOff, len(file))
			}
		})
	}
}

// TestTask334FindMetaBoxAbsTooSmall: a meta box smaller than its header plus
// the FullBox fields is not a valid FullBox, in either header form.
func TestTask334FindMetaBoxAbsTooSmall(t *testing.T) {
	t.Parallel()
	large := func(size uint64) []byte { // largesize meta box of the given total size, zero padded
		b := append([]byte{0, 0, 0, 1, 'm', 'e', 't', 'a'}, binary.BigEndian.AppendUint64(nil, size)...)
		return append(b, make([]byte, size-16)...)
	}
	for _, tc := range []struct {
		name string
		file []byte
		want bool
	}{
		{"8-byte header, no FullBox fields", bmffBox("meta", nil), false},
		{"8-byte header, 3 FullBox bytes", bmffBox("meta", []byte{0, 0, 0}), false},
		{"8-byte header, empty FullBox", bmffBox("meta", []byte{0, 0, 0, 0}), true},
		{"16-byte header, no FullBox fields", large(16), false},
		{"16-byte header, 3 FullBox bytes", large(19), false},
		{"16-byte header, empty FullBox", large(20), true},
	} {
		t.Run("HEIF-BOX-LARGESIZE-01/too small/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, _, found := findMetaBoxAbs(tc.file); found != tc.want {
				t.Errorf("found = %v, want %v", found, tc.want)
			}
		})
	}
}

// ilocWidths returns the field widths of the iloc box of the output meta box.
func ilocWidths(t *testing.T, metaContent []byte) (offsetSize, lengthSize int) {
	t.Helper()
	info, ok := parseIlocFull(metaContent)
	if !ok {
		t.Fatal("output iloc not parseable")
	}
	return info.offsetSize, info.lengthSize
}

// TestTask334InjectMetaHeaderForms is the #334 acceptance table: meta with
// 8- and 16-byte headers, top-level and in moov (itself 8 or 16 bytes), with
// the metadata items created or rewritten. The output must re-read to the
// new EXIF and XMP, keep the image item and the stco chunk offset pointing at
// the image, keep every box header form, and be a contiguous box sequence
// (ancestor sizes patched, including a 64-bit moov size).
func TestTask334InjectMetaHeaderForms(t *testing.T) {
	t.Parallel()
	newEXIF, newXMP := task309EXIF, task309XMP
	for _, tc := range []struct {
		name string
		l    task334Layout
	}{
		{"top-level 8", task334Layout{offsetSize: 4, lengthSize: 4}},
		{"top-level 16", task334Layout{metaLarge: true, offsetSize: 4, lengthSize: 4}},
		{"top-level 16, existing items", task334Layout{metaLarge: true, withItems: true, offsetSize: 4, lengthSize: 4}},
		{"top-level 8, existing items", task334Layout{withItems: true, offsetSize: 4, lengthSize: 4}},
		{"moov 8 meta 8", task334Layout{inMoov: true, offsetSize: 4, lengthSize: 4}},
		{"moov 8 meta 16", task334Layout{inMoov: true, metaLarge: true, offsetSize: 4, lengthSize: 4}},
		{"moov 16 meta 8", task334Layout{inMoov: true, moovLarge: true, offsetSize: 4, lengthSize: 4}},
		{"moov 16 meta 16", task334Layout{inMoov: true, moovLarge: true, metaLarge: true, offsetSize: 4, lengthSize: 4}},
		{"moov 16 meta 16, existing items", task334Layout{inMoov: true, moovLarge: true, metaLarge: true, withItems: true, offsetSize: 4, lengthSize: 4}},
	} {
		t.Run("HEIF-BOX-LARGESIZE-01/inject/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := buildTask334File(tc.l)
			out := injectBytes(t, src, newEXIF, newXMP)
			assertContiguousBoxes(t, out)

			rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out))
			if err != nil || !bytes.Equal(rawEXIF, newEXIF) || !bytes.Equal(rawXMP, newXMP) {
				t.Fatalf("read back: err=%v exif=%q xmp=%q", err, rawEXIF, rawXMP)
			}
			if got := itemBytes(t, out, 1); !bytes.Equal(got, task334Image) {
				t.Errorf("image item = %q, want %q", got, task334Image)
			}
			// Header forms are preserved.
			_, metaHdr := task334Meta(t, out)
			wantMetaHdr := 8
			if tc.l.metaLarge {
				wantMetaHdr = 16
			}
			if metaHdr != wantMetaHdr {
				t.Errorf("output meta header = %d bytes, want %d (source form kept)", metaHdr, wantMetaHdr)
			}
			if tc.l.inMoov {
				_, _, moovHdr, ok := flatBoxRangeHdr(out, boxTypeMoov)
				wantMoovHdr := 8
				if tc.l.moovLarge {
					wantMoovHdr = 16
				}
				if !ok || moovHdr != wantMoovHdr {
					t.Errorf("output moov header = %d bytes (found=%v), want %d", moovHdr, ok, wantMoovHdr)
				}
				// The stco chunk offset still addresses the image.
				body := out
				for _, name := range []string{"moov", "trak", "mdia", "minf", "stbl", "stco"} {
					var found bool
					if _, body, found = boxPayload(body, name); !found {
						t.Fatalf("output has no %s box", name)
					}
				}
				off := binary.BigEndian.Uint32(body[8:])
				if !bytes.Equal(out[off:int(off)+len(task334Image)], task334Image) {
					t.Errorf("stco chunk offset %d no longer addresses the image data", off)
				}
			}
			if off, _ := ilocWidths(t, mustMetaContent(t, out)); off != 4 {
				t.Errorf("iloc offset_size = %d, want 4", off)
			}
		})
	}
}

// mustMetaContent returns the meta content (after the FullBox fields) of file.
func mustMetaContent(t *testing.T, file []byte) []byte {
	t.Helper()
	c, _ := task334Meta(t, file)
	return c
}

// boxPayload returns the payload of the first child box of type typ in data
// (flat scan) and whether it was found; the first result is the box start.
func boxPayload(data []byte, typ string) (start int, payload []byte, found bool) {
	for pos := 0; pos < len(data); {
		size, t, hl, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			return 0, nil, false
		}
		if string(t[:]) == typ {
			return pos, data[pos+int(hl) : pos+int(size)], true //nolint:gosec // G115: box size bounded by len(data)
		}
		pos += int(size) //nolint:gosec // G115: box size bounded by len(data)
	}
	return 0, nil, false
}

// TestTask335ZeroWidthIloc is the #335 acceptance table: an iloc whose
// offset_size, length_size or both are 0 receives the new metadata; the zero
// widths are widened to 4 bytes with every existing extent value kept; the
// image item still resolves to the same bytes.
func TestTask335ZeroWidthIloc(t *testing.T) {
	t.Parallel()
	newEXIF, newXMP := task309EXIF, task309XMP
	for _, tc := range []struct {
		name string
		l    task334Layout
	}{
		// offset_size 0: the extent offset field is absent; items are located
		// by base_offset (ISO 14496-12 §8.11.3.3).
		{"offset_size 0, create items", task334Layout{offsetSize: 0, lengthSize: 4, baseOffsetSize: 4}},
		{"offset_size 0, rewrite items", task334Layout{offsetSize: 0, lengthSize: 4, baseOffsetSize: 4, withItems: true}},
		{"offset_size 0, 16-byte meta", task334Layout{offsetSize: 0, lengthSize: 4, baseOffsetSize: 4, metaLarge: true}},
		{"offset_size 0, meta in moov", task334Layout{offsetSize: 0, lengthSize: 4, baseOffsetSize: 4, inMoov: true}},
		// length_size 0: the extent length field is absent and reads as 0.
		{"length_size 0, create items", task334Layout{offsetSize: 4, lengthSize: 0}},
		{"length_size 0, meta in moov", task334Layout{offsetSize: 4, lengthSize: 0, inMoov: true}},
		{"both 0, create items", task334Layout{offsetSize: 0, lengthSize: 0, baseOffsetSize: 4}},
	} {
		t.Run("HEIF-ILOC-WIDTH0-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := buildTask334File(tc.l)
			srcMeta := mustMetaContent(t, src)
			srcInfo, ok := parseIlocFull(srcMeta)
			if !ok {
				t.Fatal("source iloc not parseable")
			}
			if srcInfo.offsetSize != tc.l.offsetSize || srcInfo.lengthSize != tc.l.lengthSize {
				t.Fatalf("fixture widths = %d/%d, want %d/%d", srcInfo.offsetSize, srcInfo.lengthSize, tc.l.offsetSize, tc.l.lengthSize)
			}

			out := injectBytes(t, src, newEXIF, newXMP)
			if bytes.Equal(out, src) {
				t.Fatal("Inject returned nil but left the file unchanged: the requested metadata was dropped")
			}
			assertContiguousBoxes(t, out)

			rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out))
			if err != nil || !bytes.Equal(rawEXIF, newEXIF) || !bytes.Equal(rawXMP, newXMP) {
				t.Fatalf("read back: err=%v exif=%q xmp=%q", err, rawEXIF, rawXMP)
			}

			// The widths were widened to hold the new values, never narrowed.
			outMeta := mustMetaContent(t, out)
			off, ln := ilocWidths(t, outMeta)
			if off != 4 || ln != 4 {
				t.Errorf("output iloc widths = %d/%d, want 4/4", off, ln)
			}

			// The image item resolves to the same bytes, with its length value
			// (absent = 0, or 5) unchanged.
			outInfo, _ := parseIlocFull(outMeta)
			var img *ilocFullItem
			for i := range outInfo.items {
				if outInfo.items[i].id == 1 {
					img = &outInfo.items[i]
				}
			}
			if img == nil || len(img.extents) != 1 {
				t.Fatalf("image item missing or malformed in output iloc: %+v", img)
			}
			start := img.baseOffset + img.extents[0].offset
			if !bytes.Equal(out[start:start+uint64(len(task334Image))], task334Image) {
				t.Errorf("image item at %d = %q, want %q", start, out[start:start+uint64(len(task334Image))], task334Image)
			}
			wantLen := uint64(len(task334Image))
			if tc.l.lengthSize == 0 {
				wantLen = 0
			}
			if img.extents[0].length != wantLen {
				t.Errorf("image item length = %d, want %d (value kept)", img.extents[0].length, wantLen)
			}
		})
	}
}

// task335Decline lists every branch of Inject that used to return nil without
// writing the requested metadata, and the outcome each has now. It is the
// enumeration the assertion test below runs; the production branches are:
//
//  1. Inject: rawEXIF == nil && rawXMP == nil — nothing requested; the only
//     remaining verbatim pass-through (rawIPTC is never stored: no IPTC
//     carrier exists in HEIF/AVIF, IPTC travels in XMP).
//  2. Inject: findMetaBoxAbs not found (no meta box, meta smaller than its
//     header + FullBox fields, in either header form) — now
//     ErrNoMetadataContainer.
//  3. buildInjectComponents/planInject: iloc missing, unsupported version,
//     illegal field width, item_count above the cap — now
//     ErrUnreadableItemLocations.
//  4. planInject: iloc truncated (item list or extent list) or an item with
//     more extents than the cap — formerly the rewritten iloc silently lost
//     the unread items/extents; now ErrUnreadableItemLocations.
//  5. planInject: offset_size or length_size 0 — now written (widened).
//  6. planInject: no pending item after planning — unreachable; errMetaLayout.
//  7. buildInjectComponents: ok == false pass-through — removed.
type task335Case struct {
	name string
	file []byte
	// want is the sentinel Inject must return, or nil when it must write.
	want error
}

// task335Cases builds the table of task335Case values.
func task335Cases() []task335Case {
	good := task334Layout{offsetSize: 4, lengthSize: 4}
	iloc := func(entries []ilocTestEntry) []byte {
		return boxWithHeader("iloc", buildIlocBody(0, 4, 4, 0, 0, entries))
	}
	withIloc := func(ilocBox []byte) []byte { // meta with iinf but a caller-chosen iloc
		metaBody := append([]byte{0, 0, 0, 0}, bmffIinf(bmffInfeV2(1, "hvc1"))...)
		metaBody = append(metaBody, ilocBox...)
		return append(bmffFtyp("heic", 0, "mif1"), bmffBox("meta", metaBody)...)
	}
	one := []ilocTestEntry{{id: 1, extents: [][2]uint64{{0, 5}}}}
	truncated := iloc(one)
	truncated = truncated[:len(truncated)-3]                      // box size no longer matches: cut the last extent bytes
	binary.BigEndian.PutUint32(truncated, uint32(len(truncated))) //nolint:gosec // G115: small test box
	oversizedExtents := iloc([]ilocTestEntry{{id: 1, extents: make([][2]uint64, maxIlocExtentsPerItem+1)}})
	manyItems := iloc(nil)
	binary.BigEndian.PutUint16(manyItems[8+6:], maxIlocItems+1) // item_count above the cap
	badWidth := iloc(one)
	badWidth[8+4] = 0x34 // offset_size 3: not in {0, 4, 8}
	badVersion := iloc(one)
	badVersion[8] = 3 // version 3 is undefined

	smallMetaLarge := append([]byte{0, 0, 0, 1, 'm', 'e', 't', 'a'}, binary.BigEndian.AppendUint64(nil, 16)...)
	return []task335Case{
		{"empty file", nil, ErrNoMetadataContainer},
		{"ftyp only", bmffFtyp("heic", 0, "mif1"), ErrNoMetadataContainer},
		{"meta of 8 bytes", append(bmffFtyp("heic", 0, "mif1"), bmffBox("meta", nil)...), ErrNoMetadataContainer},
		{"meta of 11 bytes", append(bmffFtyp("heic", 0, "mif1"), bmffBox("meta", []byte{0, 0, 0})...), ErrNoMetadataContainer},
		{"largesize meta of 16 bytes", append(bmffFtyp("heic", 0, "mif1"), smallMetaLarge...), ErrNoMetadataContainer},
		{"meta without iloc", append(bmffFtyp("heic", 0, "mif1"), bmffBox("meta", []byte{0, 0, 0, 0})...), ErrUnreadableItemLocations},
		{"iloc version 3", withIloc(badVersion), ErrUnreadableItemLocations},
		{"iloc field width 3", withIloc(badWidth), ErrUnreadableItemLocations},
		{"iloc item_count above cap", withIloc(manyItems), ErrUnreadableItemLocations},
		{"iloc truncated extent list", withIloc(truncated), ErrUnreadableItemLocations},
		{"iloc extent_count above cap", withIloc(oversizedExtents), ErrUnreadableItemLocations},
		{"offset_size 0", buildTask334File(task334Layout{offsetSize: 0, lengthSize: 4, baseOffsetSize: 4}), nil},
		{"length_size 0", buildTask334File(task334Layout{offsetSize: 4, lengthSize: 0}), nil},
		{"offset_size and length_size 0", buildTask334File(task334Layout{}), nil},
		{"largesize meta", buildTask334File(task334Layout{metaLarge: true, offsetSize: 4, lengthSize: 4}), nil},
		{"meta in moov", buildTask334File(task334Layout{inMoov: true, offsetSize: 4, lengthSize: 4}), nil},
		{"well-formed", buildTask334File(good), nil},
	}
}

// TestTask335InjectNeverReturnsNilWithoutWriting is the assertion test of
// task #335: over every branch of Inject that can decline (task335Case),
// Inject either writes the requested payloads — proven by reading them back —
// or returns the documented error and writes nothing. It never returns nil
// while discarding a requested change.
func TestTask335InjectNeverReturnsNilWithoutWriting(t *testing.T) {
	t.Parallel()
	for _, tc := range task335Cases() {
		for _, req := range []struct {
			name      string
			exif, xmp []byte
		}{
			{"EXIF and XMP", task309EXIF, task309XMP},
			{"EXIF only", task309EXIF, nil},
			{"XMP only", nil, task309XMP},
		} {
			t.Run("HEIF-WRITE-NODROP-01/"+tc.name+"/"+req.name, func(t *testing.T) {
				t.Parallel()
				var out bytes.Buffer
				err := Inject(bytes.NewReader(tc.file), &out, req.exif, nil, req.xmp, true)
				if tc.want != nil {
					if !errors.Is(err, tc.want) {
						t.Fatalf("Inject error = %v, want %v", err, tc.want)
					}
					if out.Len() != 0 {
						t.Errorf("a refused write produced %d bytes of output", out.Len())
					}
					return
				}
				if err != nil {
					t.Fatalf("Inject: %v", err)
				}
				if bytes.Equal(out.Bytes(), tc.file) {
					t.Fatal("Inject returned nil but left the file unchanged")
				}
				rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("Extract: %v", err)
				}
				if !bytes.Equal(rawEXIF, req.exif) || !bytes.Equal(rawXMP, req.xmp) {
					t.Errorf("read back exif=%q xmp=%q, want %q and %q", rawEXIF, rawXMP, req.exif, req.xmp)
				}
			})
		}
	}
}

// TestTask335NothingRequestedIsVerbatim: the one remaining pass-through — no
// payload requested — copies the file byte for byte, whatever its structure.
func TestTask335NothingRequestedIsVerbatim(t *testing.T) {
	t.Parallel()
	for _, tc := range task335Cases() {
		t.Run("HEIF-WRITE-NODROP-01/verbatim/"+tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(tc.file), &out, nil, nil, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if !bytes.Equal(out.Bytes(), tc.file) {
				t.Error("pass-through changed the file")
			}
		})
	}
}

// TestTask335WidenIlocFields: only zero widths are widened; a non-zero width,
// including 8, is kept.
func TestTask335WidenIlocFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ inOff, inLen, wantOff, wantLen int }{
		{0, 0, 4, 4}, {0, 4, 4, 4}, {4, 0, 4, 4}, {4, 4, 4, 4}, {8, 0, 8, 4}, {0, 8, 4, 8}, {8, 8, 8, 8},
	} {
		info := ilocBoxInfo{offsetSize: tc.inOff, lengthSize: tc.inLen}
		widenIlocFields(&info)
		if info.offsetSize != tc.wantOff || info.lengthSize != tc.wantLen {
			t.Errorf("widen(%d,%d) = %d/%d, want %d/%d", tc.inOff, tc.inLen, info.offsetSize, info.lengthSize, tc.wantOff, tc.wantLen)
		}
	}
}
