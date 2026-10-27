package heif

// oracle_test.go — the whole-file implementation of Inject that task #390
// replaced by the streaming one (stream.go), kept as the test oracle: the
// differential tests and the fuzz target of stream_task390_test.go check that
// the streaming Inject writes the same bytes for every input.
//
// patchAncestorSize, closeOpenEndedBox and the other whole-buffer helpers that
// the streaming write no longer calls live here too: the tests that pin their
// edge cases (task #83, #367, security audit F-02) call them directly.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/internal/bmff"
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
	// mdatBox is the media data box appended after suffix: its header and
	// every pending payload in item order (see buildInjectComponents).
	mdatBox []byte
	// trailer holds the bytes after the last top-level box of the source, when
	// they do not parse as a box. It is written after mdatBox, byte for byte.
	trailer []byte
}

// Inject writes a modified HEIF stream to w with updated metadata items.
//
// Existing Exif and XMP items are rewritten; when the file has no Exif item
// (or no XMP item) and a payload for it is given, the item is created (task
// #309). The iloc box is fully rebuilt; every other item keeps its location,
// shifted when the meta box changes size. Any enclosing container box sizes
// (e.g. moov) are patched if the meta box size changes. New item payloads
// are stored in a new 'mdat' box appended after the last box of the source; bytes
// after that box that do not parse as a box stay at the very end of the file,
// unchanged (task #367).
//
// preserveUnknownSegments must be true. HEIF/AVIF ISOBMFF boxes (ftyp, moov,
// mdat, etc.) are structurally mandatory; there is no concept of an "unknown
// optional segment" analogous to JPEG's APPn. Passing false returns
// ErrPreserveUnknownSegmentsNotSupported.
//
// A write is never silently skipped: when rawEXIF or rawXMP is non-nil, Inject
// either stores it or returns an error. ErrNoMetadataContainer reports a file
// without a valid metadata container box; ErrUnreadableItemLocations reports
// an item location table that is missing or cannot be rewritten without
// dropping items. Only when both payloads are nil is the file copied through
// unchanged. rawIPTC is never stored: HEIF and AVIF have no IPTC carrier, so
// IPTC travels in the XMP packet.
func oracleInject(r io.ReadSeeker, w io.Writer, rawEXIF, _, rawXMP []byte, preserveUnknownSegments bool) error { //nolint:cyclop // sequential guard clauses (option, wire-frame, size cap, pass-through cases); each is a distinct, independently tested branch
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

	// Nothing was requested (rawIPTC is never stored: HEIF/AVIF have no IPTC
	// carrier, IPTC travels in XMP): the only case in which the file is copied
	// through unchanged. Every other branch below either writes the requested
	// payloads or returns an error (task #335).
	if rawEXIF == nil && rawXMP == nil {
		return writePassThrough(w, data)
	}

	metaAbsStart, metaAbsEnd, metaContentOff, found := findMetaBoxAbs(data)
	if !found {
		return ErrNoMetadataContainer
	}

	// data is owned by Inject (iobuf.ReadAll allocates it), so every edit
	// buildInjectComponents makes is made in place — and only once it has
	// decided to rebuild the file, so a refused write leaves no partial output.
	comp, err := buildInjectComponents(data, metaAbsStart, metaAbsEnd, metaContentOff, rawEXIF, rawXMP)
	if err != nil {
		return err
	}

	return writeHEIFOutput(w, comp.outputPrefix, comp.finalMetaBox, comp.suffix, comp.mdatBox, comp.trailer)
}

