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
// EXIF. measureShift reads the segments before the first scan once, as the
// main pass does, and counts what the new metadata segments take; everything
// from the end of the scan header on is copied unchanged (the trailers that
// internal/trailer rewrites are accounted for from their plan), so one number,
// the difference between the position of the end of the scan header in the
// output and in the source, is the movement of every byte after it. The same
// pass records where the old EXIF stream was, so the position the preview had
// in the source is known: the old MakerNote position plus the stored offset.
// Only streams that hold such an offset pay for this: exif.ExternalOffsets
// answers nil for every other stream after one search of the stream.
//
// An offset is rewritten only when it addresses a JPEG stream (the SOI marker)
// that lies after the first scan header and inside the file: the bytes there
// are copied as they are. Anything else is left as it is.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

// newEXIFTIFFPos is the position of the TIFF header of the EXIF that Inject
// writes: SOI (2), then the APP1 marker (2), its length (2) and "Exif\0\0" (6).
const newEXIFTIFFPos = 2 + 4 + len("Exif\x00\x00")

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
	// oldTIFF is the EXIF stream of the source (the first EXIF APP1 segment),
	// and oldTIFFPos the position in the source of its first byte.
	oldTIFF    []byte
	oldTIFFPos int64
}

// shiftExternalOffsets returns rawEXIF with the MakerNote offsets that address
// data outside the EXIF stream (exif.ExternalOffsets) rewritten for the
// position the MakerNote and the data have in the output of Inject, or rawEXIF
// itself when it holds none, or none that can be resolved. rawEXIF is never
// modified. cr is left at the start of the file.
func shiftExternalOffsets(cr io.ReadSeeker, scratch *[]byte, rawEXIF, rawIPTC, rawXMP, origIRB []byte, preserveUnknownSegments bool, keepGUID string, plan *trailer.Plan) ([]byte, error) {
	offs := exif.ExternalOffsets(rawEXIF)
	if len(offs) == 0 {
		return rawEXIF, nil
	}
	size, err := cr.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("jpeg: seek: %w", err)
	}
	info, ok, err := measureShift(cr, scratch, rawEXIF, rawIPTC, rawXMP, origIRB, preserveUnknownSegments, keepGUID)
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
// the data is not a JPEG stream that lies after the first scan header and
// inside the source (size bytes), or its new position cannot be found.
func (s *shiftInfo) newValue(cr io.ReadSeeker, scratch *[]byte, old, o exif.ExternalOffset, size int64, plan *trailer.Plan) (uint32, bool) {
	p := s.oldTIFFPos + int64(old.Base) + int64(old.Value) // where the data is in the source
	if p < s.sosEnd || p+int64(old.Length) > size || !startsWithSOI(cr, p, scratch) {
		return 0, false
	}
	q, ok := movedPosition(p, s.delta, plan)
	nv := q - (int64(newEXIFTIFFPos) + int64(o.Base)) // counted from the MakerNote of the output
	if !ok || nv < 0 || nv > math.MaxUint32 {
		return 0, false
	}
	return uint32(nv), true
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

// measureShift reads the source exactly as the main pass does
// (copyNonMetadataSegments) up to the first scan header, counting what
// writeNewMetadataSegments writes, and returns what shiftInfo holds. ok is
// false when the source has no scan header, or no EXIF segment.
func measureShift(cr io.ReadSeeker, scratch *[]byte, rawEXIF, rawIPTC, rawXMP, origIRB []byte, preserveUnknownSegments bool, keepGUID string) (info shiftInfo, ok bool, err error) {
	var cw countWriter
	if werr := writeNewMetadataSegments(&cw, rawEXIF, rawIPTC, rawXMP, origIRB); werr != nil {
		return info, false, werr
	}
	if _, err = cr.Seek(2, io.SeekStart); err != nil { // after SOI
		return info, false, fmt.Errorf("jpeg: seek: %w", err)
	}
	out := 2 + cw.n // SOI and the new metadata segments
	for {
		marker, data, rerr := readSegment(cr, scratch)
		if rerr != nil || marker == markerEOI {
			return info, false, nil //nolint:nilerr // no scan header: the main pass reports a damaged stream, and nothing is moved
		}
		end, serr := cr.Seek(0, io.SeekCurrent)
		if serr != nil {
			return info, false, fmt.Errorf("jpeg: seek: %w", serr)
		}
		info.noteOldEXIF(marker, data, end)
		if dropsSegment(marker, data, preserveUnknownSegments, keepGUID) {
			continue
		}
		out += int64(len(data)) + 4 // marker, length and data
		if marker == markerSOS {
			info.sosEnd, info.delta = end, out-end
			return info, info.oldTIFF != nil, nil
		}
	}
}

// noteOldEXIF records the first EXIF segment of the source: data is its payload
// and end the position in the source after it.
func (s *shiftInfo) noteOldEXIF(marker byte, data []byte, end int64) {
	if marker == markerAPP1 && s.oldTIFF == nil && bytes.HasPrefix(data, identExif) {
		s.oldTIFF = bytes.Clone(data[len(identExif):])
		s.oldTIFFPos = end - int64(len(data)) + int64(len(identExif))
	}
}
