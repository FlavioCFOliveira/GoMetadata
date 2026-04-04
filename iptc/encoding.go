package iptc

import "golang.org/x/text/encoding/charmap"

// decodeString converts a raw IPTC byte value to a UTF-8 string.
// If the CodedCharacterSet dataset (1:90) declares UTF-8 (ESC % G),
// the bytes are returned as-is. Otherwise ISO-8859-1 is assumed per
// IIM §1.5.1 and converted to UTF-8.
func decodeString(b []byte, isUTF8 bool) string {
	if isUTF8 {
		return string(b)
	}
	// ISO-8859-1 → UTF-8 via golang.org/x/text
	decoded, err := charmap.ISO8859_1.NewDecoder().Bytes(b)
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

// isUTF8Declaration reports whether b is the ISO 2022 escape sequence
// that signals UTF-8 encoding in IPTC: ESC % G (IIM §1.5.1).
func isUTF8Declaration(b []byte) bool {
	return len(b) == 3 && b[0] == 0x1B && b[1] == 0x25 && b[2] == 0x47
}
