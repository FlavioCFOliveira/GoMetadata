package gometadata

// write_task400_403_test.go — tasks #400, #401, #402 and #403 against the
// committed fixtures and the optional corpus (testdata/download.sh), with
// ExifTool as the oracle where it is installed.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// corpusFiles returns the files of testdata/corpus/dir with one of the
// extensions (lower case, with the dot), sorted, skipping the test when the
// corpus is absent.
func corpusFiles(t *testing.T, dir string, exts ...string) []string {
	t.Helper()
	root := filepath.Join("testdata", "corpus", dir)
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("corpus absent (run testdata/download.sh): %s", root)
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(p))
		for _, e := range exts {
			if ext == e {
				files = append(files, filepath.ToSlash(p))
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

// jpegSegments returns the markers (APP1 as 'E' for Exif, 'X' for XMP, 'P'
// for APP13 Photoshop, others as the marker byte) of b before the first scan
// header. It reads JPEG ISO/IEC 10918-1 §B.1.1 alone, and stops at the first
// byte it cannot read as a segment.
func jpegSegments(b []byte) []byte {
	var out []byte
	pos := 2
	for pos+4 <= len(b) && b[pos] == 0xFF && b[pos+1] != 0xFF && b[pos+1] != 0 {
		m := b[pos+1]
		n := int(binary.BigEndian.Uint16(b[pos+2:]))
		if n < 2 || pos+2+n > len(b) {
			break
		}
		p := b[pos+4 : pos+2+n]
		switch {
		case m == 0xDA:
			return append(out, 'S')
		case m == 0xE1 && bytes.HasPrefix(p, []byte("Exif\x00\x00")):
			out = append(out, 'E')
		case m == 0xE1 && bytes.HasPrefix(p, []byte("http://ns.adobe.com/")):
			out = append(out, 'X')
		case m == 0xED && bytes.HasPrefix(p, []byte("Photoshop 3.0\x00")):
			out = append(out, 'P')
		case m == 0xE0 && bytes.HasPrefix(p, []byte("JFIF\x00")):
			out = append(out, 'J')
		case m == 0xE2:
			out = append(out, 'C')
		case m == 0xDB:
			out = append(out, 'Q')
		default:
			out = append(out, '?')
		}
		pos += 2 + n
	}
	return out
}

// stripJPEGMetadata returns b without its Exif, XMP and Photoshop segments
// before the first scan header; a segment that cannot be read ends the walk and
// the rest is kept (see jpegSegments).
func stripJPEGMetadata(b []byte) []byte {
	if len(b) < 2 {
		return b
	}
	out := append([]byte(nil), b[:2]...)
	pos := 2
	for {
		start, p := pos, pos
		if p+2 > len(b) || b[p] != 0xFF {
			return append(out, b[start:]...)
		}
		for p+2 < len(b) && b[p+1] == 0xFF {
			p++
		}
		m, hdrEnd := b[p+1], p+2
		switch {
		case m == 0 || m == 0xD9:
			return append(out, b[start:]...)
		case m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			out = append(out, b[start:hdrEnd]...)
			pos = hdrEnd
			continue
		}
		if hdrEnd+2 > len(b) {
			return append(out, b[start:]...)
		}
		n := int(binary.BigEndian.Uint16(b[hdrEnd:]))
		end := hdrEnd + n
		if n < 2 || end > len(b) || m == 0xDA {
			return append(out, b[start:]...)
		}
		payload := b[hdrEnd+2 : end]
		isMeta := m == 0xE1 && (bytes.HasPrefix(payload, []byte("Exif\x00\x00")) || bytes.HasPrefix(payload, []byte("http://ns.adobe.com/x"))) ||
			m == 0xED && bytes.HasPrefix(payload, []byte("Photoshop 3.0\x00"))
		if !isMeta {
			out = append(out, b[start:end]...)
		}
		pos = end
	}
}

// TestTask400_ModifiedWriteKeepsJFIFFirst: after a write that modifies the
// EXIF the JFIF APP0 segment still follows SOI (ITU-T T.871 | ISO/IEC 10918-5
// §6.3, p. 3) and the replaced EXIF segment keeps its place: APP0, APP1,
// APP2, DQT (testdata/fixtures/Canon_40D.jpg, the repro of task #400).
func TestTask400_ModifiedWriteKeepsJFIFFirst(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/fixtures/Canon_40D.jpg")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.SetISO(1234)
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatal(err)
	}
	if got, want := string(jpegSegments(out.Bytes())[:4]), "JECQ"; got != want {
		t.Errorf("segments = %q, want %q (APP0 JFIF, APP1 Exif, APP2 ICC, DQT)", got, want)
	}
	if got, want := string(jpegSegments(src)[:4]), "JECQ"; got != want {
		t.Errorf("source segments = %q, want %q", got, want)
	}
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if iso, ok := m2.ISO(); !ok || iso != 1234 {
		t.Errorf("ISO after the write = (%d, %v), want 1234", iso, ok)
	}
	if bin, lerr := exec.LookPath("exiftool"); lerr == nil {
		dir := t.TempDir()
		p := filepath.Join(dir, "out.jpg")
		if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		got, _ := exec.Command(bin, "-s3", "-ISO", "-warning", "-a", p).CombinedOutput() //nolint:gosec // G204: the binary is the tool this test exists to run
		if strings.TrimSpace(string(got)) != "1234" {
			t.Errorf("ExifTool reads %q, want only the ISO 1234 and no warning", got)
		}
	}
}

// TestTask400_Corpus_JFIFStaysFirst: every corpus JPEG that starts with a JFIF
// APP0 segment keeps it first after a write that modifies metadata.
func TestTask400_Corpus_JFIFStaysFirst(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, "jpeg", ".jpg", ".jpeg")
	pinned := readCorpusRejects(t)
	jfif := []byte("\xFF\xD8\xFF\xE0")
	var checked int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, jfif) || len(data) < 11 || string(data[6:11]) != "JFIF\x00" {
			continue
		}
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			continue
		}
		m.SetCaption("task 400")
		var out bytes.Buffer
		if err := Write(bytes.NewReader(data), &out, m); err != nil {
			if _, ok := pinned[f]; !ok {
				t.Errorf("%s: Write: %v", f, err)
			}
			continue
		}
		n := int(binary.BigEndian.Uint16(data[4:])) + 2
		if !bytes.HasPrefix(out.Bytes()[2:], data[2:2+n]) {
			t.Errorf("%s: the JFIF APP0 segment does not follow SOI after a modified write", f)
		}
		checked++
	}
	t.Logf("%d corpus JPEGs start with JFIF APP0 and were written", checked)
}

