package gometadata

// read_task389_test.go — task #389 (sprint 50): Read of a TIFF or DNG whose IFDs
// lie after the image data reads the metadata structures, not the image data,
// and gives the result of a read of the whole prefix: the same parsed metadata,
// and a Write that produces the same bytes.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// trailingIFDTIFF builds a classic TIFF of imageBytes bytes of strip data in which
// IFD0 (with the strip tags) is at the start and the Exif IFD, with a GPS IFD,
// follows the image data. dupTag adds a repeated tag to the Exif IFD, which
// exif.Parse reports as a warning; iptc adds IPTC-NAA (0x83BB), XMP (0x02BC) and a
// Photoshop resource block (0x8649) with an IPTC digest to IFD0.
func trailingIFDTIFF(imageBytes int, dupTag, iptc bool) []byte {
	const hdr, image = 8, 600
	o := binary.LittleEndian
	exifOff := image + imageBytes + 16
	gpsOff := exifOff + 400
	buf := make([]byte, gpsOff+200)
	buf[0], buf[1] = 'I', 'I'
	o.PutUint16(buf[2:], 0x002A)
	o.PutUint32(buf[4:], hdr)
	for i := image; i < image+imageBytes; i++ {
		buf[i] = byte(i * 31) //nolint:gosec // G115: a byte pattern, truncation intended
	}
	put := func(base int, next uint32, es [][4]uint32) {
		o.PutUint16(buf[base:], uint16(len(es))) //nolint:gosec // test sizes
		p := base + 2
		for _, e := range es {
			o.PutUint16(buf[p:], uint16(e[0]))   //nolint:gosec // test values
			o.PutUint16(buf[p+2:], uint16(e[1])) //nolint:gosec // test values
			o.PutUint32(buf[p+4:], e[2])
			o.PutUint32(buf[p+8:], e[3])
			p += 12
		}
		o.PutUint32(buf[p:], next)
	}
	ifd0 := [][4]uint32{
		{0x0100, 3, 1, 64}, {0x0101, 3, 1, 64}, {0x0102, 3, 1, 8}, {0x0103, 3, 1, 1}, {0x0106, 3, 1, 1},
		{0x010F, 2, 12, 300}, {0x0110, 2, 12, 320},
		{0x0111, 4, 1, image}, {0x0115, 3, 1, 1}, {0x0116, 3, 1, 64}, {0x0117, 4, 1, uint32(imageBytes)}, //nolint:gosec // test sizes
	}
	if iptc {
		// IIM record 2, dataset 120 (Caption/Abstract): "sparse iptc".
		copy(buf[340:], []byte{0x1C, 0x02, 0x78, 0x00, 0x0B, 's', 'p', 'a', 'r', 's', 'e', ' ', 'i', 'p', 't', 'c'})
		// Photoshop IRB: resource 0x0404 (IPTC, the same IIM) and 0x0425 (digest).
		irb := []byte("8BIM\x04\x04\x00\x00\x00\x00\x00\x10")
		irb = append(irb, buf[340:356]...)
		irb = append(irb, []byte("8BIM\x04\x25\x00\x00\x00\x00\x00\x10")...)
		irb = append(irb, bytes.Repeat([]byte{0x5A}, 16)...)
		copy(buf[400:], irb)
		copy(buf[500:], "<x:xmpmeta xmlns:x='adobe:ns:meta/'/>")
		ifd0 = append(ifd0, [4]uint32{0x02BC, 1, 38, 500}, [4]uint32{0x83BB, 4, 4, 340}, [4]uint32{0x8649, 1, uint32(len(irb)), 400}) //nolint:gosec // test sizes
	}
	ifd0 = append(ifd0, [4]uint32{0x8769, 4, 1, uint32(exifOff)}, [4]uint32{0x8825, 4, 1, uint32(gpsOff)}) //nolint:gosec // test sizes
	put(hdr, 0, ifd0)
	copy(buf[300:], "Maker-Name\x00\x00")
	copy(buf[320:], "Model-X-123\x00")
	at := func(off int) uint32 { return uint32(off) } //nolint:gosec // G115: test offsets
	exifEntries := [][4]uint32{
		{0x829A, 5, 1, at(exifOff + 200)},  // ExposureTime
		{0x9003, 2, 20, at(exifOff + 220)}, // DateTimeOriginal
		{0x920A, 5, 1, at(exifOff + 250)},  // FocalLength
	}
	if dupTag {
		exifEntries = append(exifEntries, [4]uint32{0x920A, 5, 1, at(exifOff + 250)})
	}
	put(exifOff, 0, exifEntries)
	copy(buf[exifOff+200:], []byte{1, 0, 0, 0, 125, 0, 0, 0})
	copy(buf[exifOff+220:], "2024:05:06 07:08:09\x00")
	copy(buf[exifOff+250:], []byte{50, 0, 0, 0, 1, 0, 0, 0})
	put(gpsOff, 0, [][4]uint32{{0x0001, 2, 2, 'N'}})
	return buf
}

