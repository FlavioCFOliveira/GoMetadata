package gometadata

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// Tests of task #387: Write encodes only the segments that were modified.

// mod387Segment returns the marker segment (marker, length, payload).
func mod387Segment(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...) //nolint:gosec // G115: tiny test payloads
}

// mod387IRB returns a Photoshop APP13 payload: IPTC (0x0404, with a resource
// name) and a stale IPTC digest (0x0425).
func mod387IRB(iim []byte) []byte {
	var b bytes.Buffer
	b.WriteString("Photoshop 3.0\x00")
	res := func(id uint16, name string, data []byte) {
		b.WriteString("8BIM")
		b.Write(binary.BigEndian.AppendUint16(nil, id))
		b.WriteByte(byte(len(name))) //nolint:gosec // G115: a two-byte test name
		b.WriteString(name)
		if (1+len(name))%2 != 0 {
			b.WriteByte(0)
		}
		b.Write(binary.BigEndian.AppendUint32(nil, uint32(len(data)))) //nolint:gosec // G115: tiny test data
		b.Write(data)
		if len(data)%2 != 0 {
			b.WriteByte(0)
		}
	}
	res(0x0404, "ab", iim)
	res(0x0425, "", bytes.Repeat([]byte{0xEE}, 16))
	return b.Bytes()
}

// mod387XMP is a minimal XMP packet with dc:title.
const mod387XMP = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?>` +
	`<x:xmpmeta xmlns:x='adobe:ns:meta/'><rdf:RDF xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'>` +
	`<rdf:Description rdf:about='' xmlns:dc='http://purl.org/dc/elements/1.1/'>` +
	`<dc:title><rdf:Alt><rdf:li xml:lang='x-default'>original title</rdf:li></rdf:Alt></dc:title>` +
	`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end='r'?>`

// mod387JPEG builds a JPEG with JFIF, EXIF (Make, Model), IPTC (caption,
// keyword) in a Photoshop IRB with a stale digest, XMP and an ICC APP2.
func mod387JPEG(tb testing.TB) []byte {
	tb.Helper()
	m := NewMetadata(format.FormatJPEG)
	m.SetMake("MakeA")
	m.SetCameraModel("ModelA")
	tiff, err := exif.Encode(m.EXIF)
	if err != nil {
		tb.Fatalf("encode EXIF: %v", err)
	}
	iim := []byte{0x1C, 0x02, 0x78, 0x00, 0x07, 'c', 'a', 'p', 't', 'i', 'o', 'n', 0x1C, 0x02, 0x19, 0x00, 0x02, 'k', '1'}
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	b.Write(mod387Segment(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")))
	b.Write(mod387Segment(0xE1, append([]byte("Exif\x00\x00"), tiff...)))
	b.Write(mod387Segment(0xED, mod387IRB(iim)))
	b.Write(mod387Segment(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), mod387XMP...)))
	b.Write(mod387Segment(0xE2, []byte("ICC_PROFILE\x00data")))
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x01, 0x02, 0xFF, 0x00, 0x03, 0xFF, 0xD9})
	return b.Bytes()
}

// mod387Segments returns the payloads of the EXIF APP1, the Photoshop APP13 and
// the XMP APP1 segments of a JPEG.
func mod387Segments(file []byte) (exifSeg, psSeg, xmpSeg []byte) { //nolint:nonamedreturns // three same-typed results: names document which is which
	for i := 2; i+4 <= len(file) && file[i] == 0xFF; {
		m := file[i+1]
		if m == 0xDA || m == 0xD9 {
			break
		}
		n := int(binary.BigEndian.Uint16(file[i+2:]))
		if n < 2 || i+2+n > len(file) {
			break
		}
		p := file[i+4 : i+2+n]
		switch {
		case m == 0xE1 && bytes.HasPrefix(p, []byte("Exif\x00\x00")):
			exifSeg = p
		case m == 0xED && bytes.HasPrefix(p, []byte("Photoshop 3.0\x00")):
			psSeg = p
		case m == 0xE1 && bytes.HasPrefix(p, []byte("http://ns.adobe.com/xap/1.0/\x00")):
			xmpSeg = p
		}
		i += 2 + n
	}
	return exifSeg, psSeg, xmpSeg
}

