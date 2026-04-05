package canon

import (
	"encoding/binary"
	"testing"
)

// buildCanonIFD creates a minimal Canon MakerNote IFD with n entries.
// The IFD has no pointer-based (offset) values — all values fit inline.
func buildCanonIFD(entries []struct {
	tag, typ uint16
	val      uint32
}) []byte {
	n := len(entries)
	// IFD: count(2) + n*12 bytes entries (no next-IFD pointer in MakerNote)
	buf := make([]byte, 2+n*12)
	le := binary.LittleEndian
	le.PutUint16(buf[0:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast
	for i, e := range entries {
		p := 2 + i*12
		le.PutUint16(buf[p:], e.tag)
		le.PutUint16(buf[p+2:], e.typ)
		le.PutUint32(buf[p+4:], 1) // count = 1
		le.PutUint32(buf[p+8:], e.val)
	}
	return buf
}

func TestParseValidIFD(t *testing.T) {
	// Build a Canon MakerNote with 3 entries (minimum for tryParseIFD to succeed).
	entries := []struct {
		tag, typ uint16
		val      uint32
	}{
		{TagCameraSettings, 3, 0x0001}, // SHORT
		{TagModelID, 4, 0x80000010},    // LONG
		{TagColorSpace, 3, 0x0001},     // SHORT
	}
	b := buildCanonIFD(entries)

	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result == nil {
		t.Fatal("Parse returned nil, want non-nil map")
	}
	if _, ok := result[TagModelID]; !ok {
		t.Errorf("TagModelID (0x%04X) not found in result", TagModelID)
	}
}

func TestParseTooShortReturnsNil(t *testing.T) {
	p := Parser{}
	result, err := p.Parse([]byte{0x01})
	if err != nil {
		t.Fatalf("Parse short: unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("Parse short: got %v, want nil", result)
	}
}

func TestParseEmptyReturnsNil(t *testing.T) {
	p := Parser{}
	result, err := p.Parse([]byte{})
	if err != nil {
		t.Fatalf("Parse empty: unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("Parse empty: got %v, want nil", result)
	}
}

func TestParseCorruptCountReturnsNil(t *testing.T) {
	// Entry count > 512 should be rejected.
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, 600)
	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("Parse corrupt count: unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("Parse corrupt count: got %v, want nil", result)
	}
}

func TestTagConstants(t *testing.T) {
	// Spot-check a few well-known Canon tag values from ExifTool Canon.pm.
	if TagCameraSettings != 0x0001 {
		t.Errorf("TagCameraSettings = 0x%04X, want 0x0001", TagCameraSettings)
	}
	if TagModelID != 0x001C {
		t.Errorf("TagModelID = 0x%04X, want 0x001C", TagModelID)
	}
	if TagLensModel != 0x0095 {
		t.Errorf("TagLensModel = 0x%04X, want 0x0095", TagLensModel)
	}
	if TagColorData != 0x4001 {
		t.Errorf("TagColorData = 0x%04X, want 0x4001", TagColorData)
	}
}
