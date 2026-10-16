package gometadata

// write_task317_321_test.go — tasks #317, #318 and #321 through the public API:
// a write into a container that holds the EXIF stream on its own keeps the
// MakerNote valid (R-20 to R-26, docs/conformance/exif-tiff.md) and creates
// EXIF with the tags that Exif 3.1 marks mandatory (S-46, S-47).

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/heif"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
	"github.com/FlavioCFOliveira/GoMetadata/format/png"
	"github.com/FlavioCFOliveira/GoMetadata/format/webp"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// containerWithEXIF stores the raw EXIF stream in a minimal file of the container.
func containerWithEXIF(t *testing.T, container string, tiff []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	var err error
	switch container {
	case "JPEG":
		err = jpeg.Inject(bytes.NewReader(buildMinimalJPEG(nil)), &out, tiff, nil, nil, true)
	case "PNG":
		err = png.Inject(bytes.NewReader(buildMinimalPNG()), &out, tiff, nil, nil, true)
	case "WebP":
		err = webp.Inject(bytes.NewReader(buildMinimalWebP()), &out, tiff, nil, nil, true)
	case "HEIF":
		src, rerr := os.ReadFile("format/heif/testdata/no_metadata.heic")
		if rerr != nil {
			t.Fatal(rerr)
		}
		err = heif.Inject(bytes.NewReader(src), &out, tiff, nil, nil, true)
	default:
		t.Fatalf("unknown container %q", container)
	}
	if err != nil {
		t.Fatalf("%s Inject: %v", container, err)
	}
	return out.Bytes()
}

// makerNoteLayouts are the synthetic layouts the containers are tested with.
func makerNoteLayouts() []struct {
	name string
	spec testutil.MNSpec
} {
	le := binary.LittleEndian
	serial := []byte("SERIAL-0123456\x00")
	entries := []testutil.MNEntry{
		{Tag: 0x0001, Type: 3, Count: 1, Value: []byte{7, 0}},
		{Tag: 0x0002, Type: 2, Count: uint32(len(serial)), Value: serial}, //nolint:gosec // G115: tiny test value
		{Tag: 0x0003, Type: 5, Count: 1, Value: []byte{1, 0, 0, 0, 3, 0, 0, 0}},
	}
	outside := append(append([]testutil.MNEntry(nil), entries...),
		testutil.MNEntry{Tag: 0x0004, Type: 4, Count: 3, Value: []byte{10, 0, 0, 0, 20, 0, 0, 0, 30, 0, 0, 0}, Outside: true})
	return []struct {
		name string
		spec testutil.MNSpec
	}{
		{"Canon", testutil.MNSpec{Make: "Canon", Order: le, TIFFRelative: true, Entries: entries, Footer: true, PadBefore: 6}},
		{"Sony", testutil.MNSpec{Make: "SONY", Order: le, Prefix: []byte("SONY DSC \x00\x00\x00"), TIFFRelative: true, Entries: entries, PadBefore: 6}},
		{"Panasonic", testutil.MNSpec{Make: "Panasonic", Order: le, Prefix: []byte("Panasonic\x00\x00\x00"), TIFFRelative: true, Entries: entries, PadBefore: 6}},
		{"Olympus OLYMP", testutil.MNSpec{Make: "OLYMPUS OPTICAL CO.,LTD", Order: le, Prefix: []byte("OLYMP\x00\x01\x00"), TIFFRelative: true, Entries: outside, PadBefore: 6}},
		{"Nikon Type 2", testutil.MNSpec{Make: "NIKON", Order: le, TIFFRelative: true, Entries: entries, PadBefore: 6}},
	}
}

