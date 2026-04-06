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

// ErrNilIFD0Write is returned when attempting to write an EXIF struct with a nil IFD0 field.
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

// TruncatedFileError is returned when the input ends unexpectedly before a
// required structure could be read.
// Alias of internal/metaerr.TruncatedFileError; all sub-packages use the same type.
type TruncatedFileError = metaerr.TruncatedFileError

// CorruptMetadataError is returned when a metadata segment is structurally
// invalid (bad offsets, impossible lengths, invalid tag types, etc.).
// Alias of internal/metaerr.CorruptMetadataError; all sub-packages use the same type.
type CorruptMetadataError = metaerr.CorruptMetadataError