// TestTask403_Corpus_JPEGSpliceMetadataless: Read, SetCaption and Write of a
// JPEG without metadata does not read the file again: no error, and the output
// is the source with the metadata segments added (an AFCP or FotoStation
// trailer is rewritten by design, so the part after the image data is not
// compared for a file that has one).
func TestTask403_Corpus_JPEGSpliceMetadataless(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, "jpeg", ".jpg", ".jpeg")
	var written, notJPEG, trailers int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := Read(bytes.NewReader(data))
		if err != nil || m.EXIF != nil || m.IPTC != nil || m.XMP != nil {
			continue
		}
		if !bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
			// Reason: a file named .jpg that holds another format (HEIF); its own
			// writer refuses it for want of a metadata container.
			notJPEG++
			continue
		}
		m.SetCaption("task 403")
		var out bytes.Buffer
		if err := Write(bytes.NewReader(data), &out, m); err != nil {
			t.Errorf("%s: Write: %v", f, err)
			continue
		}
		got, want := stripJPEGMetadata(out.Bytes()), stripJPEGMetadata(data)
		if !bytes.Equal(got, want) {
			// Reason: an AFCP or FotoStation trailer after the image holds an IPTC
			// copy that the write rewrites (task #322); nothing else may differ.
			if !bytes.Contains(data, []byte("AXS")) && !bytes.Contains(data, []byte("\x00\x00FSN")) {
				t.Errorf("%s: the output without metadata segments differs from the source", f)
			}
			trailers++
		}
		if !bytes.Contains(out.Bytes(), []byte("http://ns.adobe.com/xap/1.0/\x00")) {
			t.Errorf("%s: the XMP segment is not in the output", f)
		}
		written++
	}
	t.Logf("%d metadata-less JPEGs written (%d with a rewritten trailer); %d files named .jpg hold another format", written, trailers, notJPEG)
}