// mod387Write writes m onto src and returns the output.
func mod387Write(tb testing.TB, src []byte, m *Metadata) []byte {
	tb.Helper()
	var out bytes.Buffer
	if err := Write(bytes.NewReader(src), &out, m); err != nil {
		tb.Fatalf("Write: %v", err)
	}
	return out.Bytes()
}

// TestMod387SettersMarkTheSegmentsTheyTouch: every Set* marks the segments it
// writes to, and only those (an XMP mirror of an EXIF value is marked when it
// is written).
func TestMod387SettersMarkTheSegmentsTheyTouch(t *testing.T) {
	t.Parallel()
	const (
		e = SegmentEXIF
		i = SegmentIPTC
		x = SegmentXMP
	)
	tests := []struct {
		name string
		set  func(*Metadata)
		want Segment
	}{
		{"SetCaption", func(m *Metadata) { m.SetCaption("c") }, e | i | x},
		{"SetCopyright", func(m *Metadata) { m.SetCopyright("c") }, e | i | x},
		{"SetCreator", func(m *Metadata) { m.SetCreator("c") }, e | i | x},
		{"SetCreators", func(m *Metadata) { m.SetCreators([]string{"a", "b"}) }, e | i | x},
		{"SetDateTimeOriginal", func(m *Metadata) { m.SetDateTimeOriginal(time.Unix(0, 0).UTC()) }, e | i | x},
		{"SetCameraModel", func(m *Metadata) { m.SetCameraModel("c") }, e | x},
		{"SetGPS", func(m *Metadata) { m.SetGPS(1, 2) }, e | x},
		{"SetLensModel", func(m *Metadata) { m.SetLensModel("c") }, e | x},
		{"SetKeywords", func(m *Metadata) { m.SetKeywords([]string{"k"}) }, i | x},
		// The XMP has none of the mirrored properties: only EXIF is touched.
		{"SetMake", func(m *Metadata) { m.SetMake("c") }, e},
		{"SetExposureTime", func(m *Metadata) { m.SetExposureTime(1, 100) }, e},
		{"SetFNumber", func(m *Metadata) { m.SetFNumber(2.8) }, e},
		{"SetISO", func(m *Metadata) { m.SetISO(100) }, e},
		{"SetFocalLength", func(m *Metadata) { m.SetFocalLength(50) }, e},
		{"SetOrientation", func(m *Metadata) { m.SetOrientation(1) }, e},
		{"SetImageSize", func(m *Metadata) { m.SetImageSize(10, 10) }, e},
	}
	src := mod387JPEG(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			if m.modified != 0 {
				t.Fatalf("a Metadata fresh from Read has modified = %b", m.modified)
			}
			tc.set(m)
			if m.modified != tc.want {
				t.Errorf("modified = %03b, want %03b", m.modified, tc.want)
			}
		})
	}

	t.Run("mirror into an XMP that has the property", func(t *testing.T) {
		t.Parallel()
		withISO := bytes.Replace(src, []byte("</rdf:Description>"), []byte("<exif:ISOSpeedRatings xmlns:exif='http://ns.adobe.com/exif/1.0/'><rdf:Seq><rdf:li>50</rdf:li></rdf:Seq></exif:ISOSpeedRatings></rdf:Description>"), 1)
		// The segment grew: rebuild the JPEG from its parts.
		withISO = mod387Rebuild(t, withISO)
		m, err := Read(bytes.NewReader(withISO))
		if err != nil {
			t.Fatal(err)
		}
		m.SetISO(100)
		if m.modified != SegmentEXIF|SegmentXMP {
			t.Errorf("modified = %03b, want EXIF and XMP", m.modified)
		}
	})

	t.Run("a segment the format cannot carry is not marked", func(t *testing.T) {
		t.Parallel()
		m := NewMetadata(format.FormatPNG)
		m.SetKeywords([]string{"k"}) // PNG has no IPTC to create
		if m.modified != SegmentXMP {
			t.Errorf("modified = %03b, want XMP only", m.modified)
		}
	})

	t.Run("an unknown format marks nothing", func(t *testing.T) {
		t.Parallel()
		m := NewMetadata(format.FormatUnknown)
		m.SetCaption("c")
		if m.modified != 0 {
			t.Errorf("modified = %03b, want none", m.modified)
		}
	})
}

