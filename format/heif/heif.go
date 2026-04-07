// Package heif implements extraction and injection of EXIF and XMP metadata
// within HEIF/HEIC files (ISO 23008-12 / ISO 14496-12 ISOBMFF).
//
// HEIF stores metadata as items referenced from the 'meta' box.
// The EXIF item has handler type 'Exif'; the XMP item has content type
// "application/rdf+xml". Item locations are resolved via the 'iloc' box.
package heif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// Extract navigates the ISOBMFF box hierarchy of r and extracts raw payloads.
// rawEXIF has the 4-byte TIFF-header offset prefix stripped before return.
//
// Fast path: reads only the first 64 KB to locate the meta box (iinf+iloc+pitm),
// then seeks directly to each item payload — no full-file allocation needed.
// Slow path fallback (meta box beyond 64 KB header window): reads the full file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("heif: seek: %w", err)
	}

	// Read the first 64 KB: sufficient to contain the meta box in all typical
	// HEIF/HEIC files (iinf + iloc + pitm are compact; < 4 KB in practice).
	// largePool always provides a cap-65536 buffer so Get never allocates fresh.
	const headerWindow = 65536
	hdrPtr := iobuf.Get(headerWindow)
	hdr := *hdrPtr
	n, rerr := io.ReadFull(r, hdr[:headerWindow])
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		iobuf.Put(hdrPtr)
		return nil, nil, nil, fmt.Errorf("heif: read: %w", rerr)
	}
	hdr = hdr[:n]

	// Find the meta box within the header window.
	metaData, _ := findBox(hdr, "meta", 0)
	if metaData != nil {
		// Fast path: meta box fully within header window.
		// Read item payloads by seeking rather than slicing a full in-memory copy.
		// extractFromMetaData reads from metaData (a subslice of *hdrPtr), so
		// iobuf.Put must be called AFTER extractFromMetaData returns — returning
		// hdrPtr to the pool first would allow another goroutine to overwrite the
		// buffer while extractFromMetaData is still reading it (data race).
		rawEXIF, rawXMP, err = extractFromMetaData(r, metaData)
		iobuf.Put(hdrPtr)
		return rawEXIF, nil, rawXMP, err
	}

	// Slow path fallback: meta box not found in first 64 KB (extremely rare —
	// HEIF spec places meta at the file head). Read the full file.
	iobuf.Put(hdrPtr)
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

// readItemPayload seeks r to loc.offset and reads loc.length bytes.
// The returned slice is owned by the caller; r is left at an unspecified position.
func readItemPayload(r io.ReadSeeker, loc itemLoc) ([]byte, error) {
	if loc.length == 0 {
		return nil, nil
	}
	if _, err := r.Seek(int64(loc.offset), io.SeekStart); err != nil { //nolint:gosec // G115: offset fits int64 for any realistic file
		return nil, fmt.Errorf("heif: seek to item: %w", err)
	}
	payload := make([]byte, loc.length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("heif: read item payload: %w", err)
	}
	return payload, nil
}

// extractFromMetaData parses the meta box content and seeks r to read EXIF/XMP
// item payloads directly, avoiding a full file read.
// metaData must be the content of the meta box with the 4-byte FullBox
// version+flags already stripped (as returned by findBox).
// ISO 23008-12 §6.2 (primary item), §6.6.1 (EXIF item layout).
func extractFromMetaData(r io.ReadSeeker, metaData []byte) (rawEXIF, rawXMP []byte, err error) {
	itemTypes := parseIinf(metaData)
	itemLocs := parseIloc(metaData)
	primaryID := parsePitm(metaData)

	bestEXIFID, exifFound := selectBestItem(itemTypes, primaryID, "Exif")
	bestXMPID, xmpFound := selectBestItem(itemTypes, primaryID, "mime", "rdf+xml")

	if exifFound {
		if loc, ok := itemLocs[bestEXIFID]; ok && loc.length > 0 {
			payload, e := readItemPayload(r, loc)
			if e == nil {
				rawEXIF = extractExifFromData(payload)
			}
		}
	}
	if xmpFound {
		if loc, ok := itemLocs[bestXMPID]; ok && loc.length > 0 {
			rawXMP, err = readItemPayload(r, loc)
		}
	}
	return rawEXIF, rawXMP, err
}

