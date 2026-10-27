package gometadata

// write_task405_test.go — task #405: a write that modifies one segment leaves
// the chunks of the PNG segments it does not modify as they are (byte for byte,
// in the same order). Rule ID PNG-KEEP (docs/conformance/containers.md).

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// t405Chunk returns a PNG chunk with a correct CRC (W3C PNG 3rd ed. §5.3).
func t405Chunk(typ string, data []byte) []byte {
	b := make([]byte, 0, 12+len(data))
	b = binary.BigEndian.AppendUint32(b, uint32(len(data))) //nolint:gosec // G115: test fixture, small
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[4:]))
}

// t405Kind is the segment a PNG chunk belongs to, 0 when it holds none.
type t405Chunks struct {
	kind  Segment
	bytes []byte
}

// t405Classify returns, in file order, the chunks of src that hold EXIF, IPTC or
// XMP. It reads the chunk layout and the text keywords alone (PNG 3rd ed. §5.3,
// §11.3.4); "Raw profile type APP1" is left out because it holds EXIF or XMP.
func t405Classify(src []byte) []t405Chunks {
	out := make([]t405Chunks, 0, 4)
	for pos := 8; pos+12 <= len(src); {
		n := int(binary.BigEndian.Uint32(src[pos:]))
		if n < 0 || pos+12+n > len(src) {
			break
		}
		typ := string(src[pos+4 : pos+8])
		body := src[pos+8 : pos+8+n]
		whole := src[pos : pos+12+n]
		pos += 12 + n
		var kind Segment
		switch typ {
		case "eXIf":
			kind = SegmentEXIF
		case "tEXt", "zTXt", "iTXt":
			kw, _, _ := bytes.Cut(body, []byte{0})
			switch strings.ToLower(string(kw)) {
			case "raw profile type exif":
				kind = SegmentEXIF
			case "raw profile type iptc":
				kind = SegmentIPTC
			case "xml:com.adobe.xmp", "raw profile type xmp":
				kind = SegmentXMP
			}
		}
		if kind != 0 {
			out = append(out, t405Chunks{kind, whole})
		}
	}
	return out
}

// t405CheckKept fails t when a chunk of src of a segment that is not in
// modified is not in out, byte for byte, in the same order as in src.
func t405CheckKept(t *testing.T, name string, src, out []byte, modified Segment) (checked int) {
	t.Helper()
	var want, got [][]byte
	for _, c := range t405Classify(src) {
		if c.kind&modified == 0 {
			want = append(want, c.bytes)
		}
	}
	for _, c := range t405Classify(out) {
		if c.kind&modified == 0 {
			got = append(got, c.bytes)
		}
	}
	if len(want) != len(got) {
		t.Errorf("%s: %d unmodified chunks in the source, %d in the output (modified %b)", name, len(want), len(got), modified)
		return 0
	}
	for i := range want {
		if !bytes.Equal(want[i], got[i]) {
			t.Errorf("%s: unmodified chunk %d differs from the source (modified %b)", name, i, modified)
		}
	}
	return len(want)
}

// t405Modifiers are the edits the tests apply. MarkModified marks exactly one
// segment, so the other two must stay as they are. SetISO writes the EXIF, and
// the XMP only when it already holds the ISO properties; SetKeywords writes the
// XMP and, when the PNG has IPTC, the IPTC. Which segments a setter touched is
// read from Metadata.modified after it ran, so a setter that touched more than
// the case expects only makes the check cover fewer chunks, never wrongly more.
var t405Modifiers = []struct { //nolint:gochecknoglobals // immutable test table
	name string
	fn   func(m *Metadata)
}{
	{"MarkModified(EXIF)", func(m *Metadata) { m.MarkModified(SegmentEXIF) }},
	{"MarkModified(IPTC)", func(m *Metadata) { m.MarkModified(SegmentIPTC) }},
	{"MarkModified(XMP)", func(m *Metadata) { m.MarkModified(SegmentXMP) }},
	{"SetISO", func(m *Metadata) { m.SetISO(800) }},
	{"SetKeywords", func(m *Metadata) { m.SetKeywords([]string{"a"}) }},
}

// t405Synthetic is a PNG with an eXIf chunk and an XMP iTXt chunk.
func t405Synthetic(tb testing.TB) []byte {
	tb.Helper()
	// II*\0, IFD0 at 8: one entry, Make (0x010F, ASCII, 6 bytes at 26).
	ex := append([]byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x0F, 0x01, 2, 0, 6, 0, 0, 0, 26, 0, 0, 0, 0, 0, 0, 0}, "Canon\x00"...)
	xmp := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:rights><rdf:Alt><rdf:li xml:lang="x-default">me</rdf:li></rdf:Alt></dc:rights></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	ihdr := []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0}
	idat := []byte{0x78, 0x9c, 0x63, 0x60, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01}
	b := []byte("\x89PNG\r\n\x1a\n")
	b = append(b, t405Chunk("IHDR", ihdr)...)
	b = append(b, t405Chunk("eXIf", ex)...)
	b = append(b, t405Chunk("iTXt", append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), xmp...))...)
	b = append(b, t405Chunk("IDAT", idat)...)
	return append(b, t405Chunk("IEND", nil)...)
}

