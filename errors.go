package imgmetadata

import "fmt"

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
type TruncatedFileError struct {
	// At describes what was being read when the truncation was detected.
	At string
}

func (e *TruncatedFileError) Error() string {
	return fmt.Sprintf("imgmetadata: truncated file while reading %s", e.At)
}

// CorruptMetadataError is returned when a metadata segment is structurally
// invalid (bad offsets, impossible lengths, invalid tag types, etc.).
// The message is specific enough for the caller to locate the problem.
type CorruptMetadataError struct {
	Format string // "EXIF", "IPTC", or "XMP"
	Reason string
}

func (e *CorruptMetadataError) Error() string {
	return fmt.Sprintf("imgmetadata: corrupt %s metadata: %s", e.Format, e.Reason)
}
