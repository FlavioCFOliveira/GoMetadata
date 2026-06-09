package webp

import "errors"

// maxFileSize is the upper bound on the total number of bytes this package will
// read from an io.Reader in a single Inject call. The Inject path reads the
// full file via io.ReadAll; it is wrapped with io.LimitReader(r, maxFileSize+1)
// so that an oversized or infinite streaming reader cannot trigger unbounded
// heap allocation. ErrFileTooLarge is returned when the limit is exceeded,
// before any WebP chunk reconstruction takes place.
//
// Note: the Extract path already reads chunks individually and caps each chunk
// at maxWebPChunkSize (256 MiB), so it is not affected by this guard.
//
// Real-world WebP files are well under 100 MiB; 256 MiB gives ample headroom
// while bounding worst-case heap allocation to a predictable, safe value.
//
// Declared as a var (not a const) so that tests can lower it temporarily to
// verify the OOM-guard path without allocating 256 MiB of memory.
//
// #140 fix: cap uncapped io.ReadAll call in Inject to prevent OOM on oversized
// or infinite streaming readers.
var maxFileSize int64 = 256 << 20 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// ErrFileTooLarge is returned when the input exceeds maxFileSize. This prevents
// a streaming or adversarially large reader from causing unbounded heap
// allocation. Callers can detect this specific condition with errors.Is.
var ErrFileTooLarge = errors.New("webp: input exceeds maximum file size (256 MiB)")

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
