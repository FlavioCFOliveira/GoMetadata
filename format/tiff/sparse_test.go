package tiff

// sparse_test.go — task #389 (sprint 50): the compacted read of a TIFF whose
// IFDs lie among the image data. Every case that is compacted is compared with
// the whole-file parse (exif.Parse of the file), the oracle: the same IFDs,
// entries, values and warnings.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// sparseTestParams compacts any file of at least a few bytes.
var sparseTestParams = sparseParams{minFileSize: 0, trigger: 1 << 10, minSaving: 1 << 10, always: true} //nolint:gochecknoglobals // read-only test thresholds

// at converts a test offset to the uint32 of a TIFF field.
func at(v int) uint32 { return uint32(v) } //nolint:gosec // G115: test offsets are small

// tb builds a classic TIFF of a fixed size by writing fields at offsets.
type tb struct {
	b []byte
	o binary.ByteOrder
}

func newTB(o binary.ByteOrder, size, ifd0 int) *tb {
	t := &tb{b: make([]byte, size), o: o}
	if o == binary.BigEndian {
		t.b[0], t.b[1] = 'M', 'M'
	} else {
		t.b[0], t.b[1] = 'I', 'I'
	}
	o.PutUint16(t.b[2:], 0x002A)
	o.PutUint32(t.b[4:], at(ifd0))
	return t
}

type ent struct {
	tag, typ uint16
	count    uint32
	val      uint32 // the value or offset field
}

// ifd writes a directory at off.
func (t *tb) ifd(off int, next int, es ...ent) {
	t.o.PutUint16(t.b[off:], uint16(len(es))) //nolint:gosec // G115: test sizes
	p := off + 2
	for _, e := range es {
		t.o.PutUint16(t.b[p:], e.tag)
		t.o.PutUint16(t.b[p+2:], e.typ)
		t.o.PutUint32(t.b[p+4:], e.count)
		t.o.PutUint32(t.b[p+8:], e.val)
		p += 12
	}
	t.o.PutUint32(t.b[p:], at(next))
}

func (t *tb) put(off int, v []byte) { copy(t.b[off:], v) }

// fill writes a recognisable pattern (image data) over [from, to).
func (t *tb) fill(from, to int) {
	for i := from; i < to; i++ {
		t.b[i] = byte(i*7 + 3) //nolint:gosec // G115: a byte pattern, truncation intended
	}
}