// mod387Rebuild rebuilds a JPEG whose XMP segment was edited in place inside
// the buffer: the segment length is recomputed.
func mod387Rebuild(t *testing.T, edited []byte) []byte {
	t.Helper()
	i := bytes.Index(edited, []byte("http://ns.adobe.com/xap/1.0/\x00"))
	end := bytes.Index(edited[i:], []byte("<?xpacket end='r'?>")) + i + len("<?xpacket end='r'?>")
	payload := edited[i:end]
	var b bytes.Buffer
	b.Write(edited[:i-4])
	b.Write(mod387Segment(0xE1, payload))
	b.Write(edited[end:])
	return b.Bytes()
}

// TestMod387UnmodifiedWriteIsByteIdentical: Read and Write with no change give
// back the source, for the synthetic JPEG and every committed fixture.
func TestMod387UnmodifiedWriteIsByteIdentical(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{"synthetic JPEG": mod387JPEG(t)}
	entries, err := os.ReadDir(filepath.Join("testdata", "fixtures"))
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join("testdata", "fixtures", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = b
	}
	for name, src := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got := mod387Write(t, src, m); !bytes.Equal(got, src) {
				t.Errorf("an unmodified write changed the file (%d -> %d bytes)", len(src), len(got))
			}
			// Twice: the Metadata is not consumed.
			if got := mod387Write(t, src, m); !bytes.Equal(got, src) {
				t.Error("the second unmodified write changed the file")
			}
		})
	}
}

// TestMod387UnmodifiedWriteCorpus: the same on the corpus (JPEG, PNG, WebP,
// TIFF, HEIF/AVIF, the RAW formats and CR3), which the task requires byte
// identical, not only equal in metadata.
func TestMod387UnmodifiedWriteCorpus(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "corpus")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("corpus absent (run testdata/download.sh): %s", root)
	}
	exts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".tif": true, ".tiff": true,
		".heic": true, ".heif": true, ".avif": true, ".dng": true, ".nef": true, ".cr2": true, ".cr3": true,
		".arw": true, ".orf": true, ".rw2": true}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && exts[strings.ToLower(filepath.Ext(p))] {
			if info, ierr := d.Info(); ierr == nil && info.Size() <= 32<<20 {
				files = append(files, p)
			}
		}
		return nil
	})
	if len(files) == 0 {
		t.Skipf("no image in %s (run testdata/download.sh)", root)
	}
	var wg sync.WaitGroup
	next := make(chan string)
	var mu sync.Mutex
	var bad []string
	checked := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range next {
				src, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				m, err := Read(bytes.NewReader(src))
				if err != nil {
					continue
				}
				var out bytes.Buffer
				werr := Write(bytes.NewReader(src), &out, m)
				mu.Lock()
				checked++
				if werr != nil || !bytes.Equal(out.Bytes(), src) {
					bad = append(bad, p)
				}
				mu.Unlock()
			}
		}()
	}
	for _, p := range files {
		next <- p
	}
	close(next)
	wg.Wait()
	if checked == 0 {
		t.Fatal("no corpus file was read")
	}
	if len(bad) > 0 {
		t.Errorf("%d of %d files changed by an unmodified write; first: %v", len(bad), checked, bad[:min(len(bad), 5)])
	}
}

