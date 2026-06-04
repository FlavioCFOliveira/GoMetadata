package heif

import "errors"

// ErrMaxNestingDepth is returned when findBox exceeds the maximum recursive nesting depth.
var ErrMaxNestingDepth = errors.New("heif: findBox: exceeded maximum nesting depth")

// ErrCorruptXMP is returned when the rawXMP bytes passed to Inject are not a
// valid XMP packet. This includes the case where a caller accidentally passes an
// internal JPEG extended-XMP wire-frame to the HEIF injector; the wire-frame is a
// JPEG-only internal encoding that cannot be stored as a HEIF/AVIF XMP item.
var ErrCorruptXMP = errors.New("heif: corrupt or invalid XMP data")
