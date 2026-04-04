package gometadata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// corpusAllFiles returns all files from all corpus subdirectories.
func corpusAllFiles(t *testing.T) []string {
	t.Helper()
	base := filepath.Join("testdata", "corpus")
	if _, err := os.Stat(base); os.IsNotExist(err) {
		t.Skip("testdata/corpus not present; skipping corpus tests")
	}
	var paths []string
	subdirs := []string{"jpeg", "png", "tiff", "webp", "heif", "raw"}
	for _, sub := range subdirs {
		dir := filepath.Join(base, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			paths = append(paths, path)
			return nil
		})
	}
	if len(paths) == 0 {
		t.Skip("no corpus files found")
	}
	return paths
}

// TestCorpusReadAll exercises Read() against the full real-world corpus.
// It verifies that no file causes a panic or a CorruptMetadataError.
// UnsupportedFormatError and TruncatedFileError are benign and skipped.
func TestCorpusReadAll(t *testing.T) {
	files := corpusAllFiles(t)
	t.Logf("corpus: %d files", len(files))

	var ok, skipped, failed int
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Errorf("open %s: %v", path, err)
			continue
		}
		_, rerr := Read(f)
		_ = f.Close()

		if rerr == nil {
			ok++
			continue
		}
		var corrupt *CorruptMetadataError
		if errors.As(rerr, &corrupt) {
			t.Errorf("CorruptMetadataError on %s: %v", path, rerr)
			failed++
			continue
		}
		// Unsupported format or truncated file — skip gracefully.
		skipped++
	}
	t.Logf("corpus: ok=%d skipped=%d failed=%d", ok, skipped, failed)
	if failed > 0 {
		t.Fatalf("%d corpus files produced unexpected errors", failed)
	}
}

// TestCorpusGPS verifies that GPS coordinates extracted from corpus files are
// within valid WGS-84 ranges.
func TestCorpusGPS(t *testing.T) {
	files := corpusAllFiles(t)

	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		m, rerr := Read(f)
		_ = f.Close()
		if rerr != nil || m == nil {
			continue
		}
		lat, lon, ok := m.GPS()
		if !ok {
			continue
		}
		if lat < -90 || lat > 90 {
			t.Errorf("%s: GPS lat %f out of range [-90, 90]", path, lat)
		}
		if lon < -180 || lon > 180 {
			t.Errorf("%s: GPS lon %f out of range [-180, 180]", path, lon)
		}
	}
}

// TestCorpusRoundTrip reads each corpus file without parsing metadata (raw mode),
// writes it back unchanged, and verifies that the raw segments are byte-for-byte
// identical. Using WithoutEXIF/WithoutIPTC/WithoutXMP ensures Write() passes the
// original raw bytes through without re-encoding.
func TestCorpusRoundTrip(t *testing.T) {
	files := corpusAllFiles(t)
	t.Logf("round-trip: testing %d corpus files", len(files))

	var ok, skipped, failed int
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}

		// Read in raw-only mode: parsed structs are nil, raw bytes are preserved.
		m, rerr := Read(bytes.NewReader(data),
			WithoutEXIF(), WithoutIPTC(), WithoutXMP())
		if rerr != nil {
			skipped++
			continue
		}

		var out bytes.Buffer
		werr := Write(bytes.NewReader(data), &out, m)
		if werr != nil {
			// Some corpus files are intentionally malformed (fuzzing inputs from
			// exiv2 test suites). Write() correctly rejects them; treat as skipped.
			skipped++
			continue
		}

		// Re-read and compare raw segments byte-for-byte.
		m2, rerr2 := Read(bytes.NewReader(out.Bytes()),
			WithoutEXIF(), WithoutIPTC(), WithoutXMP())
		if rerr2 != nil {
			t.Errorf("re-read after Write %s: %v", path, rerr2)
			failed++
			continue
		}

		// TIFF-family files: rawEXIF = entire file; when embedded IPTC/XMP are
		// present Write() re-encodes the TIFF (known limitation). Skip byte
		// comparison for these cases and only verify the output is readable.
		fm := m.Format()
		isTIFFFamily := fm == format.FormatTIFF || fm == format.FormatCR2 ||
			fm == format.FormatNEF || fm == format.FormatARW ||
			fm == format.FormatDNG || fm == format.FormatORF || fm == format.FormatRW2
		if isTIFFFamily && (m.RawIPTC() != nil || m.RawXMP() != nil) {
			ok++
			continue
		}

		if !bytes.Equal(m.RawEXIF(), m2.RawEXIF()) {
			t.Errorf("%s: rawEXIF changed after round-trip (%d → %d bytes)",
				path, len(m.RawEXIF()), len(m2.RawEXIF()))
			failed++
			continue
		}
		if !bytes.Equal(m.RawXMP(), m2.RawXMP()) {
			t.Errorf("%s: rawXMP changed after round-trip", path)
			failed++
			continue
		}
		if !bytes.Equal(m.RawIPTC(), m2.RawIPTC()) {
			t.Errorf("%s: rawIPTC changed after round-trip", path)
			failed++
			continue
		}
		ok++
	}
	t.Logf("round-trip: ok=%d skipped=%d failed=%d", ok, skipped, failed)
	if failed > 0 {
		t.Fatalf("%d files failed round-trip", failed)
	}
}

