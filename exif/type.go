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

	// TypeLong8 is a BigTIFF-only 64-bit unsigned integer type (BigTIFF spec §3.3,
	// Aware Systems / libtiff). Type codes 16/17/18 are ONLY valid inside a BigTIFF
	// (magic 0x002B) container; classic TIFF parsers must reject them.
	TypeLong8  DataType = 16 // uint64, 8 bytes per element
	TypeSLong8 DataType = 17 // int64,  8 bytes per element
	TypeIFD8   DataType = 18 // uint64 IFD pointer, 8 bytes per element
)

// typeSizes holds the element size of the classic TIFF types 0-13, indexed
// by type code (TIFF 6.0 §2 Table; CIPA DC-008-2023 §4.6.3 for 13).
var typeSizes = [...]uint8{ //nolint:gochecknoglobals // read-only lookup table
	TypeByte: 1, TypeASCII: 1, TypeShort: 2, TypeLong: 4, TypeRational: 8,
	TypeSByte: 1, TypeUndefined: 1, TypeSShort: 2, TypeSLong: 4, TypeSRational: 8,
	TypeFloat: 4, TypeDouble: 8, TypeUTF8: 1,
}

// typeSize returns the byte size of a single value of the given type.
// Returns 0 for unknown types (including BigTIFF-only types 16/17/18, which
// are handled separately by typeSizeBigTIFF in BigTIFF traversal paths).
// CIPA DC-008-2023 §4.6.3: TypeUTF8 (13) has element size 1, same as
// TypeASCII. A lookup table instead of a switch: the encode loops call it
// once per entry and per pass (task #311 measurement).
func typeSize(t DataType) uint32 {
	if int(t) < len(typeSizes) {
		return uint32(typeSizes[t])
	}
	return 0
}

// typeSizeBigTIFF returns the byte size of a single value for BigTIFF contexts.
// It extends typeSize with the three BigTIFF-only 64-bit types (BigTIFF spec §3.3):
//
//	TypeLong8  (16) = uint64, 8 bytes
//	TypeSLong8 (17) = int64,  8 bytes
//	TypeIFD8   (18) = uint64 IFD offset, 8 bytes
//
// Returns 0 for unknown type codes so callers can skip unrecognised entries gracefully.
func typeSizeBigTIFF(t DataType) uint64 {
	switch t {
	case TypeByte, TypeASCII, TypeSByte, TypeUndefined, TypeUTF8:
		return 1
	case TypeShort, TypeSShort:
		return 2
	case TypeLong, TypeSLong, TypeFloat:
		return 4
	case TypeRational, TypeSRational, TypeDouble:
		return 8
	case TypeLong8, TypeSLong8, TypeIFD8:
		// BigTIFF spec §3.3: LONG8, SLONG8, IFD8 are each exactly 8 bytes.
		return 8
	}
	return 0
}

// isIFDPointerTag reports whether tag holds offsets of IFDs: SubIFDs (0x014A,
// Adobe PageMaker 6.0 TIFF Technical Notes, TIFF Tech Note 1) and the Exif,
// GPS and Interoperability IFD pointers (0x8769, 0x8825, 0xA005; EXIF
// §4.6.3).
func isIFDPointerTag(tag TagID) bool {
	switch tag {
	case TagSubIFDs, TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer:
		return true
	}
	return false
}

// entryTypeSize is typeSize for an entry of tag: on an IFD-pointer tag
// (isIFDPointerTag) the type code 13 is the TIFF IFD type, a 32-bit offset
// (TIFF Tech Note 1; libtiff TIFF_IFD; ExifTool format "ifd"), not TypeUTF8.
// libtiff writes SubIFDs with that type and some cameras write the Exif and
// GPS pointers with it (corpus "Issue 151.tif"); sized as a 1-byte string the
// pointer was truncated and the IFDs it addresses were lost (task #311). The
// type code itself is kept, so the entry is written back with type 13.
func entryTypeSize(tag TagID, t DataType) uint32 {
	if t == TypeUTF8 && isIFDPointerTag(tag) {
		return 4
	}
	return typeSize(t)
}

// entryTypeSizeBigTIFF is entryTypeSize for BigTIFF contexts (typeSizeBigTIFF).
func entryTypeSizeBigTIFF(tag TagID, t DataType) uint64 {
	if t == TypeUTF8 && isIFDPointerTag(tag) {
		return 4
	}
	return typeSizeBigTIFF(t)
}
