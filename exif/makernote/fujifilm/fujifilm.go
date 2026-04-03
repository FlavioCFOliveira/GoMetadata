// Package fujifilm parses Fujifilm MakerNote IFDs.
//
// Fujifilm MakerNote format (ExifTool Fujifilm.pm §3.1):
//
//	[0..7]   "FUJIFILM" magic (8 bytes)
//	[8..11]  version bytes (e.g. "0100", ignored during parsing)
//	[12..15] LE uint32 IFD offset relative to start of MakerNote (b[0])
//
// The IFD itself uses little-endian byte order. All value offsets inside
// IFD entries are also relative to b[0] (the start of the MakerNote payload).
//
// Selected Fujifilm MakerNote tag IDs (ExifTool Fujifilm.pm):
//
//	0x0000  Version
//	0x0010  InternalSerialNumber
//	0x0011  Quality
//	0x1000  SharpnessInt
//	0x1001  WhiteBalance
//	0x1002  Saturation
//	0x1003  Sharpness
//	0x1004  FujiFlashMode
//	0x1005  FlashExposureComp
//	0x1006  Macro
//	0x1007  FocusMode
//	0x100A  AutoBracketing
//	0x100B  SequenceNumber
//	0x100E  BlurWarning
//	0x100F  FocusWarning
//	0x1010  AutoExposureWarning
//	0x1020  DynamicRange
//	0x1021  FilmMode
//	0x1022  DynamicRangeSetting
//	0x1023  DevelopmentDynamicRange
//	0x1024  MinFocalLength
//	0x1025  MaxFocalLength
//	0x1026  MaxApertureAtMinFocal
//	0x1027  MaxApertureAtMaxFocal
//	0x1028  FujiModel
//	0x1029  FujiRating
//	0x102D  ImageStabilization
//	0x102E  AccelerometerZ
//	0x102F  AccelerometerX
//	0x1030  AccelerometerY
//	0x1031  GyroYaw
//	0x1032  GyroPitch
//	0x1033  GyroRoll
//	0x1100  AutoBracketingCount
//	0x1101  AutoBracketingSettings
//	0x1210  ColorChromeFXBlue
//	0x1400  FaceInfo
//	0x4100  FaceElementTypes
//	0x4200  NumFaceElements
//	0x8000  FileSource
//	0x8002  OrderNumber
//	0x8003  FrameNumber
//	0xB211  Parallax
package fujifilm

import (
	"encoding/binary"
	"errors"
)

// Tag IDs for Fujifilm MakerNote IFD entries.
// These are Fujifilm-proprietary tags (not EXIF-standard).
const (
	TagVersion                  uint16 = 0x0000
	TagInternalSerialNumber     uint16 = 0x0010
	TagQuality                  uint16 = 0x0011
	TagWhiteBalance             uint16 = 0x1001
	TagSaturation               uint16 = 0x1002
	TagSharpness                uint16 = 0x1003
	TagFujiFlashMode            uint16 = 0x1004
	TagFlashExposureComp        uint16 = 0x1005
	TagMacro                    uint16 = 0x1006
	TagFocusMode                uint16 = 0x1007
	TagBlurWarning              uint16 = 0x100E
	TagFocusWarning             uint16 = 0x100F
	TagAutoExposureWarning      uint16 = 0x1010
	TagDynamicRange             uint16 = 0x1020
	TagFilmMode                 uint16 = 0x1021
	TagDynamicRangeSetting      uint16 = 0x1022
	TagImageStabilization       uint16 = 0x102D
	TagAutoBracketingCount      uint16 = 0x1100
	TagFileSource               uint16 = 0x8000
	TagFrameNumber              uint16 = 0x8003
)

// magic is the required 8-byte prefix for all Fujifilm MakerNotes.
const magic = "FUJIFILM"

// minLength is the minimum valid MakerNote length:
// 8 (magic) + 4 (version) + 4 (IFD offset) = 16 bytes.
const minLength = 16

// Parser implements makernote.Parser for Fujifilm cameras.
type Parser struct{}

// Parse decodes a Fujifilm MakerNote payload into a map of tag ID → raw value bytes.
//
// The MakerNote must start with "FUJIFILM" (8 bytes), followed by a 4-byte
// version field, and a 4-byte little-endian IFD offset relative to b[0].
// Returns an error if the magic prefix is absent or the payload is too short.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < minLength {
		return nil, errors.New("fujifilm: makernote too short")
	}
	if string(b[:8]) != magic {
		return nil, errors.New("fujifilm: invalid magic")
	}
	// IFD offset at [12..15] is LE uint32 relative to b[0].
	ifdOffset := binary.LittleEndian.Uint32(b[12:16])
	result := parseIFDAt(b, int(ifdOffset), false /* little-endian */)
	if result == nil {
		// Offset out-of-bounds or malformed IFD — return empty map, not error,
		// so the caller can still use the metadata that was decoded before this point.
		return map[uint16][]byte{}, nil
	}
	return result, nil
}

// parseIFDAt walks a TIFF IFD starting at the given offset within b.
// bigEndian controls byte order for all multi-byte reads.
// All value offsets in IFD entries are relative to b[0].
// Returns nil if the IFD is malformed or the offset is out-of-bounds.
func parseIFDAt(b []byte, offset int, bigEndian bool) map[uint16][]byte {
	if offset < 0 || offset+2 > len(b) {
		return nil
	}
	count := int(readU16(b[offset:], bigEndian))
	if count <= 0 || count > 512 {
		return nil
	}
	end := offset + 2 + count*12
	if end > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ {
		pos := offset + 2 + i*12
		tag := readU16(b[pos:], bigEndian)
		typ := readU16(b[pos+2:], bigEndian)
		cnt := readU32(b[pos+4:], bigEndian)

		sz := typeSize16(typ)
		if sz == 0 {
			// Unknown type: skip entry, do not abort the whole IFD.
			continue
		}
		total := uint64(sz) * uint64(cnt)

		var value []byte
		if total <= 4 {
			// Value fits inline in the 4-byte value/offset field.
			value = b[pos+8 : pos+8+int(total)]
		} else {
			// Value is at the offset stored in the value/offset field.
			off := int(readU32(b[pos+8:], bigEndian))
			valEnd := off + int(total)
			if off < 0 || valEnd > len(b) {
				continue
			}
			value = b[off:valEnd]
		}
		result[tag] = value
	}
	return result
}

func readU16(b []byte, bigEndian bool) uint16 {
	if bigEndian {
		return binary.BigEndian.Uint16(b)
	}
	return binary.LittleEndian.Uint16(b)
}

func readU32(b []byte, bigEndian bool) uint32 {
	if bigEndian {
		return binary.BigEndian.Uint32(b)
	}
	return binary.LittleEndian.Uint32(b)
}

// typeSize16 returns the byte size for a TIFF data type code (TIFF 6.0 §2).
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
