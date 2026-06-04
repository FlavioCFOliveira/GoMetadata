package heif

import "errors"

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
