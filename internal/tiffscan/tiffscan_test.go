package tiffscan

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fixtureIFD0Off is the IFD0 offset used by every buildIFD0 fixture.
const fixtureIFD0Off = 8

// buildIFD0 builds a minimal little-endian classic TIFF buffer whose IFD0
// begins at fixtureIFD0Off and carries the given entries. Each entry's value is
// placed out-of-line when longer than 4 bytes.
func buildIFD0(entries []struct {
	tag, typ uint16
	value    []byte
}) []byte {
	// Reserve space for count(2) + entries(12 each) + next-IFD(4), then an
	// out-of-line area for any value > 4 bytes.
	const ifd0Off = fixtureIFD0Off
	fixed := 2 + len(entries)*12 + 4
	extra := 0
	for _, e := range entries {
		if len(e.value) > 4 {
			extra += len(e.value)
		}
	}
	buf := make([]byte, ifd0Off+fixed, ifd0Off+fixed+extra)
	order := binary.LittleEndian
	order.PutUint16(buf[ifd0Off:], uint16(len(entries))) //nolint:gosec // test helper
	pos := ifd0Off + 2
	for _, e := range entries {
		order.PutUint16(buf[pos:], e.tag)
		order.PutUint16(buf[pos+2:], e.typ)
		order.PutUint32(buf[pos+4:], uint32(len(e.value)/elemSize(e.typ))) //nolint:gosec // test helper; Count = number of elements (TIFF 6.0 §2)
		if len(e.value) <= 4 {
			copy(buf[pos+8:pos+8+len(e.value)], e.value)
		} else {
			off := len(buf)
			buf = append(buf, e.value...)
			order.PutUint32(buf[pos+8:], uint32(off)) //nolint:gosec // test helper
		}
		pos += 12
	}
	return buf
}

// elemSize returns the element size of the TIFF types used by these fixtures.
func elemSize(typ uint16) int {
	switch typ {
	case 3:
		return 2
	case 4, 13:
		return 4
	}
	return 1
}

func TestExtractTagValues_IPTCTypeLongTrimsPadding(t *testing.T) {
	t.Parallel()
	// TypeLong (4), Count = 2 elements (8 bytes); the last 3 bytes are
	// structural zero padding.
	payload := []byte{0x1C, 0x02, 0x00, 0x01, 'x', 0x00, 0x00, 0x00}
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x83BB, 4, payload}})

	rawIPTC, rawXMP := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if rawXMP != nil {
		t.Errorf("rawXMP = %v, want nil", rawXMP)
	}
	want := []byte{0x1C, 0x02, 0x00, 0x01, 'x'}
	if !bytes.Equal(rawIPTC, want) {
		t.Errorf("rawIPTC = %v, want %v", rawIPTC, want)
	}
}

func TestExtractTagValues_IPTCTypeUndefinedNoTrim(t *testing.T) {
	t.Parallel()
	// TypeUndefined (7): trailing zero must NOT be trimmed (task #153 / ROBUST-16).
	payload := []byte{0x1C, 0x02, 0x00, 0x01, 0x00}
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x83BB, 7, payload}})

	rawIPTC, _ := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if !bytes.Equal(rawIPTC, payload) {
		t.Errorf("rawIPTC = %v, want %v (no trim for TypeUndefined)", rawIPTC, payload)
	}
}

func TestExtractTagValues_XMP(t *testing.T) {
	t.Parallel()
	xmpPacket := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x02BC, 1, xmpPacket}})

	rawIPTC, rawXMP := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil", rawIPTC)
	}
	if !bytes.Equal(rawXMP, xmpPacket) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpPacket)
	}
}

func TestExtractTagValues_BothTags(t *testing.T) {
	t.Parallel()
	iptc := []byte{0x1C, 0x02, 0x00, 0x01, 'y'}
	xmp := []byte(`<x:xmpmeta/>`)
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{
		{0x02BC, 1, xmp},
		{0x83BB, 7, iptc},
	})

	rawIPTC, rawXMP := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if !bytes.Equal(rawIPTC, iptc) {
		t.Errorf("rawIPTC = %v, want %v", rawIPTC, iptc)
	}
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmp)
	}
}

