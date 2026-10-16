package xmp

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Security audit 2026-10-16 (v1.5.0 pre-clearance), findings F-04, F-05, F-10.
// Sub-test names are the finding IDs.

// auditWrap wraps an rdf:Description body in a minimal packet.
func auditWrap(desc string) []byte {
	return []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + desc + `</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
}

// auditNSXMLLang builds N namespace declarations followed by N xml:lang
// attributes on one element (the F-04 PoC).
func auditNSXMLLang(n int) []byte {
	var sb strings.Builder
	sb.WriteString(`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"`)
	for i := range n {
		fmt.Fprintf(&sb, ` xmlns:a%d="u%d"`, i, i)
	}
	for range n {
		sb.WriteString(` xml:lang="x"`)
	}
	sb.WriteString(`/>`)
	return auditWrap(sb.String())
}

// maxAuditChain bounds the longest hash chain of the binding index in the F-04
// tests. The index keeps its load at or below 2 bindings per bucket, so a chain
// of 24 is a Poisson tail of about 1e-12 per bucket: the bound does not flake,
// and a degenerate index (one long chain, a linear scan) exceeds it by orders
// of magnitude.
const maxAuditChain = 24

// auditIndexedDoc returns a document whose scope holds n bindings a0..a(n-1)
// (uri u0..), indexed as the scanner indexes them.
func auditIndexedDoc(n int) *xmlDoc {
	d := &xmlDoc{}
	d.reset()
	for i := range n {
		d.pushBinding([]byte(fmt.Sprintf("a%d", i)), fmt.Sprintf("u%d", i))
	}
	return d
}

// auditLongestChain returns the number of bindings on the longest chain of the
// binding index of d.
func auditLongestChain(d *xmlDoc) int {
	longest := 0
	for _, head := range d.nsHead {
		n := 0
		for i := head; i != noNode; i = d.scope[i].prev {
			n++
		}
		longest = max(longest, n)
	}
	return longest
}

