package xmp

// task394_test.go — task #394 (sprint 50): allocation of the parse and
// serialisation of a large XMP document must follow the document, not an
// estimate of its size.

import (
	"bytes"
	"strings"
	"testing"
)

func largeBlobPacket(n int) []byte {
	return []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:ex="http://example.com/ns/" ex:blob="` + strings.Repeat("QUJD", n/4) + `"/>` +
		`</rdf:RDF></x:xmpmeta>`)
}

func TestElemHint(t *testing.T) {
	t.Parallel()
	small := []byte(strings.Repeat("<a>", 100))
	if got, want := elemHint(small), len(small)/48; got != want {
		t.Errorf("elemHint(small) = %d, want the length estimate %d", got, want)
	}
	blob := largeBlobPacket(1 << 20)
	if got := elemHint(blob); got > 8 {
		t.Errorf("elemHint(1 MiB with 3 elements) = %d, want a handful", got)
	}
	// "<" inside CDATA must not make the count exceed the length estimate.
	cdata := []byte("<a><![CDATA[" + strings.Repeat("<", elemLargeDoc) + "]]></a>")
	if got, limit := elemHint(cdata), len(cdata)/48; got > limit {
		t.Errorf("elemHint(CDATA of '<') = %d, want <= %d", got, limit)
	}
	// A dense document keeps the length estimate as the bound.
	dense := []byte("<r>" + strings.Repeat("<a/>", elemLargeDoc/2) + "</r>")
	if got, limit := elemHint(dense), len(dense)/48; got != limit {
		t.Errorf("elemHint(dense) = %d, want %d", got, limit)
	}
}

// TestParseLargeBlobTableSize checks that the element table of a large
// document with few elements is sized by the elements.
func TestParseLargeBlobTableSize(t *testing.T) {
	t.Parallel()
	var d xmlDoc
	d.reset()
	if err := scanXML(largeBlobPacket(1<<20), &d); err != nil {
		t.Fatal(err)
	}
	if got := cap(d.elems); got > 64 {
		t.Errorf("element table capacity = %d for a document of 4 elements", got)
	}
}

// TestEncodeLargePacketOwnership covers the hand-off of the buffer of a large
// packet: the result is the packet, owned by the caller, and two encodes never
// share memory.
func TestEncodeLargePacketOwnership(t *testing.T) {
	t.Parallel()
	x, err := Parse(largeBlobPacket(1 << 20))
	if err != nil {
		t.Fatal(err)
	}
	a, err := Encode(x)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) < encHandOffMin {
		t.Fatalf("packet is %d bytes, want a large one", len(a))
	}
	want := bytes.Clone(a)
	for i := range a {
		a[i] = 'X' // the caller owns the result
	}
	b, err := Encode(x)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, want) {
		t.Error("a second Encode differs after the first result was overwritten: the buffers are shared")
	}
	if cap(b) > len(b)+len(b)/4 {
		t.Errorf("result retains capacity %d for %d bytes", cap(b), len(b))
	}
}
