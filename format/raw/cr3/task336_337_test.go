package cr3

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Tasks #336 and #337.
//
// #336: CMT2 is a stand-alone TIFF stream whose first IFD is the Exif IFD
// (lclevy canon_cr3; imagemeta readCrxMoovBox). It was never parsed, because
// only an ExifIFD pointer in CMT1 reached it, and a write created a second
// Exif IFD inside CMT1.
//
// #337: deleting the GPS IFD did not remove the GPS data: the source CMT4 was
// kept. ExifTool removes the CMT4 box when asked to delete the GPS group; the
// corpus shows Canon firmware writes a CMT4 holding only GPSVersionID when
// there is no GPS fix, so an empty GPS IFD would still read as "GPS present".

// asciiEntry returns an ASCII IFD entry.
func asciiEntry(tag exif.TagID, s string) exif.IFDEntry {
	return exif.IFDEntry{Tag: tag, Type: exif.TypeASCII, Count: uint32(len(s) + 1), Value: append([]byte(s), 0)} //nolint:gosec // G115: tiny test string
}

// shortEntry returns a SHORT IFD entry holding v.
func shortEntry(tag exif.TagID, v uint16) exif.IFDEntry {
	return exif.IFDEntry{Tag: tag, Type: exif.TypeShort, Count: 1, Value: binary.LittleEndian.AppendUint16(nil, v)}
}

// task336Model is an EXIF model as the root package builds it from a CR3 file:
// IFD0 with a model, an Exif IFD (date, ISO) and, optionally, an
// Interoperability IFD and a GPS IFD.
func task336Model(withInterop, withGPS bool) *exif.EXIF {
	e := &exif.EXIF{
		ByteOrder: binary.LittleEndian,
		IFD0:      &exif.IFD{Entries: []exif.IFDEntry{asciiEntry(exif.TagModel, "Canon EOS R8")}},
		ExifIFD: &exif.IFD{Entries: []exif.IFDEntry{
			asciiEntry(exif.TagDateTimeOriginal, "2024:05:06 07:08:09"),
			shortEntry(exif.TagISOSpeedRatings, 400),
		}},
	}
	if withInterop {
		e.InteropIFD = &exif.IFD{Entries: []exif.IFDEntry{asciiEntry(0x0001, "R98")}}
	}
	if withGPS {
		e.GPSIFD = &exif.IFD{Entries: []exif.IFDEntry{{Tag: 0, Type: exif.TypeByte, Count: 4, Value: []byte{2, 3, 0, 0}}}}
	}
	return e
}

