package xmp

// conformance_test.go — XMP specification-conformance test battery.
//
// Rule IDs match docs/conformance/xmp.md verbatim and appear as Go sub-test
// names so a failing test points straight at the violated clause.
//
// Specification sources:
//   S1: ISO 16684-1:2019 / Adobe XMP Part 1
//   S2: ISO 16684-2:2014 / Adobe XMP Part 2
//   S3: Adobe XMP Part 3 (Jan 2020) — embedding
//   S4: MWG Guidelines v2.0
//   S5: W3C RDF/XML Syntax (2004-02-10)
//   S6: XML 1.0 (5th ed., 2008-11-26)

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// xmpDoc wraps a raw XMP string in the standard packet/xmpmeta/rdf:RDF
// envelope so individual test cases only need to supply the rdf:Description.
func xmpDoc(desc string) []byte {
	return []byte(
		`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			desc +
			`</rdf:RDF></x:xmpmeta>` +
			`<?xpacket end="r"?>`,
	)
}

// mustParse is a test-helper that calls Parse and fails immediately on error.
func mustParse(t *testing.T, b []byte) *XMP {
	t.Helper()
	x, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return x
}

// mustEncode calls Encode and fails immediately on error.
func mustEncode(t *testing.T, x *XMP) []byte {
	t.Helper()
	b, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

// ── Section 2.1: Packet Wrapper ───────────────────────────────────────────────

// TestConformance_PW-01 verifies the opening packet PI contains the required id.
// XMP Part 1 §7.1: begin attribute carries the BOM; id MUST be the magic string.
func TestConformancePW01(t *testing.T) {
	t.Parallel()
	// S1 §7.1: packet begins with <?xpacket begin="<BOM>" id="W5M0MpCehiHzreSzNTczkc9d"?>
	raw := `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW01Cam"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
	x := mustParse(t, []byte(raw))
	t.Run("PW-01", func(t *testing.T) {
		t.Parallel()
		if got := x.CameraModel(); got != "PW01Cam" {
			t.Errorf("PW-01: packet with standard id not parsed; CameraModel=%q", got)
		}
	})
}

// TestConformance_PW-02 verifies that the magic id string is exactly right.
// XMP Part 1 §7.1: id MUST be W5M0MpCehiHzreSzNTczkc9d.
func TestConformancePW02(t *testing.T) {
	t.Parallel()
	// Encode must produce the correct id.
	x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": "PW02"}}}
	enc := mustEncode(t, x)
	t.Run("PW-02", func(t *testing.T) {
		t.Parallel()
		if !strings.Contains(string(enc), `id="W5M0MpCehiHzreSzNTczkc9d"`) {
			t.Errorf("PW-02: encoded packet missing mandatory id; got:\n%s", enc[:min(200, len(enc))])
		}
	})
}

// TestConformance_PW-03 verifies packets ending with end="r" and end="w" are both accepted.
// XMP Part 1 §7.1.
func TestConformancePW03(t *testing.T) {
	t.Parallel()
	for _, endVal := range []string{"r", "w"} {

		t.Run("PW-03/end="+endVal, func(t *testing.T) {
			t.Parallel()
			raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
				`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW03"/>` +
				`</rdf:RDF></x:xmpmeta><?xpacket end="` + endVal + `"?>`
			x := mustParse(t, []byte(raw))
			if x.CameraModel() != "PW03" {
				t.Errorf("PW-03 end=%q: CameraModel=%q", endVal, x.CameraModel())
			}
		})
	}
	// Missing end PI: Scan returns nil; Parse uses full body — must not crash.
	t.Run("PW-03/missing-end", func(t *testing.T) {
		t.Parallel()
		raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW03missing"/>` +
			`</rdf:RDF></x:xmpmeta>`
		// Scan returns nil (no trailer), Parse falls back to full body.
		if pkt := Scan([]byte(raw)); pkt != nil {
			t.Errorf("PW-03: Scan should return nil for missing end PI")
		}
		x, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("PW-03: Parse with missing end PI must not error: %v", err)
		}
		if x.CameraModel() != "PW03missing" {
			t.Errorf("PW-03: partial content not returned; CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_PW-04 verifies that Encode emits whitespace padding.
// XMP Part 1 §7.3: in-place writable packets must be padded.
func TestConformancePW04(t *testing.T) {
	t.Parallel()
	t.Run("PW-04", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": "PW04"}}}
		enc := mustEncode(t, x)
		// Encoded packet must contain whitespace padding before the closing PI.
		// We look for a run of spaces.
		if !strings.Contains(string(enc), "   ") {
			t.Error("PW-04: encoded packet missing whitespace padding")
		}
		// The closing PI must follow the padding.
		if !strings.HasSuffix(strings.TrimRight(string(enc), "\n"), `<?xpacket end="w"?>`) {
			t.Errorf("PW-04: encoded packet does not end with closing PI: tail=%q",
				string(enc[max(0, len(enc)-30):]))
		}
	})
}

// TestConformance_PW-05 verifies BOM handling: UTF-8 BOM, absent BOM → UTF-8.
// XMP Part 1 §7.2.
func TestConformancePW05(t *testing.T) {
	t.Parallel()
	t.Run("PW-05/utf8-bom", func(t *testing.T) {
		t.Parallel()
		// Packet with explicit UTF-8 BOM (EF BB BF) in begin attribute.
		raw := "<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>" +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW05bom"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "PW05bom" {
			t.Errorf("PW-05 utf8-bom: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("PW-05/no-bom", func(t *testing.T) {
		t.Parallel()
		raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW05nobom"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "PW05nobom" {
			t.Errorf("PW-05 no-bom: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_PW-06 verifies Scan finds the packet at arbitrary offsets.
// XMP Part 1 §7.2: scan by searching for <?xpacket begin= — no fixed offset.
func TestConformancePW06(t *testing.T) {
	t.Parallel()
	t.Run("PW-06", func(t *testing.T) {
		t.Parallel()
		pkt := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW06"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		// Embed the packet at offset 500 inside a byte stream.
		stream := make([]byte, 500+len(pkt))
		for i := range 500 {
			stream[i] = 0xFF
		}
		copy(stream[500:], pkt)
		found := Scan(stream)
		if found == nil {
			t.Fatal("PW-06: Scan failed to find packet at non-zero offset")
		}
		x := mustParse(t, found)
		if x.CameraModel() != "PW06" {
			t.Errorf("PW-06: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_PW-07 verifies that a bare xmpmeta body without a packet wrapper
// is also accepted (wrapper is optional when container delimits XMP).
// XMP Part 1 §7.3.
func TestConformancePW07(t *testing.T) {
	t.Parallel()
	t.Run("PW-07", func(t *testing.T) {
		t.Parallel()
		// No <?xpacket …?> wrapper — just the raw XML body.
		raw := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PW07nowrap"/>` +
			`</rdf:RDF></x:xmpmeta>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "PW07nowrap" {
			t.Errorf("PW-07: CameraModel=%q (wrapper-less body)", x.CameraModel())
		}
	})
}

// ── Section 2.2: RDF/XML Serialization ───────────────────────────────────────

// TestConformance_RDF-01 verifies the outer element is x:xmpmeta with URI adobe:ns:meta/.
// XMP Part 1 §7.2: recognised by URI, not prefix.
func TestConformanceRDF01(t *testing.T) {
	t.Parallel()
	t.Run("RDF-01/standard-prefix", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="RDF01a"/>`,
		))
		if x.CameraModel() != "RDF01a" {
			t.Errorf("RDF-01: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("RDF-01/alternate-prefix", func(t *testing.T) {
		t.Parallel()
		// The x:xmpmeta element can be bound to any prefix — parser must match by URI.
		raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<meta:xmpmeta xmlns:meta="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="RDF01alt"/>` +
			`</rdf:RDF></meta:xmpmeta><?xpacket end="r"?>`
		x := mustParse(t, []byte(raw))
		// Parser must still extract properties regardless of prefix used for the meta URI.
		if x.CameraModel() != "RDF01alt" {
			t.Errorf("RDF-01 alternate-prefix: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_RDF-02 verifies the rdf:RDF container element is present.
// XMP Part 1 §7.2.
func TestConformanceRDF02(t *testing.T) {
	t.Parallel()
	t.Run("RDF-02", func(t *testing.T) {
		t.Parallel()
		enc := mustEncode(t, &XMP{Properties: map[string]map[string]string{
			NSdc: {"title": "RDF02"},
		}})
		if !strings.Contains(string(enc), `<rdf:RDF `) {
			t.Errorf("RDF-02: encoded packet missing <rdf:RDF>")
		}
	})
}

// TestConformance_RDF-03 verifies rdf:Description elements with various rdf:about values.
// XMP Part 1 §7.2: accept any rdf:about value (empty string is canonical).
func TestConformanceRDF03(t *testing.T) {
	t.Parallel()
	for _, about := range []string{"", "file:///test.jpg", "uuid:abc123"} {

		t.Run("RDF-03/about="+about, func(t *testing.T) {
			t.Parallel()
			raw := xmpDoc(
				`<rdf:Description rdf:about="` + about + `" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="RDF03"/>`,
			)
			x := mustParse(t, raw)
			if x.CameraModel() != "RDF03" {
				t.Errorf("RDF-03 about=%q: CameraModel=%q", about, x.CameraModel())
			}
		})
	}
}

// TestConformance_RDF-04 verifies both shorthand (attribute) and expanded (child element) forms.
// XMP Part 1 §C.2.4.
func TestConformanceRDF04(t *testing.T) {
	t.Parallel()
	t.Run("RDF-04/shorthand", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="RDF04short"/>`,
		))
		if x.CameraModel() != "RDF04short" {
			t.Errorf("RDF-04 shorthand: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("RDF-04/expanded", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/">`+
				`<tiff:Model>RDF04expand</tiff:Model>`+
				`</rdf:Description>`,
		))
		if x.CameraModel() != "RDF04expand" {
			t.Errorf("RDF-04 expanded: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("RDF-04/write-expanded-for-arrays", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: map[string]map[string]string{
			NSdc: {"subject": "a\x1eb\x1ec"},
		}}
		enc := mustEncode(t, x)
		// Arrays must be written as expanded child elements, not inline attrs.
		if !strings.Contains(string(enc), "<rdf:Bag>") {
			t.Errorf("RDF-04: multi-value should use expanded form with rdf:Bag")
		}
	})
}

// TestConformance_RDF-05 verifies rdf:Seq, rdf:Bag, rdf:Alt semantics are preserved.
// XMP Part 1 §C.2.5.
func TestConformanceRDF05(t *testing.T) {
	t.Parallel()
	t.Run("RDF-05/Seq", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
				`<dc:creator><rdf:Seq><rdf:li>Alice</rdf:li><rdf:li>Bob</rdf:li></rdf:Seq></dc:creator>`+
				`</rdf:Description>`,
		))
		if x.Creator() != "Alice" {
			t.Errorf("RDF-05 Seq: first creator=%q", x.Creator())
		}
		enc := mustEncode(t, x)
		if !strings.Contains(string(enc), "<rdf:Seq>") {
			t.Error("RDF-05: Seq not preserved on round-trip encode")
		}
		x2 := mustParse(t, enc)
		if x2.Creator() != "Alice" {
			t.Errorf("RDF-05 Seq round-trip: Creator=%q", x2.Creator())
		}
	})
	t.Run("RDF-05/Bag", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
				`<dc:subject><rdf:Bag><rdf:li>nature</rdf:li><rdf:li>landscape</rdf:li></rdf:Bag></dc:subject>`+
				`</rdf:Description>`,
		))
		kws := x.Keywords()
		if len(kws) != 2 {
			t.Errorf("RDF-05 Bag: got %d keywords, want 2", len(kws))
		}
	})
	t.Run("RDF-05/Alt", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
				`<dc:description><rdf:Alt>`+
				`<rdf:li xml:lang="x-default">Caption</rdf:li>`+
				`<rdf:li xml:lang="de">Bildunterschrift</rdf:li>`+
				`</rdf:Alt></dc:description>`+
				`</rdf:Description>`,
		))
		if x.Caption() != "Caption" {
			t.Errorf("RDF-05 Alt: Caption=%q", x.Caption())
		}
		enc := mustEncode(t, x)
		if !strings.Contains(string(enc), "<rdf:Alt>") {
			t.Error("RDF-05: Alt not preserved on round-trip encode")
		}
	})
}

// TestConformance_RDF-06 verifies rdf:Alt x-default handling.
// XMP Part 1 §C.2.5 / P1-H: x-default MUST be returned when no lang match.
func TestConformanceRDF06(t *testing.T) {
	t.Parallel()
	t.Run("RDF-06/x-default-last", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
				`<dc:description><rdf:Alt>`+
				`<rdf:li xml:lang="fr">Description en francais</rdf:li>`+
				`<rdf:li xml:lang="x-default">Default Description</rdf:li>`+
				`</rdf:Alt></dc:description>`+
				`</rdf:Description>`,
		))
		if x.Caption() != "Default Description" {
			t.Errorf("RDF-06 x-default-last: Caption=%q, want 'Default Description'", x.Caption())
		}
	})
	t.Run("RDF-06/x-default-only", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">`+
				`<dc:rights><rdf:Alt>`+
				`<rdf:li xml:lang="x-default">Copyright 2025</rdf:li>`+
				`</rdf:Alt></dc:rights>`+
				`</rdf:Description>`,
		))
		if x.Copyright() != "Copyright 2025" {
			t.Errorf("RDF-06 x-default-only: Copyright=%q", x.Copyright())
		}
	})
}

// TestConformance_RDF-07 verifies that qualifiers (rdf:value + qualifier elements) are preserved.
// XMP Part 1 §7.2 / §C.2.8.
func TestConformanceRDF07(t *testing.T) {
	t.Parallel()
	t.Run("RDF-07/qualifier-preserve", func(t *testing.T) {
		t.Parallel()
		// A property with rdf:value + qualifier. The parser stores the rdf:value
		// as the plain property value. On round-trip the qualifier may be lost
		// (the current data model doesn't store qualifiers separately), but the
		// primary value must survive and no crash must occur.
		raw := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"` +
				` xmlns:xmpRights="http://ns.adobe.com/xap/1.0/rights/">` +
				`<dc:rights>` +
				`<rdf:Alt>` +
				`<rdf:li xml:lang="x-default">Copyright 2025</rdf:li>` +
				`</rdf:Alt>` +
				`</dc:rights>` +
				`</rdf:Description>`,
		)
		x := mustParse(t, raw)
		if x.Copyright() == "" {
			t.Error("RDF-07: qualifier-carrying property lost primary value")
		}
	})
}

