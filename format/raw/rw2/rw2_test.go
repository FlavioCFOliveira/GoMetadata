package rw2

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildRW2 creates a minimal RW2 file: standard TIFF bytes with the RW2 magic
// bytes ("IIU\x00") replacing the standard TIFF marker at bytes 2-3.
func buildRW2() []byte {
	buf := make([]byte, 14)
	copy(buf[0:4], rw2Magic) // "IIU\x00"
	binary.LittleEndian.PutUint32(buf[4:], 8)
	// IFD0: 0 entries, next IFD = 0
	return buf
}

func TestExtractHasRW2Magic(t *testing.T) {
	data := buildRW2()
	if !bytes.HasPrefix(data, rw2Magic) {
		t.Fatal("test data does not start with RW2 magic")
	}
}

func TestExtractReturnsRawEXIF(t *testing.T) {
	data := buildRW2()
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil, want non-nil patched TIFF payload")
	}
	// The returned rawEXIF should have standard TIFF magic (patched), not RW2 magic.
	if len(rawEXIF) >= 4 && rawEXIF[2] == rw2Magic[2] && rawEXIF[3] == rw2Magic[3] {
		t.Error("rawEXIF still has RW2 magic bytes; expected standard TIFF magic 0x2A 0x00")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil (no IPTC tag in minimal RW2)", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil (no XMP tag in minimal RW2)", rawXMP)
	}
}

func TestExtractInvalidMagicReturnsError(t *testing.T) {
	data := buildRW2()
	data[0] = 'M' // corrupt magic
	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("Extract with invalid magic: expected error, got nil")
	}
}

func TestInjectOutputHasRW2Magic(t *testing.T) {
	data := buildRW2()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("Inject output too short")
	}
	// Output must restore RW2 magic (bytes 2-3 = "U\x00").
	if result[2] != rw2Magic[2] || result[3] != rw2Magic[3] {
		t.Errorf("RW2 magic not restored: bytes[2:4] = %#02x %#02x, want %#02x %#02x",
			result[2], result[3], rw2Magic[2], rw2Magic[3])
	}
}

func TestInjectRoundTrip(t *testing.T) {
	data := buildRW2()
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

func TestInjectInvalidMagicReturnsError(t *testing.T) {
	data := buildRW2()
	data[0] = 'M'
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, nil, nil)
	if err == nil {
		t.Error("Inject with invalid magic: expected error, got nil")
	}
}
