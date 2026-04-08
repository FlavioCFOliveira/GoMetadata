// Package olympus parses Olympus MakerNote IFDs.
//
// Olympus uses two MakerNote variants (ExifTool Olympus.pm):
//
//   - Type 1 (legacy OM-1/OM-2 era): begins with "OLYMP\x00" (6 bytes).
//     IFD follows immediately at offset 8; byte order is inherited from the
//     parent TIFF. Uncommon in modern files.
//
//   - Type 2 (modern — E-system, PEN, OM-D, TG series): begins with
//     "OLYMPUS\x00" (8 bytes) followed by byte-order mark at [8..9] and
//     a 2-byte version at [10..11]. IFD entries start at offset 12. All
//     value offsets are relative to the start of the MakerNote (b[0]).
//
// This implementation supports Type 2. Type 1 files are accepted if they
// carry the "OLYMPUS\x00" prefix but are rare enough that the version
// distinction is negligible for practical use.
//
// Selected Olympus MakerNote tag IDs (ExifTool Olympus.pm):
//
//	0x0100  ThumbnailImage
//	0x0200  SpecialMode
//	0x0201  JpegQuality
//	0x0202  Macro
//	0x0203  BWMode
//	0x0204  DigitalZoom
//	0x0205  FocalPlaneDiagonal
//	0x0206  LensDistortionParams
//	0x0207  CameraType
//	0x0208  TextInfo
//	0x0209  CameraID
//	0x020B  EpsonImageWidth
//	0x020C  EpsonImageHeight
//	0x020D  EpsonSoftware
//	0x0280  PreviewImage
//	0x0300  PreCaptureFrames
//	0x0301  WhiteBoard
//	0x0302  OneTouchWB
//	0x0303  WhiteBalanceBracket
//	0x0304  WhiteBalanceBias
//	0x0400  SensorArea
//	0x0401  BlackLevel
//	0x0403  SceneMode
//	0x0404  SerialNumber
//	0x0405  Firmware
//	0x0E00  PrintIM
//	0x0F00  DataDump
//	0x0F01  DataDump2
//	0x0FE0  ZoomedPreviewStart
//	0x0FE1  ZoomedPreviewLength
//	0x0FE2  ZoomedPreviewSize
//	0x1000  ShutterSpeedValue
//	0x1001  ISOValue
//	0x1002  ApertureValue
//	0x1003  BrightnessValue
//	0x1004  FlashMode
//	0x1005  FlashDevice
//	0x1006  ExposureCompensation
//	0x1007  SensorTemperature
//	0x1008  LensTemperature
//	0x1009  LightCondition
//	0x100A  FocusRange
//	0x100B  FocusMode
//	0x100C  FocusDistance
//	0x100D  Zoom
//	0x100E  MacroFocus
//	0x100F  SharpnessFactor
//	0x1010  FlashChargeLevel
//	0x1011  ColorMatrix
//	0x1012  BlackLevel2
//	0x1015  WB_RBLevels
//	0x101C  WB_GLevel
//	0x1023  FlashExposureComp
//	0x1026  ExternalFlashBounce
//	0x1027  ExternalFlashZoom
//	0x1028  ExternalFlashMode
//	0x1029  Contrast
//	0x102A  SharpnessFactor2
//	0x102B  ColorControl
//	0x102C  ValidBits
//	0x102D  CoringFilter
//	0x102E  OlympusImageWidth
//	0x102F  OlympusImageHeight
//	0x1034  CompressionRatio
//	0x1035  PreviewImageValid
//	0x1036  PreviewImageStart
//	0x1037  PreviewImageLength
//	0x1039  CCDScanMode
//	0x103A  NoiseReduction
//	0x103B  InfinityLensStep
//	0x103C  NearLensStep
//	0x103D  LightValueCenter
//	0x103E  LightValuePeriphery
//	0x2010  Equipment
//	0x2020  CameraSettings
//	0x2030  RawDevelopment
//	0x2031  RawDev2
//	0x2040  ImageProcessing
//	0x2050  FocusInfo
//	0x3000  RawInfo
//	0x4000  MainInfo
package olympus

