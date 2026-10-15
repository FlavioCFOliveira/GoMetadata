package heif

// Adapted from libavif src/read.c:avifParseItemLocationBox,avifDecoderItemRead, Copyright 2019 Joe Drago, SPDX-License-Identifier: BSD-2-Clause
//
// Changes from libavif: the item location box is decoded into allocation-free
// views (ilocHeader, ilocItemHeader) for the read path, so that only the items
// the caller asks for are resolved and the extents of every other item are
// skipped arithmetically; the per-item extent list is capped at
// maxIlocExtentsPerItem; data_reference_index values other than 0 are resolved
// against the meta box's dref box instead of being ignored; construction
// method 2 (item offset) is skipped instead of failing the whole parse; an
// idat-stored item (construction method 1) is read from the idat box payload
// by sub-slicing the meta box bytes already in memory, without any I/O.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// ISO/IEC 14496-12 §8.11.3.2 (ItemLocationBox syntax), as decoded by libavif
// avifParseItemLocationBox:
//
//	aligned(8) class ItemLocationBox extends FullBox('iloc', version, 0) {
//	  unsigned int(4) offset_size;
//	  unsigned int(4) length_size;
//	  unsigned int(4) base_offset_size;
//	  if ((version == 1) || (version == 2)) unsigned int(4) index_size;
//	  else unsigned int(4) reserved;
//	  if (version < 2) unsigned int(16) item_count;
//	  else if (version == 2) unsigned int(32) item_count;
//	  for (i=0; i<item_count; i++) {
//	    if (version < 2) unsigned int(16) item_ID;
//	    else if (version == 2) unsigned int(32) item_ID;
//	    if ((version == 1) || (version == 2)) {
//	      unsigned int(12) reserved = 0;
//	      unsigned int(4) construction_method;
//	    }
//	    unsigned int(16) data_reference_index;
//	    unsigned int(base_offset_size*8) base_offset;
//	    unsigned int(16) extent_count;
//	    for (j=0; j<extent_count; j++) {
//	      if (((version == 1) || (version == 2)) && (index_size > 0))
//	        unsigned int(index_size*8) item_reference_index;
//	      unsigned int(offset_size*8) extent_offset;
//	      unsigned int(length_size*8) extent_length;
//	    }
//	  }
//	}
//
// data_reference_index is present for EVERY version (task #282): omitting it
// shifts every field after the first item ID by two bytes, which is why the
// former parser recovered 1 of 53 items of an iPhone HEIC.

// construction_method values (ISO/IEC 14496-12 §8.11.3.3).
const (
	constructionFileOffset = 0 // extent offsets are file offsets
	constructionIdatOffset = 1 // extent offsets are relative to the idat box payload
	// construction_method 2 (item offset) is not supported; such items are skipped.
)

// constructionMethodMask selects the 4-bit construction_method from the 16-bit
// field whose upper 12 bits are reserved (ISO/IEC 14496-12 §8.11.3.2).
const constructionMethodMask = 0x000F

// ilocFixedHeaderSize is the byte length of the iloc FullBox's fixed header,
// after the 8-byte box header: version(1)+flags(3)+offset/length-size
// nibbles(1)+base_offset/index-size nibbles(1) = 6 bytes (ISO 14496-12 §8.11.3).
const ilocFixedHeaderSize = 6

// maxIlocExtentsPerItem is the upper bound on the number of extents we will
// allocate for a single iloc item. The HEIF specification does not impose a
// hard limit, but real files have 1-3 extents per item. 1024 is a generous
// ceiling; it keeps the per-item allocation at most 1024×24 = 24 KB regardless
// of what an untrusted file claims for extent_count (max uint16 = 65535).
//
// Without this cap, a crafted file with extentCount=65535 per item causes
// ~1.5 MB of slice-header allocation per item — a denial-of-service via
// memory amplification.
const maxIlocExtentsPerItem = 1024

// maxIlocItems caps the number of items processed from a single iloc box.
// Real-world HEIF/AVIF files carry from one to a few hundred items (image
// variants, thumbnails, alpha planes, EXIF/XMP metadata items). 4096 is a
// generous ceiling that mirrors the sanity cap format/detect.go's
// parseBigTIFFIFD0 applies to BigTIFF IFD entry counts: it defends against an
// attacker-controlled item_count field (up to 2^32-1 for iloc version 2)
// driving unbounded outer-loop work, without affecting any legitimate file.
// ISO 14496-12 §8.11.3.
const maxIlocItems = 4096