// TestMod387OnlyModifiedSegmentsAreEncoded: in a JPEG, a setter that touches one
// segment leaves the others as they are, byte for byte, the IPTC digest
// included.
func TestMod387OnlyModifiedSegmentsAreEncoded(t *testing.T) {
	t.Parallel()
	src := mod387JPEG(t)
	e0, p0, x0 := mod387Segments(src)
	if e0 == nil || p0 == nil || x0 == nil {
		t.Fatal("fixture: a segment is missing")
	}

	tests := []struct {
		name                        string
		set                         func(*Metadata)
		exifSame, iptcSame, xmpSame bool
	}{
		{"SetISO touches EXIF", func(m *Metadata) { m.SetISO(200) }, false, true, true},
		{"SetGPS touches EXIF and XMP", func(m *Metadata) { m.SetGPS(38.7, -9.1) }, false, true, false},
		{"SetKeywords touches IPTC and XMP", func(m *Metadata) { m.SetKeywords([]string{"a", "b"}) }, true, false, false},
		{"SetCaption touches all", func(m *Metadata) { m.SetCaption("new caption") }, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			tc.set(m)
			out := mod387Write(t, src, m)
			e1, p1, x1 := mod387Segments(out)
			for _, c := range []struct {
				name      string
				a, b      []byte
				wantEqual bool
			}{{"EXIF", e0, e1, tc.exifSame}, {"IPTC", p0, p1, tc.iptcSame}, {"XMP", x0, x1, tc.xmpSame}} {
				if bytes.Equal(c.a, c.b) != c.wantEqual {
					t.Errorf("%s segment unchanged = %v, want %v", c.name, bytes.Equal(c.a, c.b), c.wantEqual)
				}
			}
			if !bytes.Contains(out, []byte("ICC_PROFILE\x00data")) || !bytes.HasSuffix(out, src[len(src)-11:]) {
				t.Error("an APP2 segment or the image data changed")
			}
			// The output reads back with the new value.
			m2, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Read of the output: %v", err)
			}
			if tc.name == "SetISO touches EXIF" {
				if v := m2.EXIF.ExifIFD.Get(exif.TagISOSpeedRatings); v == nil || v.Uint16() != 200 {
					t.Errorf("ISO not written: %+v", v)
				}
			}
		})
	}
}

// TestMod387IPTCDigestIsLeftAlone: the stale IPTC digest of the source stays
// when the XMP is written and the IPTC is not, and when the IPTC is written and
// the XMP is not; the APP13 resource name is kept in the first case.
func TestMod387IPTCDigestIsLeftAlone(t *testing.T) {
	t.Parallel()
	src := mod387JPEG(t)
	stale := bytes.Repeat([]byte{0xEE}, 16)

	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.SetGPS(1, 2)
	_, p, _ := mod387Segments(mod387Write(t, src, m))
	if !bytes.Contains(p, stale) {
		t.Error("the digest was rewritten when only EXIF and XMP changed")
	}

	m, err = Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m.IPTC.AddKeyword("direct")
	m.MarkModified(SegmentIPTC)
	out := mod387Write(t, src, m)
	_, p, x := mod387Segments(out)
	if !bytes.Contains(p, stale) {
		t.Error("the digest was rewritten when only IPTC changed")
	}
	if !bytes.Contains(p, []byte("direct")) {
		t.Error("the keyword added to the IPTC is not in the output")
	}
	_, _, x0 := mod387Segments(src)
	if !bytes.Equal(x, x0) {
		t.Error("the XMP changed when only IPTC was marked")
	}
}

