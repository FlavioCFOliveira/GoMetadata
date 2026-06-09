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
	return "gometadata: truncated file while reading " + e.At
}

// CorruptMetadataError is returned when a metadata segment is structurally
// invalid (bad offsets, impossible lengths, invalid tag types, etc.).
// The message is specific enough for the caller to locate the problem.
//
// # Diagnostic policy — byte offsets and buffer lengths in Reason
//
// Reason strings may include decimal byte offsets (e.g. "IFD offset 2048 out
// of bounds (buf len 1024)") and buffer lengths. These are file-position
// diagnostics that describe the malformed input, not Go-internal implementation
// state. They are intentionally retained because they let callers (and humans
// reading log output) identify the exact byte range of the problem in the source
// file, which is the definition of "actionable" in the CLAUDE.md API contract.
//
// What Reason strings MUST NOT contain:
//   - Go pointer addresses (patterns like "0x" followed by hex digits), which
//     change between runs and expose GC internals.
//   - Unexported Go identifiers, struct field names, or package-qualified type
//     names (e.g. "exif.ifd", "p.count", "metaerr.CorruptMetadataError") that
//     expose implementation vocabulary.
//
// Authors adding new CorruptMetadataError sites must follow this contract.
// The gate test TestCorruptMetadataError_NoGoInternalLeak in metaerr_test.go
// enforces this invariant on representative error strings.
type CorruptMetadataError struct {
	Format string // "EXIF", "IPTC", or "XMP"
	Reason string
}

func (e *CorruptMetadataError) Error() string {
	return fmt.Sprintf("gometadata: corrupt %s metadata: %s", e.Format, e.Reason)
}