// ifdEqualErr compares two IFD chains the way a caller sees them (the exported
// fields); the unexported out-of-line offset of an entry is the one thing that
// differs, and it is not observable except as MakerNoteOffset, which is compared.
func ifdEqualErr(a, b *exif.IFD, path string) error {
	for ; a != nil || b != nil; a, b = nextOf(a), nextOf(b) {
		if (a == nil) != (b == nil) {
			return fmt.Errorf("%s: chain length differs", path)
		}
		if len(a.Entries) != len(b.Entries) {
			return fmt.Errorf("%s: %d entries, want %d", path, len(a.Entries), len(b.Entries))
		}
		for i := range a.Entries {
			x, y := a.Entries[i], b.Entries[i]
			if x.Tag != y.Tag || x.Type != y.Type || x.Count != y.Count || !bytes.Equal(x.Value, y.Value) {
				return fmt.Errorf("%s: entry %d (tag 0x%04X) differs: %v vs %v", path, i, x.Tag, x, y)
			}
		}
		if !bytes.Equal(a.ThumbnailData, b.ThumbnailData) || (a.ThumbnailData == nil) != (b.ThumbnailData == nil) {
			return fmt.Errorf("%s: thumbnail differs", path)
		}
		if (a.Sub == nil) != (b.Sub == nil) {
			return fmt.Errorf("%s: chain sub-IFDs differ", path)
		}
		if a.Sub != nil {
			for _, p := range []struct {
				name string
				x, y *exif.IFD
			}{{"exif", a.Sub.Exif, b.Sub.Exif}, {"gps", a.Sub.GPS, b.Sub.GPS}, {"interop", a.Sub.Interop, b.Sub.Interop}} {
				if err := ifdEqualErr(p.x, p.y, path+"/sub-"+p.name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func nextOf(i *exif.IFD) *exif.IFD {
	if i == nil {
		return nil
	}
	return i.Next
}

// exifEqualErr reports the first difference between two parsed EXIF structures.
func exifEqualErr(a, b *exif.EXIF) error {
	if a == nil || b == nil {
		if a != b {
			return errors.New("one EXIF is nil")
		}
		return nil
	}
	if a.ByteOrder != b.ByteOrder || a.MakerNoteOffset != b.MakerNoteOffset || !bytes.Equal(a.MakerNote, b.MakerNote) {
		return errors.New("byte order, MakerNote or MakerNoteOffset differs")
	}
	if fmt.Sprint(a.Warnings) != fmt.Sprint(b.Warnings) {
		return fmt.Errorf("warnings differ: %v vs %v", a.Warnings, b.Warnings)
	}
	for _, p := range []struct {
		name string
		x, y *exif.IFD
	}{{"IFD0", a.IFD0, b.IFD0}, {"Exif", a.ExifIFD, b.ExifIFD}, {"GPS", a.GPSIFD, b.GPSIFD}, {"Interop", a.InteropIFD, b.InteropIFD}, {"MakerNoteIFD", a.MakerNoteIFD, b.MakerNoteIFD}} {
		if err := ifdEqualErr(p.x, p.y, p.name); err != nil {
			return err
		}
	}
	return nil
}

// checkCompact compacts data and compares it with the whole-file parse. It
// returns the Sparse (nil when the file is not compacted).
func checkCompact(t *testing.T, data []byte, wantCompact bool) *Sparse {
	t.Helper()
	raw, iptc, xmp, sp, err := extractCompactWith(bytes.NewReader(data), sparseTestParams)
	if err != nil {
		t.Fatalf("extractCompact: %v", err)
	}
	if (sp != nil) != wantCompact {
		t.Fatalf("compacted = %v, want %v", sp != nil, wantCompact)
	}
	wRaw, wIPTC, wXMP, werr := Extract(bytes.NewReader(data))
	if werr != nil {
		t.Fatalf("Extract: %v", werr)
	}
	if sp == nil {
		if !bytes.Equal(raw, wRaw) || !bytes.Equal(iptc, wIPTC) || !bytes.Equal(xmp, wXMP) {
			t.Fatal("a file that is not compacted must read as Extract reads it")
		}
		return nil
	}
	if !bytes.Equal(iptc, wIPTC) || !bytes.Equal(xmp, wXMP) {
		t.Errorf("IPTC/XMP differ from Extract: %q/%q vs %q/%q", iptc, xmp, wIPTC, wXMP)
	}
	got, perr := exif.Parse(sp.Stream(), exif.AliasThumbnail())
	sp.Restore()
	want, werr2 := exif.Parse(data, exif.AliasThumbnail())
	if (perr == nil) != (werr2 == nil) {
		t.Fatalf("parse errors differ: stream %v, file %v", perr, werr2)
	}
	if perr == nil {
		if err := exifEqualErr(got, want); err != nil {
			t.Errorf("stream parse differs from file parse: %v", err)
		}
	}
	if !bytes.Equal(sp.Head(), data[:len(sp.Head())]) {
		t.Error("Head after Restore is not a prefix of the file")
	}
	if len(sp.Stream()) >= len(data) {
		t.Errorf("stream %d bytes is not smaller than the file (%d)", len(sp.Stream()), len(data))
	}
	return sp
}

const (
	asciiLen = 12
)

// trailingExif builds a file of size bytes: IFD0 (Make, Model, ExifIFD pointer,
// strip offsets) at 8, image data, then the Exif IFD (with a MakerNote when mn)
// and the GPS IFD after it, the layout of a DNG whose Exif IFD follows the image.
func trailingExif(o binary.ByteOrder, size int, mn bool) []byte {
	exifOff, gpsOff := size-700, size-300
	t := newTB(o, size, 8)
	t.fill(300, exifOff-16)
	t.ifd(8, 0,
		ent{0x010F, 2, asciiLen, 200},
		ent{0x0110, 2, asciiLen, 220},
		ent{0x0111, 4, 1, 300},
		ent{0x8769, 4, 1, at(exifOff)},
		ent{0x8825, 4, 1, at(gpsOff)},
	)
	t.put(200, []byte("Make-Maker\x00\x00"))
	t.put(220, []byte("Model-X-123\x00"))
	exifEntries := []ent{
		{0x829A, 5, 1, at(exifOff + 200)},
		{0x9003, 2, 20, at(exifOff + 220)},
	}
	if mn {
		exifEntries = append(exifEntries, ent{0x927C, 7, 24, at(exifOff + 260)})
	}
	t.ifd(exifOff, 0, exifEntries...)
	t.put(exifOff+200, []byte{1, 0, 0, 0, 250, 0, 0, 0})
	t.put(exifOff+220, []byte("2024:01:02 03:04:05\x00"))
	t.put(exifOff+260, bytes.Repeat([]byte{0xAB}, 24))
	t.ifd(gpsOff, 0, ent{0x0001, 2, 2, 'N'})
	return t.b
}

func TestSparse_TrailingExifIFD(t *testing.T) {
	t.Parallel()
	for _, o := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(fmt.Sprint(o), func(t *testing.T) {
			t.Parallel()
			// MakerNote at exifOff+260: not in the first region (the Exif IFD is
			// far from the header), so the file is read as Extract reads it.
			checkCompact(t, trailingExif(o, 300_000, true), false)
			sp := checkCompact(t, trailingExif(o, 300_000, false), true)
			if sp != nil && len(sp.Stream()) > 4096 {
				t.Errorf("stream = %d bytes, want a few hundred", len(sp.Stream()))
			}
		})
	}
}

// TestSparse_MakerNoteInFirstRegion: a MakerNote that lies in the first region
// keeps its offset, so the file is compacted and MakerNoteOffset is the file's.
func TestSparse_MakerNoteInFirstRegion(t *testing.T) {
	t.Parallel()
	size := 300_000
	exifOff := size - 700
	tf := newTB(binary.LittleEndian, size, 8)
	tf.fill(400, exifOff-16)
	tf.ifd(8, 0,
		ent{0x0111, 4, 1, 400},
		ent{0x8769, 4, 1, at(exifOff)},
	)
	// The MakerNote value (24 bytes) follows the header region at 100.
	tf.ifd(exifOff, 0, ent{0x927C, 7, 24, 100})
	tf.put(100, bytes.Repeat([]byte{0xCD}, 24))
	// checkCompact compares MakerNoteOffset with the whole-file parse; the file
	// itself must give the offset the MakerNote has in it.
	checkCompact(t, tf.b, true)
	e, err := exif.Parse(tf.b)
	if err != nil || e.MakerNoteOffset != 100 {
		t.Fatalf("oracle: MakerNoteOffset = %v (err %v), want 100", e, err)
	}
}

// TestSparse_IFD0AfterImageData: the IFD0 offset of the header addresses the end
// of the file; the header is the whole first region.
func TestSparse_IFD0AfterImageData(t *testing.T) {
	t.Parallel()
	size := 200_000
	ifd0 := size - 500
	tf := newTB(binary.LittleEndian, size, ifd0)
	tf.fill(8, ifd0-8)
	tf.ifd(ifd0, 0,
		ent{0x010F, 2, 16, at(ifd0 + 100)},
		ent{0x02BC, 1, 20, at(ifd0 + 140)},
		ent{0x83BB, 4, 2, at(ifd0 + 170)},
	)
	tf.put(ifd0+100, []byte("Camera Make 1234"))
	tf.put(ifd0+140, []byte("<x:xmpmeta xmlns:x/>"))
	tf.put(ifd0+170, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	sp := checkCompact(t, tf.b, true)
	if sp == nil {
		return
	}
	if sp.Head() == nil || len(sp.Head()) != 8 {
		t.Errorf("Head = %d bytes, want the 8-byte header", len(sp.Head()))
	}
	if string(sp.XMP()) != "<x:xmpmeta xmlns:x/>" {
		t.Errorf("XMP = %q", sp.XMP())
	}
	if len(sp.IPTC()) == 0 {
		t.Error("IPTC lost")
	}
}

// TestSparse_ThumbnailAmongImageData: an IFD1 whose JPEG thumbnail lies in the
// middle of the file, the IFD at the end.
func TestSparse_ThumbnailAmongImageData(t *testing.T) {
	t.Parallel()
	size := 400_000
	ifd1 := size - 400
	tf := newTB(binary.BigEndian, size, 8)
	tf.fill(300, size-500)
	tf.ifd(8, ifd1, ent{0x0111, 4, 1, 300})
	tf.ifd(ifd1, 0,
		ent{0x0201, 4, 1, 150_000},
		ent{0x0202, 4, 1, 5000},
	)
	copy(tf.b[150_000:], bytes.Repeat([]byte{0xFF, 0xD8}, 2500))
	sp := checkCompact(t, tf.b, true)
	if sp == nil {
		return
	}
	if len(sp.Stream()) < 5000 || len(sp.Stream()) > 6000 {
		t.Errorf("stream = %d bytes, want the thumbnail plus the structures", len(sp.Stream()))
	}
	e, err := exif.Parse(tf.b)
	if err != nil || e.IFD0.Next == nil || len(e.IFD0.Next.ThumbnailData) != 5000 {
		t.Fatalf("oracle: thumbnail not parsed (err %v)", err)
	}
}

// TestSparse_ValueBeyondEOF: an entry whose out-of-line value overruns the file
// is dropped by exif.Parse; the compacted parse must drop it too.
func TestSparse_ValueBeyondEOF(t *testing.T) {
	t.Parallel()
	size := 200_000
	ifd0 := size - 400
	tf := newTB(binary.LittleEndian, size, ifd0)
	tf.fill(8, ifd0-8)
	tf.ifd(ifd0, 0,
		ent{0x010F, 2, 16, at(ifd0 + 100)},
		ent{0x0110, 2, 100, at(size - 10)}, // overruns the file
		ent{0x0131, 2, 100, 0xFFFFFFF0},    // starts beyond the file
		ent{0x0132, 2, 0xFFFFFFFF, 20},     // count overflowing the file from a low offset
	)
	tf.put(ifd0+100, []byte("Camera Make 123"))
	checkCompact(t, tf.b, true)
}

// TestSparse_NotCompactable lists the layouts that are read as Extract reads
// them: Parse's view of them is a warning, an overlap or a rule of the walk.
func TestSparse_NotCompactable(t *testing.T) {
	t.Parallel()
	size := 200_000
	tail := size - 600
	base := func() *tb {
		tf := newTB(binary.LittleEndian, size, 8)
		tf.fill(400, tail-8)
		return tf
	}
	cases := []struct {
		name  string
		build func() []byte
	}{
		{"value over own directory", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 1, at(tail)})
			tf.ifd(tail, 0, ent{0x010F, 2, 40, at(tail + 8)}) // value inside the directory
			return tf.b
		}},
		{"overlapping directories", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 1, at(tail)}, ent{0x8825, 4, 1, at(tail + 6)})
			tf.ifd(tail, 0, ent{0x9003, 2, 4, 0x41424300})
			tf.ifd(tail+6, 0)
			return tf.b
		}},
		{"directory over the header", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 1, 4})
			return tf.b
		}},
		{"value over the header", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x010F, 2, 16, 0}, ent{0x8769, 4, 1, at(tail)})
			tf.ifd(tail, 0)
			return tf.b
		}},
		{"Exif pointer beyond the file", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 1, at(size + 100)}, ent{0x010F, 2, 16, at(tail)})
			return tf.b
		}},
		{"Exif pointer zero", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 1, 0}, ent{0x010F, 2, 16, at(tail)})
			return tf.b
		}},
		{"directory overruns the file", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 1, at(size - 10)})
			tf.o.PutUint16(tf.b[size-10:], 100) // 100 entries do not fit in 10 bytes
			return tf.b
		}},
		{"out-of-line pointer value", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x8769, 4, 2, at(tail)}, ent{0x010F, 2, 16, at(tail + 100)})
			return tf.b
		}},
		{"out-of-line thumbnail offset", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x0201, 4, 2, at(tail)}, ent{0x0202, 4, 1, 100})
			return tf.b
		}},
		{"repeated thumbnail tag", func() []byte {
			tf := base()
			tf.ifd(8, 0, ent{0x0201, 4, 1, 400}, ent{0x0201, 4, 1, 500}, ent{0x0202, 4, 1, 100}, ent{0x010F, 2, 16, at(tail)})
			return tf.b
		}},
		{"IFD below the header", func() []byte {
			tf := base()
			tf.ifd(8, 3, ent{0x010F, 2, 16, at(tail)})
			return tf.b
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			checkCompact(t, c.build(), false)
		})
	}
}

