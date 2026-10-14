package cr3

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Task #300: CR3 stores its XMP packet in a top-level uuid box
// (be7acfcb-97a9-42e8-9c71-999491e3afac) and its MakerNote and GPS IFDs in
// the CMT3 and CMT4 sub-boxes of the Canon uuid box (imagemeta
// readCrxMoovBox). The former code read none of them and wrote XMP into a
// non-standard "XMP " sub-box that no other reader sees.

// cr3Fixture describes a synthetic CR3 file for buildTask300CR3.
type cr3Fixture struct {
	cmt1, cmt3, cmt4 []byte // nil = sub-box absent
	legacyXMP        []byte // "XMP " sub-box of the Canon uuid box
	topXMP           []byte // top-level XMP uuid box payload
	xmpBeforeMoov    bool   // place the XMP uuid box before moov
	mdat             []byte
}

// buildTask300CR3 assembles ftyp + moov{Canon uuid{CMT1..CMT4, XMP }, trak
// with an stco and a co64 box pointing at the mdat payload} + [XMP uuid] +
// mdat. The chunk offsets are absolute, so the file is built twice.
func buildTask300CR3(fx cr3Fixture) []byte {
	build := func(mdatData uint64) []byte {
		var canon []byte
		if fx.cmt1 != nil {
			canon = append(canon, buildBox("CMT1", fx.cmt1)...)
		}
		if fx.cmt3 != nil {
			canon = append(canon, buildBox("CMT3", fx.cmt3)...)
		}
		if fx.cmt4 != nil {
			canon = append(canon, buildBox("CMT4", fx.cmt4)...)
		}
		if fx.legacyXMP != nil {
			canon = append(canon, buildBox("XMP ", fx.legacyXMP)...)
		}
		canon = append(canon, buildBox("THMB", []byte("thumbnail"))...)
		stco := buildBox("stco", binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32([]byte{0, 0, 0, 0}, 1), uint32(mdatData))) //nolint:gosec // G115: test file is small
		co64 := buildBox("co64", binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint32([]byte{0, 0, 0, 0}, 1), mdatData+4))
		trak := buildBox("trak", buildBox("mdia", buildBox("minf", buildBox("stbl", append(stco, co64...)))))
		moov := buildBox("moov", append(buildUUIDBox(canonUUID, canon), trak...))
		ftyp := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'c', 'r', 'x', ' ', 0, 0, 0, 1}
		out := append([]byte{}, ftyp...)
		var xmpBox []byte
		if fx.topXMP != nil {
			xmpBox = buildUUIDBox(xmpUUID, fx.topXMP)
		}
		if fx.xmpBeforeMoov {
			out = append(out, xmpBox...)
			out = append(out, moov...)
		} else {
			out = append(out, moov...)
			out = append(out, xmpBox...)
		}
		return append(out, buildBox("mdat", fx.mdat)...)
	}
	first := build(0)
	return build(uint64(len(first) - len(fx.mdat))) //nolint:gosec // G115: non-negative test length
}

// tiffWithIFD0 encodes a TIFF stream whose first IFD holds entries.
func tiffWithIFD0(t *testing.T, entries ...exif.IFDEntry) []byte {
	t.Helper()
	out, err := exif.Encode(&exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: &exif.IFD{Entries: entries}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// gpsCMT4 returns a CMT4 stream with GPSVersionID 2.3.0.0, padded with zero
// bytes after the IFD as Canon pads it, so that a byte-exact keep is
// distinguishable from a re-encoding.
func gpsCMT4(t *testing.T) []byte {
	t.Helper()
	s := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x0000, Type: exif.TypeByte, Count: 4, Value: []byte{2, 3, 0, 0}})
	return append(s, make([]byte, 64)...)
}

// flatBox returns the payload of the first child box of type typ in data,
// without descending into containers.
func flatBox(data []byte, typ string) []byte {
	for pos := 0; pos+8 <= len(data); {
		size, t, hl, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			return nil
		}
		if string(t[:]) == typ {
			return data[pos+int(hl) : pos+int(size)] //nolint:gosec // G115: box size bounded by len(data)
		}
		pos += int(size) //nolint:gosec // G115: box size bounded by len(data)
	}
	return nil
}

