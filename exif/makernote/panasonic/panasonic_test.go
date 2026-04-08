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
		szRaw := int(typeSize(t.typ))
		if szRaw == 0 {
			szRaw = 1
		}
		sz := szRaw * (len(t.val) / szRaw)
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

// TestPanasonicOutOfLineValue verifies that parsePanasonicIFDEntry handles
// out-of-line values (total byte count > 4) correctly.
func TestPanasonicOutOfLineValue(t *testing.T) {
	t.Parallel()
	// Build a payload with 2 entries where the second has an out-of-line ASCII value.
	// Layout: "Panasonic\0\0\0"(12) + count(2) + 2 entries(24) + ascii_data
	const (
		magic    = "Panasonic\x00\x00\x00"
		ifdOff   = 12
		entryN   = 2
		valueOff = ifdOff + 2 + entryN*12
	)
	longStr := "SomeLongFirmwareString\x00" // 23 bytes > 4
	buf := make([]byte, valueOff+len(longStr))
	copy(buf[:12], magic)

	binary.LittleEndian.PutUint16(buf[ifdOff:], entryN)

	// Entry 0: TagImageQuality SHORT inline
	binary.LittleEndian.PutUint16(buf[ifdOff+2:], TagImageQuality)
	binary.LittleEndian.PutUint16(buf[ifdOff+4:], 3) // SHORT
	binary.LittleEndian.PutUint32(buf[ifdOff+6:], 1)
	binary.LittleEndian.PutUint32(buf[ifdOff+10:], 2)

	// Entry 1: TagFirmwareVersion ASCII out-of-line
	binary.LittleEndian.PutUint16(buf[ifdOff+14:], TagFirmwareVersion)
	binary.LittleEndian.PutUint16(buf[ifdOff+16:], 2)                    // ASCII
	binary.LittleEndian.PutUint32(buf[ifdOff+18:], uint32(len(longStr))) //nolint:gosec // G115: test helper
	binary.LittleEndian.PutUint32(buf[ifdOff+22:], uint32(valueOff))
	copy(buf[valueOff:], longStr)

	tags, err := Parser{}.Parse(buf)
	if err != nil {
		t.Fatalf("Parse out-of-line: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagFirmwareVersion]; !ok {
		t.Error("TagFirmwareVersion out-of-line value not found")
	}
}

// TestPanasonicInvalidEntryType verifies that entries with unknown type codes
// are skipped without error.
func TestPanasonicInvalidEntryType(t *testing.T) {
	t.Parallel()
	b := buildPanasonicMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagWhiteBalance, 0xFF, []byte{0x00, 0x00}}, // invalid type — skipped
		{TagFocusMode, 3, []byte{0x01, 0x00}},
	})
	// Only one valid entry; zero entries in result → nil.
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse invalid type: %v", err)
	}
	_ = tags // may be nil or a map with one entry
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