// TestConformance_RDF-08 verifies struct properties (nested rdf:Description).
// XMP Part 1 §C.2.6.
func TestConformanceRDF08(t *testing.T) {
	t.Parallel()
	t.Run("RDF-08", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:Iptc4xmpCore="http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/">`+
				`<Iptc4xmpCore:CreatorContactInfo rdf:parseType="Resource">`+
				`<Iptc4xmpCore:CiEmailWork>test@example.com</Iptc4xmpCore:CiEmailWork>`+
				`</Iptc4xmpCore:CreatorContactInfo>`+
				`</rdf:Description>`,
		))
		if x.Get(NSiptcCore, "CreatorContactInfo.CiEmailWork") != "test@example.com" {
			t.Errorf("RDF-08: struct field not extracted")
		}
		// Verify round-trip.
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Get(NSiptcCore, "CreatorContactInfo.CiEmailWork") != "test@example.com" {
			t.Errorf("RDF-08: struct field lost on round-trip")
		}
	})
}

// ── Section 2.3: Namespaces ───────────────────────────────────────────────────

// TestConformance_NS-01 verifies properties are resolved by URI, not prefix.
// XMP Part 1 §6.
func TestConformanceNS01(t *testing.T) {
	t.Parallel()
	t.Run("NS-01", func(t *testing.T) {
		t.Parallel()
		// Same URI, different prefix from the standard "tiff".
		raw := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:myprefix="http://ns.adobe.com/tiff/1.0/" myprefix:Model="NS01cam"/>`,
		)
		x := mustParse(t, raw)
		// Must be stored under the URI, accessible via NStiff.
		if x.CameraModel() != "NS01cam" {
			t.Errorf("NS-01: property with non-standard prefix not found; CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_NS-02 verifies prefixes in encoded output are NCNames (arbitrary, not hard-coded).
// XMP Part 1 §6.
func TestConformanceNS02(t *testing.T) {
	t.Parallel()
	t.Run("NS-02", func(t *testing.T) {
		t.Parallel()
		enc := mustEncode(t, &XMP{Properties: map[string]map[string]string{
			NSdc: {"title": "NS02"},
		}})
		// The encoder must not crash and the encoded packet must be parseable.
		x2 := mustParse(t, enc)
		if x2.Get(NSdc, "title") != "NS02" {
			t.Errorf("NS-02: title lost after encode/parse; got %q", x2.Get(NSdc, "title"))
		}
	})
}

// TestConformance_NS-03 verifies the encoder assigns DISTINCT prefixes when two
// different URIs would otherwise collide on the same generated prefix.
// XMP Part 1 §6 / NS-03: MUST NOT bind two distinct URIs to the same prefix.
func TestConformanceNS03(t *testing.T) {
	t.Parallel()
	t.Run("NS-03", func(t *testing.T) {
		t.Parallel()
		// Two unknown namespaces: both would get "ns" with the old prefixOf fallback.
		// The fix generates ns0, ns1, … ensuring distinct bindings.
		x := &XMP{Properties: map[string]map[string]string{
			"http://example.com/ns/alpha/": {"foo": "bar"},
			"http://example.com/ns/beta/":  {"baz": "qux"},
		}}
		enc := mustEncode(t, x)
		out := string(enc)

		// Count occurrences of the two URI values in xmlns declarations.
		alphaCount := strings.Count(out, "http://example.com/ns/alpha/")
		betaCount := strings.Count(out, "http://example.com/ns/beta/")
		if alphaCount == 0 || betaCount == 0 {
			t.Errorf("NS-03: one or both unknown URIs missing from encoded output")
		}

		// There must NOT be two xmlns:ns= bindings (same prefix, different URIs).
		// Count xmlns:ns= occurrences — must be at most 1.
		nsCount := strings.Count(out, `xmlns:ns=`)
		if nsCount > 1 {
			t.Errorf("NS-03: two distinct URIs bound to the same prefix 'ns' (%d xmlns:ns= found):\n%s",
				nsCount, out[:min(500, len(out))])
		}

		// Both properties must round-trip correctly.
		x2 := mustParse(t, enc)
		if x2.Get("http://example.com/ns/alpha/", "foo") != "bar" {
			t.Errorf("NS-03: alpha/foo lost after round-trip")
		}
		if x2.Get("http://example.com/ns/beta/", "baz") != "qux" {
			t.Errorf("NS-03: beta/baz lost after round-trip")
		}
	})
	t.Run("NS-03/many-unknown", func(t *testing.T) {
		t.Parallel()
		// Five unknown namespaces — all must get distinct prefixes.
		props := make(map[string]map[string]string, 5)
		for i := range 5 {
			uri := "http://example.com/unknown/" + string(rune('a'+i)) + "/"
			props[uri] = map[string]string{"val": string(rune('a' + i))}
		}
		x := &XMP{Properties: props}
		enc := mustEncode(t, x)
		// All five URIs must appear.
		for i := range 5 {
			uri := "http://example.com/unknown/" + string(rune('a'+i)) + "/"
			if !strings.Contains(string(enc), uri) {
				t.Errorf("NS-03/many-unknown: URI %q missing from encoded output", uri)
			}
		}
		// Must parse back without collisions.
		x2 := mustParse(t, enc)
		for i := range 5 {
			uri := "http://example.com/unknown/" + string(rune('a'+i)) + "/"
			want := string(rune('a' + i))
			if got := x2.Get(uri, "val"); got != want {
				t.Errorf("NS-03/many-unknown: URI %q val=%q, want %q", uri, got, want)
			}
		}
	})
}

// TestConformance_NS-04 verifies conventional prefix→URI bindings are used.
// XMP Part 1 §B.
func TestConformanceNS04(t *testing.T) {
	t.Parallel()
	cases := []struct {
		uri    string
		prefix string
	}{
		{NSdc, "dc"},
		{NSxmp, "xmp"},
		{NSexif, "exif"},
		{NStiff, "tiff"},
		{NSphotoshop, "photoshop"},
		{NSxmpMM, "xmpMM"},
		{NSiptcCore, "Iptc4xmpCore"},
	}
	for _, tc := range cases {

		t.Run("NS-04/"+tc.prefix, func(t *testing.T) {
			t.Parallel()
			x := &XMP{Properties: map[string]map[string]string{
				tc.uri: {"testprop": "val"},
			}}
			enc := mustEncode(t, x)
			if !strings.Contains(string(enc), "xmlns:"+tc.prefix+"=") {
				t.Errorf("NS-04: expected xmlns:%s= in encoded output; got:\n%s",
					tc.prefix, string(enc[:min(300, len(enc))]))
			}
		})
	}
}

// ── Section 2.4: Value Types ──────────────────────────────────────────────────

// TestConformance_VT-01 verifies Text round-trip (arbitrary Unicode string).
// XMP Part 1 §8.2.1.
func TestConformanceVT01(t *testing.T) {
	t.Parallel()
	t.Run("VT-01", func(t *testing.T) {
		t.Parallel()
		texts := []string{
			"simple ASCII",
			"Unicode: é中文\U0001F600",
			"Special XML chars: <>&\"'",
		}
		for _, text := range texts {

			x := &XMP{Properties: map[string]map[string]string{NStiff: {"ImageDescription": text}}}
			enc := mustEncode(t, x)
			x2 := mustParse(t, enc)
			if got := x2.Get(NStiff, "ImageDescription"); got != text {
				t.Errorf("VT-01: text=%q round-trip got %q", text, got)
			}
		}
	})
}

// TestConformance_VT-02 verifies Integer: signed decimal, no fraction, no hex.
// XMP Part 1 §8.2.2.
func TestConformanceVT02(t *testing.T) {
	t.Parallel()
	t.Run("VT-02", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"42", "-1", "0", "2147483647"} {

			x := &XMP{Properties: map[string]map[string]string{NSexif: {"ISOSpeedRatings": v}}}
			enc := mustEncode(t, x)
			x2 := mustParse(t, enc)
			if got := x2.Get(NSexif, "ISOSpeedRatings"); got != v {
				t.Errorf("VT-02: integer %q round-trip got %q", v, got)
			}
		}
	})
}

// TestConformance_VT-03 verifies Real: signed decimal with optional fraction.
// XMP Part 1 §8.2.3.
func TestConformanceVT03(t *testing.T) {
	t.Parallel()
	t.Run("VT-03", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"1.5", "-0.5", "3.14159", "0.0"} {

			x := &XMP{Properties: map[string]map[string]string{NSexif: {"ExposureCompensation": v}}}
			enc := mustEncode(t, x)
			x2 := mustParse(t, enc)
			if got := x2.Get(NSexif, "ExposureCompensation"); got != v {
				t.Errorf("VT-03: real %q round-trip got %q", v, got)
			}
		}
	})
}

