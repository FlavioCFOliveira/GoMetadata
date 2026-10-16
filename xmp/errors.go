package xmp

import "errors"

// ErrEmptyInput is returned when an XMP Parse call receives an empty byte slice.
var ErrEmptyInput = errors.New("xmp: empty input")

// ErrXMLNestingDepth is returned when XML parsing exceeds 100 levels of nesting.
var ErrXMLNestingDepth = errors.New("xmp: XML nesting depth exceeded 100 levels")

// ErrXMLTooComplex is returned when an XMP packet holds more elements or
// attributes than the parser accepts (524288 elements, 1048576 attributes).
// ISO 16684-1 imposes no limit; this is a local security policy that keeps the
// memory needed to parse a packet proportional to its size.
var ErrXMLTooComplex = errors.New("xmp: XML document has too many elements or attributes")

// ErrGPSValueTooShort is returned when an XMP GPS coordinate string is too short to parse.
var ErrGPSValueTooShort = errors.New("xmp: GPS value too short")

// ErrInvalidGPSFormat is returned when an XMP GPS coordinate string does not match the expected format.
var ErrInvalidGPSFormat = errors.New("xmp: invalid GPS format")

// ErrDocumentTooLarge is returned when the XMP document body exceeds maxXMPDocumentBytes
// after UTF-8 normalisation. This is a local security policy; ISO 16684-1 imposes no limit.
var ErrDocumentTooLarge = errors.New("xmp: document exceeds maximum size")

// ErrXMLNotWellFormed is returned when an XMP packet is not well-formed XML:
// an end tag that does not match its start tag, an end tag without a start
// tag, a malformed start tag, an element that is never closed, or an element or
// attribute whose namespace prefix is not declared (XML 1.0 §3; Namespaces in
// XML 1.0 §5; ISO 16684-1:2019 §7.1). The error text names the offending
// element or prefix.
var ErrXMLNotWellFormed = errors.New("xmp: XML is not well-formed")

// ErrTooLargeForJPEG is returned by PackageForJPEG when the standard XMP
// packet cannot be reduced to the size that fits a JPEG APP1 segment even
// after every movable top-level property has moved to the extended portion
// (the Adobe XMP Toolkit's kXMPErr_TooLargeForJPEG).
var ErrTooLargeForJPEG = errors.New("xmp: standard XMP cannot be reduced enough for a JPEG file")
