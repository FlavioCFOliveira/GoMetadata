// Package dji parses DJI drone MakerNote IFDs.
//
// DJI MakerNote structure (ExifTool DJI.pm):
// A plain TIFF IFD at offset 0, parent byte order. No magic prefix.
// Used by DJI Phantom, Mavic, Mini, Air, and Zenmuse series.
//
// Selected DJI MakerNote tag IDs:
//
//	0x0001  Make (always "DJI")
//	0x0003  SpeedX
//	0x0004  SpeedY
//	0x0005  SpeedZ
//	0x0006  Pitch
//	0x0007  Yaw
//	0x0008  Roll
//	0x0009  CameraPitch
//	0x000A  CameraYaw
//	0x000B  CameraRoll
package dji

import "encoding/binary"

// Tag IDs for DJI MakerNote IFD entries.
const (
	TagMake        uint16 = 0x0001
	TagSpeedX      uint16 = 0x0003
	TagSpeedY      uint16 = 0x0004
	TagSpeedZ      uint16 = 0x0005
	TagPitch       uint16 = 0x0006
	TagYaw         uint16 = 0x0007
	TagRoll        uint16 = 0x0008
	TagCameraPitch uint16 = 0x0009
	TagCameraYaw   uint16 = 0x000A
	TagCameraRoll  uint16 = 0x000B
)

// Parser implements makernote.Parser for DJI cameras.
type Parser struct{}

// Parse decodes a DJI MakerNote payload into a map of tag ID → raw value bytes.
// DJI MakerNote is a standard TIFF IFD at offset 0 (little-endian).
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 2 {
		return nil, nil //nolint:nilnil // (nil, nil) signals "unrecognized format"; Parser interface contract
	}
	// Try LE (DJI cameras use LE); fall back to BE.
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
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		tag, value, ok := parseDJIIFDEntry(b, 2+i*12, order)
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

// parseDJIIFDEntry decodes a single 12-byte IFD entry at pos within b.
// Returns (tag, value slice, true) on success, or (0, nil, false) on any
// invalid or out-of-bounds data. The value slice aliases b directly (no copy).
func parseDJIIFDEntry(b []byte, pos int, order binary.ByteOrder) (tag uint16, value []byte, ok bool) {
	if pos < 0 || pos+12 > len(b) {
		return 0, nil, false
	}
	tag = order.Uint16(b[pos:])
	typ := order.Uint16(b[pos+2:])
	cnt := order.Uint32(b[pos+4:])
	sz := typeSize(typ)
	if sz == 0 {
		return 0, nil, false
	}
	total := uint64(sz) * uint64(cnt)
	if total <= 4 {
		return tag, b[pos+8 : pos+8+int(total)], true
	}
	off := order.Uint32(b[pos+8:])
	end := uint64(off) + total
	if end > uint64(len(b)) {
		return 0, nil, false
	}
	return tag, b[off:end], true
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