import "encoding/binary"

// Tag IDs for Olympus MakerNote IFD entries.
const (
	TagSpecialMode       uint16 = 0x0200
	TagJpegQuality       uint16 = 0x0201
	TagMacro             uint16 = 0x0202
	TagDigitalZoom       uint16 = 0x0204
	TagCameraType        uint16 = 0x0207
	TagCameraID          uint16 = 0x0209
	TagSerialNumber      uint16 = 0x0404
	TagFirmware          uint16 = 0x0405
	TagPrintIM           uint16 = 0x0E00
	TagShutterSpeedValue uint16 = 0x1000
	TagISOValue          uint16 = 0x1001
	TagApertureValue     uint16 = 0x1002
	TagFlashMode         uint16 = 0x1004
	TagFocusMode         uint16 = 0x100B
	TagFocusDistance     uint16 = 0x100C
	TagContrast          uint16 = 0x1029
	TagNoiseReduction    uint16 = 0x103A
	TagEquipment         uint16 = 0x2010
	TagCameraSettings    uint16 = 0x2020
	TagImageProcessing   uint16 = 0x2040
	TagFocusInfo         uint16 = 0x2050
)

// magicType2 is the required 8-byte prefix for the modern Olympus Type 2 MakerNote.
const magicType2 = "OLYMPUS\x00"

// minLengthType2 is the minimum length for a Type 2 MakerNote:
// 8 (magic) + 2 (byte order) + 2 (version) = 12 bytes, with IFD starting at 12.
// We require at least 12 + 2 (IFD count) = 14 bytes to attempt parsing.
const minLengthType2 = 14

// Parser implements makernote.Parser for Olympus cameras (Type 2 format).
type Parser struct{}

// Parse decodes an Olympus MakerNote payload into a map of tag ID → raw value bytes.
//
// The MakerNote must begin with "OLYMPUS\x00" (8 bytes), followed by the byte-order
// mark "II" (little-endian) or "MM" (big-endian) at [8..9], a 2-byte version at
// [10..11], and IFD entries starting at offset 12.
//
// All value offsets in IFD entries are relative to b[0] (the start of the payload).
// Returns nil, nil for unrecognised or too-short input (non-fatal for dispatch callers).
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < minLengthType2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	if string(b[:8]) != magicType2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}

	// Byte order at [8..9].
	var bigEndian bool
	switch {
	case b[8] == 'I' && b[9] == 'I':
		bigEndian = false
	case b[8] == 'M' && b[9] == 'M':
		bigEndian = true
	default:
		// Unrecognised byte order: degrade gracefully.
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}

	// IFD starts at offset 12.
	return parseIFDAt(b, 12, bigEndian), nil
}

// parseIFDAt walks a TIFF IFD starting at offset within b.
// bigEndian controls byte order for all multi-byte reads.
// All value offsets are relative to b[0].
// Returns nil if the IFD is malformed or out-of-bounds.
func parseIFDAt(b []byte, offset int, bigEndian bool) map[uint16][]byte { //nolint:unparam // offset is kept for API clarity
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
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		tag, value, ok := parseOlympusIFDEntry(b, offset+2+i*12, bigEndian)
		if !ok {
			continue
		}
		result[tag] = value
	}
	return result
}

// parseOlympusIFDEntry decodes a single 12-byte IFD entry at pos within b.
// Returns (tag, value slice, true) on success, or (0, nil, false) on any
// invalid or out-of-bounds data. The value slice aliases b directly (no copy).
func parseOlympusIFDEntry(b []byte, pos int, bigEndian bool) (tag uint16, value []byte, ok bool) {
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
	valEnd := off + int(total) //nolint:gosec // G115: total is a uint64 value offset bounded by file size
	if off < 0 || valEnd > len(b) {
		return 0, nil, false
	}
	return tag, b[off:valEnd], true
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