// TestSparse_Cycles: a next-IFD offset and a pointer back to an IFD already
// walked are compacted, and the cycle ends as it does in the file.
func TestSparse_Cycles(t *testing.T) {
	t.Parallel()
	size := 300_000
	tail := size - 400
	cases := map[string]func() []byte{
		"next points at IFD0": func() []byte {
			tf := newTB(binary.LittleEndian, size, tail)
			tf.fill(8, tail-8)
			tf.ifd(tail, tail, ent{0x010F, 2, 8, 0x41424344})
			return tf.b
		},
		"Interop points at IFD0 (Kodak ProBack)": func() []byte {
			tf := newTB(binary.LittleEndian, size, 8)
			tf.fill(300, tail-8)
			tf.ifd(8, 0, ent{0x8769, 4, 1, at(tail)})
			tf.ifd(tail, 0, ent{0xA005, 4, 1, 8})
			return tf.b
		},
		"Exif pointer points at IFD0": func() []byte {
			tf := newTB(binary.LittleEndian, size, 8)
			tf.fill(300, tail-8)
			tf.ifd(8, tail, ent{0x8769, 4, 1, 8})
			tf.ifd(tail, 8, ent{0x9003, 2, 2, 0x4100})
			return tf.b
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkCompact(t, build(), true)
		})
	}
}

