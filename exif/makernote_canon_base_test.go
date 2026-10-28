package exif

// makernote_canon_base_test.go — task #407: the offset base of a Canon
// MakerNote that was moved without updating its pointers is corrected on read
// (makernote_canon_base.go).

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// canonSpec is a synthetic Canon MakerNote with a footer; shift models a note
// moved without updating its pointers (ExifTool: "Adjusted MakerNotes base by").
func canonSpec(order binary.ByteOrder, shift int) testutil.MNSpec {
	return testutil.MNSpec{Make: "Canon", Order: order, TIFFRelative: true, Entries: mnEntries(), Footer: true, FooterAdd: shift, PadBefore: 6}
}

// TestCanonBase_R407_ShiftedNoteReadsItsValues: the out-of-line values of a
// Canon note whose pointers are off by a constant are read from where they are,
// in both byte orders and for shifts of either sign.
func TestCanonBase_R407_ShiftedNoteReadsItsValues(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, shift := range []int{0, 164, -4, 2, 1000} {
			t.Run(order.String()+"/shift"+itoa(shift), func(t *testing.T) {
				t.Parallel()
				tiff, _ := testutil.MakerNoteTIFF(canonSpec(order, shift))
				e, err := Parse(tiff)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if e.MakerNoteIFD == nil {
					t.Fatal("no MakerNote IFD")
				}
				if got := e.MakerNoteIFD.Get(0x0002); got == nil || string(got.Value) != mnSerialStr {
					t.Errorf("tag 0x0002 = %v, want %q", got, mnSerialStr)
				}
				if got := e.MakerNoteIFD.Get(0x0003); got == nil || string(got.Value) != mnRatStr {
					t.Errorf("tag 0x0003 = %v, want %x", got, mnRatStr)
				}
				if got := e.MakerNoteIFD.Get(0x0001); got == nil || got.Count != 1 {
					t.Errorf("tag 0x0001 = %v", got)
				}
			})
		}
	}
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// TestCanonBase_R407_BytesUntouched: the correction reads a copy; the
// MakerNote bytes of the EXIF (what a write carries) are the source's.
func TestCanonBase_R407_BytesUntouched(t *testing.T) {
	t.Parallel()
	tiff, lay := testutil.MakerNoteTIFF(canonSpec(binary.LittleEndian, 164))
	want := bytes.Clone(tiff)
	e, err := Parse(tiff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !bytes.Equal(tiff, want) {
		t.Error("Parse modified its input")
	}
	if !bytes.Equal(e.MakerNote, want[lay.MNOffset:lay.MNOffset+lay.MNLen]) {
		t.Error("EXIF.MakerNote differs from the source bytes")
	}
}

// TestCanonBase_R407_NoCorrection: notes whose base is right, or that do not
// qualify, are not rebased.
func TestCanonBase_R407_NoCorrection(t *testing.T) {
	t.Parallel()
	le := binary.LittleEndian
	good, gl := testutil.MakerNoteTIFF(canonSpec(le, 0))
	noFooter, nl := testutil.MakerNoteTIFF(testutil.MNSpec{Make: "Canon", Order: le, TIFFRelative: true, Entries: mnEntries(), PadBefore: 6})
	shifted, sl := testutil.MakerNoteTIFF(canonSpec(le, 164))

	// A footer that disagrees while the pointers are right (Picasa and ACDSee
	// updated the pointers and not the footer): the values lie where the
	// pointers say, so the base is kept.
	picasa, pl := testutil.MakerNoteTIFF(canonSpec(le, 0))
	le.PutUint32(picasa[pl.MNOffset+pl.MNLen-4:], uint32(pl.MNOffset+164)) //nolint:gosec // G115: test sizes are small

	tests := []struct {
		name string
		mn   []byte
		off  uint64
	}{
		{"footer holds the position", good[gl.MNOffset : gl.MNOffset+gl.MNLen], uint64(gl.MNOffset)},          //nolint:gosec // G115: test sizes are small
		{"no footer", noFooter[nl.MNOffset : nl.MNOffset+nl.MNLen], uint64(nl.MNOffset)},                      //nolint:gosec // G115: test sizes are small
		{"footer disagrees, pointers right", picasa[pl.MNOffset : pl.MNOffset+pl.MNLen], uint64(pl.MNOffset)}, //nolint:gosec // G115: test sizes are small
		{"unknown position", shifted[sl.MNOffset : sl.MNOffset+sl.MNLen], 0},
		{"empty", nil, 10},
		{"too short", []byte{1, 0, 0, 0}, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if c, ok := canonRebasedCopy(tc.mn, le, tc.off); ok {
				t.Errorf("canonRebasedCopy = %x, true; want no correction", c)
			}
		})
	}
	// The shifted note is corrected at its true position.
	if _, ok := canonRebasedCopy(shifted[sl.MNOffset:sl.MNOffset+sl.MNLen], le, uint64(sl.MNOffset)); !ok { //nolint:gosec // G115: test sizes are small
		t.Error("the shifted note was not corrected")
	}
}

