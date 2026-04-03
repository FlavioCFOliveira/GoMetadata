// Package samsung parses Samsung MakerNote IFDs.
//
// Samsung MakerNote structure (ExifTool Samsung.pm):
// A plain TIFF IFD at offset 0, parent byte order. No magic prefix.
// Used by Samsung NX and Galaxy camera series.
//
// Selected Samsung MakerNote tag IDs:
//
//	0x0001  MakerNoteVersion
//	0x0021  PictureWizard
//	0x0030  LocalLocationName
//	0x0035  PreviewIFDPointer
//	0x0043  CameraTemperature
//	0x00A0  FirmwareName
//	0x0100  SerialNumber
//	0x0101  MeteringMode
//	0x0102  SensorAreas
//	0x0103  ColorSpace
//	0x0104  SmartRange
//	0x0105  ExposureCompensation
//	0x0106  WB_RGGBLevels
//	0x0107  ColorTemperature
//	0x0108  ImageAlteringDetected
//	0x0200  BurstMode
//	0x0202  DriveMode
//	0x0300  FaceDetect
//	0xA001  SamsungModel
//	0xA002  SamsungSerialNumber
package samsung

import "encoding/binary"

// Tag IDs for Samsung MakerNote IFD entries.
const (
	TagMakerNoteVersion uint16 = 0x0001
	TagPictureWizard    uint16 = 0x0021
	TagCameraTemp       uint16 = 0x0043
	TagFirmwareName     uint16 = 0x00A0
	TagSerialNumber     uint16 = 0x0100
	TagMeteringMode     uint16 = 0x0101
	TagColorSpace       uint16 = 0x0103
	TagColorTemperature uint16 = 0x0107
	TagBurstMode        uint16 = 0x0200
	TagFaceDetect       uint16 = 0x0300
	TagSamsungModel     uint16 = 0xA001
)

// Parser implements makernote.Parser for Samsung cameras.
type Parser struct{}

// Parse decodes a Samsung MakerNote payload into a map of tag ID → raw value bytes.
// Samsung MakerNote is a standard TIFF IFD at offset 0; tries little-endian first.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 2 {
		return nil, nil
	}
	if result := parseAt(b, binary.LittleEndian); result != nil {
		return result, nil
	}
	return parseAt(b, binary.BigEndian), nil
}

func parseAt(b []byte, order binary.ByteOrder) map[uint16][]byte {
	if len(b) < 2 {
		return nil
	}
	count := int(order.Uint16(b[0:]))
	if count <= 0 || count > 512 || 2+count*12 > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ {
		pos := 2 + i*12
		tag := order.Uint16(b[pos:])
		typ := order.Uint16(b[pos+2:])
		cnt := order.Uint32(b[pos+4:])
		sz := typeSize(typ)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(cnt)
		var value []byte
		if total <= 4 {
			value = b[pos+8 : pos+8+int(total)]
		} else {
			off := order.Uint32(b[pos+8:])
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
