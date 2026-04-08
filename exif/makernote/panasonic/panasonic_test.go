package panasonic

import (
	"encoding/binary"
	"testing"
)

// buildPanasonicMakerNote builds a minimal Panasonic MakerNote payload
// with the 12-byte magic prefix and a single LE IFD entry.
func buildPanasonicMakerNote(tags []struct {
	id  uint16
	typ uint16
	val []byte
}) []byte {
	// Compute out-of-line value area size.
	outOfLineSize := 0
	for _, t := range tags {
		sz := int(typeSize(t.typ)) * (len(t.val) / int(typeSize(t.typ)))
		if sz > 4 {
			outOfLineSize += sz
		}
	}

	// Layout: 12 (magic) + 2 (count) + N*12 (entries) + out-of-line values
	n := len(tags)
	buf := make([]byte, 12+2+n*12+outOfLineSize)
	copy(buf[:12], magic)
	binary.LittleEndian.PutUint16(buf[12:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast

	valueOff := uint32(12 + 2 + n*12) //nolint:gosec // G115: test helper, intentional type cast
	for i, t := range tags {
		pos := 12 + 2 + i*12
		binary.LittleEndian.PutUint16(buf[pos:], t.id)
		binary.LittleEndian.PutUint16(buf[pos+2:], t.typ)
		cnt := uint32(len(t.val)) / typeSize(t.typ) //nolint:gosec // G115: test helper, intentional type cast
		binary.LittleEndian.PutUint32(buf[pos+4:], cnt)
		total := uint64(typeSize(t.typ)) * uint64(cnt)
		if total <= 4 {
			copy(buf[pos+8:pos+12], t.val)
		} else {
			binary.LittleEndian.PutUint32(buf[pos+8:], valueOff)
			copy(buf[valueOff:], t.val)
			valueOff += uint32(len(t.val)) //nolint:gosec // G115: test helper, intentional type cast
		}
	}
	return buf
}

func TestPanasonicParserBasic(t *testing.T) {
	t.Parallel()
	p := Parser{}

	b := buildPanasonicMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagWhiteBalance, 3, []byte{0x01, 0x00}}, // SHORT = 1
		{TagFocusMode, 3, []byte{0x01, 0x00}},    // SHORT = 1
	})

	tags, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tags == nil {
		t.Fatal("Parse returned nil tags")
	}
	if _, ok := tags[TagWhiteBalance]; !ok {
		t.Error("WhiteBalance tag missing")
	}
	if _, ok := tags[TagFocusMode]; !ok {
		t.Error("FocusMode tag missing")
	}
}

func TestPanasonicParserBadMagic(t *testing.T) {
	t.Parallel()
	// Payload without the Panasonic magic prefix.
	b := make([]byte, 20)
	b[0] = 0xFF // not "Panasonic..."
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil tags for bad magic")
	}
}

func TestPanasonicParserTooShort(t *testing.T) {
	t.Parallel()
	tags, err := Parser{}.Parse([]byte("Pana"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil tags for too-short input")
	}
}

// TestPanasonicTypeSizeAllBranches exercises every branch of typeSize.
func TestPanasonicTypeSizeAllBranches(t *testing.T) {
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
		if got := typeSize(tc.typ); got != tc.want {
			t.Errorf("typeSize(%d) = %d, want %d", tc.typ, got, tc.want)
		}
	}
}

// TestParsePanasonicIFDEntryOutOfBoundsOffset exercises the out-of-bounds offset
// guard in parsePanasonicIFDEntry when total > 4 and offset is beyond b.
func TestParsePanasonicIFDEntryOutOfBoundsOffset(t *testing.T) {
	t.Parallel()
	// Build an IFD entry: LONG (4 bytes/unit), count=8, so total=32 > 4 (out-of-line).
	// Point the offset to a position beyond the buffer end.
	b := make([]byte, 12)                        // exactly one 12-byte entry, no room for out-of-line value
	binary.LittleEndian.PutUint16(b[0:], 0x0003) // tag = WhiteBalance
	binary.LittleEndian.PutUint16(b[2:], 4)      // type = LONG
	binary.LittleEndian.PutUint32(b[4:], 8)      // count = 8, total = 32 > 4
	binary.LittleEndian.PutUint32(b[8:], 9999)   // offset = 9999 (beyond len=12)
	_, _, ok := parsePanasonicIFDEntry(b, 0)
	if ok {
		t.Error("parsePanasonicIFDEntry with out-of-bounds offset should return ok=false")
	}
}

// TestParsePanasonicIFDEntryUnknownType verifies that parsePanasonicIFDEntry
// returns ok=false when the type code is unknown (typeSize returns 0).
func TestParsePanasonicIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0003) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parsePanasonicIFDEntry(buf, 0)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParseLECountTooHigh verifies that parseLE returns nil when the IFD
// entry count exceeds the 512-entry sanity limit.
func TestParseLECountTooHigh(t *testing.T) {
	t.Parallel()
	// Build a Panasonic MakerNote with magic prefix but count=600.
	buf := make([]byte, 14) // 12 (magic) + 2 (count)
	copy(buf[:12], magic)
	binary.LittleEndian.PutUint16(buf[12:], 600) // count=600 > 512
	result := parseLE(buf)
	if result != nil {
		t.Errorf("expected nil for count=600, got %v", result)
	}
}

// TestParseLEEntriesBeyondBuffer verifies that parseLE returns nil when the
// entry block extends beyond the buffer (count=1 but buffer too short).
func TestParseLEEntriesBeyondBuffer(t *testing.T) {
	t.Parallel()
	// 12 (magic) + 2 (count) = 14; count=1 needs 14+12=26, but buf is only 14.
	buf := make([]byte, 14)
	copy(buf[:12], magic)
	binary.LittleEndian.PutUint16(buf[12:], 1) // count=1, needs 12 more bytes
	result := parseLE(buf)
	if result != nil {
		t.Errorf("expected nil for entries beyond buffer, got %v", result)
	}
}

func FuzzPanasonicParser(f *testing.F) {
	// Seed: valid Panasonic MakerNote.
	f.Add(buildPanasonicMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagWhiteBalance, 3, []byte{0x01, 0x00}},
	}))
	// Seed: random bytes.
	f.Add([]byte("Panasonic\x00\x00\x00garbage"))

	f.Fuzz(func(t *testing.T, b []byte) {
		// Must not panic.
		_, _ = Parser{}.Parse(b)
	})
}