// writePassThrough copies data to w verbatim, wrapping any error.
func writePassThrough(w io.Writer, data []byte) error {
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("heif: write: %w", err)
	}
	return nil
}

// injectComponents holds the output sections produced by buildInjectComponents.
type injectComponents struct {
	outputPrefix []byte
	finalMetaBox []byte
	suffix       []byte
	updatedItems []ilocFullItem
	pendingByID  map[uint16][]byte
}

// buildInjectComponents parses the existing meta box, maps pending items, computes
// the final output prefix (with patched ancestor sizes), and builds the final
// meta box with correct iloc offsets. Returns false if injection cannot proceed
// (e.g. no matching items, unreadable iloc) — caller should pass through unchanged.
func buildInjectComponents(data []byte, metaAbsStart, metaAbsEnd, metaContentOff int, rawEXIF, rawXMP []byte) (injectComponents, bool) {
	metaContent := data[metaContentOff:metaAbsEnd]

	itemTypes := parseIinf(metaContent)
	ilocInfo, ilocOK := parseIlocFull(metaContent)
	if !ilocOK || ilocInfo.offsetSize == 0 {
		return injectComponents{}, false
	}

	pendingByID := mapPendingItems(itemTypes, rawEXIF, rawXMP)
	if len(pendingByID) == 0 {
		return injectComponents{}, false
	}

	updatedItems := updateIlocItems(ilocInfo.items, pendingByID)

	versionFlags := data[metaAbsStart+8 : metaContentOff] // 4 bytes: version + flags
	placeholderIloc := buildIlocBox(ilocInfo, updatedItems)
	placeholderMeta := buildMetaBox(versionFlags, metaContent, placeholderIloc)
	metaDelta := len(placeholderMeta) - (metaAbsEnd - metaAbsStart)

	suffix := data[metaAbsEnd:]
	outputPrefix := make([]byte, metaAbsStart)
	copy(outputPrefix, data[:metaAbsStart])
	if metaDelta != 0 {
		patchAncestorSize(outputPrefix, metaAbsStart, metaDelta)
	}

	newItemBaseOffset := uint64(len(outputPrefix)) + uint64(len(placeholderMeta)) + uint64(len(suffix))
	updatedItems = assignItemOffsets(updatedItems, pendingByID, newItemBaseOffset)

	finalIlocBytes := buildIlocBox(ilocInfo, updatedItems)
	finalMetaBox := buildMetaBox(versionFlags, metaContent, finalIlocBytes)

	return injectComponents{
		outputPrefix: outputPrefix,
		finalMetaBox: finalMetaBox,
		suffix:       suffix,
		updatedItems: updatedItems,
		pendingByID:  pendingByID,
	}, true
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

	if rawEXIF == nil && rawXMP == nil {
		return writePassThrough(w, data)
	}

	metaAbsStart, metaAbsEnd, metaContentOff, found := findMetaBoxAbs(data)
	if !found || metaAbsEnd > len(data) {
		return writePassThrough(w, data)
	}

	comp, ok := buildInjectComponents(data, metaAbsStart, metaAbsEnd, metaContentOff, rawEXIF, rawXMP)
	if !ok {
		return writePassThrough(w, data)
	}

	return writeHEIFOutput(w, comp.outputPrefix, comp.finalMetaBox, comp.suffix, comp.updatedItems, comp.pendingByID)
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

// parseHEIFBoxHeader reads an ISOBMFF box header starting at pos in data.
// It handles both the 8-byte base form and the 16-byte extended-size form
// (size == 1). Returns size, type string, headerLen, and whether the read
// succeeded.
//
// ISO 14496-12 §4.2: if size == 1, a 64-bit largesize field follows the type.
// If size == 0, the box extends to the end of the containing structure.
func parseHEIFBoxHeader(data []byte, pos int) (size uint64, typ string, headerLen uint64, valid bool) {
	if pos+8 > len(data) {
		return 0, "", 0, false
	}
	size = uint64(binary.BigEndian.Uint32(data[pos:]))
	typ = string(data[pos+4 : pos+8])
	headerLen = 8

	if size == 1 {
		if pos+16 > len(data) {
			return 0, "", 0, false
		}
		size = binary.BigEndian.Uint64(data[pos+8:])
		headerLen = 16
	}
	if size == 0 {
		// len(data)-pos is non-negative: guarded by pos+8 ≤ len(data) check above.
		size = uint64(len(data) - pos) //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
	}
	// Bounds check without casting pos: size must not exceed remaining bytes.
	if size > uint64(len(data)-pos) { //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
		return 0, "", 0, false
	}
	return size, typ, headerLen, true
}

// parseIlocFull parses the 'iloc' FullBox found within metaContent and returns
// its complete structure including all per-item extents.
func parseIlocFull(metaContent []byte) (ilocBoxInfo, bool) {
	var info ilocBoxInfo
	ilocData := findInnerBox(metaContent, "iloc")
	if len(ilocData) < 5 {
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

	itemCount, newPos, ok := parseIinfItemCount(ilocData, info.version, pos)
	if !ok {
		return info, false
	}
	pos = newPos

	for range itemCount {
		item, updatedPos, itemOK := parseIlocFullItem(ilocData, pos, info)
		if !itemOK {
			break
		}
		pos = updatedPos
		info.items = append(info.items, item)
	}
	return info, true
}

// readIlocItemID reads the version-dependent item_ID field from ilocData at pos.
// version < 2 uses a 2-byte field; version 2 uses a 4-byte field capped to uint16.
// ISO 14496-12 §8.11.3.
func readIlocItemID(ilocData []byte, pos int, version uint8) (id uint16, newPos int, ok bool) {
	if version < 2 {
		if pos+2 > len(ilocData) {
			return 0, pos, false
		}
		return binary.BigEndian.Uint16(ilocData[pos:]), pos + 2, true
	}
	if pos+4 > len(ilocData) {
		return 0, pos, false
	}
	rawID := binary.BigEndian.Uint32(ilocData[pos:])
	if rawID > math.MaxUint16 {
		return 0, pos, false // item ID exceeds uint16 range; malformed iloc box
	}
	return uint16(rawID), pos + 4, true
}

// readIlocFullExtents reads extentCount extents from ilocData at pos using the
// field sizes in info. Returns the populated extents, updated position, and
// whether all extents were read without truncation.
// ISO 14496-12 §8.11.3.
func readIlocFullExtents(ilocData []byte, pos, extentCount int, info ilocBoxInfo) (extents []ilocExtent, newPos int, ok bool) {
	for range extentCount {
		var ext ilocExtent
		if info.indexSize > 0 {
			if pos+info.indexSize > len(ilocData) {
				return extents, pos, false
			}
			ext.index = readUintN(ilocData[pos:], info.indexSize)
			pos += info.indexSize
		}
		if info.offsetSize > 0 {
			if pos+info.offsetSize > len(ilocData) {
				return extents, pos, false
			}
			ext.offset = readUintN(ilocData[pos:], info.offsetSize)
			pos += info.offsetSize
		}
		if info.lengthSize > 0 {
			if pos+info.lengthSize > len(ilocData) {
				return extents, pos, false
			}
			ext.length = readUintN(ilocData[pos:], info.lengthSize)
			pos += info.lengthSize
		}
		extents = append(extents, ext)
	}
	return extents, pos, true
}

// parseIlocFullItem reads one item entry from ilocData at pos given the box
// metadata in info. Returns the parsed item, updated position, and success.
//
// ISO 14496-12 §8.11.3: item_ID width is 2 bytes for version < 2, 4 bytes for
// version 2. construction_method present only for version 1 or 2.
func parseIlocFullItem(ilocData []byte, pos int, info ilocBoxInfo) (ilocFullItem, int, bool) {
	var item ilocFullItem

	// Read item ID (version-specific width).
	id, pos, ok := readIlocItemID(ilocData, pos, info.version)
	if !ok {
		return item, pos, false
	}
	item.id = id

	// Read construction_method (version 1 and 2 only).
	if info.version == 1 || info.version == 2 {
		if pos+2 > len(ilocData) {
			return item, pos, false
		}
		item.constructMethod = binary.BigEndian.Uint16(ilocData[pos:])
		pos += 2
	}

	// Read base_offset.
	if info.baseOffsetSize > 0 {
		if pos+info.baseOffsetSize > len(ilocData) {
			return item, pos, false
		}
		item.baseOffset = readUintN(ilocData[pos:], info.baseOffsetSize)
		pos += info.baseOffsetSize
	}

	// Read extent_count then loop over extents.
	if pos+2 > len(ilocData) {
		return item, pos, false
	}
	extentCount := int(binary.BigEndian.Uint16(ilocData[pos:]))
	pos += 2

	item.extents, pos, _ = readIlocFullExtents(ilocData, pos, extentCount, info)
	return item, pos, true
}

// buildIlocBox serialises the iloc FullBox from info (field sizes) and the
// given items (which may differ from info.items — e.g. with updated extents).
func buildIlocBox(info ilocBoxInfo, items []ilocFullItem) []byte {
	// FullBox: version(1) + flags(3), then field-size nibble-pair bytes.
	// G115: nibble-packed fields; values are 0–8 so byte cast is safe.
	body := []byte{
		info.version, 0, 0, 0,
		byte(info.offsetSize<<4 | info.lengthSize),    //nolint:gosec // G115: nibble-packed field, values are 0–8
		byte(info.baseOffsetSize<<4 | info.indexSize), //nolint:gosec // G115: nibble-packed field, values are 0–8
	}

	if info.version < 2 {
		body = appendUintN(body, 2, uint64(len(items)))
	} else {
		body = appendUintN(body, 4, uint64(len(items)))
	}

	for _, item := range items {
		body = appendIlocItem(body, item, info)
	}

	hdr := make([]byte, 0, 8+len(body))
	hdr = append(hdr, 0, 0, 0, 0, 'i', 'l', 'o', 'c')
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body))) //nolint:gosec // G115: ISOBMFF box size bounded by body length
	return append(hdr, body...)
}

