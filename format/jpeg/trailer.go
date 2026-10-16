package jpeg

// Trailers after the EOI marker (tasks #322, #353). A JPEG may carry data after
// its EOI marker; the trailers that hold an IPTC copy (AFCP, FotoStation) or an
// absolute offset (AFCP, Samsung) are located and rewritten by
// internal/trailer, whose package comment describes the layouts and the
// trailers that need no rewrite. This file finds the region after the image
// data and writes the plan into the output.
//
// Scope: trailers are located only inside the bytes after the EOI marker (and
// only when their structure is consistent); anything else is copied through
// unchanged, as before.

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
)

// startTrailerPlan looks for trailers to rewrite in the JPEG that r holds. It
// returns the plan and the writer to use for the rest of the write: w itself
// when there is nothing to do (plan is nil), and otherwise a counting wrapper
// around w. A JPEG that ends with EOI costs one two-byte read.
func startTrailerPlan(r io.ReadSeeker, w io.Writer, scratch *[]byte, newIPTC []byte) (*trailer.Plan, io.Writer, error) {
	plan, err := planTrailers(r, scratch, newIPTC)
	if err != nil || len(plan.Spans) == 0 {
		return nil, w, err
	}
	// Only now does the plan reach the heap: the common case allocates nothing.
	p := new(trailer.Plan)
	*p = plan
	return p, &trailer.OutWriter{W: w}, nil
}

// planTrailers looks for AFCP and FotoStation trailers after the EOI marker
// of the JPEG that r holds and returns the plan to rewrite their IPTC copies
// with newIPTC; the plan has no spans when there is nothing to do. scratch is
// the caller's pooled buffer. r is left at an unspecified position.
//
// A nil newIPTC leaves the IPTC copies as they are (as for the raw profiles
// of PNG, task #314), but the AFCP offsets are still rewritten when the
// trailer moves.
func planTrailers(r io.ReadSeeker, scratch *[]byte, newIPTC []byte) (trailer.Plan, error) {
	region, base, err := readTrailerRegion(r, scratch)
	if err != nil || region == nil {
		return trailer.Plan{}, err
	}
	return trailer.PlanFromRegion(region, base, newIPTC), nil
}

// readTrailerRegion returns the bytes after the EOI marker of the JPEG in r
// and the absolute offset of the first of them, or a nil region when there
// are none to look at: the JPEG ends with EOI (the common case, found with
// one seek and one two-byte read), has no EOI, or the area is larger than
// trailer.MaxRegion.
func readTrailerRegion(r io.ReadSeeker, scratch *[]byte) (region []byte, base int64, err error) { //nolint:gocyclo // a sequence of cheap exits, each with the reason it is not an error
	// One seek finds the length and the position of the last two bytes.
	pos, err := r.Seek(-2, io.SeekEnd)
	if err != nil || pos < 4 {
		return nil, 0, nil //nolint:nilerr // a stream too short to hold a trailer has none to rewrite
	}
	size := pos + 2
	tail := (*scratch)[:2]
	if n, _ := r.Read(tail); n == 2 && tail[0] == 0xFF && tail[1] == markerEOI {
		return nil, 0, nil
	}
	eoi, err := findEOI(r, scratch)
	if err != nil || eoi < 0 || eoi+2 >= size || size-(eoi+2) > trailer.MaxRegion {
		return nil, 0, err
	}
	base = eoi + 2
	region = make([]byte, size-base)
	if _, err := r.Seek(base, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("jpeg: seek: %w", err)
	}
	if _, err := io.ReadFull(r, region); err != nil {
		return nil, 0, nil //nolint:nilerr // a short read means a malformed trailer area: copy it unchanged
	}
	return region, base, nil
}

// findEOI returns the offset of the EOI marker that ends the image data of the
// JPEG in r, or -1 when there is none. It walks the segments up to the first
// SOS, then looks for the first 0xFF 0xD9: inside entropy-coded data every
// 0xFF is followed by 0x00, a restart marker or a marker, so 0xFF 0xD9 is the
// end of the image (JPEG ISO/IEC 10918-1 §B.1.1.5, §F.1.2.3).
func findEOI(r io.ReadSeeker, scratch *[]byte) (int64, error) {
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		return -1, fmt.Errorf("jpeg: seek: %w", err)
	}
	for {
		marker, _, err := readSegment(r, scratch)
		if err != nil {
			if errors.Is(err, ErrFileTooLarge) {
				return -1, err
			}
			return -1, nil
		}
		switch marker {
		case markerEOI:
			pos, serr := r.Seek(0, io.SeekCurrent)
			if serr != nil {
				return -1, fmt.Errorf("jpeg: seek: %w", serr)
			}
			return pos - 2, nil
		case markerSOS:
			return scanForEOI(r)
		}
	}
}

// scanForEOI reads from the current position of r, the start of entropy-coded
// data, and returns the offset of the first 0xFF 0xD9, or -1.
func scanForEOI(r io.ReadSeeker) (int64, error) {
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1, fmt.Errorf("jpeg: seek: %w", err)
	}
	bufPtr := iobuf.Get(iobuf.StreamCopyBufSize)
	defer iobuf.Put(bufPtr)
	buf := *bufPtr
	prevFF := false
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if prevFF && chunk[0] == markerEOI {
				return pos - 1, nil
			}
			if i := bytes.Index(chunk, []byte{0xFF, markerEOI}); i >= 0 {
				return pos + int64(i), nil
			}
			prevFF = chunk[n-1] == 0xFF
			pos += int64(n)
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return -1, nil
			}
			if errors.Is(rerr, ErrFileTooLarge) {
				return -1, fmt.Errorf("jpeg: read image data: %w", rerr)
			}
			return -1, nil
		}
	}
}

// writeSOSTrailers is writeSOS for a JPEG with trailers to rewrite: it copies
// the compressed data up to the end of the image, then writes the region after
// EOI with each trailer of plan replaced by its rewritten bytes (the bytes
// between and after them are copied as they are). w must be the writer that
// counts the output (out), which has seen every byte of the output so far.
func writeSOSTrailers(r io.ReadSeeker, w io.Writer, out *trailer.OutWriter, data []byte, plan *trailer.Plan) error {
	if err := writeSegmentCopy(w, markerSOS, data); err != nil {
		return err
	}
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}
	if n := plan.Base - pos; n > 0 {
		if err := iobuf.StreamCopyN(r, w, n); err != nil {
			return fmt.Errorf("jpeg: copy image data: %w", err)
		}
	}
	if err := plan.WriteRegion(w, out); err != nil {
		return fmt.Errorf("jpeg: write trailers: %w", err)
	}
	return nil
}
