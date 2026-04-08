package leica

import (
	"encoding/binary"
	"testing"
)

func buildPlainIFD(order binary.ByteOrder, tags []struct {
	id  uint16
	typ uint16
	val []byte
}) []byte {
	n := len(tags)
	outSize := 0
	for _, t := range tags {
		sz := int(typeSize(t.typ))
		total := sz * (len(t.val) / sz)
		if total > 4 {
			outSize += total
		}
	}
	buf := make([]byte, 2+n*12+outSize)
	order.PutUint16(buf[0:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast
	valueOff := uint32(2 + n*12)        //nolint:gosec // G115: test helper, intentional type cast
	for i, t := range tags {
		pos := 2 + i*12
		order.PutUint16(buf[pos:], t.id)
		order.PutUint16(buf[pos+2:], t.typ)
		cnt := uint32(len(t.val)) / typeSize(t.typ) //nolint:gosec // G115: test helper, intentional type cast
		order.PutUint32(buf[pos+4:], cnt)
		total := uint64(typeSize(t.typ)) * uint64(cnt)
		if total <= 4 {
			copy(buf[pos+8:pos+12], t.val)
		} else {
			order.PutUint32(buf[pos+8:], valueOff)
			copy(buf[valueOff:], t.val)
			valueOff += uint32(len(t.val)) //nolint:gosec // G115: test helper, intentional type cast
		}
	}
	return buf
}

func TestLeicaParserPrefixed(t *testing.T) {
	t.Parallel()
	// Build "LEICA\x00\x01\x00" + plain LE IFD at offset 8.
	ifd := buildPlainIFD(binary.LittleEndian, []struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagLensModel, 2, append([]byte("Summilux-M 35 f/1.4 ASPH"), 0)},
	})
	header := make([]byte, 0, 8+len(ifd))
	header = append(header, 'L', 'E', 'I', 'C', 'A', 0x00, 0x01, 0x00)
	header = append(header, ifd...)

	tags, err := Parser{}.Parse(header)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagLensModel]; !ok {
		t.Error("LensModel tag missing")
	}
}

func TestLeicaParserPlainIFD(t *testing.T) {
	t.Parallel()
	ifd := buildPlainIFD(binary.LittleEndian, []struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagLensType, 3, []byte{0x05, 0x00}},
	})
	tags, err := Parser{}.Parse(ifd)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
}

func TestLeicaParserTooShort(t *testing.T) {
	t.Parallel()
	tags, err := Parser{}.Parse([]byte{0x00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil for too-short input")
	}
}

func FuzzLeicaParser(f *testing.F) {
	ifd := buildPlainIFD(binary.LittleEndian, []struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagLensType, 3, []byte{0x05, 0x00}},
	})
	f.Add(ifd)
	f.Add([]byte("LEICA\x00\x01\x00garbage"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Parser{}.Parse(b)
	})
}

// TestParseLeicaIFDEntryUnknownType verifies that parseLeicaIFDEntry returns
// ok=false for an entry with an unknown type code.
func TestParseLeicaIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0302) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)
	_, _, ok := parseLeicaIFDEntry(buf, 0, binary.LittleEndian)
	if ok {
		t.Error("expected ok=false for unknown type, got true")
	}
}

// TestParseLeicaIFDEntryOOBOffset verifies that parseLeicaIFDEntry returns
// ok=false when the offset-based value extends beyond the buffer.
func TestParseLeicaIFDEntryOOBOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0302) // tag
	binary.LittleEndian.PutUint16(buf[2:], 2)      // ASCII
	binary.LittleEndian.PutUint32(buf[4:], 100)    // count=100 → offset-based
	binary.LittleEndian.PutUint32(buf[8:], 0xFFFF) // OOB offset
	_, _, ok := parseLeicaIFDEntry(buf, 0, binary.LittleEndian)
	if ok {
		t.Error("expected ok=false for OOB offset, got true")
	}
}

// TestParseIFDAtAllUnknownTypes verifies that parseIFDAt returns nil when all
// entries have unknown type codes (empty result map → returns nil).
func TestParseIFDAtAllUnknownTypes(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2+12)
	binary.LittleEndian.PutUint16(buf[0:], 1)      // count = 1
	binary.LittleEndian.PutUint16(buf[2:], 0x0302) // tag
	binary.LittleEndian.PutUint16(buf[4:], 0xFF)   // unknown type
	result := parseIFDAt(buf, 0, binary.LittleEndian)
	if result != nil {
		t.Errorf("expected nil for all-unknown-type IFD, got %v", result)
	}
}

// TestParseIFDAtCountTooHigh verifies that parseIFDAt returns nil when the
// entry count exceeds the 512-entry sanity limit.
func TestParseIFDAtCountTooHigh(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf[0:], 600) // count=600 > 512
	result := parseIFDAt(buf, 0, binary.LittleEndian)
	if result != nil {
		t.Errorf("expected nil for count=600, got %v", result)
	}
}

// TestLeicaTypeSizeAllBranches exercises every branch of typeSize.
func TestLeicaTypeSizeAllBranches(t *testing.T) {
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
