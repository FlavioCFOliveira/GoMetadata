package exif

// makernote_detect_test.go — tests for task #304: MakerNote layout
// detection by signature with the Make string as a fallback.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// legacyMakerNoteMakes are the 23 Make strings the former exact-Make table
// dispatched on. Every one must still select a parser.
var legacyMakerNoteMakes = []string{ //nolint:gochecknoglobals // test table
	"Canon", "NIKON CORPORATION", "Nikon", "SONY", "FUJIFILM",
	"OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus",
	"PENTAX Corporation", "Ricoh", "RICOH", "Panasonic",
	"LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica",
	"DJI", "SAMSUNG", "SIGMA",
	"CASIO COMPUTER CO.,LTD.", "Casio Computer Co.,Ltd.", "CASIO",
}

// mnBlob builds a MakerNote of prefix, zero padding up to off, and one IFD
// entry (tag 0x0001, SHORT, count 1, value 7) in order at off.
func mnBlob(order binary.ByteOrder, prefix string, off int) []byte {
	b := make([]byte, off+2+12+4)
	copy(b, prefix)
	order.PutUint16(b[off:], 1)
	order.PutUint16(b[off+2:], 1)  // tag
	order.PutUint16(b[off+4:], 3)  // SHORT
	order.PutUint32(b[off+6:], 1)  // count
	order.PutUint16(b[off+10:], 7) // value
	return b
}

