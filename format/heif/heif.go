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
	"math"

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
// New item data is appended at the end of the file and the iloc extents are
// patched in-place to point to the new locations. No box sizes change.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("heif: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("heif: read: %w", err)
	}

	// Pass through if nothing to update.
	if rawEXIF == nil && rawXMP == nil {
		_, err = w.Write(data)
		return err
	}

	// Find the meta box (absolute positions in file).
	_, metaAbsEnd, metaContentOff, found := findMetaBoxAbs(data)
	if !found || metaAbsEnd > len(data) {
		_, err = w.Write(data)
		return err
	}
	metaContent := data[metaContentOff:metaAbsEnd]

	// Parse item types and iloc info (with field positions for patching).
	itemTypes := parseIinf(metaContent)
	ilocInfo, ilocFound := findIlocPatchInfo(data, metaContentOff, metaAbsEnd)
	if !ilocFound {
		_, err = w.Write(data)
		return err
	}

	// Build the output by copying the file and appending new item data.
	out := make([]byte, len(data))
	copy(out, data)

	for id, typ := range itemTypes {
		var newData []byte
		switch typ {
		case "Exif":
			if rawEXIF == nil {
				continue
			}
			// HEIF EXIF item begins with a 4-byte offset to the TIFF header
			// within the item (ISO 23008-12 §6.6.1). Value 0 means the TIFF
			// header starts immediately.
			prefix := [4]byte{}
			newData = append(prefix[:], rawEXIF...)
		case "mime", "rdf+xml":
			if rawXMP == nil {
				continue
			}
			newData = rawXMP
		default:
			continue
		}

		newOffset := uint64(len(out))
		out = append(out, newData...)

		extPos, ok := ilocInfo.extentPos[id]
		if !ok {
			continue
		}
		// Patch the extent_offset and extent_length fields in iloc.
		if ilocInfo.offsetSize > 0 && extPos.offsetAbsPos+ilocInfo.offsetSize <= len(out) {
			writeUintNBE(out[extPos.offsetAbsPos:], ilocInfo.offsetSize, newOffset)
		}
		if ilocInfo.lengthSize > 0 && extPos.lengthAbsPos+ilocInfo.lengthSize <= len(out) {
			writeUintNBE(out[extPos.lengthAbsPos:], ilocInfo.lengthSize, uint64(len(newData)))
		}
	}

	_, err = w.Write(out)
	return err
}

// ilocPatchInfo holds the information needed to patch iloc extents in-place.
type ilocPatchInfo struct {
	offsetSize int
	lengthSize int
	// extentPos maps item ID to the absolute file positions of the first
	// extent's offset and length fields (for simple single-extent items).
	extentPos map[uint16]ilocExtentFieldPos
}

// ilocExtentFieldPos records the absolute byte positions of extent fields.
type ilocExtentFieldPos struct {
	offsetAbsPos int
	lengthAbsPos int
}

// findMetaBoxAbs finds the 'meta' FullBox in the file and returns its
// absolute start, end, and the offset where its content begins (after header+version/flags).
func findMetaBoxAbs(data []byte) (absStart, absEnd, contentOff int, found bool) {
	// Search at top level first.
	if s, e, ok := flatBoxRangeInFile(data, "meta"); ok {
		return s, e, s + 8 + 4, true // +8 header, +4 FullBox version/flags
	}
	// Search inside moov.
	ms, me, ok := flatBoxRangeInFile(data, "moov")
	if !ok {
		return 0, 0, 0, false
	}
	moovContent := data[ms+8 : me]
	if s, e, ok := flatBoxRangeInFile(moovContent, "meta"); ok {
		absS := ms + 8 + s
		absE := ms + 8 + e
		return absS, absE, absS + 8 + 4, true
	}
	return 0, 0, 0, false
}

