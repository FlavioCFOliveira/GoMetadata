// Package metaerr defines shared error types for metadata parsing and writing.
// These types live in an internal package so they can be used by both the
// root package and the format/parser sub-packages without creating import cycles.
package metaerr

import "fmt"

// TruncatedFileError is returned when the input ends unexpectedly before a
// required structure could be read.
type TruncatedFileError struct {
	// At describes what was being read when the truncation was detected.
	At string
}

func (e *TruncatedFileError) Error() string {
	return fmt.Sprintf("gometadata: truncated file while reading %s", e.At)
}

// CorruptMetadataError is returned when a metadata segment is structurally
// invalid (bad offsets, impossible lengths, invalid tag types, etc.).
// The message is specific enough for the caller to locate the problem.
type CorruptMetadataError struct {
	Format string // "EXIF", "IPTC", or "XMP"
	Reason string
}

func (e *CorruptMetadataError) Error() string {
	return fmt.Sprintf("gometadata: corrupt %s metadata: %s", e.Format, e.Reason)
}
