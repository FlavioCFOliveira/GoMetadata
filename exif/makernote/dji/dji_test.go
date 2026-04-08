package dji

import (
	"encoding/binary"
	"testing"
)

func buildDJIMakerNote(tags []struct {
	id  uint16
	typ uint16
	val []byte
}) []byte {
	n := len(tags)
	outSize := 0
	for _, t := range tags {
		sz := int(typeSize(t.typ))
		if sz == 0 {
			sz = 1
		}
		total := sz * (len(t.val) / sz)
		if total > 4 {
			outSize += total
		}
	}
	buf := make([]byte, 2+n*12+outSize)
	binary.LittleEndian.PutUint16(buf[0:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast
	valueOff := uint32(2 + n*12)                      //nolint:gosec // G115: test helper, intentional type cast
	for i, t := range tags {
		pos := 2 + i*12
		binary.LittleEndian.PutUint16(buf[pos:], t.id)
		binary.LittleEndian.PutUint16(buf[pos+2:], t.typ)
		sz := typeSize(t.typ)
		if sz == 0 {
			sz = 1
		}
		cnt := uint32(len(t.val)) / sz //nolint:gosec // G115: test helper, intentional type cast
		binary.LittleEndian.PutUint32(buf[pos+4:], cnt)
		total := uint64(sz) * uint64(cnt)
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

func TestDJIParserBasic(t *testing.T) {
	t.Parallel()
	b := buildDJIMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMake, 2, append([]byte("DJI"), 0)},
		{TagPitch, 2, append([]byte("+00.00"), 0)},
	})

	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagMake]; !ok {
		t.Error("Make tag missing")
	}
}

func TestDJIParserTooShort(t *testing.T) {
	t.Parallel()
	tags, err := Parser{}.Parse([]byte{0x00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil for too-short input")
	}
}

func FuzzDJIParser(f *testing.F) {
	f.Add(buildDJIMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMake, 2, append([]byte("DJI"), 0)},
	}))
	f.Add([]byte{0x01, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Parser{}.Parse(b)
	})
}

// TestParseDJIIFDEntryUnknownType verifies that parseDJIIFDEntry returns
// ok=false when the type code is unknown (typeSize returns 0).
func TestParseDJIIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0001) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parseDJIIFDEntry(buf, 0, binary.LittleEndian)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParseDJIIFDEntryOOBOffset verifies that parseDJIIFDEntry returns
// ok=false when the offset-based value extends beyond the buffer.
func TestParseDJIIFDEntryOOBOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0001) // tag
	binary.LittleEndian.PutUint16(buf[2:], 2)      // ASCII type (size=1)
	binary.LittleEndian.PutUint32(buf[4:], 100)    // count=100 → offset-based
	binary.LittleEndian.PutUint32(buf[8:], 0xFFFF) // OOB offset
	_, _, ok := parseDJIIFDEntry(buf, 0, binary.LittleEndian)
	if ok {
		t.Error("expected ok=false for OOB offset, got true")
	}
}

// TestParseAtDJIAllUnknownTypes verifies that parseAt returns nil when all
// entries have unknown type codes (empty result map → returns nil).
func TestParseAtDJIAllUnknownTypes(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2+12)
	binary.LittleEndian.PutUint16(buf[0:], 1)      // count = 1
	binary.LittleEndian.PutUint16(buf[2:], 0x0001) // tag
	binary.LittleEndian.PutUint16(buf[4:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[6:], 1)      // count
	result := parseAt(buf, binary.LittleEndian)
	if result != nil {
		t.Errorf("expected nil for all-unknown-type IFD, got %v", result)
	}
}

// TestParseAtDJICountTooHigh verifies that parseAt returns nil when the
// entry count exceeds the 512-entry sanity limit.
func TestParseAtDJICountTooHigh(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 600) // count=600 > 512
	result := parseAt(buf, binary.LittleEndian)
	if result != nil {
		t.Errorf("expected nil for count=600, got %v", result)
	}
}

// TestParseAtDJIEntriesBeyondBuffer verifies that parseAt returns nil when
// the entry block extends beyond the buffer.
func TestParseAtDJIEntriesBeyondBuffer(t *testing.T) {
	t.Parallel()
	// count=5: needs 2+5*12=62 bytes, but buf is only 2 bytes.
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 5) // valid count but insufficient buffer
	result := parseAt(buf, binary.LittleEndian)
	if result != nil {
		t.Errorf("expected nil for entries beyond buffer, got %v", result)
	}
}

// TestDJITypeSizeAllBranches exercises every branch of typeSize.
func TestDJITypeSizeAllBranches(t *testing.T) {
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
