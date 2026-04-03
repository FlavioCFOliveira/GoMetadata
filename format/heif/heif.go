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
//
// The iloc box is fully rebuilt with updated entries for modified items,
// preserving all other items unchanged. Any enclosing container box sizes
// (e.g. moov) are patched if the meta box size changes. New item payloads
// are appended at the end of the output file.
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

	// Locate the meta box (absolute positions in the file).
	metaAbsStart, metaAbsEnd, metaContentOff, found := findMetaBoxAbs(data)
	if !found || metaAbsEnd > len(data) {
		_, err = w.Write(data)
		return err
	}
	metaContent := data[metaContentOff:metaAbsEnd]

	// Parse item types (iinf) and full iloc structure.
	itemTypes := parseIinf(metaContent)
	ilocInfo, ilocOK := parseIlocFull(metaContent)
	if !ilocOK || ilocInfo.offsetSize == 0 {
		// Cannot encode new item offsets without an offset field.
		_, err = w.Write(data)
		return err
	}

	// Map item IDs to their new payloads.
	pendingByID := make(map[uint16][]byte)
	for id, typ := range itemTypes {
		switch typ {
		case "Exif":
			if rawEXIF != nil {
				// HEIF EXIF item begins with a 4-byte offset to the TIFF header
				// within the item (ISO 23008-12 §6.6.1). Value 0 means immediate.
				prefix := [4]byte{}
				pendingByID[id] = append(prefix[:], rawEXIF...)
			}
		case "mime", "rdf+xml":
			if rawXMP != nil {
				pendingByID[id] = rawXMP
			}
		}
	}
	if len(pendingByID) == 0 {
		_, err = w.Write(data)
		return err
	}

	// Build updated iloc items. For each item we are replacing, collapse its
	// extents to a single one with a placeholder offset/length (filled later).
	// Set baseOffset to 0 so the extent offset is the absolute file offset.
	updatedItems := make([]ilocFullItem, len(ilocInfo.items))
	copy(updatedItems, ilocInfo.items)
	for i, item := range updatedItems {
		if _, ok := pendingByID[item.id]; ok {
			updatedItems[i].baseOffset = 0
			updatedItems[i].extents = []ilocExtent{{offset: 0, length: 0}}
		}
	}

	// The iloc size depends only on the structure (field widths, item/extent
	// counts), not on the offset/length values. Build a placeholder iloc to
	// determine the final meta box size before computing item offsets.
	placeholderIloc := buildIlocBox(ilocInfo, updatedItems)
	versionFlags := data[metaAbsStart+8 : metaContentOff] // 4 bytes: version + flags
	placeholderMeta := buildMetaBox(versionFlags, metaContent, placeholderIloc)
	metaDelta := len(placeholderMeta) - (metaAbsEnd - metaAbsStart)

	// Output layout:
	//   outputPrefix (data[:metaAbsStart] with patched ancestor sizes)
	//   + finalMetaBox
	//   + suffix (data[metaAbsEnd:])
	//   + newItemData
	prefix := data[:metaAbsStart]
	suffix := data[metaAbsEnd:]

	outputPrefix := make([]byte, len(prefix))
	copy(outputPrefix, prefix)
	if metaDelta != 0 {
		patchAncestorSize(outputPrefix, metaAbsStart, metaDelta)
	}

	// New item data is appended at the end of the output file.
	newItemBaseOffset := uint64(len(outputPrefix)) + uint64(len(placeholderMeta)) + uint64(len(suffix))

	// Assign real file offsets now that we know the output layout.
	var accumOffset uint64
	for i, item := range updatedItems {
		if payload, ok := pendingByID[item.id]; ok {
			updatedItems[i].extents[0].offset = newItemBaseOffset + accumOffset
			updatedItems[i].extents[0].length = uint64(len(payload))
			accumOffset += uint64(len(payload))
		}
	}

	// Rebuild iloc and meta with the real offsets.
	finalIlocBytes := buildIlocBox(ilocInfo, updatedItems)
	finalMetaBox := buildMetaBox(versionFlags, metaContent, finalIlocBytes)

	// Write: outputPrefix + finalMeta + suffix + new item payloads.
	if _, err := w.Write(outputPrefix); err != nil {
		return err
	}
	if _, err := w.Write(finalMetaBox); err != nil {
		return err
	}
	if _, err := w.Write(suffix); err != nil {
		return err
	}
	for _, item := range updatedItems {
		if payload, ok := pendingByID[item.id]; ok {
			if _, err := w.Write(payload); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// iloc full-structure types (used by Inject only)
// ---------------------------------------------------------------------------

// ilocBoxInfo holds the fully-parsed structure of an 'iloc' FullBox.
type ilocBoxInfo struct {
	version        uint8
	offsetSize     int // 0, 4, or 8  (ISO 14496-12 §8.11.3)
	lengthSize     int // 0, 4, or 8
	baseOffsetSize int // 0, 4, or 8
	indexSize      int // 0, 4, or 8 (only for iloc v1/v2)
	items          []ilocFullItem
}

// ilocFullItem is one entry in the iloc item list, including all extents.
type ilocFullItem struct {
	id              uint16
	constructMethod uint16 // construction_method (iloc v1/v2 only)
	baseOffset      uint64
	extents         []ilocExtent
}

// ilocExtent is one extent within an iloc item.
type ilocExtent struct {
	index  uint64 // extent_index (only if indexSize > 0)
	offset uint64
	length uint64
}

// parseIlocFull parses the 'iloc' FullBox found within metaContent and returns
// its complete structure including all per-item extents.
func parseIlocFull(metaContent []byte) (ilocBoxInfo, bool) {
	var info ilocBoxInfo
	ilocData := findInnerBox(metaContent, "iloc")
	if ilocData == nil || len(ilocData) < 5 {
		return info, false
	}

	info.version = ilocData[0]
	pos := 4 // skip version + flags

	info.offsetSize = int(ilocData[pos] >> 4)
	info.lengthSize = int(ilocData[pos] & 0x0F)
	pos++
	info.baseOffsetSize = int(ilocData[pos] >> 4)
	if info.version == 1 || info.version == 2 {
		info.indexSize = int(ilocData[pos] & 0x0F)
	}
	pos++

	var itemCount int
	if info.version < 2 {
		if pos+2 > len(ilocData) {
			return info, false
		}
		itemCount = int(binary.BigEndian.Uint16(ilocData[pos:]))
		pos += 2
	} else {
		if pos+4 > len(ilocData) {
			return info, false
		}
		itemCount = int(binary.BigEndian.Uint32(ilocData[pos:]))
		pos += 4
	}

	for i := 0; i < itemCount; i++ {
		var item ilocFullItem
		if info.version < 2 {
			if pos+2 > len(ilocData) {
				break
			}
			item.id = binary.BigEndian.Uint16(ilocData[pos:])
			pos += 2
		} else {
			if pos+4 > len(ilocData) {
				break
			}
			item.id = uint16(binary.BigEndian.Uint32(ilocData[pos:]))
			pos += 4
		}
		if info.version == 1 || info.version == 2 {
			if pos+2 > len(ilocData) {
				break
			}
			item.constructMethod = binary.BigEndian.Uint16(ilocData[pos:])
			pos += 2
		}
		if info.baseOffsetSize > 0 {
			if pos+info.baseOffsetSize > len(ilocData) {
				break
			}
			item.baseOffset = readUintN(ilocData[pos:], info.baseOffsetSize)
			pos += info.baseOffsetSize
		}
		if pos+2 > len(ilocData) {
			break
		}
		extentCount := int(binary.BigEndian.Uint16(ilocData[pos:]))
		pos += 2
		for j := 0; j < extentCount; j++ {
			var ext ilocExtent
			if info.indexSize > 0 {
				if pos+info.indexSize > len(ilocData) {
					break
				}
				ext.index = readUintN(ilocData[pos:], info.indexSize)
				pos += info.indexSize
			}
			if info.offsetSize > 0 {
				if pos+info.offsetSize > len(ilocData) {
					break
				}
				ext.offset = readUintN(ilocData[pos:], info.offsetSize)
				pos += info.offsetSize
			}
			if info.lengthSize > 0 {
				if pos+info.lengthSize > len(ilocData) {
					break
				}
				ext.length = readUintN(ilocData[pos:], info.lengthSize)
				pos += info.lengthSize
			}
			item.extents = append(item.extents, ext)
		}
		info.items = append(info.items, item)
	}
	return info, true
}

// buildIlocBox serialises the iloc FullBox from info (field sizes) and the
// given items (which may differ from info.items — e.g. with updated extents).
func buildIlocBox(info ilocBoxInfo, items []ilocFullItem) []byte {
	var body []byte
	// FullBox: version(1) + flags(3)
	body = append(body, info.version, 0, 0, 0)
	// offset_size (high nibble) | length_size (low nibble)
	body = append(body, byte(info.offsetSize<<4|info.lengthSize))
	// base_offset_size (high nibble) | index_size (low nibble)
	body = append(body, byte(info.baseOffsetSize<<4|info.indexSize))

	if info.version < 2 {
		body = appendUintN(body, 2, uint64(len(items)))
	} else {
		body = appendUintN(body, 4, uint64(len(items)))
	}

	for _, item := range items {
		if info.version < 2 {
			body = appendUintN(body, 2, uint64(item.id))
		} else {
			body = appendUintN(body, 4, uint64(item.id))
		}
		if info.version == 1 || info.version == 2 {
			body = appendUintN(body, 2, uint64(item.constructMethod))
		}
		if info.baseOffsetSize > 0 {
			body = appendUintN(body, info.baseOffsetSize, item.baseOffset)
		}
		body = appendUintN(body, 2, uint64(len(item.extents)))
		for _, ext := range item.extents {
			if info.indexSize > 0 {
				body = appendUintN(body, info.indexSize, ext.index)
			}
			if info.offsetSize > 0 {
				body = appendUintN(body, info.offsetSize, ext.offset)
			}
			if info.lengthSize > 0 {
				body = appendUintN(body, info.lengthSize, ext.length)
			}
		}
	}

	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body)))
	copy(hdr[4:], "iloc")
	return append(hdr, body...)
}

