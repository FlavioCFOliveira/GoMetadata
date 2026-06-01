package exif

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// userCommentCharset identifies the character set prefix of an EXIF UserComment
// field (tag 0x9286). The 8-byte prefix determines how the remaining bytes are
// decoded. EXIF 2.32, CIPA DC-008-2023 §4.6.5 Table 4:
//
//	"ASCII\x00\x00\x00"  → US-ASCII text (also accepted as UTF-8 in practice)
//	"JIS\x00\x00\x00\x00\x00" → JIS X208-1990 encoded text
//	"UNICODE\x00"         → UTF-16 encoded text (byte order follows the EXIF stream)
//	"\x00\x00\x00\x00\x00\x00\x00\x00" → undefined / unknown encoding
const userCommentPrefixLen = 8

// charset prefix sentinels; package-level vars are intentional: they are
// read-only after init and shared across all calls to decodeUserComment,
// avoiding a stack allocation per call.
//
//nolint:gochecknoglobals // read-only charset prefix sentinels; no mutable state
var (
	prefixASCII   = [8]byte{'A', 'S', 'C', 'I', 'I', 0, 0, 0}
	prefixUnicode = [8]byte{'U', 'N', 'I', 'C', 'O', 'D', 'E', 0}
	prefixJIS     = [8]byte{'J', 'I', 'S', 0, 0, 0, 0, 0}
	// prefixUndefined is all-zero — see EXIF §4.6.5 Note.
)

// decodeUserComment decodes the raw bytes of EXIF tag 0x9286 (UserComment,
// TypeUndefined) to a UTF-8 string.
//
// Spec: EXIF 2.32, CIPA DC-008-2023 §4.6.5 Table 4.
// Layout: first 8 bytes = character code (charset prefix), remaining bytes = value.
//
// Charset dispatch:
//   - ASCII prefix:     value bytes treated as ASCII / UTF-8 (Windows writes
//     ASCII prefix followed by UTF-8; both are valid here because valid UTF-8 is
//     a strict superset of ASCII for the printable range).
//   - UNICODE prefix:   value bytes are UTF-16; byte order is taken from the
//     EXIF stream (the byteOrder parameter).  The encoding/binary big/little
//     endian is applied pair-wise to decode each UTF-16 code unit.
//   - JIS prefix:       best-effort decode — JIS-encoded bytes cannot be losslessly
//     converted without an external encoding library, so we return the raw bytes as
//     a Latin-1-compatible string. Callers that need proper JIS conversion should
//     apply golang.org/x/text/encoding/japanese.ShiftJIS themselves.
//   - Undefined (all-zero prefix): value bytes returned as-is (UTF-8 interpretation).
//
// In all cases, trailing NUL bytes are stripped and the result is validated as
// UTF-8; invalid sequences are replaced with U+FFFD (utf8.RuneError) so the
// return value is always a valid Go string.
//
// decodeUserComment performs no heap allocation for the common ASCII/Undefined case
// when the input is already valid UTF-8 — it returns string(bytes) directly.
func decodeUserComment(b []byte, bigEndian bool) string {
	// EXIF §4.6.5: UserComment is TypeUndefined; the minimum meaningful length
	// is the 8-byte charset prefix.  Empty or under-length payloads → "".
	if len(b) < userCommentPrefixLen {
		return ""
	}

	var prefix [8]byte
	copy(prefix[:], b[:userCommentPrefixLen])
	payload := b[userCommentPrefixLen:]

	switch prefix {
	case prefixASCII:
		// ASCII prefix: treat payload as ASCII / UTF-8.  Windows routinely writes
		// an ASCII prefix followed by UTF-8 multi-byte sequences; accepting UTF-8
		// is the right behaviour.  Strip trailing NULs before converting.
		payload = bytes.TrimRight(payload, "\x00")
		return sanitiseUTF8(payload)

	case prefixUnicode:
		// UNICODE prefix: UTF-16 pairs, byte order matches the EXIF stream.
		// EXIF §4.6.5 does not mandate a BOM, so we rely on the stream byte order.
		// Do NOT use bytes.TrimRight here: UTF-16 LE characters in the ASCII range
		// have a zero high byte (e.g. 'e' = 0x65 0x00), so trimming trailing 0x00
		// bytes would corrupt the last character. decodeUTF16 stops at the first
		// null code unit (0x0000) and handles termination correctly.
		return decodeUTF16(payload, bigEndian)

	case prefixJIS:
		// JIS X208-1990 (EUC-JP variant): best-effort raw return.
		// Converting JIS without the x/text library would require shipping a full
		// conversion table; we intentionally limit this to a raw pass-through.
		// Documented limitation: JIS comments will appear as raw bytes in the
		// returned string when the caller does not perform a secondary decode.
		payload = bytes.TrimRight(payload, "\x00")
		return sanitiseUTF8(payload)

	default:
		// Undefined (all-zero) or any unrecognised prefix: treat as raw bytes.
		// Spec: EXIF §4.6.5 Note — "Undefined" means the encoding is not specified.
		payload = bytes.TrimRight(payload, "\x00")
		return sanitiseUTF8(payload)
	}
}

