package xmp

// task326_test.go — regression battery for task #326: a UTF-16 or UTF-32 XMP
// packet without a byte-order mark was read as UTF-8 and lost.

import (
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// encodeXMPText returns s in the given encoding ("utf-16be", "utf-16le",
// "utf-32be", "utf-32le"), with a byte-order mark when bom is set.
func encodeXMPText(t *testing.T, s, enc string, bom bool) []byte {
	t.Helper()
	runes := []rune(s)
	if bom {
		runes = append([]rune{0xFEFF}, runes...)
	}
	var out []byte
	switch enc {
	case "utf-16be", "utf-16le":
		var order binary.AppendByteOrder = binary.BigEndian
		if enc == "utf-16le" {
			order = binary.LittleEndian
		}
		for _, u := range utf16.Encode(runes) {
			out = order.AppendUint16(out, u)
		}
	case "utf-32be", "utf-32le":
		var order binary.AppendByteOrder = binary.BigEndian
		if enc == "utf-32le" {
			order = binary.LittleEndian
		}
		for _, r := range runes {
			out = order.AppendUint32(out, uint32(r)) //nolint:gosec // G115: r is a valid code point of the test text
		}
	default:
		t.Fatalf("unknown encoding %q", enc)
	}
	return out
}

// TestTask326DetectEncoding covers every row of the decision table of
// detectEncoding (XMP Part 1 §7.1 p. 9; XML 1.0 Appendix F.1; Toolkit
// DetermineInputEncoding).
func TestTask326DetectEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want xmpEncoding
	}{
		{"empty", nil, encUTF8},
		{"one byte", []byte{'<'}, encUTF8},
		{"UTF-8 text", []byte("<?xpacket"), encUTF8},
		{"UTF-8 BOM", []byte{0xEF, 0xBB, 0xBF, '<', '?'}, encUTF8},
		{"UTF-8 starting with a non-ASCII character", []byte{0xC3, 0xA9, '<'}, encUTF8},
		{"lone 0xFF is not a BOM", []byte{0xFF, 'a', 'b', 'c'}, encUTF8},
		{"UTF-16BE BOM", []byte{0xFE, 0xFF, 0x00, '<'}, encUTF16BE},
		{"UTF-16LE BOM", []byte{0xFF, 0xFE, '<', 0x00}, encUTF16LE},
		{"UTF-32BE BOM", []byte{0x00, 0x00, 0xFE, 0xFF}, encUTF32BE},
		{"UTF-32LE BOM", []byte{0xFF, 0xFE, 0x00, 0x00}, encUTF32LE},
		{"UTF-16BE without BOM", []byte{0x00, '<', 0x00, '?'}, encUTF16BE},
		{"UTF-16LE without BOM", []byte{'<', 0x00, '?', 0x00}, encUTF16LE},
		{"UTF-32BE without BOM", []byte{0x00, 0x00, 0x00, '<'}, encUTF32BE},
		{"UTF-32LE without BOM", []byte{'<', 0x00, 0x00, 0x00}, encUTF32LE},
		{"two bytes 00 nn is UTF-16BE", []byte{0x00, '<'}, encUTF16BE},
		{"two bytes nn 00 is UTF-16LE", []byte{'<', 0x00}, encUTF16LE},
	}
	for _, tc := range tests {
		if got := detectEncoding(tc.in); got != tc.want {
			t.Errorf("%s: detectEncoding(% x) = %d, want %d", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestTask326ParseWithoutBOM requires that UTF-16 and UTF-32 packets, with
// and without a byte-order mark, parse to the properties of the UTF-8 packet
// (checklist PW-05, PW-08, ROB-09).
func TestTask326ParseWithoutBOM(t *testing.T) {
	t.Parallel()
	doc := string(xmpDoc(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/">` +
		`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Täst 日本 😀</rdf:li></rdf:Alt></dc:title>` +
		`<dc:subject><rdf:Bag><rdf:li>one</rdf:li><rdf:li>two</rdf:li></rdf:Bag></dc:subject>` +
		`<xmp:Label>label</xmp:Label></rdf:Description>`))
	want := mustParse(t, []byte(doc))
	if len(want.Properties[NSdc]) != 2 || want.Properties[NSxmp]["Label"] != "label" {
		t.Fatalf("UTF-8 reference parse is wrong: %v", want.Properties)
	}
	for _, enc := range []string{"utf-16be", "utf-16le", "utf-32be", "utf-32le"} {
		for _, bom := range []bool{false, true} {
			name := enc + map[bool]string{false: " without BOM", true: " with BOM"}[bom]
			t.Run("PW-08/"+name, func(t *testing.T) {
				t.Parallel()
				x, err := Parse(encodeXMPText(t, doc, enc, bom))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if !reflect.DeepEqual(x.Properties, want.Properties) {
					t.Errorf("Properties = %v, want %v", x.Properties, want.Properties)
				}
			})
		}
	}
}

// TestTask326Utf8NonASCIIStart guards the divergence from the Toolkit: a
// UTF-8 document that starts with a non-ASCII byte is UTF-8 (XML 1.0
// Appendix F.1), not little-endian UTF-16 or UTF-32.
func TestTask326Utf8NonASCIIStart(t *testing.T) {
	t.Parallel()
	body := strings.Replace(string(xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:Label>é</xmp:Label></rdf:Description>`)),
		`<?xpacket begin="`+"\xef\xbb\xbf"+`" id="W5M0MpCehiHzreSzNTczkc9d"?>`, "é<!-- é -->", 1)
	x := mustParse(t, []byte(body))
	if got := x.Get(NSxmp, "Label"); got != "é" {
		t.Errorf("Label = %q, want %q", got, "é")
	}
}

// TestTask326UTF32NoBOMKeepsFirstCharacter checks that decodeUTF32 does not
// swallow the first character of a packet without a BOM.
func TestTask326UTF32NoBOMKeepsFirstCharacter(t *testing.T) {
	t.Parallel()
	for _, be := range []bool{true, false} {
		enc := "utf-32le"
		if be {
			enc = "utf-32be"
		}
		in := encodeXMPText(t, "<a/>", enc, false)
		if got := string(decodeUTF32(in, be)); got != "<a/>" {
			t.Errorf("%s: decodeUTF32 = %q, want %q", enc, got, "<a/>")
		}
	}
}
