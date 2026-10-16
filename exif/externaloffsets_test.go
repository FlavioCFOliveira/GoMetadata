package exif

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// olympusStream builds the EXIF stream of an Olympus body: MakerNote prefix
// "OLYMPUS\0II\3\0" and a CameraSettings sub-IFD (0x2020) with the given
// entries.
func olympusStream(maker string, prefix string, subEntries []testutil.MNEntry) []byte {
	tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: maker, Order: binary.LittleEndian, Prefix: []byte(prefix), PadBefore: 6,
		Entries: []testutil.MNEntry{
			{Tag: 0x0200, Type: 4, Count: 1, Value: []byte{7, 0, 0, 0}},
			{Tag: 0x2020, Sub: &testutil.MNSub{Entries: subEntries}},
		},
	})
	return tiff
}

func long(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// TestExternalOffsets_X1 (task #362): the PreviewImageStart of the Olympus
// CameraSettings is found with its length, the position of its field and the
// position of the MakerNote it counts from; whatever else is not reported.
func TestExternalOffsets_X1(t *testing.T) {
	t.Parallel()
	good := []testutil.MNEntry{
		{Tag: 0x0100, Type: 4, Count: 1, Value: long(1)},
		{Tag: 0x0101, Type: 4, Count: 1, Value: long(5000)},
		{Tag: 0x0102, Type: 4, Count: 1, Value: long(321)},
	}
	for _, tc := range []struct {
		name   string
		stream []byte
		want   int
	}{
		{"Olympus", olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", good), 1},
		{"OM Digital", olympusStream("OM Digital Solutions", "OM SYSTEM\x00\x00\x00II", good), 1},
		{"length 0", olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", []testutil.MNEntry{
			{Tag: 0x0101, Type: 4, Count: 1, Value: long(5000)}, {Tag: 0x0102, Type: 4, Count: 1, Value: long(0)}}), 0},
		{"no length entry", olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", []testutil.MNEntry{
			{Tag: 0x0101, Type: 4, Count: 1, Value: long(5000)}}), 0},
		{"start is a SHORT", olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", []testutil.MNEntry{
			{Tag: 0x0101, Type: 3, Count: 1, Value: []byte{1, 0}}, {Tag: 0x0102, Type: 4, Count: 1, Value: long(9)}}), 0},
		{"no CameraSettings", olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", nil), 0},
	} {
		t.Run("R-27/"+tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExternalOffsets(tc.stream)
			if len(got) != tc.want {
				t.Fatalf("ExternalOffsets = %v, want %d offsets", got, tc.want)
			}
			if tc.want == 0 {
				return
			}
			o := got[0]
			if o.Value != 5000 || o.Length != 321 || o.Order != binary.LittleEndian {
				t.Errorf("offset = %+v, want value 5000, length 321, little-endian", o)
			}
			e, err := Parse(tc.stream)
			if err != nil {
				t.Fatal(err)
			}
			if o.Base != int(e.MakerNoteOffset) {
				t.Errorf("Base = %d, want the MakerNote offset %d", o.Base, e.MakerNoteOffset)
			}
			if binary.LittleEndian.Uint32(tc.stream[o.Field:]) != 5000 {
				t.Errorf("Field %d does not hold the value", o.Field)
			}
		})
	}
}

// TestExternalOffsets_X2: streams of other makers and layouts hold none.
func TestExternalOffsets_X2(t *testing.T) {
	t.Parallel()
	good := []testutil.MNEntry{
		{Tag: 0x0101, Type: 4, Count: 1, Value: long(5000)},
		{Tag: 0x0102, Type: 4, Count: 1, Value: long(321)},
	}
	for _, tc := range []struct {
		name   string
		stream []byte
	}{
		{"nil", nil},
		{"not TIFF", []byte("OLYMPUS and some text that is no TIFF stream at all")},
		{"Canon MakerNote", olympusStream("Canon", "", good)},
		{"Olympus OLYMP layout counts from the TIFF header", func() []byte {
			tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
				Make: "OLYMPUS OPTICAL CO.,LTD", Order: binary.LittleEndian, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, PadBefore: 6,
				Entries: []testutil.MNEntry{{Tag: 0x2020, Sub: &testutil.MNSub{Entries: good}}}})
			return tiff
		}()},
	} {
		t.Run("R-27/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ExternalOffsets(tc.stream); got != nil {
				t.Errorf("ExternalOffsets = %v, want nil", got)
			}
		})
	}
}

// TestExternalOffsets_X3_Corpus: the corpus bodies whose preview lies after the
// image (E-M1, E-M5, TG-850, SH-1, SP-100EE), and a body that has none.
func TestExternalOffsets_X3_Corpus(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "testdata", "corpus", "jpeg", "metadata-extractor")
	for _, tc := range []struct {
		name  string
		start uint32
	}{
		{"Olympus E-M1.jpg", 10976976},
		{"Olympus E-M5.jpg", 5954756},
		{"Olympus TG-850.jpg", 6978477},
		{"Olympus SH-1.jpg", 6892026},
		{"Olympus SP-100EE.jpg", 6876652},
		{"Olympus E-10.jpg", 0},
	} {
		t.Run("R-27/"+tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := os.ReadFile(filepath.Join(dir, tc.name))
			if os.IsNotExist(err) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", tc.name)
			}
			if err != nil {
				t.Fatal(err)
			}
			// The EXIF stream follows the first "Exif\0\0".
			i := bytes.Index(b, []byte("Exif\x00\x00"))
			if i < 0 {
				t.Fatal("no EXIF segment")
			}
			got := ExternalOffsets(b[i+6:])
			if tc.start == 0 {
				if got != nil {
					t.Errorf("ExternalOffsets = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Value != tc.start {
				t.Errorf("ExternalOffsets = %v, want PreviewImageStart %d", got, tc.start)
			}
		})
	}
}

// BenchmarkExternalOffsets measures the gate every JPEG write passes: a stream
// without an Olympus MakerNote (one search, no parse), and the Olympus stream
// that is parsed and reported.
func BenchmarkExternalOffsets(b *testing.B) {
	other := olympusStream("Canon", "", nil)
	olympus := olympusStream("OLYMPUS IMAGING CORP.", "OLYMPUS\x00II\x03\x00", []testutil.MNEntry{
		{Tag: 0x0101, Type: 4, Count: 1, Value: long(5000)}, {Tag: 0x0102, Type: 4, Count: 1, Value: long(321)}})
	for _, bc := range []struct {
		name   string
		stream []byte
	}{{"NotOlympus", other}, {"Olympus", olympus}} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = ExternalOffsets(bc.stream)
			}
		})
	}
}

// BenchmarkAppendPrivateIFD measures EncodeRebased of an EXIF with a Kodak
// private IFD against the same EXIF without one.
func BenchmarkAppendPrivateIFD(b *testing.B) {
	src, _ := kodakStream(noInterop, 2)
	e, err := Parse(src)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for range b.N {
		if _, err := EncodeRebased(e, src); err != nil {
			b.Fatal(err)
		}
	}
}