// maxItemPayloadSize is the upper bound on a single HEIF item payload that this
// library will allocate. Real EXIF and XMP payloads are orders of magnitude
// smaller; a value exceeding this indicates a malformed or adversarial file.
// ISO 23008-12 §6.6: item payloads are compact metadata structures, not media.
const maxItemPayloadSize = 256 << 20 // 256 MB

var (
	errItemPayloadTooLarge  = errors.New("heif: item payload exceeds size limit")
	errItemOffsetOverflow   = errors.New("heif: item location offset overflows int64")
	errIdatExtentOutOfRange = errors.New("heif: item extent lies outside the idat box")
)

// ilocHeader is the decoded fixed header of an iloc box body (the bytes after
// the 8-byte box header). It is a plain value: decoding it never allocates.
type ilocHeader struct {
	version        uint8
	offsetSize     int // 0, 4, or 8 (ISO 14496-12 §8.11.3.3)
	lengthSize     int // 0, 4, or 8
	baseOffsetSize int // 0, 4, or 8
	indexSize      int // 0, 4, or 8 (iloc version 1 and 2 only)
	itemCount      int
	itemsPos       int // offset of the first item entry within the iloc body
}

// extentSize returns the byte length of one extent entry.
func (h *ilocHeader) extentSize() int {
	return h.indexSize + h.offsetSize + h.lengthSize
}

// hasConstructionMethod reports whether item entries carry the 16-bit
// reserved/construction_method field (iloc version 1 and 2).
func (h *ilocHeader) hasConstructionMethod() bool {
	return h.version == 1 || h.version == 2
}

// validIlocFieldSize reports whether n is a legal iloc field width.
// ISO 14496-12 §8.11.3.3: offset_size, length_size, base_offset_size and
// index_size are each "taken from the set {0, 4, 8}". libavif
// avifParseItemLocationBox rejects any other value; so does this parser,
// because a width such as 15 cannot be decoded into a uint64.
func validIlocFieldSize(n int) bool {
	return n == 0 || n == 4 || n == 8
}

// parseIlocHeader decodes the fixed header of the iloc box body ilocData.
// ok is false when the body is truncated, the version is unknown (> 2), a
// field width is illegal, or item_count exceeds maxIlocItems.
func parseIlocHeader(ilocData []byte) (h ilocHeader, ok bool) {
	// HEIF-ILOC-OFFBYONE-01 fix (task #243): the fixed iloc header spans two
	// size-nibble bytes — offset_size|length_size at index 4 and
	// base_offset_size|index_size at index 5 — so both reads require
	// len(ilocData) >= 6.
	if len(ilocData) < ilocFixedHeaderSize {
		return h, false
	}
	h.version = ilocData[0]
	if h.version > 2 {
		return h, false // ISO 14496-12 §8.11.3.2 defines versions 0, 1 and 2 only
	}
	h.offsetSize = int(ilocData[4] >> 4)
	h.lengthSize = int(ilocData[4] & 0x0F)
	h.baseOffsetSize = int(ilocData[5] >> 4)
	if h.hasConstructionMethod() {
		h.indexSize = int(ilocData[5] & 0x0F)
	}
	if !validIlocFieldSize(h.offsetSize) || !validIlocFieldSize(h.lengthSize) ||
		!validIlocFieldSize(h.baseOffsetSize) || !validIlocFieldSize(h.indexSize) {
		return h, false
	}
	count, pos, countOK := parseIinfItemCount(ilocData, h.version, ilocFixedHeaderSize)
	// Security audit FIX 1 (HEIF-ILOC-EXTENT-AMPLIFICATION): reject an iloc box
	// whose declared item_count exceeds a sane ceiling before doing any
	// per-item work. iloc version 2 encodes item_count as a 4-byte field (up to
	// 2^32-1). See maxIlocItems for the rationale.
	if !countOK || count > maxIlocItems {
		return h, false
	}
	h.itemCount = count
	h.itemsPos = pos
	return h, true
}

// ilocItemHeader is the decoded fixed part of one iloc item entry: every
// field before its extent list.
type ilocItemHeader struct {
	id uint16
	// constructMethod is the raw 16-bit field of iloc version 1 and 2: 12
	// reserved bits and the 4-bit construction_method. It is 0 for version 0.
	constructMethod uint16
	dataRefIdx      uint16
	baseOffset      uint64
	extentCount     int
}

// method returns the 4-bit construction_method of the item.
func (it *ilocItemHeader) method() uint16 {
	return it.constructMethod & constructionMethodMask
}

