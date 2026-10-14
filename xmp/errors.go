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

// ErrDocumentTooLarge is returned when the XMP document body exceeds maxXMPDocumentBytes
// after UTF-8 normalisation. This is a local security policy; ISO 16684-1 imposes no limit.
var ErrDocumentTooLarge = errors.New("xmp: document exceeds maximum size")

// ErrXMLNotWellFormed is returned when an XMP packet is not well-formed XML:
// an end tag that does not match its start tag, an end tag without a start
// tag, a malformed start tag, or an element that is never closed (XML 1.0 §3;
// ISO 16684-1:2019 §7.1). The error text names the offending element.
var ErrXMLNotWellFormed = errors.New("xmp: XML is not well-formed")
