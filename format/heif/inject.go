package heif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

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
	// mdatHeader is the 8-byte header of the media data box appended after
	// suffix to hold every pending payload (see buildInjectComponents).
	mdatHeader [8]byte
}

// errIlocFieldOverflow is returned when a relocated item offset or length
// does not fit the iloc field width declared by the source file.
var errIlocFieldOverflow = errors.New("heif: item location does not fit the iloc field width")

// errPayloadTooLarge is returned when the new metadata payloads do not fit a
// media data box with a 32-bit size field.
var errPayloadTooLarge = errors.New("heif: metadata payloads exceed the media data box size limit")

// buildInjectComponents parses the existing meta box, maps pending items, computes
// the final output prefix (with patched ancestor sizes), and builds the final
// meta box with correct iloc offsets. Returns false if injection cannot proceed
// (e.g. no matching items, unreadable iloc) — caller should pass through unchanged.
//
// Payload placement (task #282): every pending payload is stored in ONE new
// 'mdat' box appended after the last top-level box of the file. ISO/IEC
// 14496-12 §4.2 requires a file to be a sequence of boxes, and §8.1.1 allows
// any number of 'mdat' boxes; bytes appended after the last box without a box
// header are read by conformant parsers as a malformed box (ExifTool reports
// "Unknown trailer"). Each item's extent offset points inside that box.
//
// Single-pass design (task #229): the iloc/meta boxes are serialised exactly
// ONCE, not twice. This is possible because every iloc field (item_ID width,
// construction_method, base_offset, extent_count, and each extent's
// index/offset/length) has a FIXED byte width once the box's field-size
// nibbles and item/extent counts are known (ISO 14496-12 §8.11.3) — the iloc
// (and therefore meta) box's total LENGTH never depends on the actual offset
// or length VALUES stored in it, only on its structure. updateIlocItemsInPlace
// fixes that structure upfront (every updated item collapses to exactly one
// placeholder extent), so newMetaBoxLen can compute the exact byte length the
// rebuilt meta box will have using pure arithmetic (nonIlocBoxesLen +
// ilocBoxSize), with no bytes written at all. That length is enough to patch
// ancestor container sizes and compute the final item base offset BEFORE the
// real offset/length values are known; assignItemOffsets then fills those
// values into the already correctly-shaped ilocInfo.items, and
// buildIlocBox/buildMetaBox each run once, already carrying the final values.
func buildInjectComponents(data []byte, metaAbsStart, metaAbsEnd, metaContentOff int, rawEXIF, rawXMP []byte) (injectComponents, bool, error) {
	metaContent := data[metaContentOff:metaAbsEnd]

	itemTypes := parseIinf(metaContent)
	ilocInfo, ilocOK := parseIlocFull(metaContent)
	// An offset or length field of width 0 cannot hold the location of a
	// relocated payload (ISO 14496-12 §8.11.3.3).
	if !ilocOK || ilocInfo.offsetSize == 0 || ilocInfo.lengthSize == 0 {
		return injectComponents{}, false, nil
	}

	pendingByID := mapPendingItems(itemTypes, rawEXIF, rawXMP)
	if len(pendingByID) == 0 {
		return injectComponents{}, false, nil
	}

	// The new payloads are appended as a new top-level box, so a last box
	// that extends to the end of the file must first get an explicit size.
	closeOpenEndedBox(data)

	// Mutates ilocInfo.items in place: parseIlocFull produced this slice fresh
	// for this call, so no other reference shares its backing array (task #229;
	// avoids the extra full-slice copy the former updateIlocItems allocated).
	updateIlocItemsInPlace(ilocInfo.items, pendingByID)

	versionFlags := data[metaAbsStart+8 : metaContentOff] // 4 bytes: version + flags
	newMetaLen := newMetaBoxLen(metaContent, ilocInfo)
	metaDelta := newMetaLen - (metaAbsEnd - metaAbsStart)

	suffix := data[metaAbsEnd:]
	outputPrefix := make([]byte, metaAbsStart)
	copy(outputPrefix, data[:metaAbsStart])
	if metaDelta != 0 {
		patchAncestorSize(outputPrefix, metaAbsStart, metaDelta)
	}

	mdatStart := uint64(len(outputPrefix)) + uint64(newMetaLen) + uint64(len(suffix)) //nolint:gosec // G115: newMetaLen is a box length bounded by file size
	payloadLen, err := assignItemOffsets(ilocInfo, pendingByID, mdatStart+8)
	if err != nil {
		return injectComponents{}, false, err
	}
	var mdatHeader [8]byte
	if payloadLen > math.MaxUint32-8 {
		return injectComponents{}, false, errPayloadTooLarge
	}
	binary.BigEndian.PutUint32(mdatHeader[0:4], uint32(payloadLen+8))
	copy(mdatHeader[4:8], "mdat")

	finalIlocBytes := buildIlocBox(ilocInfo, ilocInfo.items)
	finalMetaBox := buildMetaBox(versionFlags, metaContent, finalIlocBytes)

	return injectComponents{
		outputPrefix: outputPrefix,
		finalMetaBox: finalMetaBox,
		suffix:       suffix,
		updatedItems: ilocInfo.items,
		pendingByID:  pendingByID,
		mdatHeader:   mdatHeader,
	}, true, nil
}