// TestMod387MarkModified: an edit made through an exported field is written
// only after MarkModified (task #387).
func TestMod387MarkModified(t *testing.T) {
	t.Parallel()
	src := mod387JPEG(t)

	edits := []struct {
		name    string
		edit    func(*Metadata)
		seg     Segment
		visible func(*Metadata) bool
	}{
		{"EXIF entry", func(m *Metadata) {
			v := []byte("Edited\x00")
			e := m.EXIF.IFD0.Get(exif.TagMake)
			e.Value, e.Count = v, uint32(len(v)) //nolint:gosec // G115: tiny test value
		}, SegmentEXIF, func(m *Metadata) bool { return m.EXIF.IFD0.Get(exif.TagMake).String() == "Edited" }},
		{"IPTC dataset", func(m *Metadata) { m.IPTC.SetCaption("edited caption") }, SegmentIPTC,
			func(m *Metadata) bool { return m.IPTC != nil && m.IPTC.Caption() == "edited caption" }},
		{"XMP property", func(m *Metadata) { m.XMP.SetCopyright("edited rights") }, SegmentXMP,
			func(m *Metadata) bool { return m.XMP != nil && m.XMP.Copyright() == "edited rights" }},
	}
	for _, tc := range edits {
		t.Run(tc.name+" without a mark is not written", func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(m)
			out := mod387Write(t, src, m)
			if !bytes.Equal(out, src) {
				t.Error("an unmarked edit changed the output")
			}
		})
		t.Run(tc.name+" with a mark is written", func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(m)
			m.MarkModified(tc.seg)
			out := mod387Write(t, src, m)
			m2, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Read of the output: %v", err)
			}
			if !tc.visible(m2) {
				t.Error("a marked edit is not in the output")
			}
			// The other segments are as in the source.
			e0, p0, x0 := mod387Segments(src)
			e1, p1, x1 := mod387Segments(out)
			same := map[Segment]bool{SegmentEXIF: bytes.Equal(e0, e1), SegmentIPTC: bytes.Equal(p0, p1), SegmentXMP: bytes.Equal(x0, x1)}
			for _, seg := range []Segment{SegmentEXIF, SegmentIPTC, SegmentXMP} {
				if seg != tc.seg && !same[seg] {
					t.Errorf("segment %03b changed although it was not marked", seg)
				}
			}
		})
	}

	t.Run("flags combine and ignore unknown bits", func(t *testing.T) {
		t.Parallel()
		m := NewMetadata(format.FormatJPEG)
		m.MarkModified(SegmentEXIF|SegmentXMP, Segment(0x80))
		if m.modified != SegmentEXIF|SegmentXMP {
			t.Errorf("modified = %b", m.modified)
		}
		m.MarkModified()
		if m.modified != SegmentEXIF|SegmentXMP {
			t.Errorf("MarkModified() with no argument changed modified to %b", m.modified)
		}
	})

	t.Run("a segment that Read found absent is written when assigned", func(t *testing.T) {
		t.Parallel()
		bare := buildMinimalJPEG(nil)
		m, err := Read(bytes.NewReader(bare))
		if err != nil {
			t.Fatal(err)
		}
		m.XMP = &xmp.XMP{Properties: map[string]map[string]string{}}
		m.XMP.SetCaption("assigned")
		out := mod387Write(t, bare, m) // no mark needed: there is no source XMP to carry
		m2, err := Read(bytes.NewReader(out))
		if err != nil || m2.XMP == nil || m2.XMP.Caption() != "assigned" {
			t.Errorf("assigned XMP not written: %v, %v", m2, err)
		}
	})

	t.Run("MarkModified of a nil segment keeps the source", func(t *testing.T) {
		t.Parallel()
		m, err := Read(bytes.NewReader(src), WithoutXMP())
		if err != nil {
			t.Fatal(err)
		}
		m.MarkModified(SegmentXMP)
		if out := mod387Write(t, src, m); !bytes.Equal(out, src) {
			t.Error("marking a segment that is nil changed the output")
		}
	})
}

