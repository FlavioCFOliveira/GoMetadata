package heif

// Adapted from libavif src/write.c:avifEncoderDataCreateExifItem,avifEncoderDataCreateXMPItem,avifEncoderFinish,avifEncoderWriteTrackMetaBox, Copyright 2019 Joe Drago, SPDX-License-Identifier: BSD-2-Clause
//
// Changes from libavif: libavif writes a new file, whereas Inject edits an
// existing one. A missing Exif or XMP item is created the way libavif creates
// it (infe version 2 with item_type 'Exif', or 'mime' with content_type
// "application/rdf+xml"; an iref 'cdsc' reference from the new item to the
// primary item; one iloc extent with construction_method 0 and
// data_reference_index 0), but the new infe, iref and iloc entries are
// appended to the file's own iinf, iref and iloc boxes, keeping their
// versions and field widths. The payloads are stored in a new 'mdat' box at
// the end of the file, and every file offset that the growth of the meta box
// moves (iloc file-offset extents and stco/co64 chunk offsets) is shifted by
// the same amount. libavif writes every iloc box with offset_size and
// length_size 4; Inject keeps the field widths of the existing iloc box but
// widens a width of 0 to 4, so that the new item locations fit (task #335).

import (
	"encoding/binary"
	"errors"
	"math"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/internal/bmff"
)

