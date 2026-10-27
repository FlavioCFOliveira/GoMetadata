package jpeg

// splice.go — the splice-and-copy write path of Inject (tasks #400, #403).
//
// Inject does not parse the file again to write it. A header-only scan
// (scanSplice) reads the marker segments before the first scan header, records
// where the metadata segments are, and stops there; the write then copies the
// source to the output byte for byte, except for the old metadata segments it
// leaves out and the new ones it puts in:
//
//   - A new segment replaces the first old segment of its kind in place; later
//     old segments of the kind are dropped (the output holds one).
//   - A new segment of a kind the source has none of goes after the JFIF APP0
//     segment and the segments that must keep their place after it (see
//     scanSplice), after the EXIF segment when the source has one and, for
//     IPTC, after the XMP. Exif 3.1 (CIPA DC-008-2026) §4.5.4, p. 20, and Table
//     4, p. 22, put the Exif APP1 immediately after SOI; a file with a JFIF
//     segment cannot satisfy that and ITU-T T.871 §6.3 together, and follows
//     JFIF, as ExifTool does (task #400). The Exif APP1 stays the first
//     APP1 segment, and the others follow it.
//   - Everything else, the segment order, the fill bytes, every other APPn, the
//     tables, the scan header, the image data and the trailers after it, is
//     copied as it is. The image data is never parsed. A source whose marker
//     stream is damaged before the first scan header (a segment that runs past
//     the end of the file, a missing marker prefix) is copied as it is from the
//     damaged byte on: the write adds its metadata and does not decide whether
//     the rest is a JPEG.
//
// The AFCP and FotoStation trailers after the image (task #322) are rewritten
// from the plan startTrailerPlan made, as before.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

// Additional APPn markers the scan recognises (ISO/IEC 10918-1 §B.2.4.6).
const (
	markerAPP0  byte = 0xE0
	markerAPP14 byte = 0xEE
)

// Identifiers of the segments that precede the metadata of a JPEG.
var (
	identJFIF  = []byte("JFIF\x00") //nolint:gochecknoglobals // package-level constant bytes
	identJFXX  = []byte("JFXX\x00") //nolint:gochecknoglobals // package-level constant bytes
	identAdobe = []byte("Adobe")    //nolint:gochecknoglobals // package-level constant bytes
)

// maxSpliceDrops bounds the number of separate runs of segments one write
// leaves out. A file has a handful (the old metadata segments); the bound only
// keeps a crafted file, whose every other segment is an old metadata segment,
// from growing the plan with the size of the file.
const maxSpliceDrops = 1 << 16

// scanHeadLen is the number of payload bytes the scan reads of a segment that
// it classifies: more than the longest identifier it compares (the extended XMP
// identifier, 35 bytes, and the GUID after it, 32 bytes).
const scanHeadLen = 80

// segKind is the kind of metadata segment, in the order the write puts them.
type segKind uint8

// The metadata segment kinds. kindNone is any other segment.
const (
	kindEXIF segKind = iota // APP1 "Exif\0\0"
	kindXMP                 // APP1 XMP packet or extended XMP
	kindPS                  // APP13 "Photoshop 3.0\0" (IPTC and the other resources)
	numKinds
	kindNone = numKinds
)

// oldSeg is a metadata segment of the source: [start, end) with the fill bytes
// before its marker, and the position of its payload.
type oldSeg struct {
	start, dataStart, end int64
	ok                    bool
}

// spliceDrop is a run of segments of the source that the write leaves out.
type spliceDrop struct{ start, end int64 }

// splicePlan is what scanSplice learns about the source.
type splicePlan struct {
	size int64
	// insertAt is the offset at which new metadata goes when no old segment
	// places it: after the SOI marker and the JFIF APP0 segment (T.871 §6.3).
	insertAt int64
	// tailStart is the offset of the first byte that is copied without being
	// read: the first scan header, the EOI marker or the first byte the scan
	// could not read as a segment.
	tailStart int64
	// sosEnd is the offset after the first scan header; -1 when the scan found
	// none.
	sosEnd int64
	// first is the first old segment of each kind, kept or dropped.
	first [numKinds]oldSeg
	// dropAt is the offset of the first dropped segment of each kind; -1 when
	// the write drops none.
	dropAt [numKinds]int64
	drops  []spliceDrop
	// dropsArr backs drops for a file with few: the plan then allocates
	// nothing.
	dropsArr [8]spliceDrop
}

