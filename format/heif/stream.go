package heif

// stream.go — task #390: the HEIF/AVIF write path holds the structures it
// rewrites (the meta box, the moov boxes whose chunk offsets move, the new
// payloads) and streams every other byte of the source from r to w. The file is
// never read into memory, so memory use is proportional to the metadata and
// there is no limit on the size of the file.
//
// The model is the one CR3 uses (format/raw/cr3, streamEdits; ExifTool
// WriteQuickTime.pl: "rewrite the moov atom, copy mdat straight through"): the
// top-level box headers are read through a small window, the boxes that change
// are rebuilt in memory, and the bytes between them are copied with
// iobuf.CopyRange.
//
// The output is, byte for byte, what the whole-file implementation wrote (the
// oracle in oracle_test.go; the differential tests compare them):
//
//	prefix (source up to the meta box, with the size of a box that wraps meta
//	  patched, a last open-ended box closed and the chunk offsets of the moov
//	  boxes shifted) | rebuilt meta box | suffix (source after the meta box up to
//	  the end of the last box, likewise edited) | new 'mdat' box with the
//	  payloads | trailing bytes that are not a box.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/internal/bmff"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// errOpenEndedBoxTooLarge is returned when the last box of the source extends to
// the end of the file (size 0, ISO 14496-12 §4.2), needs an explicit size
// because a box is appended after it, and is too large for the 32-bit size
// field. Switching to the 64-bit header would move every byte after it.
var errOpenEndedBoxTooLarge = errors.New("heif: the last box of the file extends to its end and is too large to be given an explicit size")

// boxInfo is a box of a walk: its offset, its declared size (resolved against
// its container when 0), its header length and its type.
type boxInfo struct {
	start     int64
	size      uint64
	headerLen uint64
	typ       [4]byte
	openEnded bool // the size field says "to the end of the container"
}

// end returns the offset one past the last byte of the box.
func (b boxInfo) end() int64 { return b.start + int64(b.size) } //nolint:gosec // G115: size <= the remaining bytes of the container (parseBoxHeader)

// hdrLen returns the header length (8, or 16 for a 64-bit largesize box) as an int.
func (b boxInfo) hdrLen() int { return int(b.headerLen) } //nolint:gosec // G115: headerLen is 8 or 16

// content returns the offset of the first byte after the header.
func (b boxInfo) content() int64 { return b.start + int64(b.headerLen) } //nolint:gosec // G115: headerLen is 8 or 16

// boxWalker iterates over the boxes of a range of the source by their headers,
// read through a window: a box that does not parse ends the walk, as it ends
// the flat scans of the in-memory parser (parseHEIFBoxHeader).
type boxWalker struct {
	win        *iobuf.Window
	pos, limit int64
}

// next returns the box at the current position, or false when the remaining
// bytes of the range do not hold a valid box header.
func (bw *boxWalker) next() (b boxInfo, ok bool, err error) {
	remaining := bw.limit - bw.pos
	if remaining < 8 {
		return b, false, nil
	}
	head, err := bw.win.Peek(bw.pos, 16)
	if err != nil {
		return b, false, fmt.Errorf("heif: read box header: %w", err)
	}
	head = head[:min(int64(len(head)), remaining)]
	size, typ, hl, valid := parseBoxHeader(head, uint64(remaining))
	if !valid {
		return b, false, nil
	}
	b = boxInfo{start: bw.pos, size: size, headerLen: hl, typ: typ, openEnded: bmff.OpenEnded(head)}
	bw.pos += int64(size) //nolint:gosec // G115: size <= remaining (parseBoxHeader)
	return b, true, nil
}

// metaLocation is the selected meta box (findMetaBoxAbs: the first file-level
// meta box, else the first meta box that is a direct child of the first moov).
type metaLocation struct {
	found                  bool
	start, end, contentOff int64
	// wrapStart is the offset of the top-level moov box that holds the meta box,
	// or -1 when the meta box is file-level.
	wrapStart int64
}

// topLayout is what the first pass over the top-level boxes finds.
type topLayout struct {
	meta metaLocation
	// boxesEnd is the offset at which the top-level boxes end: the end of the
	// source, or the start of the first bytes that do not parse as a box.
	boxesEnd int64
	// openEnded: the last box has size 0; it starts at openStart and needs an
	// explicit size once a box is appended after it.
	openEnded bool
	openStart int64
	moovCount int
}