// pngStrip returns b without its eXIf and text chunks; it stops at the first
// chunk it cannot read and keeps the rest (W3C PNG 3rd ed. §5.3).
func pngStrip(b []byte) []byte {
	out := append([]byte(nil), b[:8]...)
	pos := 8
	for pos+12 <= len(b) {
		n := uint64(binary.BigEndian.Uint32(b[pos:]))
		if uint64(pos)+12+n > uint64(len(b)) {
			break
		}
		end := pos + 12 + int(n) //nolint:gosec // G115: n <= len(b) was checked above
		switch string(b[pos+4 : pos+8]) {
		case "eXIf", "tEXt", "zTXt", "iTXt":
		default:
			out = append(out, b[pos:end]...)
		}
		pos = end
	}
	return append(out, b[pos:]...)
}

// TestTask403_Corpus_PNGSpliceMetadataless: the same for PNG. A source that
// has no IHDR chunk to place the metadata after is refused (its first chunk
// is not IHDR, or is damaged); a source that ends at a chunk boundary without
// IEND receives one (W3C PNG 3rd ed. §5.6), which is the only change besides
// the metadata: m6-1f97f040d0b6b26faeb0a1a7f1499590.png (856 to 868 bytes
// without the XMP).
func TestTask403_Corpus_PNGSpliceMetadataless(t *testing.T) {
	t.Parallel()
	files := corpusFiles(t, "png", ".png")
	iend := []byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82}
	var written, noIHDR, addedIEND int
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := Read(bytes.NewReader(data))
		if err != nil || m.EXIF != nil || m.IPTC != nil || m.XMP != nil {
			continue
		}
		hasIHDR := len(data) >= 16 && string(data[12:16]) == "IHDR" && binary.BigEndian.Uint32(data[8:]) == 13
		m.SetCaption("task 403")
		var out bytes.Buffer
		err = Write(bytes.NewReader(data), &out, m)
		if !hasIHDR {
			// Reason: no IHDR chunk, so no place for the metadata.
			if err == nil {
				t.Errorf("%s: a file with no IHDR chunk was written", f)
			}
			noIHDR++
			continue
		}
		if err != nil {
			t.Errorf("%s: Write: %v", f, err)
			continue
		}
		got, want := pngStrip(out.Bytes()), pngStrip(data)
		switch {
		case bytes.Equal(got, want):
		case bytes.Equal(got, append(append([]byte(nil), want...), iend...)):
			addedIEND++ // the source ends at a chunk boundary without IEND
		default:
			t.Errorf("%s: the output without eXIf and text chunks differs from the source", f)
		}
		written++
	}
	t.Logf("%d metadata-less PNGs written (%d got an IEND chunk), %d have no IHDR chunk", written, addedIEND, noIHDR)
}

// TestTask403_PNG_M6GrowsByIEND explains the size of the m6 corpus file: it ends
// at a chunk boundary with a corrupted IEND ("IFND"), so a write appends the
// 12-byte IEND chunk the file lacks; with SetCaption the XMP chunk is added.
func TestTask403_PNG_M6GrowsByIEND(t *testing.T) {
	t.Parallel()
	const f = "testdata/corpus/png/metadata-extractor/m6-1f97f040d0b6b26faeb0a1a7f1499590.png"
	data, err := os.ReadFile(f)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", f)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 856 || !bytes.HasSuffix(data, []byte("IFND\xae\x42\x60\x82")) {
		t.Fatalf("fixture changed: %d bytes, tail %x", len(data), data[len(data)-8:])
	}
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCaption("m6")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(data), &out, m); err != nil {
		t.Fatal(err)
	}
	if out.Len() <= len(data) || !bytes.HasSuffix(out.Bytes(), []byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82}) {
		t.Errorf("output is %d bytes and ends with %x: want an appended IEND chunk", out.Len(), out.Bytes()[out.Len()-12:])
	}
	if want := append(append([]byte(nil), pngStrip(data)...), 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82); !bytes.Equal(pngStrip(out.Bytes()), want) {
		t.Error("the output without eXIf and text chunks is not the source and the IEND chunk")
	}
}

