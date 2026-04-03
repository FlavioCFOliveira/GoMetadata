// Package leica parses Leica MakerNote IFDs.
//
// Leica uses several MakerNote formats across its camera families
// (ExifTool Leica.pm):
//
//   - Type 0 (M8, M9, X1, X2, etc.): plain TIFF IFD at offset 0, parent byte order.
//     No magic prefix.
//
//   - Type 1–5 (S2, M Monochrom, etc.): "LEICA\x00" followed by a 2-byte
//     sub-type identifier, then a standard TIFF IFD at offset 8.
//
// Selected Leica MakerNote tag IDs:
//
//	0x0001  LensType (older cameras)
//	0x0300  OriginalFileName
//	0x0303  LensModel
//	0x0500  InternalSerialNumber
//	0x3000  SensorHeightWidth
package leica

import "encoding/binary"

// Tag IDs for Leica MakerNote IFD entries.
const (
	TagLensType            uint16 = 0x0001
	TagOriginalFileName    uint16 = 0x0300
	TagLensModel           uint16 = 0x0303
	TagInternalSerialNumber uint16 = 0x0500
	TagSensorHeightWidth   uint16 = 0x3000
)

// Parser implements makernote.Parser for Leica cameras.
type Parser struct{}

// Parse decodes a Leica MakerNote payload into a map of tag ID → raw value bytes.
// Handles both the prefixed and non-prefixed formats.
func (Parser) Parse(b []byte) (map[uint16][]byte, error) {
	if len(b) < 2 {
		return nil, nil
	}
	// Type 1–5: "LEICA\x00" prefix (6 bytes) + 2-byte sub-type + IFD at 8.
	if len(b) >= 8 && b[0] == 'L' && b[1] == 'E' && b[2] == 'I' &&
		b[3] == 'C' && b[4] == 'A' && b[5] == 0x00 {
		return parseIFDAt(b, 8, binary.LittleEndian), nil
	}
	// Type 0: plain IFD at offset 0, little-endian (most Leica cameras).
	// Try LE first, then BE.
	if result := parseIFDAt(b, 0, binary.LittleEndian); result != nil {
		return result, nil
	}
	return parseIFDAt(b, 0, binary.BigEndian), nil
}

func parseIFDAt(b []byte, offset int, order binary.ByteOrder) map[uint16][]byte {
	if len(b) < offset+2 {
		return nil
	}
	count := int(order.Uint16(b[offset:]))
	if count <= 0 || count > 512 || offset+2+count*12 > len(b) {
		return nil
	}
	result := make(map[uint16][]byte, count)
	for i := 0; i < count; i++ {
		pos := offset + 2 + i*12
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