// TestDetectMakerNote_Signatures (task #304): one synthetic MakerNote per
// signature must select the right layout, IFD position and value base, and
// parse to an IFD holding the planted entry.
func TestDetectMakerNote_Signatures(t *testing.T) {
	t.Parallel()
	le, be := binary.ByteOrder(binary.LittleEndian), binary.ByteOrder(binary.BigEndian)

	// nikon3: "Nikon\0" + version + embedded TIFF header (II*\0, IFD offset 8)
	// at 10, IFD at 18.
	nikon3 := make([]byte, 18+2+12+4)
	copy(nikon3, "Nikon\x00\x02\x10\x00\x00II*\x00")
	binary.LittleEndian.PutUint32(nikon3[14:], 8)
	copy(nikon3[18:], mnBlob(le, "", 0))

	// fujifilm: IFD offset stored LE at [8:12] (metadata-extractor
	// processMakernote; task #312), bytes 12..15 are filler.
	fuji := mnBlob(le, "FUJIFILM", 16)
	binary.LittleEndian.PutUint32(fuji[8:], 16)
	copy(fuji[12:], "0100")

	tests := []struct {
		name   string
		blob   []byte
		make   string
		order  binary.ByteOrder // parent order
		vendor string
		off    int
		base   mnBase
		hasIFD bool
	}{
		{"olympus-old OLYMP", mnBlob(le, "OLYMP\x00\x01\x00", 8), "OLYMPUS OPTICAL CO.,LTD", le, "olympus-old", 8, mnBaseTIFF, true},
		{"olympus-old EPSON", mnBlob(le, "EPSON\x00\x01\x00", 8), "EPSON", le, "olympus-old", 8, mnBaseTIFF, true},
		{"olympus-old AGFA", mnBlob(le, "AGFA\x00\x01\x00\x00", 8), "AGFA", le, "olympus-old", 8, mnBaseTIFF, true},
		{"olympus II", mnBlob(le, "OLYMPUS\x00II\x03\x00", 12), "OLYMPUS IMAGING CORP.", le, "olympus", 12, mnBaseBlob, true},
		{"om-system", mnBlob(le, "OM SYSTEM\x00\x00\x00II", 14), "OM Digital Solutions Corporation", le, "om-system", 14, mnBaseBlob, true},
		{"om-system MM", mnBlob(be, "OM SYSTEM\x00\x00\x00MM", 14), "OM Digital Solutions Corporation", le, "om-system", 14, mnBaseBlob, true},
		{"nikon-1 header", mnBlob(le, "Nikon\x00\x01\x00", 8), "NIKON", le, "nikon-1", 8, mnBaseTIFF, true},
		{"nikon-3", nikon3, "NIKON CORPORATION", le, "nikon-3", -1, mnBaseBlob, true},
		{"sony-cam CAM", mnBlob(le, "SONY CAM\x00\x00\x00\x00", 12), "SONY", le, "sony-cam", 12, mnBaseTIFF, true},
		{"sony-cam DSC", mnBlob(le, "SONY DSC\x00\x00\x00\x00", 12), "SONY", le, "sony-cam", 12, mnBaseTIFF, true},
		{"sony-ericsson", mnBlob(be, "SEMC MS\x00\x00\x00\x00\x00MM", 20), "Sony Ericsson", le, "sony-ericsson", 20, mnBaseTIFF, true},
		{"sigma", mnBlob(le, "SIGMA\x00\x00\x00\x00\x00", 10), "SIGMA", le, "sigma", 10, mnBaseTIFF, true},
		{"foveon", mnBlob(le, "FOVEON\x00\x00\x00\x00", 10), "SIGMA", le, "sigma", 10, mnBaseTIFF, true},
		{"kodak", []byte("KDK INFO\x00\x00\x00\x00\x00\x00\x00\x00\x00"), "EASTMAN KODAK COMPANY", le, "kodak", -1, mnBaseNone, false},
		{"casio-2 QVC", mnBlob(be, "QVC\x00\x00\x00", 6), "CASIO COMPUTER CO.,LTD.", le, "casio-2", 6, mnBaseTIFF, true},
		{"fujifilm", fuji, "FUJIFILM", le, "fujifilm", -1, mnBaseBlob, true},
		{"kyocera", mnBlob(le, "KYOCERA", 22), "KYOCERA", le, "kyocera", 22, mnBaseTIFF, true},
		{"leica", mnBlob(le, "LEICA\x00\x01\x00", 8), "LEICA CAMERA AG", le, "leica", 8, mnBaseBlob, true},
		{"leica TIFF-based", mnBlob(le, "LEICA\x00\x02\xFF", 8), "Leica Camera AG", le, "leica", 8, mnBaseTIFF, true},
		{"panasonic", mnBlob(le, "Panasonic\x00\x00\x00", 12), "Panasonic", le, "panasonic", 12, mnBaseTIFF, true},
		{"pentax AOC", mnBlob(be, "AOC\x00II", 6), "PENTAX Corporation", le, "pentax-aoc", 6, mnBaseBlob, true},
		{"pentax PENTAX", mnBlob(le, "PENTAX \x00II", 10), "PENTAX Corporation", le, "pentax", 10, mnBaseBlob, true},
		{"sanyo", mnBlob(le, "SANYO\x00\x01\x00", 8), "SANYO Electric Co.,Ltd.", le, "sanyo", 8, mnBaseBlob, true},
		{"ricoh", mnBlob(be, "Ricoh\x00\x00\x00", 8), "RICOH", le, "ricoh", 8, mnBaseBlob, true},
		{"apple", mnBlob(be, "Apple iOS\x00\x00\x01MM", 14), "Apple", le, "apple", 14, mnBaseBlob, true},
		{"apple without Make", mnBlob(be, "Apple iOS\x00\x00\x01MM", 14), "", le, "apple", 14, mnBaseBlob, true},
		{"reconyx UF", []byte("RECONYXUF\x00\x00\x00\x00\x00\x00\x00"), "RECONYX", le, "reconyx", -1, mnBaseNone, false},
		{"reconyx H2", []byte("RECONYXH2\x00\x00\x00\x00\x00\x00\x00"), "RECONYX", le, "reconyx", -1, mnBaseNone, false},
		{"reconyx HyperFire", []byte{0x01, 0xF1, 0, 0, 0, 0}, "RECONYX", le, "reconyx", -1, mnBaseNone, false},
		// Make-based layouts without a signature.
		{"canon", mnBlob(le, "", 0), "Canon", le, "canon", 0, mnBaseTIFF, true},
		{"canon upper", mnBlob(le, "", 0), "CANON", le, "canon", 0, mnBaseTIFF, true},
		{"minolta", mnBlob(le, "", 0), "MINOLTA CO.,LTD", le, "minolta", 0, mnBaseTIFF, true},
		{"nikon-2 upper plain", mnBlob(be, "", 0), "NIKON", le, "nikon-2", 0, mnBaseTIFF, true},
		{"samsung", mnBlob(le, "", 0), "SAMSUNG", le, "samsung", 0, mnBaseTIFF, true},
		{"dji", mnBlob(le, "", 0), "DJI", le, "dji", 0, mnBaseTIFF, true},
		{"casio plain", mnBlob(le, "", 0), "CASIO COMPUTER CO.,LTD.", le, "casio", 0, mnBaseTIFF, true},
		{"sony plain", mnBlob(le, "", 0), "SONY", le, "sony", 0, mnBaseTIFF, true},
		{"pentax optio plain", mnBlob(le, "", 0), "PENTAX Corporation", le, "pentax", 0, mnBaseBlob, true},
		{"asahi plain", mnBlob(le, "", 0), "Asahi Optical Co.,Ltd.", le, "pentax", 0, mnBaseBlob, true},
		{"om digital make, olympus signature", mnBlob(le, "OLYMPUS\x00II\x03\x00", 12), "OM Digital Solutions", le, "olympus", 12, mnBaseBlob, true},
		{"leica make, plain", mnBlob(le, "", 0), "Leica Camera AG", le, "leica", 0, mnBaseTIFF, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := detectMakerNote(tc.blob, tc.make)
			if f.name != tc.vendor || f.ifdOffset != tc.off || f.base != tc.base {
				t.Fatalf("detectMakerNote = {%q, %d, %d}, want {%q, %d, %d}", f.name, f.ifdOffset, f.base, tc.vendor, tc.off, tc.base)
			}
			ifd := parseMakerNoteIFD(tc.blob, tc.make, tc.order)
			if !tc.hasIFD {
				if ifd != nil {
					t.Fatalf("binary layout %q returned an IFD", tc.vendor)
				}
				return
			}
			if ifd == nil {
				t.Fatalf("parseMakerNoteIFD returned nil for %s", tc.name)
			}
			e := ifd.Get(1)
			if e == nil || len(e.Value) < 2 {
				t.Fatalf("planted entry 0x0001 missing from the parsed IFD")
			}
			ord := tc.order
			if strings.Contains(tc.name, "MM") || tc.base == mnBaseNone || tc.vendor == "casio-2" || tc.vendor == "apple" ||
				tc.vendor == "ricoh" || tc.vendor == "sony-ericsson" || tc.vendor == "pentax-aoc" || tc.name == "nikon-2 upper plain" {
				ord = binary.BigEndian
			}
			if got := ord.Uint16(e.Value); got != 7 {
				t.Errorf("entry value = %d, want 7", got)
			}
		})
	}
}

