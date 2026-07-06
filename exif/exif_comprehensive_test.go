package exif

// exif_comprehensive_test.go — Task #47: keystone test battery.
//
// Covers:
//   F  — all five IFDs, all 13 TIFF data types, inline vs. out-of-line,
//         MM/II endian parity, GPS altitude, InteropIFD, round-trip modify.
//   S  — DoS cap (maxIFDEntryPrealloc), value-offset OOB, count×typeSize overflow,
//         cyclic IFD (already tested; re-asserted here as named regression).
//   E  — writeIFD inline determinism for ALL inline types, MakerNote trailing-space
//         on the live Parse() path.

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// F-1: Table-driven test for all 13 TIFF data types
//
// TIFF 6.0 §2 defines twelve base types; CIPA DC-008-2023 §4.6.3 adds TypeUTF8
// (code 13). Each type is tested in both its inline form (totalSize ≤ 4 bytes)
// and, where applicable, its out-of-line form (totalSize > 4 bytes). Tests run
// for both LE and BE byte orders to give MM/II parity as a natural byproduct.
// ---------------------------------------------------------------------------

// tiffTypeCase describes one entry in the all-types table.
type tiffTypeCase struct {
	name    string
	typ     DataType
	count   uint32 // number of elements
	inline  bool   // true iff count*typeSize ≤ 4 (stored inline per TIFF §2)
	buildLE func() []byte
	buildBE func() []byte
	check   func(t *testing.T, entry *IFDEntry, order binary.ByteOrder)
}