// TestMod387OtherImageWritesEverySegment: metadata read from one image and
// written to another is written whole, as before task #387, so that Read and
// Write still copy metadata between images.
func TestMod387OtherImageWritesEverySegment(t *testing.T) {
	t.Parallel()
	src := mod387JPEG(t)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("JPEG to a bare JPEG", func(t *testing.T) {
		t.Parallel()
		dst := buildMinimalJPEG(nil)
		out := mod387Write(t, dst, m)
		m2, err := Read(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if m2.Make() != "MakeA" || m2.IPTC == nil || m2.IPTC.Caption() != "caption" || m2.XMP == nil || m2.XMP.Get(xmp.NSdc, "title") == "" && m2.Caption() == "" {
			t.Errorf("metadata not copied: make=%q iptc=%v xmp=%v", m2.Make(), m2.IPTC, m2.XMP)
		}
	})

	t.Run("JPEG to a PNG", func(t *testing.T) {
		t.Parallel()
		dst := buildMinimalPNG()
		out := mod387Write(t, dst, m)
		m2, err := Read(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if m2.Make() != "MakeA" || m2.XMP == nil {
			t.Errorf("metadata not copied: make=%q xmp=%v", m2.Make(), m2.XMP)
		}
	})

	t.Run("an unmodified Metadata of another JPEG replaces the destination's own", func(t *testing.T) {
		t.Parallel()
		other := bytes.Replace(src, []byte("MakeA"), []byte("MakeB"), 1)
		out := mod387Write(t, other, m) // m was read from src, not from other
		m2, err := Read(bytes.NewReader(out))
		if err != nil {
			t.Fatal(err)
		}
		if got := m2.Make(); got != "MakeA" {
			t.Errorf("Make = %q, want the metadata of m", got)
		}
	})
}

// TestMod387PNGKeepsAnUnmodifiedIPTCChunk: a PNG IPTC raw profile that is not
// modified is not written again.
func TestMod387PNGKeepsAnUnmodifiedIPTCChunk(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("testdata", "fixtures", "png_raw_profile_iptc.png"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if m.IPTC == nil {
		t.Fatal("fixture has no IPTC")
	}
	m.SetGPS(1, 2) // EXIF and XMP
	out := mod387Write(t, src, m)
	pngChunk := func(file []byte) []byte {
		i := bytes.Index(file, []byte("Raw profile type iptc"))
		if i < 0 {
			return nil
		}
		start := i - 8 // length and type precede the keyword
		n := int(binary.BigEndian.Uint32(file[start:]))
		return file[start : start+12+n]
	}
	if a, b := pngChunk(src), pngChunk(out); a == nil || !bytes.Equal(a, b) {
		t.Error("the unmodified IPTC raw profile chunk changed")
	}
}

// TestMod387TIFFFamilyPartialWrite: in a TIFF, a write that touches some
// segments keeps every other tag, and an unmodified write is byte identical.
func TestMod387TIFFFamilyPartialWrite(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(filepath.Join("testdata", "fixtures", "tiff_iptc_in_psir_only.tif"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if out := mod387Write(t, src, m); !bytes.Equal(out, src) {
		t.Fatal("an unmodified TIFF write is not byte identical")
	}
	before := map[exif.TagID][]byte{}
	for _, e := range m.EXIF.IFD0.Entries {
		before[e.Tag] = bytes.Clone(e.Value)
	}
	m.SetOrientation(6)
	out := mod387Write(t, src, m)
	m2, err := Read(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if v := m2.EXIF.IFD0.Get(exif.TagOrientation); v == nil || v.Uint16() != 6 {
		t.Error("Orientation was not written")
	}
	for tag, v := range before {
		if tag == exif.TagOrientation {
			continue
		}
		// Offsets to image data move; compare the tags that hold values.
		switch tag {
		case 0x0111, 0x0117, 0x0144, 0x0145, 0x014A, 0x8649, 0x83BB, 0x02BC:
			continue
		}
		if e := m2.EXIF.IFD0.Get(tag); e == nil || !bytes.Equal(e.Value, v) {
			t.Errorf("tag 0x%04X changed or was lost", tag)
		}
	}
	if m2.IPTC == nil || m2.XMP == nil && m.XMP != nil {
		t.Error("IPTC or XMP lost in the TIFF write")
	}
}

// TestMod387ConcurrentMarkAndSet: MarkModified, the setters and Write may run
// together (run with -race).
func TestMod387ConcurrentMarkAndSet(t *testing.T) {
	t.Parallel()
	src := mod387JPEG(t)
	m, err := Read(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(3)
		go func() { defer wg.Done(); m.MarkModified(SegmentIPTC) }()
		go func() { defer wg.Done(); m.SetISO(100) }()
		go func() {
			defer wg.Done()
			var out bytes.Buffer
			_ = Write(bytes.NewReader(src), &out, m)
		}()
	}
	wg.Wait()
	if m.modified&(SegmentIPTC|SegmentEXIF) != SegmentIPTC|SegmentEXIF {
		t.Errorf("modified = %b", m.modified)
	}
}

// FuzzMod387Write: for any bytes that Read accepts, an unmodified Write gives
// back the same bytes, and a write after a setter does not panic and reads
// back.
func FuzzMod387Write(f *testing.F) {
	f.Add(mod387JPEG(f))
	f.Add(buildMinimalPNG())
	f.Add(buildMinimalJPEG(minimalTIFFPayload()))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			return
		}
		var out bytes.Buffer
		if err := Write(bytes.NewReader(data), &out, m); err == nil && !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("an unmodified write changed %d bytes into %d", len(data), out.Len())
		}
		m.SetKeywords([]string{"fz"})
		out.Reset()
		_ = Write(bytes.NewReader(data), &out, m)
	})
}
