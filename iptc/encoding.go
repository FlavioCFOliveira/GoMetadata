package iptc

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// decodeString converts a raw IPTC byte value to a UTF-8 string.
// If the CodedCharacterSet dataset (1:90) declares UTF-8 (ESC % G),
// the bytes are returned as-is. Otherwise ISO-8859-1 is assumed per
// IIM §1.5.1 and converted to UTF-8.
func decodeString(b []byte, isUTF8 bool) string {
	if isUTF8 {
		return string(b)
	}
	return decodeISO88591(b)
}

// decodeISO88591 converts ISO-8859-1 (Latin-1) bytes to a UTF-8 string.
//
// IIM §1.5.1 defines ISO-8859-1 as the default coded character set when no
// 1:90 UTF-8 declaration is present. ISO-8859-1 is unique among the common
// 8-bit charsets in that its code point value equals its byte value across
// the entire 0x00-0xFF range (unlike, e.g., Windows-1252, which remaps
// 0x80-0x9F to non-Latin-1 code points). Consequently no decoding table is
// required: every byte maps directly to the identically numbered Unicode
// code point, and the corresponding UTF-8 encoding is produced with the
// standard 1- or 2-byte rule (RFC 3629 §3):
//
//	code point 0x00-0x7F: 1 UTF-8 byte,  unchanged.
//	code point 0x80-0xFF: 2 UTF-8 bytes, 0xC2|0xC3 lead byte + continuation.
//
// Task #205: this replaces a golang.org/x/text/encoding/charmap decoder
// (pool-managed to amortise its own internal allocations) that still cost two
// allocations per call — dec.Bytes' returned []byte, then the string(...)
// conversion of that []byte. Computing the exact output length up front and
// writing directly into a strings.Builder collapses this to a single
// allocation (zero when the input is pure ASCII, since ISO-8859-1 bytes below
// 0x80 are already byte-identical to UTF-8).
func decodeISO88591(b []byte) string {
	extra := 0
	for _, c := range b {
		if c >= 0x80 {
			extra++
		}
	}
	if extra == 0 {
		// Pure ASCII (or empty) input: ISO-8859-1 and UTF-8 agree byte-for-byte
		// below 0x80, so no transcoding is needed. This is the common case for
		// real-world IPTC datasets.
		return string(b)
	}

	var sb strings.Builder
	sb.Grow(len(b) + extra)
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xC0:
			// Code points 0x80-0xBF: UTF-8 lead byte is always 0xC2, and the
			// continuation byte (0x80 | (c & 0x3F)) equals c itself because c's
			// two high bits are already 0b10.
			sb.WriteByte(0xC2)
			sb.WriteByte(c)
		default:
			// Code points 0xC0-0xFF: UTF-8 lead byte is always 0xC3, and the
			// continuation byte (0x80 | (c & 0x3F)) equals c-0x40.
			sb.WriteByte(0xC3)
			sb.WriteByte(c - 0x40)
		}
	}
	return sb.String()
}

// setDecodedValue pre-populates d.decodedValue from d.Value using the stream's
// charset flag. Called by Parse after the full first pass (when the UTF-8 flag
// from dataset 1:90 is known) and by write-path helpers whenever a new Dataset
// is constructed. After this call d.decodedValue is stable and never written
// again by any read accessor, which makes concurrent reads race-free without
// any additional synchronisation.
//
// Callers that supply values already in UTF-8 (all write-path setters — Go
// strings are always UTF-8) pass isUTF8=true to skip the ISO-8859-1 decoder.
//
// The decoded text ends at the first NUL byte: a C-style terminator, or NUL
// padding that some writers append, is not part of the text (Task #386;
// metadata-extractor and ExifTool read IPTC text as a NUL-terminated string).
// A value made only of NUL bytes therefore reads as "". d.Value is not
// modified, so Encode writes the original bytes back unchanged.
func (d *Dataset) setDecodedValue(isUTF8 bool) {
	d.decodedValue = decodeString(cutAtNUL(d.Value), isUTF8)
}

// cutAtNUL returns v up to its first NUL byte, or v itself when it holds none.
//
// Parse calls it for every dataset, and IPTC values are short: eight bytes are
// tested per step with the exact "word has a zero byte" expression (Hacker's
// Delight, §6-1), and the tail is covered by one more, overlapping, word. A
// value with a NUL in it, or one shorter than a word, is searched byte by byte.
// Measured cost: 1.6 ns for 9 bytes, 3.8 ns for 47 bytes; bytes.IndexByte and a
// plain byte loop were both slower on BenchmarkIPTCParseManyDatasets. Task #386.
func cutAtNUL(v []byte) []byte {
	const (
		ones  = 0x0101010101010101
		highs = 0x8080808080808080
	)
	n := len(v)
	if n >= 8 {
		clean := true
		for i := 0; i+8 <= n; i += 8 {
			w := binary.LittleEndian.Uint64(v[i:])
			if (w-ones)&^w&highs != 0 {
				clean = false
				break
			}
		}
		if clean {
			w := binary.LittleEndian.Uint64(v[n-8:]) // the tail, overlapping the last full word
			if (w-ones)&^w&highs == 0 {
				return v
			}
		}
	}
	for i, c := range v {
		if c == 0 {
			return v[:i]
		}
	}
	return v
}

// escPercentG is the ISO 2022 escape sequence for UTF-8 (ESC % G).
// IIM §1.5.1 defines this as the coded character set declaration for UTF-8.
var escPercentG = []byte{0x1B, 0x25, 0x47} //nolint:gochecknoglobals // immutable sentinel; avoids re-allocation on every call

// isUTF8Declaration reports whether b declares UTF-8 encoding for IPTC.
//
// IIM §1.5.1 specifies dataset 1:90 (Coded Character Set) as a field of up
// to 32 octets carrying an ISO 2022 designation sequence. The canonical form
// is the 3-byte sequence ESC % G (0x1B 0x25 0x47). In practice two additional
// encodings appear in real-world files produced by older Adobe software:
//
//   - ESC%G padded with NUL bytes to an even length (e.g. 4 bytes: ESC%G + 0x00).
//     Some versions of Photoshop and Bridge write the field this way.
//   - The ASCII string "UTF8" (0x55 0x54 0x46 0x38) — a non-standard but
//     widely-observed variant from old Adobe Bridge and Photoshop workflows.
//
// Both variants are treated as equivalent to the canonical ESC%G declaration,
// matching the behaviour of ExifTool (see ExifTool source IPTC.pm, sub
// DecodeCodedCharset). Anything else keeps the ISO-8859-1 fallback.
func isUTF8Declaration(b []byte) bool {
	// Canonical: exactly ESC % G, or ESC % G appearing anywhere in the field
	// (handles NUL-padded and leading-garbage variants).
	if bytes.Contains(b, escPercentG) {
		return true
	}
	// Adobe Bridge / old Photoshop non-standard ASCII form.
	return bytes.Equal(b, []byte("UTF8"))
}