// splicePlanPool recycles plans: a plan holds its own backing arrays, which the
// compiler cannot keep on the stack, so one is drawn from the pool per write
// instead of being allocated.
var splicePlanPool = sync.Pool{New: func() any { return new(splicePlan) }} //nolint:gochecknoglobals // sync.Pool: reuse avoids a heap allocation per Inject call

// getSplicePlan returns a plan from the pool; scanSplice initialises it.
func getSplicePlan() *splicePlan {
	p, _ := splicePlanPool.Get().(*splicePlan) // comma-ok: New always stores *splicePlan; pool invariant
	return p
}

// putSplicePlan returns p to the pool, dropping the reference to a run list that
// outgrew the backing array.
func putSplicePlan(p *splicePlan) {
	p.drops = nil
	splicePlanPool.Put(p)
}

// payloads are the segments the write puts in: nil for a segment that is left
// out or kept.
type payloads struct {
	rawEXIF, rawIPTC, rawXMP, origIRB []byte
}

// has reports whether the write puts a segment of kind k in. For the
// Photoshop segment that includes the sibling resources of the source
// (task #174: a nil rawIPTC removes the IPTC resource only).
func (p *payloads) has(k segKind) bool {
	switch k {
	case kindEXIF:
		return p.rawEXIF != nil
	case kindXMP:
		return p.rawXMP != nil
	case kindPS:
		return p.rawIPTC != nil || p.origIRB != nil
	case kindNone:
	}
	return false
}

// emit writes the segments of kind k.
func (p *payloads) emit(w io.Writer, k segKind) error {
	switch k {
	case kindEXIF:
		return writeEXIFSegment(w, p.rawEXIF)
	case kindXMP:
		return writeXMPSegments(w, p.rawXMP)
	case kindPS:
		return writeIPTCOrSiblings(w, p.rawIPTC, p.origIRB, p.rawXMP != nil)
	case kindNone:
	}
	return nil
}

// spliceOp is one step of the write: put in the segments of kind k at offset
// off (end == off), or, for kindNone, leave out [off, end) of the source.
type spliceOp struct {
	off, end int64
	kind     segKind
}

// scanSplice reads the marker segments of the source before the first scan
// header into p and leaves cr at an unspecified position. keepGUID and keep
// are those of the write (see dropsSegmentKeeping): the segments they name are
// copied, never dropped.
//
// A segment is read by its header (marker and length, JPEG ISO/IEC 10918-1
// §B.1.1.4) and, for the markers that can be metadata or place it, the first
// bytes of its payload; the rest of the payload is skipped. The scan stops, and
// the source is copied from there, at the first scan header, at the EOI marker,
// and at the first byte that does not start a segment that lies inside the file
// (no marker prefix, a length below 2, a segment that runs past the end).
//
// The insertion offset follows the JFIF APP0 segment and the segments that
// follow it directly and keep their place after it: JFIF extension APP0
// segments (ITU-T T.871 | ISO/IEC 10918-5, §6.3, p. 3: the JFIF APP0 segment
// "shall immediately follow the SOI marker"; §10.2, p. 7: JFIF extension APP0
// segments "may immediately follow the JFIF APP0 marker segment") and the
// Adobe APP14 segment (Adobe Technical Note #5116: a JPEG written by Adobe
// software, with no JFIF segment, starts with it).
//
//nolint:cyclop,gocyclo // a header walk: each branch is one way a segment ends or is classified
func scanSplice(cr *countingReader, scratch *[]byte, p *splicePlan, preserveUnknownSegments bool, keepGUID string, keep Keep) error {
	if len(*scratch) < scanHeadLen+4 {
		*scratch = make([]byte, 4096)
	}
	size, err := cr.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}
	if _, err = cr.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}
	soi := (*scratch)[:2]
	if _, err = io.ReadFull(cr, soi); err != nil {
		return fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if soi[0] != 0xFF || soi[1] != markerSOI {
		return ErrNotJPEG
	}
	*p = splicePlan{size: size, insertAt: 2, tailStart: size, sosEnd: -1}
	p.drops = p.dropsArr[:0]
	for k := range p.dropAt {
		p.dropAt[k] = -1
	}

	pos := int64(2)
	inRun := true
	for {
		segStart := pos
		h, herr := readSegHead(cr, scratch, segStart, size)
		if herr != nil {
			return herr
		}
		switch {
		case !h.ok || h.marker == markerEOI:
			p.tailStart = segStart
			return nil
		case h.marker == markerSOS:
			p.tailStart, p.sosEnd = segStart, h.end
			return nil
		case h.standalone: // RSTn, TEM, a second SOI: no length, copied
			pos, inRun = h.end, false
			continue
		}
		pos = h.end

		marker, head := h.marker, h.head
		kind := classifySegment(marker, head)
		if kind != kindNone {
			if !p.first[kind].ok {
				p.first[kind] = oldSeg{start: segStart, dataStart: h.hdrEnd + 2, end: h.end, ok: true}
			}
		}
		drop := dropsSegmentKeeping(marker, head, preserveUnknownSegments, keepGUID, keep)
		if drop {
			if kind != kindNone && p.dropAt[kind] < 0 {
				p.dropAt[kind] = segStart
			}
			if !p.addDrop(segStart, h.end) {
				return ErrTooManySegments
			}
		}
		if inRun {
			if isLeadingSegment(marker, head) {
				p.insertAt = h.end
			} else {
				inRun = false
			}
		}
	}
}