// TestSparse_LongChain: a next-IFD chain longer than exif.Parse follows is not
// compacted (its budget ends the chain with a warning).
func TestSparse_LongChain(t *testing.T) {
	t.Parallel()
	n := maxExtentTraverseIFDs + 8
	size := 100 + n*30
	tf := newTB(binary.LittleEndian, size, 100)
	for i := range n {
		off := 100 + i*30
		next := off + 30
		if i == n-1 {
			next = 0
		}
		tf.ifd(off, next, ent{0x0100, 4, 1, at(i)})
	}
	data := tf.b
	if _, _, _, sp, err := extractCompactWith(bytes.NewReader(data), sparseTestParams); err != nil || sp != nil {
		t.Fatalf("a %d-IFD chain was compacted (sp %v, err %v)", n, sp != nil, err)
	}
}

// TestSparse_ScatteredChain: a multi-page TIFF whose IFDs are separated by the
// pixel data of each page.
func TestSparse_ScatteredChain(t *testing.T) {
	t.Parallel()
	const pages, page = 6, 50_000
	size := 8 + pages*page
	tf := newTB(binary.LittleEndian, size, page-300)
	for i := range pages {
		off := (i+1)*page - 300
		next := off + page
		if i == pages-1 {
			next = 0
		}
		tf.ifd(off, next, ent{0x0100, 4, 1, at(i + 1)}, ent{0x010E, 2, 14, at(off + 100)})
		tf.put(off+100, []byte(fmt.Sprintf("page %d\x00\x00\x00\x00\x00\x00\x00", i)))
	}
	sp := checkCompact(t, tf.b, true)
	if sp != nil && len(sp.Stream()) > 4096 {
		t.Errorf("stream = %d bytes for %d pages", len(sp.Stream()), pages)
	}
}

