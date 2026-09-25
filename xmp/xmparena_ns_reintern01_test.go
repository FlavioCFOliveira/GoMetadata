package xmp

// xmparena_ns_reintern01_test.go — permanent regression battery for
// XMPARENA-NS-REINTERN-01: recordContainerType must intern the resolved
// namespace URI (`ns`) only the first time a given namespace's
// containerTypes entry is created, mirroring storeProperty's guard — not
// unconditionally on every call, which would re-copy a long, once-declared
// namespace URI into XMP.arena for every sibling rdf:Alt/Seq/Bag-typed
// property sharing its prefix. `local` is guarded the same way.
//
// Also covers BUG #277 (backlog, not fixed here — see buildStructInListKey's
// doc in xmp/rdf.go): the revert-regression tests below
// (TestBug277StructInListLongFieldNameRoundTrip,
// TestBug277StructInListPrefixCollisionNoDataLoss) prove struct-in-list
// property/field names round-trip byte-exact with no length truncation.

import (
	"strconv"
	"strings"
	"testing"
)

// arenaAmplificationBound is the constant factor this battery asserts
// XMP.arena must stay within, relative to the input document's length, for
// every scenario in this file. It is deliberately generous — the fixed
// code's actual ratio for these scenarios is close to 1x (see the -v test
// log output, which prints the measured ratio for each case) — so a
// regression that reintroduces unguarded re-interning fails this bound by a
// large, unambiguous margin rather than a borderline one.
const arenaAmplificationBound = 8

// longNamespaceDocNSURILen and longNamespaceDocPropCount are the shared
// parameters for buildLongNamespaceDoc/assertArenaBounded across every
// scenario in this file — kept as named constants (rather than function
// parameters every call site would pass identically) both for clarity and
// to avoid an unparam lint finding.
const (
	longNamespaceDocNSURILen  = 4096
	longNamespaceDocPropCount = 1500
)

