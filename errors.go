package imgmetadata

import (
	"fmt"

	"github.com/flaviocfo/img-metadata/internal/metaerr"
)

// UnsupportedFormatError is returned when the magic bytes of the input do not
// match any supported image container format.
type UnsupportedFormatError struct {
	// Magic contains the first bytes read from the input.
	Magic [12]byte
}

func (e *UnsupportedFormatError) Error() string {
	return fmt.Sprintf("imgmetadata: unsupported format (magic bytes: %x)", e.Magic[:])
}

// TruncatedFileError is returned when the input ends unexpectedly before a
// required structure could be read.
// Alias of internal/metaerr.TruncatedFileError; all sub-packages use the same type.
type TruncatedFileError = metaerr.TruncatedFileError

// CorruptMetadataError is returned when a metadata segment is structurally
// invalid (bad offsets, impossible lengths, invalid tag types, etc.).
// Alias of internal/metaerr.CorruptMetadataError; all sub-packages use the same type.
type CorruptMetadataError = metaerr.CorruptMetadataError