// dumpExcluding prints v for comparison; unexported out-of-line offsets of IFD
// entries (rawOffset) are skipped: they are positions in the stream that was
// parsed and are observable only through MakerNoteOffset, which is printed.
// ws appends s to sb (strings.Builder writes never fail).
func ws(sb *strings.Builder, s string) { _, _ = sb.WriteString(s) }

func dumpExcluding(sb *strings.Builder, v reflect.Value, seen map[uintptr]bool) {
	switch v.Kind() {
	case reflect.Invalid:
		ws(sb, "nil")
	case reflect.Bool:
		ws(sb, strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		ws(sb, strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		ws(sb, strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		ws(sb, fmt.Sprint(v.Float()))
	case reflect.String:
		ws(sb, fmt.Sprintf("%q", v.String()))
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			ws(sb, "nil[]")
			return
		}
		if v.Type().Elem().Kind() == reflect.Uint8 && v.Kind() == reflect.Slice {
			ws(sb, hex.EncodeToString(v.Bytes()))
			return
		}
		ws(sb, "[")
		for i := range v.Len() {
			dumpExcluding(sb, v.Index(i), seen)
			ws(sb, ",")
		}
		ws(sb, "]")
	case reflect.Map:
		keys := v.MapKeys()
		strs := make([]string, len(keys))
		for i, k := range keys {
			var kb strings.Builder
			dumpExcluding(&kb, k, seen)
			strs[i] = kb.String() + ":"
			var vb strings.Builder
			dumpExcluding(&vb, v.MapIndex(k), seen)
			strs[i] += vb.String()
		}
		sort.Strings(strs)
		ws(sb, "{"+strings.Join(strs, ",")+"}")
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			ws(sb, "nil")
			return
		}
		if v.Kind() == reflect.Pointer {
			if seen[v.Pointer()] {
				ws(sb, "<ref>")
				return
			}
			seen[v.Pointer()] = true
		}
		dumpExcluding(sb, v.Elem(), seen)
	case reflect.Struct:
		ws(sb, v.Type().String()+"{")
		for i := range v.NumField() {
			if v.Type().Field(i).Name == "rawOffset" {
				continue
			}
			ws(sb, v.Type().Field(i).Name+"=")
			dumpExcluding(sb, v.Field(i), seen)
			ws(sb, ";")
		}
		ws(sb, "}")
	default:
		ws(sb, "<"+v.Kind().String()+">")
	}
}

// parsedDump is everything Read parsed from m.
func parsedDump(m *Metadata) string {
	var sb strings.Builder
	for _, v := range []any{m.EXIF, m.IPTC, m.XMP} {
		dumpExcluding(&sb, reflect.ValueOf(v), map[uintptr]bool{})
		ws(&sb, "|")
	}
	for _, w := range m.ParseWarnings {
		ws(&sb, w.Error()+";")
	}
	return sb.String()
}

// readBoth reads data with the compaction and without it.
func readBoth(t *testing.T, data []byte, opts ...ReadOption) (compact, dense *Metadata) {
	t.Helper()
	var err error
	if compact, err = Read(bytes.NewReader(data), opts...); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if dense, err = Read(bytes.NewReader(data), append(opts[:len(opts):len(opts)], withoutSparse())...); err != nil {
		t.Fatalf("Read (dense): %v", err)
	}
	return compact, dense
}

// writeBytes writes m over data.
func writeBytes(t *testing.T, data []byte, m *Metadata) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Write(bytes.NewReader(data), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.Bytes()
}

// TestRead_CompactedTIFFMatchesPrefixRead reads a 5 MiB TIFF whose Exif IFD
// follows the image data with and without the compaction: same parsed metadata,
// same Write output (unmodified, and after setters), and the compacted read is
// the one that does not retain the image data.
func TestRead_CompactedTIFFMatchesPrefixRead(t *testing.T) {
	t.Parallel()
	data := trailingIFDTIFF(5<<20, false, false)
	compact, dense := readBoth(t, data)
	if !compact.rawEXIFSparse {
		t.Fatal("the TIFF was not compacted")
	}
	if dense.rawEXIFSparse {
		t.Fatal("the dense read was compacted")
	}
	if got, want := parsedDump(compact), parsedDump(dense); got != want {
		t.Fatalf("parsed metadata differ:\n compact %s\n dense   %s", got, want)
	}
	if compact.CameraModel() != "Model-X-123" || compact.Make() != "Maker-Name" {
		t.Errorf("Make/Model = %q/%q", compact.Make(), compact.CameraModel())
	}
	if got, ok := compact.DateTimeOriginal(); !ok || got.Year() != 2024 {
		t.Errorf("DateTimeOriginal = %v, %v", got, ok)
	}

	// Write: the metadata prefix is read from the source again, so the output is
	// the one of the whole-prefix read, whatever the modification.
	if !bytes.Equal(writeBytes(t, data, compact), writeBytes(t, data, dense)) {
		t.Error("unmodified Write differs")
	}
	for name, mod := range map[string]func(*Metadata){
		"caption":   func(m *Metadata) { m.SetCaption("sparse caption") },
		"copyright": func(m *Metadata) { m.SetCopyright("(c) sparse") },
		"gps":       func(m *Metadata) { m.SetGPS(41.5, -8.25) },
		"model":     func(m *Metadata) { m.SetCameraModel("Other Model") },
	} {
		c, d := readBoth(t, data)
		mod(c)
		mod(d)
		if !bytes.Equal(writeBytes(t, data, c), writeBytes(t, data, d)) {
			t.Errorf("Write after %s differs", name)
		}
	}
	// A second Write of the same Metadata is repeatable.
	if !bytes.Equal(writeBytes(t, data, compact), writeBytes(t, data, compact)) {
		t.Error("Write is not repeatable")
	}
}

// byteCountReader counts the bytes read through it.
type byteCountReader struct {
	*bytes.Reader
	n int
}

func (c *byteCountReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += n
	return n, err //nolint:wrapcheck // io.EOF must stay unwrapped for io.ReadFull
}

// TestRead_CompactedTIFFReadsOnlyMetadata: Read of a file whose Exif IFD follows
// 40 MiB of image data reads (and keeps) the metadata, not the image data.
func TestRead_CompactedTIFFReadsOnlyMetadata(t *testing.T) {
	t.Parallel()
	data := trailingIFDTIFF(40<<20, false, false)
	r := &byteCountReader{Reader: bytes.NewReader(data)}
	m, err := Read(r)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !m.rawEXIFSparse {
		t.Fatal("the TIFF was not compacted")
	}
	if r.n > 1<<20 {
		t.Errorf("Read of a %d MiB file read %d bytes, want < 1 MiB", len(data)>>20, r.n)
	}
	if got := len(m.rawEXIF); got > 1<<20 {
		t.Errorf("retained raw EXIF = %d bytes, want < 1 MiB", got)
	}
}

// TestRead_CompactedTIFFIPTCXMP: IPTC, XMP and the Photoshop IPTC digest of IFD0,
// which the compacted stream carries, read as the whole-prefix read reads them.
func TestRead_CompactedTIFFIPTCXMP(t *testing.T) {
	t.Parallel()
	data := trailingIFDTIFF(5<<20, false, true)
	compact, dense := readBoth(t, data)
	if !compact.rawEXIFSparse {
		t.Fatal("the TIFF was not compacted")
	}
	if compact.IPTC == nil || compact.XMP == nil || compact.rawIPTCDigest == nil {
		t.Fatalf("IPTC %v, XMP %v, digest %x: all must be read", compact.IPTC != nil, compact.XMP != nil, compact.rawIPTCDigest)
	}
	if !bytes.Equal(compact.rawIPTCDigest, dense.rawIPTCDigest) || compact.iptcTrustElev != dense.iptcTrustElev {
		t.Errorf("IPTC digest %x/%v, want %x/%v", compact.rawIPTCDigest, compact.iptcTrustElev, dense.rawIPTCDigest, dense.iptcTrustElev)
	}
	if got, want := parsedDump(compact), parsedDump(dense); got != want {
		t.Fatalf("parsed metadata differ:\n compact %s\n dense   %s", got, want)
	}
	if !bytes.Equal(compact.rawIPTC, dense.rawIPTC) || !bytes.Equal(compact.rawXMP, dense.rawXMP) {
		t.Error("raw IPTC or XMP differ")
	}
	if !bytes.Equal(writeBytes(t, data, compact), writeBytes(t, data, dense)) {
		t.Error("unmodified Write differs")
	}
	compact.SetKeywords([]string{"alpha", "beta"})
	dense.SetKeywords([]string{"alpha", "beta"})
	if !bytes.Equal(writeBytes(t, data, compact), writeBytes(t, data, dense)) {
		t.Error("Write after SetKeywords differs")
	}
}

// TestRead_CompactedTIFFFallsBackOnWarning: an EXIF warning names offsets of the
// stream that was parsed, so such a file is read the long way and reports the
// warning with the offsets of the file.
func TestRead_CompactedTIFFFallsBackOnWarning(t *testing.T) {
	t.Parallel()
	data := trailingIFDTIFF(5<<20, true, false)
	compact, dense := readBoth(t, data)
	if compact.rawEXIFSparse {
		t.Error("a file with a parse warning stayed compacted")
	}
	if len(compact.ParseWarnings) == 0 {
		t.Fatal("the duplicate tag was not reported")
	}
	if got, want := parsedDump(compact), parsedDump(dense); got != want {
		t.Fatalf("parsed metadata differ:\n compact %s\n dense   %s", got, want)
	}
	if !bytes.Equal(writeBytes(t, data, compact), writeBytes(t, data, dense)) {
		t.Error("Write differs")
	}
}

// TestRead_CompactedTIFFOptions: the read options give the same result with and
// without the compaction.
func TestRead_CompactedTIFFOptions(t *testing.T) {
	t.Parallel()
	data := trailingIFDTIFF(5<<20, false, false)
	for name, opts := range map[string][]ReadOption{
		"WithoutEXIF":      {WithoutEXIF()},
		"WithoutMakerNote": {WithoutMakerNote()},
		"WithoutXMP":       {WithoutXMP()},
		"Strict":           {Strict()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			compact, dense := readBoth(t, data, opts...)
			if got, want := parsedDump(compact), parsedDump(dense); got != want {
				t.Fatalf("parsed metadata differ:\n compact %s\n dense   %s", got, want)
			}
			if !bytes.Equal(writeBytes(t, data, compact), writeBytes(t, data, dense)) {
				t.Error("Write differs")
			}
		})
	}
}

