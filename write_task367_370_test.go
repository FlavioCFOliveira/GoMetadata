package gometadata

// write_task367_370_test.go — tasks #368 (IFD1 thumbnail strips), #369
// (Samsung MakerNote read by ExifTool) and #370 (Olympus preview offsets) through
// the public API. Task #367 (HEIF trailing bytes) is tested in format/heif.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
)

// thumbnailStrips returns the strips of the thumbnail of IFD1 of m.
func thumbnailStrips(m *Metadata) [][]byte {
	if m == nil || m.EXIF == nil || m.EXIF.IFD0 == nil || m.EXIF.IFD0.Next == nil {
		return nil
	}
	if m.EXIF.IFD0.Next.Sub == nil {
		return nil
	}
	return m.EXIF.IFD0.Next.Sub.ThumbnailStrips
}

// TestWrite_Task368_KodakProBackThumbnailStrips (task #368, S-50): the IFD1 of
// the Kodak ProBack is an uncompressed thumbnail (156x111 RGB, one strip of
// 51948 bytes); a caption write keeps the strip data, byte for byte, with a
// StripOffsets that addresses it, and draws no new warning.
func TestWrite_Task368_KodakProBackThumbnailStrips(t *testing.T) {
	t.Parallel()
	t.Run("S-50", func(t *testing.T) {
		t.Parallel()
		src, out := corpusWrite(t, "jpeg", "metadata-extractor", "Kodak ProBack.jpg")
		m0, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		m1, err := Read(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		want, got := thumbnailStrips(m0), thumbnailStrips(m1)
		if len(want) != 1 || len(want[0]) != 51948 {
			t.Fatalf("fixture: strips of the source = %d, want one of 51948 bytes", len(want))
		}
		if len(got) != 1 || !bytes.Equal(got[0], want[0]) {
			t.Fatalf("the strip of the output differs (%d strips)", len(got))
		}
		// StripOffsets addresses the strip in the EXIF stream of the output.
		raw := extractRawEXIF(t, out)
		off := m1.EXIF.IFD0.Next.Get(exif.TagStripOffsets)
		if off == nil || off.Count != 1 {
			t.Fatal("StripOffsets is missing")
		}
		at := m1.EXIF.IFD0.Next.Get(exif.TagStripOffsets).Uint32()
		if int(at)+len(want[0]) > len(raw) || !bytes.Equal(raw[at:int(at)+len(want[0])], want[0]) {
			t.Error("StripOffsets does not address the strip")
		}
		for _, w := range m1.ParseWarnings {
			if strings.Contains(w.Error(), "thumbnail") {
				t.Errorf("warning about the thumbnail: %v", w)
			}
		}
		if len(raw) < 50000 {
			t.Errorf("the EXIF stream is %d bytes: the thumbnail is missing", len(raw))
		}
		if bin, lerr := exec.LookPath("exiftool"); lerr == nil {
			dir := t.TempDir()
			writeTemp(t, dir+"/in.jpg", src)
			writeTemp(t, dir+"/out.jpg", out)
			w := exiftoolWarnings(t, bin, []string{dir + "/in.jpg", dir + "/out.jpg"})
			for msg := range w[dir+"/out.jpg"] {
				if _, ok := w[dir+"/in.jpg"][msg]; !ok {
					t.Errorf("new warning of ExifTool: %s", msg)
				}
			}
		}
	})
}

// TestWrite_Task368_StripsOfEveryContainer: the strips of an IFD1 thumbnail of a
// JPEG survive a write that modifies the EXIF, and a TIFF is untouched by the
// rule (its image data is relocated by the TIFF writer, which carries the strips
// of every IFD itself: they are not read into the IFD).
func TestWrite_Task368_StripsOfEveryContainer(t *testing.T) {
	t.Parallel()
	strips := [][]byte{bytes.Repeat([]byte{1}, 31), bytes.Repeat([]byte{2}, 8), bytes.Repeat([]byte{3}, 5)}
	le := func(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
	counts := bytes.Join([][]byte{le(31), le(8), le(5)}, nil)
	entries := []exif.IFDEntry{
		{Tag: exif.TagCompression, Type: exif.TypeShort, Count: 1, Value: []byte{1, 0}},
		{Tag: exif.TagStripOffsets, Type: exif.TypeLong, Count: 3, Value: make([]byte, 12)},
		{Tag: exif.TagStripByteCounts, Type: exif.TypeLong, Count: 3, Value: counts},
	}
	e := exif.New(true)
	e.IFD0.Next = &exif.IFD{Entries: entries, Sub: &exif.SubIFDs{ThumbnailStrips: strips}}
	raw, err := exif.Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("S-50/JPEG", func(t *testing.T) {
		t.Parallel()
		var src bytes.Buffer
		if err := jpeg.Inject(bytes.NewReader(buildMinimalJPEG(nil)), &src, raw, nil, nil, true); err != nil {
			t.Fatal(err)
		}
		m, err := Read(bytes.NewReader(src.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if got := thumbnailStrips(m); len(got) != 3 || !bytes.Equal(got[0], strips[0]) || !bytes.Equal(got[2], strips[2]) {
			t.Fatalf("Read: strips = %d, want the three of the file", len(got))
		}
		m.SetCaption("a caption that grows IFD0")
		var out bytes.Buffer
		if err := Write(bytes.NewReader(src.Bytes()), &out, m); err != nil {
			t.Fatal(err)
		}
		m2, err := Read(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		got := thumbnailStrips(m2)
		if len(got) != 3 {
			t.Fatalf("after the write: %d strips", len(got))
		}
		for i := range strips {
			if !bytes.Equal(got[i], strips[i]) {
				t.Errorf("strip %d differs after the write", i)
			}
		}
	})
	t.Run("S-50/TIFF is not read", func(t *testing.T) {
		t.Parallel()
		// IFD0 and IFD1, each with one strip, as a two-page TIFF.
		le := binary.LittleEndian
		b := make([]byte, 8+2+3*12+4+2+3*12+4+16)
		copy(b, "II*\x00")
		le.PutUint32(b[4:], 8)
		ifd := func(at, next, strip uint32) {
			le.PutUint16(b[at:], 3)
			for i, ent := range [...][4]uint32{{0x0103, 3, 1, 1}, {0x0111, 4, 1, strip}, {0x0117, 4, 1, 8}} {
				p := at + 2 + uint32(i)*12 //nolint:gosec // G115: tiny test offsets
				le.PutUint16(b[p:], uint16(ent[0]))
				le.PutUint16(b[p+2:], uint16(ent[1])) //nolint:gosec // G115: tiny test values
				le.PutUint32(b[p+4:], ent[2])
				le.PutUint32(b[p+8:], ent[3])
			}
			le.PutUint32(b[at+2+36:], next)
		}
		ifd1 := uint32(8 + 2 + 36 + 4)
		strip0 := ifd1 + 2 + 36 + 4
		ifd(8, ifd1, strip0)
		ifd(ifd1, 0, strip0+8)
		copy(b[strip0:], "STRIP-00")
		copy(b[strip0+8:], "STRIP-11")
		m, err := Read(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if got := thumbnailStrips(m); got != nil {
			t.Fatalf("a TIFF read its IFD1 strips into the model: %d strips", len(got))
		}
		m.SetCaption("tiff caption")
		var out bytes.Buffer
		if err := Write(bytes.NewReader(b), &out, m); err != nil {
			t.Fatal(err)
		}
		if bytes.Count(out.Bytes(), []byte("STRIP-11")) != 1 || bytes.Count(out.Bytes(), []byte("STRIP-00")) != 1 {
			t.Errorf("the image data of the TIFF is not written once: %d %d", bytes.Count(out.Bytes(), []byte("STRIP-00")), bytes.Count(out.Bytes(), []byte("STRIP-11")))
		}
	})
}

// TestWrite_Task368_ThumbnailTooLargeForTheSegment (S-50): a JPEG whose EXIF
// stream fills the APP1 segment with an uncompressed thumbnail cannot take more
// metadata: the write fails with ErrEXIFPayloadTooLarge (the rule of an EXIF stream
// that does not fit one segment, jpeg.Inject) instead of dropping the thumbnail
// as it did before the strips were carried.
func TestWrite_Task368_ThumbnailTooLargeForTheSegment(t *testing.T) {
	t.Parallel()
	t.Run("S-50", func(t *testing.T) {
		t.Parallel()
		const n = 65400
		e := exif.New(true)
		e.IFD0.Next = &exif.IFD{Entries: []exif.IFDEntry{
			{Tag: exif.TagCompression, Type: exif.TypeShort, Count: 1, Value: []byte{1, 0}},
			{Tag: exif.TagStripOffsets, Type: exif.TypeLong, Count: 1, Value: make([]byte, 4)},
			{Tag: exif.TagStripByteCounts, Type: exif.TypeLong, Count: 1, Value: binary.LittleEndian.AppendUint32(nil, n)},
		}, Sub: &exif.SubIFDs{ThumbnailStrips: [][]byte{bytes.Repeat([]byte{7}, n)}}}
		raw, err := exif.Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		var src bytes.Buffer
		if err := jpeg.Inject(bytes.NewReader(buildMinimalJPEG(nil)), &src, raw, nil, nil, true); err != nil {
			t.Fatalf("fixture: %v", err)
		}
		m, err := Read(bytes.NewReader(src.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		m.SetCaption(strings.Repeat("a caption that does not fit next to the thumbnail ", 4))
		var out bytes.Buffer
		if err := Write(bytes.NewReader(src.Bytes()), &out, m); !errors.Is(err, jpeg.ErrEXIFPayloadTooLarge) {
			t.Errorf("Write = %v, want ErrEXIFPayloadTooLarge", err)
		}
	})
}

// ifd0TagOrder returns the tags of IFD0 in the order the EXIF stream stores them.
func ifd0TagOrder(raw []byte) []uint16 {
	var order binary.ByteOrder = binary.LittleEndian
	if raw[0] == 'M' {
		order = binary.BigEndian
	}
	at := int(order.Uint32(raw[4:]))
	n := int(order.Uint16(raw[at:]))
	tags := make([]uint16, n)
	for i := range n {
		tags[i] = order.Uint16(raw[at+2+12*i:])
	}
	return tags
}

// mnEntryCount reads the number of entries ExifTool finds in the MakerNote
// directory of path ("MakerNotes directory with N entries" of -v3).
func mnEntryCount(t *testing.T, bin, path string) int {
	t.Helper()
	out := exiftoolOut(t, bin, "-v3", path)
	m := regexp.MustCompile(`MakerNotes directory with (\d+) entries`).FindSubmatch(out)
	if m == nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(string(m[1]), "%d", &n)
	return n
}

// TestWrite_Task369_SamsungNote4MakerNote (task #369, R-35): the MakerNote of the
// Galaxy Note 4 is byte-identical after a caption write, with the same entries.
// "exiftool -G1 -a -MakerNotes:all" of the output lists three more values than
// the input's, and the cause is the order of the entries of IFD0 in the SOURCE:
// the camera writes IFD0 out of tag order (TIFF 6.0 §2, p. 15: ascending by
// tag), so Make (0x010F) comes after the Exif IFD that holds the MakerNote, and
// ExifTool, which decides on the MakerNote format from the Make it has read
// ("uc $$self{Make} eq 'SAMSUNG'", MakerNotes.pm MakerNoteSamsung2), treats the
// note as unknown. The output has IFD0 in tag order, so Make is read first and
// the same 98 bytes decode as Samsung Type2. The write corrupts nothing.
func TestWrite_Task369_SamsungNote4MakerNote(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Samsung SM-N910F (Galaxy Note 4).jpg", "Samsung SM-N910F (Galaxy Note 4)_2.jpg"} {
		t.Run("R-35/"+name, func(t *testing.T) {
			t.Parallel()
			src, out := corpusWrite(t, "jpeg", "metadata-extractor", name)
			m0, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			m1, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if len(m0.EXIF.MakerNote) == 0 || !bytes.Equal(m0.EXIF.MakerNote, m1.EXIF.MakerNote) {
				t.Errorf("the MakerNote differs: %d bytes before, %d after", len(m0.EXIF.MakerNote), len(m1.EXIF.MakerNote))
			}
			if a, b := len(m0.EXIF.MakerNoteIFD.Entries), len(m1.EXIF.MakerNoteIFD.Entries); a != b || a != 7 {
				t.Errorf("MakerNote entries: %d before, %d after, want 7", a, b)
			}
			srcOrder, outOrder := ifd0TagOrder(extractRawEXIF(t, src)), ifd0TagOrder(extractRawEXIF(t, out))
			sorted := func(tags []uint16) bool {
				for i := 1; i < len(tags); i++ {
					if tags[i] <= tags[i-1] {
						return false
					}
				}
				return true
			}
			if sorted(srcOrder) {
				t.Error("fixture: IFD0 of the source is in tag order: the cause of the report is gone")
			}
			if !sorted(outOrder) {
				t.Errorf("IFD0 of the output is not in tag order: %v", outOrder)
			}
			bin, lerr := exec.LookPath("exiftool")
			if lerr != nil {
				return
			}
			dir := t.TempDir()
			writeTemp(t, dir+"/in.jpg", src)
			writeTemp(t, dir+"/out.jpg", out)
			if a, b := mnEntryCount(t, bin, dir+"/in.jpg"), mnEntryCount(t, bin, dir+"/out.jpg"); a != b || a != 7 {
				t.Errorf("ExifTool reads %d MakerNote entries of the input and %d of the output, want 7", a, b)
			}
		})
	}
}

// olympusData returns the bytes that the Olympus preview offset of the EXIF
// stream of file addresses, with the offset, or nil when the file has none or the
// data is outside the file. tiffAt is where the TIFF header is in file.
func olympusData(file []byte, raw []byte, tiffAt int, opts ...exif.ParseOption) (data []byte, o exif.ExternalOffset, ok bool) {
	offs := exif.ExternalOffsets(raw, opts...)
	if len(offs) != 1 {
		return nil, exif.ExternalOffset{}, false
	}
	o = offs[0]
	at := int64(tiffAt) + o.Target()
	if at < 0 || at+int64(o.Length) > int64(len(file)) {
		return nil, o, true
	}
	return file[at : at+int64(o.Length)], o, true
}

// TestWrite_Task370_OlympusJPEGCorpus (task #370, R-31, R-33): every corpus JPEG
// with an Olympus preview offset keeps it addressing the same bytes after a
// caption write, whatever the form of the offset (the E-1 stores it inside the
// MakerNote, counted from the TIFF header). An offset that already addressed
// bytes outside the file (the truncated E-20) is left as it was.
func TestWrite_Task370_OlympusJPEGCorpus(t *testing.T) {
	t.Parallel()
	files := make([][]string, 0, 3+len(olympusFiles))
	files = append(files,
		[]string{"jpeg", "exiftool", "OlympusE1.jpg"},
		[]string{"jpeg", "exif-samples", "corrupted.jpg"},
		[]string{"jpeg", "metadata-extractor", "Olympus E-20.jpg"})
	for _, n := range olympusFiles {
		files = append(files, []string{"jpeg", "metadata-extractor", n})
	}
	for _, f := range files {
		t.Run("R-31/"+f[len(f)-1], func(t *testing.T) {
			t.Parallel()
			src, out := corpusWrite(t, f...)
			rawSrc, rawOut := extractRawEXIF(t, src), extractRawEXIF(t, out)
			tiffSrc, tiffOut := bytes.Index(src, []byte("Exif\x00\x00"))+6, bytes.Index(out, []byte("Exif\x00\x00"))+6
			want, o0, ok := olympusData(src, rawSrc, tiffSrc)
			if !ok {
				t.Fatal("the source has no Olympus preview offset")
			}
			got, o1, ok := olympusData(out, rawOut, tiffOut)
			if !ok {
				t.Fatal("the output has no Olympus preview offset")
			}
			if want == nil {
				// R-33: the data is outside the file: the value is left as it was.
				if o1.Value != o0.Value {
					t.Errorf("the offset of the output is %d, want it left at %d", o1.Value, o0.Value)
				}
				return
			}
			if !bytes.Equal(got, want) {
				t.Errorf("the offset of the output addresses %d bytes that are not the %d of the source", len(got), len(want))
			}
			if len(out) == len(src) {
				t.Error("the write changed nothing: the test proves nothing")
			}
		})
	}
}

// TestWrite_Task370_OlympusORFCorpus (task #370, R-34): the ORF files of the
// corpus keep the bytes their MakerNote preview offset addresses after a write
// that grows IFD0 (a caption, GPS and keywords), and read no new warning for the
// preview from ExifTool.
func TestWrite_Task370_OlympusORFCorpus(t *testing.T) {
	t.Parallel()
	names := []string{
		"Olympus E-M5.orf", "Olympus E-M10.orf", "Olympus E410.orf", "Olympus E600.orf", "Olympus E30.orf", "Olympus XZ1.orf",
		"Olympus PEN E-P3.orf", "Olympus Pen E-PL1.orf", "Olympus E-PL7.orf", "Olympus E-PM1.orf", "Olympus TG-4.orf", "Olympus TG-6.ORF",
		"OM System TG-7.ORF", "OM Sytem OM-1.orf", //nolint:misspell // the name of the corpus file
		"Olympus C5050Z.orf", "Olympus C8080.orf", "Olympus SP350.orf", "Olympus SP500UZ.orf",
	}
	for _, name := range names {
		t.Run("R-34/"+name, func(t *testing.T) {
			t.Parallel()
			src := corpusBytes(t, "raw", "metadata-extractor", name)
			magic := exif.AcceptRAWMagic(binary.LittleEndian.Uint16(src[2:4]))
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			m.SetCaption("orf caption that grows IFD0")
			m.SetGPS(38.7, -9.1)
			m.SetKeywords([]string{"alpha", "beta"})
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Fatal(err)
			}
			want, _, ok := olympusData(src, src, 0, magic)
			if !ok {
				return // an old body without such an offset
			}
			got, _, ok := olympusData(out.Bytes(), out.Bytes(), 0, magic)
			if !ok || !bytes.Equal(got, want) {
				t.Errorf("the preview offset of the output addresses %d bytes that differ from the source's %d (offset found: %v)", len(got), len(want), ok)
			}
		})
	}
}

// makerNoteValues returns the lines of "exiftool -G1 -a -s -MakerNotes:all" of
// path without the tags that hold a position or a size, which a write changes
// on purpose.
func makerNoteValues(t *testing.T, bin, path string) []string {
	t.Helper()
	skip := regexp.MustCompile(`(?i)(Offset|Start|Position|Length|PreviewImage|ThumbnailImage|Data|Thumbnail)`)
	var keep []string
	for line := range strings.SplitSeq(string(exiftoolOut(t, bin, "-G1", "-a", "-s", "-MakerNotes:all", path)), "\n") {
		if line != "" && !skip.MatchString(strings.SplitN(line, ":", 3)[1]) {
			keep = append(keep, line)
		}
	}
	return keep
}

// TestWrite_Task370_OlympusORFSubIFDValues (task #370, R-36): the Olympus
// compacts of the corpus whose MakerNote ("OLYMP\0", offsets counted from the
// TIFF header) keeps its sub-directories as values read the same MakerNote values
// after a write; before, the pointers inside the sub-directories (ColorMatrix2,
// CoringValues, the settings) addressed the old position.
func TestWrite_Task370_OlympusORFSubIFDValues(t *testing.T) {
	t.Parallel()
	bin := exiftoolPath(t)
	for _, name := range []string{"Olympus SP350.orf", "Olympus SP500UZ.orf", "Olympus C8080.orf"} {
		t.Run("R-36/"+name, func(t *testing.T) {
			t.Parallel()
			src := corpusBytes(t, "raw", "metadata-extractor", name)
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			m.SetCaption("orf caption that grows IFD0")
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			writeTemp(t, dir+"/in.orf", src)
			writeTemp(t, dir+"/out.orf", out.Bytes())
			want, got := makerNoteValues(t, bin, dir+"/in.orf"), makerNoteValues(t, bin, dir+"/out.orf")
			if len(want) < 20 {
				t.Fatalf("fixture: ExifTool reads %d MakerNote values of the source", len(want))
			}
			if strings.Join(want, "\n") != strings.Join(got, "\n") {
				for i := range min(len(want), len(got)) {
					if want[i] != got[i] {
						t.Errorf("first difference:\ninput  %s\noutput %s", want[i], got[i])
						break
					}
				}
				t.Errorf("%d MakerNote values before the write, %d after", len(want), len(got))
			}
		})
	}
}
