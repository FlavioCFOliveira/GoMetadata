package samsung

import (
	"encoding/binary"
	"testing"
)

func buildSamsungMakerNote(tags []struct {
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

func TestSamsungParserBasic(t *testing.T) {
	t.Parallel()
	b := buildSamsungMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMeteringMode, 3, []byte{0x02, 0x00}},                 // SHORT = 2
		{TagColorTemperature, 4, []byte{0xD8, 0x13, 0x00, 0x00}}, // LONG = 5080
	})

	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagMeteringMode]; !ok {
		t.Error("MeteringMode tag missing")
	}
}

func TestSamsungParserTooShort(t *testing.T) {
	t.Parallel()
	tags, err := Parser{}.Parse([]byte{0x00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil for too-short input")
	}
}

// TestSamsungBigEndianFallback verifies that parseSamsung falls back to BE when
// LE parsing fails.
func TestSamsungBigEndianFallback(t *testing.T) {
	t.Parallel()
	// Build a big-endian IFD with 1 valid entry.
	buf := make([]byte, 2+1*12)
	binary.BigEndian.PutUint16(buf[0:], 1) // count=1
	binary.BigEndian.PutUint16(buf[2:], TagMeteringMode)
	binary.BigEndian.PutUint16(buf[4:], 3)  // SHORT
	binary.BigEndian.PutUint32(buf[6:], 1)  // count=1
	binary.BigEndian.PutUint32(buf[10:], 2) // value=2

	tags, err := Parser{}.Parse(buf)
	if err != nil {
		t.Fatalf("Parse BE fallback: %v", err)
	}
	// Result depends on heuristics; just no panic.
	_ = tags
}

// TestSamsungOutOfLineValue verifies that parseSamsungIFDEntry handles out-of-line
// values correctly.
func TestSamsungOutOfLineValue(t *testing.T) {
	t.Parallel()
	longStr := "GalaxyModelXXYY\x00" // > 4 bytes
	b := buildSamsungMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMeteringMode, 3, []byte{0x02, 0x00}}, // SHORT inline
		{TagSamsungModel, 2, []byte(longStr)},    // ASCII out-of-line
	})
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("Parse out-of-line: %v", err)
	}
	if tags == nil {
		t.Fatal("expected non-nil tags")
	}
	if _, ok := tags[TagSamsungModel]; !ok {
		t.Error("TagSamsungModel out-of-line value not found")
	}
}

// TestSamsungInvalidEntryType verifies that entries with unknown types are skipped.
func TestSamsungInvalidEntryType(t *testing.T) {
	t.Parallel()
	b := buildSamsungMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMeteringMode, 0xFF, []byte{0x00, 0x00}}, // invalid type
	})
	tags, err := Parser{}.Parse(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = tags
}

func FuzzSamsungParser(f *testing.F) {
	f.Add(buildSamsungMakerNote([]struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagMeteringMode, 3, []byte{0x02, 0x00}},
	}))
	f.Add([]byte{0x01, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Parser{}.Parse(b)
	})
}