// buildInjectComponents parses the existing meta box, maps pending items,
// creates the missing Exif/XMP items, computes the final output prefix (with
// patched ancestor sizes and shifted offsets), and builds the final meta box
// with correct iloc offsets. It returns an error when the injection cannot
// proceed (an unreadable iloc box: ErrUnreadableItemLocations); it never
// declines silently. data is edited in place, and only after planInject has
// accepted the file.
//
// Payload placement (task #282, #367): every pending payload is stored in ONE new
// 'mdat' box appended after the last top-level box of the file (before any
// trailing bytes that do not parse as a box). ISO/IEC
// 14496-12 §4.2 requires a file to be a sequence of boxes, and §8.1.1 allows
// any number of 'mdat' boxes; bytes appended after the last box without a box
// header are read by conformant parsers as a malformed box (ExifTool reports
// "Unknown trailer"). Each item's extent offset points inside that box.
//
// Offset shifting (task #309): when the meta box changes size, every byte
// after it moves by the same delta, so every absolute file offset at or
// beyond the old end of the meta box is shifted: the iloc extents of the
// items stored by file offset (construction_method 0) and the stco/co64
// chunk offsets of any track. idat-stored items (construction_method 1) are
// relative to the meta box itself and need no change.
//
// Single-pass design (task #229): the iloc/meta boxes are serialised exactly
// ONCE. Every iloc field has a FIXED byte width once the box's field-size
// nibbles and item/extent counts are known (ISO 14496-12 §8.11.3), so the
// iloc (and therefore meta) box's total LENGTH never depends on the offset or
// length VALUES stored in it. updateIlocItemsInPlace fixes that structure
// upfront, planMetaParts lists every other part of the rebuilt meta box, and
// the final length is pure arithmetic — enough to shift offsets, patch
// ancestor sizes and place the payloads before the iloc bytes exist.
func buildInjectComponents(data []byte, metaAbsStart, metaAbsEnd, metaContentOff int, rawEXIF, rawXMP []byte) (injectComponents, error) {
	// The meta box header is 8 bytes, or 16 for a 64-bit largesize box
	// (ISO 14496-12 §4.2); the rebuilt box keeps the form of the source.
	metaHeaderLen := metaContentOff - fullBoxFieldsLen - metaAbsStart
	plan, err := planInject(data[metaContentOff:metaAbsEnd], metaHeaderLen, rawEXIF, rawXMP)
	if err != nil {
		return injectComponents{}, err
	}
	metaDelta := plan.metaLen - (metaAbsEnd - metaAbsStart)

	// The new payloads are appended as a new top-level box, so a last box
	// that extends to the end of the file must first get an explicit size.
	boxesEnd := closeOpenEndedBox(data)

	// versionFlags is captured before data is edited below (it lies inside
	// the meta box, which no edit touches, but taking it first keeps that
	// independent of the edit order).
	versionFlags := data[metaContentOff-fullBoxFieldsLen : metaContentOff] // 4 bytes: version + flags
	if derr := applyMetaDelta(data, plan.ilocInfo, plan.pendingByID, metaAbsStart, metaAbsEnd, metaContentOff, metaDelta); derr != nil {
		return injectComponents{}, derr
	}

	// The prefix and suffix alias data (no copy): data is not edited again.
	//
	// Trailing bytes (task #367): bytes after the last top-level box that do
	// not parse as a box are kept at the very end of the file, byte for byte,
	// and the new 'mdat' box goes before them. Appended after them, the new
	// box header could be read as the tail of a malformed header that starts in
	// the trailing bytes: a bogus file-level 'meta' box reaching into the 'mdat'
	// would hide the real one (ISO/IEC 14496-12 §4.2: a file is a sequence of
	// boxes, so the new box must start on a box boundary, which only the end
	// of the last box is). An extent that points into the trailing bytes is
	// outside every box and is not shifted.
	outputPrefix := data[:metaAbsStart]
	suffix := data[metaAbsEnd:boxesEnd]
	trailer := data[boxesEnd:]

	mdatStart := uint64(len(outputPrefix)) + uint64(plan.metaLen) + uint64(len(suffix)) //nolint:gosec // G115: lengths are non-negative and bounded by maxFileSize
	mdatBox, err := placePayloads(plan, mdatStart)
	if err != nil {
		return injectComponents{}, err
	}

	plan.parts[plan.ilocIdx] = buildIlocBox(plan.ilocInfo, plan.ilocInfo.items)
	finalMetaBox := buildMetaBox(versionFlags, plan.parts, plan.metaLen, metaHeaderLen)
	if len(finalMetaBox) != plan.metaLen {
		return injectComponents{}, errMetaLayout
	}

	return injectComponents{
		outputPrefix: outputPrefix,
		finalMetaBox: finalMetaBox,
		suffix:       suffix,
		mdatBox:      mdatBox,
		trailer:      trailer,
	}, nil
}

