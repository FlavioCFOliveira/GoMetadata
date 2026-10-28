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
// the caller's pooled buffer. r is left at an unspecified position, and the plan
// reads from r when it is written, so r must stay valid until then.
//
// Task #393: the area after the EOI marker is not read into memory. The finders
// search it for the signatures of the trailers with a streaming scan and read
// only the bytes they have to inspect (trailer.PlanFromSource); the rest of the
// area, the audio file of a Samsung trailer for example, is copied from r when
// the plan is written. A read error while looking means a malformed trailer
// area, which is copied unchanged, as before.
//
// A nil newIPTC leaves the IPTC copies as they are (as for the raw profiles
// of PNG, task #314), but the AFCP offsets are still rewritten when the
// trailer moves.
func planTrailers(r io.ReadSeeker, scratch *[]byte, newIPTC []byte) (trailer.Plan, error) {
	base, size, err := locateTrailerRegion(r, scratch)
	if err != nil || size == 0 {
		return trailer.Plan{}, err
	}
	plan, perr := trailer.PlanFromSource(trailer.ReaderSource{R: r}, base, size, newIPTC)
	if perr != nil {
		return trailer.Plan{}, nil //nolint:nilerr // a short or failing read means a malformed trailer area: copy it unchanged
	}
	return plan, nil
}

// locateTrailerRegion returns the absolute offset of the first byte after the
// EOI marker of the JPEG in r and the number of bytes from it to the end of the
// file, or a zero size when there are none to look at: the JPEG ends with EOI
// (the common case, found with one seek and one two-byte read), has no EOI, or
// the area is larger than trailer.MaxRegion.
func locateTrailerRegion(r io.ReadSeeker, scratch *[]byte) (base, size int64, err error) {
	// One seek finds the length and the position of the last two bytes.
	pos, err := r.Seek(-2, io.SeekEnd)
	if err != nil || pos < 4 {
		return 0, 0, nil //nolint:nilerr // a stream too short to hold a trailer has none to rewrite
	}
	fileSize := pos + 2
	tail := (*scratch)[:2]
	if n, _ := r.Read(tail); n == 2 && tail[0] == 0xFF && tail[1] == markerEOI {
		return 0, 0, nil
	}
	eoi, err := findEOI(r, scratch)
	if err != nil || eoi < 0 || eoi+2 >= fileSize || fileSize-(eoi+2) > trailer.MaxRegion {
		return 0, 0, err
	}
	return eoi + 2, fileSize - (eoi + 2), nil
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
