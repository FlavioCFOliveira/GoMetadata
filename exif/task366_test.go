package exif

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"
)

// classicIFD0 builds a little-endian TIFF whose IFD0 holds the given
// 12-byte entries verbatim, in the given order, and no out-of-line data.
func classicIFD0(entries [][12]byte) []byte {
	b := make([]byte, 8+2+len(entries)*12+4)
	copy(b, "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], 8)
	binary.LittleEndian.PutUint16(b[8:], uint16(len(entries))) //nolint:gosec // G115: test helper, a handful of entries
	for i, e := range entries {
		copy(b[10+i*12:], e[:])
	}
	return b
}

// classicEntry returns a classic-TIFF IFD entry of count 1.
func classicEntry(tag, typ uint16, val uint32) (e [12]byte) {
	binary.LittleEndian.PutUint16(e[0:], tag)
	binary.LittleEndian.PutUint16(e[2:], typ)
	binary.LittleEndian.PutUint32(e[4:], 1)
	binary.LittleEndian.PutUint32(e[8:], val)
	return e
}

// encodedIFD0Tags returns the (tag, type) pairs of the IFD0 table of an
// encoded little-endian TIFF, in file order.
func encodedIFD0Tags(t *testing.T, b []byte) [][2]uint16 {
	t.Helper()
	ifd0 := binary.LittleEndian.Uint32(b[4:])
	n := int(binary.LittleEndian.Uint16(b[ifd0:]))
	out := make([][2]uint16, n)
	for i := range out {
		p := int(ifd0) + 2 + i*12
		out[i] = [2]uint16{binary.LittleEndian.Uint16(b[p:]), binary.LittleEndian.Uint16(b[p+2:])}
	}
	return out
}

// TestUnknownTypeEntryNotFirst covers task #366: an IFD0 entry of unknown type
// (here tag 0x0001, type 0, as in the Sony DSC-HX5V file) sorts before every
// valid tag; ExifTool takes an IFD whose first entry has an invalid type for
// corrupted and reads none of it, so Encode must not write such an entry
// first. V-14 still holds: the entry is kept, byte for byte, behind the first
// entry of known type, and the order of the other entries is unchanged.
func TestUnknownTypeEntryNotFirst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries [][12]byte
		want    [][2]uint16 // tag, type in written order
	}{
		{
			name: "type 0 sorts first (issue repro)",
			entries: [][12]byte{
				classicEntry(0x0112, 3, 1),
				classicEntry(0x0001, 0, 0xce),
				classicEntry(0x0213, 3, 2),
			},
			want: [][2]uint16{{0x0112, 3}, {0x0001, 0}, {0x0213, 3}},
		},
		{
			name: "several leading unknown types, 0 / 14 / 0xffff",
			entries: [][12]byte{
				classicEntry(0x0001, 0, 0),
				classicEntry(0x0002, 14, 7),
				classicEntry(0x0003, 0xffff, 9),
				classicEntry(0x0112, 3, 1),
				classicEntry(0x0213, 3, 2),
			},
			want: [][2]uint16{{0x0112, 3}, {0x0001, 0}, {0x0002, 14}, {0x0003, 0xffff}, {0x0213, 3}},
		},
		{
			name: "unknown type after a known first entry stays in place",
			entries: [][12]byte{
				classicEntry(0x0112, 3, 1),
				classicEntry(0x0213, 0, 2),
			},
			want: [][2]uint16{{0x0112, 3}, {0x0213, 0}},
		},
		{
			name: "only unknown types: nothing to move",
			entries: [][12]byte{
				classicEntry(0x0001, 0, 0),
				classicEntry(0x0002, 99, 0),
			},
			want: [][2]uint16{{0x0001, 0}, {0x0002, 99}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := Parse(classicIFD0(tc.entries))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			before := slices.Clone(e.IFD0.Entries)
			enc, err := Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if got := encodedIFD0Tags(t, enc); !slices.Equal(got, tc.want) {
				t.Fatalf("written IFD0 (tag, type) = %#x, want %#x", got, tc.want)
			}
			// The caller's entries are not reordered.
			for i := range before {
				if e.IFD0.Entries[i].Tag != before[i].Tag {
					t.Fatalf("Encode reordered the entries of the receiver")
				}
			}
			// The raw 4-byte field of every unknown-type entry survives.
			e2, err := Parse(enc)
			if err != nil {
				t.Fatalf("re-Parse: %v", err)
			}
			for _, en := range e.IFD0.Entries {
				got := e2.IFD0.Get(en.Tag)
				if got == nil || got.Type != en.Type || !bytes.Equal(got.Value, en.Value) {
					t.Fatalf("tag %#x lost or changed across the round trip: %+v -> %+v", en.Tag, en, got)
				}
			}
		})
	}
}

// TestUnknownTypeEntryNotFirstBigTIFF is the BigTIFF counterpart: types 16-18
// are valid there, so a LONG8 entry may lead; a type 0 entry may not.
func TestUnknownTypeEntryNotFirstBigTIFF(t *testing.T) {
	t.Parallel()

	le := binary.LittleEndian
	for _, tc := range []struct {
		name     string
		entries  []bigTIFFEntry
		wantType []uint16
	}{
		{
			name: "type 0 first",
			entries: []bigTIFFEntry{
				{tag: 0x0001, typ: 0, count: 1, payload: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
				{tag: 0x0112, typ: uint16(TypeShort), count: 1, payload: shortPayload(le, 1)},
			},
			wantType: []uint16{3, 0},
		},
		{
			name: "LONG8 first is valid",
			entries: []bigTIFFEntry{
				{tag: 0x0001, typ: 16, count: 1, payload: []byte{1, 0, 0, 0, 0, 0, 0, 0}},
				{tag: 0x0112, typ: uint16(TypeShort), count: 1, payload: shortPayload(le, 1)},
			},
			wantType: []uint16{16, 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := Parse(buildBigTIFF(le, tc.entries))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			enc, err := Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			ifd0 := le.Uint64(enc[8:])
			for i, want := range tc.wantType {
				if got := le.Uint16(enc[ifd0+8+uint64(i)*20+2:]); got != want {
					t.Fatalf("entry %d has type %d, want %d", i, got, want)
				}
			}
		})
	}
}