// segHead is what the scan reads of the segment that starts at an offset.
type segHead struct {
	// ok is false when no segment lies there: the bytes do not start with a
	// marker prefix, the length is below 2, the segment runs past the end of the
	// file, or the stream ends. The other fields are then unset.
	ok         bool
	standalone bool   // the marker has no length field
	marker     byte   // the marker byte, after any fill bytes
	hdrEnd     int64  // offset after the marker
	end        int64  // offset after the segment (hdrEnd for a standalone marker)
	head       []byte // the first bytes of the payload of an APP0, APP1, APP13 or APP14 segment; aliases the scratch buffer
}

// readSegHead reads the header of the segment at offset pos of cr (cr is at pos)
// and leaves cr at its end, except for the first scan header (cr is then at an
// unspecified position, as the scan stops there). The common segment, with no
// fill bytes before its marker, is read with one call that also takes the first
// bytes of its payload; fill bytes, standalone markers and a segment that
// cannot be read take the slow path, which reads the pieces one by one. err is
// non-nil only for ErrFileTooLarge or a failed seek.
//
//nolint:cyclop,gocyclo // a header decoder: each branch is one way a header is read or refused
func readSegHead(cr *countingReader, scratch *[]byte, pos, size int64) (h segHead, err error) {
	want := max(min(int64(4+scanHeadLen), size-pos), 0)
	buf := (*scratch)[:want]
	n, rerr := io.ReadFull(cr, buf)
	if rerr != nil {
		if errors.Is(rerr, ErrFileTooLarge) {
			return h, fmt.Errorf("jpeg: read segment header: %w", rerr)
		}
		n = 0 // a read that fails is read again by the slow path
		if serr := cr.rewindTo(pos); serr != nil {
			return h, serr
		}
	}
	if n >= 4 && buf[0] == 0xFF && buf[1] != 0xFF && buf[1] != 0 && !isStandalone(buf[1]) {
		marker := buf[1]
		length := int64(buf[2])<<8 | int64(buf[3])
		end := pos + 2 + length
		if length < 2 || end > size {
			return h, nil
		}
		h = segHead{ok: true, marker: marker, hdrEnd: pos + 2, end: end}
		if marker == markerSOS {
			return h, nil
		}
		if hl := payloadHeadLen(marker, length-2); hl > 0 {
			h.head = buf[4 : 4+hl]
		}
		if serr := cr.skip(end - (pos + int64(n))); serr != nil {
			return h, serr
		}
		return h, nil
	}
	if n > 0 {
		if serr := cr.skip(-int64(n)); serr != nil {
			return h, serr
		}
	}
	return readSegHeadSlow(cr, scratch, pos, size)
}

// readSegHeadSlow is readSegHead for a header that has fill bytes before the
// marker, a standalone marker or a segment that cannot be read: the pieces are
// read one by one.
//
//nolint:cyclop,gocyclo // a header decoder: each branch is one way a header is read or refused
func readSegHeadSlow(cr *countingReader, scratch *[]byte, pos, size int64) (h segHead, err error) {
	hdrEnd, marker, ok, err := readMarker(cr, scratch, pos)
	if err != nil || !ok || marker == 0 {
		return h, err
	}
	if marker == markerEOI {
		return segHead{ok: true, marker: marker, hdrEnd: hdrEnd, end: hdrEnd}, nil
	}
	if isStandalone(marker) {
		return segHead{ok: true, standalone: true, marker: marker, hdrEnd: hdrEnd, end: hdrEnd}, nil
	}
	lenB := (*scratch)[:2]
	if _, rerr := io.ReadFull(cr, lenB); rerr != nil {
		if errors.Is(rerr, ErrFileTooLarge) {
			return h, fmt.Errorf("jpeg: read segment length: %w", rerr)
		}
		return h, nil
	}
	length := int64(lenB[0])<<8 | int64(lenB[1])
	end := hdrEnd + length
	if length < 2 || end > size {
		return h, nil
	}
	h = segHead{ok: true, marker: marker, hdrEnd: hdrEnd, end: end}
	if marker == markerSOS {
		return h, nil
	}
	need := length - 2
	consumed := payloadHeadLen(marker, need)
	if consumed > 0 {
		head := (*scratch)[:consumed]
		if _, rerr := io.ReadFull(cr, head); rerr != nil {
			if errors.Is(rerr, ErrFileTooLarge) {
				return segHead{}, fmt.Errorf("jpeg: read segment: %w", rerr)
			}
			return segHead{}, nil
		}
		h.head = head
	}
	if need > consumed {
		if serr := cr.skip(need - consumed); serr != nil {
			return segHead{}, serr
		}
	}
	return h, nil
}

