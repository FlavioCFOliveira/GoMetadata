package jpeg

import "bytes"

// guidLen is the length of the extended XMP GUID: 128 bits written as 32
// ASCII hexadecimal digits (Adobe XMP Specification Part 3 §1.1.3.1).
const guidLen = 32

// hasExtendedXMPName is the local name of the property that carries the GUID
// in the standard XMP packet (Adobe XMP Specification Part 3 §1.1.3.1).
var hasExtendedXMPName = []byte("HasExtendedXMP") //nolint:gochecknoglobals // package-level constant bytes

// extractGUIDFromMain returns the GUID stored in the xmpNote:HasExtendedXMP
// property of the standard XMP packet main, and whether one was found.
//
// RDF writes a simple property either as a property attribute of
// rdf:Description or as a property element (ISO 16684-1:2019 §7.5; RDF/XML
// Syntax §2.2 and §2.5), so both forms are recognised (task #278):
//
//	xmpNote:HasExtendedXMP="GUID"   (either quote, white space around '=')
//	<xmpNote:HasExtendedXMP>GUID</xmpNote:HasExtendedXMP>
//
// The value must be exactly 32 hexadecimal digits, as the XMP Toolkit
// requires (JPEG_MetaHandler::ProcessXMP compares its length with the GUID
// size), so an element value with surrounding white space is not a GUID. The
// prefix is not resolved to a namespace URI: the GUID only selects the
// extended chunks to reassemble, whose own GUID must match, and
// xmp.ParseExtended removes the property by namespace. The first valid
// occurrence wins.
func extractGUIDFromMain(main []byte) (guid string, ok bool) {
	for rest, base := main, 0; ; {
		i := bytes.Index(rest, hasExtendedXMPName)
		if i < 0 {
			return "", false
		}
		at := base + i
		next := at + len(hasExtendedXMPName)
		if g, found := guidAt(main, at, next); found {
			return g, true
		}
		rest, base = main[next:], next
	}
}

// guidAt reads the GUID of the occurrence of HasExtendedXMP that spans
// main[at:next], when that occurrence is the qualified name of a property
// attribute or of a property element start tag.
func guidAt(main []byte, at, next int) (string, bool) {
	// The name must be the local part of a qualified name: "prefix:".
	if at < 2 || main[at-1] != ':' {
		return "", false
	}
	p := at - 1
	for p > 0 && isNameByte(main[p-1]) {
		p--
	}
	if p == at-1 || p == 0 {
		return "", false // empty prefix, or no markup before the name
	}
	switch c := main[p-1]; {
	case isXMLSpace(c):
		return attributeGUID(main[next:])
	case c == '<':
		return elementGUID(main[next:])
	default:
		return "", false
	}
}

// attributeGUID reads the GUID of a property attribute from rest, the bytes
// that follow its name: Eq ::= S? '=' S? (XML 1.0 §2.3, production 25), then
// a quoted value.
func attributeGUID(rest []byte) (string, bool) {
	j := skipXMLSpace(rest, 0)
	if j == len(rest) || rest[j] != '=' {
		return "", false
	}
	j = skipXMLSpace(rest, j+1)
	if j == len(rest) || (rest[j] != '"' && rest[j] != '\'') {
		return "", false
	}
	return hexGUID(rest[j+1:], rest[j])
}

// elementGUID reads the GUID content of a property element from rest, the
// bytes that follow the element name in its start tag.
func elementGUID(rest []byte) (string, bool) {
	gt := bytes.IndexByte(rest, '>')
	switch {
	case gt < 0:
		return "", false
	case gt > 0 && rest[gt-1] == '/':
		return "", false // empty element
	case gt > 0 && !isXMLSpace(rest[0]):
		return "", false // a longer name such as HasExtendedXMPx
	}
	return hexGUID(rest[gt+1:], '<')
}

// hexGUID returns v[:32] when it holds 32 hexadecimal digits followed by
// term.
func hexGUID(v []byte, term byte) (string, bool) {
	if len(v) <= guidLen || v[guidLen] != term || !isAllHex(v[:guidLen]) {
		return "", false
	}
	return string(v[:guidLen]), true
}

// isNameByte reports whether c can occur in an XML name prefix: an ASCII
// name character, or any byte of a multi-byte UTF-8 sequence (XML 1.0 §2.3).
func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '-' || c == '.' || c >= 0x80
}

// isXMLSpace reports whether c is XML white space (XML 1.0 §2.3, production 3).
func isXMLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// skipXMLSpace returns the index of the first byte of b at or after i that
// is not XML white space.
func skipXMLSpace(b []byte, i int) int {
	for i < len(b) && isXMLSpace(b[i]) {
		i++
	}
	return i
}