// TestDetectMakerNote_LegacyMakesStillDispatch (task #304): no Make the old
// exact table accepted may be lost, with or without NUL padding or spaces.
func TestDetectMakerNote_LegacyMakesStillDispatch(t *testing.T) {
	t.Parallel()
	for _, mk := range legacyMakerNoteMakes {
		if f := detectMakerNote(nil, mk); f.parse == nil {
			t.Errorf("Make %q no longer selects a parser", mk)
		}
		if got := makerNoteDispatch([]byte(" "+mk+" \x00"), binary.LittleEndian, nil); got != nil {
			t.Errorf("empty MakerNote parsed to an IFD for %q", mk)
		}
	}
}

// TestDetectMakerNote_Unrecognised: no signature and an unknown or empty Make
// detect nothing.
func TestDetectMakerNote_Unrecognised(t *testing.T) {
	t.Parallel()
	blob := mnBlob(binary.LittleEndian, "", 0)
	// "Sony": the Sony family match is case-sensitive, as in the reference,
	// because Sony Mobile phones write Make "Sony" with a non-IFD header.
	for _, mk := range []string{"", "ACME", "UNKNOWN_BRAND_XYZ", "Cano", "Nik", "Sony"} {
		if f := detectMakerNote(blob, mk); f.parse != nil {
			t.Errorf("Make %q detected %q without a signature", mk, f.name)
		}
	}
	if f := detectMakerNote(nil, ""); f.parse != nil {
		t.Errorf("nil MakerNote and empty Make detected %q", f.name)
	}
	// Casio type 2 needs the full six-byte "QVC\0\0\0" signature.
	if f := detectMakerNote(mnBlob(binary.BigEndian, "QVC\x00\x01\x00", 6), ""); f.parse != nil {
		t.Errorf("partial QVC signature detected %q", f.name)
	}
}