// readMarker reads the marker that starts at offset pos of cr (cr is at pos):
// its fill bytes (JPEG ISO/IEC 10918-1 §B.1.1.2) and the marker byte. hdrEnd is
// the offset after the marker. ok is false when there is no marker there: the
// byte at pos is not 0xFF, or the stream ends. err is non-nil only for
// ErrFileTooLarge.
func readMarker(cr *countingReader, scratch *[]byte, pos int64) (hdrEnd int64, marker byte, ok bool, err error) {
	b := (*scratch)[:2]
	if _, rerr := io.ReadFull(cr, b); rerr != nil {
		if errors.Is(rerr, ErrFileTooLarge) {
			return 0, 0, false, fmt.Errorf("jpeg: read marker: %w", rerr)
		}
		return 0, 0, false, nil
	}
	if b[0] != 0xFF {
		return 0, 0, false, nil
	}
	hdrEnd = pos + 2
	for b[1] == 0xFF {
		if _, rerr := io.ReadFull(cr, b[1:2]); rerr != nil {
			if errors.Is(rerr, ErrFileTooLarge) {
				return 0, 0, false, fmt.Errorf("jpeg: read marker: %w", rerr)
			}
			return 0, 0, false, nil
		}
		hdrEnd++
	}
	return hdrEnd, b[1], true, nil
}

// payloadHeadLen returns the number of payload bytes the scan reads of a segment
// with the marker and a payload of need bytes: the first scanHeadLen of an APP0,
// APP1, APP13 or APP14 segment (the markers that can be metadata or place it),
// none of any other.
func payloadHeadLen(marker byte, need int64) int64 {
	switch marker {
	case markerAPP0, markerAPP1, markerAPP13, markerAPP14:
		return min(need, scanHeadLen)
	}
	return 0
}

// classifySegment returns the metadata kind of the segment with the marker and
// the first bytes of payload head, or kindNone.
func classifySegment(marker byte, head []byte) segKind {
	switch marker {
	case markerAPP1:
		switch {
		case bytes.HasPrefix(head, identExif):
			return kindEXIF
		case bytes.HasPrefix(head, identXMP), bytes.HasPrefix(head, identXMPNote):
			return kindXMP
		}
	case markerAPP13:
		if bytes.HasPrefix(head, identPS) {
			return kindPS
		}
	}
	return kindNone
}

// isLeadingSegment reports whether the segment is one that new metadata goes
// after when it follows the SOI marker directly: JFIF, JFIF extension or Adobe
// APP14 (see scanSplice).
func isLeadingSegment(marker byte, head []byte) bool {
	switch marker {
	case markerAPP0:
		return bytes.HasPrefix(head, identJFIF) || bytes.HasPrefix(head, identJFXX)
	case markerAPP14:
		return bytes.HasPrefix(head, identAdobe)
	}
	return false
}

// addDrop records that [start, end) is left out, merging it into the previous
// run when they touch. It reports false when the plan is full.
func (p *splicePlan) addDrop(start, end int64) bool {
	if n := len(p.drops); n > 0 && p.drops[n-1].end == start {
		p.drops[n-1].end = end
		return true
	}
	if len(p.drops) >= maxSpliceDrops {
		return false
	}
	p.drops = append(p.drops, spliceDrop{start, end})
	return true
}

// emitOffset returns the offset at which the write puts in the segments of kind
// k: the place of the first old segment it drops, else the place its kind has
// in the order EXIF, XMP, IPTC (see the top of this file).
func (p *splicePlan) emitOffset(k segKind) int64 {
	if p.dropAt[k] >= 0 {
		return p.dropAt[k]
	}
	switch k {
	case kindXMP:
		if p.first[kindEXIF].ok {
			return p.first[kindEXIF].end
		}
	case kindPS:
		if p.first[kindXMP].ok {
			return p.first[kindXMP].end
		}
		if p.first[kindEXIF].ok {
			return p.first[kindEXIF].end
		}
	case kindEXIF, kindNone:
	}
	return p.insertAt
}

