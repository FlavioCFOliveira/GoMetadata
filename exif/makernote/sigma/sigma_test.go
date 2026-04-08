package sigma

import (
	"encoding/binary"
	"testing"
)

func buildSigmaMakerNote(tags []struct {
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
	// Layout: 8 (magic) + 2 (version) + 2 (count) + N*12 (entries) + values
	buf := make([]byte, 10+2+n*12+outSize)
	copy(buf[:8], "SIGMA\x00\x00\x00")
	buf[8] = 0x01
	buf[9] = 0x00
	binary.LittleEndian.PutUint16(buf[10:], uint16(n)) //nolint:gosec // G115: test helper, intentional type cast
	valueOff := uint32(10 + 2 + n*12)                  //nolint:gosec // G115: test helper, intentional type cast
	for i, t := range tags {
		pos := 10 + 2 + i*12
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

func TestSigmaParserBasic(t *testing.T) {
	t.Parallel()
	b := buildSigmaMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagWhiteBalance, 2, append([]byte("Daylight"), 0)},
		{TagExposureMode, 2, append([]byte("Aperture Priority"), 0)},
	})

	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagWhiteBalance]; !ok {
		t.Error("WhiteBalance tag missing")
	}
}

func TestSigmaParserFoveon(t *testing.T) {
	t.Parallel()
	// FOVEON prefix variant.
	b := buildSigmaMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagFirmware, 2, append([]byte("1.00"), 0)},
	})
	// Replace "SIGMA\x00\x00\x00" with "FOVEON\x00\x00".
	copy(b[:8], "FOVEON\x00\x00")

	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse (FOVEON): %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags for FOVEON prefix")
	}
}

func TestSigmaParserBadMagic(t *testing.T) {
	t.Parallel()
	b := make([]byte, 20)
	copy(b[:8], "UNKNOWN!")
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil for unknown magic")
	}
}

func FuzzSigmaParser(f *testing.F) {
	f.Add(buildSigmaMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagWhiteBalance, 2, append([]byte("Daylight"), 0)},
	}))
	f.Add([]byte("SIGMA\x00\x00\x00\x01\x00garbage"))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Parser{}.Parse(b)
	})
}

// TestSigmaTypeSizeAllBranches exercises every branch of typeSize.
func TestSigmaTypeSizeAllBranches(t *testing.T) {
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

// TestParseSigmaIFDEntryUnknownType verifies that parseSigmaIFDEntry returns
// ok=false for an IFD entry whose type code is unknown (typeSize returns 0).
func TestParseSigmaIFDEntryUnknownType(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0002) // tag
	binary.LittleEndian.PutUint16(buf[2:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[4:], 1)      // count
	_, _, ok := parseSigmaIFDEntry(buf, 0)
	if ok {
		t.Error("expected ok=false for unknown type code, got true")
	}
}

// TestParseSigmaIFDEntryOOBOffset verifies that parseSigmaIFDEntry returns
// ok=false when the offset-based value extends beyond the buffer.
func TestParseSigmaIFDEntryOOBOffset(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint16(buf[0:], 0x0002) // tag
	binary.LittleEndian.PutUint16(buf[2:], 2)      // ASCII type (size=1)
	binary.LittleEndian.PutUint32(buf[4:], 100)    // count=100 → 100 bytes → offset-based
	binary.LittleEndian.PutUint32(buf[8:], 0xFFFF) // offset OOB
	_, _, ok := parseSigmaIFDEntry(buf, 0)
	if ok {
		t.Error("expected ok=false for OOB offset, got true")
	}
}

// TestSigmaParseCountZero verifies that parseAt returns nil when the IFD
// entry count is 0.
func TestSigmaParseCountZero(t *testing.T) {
	t.Parallel()
	// Build SIGMA magic + version + count=0 IFD.
	buf := make([]byte, 12)
	copy(buf[:8], "SIGMA\x00\x00\x00")
	// buf[8..9] = version, buf[10..11] = count=0
	binary.LittleEndian.PutUint16(buf[10:], 0) // count = 0

	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for count=0 IFD, got %v", result)
	}
}

// TestSigmaParseAllUnknownTypes verifies that Parse returns nil when all entries
// have unknown type codes (so the result map stays empty → parseAt returns nil).
func TestSigmaParseAllUnknownTypes(t *testing.T) {
	t.Parallel()
	// Build SIGMA magic + version + 1 entry with unknown type.
	buf := make([]byte, 10+2+12)
	copy(buf[:8], "SIGMA\x00\x00\x00")
	binary.LittleEndian.PutUint16(buf[10:], 1)      // count = 1
	binary.LittleEndian.PutUint16(buf[12:], 0x0002) // tag
	binary.LittleEndian.PutUint16(buf[14:], 0xFF)   // unknown type
	binary.LittleEndian.PutUint32(buf[16:], 1)      // count

	p := Parser{}
	result, err := p.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil when all entries have unknown types, got %v", result)
	}
}