// validMeta reports whether a meta box of header length hl that spans
// [start, end) can hold the FullBox fields (ISO 14496-12 §8.11.1: meta is a
// FullBox, so size >= header + 4; #169 fix).
func validMeta(start, end int64, hl uint64) bool {
	return end-start >= int64(hl)+fullBoxFieldsLen //nolint:gosec // G115: hl is 8 or 16
}

// topScanner accumulates what scanTopLevel learns from the top-level boxes.
type topScanner struct {
	lay       topLayout
	firstMoov boxInfo
	haveMoov  bool
	sawMeta   bool
}

// add takes the next top-level box. It reports stop for the box that extends
// to the end of the file, the last one.
func (sc *topScanner) add(win *iobuf.Window, b boxInfo) (stop bool, err error) {
	switch {
	case b.typ == boxTypeMeta && !sc.sawMeta:
		// The first file-level meta box is THE meta box; one that is too small
		// for a FullBox is not skipped in favour of a moov-nested one.
		sc.sawMeta = true
		if validMeta(b.start, b.end(), b.headerLen) {
			sc.lay.meta = metaLocation{found: true, start: b.start, end: b.end(), contentOff: b.content() + fullBoxFieldsLen, wrapStart: -1}
		}
	case b.typ == boxTypeMoov:
		sc.lay.moovCount++
		if !sc.haveMoov {
			sc.firstMoov, sc.haveMoov = b, true
		}
	}
	if !b.openEnded {
		return false, nil
	}
	if err := checkClosable(win, b); err != nil {
		return true, err
	}
	sc.lay.openEnded, sc.lay.openStart = true, b.start
	return true, nil
}

// scanTopLevel makes the first pass: it reads the top-level box headers of the
// source, selects the meta box (findMetaBoxAbs, task #349), finds where the
// top-level boxes end and whether the last one is open-ended
// (closeOpenEndedBox, task #367).
func scanTopLevel(win *iobuf.Window, fileLen int64) (topLayout, error) {
	sc := topScanner{}
	sc.lay.meta.wrapStart = -1
	bw := boxWalker{win: win, limit: fileLen}
	for {
		b, ok, err := bw.next()
		if err != nil || !ok {
			if err != nil {
				return sc.lay, err
			}
			break
		}
		stop, err := sc.add(win, b)
		if err != nil {
			return sc.lay, err
		}
		if stop {
			break
		}
	}
	sc.lay.boxesEnd = bw.pos
	if !sc.sawMeta && sc.haveMoov {
		var err error
		sc.lay.meta, err = nestedMeta(win, sc.firstMoov)
		return sc.lay, err
	}
	return sc.lay, nil
}

// nestedMeta looks for the first meta box that is a direct child of moov.
func nestedMeta(win *iobuf.Window, moov boxInfo) (metaLocation, error) {
	inner := boxWalker{win: win, pos: moov.content(), limit: moov.end()}
	for {
		c, ok, err := inner.next()
		if err != nil || !ok {
			return metaLocation{wrapStart: -1}, err
		}
		if c.typ != boxTypeMeta {
			continue
		}
		if !validMeta(c.start, c.end(), c.headerLen) {
			return metaLocation{wrapStart: -1}, nil
		}
		return metaLocation{found: true, start: c.start, end: c.end(), contentOff: c.content() + fullBoxFieldsLen, wrapStart: moov.start}, nil
	}
}

// checkClosable reports errOpenEndedBoxTooLarge when the open-ended box b
// cannot be given an explicit size in its own header.
func checkClosable(win *iobuf.Window, b boxInfo) error {
	head, err := win.Peek(b.start, b.hdrLen())
	if err != nil {
		return fmt.Errorf("heif: read box header: %w", err)
	}
	var hdr [16]byte
	copy(hdr[:], head)
	if !bmff.CloseHeader(hdr[:b.hdrLen()], b.size) {
		return errOpenEndedBoxTooLarge
	}
	return nil
}

// patchBoxSize adds delta to the size field of the box header hdr, which has
// the declared size size: the 32-bit field of a normal box, or the 64-bit
// largesize of an extended one (ISO 14496-12 §4.2). A new size that does not fit
// the 32-bit field leaves the field as it is: the file was already near the 4 GiB
// limit of a normal box, and a wrong size would be worse than a stale one.
func patchBoxSize(hdr []byte, size uint64, delta int) {
	newSize := int64(size) + int64(delta) //nolint:gosec // G115: size is bounded by the source, delta by the meta box size change
	if binary.BigEndian.Uint32(hdr) == 1 {
		if newSize > 0 {
			binary.BigEndian.PutUint64(hdr[8:], uint64(newSize))
		}
		return
	}
	if newSize > 0 && uint64(newSize) <= math.MaxUint32 {
		binary.BigEndian.PutUint32(hdr, uint32(newSize))
	}
}

