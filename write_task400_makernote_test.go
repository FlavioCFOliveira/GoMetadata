package gometadata

// write_task400_makernote_test.go — task #400 acceptance: after a write that
// moves the EXIF (SetCaption with a long value, SetISO), the Olympus MakerNote
// values that address data outside the EXIF stream (exif.ExternalOffsets) still
// address the same bytes, and ExifTool reads the same MakerNote values from the
// output as from the source.

import (
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// t400Modify reads src, moves its EXIF (a long caption grows the EXIF, the IPTC
// and the XMP; ISO changes the EXIF) and writes the result.
func t400Modify(t *testing.T, name string, src []byte) (m *Metadata, out []byte) {
	t.Helper()
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("%s: Read: %v", name, err)
	}
	m.SetCaption(strings.Repeat("a long caption ", 200))
	m.SetISO(1600)
	var buf bytes.Buffer
	if err := Write(bytes.NewReader(src), &buf, m); err != nil {
		t.Fatalf("%s: Write: %v", name, err)
	}
	return m, buf.Bytes()
}

// t400TIFFPos returns the position in a JPEG of the TIFF header of its first
// EXIF APP1 segment (JPEG ISO/IEC 10918-1 §B.1.1.2, Exif 3.1 §4.5.4: the
// segment holds "Exif\0\0" and then the TIFF header), or -1.
func t400TIFFPos(jpg []byte) int {
	i := bytes.Index(jpg[:min(len(jpg), 1<<20)], []byte("Exif\x00\x00"))
	if i < 0 {
		return -1
	}
	return i + 6
}