func TestSecurityAuditV150(t *testing.T) {
	t.Parallel()

	// F-04: resolve scanned the whole scope for every attribute: 5.5 s for
	// 3.4 MiB. The binding index makes a lookup walk one short hash chain. The
	// proof is structural, not a wall-clock bound (which is load-sensitive): the
	// longest chain of an index of 100000 bindings stays a small constant, so N
	// lookups cost O(N), and the xml and last-declared prefixes resolve.
	t.Run("F-04", func(t *testing.T) {
		t.Parallel()
		const n = 100_000
		d := auditIndexedDoc(n)
		if !d.nsIndexed {
			t.Fatal("binding index not built for a scope of 100000 bindings")
		}
		if longest := auditLongestChain(d); longest > maxAuditChain {
			t.Errorf("longest hash chain = %d bindings, want <= %d (lookups would degrade towards a linear scan)", longest, maxAuditChain)
		}
		for _, p := range []string{"xml", "a0", fmt.Sprintf("a%d", n-1)} {
			if _, ok := d.resolve([]byte(p)); !ok {
				t.Errorf("prefix %q not resolved", p)
			}
		}
		if _, ok := d.resolve([]byte("missing")); ok {
			t.Error("undeclared prefix resolved")
		}
		doc := auditNSXMLLang(20_000)
		if _, err := Parse(doc); err != nil {
			t.Fatalf("Parse: %v", err)
		}
	})

	// F-04: a prefix declared early, resolved N times with N other bindings in
	// scope, is the quadratic case that a fast path for "xml" alone misses. The
	// index serves it with the same bounded chain, whatever the prefix.
	t.Run("F-04-late-lookup", func(t *testing.T) {
		t.Parallel()
		const n = 50_000
		d := auditIndexedDoc(n)
		if got, ok := d.resolve([]byte("a0")); !ok || got != "u0" {
			t.Errorf("earliest prefix resolved to %q, %v", got, ok)
		}
		if longest := auditLongestChain(d); longest > maxAuditChain {
			t.Errorf("longest hash chain = %d, want <= %d", longest, maxAuditChain)
		}
		var sb strings.Builder
		sb.WriteString(`<rdf:Description rdf:about="" xmlns:p0="http://p0/"`)
		for i := 1; i < 5_000; i++ {
			fmt.Fprintf(&sb, ` xmlns:p%d="u%d"`, i, i)
		}
		sb.WriteString(`>`)
		for range 5_000 {
			sb.WriteString(`<p0:v>1</p0:v>`)
		}
		sb.WriteString(`</rdf:Description>`)
		if _, err := Parse(auditWrap(sb.String())); err != nil {
			t.Fatalf("Parse: %v", err)
		}
	})

	// F-04: the binding index must resolve exactly as the linear scan does:
	// innermost declaration wins, a redeclaration ends with its element, and
	// an undeclared prefix is still an error, with more bindings in scope than
	// nsIndexThreshold.
	t.Run("F-04-index-semantics", func(t *testing.T) {
		t.Parallel()
		var decls strings.Builder
		for i := range 3 * nsIndexThreshold {
			fmt.Fprintf(&decls, ` xmlns:q%d="http://q%d/"`, i, i)
		}
		d := decls.String()
		doc := auditWrap(`<rdf:Description rdf:about=""` + d + ` xmlns:p="http://outer/">` +
			`<p:a>1</p:a>` +
			`<q7:b xmlns:q7="http://inner/" xmlns:p="http://over/">2</q7:b>` +
			`<q7:c>3</q7:c><p:d>4</p:d><q90:e>5</q90:e>` +
			`</rdf:Description>`)
		x, err := Parse(doc)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		for _, want := range []struct{ ns, key, val string }{
			{"http://outer/", "a", "1"},
			{"http://inner/", "b", "2"},
			{"http://q7/", "c", "3"},
			{"http://outer/", "d", "4"},
			{"http://q90/", "e", "5"},
		} {
			if got := x.Properties[want.ns][want.key]; got != want.val {
				t.Errorf("%s %s = %q, want %q (properties: %v)", want.ns, want.key, got, want.val, x.Properties)
			}
		}
		_, err = Parse(auditWrap(`<rdf:Description rdf:about=""` + d + `><zz:a xmlns:zz2="x">1</zz:a></rdf:Description>`))
		if !errors.Is(err, ErrXMLNotWellFormed) {
			t.Errorf("undeclared prefix: error = %v, want ErrXMLNotWellFormed", err)
		}
	})

	t.Run("F-05-limits", func(t *testing.T) {
		t.Parallel()
		// Exactly at the element limit parses, one above fails.
		for _, tc := range []struct {
			name    string
			desc    string
			wantErr bool
		}{
			// The document holds rdf:RDF and rdf:Description besides the children.
			{"elements at limit", strings.Repeat("<a/>", maxXMLElements-3), false},
			{"elements over limit", strings.Repeat("<a/>", maxXMLElements-1), true},
			{"attributes at limit", "<a" + strings.Repeat(" b=''", maxXMLAttrs-1) + "/>", false},
			{"attributes over limit", "<a" + strings.Repeat(" b=''", maxXMLAttrs+1) + "/>", true},
		} {
			_, err := Parse(auditWrap(`<rdf:Description rdf:about="">` + tc.desc + `</rdf:Description>`))
			if got := errors.Is(err, ErrXMLTooComplex); got != tc.wantErr {
				t.Errorf("%s: error = %v, want ErrXMLTooComplex=%v", tc.name, err, tc.wantErr)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
		}
	})

	// F-10: e.extra += string(content) re-copied the text for every CDATA
	// section that followed markup: 0.65 s for 4 MiB.
	t.Run("F-10", func(t *testing.T) {
		t.Parallel()
		const n = 150_000
		doc := auditWrap(`<rdf:Description rdf:about="" xmlns:p="http://p/"><p:v>x` +
			strings.Repeat(`<!----><![CDATA[y]]>`, n) + `</p:v></rdf:Description>`)
		x, err := Parse(doc) // linear cost is proved by the allocation bound of TestSecurityAuditV150Alloc/F-10
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got, want := x.Properties["http://p/"]["v"], "x"+strings.Repeat("y", n); got != want {
			t.Errorf("value = %d bytes %.20q, want %d bytes", len(got), got, len(want))
		}
	})

	// F-10: the joined text keeps document order across sibling markup and
	// across the first run, and a lone CDATA section after markup still
	// aliases (no allocation).
	t.Run("F-10-order", func(t *testing.T) {
		t.Parallel()
		doc := auditWrap(`<rdf:Description rdf:about="" xmlns:p="http://p/">` +
			`<p:a><!--c--><![CDATA[1]]><!--c--><![CDATA[2]]>tail</p:a>` +
			`<p:b><!--c--><![CDATA[only]]></p:b>` +
			`<p:c>head<!--c--><![CDATA[a]]><!--c--><![CDATA[b]]></p:c>` +
			`</rdf:Description>`)
		x, err := Parse(doc)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		props := x.Properties["http://p/"]
		for key, want := range map[string]string{"a": "12", "b": "only", "c": "headab"} {
			if props[key] != want {
				t.Errorf("%s = %q, want %q", key, props[key], want)
			}
		}
	})
}

// TestSecurityAuditV150Alloc measures process-wide allocation, so it is NOT
// parallel: a sequential test never overlaps the parallel ones.
func TestSecurityAuditV150Alloc(t *testing.T) { //nolint:paralleltest // measures process-wide allocation; it must not overlap the parallel tests
	// F-05: a packet of 3.9 million empty elements made Parse allocate 3 GiB.
	t.Run("F-05", func(t *testing.T) { //nolint:paralleltest // process-wide allocation measurement
		doc := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="">` +
			strings.Repeat("<a/>", (15<<20)/4) + `</rdf:Description></rdf:RDF></x:xmpmeta>`)
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		_, err := Parse(doc)
		runtime.ReadMemStats(&m1)
		if !errors.Is(err, ErrXMLTooComplex) {
			t.Errorf("Parse error = %v, want ErrXMLTooComplex", err)
		}
		// The unfixed code allocates 3028 MiB.
		if got := m1.TotalAlloc - m0.TotalAlloc; got > 20*uint64(len(doc)) {
			t.Errorf("Parse allocated %d MiB for a %d MiB document", got>>20, len(doc)>>20)
		}
	})

	// F-10: e.extra += string(content) re-copied the text for every CDATA
	// section that followed markup, so the bytes copied grew with the square of
	// the section count (150000 sections: about 11 GB). The CDATA sections are
	// joined once now, so the allocation is a small multiple of the document.
	t.Run("F-10", func(t *testing.T) { //nolint:paralleltest // process-wide allocation measurement
		const n = 150_000
		doc := auditWrap(`<rdf:Description rdf:about="" xmlns:p="http://p/"><p:v>x` +
			strings.Repeat(`<!----><![CDATA[y]]>`, n) + `</p:v></rdf:Description>`)
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		_, err := Parse(doc)
		runtime.ReadMemStats(&m1)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got := m1.TotalAlloc - m0.TotalAlloc
		t.Logf("Parse of %d bytes allocated %d bytes", len(doc), got)
		if got > 30*uint64(len(doc)) {
			t.Errorf("Parse allocated %d MiB for a %d KiB document (limit 30x)", got>>20, len(doc)>>10)
		}
	})
}

// BenchmarkParseAuditV150 measures the shapes of findings F-04 and F-10 at
// sizes that ran for seconds before the fixes.
func BenchmarkParseAuditV150(b *testing.B) {
	cdata := auditWrap(`<rdf:Description rdf:about="" xmlns:p="http://p/"><p:v>x` +
		strings.Repeat(`<!----><![CDATA[y]]>`, 100_000) + `</p:v></rdf:Description>`)
	for _, tc := range []struct {
		name string
		doc  []byte
	}{
		{"F-04-ns-xmllang-20k", auditNSXMLLang(20_000)},
		{"F-10-cdata-2MiB", cdata},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(tc.doc)))
			b.ReportAllocs()
			for range b.N {
				if _, err := Parse(tc.doc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