// ISOBMFF box types used only by the write path.
var (
	boxTypeIref = [4]byte{'i', 'r', 'e', 'f'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeMdia = [4]byte{'m', 'd', 'i', 'a'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeMinf = [4]byte{'m', 'i', 'n', 'f'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeStbl = [4]byte{'s', 't', 'b', 'l'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeStco = [4]byte{'s', 't', 'c', 'o'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeCo64 = [4]byte{'c', 'o', '6', '4'} //nolint:gochecknoglobals // package-level constant box type; never mutated
)

// Item names written into new infe boxes, as libavif names them
// (avifEncoderDataCreateExifItem / avifEncoderDataCreateXMPItem). The
// trailing NUL terminates the utf8string (ISO 14496-12 §8.11.6.2).
const (
	exifItemName = "Exif\x00"
	xmpItemName  = "XMP\x00"
)

var (
	// errIlocFieldOverflow is returned when a relocated item offset or
	// length does not fit the iloc field width declared by the source file.
	errIlocFieldOverflow = errors.New("heif: item location does not fit the iloc field width")
	// errPayloadTooLarge is returned when the new metadata payloads do not
	// fit a media data box with a 32-bit size field.
	errPayloadTooLarge = errors.New("heif: metadata payloads exceed the media data box size limit")
	// errNoFreeItemID is returned when a new item is needed but every 16-bit
	// item ID is already in use.
	errNoFreeItemID = errors.New("heif: no free item ID for a new metadata item")
	// errItemTableFull is returned when the item information or reference
	// box cannot hold one more entry.
	errItemTableFull = errors.New("heif: the item table cannot hold a new metadata item")
	// errChunkOffsetOverflow is returned when a shifted stco offset does not
	// fit 32 bits.
	errChunkOffsetOverflow = errors.New("heif: shifted chunk offset does not fit the chunk offset box")
	// errMetaLayout reports an internal inconsistency between the computed
	// and the serialised meta box length; the output is never written.
	errMetaLayout = errors.New("heif: internal meta box layout mismatch")
)

// pendingPayload is the new payload of an Exif or XMP item.
type pendingPayload struct {
	data []byte
	// exif marks an Exif item: its payload is a 4-byte big-endian TIFF
	// header offset of 0 followed by data (ISO 23008-12 Annex A.2.1,
	// ExifDataBlock: exif_tiff_header_offset; data starts with the TIFF
	// header).
	exif bool
}

// size returns the byte length of the item payload.
func (p pendingPayload) size() uint64 {
	if p.exif {
		return uint64(len(p.data)) + 4
	}
	return uint64(len(p.data))
}

// appendTo appends the item payload to dst.
func (p pendingPayload) appendTo(dst []byte) []byte {
	if p.exif {
		dst = append(dst, 0, 0, 0, 0)
	}
	return append(dst, p.data...)
}

// injectPlan is the item-level plan of an Inject call: the rebuilt iloc
// structure (offsets still unassigned), the payload of every item to write,
// the parts of the rebuilt meta box, and its exact final length.
type injectPlan struct {
	ilocInfo    ilocBoxInfo
	pendingByID map[uint16]pendingPayload
	parts       [][]byte // meta box children; parts[ilocIdx] is the iloc placeholder
	ilocIdx     int
	metaLen     int
}

// planInject builds the injectPlan for metaContent, whose box header is
// metaHeaderLen bytes long. It returns ErrUnreadableItemLocations when the
// iloc box is missing or cannot be rewritten without dropping items.
//
// An iloc offset_size or length_size of 0 is legal (ISO 14496-12 §8.11.3.3:
// the sizes are taken from {0, 4, 8}; 0 means the field is absent and its
// value is 0) but leaves no field to hold the new payload location. The iloc
// box is rebuilt anyway, so the zero widths are widened to 4 bytes
// (widenIlocFields) instead of declining to write (task #335; libavif
// src/write.c lines 936-967, avifEncoderWriteTrackMetaBox, and 3582-3584,
// avifEncoderFinish, write offset_size = length_size = 4).
func planInject(metaContent []byte, metaHeaderLen int, rawEXIF, rawXMP []byte) (plan injectPlan, err error) {
	itemTypes := parseIinf(metaContent)
	info, ilocOK := parseIlocFull(metaContent)
	if !ilocOK {
		return plan, ErrUnreadableItemLocations
	}
	widenIlocFields(&info)
	pendingByID, hasEXIF, hasXMP := mapPendingItems(itemTypes, rawEXIF, rawXMP)
	newItems, err := planNewItems(metaContent, info.items, rawEXIF != nil && !hasEXIF, rawXMP != nil && !hasXMP, rawEXIF, rawXMP, pendingByID)
	if err != nil {
		return plan, err
	}
	if len(pendingByID) == 0 {
		// Unreachable: a non-nil payload either maps to an existing item or
		// creates one. Reported rather than passed through, so that a future
		// change cannot reintroduce a silent drop.
		return plan, errMetaLayout
	}
	iinfBox, irefBox, err := buildItemBoxes(metaContent, newItems)
	if err != nil {
		return plan, err
	}

	info.items = preparePendingIlocItems(info.items, pendingByID)

	plan = injectPlan{ilocInfo: info, pendingByID: pendingByID}
	plan.parts, plan.ilocIdx = planMetaParts(metaContent, iinfBox, irefBox)
	plan.metaLen = metaHeaderLen + fullBoxFieldsLen + ilocBoxSize(info, info.items)
	for _, p := range plan.parts {
		plan.metaLen += len(p)
	}
	return plan, nil
}

// widenIlocFields widens an iloc offset_size or length_size of 0 to 4 bytes.
// A width of 0 means the field is absent and reads as 0, so writing a 4-byte
// 0 into every existing extent keeps its meaning (ISO 14496-12 §8.11.3.3);
// the new payload locations need a field that can hold them. 4 bytes always
// suffice for every file below 4 GiB, which is every real HEIF file;
// assignItemOffsets reports errIlocFieldOverflow, never a truncated value, for a
// larger one (Inject has no limit on the size of the file, task #390).
func widenIlocFields(info *ilocBoxInfo) {
	if info.offsetSize == 0 {
		info.offsetSize = 4
	}
	if info.lengthSize == 0 {
		info.lengthSize = 4
	}
}

// placePayloads assigns every pending item its extent inside the new 'mdat'
// box that starts at mdatStart and returns that box: its 8-byte header and
// every payload in item order, in one buffer allocated at its exact size.
func placePayloads(plan injectPlan, mdatStart uint64) ([]byte, error) {
	payloadLen, err := assignItemOffsets(plan.ilocInfo, plan.pendingByID, mdatStart+8)
	if err != nil {
		return nil, err
	}
	if payloadLen > math.MaxUint32-8 {
		return nil, errPayloadTooLarge
	}
	box := make([]byte, 8, 8+payloadLen)
	binary.BigEndian.PutUint32(box[0:4], uint32(payloadLen+8))
	copy(box[4:8], "mdat")
	for _, item := range plan.ilocInfo.items {
		if p, ok := plan.pendingByID[item.id]; ok {
			box = p.appendTo(box)
		}
	}
	return box, nil
}

// ---------------------------------------------------------------------------
// Item creation (task #309)
// ---------------------------------------------------------------------------

// newMetadataItem is an Exif or XMP item created by Inject.
type newMetadataItem struct {
	id  uint16
	typ [4]byte // itemTypeExif or itemTypeMime
}

// planNewItems creates the metadata items the file lacks — an Exif item when
// needEXIF, an XMP item when needXMP — assigns each a fresh item ID above
// every ID in use, and records its payload in pendingByID. ISO 23008-12
// Annex A.2 (Exif item 'Exif'), A.3 (XMP as a 'mime' item with content_type
// "application/rdf+xml").
func planNewItems(metaContent []byte, items []ilocFullItem, needEXIF, needXMP bool, rawEXIF, rawXMP []byte, pendingByID map[uint16]pendingPayload) ([]newMetadataItem, error) {
	if !needEXIF && !needXMP {
		return nil, nil
	}
	next := maxItemID(metaContent, items)
	newItems := make([]newMetadataItem, 0, 2)
	add := func(typ [4]byte, payload pendingPayload) error {
		if next == math.MaxUint16 {
			return errNoFreeItemID
		}
		next++
		newItems = append(newItems, newMetadataItem{id: next, typ: typ})
		pendingByID[next] = payload
		return nil
	}
	if needEXIF {
		if err := add(itemTypeExif, pendingPayload{data: rawEXIF, exif: true}); err != nil {
			return nil, err
		}
	}
	if needXMP {
		if err := add(itemTypeMime, pendingPayload{data: rawXMP}); err != nil {
			return nil, err
		}
	}
	return newItems, nil
}

// maxItemID returns the largest item ID declared by any infe box of the iinf
// box (whatever its type or version) or by any iloc entry.
func maxItemID(metaContent []byte, items []ilocFullItem) uint16 {
	var maxID uint16
	for _, it := range items {
		maxID = max(maxID, it.id)
	}
	iinfData := findInnerBox(metaContent, boxTypeIinf)
	if len(iinfData) < 4 {
		return maxID
	}
	_, pos, ok := parseIinfEntryCount(iinfData, iinfData[0], 4)
	if !ok {
		return maxID
	}
	for pos < len(iinfData) {
		size, typ, headerLen, boxOK := parseHEIFBoxHeader(iinfData, pos)
		if !boxOK {
			break
		}
		if typ == boxTypeInfe {
			body := iinfData[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length
			maxID = max(maxID, infeItemID(body))
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return maxID
}

// infeItemID returns the item_ID of an infe box body (0 when truncated). The
// ID is 16-bit in infe versions 0-2 and 32-bit in version 3 (ISO 14496-12
// §8.11.6.2); a 32-bit ID above 0xFFFF saturates to 0xFFFF, which makes
// planNewItems refuse to allocate rather than collide.
func infeItemID(body []byte) uint16 {
	if len(body) < 4 {
		return 0
	}
	if body[0] == 3 {
		if len(body) < 8 {
			return 0
		}
		return uint16(min(binary.BigEndian.Uint32(body[4:]), math.MaxUint16))
	}
	if len(body) < 6 {
		return 0
	}
	return binary.BigEndian.Uint16(body[4:])
}

// appendInfeV2 appends an ItemInfoEntry box of version 2 for a new metadata
// item, laid out as libavif writes it (ISO 14496-12 §8.11.6.2):
//
//	FullBox('infe', 2, 0) { item_ID(16); item_protection_index(16) = 0;
//	  item_type(32); item_name (utf8string); [content_type (utf8string)] }
func appendInfeV2(dst []byte, it newMetadataItem) []byte {
	start := len(dst)
	dst = append(dst, 0, 0, 0, 0, 'i', 'n', 'f', 'e', 2, 0, 0, 0)
	dst = binary.BigEndian.AppendUint16(dst, it.id)
	dst = append(dst, 0, 0) // item_protection_index: not protected
	if it.typ == itemTypeExif {
		dst = append(dst, itemTypeExif[:]...)
		dst = append(dst, exifItemName...)
	} else {
		dst = append(dst, itemTypeMime[:]...)
		dst = append(dst, xmpItemName...)
		dst = append(dst, xmpContentType...)
		dst = append(dst, 0)
	}
	binary.BigEndian.PutUint32(dst[start:], uint32(len(dst)-start)) //nolint:gosec // G115: a few dozen bytes
	return dst
}

// buildItemBoxes returns the rebuilt iinf box (with one infe per new item
// appended and entry_count updated) and the rebuilt or new iref box (with
// one 'cdsc' reference from each new item to the primary item, per the pitm
// box). Both are nil when newItems is empty; irefBox is nil when the file has
// no primary item.
// ISO 14496-12 §8.11.6 (iinf), §8.11.12 (iref); ISO 23008-12 and libavif
// avifEncoderDataCreateExifItem: a metadata item is linked to the image it
// describes by a 'cdsc' (content describes) reference.
func buildItemBoxes(metaContent []byte, newItems []newMetadataItem) (iinfBox, irefBox []byte, err error) {
	if len(newItems) == 0 {
		return nil, nil, nil
	}
	if iinfBox, err = buildIinfBox(metaContent, newItems); err != nil {
		return nil, nil, err
	}
	primaryID := parsePitm(metaContent)
	if primaryID == 0 {
		return iinfBox, nil, nil
	}
	irefBox, err = buildIrefBox(metaContent, newItems, primaryID)
	return iinfBox, irefBox, err
}

// buildIinfBox appends one infe box per new item to the file's iinf box,
// keeping its version (entry_count is 16-bit for version 0 and 32-bit
// otherwise, ISO 14496-12 §8.11.6.2), or creates an iinf box of version 0
// when the file has none.
//
// Only the source children that parse as complete boxes are kept (the walk
// parseIinf performs), and entry_count is set to the number of infe boxes
// written. Bytes after the last complete child are dropped: appending boxes
// after them would let a truncated source infe box parse as a complete one in
// the output, turning garbage into an item (found by FuzzHEIFInject).
func buildIinfBox(metaContent []byte, newItems []newMetadataItem) ([]byte, error) {
	var body []byte
	if start, end, headerLen, found := childBoxRange(metaContent, boxTypeIinf); found {
		body = metaContent[start+headerLen : end]
	} else {
		body = []byte{0, 0, 0, 0, 0, 0} // version 0, flags 0, entry_count 0
	}
	if len(body) < 4 {
		return nil, errItemTableFull
	}
	countWidth := 2
	if body[0] != 0 {
		countWidth = 4
	}
	if len(body) < 4+countWidth {
		return nil, errItemTableFull
	}
	children, infeCount := completeChildren(body[4+countWidth:], boxTypeInfe)
	count := uint64(infeCount + len(newItems)) //nolint:gosec // G115: non-negative counts bounded by the box size
	if countWidth == 2 && count > math.MaxUint16 {
		return nil, errItemTableFull
	}
	box := make([]byte, 8, 8+4+countWidth+len(children)+len(newItems)*48)
	copy(box[4:8], "iinf")
	box = append(box, body[:4]...)
	box = appendUintN(box, countWidth, count)
	box = append(box, children...)
	for _, it := range newItems {
		box = appendInfeV2(box, it)
	}
	binary.BigEndian.PutUint32(box[0:4], uint32(len(box))) //nolint:gosec // G115: bounded by the meta box size
	return box, nil
}

// completeChildren returns the prefix of data made of complete child boxes
// (stopping at the first header that does not parse) and the number of
// those children of type typ. A last child that is open-ended (size 0, ISO
// 14496-12 §4.2) is closed in the returned prefix, which is then a copy: the
// caller appends boxes after the prefix, and a reader would otherwise take
// them as part of that child (task #359).
func completeChildren(data []byte, typ [4]byte) (prefix []byte, n int) {
	pos, last := 0, 0
	for pos < len(data) {
		size, t, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		if t == typ {
			n++
		}
		last = pos
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	if pos > 0 && bmff.OpenEnded(data[last:pos]) {
		closed := bmff.Close(data[last:pos])
		prefix = make([]byte, 0, last+len(closed))
		return append(append(prefix, data[:last]...), closed...), n
	}
	return data[:pos], n
}

// buildIrefBox appends one SingleItemTypeReferenceBox of type 'cdsc' per new
// item (from the new item to primaryID) to the file's iref box, keeping its
// version (item IDs are 16-bit for version 0 and 32-bit for version 1,
// ISO 14496-12 §8.11.12.2), or creates an iref box of version 0.
func buildIrefBox(metaContent []byte, newItems []newMetadataItem, primaryID uint16) ([]byte, error) {
	var body []byte
	if start, end, headerLen, found := childBoxRange(metaContent, boxTypeIref); found {
		body = metaContent[start+headerLen : end]
	} else {
		body = []byte{0, 0, 0, 0} // version 0, flags 0
	}
	if len(body) < 4 || body[0] > 1 {
		return nil, errItemTableFull
	}
	idWidth := 2
	if body[0] == 1 {
		idWidth = 4
	}
	refLen := 8 + idWidth + 2 + idWidth
	// Keep only the complete reference boxes (see buildIinfBox).
	children, _ := completeChildren(body[4:], [4]byte{})
	box := make([]byte, 8, 8+4+len(children)+len(newItems)*refLen)
	copy(box[4:8], "iref")
	box = append(box, body[:4]...)
	box = append(box, children...)
	for _, it := range newItems {
		// SingleItemTypeReferenceBox: size(32) + 'cdsc' + from_item_ID +
		// reference_count(16) = 1 + to_item_ID.
		box = binary.BigEndian.AppendUint32(box, uint32(refLen))
		box = append(box, 'c', 'd', 's', 'c')
		box = appendUintN(box, idWidth, uint64(it.id))
		box = binary.BigEndian.AppendUint16(box, 1)
		box = appendUintN(box, idWidth, uint64(primaryID))
	}
	binary.BigEndian.PutUint32(box[0:4], uint32(len(box))) //nolint:gosec // G115: bounded by the meta box size
	return box, nil
}

// childBoxRange returns the byte range [start, end) of the first child box of
// type typ in data (flat scan) and its header length.
func childBoxRange(data []byte, typ [4]byte) (start, end, headerLen int, found bool) {
	pos := 0
	for pos < len(data) {
		size, t, hl, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		if t == typ {
			return pos, pos + int(size), int(hl), true //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return 0, 0, 0, false
}

// ---------------------------------------------------------------------------
// Meta box rebuild
// ---------------------------------------------------------------------------

// planMetaParts lists, in output order, every child box of the rebuilt meta
// box: the source children with the first iinf and iref replaced by iinfBox
// and irefBox (when non-nil), a new iref inserted right after the iinf box
// when the source has none, and the first iloc replaced by a placeholder
// (nil) at index ilocIdx, filled once the final iloc bytes exist. Later iloc
// boxes are dropped, so the output has exactly one iloc box — the one whose
// items parseIlocFull read.
//
// The walk stops at the first child that does not parse as a box; the bytes
// after it are dropped. Both the length computation and the serialisation
// use this one list, so they cannot disagree (security audit finding
// HEIF-ILOC-DUPBOX-01 was a drift between two separate walks; regression
// gates TestInjectDuplicateIlocBoxesOffsetInBounds and
// TestInjectMetaWithTrailingUnparseableBytesOffsetInBounds).
func planMetaParts(metaContent, iinfBox, irefBox []byte) (parts [][]byte, ilocIdx int) {
	p := metaPlanner{
		// One part per source child plus the new iinf, iref and iloc boxes,
		// allocated once: countChildren is a header-only walk.
		parts:      make([][]byte, 0, countChildren(metaContent)+3),
		iinfBox:    iinfBox,
		irefBox:    irefBox,
		insertIref: irefBox != nil && findInnerBox(metaContent, boxTypeIref) == nil,
		ilocIdx:    -1,
	}
	pos := 0
	for pos < len(metaContent) {
		size, typ, _, ok := parseHEIFBoxHeader(metaContent, pos)
		if !ok {
			break
		}
		p.add(typ, bmff.Close(metaContent[pos:pos+int(size)])) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		pos += int(size)                                       //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	p.finish()
	return p.parts, p.ilocIdx
}

// countChildren returns the number of complete child boxes of data.
func countChildren(data []byte) int {
	n, pos := 0, 0
	for pos < len(data) {
		size, _, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		n++
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return n
}

// metaPlanner accumulates the parts of planMetaParts.
type metaPlanner struct {
	parts              [][]byte
	iinfBox, irefBox   []byte
	ilocIdx            int
	seenIinf, seenIref bool
	insertIref         bool // a new iref box still has to be inserted
}

// add appends the output part for the source child box of type typ.
func (p *metaPlanner) add(typ [4]byte, box []byte) {
	switch {
	case typ == boxTypeIloc:
		if p.ilocIdx < 0 {
			p.ilocIdx = len(p.parts)
			p.parts = append(p.parts, nil)
		}
	case typ == boxTypeIinf && !p.seenIinf:
		p.seenIinf = true
		p.parts = append(p.parts, replacementOr(p.iinfBox, box))
		p.flushIref()
	case typ == boxTypeIref && !p.seenIref:
		p.seenIref = true
		p.parts = append(p.parts, replacementOr(p.irefBox, box))
	default:
		p.parts = append(p.parts, box)
	}
}

// flushIref appends the new iref box if it is still pending.
func (p *metaPlanner) flushIref() {
	if p.insertIref {
		p.parts = append(p.parts, p.irefBox)
		p.insertIref = false
	}
}

// finish appends the boxes the source lacks: a new iinf box, a new iref box,
// and the iloc placeholder.
func (p *metaPlanner) finish() {
	if !p.seenIinf && p.iinfBox != nil {
		p.parts = append(p.parts, p.iinfBox)
	}
	p.flushIref()
	if p.ilocIdx < 0 {
		p.ilocIdx = len(p.parts)
		p.parts = append(p.parts, nil)
	}
}

// replacementOr returns repl when it is non-nil and orig otherwise.
func replacementOr(repl, orig []byte) []byte {
	if repl != nil {
		return repl
	}
	return orig
}

// buildMetaBox serialises a meta FullBox of exactly total bytes from its
// 4-byte version+flags and the child boxes of parts, into one pre-sized
// buffer. headerLen is the box header length: 8, or 16 when the source meta
// box used the 64-bit largesize form (size field 1 followed by the 64-bit
// size, ISO 14496-12 §4.2), which the rebuilt box keeps.
func buildMetaBox(versionFlags []byte, parts [][]byte, total, headerLen int) []byte {
	buf := make([]byte, headerLen, total)
	buf[4], buf[5], buf[6], buf[7] = 'm', 'e', 't', 'a'
	buf = append(buf, versionFlags...)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	if headerLen == 16 {
		binary.BigEndian.PutUint32(buf[0:4], 1)
		binary.BigEndian.PutUint64(buf[8:16], uint64(len(buf)))
	} else {
		binary.BigEndian.PutUint32(buf[0:4], uint32(len(buf))) //nolint:gosec // G115: ISOBMFF box size bounded by body length
	}
	return buf
}

// ---------------------------------------------------------------------------
// Offset shifting (task #309)
// ---------------------------------------------------------------------------

// shiftIlocItems adds delta to the absolute file offset of every item that is
// not pending and is stored by file offset in this file, when that offset is
// at or beyond threshold (the old end of the meta box). When an item's
// base_offset is itself at or beyond threshold, the base_offset is shifted
// (its extents are relative to it); otherwise each qualifying extent offset
// is. ISO 14496-12 §8.11.3.3: the absolute position of an extent is
// base_offset + extent_offset.
func shiftIlocItems(info ilocBoxInfo, pendingByID map[uint16]pendingPayload, metaContent []byte, threshold uint64, delta int64) error {
	for i := range info.items {
		it := &info.items[i]
		if _, pending := pendingByID[it.id]; pending {
			continue
		}
		if it.constructMethod&constructionMethodMask != constructionFileOffset || !itemDataInThisFile(metaContent, it.dataRefIdx) {
			continue
		}
		if err := shiftIlocItem(it, info, threshold, delta); err != nil {
			return err
		}
	}
	return nil
}

// shiftIlocItem applies the shift of shiftIlocItems to one item stored by
// file offset.
func shiftIlocItem(it *ilocFullItem, info ilocBoxInfo, threshold uint64, delta int64) error {
	if info.baseOffsetSize > 0 && it.baseOffset >= threshold {
		v, err := shiftValue(it.baseOffset, delta, info.baseOffsetSize)
		it.baseOffset = v
		return err
	}
	for j := range it.extents {
		ext := &it.extents[j]
		if ext.offset > math.MaxUint64-it.baseOffset || it.baseOffset+ext.offset < threshold {
			continue
		}
		v, err := shiftValue(ext.offset, delta, info.offsetSize)
		if err != nil {
			return err
		}
		ext.offset = v
	}
	return nil
}

// shiftValue returns v+delta when the result is non-negative and fits an
// iloc field of width bytes.
func shiftValue(v uint64, delta int64, width int) (uint64, error) {
	if v > math.MaxInt64 {
		return 0, errIlocFieldOverflow
	}
	n := int64(v) + delta
	if n < 0 || !fitsIlocField(uint64(n), width) {
		return 0, errIlocFieldOverflow
	}
	return uint64(n), nil
}

// shiftChunkOffsetsIn walks the children of a container box payload,
// recursing into the containers that lead to chunk offset boxes (trak, mdia,
// minf, stbl; depth capped at 32 like findBox) and patching stco/co64 in
// place.
func shiftChunkOffsetsIn(data []byte, threshold, delta int64, depth int) error {
	if depth > 32 {
		return nil
	}
	pos := 0
	for pos < len(data) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			return nil
		}
		content := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		pos += int(size)                                    //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		switch typ {
		case boxTypeTrak, boxTypeMdia, boxTypeMinf, boxTypeStbl:
			if err := shiftChunkOffsetsIn(content, threshold, delta, depth+1); err != nil {
				return err
			}
		case boxTypeStco, boxTypeCo64:
			if err := shiftChunkOffsetTable(content, typ == boxTypeCo64, threshold, delta); err != nil {
				return err
			}
		}
	}
	return nil
}

// shiftChunkOffsetTable patches one stco (32-bit entries) or co64 (64-bit
// entries) payload in place: FullBox version+flags(4), entry_count(32), then
// the entries (ISO 14496-12 §8.7.5.2). A truncated table is left as found.
func shiftChunkOffsetTable(content []byte, wide bool, threshold, delta int64) error {
	if len(content) < 8 {
		return nil
	}
	width := 4
	if wide {
		width = 8
	}
	count := uint64(binary.BigEndian.Uint32(content[4:]))
	if count > uint64((len(content)-8)/width) { //nolint:gosec // G115: len(content) >= 8
		return nil
	}
	for i := range int(count) { //nolint:gosec // G115: count bounded by len(content)/width
		if err := shiftChunkOffset(content[8+i*width:], wide, threshold, delta); err != nil {
			return err
		}
	}
	return nil
}

// shiftChunkOffset shifts the one stco (4-byte) or co64 (8-byte) entry at
// the start of p when it is at or beyond threshold.
func shiftChunkOffset(p []byte, wide bool, threshold, delta int64) error {
	var v int64
	if wide {
		raw := binary.BigEndian.Uint64(p)
		if raw > math.MaxInt64 {
			return nil // beyond any file this package accepts; never shifted
		}
		v = int64(raw)
	} else {
		v = int64(binary.BigEndian.Uint32(p))
	}
	if v < threshold {
		return nil
	}
	n := v + delta
	switch {
	case n < 0, !wide && n > math.MaxUint32:
		return errChunkOffsetOverflow
	case wide:
		binary.BigEndian.PutUint64(p, uint64(n))
	default:
		binary.BigEndian.PutUint32(p, uint32(n)) //nolint:gosec // G115: 0 <= n <= MaxUint32 checked above
	}
	return nil
}

// ---------------------------------------------------------------------------
// Inject helpers
// ---------------------------------------------------------------------------

// mapPendingItems maps the IDs of the existing Exif and XMP items to their
// new payloads based on itemTypes. EXIF items get the 4-byte TIFF header
// offset prefix (pendingPayload.exif). XMP items are stored verbatim.
//
// hasEXIF and hasXMP report whether the file has an Exif item and an XMP
// item, so the caller learns which items to create without iterating
// itemTypes again.
//
// The result map is pre-sized to len(itemTypes)+2: pendingByID never holds
// more than one entry per existing item plus the two items planNewItems may
// add (task #229).
func mapPendingItems(itemTypes map[uint16][4]byte, rawEXIF, rawXMP []byte) (pendingByID map[uint16]pendingPayload, hasEXIF, hasXMP bool) {
	pendingByID = make(map[uint16]pendingPayload, len(itemTypes)+2)
	for id, typ := range itemTypes {
		switch typ {
		case itemTypeExif:
			hasEXIF = true
			if rawEXIF != nil {
				pendingByID[id] = pendingPayload{data: rawEXIF, exif: true}
			}
		case itemTypeMime:
			hasXMP = true
			if rawXMP != nil {
				pendingByID[id] = pendingPayload{data: rawXMP}
			}
		}
	}
	return pendingByID, hasEXIF, hasXMP
}

// updateIlocItemsInPlace mutates items (the caller's freshly-parsed
// ilocInfo.items — never aliased elsewhere) so that each item appearing in
// pendingByID has its construction_method forced to 0 (file-offset), its
// data_reference_index set to 0 (this file), its base offset zeroed, and its
// extents collapsed to a single placeholder extent (offset=0, length=0).
// Real values are assigned later by assignItemOffsets once the output layout
// is known.
//
// It reuses each matched item's existing extents backing array when it
// already has room for one element (the common case), only allocating a fresh
// one-element slice when the existing array is empty (task #229).
//
// ISO 14496-12 §8.11.3: construction_method 0 = file offset, 1 = idat-relative,
// 2 = item-relative. Because relocation always appends the payload at an absolute
// file position, we must force construction_method to 0 regardless of the
// original value. Leaving it as 1 or 2 while writing an absolute file offset
// would cause conformant readers to mis-resolve the metadata location.
//
// It returns the number of distinct pending item IDs found in items, so the
// caller calls appendMissingIlocItems only when some pending item has no
// iloc entry.
func updateIlocItemsInPlace(items []ilocFullItem, pendingByID map[uint16]pendingPayload) (matched int) {
	for i := range items {
		id := items[i].id
		if _, ok := pendingByID[id]; !ok {
			continue
		}
		// A duplicate iloc entry of the same item is updated too, but is
		// counted once. Matches are rare (one or two per file), so the
		// backward scan costs nothing measurable.
		if !slices.ContainsFunc(items[:i], func(it ilocFullItem) bool { return it.id == id }) {
			matched++
		}
		items[i].constructMethod = 0
		// The relocated payload is in this file: ISO 14496-12 §8.11.3.3,
		// data_reference_index 0 means "this file".
		items[i].dataRefIdx = 0
		items[i].baseOffset = 0
		if cap(items[i].extents) >= 1 {
			items[i].extents = items[i].extents[:1]
			items[i].extents[0] = ilocExtent{}
		} else {
			items[i].extents = []ilocExtent{{}}
		}
	}
	return matched
}

// preparePendingIlocItems readies the iloc entries of every pending item:
// existing entries are reset in place (updateIlocItemsInPlace — items is the
// slice parseIlocFull produced for this call, so no other reference shares
// its backing array, task #229), and an entry is appended for every pending
// item that has none (appendMissingIlocItems, which runs only when needed).
func preparePendingIlocItems(items []ilocFullItem, pendingByID map[uint16]pendingPayload) []ilocFullItem {
	if updateIlocItemsInPlace(items, pendingByID) < len(pendingByID) {
		return appendMissingIlocItems(items, pendingByID)
	}
	return items
}

// appendMissingIlocItems appends an iloc entry (construction_method 0,
// data_reference_index 0, one placeholder extent) for every pending item
// that has none — the items created by planNewItems, and any Exif/XMP item
// that the source declared in iinf but not in iloc. Entries are appended in
// ascending item ID order so the output is deterministic.
func appendMissingIlocItems(items []ilocFullItem, pendingByID map[uint16]pendingPayload) []ilocFullItem {
	var missing []uint16
	for id := range pendingByID {
		if !slices.ContainsFunc(items, func(it ilocFullItem) bool { return it.id == id }) {
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	for _, id := range missing {
		items = append(items, ilocFullItem{id: id, extents: []ilocExtent{{}}})
	}
	return items
}

// assignItemOffsets walks info.items, finds each item in pendingByID, and
// assigns its real file offset by accumulating from base, the absolute
// offset of the first payload byte. It returns the total payload length.
// errIlocFieldOverflow is returned when an offset or length does not fit the
// iloc field widths of the source file (ISO 14496-12 §8.11.3.3).
func assignItemOffsets(info ilocBoxInfo, pendingByID map[uint16]pendingPayload, base uint64) (uint64, error) {
	var accum uint64
	for i, item := range info.items {
		payload, ok := pendingByID[item.id]
		if !ok {
			continue
		}
		off := base + accum
		n := payload.size()
		if !fitsIlocField(off, info.offsetSize) || !fitsIlocField(n, info.lengthSize) {
			return 0, errIlocFieldOverflow
		}
		info.items[i].extents[0].offset = off
		info.items[i].extents[0].length = n
		accum += n
	}
	return accum, nil
}

// fitsIlocField reports whether v can be stored in an iloc field of width
// bytes (0, 4 or 8).
func fitsIlocField(v uint64, width int) bool {
	switch width {
	case 8:
		return true
	case 4:
		return v <= math.MaxUint32
	default:
		return false
	}
}