// encodeModel encodes e, failing t on error.
func encodeModel(t *testing.T, e *exif.EXIF) []byte {
	t.Helper()
	b, err := exif.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// injectCR3 injects rawEXIF into src and returns the output.
func injectCR3(t *testing.T, src, rawEXIF []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, rawEXIF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

// extractAll runs ExtractSegments on file.
func extractAll(t *testing.T, file []byte) Segments {
	t.Helper()
	seg, err := ExtractSegments(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("ExtractSegments: %v", err)
	}
	return seg
}

// tagValue returns the value of tag in ifd, or nil.
func tagValue(ifd *exif.IFD, tag exif.TagID) []byte {
	if ifd == nil {
		return nil
	}
	if e := ifd.Get(tag); e != nil {
		return e.Value
	}
	return nil
}

// TestTask336ParseExifStream: CMT2 is returned by ExtractSegments as its own
// stream and parsed as a stand-alone TIFF stream whose first IFD is the Exif
// IFD, with the Interoperability IFD behind its 0xA005 pointer.
func TestTask336ParseExifStream(t *testing.T) {
	t.Parallel()
	t.Run("CR3-CMT2-READ-01", func(t *testing.T) {
		t.Parallel()
		st, err := splitEXIF(encodeModel(t, task336Model(true, false)))
		if err != nil {
			t.Fatal(err)
		}
		if st.cmt2 == nil {
			t.Fatal("splitEXIF produced no CMT2")
		}
		src := buildTask300CR3(cr3Fixture{cmt1: st.cmt1, cmt2: st.cmt2, mdat: []byte("RAW")})
		seg := extractAll(t, src)
		if !bytes.Equal(seg.ExifIFD, st.cmt2) {
			t.Fatal("Segments.ExifIFD differs from the CMT2 payload")
		}
		ifd, interop, order, err := ParseExifStream(seg.ExifIFD)
		if err != nil || order != binary.LittleEndian {
			t.Fatalf("ParseExifStream: order=%v err=%v", order, err)
		}
		if got := tagValue(ifd, exif.TagDateTimeOriginal); string(got) != "2024:05:06 07:08:09\x00" {
			t.Errorf("DateTimeOriginal = %q", got)
		}
		if got := tagValue(interop, 0x0001); string(got) != "R98\x00" {
			t.Errorf("InteropIndex = %q, want the Interoperability IFD behind the 0xA005 pointer", got)
		}
		// CMT1 alone has no ExifIFD pointer: the Exif IFD is reachable only
		// through CMT2.
		e, err := exif.Parse(seg.EXIF)
		if err != nil || e.ExifIFD != nil || e.IFD0.Get(exif.TagExifIFDPointer) != nil {
			t.Errorf("CMT1 carries an Exif IFD (err=%v)", err)
		}
	})
	t.Run("CR3-CMT2-READ-01/bad stream", func(t *testing.T) {
		t.Parallel()
		for _, b := range [][]byte{nil, {}, []byte("II*"), []byte("XX\x2a\x00\x08\x00\x00\x00"), []byte("II\x2a\x00\xff\xff\xff\xff")} {
			if _, _, _, err := ParseExifStream(b); err == nil {
				t.Errorf("ParseExifStream(%x) succeeded, want ErrBadCMTStream", b)
			}
		}
	})
	t.Run("CR3-CMT2-READ-01/absent", func(t *testing.T) {
		t.Parallel()
		seg := extractAll(t, buildTask300CR3(cr3Fixture{cmt1: minimalTIFF(), mdat: []byte("RAW")}))
		if seg.ExifIFD != nil {
			t.Error("Segments.ExifIFD set for a file without CMT2")
		}
	})
}

// TestTask336SplitEXIF: the Exif IFD (with its Interoperability IFD) and the
// GPS IFD leave CMT1 for their own streams.
func TestTask336SplitEXIF(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		interop, gps       bool
		wantCMT2, wantCMT4 bool
	}{
		{"exif only", false, false, true, false},
		{"exif and interop", true, false, true, false},
		{"exif and gps", false, true, true, true},
		{"exif, interop and gps", true, true, true, true},
	} {
		t.Run("CR3-CMT2-WRITE-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := splitEXIF(encodeModel(t, task336Model(tc.interop, tc.gps)))
			if err != nil {
				t.Fatal(err)
			}
			e, err := exif.Parse(st.cmt1)
			if err != nil {
				t.Fatalf("parse CMT1: %v", err)
			}
			if e.ExifIFD != nil || e.GPSIFD != nil || e.IFD0.Get(exif.TagExifIFDPointer) != nil || e.IFD0.Get(exif.TagGPSIFDPointer) != nil {
				t.Error("CMT1 still carries a sub-IFD pointer")
			}
			if string(tagValue(e.IFD0, exif.TagModel)) != "Canon EOS R8\x00" {
				t.Error("CMT1 lost the IFD0 Model")
			}
			if (st.cmt2 != nil) != tc.wantCMT2 || (st.cmt4 != nil) != tc.wantCMT4 {
				t.Fatalf("cmt2=%v cmt4=%v, want %v %v", st.cmt2 != nil, st.cmt4 != nil, tc.wantCMT2, tc.wantCMT4)
			}
			ifd, interop, _, err := ParseExifStream(st.cmt2)
			if err != nil {
				t.Fatalf("parse CMT2: %v", err)
			}
			if ifd.Get(exif.TagDateTimeOriginal) == nil || ifd.Get(exif.TagISOSpeedRatings) == nil {
				t.Error("CMT2 lost an Exif IFD entry")
			}
			if tc.interop != (interop != nil && interop.Get(0x0001) != nil) {
				t.Errorf("Interoperability IFD in CMT2 = %v, want %v", interop != nil, tc.interop)
			}
			// Every offset in CMT2 counts from its own first byte: the stream
			// parses on its own and carries no pointer to a missing IFD.
			if ifd.Get(exif.TagExifIFDPointer) != nil || ifd.Get(exif.TagGPSIFDPointer) != nil {
				t.Error("CMT2 carries an ExifIFD or GPSIFD pointer")
			}
		})
	}
	t.Run("CR3-CMT2-WRITE-01/no sub-IFD keeps rawEXIF", func(t *testing.T) {
		t.Parallel()
		raw := minimalTIFF()
		st, err := splitEXIF(raw)
		if err != nil || !bytes.Equal(st.cmt1, raw) || st.cmt2 != nil || st.cmt4 != nil || st.order != binary.LittleEndian {
			t.Errorf("splitEXIF(minimal) = %+v, %v; want rawEXIF as CMT1 only", st, err)
		}
	})
	t.Run("CR3-CMT2-WRITE-01/not TIFF", func(t *testing.T) {
		t.Parallel()
		st, err := splitEXIF([]byte("garbage"))
		if err != nil || string(st.cmt1) != "garbage" || st.order != nil {
			t.Errorf("splitEXIF(garbage) = %+v, %v", st, err)
		}
	})
}

