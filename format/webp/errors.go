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

// ErrCorruptXMP is returned when the rawXMP bytes passed to Inject are not a
// valid XMP packet. This includes the case where a caller accidentally passes an
// internal JPEG extended-XMP wire-frame to the WebP injector; the wire-frame is a
// JPEG-only internal encoding that cannot be stored as a WebP XMP chunk.
var ErrCorruptXMP = errors.New("webp: corrupt or invalid XMP data")

// ErrPreserveUnknownSegmentsNotSupported is returned by Inject when
// preserveUnknownSegments is false. WebP RIFF chunks other than EXIF and XMP
// (VP8, VP8L, VP8X, ANIM, ANMF, ALPH, etc.) are structurally required for
// correct image decoding and must never be stripped. Selective removal of
// "unknown" chunks is therefore not supported.
//
// Use PreserveUnknownSegments(true) (the default) when writing to WebP.
var ErrPreserveUnknownSegmentsNotSupported = errors.New("webp: PreserveUnknownSegments(false) is not supported for WebP; image and structural chunks must be preserved")
