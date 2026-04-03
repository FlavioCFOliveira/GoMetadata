// Package pentax parses Pentax MakerNote IFDs.
//
// Pentax DSLRs use the "AOC" MakerNote format for modern K-series and 645-series
// bodies. A small number of very old Pentax/Samsung bodies used an earlier format
// with the prefix "PENTAX \x00" (8 bytes); that variant is handled as a fallback.
//
// AOC format (ExifTool Pentax.pm §3.1 — most common for Pentax K-series DSLRs):
//
//	[0..3]  "AOC\x00"  magic (4 bytes)
//	[4..5]  version    big-endian uint16 (ignored during parsing)
//	[6..]   IFD entries, big-endian byte order
//	        All value offsets are relative to b[0].
//
// PENTAX format (older bodies, Samsung GX-series):
//
//	[0..7]  "PENTAX \x00"  magic (8 bytes, note trailing space)
//	[8..9]  "II" or "MM"   byte order
//	[10..11] version       (ignored)
//	[12..]  IFD entries
//	        All value offsets are relative to b[0].
//
// Selected Pentax MakerNote tag IDs (ExifTool Pentax.pm):
//
//	0x0000  PentaxVersion
//	0x0001  PentaxModelType
//	0x0002  PreviewImageSize
//	0x0003  PreviewImageLength
//	0x0004  PreviewImageStart
//	0x0005  PentaxModelID
//	0x0006  Date
//	0x0007  Time
//	0x0008  Quality
//	0x0009  PentaxImageSize
//	0x000B  PictureMode
//	0x000C  FlashMode
//	0x000D  FocusMode
//	0x000E  AFAreaMode
//	0x000F  AFAreaPoint
//	0x0010  AutoAFPoint
//	0x0011  FocusPosition
//	0x0012  ExposureTime
//	0x0013  FNumber
//	0x0014  ISO
//	0x0015  LightReading
//	0x0016  ExposureCompensation
//	0x0017  MeteringMode
//	0x0018  AutoBracketing
//	0x0019  WhiteBalance
//	0x001A  WhiteBalanceMode
//	0x001B  BlueBalance
//	0x001C  RedBalance
//	0x001D  FocalLength
//	0x001E  DigitalZoom
//	0x001F  Saturation
//	0x0020  Contrast
//	0x0021  Sharpness
//	0x0022  WorldTimeLocation
//	0x0023  HometownCity
//	0x0024  DestinationCity
//	0x0025  HometownDST
//	0x0026  DestinationDST
//	0x0027  DSPFirmwareVersion
//	0x0028  CPUFirmwareVersion
//	0x0029  FrameNumber
//	0x002D  EffectiveLV
//	0x0032  ImageEditing
//	0x0033  PictureMode2
//	0x0034  DriveMode
//	0x0035  SensorSize
//	0x0037  ColorSpace
//	0x0038  ImageAreaOffset
//	0x0039  RawImageSize
//	0x003C  AFPointsInFocus
//	0x003F  DataScaling
//	0x0040  PreviewImageBorders
//	0x0041  LensRec
//	0x0042  SensitivityAdjust
//	0x0047  Temperature
//	0x004D  NoiseReduction
//	0x004F  FlashExpComp
//	0x0050  ImageTone
//	0x0053  ColorTemperature
//	0x005C  ShakeReduction
//	0x005D  ShutterCount
//	0x0060  FaceInfo
//	0x0062  RawDevelopmentProcess
//	0x0067  Hue
//	0x0068  AWBInfo
//	0x0069  DynamicRange
//	0x0071  HighISONoiseReduction
//	0x0072  AFAdjustment
//	0x0073  MonochromeFilterEffect
//	0x0074  MonochromeToning
//	0x0076  FaceDetect
//	0x0077  FaceDetectFrameSize
//	0x0079  ShadowCompensation
//	0x007B  HDR
//	0x007F  ShutterType
//	0x0082  NeutralDensityFilter
//	0x0085  ISO2
//	0x008B  BlackPoint
//	0x008C  WhitePoint
//	0x0092  CameraTemperature
//	0x0095  AELock
//	0x0096  NoiseReduction2
//	0x0097  FlashStatus
//	0x0098  AccelerometerYaw
//	0x0099  AccelerometerPitch
//	0x009A  AccelerometerRoll
//	0x009D  ImageProcessingCount
//	0x00A0  MakerNoteVersion
//	0x00A1  SceneMode
//	0x00A2  ImageCount2
//	0x00A4  AFStatus
//	0x00A7  ImageCount
//	0x00A9  WBMediaInfo
//	0x00AB  SensorWidth
//	0x00AC  SensorHeight
//	0x00B0  SerialNumber
//	0x00B1  Firmware
//	0x00E0  ImageDataSize
//	0x00E2  DriveMode2
//	0x00E3  AELock2
package pentax

