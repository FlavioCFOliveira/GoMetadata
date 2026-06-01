package webp

import "errors"

// ErrNotWebP is returned when the input does not begin with a valid RIFF/WEBP header.
var ErrNotWebP = errors.New("webp: not a WebP file")

// ErrFileTooShort is returned when the WebP input is too short to contain a valid RIFF header.
var ErrFileTooShort = errors.New("webp: file too short")

// ErrChunkTooLarge is returned when a RIFF chunk's declared data size exceeds
// maxWebPChunkSize. This prevents a crafted 4-byte size field from triggering
// a multi-gigabyte allocation before any I/O takes place.
var ErrChunkTooLarge = errors.New("webp: chunk size exceeds limit")