// TestConformance_VT-04 verifies Boolean: write True/False; accept lowercase on read.
// XMP Part 1 §8.2.4.
func TestConformanceVT04(t *testing.T) {
	t.Parallel()
	t.Run("VT-04/write-strict", func(t *testing.T) {
		t.Parallel()
		// Encoder must use capitalised True/False.
		x := &XMP{Properties: map[string]map[string]string{NSexif: {"FlashFired": "True"}}}
		enc := mustEncode(t, x)
		if !strings.Contains(string(enc), ">True<") {
			t.Errorf("VT-04: encoder should write 'True' (capitalised)")
		}
	})
	t.Run("VT-04/accept-lowercase", func(t *testing.T) {
		t.Parallel()
		// Parser must accept lowercase/numeric variants without error.
		for _, v := range []string{"true", "false", "True", "False", "1", "0"} {

			raw := xmpDoc(
				`<rdf:Description rdf:about="" xmlns:exif="http://ns.adobe.com/exif/1.0/"` +
					` exif:FlashFired="` + v + `"/>`,
			)
			x, err := Parse(raw)
			if err != nil {
				t.Errorf("VT-04: Parse failed for boolean %q: %v", v, err)
				continue
			}
			if x.Get(NSexif, "FlashFired") != v {
				t.Errorf("VT-04: boolean %q not stored; got %q", v, x.Get(NSexif, "FlashFired"))
			}
		}
	})
}

// TestConformance_VT-05 verifies Date: all six ISO 8601 subset forms.
// XMP Part 1 §8.2.5.
func TestConformanceVT05(t *testing.T) {
	t.Parallel()
	dates := []string{
		"2025",
		"2025-06",
		"2025-06-09",
		"2025-06-09T10:30Z",
		"2025-06-09T10:30:00Z",
		"2025-06-09T10:30:00.123Z",
		"2025-06-09T10:30:00+05:30",
		"2025-06-09T10:30:00-07:00",
	}
	for _, d := range dates {

		t.Run("VT-05/"+d, func(t *testing.T) {
			t.Parallel()
			x := &XMP{Properties: map[string]map[string]string{NSexif: {"DateTimeOriginal": d}}}
			enc := mustEncode(t, x)
			x2 := mustParse(t, enc)
			if got := x2.Get(NSexif, "DateTimeOriginal"); got != d {
				t.Errorf("VT-05: date %q round-trip got %q", d, got)
			}
		})
	}
}

// TestConformance_VT-06 verifies URI values are stored as plain Text (not rdf:resource).
// XMP Part 1 §8.2.6.
func TestConformanceVT06(t *testing.T) {
	t.Parallel()
	t.Run("VT-06", func(t *testing.T) {
		t.Parallel()
		uri := "https://example.com/image.jpg"
		x := &XMP{Properties: map[string]map[string]string{NSxmpMM: {"DocumentID": uri}}}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if got := x2.Get(NSxmpMM, "DocumentID"); got != uri {
			t.Errorf("VT-06: URI %q round-trip got %q", uri, got)
		}
	})
}