// chunkTargets returns the stco and co64 chunk offsets of file.
func chunkTargets(t *testing.T, file []byte) (stco uint32, co64 uint64) {
	t.Helper()
	body := file
	for _, name := range []string{"moov", "trak", "mdia", "minf", "stbl"} {
		body = flatBox(body, name)
	}
	s, c := flatBox(body, "stco"), flatBox(body, "co64")
	if len(s) < 12 || len(c) < 16 {
		t.Fatal("stco/co64 not found")
	}
	return binary.BigEndian.Uint32(s[8:]), binary.BigEndian.Uint64(c[8:])
}

func TestTask300ExtractReadsTopLevelXMPAndCMT3CMT4(t *testing.T) {
	t.Parallel()
	cmt1 := minimalTIFF()
	cmt3 := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x0006, Type: exif.TypeASCII, Count: 4, Value: []byte("JPG\x00")})
	cmt4 := gpsCMT4(t)
	top := []byte("<x:xmpmeta>top-level</x:xmpmeta>")
	legacy := []byte("<x:xmpmeta>legacy</x:xmpmeta>")
	data := buildTask300CR3(cr3Fixture{cmt1: cmt1, cmt3: cmt3, cmt4: cmt4, legacyXMP: legacy, topXMP: top, mdat: []byte("RAWDATA-RAWDATA")})

	seg, err := ExtractSegments(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ExtractSegments: %v", err)
	}
	if !bytes.Equal(seg.XMP, top) {
		t.Errorf("XMP = %q, want the top-level uuid packet %q (it takes precedence over the legacy sub-box)", seg.XMP, top)
	}
	if !bytes.Equal(seg.MakerNote, cmt3) || !bytes.Equal(seg.GPS, cmt4) || !bytes.Equal(seg.EXIF, cmt1) {
		t.Errorf("CMT streams: EXIF %d/%d MakerNote %d/%d GPS %d/%d bytes", len(seg.EXIF), len(cmt1), len(seg.MakerNote), len(cmt3), len(seg.GPS), len(cmt4))
	}
	gps, order, err := ParseSubIFD(seg.GPS)
	if err != nil || order != binary.LittleEndian || gps.Get(0x0000) == nil {
		t.Errorf("ParseSubIFD(CMT4) = %v, %v, %v; want the GPSVersionID entry", gps, order, err)
	}
	mn, _, err := ParseSubIFD(seg.MakerNote)
	if err != nil || mn.Get(0x0006) == nil {
		t.Errorf("ParseSubIFD(CMT3) = %v, %v; want the MakerNote entry 0x0006", mn, err)
	}

	// Without a top-level XMP uuid box, the legacy sub-box is the fallback.
	data = buildTask300CR3(cr3Fixture{cmt1: cmt1, legacyXMP: legacy, mdat: []byte("RAW")})
	if _, _, rawXMP, err := Extract(bytes.NewReader(data)); err != nil || !bytes.Equal(rawXMP, legacy) {
		t.Errorf("legacy fallback: XMP = %q (%v), want %q", rawXMP, err, legacy)
	}
}

func TestTask300ParseSubIFDRejectsBadStreams(t *testing.T) {
	t.Parallel()
	for _, b := range [][]byte{nil, []byte("II*"), []byte("XX*\x00\x08\x00\x00\x00"), []byte("II+\x00\x08\x00\x00\x00"), []byte("II*\x00\xFF\x00\x00\x00")} {
		if _, _, err := ParseSubIFD(b); err == nil {
			t.Errorf("ParseSubIFD(%x) succeeded, want ErrBadCMTStream", b)
		}
	}
}