// applyMetaDelta moves every absolute file offset behind the meta box by
// delta, the meta box's size change: the iloc entries of the items that are
// not rewritten (shiftIlocItems), the stco/co64 chunk offsets of every track
// (shiftChunkOffsets), and the size of a box wrapping the meta box
// (patchAncestorSize). data is edited in place. Nothing is done when delta
// is 0.
func applyMetaDelta(data []byte, ilocInfo ilocBoxInfo, pendingByID map[uint16]pendingPayload, metaAbsStart, metaAbsEnd, metaContentOff, delta int) error {
	if delta == 0 {
		return nil
	}
	metaContent := data[metaContentOff:metaAbsEnd]
	threshold := uint64(metaAbsEnd) //nolint:gosec // G115: metaAbsEnd is a non-negative offset into data
	if err := shiftIlocItems(ilocInfo, pendingByID, metaContent, threshold, int64(delta)); err != nil {
		return err
	}
	if err := shiftChunkOffsets(data, metaAbsEnd, int64(delta)); err != nil {
		return err
	}
	// Applied to the whole file (not a truncated prefix copy) so that a box
	// wrapping the meta box, which ends beyond metaAbsStart, is seen whole
	// and its size patched.
	return patchAncestorSize(data, metaAbsStart, delta)
}

