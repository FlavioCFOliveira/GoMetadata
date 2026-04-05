// Package sony parses Sony MakerNote IFDs.
//
// Sony Alpha (ILCE/ILCA/SLT) and Cybershot cameras use a plain TIFF IFD at
// offset 0 with no magic prefix. Byte order is inherited from the parent TIFF
// (almost always little-endian for Sony cameras). (ExifTool Sony.pm)
//
// Selected Sony MakerNote tag IDs:
//
//	0x0102  Quality
//	0x0104  FlashExposureComp
//	0x0105  Teleconverter
//	0x0112  WhiteBalanceFineTune
//	0x0114  CameraSettings
//	0x0115  WhiteBalance
//	0x011B  Shot
//	0x011C  FaceInfo
//	0x0200  JPEGQuality
//	0x0201  FocalLength
//	0x0204  WhiteBalance2
//	0x0210  ImageHeight
//	0x0211  ImageWidth
//	0x0212  FaceInfo2
//	0x02CA  Exposure
//	0x02CB  SonyISO
//	0x02CC  ISOAutoMin
//	0x02CD  LensID
//	0x02CE  MinoltaLens
//	0x02CF  FullImageSize
//	0x02D0  PreviewImageSize
//	0x7000  PrintIM
//	0x7001  SonyModelID
//	0x7003  ColorTemperatureSetting
//	0x7004  SceneMode
//	0x7006  ZoneMatching
//	0x7007  DynamicRangeOptimizer
//	0x7008  ColorMode
//	0x7009  JPEGQuality2
//	0x700A  ImageStyle
//	0x700B  CreativeStyle
//	0x900B  FocusMode2
//	0x900C  AFPointSelected
//	0x900D  ShotInfo
//	0x900E  FileFormat
//	0x900F  SonyRawFileType
//	0x9050  Tag9050
//	0x9400  Tag9400a / Tag9400b
//	0x9401  Tag9401
//	0x9402  Tag9402
//	0x9403  Tag9403
//	0x9404  Tag9404b
//	0x9405  Tag9405
//	0x940A  Tag940A
//	0x940B  Tag940B
//	0x940C  Tag940C
//	0xB000  FileFormat
//	0xB001  SonyModelID2
//	0xB020  ColorReproduction
//	0xB021  ColorTemperature
//	0xB022  ColorCompensationFilter
//	0xB023  SceneMode
//	0xB024  ZoneMatching
//	0xB025  DynamicRangeOptimizer
//	0xB026  ImageStabilization
//	0xB027  LensType
//	0xB028  MinoltaMakerNote
//	0xB029  ColorMode
//	0xB02B  FullImageSize
//	0xB02C  PreviewImageSize
//	0xB040  ColorMode
//	0xB041  ImageStyle
//	0xB047  Anti-Blur
//	0xB04E  LongExposureNoiseReduction
//	0xB04F  DynamicRangeOptimizer
//	0xB052  IntelligentAuto
//	0xB054  WhiteBalance2
package sony

// Tag IDs for Sony MakerNote IFD entries.
const (
	TagQuality            uint16 = 0x0102
	TagFlashExposureComp  uint16 = 0x0104
	TagCameraSettings     uint16 = 0x0114
	TagWhiteBalance       uint16 = 0x0115
	TagFocalLength        uint16 = 0x0201
	TagSonyModelID        uint16 = 0x7001
	TagSceneMode          uint16 = 0xB023
	TagLensType           uint16 = 0xB027
	TagColorMode          uint16 = 0xB029
	TagFullImageSize      uint16 = 0xB02B
	TagImageStabilization uint16 = 0xB026
	TagDynamicRangeOpt    uint16 = 0xB025
	TagAntiBlur           uint16 = 0xB047
	TagLongExpNR          uint16 = 0xB04E
)

// Parser implements makernote.Parser for Sony cameras.
type Parser struct{}

// Parse decodes a Sony MakerNote payload into a map of tag ID → raw value bytes.
// Sony MakerNote is a standard TIFF IFD at offset 0; byte order is detected by
// trying little-endian first (Sony cameras are almost always LE).
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	return parseSonyIFD(b), nil
}

// parseSonyIFD scans the IFD at offset 0, trying LE then BE.
func parseSonyIFD(b []byte) map[uint16][]byte {
	for _, be := range []bool{false, true} {
		if result := parseRawIFD(b, be); result != nil {
			return result
		}
	}
	return nil
}

func parseRawIFD(b []byte, bigEndian bool) map[uint16][]byte {
	if len(b) < 2 {
		return nil
	}
	count := int(readU16(b[0:], bigEndian))
	if count <= 0 || count > 1024 || 2+count*12 > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		tag, value, ok := parseSonyIFDEntry(b, 2+i*12, bigEndian)
		if !ok {
			continue
		}
		result[tag] = value
	}
	if len(result) < 2 {
		return nil
	}
	return result
}

// parseSonyIFDEntry decodes a single 12-byte IFD entry at pos within b.
// Returns (tag, value slice, true) on success, or (0, nil, false) on any
// invalid or out-of-bounds data. The value slice aliases b directly (no copy).
func parseSonyIFDEntry(b []byte, pos int, bigEndian bool) (tag uint16, value []byte, ok bool) {
	if pos < 0 || pos+12 > len(b) {
		return 0, nil, false
	}
	tag = readU16(b[pos:], bigEndian)
	typ := readU16(b[pos+2:], bigEndian)
	cnt := readU32(b[pos+4:], bigEndian)

	sz := typeSize16(typ)
	if sz == 0 {
		return 0, nil, false
	}
	total := uint64(sz) * uint64(cnt)
	if total <= 4 {
		return tag, b[pos+8 : pos+8+int(total)], true
	}
	off := int(readU32(b[pos+8:], bigEndian))
	end := off + int(total) //nolint:gosec // G115: total is a uint64 value offset bounded by file size
	if off < 0 || end > len(b) {
		return 0, nil, false
	}
	return tag, b[off:end], true
}

func readU16(b []byte, bigEndian bool) uint16 {
	if bigEndian {
		return uint16(b[0])<<8 | uint16(b[1])
	}
	return uint16(b[1])<<8 | uint16(b[0])
}

func readU32(b []byte, bigEndian bool) uint32 {
	if bigEndian {
		return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	}
	return uint32(b[3])<<24 | uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0])
}

// typeSize16 returns the byte size for a TIFF data type code.
func typeSize16(t uint16) uint32 {
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