// TestTask300InjectXMPTopLevel: the packet is written into the top-level XMP
// uuid box (replaced in place, or inserted right after moov), no "XMP "
// sub-box is created, an existing one is kept in step, and the chunk offsets
// still address the same mdat bytes.
func TestTask300InjectXMPTopLevel(t *testing.T) {
	t.Parallel()
	newXMP := []byte("<x:xmpmeta>new packet, longer than the old one</x:xmpmeta>")
	mdat := []byte("RAW-SENSOR-DATA-0123456789")
	for _, fx := range []struct {
		name string
		fx   cr3Fixture
	}{
		{"replace existing XMP uuid", cr3Fixture{cmt1: minimalTIFF(), topXMP: bytes.Repeat([]byte(" "), 200), mdat: mdat}},
		{"insert XMP uuid after moov", cr3Fixture{cmt1: minimalTIFF(), mdat: mdat}},
		{"XMP uuid before moov", cr3Fixture{cmt1: minimalTIFF(), topXMP: []byte("<x/>"), xmpBeforeMoov: true, mdat: mdat}},
		{"legacy sub-box kept in step", cr3Fixture{cmt1: minimalTIFF(), legacyXMP: []byte("<x:xmpmeta>old</x:xmpmeta>"), mdat: mdat}},
	} {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			src := buildTask300CR3(fx.fx)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, nil, nil, newXMP, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			got := out.Bytes()
			lay, err := scanTopLevel(bytes.NewReader(got), int64(len(got)), true)
			if err != nil || !bytes.Equal(lay.xmp, newXMP) {
				t.Fatalf("top-level XMP uuid = %q (%v), want the new packet", lay.xmp, err)
			}
			canon := findUUIDBox(lay.moov, canonUUID)
			legacy := findBox(canon, boxXMP, 0)
			if fx.fx.legacyXMP == nil && legacy != nil {
				t.Error("an \"XMP \" sub-box was created")
			}
			if fx.fx.legacyXMP != nil && !bytes.Equal(legacy, newXMP) {
				t.Errorf("legacy \"XMP \" sub-box = %q, want it kept in step", legacy)
			}
			stco, co64 := chunkTargets(t, got)
			if !bytes.Equal(got[stco:stco+uint32(len(mdat))], mdat) || !bytes.Equal(got[co64:co64+4], mdat[4:8]) { //nolint:gosec // G115: small test length
				t.Errorf("chunk offsets stco=%d co64=%d no longer address the mdat data", stco, co64)
			}
		})
	}
}

// TestTask300ChunkShiftComparesSourceOffsets is the regression gate for a
// defect found by the corpus sweep while implementing this task: with two
// edited boxes where the first SHRINKS, comparing the partially shifted
// offset with the second box's end skipped the second shift.
func TestTask300ChunkShiftComparesSourceOffsets(t *testing.T) {
	t.Parallel()
	var s chunkShift
	s.add(1000, -150)
	s.add(1100, 400)
	for _, tc := range [][2]int64{{999, 999}, {1000, 850}, {1099, 949}, {1100, 1350}, {5000, 5250}} {
		if got := s.apply(tc[0]); got != tc[1] {
			t.Errorf("apply(%d) = %d, want %d", tc[0], got, tc[1])
		}
	}
	// End to end: CMT1 shrinks (moov shrinks) while the XMP box grows.
	big := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x010E, Type: exif.TypeASCII, Count: 300, Value: append(bytes.Repeat([]byte("x"), 299), 0)})
	mdat := []byte("RAW-SENSOR-DATA-0123456789")
	src := buildTask300CR3(cr3Fixture{cmt1: big, topXMP: []byte("<x/>"), mdat: mdat})
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, minimalTIFF(), nil, bytes.Repeat([]byte("y"), 500), true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	got := out.Bytes()
	stco, co64 := chunkTargets(t, got)
	if !bytes.Equal(got[stco:stco+uint32(len(mdat))], mdat) || !bytes.Equal(got[co64:co64+4], mdat[4:8]) { //nolint:gosec // G115: small test length
		t.Errorf("chunk offsets stco=%d co64=%d no longer address the mdat data", stco, co64)
	}
}

