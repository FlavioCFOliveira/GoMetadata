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

// ErrMaxNestingDepth is no longer returned: the metadata container is selected
// by a bounded, non-recursive scan (task #349). It is kept so that code
// comparing against it keeps compiling.
//
// Deprecated: never returned.
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

// ErrNoMetadataContainer is returned by Inject when it is asked to store EXIF
// or XMP data in a file that has no metadata container box, or whose
// container box is too small to be valid. Without that box the file has no
// item table to hold the metadata, so the request cannot be honoured; the
// output is never silently left without the requested metadata.
var ErrNoMetadataContainer = errors.New("heif: the file has no valid metadata container, so EXIF and XMP cannot be stored in it")

// ErrUnreadableItemLocations is returned by Inject when it is asked to store
// EXIF or XMP data in a file whose item location table is missing, uses an
// unsupported version or field size, or is truncated or too large to be
// rewritten safely. Rewriting such a table would drop items of the file, and
// leaving it untouched would drop the requested metadata, so the request is
// refused instead.
var ErrUnreadableItemLocations = errors.New("heif: the item location table is missing or cannot be read, so metadata cannot be stored in the file")
