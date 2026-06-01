package exif

// DataType is a TIFF field type code (TIFF §2, Table 1).
type DataType uint16

// DataType constants identify the EXIF data type of a tag value (EXIF 2.3 §4.6.3).
const (
	TypeByte      DataType = 1  // 8-bit unsigned
	TypeASCII     DataType = 2  // NUL-terminated string
	TypeShort     DataType = 3  // 16-bit unsigned
	TypeLong      DataType = 4  // 32-bit unsigned
	TypeRational  DataType = 5  // two LONGs: numerator / denominator
	TypeSByte     DataType = 6  // 8-bit signed (EXIF extension)
	TypeUndefined DataType = 7  // arbitrary bytes (EXIF extension)
	TypeSShort    DataType = 8  // 16-bit signed (EXIF extension)
	TypeSLong     DataType = 9  // 32-bit signed (EXIF extension)
	TypeSRational DataType = 10 // two SLONGs (EXIF extension)
	TypeFloat     DataType = 11 // IEEE 754 single (EXIF extension)
	TypeDouble    DataType = 12 // IEEE 754 double (EXIF extension)
	// TypeUTF8 is the UTF-8 string type introduced in EXIF 3.0.
	// CIPA DC-008-2023 §4.6.3: type code 13, element size 1 byte, NUL-terminated
	// UTF-8 string. Semantically analogous to TypeASCII but the character set is
	// Unicode UTF-8 rather than US-ASCII.
	TypeUTF8 DataType = 13
)

// typeSize returns the byte size of a single value of the given type.
// Returns 0 for unknown types.
func typeSize(t DataType) uint32 {
	switch t {
	case TypeByte, TypeASCII, TypeSByte, TypeUndefined, TypeUTF8:
		// CIPA DC-008-2023 §4.6.3: TypeUTF8 (13) has element size 1, same as TypeASCII.
		return 1
	case TypeShort, TypeSShort:
		return 2
	case TypeLong, TypeSLong, TypeFloat:
		return 4
	case TypeRational, TypeSRational, TypeDouble:
		return 8
	}
	return 0
}
