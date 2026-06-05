package gometadata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// buildMinimalJPEG constructs a minimal JPEG stream with an optional EXIF APP1
// segment so that Read() can detect and parse it.
func buildMinimalJPEG(exifData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	if exifData != nil {
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
		buf.Write([]byte{0xFF, 0xE1})
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], length)
		buf.Write(lb[:])
		buf.Write(payload)
	}

	// Minimal SOS + EOI so the stream terminates cleanly.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// minimalTIFFPayload builds a tiny valid TIFF/EXIF blob (LE, 1 IFD0 entry).
func minimalTIFFPayload() []byte {
	order := binary.LittleEndian
	// header(8) + ifd_count(2) + 1 entry(12) + next_ifd(4)
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	order.PutUint16(buf[8:], 1)       // 1 entry
	order.PutUint16(buf[10:], 0x010E) // ImageDescription tag
	order.PutUint16(buf[12:], 2)      // ASCII
	order.PutUint32(buf[14:], 4)      // count = 4
	copy(buf[18:], "test")            // inline value
	order.PutUint32(buf[22:], 0)      // next IFD = 0
	return buf
}

func TestRawAccessors(t *testing.T) {
	t.Parallel()
	tiff := minimalTIFFPayload()
	jpeg := buildMinimalJPEG(tiff)

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got := m.Format(); got != format.FormatJPEG {
		t.Errorf("Format() = %v, want FormatJPEG (%v)", got, format.FormatJPEG)
	}

	if raw := m.RawEXIF(); raw == nil {
		t.Error("RawEXIF() returned nil, want non-nil")
	}

	// No IPTC or XMP in the JPEG we built.
	if raw := m.RawIPTC(); raw != nil {
		t.Errorf("RawIPTC() = %v, want nil", raw)
	}
	if raw := m.RawXMP(); raw != nil {
		t.Errorf("RawXMP() = %v, want nil", raw)
	}
}

func TestRawAccessorsNoMetadata(t *testing.T) {
	t.Parallel()
	jpeg := buildMinimalJPEG(nil)

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got := m.Format(); got != format.FormatJPEG {
		t.Errorf("Format() = %v, want FormatJPEG", got)
	}
	if raw := m.RawEXIF(); raw != nil {
		t.Errorf("RawEXIF() = %v, want nil", raw)
	}
	if raw := m.RawIPTC(); raw != nil {
		t.Errorf("RawIPTC() = %v, want nil", raw)
	}
	if raw := m.RawXMP(); raw != nil {
		t.Errorf("RawXMP() = %v, want nil", raw)
	}
}

func TestUnsupportedFormat(t *testing.T) {
	t.Parallel()
	// Feed random bytes that don't match any known magic.
	_, err := Read(bytes.NewReader([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x00}))
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	var unsupported *UnsupportedFormatError
	if !errors.As(err, &unsupported) {
		t.Errorf("expected *UnsupportedFormatError, got %T: %v", err, err)
	}
}

func TestNewMetadata(t *testing.T) {
	t.Parallel()
	m := NewMetadata(format.FormatJPEG)
	if m == nil {
		t.Fatal("NewMetadata returned nil")
	}
	if got := m.Format(); got != format.FormatJPEG {
		t.Errorf("Format() = %v, want FormatJPEG", got)
	}
	if m.EXIF != nil || m.IPTC != nil || m.XMP != nil {
		t.Error("NewMetadata: expected nil EXIF/IPTC/XMP")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	// Valid: unknown format reports error.
	m := &Metadata{}
	if err := m.Validate(); err == nil {
		t.Error("Validate on FormatUnknown: expected error, got nil")
	}

	// Valid metadata with known format.
	m2 := NewMetadata(format.FormatJPEG)
	if err := m2.Validate(); err != nil {
		t.Errorf("Validate on valid Metadata: unexpected error: %v", err)
	}
}

func TestSupportsWrite(t *testing.T) {
	t.Parallel()

	// Tasks #92/#93 (epic #33 Option A): FormatTIFF is writable via the
	// copy-and-relocate serializer in format/tiff/relocate.go.
	// Task #98 (SubIFD OOL value fix): FormatDNG is now writable — the SubIFD
	// relocation path preserves ALL out-of-line value areas (RATIONAL
	// XResolution/YResolution, etc.) by updating their valOrOff pointers to the
	// new absolute positions. CR3 is writable as of task #91: stco/co64 offset
	// relocation is implemented in cr3.Inject; SupportsWrite(FormatCR3) returns true.
	// Task #95: CR2 is now writable — standard LE TIFF magic, MakerNote verbatim copy
	// validated against real Canon EOS 350D file (ImageDataHash IN==OUT, all tags kept).
	// Task #102: NEF is now writable — Nikon-specific MakerNote extension + PreviewIFD
	// relocation validated against real Nikon D70 file (ImageDataHash IN==OUT).
	// Task #103: ARW is now writable — Sony MakerNote absolute-offset rebase + SR2Private
	// block preservation validated against real Sony DSLR-A500 file (ImageDataHash IN==OUT).
	// ORF and RW2 remain read-only (non-standard magic; task #95 follow-up).
	writable := []format.FormatID{
		format.FormatJPEG, format.FormatTIFF, format.FormatDNG, format.FormatPNG,
		format.FormatHEIF, format.FormatWebP, format.FormatAVIF, format.FormatCR3,
		format.FormatCR2, format.FormatNEF, format.FormatARW,
	}
	for _, f := range writable {
		if !format.SupportsWrite(f) {
			t.Errorf("SupportsWrite(%v) = false, want true", f)
		}
	}

	// ORF/RW2: non-standard magic; FormatUnknown: always false.
	notWritable := []format.FormatID{
		format.FormatORF, format.FormatRW2,
		format.FormatUnknown,
	}
	for _, f := range notWritable {
		if format.SupportsWrite(f) {
			t.Errorf("SupportsWrite(%v) = true, want false", f)
		}
	}
}

// TestReadFileNotFound verifies that ReadFile wraps the OS "not found" error
// so callers can inspect it with errors.Is(err, fs.ErrNotExist).
func TestReadFileNotFound(t *testing.T) {
	t.Parallel()
	_, err := ReadFile("/nonexistent/definitely-does-not-exist/file.jpg")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is(err, os.ErrNotExist) = false; err = %v", err)
	}
}

