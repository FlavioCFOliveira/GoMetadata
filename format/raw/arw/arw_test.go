package arw

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// minimalTIFF builds a bare-minimum little-endian TIFF stream (IFD0 with 0 entries).
func minimalTIFF() []byte {
	buf := make([]byte, 14)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 8)
	// IFD0: 0 entries, next IFD = 0
	return buf
}

func TestExtractReturnsRawEXIF(t *testing.T) {
	t.Parallel()
	data := minimalTIFF()
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil, want non-nil TIFF payload")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil (no IPTC in minimal TIFF)", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil (no XMP in minimal TIFF)", rawXMP)
	}
}

func TestInjectRoundTrip(t *testing.T) {
	t.Parallel()
	data := minimalTIFF()

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil after round-trip")
	}
}

// BenchmarkARWExtract measures the cost of extracting metadata from a minimal
// TIFF/ARW byte stream.
func BenchmarkARWExtract(b *testing.B) {
	data := minimalTIFF()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}
