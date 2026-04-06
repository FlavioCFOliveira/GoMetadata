package xmp

import "errors"

// ErrEmptyInput is returned when an XMP Parse call receives an empty byte slice.
var ErrEmptyInput = errors.New("xmp: empty input")

// ErrXMLNestingDepth is returned when XML parsing exceeds 100 levels of nesting.
var ErrXMLNestingDepth = errors.New("xmp: XML nesting depth exceeded 100 levels")

// ErrGPSValueTooShort is returned when an XMP GPS coordinate string is too short to parse.
var ErrGPSValueTooShort = errors.New("xmp: GPS value too short")

// ErrInvalidGPSFormat is returned when an XMP GPS coordinate string does not match the expected format.
var ErrInvalidGPSFormat = errors.New("xmp: invalid GPS format")