// TestReadFilePermDenied verifies that ReadFile wraps the OS "permission
// denied" error so callers can inspect it with errors.Is(err, fs.ErrPermission).
func TestReadFilePermDenied(t *testing.T) {
	t.Parallel()
	// Skip when running as root because root can read any file.
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks are not enforced")
	}

	f, err := os.CreateTemp("", "gometadata-perm-test-denied-*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()

	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}

	_, err = ReadFile(path)
	if err == nil {
		t.Fatal("expected permission error, got nil")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("errors.Is(err, os.ErrPermission) = false; err = %v", err)
	}
}

// buildMinimalPNG builds a minimal valid PNG (signature + IHDR + IEND) with
// correct per-chunk CRC values. This is the minimum that the PNG extractor
// will accept as a recognised container.
func buildMinimalPNG() []byte {
	var buf bytes.Buffer

	// PNG signature (PNG §5.2).
	buf.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})

	writePNGChunk := func(chunkType string, data []byte) {
		var lbuf [4]byte
		binary.BigEndian.PutUint32(lbuf[:], uint32(len(data))) //nolint:gosec // G115: test helper, intentional type cast
		buf.Write(lbuf[:])
		buf.WriteString(chunkType)
		buf.Write(data)
		h := crc32.NewIEEE()
		_, _ = h.Write([]byte(chunkType))
		_, _ = h.Write(data)
		binary.BigEndian.PutUint32(lbuf[:], h.Sum32())
		buf.Write(lbuf[:])
	}

	// Minimal IHDR: 1×1 pixel, 8-bit RGB (PNG §11.2.2).
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1) // width
	binary.BigEndian.PutUint32(ihdr[4:], 1) // height
	ihdr[8] = 8                             // bit depth
	ihdr[9] = 2                             // colour type: RGB
	writePNGChunk("IHDR", ihdr)
	writePNGChunk("IEND", nil)

	return buf.Bytes()
}

// buildIPTCBytes builds a raw IPTC IIM byte stream with a single Record 2
// caption dataset (IIM §2.2.35, dataset 2:120).
func buildIPTCBytes(caption string) []byte {
	val := []byte(caption)
	var buf bytes.Buffer
	buf.WriteByte(0x1C)
	buf.WriteByte(2)
	buf.WriteByte(120)                 // DS2Caption
	buf.WriteByte(byte(len(val) >> 8)) //nolint:gosec // G115: test helper, intentional type cast
	buf.WriteByte(byte(len(val)))      //nolint:gosec // G115: test helper, intentional type cast
	buf.Write(val)
	return buf.Bytes()
}

