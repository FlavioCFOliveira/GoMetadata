package cr3

import "errors"

// maxFileSize is the upper bound on the total number of bytes this package will
// read from an io.Reader in a single Extract or Inject call. Reads via
// io.ReadAll are wrapped with io.LimitReader(r, maxFileSize+1); if the reader
// delivers more bytes than this limit the operation is aborted with
// ErrFileTooLarge before any allocation proportional to file size is retained.
//
// Real-world Canon CR3 files are well under 100 MiB; 256 MiB gives ample
// headroom for future camera improvements while bounding worst-case heap
// allocation to a predictable, safe value.
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
var ErrFileTooLarge = errors.New("cr3: input exceeds maximum file size (256 MiB)")

// ErrNoMoovBox is returned when the CR3 container does not contain a moov box.
var ErrNoMoovBox = errors.New("cr3: no moov box found")

// ErrNoCMT1Box is returned by Extract when the moov/UUID structure is present
// but no CMT1 sub-box is found — neither inside the Canon UUID box nor via the
// flat fallback search. This distinguishes "no EXIF metadata" from a structurally
// broken container.
//
// lclevy canon_cr3: CMT1 carries IFD0 (the mandatory TIFF header + IFD entries).
// Its absence means the file has no extractable EXIF.
//
// Extract returns a non-nil error so that callers can distinguish the
// no-CMT1 case from a successful parse. rawXMP and rawIPTC are still returned
// (non-nil) when those sub-boxes are present, allowing other metadata to be used.
//
// The top-level gometadata.Read converts ErrNoCMT1Box to a ParseWarning rather
// than a fatal error, so that XMP and other metadata remain accessible.
var ErrNoCMT1Box = errors.New("cr3: CMT1 sub-box absent; no EXIF data in this CR3 file")

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