// buildLongNamespaceDoc builds an XMP document whose top-level
// rdf:Description declares ONE namespace prefix "t" bound to a
// longNamespaceDocNSURILen-byte URI, then emits longNamespaceDocPropCount
// property elements via genProp (each expected to internally use the "t"
// prefix so they all share the same namespace).
func buildLongNamespaceDoc(genProp func(b *strings.Builder, i int)) []byte {
	var b strings.Builder
	b.WriteString(`<?xpacket begin="" uid="x"?>`)
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">`)
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description xmlns:t="http://example.com/` + strings.Repeat("N", longNamespaceDocNSURILen) + `/">`)
	for i := range longNamespaceDocPropCount {
		genProp(&b, i)
	}
	b.WriteString(`</rdf:Description>`)
	b.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	return []byte(b.String())
}

// assertArenaBounded parses doc, requires it to succeed and to actually
// populate at least longNamespaceDocPropCount distinct property/
// container-type entries (summed across every namespace's inner map — the
// outer map itself only ever has a handful of DISTINCT namespace entries in
// these tests, since all properties intentionally share the SAME
// namespace), and asserts len(x.arena) stays within arenaAmplificationBound
// x len(doc).
func assertArenaBounded(t *testing.T, name string, doc []byte) {
	t.Helper()

	x, err := Parse(doc)
	if err != nil {
		t.Fatalf("%s: Parse: %v", name, err)
	}
	got := 0
	for _, m := range x.Properties {
		got += len(m)
	}
	for _, m := range x.containerTypes {
		got += len(m)
	}
	if got < longNamespaceDocPropCount {
		t.Fatalf("%s: parsed %d total property/container-type entries, want >= %d — the test document did not exercise the intended path", name, got, longNamespaceDocPropCount)
	}

	ratio := float64(len(x.arena)) / float64(len(doc))
	t.Logf("%s: doc=%d bytes, arena=%d bytes, ratio=%.2fx", name, len(doc), len(x.arena), ratio)

	if len(x.arena) > arenaAmplificationBound*len(doc) {
		t.Errorf("%s: arena grew to %d bytes for a %d-byte document (%.2fx) — want <= %dx (XMPARENA-NS-REINTERN-01 class regression: a namespace URI or key component is being re-interned per occurrence instead of once per distinct map key)",
			name, len(x.arena), len(doc), ratio, arenaAmplificationBound)
	}
}

// TestXMPArenaNSReinternContainerSiblings is the primary regression gate:
// N sibling rdf:Seq-typed properties (each a DISTINCT local name, forcing
// recordContainerType to be called once per property) sharing one long
// namespace URI.
func TestXMPArenaNSReinternContainerSiblings(t *testing.T) {
	t.Parallel()

	doc := buildLongNamespaceDoc(func(b *strings.Builder, i int) {
		b.WriteString(`<t:P` + strconv.Itoa(i) + `><rdf:Seq><rdf:li>v</rdf:li></rdf:Seq></t:P` + strconv.Itoa(i) + `>`)
	})
	assertArenaBounded(t, "container-siblings", doc)
}

// TestXMPArenaNSReinternSimpleProperties covers plain (non-collection)
// scalar properties sharing one long namespace URI — exercises
// storeProperty's ns-guard directly (already correct; this is a permanent
// regression gate against it regressing back to recordContainerType's bug).
func TestXMPArenaNSReinternSimpleProperties(t *testing.T) {
	t.Parallel()

	doc := buildLongNamespaceDoc(func(b *strings.Builder, i int) {
		b.WriteString(`<t:P` + strconv.Itoa(i) + `>v</t:P` + strconv.Itoa(i) + `>`)
	})
	assertArenaBounded(t, "simple-properties", doc)
}

// TestXMPArenaNSReinternShorthandAttributes covers shorthand (inline
// attribute) properties sharing one long namespace URI, spread across
// multiple rdf:Description blocks (attrBuf caps at 32 attributes captured
// per element, so reaching n=1500 requires multiple elements) — exercises
// onStartTopLevelDesc's storeProperty(a.ns, ...) call.
func TestXMPArenaNSReinternShorthandAttributes(t *testing.T) {
	t.Parallel()
	const attrsPerDesc = 20 // well under the 32-attribute attrBuf cap

	var b strings.Builder
	b.WriteString(`<?xpacket begin="" uid="x"?>`)
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">`)
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	i := 0
	for i < longNamespaceDocPropCount {
		b.WriteString(`<rdf:Description xmlns:t="http://example.com/` + strings.Repeat("N", longNamespaceDocNSURILen) + `/"`)
		for j := 0; j < attrsPerDesc && i < longNamespaceDocPropCount; j++ {
			b.WriteString(` t:P` + strconv.Itoa(i) + `="v"`)
			i++
		}
		b.WriteString(`/>`)
	}
	b.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	doc := []byte(b.String())

	assertArenaBounded(t, "shorthand-attributes", doc)
}

// TestXMPArenaNSReinternStructFields covers N DISTINCT struct-valued
// properties (each its own top-level property with one inline struct field),
// all sharing one long namespace URI as their PARENT property's namespace —
// exercises onStartStructValueNode's storeProperty(p.propNS, ...) call.
func TestXMPArenaNSReinternStructFields(t *testing.T) {
	t.Parallel()

	doc := buildLongNamespaceDoc(func(b *strings.Builder, i int) {
		b.WriteString(`<t:P` + strconv.Itoa(i) + `><rdf:Description t:f="v"/></t:P` + strconv.Itoa(i) + `>`)
	})
	assertArenaBounded(t, "struct-fields", doc)
}

// TestXMPArenaNSReinternSanityDocumentActuallyReused is a meta-test:
// verifies buildLongNamespaceDoc actually produces documents where the
// namespace declaration text appears exactly once (not once per property),
// so the test documents genuinely exercise the "declare once, reference
// cheaply many times" pattern the finding depends on — otherwise the
// bounded-ratio assertions above would pass vacuously.
func TestXMPArenaNSReinternSanityDocumentActuallyReused(t *testing.T) {
	t.Parallel()
	doc := buildLongNamespaceDoc(func(b *strings.Builder, i int) {
		b.WriteString(`<t:P` + strconv.Itoa(i) + `>v</t:P` + strconv.Itoa(i) + `>`)
	})
	marker := strings.Repeat("N", longNamespaceDocNSURILen)
	count := strings.Count(string(doc), marker)
	if count != 1 {
		t.Fatalf("namespace URI marker appears %d times in the test document, want exactly 1 (declared once, referenced via prefix)", count)
	}
}

