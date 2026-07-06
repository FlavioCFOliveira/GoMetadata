package gometadata

import (
	"errors"
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/internal/metaerr"
)

// maxFileSize is the upper bound on the total number of bytes the root
// package's TIFF-family write paths (writeTIFF, writeTIFFCR2, writeTIFFARW,
// writeTIFFORF, writeTIFFRW2, writeTIFFNEF) will read from an io.Reader in a
// single Write call when m.rawEXIF is nil (forcing a full-file read of the
// source container). Reads are wrapped with io.LimitReader(r, maxFileSize+1);
// if the reader delivers more bytes than this limit, the write is aborted
// with ErrFileTooLarge before any allocation proportional to an unbounded or
// adversarial stream length is retained.
//
// Real-world TIFF/DNG/NEF/ARW/CR2/ORF/RW2 files are well under 100 MiB;
// 256 MiB gives ample headroom for future sensor improvements while bounding
// worst-case heap allocation to a predictable, safe value. This mirrors the
// identical maxFileSize cap already applied to every format/* package's
// Extract and Inject entry points (format/tiff/errors.go, format/heif/errors.go)
// under the #140 fix; the six root-package call sites above were missed by
// that fix and are the subject of security audit FIX 3 (CWE-770/400).
//
// Declared as a var (not a const) so that tests can lower it temporarily to
// verify the OOM-guard path without allocating 256 MiB of memory.
var maxFileSize int64 = 256 << 20 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// ErrFileTooLarge is returned by Write when the source container exceeds
// maxFileSize and m.rawEXIF is nil (forcing a full-file read). This prevents
// a streaming or adversarially large reader from causing unbounded heap
// allocation. Callers can detect this specific condition with errors.Is.
var ErrFileTooLarge = errors.New("gometadata: input exceeds maximum file size (256 MiB)")

// ErrNilIFD0 is returned when an EXIF struct has a nil IFD0 field.
var ErrNilIFD0 = errors.New("gometadata: EXIF struct has nil IFD0; use exif.Parse to construct a valid EXIF")

// ErrNilXMPProperties is returned when an XMP struct has a nil Properties map.
var ErrNilXMPProperties = errors.New("gometadata: XMP struct has nil Properties map")

// ErrNilIFD0Write was the sentinel returned by Write when m.EXIF.IFD0 was nil.
//
// Deprecated: Write now calls m.Validate() first, which returns ErrNilIFD0 for
// this condition. ErrNilIFD0Write is retained for backwards compatibility only;
// callers should switch to errors.Is(err, ErrNilIFD0).
var ErrNilIFD0Write = errors.New("gometadata: EXIF struct has nil IFD0")

// UnsupportedFormatError is returned when the magic bytes of the input do not
// match any supported image container format.
type UnsupportedFormatError struct {
	// Magic contains the first bytes read from the input.
	Magic [12]byte
}

func (e *UnsupportedFormatError) Error() string {
	return fmt.Sprintf("gometadata: unsupported format (magic bytes: %x)", e.Magic[:])
}

// ErrWriteNotSupported is returned by Write and WriteFile when the caller
// attempts to write metadata into a container format that is not yet writable.
//
// All formats that are currently detected by the library either have a
// dedicated write path or return [UnsupportedFormatError] (for unknown magic).
// ErrWriteNotSupported is retained for future use when a new format is
// detected but its write path is not yet implemented.
//
// Use errors.Is(err, ErrWriteNotSupported) to detect this condition.
var ErrWriteNotSupported = errors.New("writing metadata into this container is not yet supported")

// ErrFormatMismatch is returned by Write when the Metadata value was read from
// a container of a different format than the one detected from r.
//
// Example: reading a JPEG into m and then calling Write(tiffReader, w, m)
// would mix JPEG-origin metadata (including rawEXIF sourced from the JPEG) with
// a TIFF image body, silently discarding the TIFF's image data.
//
// When m.Format() is FormatUnknown the check is skipped; a dedicated task
// handles that case separately.
//
// Use errors.Is(err, ErrFormatMismatch) to detect this condition.
var ErrFormatMismatch = errors.New("gometadata: metadata was read from a different container format than the write target")

// TruncatedFileError is returned when the input ends unexpectedly before a
// required structure could be read.
// Alias of internal/metaerr.TruncatedFileError; all sub-packages use the same type.
type TruncatedFileError = metaerr.TruncatedFileError

// CorruptMetadataError is returned when a metadata segment is structurally
// invalid (bad offsets, impossible lengths, invalid tag types, etc.).
// Alias of internal/metaerr.CorruptMetadataError; all sub-packages use the same type.
type CorruptMetadataError = metaerr.CorruptMetadataError

// ParseSegmentError is returned by Read when a metadata segment is present
// (raw bytes were successfully extracted from the container) but the format
// parser failed to decode it.
//
// Segment identifies which layer failed: "EXIF", "IPTC", or "XMP".
// Unwrap returns the underlying parser error so callers can use errors.As to
// inspect sub-package error types (e.g. CorruptMetadataError, TruncatedFileError).
//
// In best-effort mode (the default) this error is never returned by Read; it
// is collected in Metadata.ParseWarnings instead. It is returned by Read only
// when the Strict() option is active.
type ParseSegmentError struct {
	// Segment is the metadata layer that failed to parse: "EXIF", "IPTC", or "XMP".
	Segment string
	// Err is the underlying parser error.
	Err error
}

func (e *ParseSegmentError) Error() string {
	return fmt.Sprintf("gometadata: %s segment present but failed to parse: %v", e.Segment, e.Err)
}

// Unwrap satisfies the errors.Unwrap interface so callers can use errors.As
// and errors.Is on the underlying parser error.
func (e *ParseSegmentError) Unwrap() error { return e.Err }
