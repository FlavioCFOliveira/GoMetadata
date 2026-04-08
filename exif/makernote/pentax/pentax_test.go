package pentax

import (
	"encoding/binary"
	"testing"
)

// buildAOCMakerNote constructs a minimal valid Pentax AOC MakerNote.
//
// Layout (big-endian):
//
//	[0..3]   "AOC\x00"
//	[4..5]   version = 0x0100
//	[6..7]   entry count = 1 (BE uint16)
//	[8..19]  one 12-byte IFD entry
func buildAOCMakerNote() []byte {
	// magic(4) + version(2) + count(2) + 1 entry(12) = 20 bytes
	buf := make([]byte, 20)
	copy(buf[0:4], magicAOC)
	binary.BigEndian.PutUint16(buf[4:6], 0x0100) // version

	// IFD at offset 6.
	binary.BigEndian.PutUint16(buf[6:8], 1) // 1 entry

	// Entry: TagPentaxVersion (0x0000), type UNDEFINED (7), count 4, inline "0100".
	binary.BigEndian.PutUint16(buf[8:10], TagPentaxVersion)
	binary.BigEndian.PutUint16(buf[10:12], 7) // UNDEFINED
	binary.BigEndian.PutUint32(buf[12:16], 4) // count
	copy(buf[16:20], "0100")                  // inline value

	return buf
}

// buildAOCMakerNoteWithOffset constructs an AOC MakerNote with an offset-based value.
func buildAOCMakerNoteWithOffset() []byte {
	// magic(4) + version(2) + count(2) + 1 entry(12) + value(8) = 28 bytes
	const valueOffset = 20
	buf := make([]byte, valueOffset+8)
	copy(buf[0:4], magicAOC)
	binary.BigEndian.PutUint16(buf[4:6], 0x0100)
	binary.BigEndian.PutUint16(buf[6:8], 1)

	// Entry: TagFocalLength (0x001D), RATIONAL (5), count 1 → 8 bytes → offset.
	binary.BigEndian.PutUint16(buf[8:10], TagFocalLength)
	binary.BigEndian.PutUint16(buf[10:12], 5)           // RATIONAL
	binary.BigEndian.PutUint32(buf[12:16], 1)           // count
	binary.BigEndian.PutUint32(buf[16:20], valueOffset) // offset

	// RATIONAL value: 50/1 (50 mm).
	binary.BigEndian.PutUint32(buf[valueOffset:valueOffset+4], 50)
	binary.BigEndian.PutUint32(buf[valueOffset+4:valueOffset+8], 1)

	return buf
}

// buildPentaxPrefixMakerNote constructs a valid PENTAX-prefix MakerNote (little-endian).
func buildPentaxPrefixMakerNote() []byte {
	// magic(8) + bo(2) + version(2) + count(2) + 1 entry(12) = 26 bytes
	buf := make([]byte, 26)
	copy(buf[0:8], magicPENTAX)
	buf[8] = 'I'
	buf[9] = 'I'
	buf[10] = 0x00
	buf[11] = 0x01 // version

	// IFD at offset 12.
	binary.LittleEndian.PutUint16(buf[12:14], 1)

	// Entry: TagISO (0x0014), SHORT, count 1, value 200.
	binary.LittleEndian.PutUint16(buf[14:16], TagISO)
	binary.LittleEndian.PutUint16(buf[16:18], 3) // SHORT
	binary.LittleEndian.PutUint32(buf[18:22], 1)
	binary.LittleEndian.PutUint32(buf[22:26], 200)

	return buf
}

func TestPentaxParse_ValidAOC(t *testing.T) {
	t.Parallel()
	b := buildAOCMakerNote()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse AOC: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Parse AOC: got nil map, want non-nil")
	}
	if _, ok := result[TagPentaxVersion]; !ok {
		t.Errorf("TagPentaxVersion (0x%04X) not found in result", TagPentaxVersion)
	}
}

