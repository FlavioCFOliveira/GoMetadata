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
	tags, err := Parser{}.Parse([]byte{0x00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil for too-short input")
	}
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
