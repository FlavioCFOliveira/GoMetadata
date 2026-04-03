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
	binary.LittleEndian.PutUint16(buf[0:], uint16(n))
	valueOff := uint32(2 + n*12)
	for i, t := range tags {
		pos := 2 + i*12
		binary.LittleEndian.PutUint16(buf[pos:], t.id)
		binary.LittleEndian.PutUint16(buf[pos+2:], t.typ)
		sz := typeSize(t.typ)
		if sz == 0 {
			sz = 1
		}
		cnt := uint32(len(t.val)) / sz
		binary.LittleEndian.PutUint32(buf[pos+4:], cnt)
		total := uint64(sz) * uint64(cnt)
		if total <= 4 {
			copy(buf[pos+8:pos+12], t.val)
		} else {
			binary.LittleEndian.PutUint32(buf[pos+8:], valueOff)
			copy(buf[valueOff:], t.val)
			valueOff += uint32(len(t.val))
		}
	}
	return buf
}

func TestDJIParserBasic(t *testing.T) {
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