// TestSparse_RestoreIdempotent: Restore puts every offset back, and a second
// call changes nothing.
func TestSparse_RestoreIdempotent(t *testing.T) {
	t.Parallel()
	data := trailingExif(binary.LittleEndian, 300_000, false)
	_, _, _, sp, err := extractCompactWith(bytes.NewReader(data), sparseTestParams)
	if err != nil || sp == nil {
		t.Fatalf("not compacted: %v", err)
	}
	if bytes.Equal(sp.Head(), data[:len(sp.Head())]) {
		t.Error("no offset of the head was rewritten (the Exif pointer of IFD0 must be)")
	}
	sp.Restore()
	first := bytes.Clone(sp.Stream())
	sp.Restore()
	if !bytes.Equal(first, sp.Stream()) {
		t.Error("second Restore changed the stream")
	}
	if !bytes.Equal(sp.Head(), data[:len(sp.Head())]) {
		t.Error("head not restored")
	}
}

// TestSparse_Thresholds: the production thresholds leave a small file, and a file
// whose structures are all within reach, to Extract.
func TestSparse_Thresholds(t *testing.T) {
	t.Parallel()
	data := trailingExif(binary.LittleEndian, 300_000, false)
	if _, _, _, sp, err := ExtractCompact(bytes.NewReader(data)); err != nil || sp != nil {
		t.Errorf("a 300 kB file was compacted (sp %v, err %v)", sp != nil, err)
	}
	// Large enough: a 6 MiB file whose Exif IFD is at the end.
	big := trailingExif(binary.LittleEndian, 6<<20, false)
	raw, _, _, sp, err := ExtractCompact(bytes.NewReader(big))
	if err != nil || sp == nil {
		t.Fatalf("6 MiB file not compacted (err %v)", err)
	}
	if len(raw) > 4096 {
		t.Errorf("rawEXIF = %d bytes", len(raw))
	}
}