// injector holds the state of one streaming write.
type injector struct {
	r       io.ReadSeeker
	w       io.Writer
	win     *iobuf.Window
	fileLen int64
	lay     topLayout

	// delta is the size change of the meta box; threshold the old end of the meta
	// box: every absolute offset at or beyond it moves by delta.
	delta     int
	threshold int64

	finalMeta []byte // the rebuilt meta box
	mdat      []byte // the new 'mdat' box with the payloads
	moov      []byte // scratch: the content of the moov box being patched
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
// Task #390: the file is never held in memory. r must be seekable: the box
// headers are read through a small window, the structures that change (the meta
// box, the moov boxes whose chunk offsets move, the payloads) are built in
// memory, and everything else is copied from r to w through a fixed pooled
// buffer. Memory use is therefore proportional to the metadata, and there is no
// limit on the size of the file; the boxes held in memory are limited to
// maxFileSize each (ErrFileTooLarge). Every error that can be foreseen from the
// structures is returned before the first byte is written; a source that ends
// before the size it reported makes Inject return an error after part of the
// output has been written (Write has the same trust boundary).
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
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error { //nolint:cyclop,gocyclo // sequential guard clauses (option, wire-frame, pass-through, planning); each is a distinct, independently tested branch
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
	fileLen, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("heif: seek: %w", err)
	}

	// Nothing was requested (rawIPTC is never stored: HEIF/AVIF have no IPTC
	// carrier, IPTC travels in XMP): the only case in which the file is copied
	// through unchanged. Every other branch below either writes the requested
	// payloads or returns an error (task #335).
	if rawEXIF == nil && rawXMP == nil {
		if err = iobuf.CopyRange(r, w, 0, fileLen); err != nil {
			return fmt.Errorf("heif: copy: %w", err)
		}
		return nil
	}

	win := iobuf.NewWindow(r, fileLen)
	defer win.Close()
	in := injector{r: r, w: w, win: win, fileLen: fileLen}
	if in.lay, err = scanTopLevel(win, fileLen); err != nil {
		return fmt.Errorf("heif: read: %w", err)
	}
	if !in.lay.meta.found {
		return ErrNoMetadataContainer
	}
	if perr := in.plan(rawEXIF, rawXMP); perr != nil {
		return perr
	}
	return in.emit()
}

// readAt reads n bytes of the source at off into buf (grown when needed) and
// returns them. what names the structure for the error of a box over the limit.
func (in *injector) readAt(buf []byte, off, n int64, what string) ([]byte, error) {
	if n > maxFileSize {
		return nil, fmt.Errorf("heif: %s box of %d bytes exceeds the %d byte limit: %w", what, n, maxFileSize, ErrFileTooLarge)
	}
	if int64(cap(buf)) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := in.r.Seek(off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("heif: seek: %w", err)
	}
	if _, err := io.ReadFull(in.r, buf); err != nil {
		return nil, fmt.Errorf("heif: read %s box: %w", what, err)
	}
	return buf, nil
}

