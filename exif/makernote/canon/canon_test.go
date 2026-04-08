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
	t.Parallel()
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

func TestParseCorruptCountReturnsNil(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// TestTypeSize16AllBranches exercises every branch of typeSize16.
func TestTypeSize16AllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		typ  uint16
		want uint32
	}{
		{1, 1},  // BYTE
		{2, 1},  // ASCII
		{6, 1},  // SBYTE
		{7, 1},  // UNDEFINED
		{3, 2},  // SHORT
		{8, 2},  // SSHORT
		{4, 4},  // LONG
		{9, 4},  // SLONG
		{11, 4}, // FLOAT
		{5, 8},  // RATIONAL
		{10, 8}, // SRATIONAL
		{12, 8}, // DOUBLE
		{0, 0},  // unknown
		{99, 0}, // unknown
	}
	for _, tc := range tests {
		if got := typeSize16(tc.typ); got != tc.want {
			t.Errorf("typeSize16(%d) = %d, want %d", tc.typ, got, tc.want)
		}
	}
}

// TestParseCanonIFDEntryOutOfBoundsOffset verifies that parseCanonIFDEntry
// rejects an out-of-bounds offset.
func TestParseCanonIFDEntryOutOfBoundsOffset(t *testing.T) {
	t.Parallel()
	// Build an IFD entry whose value count > 4 bytes but offset points past end.
	buf := make([]byte, 12)
	le := binary.LittleEndian
	le.PutUint16(buf[0:], 0x0001) // tag
	le.PutUint16(buf[2:], 2)      // type ASCII (1 byte each)
	le.PutUint32(buf[4:], 100)    // count = 100 → total 100 bytes, > 4
	le.PutUint32(buf[8:], 0xFFFF) // offset way beyond end of buf
	_, _, ok := parseCanonIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for out-of-bounds offset")
	}
}

// TestParseCanonIFDEntryUnknownType verifies that parseCanonIFDEntry returns
// ok=false when the type code is unknown (typeSize16 returns 0).
func TestParseCanonIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0001) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parseCanonIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestTryParseIFDCountTooHigh verifies that tryParseIFD returns nil when the
// entry count exceeds the 512-entry sanity limit.
func TestTryParseIFDCountTooHigh(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 600) // count=600 > 512
	result := tryParseIFD(buf, false)
	if result != nil {
		t.Errorf("expected nil for count=600, got %v", result)
	}
}

// TestTryParseIFDAllUnknownTypes verifies that tryParseIFD returns nil when
// all entries have unknown type codes (len(result) < 2 → returns nil).
func TestTryParseIFDAllUnknownTypes(t *testing.T) {
	t.Parallel()
	// 1 entry with unknown type — result will be empty → len < 2 → nil.
	buf := make([]byte, 2+12)
	binary.LittleEndian.PutUint16(buf[0:], 1)      // count=1
	binary.LittleEndian.PutUint16(buf[2:], 0x0001) // tag
	binary.LittleEndian.PutUint16(buf[4:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[6:], 1)      // count
	result := tryParseIFD(buf, false)
	if result != nil {
		t.Errorf("expected nil for all-unknown-type IFD, got %v", result)
	}
}

// TestTryParseIFDEntriesBeyondBuffer verifies that tryParseIFD returns nil
// when the entry block extends beyond the buffer.
func TestTryParseIFDEntriesBeyondBuffer(t *testing.T) {
	t.Parallel()
	// count=5: needs 2+5*12=62 bytes, but buf is only 2 bytes.
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 5)
	result := tryParseIFD(buf, false)
	if result != nil {
		t.Errorf("expected nil for entries beyond buffer, got %v", result)
	}
}

// TestCanonRead32BigEndian covers the big-endian branch of canonRead32.
func TestCanonRead32BigEndian(t *testing.T) {
	t.Parallel()
	b := []byte{0x01, 0x02, 0x03, 0x04}
	want := uint32(0x01020304)
	if got := canonRead32(b, true); got != want {
		t.Errorf("canonRead32 BE = 0x%08X, want 0x%08X", got, want)
	}
	// little-endian: reversed interpretation
	wantLE := uint32(0x04030201)
	if got := canonRead32(b, false); got != wantLE {
		t.Errorf("canonRead32 LE = 0x%08X, want 0x%08X", got, wantLE)
	}
}