// exiftoolValues runs ExifTool over files and returns, for each file, the
// values of the group-qualified tags it reports.
func exiftoolValues(t *testing.T, bin string, files []string, tags ...string) map[string]map[string]string {
	t.Helper()
	list := filepath.Join(t.TempDir(), "files.txt")
	if err := os.WriteFile(list, []byte(strings.Join(files, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-json", "-G1", "-a", "-fast", "-@", list}, tags...)
	out, _ := exec.Command(bin, args...).Output() //nolint:gosec // G204: the binary is the tool this test exists to run; exit status 1 means some file could not be read
	var objs []map[string]any
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	if err := dec.Decode(&objs); err != nil {
		t.Fatalf("ExifTool output: %v", err)
	}
	res := make(map[string]map[string]string, len(objs))
	for _, o := range objs {
		src, _ := o["SourceFile"].(string)
		vals := map[string]string{}
		for k, v := range o {
			switch x := v.(type) {
			case string:
				vals[k] = x
			case json.Number:
				vals[k] = x.String()
			}
		}
		res[src] = vals
	}
	return res
}

// TestTask401_Corpus_TextTagsMatchExifTool: Make, Model, Software, LensModel and
// Artist of the EXIF of every corpus file are the strings ExifTool reports
// (`exiftool -G1`, which removes trailing blanks, Exif.pm RawConv
// s/\s+$//), for every file where ExifTool reports them.
func TestTask401_Corpus_TextTagsMatchExifTool(t *testing.T) {
	t.Parallel()
	bin, lerr := exec.LookPath("exiftool")
	if lerr != nil {
		t.Skip("exiftool binary absent (install exiftool): the ExifTool oracle check cannot run")
	}
	files := corpusFiles(t, ".", ".jpg", ".jpeg", ".png", ".tif", ".tiff", ".webp", ".heic", ".heif", ".avif", ".dng", ".nef", ".arw", ".cr2", ".cr3", ".orf", ".rw2")
	et := exiftoolValues(t, bin, files, "-Make", "-Model", "-Software", "-LensModel", "-Artist")
	type check struct {
		key   string
		entry func(*exif.EXIF) *exif.IFDEntry
		got   func(*exif.EXIF) string
	}
	checks := []check{
		{"IFD0:Make", func(e *exif.EXIF) *exif.IFDEntry { return e.IFD0.Get(exif.TagMake) }, (*exif.EXIF).Make},
		{"IFD0:Model", func(e *exif.EXIF) *exif.IFDEntry { return e.IFD0.Get(exif.TagModel) }, (*exif.EXIF).CameraModel},
		{"IFD0:Software", func(e *exif.EXIF) *exif.IFDEntry { return e.IFD0.Get(exif.TagSoftware) }, (*exif.EXIF).Software},
		{"ExifIFD:LensModel", func(e *exif.EXIF) *exif.IFDEntry { return e.ExifIFD.Get(exif.TagLensModel) }, (*exif.EXIF).LensModel},
		{"IFD0:Artist", func(e *exif.EXIF) *exif.IFDEntry { return e.IFD0.Get(exif.TagArtist) }, (*exif.EXIF).Creator},
	}
	var compared, absent, nonString, mismatches int
	for _, f := range files {
		vals, ok := et[f]
		if !ok {
			continue
		}
		m, err := ReadFile(f)
		if err != nil || m.EXIF == nil {
			continue
		}
		for _, c := range checks {
			want, has := vals[c.key]
			if !has {
				continue
			}
			ent := c.entry(m.EXIF)
			switch {
			case ent == nil:
				// Reason: ExifTool reports the tag of another directory that has
				// the same name (the IFD0 of the JpgFromRaw preview of an RW2
				// file, listed as IFD0 with -G1); this file's own IFD has none.
				absent++
				continue
			case ent.Type != exif.TypeASCII && ent.Type != exif.TypeUTF8:
				// Reason: the tag is stored with a numeric type (a crafted file,
				// exiv2 pocIssue283 and issue_839); ExifTool prints the number,
				// the accessor returns no text, as it always has.
				nonString++
				continue
			}
			compared++
			// ExifTool prints a byte that is not valid text as "?".
			if got := strings.ToValidUTF8(c.got(m.EXIF), "?"); got != want {
				mismatches++
				t.Errorf("%s: %s = %q, ExifTool reports %q", f, c.key, got, want)
			}
		}
	}
	t.Logf("%d values compared with ExifTool, %d mismatches; %d not held by the file's own IFDs, %d stored with a numeric type", compared, mismatches, absent, nonString)
}

// TestTask401_Corpus_NamedFiles: the cases of the task: Make of a CASIO file that
// pads it with blanks, Software of a HEIC, Creator of a file with a padded Artist.
func TestTask401_Corpus_NamedFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file string
		got  func(*Metadata) string
		want string
	}{
		{"testdata/corpus/jpeg/exif-samples/35-empty.jpg", (*Metadata).Make, "CASIO COMPUTER CO.,LTD"},
		{"testdata/corpus/heif/exiv2/Stonehenge.heic", (*Metadata).Software, "Ver.1.00"},
	} {
		m, err := ReadFile(tc.file)
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("corpus file absent (run testdata/download.sh): %s", tc.file)
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if got := tc.got(m); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.file, got, tc.want)
		}
	}
	const f = "testdata/corpus/jpeg/exif-samples/32-lens_data.jpeg"
	m, err := ReadFile(f)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", f)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Creator(); got != strings.TrimRight(got, " \t\r\n\v\f") {
		t.Errorf("Creator = %q: trailing blank kept", got)
	}
}

