package iobuf

import (
	"errors"
	"io"
)

// ErrTooLarge is returned by ReadAll when the input holds more than limit
// bytes. Callers translate it into their own package-level error.
var ErrTooLarge = errors.New("iobuf: input exceeds size limit")

// ReadAll reads r from its current position to EOF and returns the bytes
// read. It returns ErrTooLarge, without allocating a buffer for the input,
// when more than limit bytes remain.
//
// When r reports its remaining size through Seek, ReadAll allocates exactly
// that many bytes and fills them with one io.ReadFull, instead of the
// geometric growth of io.ReadAll. When seeking fails or reports a
// non-positive remaining size, ReadAll falls back to
// io.ReadAll(io.LimitReader(r, limit+1)). If the reader delivers fewer bytes
// than Seek reported, the bytes actually read are returned.
//
// limit must be non-negative and must not exceed the maximum int value.
func ReadAll(r io.ReadSeeker, limit int64) ([]byte, error) {
	cur, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return readAllLimited(r, limit)
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return readAllLimited(r, limit)
	}
	if _, err = r.Seek(cur, io.SeekStart); err != nil {
		// The reader moved to end but cannot return: no fallback can
		// recover the data.
		return nil, err
	}
	size := end - cur
	if size <= 0 {
		return readAllLimited(r, limit)
	}
	if size > limit {
		return nil, ErrTooLarge
	}

	buf := make([]byte, size)
	n, err := io.ReadFull(r, buf)
	switch {
	case err == nil:
		return buf, nil
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		// The input shrank after Seek reported its size.
		return buf[:n], nil
	default:
		return nil, err
	}
}

// readAllLimited is the non-seekable path: it reads at most limit+1 bytes
// and reports ErrTooLarge when the limit is exceeded.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}