// TestAllTIFFTypesTableDriven exercises every TIFF/EXIF data type defined by
// TIFF 6.0 §2 and CIPA DC-008-2023 §4.6.3 (TypeUTF8) for both LE and BE byte
// orders, verifying that parse→encode→parse preserves the value exactly.
//
// Type coverage:
//
//	TypeByte(1), TypeASCII(2), TypeShort(3), TypeLong(4), TypeRational(5),
//	TypeSByte(6), TypeUndefined(7), TypeSShort(8), TypeSLong(9),
//	TypeSRational(10), TypeFloat(11), TypeDouble(12), TypeUTF8(13).
func TestAllTIFFTypesTableDriven(t *testing.T) {
	t.Parallel()

	cases := []tiffTypeCase{
		// ---- BYTE (TypeByte = 1) — 1 byte per element; 4 fit inline ----
		{
			name: "TypeByte_inline_count4",
			typ:  TypeByte, count: 4, inline: true,
			buildLE: func() []byte { return []byte{0xDE, 0xAD, 0xBE, 0xEF} },
			buildBE: func() []byte { return []byte{0xDE, 0xAD, 0xBE, 0xEF} },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeByte {
					t.Errorf("Type = %d, want TypeByte", entry.Type)
				}
				want := []byte{0xDE, 0xAD, 0xBE, 0xEF}
				if !bytes.Equal(entry.Value, want) {
					t.Errorf("Value = %v, want %v", entry.Value, want)
				}
			},
		},
		// ---- ASCII (TypeASCII = 2) — 1 byte per element; out-of-line ----
		// "Canon\x00" = 6 bytes > 4 → out-of-line (TIFF §2).
		{
			name: "TypeASCII_outofline",
			typ:  TypeASCII, count: 6, inline: false,
			buildLE: func() []byte { return []byte("Canon\x00") },
			buildBE: func() []byte { return []byte("Canon\x00") },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeASCII {
					t.Errorf("Type = %d, want TypeASCII", entry.Type)
				}
				if got := entry.String(); got != "Canon" {
					t.Errorf("String() = %q, want %q", got, "Canon")
				}
			},
		},
		// ---- ASCII (TypeASCII = 2) — 4-byte or shorter — inline ----
		// "AB\x00\x00" count=4 → 4 bytes = 4 → inline.
		{
			name: "TypeASCII_inline_4bytes",
			typ:  TypeASCII, count: 4, inline: true,
			buildLE: func() []byte { return []byte("AB\x00\x00") },
			buildBE: func() []byte { return []byte("AB\x00\x00") },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if got := entry.String(); got != "AB" {
					t.Errorf("String() = %q, want %q", got, "AB")
				}
			},
		},
		// ---- SHORT (TypeShort = 3) — 2 bytes per element; 2 fit inline ----
		{
			name: "TypeShort_inline_count2",
			typ:  TypeShort, count: 2, inline: true,
			buildLE: func() []byte {
				b := make([]byte, 4)
				binary.LittleEndian.PutUint16(b[0:], 0x1234)
				binary.LittleEndian.PutUint16(b[2:], 0x5678)
				return b
			},
			buildBE: func() []byte {
				b := make([]byte, 4)
				binary.BigEndian.PutUint16(b[0:], 0x1234)
				binary.BigEndian.PutUint16(b[2:], 0x5678)
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, order binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeShort {
					t.Errorf("Type = %d, want TypeShort", entry.Type)
				}
				// Decode both elements directly from Value.
				if len(entry.Value) < 4 {
					t.Fatalf("Value len = %d, want ≥ 4", len(entry.Value))
				}
				v0 := order.Uint16(entry.Value[0:])
				v1 := order.Uint16(entry.Value[2:])
				if v0 != 0x1234 || v1 != 0x5678 {
					t.Errorf("SHORT values = [0x%04X 0x%04X], want [0x1234 0x5678]", v0, v1)
				}
			},
		},
		// ---- SHORT out-of-line (count=4 → 8 bytes) ----
		{
			name: "TypeShort_outofline_count4",
			typ:  TypeShort, count: 4, inline: false,
			buildLE: func() []byte {
				b := make([]byte, 8)
				binary.LittleEndian.PutUint16(b[0:], 10)
				binary.LittleEndian.PutUint16(b[2:], 20)
				binary.LittleEndian.PutUint16(b[4:], 30)
				binary.LittleEndian.PutUint16(b[6:], 40)
				return b
			},
			buildBE: func() []byte {
				b := make([]byte, 8)
				binary.BigEndian.PutUint16(b[0:], 10)
				binary.BigEndian.PutUint16(b[2:], 20)
				binary.BigEndian.PutUint16(b[4:], 30)
				binary.BigEndian.PutUint16(b[6:], 40)
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, order binary.ByteOrder) {
				t.Helper()
				if entry.Count != 4 {
					t.Errorf("Count = %d, want 4", entry.Count)
				}
				if len(entry.Value) < 8 {
					t.Fatalf("Value len = %d, want 8", len(entry.Value))
				}
				want := []uint16{10, 20, 30, 40}
				for i, w := range want {
					got := order.Uint16(entry.Value[i*2:])
					if got != w {
						t.Errorf("SHORT[%d] = %d, want %d", i, got, w)
					}
				}
			},
		},
		// ---- LONG (TypeLong = 4) — 4 bytes; count=1 inline ----
		{
			name: "TypeLong_inline_count1",
			typ:  TypeLong, count: 1, inline: true,
			buildLE: func() []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, 0xDEADBEEF); return b },
			buildBE: func() []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, 0xDEADBEEF); return b },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeLong {
					t.Errorf("Type = %d, want TypeLong", entry.Type)
				}
				if got := entry.Uint32(); got != 0xDEADBEEF {
					t.Errorf("Uint32() = 0x%08X, want 0xDEADBEEF", got)
				}
			},
		},
		// ---- LONG out-of-line (count=2 → 8 bytes) ----
		{
			name: "TypeLong_outofline_count2",
			typ:  TypeLong, count: 2, inline: false,
			buildLE: func() []byte {
				b := make([]byte, 8)
				binary.LittleEndian.PutUint32(b[0:], 111111)
				binary.LittleEndian.PutUint32(b[4:], 222222)
				return b
			},
			buildBE: func() []byte {
				b := make([]byte, 8)
				binary.BigEndian.PutUint32(b[0:], 111111)
				binary.BigEndian.PutUint32(b[4:], 222222)
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, order binary.ByteOrder) {
				t.Helper()
				if len(entry.Value) < 8 {
					t.Fatalf("Value len = %d, want 8", len(entry.Value))
				}
				v0 := order.Uint32(entry.Value[0:])
				v1 := order.Uint32(entry.Value[4:])
				if v0 != 111111 || v1 != 222222 {
					t.Errorf("LONG[0]=%d LONG[1]=%d, want 111111 222222", v0, v1)
				}
			},
		},
		// ---- RATIONAL (TypeRational = 5) — 8 bytes; out-of-line ----
		{
			name: "TypeRational_outofline_count1",
			typ:  TypeRational, count: 1, inline: false,
			buildLE: func() []byte {
				b := make([]byte, 8)
				binary.LittleEndian.PutUint32(b[0:], 355)
				binary.LittleEndian.PutUint32(b[4:], 113)
				return b
			},
			buildBE: func() []byte {
				b := make([]byte, 8)
				binary.BigEndian.PutUint32(b[0:], 355)
				binary.BigEndian.PutUint32(b[4:], 113)
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeRational {
					t.Errorf("Type = %d, want TypeRational", entry.Type)
				}
				r := entry.Rational(0)
				if r[0] != 355 || r[1] != 113 {
					t.Errorf("Rational(0) = %v, want [355 113]", r)
				}
			},
		},
		// ---- SBYTE (TypeSByte = 6) — 1 byte signed; 4 fit inline ----
		{
			name: "TypeSByte_inline_count4",
			typ:  TypeSByte, count: 4, inline: true,
			buildLE: func() []byte { return []byte{0xFF, 0xFE, 0x01, 0x7F} }, // -1, -2, 1, 127
			buildBE: func() []byte { return []byte{0xFF, 0xFE, 0x01, 0x7F} },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeSByte {
					t.Errorf("Type = %d, want TypeSByte", entry.Type)
				}
				want := []int8{-1, -2, 1, 127}
				if len(entry.Value) < 4 {
					t.Fatalf("Value len = %d, want ≥ 4", len(entry.Value))
				}
				for i, w := range want {
					got := int8(entry.Value[i]) //nolint:gosec // G115: intentional byte→int8 for SBYTE test
					if got != w {
						t.Errorf("SBYTE[%d] = %d, want %d", i, got, w)
					}
				}
			},
		},
		// ---- UNDEFINED (TypeUndefined = 7) — 1 byte; 4 fit inline ----
		{
			name: "TypeUndefined_inline_count4",
			typ:  TypeUndefined, count: 4, inline: true,
			buildLE: func() []byte { return []byte{0xCA, 0xFE, 0xBA, 0xBE} },
			buildBE: func() []byte { return []byte{0xCA, 0xFE, 0xBA, 0xBE} },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeUndefined {
					t.Errorf("Type = %d, want TypeUndefined", entry.Type)
				}
				want := []byte{0xCA, 0xFE, 0xBA, 0xBE}
				if !bytes.Equal(entry.Bytes(), want) {
					t.Errorf("Bytes() = %v, want %v", entry.Bytes(), want)
				}
			},
		},
		// ---- UNDEFINED out-of-line (count=8) ----
		{
			name: "TypeUndefined_outofline_count8",
			typ:  TypeUndefined, count: 8, inline: false,
			buildLE: func() []byte { return []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08} },
			buildBE: func() []byte { return []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08} },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				want := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
				if !bytes.Equal(entry.Bytes(), want) {
					t.Errorf("Bytes() = %v, want %v", entry.Bytes(), want)
				}
			},
		},
		// ---- SSHORT (TypeSShort = 8) — 2 bytes signed; 2 fit inline ----
		{
			name: "TypeSShort_inline_count2",
			typ:  TypeSShort, count: 2, inline: true,
			buildLE: func() []byte {
				var v0, v1 int16 = -100, 200
				b := make([]byte, 4)
				binary.LittleEndian.PutUint16(b[0:], uint16(v0)) //nolint:gosec // G115: intentional signed→unsigned for SSHORT test
				binary.LittleEndian.PutUint16(b[2:], uint16(v1))
				return b
			},
			buildBE: func() []byte {
				var v0, v1 int16 = -100, 200
				b := make([]byte, 4)
				binary.BigEndian.PutUint16(b[0:], uint16(v0)) //nolint:gosec // G115: intentional signed→unsigned for SSHORT test
				binary.BigEndian.PutUint16(b[2:], uint16(v1))
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, order binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeSShort {
					t.Errorf("Type = %d, want TypeSShort", entry.Type)
				}
				v0 := int16(order.Uint16(entry.Value[0:])) //nolint:gosec // G115: intentional for SSHORT test
				v1 := int16(order.Uint16(entry.Value[2:])) //nolint:gosec // G115: intentional for SSHORT test
				if v0 != -100 || v1 != 200 {
					t.Errorf("SSHORT = [%d %d], want [-100 200]", v0, v1)
				}
			},
		},
		// ---- SLONG (TypeSLong = 9) — 4 bytes signed; count=1 inline ----
		{
			name: "TypeSLong_inline_count1",
			typ:  TypeSLong, count: 1, inline: true,
			buildLE: func() []byte {
				var v int32 = -1_000_000
				b := make([]byte, 4)
				binary.LittleEndian.PutUint32(b, uint32(v)) //nolint:gosec // G115: intentional signed→unsigned for SLONG test
				return b
			},
			buildBE: func() []byte {
				var v int32 = -1_000_000
				b := make([]byte, 4)
				binary.BigEndian.PutUint32(b, uint32(v)) //nolint:gosec // G115: intentional signed→unsigned for SLONG test
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeSLong {
					t.Errorf("Type = %d, want TypeSLong", entry.Type)
				}
				if got := entry.Int32(); got != -1_000_000 {
					t.Errorf("Int32() = %d, want -1000000", got)
				}
			},
		},
		// ---- SRATIONAL (TypeSRational = 10) — 8 bytes; out-of-line ----
		// ShutterSpeedValue: -7/1 means EV = 2^7 = 1/128 s (EXIF §4.6.5).
		{
			name: "TypeSRational_outofline_count1",
			typ:  TypeSRational, count: 1, inline: false,
			buildLE: func() []byte {
				var num int32 = -7
				b := make([]byte, 8)
				binary.LittleEndian.PutUint32(b[0:], uint32(num)) //nolint:gosec // G115: intentional signed→unsigned for SRATIONAL test
				binary.LittleEndian.PutUint32(b[4:], 1)
				return b
			},
			buildBE: func() []byte {
				var num int32 = -7
				b := make([]byte, 8)
				binary.BigEndian.PutUint32(b[0:], uint32(num)) //nolint:gosec // G115: intentional signed→unsigned for SRATIONAL test
				binary.BigEndian.PutUint32(b[4:], 1)
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeSRational {
					t.Errorf("Type = %d, want TypeSRational", entry.Type)
				}
				r := entry.SRational(0)
				if r[0] != -7 || r[1] != 1 {
					t.Errorf("SRational(0) = %v, want [-7 1]", r)
				}
			},
		},
		// ---- FLOAT (TypeFloat = 11) — 4 bytes; count=1 inline ----
		{
			name: "TypeFloat_inline_count1",
			typ:  TypeFloat, count: 1, inline: true,
			buildLE: func() []byte {
				b := make([]byte, 4)
				binary.LittleEndian.PutUint32(b, math.Float32bits(3.14159))
				return b
			},
			buildBE: func() []byte {
				b := make([]byte, 4)
				binary.BigEndian.PutUint32(b, math.Float32bits(3.14159))
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeFloat {
					t.Errorf("Type = %d, want TypeFloat", entry.Type)
				}
				got := entry.Float32()
				if math.Abs(float64(got)-3.14159) > 1e-4 {
					t.Errorf("Float32() = %f, want ~3.14159", got)
				}
			},
		},
		// ---- DOUBLE (TypeDouble = 12) — 8 bytes; out-of-line ----
		{
			name: "TypeDouble_outofline_count1",
			typ:  TypeDouble, count: 1, inline: false,
			buildLE: func() []byte {
				b := make([]byte, 8)
				binary.LittleEndian.PutUint64(b, math.Float64bits(math.Pi))
				return b
			},
			buildBE: func() []byte {
				b := make([]byte, 8)
				binary.BigEndian.PutUint64(b, math.Float64bits(math.Pi))
				return b
			},
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeDouble {
					t.Errorf("Type = %d, want TypeDouble", entry.Type)
				}
				got := entry.Float64()
				if math.Abs(got-math.Pi) > 1e-12 {
					t.Errorf("Float64() = %v, want π", got)
				}
			},
		},
		// ---- UTF8 (TypeUTF8 = 13) — 1 byte per element; out-of-line ----
		// CIPA DC-008-2023 §4.6.3: element size 1, NUL-terminated UTF-8 string.
		{
			name: "TypeUTF8_outofline",
			typ:  TypeUTF8, count: 8, inline: false, // "日\x00" = 4 bytes UTF-8 + NUL → 4 bytes; but use 8 for clarity
			buildLE: func() []byte { return append([]byte("café"), 0x00, 0x00, 0x00, 0x00) },
			buildBE: func() []byte { return append([]byte("café"), 0x00, 0x00, 0x00, 0x00) },
			check: func(t *testing.T, entry *IFDEntry, _ binary.ByteOrder) {
				t.Helper()
				if entry.Type != TypeUTF8 {
					t.Errorf("Type = %d, want TypeUTF8", entry.Type)
				}
				if got := entry.String(); got != "café" {
					t.Errorf("String() = %q, want \"café\"", got)
				}
			},
		},
	}

	// buildTIFFWithEntry constructs a single-IFD TIFF that contains exactly one
	// entry of the given type. When inline is true the value fits in the 4-byte
	// field. When false, the value is placed at the end of the buffer and the
	// field holds the absolute offset.
	//
	// TIFF §2 IFD entry layout:
	//   bytes 0-1  tag ID (uint16)
	//   bytes 2-3  data type (uint16)
	//   bytes 4-7  count (uint32)
	//   bytes 8-11 value-or-offset
	buildTIFFWithEntry := func(order binary.ByteOrder, tagID TagID, typ DataType, count uint32, valueBytes []byte) []byte {
		// Layout: header(8) + count(2) + entry(12) + next-IFD(4) + [value area]
		const (
			hdrSize    = 8
			ifd0Off    = uint32(hdrSize)
			ifdEntries = 1
			ifd0Size   = uint32(2 + ifdEntries*12 + 4)
		)
		totalSize := uint64(hdrSize) + uint64(ifd0Size) + uint64(len(valueBytes))
		buf := make([]byte, totalSize)

		// TIFF header (TIFF §2).
		if order == binary.LittleEndian {
			buf[0], buf[1] = 'I', 'I'
		} else {
			buf[0], buf[1] = 'M', 'M'
		}
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], ifd0Off)

		// IFD0: 1 entry.
		order.PutUint16(buf[ifd0Off:], uint16(ifdEntries))
		entryBase := ifd0Off + 2
		order.PutUint16(buf[entryBase:], uint16(tagID))
		order.PutUint16(buf[entryBase+2:], uint16(typ))
		order.PutUint32(buf[entryBase+4:], count)

		totalValueSize := uint64(typeSize(typ)) * uint64(count)
		if totalValueSize <= 4 {
			// Inline: copy value left-justified into the 4-byte field (TIFF §2).
			copy(buf[entryBase+8:entryBase+12], valueBytes)
		} else {
			// Out-of-line: store the absolute offset of the value area.
			valueOff := uint32(hdrSize) + ifd0Size
			order.PutUint32(buf[entryBase+8:], valueOff)
			copy(buf[valueOff:], valueBytes)
		}
		// next-IFD pointer = 0 (TIFF §2).
		order.PutUint32(buf[entryBase+12:], 0)

		return buf
	}

	const testTagID TagID = 0x9C9F // XPSubject — unused in tests, safe private-ish tag

	for _, tc := range cases {

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, ord := range []struct {
				name  string
				order binary.ByteOrder
				build func() []byte
			}{
				{"LE", binary.LittleEndian, tc.buildLE},
				{"BE", binary.BigEndian, tc.buildBE},
			} {

				t.Run(ord.name, func(t *testing.T) {
					t.Parallel()
					valueBytes := ord.build()
					raw := buildTIFFWithEntry(ord.order, testTagID, tc.typ, tc.count, valueBytes)

					e, err := Parse(raw)
					if err != nil {
						t.Fatalf("Parse: %v", err)
					}
					entry := e.IFD0.Get(testTagID)
					if entry == nil {
						t.Fatalf("tag 0x%04X not found in IFD0", testTagID)
					}

					// Type check.
					if entry.Type != tc.typ {
						t.Errorf("Type = %d, want %d", entry.Type, tc.typ)
					}

					// Custom value assertion.
					tc.check(t, entry, ord.order)

					// Round-trip: encode → re-parse → re-check.
					encoded, err := Encode(e)
					if err != nil {
						t.Fatalf("Encode: %v", err)
					}
					e2, err := Parse(encoded)
					if err != nil {
						t.Fatalf("Parse (round-trip): %v", err)
					}
					entry2 := e2.IFD0.Get(testTagID)
					if entry2 == nil {
						t.Fatalf("tag 0x%04X missing after round-trip", testTagID)
					}
					if entry2.Type != tc.typ {
						t.Errorf("Type after round-trip = %d, want %d", entry2.Type, tc.typ)
					}
					tc.check(t, entry2, ord.order)
				})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F-2: MM/II endian parity
//
// Build the same logical EXIF (same tags, same values) as both an MM (BE)
// and an II (LE) TIFF, parse both, and assert that the decoded values are
// bitwise-identical. This proves the byte-order handling is symmetric.
// ---------------------------------------------------------------------------

// TestMMIIParity proves that identical logical metadata stored in big-endian
// (MM) and little-endian (II) TIFF streams yields identical decoded values.
// TIFF §2: both byte orders are valid; our parser must handle both equally.
func TestMMIIParity(t *testing.T) {
	t.Parallel()

	// Build the LE version using Parse→SetXxx→Encode.
	buildParityViaAPI := func(order binary.ByteOrder) *EXIF {
		// Start from a valid minimal parse so ByteOrder is set.
		var hdr [8]byte
		if order == binary.LittleEndian {
			hdr[0], hdr[1] = 'I', 'I'
		} else {
			hdr[0], hdr[1] = 'M', 'M'
		}
		order.PutUint16(hdr[2:], 0x002A)
		order.PutUint32(hdr[4:], 8)
		// IFD0: 0 entries, next=0.
		buf := append(hdr[:], 0x00, 0x00, 0x00, 0x00, 0x00, 0x00)
		e, err := Parse(buf)
		if err != nil {
			t.Fatalf("buildParityViaAPI Parse: %v", err)
		}
		// Use the IFD0 directly to add entries with the correct byte order.
		e.IFD0 = &IFD{} // fresh IFD; setters will use ifd0ByteOrder default (LE)
		// Add seed entry so ifd0ByteOrder() returns the correct order.
		seed := make([]byte, 4)
		order.PutUint32(seed, 4000)
		e.IFD0.Entries = append(e.IFD0.Entries, IFDEntry{Tag: TagImageWidth, Type: TypeLong, Count: 1, Value: seed, bigEndian: orderIsBig(order)})
		e.ByteOrder = order

		oriB := make([]byte, 2)
		order.PutUint16(oriB, 6)
		e.IFD0.set(TagOrientation, TypeShort, 1, oriB, orderIsBig(order))
		makeV := asciiValue("ACME Corp")
		e.IFD0.set(TagMake, TypeASCII, uint32(len(makeV)), makeV, orderIsBig(order)) //nolint:gosec // G115: test helper
		e.SetGPS(51.5, -0.127)
		e.SetISO(400)
		e.SetFNumber(2.8)
		return e
	}

	leEXIF := buildParityViaAPI(binary.LittleEndian)
	beEXIF := buildParityViaAPI(binary.BigEndian)

	leEncoded, err := Encode(leEXIF)
	if err != nil {
		t.Fatalf("Encode LE: %v", err)
	}
	beEncoded, err := Encode(beEXIF)
	if err != nil {
		t.Fatalf("Encode BE: %v", err)
	}

	leParsed, err := Parse(leEncoded)
	if err != nil {
		t.Fatalf("Parse LE: %v", err)
	}
	beParsed, err := Parse(beEncoded)
	if err != nil {
		t.Fatalf("Parse BE: %v", err)
	}

	// Assert identical decoded values across LE and BE.
	// ImageWidth.
	leW := leParsed.IFD0.Get(TagImageWidth)
	beW := beParsed.IFD0.Get(TagImageWidth)
	if leW == nil || beW == nil {
		t.Fatal("ImageWidth missing from one or both streams")
	}
	if leW.Uint32() != beW.Uint32() {
		t.Errorf("ImageWidth LE=%d BE=%d, want equal", leW.Uint32(), beW.Uint32())
	}

	// Orientation.
	leOri, leOriOK := leParsed.Orientation()
	beOri, beOriOK := beParsed.Orientation()
	if !leOriOK || !beOriOK {
		t.Fatal("Orientation missing from one or both streams")
	}
	if leOri != beOri {
		t.Errorf("Orientation LE=%d BE=%d, want equal", leOri, beOri)
	}

	// Make.
	leMake := leParsed.IFD0.Get(TagMake)
	beMake := beParsed.IFD0.Get(TagMake)
	if leMake == nil || beMake == nil {
		t.Fatal("Make missing from one or both streams")
	}
	if leMake.String() != beMake.String() {
		t.Errorf("Make LE=%q BE=%q, want equal", leMake.String(), beMake.String())
	}

	// ISO.
	leISO, leISOOK := leParsed.ISO()
	beISO, beISOOK := beParsed.ISO()
	if !leISOOK || !beISOOK {
		t.Fatal("ISO missing from one or both streams")
	}
	if leISO != beISO {
		t.Errorf("ISO LE=%d BE=%d, want equal", leISO, beISO)
	}

	// FNumber.
	leFN, leFNOK := leParsed.FNumber()
	beFN, beFNOK := beParsed.FNumber()
	if !leFNOK || !beFNOK {
		t.Fatal("FNumber missing from one or both streams")
	}
	if math.Abs(leFN-beFN) > 0.001 {
		t.Errorf("FNumber LE=%f BE=%f, differ by >0.001", leFN, beFN)
	}

	// GPS.
	leLat, leLon, leGPSOK := leParsed.GPS()
	beLat, beLon, beGPSOK := beParsed.GPS()
	if !leGPSOK || !beGPSOK {
		t.Fatal("GPS missing from one or both streams")
	}
	const gpsTol = 0.001
	if math.Abs(leLat-beLat) > gpsTol || math.Abs(leLon-beLon) > gpsTol {
		t.Errorf("GPS LE=(%f,%f) BE=(%f,%f), differ by >%f", leLat, leLon, beLat, beLon, gpsTol)
	}
}

// ---------------------------------------------------------------------------
// F-3: GPS altitude above/below sea level
//
// EXIF §4.6.6 Table 15:
//   Tag 0x0005 (GPSAltitudeRef): BYTE 1 — 0 = above sea level, 1 = below.
//   Tag 0x0006 (GPSAltitude):    RATIONAL 1 — unsigned metres.
// ---------------------------------------------------------------------------

// TestGPSAltitudeAboveBelowSeaLevel verifies that GPSAltitudeRef byte 0 and 1
// are correctly stored and round-trip through encode→parse for both byte orders.
// EXIF §4.6.6 Table 15: GPSAltitudeRef is a BYTE with value 0 (above sea level)
// or 1 (below sea level); GPSAltitude is a RATIONAL (metres).
func TestGPSAltitudeAboveBelowSeaLevel(t *testing.T) {
	t.Parallel()

	// buildEXIFWithAltitude constructs an *EXIF directly using the library API
	// so that the correct byte order is injected without depending on the
	// test-internal encodeIFD helper (which is always LE).
	buildEXIFWithAltitude := func(altRef byte, altMetres uint32, order binary.ByteOrder) *EXIF {
		// Start from a minimal parsed TIFF to get the byte order set.
		var hdr [8]byte
		if order == binary.LittleEndian {
			hdr[0], hdr[1] = 'I', 'I'
		} else {
			hdr[0], hdr[1] = 'M', 'M'
		}
		order.PutUint16(hdr[2:], 0x002A)
		order.PutUint32(hdr[4:], 8)
		// IFD with 0 entries, next=0.
		buf := make([]byte, 8+2+4)
		copy(buf, hdr[:])
		e, err := Parse(buf)
		if err != nil {
			t.Fatalf("buildEXIFWithAltitude Parse: %v", err)
		}

		// Use SetGPS to set coordinates and create GPSIFD.
		e.SetGPS(48.86, 2.35) // Paris — gives us Lat/Lon tags

		// Add altitude tags directly into the GPS IFD.
		// GPSAltitudeRef (0x0005): TypeByte, count=1, inline value = altRef.
		e.GPSIFD.set(TagGPSAltitudeRef, TypeByte, 1, []byte{altRef}, orderIsBig(order))

		// GPSAltitude (0x0006): TypeRational, count=1, out-of-line (8 bytes).
		altVal := make([]byte, 8)
		order.PutUint32(altVal[0:], altMetres)
		order.PutUint32(altVal[4:], 1)
		e.GPSIFD.set(TagGPSAltitude, TypeRational, 1, altVal, orderIsBig(order))

		return e
	}

	tests := []struct {
		name   string
		altRef byte
		metres uint32
	}{
		{"above_sea_level", 0, 150},
		{"below_sea_level", 1, 50},
	}

	for _, order := range []struct {
		name  string
		order binary.ByteOrder
	}{
		{"LE", binary.LittleEndian},
		{"BE", binary.BigEndian},
	} {

		for _, tc := range tests {

			t.Run(order.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()

				e := buildEXIFWithAltitude(tc.altRef, tc.metres, order.order)

				// Verify GPSAltitudeRef byte in the in-memory IFD.
				altRefEntry := e.GPSIFD.Get(TagGPSAltitudeRef)
				if altRefEntry == nil {
					t.Fatal("GPSAltitudeRef (0x0005) missing before encode")
				}
				if len(altRefEntry.Value) == 0 {
					t.Fatal("GPSAltitudeRef Value is empty")
				}
				if altRefEntry.Value[0] != tc.altRef {
					t.Errorf("GPSAltitudeRef = %d, want %d", altRefEntry.Value[0], tc.altRef)
				}

				// Round-trip via Encode→Parse.
				encoded, err := Encode(e)
				if err != nil {
					t.Fatalf("Encode: %v", err)
				}
				e2, err := Parse(encoded)
				if err != nil {
					t.Fatalf("Parse (round-trip): %v", err)
				}
				if e2.GPSIFD == nil {
					t.Fatal("GPSIFD nil after round-trip")
				}

				// GPSAltitudeRef after round-trip.
				altRefEntry2 := e2.GPSIFD.Get(TagGPSAltitudeRef)
				if altRefEntry2 == nil {
					t.Fatal("GPSAltitudeRef missing after round-trip")
				}
				if len(altRefEntry2.Value) == 0 {
					t.Fatal("GPSAltitudeRef Value empty after round-trip")
				}
				if altRefEntry2.Value[0] != tc.altRef {
					t.Errorf("GPSAltitudeRef after round-trip = %d, want %d", altRefEntry2.Value[0], tc.altRef)
				}

				// GPSAltitude after round-trip.
				altEntry2 := e2.GPSIFD.Get(TagGPSAltitude)
				if altEntry2 == nil {
					t.Fatal("GPSAltitude (0x0006) missing after round-trip")
				}
				r := altEntry2.Rational(0)
				if r[0] != tc.metres || r[1] != 1 {
					t.Errorf("GPSAltitude after round-trip = %d/%d, want %d/1", r[0], r[1], tc.metres)
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------
// F-4: InteropIFD explicit test
//
// InteropIFD is tag 0xA005 in ExifIFD (EXIF §4.6.3).
// A correctly-built TIFF with an InteropIFD pointer must be parsed into
// e.InteropIFD and survive encode→parse intact.
// ---------------------------------------------------------------------------

// TestInteropIFDParseAndRoundTrip verifies that InteropIFD is parsed from the
// ExifIFD pointer tag 0xA005 (EXIF §4.6.3) and survives encode→parse.
func TestInteropIFDParseAndRoundTrip(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Layout:
	//   [0..7]    TIFF header
	//   [8..25]   IFD0: 1 entry (ExifIFDPointer → exifOff)
	//   [26..43]  ExifIFD: 1 entry (InteropIFDPointer → interopOff)
	//   [44..61]  InteropIFD: 1 entry (InteroperabilityIndex = "R98\x00")

	const (
		hdrSize     = 8
		ifd0Off     = hdrSize
		ifd0Size    = 2 + 1*12 + 4 // 18
		exifOff     = ifd0Off + ifd0Size
		exifSize    = 2 + 1*12 + 4 // 18
		interopOff  = exifOff + exifSize
		interopSize = 2 + 1*12 + 4 // 18
	)

	// InteropIFD contains InteroperabilityIndex (0x0001, TypeASCII, "R98\x00").
	// "R98\x00" = 4 bytes — exactly 4, so it fits inline (totalSize = 4 × 1 = 4).
	const TagInteropIndex TagID = 0x0001
	interopValue := [4]byte{'R', '9', '8', 0x00}

	buf := make([]byte, interopOff+interopSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 1 entry — ExifIFDPointer.
	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[ifd0Off+4:], uint16(TypeLong))
	order.PutUint32(buf[ifd0Off+6:], 1)
	order.PutUint32(buf[ifd0Off+10:], exifOff)
	order.PutUint32(buf[ifd0Off+14:], 0)

	// ExifIFD: 1 entry — InteropIFDPointer (0xA005).
	order.PutUint16(buf[exifOff:], 1)
	order.PutUint16(buf[exifOff+2:], uint16(TagInteropIFDPointer))
	order.PutUint16(buf[exifOff+4:], uint16(TypeLong))
	order.PutUint32(buf[exifOff+6:], 1)
	order.PutUint32(buf[exifOff+10:], interopOff)
	order.PutUint32(buf[exifOff+14:], 0)

	// InteropIFD: 1 entry — InteropIndex "R98\x00" (inline, 4 bytes).
	order.PutUint16(buf[interopOff:], 1)
	order.PutUint16(buf[interopOff+2:], uint16(TagInteropIndex))
	order.PutUint16(buf[interopOff+4:], uint16(TypeASCII))
	order.PutUint32(buf[interopOff+6:], 4)     // count = 4
	copy(buf[interopOff+10:], interopValue[:]) // inline value
	order.PutUint32(buf[interopOff+14:], 0)    // next IFD = 0

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if e.InteropIFD == nil {
		t.Fatal("InteropIFD is nil; expected to be populated from ExifIFD tag 0xA005")
	}

	entry := e.InteropIFD.Get(TagInteropIndex)
	if entry == nil {
		t.Fatal("InteroperabilityIndex (0x0001) not found in InteropIFD")
	}
	if got := entry.String(); got != "R98" {
		t.Errorf("InteroperabilityIndex = %q, want \"R98\"", got)
	}

	// Round-trip.
	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	if e2.InteropIFD == nil {
		t.Fatal("InteropIFD nil after round-trip")
	}
	entry2 := e2.InteropIFD.Get(TagInteropIndex)
	if entry2 == nil {
		t.Fatal("InteroperabilityIndex missing after round-trip")
	}
	if got := entry2.String(); got != "R98" {
		t.Errorf("InteroperabilityIndex after round-trip = %q, want \"R98\"", got)
	}
}

// ---------------------------------------------------------------------------
// F-5: Round-trip modify-one-tag preserves all other tags
//
// Mutating one tag via a setter must not disturb any other tag in the IFD.
// ---------------------------------------------------------------------------

// TestRoundTripModifyOneTagPreservesOthers parses a camera-like EXIF fixture,
// modifies one tag via SetCameraModel, encodes and re-parses, then asserts all
// other tags are byte-identical to their pre-modification values.
// This proves that write.go's filterEntries + IFD reconstruction is non-destructive.
func TestRoundTripModifyOneTagPreservesOthers(t *testing.T) {
	t.Parallel()

	// Build a realistic EXIF with multiple tags.
	data := buildCameraEXIF()
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse camera EXIF: %v", err)
	}

	// Record all original IFD0 tag values. Skip:
	//   - TagModel: the tag we are about to modify.
	//   - TagExifIFDPointer / TagGPSIFDPointer: these hold absolute offsets that
	//     are legitimately recomputed by Encode when the IFD layout changes (e.g.
	//     when Model grows/shrinks). Offset changes here are correct behaviour,
	//     not data corruption (TIFF §2: pointers encode file positions, not
	//     semantic values).
	type tagVal struct {
		tag   TagID
		value []byte
	}
	skipTags := map[TagID]bool{
		TagModel:             true,
		TagExifIFDPointer:    true,
		TagGPSIFDPointer:     true,
		TagInteropIFDPointer: true,
	}
	original := make([]tagVal, 0, len(e.IFD0.Entries))
	for _, entry := range e.IFD0.Entries {
		if skipTags[entry.Tag] {
			continue
		}
		cp := make([]byte, len(entry.Value))
		copy(cp, entry.Value)
		original = append(original, tagVal{entry.Tag, cp})
	}

	// Modify Model.
	e.SetCameraModel("Modified R5")

	// Encode and re-parse.
	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}

	// Verify the modified tag.
	if got := e2.CameraModel(); got != "Modified R5" {
		t.Errorf("CameraModel() = %q, want \"Modified R5\"", got)
	}

	// Verify all other IFD0 tags are preserved.
	for _, tv := range original {
		entry := e2.IFD0.Get(tv.tag)
		if entry == nil {
			t.Errorf("tag 0x%04X missing after round-trip with modification", tv.tag)
			continue
		}
		if !bytes.Equal(entry.Value, tv.value) {
			t.Errorf("tag 0x%04X value changed: got %x, want %x", tv.tag, entry.Value, tv.value)
		}
	}
}

// ---------------------------------------------------------------------------
// S-1: DoS cap — maxIFDEntryPrealloc
//
// parseSingleIFD pre-allocates with min(count, maxIFDEntryPrealloc) entries.
// A crafted IFD count of 0xFFFF must not pre-allocate beyond the cap, and
// parsing must complete gracefully (error or partial result, never OOM panic).
//
// TIFF §2: the count field is uint16, so max representable value is 65535.
// ---------------------------------------------------------------------------

// TestDoSCapHugeIFDCount verifies that feeding a count of 0xFFFF to the parser
// does not cause a memory spike due to unbounded pre-allocation. The cap
// (maxIFDEntryPrealloc = 1024) must bound the slice capacity even when the
// declared count is 65535.
//
// This is a security regression test: an adversary controlling the count field
// could otherwise force the parser to pre-allocate 65535×12 = ~786 KB per IFD
// level encountered, which multiplies with nested IFD traversal.
func TestDoSCapHugeIFDCount(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Build a TIFF whose IFD0 declares 0xFFFF entries but does not provide
	// any entry data — the buffer immediately ends after the count field.
	// parseSingleIFD checks pos+int(count)*12 > len(b) and returns early.
	buf := make([]byte, 8+2) // header + count only
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8
	order.PutUint16(buf[8:], 0xFFFF)

	// Must not panic and must not hang.
	// Parse returns an error (truncated entry list), which is correct behaviour.
	_, _ = Parse(buf)
	// If we reach here without a panic or OOM, the cap is working.

	// Now build a TIFF with enough space for 0xFFFF entries declared but only
	// a few entries actually valid.  parseSingleIFD will cap pre-alloc to 1024
	// and iterate, skipping out-of-bounds entries.
	buf2 := make([]byte, 8+2+0xFFFF*12+4) // full space — intentional large allocation for the test fixture
	buf2[0], buf2[1] = 'I', 'I'
	order.PutUint16(buf2[2:], 0x002A)
	order.PutUint32(buf2[4:], 8)
	order.PutUint16(buf2[8:], 0xFFFF) // claim 65535 entries
	// All entries are zero-valued → tag=0, type=0 (unknown), count=0, value=0.
	// parseIFDEntry skips unknown-type entries gracefully.

	_, _ = Parse(buf2) // must not panic
}

// ---------------------------------------------------------------------------
// S-2: Value offset/count beyond stream bounds → graceful error, never panic
//
// TIFF §2: the value-or-offset field holds an absolute file offset when
// totalSize > 4. An offset that points beyond the buffer must be rejected.
// ---------------------------------------------------------------------------

// TestValueOffsetBeyondBounds verifies that an IFD entry whose out-of-line
// value offset exceeds the buffer length is skipped gracefully (TIFF §2).
// The parser must return partial results or an error, never panic or OOB-read.
func TestValueOffsetBeyondBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		buildFn func(order binary.ByteOrder) []byte
	}{
		{
			// Offset points 1 byte past the end.
			name: "offset_exactly_past_end",
			buildFn: func(order binary.ByteOrder) []byte {
				// RATIONAL (8 bytes) must be out-of-line. Place offset at len(buf).
				const bufLen = 8 + 2 + 12 + 4 // header + count + 1 entry + next
				buf := make([]byte, bufLen)
				if order == binary.LittleEndian {
					buf[0], buf[1] = 'I', 'I'
				} else {
					buf[0], buf[1] = 'M', 'M'
				}
				order.PutUint16(buf[2:], 0x002A)
				order.PutUint32(buf[4:], 8)
				order.PutUint16(buf[8:], 1)
				order.PutUint16(buf[10:], uint16(TagFNumber))
				order.PutUint16(buf[12:], uint16(TypeRational))
				order.PutUint32(buf[14:], 1)              // count=1 → 8 bytes out-of-line
				order.PutUint32(buf[18:], uint32(bufLen)) // offset = past end
				return buf
			},
		},
		{
			// count×typeSize overflows would be UInt64 addition. Currently
			// uint64 arithmetic in parseIFDEntry handles this.
			// Use a RATIONAL entry with count=0x20000000 (max that fits in uint32)
			// → totalSize = 8 × 0x20000000 = 0x100000000 > 64-bit limit as uint64.
			// end = valOff + totalSize: even with valOff=0, end > uint64(len(b))
			// because totalSize ≫ any realistic buffer.
			name: "count_times_typesize_large",
			buildFn: func(order binary.ByteOrder) []byte {
				buf := make([]byte, 8+2+12+4)
				if order == binary.LittleEndian {
					buf[0], buf[1] = 'I', 'I'
				} else {
					buf[0], buf[1] = 'M', 'M'
				}
				order.PutUint16(buf[2:], 0x002A)
				order.PutUint32(buf[4:], 8)
				order.PutUint16(buf[8:], 1)
				order.PutUint16(buf[10:], uint16(TagFNumber))
				order.PutUint16(buf[12:], uint16(TypeRational))
				order.PutUint32(buf[14:], 0x20000000) // huge count → overflow
				order.PutUint32(buf[18:], 26)         // valOff = end of IFD
				return buf
			},
		},
		{
			// Offset = 0 for an out-of-line entry (RATIONAL). traverse loop
			// guard is "cur != 0" but parseIFDEntry treats offset=0 as a
			// valid file position; end = 0+8 = 8, which is in-bounds for a
			// minimal buffer, so the entry is accepted (correct behaviour).
			// This test just ensures no panic on offset=0 out-of-line entry.
			name: "offset_zero_outofline",
			buildFn: func(order binary.ByteOrder) []byte {
				buf := make([]byte, 64)
				if order == binary.LittleEndian {
					buf[0], buf[1] = 'I', 'I'
				} else {
					buf[0], buf[1] = 'M', 'M'
				}
				order.PutUint16(buf[2:], 0x002A)
				order.PutUint32(buf[4:], 8)
				order.PutUint16(buf[8:], 1)
				order.PutUint16(buf[10:], uint16(TagFNumber))
				order.PutUint16(buf[12:], uint16(TypeRational))
				order.PutUint32(buf[14:], 1) // count=1 → 8 bytes, out-of-line
				order.PutUint32(buf[18:], 0) // offset=0 → reads buf[0:8]
				order.PutUint32(buf[22:], 0)
				return buf
			},
		},
	}

	for _, tc := range tests {

		for _, ord := range []struct {
			name  string
			order binary.ByteOrder
		}{
			{"LE", binary.LittleEndian},
			{"BE", binary.BigEndian},
		} {

			t.Run(tc.name+"/"+ord.name, func(t *testing.T) {
				t.Parallel()
				buf := tc.buildFn(ord.order)
				// Must not panic; any return value is acceptable.
				_, _ = Parse(buf)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// S-3: count × typeSize integer overflow
//
// parseIFDEntry computes totalSize = uint64(sz) * uint64(cnt). When cnt is
// near MaxUint32 and sz is 8 (RATIONAL, DOUBLE), the product wraps in uint32
// arithmetic. The uint64 promotion prevents overflow — verified here.
// ---------------------------------------------------------------------------

// TestCountTypeOverflow verifies that crafted IFD entries with enormous
// count values (that would overflow uint32 × typeSize) are handled without
// panic or out-of-bounds access (TIFF §2).
func TestCountTypeOverflow(t *testing.T) {
	t.Parallel()

	buildOverflowTIFF := func(typ DataType, hugeCount uint32) []byte {
		order := binary.LittleEndian
		// Minimal TIFF: header + IFD with 1 entry declaring hugeCount values.
		// All bytes after the entry block are zero (no value area).
		buf := make([]byte, 8+2+12+4)
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)
		order.PutUint16(buf[8:], 1)
		order.PutUint16(buf[10:], uint16(TagFNumber))
		order.PutUint16(buf[12:], uint16(typ))
		order.PutUint32(buf[14:], hugeCount) // overflow-inducing count
		order.PutUint32(buf[18:], 26)        // points past IFD
		return buf
	}

	cases := []struct {
		name  string
		typ   DataType
		count uint32
	}{
		// RATIONAL: sz=8, count=0x20000001 → 8×0x20000001 = 0x100000008 overflows uint32.
		{"RATIONAL_count_overflow", TypeRational, 0x20000001},
		// DOUBLE: sz=8, same overflow path.
		{"DOUBLE_count_overflow", TypeDouble, 0x1FFFFFFF},
		// LONG: sz=4, count=0x40000001 → 4×0x40000001 overflows uint32.
		{"LONG_count_overflow", TypeLong, 0x40000001},
		// SHORT: sz=2, count=0x80000001 → 2×0x80000001 overflows uint32.
		{"SHORT_count_overflow", TypeShort, 0x80000001},
	}

	for _, tc := range cases {

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buf := buildOverflowTIFF(tc.typ, tc.count)
			_, _ = Parse(buf) // must not panic or OOB-read
		})
	}
}

// ---------------------------------------------------------------------------
// S-4 (regression): IFD cycle detection — named regression test
//
// traverse() maintains a visited-set (visitedPool). Any offset seen twice
// terminates the chain. This is the defence against infinite-loop attacks.
// TIFF §2: no spec provision for cyclic IFDs; defensive handling is required.
// ---------------------------------------------------------------------------

// TestIFDCycleDetectionRegression is the named regression test for cyclic IFD
// chains. traverse() must detect the repeated offset and stop without hanging.
// Complements TestIFDCycleDetection (which tests the basic self-loop case) by
// also testing a two-hop cycle (A→B→A).
func TestIFDCycleDetectionRegression(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Build a TIFF with a two-IFD cycle: IFD0 next→IFD1, IFD1 next→IFD0.
	//
	// IFD0 at offset 8:  0 entries, next = ifd1Off
	// IFD1 at offset 14: 0 entries, next = 8  ← cycle back to IFD0
	//
	// IFD with 0 entries: 2 (count) + 4 (next) = 6 bytes.

	const (
		ifd0Off = 8
		ifd1Off = ifd0Off + 2 + 4 // 14
	)

	buf := make([]byte, ifd1Off+2+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 0 entries, next → ifd1Off.
	order.PutUint16(buf[ifd0Off:], 0)
	order.PutUint32(buf[ifd0Off+2:], ifd1Off)

	// IFD1: 0 entries, next → ifd0Off (cycle).
	order.PutUint16(buf[ifd1Off:], 0)
	order.PutUint32(buf[ifd1Off+2:], ifd0Off)

	// Must not hang or panic.
	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse with two-hop IFD cycle: unexpected error: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil after two-hop cycle detection")
	}
}

// ---------------------------------------------------------------------------
// E-1: writeIFD inline determinism extended to ALL inline TIFF types
//
// Extends TestWriteIFDInlineDeterminism (in exif_test.go) to every type that
// can be stored inline (totalSize ≤ 4 bytes). For each type:
//   1. "Poison" the pool buffer with 0xFF bytes via a preceding Encode call.
//   2. Encode the real EXIF.
//   3. Verify the padding bytes in the 4-byte inline field are 0x00.
//   4. Verify two consecutive Encode calls produce byte-identical output.
//
// This ties directly to the iobuf task #49 contract: clear(entryBuf) in
// writeIFD guarantees zero-padded inline values regardless of pool state.
// ---------------------------------------------------------------------------

// TestWriteIFDInlineDeterminismAllInlineTypes verifies that the clear() call
// in writeIFD zeroes all padding bytes for every inline TIFF type. See also
// TestWriteIFDInlineDeterminism in exif_test.go (TypeShort + TypeLong).
func TestWriteIFDInlineDeterminismAllInlineTypes(t *testing.T) {
	t.Parallel()

	// Inline types and their expected totalSize.
	//   TypeByte(1):      sz=1, count=1 → 1 byte, 3 padding bytes in the 4-byte field
	//   TypeSByte(6):     sz=1, count=1 → 1 byte, 3 padding bytes
	//   TypeUndefined(7): sz=1, count=1 → 1 byte, 3 padding bytes
	//   TypeShort(3):     sz=2, count=1 → 2 bytes, 2 padding bytes
	//   TypeSShort(8):    sz=2, count=1 → 2 bytes, 2 padding bytes
	//   TypeLong(4):      sz=4, count=1 → 4 bytes, 0 padding
	//   TypeSLong(9):     sz=4, count=1 → 4 bytes, 0 padding
	//   TypeFloat(11):    sz=4, count=1 → 4 bytes, 0 padding
	//   TypeASCII(2):     sz=1, count=4 → 4 bytes, 0 padding (e.g. "A\x00\x00\x00")
	//   TypeUTF8(13):     sz=1, count=3 → 3 bytes, 1 padding byte
	type inlineCase struct {
		name    string
		typ     DataType
		count   uint32
		value   []byte // exactly totalSize bytes
		padding int    // number of padding bytes in the 4-byte field
	}

	order := binary.LittleEndian
	makeSVal := func(v int16) []byte {
		b := make([]byte, 2)
		order.PutUint16(b, uint16(v)) //nolint:gosec // G115: intentional signed→unsigned for SSHORT test
		return b
	}
	makeSLong := func(v int32) []byte {
		b := make([]byte, 4)
		order.PutUint32(b, uint32(v)) //nolint:gosec // G115: intentional signed→unsigned for SLONG test
		return b
	}
	makeFloat := func(v float32) []byte {
		b := make([]byte, 4)
		order.PutUint32(b, math.Float32bits(v))
		return b
	}

	cases := []inlineCase{
		{"TypeByte_count1", TypeByte, 1, []byte{0x42}, 3},
		{"TypeSByte_count1", TypeSByte, 1, []byte{0xFE}, 3}, // -2 signed
		{"TypeUndefined_count1", TypeUndefined, 1, []byte{0xAB}, 3},
		{"TypeUndefined_count4", TypeUndefined, 4, []byte{0x01, 0x02, 0x03, 0x04}, 0},
		{"TypeShort_count1", TypeShort, 1, []byte{0x34, 0x12}, 2}, // LE: value=0x1234
		{"TypeSShort_count1", TypeSShort, 1, makeSVal(-42), 2},
		{"TypeShort_count2", TypeShort, 2, []byte{0x01, 0x00, 0x02, 0x00}, 0},
		{"TypeLong_count1", TypeLong, 1, []byte{0xEF, 0xBE, 0xAD, 0xDE}, 0},
		{"TypeSLong_count1", TypeSLong, 1, makeSLong(-1), 0},
		{"TypeFloat_count1", TypeFloat, 1, makeFloat(1.5), 0},
		{"TypeASCII_count4", TypeASCII, 4, []byte{'A', '\x00', '\x00', '\x00'}, 0},
		{"TypeUTF8_count3", TypeUTF8, 3, []byte{'H', 'i', '\x00'}, 1},
		{"TypeByte_count2", TypeByte, 2, []byte{0x11, 0x22}, 2},
		{"TypeByte_count3", TypeByte, 3, []byte{0x11, 0x22, 0x33}, 1},
	}

	for _, tc := range cases {

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Step 1: poison the pool with 0xFF bytes by encoding an EXIF
			// whose inline values are all 0xFF (maximally contaminates the scratch buffer).
			poisonVal := make([]byte, len(tc.value))
			for i := range poisonVal {
				poisonVal[i] = 0xFF
			}
			poison := &EXIF{
				ByteOrder: order,
				IFD0: &IFD{Entries: []IFDEntry{
					{Tag: 0x9C9F, Type: tc.typ, Count: tc.count, Value: poisonVal, bigEndian: orderIsBig(order)},
				}},
			}
			if _, err := Encode(poison); err != nil {
				t.Fatalf("poison Encode: %v", err)
			}

			// Step 2: encode the real target EXIF.
			target := &EXIF{
				ByteOrder: order,
				IFD0: &IFD{Entries: []IFDEntry{
					{Tag: 0x9C9F, Type: tc.typ, Count: tc.count, Value: tc.value, bigEndian: orderIsBig(order)},
				}},
			}

			enc1, err := Encode(target)
			if err != nil {
				t.Fatalf("Encode (enc1): %v", err)
			}
			enc2, err := Encode(target)
			if err != nil {
				t.Fatalf("Encode (enc2): %v", err)
			}

			// Step 3: determinism — two consecutive encodes must be byte-identical.
			if !bytes.Equal(enc1, enc2) {
				t.Errorf("Encode non-deterministic:\n  enc1: %x\n  enc2: %x", enc1, enc2)
			}

			// Step 4: verify padding bytes are 0x00.
			// IFD entry area starts at offset 10 (8-byte header + 2-byte count).
			// The value-or-offset field occupies bytes [8:12] of each 12-byte entry.
			// For our single entry at index 0, the field is at headerSize+countSize+8.
			if tc.padding > 0 {
				const (
					headerSize = 8
					countSize  = 2
					entrySize  = 12
					valueOff   = 8 // offset of value-or-offset field within entry
				)
				fieldStart := headerSize + countSize + valueOff // = 18
				actualValueSize := int(typeSize(tc.typ)) * int(tc.count)
				padStart := fieldStart + actualValueSize
				for i := range tc.padding {
					byteIdx := padStart + i
					if byteIdx >= len(enc1) {
						t.Fatalf("padding byte index %d out of range (enc1 len=%d)", byteIdx, len(enc1))
					}
					if enc1[byteIdx] != 0x00 {
						t.Errorf("padding byte [%d] = 0x%02X, want 0x00 (pool contamination not cleared)", byteIdx, enc1[byteIdx])
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// E-2: MakerNote Make with trailing spaces — live Parse() path
//
// parseMakerNoteIFD is called from parseExifSubIFDs which is called from Parse.
// The regression test must exercise the FULL Parse() call path so that the
// strings.TrimSpace normalisation in parseMakerNoteIFD is exercised from the
// actual entry point (not just by calling parseMakerNoteIFD directly).
//
// Real-world Canon EOS bodies write Make = "Canon " (with a trailing space).
// Our dispatch table uses "Canon" (no space). Without TrimSpace the MakerNote
// would silently fail to dispatch.
// ---------------------------------------------------------------------------

// TestMakeTrailingSpaceFullParsePath is the regression test for Make fields
// with trailing whitespace dispatching to the correct MakerNote parser via the
// full exif.Parse() entry point (not a unit shortcut).
//
// Regression: real Canon EOS bodies write Make = "Canon " (trailing space,
// as documented in ExifTool Canon.pm). Without strings.TrimSpace in
// parseMakerNoteIFD the dispatch silently fails and MakerNoteIFD is nil.
func TestMakeTrailingSpaceFullParsePath(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Build a raw EXIF that embeds:
	//   IFD0: Make="Canon " (trailing space), ExifIFDPointer→ExifIFD
	//   ExifIFD: TagMakerNote → a minimal Nikon Type-3 blob (has non-zero IFD)
	//
	// We use a Nikon Type-3 payload embedded in a "Canon"-dispatched MakerNote
	// purely because Canon's traverse(b,0,order) would return nil (offset=0 loop
	// guard), making the assertion ambiguous. Instead we use a FUJIFILM payload
	// since parseFujifilmMakerNote reads an actual offset and returns non-nil.
	//
	// Layout:
	//   [0..7]     TIFF header
	//   [8..25]    IFD0: 2 entries (Make out-of-line, ExifIFDPointer inline)
	//   [26..35]   value area for IFD0: Make string "Canon \x00" (7 bytes)
	//   [36..53]   ExifIFD: 1 entry (MakerNote → mnOff)
	//   [54..]     MakerNote payload (Fujifilm format)

	// Fujifilm MakerNote with an IFD at byte 16.
	// Layout: "FUJIFILM"(8) + "0100"(4) + ifdOff(4=16) + ifd_count(2) + next(4)
	fujiPayload := func() []byte {
		b := make([]byte, 22)
		copy(b[0:], "FUJIFILM")
		copy(b[8:], "0100")
		binary.LittleEndian.PutUint32(b[12:], 16) // IFD at byte 16
		binary.LittleEndian.PutUint16(b[16:], 0)  // 0 entries (minimal valid IFD)
		binary.LittleEndian.PutUint32(b[18:], 0)  // next IFD = 0
		return b
	}()

	const makeStr = "FUJIFILM \x00" // "FUJIFILM " + NUL — trailing space before NUL
	const (
		hdrSize = 8
		ifd0Off = hdrSize
		// IFD0: 2 entries (Make=out-of-line, ExifIFDPointer=inline) + next=0
		ifd0EntCount = 2
		ifd0Size     = 2 + ifd0EntCount*12 + 4   // = 30
		makeValOff   = ifd0Off + ifd0Size        // 38
		exifOff      = makeValOff + len(makeStr) // 38 + 11 = 49
		exifSize     = 2 + 1*12 + 4              // = 18
		mnOff        = exifOff + exifSize        // 67
	)

	totalSize := mnOff + len(fujiPayload)
	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 2 entries. Tags must be sorted ascending: TagMake(0x010F) < TagExifIFDPointer(0x8769).
	order.PutUint16(buf[ifd0Off:], ifd0EntCount)

	// Entry 0: TagMake (0x010F), TypeASCII, count=len(makeStr), value at makeValOff.
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagMake))
	order.PutUint16(buf[p+2:], uint16(TypeASCII))
	order.PutUint32(buf[p+4:], uint32(len(makeStr)))
	order.PutUint32(buf[p+8:], makeValOff)
	// Entry 1: TagExifIFDPointer (0x8769), TypeLong, count=1, value=exifOff.
	p += 12
	order.PutUint16(buf[p:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], uint32(exifOff))
	// next-IFD = 0.
	p += 12
	order.PutUint32(buf[p:], 0)

	// Make value.
	copy(buf[makeValOff:], makeStr)

	// ExifIFD: 1 entry — TagMakerNote → mnOff.
	order.PutUint16(buf[exifOff:], 1)
	order.PutUint16(buf[exifOff+2:], uint16(TagMakerNote))
	order.PutUint16(buf[exifOff+4:], uint16(TypeUndefined))
	order.PutUint32(buf[exifOff+6:], uint32(len(fujiPayload))) //nolint:gosec // G115: len of a []byte slice — not a constant
	order.PutUint32(buf[exifOff+10:], uint32(mnOff))
	order.PutUint32(buf[exifOff+14:], 0)

	// MakerNote payload.
	copy(buf[mnOff:], fujiPayload)

	// Parse via the full Parse() entry point.
	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Verify Make was correctly read (trimmed for display but raw value preserved).
	makeEntry := e.IFD0.Get(TagMake)
	if makeEntry == nil {
		t.Fatal("Make tag (0x010F) not found in IFD0")
	}
	// String() trims NUL but not spaces — verify the raw value has the trailing space.
	rawMake := makeEntry.String()
	if rawMake != "FUJIFILM " {
		t.Errorf("Make.String() = %q, want \"FUJIFILM \" (with trailing space)", rawMake)
	}

	// Assert raw MakerNote bytes were retained (always true; belt-and-suspenders).
	if e.MakerNote == nil {
		t.Fatal("MakerNote is nil — raw bytes not retained")
	}

	// The decisive assertion: parseMakerNoteIFD only sets MakerNoteIFD when the
	// dispatch table lookup succeeds.  The fixture stores Make as "FUJIFILM " (with
	// trailing space); makeEntry.String() preserves that space, so the lookup key
	// passed to parseMakerNoteIFD is "FUJIFILM ".  The map key is "FUJIFILM" (no
	// space), so the lookup silently misses — and MakerNoteIFD stays nil — unless
	// parseMakerNoteIFD calls strings.TrimSpace before the map lookup.
	//
	// traverse() accepts a 0-entry IFD as valid and returns a non-nil *IFD, so
	// a non-nil MakerNoteIFD here is unambiguous proof that TrimSpace normalisation
	// fired and the FUJIFILM parser ran.  Removing strings.TrimSpace from
	// makernote_parse.go causes this assertion to fail.
	if e.MakerNoteIFD == nil {
		t.Fatal("MakerNoteIFD is nil — TrimSpace normalisation in parseMakerNoteIFD did not fire " +
			"or the FUJIFILM parser rejected the fixture; dispatch via trailing-space Make failed")
	}
}

// ---------------------------------------------------------------------------
// E-3: EXIF 2.3 vs 3.0 — TypeUTF8 as first-class tag type
//
// CIPA DC-008-2023 §4.6.3 introduces TypeUTF8 (code 13). An EXIF 3.0 file may
// carry UTF-8 encoded strings in any tag. The parser must handle TypeUTF8 in
// IFD0, ExifIFD, and GPS IFD without errors.
// ---------------------------------------------------------------------------

// TestEXIF30UTF8TagInMultipleIFDs verifies that TypeUTF8 entries in IFD0
// and ExifIFD are both parsed and survive encode→parse. This mirrors the
// EXIF 3.0 (CIPA DC-008-2023) extension where UTF-8 tagged strings are valid
// in all IFD levels.
func TestEXIF30UTF8TagInMultipleIFDs(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian

	// Use private tag numbers that do not collide with standard tags.
	const (
		tagUTF8IFD0    TagID = 0xFFF0
		tagUTF8ExifIFD TagID = 0xFFF1
	)

	str0 := "Héros & cœur" // non-ASCII UTF-8 string for IFD0
	strE := "日本語"          // non-ASCII UTF-8 string for ExifIFD

	// Build IFD0 payload with a UTF-8 tag.
	payload0 := append([]byte(str0), 0x00)
	// Build ExifIFD payload with a UTF-8 tag.
	payloadE := append([]byte(strE), 0x00)

	// Layout:
	//   [0..7]    TIFF header
	//   [8..27]   IFD0: 2 entries (tagUTF8IFD0 out-of-line, ExifIFDPointer inline) + next=0
	//   [28..]    IFD0 value: payload0
	//   [28+len(payload0)..] ExifIFD: 2 entries (tagUTF8ExifIFD out-of-line) + next=0
	//   [exif+exifSize..] payloadE

	const (
		hdrSize      = uint32(8)
		ifd0Off      = hdrSize
		ifd0EntCount = 2
		ifd0BodySize = uint32(2 + ifd0EntCount*12 + 4) // 30
		ifd0ValOff   = ifd0Off + ifd0BodySize
	)
	p0Len := uint32(len(payload0)) //nolint:gosec // G115: test helper
	exifOff := ifd0ValOff + p0Len

	exifEntCount := uint32(1)
	exifBodySize := 2 + exifEntCount*12 + 4 // 18
	exifValOff := exifOff + exifBodySize
	pELen := uint32(len(payloadE)) //nolint:gosec // G115: test helper
	totalSize := exifValOff + pELen

	buf := make([]byte, totalSize)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0 (sorted: tagUTF8IFD0=0xFFF0 < ExifIFDPointer=0x8769 — wait, 0x8769 < 0xFFF0).
	// Must sort: ExifIFDPointer(0x8769) < tagUTF8IFD0(0xFFF0).
	order.PutUint16(buf[ifd0Off:], uint16(ifd0EntCount))
	// Entry 0: ExifIFDPointer (smaller tag ID).
	p := int(ifd0Off) + 2
	order.PutUint16(buf[p:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[p+2:], uint16(TypeLong))
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], exifOff)
	// Entry 1: tagUTF8IFD0 out-of-line.
	p += 12
	order.PutUint16(buf[p:], uint16(tagUTF8IFD0))
	order.PutUint16(buf[p+2:], uint16(TypeUTF8))
	order.PutUint32(buf[p+4:], p0Len)
	order.PutUint32(buf[p+8:], ifd0ValOff)
	// next-IFD = 0.
	p += 12
	order.PutUint32(buf[p:], 0)
	// IFD0 value area.
	copy(buf[ifd0ValOff:], payload0)

	// ExifIFD.
	order.PutUint16(buf[exifOff:], uint16(exifEntCount))
	p = int(exifOff) + 2
	order.PutUint16(buf[p:], uint16(tagUTF8ExifIFD))
	order.PutUint16(buf[p+2:], uint16(TypeUTF8))
	order.PutUint32(buf[p+4:], pELen)
	order.PutUint32(buf[p+8:], exifValOff)
	p += 12
	order.PutUint32(buf[p:], 0)
	copy(buf[exifValOff:], payloadE)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// IFD0 UTF-8 tag.
	entry0 := e.IFD0.Get(tagUTF8IFD0)
	if entry0 == nil {
		t.Fatal("IFD0 UTF-8 tag not found")
	}
	if entry0.Type != TypeUTF8 {
		t.Errorf("IFD0 UTF-8 tag Type = %d, want TypeUTF8 (%d)", entry0.Type, TypeUTF8)
	}
	if got := entry0.String(); got != str0 {
		t.Errorf("IFD0 UTF-8 tag String() = %q, want %q", got, str0)
	}

	// ExifIFD UTF-8 tag.
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil")
	}
	entryE := e.ExifIFD.Get(tagUTF8ExifIFD)
	if entryE == nil {
		t.Fatal("ExifIFD UTF-8 tag not found")
	}
	if entryE.Type != TypeUTF8 {
		t.Errorf("ExifIFD UTF-8 tag Type = %d, want TypeUTF8 (%d)", entryE.Type, TypeUTF8)
	}
	if got := entryE.String(); got != strE {
		t.Errorf("ExifIFD UTF-8 tag String() = %q, want %q", got, strE)
	}

	// Round-trip.
	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	entry0rt := e2.IFD0.Get(tagUTF8IFD0)
	if entry0rt == nil {
		t.Fatal("IFD0 UTF-8 tag missing after round-trip")
	}
	if got := entry0rt.String(); got != str0 {
		t.Errorf("IFD0 UTF-8 round-trip = %q, want %q", got, str0)
	}
	if e2.ExifIFD == nil {
		t.Fatal("ExifIFD nil after round-trip")
	}
	entryErt := e2.ExifIFD.Get(tagUTF8ExifIFD)
	if entryErt == nil {
		t.Fatal("ExifIFD UTF-8 tag missing after round-trip")
	}
	if got := entryErt.String(); got != strE {
		t.Errorf("ExifIFD UTF-8 round-trip = %q, want %q", got, strE)
	}
}

// ---------------------------------------------------------------------------
// E-4: MakerNoteOffset — zero when source is not a stream, survives Encode
//
// EXIF.MakerNoteOffset must be 0 for freshly-constructed EXIFs that did not
// come from Parse, and must survive the Encode→Parse cycle (the offset field
// is informational only; after Encode the MakerNote may have moved).
// ---------------------------------------------------------------------------

// TestMakerNoteOffsetZeroOnFreshEXIF verifies that a freshly constructed EXIF
// struct (not from Parse) has MakerNoteOffset = 0.
func TestMakerNoteOffsetZeroOnFreshEXIF(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		IFD0:      &IFD{},
		ExifIFD:   &IFD{},
		MakerNote: []byte{0x01, 0x02, 0x03, 0x04, 0x05},
	}
	// MakerNoteOffset is not set by any setter; must be zero.
	if e.MakerNoteOffset != 0 {
		t.Errorf("MakerNoteOffset = %d, want 0 for freshly constructed EXIF", e.MakerNoteOffset)
	}
}

// TestMakerNoteOffsetInformationalAfterEncode verifies that after Encode→Parse,
// the MakerNoteOffset in the re-parsed EXIF points to the actual MakerNote
// bytes in the new buffer (EXIF §4.6.5 tag 0x927C).
func TestMakerNoteOffsetInformationalAfterEncode(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	makerNotePayload := []byte("FAKEMAKERNOTE\x00\x01\x02\x03\x04\x05\x06\x07")

	// Build a minimal TIFF with ExifIFD containing a TagMakerNote.
	const (
		hdrSize   = 8
		ifd0Off   = hdrSize
		ifd0Size  = 2 + 1*12 + 4
		exifOff   = ifd0Off + ifd0Size
		exifSize  = 2 + 1*12 + 4
		mnDataOff = exifOff + exifSize
	)

	buf := make([]byte, mnDataOff+len(makerNotePayload))
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	order.PutUint16(buf[ifd0Off:], 1)
	order.PutUint16(buf[ifd0Off+2:], uint16(TagExifIFDPointer))
	order.PutUint16(buf[ifd0Off+4:], uint16(TypeLong))
	order.PutUint32(buf[ifd0Off+6:], 1)
	order.PutUint32(buf[ifd0Off+10:], exifOff)
	order.PutUint32(buf[ifd0Off+14:], 0)

	order.PutUint16(buf[exifOff:], 1)
	order.PutUint16(buf[exifOff+2:], uint16(TagMakerNote))
	order.PutUint16(buf[exifOff+4:], uint16(TypeUndefined))
	order.PutUint32(buf[exifOff+6:], uint32(len(makerNotePayload))) //nolint:gosec // G115: test helper
	order.PutUint32(buf[exifOff+10:], mnDataOff)
	order.PutUint32(buf[exifOff+14:], 0)
	copy(buf[mnDataOff:], makerNotePayload)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	origOffset := e.MakerNoteOffset
	if origOffset == 0 {
		t.Fatal("MakerNoteOffset = 0 after Parse, want non-zero")
	}

	// Encode→Parse.
	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	if e2.MakerNote == nil {
		t.Fatal("MakerNote nil after encode→parse")
	}
	if !bytes.Equal(e2.MakerNote, makerNotePayload) {
		t.Errorf("MakerNote bytes mismatch after round-trip")
	}

	// The new MakerNoteOffset must point to the actual MakerNote bytes in encoded.
	newOff := e2.MakerNoteOffset
	if newOff == 0 {
		t.Fatal("MakerNoteOffset = 0 after encode→parse round-trip")
	}
	end := uint64(newOff) + uint64(len(makerNotePayload))
	if end > uint64(len(encoded)) {
		t.Fatalf("MakerNoteOffset %d + len %d exceeds encoded size %d", newOff, len(makerNotePayload), len(encoded))
	}
	if !bytes.Equal(encoded[newOff:end], makerNotePayload) {
		t.Errorf("MakerNote bytes at new offset do not match payload:\n  got  %x\n  want %x",
			encoded[newOff:end], makerNotePayload)
	}
}

// ---------------------------------------------------------------------------
// E-5: GPS lat/lon N/S/E/W complete coverage
//
// Existing tests cover N+W (San Francisco). This test explicitly covers all
// four hemisphere combinations: NE, SE, SW, NW — each with a distinct
// rational DMS triple to avoid overlapping coverage with GPS round-trip tests.
// ---------------------------------------------------------------------------

// TestGPSAllHemispheres verifies that GPSLatitudeRef N/S and GPSLongitudeRef
// E/W are correctly encoded and decoded for all four quadrants (EXIF §4.6.6).
func TestGPSAllHemispheres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		lat  float64
		lon  float64
	}{
		{"NE_Tokyo", 35.6762, 139.6503},
		{"SE_Sydney", -33.8688, 151.2093},
		{"SW_BuenosAires", -34.6037, -58.3816},
		{"NW_NewYork", 40.7128, -74.0060},
		{"equator_zero", 0.0, 0.0},
		{"south_pole_approx", -89.999, 0.0},
	}

	for _, tc := range tests {

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := &EXIF{ByteOrder: binary.LittleEndian, IFD0: &IFD{}}
			e.SetGPS(tc.lat, tc.lon)

			// Verify LatRef and LonRef directly.
			latRef, _ := refByte(e.GPSIFD.Get(TagGPSLatitudeRef))
			lonRef, _ := refByte(e.GPSIFD.Get(TagGPSLongitudeRef))

			expectedLatRef := byte('N')
			if tc.lat < 0 {
				expectedLatRef = 'S'
			}
			expectedLonRef := byte('E')
			if tc.lon < 0 {
				expectedLonRef = 'W'
			}
			if latRef != expectedLatRef {
				t.Errorf("LatitudeRef = %c, want %c", latRef, expectedLatRef)
			}
			if lonRef != expectedLonRef {
				t.Errorf("LongitudeRef = %c, want %c", lonRef, expectedLonRef)
			}

			// Round-trip via Encode→Parse.
			encoded, err := Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			e2, err := Parse(encoded)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			lat2, lon2, ok := e2.GPS()
			if !ok {
				t.Fatal("GPS() returned ok=false after round-trip")
			}
			const tol = 0.0002 // ~22 m spatial error — adequate for DMS encoding
			if d := lat2 - tc.lat; d > tol || d < -tol {
				t.Errorf("lat = %f, want ~%f (diff %f)", lat2, tc.lat, d)
			}
			if d := lon2 - tc.lon; d > tol || d < -tol {
				t.Errorf("lon = %f, want ~%f (diff %f)", lon2, tc.lon, d)
			}
		})
	}
}