// shiftedCanonCorpus names the corpus files whose Canon MakerNote has a shifted
// base, relative to testdata/corpus: the files for which ExifTool 13.55
// ("exiftool -v3") reports "Adjusted MakerNotes base by" (-54, 62, -128, -164,
// -256), captured once over every Canon file of the corpus; no test runs it.
var shiftedCanonCorpus = map[string]bool{ //nolint:gochecknoglobals // test table
	"jpeg/exif-samples/11-tests.jpg":                    true,
	"jpeg/exif-samples/11-tests_2.jpg":                  true,
	"jpeg/exif-samples/11-tests_3.jpg":                  true,
	"jpeg/exif-samples/28-hex_value.jpg":                true,
	"jpeg/exif-samples/28-hex_value_2.jpg":              true,
	"jpeg/exif-samples/28-hex_value_3.jpg":              true,
	"jpeg/exif-samples/canon_hdr_NO.jpg":                true,
	"jpeg/exif-samples/canon_hdr_NO_2.jpg":              true,
	"jpeg/exif-samples/canon_hdr_NO_3.jpg":              true,
	"jpeg/exif-samples/canon_hdr_YES.jpg":               true,
	"jpeg/exif-samples/canon_hdr_YES_2.jpg":             true,
	"jpeg/exif-samples/canon_hdr_YES_3.jpg":             true,
	"jpeg/metadata-extractor/Canon EOS REBEL T2i.jpg":   true,
	"jpeg/metadata-extractor/Canon EOS REBEL T2i_2.jpg": true,
	"jpeg/metadata-extractor/Canon EOS REBEL T3i.jpg":   true,
	"jpeg/metadata-extractor/Canon EOS REBEL T3i_2.jpg": true,
	"jpeg/metadata-extractor/Canon PowerShot A85.jpg":   true,
	"jpeg/metadata-extractor/Canon PowerShot A85_2.jpg": true,
}

// TestCanonBase_R407_Corpus: over every corpus file with a Canon MakerNote, the
// correction applies to exactly the 18 shifted files, and the IFD read of every
// other file equals the IFD read without the correction (they read exactly as
// before the task).
func TestCanonBase_R407_Corpus(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, ".jpg", ".jpeg", ".tif", ".tiff", ".cr2", ".dng")
	if len(files) == 0 {
		t.Skip("corpus absent (see testdata/download.sh)")
	}
	canon, corrected := 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) > 32<<20 {
			continue
		}
		off := 0
		if i := bytes.Index(data[:min(len(data), 1<<16)], []byte("Exif\x00\x00")); i >= 0 {
			off = i + 6
		}
		tiff := data[off:]
		e, err := Parse(tiff)
		if err != nil || e == nil || e.IFD0 == nil || len(e.MakerNote) == 0 || e.MakerNoteOffset == 0 {
			continue
		}
		me := e.IFD0.Get(TagMake)
		if me == nil || !eqFold(trimMake(me.Value), "CANON") || detectLayout(e.MakerNote, trimMake(me.Value)) != mnCanon {
			continue
		}
		canon++
		_, ok := canonRebasedCopy(e.MakerNote, e.ByteOrder, uint64(e.MakerNoteOffset))
		rel, _ := filepath.Rel(filepath.Join("..", "testdata", "corpus"), f)
		if want := shiftedCanonCorpus[filepath.ToSlash(rel)]; ok != want {
			t.Errorf("%s: correction = %v, want %v", f, ok, want)
		}
		if ok {
			corrected++
			continue
		}
		legacy := mnLayouts[mnCanon].parse(makerNoteView(mnBaseTIFF, e.MakerNote, tiff, uint64(e.MakerNoteOffset)), e.ByteOrder)
		if !sameIFD(legacy, e.MakerNoteIFD) {
			t.Errorf("%s: the IFD read differs from the one read without the correction", f)
		}
	}
	if canon == 0 {
		t.Fatal("no corpus file with a Canon MakerNote")
	}
	t.Logf("%d Canon MakerNotes, %d corrected", canon, corrected)
}

