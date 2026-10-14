package xmp

// task283_test.go — regression battery for task #283: the attributes past
// the 32nd and the namespace declarations past the 64th on one element were
// silently dropped by fixed-size scanner buffers. The element tree scanner
// (xmlscan.go) grows both lists on demand.

import (
	"fmt"
	"strings"
	"testing"
)

// manyAttrsDoc builds a top-level rdf:Description that declares nsCount
// namespaces and carries attrCount shorthand properties spread over them.
func manyAttrsDoc(nsCount, attrCount int, declAfterUse bool) string {
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about=""`)
	decls := func() {
		for i := range nsCount {
			fmt.Fprintf(&b, ` xmlns:p%d="http://example.com/ns%d/"`, i, i)
		}
	}
	if !declAfterUse {
		decls()
	}
	for i := range attrCount {
		fmt.Fprintf(&b, ` p%d:prop%d="value %d"`, i%nsCount, i, i)
	}
	if declAfterUse {
		// XML Namespaces §6.1: a declaration is in scope for the whole start
		// tag, including attributes written before it.
		decls()
	}
	b.WriteString(`/></rdf:RDF></x:xmpmeta>`)
	return b.String()
}

// TestTask283ManyAttributesAndNamespaces verifies that every attribute and
// every namespace declaration of one element is read, well past the former
// limits of 32 attributes and 64 declarations, and survives a round trip.
func TestTask283ManyAttributesAndNamespaces(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		nsCount, attrs  int
		declarationLast bool
	}{
		{"40 attributes", 4, 40, false},
		{"131 attributes", 11, 131, false},
		{"70 namespaces", 70, 70, false},
		{"200 namespaces", 200, 400, false},
		{"declarations after use", 70, 140, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := mustParse(t, []byte(manyAttrsDoc(c.nsCount, c.attrs, c.declarationLast)))
			check := func(x *XMP, stage string) {
				t.Helper()
				for i := range c.attrs {
					ns := fmt.Sprintf("http://example.com/ns%d/", i%c.nsCount)
					if got, want := x.Get(ns, fmt.Sprintf("prop%d", i)), fmt.Sprintf("value %d", i); got != want {
						t.Fatalf("%s: prop%d = %q, want %q", stage, i, got, want)
					}
				}
				n := len(x.Properties)
				if _, ok := x.Properties[NSdc]; ok {
					n-- // the caption and copyright of the round trip
				}
				if n != c.nsCount {
					t.Fatalf("%s: %d namespaces, want %d", stage, n, c.nsCount)
				}
			}
			check(x, "parse")
			x.SetCaption("caption")
			x.SetCopyright("copyright")
			check(mustParse(t, mustEncode(t, x)), "round trip")
		})
	}
}

// TestTask283NestedElementManyNamespaces covers declarations on an inner
// element (a struct field) rather than on rdf:Description, and verifies
// that they go out of scope when the element closes.
func TestTask283NestedElementManyNamespaces(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:s="http://example.com/s/"><s:Struct rdf:parseType="Resource"`)
	for i := range 80 {
		fmt.Fprintf(&b, ` xmlns:f%d="http://example.com/f%d/"`, i, i)
	}
	b.WriteString(`>`)
	for i := range 80 {
		fmt.Fprintf(&b, `<f%d:F%d>v%d</f%d:F%d>`, i, i, i, i, i)
	}
	// f0 is out of scope here: this element has no namespace and is ignored.
	b.WriteString(`</s:Struct><f0:Outside>x</f0:Outside></rdf:Description></rdf:RDF></x:xmpmeta>`)
	x := mustParse(t, []byte(b.String()))
	for i := range 80 {
		if got, want := x.Get("http://example.com/s/", fmt.Sprintf("Struct.F%d", i)), fmt.Sprintf("v%d", i); got != want {
			t.Fatalf("Struct.F%d = %q, want %q", i, got, want)
		}
	}
	if len(x.Properties) != 1 {
		t.Fatalf("got %d namespaces, want 1 (f0:Outside must not resolve)", len(x.Properties))
	}
	enc := mustEncode(t, x)
	if a, b := modelDump(x), modelDump(mustParse(t, enc)); a != b {
		t.Fatalf("round trip changed the model:\n%s\n---\n%s", a, b)
	}
}
