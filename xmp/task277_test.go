package xmp

// task277_test.go — regression battery for task #277: the "P[N].F" keys of
// struct array items (and the "P.F" keys of struct fields) repeat the
// property name P in every key, so the flat Properties view grew with
// items × len(P), quadratically in the document size. The parse tree holds
// every name once; composed keys share a budget proportional to the document
// size (project.go), and Encode writes every item back from the tree, so no
// legal name is ever truncated or lost.

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// structListDoc builds a struct array property named propName with items
// items, each holding one short field.
func structListDoc(propName, fieldName string, items int) []byte {
	var b strings.Builder
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:t="http://example.com/t/"><t:`)
	b.WriteString(propName)
	b.WriteString(`><rdf:Seq>`)
	for range items {
		b.WriteString(`<rdf:li rdf:parseType="Resource"><t:`)
		b.WriteString(fieldName)
		b.WriteString(`>v</t:`)
		b.WriteString(fieldName)
		b.WriteString(`></rdf:li>`)
	}
	b.WriteString(`</rdf:Seq></t:`)
	b.WriteString(propName)
	b.WriteString(`></rdf:Description></rdf:RDF></x:xmpmeta>`)
	return []byte(b.String())
}

// TestTask277MemoryBounded parses the reported shape — a 4000-byte property
// name with 2000 items of one short field — and requires the retained heap
// to stay within 32 times the document size (it was about 85 times).
//
//nolint:paralleltest // measures global heap statistics (runtime.ReadMemStats); parallel tests would pollute the delta
func TestTask277MemoryBounded(t *testing.T) {
	doc := structListDoc(strings.Repeat("P", 4000), "f", 2000)
	before := heapAlloc()
	x, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	after := heapAlloc()
	runtime.KeepAlive(doc)
	var grown uint64
	if after > before {
		grown = after - before
	}
	ratio := float64(grown) / float64(len(doc))
	t.Logf("document %d bytes, retained heap %d bytes (%.2fx), arena %d bytes, %d nodes",
		len(doc), grown, ratio, len(x.arena), len(x.nodes))
	if grown > uint64(32*len(doc)) {
		t.Fatalf("retained heap %d bytes is %.1fx the %d-byte document, want <= 32x", grown, ratio, len(doc))
	}
	// Nothing is lost: every item is written back, and writing the result
	// again reproduces it. (The Properties views differ: the written packet
	// is longer than the input, so its composed-key budget is larger.)
	enc := mustEncode(t, x)
	if again := mustEncode(t, mustParse(t, enc)); !bytes.Equal(enc, again) {
		t.Fatal("Encode is not a fixpoint")
	}
	if got := bytes.Count(enc, []byte("<t:f>v</t:f>")); got != 2000 {
		t.Fatalf("written packet holds %d items, want 2000", got)
	}
	runtime.KeepAlive(x)
}

// TestTask277LongNamesRoundTrip verifies that long property and field names
// survive Parse → Encode → Parse byte for byte, both in the Properties keys
// and in the written packet.
func TestTask277LongNamesRoundTrip(t *testing.T) {
	t.Parallel()
	prop := strings.Repeat("Prop", 75) // 300 bytes
	field := strings.Repeat("Field", 60)
	x := mustParse(t, structListDoc(prop, field, 3))
	enc := mustEncode(t, x)
	x2 := mustParse(t, enc)
	for _, xx := range []*XMP{x, x2} {
		for i := range 3 {
			key := prop + "[" + string(rune('0'+i)) + "]." + field
			if got := xx.Get("http://example.com/t/", key); got != "v" {
				t.Fatalf("Get(%q…) = %q, want %q", key[:20], got, "v")
			}
		}
	}
	if !bytes.Contains(enc, []byte("<t:"+prop+">")) || !bytes.Contains(enc, []byte("<t:"+field+">")) {
		t.Fatal("long names were not written intact")
	}
	if a, b := modelDump(x), modelDump(x2); a != b {
		t.Fatal("round trip changed the model")
	}
}

// TestTask277CollidingPrefixes verifies that two struct array properties
// whose 300-byte names differ only in the last byte stay distinct — a
// truncating key builder would merge them.
func TestTask277CollidingPrefixes(t *testing.T) {
	t.Parallel()
	common := strings.Repeat("N", 299)
	doc := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:t="http://example.com/t/">` +
		`<t:` + common + `A><rdf:Bag><rdf:li rdf:parseType="Resource"><t:f>first</t:f></rdf:li></rdf:Bag></t:` + common + `A>` +
		`<t:` + common + `B><rdf:Bag><rdf:li rdf:parseType="Resource"><t:f>second</t:f></rdf:li></rdf:Bag></t:` + common + `B>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`
	x := mustParse(t, []byte(doc))
	x2 := mustParse(t, mustEncode(t, x))
	for _, xx := range []*XMP{x, x2} {
		if got := xx.Get("http://example.com/t/", common+"A[0].f"); got != "first" {
			t.Errorf("…A[0].f = %q, want first", got)
		}
		if got := xx.Get("http://example.com/t/", common+"B[0].f"); got != "second" {
			t.Errorf("…B[0].f = %q, want second", got)
		}
	}
}

// TestTask277BudgetSpentKeepsItems verifies that items whose keys exceed the
// composed-key budget are absent from Properties but are still written by
// Encode, and that an edit of an admitted key does not drop them.
func TestTask277BudgetSpentKeepsItems(t *testing.T) {
	t.Parallel()
	prop := strings.Repeat("Q", 4000)
	x := mustParse(t, structListDoc(prop, "f", 2000))
	m := x.Properties["http://example.com/t/"]
	if len(m) == 0 || len(m) >= 2000 {
		t.Fatalf("got %d composed keys, want some but fewer than 2000 (budget)", len(m))
	}
	m[prop+"[0].f"] = "edited"
	x2 := mustParse(t, mustEncode(t, x))
	if got := x2.Get("http://example.com/t/", prop+"[0].f"); got != "edited" {
		t.Fatalf("edited key = %q", got)
	}
	items := 0
	for it := x2.nodes[x2.nodes[x2.nodes[0].firstChild].firstChild].firstChild; it != noNode; it = x2.nodes[it].next {
		items++
	}
	if items != 2000 {
		t.Fatalf("written array has %d items, want 2000", items)
	}
}