// TestWrite_Task317_MakerNoteFollowsTheMove: a SetCaption write into JPEG, PNG,
// WebP and HEIF re-encodes the EXIF and so moves the MakerNote; every value of
// its IFD reads the same afterwards, including the Olympus value stored after
// the note (task #318).
func TestWrite_Task317_MakerNoteFollowsTheMove(t *testing.T) {
	t.Parallel()
	for _, lay := range makerNoteLayouts() {
		tiff, mn := testutil.MakerNoteTIFF(lay.spec)
		for _, container := range []string{"JPEG", "PNG", "WebP", "HEIF"} {
			t.Run("R-20/"+lay.name+"/"+container, func(t *testing.T) {
				t.Parallel()
				src := containerWithEXIF(t, container, tiff)
				m, err := Read(bytes.NewReader(src))
				if err != nil {
					t.Fatalf("Read: %v", err)
				}
				if m.EXIF == nil || len(m.EXIF.MakerNote) == 0 {
					t.Fatal("the source container did not yield the MakerNote")
				}
				l, ok := m.EXIF.MakerNoteLayout()
				if !ok {
					t.Fatal("MakerNote layout not detected")
				}
				rawBefore, _, _ := m.RawSegments()
				want := testutil.DecodeMN(rawBefore, int(m.EXIF.MakerNoteOffset), l.IFDOffset, l.ByteOrder, true, 0)

				m.SetCaption("moved")
				var out bytes.Buffer
				if err := Write(bytes.NewReader(src), &out, m); err != nil {
					t.Fatalf("Write: %v", err)
				}
				after, err := Read(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("Read(output): %v", err)
				}
				rawAfter, _, _ := after.RawSegments()
				if int(after.EXIF.MakerNoteOffset) == mn.MNOffset {
					t.Fatalf("the MakerNote did not move (%d): the case proves nothing", mn.MNOffset)
				}
				la, _ := after.EXIF.MakerNoteLayout()
				got := testutil.DecodeMN(rawAfter, int(after.EXIF.MakerNoteOffset), la.IFDOffset, la.ByteOrder, true, 0)
				if len(want) == 0 || len(got) != len(want) {
					t.Fatalf("entries: %d before, %d after", len(want), len(got))
				}
				for tag, w := range want {
					if !bytes.Equal(got[tag], w) {
						t.Errorf("tag %#04x = %x, want %x", tag, got[tag], w)
					}
				}
			})
		}
	}
}

// TestWrite_Task317_HEIFPrefixIsNotPartOfTheStream pins the TIFF-relative
// offsets of a MakerNote in a HEIF item against the 4-byte
// exif_tiff_header_offset prefix of the item (ISO/IEC 23008-12 Annex A): the
// offsets count from the TIFF header, not from the start of the item.
func TestWrite_Task317_HEIFPrefixIsNotPartOfTheStream(t *testing.T) {
	t.Parallel()
	lay := makerNoteLayouts()[0] // Canon
	tiff, _ := testutil.MakerNoteTIFF(lay.spec)
	src := containerWithEXIF(t, "HEIF", tiff)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.SetCaption("heif prefix")
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		t.Fatal(err)
	}
	after, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := after.RawSegments()
	if len(raw) < 8 || (string(raw[:2]) != "II" && string(raw[:2]) != "MM") {
		t.Fatalf("the EXIF stream of the HEIF item does not start at the TIFF header: % x", raw[:min(8, len(raw))])
	}
	l, _ := after.EXIF.MakerNoteLayout()
	got := testutil.DecodeMN(raw, int(after.EXIF.MakerNoteOffset), l.IFDOffset, l.ByteOrder, true, 0)
	if string(got[2]) != "SERIAL-0123456\x00" {
		t.Errorf("serial = %q, want the value of the source", got[2])
	}
	// A reader that applies the 4-byte prefix as part of the stream would see
	// the footer 4 bytes off.
	foot := raw[int(after.EXIF.MakerNoteOffset)+len(after.EXIF.MakerNote)-8:]
	if off := binary.LittleEndian.Uint32(foot[4:]); off != after.EXIF.MakerNoteOffset {
		t.Errorf("Canon footer offset = %d, want %d (relative to the TIFF header)", off, after.EXIF.MakerNoteOffset)
	}
}

// exiftoolPath returns the exiftool binary, skipping the test when it is absent.
func exiftoolPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool binary absent (install exiftool): the ExifTool oracle check cannot run")
	}
	return p
}

// exiftoolWarnings runs "exiftool -validate -warning -a" over the files and
// returns the warnings per file (keyed by the path given), with the digits
// replaced by N.
func exiftoolWarnings(t *testing.T, bin string, files []string) map[string]map[string]int {
	t.Helper()
	list := filepath.Join(t.TempDir(), "files.txt")
	if err := os.WriteFile(list, []byte(strings.Join(files, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-validate", "-warning", "-a", "-@", list) //nolint:gosec // G204: the binary is the tool this test exists to run
	out, _ := cmd.Output()                                              // exiftool exits 1 when a file cannot be read: the rest is still reported
	digits := regexp.MustCompile(`\d+`)
	res := map[string]map[string]int{}
	var cur string
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "======== "):
			cur = strings.TrimPrefix(line, "======== ")
			res[cur] = map[string]int{}
		case strings.HasPrefix(line, "Warning") && cur != "":
			_, msg, _ := strings.Cut(line, ":")
			res[cur][digits.ReplaceAllString(strings.TrimSpace(msg), "N")]++
		}
	}
	return res
}

// corpusCopy matches the name of a corpus file that the download script saves
// twice more under "<name>_2.<ext>" and "<name>_3.<ext>" (identical files).
var corpusCopy = regexp.MustCompile(`_[0-9]+(\.[A-Za-z0-9]+)$`)

