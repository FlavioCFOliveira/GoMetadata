package nikon

import (
	"encoding/binary"
	"testing"
)

// buildNikonType1IFD creates a minimal Nikon Type 1 MakerNote IFD (big-endian, IFD at offset 0).
func buildNikonType1IFD() []byte {
	// 1 entry: TagISO (0x0002), type SHORT (3), count 1, value 400.
	buf := make([]byte, 2+1*12)
	binary.BigEndian.PutUint16(buf[0:], 1)      // entry count = 1
	binary.BigEndian.PutUint16(buf[2:], TagISO) // tag
	binary.BigEndian.PutUint16(buf[4:], 3)      // type SHORT
	binary.BigEndian.PutUint32(buf[6:], 1)      // count
	binary.BigEndian.PutUint32(buf[10:], 400)   // value 400
	return buf
}

// buildNikonType3 creates a minimal Nikon Type 3 MakerNote with an embedded
// little-endian TIFF containing one entry.
func buildNikonType3() []byte {
	// Type 3 layout:
	//   [0..5]  "Nikon\0"
	//   [6..7]  version (0x02 0x10)
	//   [8..]   embedded TIFF: "II" + 0x2A00 + IFD offset(4) + count(2) + 1*12-byte entry + next(4)
	const tiffBase = 8
	// Embedded TIFF: header(8) + IFD at offset 8.
	// IFD: count(2) + 1 entry(12) + next(4)
	ifd := make([]byte, 2+12+4)
	le := binary.LittleEndian
	le.PutUint16(ifd[0:], 1)               // 1 entry
	le.PutUint16(ifd[2:], TagShutterCount) // tag
	le.PutUint16(ifd[4:], 4)               // LONG
	le.PutUint32(ifd[6:], 1)               // count
	le.PutUint32(ifd[10:], 5000)           // value: 5000 shutter actuations
	// next IFD = 0
	_ = tiffBase

	tiffHdr := make([]byte, 0, 8+len(ifd))
	tiffHdr = append(tiffHdr, 'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00)
	tiffHdr = append(tiffHdr, ifd...)
	prefix := make([]byte, 0, 8+len(tiffHdr))
	prefix = append(prefix, 'N', 'i', 'k', 'o', 'n', 0x00, 0x02, 0x10)
	return append(prefix, tiffHdr...)
}

func TestParseType1(t *testing.T) {
	b := buildNikonType1IFD()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse Type1: %v", err)
	}
	if result == nil {
		t.Fatal("Parse Type1: got nil, want non-nil map")
	}
	if _, ok := result[TagISO]; !ok {
		t.Errorf("TagISO (0x%04X) not found in Type1 result", TagISO)
	}
}

func TestParseType3(t *testing.T) {
	b := buildNikonType3()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse Type3: %v", err)
	}
	if result == nil {
		t.Fatal("Parse Type3: got nil, want non-nil map")
	}
	if _, ok := result[TagShutterCount]; !ok {
		t.Errorf("TagShutterCount (0x%04X) not found in Type3 result", TagShutterCount)
	}
}

func TestParseTooShortReturnsNil(t *testing.T) {
	p := Parser{}
	result, err := p.Parse([]byte{0x00})
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
	if TagMakerNoteVersion != 0x0001 {
		t.Errorf("TagMakerNoteVersion = 0x%04X, want 0x0001", TagMakerNoteVersion)
	}
	if TagISO != 0x0002 {
		t.Errorf("TagISO = 0x%04X, want 0x0002", TagISO)
	}
	if TagShutterCount != 0x00A7 {
		t.Errorf("TagShutterCount = 0x%04X, want 0x00A7", TagShutterCount)
	}
}