// plan reads the meta box, plans the rewrite of its items, and validates the
// edits of the other structures, so that everything that can fail has failed
// before the first byte is written. It sets in.delta, in.threshold, in.finalMeta
// and in.mdat.
//
// Offset shifting (task #309): when the meta box changes size, every byte after
// it moves by the same delta, so every absolute file offset at or beyond the
// old end of the meta box is shifted: the iloc extents of the items stored by
// file offset (construction_method 0) and the stco/co64 chunk offsets of any
// track. idat-stored items (construction_method 1) are relative to the meta box
// itself and need no change.
//
// Payload placement (task #282, #367): every pending payload is stored in ONE
// new 'mdat' box appended after the last top-level box of the file (before any
// trailing bytes that do not parse as a box). ISO/IEC 14496-12 §4.2 requires a
// file to be a sequence of boxes, and §8.1.1 allows any number of 'mdat' boxes;
// bytes appended after the last box without a box header are read by conformant
// parsers as a malformed box (ExifTool reports "Unknown trailer"). Each item's
// extent offset points inside that box.
//
// Single-pass design (task #229): the iloc/meta boxes are serialised exactly
// ONCE. Every iloc field has a FIXED byte width once the box's field-size
// nibbles and item/extent counts are known (ISO 14496-12 §8.11.3), so the
// iloc (and therefore meta) box's total LENGTH never depends on the offset or
// length VALUES stored in it. updateIlocItemsInPlace fixes that structure
// upfront, planMetaParts lists every other part of the rebuilt meta box, and
// the final length is pure arithmetic — enough to shift offsets, patch
// ancestor sizes and place the payloads before the iloc bytes exist.
func (in *injector) plan(rawEXIF, rawXMP []byte) error {
	meta := in.lay.meta
	// The version and flags of the FullBox, then its content.
	metaBuf, err := in.readAt(nil, meta.contentOff-fullBoxFieldsLen, meta.end-meta.contentOff+fullBoxFieldsLen, "meta")
	if err != nil {
		return err
	}
	versionFlags, metaContent := metaBuf[:fullBoxFieldsLen], metaBuf[fullBoxFieldsLen:]

	// The meta box header is 8 bytes, or 16 for a 64-bit largesize box
	// (ISO 14496-12 §4.2); the rebuilt box keeps the form of the source.
	metaHeaderLen := int(meta.contentOff - fullBoxFieldsLen - meta.start)
	plan, err := planInject(metaContent, metaHeaderLen, rawEXIF, rawXMP)
	if err != nil {
		return err
	}
	in.delta = plan.metaLen - int(meta.end-meta.start)
	in.threshold = meta.end

	if in.delta != 0 {
		if serr := shiftIlocItems(plan.ilocInfo, plan.pendingByID, metaContent, uint64(meta.end), int64(in.delta)); serr != nil { //nolint:gosec // G115: meta.end is a non-negative offset into the source
			return serr
		}
		if verr := in.validateMoovShifts(); verr != nil {
			return verr
		}
	}

	// The new box goes right after the last top-level box, which the meta box
	// ends at or before: the bytes after the meta box move by delta.
	if in.lay.boxesEnd < meta.end {
		return errMetaLayout
	}
	mdatStart := uint64(in.lay.boxesEnd) + uint64(int64(in.delta)) //nolint:gosec // G115: boxesEnd is non-negative; boxesEnd+delta is the non-negative output offset of the new box
	if in.mdat, err = placePayloads(plan, mdatStart); err != nil {
		return err
	}

	plan.parts[plan.ilocIdx] = buildIlocBox(plan.ilocInfo, plan.ilocInfo.items)
	in.finalMeta = buildMetaBox(versionFlags, plan.parts, plan.metaLen, metaHeaderLen)
	if len(in.finalMeta) != plan.metaLen {
		return errMetaLayout
	}
	return nil
}

// walkMoov calls fn for every top-level moov box of the source, in order.
func (in *injector) walkMoov(fn func(b boxInfo) error) error {
	bw := boxWalker{win: in.win, limit: in.fileLen}
	for {
		b, ok, err := bw.next()
		if err != nil || !ok {
			return err
		}
		if b.typ != boxTypeMoov {
			continue
		}
		if ferr := fn(b); ferr != nil {
			return ferr
		}
	}
}

// loadMoov reads the content of the moov box b into in.moov.
func (in *injector) loadMoov(b boxInfo) error {
	var err error
	in.moov, err = in.readAt(in.moov, b.content(), int64(b.size-b.headerLen), "moov") //nolint:gosec // G115: size >= headerLen
	return err
}

// validateMoovShifts shifts the chunk offsets of every moov box, in memory and
// without writing, to find the errors of the shift (an offset that no longer
// fits its field) before any output is written. emit repeats the shift.
func (in *injector) validateMoovShifts() error {
	if in.lay.moovCount == 0 {
		return nil
	}
	return in.walkMoov(in.shiftedMoov)
}

// write writes p to the destination.
func (in *injector) write(p []byte, what string) error {
	if _, err := in.w.Write(p); err != nil {
		return fmt.Errorf("heif: write %s: %w", what, err)
	}
	return nil
}

// copyTo copies the source bytes [from, to) to the destination.
func (in *injector) copyTo(from, to int64, what string) error {
	if err := iobuf.CopyRange(in.r, in.w, from, to-from); err != nil {
		return fmt.Errorf("heif: copy %s: %w", what, err)
	}
	return nil
}

// emit writes the output: the source with its top-level boxes edited, then the
// new 'mdat' box, then the trailing bytes.
func (in *injector) emit() error {
	cursor := int64(0) // the first source byte that is not yet written
	bw := boxWalker{win: in.win, limit: in.fileLen}
	for {
		b, ok, err := bw.next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if cursor, err = in.emitBox(cursor, b); err != nil {
			return err
		}
	}
	if err := in.copyTo(cursor, in.lay.boxesEnd, "boxes"); err != nil {
		return err
	}
	if err := in.write(in.mdat, "item payloads"); err != nil {
		return err
	}
	return in.copyTo(in.lay.boxesEnd, in.fileLen, "trailing bytes")
}