// parseIlocItemHeader decodes the fixed fields of the item entry at pos and
// returns them with the position of the item's first extent.
// ISO 14496-12 §8.11.3.2; field order as in libavif avifParseItemLocationBox.
func parseIlocItemHeader(ilocData []byte, pos int, h *ilocHeader) (it ilocItemHeader, extPos int, ok bool) {
	it.id, pos, ok = readIlocItemID(ilocData, pos, h.version)
	if !ok {
		return it, pos, false
	}
	if h.hasConstructionMethod() {
		if pos+2 > len(ilocData) {
			return it, pos, false
		}
		it.constructMethod = binary.BigEndian.Uint16(ilocData[pos:])
		pos += 2
	}
	// data_reference_index: unsigned int(16), present for every version.
	if pos+2 > len(ilocData) {
		return it, pos, false
	}
	it.dataRefIdx = binary.BigEndian.Uint16(ilocData[pos:])
	pos += 2
	if h.baseOffsetSize > 0 {
		if pos+h.baseOffsetSize > len(ilocData) {
			return it, pos, false
		}
		it.baseOffset = readUintN(ilocData[pos:], h.baseOffsetSize)
		pos += h.baseOffsetSize
	}
	if pos+2 > len(ilocData) {
		return it, pos, false
	}
	it.extentCount = int(binary.BigEndian.Uint16(ilocData[pos:]))
	return it, pos + 2, true
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

// findIlocItem returns the item entry whose item_ID is id, together with the
// position of its first extent. The extent lists of the items before it are
// skipped by arithmetic (extent_count × extent size), so the lookup costs one
// header decode per preceding item, allocates nothing, and is immune to the
// zero-field-width extent amplification of security audit FIX 1 (a zero
// extent size skips zero bytes in O(1)).
func findIlocItem(ilocData []byte, h *ilocHeader, id uint16) (it ilocItemHeader, extPos int, found bool) {
	pos := h.itemsPos
	extSize := h.extentSize()
	for range h.itemCount {
		var ok bool
		it, extPos, ok = parseIlocItemHeader(ilocData, pos, h)
		if !ok {
			return it, 0, false
		}
		// extentCount ≤ 65535 and extSize ≤ 24, so the product cannot overflow.
		end := extPos + it.extentCount*extSize
		if end > len(ilocData) {
			return it, 0, false // truncated extent list
		}
		if it.id == id {
			return it, extPos, true
		}
		pos = end
	}
	return it, 0, false
}

// decodeIlocExtent decodes the extent entry at pos. The caller guarantees that
// pos+h.extentSize() <= len(ilocData).
func decodeIlocExtent(ilocData []byte, pos int, h *ilocHeader) ilocExtent {
	var ext ilocExtent
	if h.indexSize > 0 {
		ext.index = readUintN(ilocData[pos:], h.indexSize)
		pos += h.indexSize
	}
	if h.offsetSize > 0 {
		ext.offset = readUintN(ilocData[pos:], h.offsetSize)
		pos += h.offsetSize
	}
	if h.lengthSize > 0 {
		ext.length = readUintN(ilocData[pos:], h.lengthSize)
	}
	return ext
}

// itemSource gives access to the bytes an iloc item can reference.
type itemSource struct {
	r           io.ReadSeeker // the whole file (construction_method 0)
	metaContent []byte        // meta box content, for dref and idat lookups
}

// idat returns the payload of the meta box's ItemDataBox, or nil when the
// meta box has none. ISO/IEC 14496-12 §8.11.11: the idat box "contains the
// data of metadata items that use the construction method indicating that an
// item's data extents are stored within this box".
func (src itemSource) idat() []byte {
	return findInnerBox(src.metaContent, boxTypeIdat)
}

// readIlocItem returns the payload of item it, the concatenation of all its
// extents in order (ISO 14496-12 §8.11.3.1: "the data of the item is the
// concatenation of its extents"), as libavif avifDecoderItemRead merges them.
// The payload is allocated once at its exact size.
//
// ISO/IEC 14496-12 §8.11.3.3: construction_method 0 (file offset) resolves
// base_offset+extent_offset as an absolute file position; construction_method
// 1 (idat offset) resolves it relative to the first byte of the idat box
// payload of the same meta box (libavif avifDecoderItemRead: idatBuffer).
//
// Items that this reader cannot resolve return (nil, nil): construction
// method 2 (item offset), a data_reference_index that designates another
// file, an idat-stored item when the meta box has no idat box, an item with
// no extents or a zero total length, and an extent count above
// maxIlocExtentsPerItem.
func readIlocItem(src itemSource, ilocData []byte, h *ilocHeader, it ilocItemHeader, extPos int) ([]byte, error) {
	idat, ok := itemStorage(src, it)
	if !ok || it.extentCount == 0 || it.extentCount > maxIlocExtentsPerItem {
		return nil, nil
	}
	total, err := itemLength(ilocData, h, it, extPos)
	if err != nil || total == 0 {
		return nil, err
	}
	payload := make([]byte, total)
	extSize := h.extentSize()
	var filled uint64
	for i := range it.extentCount {
		ext := decodeIlocExtent(ilocData, extPos+i*extSize, h)
		if ext.length == 0 {
			continue
		}
		if err := readExtent(src.r, idat, it.baseOffset+ext.offset, payload[filled:filled+ext.length]); err != nil {
			return nil, err
		}
		filled += ext.length
	}
	return payload, nil
}

// itemStorage resolves where the extents of item it are stored: the file
// (construction_method 0, idat nil) or the meta box's idat payload
// (construction_method 1). ok is false for an item this reader cannot
// resolve: construction_method 2, a data_reference_index that designates
// another file, or an idat-stored item without an idat box.
func itemStorage(src itemSource, it ilocItemHeader) (idat []byte, ok bool) {
	switch it.method() {
	case constructionFileOffset:
		return nil, itemDataInThisFile(src.metaContent, it.dataRefIdx)
	case constructionIdatOffset:
		// The idat box is part of the meta box itself: data_reference_index
		// does not apply to construction method 1.
		idat = src.idat()
		return idat, idat != nil
	default:
		return nil, false
	}
}

// itemLength validates every extent of item it and returns their total
// length, capped at maxItemPayloadSize.
func itemLength(ilocData []byte, h *ilocHeader, it ilocItemHeader, extPos int) (uint64, error) {
	extSize := h.extentSize()
	var total uint64
	for i := range it.extentCount {
		ext := decodeIlocExtent(ilocData, extPos+i*extSize, h)
		if ext.offset > math.MaxUint64-it.baseOffset {
			return 0, errItemOffsetOverflow
		}
		total += ext.length
		if total > maxItemPayloadSize {
			return 0, errItemPayloadTooLarge
		}
	}
	return total, nil
}

// readExtent fills dst with the extent at off: from idat when it is non-nil
// (construction_method 1), otherwise from the file.
func readExtent(r io.ReadSeeker, idat []byte, off uint64, dst []byte) error {
	if idat == nil {
		return readFileExtent(r, off, dst)
	}
	// Bounds check in uint64 before any int conversion (32-bit safe).
	n := uint64(len(dst))
	if off > uint64(len(idat)) || n > uint64(len(idat))-off {
		return errIdatExtentOutOfRange
	}
	copy(dst, idat[off:off+n])
	return nil
}

// readFileExtent fills dst with the bytes at absolute file offset off
// (construction_method 0).
func readFileExtent(r io.ReadSeeker, off uint64, dst []byte) error {
	// Validate that offset fits int64 to prevent sign-wrapping on conversion.
	if off > math.MaxInt64 {
		return errItemOffsetOverflow
	}
	if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
		return fmt.Errorf("heif: seek to item: %w", err)
	}
	if _, err := io.ReadFull(r, dst); err != nil {
		return fmt.Errorf("heif: read item payload: %w", err)
	}
	return nil
}

