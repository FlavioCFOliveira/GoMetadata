package xmp

// task278_test.go — regression battery for ParseExtended (tasks #278, #315):
// the standard XMP packet and the extended XMP serialisation of a JPEG file
// are recomposed into one data model tree, nested structures of the extended
// portion included, without xmpNote:HasExtendedXMP (Adobe XMP Specification
// Part 3 §1.1.3.1).

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// extStandard returns a standard XMP packet whose xmpNote namespace is ns and
// whose dc:title is title.
func extStandard(ns, title string) []byte {
	return []byte(`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Standard Toolkit">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="` + ns + `" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<xmpNote:HasExtendedXMP>0123456789ABCDEF0123456789ABCDEF</xmpNote:HasExtendedXMP>` +
		`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + title + `</rdf:li></rdf:Alt></dc:title>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>` +
		`<?xpacket end="w"?>`)
}

// extExtended is an extended XMP serialisation (no packet wrapper) whose
// Container:Directory nests a struct inside a struct array item, which the
// flat Properties view cannot hold, and whose dc:title conflicts with the
// standard packet.
const extExtended = `<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Extended Toolkit">` +
	`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about=""` +
	` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
	` xmlns:GImage="http://ns.google.com/photos/1.0/image/"` +
	` xmlns:Container="http://ns.google.com/photos/1.0/container/"` +
	` xmlns:Item="http://ns.google.com/photos/1.0/container/item/"` +
	` GImage:Mime="image/jpeg">` +
	`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Extended title</rdf:li></rdf:Alt></dc:title>` +
	`<Container:Directory><rdf:Seq>` +
	`<rdf:li rdf:parseType="Resource"><Container:Item Item:Mime="image/jpeg" Item:Semantic="Primary"/></rdf:li>` +
	`<rdf:li rdf:parseType="Resource"><Container:Item Item:Length="52666" Item:Mime="image/jpeg" Item:Semantic="GainMap"/></rdf:li>` +
	`</rdf:Seq></Container:Directory>` +
	`</rdf:Description></rdf:RDF></x:xmpmeta>`

const nsGImage = "http://ns.google.com/photos/1.0/image/"

// TestParseExtended_RecomposesOneTree covers both xmpNote namespace URIs.
func TestParseExtended_RecomposesOneTree(t *testing.T) {
	t.Parallel()
	for _, ns := range []string{NSxmpNote, nsXMPNoteLegacy} {
		x, err := ParseExtended(extStandard(ns, "Standard title"), []byte(extExtended))
		if err != nil {
			t.Fatalf("%s: ParseExtended: %v", ns, err)
		}
		// A property only in the extended portion is read.
		if got := x.Get(nsGImage, "Mime"); got != "image/jpeg" {
			t.Errorf("%s: GImage:Mime = %q, want image/jpeg", ns, got)
		}
		// A property in both portions takes the extended value
		// (XMPUtils::MergeFromJPEG).
		if got := x.Get(NSdc, "title"); got != "Extended title" {
			t.Errorf("%s: dc:title = %q, want the extended value", ns, got)
		}
		// HasExtendedXMP is removed (Part 3 §1.1.3.1), in either namespace.
		if got := x.Get(ns, "HasExtendedXMP"); got != "" {
			t.Errorf("%s: HasExtendedXMP = %q, want it removed", ns, got)
		}
		if x.toolkit != "Standard Toolkit" {
			t.Errorf("%s: x:xmptk = %q, want the standard packet's", ns, x.toolkit)
		}

		// The nested structure of the extended portion survives Encode and a
		// second Parse unchanged (task #278: no flattening).
		enc, err := Encode(x)
		if err != nil {
			t.Fatalf("%s: Encode: %v", ns, err)
		}
		s := string(enc)
		for _, want := range []string{"Primary", "GainMap", "52666", "Container:Item", "Item:Semantic"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: encoded packet lacks %q:\n%s", ns, want, s)
			}
		}
		if strings.Contains(s, "HasExtendedXMP") {
			t.Errorf("%s: encoded packet still carries HasExtendedXMP:\n%s", ns, s)
		}
		x2, err := Parse(enc)
		if err != nil {
			t.Fatalf("%s: Parse(Encode): %v", ns, err)
		}
		enc2, err := Encode(x2)
		if err != nil {
			t.Fatalf("%s: second Encode: %v", ns, err)
		}
		if !bytes.Equal(enc, enc2) {
			t.Errorf("%s: round trip changed the packet:\n%s\n---\n%s", ns, enc, enc2)
		}
	}
}

// TestParseExtended_MatchesSingleDocument proves that splitting a document
// into a standard and an extended portion and recomposing it yields what
// parsing the whole document yields.
func TestParseExtended_MatchesSingleDocument(t *testing.T) {
	t.Parallel()
	whole, err := Parse([]byte(nestedXMP))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want, err := Encode(whole)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	std := extStandard(NSxmpNote, "x")
	// The standard portion carries only HasExtendedXMP and a title that the
	// whole document does not have; drop the title from the comparison by
	// using a standard packet without it.
	std = bytes.Replace(std, []byte(`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">x</rdf:li></rdf:Alt></dc:title>`), nil, 1)
	x, err := ParseExtended(std, []byte(nestedXMP))
	if err != nil {
		t.Fatalf("ParseExtended: %v", err)
	}
	x.toolkit = whole.toolkit // the standard packet names its own toolkit
	got, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("recomposed packet differs from the whole document:\n%s\n---\n%s", got, want)
	}
}

