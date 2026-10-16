package jpeg

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPUtils.cpp:XMPUtils::PackageForJPEG, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file.
//
// writeExtendedXMP is the fallback of the XMP writer (writePartitionedXMP
// partitions the XMP as Part 3 §1.1.3.2 and the Toolkit's PackageForJPEG
// describe): it stores a packet that cannot be parsed, or whose standard
// portion cannot be made to fit, whole as extended XMP behind a standard
// packet that carries only xmpNote:HasExtendedXMP. Like the Toolkit it names
// the extended XMP by the uppercase hexadecimal MD5 digest of its
// serialisation, which it writes without a packet wrapper.

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the digest that Adobe XMP Specification Part 3 §1.1.3.1 mandates for the extended XMP GUID; it is an identifier, not a security control
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// guidLen is the length of the extended XMP GUID: 128 bits written as 32
// ASCII hexadecimal digits (Adobe XMP Specification Part 3 §1.1.3.1).
const guidLen = 32

// hasExtendedXMPName is the local name of the property that carries the GUID
// in the standard XMP packet (Adobe XMP Specification Part 3 §1.1.3.1).
var hasExtendedXMPName = []byte("HasExtendedXMP") //nolint:gochecknoglobals // package-level constant bytes

// xpacketBegin and xpacketEnd open the two processing instructions of an XMP
// packet wrapper (XMP Part 1 §7.3).
var (
	xpacketBegin = []byte("<?xpacket begin=") //nolint:gochecknoglobals // package-level constant bytes
	xpacketEnd   = []byte("<?xpacket end=")   //nolint:gochecknoglobals // package-level constant bytes
)

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

// extendedSerialization returns the extended XMP serialisation of the XMP
// packet: packet without its <?xpacket?> wrapper, its padding, and the white
// space around the document. Adobe XMP Specification Part 3 §1.1.3.1: "The
// extended portion must be serialized without a packet wrapper". A packet
// that does not begin with a wrapper is returned with only the surrounding
// white space removed. The result is a sub-slice of packet.
func extendedSerialization(packet []byte) []byte {
	b := packet[skipXMLSpace(packet, 0):]
	if bytes.HasPrefix(b, xpacketBegin) {
		if end := bytes.Index(b, xmlPIEnd); end >= 0 {
			b = b[end+len(xmlPIEnd):]
			if tail := bytes.LastIndex(b, xpacketEnd); tail >= 0 {
				b = b[:tail]
			}
		}
	}
	end := len(b)
	for end > 0 && isXMLSpace(b[end-1]) {
		end--
	}
	return b[skipXMLSpace(b[:end], 0):end]
}

// xmlPIEnd closes an XML processing instruction (XML 1.0 §2.6).
var xmlPIEnd = []byte("?>") //nolint:gochecknoglobals // package-level constant bytes

// extendedXMPGUID returns the GUID of the extended XMP serialisation ext:
// the MD5 digest of ext in uppercase hexadecimal. Adobe XMP Specification
// Part 3 §1.1.3.1: "The GUID is a 128-bit MD5 digest of the full ExtendedXMP
// serialization", stored "as a 32-byte ASCII hex string, capital A-F".
// Deriving it from the content makes a write deterministic (task #295).
func extendedXMPGUID(ext []byte) [guidLen]byte {
	const hexDigits = "0123456789ABCDEF"
	sum := md5.Sum(ext) //nolint:gosec // G401: see the import comment; the spec mandates MD5
	var g [guidLen]byte
	for i, c := range sum {
		g[2*i] = hexDigits[c>>4]
		g[2*i+1] = hexDigits[c&0x0F]
	}
	return g
}

// writeExtendedXMP writes rawXMP, which is too large for one APP1 segment and
// cannot be partitioned, as a standard XMP packet followed by extended XMP
// APP1 segments (Adobe XMP Specification Part 3 §1.1.3.1):
//
//  1. The extended serialisation is rawXMP without its packet wrapper.
//  2. Its GUID is the uppercase hexadecimal MD5 digest of that
//     serialisation, so the same XMP always produces the same bytes.
//  3. The standard packet is a minimal XMP packet whose only property is
//     xmpNote:HasExtendedXMP, in the NSxmpNote namespace, set to the GUID.
//  4. The serialisation is written in chunks of at most maxExtChunkSize
//     bytes, one extended APP1 segment each.
func writeExtendedXMP(w io.Writer, rawXMP []byte) error {
	ext := extendedSerialization(rawXMP)
	guid := extendedXMPGUID(ext)

	// The literal template is faster and simpler than invoking the xmp
	// package's encoder from the format layer.
	mainXMP := []byte(
		`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="GoMetadata">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about=""` +
			` xmlns:xmpNote="` + xmppkg.NSxmpNote + `"` +
			` xmpNote:HasExtendedXMP="` + string(guid[:]) + `"/>` +
			`</rdf:RDF></x:xmpmeta>` +
			`<?xpacket end="w"?>`,
	)
	if len(mainXMP) > maxXMPPayload {
		// This cannot happen in practice — the template is ~300 bytes — but
		// guard defensively so the error is actionable rather than silent.
		return fmt.Errorf("jpeg: extended XMP: main XMP stub (%d bytes) exceeds APP1 limit: %w", len(mainXMP), ErrXMPStubTooLarge)
	}

	// #216: header + prefix + stub built in one pooled buffer, one w.Write.
	bufPtr := iobuf.Get(segHeaderSize + len(identXMP) + len(mainXMP))
	b := *bufPtr
	copy(b[segHeaderSize:], identXMP)
	copy(b[segHeaderSize+len(identXMP):], mainXMP)
	if err := writeHeaderedBuf(w, markerAPP1, bufPtr); err != nil {
		return err
	}
	return writeExtendedChunks(w, guid[:], ext)
}