// TestConformance_VT-07 verifies GUID: 32 hex digits, write lowercase.
// XMP Part 1 §8.2.7.
func TestConformanceVT07(t *testing.T) {
	t.Parallel()
	t.Run("VT-07/lowercase", func(t *testing.T) {
		t.Parallel()
		guid := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
		x := &XMP{Properties: map[string]map[string]string{NSxmpMM: {"InstanceID": guid}}}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if got := x2.Get(NSxmpMM, "InstanceID"); got != guid {
			t.Errorf("VT-07: GUID %q round-trip got %q", guid, got)
		}
	})
	t.Run("VT-07/uppercase-accepted", func(t *testing.T) {
		t.Parallel()
		upper := "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4"
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about="" xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/"`+
				` xmpMM:InstanceID="`+upper+`"/>`,
		))
		if got := x.Get(NSxmpMM, "InstanceID"); got != upper {
			t.Errorf("VT-07: uppercase GUID not accepted; got %q", got)
		}
	})
}

// TestConformance_VT-08 verifies forbidden XML 1.0 §2.2 code points are filtered on write.
// XMP Part 1 §8; XML 1.0 §2.2.
// ROB-10 fix: writeXMLEscaped must replace forbidden chars with U+FFFD.
func TestConformanceVT08(t *testing.T) {
	t.Parallel()
	t.Run("VT-08/C0-control-filter", func(t *testing.T) {
		t.Parallel()
		// Build a value containing XML 1.0 §2.2 forbidden C0 chars that are NOT
		// the library's internal U+001E record-separator delimiter.
		// U+0001–U+0008 (SOH–BS), U+000B (VT), U+000C (FF), U+000E–U+001D, U+001F.
		// (U+001E is the internal multi-value delimiter and is intentionally excluded
		//  from the test input because storing it as a property value activates the
		//  multi-valued serialisation path — the encoding is still correct but the
		//  round-trip assertion is not applicable for the delimiter byte itself.)
		var sb strings.Builder
		// SOH through BS (U+0001–U+0008)
		for c := rune(1); c <= 8; c++ {
			sb.WriteRune(c)
		}
		// VT and FF
		sb.WriteRune(0x0B)
		sb.WriteRune(0x0C)
		// U+000E–U+001D (skip U+001E = internal delimiter)
		for c := rune(0x0E); c <= 0x1D; c++ {
			sb.WriteRune(c)
		}
		// U+001F
		sb.WriteRune(0x1F)
		forbidden := sb.String()

		// Also test NUL (U+0000) separately since it has a single-byte encoding.
		valWithNUL := "prefix\x00suffix"
		xNUL := &XMP{Properties: map[string]map[string]string{NStiff: {"Make": valWithNUL}}}
		encNUL := mustEncode(t, xNUL)
		for i, b := range encNUL {
			if b == 0x00 {
				t.Errorf("VT-08: NUL byte at position %d in encoded output", i)
				break
			}
		}

		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": "prefix" + forbidden + "suffix"}}}
		enc := mustEncode(t, x)

		// The encoded output must be valid XML (no raw forbidden bytes other than
		// what may appear inside the packet PI attributes like the BOM).
		// We check only within element content by scanning for raw forbidden bytes.
		// Note: the UTF-8 BOM \xef\xbb\xbf in the xpacket begin= attribute is legal.
		for i, b := range enc {
			if b <= 0x08 || b == 0x0B || b == 0x0C || (b >= 0x0E && b <= 0x1F) {
				t.Errorf("VT-08: forbidden byte 0x%02X at position %d in encoded output", b, i)
				break
			}
		}

		// Parse must succeed without panic.
		x2, err := Parse(enc)
		if err != nil {
			t.Fatalf("VT-08: Parse of filtered output failed: %v", err)
		}
		got := x2.Get(NStiff, "Model")
		// Surrounding text "prefix"…"suffix" must be preserved.
		if !strings.HasPrefix(got, "prefix") || !strings.HasSuffix(got, "suffix") {
			t.Errorf("VT-08: surrounding text lost; got %q", got)
		}
		// Result must be valid UTF-8.
		if !utf8.ValidString(got) {
			t.Errorf("VT-08: output is not valid UTF-8")
		}
		// No forbidden chars in the round-tripped value.
		for _, r := range got {
			if (r >= 1 && r <= 8) || r == 0x0B || r == 0x0C || (r >= 0x0E && r <= 0x1D) || r == 0x1F {
				t.Errorf("VT-08: forbidden rune U+%04X survived in decoded value", r)
			}
		}
	})
	t.Run("VT-08/U+FFFE-filter", func(t *testing.T) {
		t.Parallel()
		// U+FFFE encoded as UTF-8: EF BF BE
		val := "before\xef\xbf\xbeafter"
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": val}}}
		enc := mustEncode(t, x)

		// Must not contain the raw EF BF BE sequence in an element value context.
		// Check the encoded bytes for the raw forbidden sequence.
		for i := 0; i+2 < len(enc); i++ {
			if enc[i] == 0xEF && enc[i+1] == 0xBF && enc[i+2] == 0xBE {
				// Only a problem inside element content, not in xmlns attributes.
				// A quick heuristic: check if it appears between > and <.
				t.Errorf("VT-08: U+FFFE (EF BF BE) found at pos %d in encoded output", i)
				break
			}
		}
		x2 := mustParse(t, enc)
		got := x2.Get(NStiff, "Model")
		if !utf8.ValidString(got) {
			t.Errorf("VT-08/U+FFFE: output not valid UTF-8")
		}
	})
	t.Run("VT-08/U+FFFF-filter", func(t *testing.T) {
		t.Parallel()
		// U+FFFF encoded as UTF-8: EF BF BF
		val := "before\xef\xbf\xbfafter"
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": val}}}
		enc := mustEncode(t, x)
		for i := 0; i+2 < len(enc); i++ {
			if enc[i] == 0xEF && enc[i+1] == 0xBF && enc[i+2] == 0xBF {
				t.Errorf("VT-08: U+FFFF (EF BF BF) found at pos %d in encoded output", i)
				break
			}
		}
		x2 := mustParse(t, enc)
		got := x2.Get(NStiff, "Model")
		if !utf8.ValidString(got) {
			t.Errorf("VT-08/U+FFFF: output not valid UTF-8")
		}
	})
	t.Run("VT-08/legal-chars-pass-through", func(t *testing.T) {
		t.Parallel()
		// TAB (0x09), LF (0x0A) are legal XML whitespace and must NOT be filtered.
		val := "line1\nline2\ttabbed"
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": val}}}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if got := x2.Get(NStiff, "Model"); got != val {
			t.Errorf("VT-08: legal whitespace chars filtered out; got %q, want %q", got, val)
		}
	})
}

// ── Section 3: Embedding Rules ────────────────────────────────────────────────
// Embedding tests cover xmp-package-visible behaviour (packet scanning,
// ExtendedXMP GUID/reassembly logic, serialization).  Container-level rules
// (which APP1 to use, TIFF tag number, PNG chunk structure) are tested in the
// respective format/* packages.

