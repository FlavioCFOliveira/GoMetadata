package iobuf

import "io"

// StreamCopyBufSize is the fixed size of the reusable buffer StreamCopyN uses
// to move bytes from r to w. Equal to the largePool tier ceiling (largeSize)
// so the buffer is served from that existing pool instead of a dedicated one.
const StreamCopyBufSize = largeSize

// StreamCopyN copies exactly n bytes from r to w using one pooled, fixed-size
// buffer reused across the whole call, never allocating memory proportional
// to n. This is the shared primitive behind every container format's
// "rebuild only the small metadata region, stream everything else verbatim"
// write path (PNG's pass-through chunks, CR3's ftyp/mdat regions and
// #291's TIFF-family image-block copies): none of them need n's bytes to be
// inspected, only relocated in the output stream, so holding them in memory
// even briefly would defeat the whole point of avoiding a whole-file buffer.
//
// n <= 0 is a no-op (returns nil immediately) rather than an error, since
// callers commonly compute n as a difference between two file offsets that
// may legitimately be zero (e.g. a container with nothing between two
// adjacent regions).
//
// Returns the first read or write error encountered, unwrapped — callers add
// their own format-specific context (e.g. "cr3: copy pre-moov bytes: %w").
func StreamCopyN(r io.Reader, w io.Writer, n int64) error {
	if n <= 0 {
		return nil
	}
	bufPtr := Get(StreamCopyBufSize)
	buf := *bufPtr
	for n > 0 {
		chunk := buf
		if int64(len(chunk)) > n {
			chunk = chunk[:n]
		}
		if _, err := io.ReadFull(r, chunk); err != nil {
			Put(bufPtr)
			return err
		}
		if _, err := w.Write(chunk); err != nil {
			Put(bufPtr)
			return err
		}
		n -= int64(len(chunk))
	}
	Put(bufPtr)
	return nil
}
