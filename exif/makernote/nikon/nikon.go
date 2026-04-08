// Package nikon parses Nikon MakerNote IFDs.
//
// Nikon uses two distinct MakerNote formats (ExifTool Nikon.pm):
//
//   - Type 1 (legacy D1, early Coolpix): plain IFD at offset 0, big-endian.
//
//   - Type 3 (all modern Nikon DSLRs / Coolpix / Z-series): embedded TIFF header
//     prefixed by "Nikon\0" + 2-byte version. Layout:
//     [0..5]   "Nikon\0"   magic
//     [6..7]   version     (e.g. 0x02 0x10)
//     [8..9]   byte order  "II" or "MM"
//     [10..11] TIFF magic  0x002A
//     [12..15] IFD offset  (relative to byte 8)
//
// Selected Nikon MakerNote tag IDs:
//
//	0x0001  MakerNoteVersion
//	0x0002  ISO
//	0x0003  ColorMode
//	0x0004  Quality
//	0x0005  WhiteBalance
//	0x0006  Sharpness
//	0x0007  FocusMode
//	0x0008  FlashSetting
//	0x0009  FlashType
//	0x000B  WhiteBalanceFineTune
//	0x000C  WB_RBLevels
//	0x000D  ProgramShift
//	0x000E  ExposureDifference
//	0x000F  ISOSelection
//	0x0011  ThumbnailOffset
//	0x0012  ThumbnailLength
//	0x0013  FlashExposureComp
//	0x0016  FlashBracketComp
//	0x0017  ExposureBracketComp
//	0x0018  ImageStabilization
//	0x0019  ShootingMode
//	0x001A  AutoBracketRelease
//	0x001B  LensStopAdjust
//	0x001C  AutoBracketModeD
//	0x001D  AutoBracketOrder
//	0x001E  AutoBracketSet
//	0x001F  NikonImageSize
//	0x0022  AutofocusMode
//	0x0023  AFAssistIllum
//	0x0024  ImageStabilizationOld
//	0x0025  ShotInfo
//	0x0080  ImageAdjustment
//	0x0081  ToneComp
//	0x0082  AuxiliaryLens
//	0x0083  LensType
//	0x0084  Lens
//	0x0085  FocusDistance
//	0x0086  DigitalZoom
//	0x0087  FlashMode
//	0x0088  AFInfo
//	0x0089  ShootingMode2
//	0x008A  AutoBracketCount
//	0x008B  LensFStops
//	0x008C  ContrastCurve
//	0x008D  ColorHue
//	0x008F  SceneMode
//	0x0090  LightSource
//	0x0091  ShotInfoD80
//	0x0092  HueAdjustment
//	0x0093  NEFCompression
//	0x0094  Saturation
//	0x0095  NoiseReduction
//	0x0096  LinearizationTable
//	0x0097  ColorBalance
//	0x0098  LensData
//	0x0099  RawImageCenter
//	0x009A  SensorPixelSize
//	0x009C  Scene Assist
//	0x009E  RetouchHistory
//	0x00A0  SerialNumber
//	0x00A2  ImageDataSize
//	0x00A5  ImageCount
//	0x00A6  DeletedImageCount
//	0x00A7  ShutterCount
//	0x00A9  ImageOptimization
//	0x00AA  Saturation2
//	0x00AB  VariProgram
//	0x00AC  ImageStabilization2
//	0x00AD  AFResponse
//	0x00B0  MultiExposure
//	0x00B1  HighISONoiseReduction
//	0x00B3  ToningEffect
//	0x00B7  AFInfo2
//	0x00B8  FileInfo
//	0x00B9  AFTune
//	0x0E00  PrintIM
//	0x0E01  CaptureData
//	0x0E09  CaptureVersion
//	0x0E0E  CaptureOffsets
//	0x0E10  ScanIFD
//	0x0E13  ICCProfile
//	0x0E1D  CaptureOutput
package nikon

import "encoding/binary"

// Tag IDs for Nikon MakerNote IFD entries.
const (
	TagMakerNoteVersion uint16 = 0x0001
	TagISO              uint16 = 0x0002
	TagColorMode        uint16 = 0x0003
	TagQuality          uint16 = 0x0004
	TagWhiteBalance     uint16 = 0x0005
	TagSharpness        uint16 = 0x0006
	TagFocusMode        uint16 = 0x0007
	TagFlashMode        uint16 = 0x0087
	TagAFInfo           uint16 = 0x0088
	TagLensData         uint16 = 0x0098
	TagSerialNumber     uint16 = 0x00A0
	TagShutterCount     uint16 = 0x00A7
	TagAFInfo2          uint16 = 0x00B7
)

// Parser implements makernote.Parser for Nikon cameras.
type Parser struct{}

// Parse decodes a Nikon MakerNote payload into a map of tag ID → raw value bytes.
// Both Type 1 (legacy big-endian IFD at offset 0) and Type 3 (embedded TIFF with
// "Nikon\0" prefix) are supported.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	return parseNikonIFD(b), nil
}

// parseNikonIFD detects the Nikon MakerNote type and delegates accordingly.
func parseNikonIFD(b []byte) map[uint16][]byte {
	// Type 3: "Nikon\0" prefix followed by an embedded TIFF header at offset 8.
	if len(b) >= 18 &&
		b[0] == 'N' && b[1] == 'i' && b[2] == 'k' &&
		b[3] == 'o' && b[4] == 'n' && b[5] == 0x00 {
		return parseType3(b[8:])
	}

	// Type 1: plain IFD at offset 0, big-endian.
	return parseRawIFD(b, true)
}

// parseType3 parses a Nikon Type 3 embedded TIFF header.
// tiff is b[8:] — the embedded TIFF starts at offset 8 within the MakerNote.
func parseType3(tiff []byte) map[uint16][]byte {
	if len(tiff) < 8 {
		return nil
	}
	var bigEndian bool
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bigEndian = false
	case tiff[0] == 'M' && tiff[1] == 'M':
		bigEndian = true
	default:
		return nil
	}

	magic := readU16(tiff[2:], bigEndian)
	if magic != 0x002A {
		return nil
	}

	ifdOff := readU32(tiff[4:], bigEndian) //nolint:gosec // G602: len(tiff) >= 8 guaranteed by guard above
	return parseIFDAt(tiff, int(ifdOff), bigEndian)
}

// parseRawIFD parses an IFD starting at offset 0 with the given byte order.
func parseRawIFD(b []byte, bigEndian bool) map[uint16][]byte {
	return parseIFDAt(b, 0, bigEndian)
}

func parseIFDAt(b []byte, offset int, bigEndian bool) map[uint16][]byte {
	if offset < 0 || offset+2 > len(b) {
		return nil
	}
	count := int(readU16(b[offset:], bigEndian))
	if count <= 0 || count > 512 || offset+2+count*12 > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := range count {
		tag, value, ok := parseNikonIFDEntry(b, offset+2+i*12, bigEndian)
		if !ok {
			continue
		}
		result[tag] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// parseNikonIFDEntry decodes a single 12-byte IFD entry at pos within b.
// Returns (tag, value slice, true) on success, or (0, nil, false) on any
// invalid or out-of-bounds data. The value slice aliases b directly (no copy).
func parseNikonIFDEntry(b []byte, pos int, bigEndian bool) (tag uint16, value []byte, ok bool) {
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
