package tiff

// inject_stream.go — task #392: InjectWithMagic, Inject for a source that is
// not held in memory. The ORF and RW2 packages write through it.

import (
	"fmt"
	"io"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// InjectWithMagic is Inject for TIFF-based containers whose header carries a
// magic other than 0x002A at bytes [2:4] (Olympus ORF, Panasonic RW2), and for a
// source that is not held in memory. acceptMagic is that magic as the byte
// order of the file reads it (as ExtractWithMagic takes it); the stream that
// is written has the standard header, and the caller restores its own (see
// iobuf.MagicWriter).
//
// rawEXIF is Inject's: when it is not nil it is the base the image blocks are
// taken from and r is not read. When it is nil the base is the metadata prefix
// of r (the IFDs and their values, as ExtractWithMagic finds them) and the image
// blocks are streamed from r through a fixed pooled buffer, so memory use is
// proportional to the metadata, and r has no size limit but the 4 GiB that the
// 32-bit offsets of a classic TIFF address: ErrFileTooLarge is returned for a
// source, or an output, beyond it, and for a source whose metadata does not fit
// the metadata limit (256 MiB). A source that cannot seek is buffered whole, up
// to that limit.
//
// With no IPTC and no XMP to write the source is copied unchanged.
func InjectWithMagic(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, acceptMagic uint16) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}
	if rawEXIF != nil {
		return injectBase(r, w, rawEXIF, rawIPTC, rawXMP)
	}
	fileLen, err := seekFileSize(r)
	if err != nil {
		return err
	}
	if fileLen < 0 { // a source that cannot report its size is buffered whole
		return injectBuffered(r, w, rawIPTC, rawXMP, acceptMagic)
	}
	if fileLen > math.MaxUint32 {
		return fmt.Errorf("tiff: input of %d bytes exceeds the 4 GiB that classic TIFF offsets address: %w", fileLen, ErrFileTooLarge)
	}
	if len(rawIPTC) == 0 && len(rawXMP) == 0 {
		if err = iobuf.CopyRange(r, w, 0, fileLen); err != nil {
			return fmt.Errorf("tiff: copy: %w", err)
		}
		return nil
	}
	return injectSparse(r, w, fileLen, rawIPTC, rawXMP, acceptMagic)
}

// injectBuffered is InjectWithMagic for a source that cannot report its size:
// the whole file is read, up to maxFileSize, as Inject reads it.
func injectBuffered(r io.ReadSeeker, w io.Writer, rawIPTC, rawXMP []byte, acceptMagic uint16) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tiff: seek: %w", err)
	}
	base, err := readInput(r)
	if err != nil {
		return err
	}
	restoreStandardMagic(base, acceptMagic)
	return injectBase(r, w, base, rawIPTC, rawXMP)
}

// injectSparse is InjectWithMagic for a source of fileLen bytes, which is not
// held in memory: the base is its metadata prefix and the image blocks are
// streamed from r.
func injectSparse(r io.ReadSeeker, w io.Writer, fileLen int64, rawIPTC, rawXMP []byte, acceptMagic uint16) error {
	prefix, err := sourcePrefix(r, fileLen, acceptMagic)
	if err != nil {
		return err
	}
	wholeFile := int64(len(prefix)) == fileLen
	restoreStandardMagic(prefix, acceptMagic)
	header, blocks, err := relocateTIFFPrefix(prefix, rawIPTC, rawXMP, uint64(fileLen)) //nolint:gosec // G115: fileLen is non-negative
	if err != nil {
		return err
	}
	// The output is the header, the image blocks and the tail of the source:
	// never more than the header and the source. The offsets of classic TIFF
	// are 32 bits, so an output beyond them cannot be addressed (the relocation
	// above has already truncated the offsets it assigned; nothing is written).
	if int64(len(header))+fileLen > math.MaxUint32 {
		return fmt.Errorf("tiff: output exceeds the 4 GiB that classic TIFF offsets address: %w", ErrFileTooLarge)
	}
	if err = writeRelocated(r, w, header, blocks, prefix, wholeFile, rawIPTC); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// sourcePrefix returns the metadata prefix of the classic TIFF in r, a source of
// fileLen bytes: the bytes from the start of the file to the end of the last IFD
// or value that an IFD addresses, or the whole file when it is small
// (ExtractWithMagic's rules, without the limit on the size of the file: only the
// prefix is held in memory, and it is limited to maxFileSize).
func sourcePrefix(r io.ReadSeeker, fileLen int64, acceptMagic uint16) ([]byte, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("tiff: seek: %w", err)
	}
	if fileLen <= smallFileWholeReadThreshold {
		prefix, _, _, err := extractWholeFile(r, acceptMagic)
		return prefix, err
	}
	initial, err := readInitialPrefix(r, fileLen)
	if err != nil {
		return nil, err
	}
	// The scanner clamps what it reads to maxFileSize, so that a corrupt
	// offset cannot make it read an unbounded amount.
	prefix, _, _, err := extractPrefixedFrom(r, initial, uint64(min(fileLen, maxFileSize)), acceptMagic) //nolint:gosec // G115: fileLen is non-negative and maxFileSize positive
	if err != nil {
		return nil, err
	}
	if fileLen > maxFileSize {
		// The scan stopped at the limit: refuse a source whose metadata lies
		// beyond it, instead of writing a file without it.
		order, oerr := byteOrder(prefix)
		if oerr != nil {
			return nil, oerr
		}
		need := scanExtentPass(&extentScan{seenIFD: make(map[uint64]bool, 16)}, prefix, uint64(order.Uint32(prefix[4:])), order, false)
		if need > uint64(len(prefix)) {
			return nil, fmt.Errorf("tiff: the metadata of the input extends beyond %d bytes: %w", maxFileSize, ErrFileTooLarge)
		}
	}
	return prefix, nil
}

// restoreStandardMagic rewrites the magic of buf, which the caller owns, to the
// standard 0x002A when it is acceptMagic: the relocation parses and rebuilds a
// standard TIFF stream.
func restoreStandardMagic(buf []byte, acceptMagic uint16) {
	if acceptMagic == 0 || len(buf) < 8 {
		return
	}
	order, err := byteOrder(buf)
	if err != nil || order.Uint16(buf[2:]) != acceptMagic {
		return
	}
	order.PutUint16(buf[2:], 0x002A)
}
