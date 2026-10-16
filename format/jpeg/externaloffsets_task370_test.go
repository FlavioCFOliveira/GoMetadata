package jpeg

// externaloffsets_task370_test.go — task #370, rule R-31/R-33 (docs/conformance/
// exif-tiff.md): an Olympus preview offset whose data is inside the MakerNote
// (E-1), in a segment before the first scan header, or nowhere that can be
// placed.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// e1IFDValue returns the entries of the CameraSettings of an E-1: an IFD stored
// as the value of tag 0x2020, with PreviewImageStart and PreviewImageLength
// inline.
func e1IFDValue(start, length uint32) []byte {
	le := binary.LittleEndian
	v := le.AppendUint16(nil, 3)
	for _, e := range [...]struct {
		tag uint16
		val uint32
	}{{0x0100, 1}, {0x0101, start}, {0x0102, length}} {
		v = le.AppendUint16(v, e.tag)
		v = le.AppendUint16(v, 4)
		v = le.AppendUint32(v, 1)
		v = le.AppendUint32(v, e.val)
	}
	return le.AppendUint32(v, 0)
}

// olympE1EXIF builds the EXIF stream of an "OLYMP\0" body (offsets counted from
// the TIFF header) whose CameraSettings is a value (the E-1 form) with the given
// start and length, and whose MakerNote holds data as a value of its own. pad
// moves the MakerNote. It returns the stream and the position in it of data.
func olympE1EXIF(start, length uint32, pad int, data []byte) (stream []byte, dataAt uint32) {
	tiff, lay := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: "OLYMPUS OPTICAL CO.,LTD", Order: binary.LittleEndian, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, PadBefore: pad,
		Entries: []testutil.MNEntry{
			{Tag: 0x0300, Type: 7, Count: uint32(len(data)), Value: data}, //nolint:gosec // G115: tiny test sizes
			{Tag: 0x2020, Type: 7, Count: 42, Value: e1IFDValue(start, length)},
		},
	})
	return tiff, uint32(lay.Values[0]) //nolint:gosec // G115: tiny test offsets
}

// plainJPEG wraps an EXIF stream in SOI, APP1, the segments after, and a scan.
func plainJPEG(exifStream []byte, after ...[]byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	b.Write(binary.BigEndian.AppendUint16(nil, uint16(2+6+len(exifStream)))) //nolint:gosec // G115: small test sizes
	b.WriteString("Exif\x00\x00")
	b.Write(exifStream)
	for _, s := range after {
		b.Write(s)
	}
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x01, 0x02, 0xFF, 0x00, 0x03, 0xFF, 0xD9})
	return b.Bytes()
}

// appSegment returns an APPn segment (marker 0xFF, 0xE0+n) with the payload.
func appSegment(n byte, payload []byte) []byte {
	b := []byte{0xFF, 0xE0 + n}
	b = binary.BigEndian.AppendUint16(b, uint16(2+len(payload))) //nolint:gosec // G115: small test sizes
	return append(b, payload...)
}

// dataAtOffset returns the bytes that the first external offset of the EXIF of
// file addresses, counted as a reader of the file does, or nil.
func dataAtOffset(t *testing.T, file []byte) (data []byte, value uint32) {
	t.Helper()
	raw, _, _, err := Extract(bytes.NewReader(file))
	if err != nil || raw == nil {
		t.Fatalf("Extract: %v", err)
	}
	offs := exif.ExternalOffsets(raw)
	if len(offs) != 1 {
		t.Fatalf("ExternalOffsets = %+v, want one offset", offs)
	}
	o := offs[0]
	i := bytes.Index(file, []byte("Exif\x00\x00"))
	at := int64(i) + 6 + o.Target()
	if at < 0 || at+int64(o.Length) > int64(len(file)) {
		return nil, o.Value
	}
	return file[at : at+int64(o.Length)], o.Value
}