// TestDetectMakerNote_ZeroAllocs (task #304): detection must not allocate.
func TestDetectMakerNote_ZeroAllocs(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics under t.Parallel
	apple := mnBlob(binary.BigEndian, "Apple iOS\x00\x00\x01MM", 14)
	plain := mnBlob(binary.LittleEndian, "", 0)
	mk := []byte("NIKON CORPORATION")
	allocs := testing.AllocsPerRun(1000, func() {
		_ = detectMakerNote(apple, mk[:0])
		_ = detectMakerNote(plain, mk)
		_ = detectMakerNote(plain, "Canon")
	})
	if allocs != 0 {
		t.Errorf("detectMakerNote allocates %.0f times per run, want 0", allocs)
	}
}

// legacyMakerNoteParser is the exact-Make dispatch table GoMetadata used
// before task #304, kept here as the baseline the corpus regression test
// compares against.
func legacyMakerNoteParser(camMake string) func([]byte, binary.ByteOrder) *IFD {
	switch strings.TrimSpace(camMake) {
	case "Canon":
		return parseCanonMakerNote
	case "NIKON CORPORATION", "Nikon":
		return func(b []byte, _ binary.ByteOrder) *IFD { return parseNikonMakerNote(b) }
	case "SONY":
		return parseSonyMakerNote
	case "FUJIFILM":
		return func(b []byte, _ binary.ByteOrder) *IFD { return parseFujifilmMakerNote(b) }
	case "OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus":
		return func(b []byte, _ binary.ByteOrder) *IFD { return parseOlympusMakerNote(b) }
	case "PENTAX Corporation", "Ricoh", "RICOH":
		return func(b []byte, _ binary.ByteOrder) *IFD { return parsePentaxMakerNote(b) }
	case "Panasonic":
		return func(b []byte, _ binary.ByteOrder) *IFD { return parsePanasonicMakerNote(b) }
	case "LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica":
		return parseLeicaMakerNote
	case "DJI":
		return parseDJIMakerNote
	case "SAMSUNG":
		return parseSamsungMakerNote
	case "SIGMA":
		return func(b []byte, _ binary.ByteOrder) *IFD { return parseSigmaMakerNote(b) }
	case "CASIO COMPUTER CO.,LTD.", "Casio Computer Co.,Ltd.", "CASIO":
		return parseCasioMakerNote
	}
	return nil
}

// corpusEXIF returns the parsed EXIF of a corpus file: the TIFF stream after
// the first JPEG "Exif\0\0" identifier, or the file itself for TIFF-based
// files.
func corpusEXIF(data []byte) (*EXIF, error) {
	off := 0
	if i := bytes.Index(data[:min(len(data), 1<<16)], []byte("Exif\x00\x00")); i >= 0 {
		off = i + 6
	}
	return Parse(data[off:])
}