// TestTask336InjectCMT2 is the CMT2 write contract: a changed Exif IFD
// replaces CMT2 in place, an unchanged one keeps the source bytes, a missing
// CMT2 is created right after CMT1, and no Exif IFD appears twice.
func TestTask336InjectCMT2(t *testing.T) {
	t.Parallel()
	base := task336Model(false, false)
	srcStream, err := splitEXIF(encodeModel(t, base))
	if err != nil {
		t.Fatal(err)
	}
	// A CMT2 padded with zero bytes as Canon pads it: a byte-exact keep is
	// distinguishable from a re-encoding.
	paddedCMT2 := append(append([]byte{}, srcStream.cmt2...), make([]byte, 64)...)
	cmt3 := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x0006, Type: exif.TypeASCII, Count: 4, Value: []byte("JPG\x00")})
	mdat := []byte("RAW-SENSOR-DATA-0123456789")

	changed := task336Model(false, false)
	changed.ExifIFD.Entries[0] = asciiEntry(exif.TagDateTimeOriginal, "2030:01:02 03:04:05")
	withInterop := task336Model(true, false)
	// The models carry the source GPS IFD, so that CMT4 is kept (a model
	// without a GPS IFD removes it, task #337).
	gps, _, err := ParseSubIFD(gpsCMT4(t))
	if err != nil {
		t.Fatal(err)
	}
	base.GPSIFD, changed.GPSIFD, withInterop.GPSIFD = gps, gps, gps

	for _, tc := range []struct {
		name    string
		srcCMT2 []byte
		model   *exif.EXIF
		want    string // DateTimeOriginal in the output CMT2
		sameCMT bool   // CMT2 kept byte for byte
		wantInt bool   // Interoperability IFD in the output CMT2
	}{
		{"unchanged keeps CMT2", paddedCMT2, base, "2024:05:06 07:08:09", true, false},
		{"changed replaces CMT2", paddedCMT2, changed, "2030:01:02 03:04:05", false, false},
		{"missing CMT2 is created", nil, base, "2024:05:06 07:08:09", false, false},
		{"interop IFD is written", paddedCMT2, withInterop, "2024:05:06 07:08:09", false, true},
	} {
		t.Run("CR3-CMT2-WRITE-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := buildTask300CR3(cr3Fixture{cmt1: minimalTIFF(), cmt2: tc.srcCMT2, cmt3: cmt3, cmt4: gpsCMT4(t), mdat: mdat})
			out := injectCR3(t, src, encodeModel(t, tc.model))
			seg := extractAll(t, out)

			e, err := exif.Parse(seg.EXIF)
			if err != nil || e.ExifIFD != nil || e.IFD0.Get(exif.TagExifIFDPointer) != nil {
				t.Fatalf("CMT1 holds an Exif IFD (err=%v): a second copy of the Exif tags", err)
			}
			if tc.sameCMT && !bytes.Equal(seg.ExifIFD, tc.srcCMT2) {
				t.Error("unchanged CMT2 was rewritten")
			}
			ifd, interop, _, err := ParseExifStream(seg.ExifIFD)
			if err != nil {
				t.Fatalf("CMT2: %v", err)
			}
			if got := string(tagValue(ifd, exif.TagDateTimeOriginal)); got != tc.want+"\x00" {
				t.Errorf("CMT2 DateTimeOriginal = %q, want %q", got, tc.want)
			}
			if tc.wantInt != (interop != nil && interop.Get(0x0001) != nil) {
				t.Errorf("CMT2 Interoperability IFD present = %v, want %v", interop != nil, tc.wantInt)
			}
			if bytes.Count(out, []byte("CMT2")) != 1 {
				t.Errorf("output holds %d CMT2 boxes, want 1", bytes.Count(out, []byte("CMT2")))
			}
			if !bytes.Equal(seg.MakerNote, cmt3) || !bytes.Equal(seg.GPS, gpsCMT4(t)) {
				t.Error("CMT3 or CMT4 changed")
			}
			// Canon order: CMT1 < CMT2 < CMT3 < CMT4.
			canon := findUUIDBox(flatBox(out, "moov"), canonUUID)
			i1, i2, i3, i4 := bytes.Index(canon, []byte("CMT1")), bytes.Index(canon, []byte("CMT2")), bytes.Index(canon, []byte("CMT3")), bytes.Index(canon, []byte("CMT4"))
			if i1 >= i2 || i2 >= i3 || i3 >= i4 {
				t.Errorf("sub-box order CMT1@%d CMT2@%d CMT3@%d CMT4@%d", i1, i2, i3, i4)
			}
			stco, co64 := chunkTargets(t, out)
			if !bytes.Equal(out[stco:stco+uint32(len(mdat))], mdat) || !bytes.Equal(out[co64:co64+4], mdat[4:8]) { //nolint:gosec // G115: small test length
				t.Errorf("chunk offsets stco=%d co64=%d no longer address the mdat data", stco, co64)
			}
		})
	}
}

