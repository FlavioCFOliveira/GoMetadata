package exif

import (
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// canonLikeEXIF returns the EXIF and the source stream of a synthetic Canon
// MakerNote with out-of-line values, for the benchmarks of EncodeRebased.
func canonLikeEXIF(b *testing.B) (*EXIF, []byte) {
	b.Helper()
	spec := testutil.MNSpec{Make: "Canon", Order: binary.LittleEndian, TIFFRelative: true, Entries: mnEntries(), Footer: true, PadBefore: 6}
	tiff, _ := testutil.MakerNoteTIFF(spec)
	e, err := Parse(tiff)
	if err != nil {
		b.Fatal(err)
	}
	return e, tiff
}

// BenchmarkEncodeRebased_Canon: the repair of a TIFF-relative MakerNote on top
// of Encode (tasks #317, #318). Compare BenchmarkEncodeRebased_CanonPlain.
func BenchmarkEncodeRebased_Canon(b *testing.B) {
	e, tiff := canonLikeEXIF(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodeRebased(e, tiff); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeRebased_CanonPlain: Encode of the same EXIF, the baseline.
func BenchmarkEncodeRebased_CanonPlain(b *testing.B) {
	e, _ := canonLikeEXIF(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Encode(e); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeRebased_NoMakerNote: the cost of the repair for an EXIF
// without MakerNote, which must be a nil check.
func BenchmarkEncodeRebased_NoMakerNote(b *testing.B) {
	e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}}
	e.SetCaption("benchmark")
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodeRebased(e, nil); err != nil {
			b.Fatal(err)
		}
	}
}
