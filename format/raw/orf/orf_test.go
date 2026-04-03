package orf

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildORF creates a minimal ORF file: standard TIFF bytes with the ORF magic
// bytes ("IIRO") replacing the standard TIFF marker at bytes 2-3.
func buildORF() []byte {
	buf := make([]byte, 14)
	copy(buf[0:4], orfMagic) // "IIRO"
	binary.LittleEndian.PutUint32(buf[4:], 8)
	// IFD0: 0 entries, next IFD = 0
	return buf
}

func TestExtractHasORFMagic(t *testing.T) {
	data := buildORF()
	if !bytes.HasPrefix(data, orfMagic) {
		t.Fatal("test data does not start with ORF magic")
	}
}

func TestExtractReturnsRawEXIF(t *testing.T) {
	data := buildORF()
	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil, want non-nil patched TIFF payload")
	}
	// The returned rawEXIF should have standard TIFF magic (patched), not ORF magic.
	if len(rawEXIF) >= 4 && rawEXIF[2] == orfMagic[2] && rawEXIF[3] == orfMagic[3] {
		t.Error("rawEXIF still has ORF magic bytes; expected standard TIFF magic 0x2A 0x00")
	}
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil (no IPTC tag in minimal ORF)", rawIPTC)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil (no XMP tag in minimal ORF)", rawXMP)
	}
}

func TestExtractInvalidMagicReturnsError(t *testing.T) {
	data := buildORF()
	data[0] = 'M' // corrupt magic
	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Error("Extract with invalid magic: expected error, got nil")
	}
}

func TestInjectOutputHasORFMagic(t *testing.T) {
	data := buildORF()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, nil, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 4 {
		t.Fatal("Inject output too short")
	}
	// Output must restore ORF magic (bytes 2-3 = "RO").
	if result[2] != orfMagic[2] || result[3] != orfMagic[3] {
		t.Errorf("ORF magic not restored: bytes[2:4] = %#02x %#02x, want %#02x %#02x",
			result[2], result[3], orfMagic[2], orfMagic[3])
	}
}

func TestInjectRoundTrip(t *testing.T) {
	data := buildORF()
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
	data := buildORF()
	data[0] = 'M'
	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, nil, nil, nil)
	if err == nil {
		t.Error("Inject with invalid magic: expected error, got nil")
	}
}