// corpusFiles lists the corpus files with the given extensions.
func corpusFiles(t *testing.T, exts ...string) []string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(filepath.Join("..", "testdata", "corpus"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && slices.Contains(exts, strings.ToLower(filepath.Ext(p))) {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// TestParse_CorpusMakerNoteNoVendorLost (task #304, corpus): for every corpus
// file whose MakerNote the former exact-Make table parsed, signature detection
// still yields a MakerNote IFD. Run against HEAD 31b187b, the comparison
// showed 544 files parsed before and 762 after; the only entry-count drops
// are Sony "SONY DSC"/"SONY CAM" notes, which the former table parsed from
// offset 0, reading the ASCII header as an entry count (for example "Sony
// DSC-H9.jpg": 437 entries before, 36 after, 37 per ExifTool).
func TestParse_CorpusMakerNoteNoVendorLost(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, ".jpg", ".jpeg", ".tif", ".tiff", ".nef", ".arw", ".cr2", ".dng", ".orf", ".rw2")
	if len(files) == 0 {
		t.Skip("corpus absent (see testdata/download.sh)")
	}
	checked := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil || len(data) > 32<<20 {
			continue
		}
		e, err := corpusEXIF(data)
		if err != nil || e == nil || e.IFD0 == nil || len(e.MakerNote) == 0 {
			continue
		}
		var camMake string
		if me := e.IFD0.Get(TagMake); me != nil {
			camMake = strings.TrimRight(string(me.Value), "\x00")
		}
		legacy := legacyMakerNoteParser(camMake)
		if legacy == nil || legacy(e.MakerNote, e.ByteOrder) == nil {
			continue
		}
		checked++
		if e.MakerNoteIFD == nil {
			t.Errorf("%s (Make %q): MakerNote parsed by the former table is no longer parsed", filepath.Base(f), camMake)
		}
	}
	if checked == 0 {
		t.Fatal("no corpus file with a MakerNote the former table parsed")
	}
}

// TestParse_CorpusMakerNoteEntryCounts (task #304, corpus): vendors that the
// former exact-Make table missed are now parsed, and their IFDs hold the
// entry count ExifTool 13.55 reports for the same file ("exiftool -v1",
// MakerNotes directory). The PENTAX rows also pin the "PENTAX \0" + byte-order
// layout, whose IFD starts at offset 10.
func TestParse_CorpusMakerNoteEntryCounts(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("..", "testdata", "corpus", "jpeg", "metadata-extractor")
	cases := []struct {
		file   string
		vendor string
		want   int
	}{
		{"Apple iPhone 6 plus.jpg", "apple", 14},
		{"Pentax K-1 Mark II.jpg", "pentax", 114},
		{"Pentax QS1.jpg", "pentax", 93},
		{"Ricoh WG20.jpg", "pentax", 67},
		{"Ricoh WG4 GPS.jpg", "pentax", 72},
		{"Pentax Optio 430_2.jpg", "pentax", 27},
		{"Samsung SM-N950F (Note 8).jpg", "samsung", 7},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(dir, tc.file))
			if errors.Is(err, os.ErrNotExist) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", tc.file)
			}
			if err != nil {
				t.Fatal(err)
			}
			e, err := corpusEXIF(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			var camMake string
			if me := e.IFD0.Get(TagMake); me != nil {
				camMake = strings.TrimSpace(strings.TrimRight(string(me.Value), "\x00"))
			}
			if f := detectMakerNote(e.MakerNote, camMake); f.name != tc.vendor {
				t.Errorf("detected %q, want %q", f.name, tc.vendor)
			}
			if e.MakerNoteIFD == nil {
				t.Fatal("MakerNote IFD not parsed")
			}
			if got := len(e.MakerNoteIFD.Entries); got != tc.want {
				t.Errorf("MakerNote entries = %d, ExifTool reports %d", got, tc.want)
			}
		})
	}
}

// BenchmarkDetectMakerNote measures detection alone.
func BenchmarkDetectMakerNote(b *testing.B) {
	apple := mnBlob(binary.BigEndian, "Apple iOS\x00\x00\x01MM", 14)
	canon := mnBlob(binary.LittleEndian, "", 0)
	mk := []byte("Canon")
	b.ReportAllocs()
	for range b.N {
		_ = detectMakerNote(apple, mk)
		_ = detectMakerNote(canon, mk)
	}
}
