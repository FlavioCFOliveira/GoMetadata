package heif

import "errors"

// maxFileSize is the upper bound on the total number of bytes this package will
// read from an io.Reader in a single Extract (slow path) or Inject call. Reads
// via io.ReadAll are wrapped with io.LimitReader(r, maxFileSize+1); if the
// reader delivers more bytes than this limit the operation is aborted with
// ErrFileTooLarge before any allocation proportional to file size is retained.
//
// Real-world HEIF/HEIC/AVIF files are well under 100 MiB; 256 MiB gives ample
// headroom for future camera improvements while bounding worst-case heap
// allocation to a predictable, safe value.
//
// The fast-path Extract (meta box within 64 KB header window) is already safe:
// it reads at most 64 KB for the header and then seeks to individual item
// payloads which are already capped by maxItemPayloadSize. Only the slow-path
// io.ReadAll fallback and the Inject full-file read are affected by this guard.
//
// Declared as a var (not a const) so that tests can lower it temporarily to
// verify the OOM-guard path without allocating 256 MiB of memory.
//
// #140 fix: cap uncapped io.ReadAll calls to prevent OOM on oversized or
// infinite streaming readers.
var maxFileSize int64 = 256 << 20 //nolint:gochecknoglobals // test-overridable cap; never mutated in production paths

// ErrFileTooLarge is returned when the input exceeds maxFileSize. This prevents
// a streaming or adversarially large reader from causing unbounded heap
// allocation. Callers can detect this specific condition with errors.Is.
var ErrFileTooLarge = errors.New("heif: input exceeds maximum file size (256 MiB)")

// ErrMaxNestingDepth is returned when findBox exceeds the maximum recursive nesting depth.
var ErrMaxNestingDepth = errors.New("heif: findBox: exceeded maximum nesting depth")

// ErrCorruptXMP is returned when the rawXMP bytes passed to Inject are not a
// valid XMP packet. This includes the case where a caller accidentally passes an
// internal JPEG extended-XMP wire-frame to the HEIF injector; the wire-frame is a
// JPEG-only internal encoding that cannot be stored as a HEIF/AVIF XMP item.
var ErrCorruptXMP = errors.New("heif: corrupt or invalid XMP data")

// ErrPreserveUnknownSegmentsNotSupported is returned by Inject when
// preserveUnknownSegments is false. HEIF/AVIF ISOBMFF boxes (ftyp, moov,
// mdat, etc.) are structurally mandatory; there is no concept of an
// "unknown optional segment" analogous to JPEG's APPn. Stripping non-metadata
// boxes would corrupt or invalidate the container.
//
// Use PreserveUnknownSegments(true) (the default) when writing to HEIF/AVIF.
var ErrPreserveUnknownSegmentsNotSupported = errors.New("heif: PreserveUnknownSegments(false) is not supported for HEIF/AVIF; ISOBMFF boxes are structurally mandatory")
