// Package canon parses Canon MakerNote IFDs.
//
// Canon MakerNote is a plain TIFF IFD at offset 0 with no magic prefix.
// Byte order is inherited from the parent TIFF block.
// The actual IFD traversal is performed in the exif package (parseMakerNoteIFD);
// this package provides Canon-specific tag constants and field accessors.
//
// Selected Canon MakerNote tag IDs (from ExifTool Canon.pm and Canon EOS SDK docs):
//
//	0x0001  CameraSettings
//	0x0002  FocalLength
//	0x0004  ShotInfo
//	0x0006  ImageType
//	0x0007  FirmwareVersion
//	0x0008  FileNumber
//	0x0009  OwnerName
//	0x000C  SerialNumber
//	0x0010  CameraInfoLength
//	0x0013  CameraInfoD30
//	0x0019  AFInfo
//	0x001C  ModelID
//	0x001D  DeWarpingData
//	0x002F  AFInfoSize
//	0x0035  TimeInfo
//	0x0093  FileInfo
//	0x0095  LensModel
//	0x0096  InternalSerialNumber
//	0x0097  DustRemovalData
//	0x009A  AspectInfo
//	0x00A0  ProcessingInfo
//	0x00AA  MeasuredColor
//	0x00B4  ColorSpace
//	0x00D0  VRDOffset
//	0x00E0  SensorInfo
//	0x4001  ColorData
//	0x4002  CRWParam
//	0x4003  ColorInfo
//	0x4013  AFMicroAdj
//	0x4015  VignettingCorr
//	0x4016  VignettingCorr2
//	0x4018  LightingOpt
//	0x4019  LensInfo
//	0x4020  AmbienceInfo
//	0x4021  MultiExp
//	0x4024  FilterInfo
//	0x4025  HDRInfo
//	0x4028  AFConfig
package canon

// Tag IDs for Canon MakerNote IFD entries.
// These are Canon-proprietary tags (not EXIF-standard).
const (
	TagCameraSettings  uint16 = 0x0001
	TagFocalLength     uint16 = 0x0002
	TagShotInfo        uint16 = 0x0004
	TagImageType       uint16 = 0x0006
	TagFirmwareVersion uint16 = 0x0007
	TagOwnerName       uint16 = 0x0009
	TagSerialNumber    uint16 = 0x000C
	TagModelID         uint16 = 0x001C
	TagLensModel       uint16 = 0x0095
	TagColorSpace      uint16 = 0x00B4
	TagSensorInfo      uint16 = 0x00E0
	TagColorData       uint16 = 0x4001
	TagLensInfo        uint16 = 0x4019
)

// Parser implements makernote.Parser for Canon cameras.
type Parser struct{}

// Parse decodes a Canon MakerNote payload into a map of tag ID → raw value bytes.
// Canon MakerNote is a standard TIFF IFD at offset 0; this implementation performs
// a direct byte-level scan to populate the tag map without a full TIFF traversal
// (which requires knowing the parent byte order).
//
// The returned map contains raw value bytes keyed by tag ID. Use the exif package's
// IFD methods for typed access; this function provides a low-level raw-byte view.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	return parseCanonIFD(b), nil
}

// parseCanonIFD scans the IFD at offset 0, trying little-endian byte order first
// (Canon cameras are almost always LE). Returns nil if parsing fails.
func parseCanonIFD(b []byte) map[uint16][]byte {
	// Try LE first; fall back to BE.
	for _, be := range []bool{false, true} {
		result := tryParseIFD(b, be)
		if result != nil {
			return result
		}
	}
	return nil
}

func tryParseIFD(b []byte, bigEndian bool) map[uint16][]byte {
	if len(b) < 2 {
		return nil
	}

	count := int(canonRead16(b[0:], bigEndian))
	if count <= 0 || count > 512 || 2+count*12 > len(b) {
		return nil
	}

	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		tag, value, ok := parseCanonIFDEntry(b, 2+i*12, bigEndian)
		if !ok {
			continue
		}
		result[tag] = value
	}

	// Require at least a few entries to consider this a valid parse.
	if len(result) < 2 {
		return nil
	}
	return result
}

// parseCanonIFDEntry decodes a single 12-byte IFD entry at pos within b.
// Returns (tag, value slice, true) on success, or (0, nil, false) on any
// invalid or out-of-bounds data. The value slice aliases b directly (no copy).
func parseCanonIFDEntry(b []byte, pos int, bigEndian bool) (tag uint16, value []byte, ok bool) {
	if pos < 0 || pos+12 > len(b) {
		return 0, nil, false
	}
	tag = canonRead16(b[pos:], bigEndian)
	typ := canonRead16(b[pos+2:], bigEndian)
	cnt := canonRead32(b[pos+4:], bigEndian)

	sz := typeSize16(typ)
	if sz == 0 {
		return 0, nil, false
	}
	total := uint64(sz) * uint64(cnt)
	if total <= 4 {
		return tag, b[pos+8 : pos+8+int(total)], true
	}
	off := uint64(canonRead32(b[pos+8:], bigEndian))
	end := off + total
	if end > uint64(len(b)) {
		return 0, nil, false
	}
	return tag, b[off:end], true
}

func canonRead16(p []byte, bigEndian bool) uint16 {
	if bigEndian {
		return uint16(p[0])<<8 | uint16(p[1])
	}
	return uint16(p[1])<<8 | uint16(p[0])
}

func canonRead32(p []byte, bigEndian bool) uint32 {
	if bigEndian {
		return uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
	}
	return uint32(p[3])<<24 | uint32(p[2])<<16 | uint32(p[1])<<8 | uint32(p[0])
}

// typeSize16 returns the byte size of a TIFF data type by numeric code.
func typeSize16(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 0
}