// buildMetaBox constructs a meta FullBox by copying all child boxes from
// metaContent except iloc, then appending newIloc.
// versionFlags is the 4-byte FullBox version+flags field.
func buildMetaBox(versionFlags, metaContent, newIloc []byte) []byte {
	var body []byte
	body = append(body, versionFlags...)

	pos := 0
	for pos+8 <= len(metaContent) {
		size := uint64(binary.BigEndian.Uint32(metaContent[pos:]))
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(metaContent) {
				break
			}
			size = binary.BigEndian.Uint64(metaContent[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(metaContent) - pos)
		}
		if uint64(pos)+size > uint64(len(metaContent)) {
			break
		}
		typ := string(metaContent[pos+4 : pos+8])
		if typ != "iloc" {
			body = append(body, metaContent[pos:pos+int(size)]...)
		}
		_ = headerLen
		pos += int(size)
	}
	body = append(body, newIloc...)

	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body)))
	copy(hdr[4:], "meta")
	return append(hdr, body...)
}

// patchAncestorSize adds delta to the size field of any top-level container
// box in data whose byte range contains metaAbsStart (i.e. wraps the meta
// box). This is needed when meta is nested inside moov and meta changes size.
// Only 32-bit box sizes are handled; extended-size (size==1) boxes are left
// unchanged because they are not used in typical HEIF files.
func patchAncestorSize(data []byte, metaAbsStart, delta int) {
	pos := 0
	for pos+8 <= len(data) {
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		if size == 1 {
			// Extended 64-bit size — skip patching (uncommon in HEIF photos).
			break
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
			break
		}
		boxEnd := pos + int(size)
		if pos < metaAbsStart && metaAbsStart < boxEnd {
			// This box wraps the meta box — update its size.
			newSize := int(size) + delta
			if newSize > 0 {
				binary.BigEndian.PutUint32(data[pos:], uint32(newSize))
			}
			break
		}
		pos += int(size)
	}
}

// appendUintN appends v encoded as an n-byte big-endian integer to b.
func appendUintN(b []byte, n int, v uint64) []byte {
	tmp := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		tmp[i] = byte(v)
		v >>= 8
	}
	return append(b, tmp...)
}

// ---------------------------------------------------------------------------
// Extract helpers (unchanged from original)
// ---------------------------------------------------------------------------

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
