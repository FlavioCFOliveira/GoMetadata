// Package sigma parses Sigma MakerNote IFDs.
//
// Sigma MakerNote layout (ExifTool Sigma.pm):
//
//	[0..7]   "SIGMA\x00\x00\x00" or "FOVEON\x00\x00" magic (8 bytes)
//	[8..9]   version (2 bytes, ignored)
//	[10..]   little-endian TIFF IFD; offsets relative to b[0]
//
// Selected Sigma MakerNote tag IDs:
//
//	0x0002  SerialNumber
//	0x0003  DriveMode
//	0x0004  ResolutionMode
//	0x0005  AutofocusMode
//	0x0006  FocusSetting
//	0x0007  WhiteBalance
//	0x0008  ExposureMode
//	0x0009  MeteringMode
//	0x000A  LensFocalRange
//	0x000B  ColorSpace
//	0x000C  Exposure
//	0x000D  Contrast
//	0x000E  Shadow
//	0x000F  Highlight
//	0x0010  Saturation
//	0x0011  Sharpness
//	0x0012  X3FillLight
//	0x0014  ColorAdjustment
//	0x0015  AdjustmentMode
//	0x0016  Quality
//	0x0017  Firmware
//	0x0018  Software
//	0x0019  AutoBracket
//	0x001A  PreviewImageStart
package sigma

import "encoding/binary"

// Tag IDs for Sigma MakerNote IFD entries.
const (
	TagSerialNumber   uint16 = 0x0002
	TagDriveMode      uint16 = 0x0003
	TagWhiteBalance   uint16 = 0x0007
	TagExposureMode   uint16 = 0x0008
	TagMeteringMode   uint16 = 0x0009
	TagLensFocalRange uint16 = 0x000A
	TagColorSpace     uint16 = 0x000B
	TagContrast       uint16 = 0x000D
	TagSaturation     uint16 = 0x0010
	TagSharpness      uint16 = 0x0011
	TagQuality        uint16 = 0x0016
	TagFirmware       uint16 = 0x0017
	TagSoftware       uint16 = 0x0018
)

// Parser implements makernote.Parser for Sigma cameras.
type Parser struct{}

// Parse decodes a Sigma MakerNote payload into a map of tag ID → raw value bytes.
// Handles both "SIGMA\x00\x00\x00" and "FOVEON\x00\x00" prefixes.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 10 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	// Validate known prefixes.
	switch {
	case len(b) >= 8 && string(b[:8]) == "SIGMA\x00\x00\x00":
	case len(b) >= 8 && string(b[:8]) == "FOVEON\x00\x00":
	default:
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	// IFD starts at offset 10, little-endian.
	return parseAt(b, 10), nil
}

func parseAt(b []byte, offset int) map[uint16][]byte {
	if len(b) < offset+2 {
		return nil
	}
	count := int(binary.LittleEndian.Uint16(b[offset:]))
	if count <= 0 || count > 512 || offset+2+count*12 > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ {
		pos := offset + 2 + i*12
		tag := binary.LittleEndian.Uint16(b[pos:])
		typ := binary.LittleEndian.Uint16(b[pos+2:])
		cnt := binary.LittleEndian.Uint32(b[pos+4:])
		sz := typeSize(typ)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(cnt)
		var value []byte
		if total <= 4 {
			value = b[pos+8 : pos+8+int(total)]
		} else {
			off := binary.LittleEndian.Uint32(b[pos+8:])
			end := uint64(off) + total
			if end > uint64(len(b)) {
				continue
			}
			value = b[off:end]
		}
		result[tag] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11:
		return 4
	case 5, 10, 12:
		return 8
	}
	return 0
}
