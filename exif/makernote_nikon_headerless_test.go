package exif

import (
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// nikonHeaderless builds a headerless Nikon MakerNote IFD in order: count
// SHORT entries with the tags 1..count and the values 10..count+9, no next-IFD
// pointer beyond the 4 zero bytes.
// testOrder is a byte order that can also append.
type testOrder interface {
	binary.ByteOrder
	binary.AppendByteOrder
}

func nikonHeaderless(order testOrder, count int) []byte {
	b := order.AppendUint16(nil, uint16(count)) //nolint:gosec // G115: tiny test count
	for i := 1; i <= count; i++ {
		b = order.AppendUint16(b, uint16(i))
		b = order.AppendUint16(b, 3) // SHORT
		b = order.AppendUint32(b, 1) // count
		b = order.AppendUint16(b, uint16(i+9))
		b = order.AppendUint16(b, 0) // padding of the value field
	}
	return order.AppendUint32(b, 0)
}

// TestNikonHeaderless_N1_EitherByteOrder (task #364): a headerless Nikon note
// (Coolpix 775, E990, E5000: little-endian; D1: big-endian) is parsed in the
// byte order of its entry count, whatever the byte order of the TIFF stream.
func TestNikonHeaderless_N1_EitherByteOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		order testOrder
		count int
	}{
		{"little-endian", binary.LittleEndian, 16},
		{"big-endian", binary.BigEndian, 16},
		{"little-endian one entry", binary.LittleEndian, 1},
		{"big-endian one entry", binary.BigEndian, 1},
		{"little-endian 255 entries", binary.LittleEndian, 255},
		{"big-endian 255 entries", binary.BigEndian, 255},
	} {
		t.Run("R-30/"+tc.name, func(t *testing.T) {
			t.Parallel()
			ifd := parseNikonType1(nikonHeaderless(tc.order, tc.count), nil)
			if ifd == nil || len(ifd.Entries) != tc.count {
				t.Fatalf("parseNikonType1 = %v, want %d entries", ifd, tc.count)
			}
			for i, e := range ifd.Entries {
				if e.Tag != TagID(i+1) {
					t.Fatalf("entry %d has tag %#x", i, uint16(e.Tag))
				}
				if got := tc.order.Uint16(e.Value); int(got) != i+10 {
					t.Fatalf("entry %d value = %d, want %d", i, got, i+10)
				}
			}
		})
	}
}

// TestNikonHeaderless_N2_Refused: counts that are no IFD of either byte order
// are refused, as are notes too short to hold a count.
func TestNikonHeaderless_N2_Refused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"one byte", []byte{1}},
		{"count 0", []byte{0, 0, 1, 2, 3, 4}},
		{"count 256 either way", []byte{1, 1, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}},
		{"count above 255 in both orders", []byte{0x12, 0x34, 0, 0, 0, 0}},
		{"entries do not fit", []byte{0x10, 0x00, 1, 0, 3, 0}},
	} {
		t.Run("R-30/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if ifd := parseNikonType1(tc.b, nil); ifd != nil {
				t.Errorf("parseNikonType1 = %v, want nil", ifd)
			}
		})
	}
}

// TestNikonHeaderless_N3_ParseOfTheStream: exif.Parse reads the headerless note
// of a stream of either byte order through the Make "NIKON" (Nikon Type 2 of
// ExifTool, layout of Coolpix 775, E990).
func TestNikonHeaderless_N3_ParseOfTheStream(t *testing.T) {
	t.Parallel()
	for _, order := range []testOrder{binary.LittleEndian, binary.BigEndian} {
		short := func(v uint16) []byte { return order.AppendUint16(nil, v) }
		t.Run("R-30/"+order.String(), func(t *testing.T) {
			t.Parallel()
			tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
				Make: "NIKON", Order: order, TIFFRelative: true, PadBefore: 6,
				Entries: []testutil.MNEntry{
					{Tag: 0x0001, Type: 3, Count: 1, Value: short(7)},
					{Tag: 0x0002, Type: 3, Count: 1, Value: short(8)},
					{Tag: 0x0003, Type: 4, Count: 1, Value: order.AppendUint32(nil, 9)},
				},
			})
			e, err := Parse(tiff)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if e.MakerNoteIFD == nil || len(e.MakerNoteIFD.Entries) != 3 {
				t.Fatalf("MakerNoteIFD = %v, want 3 entries", e.MakerNoteIFD)
			}
			if got := order.Uint16(e.MakerNoteIFD.Get(0x0002).Value); got != 8 {
				t.Errorf("entry 2 = %d, want 8", got)
			}
		})
	}
}
