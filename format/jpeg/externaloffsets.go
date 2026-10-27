package jpeg

// externaloffsets.go — the MakerNote offsets that address data outside the EXIF
// stream (task #362).
//
// The Olympus E-series and compact cameras store in the MakerNote the offset,
// counted from the start of the MakerNote, of a preview image that follows the
// end of the image (exif.ExternalOffsets describes the value). A write that
// moves the MakerNote or the preview by different distances breaks it
// (ExifTool: "MakerNotes:PreviewImageStart+PreviewImageLength runs past end of
// file"; CLAUDE.md §5: a write keeps every offset valid): the MakerNote moves
// with the new EXIF, the preview moves with the size of everything before it.
//
// Inject writes the new metadata segments before it copies the rest of the
// file, so it must know the movement of the image data before it writes the
// EXIF. measureShift takes the steps of the write (splice.go: the segments
// that are put in and the runs that are left out) and counts what the new
// metadata segments take; everything from the end of the scan header on is
// copied unchanged (the trailers that internal/trailer rewrites are accounted
// for from their plan), so one number, the difference between the position of
// the end of the scan header in the output and in the source, is the movement
// of every byte after it. The position of the new EXIF stream in the output
// follows from the same steps, and the position of the old one, which the scan
// records, tells where the preview was in the source: the old MakerNote
// position plus the stored offset.
// Only streams that hold such an offset pay for this: exif.ExternalOffsets
// answers nil for every other stream after one search of the stream.
//
// What an offset addresses decides how it is rewritten (task #370):
//
//   - bytes of the MakerNote itself (the E-1 stores the preview, counted from the
//     TIFF header, inside the MakerNote): they move with it, so the offset follows
//     the MakerNote;
//   - bytes of a segment before the first scan header that the write copies (an
//     APPn segment): the offset follows the segment, whose new position
//     shiftInfo.movedHeader computes;
//   - a JPEG stream (the SOI marker) after the first scan header and inside the
//     file: the bytes there are copied as they are and move by the difference of
//     the positions of the end of the scan header, plus the growth of every
//     rewritten trailer before them.
//
// Anything else is left as it is, because the bytes it addresses are not copied
// to a position that can be known: a segment that the write drops or replaces,
// the EXIF stream outside the MakerNote (the stream is encoded again), a
// trailer that is rewritten, and bytes outside the file.

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

// exifTIFFOffset is the offset of the TIFF header of an EXIF stream from the
// start of the APP1 segment that holds it: the marker (2), the length (2) and
// "Exif\0\0" (6).
const exifTIFFOffset int64 = 4 + int64(len("Exif\x00\x00"))

// countWriter counts the bytes written to it.
type countWriter struct{ n int64 }

// Write counts p.
func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// shiftInfo is what measureShift learns about the source and the output.
type shiftInfo struct {
	sosEnd int64 // position in the source of the end of the first scan header
	delta  int64 // position in the output minus position in the source of that point
	// steps are the steps of the write before the first scan header, each with
	// the number of bytes it puts in the output: a segment put in (off == end)
	// or a run of the source left out.
	steps []shiftStep
	// newTIFFPos is the position in the output of the TIFF header of the EXIF
	// stream that the write puts in.
	newTIFFPos int64
	// oldTIFF is the EXIF stream of the source (the first EXIF APP1 segment),
	// and oldTIFFPos the position in the source of its first byte.
	oldTIFF    []byte
	oldTIFFPos int64
}

// shiftStep is a step of the write: the segments of one kind put in at offset
// off of the source with n bytes (off == end), or, for kindNone, the run
// [off, end) left out.
type shiftStep struct {
	off, end int64
	n        int64
	kind     segKind
}

