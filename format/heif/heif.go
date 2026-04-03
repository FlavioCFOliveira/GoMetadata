// Package heif implements extraction and injection of EXIF and XMP metadata
// within HEIF/HEIC files (ISO 23008-12 / ISO 14496-12 ISOBMFF).
//
// HEIF stores metadata as items referenced from the 'meta' box.
// The EXIF item has handler type 'Exif'; the XMP item has content type
// "application/rdf+xml". Item locations are resolved via the 'iloc' box.
package heif

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/flaviocfo/img-metadata/internal/bmff"
)

// Extract navigates the ISOBMFF box hierarchy of r and extracts raw payloads.
// rawEXIF has the 4-byte TIFF-header offset prefix stripped before return.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("heif: seek: %w", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("heif: read: %w", err)
	}

	rawEXIF, rawXMP, err = parseHEIFMetadata(data)
	return rawEXIF, nil, rawXMP, err
}

// Inject writes a modified HEIF stream to w with updated metadata items.
// This implementation is a pass-through; full HEIF box rewriting is not yet
// supported. A future implementation would update the iloc extents.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("heif: seek: %w", err)
	}
	_, err := io.Copy(w, r)
	return err
}

// parseHEIFMetadata parses the box hierarchy to find EXIF and XMP items.
func parseHEIFMetadata(data []byte) (rawEXIF, rawXMP []byte, err error) {
	// Find the 'meta' box. It can be inside 'moov' or at top-level.
	metaData, err := findBox(data, "meta")
	if err != nil || metaData == nil {
		return nil, nil, nil
	}

	// Parse item info (iinf) to map item IDs to their type.
	// Parse item locations (iloc) to find the data offset+size for each item.
	itemTypes := parseIinf(metaData)
	itemLocs := parseIloc(metaData)

	for id, typ := range itemTypes {
		loc, ok := itemLocs[id]
		if !ok {
			continue
		}
		if loc.offset+loc.length > uint64(len(data)) {
			continue
		}
		itemData := data[loc.offset : loc.offset+loc.length]

		switch typ {
		case "Exif":
			// HEIF EXIF item begins with a 4-byte offset to the TIFF header
			// within the item (ISO 23008-12 §6.6.1). Skip it.
			if len(itemData) >= 4 {
				skip := int(binary.BigEndian.Uint32(itemData[:4])) + 4
				if skip <= len(itemData) {
					rawEXIF = itemData[skip:]
				}
			}
		case "mime", "rdf+xml":
			rawXMP = itemData
		}
	}

	return rawEXIF, rawXMP, nil
}

// itemLoc holds the resolved location of an ISOBMFF item.
type itemLoc struct {
	offset uint64
	length uint64
}

// findBox searches data for the first box of the given type, returning its data.
// It performs a shallow search at the top level and one level deep.
func findBox(data []byte, boxType string) ([]byte, error) {
	pos := 0
	for pos+8 <= len(data) {
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		headerLen := uint64(8)

		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
			break
		}

		boxData := data[pos+int(headerLen) : pos+int(size)]

		if typ == boxType {
			// Skip the 4-byte version/flags for full boxes.
			if len(boxData) >= 4 {
				return boxData[4:], nil
			}
			return boxData, nil
		}

		// Recurse into container boxes.
		if typ == "moov" || typ == "trak" || typ == "udta" {
			inner, err := findBox(boxData, boxType)
			if err == nil && inner != nil {
				return inner, nil
			}
		}

		pos += int(size)
	}
	return nil, nil
}

// parseIinf parses an 'iinf' box body and returns a map from item ID to type string.
func parseIinf(metaData []byte) map[uint16]string {
	result := make(map[uint16]string)
	iinfData := findInnerBox(metaData, "iinf")
	if iinfData == nil {
		return result
	}

	// iinf: version(1) + flags(3) + item_count(2 or 4)
	if len(iinfData) < 2 {
		return result
	}
	version := iinfData[0]
	pos := 4 // skip version + flags
	var itemCount int
	if version == 0 {
		if pos+2 > len(iinfData) {
			return result
		}
		itemCount = int(binary.BigEndian.Uint16(iinfData[pos:]))
		pos += 2
	} else {
		if pos+4 > len(iinfData) {
			return result
		}
		itemCount = int(binary.BigEndian.Uint32(iinfData[pos:]))
		pos += 4
	}
	_ = itemCount

	// Parse infe (item info entry) boxes.
	for pos+8 <= len(iinfData) {
		size := uint64(binary.BigEndian.Uint32(iinfData[pos:]))
		typ := string(iinfData[pos+4 : pos+8])
		if size < 8 || uint64(pos)+size > uint64(len(iinfData)) {
			break
		}
		if typ == "infe" {
			infeData := iinfData[pos+8 : pos+int(size)]
			id, itemType := parseInfe(infeData)
			if itemType != "" {
				result[id] = itemType
			}
		}
		pos += int(size)
	}
	return result
}