// isCorpusCopy reports whether p is such a copy of a file that exists.
func isCorpusCopy(p string) bool {
	orig := corpusCopy.ReplaceAllString(p, "$1")
	if orig == p {
		return false
	}
	_, err := os.Stat(orig)
	return err == nil
}

// makerNoteWarningClass maps an ExifTool warning about the structure of a
// MakerNote to a class, and returns "" for any other warning. Variants of one
// problem share a class, as ExifTool words it differently with the context
// ("Bad NikonScanIFD directory" and "Bad NikonScanIFD SubDirectory start").
// The file-absolute preview offsets of Olympus MakerNotes
// ("PreviewImageStart+PreviewImageLength runs past end of file") are not
// included: they point into the file, which a write resizes (reported as a
// known limit, see CHANGELOG).
func makerNoteWarningClass(msg string) string {
	for _, c := range []struct{ contains, class string }{
		{"Possibly incorrect maker notes offsets", "offsets"},
		{"Adjusted MakerNotes base", "adjusted base"},
		{"Overlapping MakerNotes values", "overlap"},
		{"Bad offset for MakerNotes", "bad offset"},
		{"Bad PreviewIFD", "PreviewIFD"},
		{"Suspicious PreviewIFD", "PreviewIFD"},
		{"offset for PreviewIFD", "PreviewIFD"},
		{"Bad NikonScanIFD", "NikonScanIFD"},
		{"Invalid Canon", "Canon data"},
	} {
		if strings.Contains(msg, c.contains) {
			return c.class
		}
	}
	return ""
}

// TestCorpus_Task317_NoNewMakerNoteWarning (task #317 AC): for every JPEG, PNG,
// WebP and HEIF/AVIF file of the corpus, the SetCaption output draws no class of
// MakerNote warning from "exiftool -validate -warning" more often than the
// input did. It needs the corpus and the exiftool binary.
func TestCorpus_Task317_NoNewMakerNoteWarning(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "corpus")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("corpus absent (run testdata/download.sh): %s", root)
	}
	bin := exiftoolPath(t)
	outDir := t.TempDir()
	var inputs, outputs []string // the files that carry a MakerNote, and what was written for them
	for _, sub := range []string{"jpeg", "png", "webp", "heif"} {
		_ = filepath.WalkDir(filepath.Join(root, sub), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || isCorpusCopy(p) {
				return nil //nolint:nilerr // a corpus entry that cannot be read, or a copy of another, is outside this check
			}
			src, err := os.ReadFile(p) //nolint:gosec // G122: the corpus is a local, trusted directory
			if err != nil {
				return nil //nolint:nilerr // see above
			}
			m, err := Read(bytes.NewReader(src))
			if err != nil || m.EXIF == nil || len(m.EXIF.MakerNote) == 0 {
				return nil //nolint:nilerr // no MakerNote: nothing to move
			}
			m.SetCaption("corpus")
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				return nil //nolint:nilerr // files the writer refuses are outside this check
			}
			rel, _ := filepath.Rel(root, p)
			dst := filepath.Join(outDir, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, out.Bytes(), 0o644); err != nil { //nolint:gosec // G306: temporary test output
				t.Fatal(err)
			}
			inputs, outputs = append(inputs, p), append(outputs, dst)
			return nil
		})
	}
	if len(inputs) == 0 {
		t.Skipf("corpus holds no image with a MakerNote under %s (run testdata/download.sh)", root)
	}
	in := exiftoolWarnings(t, bin, inputs)
	count := func(w map[string]int) map[string]int {
		c := map[string]int{}
		for msg, n := range w {
			if class := makerNoteWarningClass(msg); class != "" {
				c[class] += n
			}
		}
		return c
	}
	var bad []string
	got := exiftoolWarnings(t, bin, outputs)
	for i, p := range inputs {
		before := count(in[p])
		for class, n := range count(got[outputs[i]]) {
			if n > before[class] {
				bad = append(bad, p+": "+class)
			}
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error("new MakerNote warning: " + b)
	}
}

// corpusWrite reads the corpus file at path (skipping the test when it is
// absent), sets a caption and writes it; it returns the input and the output.
func corpusWrite(t *testing.T, parts ...string) (src, out []byte) {
	t.Helper()
	src = corpusBytes(t, parts...)
	return src, writeCaption(t, src, "task 318")
}