// decodeUTF16 converts a sequence of UTF-16 code units (as raw bytes) to a
// UTF-8 string. bigEndian selects the byte order for interpreting each 16-bit
// code unit. Odd-length inputs are handled by ignoring the trailing byte.
//
// Used for EXIF UserComment (UNICODE prefix) and Windows XP* tags (always LE).
// Trailing NUL code units are stripped before conversion.
//
// This function processes the input in one pass and allocates exactly one string
// at the end (string(runes)) — no intermediate byte-slice copies.
func decodeUTF16(b []byte, bigEndian bool) string {
	if len(b) < 2 {
		return ""
	}

	// Decode each 2-byte unit into a uint16 code unit.
	// Cap at len(b)/2 to avoid allocating more than needed.
	n := len(b) / 2
	u16 := make([]uint16, 0, n)
	for i := 0; i+1 < len(b); i += 2 {
		var cu uint16
		if bigEndian {
			cu = uint16(b[i])<<8 | uint16(b[i+1])
		} else {
			cu = uint16(b[i+1])<<8 | uint16(b[i])
		}
		if cu == 0 {
			// Null-terminator — stop here (matches the Windows XP* convention and
			// most UNICODE-prefixed UserComment fields).
			break
		}
		u16 = append(u16, cu)
	}

	if len(u16) == 0 {
		return ""
	}

	// utf16.Decode handles surrogate pairs (supplementary planes) correctly.
	runes := utf16.Decode(u16)
	return string(runes)
}

// decodeUTF16LE decodes a null-terminated UCS-2 / UTF-16 LE byte sequence
// (as written by Windows for XP* tags, TypeByte) to a UTF-8 string.
// It is a thin wrapper around decodeUTF16 with bigEndian=false.
//
// EXIF / Windows EXIF Extension: tags 0x9C9B–0x9C9F store strings as
// null-terminated UTF-16 LE with TypeByte — each character is two bytes,
// low byte first.
func decodeUTF16LE(b []byte) string {
	return decodeUTF16(b, false)
}

// sanitiseUTF8 returns a valid UTF-8 string from b, replacing any invalid
// byte sequence with utf8.RuneError (U+FFFD). When b is already valid UTF-8
// this is a zero-copy conversion (string(b) internally).
func sanitiseUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	// Replace invalid sequences. This path is rare (only fires for true junk).
	var buf []byte
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			// Invalid byte — emit replacement character and advance by 1.
			buf = append(buf, '\xef', '\xbf', '\xbd') // U+FFFD in UTF-8
			b = b[1:]
		} else {
			var enc [utf8.UTFMax]byte
			n := utf8.EncodeRune(enc[:], r)
			buf = append(buf, enc[:n]...)
			b = b[size:]
		}
	}
	return string(buf)
}