// TestTask405_PNGKeepsUnmodifiedChunks is the repro of task #405: Read, a
// change to another segment, Write; the eXIf and XMP chunks that were not
// modified are in the output, byte for byte.
func TestTask405_PNGKeepsUnmodifiedChunks(t *testing.T) {
	t.Parallel()
	src := t405Synthetic(t)
	for _, mod := range t405Modifiers {
		t.Run(mod.name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			mod.fn(m)
			if m.modified == 0 {
				t.Fatal("the modifier marked no segment")
			}
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if n := t405CheckKept(t, mod.name, src, out.Bytes(), m.modified); n == 0 {
				t.Error("no unmodified chunk was compared")
			}
		})
	}

	t.Run("SetKeywords keeps the eXIf of the issue", func(t *testing.T) {
		t.Parallel()
		m, err := Read(bytes.NewReader(src))
		if err != nil {
			t.Fatal(err)
		}
		m.SetKeywords([]string{"a"})
		var out bytes.Buffer
		if err := Write(bytes.NewReader(src), &out, m); err != nil {
			t.Fatal(err)
		}
		var srcE, outE []byte
		for _, c := range t405Classify(src) {
			if c.kind == SegmentEXIF {
				srcE = c.bytes
			}
		}
		for _, c := range t405Classify(out.Bytes()) {
			if c.kind == SegmentEXIF {
				outE = c.bytes
			}
		}
		if srcE == nil || !bytes.Equal(srcE, outE) {
			t.Error("the eXIf chunk is not the source's after SetKeywords")
		}
	})
}

// TestTask405_PNGKeepsUnmodifiedChunks_Files runs every modifier on the
// committed PNG fixtures and on every PNG of the corpus, and checks that each
// eXIf, XMP and IPTC raw profile chunk of a segment that was not modified is
// byte-identical in the output and in the same order.
func TestTask405_PNGKeepsUnmodifiedChunks_Files(t *testing.T) {
	t.Parallel()
	var files []string
	for _, root := range []string{filepath.Join("testdata", "fixtures"), filepath.Join("testdata", "corpus", "png")} {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			if strings.Contains(root, "corpus") {
				t.Logf("corpus absent (run testdata/download.sh): %s", root)
				continue
			}
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".png") {
				files = append(files, filepath.ToSlash(p))
			}
			return err
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no PNG fixture found")
	}
	pinned := readCorpusRejects(t)

	var withKinds [3]int // files holding an eXIf, an XMP, an IPTC chunk
	var compared, skipped int
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var seen Segment
		for _, c := range t405Classify(src) {
			seen |= c.kind
		}
		for i, s := range []Segment{SegmentEXIF, SegmentXMP, SegmentIPTC} {
			if seen&s != 0 {
				withKinds[i]++
			}
		}
		if seen == 0 {
			continue // nothing the write could have lost
		}
		for _, mod := range t405Modifiers {
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				skipped++
				break
			}
			mod.fn(m)
			var out bytes.Buffer
			if err := Write(bytes.NewReader(src), &out, m); err != nil {
				if _, ok := pinned[f]; !ok {
					t.Errorf("%s: %s: Write: %v", f, mod.name, err)
				}
				continue
			}
			if m.modified == 0 {
				continue
			}
			compared += t405CheckKept(t, f+" "+mod.name, src, out.Bytes(), m.modified)
		}
	}
	t.Logf("%d PNGs: %d with eXIf, %d with XMP, %d with IPTC raw profile; %d unreadable; %d unmodified chunks compared",
		len(files), withKinds[0], withKinds[1], withKinds[2], skipped, compared)
	if compared == 0 {
		t.Error("no chunk was compared")
	}
}

// BenchmarkTask405_PNGWrite measures a write that modifies one segment of a PNG
// that holds the others: SetISO (EXIF) on a PNG with XMP, and SetKeywords (XMP)
// on a PNG with an eXIf chunk.
func BenchmarkTask405_PNGWrite(b *testing.B) {
	for _, bc := range []struct {
		name   string
		file   string
		modify func(*Metadata)
	}{
		{"synthetic-SetISO", "", func(m *Metadata) { m.SetISO(1234) }},
		{"ReaganLargePng-SetISO", "testdata/corpus/png/exiv2/ReaganLargePng.png", func(m *Metadata) { m.SetISO(1234) }},
		{"ReaganLargePng-SetKeywords", "testdata/corpus/png/exiv2/ReaganLargePng.png", func(m *Metadata) { m.SetKeywords([]string{"a"}) }},
	} {
		b.Run(bc.name, func(b *testing.B) {
			var data []byte
			if bc.file == "" {
				data = t405Synthetic(b)
			} else {
				var err error
				if data, err = os.ReadFile(bc.file); err != nil {
					b.Skipf("corpus file absent (run testdata/download.sh): %s", bc.file)
				}
			}
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				b.Fatal(err)
			}
			bc.modify(m)
			var out bytes.Buffer
			out.Grow(len(data) + 1<<16)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for range b.N {
				out.Reset()
				if err := Write(bytes.NewReader(data), &out, m); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
