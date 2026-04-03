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
	binary.LittleEndian.PutUint16(buf[12:], uint16(n))

	valueOff := uint32(12 + 2 + n*12)
	for i, t := range tags {
		pos := 12 + 2 + i*12
		binary.LittleEndian.PutUint16(buf[pos:], t.id)
		binary.LittleEndian.PutUint16(buf[pos+2:], t.typ)
		cnt := uint32(len(t.val)) / typeSize(t.typ)
		binary.LittleEndian.PutUint32(buf[pos+4:], cnt)
		total := uint64(typeSize(t.typ)) * uint64(cnt)
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

func TestPanasonicParserBasic(t *testing.T) {
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
	tags, err := Parser{}.Parse([]byte("Pana"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Error("expected nil tags for too-short input")
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