// t400ExifToolLines returns the lines of `exiftool -a -G1 -s -n args... path`.
func t400ExifToolLines(t *testing.T, bin, path string, args ...string) []string {
	t.Helper()
	argv := append([]string{"-a", "-G1", "-s", "-n"}, args...)
	argv = append(argv, path)
	out, _ := exec.Command(bin, argv...).Output() //nolint:gosec // G204: the binary is the oracle this test exists to run; a non-zero status means a warning
	var lines []string
	for l := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestTask400_MakerNoteOffsetsAfterWrite: for every corpus JPEG whose MakerNote
// has external offsets, the bytes each offset addresses are the same in the
// output as in the source, and the output reads as the source does in ExifTool.
func TestTask400_MakerNoteOffsetsAfterWrite(t *testing.T) {
	t.Parallel()
	files := append(corpusFiles(t, "jpeg", ".jpg", ".jpeg", ".jpe"), "testdata/fixtures/Canon_40D.jpg")
	bin, lerr := exec.LookPath("exiftool")

	var withOffsets, checkedBytes, outside, oracle, previewDiffs int
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m0, err := Read(bytes.NewReader(src))
		if err != nil || m0.rawEXIF == nil {
			continue
		}
		offs := exif.ExternalOffsets(m0.rawEXIF)
		if len(offs) == 0 {
			continue
		}
		withOffsets++
		_, out := t400Modify(t, f, src)
		if bytes.Equal(out, src) {
			t.Errorf("%s: the write did not change the file", f)
			continue
		}
		m2, err := Read(bytes.NewReader(out))
		if err != nil {
			t.Errorf("%s: Read of the output: %v", f, err)
			continue
		}
		offs2 := exif.ExternalOffsets(m2.rawEXIF)
		if len(offs2) != len(offs) {
			t.Errorf("%s: %d external offsets in the source, %d in the output", f, len(offs), len(offs2))
			continue
		}
		srcTIFF, outTIFF := t400TIFFPos(src), t400TIFFPos(out)
		for i, o := range offs {
			o2 := offs2[i]
			if o.Length != o2.Length {
				t.Errorf("%s: offset %d: length %d, was %d", f, i, o2.Length, o.Length)
			}
			a, b := int64(srcTIFF)+o.Target(), int64(outTIFF)+o2.Target()
			if a < 0 || a+int64(o.Length) > int64(len(src)) {
				// The source's offset points outside the file: nothing to follow, and
				// the writer leaves the value alone (format/jpeg/externaloffsets.go).
				outside++
				if o.Value != o2.Value {
					t.Errorf("%s: offset %d points outside the source and was rewritten from %d to %d", f, i, o.Value, o2.Value)
				}
				continue
			}
			if b < 0 || b+int64(o.Length) > int64(len(out)) {
				t.Errorf("%s: offset %d now points outside the file (%d+%d > %d)", f, i, b, o.Length, len(out))
				continue
			}
			if sha256.Sum256(src[a:a+int64(o.Length)]) != sha256.Sum256(out[b:b+int64(o.Length)]) {
				t.Errorf("%s: offset %d addresses other bytes in the output (source %d, output %d, %d bytes)", f, i, a, b, o.Length)
			}
			checkedBytes++
		}

		if lerr != nil {
			continue
		}
		dir := t.TempDir()
		sp, op := filepath.Join(dir, "src.jpg"), filepath.Join(dir, "out.jpg")
		if err := os.WriteFile(sp, src, 0o600); err != nil { //nolint:gosec // G703: sp is a file name under t.TempDir()
			t.Fatal(err)
		}
		if err := os.WriteFile(op, out, 0o600); err != nil { //nolint:gosec // G703: op is a file name under t.TempDir()
			t.Fatal(err)
		}
		oracle++
		sl := t400ExifToolLines(t, bin, sp, "-MakerNotes:all")
		ol := t400ExifToolLines(t, bin, op, "-MakerNotes:all")
		if len(sl) != len(ol) {
			t.Errorf("%s: ExifTool reads %d MakerNote values from the source, %d from the output", f, len(sl), len(ol))
			continue
		}
		for i := range sl {
			if sl[i] == ol[i] {
				continue
			}
			// The preview start is counted from the MakerNote, which moved; the
			// check above proves it addresses the same bytes.
			if strings.Contains(sl[i], " PreviewImageStart ") && strings.Contains(ol[i], " PreviewImageStart ") {
				previewDiffs++
				continue
			}
			t.Errorf("%s: MakerNote value differs:\n source %s\n output %s", f, sl[i], ol[i])
		}
		// The previews and the thumbnail are the same bytes; the output holds no
		// warning the source lacks (it may hold fewer: the write fixes odd offsets).
		for _, tag := range []string{"-PreviewImage", "-ThumbnailImage"} {
			sb, _ := exec.Command(bin, "-b", tag, sp).Output() //nolint:gosec // G204: oracle binary
			ob, _ := exec.Command(bin, "-b", tag, op).Output() //nolint:gosec // G204: oracle binary
			if !bytes.Equal(sb, ob) {
				t.Errorf("%s: %s differs (%d bytes in the source, %d in the output)", f, tag, len(sb), len(ob))
			}
		}
		warn := func(p string) map[string]bool {
			set := map[string]bool{}
			for _, l := range t400ExifToolLines(t, bin, p, "-validate", "-Warning") {
				if strings.Contains(l, "Warning") {
					_, v, _ := strings.Cut(l, ":")
					set[strings.TrimSpace(v)] = true
				}
			}
			return set
		}
		sw := warn(sp)
		var extra []string
		for w := range warn(op) {
			if !sw[w] {
				extra = append(extra, w)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			t.Errorf("%s: warnings that the source does not have: %v", f, extra)
		}
	}
	if withOffsets == 0 {
		t.Fatal("no JPEG with MakerNote external offsets found")
	}
	t.Logf("%d JPEGs with external offsets; %d offsets checked byte for byte, %d point outside the source file; ExifTool compared %d files (%d PreviewImageStart values moved with the MakerNote)",
		withOffsets, checkedBytes, outside, oracle, previewDiffs)
	if checkedBytes == 0 {
		t.Error("no offset addressed bytes inside the file")
	}
}
