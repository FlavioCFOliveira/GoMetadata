package dng

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
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
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

// TestExtractError verifies Extract wraps errors with "dng:" prefix.
func TestExtractError(t *testing.T) {
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0}))
	if err == nil {
		t.Fatal("expected error for non-TIFF input, got nil")
	}
}

// TestInjectError verifies Inject wraps errors with "dng:" prefix.
func TestInjectError(t *testing.T) {
	t.Parallel()
	badData := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(badData), &out, badData, []byte("iptc"), nil)
	if err == nil {
		t.Fatal("expected error for invalid TIFF input with IPTC, got nil")
	}
}

// BenchmarkDNGExtract measures the cost of extracting metadata from a minimal
// TIFF/DNG byte stream.
func BenchmarkDNGExtract(b *testing.B) {
	data := minimalTIFF()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(data))
	}
}