// TestTask336CMT2WithoutCMT1: a source without CMT1 gets CMT1 appended, then
// CMT2 after it.
func TestTask336CMT2WithoutCMT1(t *testing.T) {
	t.Parallel()
	t.Run("CR3-CMT2-WRITE-01/no CMT1 in source", func(t *testing.T) {
		t.Parallel()
		cmt3 := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x0006, Type: exif.TypeASCII, Count: 4, Value: []byte("JPG\x00")})
		src := buildTask300CR3(cr3Fixture{cmt3: cmt3, mdat: []byte("RAW")})
		out := injectCR3(t, src, encodeModel(t, task336Model(false, false)))
		seg := extractAll(t, out)
		if seg.ExifIFD == nil || seg.EXIF == nil {
			t.Fatal("CMT1 or CMT2 missing in the output")
		}
		canon := findUUIDBox(flatBox(out, "moov"), canonUUID)
		if i1, i2 := bytes.Index(canon, []byte("CMT1")), bytes.Index(canon, []byte("CMT2")); i1 < 0 || i1 >= i2 {
			t.Errorf("sub-box order CMT1@%d CMT2@%d, want CMT1 < CMT2", i1, i2)
		}
	})
}

// TestTask337GPSRemoval: a rawEXIF without a GPS IFD removes the source CMT4
// (ExifTool deletes CMT4 for -GPS:all=), but only when the reader exposes that
// CMT4 — a CMT4 the caller could not have seen is kept. The box, uuid and
// moov sizes and the chunk offsets follow the removal.
func TestTask337GPSRemoval(t *testing.T) {
	t.Parallel()
	le := task336Model(false, false)
	beGPS := tiffBE(t, exif.IFDEntry{Tag: 0, Type: exif.TypeByte, Count: 4, Value: []byte{2, 3, 0, 0}})
	cmt3 := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x0006, Type: exif.TypeASCII, Count: 4, Value: []byte("JPG\x00")})
	mdat := []byte("RAW-SENSOR-DATA-0123456789")
	for _, tc := range []struct {
		name     string
		fixture  cr3Fixture
		model    *exif.EXIF
		wantCMT4 bool
	}{
		{"GPS removed", cr3Fixture{cmt1: minimalTIFF(), cmt3: cmt3, cmt4: gpsCMT4(t)}, le, false},
		{"GPS removed, no CMT3", cr3Fixture{cmt1: minimalTIFF(), cmt4: gpsCMT4(t)}, le, false},
		{"no CMT4 stays absent", cr3Fixture{cmt1: minimalTIFF(), cmt3: cmt3}, le, false},
		{"other byte order is kept", cr3Fixture{cmt1: minimalTIFF(), cmt3: cmt3, cmt4: beGPS}, le, true},
		{"unreadable CMT4 is kept", cr3Fixture{cmt1: minimalTIFF(), cmt3: cmt3, cmt4: []byte("not a TIFF stream")}, le, true},
		{"CMT4 without CMT1 is kept", cr3Fixture{cmt3: cmt3, cmt4: gpsCMT4(t)}, le, true},
	} {
		t.Run("CR3-CMT4-DELETE-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			fx := tc.fixture
			fx.mdat = mdat
			src := buildTask300CR3(fx)
			out := injectCR3(t, src, encodeModel(t, tc.model))
			seg := extractAll(t, out)
			if (seg.GPS != nil) != tc.wantCMT4 {
				t.Fatalf("CMT4 present = %v, want %v", seg.GPS != nil, tc.wantCMT4)
			}
			if tc.wantCMT4 && !bytes.Equal(seg.GPS, fx.cmt4) {
				t.Error("a kept CMT4 was rewritten")
			}
			if fx.cmt3 != nil && !bytes.Equal(seg.MakerNote, cmt3) {
				t.Error("CMT3 changed")
			}
			// Box structure: every box ends where its parent says, and the
			// chunk offsets still address the mdat data.
			assertBoxTreeConsistent(t, out)
			stco, co64 := chunkTargets(t, out)
			if !bytes.Equal(out[stco:stco+uint32(len(mdat))], mdat) || !bytes.Equal(out[co64:co64+4], mdat[4:8]) { //nolint:gosec // G115: small test length
				t.Errorf("chunk offsets stco=%d co64=%d no longer address the mdat data", stco, co64)
			}
			if !tc.wantCMT4 && fx.cmt4 != nil && len(out) >= len(src) {
				t.Errorf("output (%d bytes) did not shrink below the source (%d) after removing CMT4", len(out), len(src))
			}
		})
	}
	t.Run("CR3-CMT4-DELETE-01/unchanged GPS keeps CMT4 byte for byte", func(t *testing.T) {
		t.Parallel()
		cmt4 := gpsCMT4(t)
		src := buildTask300CR3(cr3Fixture{cmt1: minimalTIFF(), cmt3: cmt3, cmt4: cmt4, mdat: mdat})
		gps, _, err := ParseSubIFD(cmt4)
		if err != nil {
			t.Fatal(err)
		}
		m := task336Model(false, false)
		m.GPSIFD = gps
		out := injectCR3(t, src, encodeModel(t, m))
		if seg := extractAll(t, out); !bytes.Equal(seg.GPS, cmt4) {
			t.Error("an unchanged GPS IFD was rewritten")
		}
	})
	t.Run("CR3-CMT4-DELETE-01/Exif IFD removed removes CMT2", func(t *testing.T) {
		t.Parallel()
		st, err := splitEXIF(encodeModel(t, le))
		if err != nil {
			t.Fatal(err)
		}
		src := buildTask300CR3(cr3Fixture{cmt1: minimalTIFF(), cmt2: st.cmt2, cmt3: cmt3, mdat: mdat})
		// rawEXIF with IFD0 only: the caller deleted the Exif IFD.
		out := injectCR3(t, src, encodeModel(t, &exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: le.IFD0}))
		if seg := extractAll(t, out); seg.ExifIFD != nil {
			t.Error("CMT2 kept although the Exif IFD was removed")
		}
	})
}

