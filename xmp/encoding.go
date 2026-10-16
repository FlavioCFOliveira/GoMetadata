package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPMeta-Parse.cpp:DetermineInputEncoding, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file. Only the
// decision table of detectEncoding is adapted, with the divergence documented
// there; the transcoding is original.

import (
	"bytes"
	"unicode"

	gounicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// bomUTF32BE is the UTF-32 big-endian byte order mark.
// XMP Part 1 §7.2: a UTF-32 BE XMP packet begins with 00 00 FE FF.
var bomUTF32BE = []byte{0x00, 0x00, 0xFE, 0xFF} //nolint:gochecknoglobals // immutable BOM sentinel

// bomUTF32LE is the UTF-32 little-endian byte order mark.
// XMP Part 1 §7.2: a UTF-32 LE XMP packet begins with FF FE 00 00.
var bomUTF32LE = []byte{0xFF, 0xFE, 0x00, 0x00} //nolint:gochecknoglobals // immutable BOM sentinel

// xmpEncoding classifies the character encoding of a raw XMP packet buffer.
type xmpEncoding int

const (
	encUTF8    xmpEncoding = iota // native; no transcoding needed
	encUTF16BE                    // UTF-16 big-endian
	encUTF16LE                    // UTF-16 little-endian
	encUTF32BE                    // UTF-32 big-endian (rare)
	encUTF32LE                    // UTF-32 little-endian (rare)
)

// detectEncoding determines the character encoding of the XMP packet b from
// its leading bytes.
//
// XMP Specification Part 1 (ISO 16684-1:2019) §7.1 (p. 9) and §7.3.2: a packet
// is UTF-8, UTF-16 or UTF-32, and the encoding is recognised from the first
// bytes of the packet. The byte-order mark (BOM) is only an optional prefix:
// the <?xpacket begin="..."?> processing instruction starts with the ASCII
// character '<', and XML 1.0 Appendix F.1 ("Detection Without External
// Encoding Information") identifies UTF-16 and UTF-32 from the pattern of the
// NUL bytes of that first character even when no BOM is present (for example
// 00 3C 00 3F for UTF-16BE "<?"). Packets without a BOM are common in the wild:
// ExifTool's XMP.jpg fixture is one.
//
// The decision table is a port of DetermineInputEncoding in the Adobe XMP
// Toolkit (XMPCore/source/XMPMeta-Parse.cpp, lines 753-822), which assumes
// that the first character is U+FEFF or ASCII and that U+0000 is not allowed:
//
//	EF BB BF        UTF-8 with BOM
//	FE FF           UTF-16BE with BOM
//	00 00 FE FF     UTF-32BE with BOM
//	FF FE 00 00     UTF-32LE with BOM (tested before UTF-16LE, its prefix)
//	FF FE           UTF-16LE with BOM
//	00 00 00 nn     UTF-32BE without BOM
//	00 nn           UTF-16BE without BOM
//	nn 00 00 00     UTF-32LE without BOM
//	nn 00           UTF-16LE without BOM
//	anything else   UTF-8
//
// Divergence from the Toolkit, by design: the Toolkit sends every first byte
// of 0x80 or more that is not EF or FE to its little-endian branch, so a UTF-8
// document that starts with a non-ASCII character (for example C3 A9) is read
// as UTF-16LE or UTF-32LE. XML 1.0 Appendix F.1 recognises only the BOMs and
// the '<' patterns, so here such a document is UTF-8, and only an exact FF FE
// is a little-endian BOM.
func detectEncoding(b []byte) xmpEncoding { //nolint:gocyclo // one branch per row of the Toolkit decision table above
	if len(b) < 2 {
		return encUTF8
	}
	switch c := b[0]; {
	case c == 0:
		// 00 nn: UTF-16BE; 00 00 00 nn and 00 00 FE FF: UTF-32BE.
		if len(b) < 4 || b[1] != 0 {
			return encUTF16BE
		}
		return encUTF32BE
	case c < 0x80:
		// nn mm: UTF-8; nn 00 00 00: UTF-32LE; nn 00: UTF-16LE.
		if b[1] != 0 {
			return encUTF8
		}
		if len(b) < 4 || b[2] != 0 {
			return encUTF16LE
		}
		return encUTF32LE
	case c == 0xFE && b[1] == 0xFF:
		return encUTF16BE
	case c == 0xFF && b[1] == 0xFE:
		if len(b) >= 4 && b[2] == 0 && b[3] == 0 {
			return encUTF32LE
		}
		return encUTF16LE
	default:
		return encUTF8 // EF BB BF, or non-BOM UTF-8
	}
}

// maxXMPTranscodeBytes is the maximum number of input bytes accepted by toUTF8
// for UTF-16 transcoding before rejecting the input.
//
// Security (#38): transform.Bytes allocates an output buffer sized proportional
// to len(b). A UTF-16 input of ~256 MiB (reachable via a crafted PNG/HEIF chunk)
// would cause transform.Bytes to allocate ~512 MiB before any parsing happens.
// Capping at 16 MiB limits the worst-case transcoding allocation to ~32 MiB,
// consistent with the maxUnescapedXMLBytes (1 MiB) cap applied inside the parser.
// Real-world XMP packets embedded in images are typically < 100 KiB.
const maxXMPTranscodeBytes = 16 << 20 // 16 MiB

// toUTF8 transcodes b from the detected non-UTF-8 encoding to UTF-8 and
// returns the result.  The BOM, if present, is consumed by the decoder and
// does not appear in the output.
//
// golang.org/x/text/encoding/unicode decoders strip the BOM and handle both
// byte orders correctly.  transform.Bytes allocates exactly one output buffer
// sized for the expected expansion (UTF-16 → UTF-8 is at most 1.5× for BMP
// characters; surrogates can be 2×).
//
// Security (#38): UTF-16 inputs larger than maxXMPTranscodeBytes are rejected
// (return nil) before transcoding, preventing unbounded allocation.  The UTF-32
// path is not affected here because decodeUTF32 already caps at maxUnescapedXMLBytes.
//
// Error handling: if the transcoding fails (malformed input) or the input exceeds
// the size cap, toUTF8 returns nil.  Callers treat nil as "no valid UTF-8 content"
// and fall back to skipping the packet.
func toUTF8(b []byte, enc xmpEncoding) []byte {
	switch enc {
	case encUTF16BE:
		// #38: reject oversized inputs before allocating the transcode buffer.
		if len(b) > maxXMPTranscodeBytes {
			return nil
		}
		t := gounicode.UTF16(gounicode.BigEndian, gounicode.UseBOM).NewDecoder()
		tr := transform.Transformer(t)
		out, _, err := transform.Bytes(tr, b)
		if err != nil {
			return nil
		}
		return out
	case encUTF16LE:
		// #38: reject oversized inputs before allocating the transcode buffer.
		if len(b) > maxXMPTranscodeBytes {
			return nil
		}
		t := gounicode.UTF16(gounicode.LittleEndian, gounicode.UseBOM).NewDecoder()
		tr := transform.Transformer(t)
		out, _, err := transform.Bytes(tr, b)
		if err != nil {
			return nil
		}
		return out
	case encUTF32BE:
		// golang.org/x/text does not expose a direct UTF-32 codec, but UTF-32
		// is extremely rare in real-world XMP.  Decode manually.
		// decodeUTF32 already caps output at maxUnescapedXMLBytes.
		return decodeUTF32(b, true)
	case encUTF32LE:
		return decodeUTF32(b, false)
	default:
		return b // encUTF8: no transcoding needed
	}
}

// decodeUTF32 decodes a UTF-32 encoded byte slice to UTF-8. bigEndian selects
// byte order. A leading 4-byte BOM is skipped when present; a packet without a
// BOM (XMP Part 1 §7.1 p. 9; XML 1.0 Appendix F.1) is decoded from its first
// byte.
//
// XMP Part 1 §7.2: UTF-32 BE BOM = 00 00 FE FF; UTF-32 LE BOM = FF FE 00 00.
// Each code point occupies exactly 4 bytes.  We validate that len(b) is a
// multiple of 4 (after BOM), and cap output at maxUnescapedXMLBytes to prevent
// unbounded allocation from crafted input.
func decodeUTF32(b []byte, bigEndian bool) []byte { //nolint:cyclop // big/little-endian branch plus uint32 assembly are inherent to the spec; cannot be reduced
	if len(b) < 4 {
		return nil
	}
	b = skipUTF32BOM(b, bigEndian)
	if len(b)%4 != 0 {
		return nil
	}

	// Pre-allocate: each UTF-32 code unit becomes at most 4 UTF-8 bytes.
	// Cap at maxUnescapedXMLBytes to bound memory use.
	nCodePoints := len(b) / 4
	maxOut := min(nCodePoints*4, maxUnescapedXMLBytes)
	out := make([]byte, 0, maxOut)

	for i := 0; i < len(b); i += 4 {
		var cp uint32
		if bigEndian {
			cp = uint32(b[i])<<24 | uint32(b[i+1])<<16 | uint32(b[i+2])<<8 | uint32(b[i+3])
		} else {
			cp = uint32(b[i+3])<<24 | uint32(b[i+2])<<16 | uint32(b[i+1])<<8 | uint32(b[i])
		}
		// #41: Validate code point before encoding.
		// Unicode code points above U+10FFFF are not defined (Unicode 15.0 §3.9 D65).
		// Surrogate code points U+D800–U+DFFF are reserved for UTF-16 encoding
		// and are not valid in UTF-32 or UTF-8 (Unicode §3.8 D73–D76).
		// Both cases are replaced with U+FFFD (REPLACEMENT CHARACTER) per
		// Unicode §5.22 "Best Practice for U+FFFD Substitution".
		if cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF) {
			cp = uint32(unicode.ReplacementChar)
		}
		// #81: Check cap BEFORE appending. A supplementary code point (U+10000–
		// U+10FFFF) encodes to 4 UTF-8 bytes; checking AFTER the append allowed
		// len(out) to reach maxUnescapedXMLBytes+1..+3, triggering an unexpected
		// backing-array reallocation. The pre-check ensures len(out) never exceeds
		// the cap regardless of UTF-8 sequence length (1–4 bytes).
		if len(out)+4 > maxUnescapedXMLBytes {
			break
		}
		out = appendUTF8Rune(out, cp)
	}
	return out
}

