package cr3

import "errors"

// ErrNoMoovBox is returned when the CR3 container does not contain a moov box.
var ErrNoMoovBox = errors.New("cr3: no moov box found")

// ErrWriteNotSupported was returned by Inject when CR3 writes were blocked
// (task #56 safe gate). As of task #91, CR3 writes with stco/co64 relocation
// are fully supported. This sentinel is preserved for compatibility — it is no
// longer returned by Inject under normal conditions.
//
// Deprecated: CR3 write is now supported. This error is retained for API
// compatibility and may be removed in a future major release.
var ErrWriteNotSupported = errors.New("cr3: metadata write not supported: stco/co64 offset relocation is required but not yet implemented")

// ErrStcoOverflow is returned by Inject when a stco chunk-offset relocation
// would produce a value that exceeds the maximum uint32 range (4 294 967 295).
// stco boxes store 32-bit absolute file offsets; if the relocated offset would
// require more than 32 bits, the write is aborted rather than silently
// truncating the value and corrupting the file.
//
// This situation can only arise for very large CR3 files (> 4 GiB) where moov
// grows such that a chunk offset crosses the 4 GiB boundary. Such files should
// use co64 (64-bit offsets) instead of stco. Canon cameras that produce files
// this large always use co64 in practice; stco overflow is therefore a
// theoretical concern for ordinary shooting workflows.
var ErrStcoOverflow = errors.New("cr3: stco chunk offset would overflow uint32 after relocation")

// ErrPreserveUnknownSegmentsNotSupported is returned by Inject when
// preserveUnknownSegments is false. CR3 is an ISOBMFF container; its boxes
// (ftyp, moov, mdat, UUID, etc.) are structurally mandatory. There is no
// concept of an "unknown optional segment" analogous to JPEG's APPn segments.
// Stripping non-metadata boxes would corrupt the container.
//
// Use PreserveUnknownSegments(true) (the default) when writing to CR3.
var ErrPreserveUnknownSegmentsNotSupported = errors.New("cr3: PreserveUnknownSegments(false) is not supported for CR3; ISOBMFF boxes are structurally mandatory")