// findIlocPatchInfo locates the iloc box within meta content and parses it
// to collect per-item extent field positions (absolute in the file).
func findIlocPatchInfo(data []byte, metaContentOff, metaAbsEnd int) (ilocPatchInfo, bool) {
	result := ilocPatchInfo{extentPos: make(map[uint16]ilocExtentFieldPos)}
	metaContent := data[metaContentOff:metaAbsEnd]

	ilocRelStart, ilocRelEnd, found := flatBoxRangeInFile(metaContent, "iloc")
	if !found || ilocRelEnd > len(metaContent) {
		return result, false
	}

	// iloc box starts at: metaContentOff + ilocRelStart (absolute in file)
	ilocAbsStart := metaContentOff + ilocRelStart
	// iloc body (after 8-byte header) absolute start:
	ilocBodyAbs := ilocAbsStart + 8

	ilocData := metaContent[ilocRelStart+8 : ilocRelEnd]
	if len(ilocData) < 5 {
		return result, false
	}

	version := ilocData[0]
	pos := 4 // skip version + flags

	result.offsetSize = int(ilocData[pos] >> 4)
	result.lengthSize = int(ilocData[pos] & 0x0F)
	pos++
	baseOffsetSize := int(ilocData[pos] >> 4)
	indexSize := 0
	if version == 1 || version == 2 {
		indexSize = int(ilocData[pos] & 0x0F)
	}
	pos++

	var itemCount int
	if version < 2 {
		if pos+2 > len(ilocData) {
			return result, false
		}
		itemCount = int(binary.BigEndian.Uint16(ilocData[pos:]))
		pos += 2
	} else {
		if pos+4 > len(ilocData) {
			return result, false
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
		if baseOffsetSize > 0 {
			if pos+baseOffsetSize > len(ilocData) {
				break
			}
			pos += baseOffsetSize
		}
		if pos+2 > len(ilocData) {
			break
		}
		extentCount := int(binary.BigEndian.Uint16(ilocData[pos:]))
		pos += 2

		// Only patch the first extent (most HEIF items have a single extent).
		var extPos ilocExtentFieldPos
		for j := 0; j < extentCount; j++ {
			if indexSize > 0 {
				pos += indexSize
			}
			absOffPos := ilocBodyAbs + pos
			if j == 0 {
				extPos.offsetAbsPos = absOffPos
			}
			pos += result.offsetSize
			absLenPos := ilocBodyAbs + pos
			if j == 0 {
				extPos.lengthAbsPos = absLenPos
			}
			pos += result.lengthSize
		}
		result.extentPos[id] = extPos
	}

	return result, true
}

// flatBoxRangeInFile performs a flat scan and returns start+end of the first
// box matching boxType. Returns (0, 0, false) if not found.
func flatBoxRangeInFile(data []byte, boxType string) (start, end int, found bool) {
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
			return pos, pos + int(size), true
		}
		_ = headerLen
		pos += int(size)
	}
	return 0, 0, false
}

// writeUintNBE writes v as an n-byte big-endian integer into b.
func writeUintNBE(b []byte, n int, v uint64) {
	for i := n - 1; i >= 0; i-- {
		if i < len(b) {
			b[i] = byte(v)
		}
		v >>= 8
	}
}

// parseHEIFMetadata parses the box hierarchy to find EXIF and XMP items.
func parseHEIFMetadata(data []byte) (rawEXIF, rawXMP []byte, err error) {
	// Find the 'meta' box. It can be inside 'moov' or at top-level.
	metaData, err := findBox(data, "meta", 0)
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
// It performs a shallow search at the top level and recurses into container boxes
// up to depth levels deep (max 32) to prevent stack exhaustion on crafted input.
func findBox(data []byte, boxType string, depth int) ([]byte, error) {
	if depth > 32 {
		return nil, nil
	}
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
			inner, err := findBox(boxData, boxType, depth+1)
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
				rawOff := readUintN(ilocData[pos:], offsetSize)
				// Guard against integer overflow in offset + baseOffset.
				if rawOff > math.MaxUint64-baseOffset {
					break
				}
				offset = rawOff + baseOffset
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

// bmff is imported for future use in Inject (HEIF box rewriting, Batch 5).
// The blank assignment retains the import while the full implementation is pending.
var _ = bmff.ReadBox
