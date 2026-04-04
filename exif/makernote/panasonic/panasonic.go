// Package panasonic parses Panasonic/Lumix MakerNote IFDs.
//
// Panasonic MakerNote layout (ExifTool Panasonic.pm):
//
//	[0..11]  "Panasonic\x00\x00\x00"  12-byte magic prefix
//	[12..]   little-endian TIFF IFD; offsets relative to b[0]
//
// Selected Panasonic MakerNote tag IDs:
//
//	0x0001  ImageQuality
//	0x0002  FirmwareVersion
//	0x0003  WhiteBalance
//	0x0007  FocusMode
//	0x000F  AFAreaMode
//	0x001A  ImageStabilization
//	0x001C  MacroMode
//	0x001F  ShootingMode
//	0x0020  Audio
//	0x0022  DataDump
//	0x0023  EasyMode
//	0x0025  WhiteBalanceBias
//	0x0026  FlashBias
//	0x002B  OpticalZoomMode
//	0x002C  ConversionLens
//	0x0034  ColorEffect
//	0x0035  TimeSincePowerOn
//	0x0036  BurstMode
//	0x003C  Contrast
//	0x003D  NoiseReduction
//	0x003F  FlashCurtain
//	0x0040  LongExposureNR
//	0x004B  PanasonicExifVersion
//	0x004C  ColorSpace
//	0x004E  Compression
//	0x0051  LensType
//	0x0052  LensSerialNumber
//	0x0053  AccessoryType
//	0x0059  IntelligentExposure
//	0x005D  BurstSheed
//	0x0060  IntelligentD-Range
//	0x0070  IntelligentResolution
//	0x0079  Hue
//	0x007C  Filter
//	0x007D  VideoFrameRate
package panasonic

import "encoding/binary"

// Tag IDs for Panasonic MakerNote IFD entries.
const (
	TagImageQuality         uint16 = 0x0001
	TagFirmwareVersion      uint16 = 0x0002
	TagWhiteBalance         uint16 = 0x0003
	TagFocusMode            uint16 = 0x0007
	TagAFAreaMode           uint16 = 0x000F
	TagImageStabilization   uint16 = 0x001A
	TagMacroMode            uint16 = 0x001C
	TagShootingMode         uint16 = 0x001F
	TagWhiteBalanceBias     uint16 = 0x0025
	TagFlashBias            uint16 = 0x0026
	TagColorEffect          uint16 = 0x0034
	TagContrast             uint16 = 0x003C
	TagNoiseReduction       uint16 = 0x003D
	TagColorSpace           uint16 = 0x004C
	TagLensType             uint16 = 0x0051
	TagLensSerialNumber     uint16 = 0x0052
	TagIntelligentExposure  uint16 = 0x0059
	TagIntelligentResolution uint16 = 0x0070
)

// magic is the 12-byte Panasonic MakerNote prefix.
const magic = "Panasonic\x00\x00\x00"

// Parser implements makernote.Parser for Panasonic cameras.
type Parser struct{}

// Parse decodes a Panasonic MakerNote payload into a map of tag ID → raw value bytes.
// The payload must begin with the 12-byte "Panasonic\x00\x00\x00" magic prefix
// followed by a little-endian TIFF IFD.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < len(magic)+2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	if string(b[:len(magic)]) != magic {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	return parseLE(b), nil
}

// parseLE scans the IFD at offset 12 using little-endian byte order.
func parseLE(b []byte) map[uint16][]byte {
	const ifdOffset = 12
	if len(b) < ifdOffset+2 {
		return nil
	}
	count := int(binary.LittleEndian.Uint16(b[ifdOffset:]))
	if count <= 0 || count > 512 || ifdOffset+2+count*12 > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ {
		pos := ifdOffset + 2 + i*12
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