func TestPentaxParse_AOCOffsetValue(t *testing.T) {
	t.Parallel()
	b := buildAOCMakerNoteWithOffset()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse AOC offset: %v", err)
	}
	val, ok := result[TagFocalLength]
	if !ok {
		t.Fatal("TagFocalLength not found")
	}
	if len(val) != 8 {
		t.Errorf("TagFocalLength value len = %d, want 8", len(val))
	}
	// Verify the rational: 50/1.
	num := binary.BigEndian.Uint32(val[0:4])
	den := binary.BigEndian.Uint32(val[4:8])
	if num != 50 || den != 1 {
		t.Errorf("TagFocalLength = %d/%d, want 50/1", num, den)
	}
}

func TestPentaxParse_ValidPentaxPrefix(t *testing.T) {
	t.Parallel()
	b := buildPentaxPrefixMakerNote()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse PENTAX prefix: %v", err)
	}
	if result == nil {
		t.Fatal("Parse PENTAX prefix: got nil, want non-nil")
	}
	if _, ok := result[TagISO]; !ok {
		t.Errorf("TagISO (0x%04X) not found", TagISO)
	}
}

func TestPentaxParse_TooShort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty", []byte{}},
		{"3 bytes", []byte("AOC")},
		{"4 bytes (AOC magic)", []byte("AOC\x00")},
		{"7 bytes (AOC, truncated)", []byte("AOC\x00\x01\x00\x01")},
		{"7 bytes PENTAX", []byte("PENTAX ")},
	}
	p := Parser{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := p.Parse(tc.b)
			if err != nil {
				t.Errorf("expected nil error, got: %v", err)
			}
			if result != nil {
				t.Errorf("expected nil result for short input, got: %v", result)
			}
		})
	}
}

func TestPentaxParse_BadMagic(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[0:4], "NOPE")
	binary.BigEndian.PutUint16(b[4:6], 0x0100)

	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Errorf("expected nil error for bad magic, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for bad magic, got: %v", result)
	}
}

func TestPentaxParse_IFDOffsetOutOfBounds(t *testing.T) {
	t.Parallel()
	// Craft an AOC MakerNote where count > available entries.
	buf := make([]byte, minLengthAOC)
	copy(buf[0:4], magicAOC)
	binary.BigEndian.PutUint16(buf[4:6], 0x0100)
	binary.BigEndian.PutUint16(buf[6:8], 500) // 500 entries but buffer is tiny

	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for out-of-bounds IFD, got: %v", result)
	}
}

func TestPentaxParse_PentaxPrefixBadByteOrder(t *testing.T) {
	t.Parallel()
	b := buildPentaxPrefixMakerNote()
	b[8] = 'X'
	b[9] = 'X'

	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for bad byte order, got: %v", result)
	}
}

func TestPentaxParse_MultipleEntries(t *testing.T) {
	t.Parallel()
	// magic(4) + version(2) + count(2) + 2 entries(24) = 32 bytes
	buf := make([]byte, 32)
	copy(buf[0:4], magicAOC)
	binary.BigEndian.PutUint16(buf[4:6], 0x0100)
	binary.BigEndian.PutUint16(buf[6:8], 2) // 2 entries

	// Entry 0: TagShutterCount (0x005D), LONG, count 1, value 12345.
	binary.BigEndian.PutUint16(buf[8:10], TagShutterCount)
	binary.BigEndian.PutUint16(buf[10:12], 4) // LONG
	binary.BigEndian.PutUint32(buf[12:16], 1)
	binary.BigEndian.PutUint32(buf[16:20], 12345)

	// Entry 1: TagISO (0x0014), SHORT, count 1, value 800.
	binary.BigEndian.PutUint16(buf[20:22], TagISO)
	binary.BigEndian.PutUint16(buf[22:24], 3) // SHORT
	binary.BigEndian.PutUint32(buf[24:28], 1)
	binary.BigEndian.PutUint32(buf[28:32], 800)

	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 entries, got %d", len(result))
	}
	if _, ok := result[TagShutterCount]; !ok {
		t.Error("TagShutterCount missing")
	}
	if _, ok := result[TagISO]; !ok {
		t.Error("TagISO missing")
	}
}

func TestTagConstants(t *testing.T) {
	t.Parallel()
	if TagPentaxVersion != 0x0000 {
		t.Errorf("TagPentaxVersion = 0x%04X, want 0x0000", TagPentaxVersion)
	}
	if TagShutterCount != 0x005D {
		t.Errorf("TagShutterCount = 0x%04X, want 0x005D", TagShutterCount)
	}
	if TagSerialNumber != 0x00B0 {
		t.Errorf("TagSerialNumber = 0x%04X, want 0x00B0", TagSerialNumber)
	}
}