// TestExternalOffsets_R31_E1PreviewInsideTheMakerNote (task #370): the E-1
// stores the preview inside the MakerNote, counted from the TIFF header; a
// write that moves the MakerNote moves the offset with it. R-31.
func TestExternalOffsets_R31_E1PreviewInsideTheMakerNote(t *testing.T) {
	t.Parallel()
	data := []byte("<Dummy preview image data>")
	n := uint32(len(data)) //nolint:gosec // G115: tiny test sizes
	_, pos := olympE1EXIF(0, n, 6, data)
	srcStream, _ := olympE1EXIF(pos, n, 6, data)
	src := plainJPEG(srcStream)
	if got, _ := dataAtOffset(t, src); !bytes.Equal(got, data) {
		t.Fatalf("fixture: the source offset addresses %q", got)
	}
	for _, pad := range []int{0, 40, 200} {
		t.Run(fmt.Sprintf("R-31/MakerNote moved by pad %d", pad), func(t *testing.T) {
			t.Parallel()
			// What Encode writes: the MakerNote at its new place, the offset as stored.
			newStream, newPos := olympE1EXIF(pos, n, pad, data)
			if pad != 6 && newPos == pos {
				t.Fatal("fixture: the data did not move")
			}
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, newStream, nil, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			got, _ := dataAtOffset(t, out.Bytes())
			if !bytes.Equal(got, data) {
				t.Errorf("after the write the offset addresses %q, want the data in the MakerNote", got)
			}
		})
	}
}

// TestExternalOffsets_R31_SegmentBeforeTheScan: the data is in an APPn segment
// before the first scan header that the write copies; it moves when the new
// EXIF is longer, or the IPTC segment is added. The data is not a JPEG stream.
func TestExternalOffsets_R31_SegmentBeforeTheScan(t *testing.T) {
	t.Parallel()
	data := []byte("not a JPEG stream: any bytes of an APP segment")
	n := uint32(len(data)) //nolint:gosec // G115: tiny test sizes
	seg := appSegment(4, append([]byte("OLYMPUS-PREVIEW\x00"), data...))
	mnFile := int64(12) + mnOffsetOf(t, olympusEXIF(0, n, 6))
	build := func(start uint32) []byte { return plainJPEG(olympusEXIF(start, n, 6), seg) }
	at := int64(bytes.Index(build(0), data))
	src := build(uint32(at - mnFile)) //nolint:gosec // G115: positive test offset
	if got, _ := dataAtOffset(t, src); !bytes.Equal(got, data) {
		t.Fatalf("fixture: the source offset addresses %q", got)
	}
	_, value := dataAtOffset(t, src)
	for _, tc := range []struct {
		name string
		pad  int
		iptc []byte
	}{
		{"EXIF grows", 40, nil},
		{"EXIF shrinks", 0, nil},
		{"IPTC added", 6, newIIM},
		{"both", 100, newIIM},
	} {
		t.Run("R-31/"+tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, olympusEXIF(value, n, tc.pad), tc.iptc, nil, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if got, _ := dataAtOffset(t, out.Bytes()); !bytes.Equal(got, data) {
				t.Errorf("after the write the offset addresses %q, want the data of the segment", got)
			}
		})
	}
}

