package fujifilm

import (
	"encoding/binary"
	"testing"
)

// buildFujifilmMakerNote constructs a minimal but valid Fujifilm MakerNote.
//
// Layout:
//
//	[0..7]   "FUJIFILM"
//	[8..11]  "0100" (version)
//	[12..15] LE uint32 IFD offset (= 16, immediately after the header)
//	[16..17] entry count (LE uint16)
//	[18..29] one 12-byte IFD entry
//
// The entry uses TagVersion (0x0000), type UNDEFINED (7), count 4, inline value "0100".
func buildFujifilmMakerNote() []byte {
	const ifdOffset = 16
	buf := make([]byte, ifdOffset+2+12)

	// Magic + version.
	copy(buf[0:8], "FUJIFILM")
	copy(buf[8:12], "0100")

	// IFD offset (LE).
	binary.LittleEndian.PutUint32(buf[12:16], ifdOffset)

	// IFD: 1 entry.
	binary.LittleEndian.PutUint16(buf[16:18], 1)

	// Entry: TagVersion (0x0000), type UNDEFINED (7), count 4, value "0100".
	binary.LittleEndian.PutUint16(buf[18:20], TagVersion) // tag
	binary.LittleEndian.PutUint16(buf[20:22], 7)          // type UNDEFINED
	binary.LittleEndian.PutUint32(buf[22:26], 4)          // count
	copy(buf[26:30], "0100")                              // inline value (4 bytes fit in value field)

	return buf
}

// buildFujifilmMakerNoteWithOffset constructs a Fujifilm MakerNote with a tag
// whose value is stored at an offset (total > 4 bytes).
func buildFujifilmMakerNoteWithOffset() []byte {
	// Header (16) + IFD count (2) + 1 entry (12) + value data (8) = 38 bytes.
	const ifdOffset = 16
	const valueOffset = 30 // 16 (header) + 2 (count) + 12 (entry)
	buf := make([]byte, valueOffset+8)

	copy(buf[0:8], "FUJIFILM")
	copy(buf[8:12], "0100")
	binary.LittleEndian.PutUint32(buf[12:16], ifdOffset)
	binary.LittleEndian.PutUint16(buf[16:18], 1)

	// Entry: TagWhiteBalance (0x1001), type RATIONAL (5), count 1 → 8 bytes → offset-based.
	binary.LittleEndian.PutUint16(buf[18:20], TagWhiteBalance)
	binary.LittleEndian.PutUint16(buf[20:22], 5)           // RATIONAL
	binary.LittleEndian.PutUint32(buf[22:26], 1)           // count 1
	binary.LittleEndian.PutUint32(buf[26:30], valueOffset) // offset to value

	// RATIONAL value: numerator=1, denominator=1.
	binary.LittleEndian.PutUint32(buf[valueOffset:valueOffset+4], 1)
	binary.LittleEndian.PutUint32(buf[valueOffset+4:valueOffset+8], 1)

	return buf
}

func TestFujifilmParse_Valid(t *testing.T) {
	t.Parallel()
	b := buildFujifilmMakerNote()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Parse: got nil map, want non-nil")
	}
	if _, ok := result[TagVersion]; !ok {
		t.Errorf("TagVersion (0x%04X) not found in result", TagVersion)
	}
}

func TestFujifilmParse_OffsetValue(t *testing.T) {
	t.Parallel()
	b := buildFujifilmMakerNoteWithOffset()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if _, ok := result[TagWhiteBalance]; !ok {
		t.Errorf("TagWhiteBalance (0x%04X) not found in result", TagWhiteBalance)
	}
	val := result[TagWhiteBalance]
	if len(val) != 8 {
		t.Errorf("TagWhiteBalance value len = %d, want 8", len(val))
	}
}

func TestFujifilmParse_TooShort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty", []byte{}},
		{"7 bytes", []byte("FUJIFILM")[:7]},
		{"magic only", []byte("FUJIFILM")},
		{"magic+version only", append([]byte("FUJIFILM"), "0100"...)},
	}
	p := Parser{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := p.Parse(tc.b)
			if err == nil {
				t.Error("expected error for too-short input, got nil")
			}
		})
	}
}

func TestFujifilmParse_BadMagic(t *testing.T) {
	t.Parallel()
	b := make([]byte, minLength)
	copy(b[0:8], "BADMAGIC")
	copy(b[8:12], "0100")
	binary.LittleEndian.PutUint32(b[12:16], 16)

	p := Parser{}
	_, err := p.Parse(b)
	if err == nil {
		t.Error("expected error for bad magic, got nil")
	}
}

func TestFujifilmParse_IFDOffsetOutOfBounds(t *testing.T) {
	t.Parallel()
	// IFD offset points beyond the buffer.
	b := make([]byte, minLength)
	copy(b[0:8], "FUJIFILM")
	copy(b[8:12], "0100")
	binary.LittleEndian.PutUint32(b[12:16], 0xFFFFFFFF)

	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("unexpected error for out-of-bounds offset: %v", err)
	}
	// Should return an empty (non-nil) map, not nil.
	if result == nil {
		t.Error("expected empty map, got nil")
	}
}