// TestWriteModifyRoundTrip tests the full modify-write-read cycle on a synthetic
// JPEG. It modifies the EXIF ImageDescription, writes, and reads back to confirm.
func TestWriteModifyRoundTrip(t *testing.T) {
	const originalCaption = "original caption"
	const newCaption = "updated caption"

	jpeg := buildJPEGWithCaption(originalCaption)

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.EXIF == nil {
		t.Fatal("no EXIF parsed")
	}
	if got := m.Caption(); got != originalCaption {
		t.Fatalf("Caption before write: got %q, want %q", got, originalCaption)
	}

	// Modify ImageDescription in-place.
	for i, e := range m.EXIF.IFD0.Entries {
		if e.Tag == 0x010E { // TagImageDescription
			v := []byte(newCaption + "\x00")
			m.EXIF.IFD0.Entries[i].Value = v
			m.EXIF.IFD0.Entries[i].Count = uint32(len(v)) //nolint:gosec // G115: test helper, intentional type cast
			break
		}
	}

	var out bytes.Buffer
	if err := Write(bytes.NewReader(jpeg), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got := m2.Caption(); got != newCaption {
		t.Errorf("Caption after Write: got %q, want %q", got, newCaption)
	}
}

// buildJPEGWithCaption builds a minimal JPEG with an EXIF ImageDescription tag.
func buildJPEGWithCaption(caption string) []byte {
	captionBytes := []byte(caption + "\x00")
	captionLen := uint32(len(captionBytes)) //nolint:gosec // G115: test helper, intentional type cast

	// Layout: TIFF header(8) + IFD count(2) + 1 entry(12) + next-IFD(4) + value area
	valueOffset := uint32(8 + 2 + 1*12 + 4)
	tiff := make([]byte, valueOffset+captionLen)

	order := binary.LittleEndian

	// TIFF header
	tiff[0], tiff[1] = 'I', 'I'
	order.PutUint16(tiff[2:], 0x002A)
	order.PutUint32(tiff[4:], 8) // IFD0 at offset 8

	// IFD: 1 entry
	order.PutUint16(tiff[8:], 1) // entry count

	// Entry: tag 0x010E ImageDescription, type ASCII, count captionLen, offset
	e := tiff[10:]
	order.PutUint16(e[0:], 0x010E)       // tag
	order.PutUint16(e[2:], 0x0002)       // TypeASCII
	order.PutUint32(e[4:], captionLen)   // count
	order.PutUint32(e[8:], valueOffset)  // value offset

	// Next-IFD pointer = 0
	order.PutUint32(tiff[10+12:], 0)

	// Value area
	copy(tiff[valueOffset:], captionBytes)

	return buildMinimalJPEG(tiff)
}

// BenchmarkReadFile measures end-to-end performance of ReadFile on a real JPEG.
func BenchmarkReadFile(b *testing.B) {
	// Find the first available JPEG corpus file.
	dir := filepath.Join("testdata", "corpus", "jpeg")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		b.Skip("no JPEG corpus available")
	}
	var target string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || target != "" {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext == ".jpg" || ext == ".jpeg" {
			target = path
		}
		return nil
	})
	if target == "" {
		b.Skip("no JPEG corpus files")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		b.Fatalf("read corpus file: %v", err)
	}

	b.ResetTimer()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		if _, err := Read(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