// TestExternalOffsets_R33_LeftAsItIs: an offset whose data cannot be placed is
// left as it is, and the write succeeds: the data is in a segment that the write
// replaces, in the EXIF stream outside the MakerNote, in a trailer the write
// rewrites, or past the end of the file. R-33.
func TestExternalOffsets_R33_LeftAsItIs(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	data := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte("data-"), 8)...)
	n := uint32(len(data)) //nolint:gosec // G115: tiny test sizes
	mnFile := int64(12) + mnOffsetOf(t, olympusEXIF(0, n, 6))
	xmp := appSegment(1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), append([]byte("<x:xmpmeta>"), data...)...))
	afcp := func(at int64) []byte {
		return afcpOf(binary.BigEndian, "AXS!", at, [][2]string{{"IPTC", string(oldIIM)}, {"PREV", string(data)}})
	}

	type fixture struct {
		name string
		file func(start uint32) []byte
		at   func(file []byte) int64 // position of the data in the fixture file
		iptc []byte
		xmp  []byte
	}
	for _, tc := range []fixture{
		{"segment the write replaces", func(s uint32) []byte { return plainJPEG(olympusEXIF(s, n, 6), xmp) },
			func(f []byte) int64 { return int64(bytes.Index(f, data)) }, nil, []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"></x:xmpmeta>")},
		{"trailer the write rewrites", func(s uint32) []byte {
			f := plainJPEG(olympusEXIF(s, n, 6))
			return append(f, afcp(int64(len(f)))...)
		}, func(f []byte) int64 { return int64(bytes.LastIndex(f, data)) }, newIIM, nil},
	} {
		t.Run("R-33/"+tc.name, func(t *testing.T) {
			t.Parallel()
			at := tc.at(tc.file(0))
			src := tc.file(uint32(at - mnFile)) //nolint:gosec // G115: positive test offset
			_, value := dataAtOffset(t, src)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, olympusEXIF(value, n, 40), tc.iptc, tc.xmp, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if _, got := dataAtOffset(t, out.Bytes()); got != value {
				t.Errorf("offset = %d, want it left at %d", got, value)
			}
		})
	}

	t.Run("R-33/EXIF stream outside the MakerNote", func(t *testing.T) {
		t.Parallel()
		tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
			Make: "OLYMPUS IMAGING CORP.", Order: le, Prefix: []byte("OLYMPUS\x00II\x03\x00"), PadBefore: 6,
			Entries: []testutil.MNEntry{{Tag: 0x2020, Sub: &testutil.MNSub{Entries: []testutil.MNEntry{
				{Tag: 0x0101, Type: 4, Count: 1, PtrOutside: data},
				{Tag: 0x0102, Type: 4, Count: 1, Value: le.AppendUint32(nil, n)},
			}}}},
		})
		src := plainJPEG(tiff)
		_, value := dataAtOffset(t, src)
		moved, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
			Make: "OLYMPUS IMAGING CORP.", Order: le, Prefix: []byte("OLYMPUS\x00II\x03\x00"), PadBefore: 40,
			Entries: []testutil.MNEntry{{Tag: 0x2020, Sub: &testutil.MNSub{Entries: []testutil.MNEntry{
				{Tag: 0x0101, Type: 4, Count: 1, Value: le.AppendUint32(nil, value)},
				{Tag: 0x0102, Type: 4, Count: 1, Value: le.AppendUint32(nil, n)},
			}}}},
		})
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, moved, nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if _, got := dataAtOffset(t, out.Bytes()); got != value {
			t.Errorf("offset = %d, want it left at %d", got, value)
		}
	})

	t.Run("R-33/past the end of the file", func(t *testing.T) {
		t.Parallel()
		src := plainJPEG(olympusEXIF(1<<24, n, 6))
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, olympusEXIF(1<<24, n, 40), nil, nil, true); err != nil {
			t.Fatalf("Inject: %v", err)
		}
		if _, got := dataAtOffset(t, out.Bytes()); got != 1<<24 {
			t.Errorf("offset = %d, want it left", got)
		}
	})
}

// TestExternalOffsets_R31_CorpusE1: the E-1 of the corpus (CameraSettings as a
// value, 26 bytes of preview inside the MakerNote, counted from the TIFF header)
// keeps its preview addressed after a write that moves the MakerNote.
func TestExternalOffsets_R31_CorpusE1(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "testdata", "corpus", "jpeg", "exiftool", "OlympusE1.jpg")
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	want, _ := dataAtOffset(t, src)
	if string(want) != "<Dummy preview image data>" {
		t.Fatalf("fixture: the source offset addresses %q", want)
	}
	raw, _, _, err := Extract(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	// An EXIF stream with 100 bytes more before the MakerNote: the IFD0 Make
	// value is longer. Rebuilt by the library.
	e, err := exif.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	mk := e.IFD0.Get(exif.TagMake)
	mk.Value = append(bytes.TrimRight(mk.Value, "\x00"), append(bytes.Repeat([]byte("x"), 100), 0)...)
	mk.Count = uint32(len(mk.Value)) //nolint:gosec // G115: tiny test sizes
	stream, err := exif.EncodeRebased(e, raw)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, stream, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	got, _ := dataAtOffset(t, out.Bytes())
	if !bytes.Equal(got, want) {
		t.Errorf("after the write the offset addresses %q, want %q", got, want)
	}
}