// emitBox writes the top-level box b with its edits, if it has any, and returns
// the new cursor: the first source byte not yet written. A box with no edit stays
// in the range that is copied.
func (in *injector) emitBox(cursor int64, b boxInfo) (int64, error) {
	switch {
	case in.lay.meta.wrapStart < 0 && b.start == in.lay.meta.start:
		if err := in.copyTo(cursor, b.start, "boxes before meta"); err != nil {
			return cursor, err
		}
		return b.end(), in.write(in.finalMeta, "meta")
	case b.typ == boxTypeMoov:
		return in.emitMoov(cursor, b)
	case b.openEnded:
		// The last box gets an explicit size: a box follows it now.
		return in.emitClosedHeader(cursor, b)
	}
	return cursor, nil
}

// header returns a copy of the header of b, with the size of an open-ended box
// closed (bmff.CloseHeader) and, for the moov box that wraps the meta box, the
// size patched by the size change of the meta box (ISO 14496-12 §4.2).
func (in *injector) header(b boxInfo) ([16]byte, error) {
	var hdr [16]byte
	head, err := in.win.Peek(b.start, b.hdrLen())
	if err != nil {
		return hdr, fmt.Errorf("heif: read box header: %w", err)
	}
	copy(hdr[:], head)
	if b.openEnded && !bmff.CloseHeader(hdr[:b.hdrLen()], b.size) {
		return hdr, errOpenEndedBoxTooLarge // foreseen by scanTopLevel
	}
	if in.delta != 0 && b.start == in.lay.meta.wrapStart {
		patchBoxSize(hdr[:b.hdrLen()], b.size, in.delta)
	}
	return hdr, nil
}

// emitClosedHeader writes the source up to b, then its header with an explicit
// size, and returns the new cursor (the end of the header).
func (in *injector) emitClosedHeader(cursor int64, b boxInfo) (int64, error) {
	hdr, err := in.header(b)
	if err != nil {
		return cursor, err
	}
	if err := in.copyTo(cursor, b.start, "boxes"); err != nil {
		return cursor, err
	}
	return b.content(), in.write(hdr[:b.hdrLen()], "box header")
}

// emitMoov writes the moov box b with its edits and returns the new cursor. The
// edits are the size of a moov box that wraps the meta box (header), the chunk
// offsets behind the meta box (stco/co64 tables in the content) and the
// replacement of a nested meta box.
func (in *injector) emitMoov(cursor int64, b boxInfo) (int64, error) {
	wrapper := b.start == in.lay.meta.wrapStart
	switch {
	case in.delta == 0 && !wrapper && !b.openEnded:
		return cursor, nil
	case in.delta == 0 && !wrapper: // only the size of an open-ended box changes
		return in.emitClosedHeader(cursor, b)
	}
	hdr, err := in.header(b)
	if err != nil {
		return cursor, err
	}
	err = in.shiftedMoov(b)
	if err != nil {
		return cursor, err
	}
	err = in.copyTo(cursor, b.start, "boxes")
	if err != nil {
		return cursor, err
	}
	err = in.write(hdr[:b.hdrLen()], "moov header")
	if err != nil {
		return cursor, err
	}
	return b.end(), in.writeMoovContent(b, wrapper)
}

// shiftedMoov loads the content of the moov box b into in.moov and, when the
// meta box changed size, shifts its chunk offsets.
func (in *injector) shiftedMoov(b boxInfo) error {
	if err := in.loadMoov(b); err != nil {
		return err
	}
	if in.delta == 0 {
		return nil
	}
	return shiftChunkOffsetsIn(in.moov, in.threshold, int64(in.delta), 0)
}

// writeMoovContent writes the content of the moov box b, held in in.moov. When
// the box wraps the meta box, the meta box inside it is replaced.
func (in *injector) writeMoovContent(b boxInfo, wrapper bool) error {
	if !wrapper {
		return in.write(in.moov, "moov")
	}
	content, meta := b.content(), in.lay.meta
	if err := in.write(in.moov[:meta.start-content], "moov"); err != nil {
		return err
	}
	if err := in.write(in.finalMeta, "meta"); err != nil {
		return err
	}
	return in.write(in.moov[meta.end-content:], "moov")
}