// appendIlocItem serialises one iloc item entry (version-specific ID width,
// optional construction method, base offset, and extent list) onto body and
// returns the extended slice.
//
// ISO 14496-12 §8.11.3: item serialization mirrors the parse layout exactly.
func appendIlocItem(body []byte, item ilocFullItem, info ilocBoxInfo) []byte {
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
	return body
}

// buildMetaBox constructs a meta FullBox by copying all child boxes from
// metaContent except iloc, then appending newIloc.
// versionFlags is the 4-byte FullBox version+flags field.
func buildMetaBox(versionFlags, metaContent, newIloc []byte) []byte {
	var body []byte
	body = append(body, versionFlags...)

	pos := 0
	for pos < len(metaContent) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(metaContent, pos)
		if !ok {
			break
		}
		if typ != "iloc" {
			body = append(body, metaContent[pos:pos+int(size)]...) //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		_ = headerLen
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	body = append(body, newIloc...)

	hdr := make([]byte, 0, 8+len(body))
	hdr = append(hdr, 0, 0, 0, 0, 'm', 'e', 't', 'a')
	binary.BigEndian.PutUint32(hdr, uint32(8+len(body))) //nolint:gosec // G115: ISOBMFF box size bounded by body length
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
		boxEnd := pos + int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
		if pos < metaAbsStart && metaAbsStart < boxEnd {
			// This box wraps the meta box — update its size.
			newSize := int(size) + delta //nolint:gosec // G115: ISOBMFF box size bounded by file size
			if newSize > 0 {
				binary.BigEndian.PutUint32(data[pos:], uint32(newSize)) //nolint:gosec // G115: newSize > 0 checked above
			}
			break
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
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
// When multiple items of the same type exist, the primary item (per 'pitm' box)
// is preferred; otherwise the item with the lowest ID is selected.
// ISO 23008-12 §6.2 (primary item), §6.6.1 (EXIF item layout).
func parseHEIFMetadata(data []byte) (rawEXIF, rawXMP []byte, err error) {
	// Find the 'meta' box. It can be inside 'moov' or at top-level.
	metaData, err := findBox(data, "meta", 0)
	if err != nil {
		return nil, nil, err
	}
	if metaData == nil {
		return nil, nil, nil
	}

	// Parse item info (iinf) to map item IDs to their type.
	// Parse item locations (iloc) to find the data offset+size for each item.
	itemTypes := parseIinf(metaData)
	itemLocs := parseIloc(metaData)

	// Determine primary item ID from pitm box; 0 means none found.
	primaryID := parsePitm(metaData)

	bestEXIFID, exifFound := selectBestItem(itemTypes, primaryID, "Exif")
	bestXMPID, xmpFound := selectBestItem(itemTypes, primaryID, "mime", "rdf+xml")

	if exifFound {
		if loc, ok := itemLocs[bestEXIFID]; ok && loc.offset+loc.length <= uint64(len(data)) {
			itemData := data[loc.offset : loc.offset+loc.length]
			rawEXIF = extractExifFromData(itemData)
		}
	}
	if xmpFound {
		if loc, ok := itemLocs[bestXMPID]; ok && loc.offset+loc.length <= uint64(len(data)) {
			rawXMP = data[loc.offset : loc.offset+loc.length]
		}
	}

	return rawEXIF, rawXMP, nil
}

// selectBestItem selects the best item ID from itemTypes matching one of
// targetTypes. The primary item is preferred; among non-primary items the
// lowest ID is chosen for determinism.
// ISO 23008-12 §6.2 (primary item selection).
func selectBestItem(itemTypes map[uint16]string, primaryID uint16, targetTypes ...string) (bestID uint16, found bool) {
	for id, typ := range itemTypes {
		if !slices.Contains(targetTypes, typ) {
			continue
		}
		if !found || id == primaryID || (bestID != primaryID && id < bestID) {
			bestID = id
			found = true
		}
	}
	return bestID, found
}

// extractExifFromData reads an HEIF EXIF item payload, validates its length,
// computes the skip offset, and returns the trimmed EXIF bytes.
// Returns nil if the data is too short or the skip offset is out of range.
//
// ISO 23008-12 §6.6.1: EXIF item begins with a 4-byte offset to the TIFF
// header within the item. Skip = value + 4 (to account for the prefix itself).
func extractExifFromData(data []byte) []byte {
	if len(data) < 4 {
		return nil
	}
	skip := int(binary.BigEndian.Uint32(data[:4])) + 4
	if skip > len(data) {
		return nil
	}
	return data[skip:]
}

// parsePitm parses the 'pitm' FullBox from metaContent and returns the primary
// item ID. Returns 0 if the box is absent or cannot be parsed.
// ISO 14496-12 §8.11.4 (pitm box).
func parsePitm(metaContent []byte) uint16 {
	pitm := findInnerBox(metaContent, "pitm")
	if pitm == nil {
		return 0
	}
	// FullBox: version(1) + flags(3); then item_ID.
	// version 0: item_ID is uint16; version 1: item_ID is uint32.
	if len(pitm) < 1 {
		return 0
	}
	version := pitm[0]
	pos := 4 // skip version + flags
	if version == 0 {
		if pos+2 > len(pitm) {
			return 0
		}
		return binary.BigEndian.Uint16(pitm[pos:])
	}
	// version 1
	if pos+4 > len(pitm) {
		return 0
	}
	id := binary.BigEndian.Uint32(pitm[pos:])
	if id > 0xFFFF {
		return 0
	}
	return uint16(id)
}

// itemLoc holds the resolved location of an ISOBMFF item.
type itemLoc struct {
	offset uint64
	length uint64
}

// isContainerBox reports whether typ is an ISOBMFF container box that may
// contain nested boxes (and therefore warrants recursive descent).
func isContainerBox(typ string) bool {
	return typ == "moov" || typ == "trak" || typ == "udta"
}

// findBox searches data for the first box of the given type, returning its data.
// It performs a shallow search at the top level and recurses into container boxes
// up to depth levels deep (max 32) to prevent stack exhaustion on crafted input.
func findBox(data []byte, boxType string, depth int) ([]byte, error) {
	if depth > 32 {
		return nil, ErrMaxNestingDepth
	}
	pos := 0
	for pos < len(data) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}

		boxData := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size

		if typ == boxType {
			// Skip the 4-byte version/flags for full boxes.
			if len(boxData) >= 4 {
				return boxData[4:], nil
			}
			return boxData, nil
		}

		// Recurse into container boxes.
		if isContainerBox(typ) {
			inner, err := findBox(boxData, boxType, depth+1)
			if err == nil && inner != nil {
				return inner, nil
			}
		}

		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil, nil
}

// parseIinfItemCount decodes the item_count field from iinfData (or ilocData)
// at pos, using version to determine whether it is a 2-byte or 4-byte field.
// Returns count, updated pos, and success.
//
// ISO 14496-12 §8.11.3 / §8.11.6: version < 2 uses uint16, version 2 uses uint32.
func parseIinfItemCount(data []byte, version byte, pos int) (count, newPos int, ok bool) {
	if version < 2 {
		if pos+2 > len(data) {
			return 0, pos, false
		}
		return int(binary.BigEndian.Uint16(data[pos:])), pos + 2, true
	}
	if pos+4 > len(data) {
		return 0, pos, false
	}
	return int(binary.BigEndian.Uint32(data[pos:])), pos + 4, true
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

	itemCount, pos, ok := parseIinfItemCount(iinfData, version, pos)
	if !ok {
		return result
	}
	_ = itemCount

	// Parse infe (item info entry) boxes.
	for pos < len(iinfData) {
		size, typ, _, ok := parseHEIFBoxHeader(iinfData, pos)
		if !ok || size < 8 {
			break
		}
		if typ == "infe" {
			infeData := iinfData[pos+8 : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
			id, itemType := parseInfe(infeData)
			if itemType != "" {
				result[id] = itemType
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return result
}

// parseInfe parses an 'infe' box body and returns (item_ID, item_type).
// Handles infe versions 0, 1, 2, and 3 per ISO 14496-12 §8.11.6.
func parseInfe(data []byte) (uint16, string) {
	if len(data) < 4 {
		return 0, ""
	}
	version := data[0]
	// Skip version + flags (4 bytes total).
	pos := 4

	switch version {
	case 0, 1:
		return parseInfeV0V1(data, pos)
	case 2:
		return parseInfeV2V3(data, pos, 2)
	case 3:
		return parseInfeV2V3(data, pos, 3)
	}
	return 0, ""
}

// parseInfeV0V1 handles infe version 0 and 1 parsing.
// infe v0/v1: item_ID(2) + item_protection_index(2) + item_name(NUL-term)
// + content_type(NUL-term) [+ content_encoding(NUL-term) for v1].
// The item type is derived from the content_type MIME string.
// ISO 14496-12 §8.11.6.
func parseInfeV0V1(data []byte, pos int) (uint16, string) {
	if pos+2 > len(data) {
		return 0, ""
	}
	id := binary.BigEndian.Uint16(data[pos:])
	pos += 2
	pos += 2 // item_protection_index
	// Skip item_name (NUL-terminated string).
	nul := indexByte(data[pos:], 0x00)
	if nul < 0 {
		return id, ""
	}
	pos += nul + 1
	// Read content_type (NUL-terminated MIME string).
	nul2 := indexByte(data[pos:], 0x00)
	var contentType string
	if nul2 >= 0 {
		contentType = string(data[pos : pos+nul2])
	} else {
		contentType = string(data[pos:])
	}
	// Map content type to internal type string.
	// Exif in v0/v1 has no formal item_type; the item_content_type is typically
	// empty or "image/jpeg". We cannot reliably identify Exif items in v0/v1.
	if contentType == "application/rdf+xml" {
		return id, "mime"
	}
	return id, ""
}

// parseInfeV2V3 handles infe version 2 and 3 parsing.
// v2: item_ID is uint16; v3: item_ID is uint32 (must fit uint16).
// Both versions include: item_protection_index(2) + item_type(4).
// ISO 14496-12 §8.11.6.
func parseInfeV2V3(data []byte, pos int, version byte) (uint16, string) {
	var id uint16
	if version == 2 {
		if pos+2 > len(data) {
			return 0, ""
		}
		id = binary.BigEndian.Uint16(data[pos:])
		pos += 2
	} else {
		// version 3: item_ID is uint32
		if pos+4 > len(data) {
			return 0, ""
		}
		rawID := binary.BigEndian.Uint32(data[pos:])
		if rawID > math.MaxUint16 {
			return 0, "" // item ID exceeds uint16 range; malformed infe box
		}
		id = uint16(rawID)
		pos += 4
	}
	pos += 2 // item_protection_index
	if pos+4 > len(data) {
		return 0, ""
	}
	itemType := string(data[pos : pos+4])
	return id, itemType
}

// indexByte returns the index of the first occurrence of b in s, or -1.
func indexByte(s []byte, b byte) int {
	for i, v := range s {
		if v == b {
			return i
		}
	}
	return -1
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

	itemCount, pos, ok := parseIinfItemCount(ilocData, version, pos)
	if !ok {
		return result
	}

	for range itemCount {
		id, loc, newPos, itemOK := parseIlocItemSimple(ilocData, pos, version, offsetSize, lengthSize, baseOffsetSize, indexSize)
		if !itemOK {
			break
		}
		pos = newPos
		result[id] = loc
	}
	return result
}

// readIlocSimpleExtents reads extentCount extents from the simple iloc parser,
// resolving each extent's offset against baseOffset. Returns the last observed
// offset and length (matching the original accumulator behaviour), the updated
// position, and whether parsing succeeded without truncation.
//
// ISO 14496-12 §8.11.3: extents accumulate; only the last extent's values are
// retained here (matching the original implementation's behaviour).
func readIlocSimpleExtents(ilocData []byte, pos, extentCount int, baseOffset uint64, offsetSize, lengthSize, indexSize int) (offset, length uint64, newPos int, ok bool) {
	for range extentCount {
		if indexSize > 0 {
			pos += indexSize
		}
		if offsetSize > 0 {
			if pos+offsetSize > len(ilocData) {
				return offset, length, pos, false
			}
			rawOff := readUintN(ilocData[pos:], offsetSize)
			// Guard against integer overflow in offset + baseOffset.
			if rawOff > math.MaxUint64-baseOffset {
				return offset, length, pos, false
			}
			offset = rawOff + baseOffset
			pos += offsetSize
		}
		if lengthSize > 0 {
			if pos+lengthSize > len(ilocData) {
				return offset, length, pos, false
			}
			length = readUintN(ilocData[pos:], lengthSize)
			pos += lengthSize
		}
	}
	return offset, length, pos, true
}

// parseIlocItemSimple reads one item entry from the simple iloc parser (parseIloc).
// It handles version-specific ID width, optional construction method, base offset,
// extent count, and the extent loop — returning the resolved offset and length.
//
// ISO 14496-12 §8.11.3: extents accumulate; only the last extent's values are
// retained here (matching the original implementation's behaviour).
func parseIlocItemSimple(ilocData []byte, pos int, version uint8, offsetSize, lengthSize, baseOffsetSize, indexSize int) (id uint16, loc itemLoc, newPos int, ok bool) {
	// Read item ID (version-specific width).
	id, pos, ok = readIlocItemID(ilocData, pos, version)
	if !ok {
		return 0, loc, pos, false
	}

	if version == 1 || version == 2 {
		pos += 2 // construction_method
	}

	var baseOffset uint64
	if baseOffsetSize > 0 {
		if pos+baseOffsetSize > len(ilocData) {
			return 0, loc, pos, false
		}
		baseOffset = readUintN(ilocData[pos:], baseOffsetSize)
		pos += baseOffsetSize
	}

	if pos+2 > len(ilocData) {
		return 0, loc, pos, false
	}
	extentCount := int(binary.BigEndian.Uint16(ilocData[pos:]))
	pos += 2

	var offset, length uint64
	offset, length, pos, _ = readIlocSimpleExtents(ilocData, pos, extentCount, baseOffset, offsetSize, lengthSize, indexSize)
	return id, itemLoc{offset: offset, length: length}, pos, true
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
	for pos < len(data) {
		size, typ, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		if typ == boxType {
			return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// findInnerBox searches for a box of the given type within data (flat scan).
func findInnerBox(data []byte, boxType string) []byte {
	pos := 0
	for pos < len(data) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		// A box whose size is smaller than its header is malformed; stop.
		if size < headerLen {
			break
		}
		if typ == boxType {
			boxData := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
			return boxData
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
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

// ---------------------------------------------------------------------------
// Inject helpers
// ---------------------------------------------------------------------------

// mapPendingItems maps item IDs to their new payloads based on itemTypes.
// EXIF items get a 4-byte zero prefix prepended (ISO 23008-12 §6.6.1: the
// EXIF item begins with a 4-byte offset to the TIFF header; value 0 means
// immediate). XMP items are stored verbatim.
func mapPendingItems(itemTypes map[uint16]string, rawEXIF, rawXMP []byte) map[uint16][]byte {
	pendingByID := make(map[uint16][]byte)
	for id, typ := range itemTypes {
		switch typ {
		case "Exif":
			if rawEXIF != nil {
				prefix := [4]byte{}
				pendingByID[id] = append(prefix[:], rawEXIF...)
			}
		case "mime", "rdf+xml":
			if rawXMP != nil {
				pendingByID[id] = rawXMP
			}
		}
	}
	return pendingByID
}

// updateIlocItems returns a copy of items with each item that appears in
// pendingByID having its base offset zeroed and its extents collapsed to a
// single placeholder extent (offset=0, length=0). Real values are assigned
// later by assignItemOffsets once the output layout is known.
func updateIlocItems(items []ilocFullItem, pendingByID map[uint16][]byte) []ilocFullItem {
	updated := make([]ilocFullItem, len(items))
	copy(updated, items)
	for i, item := range updated {
		if _, ok := pendingByID[item.id]; ok {
			updated[i].baseOffset = 0
			updated[i].extents = []ilocExtent{{offset: 0, length: 0}}
		}
	}
	return updated
}

// assignItemOffsets walks updatedItems, finds each item in pendingByID, and
// assigns its real file offset by accumulating from newItemBaseOffset.
// Returns the slice with offsets and lengths filled in.
func assignItemOffsets(updatedItems []ilocFullItem, pendingByID map[uint16][]byte, newItemBaseOffset uint64) []ilocFullItem {
	var accumOffset uint64
	for i, item := range updatedItems {
		if payload, ok := pendingByID[item.id]; ok {
			updatedItems[i].extents[0].offset = newItemBaseOffset + accumOffset
			updatedItems[i].extents[0].length = uint64(len(payload))
			accumOffset += uint64(len(payload))
		}
	}
	return updatedItems
}

// writeHEIFOutput writes the four output sections to w:
// prefix (patched ancestor sizes), meta box, suffix, then new item payloads
// in item order.
func writeHEIFOutput(w io.Writer, prefix, meta, suffix []byte, updatedItems []ilocFullItem, pendingByID map[uint16][]byte) error {
	if _, err := w.Write(prefix); err != nil {
		return fmt.Errorf("heif: write prefix: %w", err)
	}
	if _, err := w.Write(meta); err != nil {
		return fmt.Errorf("heif: write meta: %w", err)
	}
	if _, err := w.Write(suffix); err != nil {
		return fmt.Errorf("heif: write suffix: %w", err)
	}
	for _, item := range updatedItems {
		if payload, ok := pendingByID[item.id]; ok {
			if _, err := w.Write(payload); err != nil {
				return fmt.Errorf("heif: write item payload: %w", err)
			}
		}
	}
	return nil
}