// TestParseExtended_Errors covers the error contract.
func TestParseExtended_Errors(t *testing.T) {
	t.Parallel()
	std := extStandard(NSxmpNote, "t")
	tests := []struct {
		name          string
		standard, ext []byte
		want          error
	}{
		{"empty standard", nil, []byte(extExtended), ErrEmptyInput},
		{"empty extended", std, nil, ErrEmptyInput},
		{"extended without properties", std, []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`), ErrNoExtendedProperties},
		// A bare rdf:Description that relies on the namespace declarations of
		// another document is not an XMP document of its own.
		{"fragment", std, []byte(`<rdf:Description rdf:about=""><dc:title>t</dc:title></rdf:Description>`), ErrNoExtendedProperties},
		// An end tag without a start tag is not well-formed (XML 1.0 §3, task #327).
		{"stray end tag", std, []byte(`<rdf:Description rdf:about=""/></rdf:RDF>`), ErrXMLNotWellFormed},
		{"too deep", std, []byte(strings.Repeat("<a>", 200)), ErrXMLNestingDepth},
		{"too large", std, bytes.Repeat([]byte(" "), maxXMPDocumentBytes+1), ErrDocumentTooLarge},
	}
	for _, tc := range tests {
		x, err := ParseExtended(tc.standard, tc.ext)
		if !errors.Is(err, tc.want) || x != nil {
			t.Errorf("%s: ParseExtended = %v, %v; want nil, %v", tc.name, x, err, tc.want)
		}
	}
}

// TestParseExtended_ExtendedOnlyDuplicates proves that an extended portion
// whose properties all repeat the standard packet is not an error: it holds
// properties, and the extended values win.
func TestParseExtended_ExtendedOnlyDuplicates(t *testing.T) {
	t.Parallel()
	ext := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Other</rdf:li></rdf:Alt></dc:title>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`
	x, err := ParseExtended(extStandard(NSxmpNote, "Replaced"), []byte(ext))
	if err != nil {
		t.Fatalf("ParseExtended: %v", err)
	}
	if got := x.Get(NSdc, "title"); got != "Other" {
		t.Errorf("dc:title = %q, want Other", got)
	}
}

// TestParseExtended_ExtendedWinsWholeProperty pins the overlap policy of the
// Adobe XMP Toolkit (XMPUtils::MergeFromJPEG): a simple property and a nested
// property present in both portions take the extended value as a whole —
// fields only the standard copy has are not kept — while a property only in
// the standard packet stays, and rdf:about and x:xmptk come from the
// standard packet.
func TestParseExtended_ExtendedWinsWholeProperty(t *testing.T) {
	t.Parallel()
	std := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Standard Toolkit">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="std-about" xmlns:xmpNote="http://ns.adobe.com/xmp/note/"` +
		` xmlns:GImage="http://ns.google.com/photos/1.0/image/" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` xmlns:Container="http://ns.google.com/photos/1.0/container/" xmlns:Item="http://ns.google.com/photos/1.0/container/item/"` +
		` xmpNote:HasExtendedXMP="0123456789ABCDEF0123456789ABCDEF" GImage:Mime="image/png" tiff:Model="StandardOnly">` +
		`<Container:Directory><rdf:Seq>` +
		`<rdf:li rdf:parseType="Resource"><Container:Item Item:Semantic="MainOnlyItem" Item:Padding="7"/></rdf:li>` +
		`<rdf:li rdf:parseType="Resource"><Container:Item Item:Semantic="SecondMainItem"/></rdf:li>` +
		`</rdf:Seq></Container:Directory>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	x, err := ParseExtended(std, []byte(extExtended))
	if err != nil {
		t.Fatalf("ParseExtended: %v", err)
	}
	if got := x.Get(nsGImage, "Mime"); got != "image/jpeg" {
		t.Errorf("GImage:Mime = %q, want the extended image/jpeg", got)
	}
	if got := x.Get(NStiff, "Model"); got != "StandardOnly" {
		t.Errorf("tiff:Model = %q, want the standard-only value", got)
	}
	if x.about != "std-about" || x.toolkit != "Standard Toolkit" {
		t.Errorf("about, toolkit = %q, %q; want the standard packet's", x.about, x.toolkit)
	}
	enc, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for _, want := range []string{"Primary", "GainMap", "52666"} {
		if !bytes.Contains(enc, []byte(want)) {
			t.Errorf("encoded packet lacks the extended nested value %q", want)
		}
	}
	for _, gone := range []string{"MainOnlyItem", "SecondMainItem", "Padding", "image/png", "HasExtendedXMP"} {
		if bytes.Contains(enc, []byte(gone)) {
			t.Errorf("encoded packet keeps %q of the replaced standard property", gone)
		}
	}
}

// FuzzParseExtended checks that ParseExtended never panics, never keeps
// HasExtendedXMP, and that its result encodes and reaches a fixpoint.
func FuzzParseExtended(f *testing.F) {
	f.Add(extStandard(NSxmpNote, "t"), []byte(extExtended))
	f.Add(extStandard(nsXMPNoteLegacy, "t"), []byte(nestedXMP))
	f.Add([]byte(representativeXMP), []byte(extExtended))
	f.Add([]byte(nestedXMP), []byte(`<rdf:Description/>`))

	f.Fuzz(func(t *testing.T, std, ext []byte) {
		x, err := ParseExtended(std, ext)
		if err != nil || x == nil {
			return
		}
		if limit := fuzzArenaRatioBound*(len(std)+len(ext)) + composedKeySlack; len(x.arena) > limit {
			t.Fatalf("XMP.arena grew to %d bytes for %d+%d input bytes", len(x.arena), len(std), len(ext))
		}
		for _, ns := range []string{NSxmpNote, nsXMPNoteLegacy} {
			if _, ok := x.Properties[ns]["HasExtendedXMP"]; ok {
				t.Fatalf("HasExtendedXMP kept in %s", ns)
			}
		}
		enc, err := Encode(x)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		x2, err := Parse(enc)
		if err != nil {
			t.Fatalf("Parse(Encode(x)): %v", err)
		}
		enc2, err := Encode(x2)
		if err != nil {
			t.Fatalf("second Encode: %v", err)
		}
		x3, err := Parse(enc2)
		if err != nil {
			t.Fatalf("third Parse: %v", err)
		}
		if enc3, _ := Encode(x3); !bytes.Equal(enc3, enc2) {
			t.Fatalf("Encode did not reach a fixpoint:\n%s\n---\n%s", enc2, enc3)
		}
	})
}