// ── BUG #277 revert-regression tests ────────────────────────────────────────
//
// These two tests are the byte-exact proof that struct-in-list property/
// field names are never truncated or collided: ISO 16684-1 / XML 1.0 §2.3
// places no length limit on the Name production, so any length bound on
// these keys must not corrupt (test 1) or drop (test 2) legal metadata.

// TestBug277StructInListLongFieldNameRoundTrip proves a 300-byte
// struct-in-list field name survives Parse -> Encode -> Parse byte-exact,
// with its value intact throughout.
func TestBug277StructInListLongFieldNameRoundTrip(t *testing.T) {
	t.Parallel()

	// All-letter so writeXMLName (xmp/write.go) passes it through unchanged on Encode
	// (only C0 controls and a handful of XML-special bytes are stripped),
	// making a byte-exact round-trip the correct expectation.
	longFieldName := strings.Repeat("F", 300)

	doc := []byte(`<?xpacket begin="" uid="x"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description xmlns:a="http://x/a/">` +
		`<a:Prop><rdf:Seq><rdf:li><rdf:Description a:` + longFieldName + `="value1"/></rdf:li></rdf:Seq></a:Prop>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	const ns = "http://x/a/"
	wantKey := "Prop[0]." + longFieldName
	const wantVal = "value1"

	x1, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x1.Properties[ns][wantKey]; got != wantVal {
		t.Fatalf("precondition failed: Properties[%q][%q] = %q, want %q (full-length key not stored — truncation still present?)", ns, wantKey, got, wantVal)
	}

	encoded, err := Encode(x1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse(encoded): %v", err)
	}
	if got := x2.Properties[ns][wantKey]; got != wantVal {
		t.Errorf("round-trip: Properties[%q][%q] = %q, want %q — long field name corrupted or value lost across Parse->Encode->Parse", ns, wantKey, got, wantVal)
	}
}

// TestBug277StructInListPrefixCollisionNoDataLoss proves two DISTINCT
// struct-in-list parent property names sharing an identical 256-byte prefix
// are stored as two SEPARATE entries with no data loss, and both survive
// Parse -> Encode -> Parse byte-exact.
func TestBug277StructInListPrefixCollisionNoDataLoss(t *testing.T) {
	t.Parallel()

	prefix256 := strings.Repeat("Q", 256)
	parentA := prefix256 + "_PROP_A_DISTINCT"
	parentB := prefix256 + "_PROP_B_DISTINCT"

	doc := []byte(`<?xpacket begin="" uid="x"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description xmlns:a="http://x/a/">` +
		`<a:` + parentA + `><rdf:Seq><rdf:li><rdf:Description a:f="valueA"/></rdf:li></rdf:Seq></a:` + parentA + `>` +
		`<a:` + parentB + `><rdf:Seq><rdf:li><rdf:Description a:f="valueB"/></rdf:li></rdf:Seq></a:` + parentB + `>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	const ns = "http://x/a/"
	keyA := parentA + "[0].f"
	keyB := parentB + "[0].f"

	checkBoth := func(t *testing.T, x *XMP, stage string) {
		t.Helper()
		if got := x.Properties[ns][keyA]; got != "valueA" {
			t.Errorf("%s: Properties[%q][%q] = %q, want %q", stage, ns, keyA, got, "valueA")
		}
		if got := x.Properties[ns][keyB]; got != "valueB" {
			t.Errorf("%s: Properties[%q][%q] = %q, want %q", stage, ns, keyB, got, "valueB")
		}
	}

	x1, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	checkBoth(t, x1, "after first Parse")

	encoded, err := Encode(x1)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse(encoded): %v", err)
	}
	checkBoth(t, x2, "after Parse->Encode->Parse round-trip")
}
