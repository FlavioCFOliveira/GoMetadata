package sony

import (
	"encoding/binary"
	"testing"
)

// buildSonyIFD creates a minimal Sony MakerNote IFD (little-endian, plain IFD at offset 0).
// Sony requires at least 2 entries for successful parse.
func buildSonyIFD() []byte {
	// 2 entries: TagQuality + TagSonyModelID — all values inline (SHORT / LONG).
	buf := make([]byte, 2+2*12)
	le := binary.LittleEndian
	le.PutUint16(buf[0:], 2) // 2 entries

	// Entry 0: TagQuality (0x0102), SHORT, value = 2
	le.PutUint16(buf[2:], TagQuality)
	le.PutUint16(buf[4:], 3) // SHORT
	le.PutUint32(buf[6:], 1) // count
	le.PutUint32(buf[10:], 2)

	// Entry 1: TagSonyModelID (0x7001), LONG, value = 0x0310
	le.PutUint16(buf[14:], TagSonyModelID)
	le.PutUint16(buf[16:], 4) // LONG
	le.PutUint32(buf[18:], 1) // count
	le.PutUint32(buf[22:], 0x0310)

	return buf
}

func TestParseValidIFD(t *testing.T) {
	b := buildSonyIFD()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result == nil {
		t.Fatal("Parse returned nil, want non-nil map")
	}
	if _, ok := result[TagQuality]; !ok {
		t.Errorf("TagQuality (0x%04X) not found in result", TagQuality)
	}
	if _, ok := result[TagSonyModelID]; !ok {
		t.Errorf("TagSonyModelID (0x%04X) not found in result", TagSonyModelID)
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

func TestTagConstants(t *testing.T) {
	if TagQuality != 0x0102 {
		t.Errorf("TagQuality = 0x%04X, want 0x0102", TagQuality)
	}
	if TagSonyModelID != 0x7001 {
		t.Errorf("TagSonyModelID = 0x%04X, want 0x7001", TagSonyModelID)
	}
	if TagLensType != 0xB027 {
		t.Errorf("TagLensType = 0x%04X, want 0xB027", TagLensType)
	}
}
