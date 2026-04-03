package imgmetadata

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/flaviocfo/img-metadata/format"
)

// buildMinimalJPEG constructs a minimal JPEG stream with an optional EXIF APP1
// segment so that Read() can detect and parse it.
func buildMinimalJPEG(exifData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	if exifData != nil {
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2)
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

	order.PutUint16(buf[8:], 1)           // 1 entry
	order.PutUint16(buf[10:], 0x010E)     // ImageDescription tag
	order.PutUint16(buf[12:], 2)          // ASCII
	order.PutUint32(buf[14:], 4)          // count = 4
	copy(buf[18:], []byte("test"))        // inline value
	order.PutUint32(buf[22:], 0)          // next IFD = 0
	return buf
}

func TestRawAccessors(t *testing.T) {
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
	// Feed random bytes that don't match any known magic.
	_, err := Read(bytes.NewReader([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x00}))
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	if _, ok := err.(*UnsupportedFormatError); !ok {
		t.Errorf("expected *UnsupportedFormatError, got %T: %v", err, err)
	}
}

func TestNewMetadata(t *testing.T) {
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
	writable := []format.FormatID{
		format.FormatJPEG, format.FormatTIFF, format.FormatPNG, format.FormatHEIF,
		format.FormatWebP, format.FormatCR2, format.FormatCR3, format.FormatNEF,
		format.FormatARW, format.FormatDNG, format.FormatORF, format.FormatRW2,
	}
	for _, f := range writable {
		if !format.SupportsWrite(f) {
			t.Errorf("SupportsWrite(%v) = false, want true", f)
		}
	}
	if format.SupportsWrite(format.FormatUnknown) {
		t.Error("SupportsWrite(FormatUnknown) = true, want false")
	}
}

// TestReadFileNotFound verifies that ReadFile propagates the OS "not found"
// error unchanged so callers can use os.IsNotExist.
func TestReadFileNotFound(t *testing.T) {
	_, err := ReadFile("/nonexistent/definitely-does-not-exist/file.jpg")
	if err == nil {
		t.Fatal("expected error for non-existent path, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("os.IsNotExist(err) = false; err = %v", err)
	}
}

// TestReadFilePermDenied verifies that ReadFile propagates the OS "permission
// denied" error so callers can use os.IsPermission.
func TestReadFilePermDenied(t *testing.T) {
	// Skip when running as root because root can read any file.
	if os.Getuid() == 0 {
		t.Skip("running as root: permission checks are not enforced")
	}

	f, err := os.CreateTemp("", "imgmetadata-perm-test-denied-*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}

	_, err = ReadFile(path)
	if err == nil {
		t.Fatal("expected permission error, got nil")
	}
	if !os.IsPermission(err) {
		t.Errorf("os.IsPermission(err) = false; err = %v", err)
	}
}

func TestWriteFilePreservesPermissions(t *testing.T) {
	// Write a minimal JPEG to a temp file with mode 0644, then WriteFile it and
	// assert the mode is preserved after the atomic rename.
	jpeg := buildMinimalJPEG(minimalTIFFPayload())

	f, err := os.CreateTemp("", "imgmetadata-perm-test-*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	defer os.Remove(path)

	if _, err := f.Write(jpeg); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

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