func TestFujifilmParse_MultipleEntries(t *testing.T) {
	t.Parallel()
	// Build a MakerNote with two tags.
	const ifdOffset = 16
	// header(16) + count(2) + 2 entries(24) = 42
	buf := make([]byte, 42)
	copy(buf[0:8], "FUJIFILM")
	copy(buf[8:12], "0100")
	binary.LittleEndian.PutUint32(buf[12:16], ifdOffset)
	binary.LittleEndian.PutUint16(buf[16:18], 2)

	// Entry 0: TagVersion, UNDEFINED, count 4, inline "0100"
	binary.LittleEndian.PutUint16(buf[18:20], TagVersion)
	binary.LittleEndian.PutUint16(buf[20:22], 7)
	binary.LittleEndian.PutUint32(buf[22:26], 4)
	copy(buf[26:30], "0100")

	// Entry 1: TagSharpness (0x1003), SHORT, count 1, value 2
	binary.LittleEndian.PutUint16(buf[30:32], TagSharpness)
	binary.LittleEndian.PutUint16(buf[32:34], 3)
	binary.LittleEndian.PutUint32(buf[34:38], 1)
	binary.LittleEndian.PutUint32(buf[38:42], 2) // value=2

	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 entries, got %d", len(result))
	}
	if _, ok := result[TagVersion]; !ok {
		t.Error("TagVersion missing")
	}
	if _, ok := result[TagSharpness]; !ok {
		t.Error("TagSharpness missing")
	}
}

func TestTagConstants(t *testing.T) {
	t.Parallel()
	if TagVersion != 0x0000 {
		t.Errorf("TagVersion = 0x%04X, want 0x0000", TagVersion)
	}
	if TagWhiteBalance != 0x1001 {
		t.Errorf("TagWhiteBalance = 0x%04X, want 0x1001", TagWhiteBalance)
	}
	if TagFileSource != 0x8000 {
		t.Errorf("TagFileSource = 0x%04X, want 0x8000", TagFileSource)
	}
}

// TestFujifilmReadU16U32BEAndLE exercises both branches of readU16 and readU32.
func TestFujifilmReadU16U32BEAndLE(t *testing.T) {
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

// TestFujifilmTypeSize16AllBranches exercises every branch of typeSize16.
func TestFujifilmTypeSize16AllBranches(t *testing.T) {
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

// TestParseFujifilmIFDEntryUnknownType verifies that parseFujifilmIFDEntry
// returns ok=false when the type code is unknown (typeSize16 returns 0).
func TestParseFujifilmIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x1000) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parseFujifilmIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParseFujifilmIFDEntryOOBOffset verifies that parseFujifilmIFDEntry
// returns ok=false when the offset-based value extends beyond the buffer.
func TestParseFujifilmIFDEntryOOBOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x1000) // tag
	binary.LittleEndian.PutUint16(buf[2:], 2)      // ASCII type (size=1)
	binary.LittleEndian.PutUint32(buf[4:], 100)    // count=100 → offset-based
	binary.LittleEndian.PutUint32(buf[8:], 0xFFFF) // OOB offset
	_, _, ok := parseFujifilmIFDEntry(buf, 0, false)
	if ok {
		t.Error("expected ok=false for OOB offset, got true")
	}
}

// TestParseIFDAtFujifilmCountTooHigh verifies that parseIFDAt returns nil when
// the entry count exceeds the 512-entry sanity limit.
func TestParseIFDAtFujifilmCountTooHigh(t *testing.T) {
	t.Parallel()
	// Build a minimal Fujifilm MakerNote buffer (16-byte header + 2-byte count).
	// The IFD offset is at bytes 8-9 (LE uint16) in the Fujifilm format.
	// For direct parseIFDAt testing, we just pass a tiny buffer with count=600.
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 600) // count=600 > 512
	result := parseIFDAt(buf, 0, false)
	if result != nil {
		t.Errorf("expected nil for count=600, got %v", result)
	}
}

// TestParseIFDAtFujifilmEntriesBeyondBuffer verifies that parseIFDAt returns nil
// when the entry block extends beyond the buffer.
func TestParseIFDAtFujifilmEntriesBeyondBuffer(t *testing.T) {
	t.Parallel()
	// count=5: needs 2+5*12=62 bytes, but buf is only 2 bytes.
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 5)
	result := parseIFDAt(buf, 0, false)
	if result != nil {
		t.Errorf("expected nil for entries beyond buffer, got %v", result)
	}
}

func FuzzFujifilmParse(f *testing.F) {
	// Seed: valid minimal MakerNote.
	f.Add(buildFujifilmMakerNote())
	// Seed: valid MakerNote with offset-based value.
	f.Add(buildFujifilmMakerNoteWithOffset())
	// Seed: empty input.
	f.Add([]byte{})
	// Seed: magic prefix only.
	f.Add([]byte("FUJIFILM"))
	// Seed: 16 bytes of zeros.
	f.Add(make([]byte, 16))
	// Seed: magic + version + zero IFD offset.
	seed := make([]byte, 16)
	copy(seed[0:], "FUJIFILM0100")
	f.Add(seed)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		p := Parser{}
		_, _ = p.Parse(data)
	})
}
