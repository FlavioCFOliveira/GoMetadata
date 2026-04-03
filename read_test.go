package imgmetadata

import (
	"bytes"
	"encoding/binary"
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