// shiftChunkOffsets adds delta, in place, to every stco/co64 chunk offset
// that is at or beyond metaAbsEnd, in every track of every top-level 'moov'
// box of data. ISO 14496-12 §8.7.5: chunk offsets are absolute file offsets,
// so a size change of the meta box moves every chunk stored after it. Only
// trak/mdia/minf/stbl are entered, so the meta box (which carries no chunk
// offsets) is never walked.
func shiftChunkOffsets(data []byte, metaAbsEnd int, delta int64) error {
	threshold := int64(metaAbsEnd)
	pos := 0
	for pos < len(data) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			return nil
		}
		if typ == boxTypeMoov {
			content := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length
			if err := shiftChunkOffsetsIn(content, threshold, delta, 0); err != nil {
				return err
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return nil
}

// errMalformedAncestorBox reports a top-level box whose declared size cannot
// be valid: below its own header length, or an extended size below 16.
var errMalformedAncestorBox = errors.New("heif: malformed box size before the metadata box")

// patchAncestorSize adds delta to the size field of any top-level container
// box in data whose byte range contains metaAbsStart (i.e. wraps the meta
// box). This is needed when meta is nested inside moov and meta changes size.
// data must be the whole file: a box that wraps the meta box ends beyond
// metaAbsStart and is skipped when data is truncated before its end.
//
// Only boxes that START before metaAbsStart are walked: a box that wraps the
// meta box necessarily begins before it, so the boxes after it (and any
// trailing bytes) are never examined (security audit F-02).
//
// Both 32-bit and 64-bit extended-size (size==1) boxes are handled:
//   - 32-bit: if the updated size would exceed 0xFFFFFFFF the patch is skipped
//     (the file was already near the 4 GB ISOBMFF single-box limit; corrupting
//     the size field would be worse than leaving it stale).
//   - 64-bit (size field == 1): the 8-byte largesize at bytes [pos+8:pos+16] is
//     read, incremented by delta, and written back.
//
// A box whose declared size is smaller than its own header never advances the
// walk; it returns errMalformedAncestorBox. ISO/IEC 14496-12 §4.2: size is
// "the entire box, including the box header"; a value of 2..7, or an extended
// size below 16, cannot describe a box. Reference: libheif
// BoxHeader::parse_header rejects the same sizes.
func patchAncestorSize(data []byte, metaAbsStart, delta int) error {
	pos := 0
	for pos+8 <= len(data) && pos < metaAbsStart {
		var (
			advanced int
			done     bool
			err      error
		)
		if binary.BigEndian.Uint32(data[pos:]) == 1 {
			advanced, done, err = patchAncestorSizeExtended(data, pos, metaAbsStart, delta)
		} else {
			advanced, done, err = patchAncestorSize32(data, pos, metaAbsStart, delta)
		}
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if advanced <= pos { // defensive: the walk must always make progress
			return errMalformedAncestorBox
		}
		pos = advanced
	}
	return nil
}

// patchAncestorSizeExtended handles the extended 64-bit size (size==1) branch
// of patchAncestorSize. Returns (newPos, true, nil) when scanning must stop
// (box wraps metaAbsStart or data is truncated); returns (newPos, false, nil)
// to advance.
// ISO 14496-12 §4.2: if size==1, the 64-bit largesize follows the 4-byte type.
func patchAncestorSizeExtended(data []byte, pos, metaAbsStart, delta int) (newPos int, done bool, err error) {
	if pos+16 > len(data) {
		return pos, true, nil
	}
	size64 := binary.BigEndian.Uint64(data[pos+8:])
	if size64 < 16 { // header is 16 bytes: a smaller largesize never advances
		return pos, true, errMalformedAncestorBox
	}
	// Bound by the remaining length, not pos+size64, which can wrap uint64.
	if size64 > uint64(len(data)-pos) { //nolint:gosec // G115: pos <= len(data) (loop guard)
		return pos, true, nil
	}
	boxEnd := pos + int(size64) //nolint:gosec // G115: size64 <= len(data)-pos
	if pos < metaAbsStart && metaAbsStart < boxEnd {
		newSize64 := int64(size64) + int64(delta) //nolint:gosec // G115: delta bounded by meta box size change
		if newSize64 > 0 {
			binary.BigEndian.PutUint64(data[pos+8:], uint64(newSize64))
		}
		return pos, true, nil
	}
	return boxEnd, false, nil
}

// patchAncestorSize32 handles the 32-bit size branch of patchAncestorSize.
// Returns (newPos, true, nil) when scanning must stop; returns
// (newPos, false, nil) to advance to the next box.
func patchAncestorSize32(data []byte, pos, metaAbsStart, delta int) (newPos int, done bool, err error) {
	size := uint64(binary.BigEndian.Uint32(data[pos:]))
	if size == 0 {
		size = uint64(len(data) - pos) //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
	}
	if size < 8 { // header is 8 bytes: 2..7 cannot describe a box
		return pos, true, errMalformedAncestorBox
	}
	if size > uint64(len(data)-pos) { //nolint:gosec // G115: pos <= len(data) (loop guard)
		return pos, true, nil
	}
	boxEnd := pos + int(size) //nolint:gosec // G115: size <= len(data)-pos
	if pos < metaAbsStart && metaAbsStart < boxEnd {
		// Guard against uint32 overflow: if newSize > 0xFFFFFFFF, skip the
		// patch rather than silently truncating. ISO 14496-12 §4.2: boxes
		// > 4 GB must use the extended-size (size==1) form.
		newSize := int64(size) + int64(delta) //nolint:gosec // G115: delta bounded by meta box size change
		if newSize > 0 && uint64(newSize) <= 0xFFFFFFFF {
			binary.BigEndian.PutUint32(data[pos:], uint32(newSize))
		}
		return pos, true, nil
	}
	return boxEnd, false, nil
}

// closeOpenEndedBox gives the last top-level box of data an explicit size
// when its size field is 0. ISO 14496-12 §4.2: size 0 means "this box
// extends to the end of the file", which stops being true once a box is
// appended after it. data is owned by the caller and edited in place; a size
// that does not fit the 32-bit field (impossible below maxFileSize) is left
// unchanged. It returns the offset at which the top-level boxes end: len(data),
// or the start of the first bytes that do not parse as a box.
func closeOpenEndedBox(data []byte) (boxesEnd int) {
	pos := 0
	for pos+8 <= len(data) {
		size, _, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			return pos
		}
		if bmff.OpenEnded(data[pos:]) {
			bmff.CloseInPlace(data[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
			return pos + int(size)                       //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return pos
}

// writeHEIFOutput writes the output sections to w: prefix (patched ancestor
// sizes), meta box, suffix, the new 'mdat' box holding every pending payload,
// then the trailing non-box bytes of the source.
func writeHEIFOutput(w io.Writer, prefix, meta, suffix, mdat, trailer []byte) error {
	if _, err := w.Write(prefix); err != nil {
		return fmt.Errorf("heif: write prefix: %w", err)
	}
	if _, err := w.Write(meta); err != nil {
		return fmt.Errorf("heif: write meta: %w", err)
	}
	if _, err := w.Write(suffix); err != nil {
		return fmt.Errorf("heif: write suffix: %w", err)
	}
	if _, err := w.Write(mdat); err != nil {
		return fmt.Errorf("heif: write item payloads: %w", err)
	}
	if _, err := w.Write(trailer); err != nil {
		return fmt.Errorf("heif: write trailing bytes: %w", err)
	}
	return nil
}