// ops returns the steps of the write in the order of the output: the drops of
// the plan and the segments of pl, by offset; at one offset the segments are
// put in before a run is left out, and in the order EXIF, XMP, IPTC. buf backs
// the result when it is large enough.
func (p *splicePlan) ops(pl *payloads, buf []spliceOp) []spliceOp {
	ops := buf[:0]
	emits, ne := p.emitOps(pl)
	di, ei := 0, 0
	for di < len(p.drops) || ei < ne {
		if ei < ne && (di >= len(p.drops) || emits[ei].off <= p.drops[di].start) {
			ops = append(ops, emits[ei])
			ei++
			continue
		}
		d := p.drops[di]
		ops = append(ops, spliceOp{off: d.start, end: d.end, kind: kindNone})
		di++
	}
	return ops
}

// emitOps returns the steps that put in the segments of pl, ne of them, sorted
// by offset and, at one offset, by kind.
func (p *splicePlan) emitOps(pl *payloads) (emits [numKinds]spliceOp, ne int) {
	for k := range numKinds {
		if pl.has(k) {
			off := p.emitOffset(k)
			emits[ne] = spliceOp{off: off, end: off, kind: k}
			ne++
		}
	}
	// At most three: insertion sort, stable, so equal offsets keep the kind order.
	for i := 1; i < ne; i++ {
		for j := i; j > 0 && emits[j].off < emits[j-1].off; j-- {
			emits[j], emits[j-1] = emits[j-1], emits[j]
		}
	}
	return emits, ne
}

// writeSplice writes the output of Inject: the source, with the steps of ops
// applied, then the rest of the source from the first byte the scan did not
// read. cr is at the start of the source. plan is the trailer rewrite (see
// startTrailerPlan; w is then its counting writer) and is nil when there is
// none.
func writeSplice(cr *countingReader, w io.Writer, scratch *[]byte, p *splicePlan, ops []spliceOp, pl *payloads, plan *trailer.Plan) error {
	cursor := int64(0)
	for _, op := range ops {
		if op.off > cursor {
			if err := copyRange(cr, w, scratch, op.off-cursor); err != nil {
				return err
			}
			cursor = op.off
		}
		if op.kind != kindNone {
			if err := pl.emit(w, op.kind); err != nil {
				return err
			}
			continue
		}
		if op.end > cursor {
			if err := cr.skip(op.end - cursor); err != nil {
				return err
			}
			cursor = op.end
		}
	}
	return writeTail(cr, w, scratch, cursor, p.tailStart, plan)
}

// writeTail copies the source from offset cursor to its end: the image data up
// to the end of the image, then the region after it with its trailers rewritten
// when there is a plan (task #322; w is then its counting writer), or the rest
// of the file as it is.
func writeTail(cr *countingReader, w io.Writer, scratch *[]byte, cursor, tailStart int64, plan *trailer.Plan) error {
	if rs, isOut := w.(*trailer.OutWriter); isOut && plan != nil && plan.Base >= cursor && plan.Base >= tailStart {
		if err := copyRange(cr, w, scratch, plan.Base-cursor); err != nil {
			return err
		}
		if err := plan.WriteRegion(w, rs); err != nil {
			return fmt.Errorf("jpeg: write trailers: %w", err)
		}
		return nil
	}
	if _, err := io.Copy(w, cr); err != nil {
		return fmt.Errorf("jpeg: copy image data: %w", err)
	}
	return nil
}

// copyRange copies the next n bytes of cr to w. A short range, the usual one
// between two metadata segments, goes through the caller's scratch buffer:
// iobuf.StreamCopyN reads into a *bytes.Buffer with a minimum growth of 512
// bytes per call, which a copy of a few dozen bytes would pay in every call.
func copyRange(cr *countingReader, w io.Writer, scratch *[]byte, n int64) error {
	if n <= 0 {
		return nil
	}
	if n <= int64(len(*scratch)) {
		buf := (*scratch)[:n]
		if _, err := io.ReadFull(cr, buf); err != nil {
			return fmt.Errorf("jpeg: copy: %w", err)
		}
		if _, err := w.Write(buf); err != nil {
			return fmt.Errorf("jpeg: copy: %w", err)
		}
		return nil
	}
	if err := iobuf.StreamCopyN(cr, w, n); err != nil {
		return fmt.Errorf("jpeg: copy: %w", err)
	}
	return nil
}
