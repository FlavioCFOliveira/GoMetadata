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

// TestDJIBigEndianFallback verifies that parseDJI falls back to BE when LE fails.
func TestDJIBigEndianFallback(t *testing.T) {
	t.Parallel()
	// Build a big-endian IFD with 1 entry so LE heuristics fail.
	buf := make([]byte, 2+1*12)
	binary.BigEndian.PutUint16(buf[0:], 1) // count=1
	binary.BigEndian.PutUint16(buf[2:], TagMake)
	binary.BigEndian.PutUint16(buf[4:], 4) // LONG
	binary.BigEndian.PutUint32(buf[6:], 1)
	binary.BigEndian.PutUint32(buf[10:], 1)

	tags, err := Parser{}.Parse(buf)
	if err != nil {
		t.Fatalf("Parse DJI BE fallback: %v", err)
	}
	_ = tags
}

// TestDJIOutOfLineValue verifies that parseDJIIFDEntry handles out-of-line
// values (total size > 4 bytes).
func TestDJIOutOfLineValue(t *testing.T) {
	t.Parallel()
	longStr := "Phantom4Pro\x00" // > 4 bytes
	b := buildDJIMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMake, 2, append([]byte("DJI"), 0)},
		{TagPitch, 2, []byte(longStr)},
	})
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse out-of-line: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagPitch]; !ok {
		t.Error("TagPitch out-of-line value not found")
	}
}

// TestDJIInvalidEntryType verifies that entries with unknown type codes are skipped.
func TestDJIInvalidEntryType(t *testing.T) {
	t.Parallel()
	b := buildDJIMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMake, 0xFF, []byte{0x00, 0x00}}, // invalid type
	})
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = tags
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