func TestExtractTagValues_NoTags(t *testing.T) {
	t.Parallel()
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x0100, 4, []byte{1, 0, 0, 0}}}) // ImageWidth, unrelated tag

	rawIPTC, rawXMP := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("rawIPTC=%v rawXMP=%v, want both nil", rawIPTC, rawXMP)
	}
}

func TestExtractTagValues_UnknownTypeSkipped(t *testing.T) {
	t.Parallel()
	// With type13IsIFD == false, an IPTC tag declared with type 13 is
	// skipped, not extracted.
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x83BB, 13, []byte{1, 2, 3, 4}}})

	rawIPTC, _ := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if rawIPTC != nil {
		t.Errorf("rawIPTC = %v, want nil (type 13 unrecognised)", rawIPTC)
	}
}

func TestExtractTagValues_TruncatedIFD0Header(t *testing.T) {
	t.Parallel()
	// ifd0Off+2 > len(data): cannot even read the entry count.
	rawIPTC, rawXMP := ExtractTagValues([]byte{0x00}, 4, binary.LittleEndian, false)
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("rawIPTC=%v rawXMP=%v, want both nil for truncated header", rawIPTC, rawXMP)
	}
}

func TestExtractTagValues_TruncatedEntry(t *testing.T) {
	t.Parallel()
	// Declares 2 entries but the buffer only holds room for the count field
	// plus a partial first entry.
	buf := make([]byte, 8+2+6)
	binary.LittleEndian.PutUint16(buf[8:], 2)
	rawIPTC, rawXMP := ExtractTagValues(buf, 8, binary.LittleEndian, false)
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("rawIPTC=%v rawXMP=%v, want both nil for truncated entry table", rawIPTC, rawXMP)
	}
}

func TestExtractTagValues_OutOfLineOffsetOutOfBounds(t *testing.T) {
	t.Parallel()
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x02BC, 1, []byte("0123456789")}})
	// Corrupt the out-of-line offset to point past the buffer.
	binary.LittleEndian.PutUint32(data[8+2+8:], uint32(len(data)+1000)) //nolint:gosec // test fixture: small length

	rawIPTC, rawXMP := ExtractTagValues(data, 8, binary.LittleEndian, false)
	if rawIPTC != nil || rawXMP != nil {
		t.Errorf("rawIPTC=%v rawXMP=%v, want both nil for out-of-bounds offset", rawIPTC, rawXMP)
	}
}

func TestExtractTagValues_BigEndian(t *testing.T) {
	t.Parallel()
	xmp := []byte(`<x:xmpmeta/>`)
	// buildIFD0 always writes little-endian; build the big-endian case
	// directly here.
	order := binary.BigEndian
	const ifd0Off = 8
	buf := make([]byte, ifd0Off+2+12+4, ifd0Off+2+12+4+len(xmp))
	order.PutUint16(buf[ifd0Off:], 1)
	pos := ifd0Off + 2
	order.PutUint16(buf[pos:], 0x02BC)
	order.PutUint16(buf[pos+2:], 1)                // TypeByte
	order.PutUint32(buf[pos+4:], uint32(len(xmp))) //nolint:gosec // test fixture: small length
	off := len(buf)
	buf = append(buf, xmp...)
	order.PutUint32(buf[pos+8:], uint32(off)) //nolint:gosec // test helper

	_, rawXMP := ExtractTagValues(buf, ifd0Off, order, false)
	if !bytes.Equal(rawXMP, xmp) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmp)
	}
}

func TestExtractTagValues_Type13IsIFD(t *testing.T) {
	t.Parallel()
	// With type13IsIFD == true, type 13 is a 4-byte element (TIFF 6.0
	// Extensions IFD type), matching format/tiff's type table.
	payload := []byte{1, 2, 3, 4}
	data := buildIFD0([]struct {
		tag, typ uint16
		value    []byte
	}{{0x83BB, 13, payload}})

	rawIPTC, _ := ExtractTagValues(data, 8, binary.LittleEndian, true)
	if !bytes.Equal(rawIPTC, payload) {
		t.Errorf("rawIPTC = %v, want %v (type 13 as IFD)", rawIPTC, payload)
	}
}