// sameIFD reports whether two IFDs hold the same entries (tag, type, count and
// value bytes).
func sameIFD(a, b *IFD) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Entries) != len(b.Entries) {
		return false
	}
	for i := range a.Entries {
		x, y := &a.Entries[i], &b.Entries[i]
		if x.Tag != y.Tag || x.Type != y.Type || x.Count != y.Count || !bytes.Equal(x.Value, y.Value) {
			return false
		}
	}
	return true
}

// TestCanonBase_R407_CorpusShiftedValues pins what the four shifted files read
// after the correction, captured once from ExifTool 13.55 ("exiftool -a -G1 -s":
// Canon LensModel and InternalSerialNumber), without running it.
func TestCanonBase_R407_CorpusShiftedValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file, lens, serial string
	}{
		{"Canon EOS REBEL T3i.jpg", "EF-S18-55mm f/3.5-5.6 IS II", "ZA2146315"},
		{"Canon EOS REBEL T3i_2.jpg", "EF-S18-55mm f/3.5-5.6 IS II", "ZA2146315"},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("..", "testdata", "corpus", "jpeg", "metadata-extractor", tc.file))
			if err != nil {
				t.Skipf("corpus file absent: %v", err)
			}
			e, err := corpusEXIF(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := e.MakerNoteLens(); got != tc.lens {
				t.Errorf("MakerNoteLens = %q, want %q", got, tc.lens)
			}
			if got := e.MakerNoteIFD.Get(0x0096).blankTrimmedString(); got != tc.serial {
				t.Errorf("InternalSerialNumber = %q, want %q", got, tc.serial)
			}
		})
	}
}

// FuzzCanonRebase (task #407): the correction never panics, never modifies its
// input, returns a copy of the same length, and every out-of-line pointer of
// the copy lies inside the note.
func FuzzCanonRebase(f *testing.F) {
	for _, shift := range []int{0, 164, -4} {
		tiff, lay := testutil.MakerNoteTIFF(canonSpec(binary.LittleEndian, shift))
		f.Add(bytes.Clone(tiff[lay.MNOffset:lay.MNOffset+lay.MNLen]), uint32(lay.MNOffset), false) //nolint:gosec // G115: test sizes are small
	}
	f.Add([]byte{}, uint32(0), true)
	f.Fuzz(func(t *testing.T, mn []byte, off uint32, be bool) {
		var parent binary.ByteOrder = binary.LittleEndian
		if be {
			parent = binary.BigEndian
		}
		in := bytes.Clone(mn)
		c, ok := canonRebasedCopy(mn, parent, uint64(off))
		if !bytes.Equal(in, mn) {
			t.Fatal("input modified")
		}
		if !ok {
			return
		}
		if len(c) != len(mn) {
			t.Fatalf("copy length %d, want %d", len(c), len(mn))
		}
		order, _ := countOrder(mn[0], mn[1], parent)
		lay := MakerNoteLayout{ByteOrder: order, TIFFRelative: true, layout: mnCanon}
		eachOOL(c, lay, func(ptr, total int64) {
			if ptr < 0 || ptr+total > int64(len(c)) {
				t.Fatalf("pointer %d+%d outside the %d-byte note", ptr, total, len(c))
			}
		})
	})
}
