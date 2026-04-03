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
	order.PutUint16(buf[0:], uint16(n))
	valueOff := uint32(2 + n*12)
	for i, t := range tags {
		pos := 2 + i*12
		order.PutUint16(buf[pos:], t.id)
		order.PutUint16(buf[pos+2:], t.typ)
		cnt := uint32(len(t.val)) / typeSize(t.typ)
		order.PutUint32(buf[pos+4:], cnt)
		total := uint64(typeSize(t.typ)) * uint64(cnt)
		if total <= 4 {
			copy(buf[pos+8:pos+12], t.val)
		} else {
			order.PutUint32(buf[pos+8:], valueOff)
			copy(buf[valueOff:], t.val)
			valueOff += uint32(len(t.val))
		}
	}
	return buf
}

func TestLeicaParserPrefixed(t *testing.T) {
	// Build "LEICA\x00\x01\x00" + plain LE IFD at offset 8.
	header := []byte{'L', 'E', 'I', 'C', 'A', 0x00, 0x01, 0x00}
	ifd := buildPlainIFD(binary.LittleEndian, []struct {
		id  uint16
		typ uint16
		val []byte
	}{
		{TagLensModel, 2, append([]byte("Summilux-M 35 f/1.4 ASPH"), 0)},
	})
	b := append(header, ifd...)

	tags, err := Parser{}.Parse(b)
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
