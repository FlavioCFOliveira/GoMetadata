package jpeg

import "errors"

// ErrNotJPEG is returned when the input does not begin with the JPEG SOI marker.
var ErrNotJPEG = errors.New("jpeg: not a JPEG file")

// ErrEXIFPayloadTooLarge is returned when the EXIF payload exceeds the APP1 segment limit.
var ErrEXIFPayloadTooLarge = errors.New("jpeg: EXIF payload exceeds APP1 segment limit")

// ErrIPTCPayloadTooLarge is returned when the IPTC IRB payload exceeds the APP13 segment limit.
var ErrIPTCPayloadTooLarge = errors.New("jpeg: IPTC IRB payload exceeds APP13 segment limit")

// ErrXMPStubTooLarge is returned when the generated extended XMP main stub exceeds the APP1 limit.
var ErrXMPStubTooLarge = errors.New("jpeg: extended XMP main stub exceeds APP1 limit")

// ErrInvalidMarkerPrefix is returned when a segment header does not begin with 0xFF.
var ErrInvalidMarkerPrefix = errors.New("jpeg: invalid marker prefix")

// ErrInvalidMarkerLength is returned when a marker length field is less than 2.
var ErrInvalidMarkerLength = errors.New("jpeg: marker has invalid length")

// ErrSegmentTooLarge is returned when a segment payload would exceed the 65535-byte APP segment limit.
var ErrSegmentTooLarge = errors.New("jpeg: segment payload exceeds APP segment limit")
