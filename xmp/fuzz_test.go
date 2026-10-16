package xmp

import (
	"bytes"
	"strings"
	"testing"
)

// fuzzArenaRatioBound bounds len(x.arena) relative to len(input) for every
// successfully parsed document, with no exclusion: since task #277 the
// composed "P.F" and "P[N].F" keys share a budget of composedKeyFactor ×
// len(input) + composedKeySlack, every other stored string is a copy of input
// content made at most once (twice for an array item, as item and in the
// joined value), and the arena is allocated once from an upper estimate.
const fuzzArenaRatioBound = 8

// fuzzRetainedRatioBound bounds the whole retained parse result — arena,
// retained tree nodes, and Properties entries — relative to len(input).
const fuzzRetainedRatioBound = 32

// retainedBytes approximates the memory that x keeps: the arena capacity,
// the retained nodes, and two string headers per Properties entry.
func retainedBytes(x *XMP) int {
	const stringHeader = 16
	const nodeSize = 72 // unsafe.Sizeof(node{}) on 64-bit platforms
	n := cap(x.arena) + cap(x.nodes)*nodeSize
	for _, m := range x.Properties {
		n += len(m) * 2 * stringHeader
	}
	return n
}

// FuzzParseXMP exercises the XMP parser against arbitrary byte inputs and
// checks the memory bounds of the result and the Encode round trip.
// Run with: go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...
func FuzzParseXMP(f *testing.F) {
	f.Add([]byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`))
	f.Add([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`))
	f.Add([]byte(nestedXMP))
	f.Add([]byte(representativeXMP))
	f.Add(structListDoc("PropertyName", "f", 20))
	// Attribute value normalisation (#344) and undeclared prefixes (#345).
	f.Add(attrDoc("a\tb\nc\r\nd&#9;&#10;&#13;e", `"`))
	f.Add(xmpDoc(`<rdf:Description rdf:about="urn:a&#9;b&#10;c" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:Label="v"/>`))
	f.Add(xmpDoc(`<rdf:Description rdf:about=""><zz:Foo>v</zz:Foo></rdf:Description>`))
	// Security audit 2026-10-16: F-04 (namespace scope index), F-05 (element
	// tree size), F-10 (CDATA chain), at sizes that cross nsIndexThreshold.
	f.Add(auditNSXMLLang(3 * nsIndexThreshold))
	f.Add(auditWrap(`<rdf:Description rdf:about="">` + strings.Repeat("<a/>", 4096) + `</rdf:Description>`))
	f.Add(auditWrap(`<rdf:Description rdf:about="" xmlns:p="http://p/"><p:v>x` + strings.Repeat(`<!----><![CDATA[y]]>`, 512) + `</p:v></rdf:Description>`))

	f.Fuzz(func(t *testing.T, b []byte) {
		x, err := Parse(b)
		if err != nil || x == nil {
			return
		}
		if limit := fuzzArenaRatioBound*len(b) + composedKeySlack; len(x.arena) > limit {
			t.Fatalf("XMP.arena grew to %d bytes for a %d-byte input (%.2fx, limit %dx + %d)",
				len(x.arena), len(b), float64(len(x.arena))/float64(len(b)), fuzzArenaRatioBound, composedKeySlack)
		}
		if got, limit := retainedBytes(x), fuzzRetainedRatioBound*len(b)+composedKeySlack+4096; got > limit {
			t.Fatalf("retained %d bytes for a %d-byte input, limit %d", got, len(b), limit)
		}
		// Encode must accept every parsed value and reach a fixpoint after
		// one more round trip.
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
			t.Fatalf("Encode(Parse(Encode(x))): %v", err)
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
