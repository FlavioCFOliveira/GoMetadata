package exif

import (
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ratBytes encodes rationals (numerator, denominator pairs) little-endian.
func ratBytes(v ...uint32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, x)
	}
	return b
}

// nikonLensEntry is a Nikon Lens tag (0x0084) of four rationals.
func nikonLensEntry(v ...uint32) testutil.MNEntry {
	return testutil.MNEntry{Tag: 0x0084, Type: 5, Count: 4, Value: ratBytes(v...)}
}

// TestNikonLensSpec (task #396): the description follows
// TagDescriptor.getLensSpecificationDescription of metadata-extractor, with the
// ExifTool form of an f-number without a decimal.
func TestNikonLensSpec(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		v    []uint32 // min focal, max focal, min f, max f as numerator/denominator pairs
		want string
	}{
		{"zoom", []uint32{18, 1, 55, 1, 35, 10, 56, 10}, "18-55mm f/3.5-5.6"},
		{"prime", []uint32{50, 1, 50, 1, 18, 10, 18, 10}, "50mm f/1.8"},
		{"constant aperture zoom", []uint32{24, 1, 70, 1, 4, 1, 4, 1}, "24-70mm f/4"},
		{"super zoom", []uint32{150, 1, 600, 1, 5, 1, 63, 10}, "150-600mm f/5-6.3"},
		{"one decimal half up", []uint32{35, 1, 35, 1, 25, 10, 25, 10}, "35mm f/2.5"},
		{"half up", []uint32{35, 1, 35, 1, 225, 100, 225, 100}, "35mm f/2.3"},
		{"non reduced numbers", []uint32{180, 10, 550, 10, 350, 100, 560, 100}, "18-55mm f/3.5-5.6"},
		{"decimal focal length", []uint32{175, 10, 175, 10, 28, 10, 28, 10}, "17.5mm f/2.8"},
		{"fraction when the decimal is long", []uint32{1005, 10, 1005, 10, 28, 10, 28, 10}, "201/2mm f/2.8"},
		{"no aperture", []uint32{50, 1, 50, 1, 0, 1, 0, 1}, "50mm"},
		{"unknown lens", []uint32{0, 1, 0, 1, 0, 1, 0, 1}, ""},
		{"zero denominator", []uint32{18, 0, 55, 1, 35, 10, 56, 10}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ent := IFDEntry{Tag: 0x0084, Type: TypeRational, Count: 4, Value: ratBytes(tc.v...)}
			if got := nikonLensSpec(&ent); got != tc.want {
				t.Errorf("nikonLensSpec = %q, want %q", got, tc.want)
			}
		})
	}
	// Entries that are not a lens specification.
	if got := nikonLensSpec(nil); got != "" {
		t.Errorf("nil entry = %q", got)
	}
	short := IFDEntry{Tag: 0x0084, Type: TypeRational, Count: 3, Value: ratBytes(18, 1, 55, 1, 35, 10)}
	if got := nikonLensSpec(&short); got != "" {
		t.Errorf("three rationals = %q", got)
	}
	wrong := IFDEntry{Tag: 0x0084, Type: TypeShort, Count: 4, Value: ratBytes(18, 1, 55, 1, 35, 10, 56, 10)}
	if got := nikonLensSpec(&wrong); got != "" {
		t.Errorf("wrong type = %q", got)
	}
	truncated := IFDEntry{Tag: 0x0084, Type: TypeRational, Count: 4, Value: ratBytes(18, 1, 55, 1)}
	if got := nikonLensSpec(&truncated); got != "" {
		t.Errorf("truncated value = %q", got)
	}
}

// TestMakerNoteLens (task #396): Canon LensModel and Nikon Lens are read from
// the MakerNote IFD of a parsed file.
func TestMakerNoteLens(t *testing.T) {
	t.Parallel()
	canonModel := append([]byte("EF-S18-55mm f/3.5-5.6 IS II"), 0, ' ', ' ') // NUL and blanks pad the field
	for _, tc := range []struct {
		name, mk string
		entries  []testutil.MNEntry
		want     string
	}{
		{"canon", "Canon", []testutil.MNEntry{
			{Tag: 0x0006, Type: 2, Count: 4, Value: []byte("EOS\x00")},
			{Tag: 0x0095, Type: 2, Count: uint32(len(canonModel)), Value: canonModel}, //nolint:gosec // G115: a 30-byte test value
		}, "EF-S18-55mm f/3.5-5.6 IS II"},
		{"canon without the tag", "Canon", []testutil.MNEntry{
			{Tag: 0x0006, Type: 2, Count: 4, Value: []byte("EOS\x00")},
		}, ""},
		{"canon empty value", "Canon", []testutil.MNEntry{
			{Tag: 0x0095, Type: 2, Count: 8, Value: make([]byte, 8)},
		}, ""},
		{"nikon", "NIKON CORPORATION", []testutil.MNEntry{nikonLensEntry(28, 1, 300, 1, 35, 10, 56, 10)}, "28-300mm f/3.5-5.6"},
		{"nikon without the tag", "NIKON CORPORATION", []testutil.MNEntry{
			{Tag: 0x0002, Type: 3, Count: 1, Value: []byte{7, 0}},
		}, ""},
		{"sony has no lens description", "SONY", []testutil.MNEntry{
			{Tag: 0xB027, Type: 4, Count: 1, Value: []byte{1, 0, 0, 0}},
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := parsedMakerNote(t, tc.mk, "", true, tc.entries)
			if got := e.MakerNoteLens(); got != tc.want {
				t.Errorf("MakerNoteLens = %q, want %q", got, tc.want)
			}
			if got := e.LensModel(); got != "" {
				t.Errorf("EXIF LensModel = %q: the MakerNote must not leak into the Exif tag accessor", got)
			}
		})
	}
	var nilEXIF *EXIF
	if got := nilEXIF.MakerNoteLens(); got != "" {
		t.Errorf("nil EXIF = %q", got)
	}
	if got := (&EXIF{}).MakerNoteLens(); got != "" {
		t.Errorf("EXIF without MakerNote = %q", got)
	}
}

// TestMakerNoteLens_SkipMakerNote: without the parsed MakerNote IFD there is
// no fallback.
func TestMakerNoteLens_SkipMakerNote(t *testing.T) {
	t.Parallel()
	tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: "NIKON CORPORATION", Order: binary.LittleEndian, TIFFRelative: true,
		Entries: []testutil.MNEntry{nikonLensEntry(50, 1, 50, 1, 18, 10, 18, 10)},
	})
	e, err := Parse(tiff, SkipMakerNote())
	if err != nil {
		t.Fatal(err)
	}
	if got := e.MakerNoteLens(); got != "" {
		t.Errorf("MakerNoteLens = %q with SkipMakerNote", got)
	}
}

// BenchmarkNikonLensSpec measures the formatting of a Nikon lens specification.
func BenchmarkNikonLensSpec(b *testing.B) {
	ent := IFDEntry{Tag: 0x0084, Type: TypeRational, Count: 4, Value: ratBytes(28, 1, 300, 1, 35, 10, 56, 10)}
	b.ReportAllocs()
	for range b.N {
		if nikonLensSpec(&ent) == "" {
			b.Fatal("empty")
		}
	}
}
