package xmp

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
// Note: checked before bomUTF16LE because FF FE 00 00 also starts with FF FE.
var bomUTF32LE = []byte{0xFF, 0xFE, 0x00, 0x00} //nolint:gochecknoglobals // immutable BOM sentinel

// bomUTF16BE is the UTF-16 big-endian byte order mark.
// XMP Part 1 §7.2: a UTF-16 BE XMP packet begins with FE FF.
var bomUTF16BE = []byte{0xFE, 0xFF} //nolint:gochecknoglobals // immutable BOM sentinel

// bomUTF16LE is the UTF-16 little-endian byte order mark.
// XMP Part 1 §7.2: a UTF-16 LE XMP packet begins with FF FE (when not UTF-32 LE).
var bomUTF16LE = []byte{0xFF, 0xFE} //nolint:gochecknoglobals // immutable BOM sentinel

// xmpEncoding classifies the character encoding of a raw XMP packet buffer.
type xmpEncoding int

const (
	encUTF8    xmpEncoding = iota // native; no transcoding needed
	encUTF16BE                    // UTF-16 big-endian
	encUTF16LE                    // UTF-16 little-endian
	encUTF32BE                    // UTF-32 big-endian (rare)
	encUTF32LE                    // UTF-32 little-endian (rare)
)

// detectEncoding probes the leading BOM bytes of b to determine the character
// encoding of the XMP packet.
//
// XMP Specification Part 1 §7.2 specifies that a compliant XMP packet MUST be
// serialised as UTF-8, UTF-16, or UTF-32.  The encoding is declared by a BOM at
// the start of the serialisation.  For UTF-8, a BOM is optional; for UTF-16/32 it
// is mandatory.
//
// Detection order matters:
//  1. UTF-32 BOMs (4 bytes) are tested before UTF-16 BOMs (2 bytes) because the
//     UTF-32 LE BOM (FF FE 00 00) is a superset of the UTF-16 LE BOM (FF FE).
//  2. Absent a BOM, UTF-8 is assumed (the vast majority of real-world XMP packets).
func detectEncoding(b []byte) xmpEncoding {
	switch {
	case bytes.HasPrefix(b, bomUTF32BE):
		return encUTF32BE
	case bytes.HasPrefix(b, bomUTF32LE):
		return encUTF32LE
	case bytes.HasPrefix(b, bomUTF16BE):
		return encUTF16BE
	case bytes.HasPrefix(b, bomUTF16LE):
		return encUTF16LE
	default:
		return encUTF8
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

// decodeUTF32 decodes a UTF-32 encoded byte slice (with BOM) to UTF-8.
// bigEndian selects byte order.  The leading 4-byte BOM is skipped.
//
// XMP Part 1 §7.2: UTF-32 BE BOM = 00 00 FE FF; UTF-32 LE BOM = FF FE 00 00.
// Each code point occupies exactly 4 bytes.  We validate that len(b) is a
// multiple of 4 (after BOM), and cap output at maxUnescapedXMLBytes to prevent
// unbounded allocation from crafted input.
func decodeUTF32(b []byte, bigEndian bool) []byte { //nolint:cyclop // big/little-endian branch plus uint32 assembly are inherent to the spec; cannot be reduced
	// Skip the 4-byte BOM.
	if len(b) < 4 {
		return nil
	}
	b = b[4:]
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
			cp = uint32(b[i])<<24 | uint32(b[i+1])<<16 | uint32(b[i+2])<<8 | uint32(b[i+3]) //nolint:gosec // G602: loop step is 4 and len(b)%4==0 is validated above; b[i+1..i+3] are always in bounds
		} else {
			cp = uint32(b[i+3])<<24 | uint32(b[i+2])<<16 | uint32(b[i+1])<<8 | uint32(b[i]) //nolint:gosec // G602: same invariant as above; all indices in bounds
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
		out = appendUTF8Rune(out, cp)
		if len(out) >= maxUnescapedXMLBytes {
			break
		}
	}
	return out
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
// This is the entry point called by Scan and Parse before any parsing.
func normaliseToUTF8(b []byte) []byte {
	enc := detectEncoding(b)
	if enc == encUTF8 {
		return b // fast path: already UTF-8
	}
	return toUTF8(b, enc)
}