// TestPentaxParse_PentaxPrefixBigEndian verifies that Parse correctly decodes
// a PENTAX-prefix MakerNote with big-endian ('M','M') byte order marker.
func TestPentaxParse_PentaxPrefixBigEndian(t *testing.T) {
	t.Parallel()
	// magic(8) + "MM"(2) + version(2) + count(2) + 1 entry(12) = 26 bytes
	buf := make([]byte, 26)
	copy(buf[0:8], magicPENTAX)
	buf[8] = 'M'
	buf[9] = 'M'
	buf[10] = 0x00
	buf[11] = 0x01 // version

	// IFD at offset 12: 1 entry (BE).
	binary.BigEndian.PutUint16(buf[12:14], 1)

	// Entry: TagISO (0x0014), SHORT, count 1, value 400.
	binary.BigEndian.PutUint16(buf[14:16], TagISO)
	binary.BigEndian.PutUint16(buf[16:18], 3) // SHORT
	binary.BigEndian.PutUint32(buf[18:22], 1)
	binary.BigEndian.PutUint32(buf[22:26], 400)

	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("Parse PENTAX prefix BE: %v", err)
	}
	if result == nil {
		t.Fatal("Parse PENTAX prefix BE: got nil, want non-nil")
	}
	if _, ok := result[TagISO]; !ok {
		t.Error("TagISO not found in BE result")
	}
}

// TestParsePentaxIFDEntryUnknownType verifies that parsePentaxIFDEntry returns
// ok=false when the type code is unknown (typeSize16 returns 0).
func TestParsePentaxIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.BigEndian.PutUint16(buf[0:], 0x0014) // tag
	binary.BigEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.BigEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parsePentaxIFDEntry(buf, 0, true)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParsePentaxIFDEntryOOBOffset verifies that parsePentaxIFDEntry returns
// ok=false when the offset-based value extends beyond the buffer.
func TestParsePentaxIFDEntryOOBOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.BigEndian.PutUint16(buf[0:], 0x0014) // tag
	binary.BigEndian.PutUint16(buf[2:], 2)      // ASCII type (size=1)
	binary.BigEndian.PutUint32(buf[4:], 100)    // count=100 → offset-based
	binary.BigEndian.PutUint32(buf[8:], 0xFFFF) // OOB offset
	_, _, ok := parsePentaxIFDEntry(buf, 0, true)
	if ok {
		t.Error("expected ok=false for OOB offset, got true")
	}
}

// TestParseIFDAtPentaxEntriesBeyondBuffer verifies that parseIFDAt returns nil
// when the entry block extends beyond the buffer.
func TestParseIFDAtPentaxEntriesBeyondBuffer(t *testing.T) {
	t.Parallel()
	// Build an AOC header (6 bytes for magic+version) + 2-byte count.
	// count=5: needs 6+2+5*12=68 bytes, but provide only 8.
	buf := make([]byte, 8)
	copy(buf[0:4], magicAOC)
	binary.BigEndian.PutUint16(buf[4:6], 0x0100) // version
	binary.BigEndian.PutUint16(buf[6:8], 5)      // count=5, needs 60 more bytes
	result := parseIFDAt(buf, 6, true)
	if result != nil {
		t.Errorf("expected nil for entries beyond buffer, got %v", result)
	}
}

// TestPentaxTypeSizeAllBranches exercises every branch of typeSize16.
func TestPentaxTypeSizeAllBranches(t *testing.T) {
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

func FuzzPentaxParse(f *testing.F) {
	// Seeds: valid inputs.
	f.Add(buildAOCMakerNote())
	f.Add(buildAOCMakerNoteWithOffset())
	f.Add(buildPentaxPrefixMakerNote())
	// Seeds: degenerate inputs.
	f.Add([]byte{})
	f.Add([]byte("AOC\x00"))
	f.Add([]byte("PENTAX \x00"))
	f.Add(make([]byte, 20))
	// Seed: AOC with zero count.
	zero := make([]byte, 8)
	copy(zero[0:4], magicAOC)
	f.Add(zero)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		p := Parser{}
		_, _ = p.Parse(data)
	})
}