// nikonPreviewImage returns the preview JPEG that the PreviewIFD (tag 0x0011)
// of the Nikon Type 3 MakerNote of e addresses in raw, the TIFF stream e was
// parsed from, or nil.
func nikonPreviewImage(raw []byte, e *exif.EXIF) []byte {
	l, ok := e.MakerNoteLayout()
	if !ok || e.MakerNoteIFD == nil || l.TIFFRelative {
		return nil
	}
	p := e.MakerNoteIFD.Get(0x0011)
	if p == nil || len(p.Value) < 4 {
		return nil
	}
	base := int(e.MakerNoteOffset) + l.Base
	sub := base + int(l.ByteOrder.Uint32(p.Value))
	if sub < 0 || sub+2 > len(raw) {
		return nil
	}
	var off, n int
	for i := range int(l.ByteOrder.Uint16(raw[sub:])) {
		ep := sub + 2 + 12*i
		if ep+12 > len(raw) {
			return nil
		}
		switch l.ByteOrder.Uint16(raw[ep:]) {
		case 0x0201:
			off = int(l.ByteOrder.Uint32(raw[ep+8:]))
		case 0x0202:
			n = int(l.ByteOrder.Uint32(raw[ep+8:]))
		}
	}
	if off <= 0 || n <= 0 || base+off+n > len(raw) {
		return nil
	}
	return raw[base+off : base+off+n]
}

// TestCorpus_Task318_NikonPreviewImageIsCarried (task #318 AC): the preview
// image that the Nikon Coolpix P7000 stores 17 KB past its declared MakerNote
// (JPEG and WebP) is in the output, byte for byte.
func TestCorpus_Task318_NikonPreviewImageIsCarried(t *testing.T) {
	t.Parallel()
	for _, name := range [][]string{
		{"jpeg", "metadata-extractor", "Nikon Coolpix P7000.jpg"},
		{"webp", "metadata-extractor", "Nikon Coolpix P7000.webp"},
	} {
		t.Run("R-22/"+name[0], func(t *testing.T) {
			t.Parallel()
			src, out := corpusWrite(t, name...)
			before, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			after, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			rawBefore, _, _ := before.RawSegments()
			rawAfter, _, _ := after.RawSegments()
			want := nikonPreviewImage(rawBefore, before.EXIF)
			if len(want) == 0 {
				t.Fatal("the source preview image cannot be located: the test cannot judge")
			}
			if got := nikonPreviewImage(rawAfter, after.EXIF); !bytes.Equal(got, want) {
				t.Errorf("preview image after the write is %d bytes, want %d identical bytes", len(got), len(want))
			}
			if len(after.EXIF.MakerNote) <= len(before.EXIF.MakerNote) {
				t.Errorf("the MakerNote did not grow (%d -> %d)", len(before.EXIF.MakerNote), len(after.EXIF.MakerNote))
			}
		})
	}
}

// TestCorpus_Task318_ExifToolReadsTheSameMakerNote: the 49 values that ExifTool
// reads from the MakerNote of the Olympus C-720UZ (stored after the declared
// note) are read identically from the output, and so is the Nikon preview.
func TestCorpus_Task318_ExifToolReadsTheSameMakerNote(t *testing.T) {
	t.Parallel()
	bin := exiftoolPath(t)
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		out, err := exec.Command(bin, args...).Output() //nolint:gosec // G204: the binary is the tool this test exists to run
		if err != nil {
			t.Fatalf("exiftool %v: %v", args, err)
		}
		return string(out)
	}
	write := func(t *testing.T, data []byte, ext string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "in"+ext)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("R-21/Olympus C-720UZ", func(t *testing.T) {
		t.Parallel()
		src, out := corpusWrite(t, "jpeg", "metadata-extractor", "Olympus C720UZ.jpg")
		a := run(t, "-G1", "-a", "-s", "-MakerNotes:all", write(t, src, ".jpg"))
		b := run(t, "-G1", "-a", "-s", "-MakerNotes:all", write(t, out, ".jpg"))
		if strings.Count(a, "\n") < 40 {
			t.Fatalf("the source shows %d MakerNote lines, want about 49", strings.Count(a, "\n"))
		}
		if a != b {
			t.Errorf("ExifTool reads different MakerNote values from the output:\n--- input\n%s\n--- output\n%s", a, b)
		}
	})
	for _, name := range [][]string{
		{"jpeg", "metadata-extractor", "Nikon Coolpix P7000.jpg"},
		{"webp", "metadata-extractor", "Nikon Coolpix P7000.webp"},
	} {
		t.Run("R-22/preview/"+name[0], func(t *testing.T) {
			t.Parallel()
			src, out := corpusWrite(t, name...)
			ext := filepath.Ext(name[2])
			a := run(t, "-b", "-PreviewImage", write(t, src, ext))
			b := run(t, "-b", "-PreviewImage", write(t, out, ext))
			if len(a) < 1000 || a != b {
				t.Errorf("preview image: %d bytes before, %d after (want identical and non-empty)", len(a), len(b))
			}
		})
	}
}
