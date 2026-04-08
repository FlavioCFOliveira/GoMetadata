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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestNikonTypeSize16AllBranches exercises every branch of typeSize16.
func TestNikonTypeSize16AllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ  uint16
		want uint32
	}{
		{1, 1}, {2, 1}, {6, 1}, {7, 1},
		{3, 2}, {8, 2},
		{4, 4}, {9, 4}, {11, 4},
		{5, 8}, {10, 8}, {12, 8},
		{0, 0}, {99, 0},
	}
	for _, tc := range tests {
		if got := typeSize16(tc.typ); got != tc.want {
			t.Errorf("typeSize16(%d) = %d, want %d", tc.typ, got, tc.want)
		}
	}
}

// TestParseNikonIFDEntryOutOfBoundsOffset verifies that parseNikonIFDEntry
// rejects an out-of-bounds offset value.
// TestParseType3BigEndian exercises the big-endian branch of parseType3.
func TestParseType3BigEndian(t *testing.T) {
	t.Parallel()
	// Build a minimal BE embedded TIFF: MM + magic(0x002A) + IFD offset(8)
	// followed by a 1-entry IFD with a SHORT tag inline.
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'M', 'M'
	binary.BigEndian.PutUint16(buf[2:], 0x002A)
	binary.BigEndian.PutUint32(buf[4:], 8)
	binary.BigEndian.PutUint16(buf[8:], 1)       // 1 entry
	binary.BigEndian.PutUint16(buf[10:], TagISO) // tag
	binary.BigEndian.PutUint16(buf[12:], 3)      // SHORT
	binary.BigEndian.PutUint32(buf[14:], 1)      // count
	binary.BigEndian.PutUint32(buf[18:], 400)    // value inline
	binary.BigEndian.PutUint32(buf[22:], 0)      // next IFD
	result := parseType3(buf)
	if result == nil {
		t.Error("parseType3 BE: expected non-nil result")
	}
}

// TestParseType3BadMagic exercises the bad magic and bad byte-order branches.
func TestParseType3BadMagic(t *testing.T) {
	t.Parallel()
	t.Run("bad TIFF magic", func(t *testing.T) {
		t.Parallel()
		buf := make([]byte, 8)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x00FF) // wrong magic
		if got := parseType3(buf); got != nil {
			t.Error("parseType3 bad magic: expected nil")
		}
	})
	t.Run("bad byte order", func(t *testing.T) {
		t.Parallel()
		buf := make([]byte, 8)
		buf[0], buf[1] = 'X', 'X' // neither II nor MM
		if got := parseType3(buf); got != nil {
			t.Error("parseType3 bad byte order: expected nil")
		}
	})
	t.Run("too short", func(t *testing.T) {
		t.Parallel()
		if got := parseType3([]byte{0x00, 0x01}); got != nil {
			t.Error("parseType3 too short: expected nil")
		}
	})
}

func TestParseNikonIFDEntryOutOfBoundsOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	le := binary.LittleEndian
	le.PutUint16(buf[0:], 0x0002) // tag
	le.PutUint16(buf[2:], 2)      // ASCII
	le.PutUint32(buf[4:], 100)    // count=100 → total>4
	le.PutUint32(buf[8:], 0xFFFF) // offset OOB
	_, _, ok := parseNikonIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for OOB offset")
	}
}

// TestParseNikonIFDEntryUnknownType verifies that parseNikonIFDEntry returns
// ok=false when the type code is unknown (typeSize16 returns 0).
func TestParseNikonIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0002) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parseNikonIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParseIFDAtNikonAllUnknownTypes verifies that parseIFDAt returns nil when
// all entries have unknown type codes (empty result → returns nil).
func TestParseIFDAtNikonAllUnknownTypes(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2+12)
	binary.LittleEndian.PutUint16(buf[0:], 1)      // count=1
	binary.LittleEndian.PutUint16(buf[2:], 0x0002) // tag
	binary.LittleEndian.PutUint16(buf[4:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[6:], 1)      // count
	result := parseIFDAt(buf, 0, false)
	if result != nil {
		t.Errorf("expected nil for all-unknown-type IFD, got %v", result)
	}
}

// TestParseIFDAtNikonCountTooHigh verifies that parseIFDAt returns nil when
// the entry count exceeds the 512-entry sanity limit.
func TestParseIFDAtNikonCountTooHigh(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 600) // count=600 > 512
	result := parseIFDAt(buf, 0, false)
	if result != nil {
		t.Errorf("expected nil for count=600, got %v", result)
	}
}

// TestParseIFDAtNikonEntriesBeyondBuffer verifies that parseIFDAt returns nil
// when the entry block extends beyond the buffer.
func TestParseIFDAtNikonEntriesBeyondBuffer(t *testing.T) {
	t.Parallel()
	// count=5: needs 2+5*12=62 bytes, but buf is only 2 bytes.
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 5)
	result := parseIFDAt(buf, 0, false)
	if result != nil {
		t.Errorf("expected nil for entries beyond buffer, got %v", result)
	}
}