// TestRead_CompactedCorpus reads every corpus TIFF and DNG of more than 4 MiB with
// and without the compaction and compares what was parsed and what Write
// produces.
func TestRead_CompactedCorpus(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "corpus")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("corpus absent (run testdata/download.sh): " + root)
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*", "*"))
	var compacted, compared int
	for _, p := range files {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() <= 4<<20 {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		c, err := Read(bytes.NewReader(data))
		if err != nil || c.Format().String() != "TIFF" && c.Format().String() != "DNG" {
			continue
		}
		compared++
		if !c.rawEXIFSparse {
			continue
		}
		compacted++
		d, err := Read(bytes.NewReader(data), withoutSparse())
		if err != nil {
			t.Errorf("%s: dense Read: %v", p, err)
			continue
		}
		if parsedDump(c) != parsedDump(d) {
			t.Errorf("%s: parsed metadata differ", p)
		}
		c.SetCaption("corpus caption")
		d.SetCaption("corpus caption")
		var oc, od bytes.Buffer
		if err := Write(bytes.NewReader(data), &oc, c); err != nil {
			t.Errorf("%s: Write: %v", p, err)
			continue
		}
		if err := Write(bytes.NewReader(data), &od, d); err != nil {
			t.Errorf("%s: dense Write: %v", p, err)
			continue
		}
		if !bytes.Equal(oc.Bytes(), od.Bytes()) {
			t.Errorf("%s: Write output differs", p)
		}
	}
	t.Logf("corpus: %d TIFF/DNG files above 4 MiB read, %d compacted", compared, compacted)
}