import "encoding/binary"

// Tag IDs for Pentax MakerNote IFD entries.
const (
	TagPentaxVersion        uint16 = 0x0000
	TagPentaxModelID        uint16 = 0x0005
	TagQuality              uint16 = 0x0008
	TagPentaxImageSize      uint16 = 0x0009
	TagPictureMode          uint16 = 0x000B
	TagFlashMode            uint16 = 0x000C
	TagFocusMode            uint16 = 0x000D
	TagISO                  uint16 = 0x0014
	TagExposureCompensation uint16 = 0x0016
	TagMeteringMode         uint16 = 0x0017
	TagWhiteBalance         uint16 = 0x0019
	TagFocalLength          uint16 = 0x001D
	TagSaturation           uint16 = 0x001F
	TagContrast             uint16 = 0x0020
	TagSharpness            uint16 = 0x0021
	TagFrameNumber          uint16 = 0x0029
	TagColorSpace           uint16 = 0x0037
	TagShakeReduction       uint16 = 0x005C
	TagShutterCount         uint16 = 0x005D
	TagDynamicRange         uint16 = 0x0069
	TagHDR                  uint16 = 0x007B
	TagSerialNumber         uint16 = 0x00B0
	TagFirmware             uint16 = 0x00B1
	TagImageDataSize        uint16 = 0x00E0
)

// magicAOC is the 4-byte magic for the modern Pentax AOC MakerNote.
const magicAOC = "AOC\x00"

// magicPENTAX is the 8-byte magic for the older Pentax MakerNote variant.
const magicPENTAX = "PENTAX \x00"

// minLengthAOC is the minimum AOC MakerNote length:
// 4 (magic) + 2 (version) = 6 bytes, with IFD beginning at 6.
// We need at least 6 + 2 (IFD count) = 8 bytes.
const minLengthAOC = 8

// minLengthPENTAX is the minimum PENTAX-prefix MakerNote length:
// 8 (magic) + 2 (byte order) + 2 (version) = 12, IFD at 12. Need 12+2 = 14.
const minLengthPENTAX = 14

// Parser implements makernote.Parser for Pentax cameras.
type Parser struct{}

// Parse decodes a Pentax MakerNote payload into a map of tag ID → raw value bytes.
//
// Two formats are supported:
//   - AOC format ("AOC\x00" prefix, big-endian, IFD at offset 6). Used by all
//     modern K-series and 645-series DSLRs.
//   - PENTAX format ("PENTAX \x00" prefix, byte order at [8..9], IFD at offset 12).
//     Used by older Samsung GX-series / early Pentax DSLRs.
//
// Returns nil, nil for unrecognised or too-short input.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	switch {
	case len(b) >= minLengthAOC && string(b[:4]) == magicAOC:
		// AOC format: big-endian, IFD at offset 6.
		return parseIFDAt(b, 6, true /* big-endian */), nil

	case len(b) >= minLengthPENTAX && string(b[:8]) == magicPENTAX:
		// PENTAX prefix format: byte order at [8..9], IFD at offset 12.
		var bigEndian bool
		switch {
		case b[8] == 'I' && b[9] == 'I':
			bigEndian = false
		case b[8] == 'M' && b[9] == 'M':
			bigEndian = true
		default:
			return nil, nil
		}
		return parseIFDAt(b, 12, bigEndian), nil
	}
	return nil, nil
}

// parseIFDAt walks a TIFF IFD starting at offset within b.
// bigEndian controls byte order for all multi-byte reads.
// All value offsets in IFD entries are relative to b[0].
// Returns nil if the IFD is malformed or out-of-bounds.
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
			continue
		}
		total := uint64(sz) * uint64(cnt)

		var value []byte
		if total <= 4 {
			value = b[pos+8 : pos+8+int(total)]
		} else {
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