// TestSparse_Corpus compacts every TIFF-family file of the corpus that exists
// and compares it with the whole-file parse.
func TestSparse_Corpus(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "testdata", "corpus")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("corpus absent (run testdata/download.sh): " + root)
	}
	var compacted, files int
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", p, err)
		}
		if info.Size() <= smallFileWholeReadThreshold {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // G122: the test walks the repository's own corpus
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if len(data) < 8 || (data[0] != 'I' && data[0] != 'M') || data[1] != data[0] {
			return nil
		}
		if (binary.LittleEndian.Uint16(data[2:]) != 0x2A && data[0] == 'I') || (binary.BigEndian.Uint16(data[2:]) != 0x2A && data[0] == 'M') {
			return nil // BigTIFF and the RAW variants with their own magic
		}
		files++
		_, _, _, sp, err := ExtractCompact(bytes.NewReader(data))
		if err != nil || sp == nil {
			return nil //nolint:nilerr // not compacted: nothing to compare
		}
		compacted++
		got, perr := exif.Parse(sp.Stream(), exif.AliasThumbnail())
		sp.Restore()
		want, werr := exif.Parse(data, exif.AliasThumbnail())
		if (perr == nil) != (werr == nil) {
			t.Errorf("%s: parse errors differ: %v vs %v", p, perr, werr)
		} else if perr == nil && len(got.Warnings) == 0 {
			if err := exifEqualErr(got, want); err != nil {
				t.Errorf("%s: %v", p, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("corpus: %d TIFF-family files above %d bytes, %d compacted", files, smallFileWholeReadThreshold, compacted)
}

// sparseSeedFiles returns small TIFFs for the fuzz corpus of FuzzTIFFExtract:
// the layouts of this file's tests (a trailing Exif IFD, IFD0 and thumbnail
// among the image data), and the hostile ones — overlapping ranges, offsets
// beyond the end of the file, cycles, shared values.
func sparseSeedFiles() [][]byte {
	le, be := binary.LittleEndian, binary.BigEndian
	var seeds [][]byte
	seeds = append(seeds,
		trailingExif(le, 20_000, false), trailingExif(be, 20_000, false),
		trailingExif(le, 20_000, true), trailingExif(be, 9_000, true))

	// IFD0 after the image data, with XMP and IPTC.
	{
		size := 12_000
		ifd0 := size - 500
		tf := newTB(le, size, ifd0)
		tf.fill(8, ifd0-8)
		tf.ifd(ifd0, 0, ent{0x010F, 2, 16, at(ifd0 + 100)}, ent{0x02BC, 1, 20, at(ifd0 + 140)}, ent{0x83BB, 4, 2, at(ifd0 + 170)})
		tf.put(ifd0+100, []byte("Camera Make 1234"))
		tf.put(ifd0+140, []byte("<x:xmpmeta xmlns:x/>"))
		seeds = append(seeds, tf.b)
	}
	// IFD1 with a thumbnail in the middle of the file.
	{
		size := 14_000
		tf := newTB(be, size, 8)
		tf.fill(300, size-500)
		tf.ifd(8, size-400, ent{0x0111, 4, 1, 300})
		tf.ifd(size-400, 0, ent{0x0201, 4, 1, 5000}, ent{0x0202, 4, 1, 900})
		seeds = append(seeds, tf.b)
	}
	// Out-of-range offsets: values past the end, a count overflowing from a low
	// offset, a pointer past the end.
	{
		size := 9_000
		ifd0 := size - 400
		tf := newTB(le, size, ifd0)
		tf.ifd(ifd0, 0, ent{0x010F, 2, 16, at(ifd0 + 100)}, ent{0x0110, 2, 100, at(size - 10)}, ent{0x0131, 2, 100, 0xFFFFFFF0}, ent{0x0132, 2, 0xFFFFFFFF, 20}, ent{0x8769, 4, 1, 0xFFFFFFFF})
		seeds = append(seeds, tf.b)
	}
	// Overlapping ranges: a value over its own directory, two directories one
	// over the other, two entries sharing one value, a value over the header.
	{
		size := 8_000
		tail := size - 600
		tf := newTB(le, size, 8)
		tf.ifd(8, 0, ent{0x8769, 4, 1, at(tail)}, ent{0x8825, 4, 1, at(tail + 6)}, ent{0x010F, 2, 16, 0}, ent{0x0110, 2, 16, 0})
		tf.ifd(tail, 0, ent{0x010F, 2, 40, at(tail + 8)}, ent{0x0110, 2, 40, at(tail + 8)})
		tf.ifd(tail+6, 0)
		seeds = append(seeds, tf.b)
	}
	// Cycles: next-IFD to IFD0, Interoperability pointer to IFD0, Exif pointer to IFD0.
	{
		size := 7_000
		tf := newTB(le, size, 8)
		tf.ifd(8, size-300, ent{0x8769, 4, 1, 8})
		tf.ifd(size-300, 8, ent{0xA005, 4, 1, 8}, ent{0x8769, 4, 1, at(size - 300)})
		seeds = append(seeds, tf.b)
	}
	return seeds
}

// checkSparseProperty is the property that compaction must keep for any input:
// when the compacted stream parses without error or warning, the whole file
// parses to the same EXIF (and the same IPTC and XMP), without warnings.
func checkSparseProperty(t *testing.T, data []byte) {
	t.Helper()
	_, iptc, xmp, sp, err := extractCompactWith(bytes.NewReader(data), sparseTestParams)
	if err != nil || sp == nil {
		return
	}
	got, perr := exif.Parse(sp.Stream(), exif.AliasThumbnail())
	sp.Restore()
	if !bytes.Equal(sp.Head(), data[:len(sp.Head())]) {
		t.Fatal("Head after Restore is not a prefix of the file")
	}
	wRaw, wIPTC, wXMP, werr := Extract(bytes.NewReader(data))
	_ = wRaw
	if werr == nil && (!bytes.Equal(iptc, wIPTC) || !bytes.Equal(xmp, wXMP)) {
		t.Fatalf("IPTC/XMP of the compacted file differ from Extract: %q/%q vs %q/%q", iptc, xmp, wIPTC, wXMP)
	}
	if perr != nil || len(got.Warnings) != 0 {
		return // Read reads such a file as Extract reads it
	}
	want, werr := exif.Parse(data, exif.AliasThumbnail())
	if werr != nil {
		t.Fatalf("the compacted stream parses, the file does not: %v", werr)
	}
	if err := exifEqualErr(got, want); err != nil {
		t.Fatalf("compacted parse differs from the file's: %v", err)
	}
}

// TestSparse_Seeds runs the seeds of the fuzz target as unit tests.
func TestSparse_Seeds(t *testing.T) {
	t.Parallel()
	for i, s := range sparseSeedFiles() {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			checkSparseProperty(t, s)
		})
	}
}
