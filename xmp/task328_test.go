package xmp

// task328_test.go — regression battery for task #328: leading and trailing
// white space of element text values was trimmed (XML 1.0 §2.10; XMP Part 1
// §7.5).

import (
	"testing"
)

// TestTask328ElementTextIsVerbatim requires that a literal property element
// keeps its white space exactly, as the attribute form of the same value does
// (checklist RDF-09).
func TestTask328ElementTextIsVerbatim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, elem, attr, want string
	}{
		{"leading and trailing spaces", "  two  spaces  ", "  two  spaces  ", "  two  spaces  "},
		{"tabs", "\ttab\t", "&#x9;tab&#x9;", "\ttab\t"},
		{"newlines", "\nline\n", "&#xA;line&#xA;", "\nline\n"},
		{"mixed", " \t\n x \n\t ", " &#x9;&#xA; x &#xA;&#x9; ", " \t\n x \n\t "},
		{"inner white space", "a  b\tc", "a  b&#x9;c", "a  b\tc"},
		{"only spaces", "   ", "   ", "   "},
	}
	for _, tc := range tests {
		t.Run("RDF-09/"+tc.name, func(t *testing.T) {
			t.Parallel()
			elem := mustParse(t, xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:Label>`+tc.elem+`</xmp:Label></rdf:Description>`))
			attr := mustParse(t, xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Label="`+tc.attr+`"/>`))
			if got := elem.Get(NSxmp, "Label"); got != tc.want {
				t.Errorf("element form = %q, want %q", got, tc.want)
			}
			if got := attr.Get(NSxmp, "Label"); got != tc.want {
				t.Errorf("attribute form = %q, want %q", got, tc.want)
			}
			// The value survives Encode and a second Parse.
			again := mustParse(t, mustEncode(t, elem))
			if got := again.Get(NSxmp, "Label"); got != tc.want {
				t.Errorf("after round trip = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTask328LineEnds checks the line-end normalisation that makes verbatim
// text correct (XML 1.0 §2.11): CRLF and CR read as LF, a character
// reference for CR does not.
func TestTask328LineEnds(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, in, want string }{
		{"CRLF", "a\r\nb\r\n", "a\nb\n"},
		{"CR", "a\rb\r", "a\nb\n"},
		{"LF", "a\nb\n", "a\nb\n"},
		{"character reference", "a&#13;b", "a\rb"},
		{"CDATA", "<![CDATA[a\r\nb]]>", "a\nb"},
	}
	for _, tc := range tests {
		x := mustParse(t, xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:Label>`+tc.in+`</xmp:Label></rdf:Description>`))
		if got := x.Get(NSxmp, "Label"); got != tc.want {
			t.Errorf("%s: Label = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestTask328BetweenElementsStaysIgnored requires that the white space
// between the child elements of a node is still formatting, not content, so
// pretty-printed documents parse as before (RDF/XML Syntax §7.2.14).
func TestTask328BetweenElementsStaysIgnored(t *testing.T) {
	t.Parallel()
	x := mustParse(t, xmpDoc("\n  "+`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/">`+"\n   "+
		`<dc:subject>`+"\n    "+`<rdf:Bag>`+"\n     "+`<rdf:li>a</rdf:li>`+"\n     "+`<rdf:li> b </rdf:li>`+"\n    "+`</rdf:Bag>`+"\n   "+`</dc:subject>`+"\n   "+
		`<xmpMM:DerivedFrom rdf:parseType="Resource">`+"\n    "+`<xmpMM:documentID>d</xmpMM:documentID>`+"\n   "+`</xmpMM:DerivedFrom>`+"\n  "+
		`</rdf:Description>`+"\n"))
	if got := x.Get(NSdc, "subject"); got != "a\x1e b " {
		t.Errorf("subject = %q, want %q", got, "a\x1e b ")
	}
	if got := x.Get(NSxmpMM, "DerivedFrom.documentID"); got != "d" {
		t.Errorf("DerivedFrom.documentID = %q, want d", got)
	}
}

// TestTask328EmptyElementStaysEmpty requires that an element without content
// still yields an empty value and not a struct or a dropped property.
func TestTask328EmptyElementStaysEmpty(t *testing.T) {
	t.Parallel()
	x := mustParse(t, xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:Label></xmp:Label><xmp:Rating/></rdf:Description>`))
	for _, name := range []string{"Label", "Rating"} {
		v, ok := x.Properties[NSxmp][name]
		if !ok || v != "" {
			t.Errorf("%s = %q, present %v; want empty and present", name, v, ok)
		}
	}
}