// tiffBE returns a big-endian TIFF stream whose first IFD holds entries.
func tiffBE(t *testing.T, entries ...exif.IFDEntry) []byte {
	t.Helper()
	b, err := exif.Encode(&exif.EXIF{ByteOrder: binary.BigEndian, IFD0: &exif.IFD{Entries: entries}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertBoxTreeConsistent fails t unless the top-level boxes of file tile it
// exactly and, inside moov and the Canon uuid box, every child box ends
// within its parent.
func assertBoxTreeConsistent(t *testing.T, file []byte) {
	t.Helper()
	var walk func(data []byte, depth int)
	walk = func(data []byte, depth int) {
		pos := 0
		for pos < len(data) {
			size, typ, hl, ok := parseCR3BoxHeader(data, pos)
			if !ok {
				t.Fatalf("malformed box at offset %d (depth %d)", pos, depth)
			}
			if string(typ[:]) == "moov" {
				walk(data[pos+int(hl):pos+int(size)], depth+1) //nolint:gosec // G115: bounded by len(data)
			}
			if string(typ[:]) == "uuid" && size >= hl+16 && bytes.Equal(data[pos+int(hl):pos+int(hl)+16], canonUUID) { //nolint:gosec // G115: bounded by len(data)
				walk(data[pos+int(hl)+16:pos+int(size)], depth+1) //nolint:gosec // G115: bounded by len(data)
			}
			pos += int(size) //nolint:gosec // G115: bounded by len(data)
		}
		if pos != len(data) {
			t.Fatalf("boxes end at %d, container is %d bytes (depth %d)", pos, len(data), depth)
		}
	}
	walk(file, 0)
}
