package xmp

// task327_test.go — regression battery for task #327: the XML scanner did not
// match end tags to start tags, so an ill-formed packet was read as a
// truncated tree and written back with most of its properties lost.

import (
	"errors"
	"strings"
	"testing"
)

const rdfOpen = `<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:xmp="http://ns.adobe.com/xap/1.0/">`

// TestTask327WellFormedness requires an error that wraps ErrXMLNotWellFormed
// and names the element for every well-formedness violation of the tree
// (XML 1.0 §3 Element Type Match; XMP Part 1 §7.1; checklist ROB-02).
func TestTask327WellFormedness(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, desc, wantNamed string
	}{
		{"mismatched end tag", rdfOpen + `<dc:format>image/jpeg</dc:title></rdf:Description>`, "dc:title"},
		{"mismatched end tag names the start tag too", rdfOpen + `<dc:format>image/jpeg</dc:title></rdf:Description>`, "dc:format"},
		{"same local name, other prefix", rdfOpen + `<dc:format>x</xmp:format></rdf:Description>`, "xmp:format"},
		{"prefix is part of the name", rdfOpen + `<format>x</dc:format></rdf:Description>`, "dc:format"},
		{"overlapping elements", rdfOpen + `<dc:format><xmp:Label>x</dc:format></xmp:Label></rdf:Description>`, "xmp:Label"},
		{"end tag without start tag", rdfOpen + `<dc:format>x</dc:format></rdf:Description></dc:format>`, "dc:format"},
		{"element not closed", rdfOpen + `<dc:format>x</dc:format>`, "rdf:Description"},
		{"slash inside a name", rdfOpen + `<xmp:TIFF/EPStandardID>1</xmp:TIFF/EPStandardID></rdf:Description>`, "xmp:TIFF"},
		{"markup inside a start tag", rdfOpen + `<dc:title<inject/>>v</dc:title></rdf:Description>`, "dc:title"},
		{"start tag without a name", rdfOpen + `< >x</rdf:Description>`, "name"},
	}
	for _, tc := range tests {
		t.Run("ROB-02/"+tc.name, func(t *testing.T) {
			t.Parallel()
			x, err := Parse(xmpDoc(tc.desc))
			if !errors.Is(err, ErrXMLNotWellFormed) || x != nil {
				t.Fatalf("Parse = %v, %v; want nil, ErrXMLNotWellFormed", x, err)
			}
			if !strings.Contains(err.Error(), tc.wantNamed) {
				t.Errorf("error %q does not name %q", err, tc.wantNamed)
			}
		})
	}
}

// TestTask327WellFormedUnchanged requires that correct documents still parse
// to the same properties, whatever their white space and comments.
func TestTask327WellFormedUnchanged(t *testing.T) {
	t.Parallel()
	docs := map[string]string{
		"nested":              rdfOpen + `<dc:subject><rdf:Bag><rdf:li>a</rdf:li><rdf:li>b</rdf:li></rdf:Bag></dc:subject></rdf:Description>`,
		"end tag with spaces": rdfOpen + `<dc:subject><rdf:Bag><rdf:li>a</rdf:li ><rdf:li>b</rdf:li	></rdf:Bag ></dc:subject></rdf:Description >`,
		"empty element tag":   rdfOpen + `<dc:subject><rdf:Bag><rdf:li>a</rdf:li><rdf:li/><rdf:li>b</rdf:li></rdf:Bag></dc:subject></rdf:Description>`,
		"comments and PIs":    rdfOpen + `<!-- a </x> --><dc:subject><?pi </y> ?><rdf:Bag><rdf:li>a</rdf:li><rdf:li>b</rdf:li></rdf:Bag></dc:subject></rdf:Description>`,
		"CDATA with markup":   rdfOpen + `<dc:subject><rdf:Bag><rdf:li><![CDATA[a</dc:b>]]></rdf:li></rdf:Bag></dc:subject></rdf:Description>`,
	}
	for name, desc := range docs {
		x, err := Parse(xmpDoc(desc))
		if err != nil {
			t.Errorf("%s: Parse: %v", name, err)
			continue
		}
		if len(x.Properties[NSdc]) != 1 || x.Properties[NSdc]["subject"] == "" {
			t.Errorf("%s: Properties = %v", name, x.Properties)
		}
	}
}

// TestTask327ExtendedPacket applies the check to ParseExtended, which
// shares the scanner.
func TestTask327ExtendedPacket(t *testing.T) {
	t.Parallel()
	std := extStandard(NSxmpNote, "t")
	ext := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		rdfOpen + `<dc:format>x</dc:title></rdf:Description></rdf:RDF></x:xmpmeta>`
	if x, err := ParseExtended(std, []byte(ext)); !errors.Is(err, ErrXMLNotWellFormed) || x != nil {
		t.Errorf("ParseExtended = %v, %v; want nil, ErrXMLNotWellFormed", x, err)
	}
}

// FuzzTask327EndTagMatching checks that a document that Parse accepts keeps
// parsing after Encode: Encode output is always well-formed.
func FuzzTask327EndTagMatching(f *testing.F) {
	f.Add(string(xmpDoc(rdfOpen + `<dc:format>x</dc:format></rdf:Description>`)))
	f.Add(string(xmpDoc(rdfOpen + `<dc:format>x</dc:title></rdf:Description>`)))
	f.Fuzz(func(t *testing.T, doc string) {
		x, err := Parse([]byte(doc))
		if err != nil {
			return
		}
		enc, err := Encode(x)
		if err != nil {
			return
		}
		if _, err := Parse(enc); err != nil {
			t.Fatalf("Encode output does not parse: %v\n%s", err, enc)
		}
	})
}