// TestTask300GPSGoesToCMT4: a GPS IFD in rawEXIF is written to CMT4, never
// behind a GPS pointer in CMT1; an unchanged GPS IFD keeps the source CMT4
// byte for byte; a changed one replaces it; a missing CMT4 is created after
// the last CMT box.
func TestTask300GPSGoesToCMT4(t *testing.T) {
	t.Parallel()
	cmt4 := gpsCMT4(t)
	gpsIFD, _, err := ParseSubIFD(cmt4)
	if err != nil {
		t.Fatal(err)
	}
	ifd0 := &exif.IFD{Entries: []exif.IFDEntry{{Tag: 0x0110, Type: exif.TypeASCII, Count: 6, Value: []byte("Model\x00")}}}
	encode := func(gps *exif.IFD) []byte {
		b, err := exif.Encode(&exif.EXIF{ByteOrder: binary.LittleEndian, IFD0: ifd0, GPSIFD: gps})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	changedGPS := &exif.IFD{Entries: []exif.IFDEntry{{Tag: 0x0000, Type: exif.TypeByte, Count: 4, Value: []byte{2, 2, 0, 0}}}}

	for _, tc := range []struct {
		name      string
		srcCMT4   []byte
		rawEXIF   []byte
		wantSame  bool   // CMT4 kept byte for byte
		wantGPSVr []byte // GPSVersionID in the output CMT4
	}{
		{"unchanged GPS keeps CMT4", cmt4, encode(gpsIFD), true, []byte{2, 3, 0, 0}},
		{"changed GPS replaces CMT4", cmt4, encode(changedGPS), false, []byte{2, 2, 0, 0}},
		{"no GPS keeps CMT4", cmt4, encode(nil), true, []byte{2, 3, 0, 0}},
		{"GPS without CMT4 creates it", nil, encode(changedGPS), false, []byte{2, 2, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmt3 := tiffWithIFD0(t, exif.IFDEntry{Tag: 0x0006, Type: exif.TypeASCII, Count: 4, Value: []byte("JPG\x00")})
			src := buildTask300CR3(cr3Fixture{cmt1: minimalTIFF(), cmt3: cmt3, cmt4: tc.srcCMT4, mdat: []byte("RAW")})
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, tc.rawEXIF, nil, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			seg, err := ExtractSegments(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("ExtractSegments: %v", err)
			}
			e, err := exif.Parse(seg.EXIF)
			if err != nil {
				t.Fatalf("parse CMT1: %v", err)
			}
			if e.GPSIFD != nil || e.IFD0.Get(exif.TagGPSIFDPointer) != nil {
				t.Error("CMT1 carries a GPS IFD; it belongs in CMT4")
			}
			if e.CameraModel() != "Model" {
				t.Errorf("CMT1 Model = %q", e.CameraModel())
			}
			if tc.wantSame && !bytes.Equal(seg.GPS, tc.srcCMT4) {
				t.Error("unchanged CMT4 was rewritten")
			}
			gps, _, err := ParseSubIFD(seg.GPS)
			if err != nil || gps.Get(0) == nil || !bytes.Equal(gps.Get(0).Value, tc.wantGPSVr) {
				t.Errorf("CMT4 GPSVersionID = %v (%v), want %v", gps, err, tc.wantGPSVr)
			}
			if !bytes.Equal(seg.MakerNote, cmt3) {
				t.Error("CMT3 changed")
			}
			// CMT4 sits right after CMT3, before THMB.
			canon := findUUIDBox(flatBox(out.Bytes(), "moov"), canonUUID)
			if i3, i4, iT := bytes.Index(canon, []byte("CMT3")), bytes.Index(canon, []byte("CMT4")), bytes.Index(canon, []byte("THMB")); i4 < i3 || i4 > iT {
				t.Errorf("sub-box order CMT3@%d CMT4@%d THMB@%d, want CMT3 < CMT4 < THMB", i3, i4, iT)
			}
		})
	}
}
