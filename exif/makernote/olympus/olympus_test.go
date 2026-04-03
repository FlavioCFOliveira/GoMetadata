package olympus

import (
	"encoding/binary"
	"testing"
)

// buildOlympusMakerNote constructs a minimal valid Olympus Type 2 MakerNote.
//
// Layout (little-endian):
//
//	[0..7]   "OLYMPUS\x00"
//	[8..9]   "II" (little-endian)
//	[10..11] 0x03 0x00 (version)
//	[12..13] entry count = 1 (LE uint16)
//	[14..25] one 12-byte IFD entry
func buildOlympusMakerNote() []byte {
	// header(12) + count(2) + 1 entry(12) = 26 bytes
	buf := make([]byte, 26)
	copy(buf[0:8], magicType2)
	buf[8] = 'I'
	buf[9] = 'I'
	buf[10] = 0x03
	buf[11] = 0x00

	// IFD at offset 12: 1 entry.
	binary.LittleEndian.PutUint16(buf[12:14], 1)

	// Entry: TagCameraType (0x0207), type ASCII (2), count 4, inline value "EM5"0.
	binary.LittleEndian.PutUint16(buf[14:16], TagCameraType) // tag
	binary.LittleEndian.PutUint16(buf[16:18], 2)             // type ASCII
	binary.LittleEndian.PutUint32(buf[18:22], 4)             // count
	copy(buf[22:26], "EM5\x00")                              // inline value

	return buf
}

// buildOlympusMakerNoteBE constructs a big-endian Olympus Type 2 MakerNote.
func buildOlympusMakerNoteBE() []byte {
	buf := make([]byte, 26)
	copy(buf[0:8], magicType2)
	buf[8] = 'M'
	buf[9] = 'M'
	buf[10] = 0x00
	buf[11] = 0x03

	binary.BigEndian.PutUint16(buf[12:14], 1)

	// Entry: TagJpegQuality (0x0201), SHORT, count 1, value 5 (high quality).
	binary.BigEndian.PutUint16(buf[14:16], TagJpegQuality)
	binary.BigEndian.PutUint16(buf[16:18], 3) // SHORT
	binary.BigEndian.PutUint32(buf[18:22], 1)
	binary.BigEndian.PutUint32(buf[22:26], 5)

	return buf
}

// buildOlympusMakerNoteWithOffset constructs a MakerNote with an offset-based value.
func buildOlympusMakerNoteWithOffset() []byte {
	// header(12) + count(2) + 1 entry(12) + value(8) = 34 bytes
	const valueOffset = 26
	buf := make([]byte, valueOffset+8)
	copy(buf[0:8], magicType2)
	buf[8] = 'I'
	buf[9] = 'I'
	buf[10] = 0x03
	buf[11] = 0x00

	binary.LittleEndian.PutUint16(buf[12:14], 1)

	// Entry: TagApertureValue, RATIONAL (5), count 1 → 8 bytes → offset-based.
	binary.LittleEndian.PutUint16(buf[14:16], TagApertureValue)
	binary.LittleEndian.PutUint16(buf[16:18], 5)            // RATIONAL
	binary.LittleEndian.PutUint32(buf[18:22], 1)            // count 1
	binary.LittleEndian.PutUint32(buf[22:26], valueOffset)  // offset

	// Value: numerator=14, denominator=10 → f/1.4
	binary.LittleEndian.PutUint32(buf[valueOffset:valueOffset+4], 14)
	binary.LittleEndian.PutUint32(buf[valueOffset+4:valueOffset+8], 10)

	return buf
}

func TestOlympusParse_Valid(t *testing.T) {
	b := buildOlympusMakerNote()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Parse: got nil map, want non-nil")
	}
	if _, ok := result[TagCameraType]; !ok {
		t.Errorf("TagCameraType (0x%04X) not found in result", TagCameraType)
	}
}

func TestOlympusParse_BigEndian(t *testing.T) {
	b := buildOlympusMakerNoteBE()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Parse: got nil map, want non-nil")
	}
	if _, ok := result[TagJpegQuality]; !ok {
		t.Errorf("TagJpegQuality (0x%04X) not found in result", TagJpegQuality)
	}
}

func TestOlympusParse_OffsetValue(t *testing.T) {
	b := buildOlympusMakerNoteWithOffset()
	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	val, ok := result[TagApertureValue]
	if !ok {
		t.Fatalf("TagApertureValue not found")
	}
	if len(val) != 8 {
		t.Errorf("TagApertureValue value len = %d, want 8", len(val))
	}
}

func TestOlympusParse_TooShort(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty", []byte{}},
		{"7 bytes", []byte("OLYMPUS")},
		{"magic only (8 bytes)", []byte("OLYMPUS\x00")},
		{"magic+BO (10 bytes)", []byte("OLYMPUS\x00II")},
		{"13 bytes (truncated count)", append([]byte("OLYMPUS\x00II\x03\x00"), 0x01)},
	}
	p := Parser{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := p.Parse(tc.b)
			if err != nil {
				t.Errorf("expected nil error, got: %v", err)
			}
			if result != nil {
				t.Errorf("expected nil result for short input, got: %v", result)
			}
		})
	}
}

func TestOlympusParse_BadMagic(t *testing.T) {
	b := make([]byte, 26)
	copy(b[0:8], "BADMAGIC")
	b[8] = 'I'
	b[9] = 'I'
	binary.LittleEndian.PutUint16(b[12:14], 1)

	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Errorf("expected nil error for bad magic, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for bad magic, got: %v", result)
	}
}

func TestOlympusParse_BadByteOrder(t *testing.T) {
	b := buildOlympusMakerNote()
	// Overwrite byte order with invalid value.
	b[8] = 'X'
	b[9] = 'X'

	p := Parser{}
	result, err := p.Parse(b)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for invalid byte order, got: %v", result)
	}
}

func TestTagConstants(t *testing.T) {
	if TagSpecialMode != 0x0200 {
		t.Errorf("TagSpecialMode = 0x%04X, want 0x0200", TagSpecialMode)
	}
	if TagCameraType != 0x0207 {
		t.Errorf("TagCameraType = 0x%04X, want 0x0207", TagCameraType)
	}
	if TagEquipment != 0x2010 {
		t.Errorf("TagEquipment = 0x%04X, want 0x2010", TagEquipment)
	}
}

func FuzzOlympusParse(f *testing.F) {
	// Seeds: valid inputs.
	f.Add(buildOlympusMakerNote())
	f.Add(buildOlympusMakerNoteBE())
	f.Add(buildOlympusMakerNoteWithOffset())
	// Seeds: degenerate inputs.
	f.Add([]byte{})
	f.Add([]byte("OLYMPUS\x00"))
	f.Add([]byte("OLYMPUS\x00II"))
	// Seed: magic with bad byte order.
	f.Add([]byte("OLYMPUS\x00XX\x03\x00\x01\x00"))
	// Seed: magic + all zeros.
	f.Add(make([]byte, 26))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		p := Parser{}
		_, _ = p.Parse(data)
	})
}
