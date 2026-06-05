package gometadata

import (
	"errors"
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/internal/metaerr"
)

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
// Gated formats:
//   - DNG: write is disabled pending bug #98 (SubIFD out-of-line RATIONAL
//     value loss — XResolution/YResolution silently become undef after write).
//   - CR2, NEF, ARW, ORF, RW2: require manufacturer-specific offset handling
//     (task #95) that is not yet implemented.
//
// TIFF write is fully supported via the copy-and-relocate serializer (tasks
// #92/#93). CR3 write is supported via ISOBMFF offset relocation (task #91).
//
// Use errors.Is(err, ErrWriteNotSupported) to detect this condition.
var ErrWriteNotSupported = errors.New("writing metadata into this container is not yet supported: DNG (bug #98 SubIFD value loss), CR2/NEF/ARW/ORF/RW2 (task #95 manufacturer offset handling)")

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