// shiftExternalOffsets returns rawEXIF with the MakerNote offsets that address
// data outside the EXIF stream (exif.ExternalOffsets) rewritten for the
// position the MakerNote and the data have in the output of Inject, or rawEXIF
// itself when it holds none, or none that can be resolved. rawEXIF is never
// modified. cr is left at the start of the file.
func shiftExternalOffsets(cr io.ReadSeeker, scratch *[]byte, sp *splicePlan, pl *payloads, plan *trailer.Plan) ([]byte, error) {
	rawEXIF := pl.rawEXIF
	offs := exif.ExternalOffsets(rawEXIF)
	if len(offs) == 0 {
		return rawEXIF, nil
	}
	size := sp.size
	info, ok, err := measureShift(cr, sp, pl)
	if err != nil {
		return nil, err
	}
	patched := rawEXIF
	if ok {
		// The same offsets in the stream of the source: they say where the
		// MakerNote was and so where the data is.
		patched = info.rewrite(cr, scratch, rawEXIF, offs, exif.ExternalOffsets(info.oldTIFF), size, plan)
	}
	return patched, seekStart(cr)
}

// rewrite returns rawEXIF with each offset of offs (found in the new stream)
// that corresponds to one of olds (found in the stream of the source) rewritten
// for the position of the data in the output, or rawEXIF itself when none is.
func (s *shiftInfo) rewrite(cr io.ReadSeeker, scratch *[]byte, rawEXIF []byte, offs, olds []exif.ExternalOffset, size int64, plan *trailer.Plan) []byte {
	if len(olds) != len(offs) {
		return rawEXIF
	}
	patched := rawEXIF
	for i, o := range offs {
		old := olds[i]
		if old.Value != o.Value || old.Length != o.Length {
			continue // the caller changed the value: it is not a copy of the source's
		}
		nv, ok := s.newValue(cr, scratch, old, o, size, plan)
		if !ok || nv == o.Value {
			continue
		}
		if &patched[0] == &rawEXIF[0] {
			patched = append([]byte(nil), rawEXIF...) // never modify the caller's stream
		}
		o.Order.PutUint32(patched[o.Field:], nv)
	}
	return patched
}

// seekStart leaves r at the start of the file, for the main pass.
func seekStart(r io.Seeker) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}
	return nil
}

// newValue returns the value that the offset o of the new stream must hold so
// that it addresses the data that old addresses in the source, or false when
// the data cannot be placed (see the top of this file).
func (s *shiftInfo) newValue(cr io.ReadSeeker, scratch *[]byte, old, o exif.ExternalOffset, size int64, plan *trailer.Plan) (uint32, bool) {
	q, ok := s.outputPosition(cr, scratch, old, o, size, plan)
	nv := q - (s.newTIFFPos + int64(o.Base)) // counted from the byte the new stream counts from
	if !ok || nv < 0 || nv > math.MaxUint32 {
		return 0, false
	}
	return uint32(nv), true
}

// outputPosition returns the position in the output of the data that old
// addresses in the source (see the top of this file for the classes), or false.
func (s *shiftInfo) outputPosition(cr io.ReadSeeker, scratch *[]byte, old, o exif.ExternalOffset, size int64, plan *trailer.Plan) (int64, bool) {
	rel := old.Target() // position of the data in the EXIF stream of the source
	p := s.oldTIFFPos + rel
	n := int64(old.Length)
	switch {
	case rel >= int64(old.MNStart) && rel+n <= int64(old.MNStart)+int64(old.MNLen):
		// Inside the MakerNote: it is copied as it is to the new stream.
		return s.newTIFFPos + int64(o.MNStart) + (rel - int64(old.MNStart)), true
	case rel >= 0 && rel < int64(len(s.oldTIFF)):
		return 0, false // the EXIF stream outside the MakerNote is encoded again
	case p < s.sosEnd:
		return s.movedHeader(p, n)
	case p+n > size || !startsWithSOI(cr, p, scratch):
		return 0, false
	}
	return movedPosition(p, s.delta, plan)
}

// movedHeader returns the position in the output of the n bytes at position p of
// the source, when they lie before the first scan header and the write copies
// them as one piece: no run left out touches them and no segment is put in
// between them. It replays the steps as writeSplice does.
func (s *shiftInfo) movedHeader(p, n int64) (int64, bool) {
	cursor, out := int64(0), int64(0)
	for _, st := range s.steps {
		if st.off > cursor {
			if p >= cursor && p+n <= st.off {
				return out + (p - cursor), true
			}
			out += st.off - cursor
			cursor = st.off
		}
		if st.kind != kindNone {
			out += st.n
			continue
		}
		cursor = max(cursor, st.end)
	}
	if p >= cursor {
		return out + (p - cursor), true
	}
	return 0, false
}

