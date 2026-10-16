package exif

// DataType is a TIFF field type code (TIFF 6.0 §2, Table 1; the Exif-specific
// additions of CIPA DC-008-2026, Exif 3.1, §4.6.2, pp. 27-28).
type DataType uint16

// DataType constants identify the data type of a tag value.
const (
	TypeByte      DataType = 1  // 8-bit unsigned
	TypeASCII     DataType = 2  // NUL-terminated 7-bit ASCII string
	TypeShort     DataType = 3  // 16-bit unsigned
	TypeLong      DataType = 4  // 32-bit unsigned
	TypeRational  DataType = 5  // two LONGs: numerator / denominator
	TypeSByte     DataType = 6  // 8-bit signed (TIFF 6.0 extension)
	TypeUndefined DataType = 7  // arbitrary bytes
	TypeSShort    DataType = 8  // 16-bit signed (TIFF 6.0 extension)
	TypeSLong     DataType = 9  // 32-bit signed
	TypeSRational DataType = 10 // two SLONGs
	TypeFloat     DataType = 11 // IEEE 754 single (TIFF 6.0 extension)
	TypeDouble    DataType = 12 // IEEE 754 double (TIFF 6.0 extension)

	// TypeIFD is the IFD type: a 32-bit offset of an IFD, element size 4.
	// Adobe PageMaker 6.0 TIFF Technical Notes, TIFF Tech Note 1 (SubIFD tag
	// 0x014A, "IFD = 13"); libtiff TIFF_IFD. Exif 3.1 does not redefine code
	// 13. Some writers use it for the SubIFDs tag and for the Exif and GPS
	// pointers.
	TypeIFD DataType = 13

	// TypeUTF8 is the UTF-8 string type of Exif 3.0 and 3.1: element size 1,
	// NUL-terminated, no BOM, the count includes the NUL. CIPA DC-008-2026
	// (Exif 3.1) §4.6.2, p. 28: "129 = UTF-8 ... This is defined
	// independently by this standard, rather than in TIFF6.0". It may be
	// used instead of TypeASCII where a tag is defined as "ASCII or UTF-8":
	// ImageDescription, Make, Model, Software, Artist, Copyright (Table 6,
	// pp. 31-32) and the Exif IFD tags CameraOwnerName, LensMake, LensModel,
	// ImageTitle, Photographer, ImageEditor, CameraFirmware,
	// RAWDevelopingSoftware, ImageEditingSoftware and MetadataEditingSoftware
	// (Table 9, p. 45); DevelopmentTypeDescription (0xA40E) is UTF-8 only
	// (§4.6.6.7.48, pp. 73-74).
	//
	// Before this value was corrected, TypeUTF8 was 13, which collided with
	// the TIFF IFD type (see TypeIFD).
	TypeUTF8 DataType = 129

	// TypeLong8 is a BigTIFF-only 64-bit unsigned integer type (BigTIFF spec §3.3,
	// Aware Systems / libtiff). Type codes 16/17/18 are ONLY valid inside a BigTIFF
	// (magic 0x002B) container; classic TIFF parsers must reject them.
	TypeLong8  DataType = 16 // uint64, 8 bytes per element
	TypeSLong8 DataType = 17 // int64,  8 bytes per element
	TypeIFD8   DataType = 18 // uint64 IFD pointer, 8 bytes per element
)

// typeSizes holds the element size of the classic types, indexed by type
// code: TIFF 6.0 §2 Table 1 (1-12), TIFF Tech Note 1 (13, IFD) and Exif 3.1
// §4.6.2 p. 28 (129, UTF-8).
var typeSizes = [...]uint8{ //nolint:gochecknoglobals // read-only lookup table
	TypeByte: 1, TypeASCII: 1, TypeShort: 2, TypeLong: 4, TypeRational: 8,
	TypeSByte: 1, TypeUndefined: 1, TypeSShort: 2, TypeSLong: 4, TypeSRational: 8,
	TypeFloat: 4, TypeDouble: 8, TypeIFD: 4, TypeUTF8: 1,
}

// typeSize returns the byte size of a single value of the given type.
// Returns 0 for unknown types (including BigTIFF-only types 16/17/18, which
// are handled separately by typeSizeBigTIFF in BigTIFF traversal paths).
// A lookup table instead of a switch: the encode loops call it once per entry
// and per pass (task #311 measurement).
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
	case TypeLong, TypeSLong, TypeFloat, TypeIFD:
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