// Inject writes a modified HEIF stream to w with updated metadata items.
//
// The iloc box is fully rebuilt with updated entries for modified items,
// preserving all other items unchanged. Any enclosing container box sizes
// (e.g. moov) are patched if the meta box size changes. New item payloads
// are stored in a new 'mdat' box appended at the end of the output file.
//
// preserveUnknownSegments must be true. HEIF/AVIF ISOBMFF boxes (ftyp, moov,
// mdat, etc.) are structurally mandatory; there is no concept of an "unknown
// optional segment" analogous to JPEG's APPn. Passing false returns
// ErrPreserveUnknownSegmentsNotSupported.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error { //nolint:cyclop,gocyclo // preserveUnknownSegments guard adds one branch; function already contained maximum allowed complexity pre-#85
	// Reject PreserveUnknownSegments(false) for HEIF/AVIF: ISOBMFF boxes are
	// structurally mandatory. There is no concept of "unknown optional segment"
	// in ISOBMFF analogous to JPEG's APPn segments.
	if !preserveUnknownSegments {
		return ErrPreserveUnknownSegmentsNotSupported
	}

	// Defense in depth: reject a JPEG extended-XMP wire-frame that was not
	// filtered out by the encodeXMP format check in write.go. The wire-frame
	// begins with 0x00XMPEXT\x00 — an invalid start for any XMP packet — and
	// can only be decoded by jpeg.Inject. Writing it verbatim to a HEIF XMP
	// item would produce a corrupt, non-XMP blob. (Bug #70.)
	if len(rawXMP) >= len(xmpWireFrameMagic) && [8]byte(rawXMP[:8]) == xmpWireFrameMagic {
		return fmt.Errorf("heif: rawXMP contains an internal JPEG wire-frame encoding that cannot be stored in a HEIF/AVIF container: %w", ErrCorruptXMP)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("heif: seek: %w", err)
	}
	// #140 fix, #230 refinement: cap the full-file read to maxFileSize bytes so
	// that an oversized or infinite streaming reader cannot trigger unbounded
	// heap allocation. iobuf.ReadAll allocates exactly one buffer sized to the
	// input's Seek-reported length (filled with a single io.ReadFull) instead
	// of io.ReadAll's geometric-growth strategy, and rejects an oversized input
	// via ErrTooLarge before allocating anything at all.
	data, err := iobuf.ReadAll(r, maxFileSize)
	if err != nil {
		if errors.Is(err, iobuf.ErrTooLarge) {
			return fmt.Errorf("heif: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
		}
		return fmt.Errorf("heif: read: %w", err)
	}

	if rawEXIF == nil && rawXMP == nil {
		return writePassThrough(w, data)
	}

	metaAbsStart, metaAbsEnd, metaContentOff, found := findMetaBoxAbs(data)
	if !found || metaAbsEnd > len(data) {
		return writePassThrough(w, data)
	}

	// data is owned by Inject (iobuf.ReadAll allocates it), so every edit
	// buildInjectComponents makes is made in place — and only once it has
	// decided to rebuild the file, so a pass-through stays byte-identical.
	comp, ok, err := buildInjectComponents(data, metaAbsStart, metaAbsEnd, metaContentOff, rawEXIF, rawXMP)
	if err != nil {
		return err
	}
	if !ok {
		return writePassThrough(w, data)
	}

	return writeHEIFOutput(w, &comp)
}