// startsWithSOI reports whether the two bytes of r at offset p are the SOI
// marker of a JPEG (the preview is a JPEG stream; ISO/IEC 10918-1 §B.1.1.3).
func startsWithSOI(r io.ReadSeeker, p int64, scratch *[]byte) bool {
	if _, err := r.Seek(p, io.SeekStart); err != nil {
		return false
	}
	b := (*scratch)[:2]
	if _, err := io.ReadFull(r, b); err != nil {
		return false
	}
	return binary.BigEndian.Uint16(b) == 0xFFD8
}

// movedPosition returns the position in the output of the source byte at
// position p, which lies after the first scan header: p + delta, plus the
// growth of every rewritten trailer that precedes p. It reports false when p
// lies inside a trailer that is rewritten (the bytes it addresses are not
// copied), or when a trailer cannot be sized.
func movedPosition(p, delta int64, plan *trailer.Plan) (int64, bool) {
	q := p + delta
	if plan == nil {
		return q, true
	}
	for _, sp := range plan.Spans {
		if sp.Start > p {
			continue
		}
		b, changed, err := sp.Build(sp.Start) // the length does not depend on the position
		if err != nil {
			return 0, false
		}
		switch {
		case sp.End <= p:
			if changed {
				q += int64(len(b)) - (sp.End - sp.Start)
			}
		case changed:
			return 0, false // p is inside a trailer that is rewritten
		}
	}
	return q, true
}

// measureShift computes what shiftInfo holds from the steps of the write (see
// splicePlan.ops), counting what each segment put in takes. ok is false when the
// source has no scan header, or no EXIF segment.
func measureShift(cr io.ReadSeeker, sp *splicePlan, pl *payloads) (info shiftInfo, ok bool, err error) {
	var opsBuf [16]spliceOp
	ops := sp.ops(pl, opsBuf[:0])
	info.steps = make([]shiftStep, 0, len(ops))
	cursor, out := int64(0), int64(0) // replays the steps as writeSplice does
	for _, op := range ops {
		if op.off > cursor {
			out += op.off - cursor
			cursor = op.off
		}
		st := shiftStep{off: op.off, end: op.end, kind: op.kind}
		if op.kind == kindNone {
			cursor = max(cursor, op.end)
		} else {
			if st.n, err = countSegments(pl, op.kind); err != nil {
				return info, false, err
			}
			if op.kind == kindEXIF {
				info.newTIFFPos = out + exifTIFFOffset
			}
			out += st.n
		}
		info.steps = append(info.steps, st)
	}
	if sp.sosEnd < 0 || !sp.first[kindEXIF].ok {
		return info, false, nil // no scan header or no old EXIF: nothing is moved
	}
	// Every byte from the first scan header on moves by out - cursor.
	info.sosEnd, info.delta = sp.sosEnd, out-cursor
	info.oldTIFF, info.oldTIFFPos, err = readOldEXIF(cr, sp.first[kindEXIF])
	return info, err == nil && info.oldTIFF != nil, err
}

// countSegments returns the number of bytes the segments of kind k take.
func countSegments(pl *payloads, k segKind) (int64, error) {
	var cw countWriter
	if err := pl.emit(&cw, k); err != nil {
		return 0, err
	}
	return cw.n, nil
}

// readOldEXIF returns the EXIF stream of the first EXIF segment f of the source
// (what the offsets of the old MakerNote are counted in) and the position in the
// source of its first byte. The stream is nil when the segment cannot be read:
// nothing is then moved.
func readOldEXIF(cr io.ReadSeeker, f oldSeg) (stream []byte, pos int64, err error) {
	pos = f.dataStart + int64(len(identExif))
	n := f.end - pos
	if n <= 0 {
		return nil, 0, nil
	}
	if _, serr := cr.Seek(pos, io.SeekStart); serr != nil {
		return nil, 0, fmt.Errorf("jpeg: seek: %w", serr)
	}
	stream = make([]byte, n)
	if _, rerr := io.ReadFull(cr, stream); rerr != nil {
		return nil, 0, nil //nolint:nilerr // the segment cannot be read: nothing is moved
	}
	return stream, pos, nil
}
