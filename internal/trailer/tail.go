package trailer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// Source gives random access to the bytes of a file, for FindTail and
// PlanFromSource.
type Source interface {
	// Range returns the n bytes of the file at offset off (the caller keeps
	// 0 <= off and off+n <= the file size). The result may alias the file's own
	// buffer and must not be modified.
	Range(off, n int64) ([]byte, error)
	// Index returns the offset of the first occurrence of pat in the bytes
	// [from, to) of the file, or -1 (the caller keeps 0 <= from <= to <= the file
	// size). A streaming source finds it without holding the range in memory.
	Index(from, to int64, pat []byte) (int64, error)
	// CopyRange writes the n bytes of the file at offset off to w.
	CopyRange(w io.Writer, off, n int64) error
}

// BytesSource is a Source over a file held in memory.
type BytesSource []byte

// Range implements Source.
func (b BytesSource) Range(off, n int64) ([]byte, error) { return b[off : off+n : off+n], nil }

// Index implements Source.
func (b BytesSource) Index(from, to int64, pat []byte) (int64, error) {
	i := bytes.Index(b[from:to], pat)
	if i < 0 {
		return -1, nil
	}
	return from + int64(i), nil
}

// CopyRange implements Source.
func (b BytesSource) CopyRange(w io.Writer, off, n int64) error {
	if n <= 0 {
		return nil
	}
	_, err := w.Write(b[off : off+n])
	return err
}

// ReaderSource is a Source over a seekable stream.
type ReaderSource struct{ R io.ReadSeeker }

// Index implements Source: a sequential scan through one pooled buffer
// (iobuf.IndexRange).
func (s ReaderSource) Index(from, to int64, pat []byte) (int64, error) {
	i, err := iobuf.IndexRange(s.R, from, to, pat)
	if err != nil {
		return -1, fmt.Errorf("trailer: search: %w", err)
	}
	return i, nil
}

// CopyRange implements Source: iobuf.CopyRange, through the pooled buffer.
func (s ReaderSource) CopyRange(w io.Writer, off, n int64) error {
	return iobuf.CopyRange(s.R, w, off, n)
}

// Range implements Source.
func (s ReaderSource) Range(off, n int64) ([]byte, error) {
	if _, err := s.R.Seek(off, io.SeekStart); err != nil {
		return nil, fmt.Errorf("trailer: seek: %w", err)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(s.R, b); err != nil {
		return nil, fmt.Errorf("trailer: read: %w", err)
	}
	return b, nil
}

// maxTailRecords bounds the FotoStation records walked back from the end of a
// file.
const maxTailRecords = 64

// FindTail looks for AFCP and FotoStation trailers at the END of a file of
// size bytes, the way a TIFF carries them (the trailers follow all TIFF data,
// and nothing follows them; ExifTool IdentifyTrailer likewise identifies a
// trailer by its last bytes). It returns the bytes of the file from the
// absolute offset base, the first byte of the earliest trailer found, to its
// end, for PlanFromRegion; region is nil when the file does not end with such
// a trailer, or the trailer is larger than MaxRegion.
//
// A FotoStation record is walked back by its footer (the last 10 bytes: tag,
// record size, signature); an AFCP trailer is found by its EOF record (the last
// 12 bytes: signature, offset of the header, reserved), which names the start
// of the trailer. Both kinds may precede each other, as in a JPEG; the
// structure itself is checked by PlanFromRegion.
//
// FindTail reads only a few bytes at the end of the file per record, so a file
// without a trailer costs one 12-byte read.
func FindTail[S Source](src S, size int64) (region []byte, base int64, err error) {
	end, start := size, size
	sawAFCP := false
	for range maxTailRecords {
		prev, isAFCP, serr := tailStep(src, end, sawAFCP)
		if serr != nil {
			return nil, 0, serr
		}
		if prev < 0 {
			break
		}
		start, end = prev, prev
		sawAFCP = sawAFCP || isAFCP
	}
	if start == size || size-start > MaxRegion {
		return nil, 0, nil
	}
	region, err = src.Range(start, size-start)
	if err != nil {
		return nil, 0, err
	}
	return region, start, nil
}

// tailStep looks at the trailer that ends at end: an AFCP trailer when none was
// seen yet (isAFCP), or a FotoStation record. It returns the offset where it
// starts, or -1 when the bytes before end are neither.
func tailStep[S Source](src S, end int64, sawAFCP bool) (start int64, isAFCP bool, err error) {
	if !sawAFCP && end >= 12+12 {
		foot, rerr := src.Range(end-12, 12)
		if rerr != nil {
			return -1, false, rerr
		}
		if hdr, ok := afcpHeaderOffset(foot); ok && hdr >= 0 && hdr < end-12 {
			return hdr, true, nil
		}
	}
	rec, ok, err := fotoRecordStart(src, end)
	if err != nil || !ok {
		return -1, false, err
	}
	return rec, false, nil
}

// afcpHeaderOffset returns the header offset of foot (12 bytes) when it is an
// AFCP EOF record.
func afcpHeaderOffset(foot []byte) (int64, bool) {
	switch string(foot[:4]) {
	case string(afcpSigBE):
		return int64(binary.BigEndian.Uint32(foot[4:])), true
	case string(afcpSigLE):
		return int64(binary.LittleEndian.Uint32(foot[4:])), true
	}
	return 0, false
}

// fotoRecordStart returns the offset of the FotoStation record that ends at
// end, when the bytes before end are a record footer.
func fotoRecordStart[S Source](src S, end int64) (int64, bool, error) {
	if end < 10 {
		return 0, false, nil
	}
	foot, err := src.Range(end-10, 10)
	if err != nil {
		return 0, false, err
	}
	if !bytes.Equal(foot[6:], fotoSig) {
		return 0, false, nil
	}
	rec := int64(binary.BigEndian.Uint32(foot[2:]))
	if rec < 10 || rec > end {
		return 0, false, nil
	}
	return end - rec, true, nil
}