// nonIlocBoxesLen sums the sizes of every child box in metaContent that is
// NOT of type boxTypeIloc, by walking metaContent box-by-box and stopping
// the instant parseHEIFBoxHeader fails to parse the next header — the EXACT
// same traversal buildMetaBox itself performs for both of its own passes
// (the size pre-computation pass and the copy pass). Any trailing bytes
// that traversal cannot parse as a box header are silently dropped by
// buildMetaBox's real output, so they must also be excluded here.
//
// This single shared helper is what makes newMetaBoxLen provably match what
// buildMetaBox will actually produce for ANY metaContent — well-formed or
// not — instead of relying on two independently-written loops (one for
// measuring, one for building) to stay in lockstep by convention. Security
// audit finding HEIF-ILOC-DUPBOX-01 (2026-09-25) was exactly that class of
// drift: an earlier, hand-written size-measuring loop stopped at the FIRST
// 'iloc'-typed child instead of summing every one buildMetaBox's copy loop
// actually drops (`if typ != boxTypeIloc { append }` — no early exit), and a
// closely related second drift (found via the fuzz invariant added for this
// same finding) affected metaContent with trailing bytes that fail to parse
// as a box header at all: buildMetaBox's copy loop stops there and drops
// them, but a naive "declared meta box length minus removed iloc size"
// computation does not know to exclude them. Extracting the traversal into
// one function used by both call sites closes both variants at once, and
// any future one sharing the same root cause. Regression gates:
// TestInjectDuplicateIlocBoxesOffsetInBounds,
// TestInjectMetaWithTrailingUnparseableBytesOffsetInBounds.
func nonIlocBoxesLen(metaContent []byte) int {
	total := 0
	pos := 0
	for pos < len(metaContent) {
		size, typ, _, ok := parseHEIFBoxHeader(metaContent, pos)
		if !ok {
			break
		}
		if typ != boxTypeIloc {
			total += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return total
}

// newMetaBoxLen returns the exact total byte length (including the 8-byte
// box header) that buildMetaBox(versionFlags, metaContent, newIloc) would
// produce for an iloc box built from info/info.items, without serialising
// either box. It is arithmetic-only (task #229) but, unlike computing a
// "delta" from the OLD meta box's own declared length, it is built entirely
// from the same nonIlocBoxesLen traversal buildMetaBox itself uses — see
// nonIlocBoxesLen's doc comment for why that equivalence matters.
func newMetaBoxLen(metaContent []byte, info ilocBoxInfo) int {
	return 8 + 4 + nonIlocBoxesLen(metaContent) + ilocBoxSize(info, info.items)
}

// buildMetaBox constructs a meta FullBox by copying all child boxes from
// metaContent except iloc, then appending newIloc, into a single pre-sized
// buffer (box header + body together — one allocation instead of the former
// separate body+hdr buffers, task #229).
// versionFlags is the 4-byte FullBox version+flags field.
func buildMetaBox(versionFlags, metaContent, newIloc []byte) []byte {
	// Pre-compute body size to avoid realloc churn, via the same traversal
	// the copy pass below performs (see nonIlocBoxesLen's doc comment for
	// why sharing this exact walk with newMetaBoxLen matters).
	totalBody := 4 + nonIlocBoxesLen(metaContent) // version+flags + non-iloc children
	totalBody += len(newIloc)

	buf := make([]byte, 8, 8+totalBody)
	buf[4], buf[5], buf[6], buf[7] = 'm', 'e', 't', 'a'
	buf = append(buf, versionFlags...)

	pos := 0
	for pos < len(metaContent) {
		size, typ, _, ok := parseHEIFBoxHeader(metaContent, pos)
		if !ok {
			break
		}
		if typ != boxTypeIloc {
			buf = append(buf, metaContent[pos:pos+int(size)]...) //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	buf = append(buf, newIloc...)

	binary.BigEndian.PutUint32(buf[0:4], uint32(len(buf))) //nolint:gosec // G115: ISOBMFF box size bounded by body length
	return buf
}

// patchAncestorSize adds delta to the size field of any top-level container
// box in data whose byte range contains metaAbsStart (i.e. wraps the meta
// box). This is needed when meta is nested inside moov and meta changes size.
//
// Both 32-bit and 64-bit extended-size (size==1) boxes are handled:
//   - 32-bit: if the updated size would exceed 0xFFFFFFFF the patch is skipped
//     (the file was already near the 4 GB ISOBMFF single-box limit; corrupting
//     the size field would be worse than leaving it stale).
//   - 64-bit (size field == 1): the 8-byte largesize at bytes [pos+8:pos+16] is
//     read, incremented by delta, and written back.
func patchAncestorSize(data []byte, metaAbsStart, delta int) {
	pos := 0
	for pos+8 <= len(data) {
		rawSize32 := binary.BigEndian.Uint32(data[pos:])
		if rawSize32 == 1 {
			advanced, done := patchAncestorSizeExtended(data, pos, metaAbsStart, delta)
			if done {
				return
			}
			pos = advanced
			continue
		}
		advanced, done := patchAncestorSize32(data, pos, metaAbsStart, delta)
		if done {
			return
		}
		pos = advanced
	}
}

// patchAncestorSizeExtended handles the extended 64-bit size (size==1) branch
// of patchAncestorSize. Returns (newPos, true) when scanning must stop (box
// wraps metaAbsStart or data is truncated); returns (newPos, false) to advance.
// ISO 14496-12 §4.2: if size==1, the 64-bit largesize follows the 4-byte type.
func patchAncestorSizeExtended(data []byte, pos, metaAbsStart, delta int) (newPos int, done bool) {
	if pos+16 > len(data) {
		return pos, true
	}
	size64 := binary.BigEndian.Uint64(data[pos+8:])
	if uint64(pos)+size64 > uint64(len(data)) { //nolint:gosec // G115: pos is non-negative (loop guard)
		return pos, true
	}
	boxEnd := pos + int(size64) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	if pos < metaAbsStart && metaAbsStart < boxEnd {
		newSize64 := int64(size64) + int64(delta) //nolint:gosec // G115: delta bounded by meta box size change
		if newSize64 > 0 {
			binary.BigEndian.PutUint64(data[pos+8:], uint64(newSize64))
		}
		return pos, true
	}
	return pos + int(size64), false //nolint:gosec // G115: ISOBMFF box size bounded by file size
}

// patchAncestorSize32 handles the 32-bit size branch of patchAncestorSize.
// Returns (newPos, true) when scanning must stop; returns (newPos, false) to
// advance to the next box.
func patchAncestorSize32(data []byte, pos, metaAbsStart, delta int) (newPos int, done bool) {
	size := uint64(binary.BigEndian.Uint32(data[pos:]))
	if size == 0 {
		size = uint64(len(data) - pos) //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
	}
	if uint64(pos)+size > uint64(len(data)) { //nolint:gosec // G115: pos is non-negative (loop guard)
		return pos, true
	}
	boxEnd := pos + int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	if pos < metaAbsStart && metaAbsStart < boxEnd {
		// Guard against uint32 overflow: if newSize > 0xFFFFFFFF, skip the
		// patch rather than silently truncating. ISO 14496-12 §4.2: boxes
		// > 4 GB must use the extended-size (size==1) form.
		newSize := int64(size) + int64(delta) //nolint:gosec // G115: delta bounded by meta box size change
		if newSize > 0 && uint64(newSize) <= 0xFFFFFFFF {
			binary.BigEndian.PutUint32(data[pos:], uint32(newSize))
		}
		return pos, true
	}
	return pos + int(size), false //nolint:gosec // G115: ISOBMFF box size bounded by file size
}

// ---------------------------------------------------------------------------
// Inject helpers
// ---------------------------------------------------------------------------

// mapPendingItems maps item IDs to their new payloads based on itemTypes.
// EXIF items get a 4-byte zero prefix prepended (ISO 23008-12 §6.6.1: the
// EXIF item begins with a 4-byte offset to the TIFF header; value 0 means
// immediate). XMP items are stored verbatim.
//
// The result map is pre-sized to len(itemTypes), a safe (if usually loose)
// upper bound computed at zero extra cost — pendingByID can never gain more
// entries than itemTypes has, and real files typically have only one or two
// EXIF/XMP items among many image-variant items (task #229).
func mapPendingItems(itemTypes map[uint16][4]byte, rawEXIF, rawXMP []byte) map[uint16][]byte {
	pendingByID := make(map[uint16][]byte, len(itemTypes))
	for id, typ := range itemTypes {
		switch typ {
		case itemTypeExif:
			if rawEXIF != nil {
				prefix := [4]byte{}
				pendingByID[id] = append(prefix[:], rawEXIF...)
			}
		case itemTypeMime:
			if rawXMP != nil {
				pendingByID[id] = rawXMP
			}
		}
	}
	return pendingByID
}

// updateIlocItemsInPlace mutates items (the caller's freshly-parsed
// ilocInfo.items — never aliased elsewhere) so that each item appearing in
// pendingByID has its construction_method forced to 0 (file-offset), its base
// offset zeroed, and its extents collapsed to a single placeholder extent
// (offset=0, length=0). Real values are assigned later by assignItemOffsets
// once the output layout is known.
//
// Unlike the former updateIlocItems, this does not allocate a copy of items:
// it mutates in place, and reuses each matched item's existing extents
// backing array when it already has room for one element (the common case —
// almost every real HEIF item has exactly one extent already), only
// allocating a fresh one-element slice when the existing array is empty
// (task #229).
//
// ISO 14496-12 §8.11.3: construction_method 0 = file offset, 1 = idat-relative,
// 2 = item-relative. Because relocation always appends the payload at an absolute
// file position, we must force construction_method to 0 regardless of the
// original value. Leaving it as 1 or 2 while writing an absolute file offset
// would cause conformant readers to mis-resolve the metadata location.
func updateIlocItemsInPlace(items []ilocFullItem, pendingByID map[uint16][]byte) {
	for i := range items {
		if _, ok := pendingByID[items[i].id]; !ok {
			continue
		}
		// Force to file-offset semantics: the relocated payload is always
		// written at an absolute file position (construction_method == 0).
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
}

// assignItemOffsets walks info.items, finds each item in pendingByID, and
// assigns its real file offset by accumulating from base, the absolute
// offset of the first payload byte. It returns the total payload length.
// errIlocFieldOverflow is returned when an offset or length does not fit the
// iloc field widths of the source file (ISO 14496-12 §8.11.3.3).
func assignItemOffsets(info ilocBoxInfo, pendingByID map[uint16][]byte, base uint64) (uint64, error) {
	var accum uint64
	for i, item := range info.items {
		payload, ok := pendingByID[item.id]
		if !ok {
			continue
		}
		off := base + accum
		n := uint64(len(payload))
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

// closeOpenEndedBox gives the last top-level box of data an explicit size
// when its size field is 0. ISO 14496-12 §4.2: size 0 means "this box
// extends to the end of the file", which stops being true once a box is
// appended after it. data is owned by the caller and edited in place; a size
// that does not fit the 32-bit field (impossible below maxFileSize) is left
// unchanged.
func closeOpenEndedBox(data []byte) {
	pos := 0
	for pos+8 <= len(data) {
		size, _, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			return
		}
		if binary.BigEndian.Uint32(data[pos:]) == 0 && size <= math.MaxUint32 {
			binary.BigEndian.PutUint32(data[pos:], uint32(size))
			return
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
}

// writeHEIFOutput writes the output sections to w: prefix (patched ancestor
// sizes), meta box, suffix, then the new 'mdat' box holding every pending
// payload in item order.
func writeHEIFOutput(w io.Writer, comp *injectComponents) error {
	if _, err := w.Write(comp.outputPrefix); err != nil {
		return fmt.Errorf("heif: write prefix: %w", err)
	}
	if _, err := w.Write(comp.finalMetaBox); err != nil {
		return fmt.Errorf("heif: write meta: %w", err)
	}
	if _, err := w.Write(comp.suffix); err != nil {
		return fmt.Errorf("heif: write suffix: %w", err)
	}
	if _, err := w.Write(comp.mdatHeader[:]); err != nil {
		return fmt.Errorf("heif: write mdat header: %w", err)
	}
	for _, item := range comp.updatedItems {
		if payload, ok := comp.pendingByID[item.id]; ok {
			if _, err := w.Write(payload); err != nil {
				return fmt.Errorf("heif: write item payload: %w", err)
			}
		}
	}
	return nil
}
