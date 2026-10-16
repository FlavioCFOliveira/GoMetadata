package xmp

// task344_345_test.go — regression tests for sprint 46 tasks #344 (attribute
// value normalisation) and #345 (unbound namespace prefix).

import (
	"errors"
	"strings"
	"testing"
)

// attrDoc returns an XMP document whose rdf:Description holds the attribute
// xmp:Label with the literal source text val (not escaped).
func attrDoc(val, quote string) []byte {
	return xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Label=` + quote + val + quote + `/>`)
}

// TestTask344AttributeValueNormalisation (RDF-13): XML 1.0 §3.3.3 turns a
// literal TAB, LF and CR of an attribute value into one space each (a CR LF
// pair into one space, §2.11 having reduced it to LF first), and leaves a
// character reference to them as the character. Expat, the parser of the
// Adobe XMP Toolkit, does so; encoding/xml of the Go standard library does
// not (it keeps TAB and LF), and the specification is followed.
func TestTask344AttributeValueNormalisation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, src, want string
	}{
		{"literal tab, line feed", "a\tb\nc", "a b c"},
		{"literal carriage return", "a\rb", "a b"},
		{"literal CR LF is one space", "a\r\nb", "a b"},
		{"literal LF CR is two spaces", "a\n\rb", "a  b"},
		{"leading and trailing", "\t\nx\r", "  x "},
		{"character references stay", "a&#9;b&#10;c&#13;d", "a\tb\nc\rd"},
		{"hex character references stay", "a&#x9;b&#xA;c&#xD;d", "a\tb\nc\rd"},
		{"literal next to a reference", "\t&#9;\n&#10;", " \t \n"},
		{"entity and literal", "a&amp;\tb", "a& b"},
		{"no special characters", "plain value", "plain value"},
	}
	for _, tc := range tests {
		for _, quote := range []string{`"`, `'`} {
			t.Run("RDF-13/"+tc.name+"/"+quote, func(t *testing.T) {
				t.Parallel()
				x, err := Parse(attrDoc(tc.src, quote))
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if got := x.Get(NSxmp, "Label"); got != tc.want {
					t.Errorf("xmp:Label = %q, want %q", got, tc.want)
				}
			})
		}
	}
	// Element text is not normalised: it is verbatim (RDF-09).
	t.Run("RDF-13/element text stays verbatim", func(t *testing.T) {
		t.Parallel()
		x, err := Parse(xmpDoc(`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:Label>a` + "\t\n" + `b</xmp:Label></rdf:Description>`))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := x.Get(NSxmp, "Label"); got != "a\t\nb" {
			t.Errorf("xmp:Label = %q, want the text verbatim", got)
		}
	})
}

// TestTask344EncodeEscapesAttributeWhitespace (RDF-13): Encode writes TAB, LF
// and CR of an attribute value as character references, so that a value
// survives the normalisation of the parser.
func TestTask344EncodeEscapesAttributeWhitespace(t *testing.T) {
	t.Parallel()
	const about = "urn:a\tb\nc\rd"
	x, err := Parse(xmpDoc(`<rdf:Description rdf:about="urn:a&#9;b&#10;c&#13;d" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Label="v"/>`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if x.about != about {
		t.Fatalf("about = %q, want %q", x.about, about)
	}
	enc := mustEncode(t, x)
	encStr := string(enc)
	if strings.ContainsAny(encStr[strings.Index(encStr, `rdf:about="`):strings.Index(encStr, `" xmlns:`)], "\t\n\r") {
		t.Errorf("Encode wrote a literal TAB, LF or CR into an attribute value:\n%s", enc)
	}
	back, err := Parse(enc)
	if err != nil {
		t.Fatalf("Parse(Encode(x)): %v", err)
	}
	if back.about != about {
		t.Errorf("about after a round trip = %q, want %q", back.about, about)
	}
}

// TestTask345UnboundPrefix (ROB-13): Namespaces in XML 1.0 §5, Namespace
// Constraint "Prefix Declared": an element or attribute whose prefix no
// declaration in scope binds makes the document ill-formed (XMP Part 1 §7.1;
// the Toolkit's Expat adapter, which runs with namespace processing, stops at
// it, as does xmllint). The error wraps ErrXMLNotWellFormed and names the
// prefix. The xml prefix is bound by definition, and a declaration binds the
// prefix in the element that carries it and in its descendants only.
func TestTask345UnboundPrefix(t *testing.T) {
	t.Parallel()
	const dc = ` xmlns:dc="http://purl.org/dc/elements/1.1/"`
	errCases := []struct {
		name, desc, wantNamed string
	}{
		{"element property", `<rdf:Description rdf:about=""><zz:Foo>v</zz:Foo></rdf:Description>`, "zz"},
		{"empty element property", `<rdf:Description rdf:about=""><zz:Foo/></rdf:Description>`, "zz"},
		{"property attribute", `<rdf:Description rdf:about="" zz:Foo="v"/>`, "zz"},
		{"attribute of a property element", `<rdf:Description rdf:about=""` + dc + `><dc:title zz:x="1">v</dc:title></rdf:Description>`, "zz"},
		{"nested element", `<rdf:Description rdf:about=""` + dc + `><dc:creator><rdf:Seq><zz:li>a</zz:li></rdf:Seq></dc:creator></rdf:Description>`, "zz"},
		{"declaration out of scope", `<rdf:Description rdf:about=""><a xmlns:zz="urn:z"><zz:b/></a><zz:Foo>v</zz:Foo></rdf:Description>`, "zz"},
		{"declaration of the sibling", `<rdf:Description rdf:about="" xmlns:zz="urn:z"/><rdf:Description rdf:about=""><zz:Foo>v</zz:Foo></rdf:Description>`, "zz"},
		{"xmlns is reserved", `<rdf:Description rdf:about=""><xmlns:Foo>v</xmlns:Foo></rdf:Description>`, "xmlns"},
		{"unbound rdf prefix", `<r:Description rdf:about=""/>`, "r"},
	}
	for _, tc := range errCases {
		t.Run("ROB-13/"+tc.name, func(t *testing.T) {
			t.Parallel()
			x, err := Parse(xmpDoc(tc.desc))
			if !errors.Is(err, ErrXMLNotWellFormed) || x != nil {
				t.Fatalf("Parse = %v, %v; want nil, ErrXMLNotWellFormed", x, err)
			}
			if !strings.Contains(err.Error(), `prefix "`+tc.wantNamed+`"`) {
				t.Errorf("error %q does not name the prefix %q", err, tc.wantNamed)
			}
		})
	}
	okCases := []struct{ name, desc string }{
		{"xml:lang", `<rdf:Description rdf:about=""` + dc + `><dc:title><rdf:Alt><rdf:li xml:lang="x-default">t</rdf:li></rdf:Alt></dc:title></rdf:Description>`},
		{"declared on the element itself", `<rdf:Description rdf:about=""><zz:Foo xmlns:zz="urn:z">v</zz:Foo></rdf:Description>`},
		{"declared after first use in the tag", `<rdf:Description rdf:about="" zz:Foo="v" xmlns:zz="urn:z"/>`},
		{"redeclared in a descendant", `<rdf:Description rdf:about="" xmlns:zz="urn:a"><zz:Foo xmlns:zz="urn:b">v</zz:Foo></rdf:Description>`},
		{"unprefixed about attribute", `<rdf:Description about=""/>`},
	}
	for _, tc := range okCases {
		t.Run("ROB-13/accepted/"+tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(xmpDoc(tc.desc)); err != nil {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
}