// itemDataInThisFile reports whether the data_reference_index idx of an iloc
// item designates the file being parsed.
//
// ISO 14496-12 §8.11.3.3: "data_reference_index is either zero ('this file')
// or an index, with value 1 indicating the first entry, into the data
// references in the Data Information Box". §8.7.2.3: a DataEntryUrlBox or
// DataEntryUrnBox whose flags have bit 0 set (self-contained) designates the
// same file. Any other entry designates external data, which this package
// never reads or writes.
func itemDataInThisFile(metaContent []byte, idx uint16) bool {
	if idx == 0 {
		return true
	}
	dref := findInnerBox(findInnerBox(metaContent, boxTypeDinf), boxTypeDref)
	// dref FullBox: version(1)+flags(3)+entry_count(4), then the entry boxes.
	if len(dref) < 8 || uint32(idx) > binary.BigEndian.Uint32(dref[4:]) {
		return false
	}
	pos := 8
	for i := uint16(1); pos < len(dref); i++ {
		size, _, headerLen, ok := parseHEIFBoxHeader(dref, pos)
		if !ok {
			return false
		}
		if i == idx {
			entry := dref[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length
			return len(entry) >= 4 && entry[3]&0x01 != 0
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return false
}

// ---------------------------------------------------------------------------
// Full iloc structure (write path)
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
	id uint16
	// constructMethod is the raw 16-bit reserved/construction_method field
	// (iloc version 1 and 2 only), written back unchanged.
	constructMethod uint16
	dataRefIdx      uint16 // data_reference_index, written back unchanged
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
//
// ok is false when the iloc box is missing, has an unsupported version or an
// illegal field width, declares more items than maxIlocItems, or cannot be
// parsed in full: a truncated item list or extent list, or an item with more
// than maxIlocExtentsPerItem extents. The structure feeds buildIlocBox, which
// re-serialises every item; accepting a partial parse would silently drop the
// items and extents that were not read from the rewritten file, so the write
// path reports the file as unwritable instead (ErrUnreadableItemLocations).
func parseIlocFull(metaContent []byte) (ilocBoxInfo, bool) {
	var info ilocBoxInfo
	ilocData := findInnerBox(metaContent, boxTypeIloc)
	h, ok := parseIlocHeader(ilocData)
	if !ok {
		return info, false
	}
	info.version = h.version
	info.offsetSize = h.offsetSize
	info.lengthSize = h.lengthSize
	info.baseOffsetSize = h.baseOffsetSize
	info.indexSize = h.indexSize

	pos := h.itemsPos
	for range h.itemCount {
		item, updatedPos, itemOK := parseIlocFullItem(ilocData, pos, &h)
		if !itemOK {
			return info, false
		}
		pos = updatedPos
		info.items = append(info.items, item)
	}
	return info, true
}

// parseIlocFullItem reads one item entry, extents included, from ilocData at
// pos. Returns the parsed item, the position after it, and success.
func parseIlocFullItem(ilocData []byte, pos int, h *ilocHeader) (ilocFullItem, int, bool) {
	hdr, extPos, ok := parseIlocItemHeader(ilocData, pos, h)
	if !ok {
		return ilocFullItem{}, pos, false
	}
	item := ilocFullItem{
		id:              hdr.id,
		constructMethod: hdr.constructMethod,
		dataRefIdx:      hdr.dataRefIdx,
		baseOffset:      hdr.baseOffset,
	}
	item.extents, pos, ok = readIlocFullExtents(ilocData, extPos, hdr.extentCount, h)
	return item, pos, ok
}

// readIlocFullExtents reads extentCount extents from ilocData at pos using the
// field sizes in h. Returns the populated extents, the updated position, and
// whether every extent was read: ok is false when the data is truncated before
// all extents are read or when extentCount exceeds maxIlocExtentsPerItem.
//
// Security (CWE-770/834): extentCount is checked against maxIlocExtentsPerItem
// BEFORE any allocation or loop, so an attacker-controlled extent_count of up
// to 65535 cannot force a large allocation or, when every per-extent field
// width is zero (indexSize == offsetSize == lengthSize == 0, so no iteration
// consumes any input byte), 65535 no-op iterations per item (security audit
// FIX 1, HEIF-ILOC-EXTENT-AMPLIFICATION). The former behaviour of dropping the
// surplus extents was a silent data loss on write (the rewritten iloc listed
// fewer extents than the source); an item that large is reported instead.
// ISO 14496-12 §8.11.3.
func readIlocFullExtents(ilocData []byte, pos, extentCount int, h *ilocHeader) (extents []ilocExtent, newPos int, ok bool) {
	if extentCount > maxIlocExtentsPerItem {
		return nil, pos, false
	}
	extents = make([]ilocExtent, 0, extentCount)

	extSize := h.extentSize()
	for range extentCount {
		if pos+extSize > len(ilocData) {
			return extents, pos, false
		}
		extents = append(extents, decodeIlocExtent(ilocData, pos, h))
		pos += extSize
	}
	return extents, pos, true
}

// ilocBoxSize returns the exact total byte length (including the 8-byte box
// header) that buildIlocBox would produce for items under info. It depends
// only on field widths and per-item extent counts — never on the actual
// offset/length VALUES stored — so it can be computed with no bytes written
// at all (task #229; used to size the meta box before the final iloc bytes
// exist).
func ilocBoxSize(info ilocBoxInfo, items []ilocFullItem) int {
	itemCountWidth := 2
	if info.version >= 2 {
		itemCountWidth = 4
	}
	total := 8 + ilocFixedHeaderSize + itemCountWidth
	for _, item := range items {
		total += ilocItemSize(item, info)
	}
	return total
}

// buildIlocBox serialises the iloc FullBox from info (field sizes) and the
// given items into a single pre-sized buffer: the 8-byte box header is written
// directly into the buffer (with the size field patched in once the exact
// total is known), and the body is appended in place (task #229).
func buildIlocBox(info ilocBoxInfo, items []ilocFullItem) []byte {
	total := ilocBoxSize(info, items)
	buf := make([]byte, 8, total)
	buf[4], buf[5], buf[6], buf[7] = 'i', 'l', 'o', 'c'

	// FullBox: version(1) + flags(3), then field-size nibble-pair bytes.
	// G115: nibble-packed fields; values are 0, 4 or 8 so the byte cast is safe.
	buf = append(buf,
		info.version, 0, 0, 0,
		byte(info.offsetSize<<4|info.lengthSize),    //nolint:gosec // G115: nibble-packed field, values are 0, 4 or 8
		byte(info.baseOffsetSize<<4|info.indexSize), //nolint:gosec // G115: nibble-packed field, values are 0, 4 or 8
	)

	if info.version < 2 {
		buf = appendUintN(buf, 2, uint64(len(items)))
	} else {
		buf = appendUintN(buf, 4, uint64(len(items)))
	}

	for _, item := range items {
		buf = appendIlocItem(buf, item, info)
	}

	binary.BigEndian.PutUint32(buf[0:4], uint32(len(buf))) //nolint:gosec // G115: ISOBMFF box size bounded by body length
	return buf
}

// ilocItemSize returns the exact byte length that appendIlocItem would write
// for item given the encoding parameters in info.
//
// ISO 14496-12 §8.11.3.2: mirrors the conditional field layout in appendIlocItem.
func ilocItemSize(item ilocFullItem, info ilocBoxInfo) int {
	size := 2 // item_ID (16-bit)
	if info.version >= 2 {
		size = 4 // item_ID (32-bit)
	}
	if info.version == 1 || info.version == 2 {
		size += 2 // reserved + construction_method
	}
	size += 2                   // data_reference_index (every version)
	size += info.baseOffsetSize // base_offset
	size += 2                   // extent_count (always 16-bit)
	size += len(item.extents) * (info.indexSize + info.offsetSize + info.lengthSize)
	return size
}

// appendIlocItem serialises one iloc item entry (version-specific ID width,
// optional construction method, data reference index, base offset, and
// extent list) onto body and returns the extended slice.
//
// ISO 14496-12 §8.11.3.2: item serialization mirrors the parse layout exactly.
func appendIlocItem(body []byte, item ilocFullItem, info ilocBoxInfo) []byte {
	if info.version < 2 {
		body = appendUintN(body, 2, uint64(item.id))
	} else {
		body = appendUintN(body, 4, uint64(item.id))
	}
	if info.version == 1 || info.version == 2 {
		body = appendUintN(body, 2, uint64(item.constructMethod))
	}
	body = appendUintN(body, 2, uint64(item.dataRefIdx))
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

// appendUintN appends v encoded as an n-byte big-endian integer to b.
// Uses binary.BigEndian.AppendUint* for the standard ISOBMFF field widths
// (1, 2, 4, 8) to avoid a temporary allocation; falls back to a generic
// loop for non-standard widths (unreachable in normal HEIF parsing).
func appendUintN(b []byte, n int, v uint64) []byte {
	switch n {
	case 1:
		return append(b, byte(v)) //nolint:gosec // G115: value range controlled by caller
	case 2:
		return binary.BigEndian.AppendUint16(b, uint16(v)) //nolint:gosec // G115: value range controlled by caller
	case 4:
		return binary.BigEndian.AppendUint32(b, uint32(v)) //nolint:gosec // G115: value range controlled by caller
	case 8:
		return binary.BigEndian.AppendUint64(b, v)
	default:
		// Fallback for non-standard widths (not reached in normal HEIF parsing).
		for i := n - 1; i >= 0; i-- {
			b = append(b, byte(v>>(8*uint(i)))) //nolint:gosec // G115: i is a small non-negative loop index
		}
		return b
	}
}

// readUintN reads an n-byte big-endian unsigned integer from b.
func readUintN(b []byte, n int) uint64 {
	var v uint64
	for i := 0; i < n && i < len(b); i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}