// skipUTF32BOM returns b without its leading 4-byte BOM when it has the BOM
// of the stated byte order. The pattern is that of the byte order, so U+FEFF
// is never mistaken for the first character of a packet without a BOM.
func skipUTF32BOM(b []byte, bigEndian bool) []byte {
	bom := bomUTF32LE
	if bigEndian {
		bom = bomUTF32BE
	}
	return bytes.TrimPrefix(b, bom)
}

// appendUTF8Rune encodes the Unicode code point cp as UTF-8 and appends the
// result to dst, returning the extended slice.
//
// Precondition (#41): cp must be a valid Unicode scalar value (≤ U+10FFFF and
// not a surrogate U+D800–U+DFFF).  Callers are responsible for this validation;
// decodeUTF32 replaces invalid code points with unicode.ReplacementChar before
// calling this function.
//
// The integer conversions from uint32/rune to byte are safe here: each case
// masks the value to the relevant bits before narrowing, so no information is
// lost and the result always fits in a byte.
func appendUTF8Rune(dst []byte, cp uint32) []byte {
	r := rune(cp) //nolint:gosec // G115: cp is a validated Unicode scalar value (≤ 0x10FFFF, not surrogate); rune(uint32) is safe
	switch {
	case r < 0x80:
		return append(dst, byte(r))
	case r < 0x800:
		return append(dst,
			byte(0xC0|(r>>6)),   //nolint:gosec // G115: value has at most 5 significant bits after masking; fits in byte
			byte(0x80|(r&0x3F)), //nolint:gosec // G115: value masked to 6 bits; fits in byte
		)
	case r < 0x10000:
		return append(dst,
			byte(0xE0|(r>>12)),       //nolint:gosec // G115: value has at most 4 significant bits after masking; fits in byte
			byte(0x80|((r>>6)&0x3F)), //nolint:gosec // G115: value masked to 6 bits; fits in byte
			byte(0x80|(r&0x3F)),      //nolint:gosec // G115: value masked to 6 bits; fits in byte
		)
	default: // r <= 0x10FFFF
		return append(dst,
			byte(0xF0|(r>>18)),        //nolint:gosec // G115: value has at most 3 significant bits after masking; fits in byte
			byte(0x80|((r>>12)&0x3F)), //nolint:gosec // G115: value masked to 6 bits; fits in byte
			byte(0x80|((r>>6)&0x3F)),  //nolint:gosec // G115: value masked to 6 bits; fits in byte
			byte(0x80|(r&0x3F)),       //nolint:gosec // G115: value masked to 6 bits; fits in byte
		)
	}
}

// normaliseToUTF8 detects the encoding of b and, if non-UTF-8, transcodes it.
// Returns b unchanged for UTF-8 input (zero-copy fast path).
// Returns nil if transcoding fails.
//
// The returned slice (whether b itself or a freshly transcoded buffer) is
// read directly, transiently, and synchronously by parseRDF — see
// transientAliasString's doc (rdf.go) and the XMP.arena field doc (xmp.go)
// for why Parse no longer needs to know or care whether this call
// transcoded: nothing retains a reference to the returned slice beyond the
// single synchronous Parse call regardless of its provenance.
//
// This is the entry point called by Scan and Parse before any parsing.
func normaliseToUTF8(b []byte) []byte {
	enc := detectEncoding(b)
	if enc == encUTF8 {
		return b // fast path: already UTF-8
	}
	return toUTF8(b, enc)
}
