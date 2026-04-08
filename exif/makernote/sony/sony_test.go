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
	t.Parallel()
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
	t.Parallel()
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

// TestParseSonyIFDEntryOutOfBoundsOffset exercises the out-of-bounds offset
// guard in parseSonyIFDEntry when total > 4 and offset points beyond b.
func TestParseSonyIFDEntryOutOfBoundsOffset(t *testing.T) {
	t.Parallel()
	// LONG, count=8, total=32 > 4 → out-of-line; offset 9999 is beyond len.
	b := make([]byte, 12)
	le := binary.LittleEndian
	le.PutUint16(b[0:], 0x0102) // tag
	le.PutUint16(b[2:], 4)      // type LONG
	le.PutUint32(b[4:], 8)      // count
	le.PutUint32(b[8:], 9999)   // offset beyond buffer
	_, _, ok := parseSonyIFDEntry(b, 0, false)
	if ok {
		t.Error("parseSonyIFDEntry OOB offset should return ok=false")
	}
}

// TestReadU16BigEndianAndLittleEndian covers both branches of readU16/readU32.
func TestReadU16BigEndianAndLittleEndian(t *testing.T) {
	t.Parallel()
	b := []byte{0x12, 0x34}
	if got := readU16(b, true); got != 0x1234 {
		t.Errorf("readU16 BE = 0x%04X, want 0x1234", got)
	}
	if got := readU16(b, false); got != 0x3412 {
		t.Errorf("readU16 LE = 0x%04X, want 0x3412", got)
	}
	b32 := []byte{0x01, 0x02, 0x03, 0x04}
	if got := readU32(b32, true); got != 0x01020304 {
		t.Errorf("readU32 BE = 0x%08X, want 0x01020304", got)
	}
	if got := readU32(b32, false); got != 0x04030201 {
		t.Errorf("readU32 LE = 0x%08X, want 0x04030201", got)
	}
}

// TestParseSonyIFDEntryUnknownType verifies that parseSonyIFDEntry returns
// ok=false when the type code is unknown (typeSize16 returns 0).
func TestParseSonyIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	le := binary.LittleEndian
	le.PutUint16(buf[0:], 0x0102) // tag
	le.PutUint16(buf[2:], 0xFF)   // unknown type
	le.PutUint32(buf[4:], 1)      // count
	_, _, ok := parseSonyIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParseRawIFDFewerThanTwoResults verifies that parseRawIFD returns nil
// when only one valid entry is found (len(result) < 2).
func TestParseRawIFDFewerThanTwoResults(t *testing.T) {
	t.Parallel()
	// Build IFD with 2 entries but second has unknown type → only 1 valid result.
	buf := make([]byte, 2+2*12)
	le := binary.LittleEndian
	le.PutUint16(buf[0:], 2) // 2 entries

	// Entry 0: valid SHORT
	le.PutUint16(buf[2:], TagQuality)
	le.PutUint16(buf[4:], 3) // SHORT
	le.PutUint32(buf[6:], 1)
	le.PutUint32(buf[10:], 42)

	// Entry 1: unknown type → parseSonyIFDEntry returns ok=false
	le.PutUint16(buf[14:], TagSonyModelID)
	le.PutUint16(buf[16:], 0xFF) // unknown type
	le.PutUint32(buf[18:], 1)
	le.PutUint32(buf[22:], 0)

	result := parseRawIFD(buf, false)
	if result != nil {
		t.Errorf("expected nil when < 2 valid entries, got %v", result)
	}
}

// TestParseSonyIFDBEFallback verifies that parseSonyIFD falls back to
// big-endian parsing when little-endian fails.
func TestParseSonyIFDBEFallback(t *testing.T) {
	t.Parallel()
	// Build a valid IFD in big-endian byte order with 2 entries.
	buf := make([]byte, 2+2*12)
	be := binary.BigEndian
	be.PutUint16(buf[0:], 2) // 2 entries

	// Entry 0: TagQuality, SHORT
	be.PutUint16(buf[2:], TagQuality)
	be.PutUint16(buf[4:], 3) // SHORT
	be.PutUint32(buf[6:], 1)
	be.PutUint32(buf[10:], 2)

	// Entry 1: TagSonyModelID, LONG
	be.PutUint16(buf[14:], TagSonyModelID)
	be.PutUint16(buf[16:], 4) // LONG
	be.PutUint32(buf[18:], 1)
	be.PutUint32(buf[22:], 0x0310)

	// LE parse of this buffer: count = 0x0002 LE = 512, which is > 1024? No,
	// 512 < 1024. But 2+512*12=6146 > len(buf)=26 → parseRawIFD LE returns nil.
	// BE parse: count=2, entries valid → returns result.
	result := parseSonyIFD(buf)
	if result == nil {
		t.Fatal("expected non-nil result from BE fallback, got nil")
	}
	if _, ok := result[TagQuality]; !ok {
		t.Error("TagQuality not found in BE-parsed result")
	}
}

// TestParseSonyIFDBothOrdersFail verifies that parseSonyIFD returns nil when
// both LE and BE parses fail (e.g., the buffer is too short for any valid IFD).
func TestParseSonyIFDBothOrdersFail(t *testing.T) {
	t.Parallel()
	// A 2-byte buffer encoding count=1: 2+1*12=14 > 2, so both LE and BE fail.
	buf := []byte{0x01, 0x00}
	result := parseSonyIFD(buf)
	if result != nil {
		t.Errorf("expected nil when both orders fail, got %v", result)
	}
}

// TestSonyTypeSize16AllBranches exercises every branch of typeSize16.
func TestSonyTypeSize16AllBranches(t *testing.T) {
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