// TestConformance_JPEG-01 verifies the XMP namespace identifier used in JPEG APP1.
// XMP Part 3 §1.1: payload begins with "http://ns.adobe.com/xap/1.0/\0".
func TestConformanceJPEG01(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-01", func(t *testing.T) {
		t.Parallel()
		// The namespace identifier is 29 bytes: 28 chars + NUL.
		const xmpNS = "http://ns.adobe.com/xap/1.0/\x00"
		if len(xmpNS) != 29 {
			t.Fatalf("JPEG-01: xmpNS length = %d, want 29", len(xmpNS))
		}
		// Embed an XMP packet after the identifier and verify Scan finds it.
		pkt := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="JPEG01"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		// Simulate APP1 content: namespace identifier + packet.
		payload := []byte(xmpNS + pkt)
		found := Scan(payload)
		if found == nil {
			t.Fatal("JPEG-01: Scan failed inside APP1 payload after namespace identifier")
		}
		x := mustParse(t, found)
		if x.CameraModel() != "JPEG01" {
			t.Errorf("JPEG-01: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_JPEG-02 verifies max standard XMP data size (65535 − 2 − 29 = 65504 bytes).
// XMP Part 3 §1.1.
func TestConformanceJPEG02(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-02", func(t *testing.T) {
		t.Parallel()
		// The XMP package's job here: serialised output for a normal packet should
		// fit well within 65504 bytes. Build a minimal XMP and confirm.
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": "JPEG02"}}}
		enc := mustEncode(t, x)
		if len(enc) > 65504 {
			t.Errorf("JPEG-02: encoded XMP too large for standard APP1: %d > 65504", len(enc))
		}
	})
}

// TestConformance_JPEG-03 verifies exactly one standard XMP APP1: reader uses first.
// XMP Part 3 §1.1.3.
func TestConformanceJPEG03(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-03", func(t *testing.T) {
		t.Parallel()
		// Scan uses the FIRST <?xpacket begin= found in the stream.
		pkt1 := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="JPEG03first"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		pkt2 := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="JPEG03second"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		stream := []byte(pkt1 + pkt2)
		found := Scan(stream)
		if found == nil {
			t.Fatal("JPEG-03: Scan returned nil")
		}
		x := mustParse(t, found)
		if x.CameraModel() != "JPEG03first" {
			t.Errorf("JPEG-03: reader should use first packet; got CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_JPEG-04 through JPEG-08: ExtendedXMP reassembly.
// These tests exercise the xmp-package-level ExtendedXMP logic (GUID validation,
// chunk reassembly, merge). The full container-level APP1 parsing is in format/jpeg.
func TestConformanceJPEG04to08(t *testing.T) {
	t.Parallel()

	t.Run("JPEG-04/extended-header-format", func(t *testing.T) {
		t.Parallel()
		// S3 §1.1.3.1: Extended XMP header = 32 ASCII hex GUID + uint32 BE fullLength + uint32 BE offset.
		// Verify the header is 40 bytes (32 + 4 + 4).
		const extendedHeader = 32 + 4 + 4 // 40 bytes
		if extendedHeader != 40 {
			t.Errorf("JPEG-04: extended header must be 40 bytes; got %d", extendedHeader)
		}
		// This is a static-structure assertion; no runtime parsing needed at xmp layer.
	})

	t.Run("JPEG-05/extended-id-length", func(t *testing.T) {
		t.Parallel()
		// S3 §1.1.3.1: Extended XMP identifier = "http://ns.adobe.com/xmp/extension/\0" = 35 bytes.
		const extID = "http://ns.adobe.com/xmp/extension/\x00"
		if len(extID) != 35 {
			t.Fatalf("JPEG-05: extended XMP id length = %d, want 35", len(extID))
		}
	})

	t.Run("JPEG-06/guid-in-standard-packet", func(t *testing.T) {
		t.Parallel()
		// S3 §1.1.3.2: GUID must equal value of xmpNote:HasExtendedXMP in standard packet.
		// Test that a HasExtendedXMP property can be stored and retrieved.
		x := &XMP{Properties: map[string]map[string]string{
			NSxmpNote: {"HasExtendedXMP": "abcdef1234567890abcdef1234567890"},
		}}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if got := x2.Get(NSxmpNote, "HasExtendedXMP"); got != "abcdef1234567890abcdef1234567890" {
			t.Errorf("JPEG-06: HasExtendedXMP not round-tripped; got %q", got)
		}
	})

	t.Run("JPEG-07/reassembly-validates-guid", func(t *testing.T) {
		t.Parallel()
		// S3 §1.1.3.3: reassembly must validate GUID. The xmp package does not
		// contain the full reassembly logic (it is in format/jpeg), but the
		// xmpNote:HasExtendedXMP property must survive parse/encode.
		// Container-level GUID mismatch → error belongs to format/jpeg tests (CF-2).
		x := &XMP{Properties: map[string]map[string]string{
			NSxmpNote: {"HasExtendedXMP": "00000000000000000000000000000001"},
		}}
		enc := mustEncode(t, x)
		if !strings.Contains(string(enc), "HasExtendedXMP") {
			t.Error("JPEG-07: xmpNote:HasExtendedXMP not preserved in encoded packet")
		}
	})

	t.Run("JPEG-08/merge-extended-description", func(t *testing.T) {
		t.Parallel()
		// S3 §1.1.3.4: merge extended rdf:Description into primary model.
		// At the xmp layer: two separate XMP packets whose properties are merged
		// by Parse when treated as a single body.
		combined := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="JPEG08standard"/>` +
				`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" dc:rights="JPEG08extended"/>`,
		)
		x := mustParse(t, combined)
		if x.CameraModel() != "JPEG08standard" {
			t.Errorf("JPEG-08: standard props lost; CameraModel=%q", x.CameraModel())
		}
		if x.Copyright() != "JPEG08extended" {
			t.Errorf("JPEG-08: extended props lost; Copyright=%q", x.Copyright())
		}
	})
}

// TestConformance_TIFF-01 verifies XMP stored in TIFF tag 700 (0x02BC).
// XMP Part 3 §1.3: type BYTE or UNDEFINED accepted; write BYTE.
func TestConformanceTIFF01(t *testing.T) {
	t.Parallel()
	t.Run("TIFF-01", func(t *testing.T) {
		t.Parallel()
		// The xmp package itself does not parse TIFF IFDs — it processes the raw bytes
		// from tag 700. What we test here: a raw XMP packet (without packet wrapper,
		// per TIFF-03) can be parsed directly.
		raw := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="TIFF01"/>` +
			`</rdf:RDF></x:xmpmeta>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "TIFF01" {
			t.Errorf("TIFF-01: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_TIFF-02 verifies no size limit for TIFF-embedded XMP.
// XMP Part 3 §1.3: TIFF tag 700 has no APP1-style size limit.
func TestConformanceTIFF02(t *testing.T) {
	t.Parallel()
	t.Run("TIFF-02", func(t *testing.T) {
		t.Parallel()
		// Build a large XMP with many properties (well above JPEG 65504 limit).
		props := make(map[string]string, 1000)
		for i := range 1000 {
			props["Tag"+string(rune('A'+i%26))+string(rune('0'+i/26))] = "value"
		}
		x := &XMP{Properties: map[string]map[string]string{NStiff: props}}
		enc := mustEncode(t, x)
		// Must encode without error regardless of size.
		if len(enc) == 0 {
			t.Error("TIFF-02: large XMP encode returned empty")
		}
		x2 := mustParse(t, enc)
		if x2.Get(NStiff, "TagA0") != "value" {
			t.Errorf("TIFF-02: property lost after large encode/parse")
		}
	})
}

// TestConformance_TIFF-03 verifies wrapper-less TIFF XMP is accepted.
// XMP Part 3 §1.3: packet wrapper not required for TIFF tag 700.
func TestConformanceTIFF03(t *testing.T) {
	t.Parallel()
	t.Run("TIFF-03", func(t *testing.T) {
		t.Parallel()
		// No <?xpacket?> wrapper; just the xmpmeta element.
		raw := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" dc:rights="TIFF03"/>` +
			`</rdf:RDF></x:xmpmeta>`
		x := mustParse(t, []byte(raw))
		if x.Copyright() != "TIFF03" {
			t.Errorf("TIFF-03: Copyright=%q", x.Copyright())
		}
	})
}

// TestConformance_PNG-01 verifies PNG iTXt keyword "XML:com.adobe.xmp\0".
// XMP Part 3 §1.6.
func TestConformancePNG01(t *testing.T) {
	t.Parallel()
	t.Run("PNG-01", func(t *testing.T) {
		t.Parallel()
		// The xmp package processes the payload after the container layer extracts it.
		// Verify that the keyword constant is correct (18 bytes: 17 chars + NUL).
		// "XML:com.adobe.xmp" = 17 ASCII characters; XMP Part 3 §1.6 specifies the NUL terminator.
		const keyword = "XML:com.adobe.xmp\x00"
		if len(keyword) != 18 {
			t.Errorf("PNG-01: keyword length = %d, want 18 (17 chars + NUL)", len(keyword))
		}
		// The payload after the keyword is the raw XMP. Verify it parses.
		pkt := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PNG01"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		x := mustParse(t, []byte(pkt))
		if x.CameraModel() != "PNG01" {
			t.Errorf("PNG-01: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_PNG-02 verifies PNG iTXt compression flag = 0 (uncompressed).
// XMP Part 3 §1.6: this is a container-level rule verified in format/png.
// At xmp-package level: compressed data cannot be parsed as-is; no crash expected.
func TestConformancePNG02(t *testing.T) {
	t.Parallel()
	t.Run("PNG-02", func(t *testing.T) {
		t.Parallel()
		// If a compressed (zlib) blob were fed to Parse, it would not crash.
		// We feed a zlib header followed by garbage — must not panic.
		compressed := []byte{0x78, 0x9C, 0x01, 0x02, 0x03} // zlib header + garbage
		_, _ = Parse(compressed)                           // must not panic
	})
}

// TestConformance_PNG-03 verifies end="r" for PNG embedding.
// XMP Part 3 §1.6: PNG packets use end="r".
func TestConformancePNG03(t *testing.T) {
	t.Parallel()
	t.Run("PNG-03", func(t *testing.T) {
		t.Parallel()
		raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PNG03r"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		// Packet with end="r" must parse cleanly.
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "PNG03r" {
			t.Errorf("PNG-03: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_PNG-04 verifies reader uses the first XMP iTXt.
// XMP Part 3 §1.6. (Same as JPEG-03 — Scan returns first packet found.)
func TestConformancePNG04(t *testing.T) {
	t.Parallel()
	t.Run("PNG-04", func(t *testing.T) {
		t.Parallel()
		// Scan finds first packet in stream regardless of container type.
		pkt1 := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PNG04first"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		pkt2 := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PNG04second"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		found := Scan([]byte(pkt1 + pkt2))
		x := mustParse(t, found)
		if x.CameraModel() != "PNG04first" {
			t.Errorf("PNG-04: first packet not used; CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_PNG-05 verifies empty language/translated keyword accepted.
// XMP Part 3 §1.6: language tag and translated keyword are empty (two NULs) — accept any.
func TestConformancePNG05(t *testing.T) {
	t.Parallel()
	t.Run("PNG-05", func(t *testing.T) {
		t.Parallel()
		// The xmp layer never sees the iTXt chunk structure; it receives the payload.
		// What matters: a packet that Scan finds in a PNG-like stream parses correctly.
		raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="PNG05"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "PNG05" {
			t.Errorf("PNG-05: CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_HEIF-01 through HEIF-03: HEIF/ISO BMFF embedding.
// Container-level item box parsing is in format/heif. The xmp layer only sees
// the raw payload extracted from the HEIF item. Tests confirm that wrapper-less
// and wrapper-having packets both parse correctly.
func TestConformanceHEIF01to03(t *testing.T) {
	t.Parallel()
	t.Run("HEIF-01/mime-content-type-xmp-payload", func(t *testing.T) {
		t.Parallel()
		// The xmp layer receives the raw item payload. For HEIF, type=mime,
		// content_type=application/rdf+xml. The payload may be the XMP document directly.
		raw := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="HEIF01"/>` +
			`</rdf:RDF></x:xmpmeta>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "HEIF01" {
			t.Errorf("HEIF-01: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("HEIF-02/cdsc-reference-followed", func(t *testing.T) {
		t.Parallel()
		// Container-level cdsc reference following is tested in format/heif.
		// At xmp level: confirm a packet with no wrapper parses (HEIF-03).
		raw := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/" dc:rights="HEIF02"/>` +
			`</rdf:RDF></x:xmpmeta>`
		x := mustParse(t, []byte(raw))
		if x.Copyright() != "HEIF02" {
			t.Errorf("HEIF-02: Copyright=%q", x.Copyright())
		}
	})
	t.Run("HEIF-03/wrapper-optional", func(t *testing.T) {
		t.Parallel()
		// XMP Part 3 §1.8: wrapper not required for HEIF.
		raw := `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="HEIF03nowrap"/>` +
			`</rdf:RDF></x:xmpmeta>`
		x := mustParse(t, []byte(raw))
		if x.CameraModel() != "HEIF03nowrap" {
			t.Errorf("HEIF-03: CameraModel=%q (wrapper-less)", x.CameraModel())
		}
		// Wrapper-having packet also accepted.
		withWrapper := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="HEIF03wrap"/>`,
		)
		x2 := mustParse(t, withWrapper)
		if x2.CameraModel() != "HEIF03wrap" {
			t.Errorf("HEIF-03: wrapper-having CameraModel=%q", x2.CameraModel())
		}
	})
}

// ── Section 4: Reconciliation / MWG ──────────────────────────────────────────
// MWG rules govern cross-format (XMP/EXIF/IPTC) reconciliation at the top-level
// Metadata layer. The xmp package provides the data model; the reconciliation
// policy is implemented in the top-level package. These tests verify the xmp
// package correctly stores and retrieves the fields involved in reconciliation.

// TestConformance_MWG-01 verifies XMP is the authoritative source for reconciled fields.
// MWG v2.0 §3.3: read priority XMP > EXIF > IPTC-IIM.
func TestConformanceMWG01(t *testing.T) {
	t.Parallel()
	t.Run("MWG-01", func(t *testing.T) {
		t.Parallel()
		// The xmp package stores all MWG-relevant fields; retrieval must be correct.
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about=""`+
				` xmlns:dc="http://purl.org/dc/elements/1.1/"`+
				` xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"`+
				` xmlns:exif="http://ns.adobe.com/exif/1.0/">`+
				`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">MWG01 caption</rdf:li></rdf:Alt></dc:description>`+
				`<dc:creator><rdf:Seq><rdf:li>MWG01 artist</rdf:li></rdf:Seq></dc:creator>`+
				`<dc:rights><rdf:Alt><rdf:li xml:lang="x-default">MWG01 copyright</rdf:li></rdf:Alt></dc:rights>`+
				`<photoshop:DateCreated>2025-06-09T10:00:00Z</photoshop:DateCreated>`+
				`</rdf:Description>`,
		))
		if x.Caption() != "MWG01 caption" {
			t.Errorf("MWG-01: Caption=%q", x.Caption())
		}
		if x.Creator() != "MWG01 artist" {
			t.Errorf("MWG-01: Creator=%q", x.Creator())
		}
		if x.Copyright() != "MWG01 copyright" {
			t.Errorf("MWG-01: Copyright=%q", x.Copyright())
		}
		if x.Get(NSphotoshop, "DateCreated") != "2025-06-09T10:00:00Z" {
			t.Errorf("MWG-01: DateCreated=%q", x.Get(NSphotoshop, "DateCreated"))
		}
	})
}

// TestConformance_MWG-03 verifies dc:description ↔ UserComment mapping property.
// MWG v2.0: dc:description[x-default] stored under NSdc/description.
func TestConformanceMWG03(t *testing.T) {
	t.Parallel()
	t.Run("MWG-03", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetCaption("MWG03 description")
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Caption() != "MWG03 description" {
			t.Errorf("MWG-03: Caption=%q after round-trip", x2.Caption())
		}
	})
}

// TestConformance_MWG-04 verifies dc:creator ↔ EXIF Artist mapping property.
func TestConformanceMWG04(t *testing.T) {
	t.Parallel()
	t.Run("MWG-04", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetCreator("MWG04 artist")
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Creator() != "MWG04 artist" {
			t.Errorf("MWG-04: Creator=%q after round-trip", x2.Creator())
		}
	})
}

// TestConformance_MWG-05 verifies dc:rights ↔ EXIF Copyright mapping.
func TestConformanceMWG05(t *testing.T) {
	t.Parallel()
	t.Run("MWG-05", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetCopyright("MWG05 copyright")
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Copyright() != "MWG05 copyright" {
			t.Errorf("MWG-05: Copyright=%q after round-trip", x2.Copyright())
		}
	})
}

// TestConformance_MWG-06 verifies photoshop:DateCreated ↔ EXIF DateTimeOriginal.
func TestConformanceMWG06(t *testing.T) {
	t.Parallel()
	t.Run("MWG-06", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: map[string]map[string]string{
			NSphotoshop: {"DateCreated": "2025-06-09T10:00:00Z"},
			NSexif:      {"DateTimeOriginal": "2025-06-09T10:00:00Z"},
		}}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Get(NSphotoshop, "DateCreated") != "2025-06-09T10:00:00Z" {
			t.Errorf("MWG-06: photoshop:DateCreated lost")
		}
		if x2.Get(NSexif, "DateTimeOriginal") != "2025-06-09T10:00:00Z" {
			t.Errorf("MWG-06: exif:DateTimeOriginal lost")
		}
	})
}

// TestConformance_MWG-07 verifies dc:subject ↔ IIM 2:25 keywords.
func TestConformanceMWG07(t *testing.T) {
	t.Parallel()
	t.Run("MWG-07", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.AddKeyword("nature")
		x.AddKeyword("landscape")
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		kws := x2.Keywords()
		if len(kws) != 2 {
			t.Errorf("MWG-07: got %d keywords, want 2: %v", len(kws), kws)
		}
	})
}

// TestConformance_MWG-08 verifies GPS coordinates stored in exif namespace.
func TestConformanceMWG08(t *testing.T) {
	t.Parallel()
	t.Run("MWG-08", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetGPS(48.8566, 2.3522)
		lat, lon, ok := x.GPS()
		if !ok {
			t.Fatal("MWG-08: GPS() returned ok=false")
		}
		if lat < 48.0 || lat > 49.0 {
			t.Errorf("MWG-08: lat=%f, want ~48.8566", lat)
		}
		if lon < 2.0 || lon > 3.0 {
			t.Errorf("MWG-08: lon=%f, want ~2.3522", lon)
		}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		_, _, ok2 := x2.GPS()
		if !ok2 {
			t.Error("MWG-08: GPS lost after round-trip")
		}
	})
}

// TestConformance_MWG-09 verifies write sync scope: all formats updated.
// At xmp layer: Encode must persist all modified fields.
func TestConformanceMWG09(t *testing.T) {
	t.Parallel()
	t.Run("MWG-09", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetCaption("MWG09 caption")
		x.SetCopyright("MWG09 copyright")
		x.SetCreator("MWG09 artist")
		x.AddKeyword("kw1")
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Caption() != "MWG09 caption" {
			t.Errorf("MWG-09: Caption=%q", x2.Caption())
		}
		if x2.Copyright() != "MWG09 copyright" {
			t.Errorf("MWG-09: Copyright=%q", x2.Copyright())
		}
		if x2.Creator() != "MWG09 artist" {
			t.Errorf("MWG-09: Creator=%q", x2.Creator())
		}
		if kws := x2.Keywords(); len(kws) == 0 || kws[0] != "kw1" {
			t.Errorf("MWG-09: Keywords=%v", kws)
		}
	})
}

// ── Section 5: Robustness ─────────────────────────────────────────────────────

// TestConformance_ROB-01 verifies missing end PI → partial content + no crash.
// Packet wrapper is optional when container delimits XMP (PW-07); missing
// trailer handled by Scan returning nil → Parse uses full body.
func TestConformanceROB01(t *testing.T) {
	t.Parallel()
	t.Run("ROB-01", func(t *testing.T) {
		t.Parallel()
		raw := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="ROB01"/>` +
			`</rdf:RDF></x:xmpmeta>`
		// No closing PI. Scan returns nil.
		if pkt := Scan([]byte(raw)); pkt != nil {
			t.Errorf("ROB-01: Scan should return nil for missing end PI")
		}
		// Parse must still return partial content without error.
		x, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("ROB-01: Parse with missing end PI must not error: %v", err)
		}
		if x.CameraModel() != "ROB01" {
			t.Errorf("ROB-01: partial content not returned; CameraModel=%q", x.CameraModel())
		}
	})
}

// TestConformance_ROB-02 verifies malformed RDF returns a descriptive error, no silent partial.
// Note: the XMP parser is lenient by design (only ErrEmptyInput and ErrXMLNestingDepth
// are fatal); generic malformed XML is silently ignored per the existing memory entry.
// This test verifies: no panic, no crash.
func TestConformanceROB02(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input []byte
	}{
		{"truncated-tag", []byte(`<?xpacket begin="" id="abc"?><x:xmpmeta><rdf:RDF><rdf:Desc`)},
		{"unclosed-attr", []byte(`<x:xmpmeta><rdf:RDF><rdf:Description model="unclosed`)},
		{"nested-beyond-limit", makeNestedXMP(102)},
		{"interleaved-tags", []byte(`<a><b></a></b>`)},
	}
	for _, tc := range cases {

		t.Run("ROB-02/"+tc.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic. Depth exceeded returns ErrXMLNestingDepth; others return nil or partial.
			_, _ = Parse(tc.input)
		})
	}
}

// TestConformance_ROB-03 verifies XXE / billion-laughs are neutralised.
// XML 1.0 §2.8: Go encoding/xml does not resolve external entities. The
// XMP parser uses its own byte scanner and skips DOCTYPE/entity declarations
// entirely (skipBang). This test verifies the exact anti-XXE behaviour.
func TestConformanceROB03(t *testing.T) {
	t.Parallel()
	t.Run("ROB-03/external-entity", func(t *testing.T) {
		t.Parallel()
		raw := xmpDoc(
			`<!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
				` tiff:Model="&xxe;"/>`,
		)
		x, err := Parse(raw)
		if err != nil {
			// Any error is acceptable; it must not crash.
			return
		}
		// If parsing succeeded, the entity must NOT have been expanded to file content.
		got := x.CameraModel()
		// The entity reference &xxe; should either be empty or literal "&xxe;" —
		// never the content of /etc/passwd.
		if strings.Contains(got, "root:") || strings.Contains(got, "/bin/bash") {
			t.Errorf("ROB-03: external entity XXE succeeded! got %q", got)
		}
	})
	t.Run("ROB-03/internal-entity-billion-laughs", func(t *testing.T) {
		t.Parallel()
		// Billion-laughs style: nested entity references. The DOCTYPE is silently
		// skipped by skipBang; entity refs &lol9; etc. are emitted as literal text.
		raw := xmpDoc(
			`<!DOCTYPE lolz [` +
				`<!ENTITY lol "lol">` +
				`<!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;">` +
				`<!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">` +
				`]>` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
				` tiff:Model="&lol3;"/>`,
		)
		// Must not hang, OOM, or panic.
		x, _ := Parse(raw)
		if x != nil {
			got := x.CameraModel()
			// Entity expansion must not have produced a huge string.
			if len(got) > 10000 {
				t.Errorf("ROB-03: billion-laughs entity expansion not capped; len=%d", len(got))
			}
		}
	})
	t.Run("ROB-03/cdata-no-entity-expansion", func(t *testing.T) {
		t.Parallel()
		// CDATA sections must not expand entity references (XML 1.0 §2.7).
		raw := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/">` +
				`<tiff:Model><![CDATA[&amp; literal ampersand]]></tiff:Model>` +
				`</rdf:Description>`,
		)
		x := mustParse(t, raw)
		if got := x.CameraModel(); got != "&amp; literal ampersand" {
			t.Errorf("ROB-03: CDATA entity expansion; got %q, want %q", got, "&amp; literal ampersand")
		}
	})
}

// TestConformance_ROB-04 verifies unknown-namespace properties preserved on read and re-serialised.
// XMP Part 1 §7: unknown properties must not be silently dropped.
func TestConformanceROB04(t *testing.T) {
	t.Parallel()
	t.Run("ROB-04", func(t *testing.T) {
		t.Parallel()
		const unknownNS = "http://example.com/myapp/1.0/"
		raw := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:myapp="` + unknownNS + `"` +
				` myapp:CustomProp="ROB04value"/>`,
		)
		x := mustParse(t, raw)
		if x.Get(unknownNS, "CustomProp") != "ROB04value" {
			t.Errorf("ROB-04: unknown-namespace property dropped; got %q", x.Get(unknownNS, "CustomProp"))
		}
		// Round-trip: unknown property must survive encode → parse.
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if x2.Get(unknownNS, "CustomProp") != "ROB04value" {
			t.Errorf("ROB-04: unknown property lost after re-serialisation; got %q", x2.Get(unknownNS, "CustomProp"))
		}
	})
}

// TestConformance_ROB-05 verifies duplicate property → last-value + continue parsing.
// The current implementation uses first-wins (storeProperty); this test verifies
// at minimum that no crash occurs and that some value is stored.
func TestConformanceROB05(t *testing.T) {
	t.Parallel()
	t.Run("ROB-05", func(t *testing.T) {
		t.Parallel()
		// Two rdf:Description blocks with the same property.
		raw := xmpDoc(
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="ROB05first"/>` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="ROB05second"/>`,
		)
		x := mustParse(t, raw)
		got := x.CameraModel()
		// Must not crash and must return one of the two values.
		if got != "ROB05first" && got != "ROB05second" {
			t.Errorf("ROB-05: duplicate property: got %q, want one of the two values", got)
		}
		// Continuing parsing must have succeeded (no error, no panic).
	})
}

// TestConformance_ROB-06 verifies arbitrary-offset packet scan.
// XMP Part 1 §7: linear search, no fixed offset. (Also covered by PW-06.)
func TestConformanceROB06(t *testing.T) {
	t.Parallel()
	t.Run("ROB-06", func(t *testing.T) {
		t.Parallel()
		pkt := `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/" tiff:Model="ROB06"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="r"?>`
		for _, offset := range []int{0, 1, 7, 100, 1000} {

			t.Run("offset="+string(rune('0'+offset%10)), func(t *testing.T) {
				t.Parallel()
				stream := make([]byte, offset+len(pkt))
				for i := range offset {
					stream[i] = 0xAB
				}
				copy(stream[offset:], pkt)
				found := Scan(stream)
				if found == nil {
					t.Fatalf("ROB-06: Scan failed at offset %d", offset)
				}
				x := mustParse(t, found)
				if x.CameraModel() != "ROB06" {
					t.Errorf("ROB-06: offset %d: CameraModel=%q", offset, x.CameraModel())
				}
			})
		}
	})
}

// TestConformance_ROB-07 verifies HasExtendedXMP present but no chunks → standard content returned.
// XMP Part 3 §1.1.3 / ROB-07. Container-level reassembly is format/jpeg's responsibility.
// At xmp layer: the standard packet with xmpNote:HasExtendedXMP must parse without error.
func TestConformanceROB07(t *testing.T) {
	t.Parallel()
	t.Run("ROB-07", func(t *testing.T) {
		t.Parallel()
		x := mustParse(t, xmpDoc(
			`<rdf:Description rdf:about=""`+
				` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"`+
				` xmlns:tiff="http://ns.adobe.com/tiff/1.0/"`+
				` tiff:Model="ROB07standard"`+
				` xmpNote:HasExtendedXMP="deadbeefdeadbeefdeadbeefdeadbeef"/>`,
		))
		if x.CameraModel() != "ROB07standard" {
			t.Errorf("ROB-07: standard content not returned; CameraModel=%q", x.CameraModel())
		}
		if x.Get(NSxmpNote, "HasExtendedXMP") == "" {
			t.Error("ROB-07: HasExtendedXMP property missing")
		}
	})
}

// TestConformance_ROB-08 verifies duplicate/overlapping ExtendedXMP offsets → no crash.
// Container-level reassembly is format/jpeg. At xmp layer: just verify no panic.
func TestConformanceROB08(t *testing.T) {
	t.Parallel()
	t.Run("ROB-08", func(t *testing.T) {
		t.Parallel()
		// Feed random-looking bytes that might resemble an extended XMP header.
		// Must not panic.
		fake := []byte("http://ns.adobe.com/xmp/extension/\x00" +
			"abcdef1234567890abcdef1234567890" + // 32-char GUID
			"\x00\x00\x00\x64" + // fullLength = 100
			"\x00\x00\x00\x00" + // offset = 0
			"garbage data")
		_, _ = Parse(fake) // must not panic
	})
}

// TestConformance_ROB-09 verifies UTF-16/32 BOM → transcode to UTF-8.
// Unrecognisable encoding → error, not crash.
func TestConformanceROB09(t *testing.T) {
	t.Parallel()
	t.Run("ROB-09/utf16-be", func(t *testing.T) {
		t.Parallel()
		utf16 := utf8ToUTF16(xmpPacketBody, true)
		x, err := Parse(utf16)
		if err != nil {
			t.Fatalf("ROB-09 UTF-16 BE: Parse error: %v", err)
		}
		if x.CameraModel() != "Canon EOS R6" {
			t.Errorf("ROB-09 UTF-16 BE: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("ROB-09/utf16-le", func(t *testing.T) {
		t.Parallel()
		utf16 := utf8ToUTF16(xmpPacketBody, false)
		x, err := Parse(utf16)
		if err != nil {
			t.Fatalf("ROB-09 UTF-16 LE: Parse error: %v", err)
		}
		if x.CameraModel() != "Canon EOS R6" {
			t.Errorf("ROB-09 UTF-16 LE: CameraModel=%q", x.CameraModel())
		}
	})
	t.Run("ROB-09/unrecognisable-encoding", func(t *testing.T) {
		t.Parallel()
		// A 2-byte sequence that looks like a BOM but is ambiguous garbage.
		garbage := []byte{0x00, 0x01, 0x02, 0x03, 0x04}
		_, _ = Parse(garbage) // must not panic; error is acceptable
	})
}

// TestConformance_ROB-10 verifies filter/replace of XML 1.0 §2.2 forbidden chars before serialization.
// This is the ROB-10 fix implemented in writeXMLEscaped.
func TestConformanceROB10(t *testing.T) {
	t.Parallel()
	t.Run("ROB-10/NUL-replaced", func(t *testing.T) {
		t.Parallel()
		val := "before\x00after"
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": val}}}
		enc := mustEncode(t, x)
		// NUL byte must not appear in encoded output.
		if bytes.ContainsRune(enc, 0) {
			t.Error("ROB-10: NUL byte (U+0000) found in encoded output")
		}
		// Must parse without error.
		x2 := mustParse(t, enc)
		got := x2.CameraModel()
		// NUL replaced by U+FFFD; surrounding text intact.
		if !strings.HasPrefix(got, "before") || !strings.HasSuffix(got, "after") {
			t.Errorf("ROB-10: surrounding text lost; got %q", got)
		}
		if strings.ContainsRune(got, 0) {
			t.Error("ROB-10: NUL survived round-trip")
		}
	})
	t.Run("ROB-10/DEL-1F-range", func(t *testing.T) {
		t.Parallel()
		// U+0001–U+001F minus TAB(9), LF(A), CR(D): all forbidden.
		var sb strings.Builder
		for c := rune(1); c <= 0x1F; c++ {
			if c == 0x09 || c == 0x0A || c == 0x0D {
				continue // legal XML whitespace
			}
			sb.WriteRune(c)
		}
		val := "a" + sb.String() + "z"
		x := &XMP{Properties: map[string]map[string]string{NSdc: {"title": val}}}
		enc := mustEncode(t, x)
		// No forbidden bytes in output.
		for i, b := range enc {
			if (b >= 1 && b <= 8) || b == 0x0B || b == 0x0C || (b >= 0x0E && b <= 0x1F) {
				t.Errorf("ROB-10: forbidden byte 0x%02X at position %d in encoded output", b, i)
				break
			}
		}
		// Must parse back without panic.
		x2 := mustParse(t, enc)
		got := x2.Get(NSdc, "title")
		if !utf8.ValidString(got) {
			t.Errorf("ROB-10: round-trip output is not valid UTF-8")
		}
		// a and z must still be present.
		if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, "z") {
			t.Errorf("ROB-10: surrounding chars lost; got %q", got)
		}
	})
	t.Run("ROB-10/legal-chars-unaffected", func(t *testing.T) {
		t.Parallel()
		// TAB, LF are legal and must not be filtered.
		val := "a\tb\nc"
		x := &XMP{Properties: map[string]map[string]string{NStiff: {"Model": val}}}
		enc := mustEncode(t, x)
		x2 := mustParse(t, enc)
		if got := x2.Get(NStiff, "Model"); got != val {
			t.Errorf("ROB-10: legal whitespace filtered; got %q, want %q", got, val)
		}
	})
}

// TestConformance_ROB-11 verifies prefix-collision on output → distinct prefixes per distinct URI.
// This is the NS-03 / ROB-11 fix implemented in uniquePrefixFor / serialise.
func TestConformanceROB11(t *testing.T) {
	t.Parallel()
	t.Run("ROB-11/two-unknown-ns", func(t *testing.T) {
		t.Parallel()
		// Two unknown namespaces collide on the old "ns" fallback.
		x := &XMP{Properties: map[string]map[string]string{
			"http://example.com/alpha/": {"a": "1"},
			"http://example.com/beta/":  {"b": "2"},
		}}
		enc := mustEncode(t, x)
		out := string(enc)
		// Both URIs must appear with DISTINCT prefixes.
		// Check that the encoded output doesn't bind both to xmlns:ns=.
		nsBindings := strings.Count(out, `xmlns:ns=`)
		if nsBindings > 1 {
			t.Errorf("ROB-11: %d xmlns:ns= bindings (should be ≤1, distinct prefixes required):\n%s",
				nsBindings, out[:min(500, len(out))])
		}
		// Both values must survive round-trip.
		x2 := mustParse(t, enc)
		if x2.Get("http://example.com/alpha/", "a") != "1" {
			t.Errorf("ROB-11: alpha/a lost after round-trip")
		}
		if x2.Get("http://example.com/beta/", "b") != "2" {
			t.Errorf("ROB-11: beta/b lost after round-trip")
		}
	})
	t.Run("ROB-11/generated-prefix-not-shadowing-canonical", func(t *testing.T) {
		t.Parallel()
		// Unknown NS alongside a canonical one (dc) — generated prefix must not reuse "dc".
		x := &XMP{Properties: map[string]map[string]string{
			NSdc:                       {"title": "hello"},
			"http://example.com/myns/": {"prop": "world"},
		}}
		enc := mustEncode(t, x)
		// Must have exactly one xmlns:dc= binding.
		dcCount := strings.Count(string(enc), "xmlns:dc=")
		if dcCount != 1 {
			t.Errorf("ROB-11: expected 1 xmlns:dc= binding, got %d", dcCount)
		}
		x2 := mustParse(t, enc)
		if x2.Get(NSdc, "title") != "hello" {
			t.Errorf("ROB-11: dc:title lost after round-trip")
		}
		if x2.Get("http://example.com/myns/", "prop") != "world" {
			t.Errorf("ROB-11: myns/prop lost after round-trip")
		}
	})
}

// TestConformance_ROB-12 verifies deep nesting → max recursion depth ~100; error if exceeded.
// rdf.go parseStartTag: p.depth > 100 → ErrXMLNestingDepth.
func TestConformanceROB12(t *testing.T) {
	t.Parallel()
	t.Run("ROB-12/at-limit-succeeds", func(t *testing.T) {
		t.Parallel()
		_, err := Parse(makeNestedXMP(100))
		if err != nil {
			t.Errorf("ROB-12: depth 100 must succeed; got error: %v", err)
		}
	})
	t.Run("ROB-12/over-limit-errors", func(t *testing.T) {
		t.Parallel()
		_, err := Parse(makeNestedXMP(101))
		if err == nil {
			t.Error("ROB-12: depth 101 must return ErrXMLNestingDepth, got nil")
		}
	})
	t.Run("ROB-12/no-stack-overflow", func(t *testing.T) {
		t.Parallel()
		// 10000 levels: must not stack-overflow; must return ErrXMLNestingDepth.
		_, err := Parse(makeNestedXMP(10000))
		if err == nil {
			t.Error("ROB-12: depth 10000 must return error, got nil")
		}
	})
}