// parseInfe parses an 'infe' box body and returns (item_ID, item_type).
func parseInfe(data []byte) (uint16, string) {
	if len(data) < 4 {
		return 0, ""
	}
	version := data[0]
	// Skip version + flags (4 bytes total).
	pos := 4

	var id uint16
	switch version {
	case 2:
		if pos+2 > len(data) {
			return 0, ""
		}
		id = binary.BigEndian.Uint16(data[pos:])
		pos += 2
		pos += 2 // item_protection_index
		if pos+4 > len(data) {
			return 0, ""
		}
		itemType := string(data[pos : pos+4])
		return id, itemType
	case 3:
		if pos+4 > len(data) {
			return 0, ""
		}
		id = uint16(binary.BigEndian.Uint32(data[pos:]))
		pos += 4
		pos += 2 // item_protection_index
		if pos+4 > len(data) {
			return 0, ""
		}
		itemType := string(data[pos : pos+4])
		return id, itemType
	}
	return 0, ""
}

// parseIloc parses an 'iloc' box body and returns a map from item ID to location.
func parseIloc(metaData []byte) map[uint16]itemLoc {
	result := make(map[uint16]itemLoc)
	ilocData := findInnerBox(metaData, "iloc")
	if ilocData == nil {
		return result
	}

	if len(ilocData) < 5 {
		return result
	}
	version := ilocData[0]
	pos := 4 // skip version + flags

	offsetSize := int(ilocData[pos] >> 4)
	lengthSize := int(ilocData[pos] & 0x0F)
	pos++
	baseOffsetSize := int(ilocData[pos] >> 4)
	indexSize := int(0)
	if version == 1 || version == 2 {
		indexSize = int(ilocData[pos] & 0x0F)
	}
	pos++

	var itemCount int
	if version < 2 {
		if pos+2 > len(ilocData) {
			return result
		}
		itemCount = int(binary.BigEndian.Uint16(ilocData[pos:]))
		pos += 2
	} else {
		if pos+4 > len(ilocData) {
			return result
		}
		itemCount = int(binary.BigEndian.Uint32(ilocData[pos:]))
		pos += 4
	}

	for i := 0; i < itemCount; i++ {
		var id uint16
		if version < 2 {
			if pos+2 > len(ilocData) {
				break
			}
			id = binary.BigEndian.Uint16(ilocData[pos:])
			pos += 2
		} else {
			if pos+4 > len(ilocData) {
				break
			}
			id = uint16(binary.BigEndian.Uint32(ilocData[pos:]))
			pos += 4
		}

		if version == 1 || version == 2 {
			pos += 2 // construction_method
		}

		var baseOffset uint64
		if baseOffsetSize > 0 {
			if pos+baseOffsetSize > len(ilocData) {
				break
			}
			baseOffset = readUintN(ilocData[pos:], baseOffsetSize)
			pos += baseOffsetSize
		}

		if pos+2 > len(ilocData) {
			break
		}
		extentCount := int(binary.BigEndian.Uint16(ilocData[pos:]))
		pos += 2

		var offset, length uint64
		for j := 0; j < extentCount; j++ {
			if indexSize > 0 {
				pos += indexSize
			}
			if offsetSize > 0 {
				if pos+offsetSize > len(ilocData) {
					break
				}
				offset = readUintN(ilocData[pos:], offsetSize) + baseOffset
				pos += offsetSize
			}
			if lengthSize > 0 {
				if pos+lengthSize > len(ilocData) {
					break
				}
				length = readUintN(ilocData[pos:], lengthSize)
				pos += lengthSize
			}
		}

		result[id] = itemLoc{offset: offset, length: length}
	}
	return result
}

// findInnerBox searches for a box of the given type within data (flat scan).
func findInnerBox(data []byte, boxType string) []byte {
	pos := 0
	for pos+8 <= len(data) {
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
			break
		}
		if typ == boxType {
			boxData := data[pos+int(headerLen) : pos+int(size)]
			return boxData
		}
		pos += int(size)
	}
	return nil
}

// readUintN reads an n-byte big-endian unsigned integer from b.
func readUintN(b []byte, n int) uint64 {
	var v uint64
	for i := 0; i < n && i < len(b); i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

// newBMFFReader is used to satisfy the import of bmff package.
var _ = bmff.ReadBox