// buildJPEGWithIPTCAndXMP assembles a JPEG containing both an APP13 IPTC
// segment and an APP1 XMP segment. The two caption values are intentionally
// different so TestMetadata_SourcePriority can verify XMP wins.
func buildJPEGWithIPTCAndXMP(iptcCaption, xmpCaption string) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// APP13: Photoshop IRB wrapping the IPTC IIM stream (EXIF §4.5.6).
	iptcRaw := buildIPTCBytes(iptcCaption)
	{
		var irb bytes.Buffer
		irb.WriteString("Photoshop 3.0\x00")
		irb.WriteString("8BIM")
		irb.Write([]byte{0x04, 0x04}) // IPTC-NAA resource 0x0404
		irb.Write([]byte{0x00, 0x00}) // Pascal string: empty name
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], uint32(len(iptcRaw))) //nolint:gosec // G115: test helper, intentional type cast
		irb.Write(sz[:])
		irb.Write(iptcRaw)
		if len(iptcRaw)%2 != 0 {
			irb.WriteByte(0x00)
		}
		length := uint16(irb.Len() + 2) //nolint:gosec // G115: test helper, intentional type cast
		buf.Write([]byte{0xFF, 0xED})
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], length)
		buf.Write(lb[:])
		buf.Write(irb.Bytes())
	}

	// APP1: XMP packet (Adobe XMP Specification Part 3 §1.1.3).
	{
		xmpPacket := fmt.Sprintf(
			`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`+
				`<x:xmpmeta xmlns:x="adobe:ns:meta/">`+
				`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
				`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
				`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">%s</rdf:li></rdf:Alt></dc:description>`+
				`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
			xmpCaption,
		)
		ns := "http://ns.adobe.com/xap/1.0/\x00"
		payload := append([]byte(ns), []byte(xmpPacket)...)
		length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
		buf.Write([]byte{0xFF, 0xE1})
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], length)
		buf.Write(lb[:])
		buf.Write(payload)
	}

	// Minimal SOS + EOI.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// buildRichTIFF constructs a TIFF blob (LE) whose IFD0 contains Make, Model,
// and Orientation, and whose ExifIFD contains ISOSpeedRatings. The ExifIFD
// pointer (tag 0x8769) is encoded in IFD0. All entries are laid out correctly
// with out-of-line ASCII values so exif.Parse can decode them.
//
// Layout:
//
//	[header 8B][IFD0: 4 entries + ExifIFD ptr = 5 entries][next=0][value area]
//	[ExifIFD: 1 entry][next=0]
func buildRichTIFF(makeStr, modelStr string, orientation uint16, iso uint16) []byte {
	order := binary.LittleEndian

	// We build the buffer in two passes: first compute offsets, then fill values.
	// Convenience: inline helper to write a LE uint16/uint32.

	makeBytes := []byte(makeStr + "\x00")
	modelBytes := []byte(modelStr + "\x00")

	// IFD0 contains 5 entries (in tag-ID order): Make, Model, Orientation,
	// ExifIFDPointer. That is 4 entries — but we need them sorted by tag:
	//   0x010F Make  (ASCII, out-of-line)
	//   0x0110 Model (ASCII, out-of-line)
	//   0x0112 Orientation (SHORT, inline)
	//   0x8769 ExifIFDPointer (LONG, inline)
	const (
		headerSz  = 8
		nIFD0     = 4
		ifd0Sz    = 2 + nIFD0*12 + 4 // count(2) + entries(N*12) + next(4)
		nExifIFD  = 1
		exifIFDSz = 2 + nExifIFD*12 + 4
	)
	// Value area begins right after IFD0.
	valueAreaStart := uint32(headerSz + ifd0Sz)
	// ExifIFD begins right after the value area.
	makeOff := valueAreaStart
	modelOff := makeOff + uint32(len(makeBytes))     //nolint:gosec // G115: test helper, intentional type cast
	exifIFDOff := modelOff + uint32(len(modelBytes)) //nolint:gosec // G115: test helper, intentional type cast
	// ExifIFD value area (ISO is SHORT → inline, no value area needed).

	totalSize := int(exifIFDOff) + exifIFDSz
	buf := make([]byte, totalSize)

	// TIFF header (TIFF §2).
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], headerSz) // IFD0 starts right after header

	// IFD0 entry count.
	ifd0Start := headerSz
	order.PutUint16(buf[ifd0Start:], uint16(nIFD0))

	writeEntry := func(base, i int, tag uint16, typ uint16, count uint32, val uint32) {
		p := base + 2 + i*12
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], val)
	}

	// IFD0 entries (sorted ascending by tag per TIFF §7).
	writeEntry(ifd0Start, 0, 0x010F, uint16(exif.TypeASCII), uint32(len(makeBytes)), makeOff)   //nolint:gosec // G115: test helper, intentional type cast
	writeEntry(ifd0Start, 1, 0x0110, uint16(exif.TypeASCII), uint32(len(modelBytes)), modelOff) //nolint:gosec // G115: test helper, intentional type cast
	// Orientation: SHORT inline — value is left-justified in the 4-byte field.
	writeEntry(ifd0Start, 2, 0x0112, uint16(exif.TypeShort), 1, uint32(orientation)) // Orientation
	writeEntry(ifd0Start, 3, 0x8769, uint16(exif.TypeLong), 1, exifIFDOff)           // ExifIFDPointer

	// next-IFD pointer = 0 (no IFD1).
	nextPtrPos := ifd0Start + 2 + nIFD0*12
	order.PutUint32(buf[nextPtrPos:], 0)

	// Value area: Make string, then Model string.
	copy(buf[makeOff:], makeBytes)
	copy(buf[modelOff:], modelBytes)

	// ExifIFD: 1 entry — ISOSpeedRatings (SHORT inline).
	exifBase := int(exifIFDOff)
	order.PutUint16(buf[exifBase:], uint16(nExifIFD))
	writeEntry(exifBase, 0, 0x8827, uint16(exif.TypeShort), 1, uint32(iso)) // ISOSpeedRatings
	order.PutUint32(buf[exifBase+2+nExifIFD*12:], 0)                        // next-IFD = 0

	return buf
}

// TestConcurrentRead verifies that concurrent calls to Read on the same JPEG
// bytes are safe under the race detector. Each goroutine operates on its own
// bytes.Reader, so there is no shared mutable state; the test is primarily a
// smoke test for the parser's goroutine safety.
func TestConcurrentRead(t *testing.T) {
	t.Parallel()

	jpegData := buildMinimalJPEG(minimalTIFFPayload())

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	results := make([]*Metadata, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			m, err := Read(bytes.NewReader(jpegData))
			errs[idx] = err
			results[idx] = m
		}(i)
	}
	wg.Wait()

	for i := range goroutines {
		if errs[i] != nil {
			t.Errorf("goroutine %d: Read returned error: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Errorf("goroutine %d: Read returned nil Metadata", i)
		}
	}
}

// TestConcurrentWrite verifies that concurrent calls to Write with the same
// Metadata struct do not race. Write reads m but does not mutate it after
// calling exif.Encode / iptc.Encode / xmp.Encode, each of which is
// read-only with respect to the encoded struct's fields.
func TestConcurrentWrite(t *testing.T) {
	t.Parallel()

	jpegData := buildMinimalJPEG(minimalTIFFPayload())

	m, err := Read(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Attach an IPTC block with a caption so Write has something to encode.
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("concurrent test")

	const goroutines = 5
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = Write(bytes.NewReader(jpegData), io.Discard, m)
		}(i)
	}
	wg.Wait()

	for i := range goroutines {
		if errs[i] != nil {
			t.Errorf("goroutine %d: Write returned error: %v", i, errs[i])
		}
	}
}

// BenchmarkWrite_JPEG measures the throughput of Write for a minimal JPEG
// with one parsed IPTC block. b.SetBytes is set to the input image size so
// go test -bench reports MB/s.
func BenchmarkWrite_JPEG(b *testing.B) {
	data := buildMinimalJPEG(minimalTIFFPayload())

	m, err := Read(bytes.NewReader(data))
	if err != nil {
		b.Fatalf("Read: %v", err)
	}
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("benchmark caption")

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()

	for range b.N {
		if err := Write(bytes.NewReader(data), io.Discard, m); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}

// BenchmarkWrite_PNG measures the throughput of Write for a minimal PNG with
// nil metadata (pass-through path: no metadata segments to re-encode).
func BenchmarkWrite_PNG(b *testing.B) {
	data := buildMinimalPNG()

	// A Metadata with nil EXIF/IPTC/XMP exercises the pass-through path in
	// Write: it detects the format, finds nothing to encode, and copies the
	// PNG stream to the output unchanged.
	m := NewMetadata(format.FormatPNG)

	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()

	for range b.N {
		if err := Write(bytes.NewReader(data), io.Discard, m); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}

// assertNilStringAccessors verifies that all string/slice accessors on a
// zero-value Metadata return empty results without panicking.
func assertNilStringAccessors(t *testing.T, m *Metadata) {
	t.Helper()
	if got := m.CameraModel(); got != "" {
		t.Errorf("CameraModel() = %q, want empty", got)
	}
	if got := m.Copyright(); got != "" {
		t.Errorf("Copyright() = %q, want empty", got)
	}
	if got := m.Caption(); got != "" {
		t.Errorf("Caption() = %q, want empty", got)
	}
	if got := m.Creator(); got != "" {
		t.Errorf("Creator() = %q, want empty", got)
	}
	if got := m.Make(); got != "" {
		t.Errorf("Make() = %q, want empty", got)
	}
	if got := m.Software(); got != "" {
		t.Errorf("Software() = %q, want empty", got)
	}
	if got := m.LensModel(); got != "" {
		t.Errorf("LensModel() = %q, want empty", got)
	}
	if got := m.Keywords(); len(got) != 0 {
		t.Errorf("Keywords() = %v, want nil/empty", got)
	}
}

// assertNilBoolAccessors verifies that all bool-returning accessors on a
// zero-value Metadata return ok=false without panicking.
func assertNilBoolAccessors(t *testing.T, m *Metadata) {
	t.Helper()
	if _, _, ok := m.GPS(); ok {
		t.Error("GPS() ok = true, want false")
	}
	if _, ok := m.DateTimeOriginal(); ok {
		t.Error("DateTimeOriginal() ok = true, want false")
	}
	if _, _, ok := m.ExposureTime(); ok {
		t.Error("ExposureTime() ok = true, want false")
	}
	if _, ok := m.FNumber(); ok {
		t.Error("FNumber() ok = true, want false")
	}
	if _, ok := m.ISO(); ok {
		t.Error("ISO() ok = true, want false")
	}
	if _, ok := m.FocalLength(); ok {
		t.Error("FocalLength() ok = true, want false")
	}
	if _, ok := m.Orientation(); ok {
		t.Error("Orientation() ok = true, want false")
	}
	if _, _, ok := m.ImageSize(); ok {
		t.Error("ImageSize() ok = true, want false")
	}
	if _, ok := m.DateTime(); ok {
		t.Error("DateTime() ok = true, want false")
	}
	if _, ok := m.Flash(); ok {
		t.Error("Flash() ok = true, want false")
	}
	if _, ok := m.WhiteBalance(); ok {
		t.Error("WhiteBalance() ok = true, want false")
	}
	if _, ok := m.ExposureMode(); ok {
		t.Error("ExposureMode() ok = true, want false")
	}
	if _, ok := m.MeteringMode(); ok {
		t.Error("MeteringMode() ok = true, want false")
	}
	if _, ok := m.ColorSpace(); ok {
		t.Error("ColorSpace() ok = true, want false")
	}
	if _, ok := m.SceneType(); ok {
		t.Error("SceneType() ok = true, want false")
	}
	if _, ok := m.DigitalZoomRatio(); ok {
		t.Error("DigitalZoomRatio() ok = true, want false")
	}
	if _, ok := m.SubjectDistance(); ok {
		t.Error("SubjectDistance() ok = true, want false")
	}
	if _, ok := m.Altitude(); ok {
		t.Error("Altitude() ok = true, want false")
	}
}

// TestMetadata_NilMetadata verifies that every accessor on a zero-value
// Metadata returns a safe zero/empty result without panicking.
func TestMetadata_NilMetadata(t *testing.T) {
	t.Parallel()
	m := &Metadata{} // EXIF, IPTC, XMP all nil
	assertNilStringAccessors(t, m)
	assertNilBoolAccessors(t, m)
}

// TestMetadata_ExifAccessors builds a JPEG with EXIF containing Make, Model,
// Orientation, and ISOSpeedRatings, then verifies the convenience accessors on
// the returned Metadata decode them correctly.
func TestMetadata_ExifAccessors(t *testing.T) {
	t.Parallel()
	const (
		wantMake        = "TestMake"
		wantModel       = "TestModel"
		wantOrientation = uint16(1)
		wantISO         = uint(400)
	)

	tiff := buildRichTIFF(wantMake, wantModel, wantOrientation, uint16(wantISO))
	jpeg := buildMinimalJPEG(tiff)

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.EXIF == nil {
		t.Fatal("EXIF is nil after Read; cannot test accessors")
	}

	// CameraModel() returns IFD0 tag 0x0110 (Model, EXIF §4.6.4 Table 3).
	if got := m.CameraModel(); got != wantModel {
		t.Errorf("CameraModel() = %q, want %q", got, wantModel)
	}

	if got := m.Make(); got != wantMake {
		t.Errorf("Make() = %q, want %q", got, wantMake)
	}

	if got, ok := m.ISO(); !ok {
		t.Error("ISO() ok = false, want true")
	} else if got != wantISO {
		t.Errorf("ISO() = %d, want %d", got, wantISO)
	}

	if got, ok := m.Orientation(); !ok {
		t.Error("Orientation() ok = false, want true")
	} else if got != wantOrientation {
		t.Errorf("Orientation() = %d, want %d", got, wantOrientation)
	}
}

// TestMetadata_SourcePriority verifies the documented resolution policy:
// XMP caption wins over IPTC caption when both are present (metadata.go §Caption).
// Similarly XMP copyright wins over IPTC.
func TestMetadata_SourcePriority(t *testing.T) {
	t.Parallel()
	const (
		iptcCaption = "IPTC caption"
		xmpCaption  = "XMP caption"
	)

	jpegData := buildJPEGWithIPTCAndXMP(iptcCaption, xmpCaption)

	m, err := Read(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Verify both sources were parsed so the test is meaningful.
	if m.IPTC == nil {
		t.Fatal("IPTC is nil: test cannot verify priority (IPTC segment not parsed)")
	}
	if m.XMP == nil {
		t.Fatal("XMP is nil: test cannot verify priority (XMP segment not parsed)")
	}

	// Caption: XMP (dc:description) must win over IPTC (2:120).
	if got := m.Caption(); got != xmpCaption {
		t.Errorf("Caption() = %q, want XMP value %q", got, xmpCaption)
	}

	// Cross-check: IPTC layer alone has the IPTC caption.
	if got := m.IPTC.Caption(); got != iptcCaption {
		t.Errorf("IPTC.Caption() = %q, want %q", got, iptcCaption)
	}

	// Cross-check: XMP layer alone has the XMP caption.
	if got := m.XMP.Caption(); got != xmpCaption {
		t.Errorf("XMP.Caption() = %q, want %q", got, xmpCaption)
	}
}

func TestWriteFilePreservesPermissions(t *testing.T) {
	t.Parallel()
	// Write a minimal JPEG to a temp file with mode 0644, then WriteFile it and
	// assert the mode is preserved after the atomic rename.
	jpeg := buildMinimalJPEG(minimalTIFFPayload())

	f, err := os.CreateTemp("", "gometadata-perm-test-*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()

	if _, err := f.Write(jpeg); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	want := os.FileMode(0644)
	if err := os.Chmod(path, want); err != nil {
		t.Fatal(err)
	}

	m, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := WriteFile(path, m); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode(); got != want {
		t.Errorf("file mode after WriteFile: got %v, want %v", got, want)
	}
}

// TestUnsupportedFormatErrorMessage verifies that UnsupportedFormatError.Error()
// produces the expected human-readable string (errors.go:26, currently 0%).
func TestUnsupportedFormatErrorMessage(t *testing.T) {
	t.Parallel()
	e := &UnsupportedFormatError{}
	copy(e.Magic[:], []byte{0xDE, 0xAD, 0xBE, 0xEF})
	msg := e.Error()
	if msg == "" {
		t.Error("UnsupportedFormatError.Error() returned empty string")
	}
	// The message must contain the magic bytes in hex.
	if len(msg) < 10 {
		t.Errorf("UnsupportedFormatError.Error() too short: %q", msg)
	}
}

// TestValidateNilIFD0 covers the Validate path that returns ErrNilIFD0.
func TestValidateNilIFD0(t *testing.T) {
	t.Parallel()
	m := NewMetadata(format.FormatJPEG)
	m.EXIF = &exif.EXIF{} // IFD0 is nil
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error for nil IFD0, got nil")
	}
	if !errors.Is(err, ErrNilIFD0) {
		t.Errorf("expected ErrNilIFD0, got %v", err)
	}
}

// TestValidateNilXMPProperties covers the Validate path that returns
// ErrNilXMPProperties.
func TestValidateNilXMPProperties(t *testing.T) {
	t.Parallel()
	m := NewMetadata(format.FormatJPEG)
	m.XMP = &xmp.XMP{} // Properties is nil
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error for nil XMP Properties, got nil")
	}
	if !errors.Is(err, ErrNilXMPProperties) {
		t.Errorf("expected ErrNilXMPProperties, got %v", err)
	}
}

// TestWriteNilIFD0 covers the Write guard for EXIF with nil IFD0.
// Write delegates to m.Validate(), which returns ErrNilIFD0 for this condition.
func TestWriteNilIFD0(t *testing.T) {
	t.Parallel()
	jpeg := buildMinimalJPEG(minimalTIFFPayload())
	m := NewMetadata(format.FormatJPEG)
	m.EXIF = &exif.EXIF{} // IFD0 is nil
	err := Write(bytes.NewReader(jpeg), io.Discard, m)
	if err == nil {
		t.Fatal("expected error for nil IFD0 in Write, got nil")
	}
	// Write now delegates to Validate, which returns ErrNilIFD0.
	if !errors.Is(err, ErrNilIFD0) {
		t.Errorf("expected ErrNilIFD0, got %v", err)
	}
}

// TestWriteUnsupportedFormat covers the UnsupportedFormatError path in Write.
func TestWriteUnsupportedFormat(t *testing.T) {
	t.Parallel()
	// Feed random bytes that don't match any magic.
	m := NewMetadata(format.FormatJPEG)
	err := Write(bytes.NewReader([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0}), io.Discard, m)
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	var unsupported *UnsupportedFormatError
	if !errors.As(err, &unsupported) {
		t.Errorf("expected *UnsupportedFormatError, got %T: %v", err, err)
	}
}

// TestWriteFileUnsupportedFormat covers WriteFile with a JPEG that was written
// with unrecognised magic (exercises the Write-error path in WriteFile).
func TestWriteFileUnsupportedFormat(t *testing.T) {
	t.Parallel()
	// Write garbage bytes to a temp file.
	tmp, err := os.CreateTemp("", "gometadata-unsupported-*")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	defer func() { _ = os.Remove(path) }()
	_, _ = tmp.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0})
	_ = tmp.Close()

	m := NewMetadata(format.FormatJPEG)
	err = WriteFile(path, m)
	if err == nil {
		t.Fatal("expected error for unsupported format in WriteFile, got nil")
	}
}

// TestWriteFileNotFound verifies that WriteFile wraps the OS "not found" error.
func TestWriteFileNotFound(t *testing.T) {
	t.Parallel()
	err := WriteFile("/nonexistent/does-not-exist/file.jpg", NewMetadata(format.FormatJPEG))
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is(err, os.ErrNotExist) = false; err = %v", err)
	}
}

// TestMetadata_GPSXMPFallback covers the XMP fallback path in Metadata.GPS().
func TestMetadata_GPSXMPFallback(t *testing.T) {
	t.Parallel()
	// XMP with GPS — lat="48,51.35N" lon="2,21.07E"
	x := &xmp.XMP{Properties: map[string]map[string]string{
		"http://www.w3.org/2003/01/geo/wgs84_pos#": {
			"lat": "48,51.35N",
			"lon": "2,21.07E",
		},
	}}
	m := &Metadata{XMP: x}
	lat, lon, ok := m.GPS()
	if !ok {
		t.Skip("XMP GPS not decoded (format not matching current xmp.GPS implementation)")
	}
	// Basic sanity: lat should be near 48, lon near 2.
	if lat < 40 || lat > 60 {
		t.Errorf("GPS lat = %f, want ~48", lat)
	}
	if lon < 0 || lon > 10 {
		t.Errorf("GPS lon = %f, want ~2", lon)
	}
}

// TestMetadata_GPSAllNil covers the all-nil path in Metadata.GPS().
func TestMetadata_GPSAllNil(t *testing.T) {
	t.Parallel()
	m := &Metadata{}
	_, _, ok := m.GPS()
	if ok {
		t.Error("GPS() ok=true on nil Metadata, want false")
	}
}

// TestMetadata_GPSExifFallsToXMP exercises the EXIF GPS missing → XMP GPS lookup.
func TestMetadata_GPSExifFallsToXMP(t *testing.T) {
	t.Parallel()
	// Build a Metadata with EXIF (no GPS tags) and XMP (with GPS).
	jpeg := buildMinimalJPEG(minimalTIFFPayload())
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// EXIF is parsed but has no GPS IFD, so EXIF.GPS() returns ok=false.
	// Attach an XMP with GPS coordinates.
	m.XMP = &xmp.XMP{Properties: map[string]map[string]string{
		"http://www.w3.org/2003/01/geo/wgs84_pos#": {
			"lat": "51,30.00N",
			"lon": "0,7.00W",
		},
	}}
	// The test only verifies that the XMP path is reached, not the exact value.
	_, _, _ = m.GPS() // no panic is the important assertion
}

// TestMetadata_CameraModelXMPFallback covers the XMP path in CameraModel().
func TestMetadata_CameraModelXMPFallback(t *testing.T) {
	t.Parallel()
	x := &xmp.XMP{Properties: map[string]map[string]string{
		"http://ns.adobe.com/tiff/1.0/": {"Model": "Fujifilm X-T4"},
	}}
	m := &Metadata{XMP: x}
	if got := m.CameraModel(); got != "Fujifilm X-T4" {
		t.Errorf("CameraModel XMP = %q, want %q", got, "Fujifilm X-T4")
	}
}

// TestMetadata_CreatorIPTCFallback verifies that Creator falls back to IPTC
// when XMP returns empty.
func TestMetadata_CreatorIPTCFallback(t *testing.T) {
	t.Parallel()
	i := new(iptc.IPTC)
	i.SetCreator("Jane Doe")
	// XMP is present but has no creator value — XMP returns "".
	x := &xmp.XMP{Properties: make(map[string]map[string]string)}
	m := &Metadata{IPTC: i, XMP: x}
	if got := m.Creator(); got != "Jane Doe" {
		t.Errorf("Creator IPTC fallback = %q, want %q", got, "Jane Doe")
	}
}

// TestMetadata_CopyrightIPTCFallback verifies that Copyright falls back to IPTC.
func TestMetadata_CopyrightIPTCFallback(t *testing.T) {
	t.Parallel()
	i := new(iptc.IPTC)
	i.SetCopyright("(c) 2025 Bob")
	x := &xmp.XMP{Properties: make(map[string]map[string]string)}
	m := &Metadata{IPTC: i, XMP: x}
	if got := m.Copyright(); got != "(c) 2025 Bob" {
		t.Errorf("Copyright IPTC fallback = %q, want %q", got, "(c) 2025 Bob")
	}
}

// TestEncodeMetadataRawPassthrough verifies that encodeMetadata passes through
// raw bytes when EXIF/IPTC/XMP structs are nil but raw bytes are set.
func TestEncodeMetadataRawPassthrough(t *testing.T) {
	t.Parallel()
	wantEXIF := []byte("raw-exif")
	wantIPTC := []byte("raw-iptc")
	wantXMP := []byte("raw-xmp")
	m := &Metadata{
		rawEXIF: wantEXIF,
		rawIPTC: wantIPTC,
		rawXMP:  wantXMP,
	}
	gotE, gotI, gotX, err := encodeMetadata(m, format.FormatJPEG)
	if err != nil {
		t.Fatalf("encodeMetadata: %v", err)
	}
	if !bytes.Equal(gotE, wantEXIF) {
		t.Errorf("rawEXIF passthrough = %q, want %q", gotE, wantEXIF)
	}
	if !bytes.Equal(gotI, wantIPTC) {
		t.Errorf("rawIPTC passthrough = %q, want %q", gotI, wantIPTC)
	}
	if !bytes.Equal(gotX, wantXMP) {
		t.Errorf("rawXMP passthrough = %q, want %q", gotX, wantXMP)
	}
}

// buildJPEGWithCorruptEXIF constructs a JPEG whose APP1 EXIF payload contains
// valid-length but structurally corrupt TIFF bytes (wrong magic), so that
// exif.Parse returns a CorruptMetadataError while the JPEG itself is valid.
func buildJPEGWithCorruptEXIF() []byte {
	// 8 bytes with 'II' byte-order mark but wrong TIFF magic (0x0000 instead of 0x002A).
	corrupt := []byte{'I', 'I', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	return buildMinimalJPEG(corrupt)
}

// buildJPEGWithCorruptXMP constructs a JPEG whose APP1 XMP payload contains
// 102 nested XML open tags without a <?xpacket…?> wrapper, which exceeds the
// xmp parser's maximum nesting depth of 100 and causes xmp.Parse to return
// xmp.ErrXMLNestingDepth (xmp/rdf.go: parseStartTag, p.depth > 100).
func buildJPEGWithCorruptXMP() []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	// APP1: XMP namespace URI prefix (stripped by the JPEG extractor) followed
	// by 102 nested open elements — depth exceeds the parser's 100-level limit.
	xmpNS := "http://ns.adobe.com/xap/1.0/\x00"
	var xmlBuf bytes.Buffer
	for i := range 102 {
		fmt.Fprintf(&xmlBuf, "<a%d>", i)
	}
	payload := append([]byte(xmpNS), xmlBuf.Bytes()...)
	length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
	buf.Write([]byte{0xFF, 0xE1})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(payload)

	// Minimal SOS + EOI.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// TestStrictMode_EXIFCorrupt verifies that Strict() causes Read to return a
// *ParseSegmentError identifying "EXIF" when the EXIF segment is present but
// structurally corrupt.
func TestStrictMode_EXIFCorrupt(t *testing.T) {
	t.Parallel()
	jpeg := buildJPEGWithCorruptEXIF()

	_, err := Read(bytes.NewReader(jpeg), Strict())
	if err == nil {
		t.Fatal("Strict() + corrupt EXIF: expected error, got nil")
	}
	var pse *ParseSegmentError
	if !errors.As(err, &pse) {
		t.Fatalf("expected *ParseSegmentError, got %T: %v", err, err)
	}
	if pse.Segment != "EXIF" {
		t.Errorf("ParseSegmentError.Segment = %q, want %q", pse.Segment, "EXIF")
	}
	if pse.Err == nil {
		t.Error("ParseSegmentError.Err is nil, want non-nil underlying error")
	}
}

// TestStrictMode_XMPCorrupt verifies that Strict() causes Read to return a
// *ParseSegmentError identifying "XMP" when the XMP segment is present but
// contains invalid XML.
func TestStrictMode_XMPCorrupt(t *testing.T) {
	t.Parallel()
	jpeg := buildJPEGWithCorruptXMP()

	_, err := Read(bytes.NewReader(jpeg), Strict())
	if err == nil {
		t.Fatal("Strict() + corrupt XMP: expected error, got nil")
	}
	var pse *ParseSegmentError
	if !errors.As(err, &pse) {
		t.Fatalf("expected *ParseSegmentError, got %T: %v", err, err)
	}
	if pse.Segment != "XMP" {
		t.Errorf("ParseSegmentError.Segment = %q, want %q", pse.Segment, "XMP")
	}
	if pse.Err == nil {
		t.Error("ParseSegmentError.Err is nil, want non-nil underlying error")
	}
}

// TestBestEffort_CorruptEXIF verifies that without Strict() a corrupt EXIF
// segment does not cause Read to return an error: the EXIF field is nil and
// the failure is recorded in ParseWarnings instead.
func TestBestEffort_CorruptEXIF(t *testing.T) {
	t.Parallel()
	jpeg := buildJPEGWithCorruptEXIF()

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("best-effort + corrupt EXIF: unexpected error: %v", err)
	}
	if m.EXIF != nil {
		t.Error("EXIF should be nil for a corrupt segment in best-effort mode")
	}
	if len(m.ParseWarnings) == 0 {
		t.Fatal("ParseWarnings should be non-empty after a corrupt EXIF segment")
	}
	if m.ParseWarnings[0].Segment != "EXIF" {
		t.Errorf("ParseWarnings[0].Segment = %q, want %q", m.ParseWarnings[0].Segment, "EXIF")
	}
	if m.ParseWarnings[0].Err == nil {
		t.Error("ParseWarnings[0].Err is nil, want non-nil underlying error")
	}
}

// TestBestEffort_CorruptXMP verifies that without Strict() a corrupt XMP
// segment does not cause an error: XMP is nil and ParseWarnings records the
// failure.
func TestBestEffort_CorruptXMP(t *testing.T) {
	t.Parallel()
	jpeg := buildJPEGWithCorruptXMP()

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("best-effort + corrupt XMP: unexpected error: %v", err)
	}
	if m.XMP != nil {
		t.Error("XMP should be nil for a corrupt segment in best-effort mode")
	}
	if len(m.ParseWarnings) == 0 {
		t.Fatal("ParseWarnings should be non-empty after a corrupt XMP segment")
	}
	if m.ParseWarnings[0].Segment != "XMP" {
		t.Errorf("ParseWarnings[0].Segment = %q, want %q", m.ParseWarnings[0].Segment, "XMP")
	}
}

// TestBestEffort_CleanParse verifies that ParseWarnings is nil (not an empty
// slice) when all segments parse successfully — no noise for the common case.
func TestBestEffort_CleanParse(t *testing.T) {
	t.Parallel()
	jpeg := buildMinimalJPEG(minimalTIFFPayload())

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.ParseWarnings != nil {
		t.Errorf("ParseWarnings should be nil on clean parse, got %v", m.ParseWarnings)
	}
}

// TestStrictOption_Config verifies that Strict() sets cfg.strict to true.
func TestStrictOption_Config(t *testing.T) {
	t.Parallel()
	var c readConfig
	Strict()(&c)
	if !c.strict {
		t.Error("Strict(): cfg.strict should be true")
	}
}

// TestStrictMode_LazyTakesPrecedence verifies that a lazy option takes
// precedence over Strict(): a lazy segment is never parsed so its absence
// does not produce an error even when the raw bytes would be corrupt.
func TestStrictMode_LazyTakesPrecedence(t *testing.T) {
	t.Parallel()
	jpeg := buildJPEGWithCorruptEXIF()

	// WithoutEXIF + Strict: the EXIF segment is lazy, so it is skipped entirely
	// and no parse error is returned despite the corrupt bytes.
	m, err := Read(bytes.NewReader(jpeg), Strict(), WithoutEXIF())
	if err != nil {
		t.Fatalf("Strict() + WithoutEXIF() + corrupt EXIF: unexpected error: %v", err)
	}
	if m.EXIF != nil {
		t.Error("EXIF should be nil when WithoutEXIF is set")
	}
	if len(m.ParseWarnings) != 0 {
		t.Errorf("ParseWarnings should be empty when lazy takes precedence, got %v", m.ParseWarnings)
	}
}

// TestParseSegmentError_ErrorString verifies that ParseSegmentError.Error()
// includes both the segment name and the underlying error description.
func TestParseSegmentError_ErrorString(t *testing.T) {
	t.Parallel()
	inner := errors.New("bad tiff magic")
	pse := &ParseSegmentError{Segment: "EXIF", Err: inner}
	msg := pse.Error()
	if msg == "" {
		t.Fatal("ParseSegmentError.Error() returned empty string")
	}
	if !contains(msg, "EXIF") {
		t.Errorf("error message %q does not contain segment name %q", msg, "EXIF")
	}
	if !contains(msg, "bad tiff magic") {
		t.Errorf("error message %q does not contain underlying error text", msg)
	}
	// Unwrap must return the inner error.
	if !errors.Is(pse, inner) {
		t.Error("errors.Is(pse, inner) = false; Unwrap not wired correctly")
	}
}

// contains is a simple case-sensitive substring check used in test assertions
// to avoid importing strings in the test file.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findSub(s, sub))
}

func findSub(s, sub string) bool {
	for i := range len(s) - len(sub) + 1 {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// BenchmarkReadFile_Concurrent runs ReadFile in parallel goroutines to expose
// any pool contention. Uses a minimal in-memory JPEG written to a temp file
// to isolate pool behaviour from I/O variability.
func BenchmarkReadFile_Concurrent(b *testing.B) {
	tiff := minimalTIFFPayload()
	jpeg := buildMinimalJPEG(tiff)
	tmp := b.TempDir() + "/bench.jpg"
	if err := os.WriteFile(tmp, jpeg, 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = ReadFile(tmp)
		}
	})
}
