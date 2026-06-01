package iptc

import (
	"bytes"
	"sync"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

// iso88591DecoderPool reuses ISO-8859-1 decoder instances across calls to
// avoid per-call heap allocation. Each decoder is Reset before use so that
// streaming state from a prior call cannot leak into the next.
var iso88591DecoderPool = sync.Pool{ //nolint:gochecknoglobals // pool for perf
	New: func() any { return charmap.ISO8859_1.NewDecoder() },
}

// decodeString converts a raw IPTC byte value to a UTF-8 string.
// If the CodedCharacterSet dataset (1:90) declares UTF-8 (ESC % G),
// the bytes are returned as-is. Otherwise ISO-8859-1 is assumed per
// IIM §1.5.1 and converted to UTF-8.
func decodeString(b []byte, isUTF8 bool) string {
	if isUTF8 {
		return string(b)
	}
	// ISO-8859-1 → UTF-8 via golang.org/x/text; decoder is reused from pool.
	dec := iso88591DecoderPool.Get().(*encoding.Decoder) //nolint:forcetypeassert,revive // pool always holds *encoding.Decoder
	dec.Reset()
	decoded, err := dec.Bytes(b)
	iso88591DecoderPool.Put(dec)
	if err != nil {
		// Fallback: treat as raw bytes; non-ASCII becomes replacement chars.
		return string(b)
	}
	return string(decoded)
}

// stringValue returns the Dataset value as a UTF-8 string, caching the result
// so that repeated calls (e.g. iterating Keywords) pay the ISO-8859-1 → UTF-8
// conversion cost only once. The pointer receiver is required to write back
// the cached result to the Dataset stored in the slice.
func (d *Dataset) stringValue(isUTF8 bool) string {
	if d.decoded {
		return d.decodedValue
	}
	d.decodedValue = decodeString(d.Value, isUTF8)
	d.decoded = true
	return d.decodedValue
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