// TestTask402_Corpus_GPSMatchesExifTool: the GPS position of every corpus file
// for which ExifTool reports one is the one GPS() returns (a zero denominator
// is the only value GPS() now refuses: ExifTool reports no position for it).
func TestTask402_Corpus_GPSMatchesExifTool(t *testing.T) {
	t.Parallel()
	bin, lerr := exec.LookPath("exiftool")
	if lerr != nil {
		t.Skip("exiftool binary absent (install exiftool): the ExifTool oracle check cannot run")
	}
	files := corpusFiles(t, ".", ".jpg", ".jpeg", ".png", ".tif", ".tiff", ".webp", ".heic", ".heif", ".avif", ".dng", ".nef", ".arw", ".cr2", ".cr3", ".orf", ".rw2")
	list := filepath.Join(t.TempDir(), "files.txt")
	if err := os.WriteFile(list, []byte(strings.Join(files, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(bin, "-json", "-n", "-G1", "-a", "-fast", "-@", list, "-Composite:GPSLatitude", "-Composite:GPSLongitude").Output() //nolint:gosec // G204: the binary is the tool this test exists to run
	var objs []map[string]any
	if err := json.Unmarshal(out, &objs); err != nil {
		t.Fatalf("ExifTool output: %v", err)
	}
	var compared int
	for _, o := range objs {
		src, _ := o["SourceFile"].(string)
		lat, latOK := o["Composite:GPSLatitude"].(float64)
		lon, lonOK := o["Composite:GPSLongitude"].(float64)
		if !latOK || !lonOK {
			continue
		}
		m, err := ReadFile(src)
		if err != nil || m.EXIF == nil {
			continue
		}
		glat, glon, ok := m.EXIF.GPS()
		if !ok {
			t.Errorf("%s: GPS() ok = false, ExifTool reports %v, %v", src, lat, lon)
			continue
		}
		compared++
		// The Composite tags are the signed position (the Ref tags apply to it).
		if math.Abs(glat-lat) > 1e-6 || math.Abs(glon-lon) > 1e-6 {
			t.Errorf("%s: GPS() = (%v, %v), ExifTool reports (%v, %v)", src, glat, glon, lat, lon)
		}
	}
	t.Logf("%d positions compared with ExifTool", compared)
}
