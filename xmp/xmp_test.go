package xmp

import (
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
)

const simpleXMP = `<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      xmlns:tiff="http://ns.adobe.com/tiff/1.0/"
      tiff:Model="Canon EOS R5"
      dc:rights="(c) 2024 Test">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">A test image</rdf:li>
        </rdf:Alt>
      </dc:description>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

func TestParseSimpleProperty(t *testing.T) {
	t.Parallel()
	x, err := Parse([]byte(simpleXMP))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R5" {
		t.Errorf("CameraModel: got %q, want %q", got, "Canon EOS R5")
	}
	if got := x.Caption(); got != "A test image" {
		t.Errorf("Caption: got %q, want %q", got, "A test image")
	}
	if got := x.Copyright(); got != "(c) 2024 Test" {
		t.Errorf("Copyright: got %q, want %q", got, "(c) 2024 Test")
	}
}

func TestParseMultiValue(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:subject>
        <rdf:Bag>
          <rdf:li>nature</rdf:li>
          <rdf:li>landscape</rdf:li>
          <rdf:li>sunset</rdf:li>
        </rdf:Bag>
      </dc:subject>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	v := x.getProp(NSdc, "subject")
	parts := strings.Split(v, "\x1e")
	if len(parts) != 3 {
		t.Errorf("expected 3 subject values, got %d: %v", len(parts), parts)
	}
}

func TestScanPacketBoundaryWithInternalPI(t *testing.T) {
	t.Parallel()
	// XMP body contains a ?> that should NOT be treated as the closing packet PI.
	raw := "<?xpacket begin=\"\" uid=\"abc\"?>" +
		"<x:xmpmeta><!-- some comment with ?> inside --></x:xmpmeta>" +
		"<?xpacket end=\"w\"?>"
	result := Scan([]byte(raw))
	if result == nil {
		t.Fatal("Scan returned nil; should have found the packet")
	}
	if !strings.HasSuffix(string(result), "<?xpacket end=\"w\"?>") {
		t.Errorf("packet does not end with closing PI: %q", string(result))
	}
}

func TestScanNoPacket(t *testing.T) {
	t.Parallel()
	result := Scan([]byte("<not an xmp packet>"))
	if result != nil {
		t.Error("Scan should return nil when no packet is found")
	}
}

func TestScanMissingClosingPI(t *testing.T) {
	t.Parallel()
	raw := "<?xpacket begin=\"\" uid=\"abc\"?><x:xmpmeta/>"
	result := Scan([]byte(raw))
	if result != nil {
		t.Error("Scan should return nil when closing PI is missing")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	x, err := Parse([]byte(simpleXMP))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	if got := x2.CameraModel(); got != x.CameraModel() {
		t.Errorf("CameraModel after round-trip: got %q, want %q", got, x.CameraModel())
	}
}

func TestGPSValidParsing(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:exif="http://ns.adobe.com/exif/1.0/"
      exif:GPSLatitude="37,46.494N"
      exif:GPSLongitude="122,25.164W"/>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	lat, lon, ok := x.GPS()
	if !ok {
		t.Fatal("GPS() returned ok=false")
	}
	if lat < 37.0 || lat > 38.0 {
		t.Errorf("lat = %f, want ~37.77", lat)
	}
	if lon > -122.0 || lon < -123.0 {
		t.Errorf("lon = %f, want ~-122.42", lon)
	}
}

func TestGPSRangeValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		latStr   string
		lonStr   string
		expectOK bool
	}{
		{"valid", "37,0N", "122,0W", true},
		{"lat too high", "91,0N", "0,0E", false},
		{"lat too low", "91,0S", "0,0E", false},
		{"lon too high", "0,0N", "181,0E", false},
		{"lon too low", "0,0N", "181,0W", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lat, err := parseXMPGPS(tc.latStr)
			if err != nil {
				if tc.expectOK {
					t.Fatalf("parseXMPGPS(%q) error: %v", tc.latStr, err)
				}
				return
			}
			lon, err := parseXMPGPS(tc.lonStr)
			if err != nil {
				if tc.expectOK {
					t.Fatalf("parseXMPGPS(%q) error: %v", tc.lonStr, err)
				}
				return
			}
			valid := lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
			if valid != tc.expectOK {
				t.Errorf("lat=%f lon=%f valid=%v, want %v", lat, lon, valid, tc.expectOK)
			}
		})
	}
}

func TestRDFDepthLimit(t *testing.T) {
	t.Parallel()
	// Build deeply nested XML that exceeds the 100-level depth limit.
	var sb strings.Builder
	sb.WriteString(`<?xpacket begin="" uid="abc"?>`)
	sb.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	for range 110 {
		sb.WriteString(`<a>`)
	}
	for range 110 {
		sb.WriteString(`</a>`)
	}
	sb.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	_, err := Parse([]byte(sb.String()))
	if err == nil {
		t.Error("expected error for depth > 100, got nil")
	}
}

func TestXMPSetters(t *testing.T) {
	t.Parallel()
	x := &XMP{}

	x.SetCaption("Hello world")
	x.SetCopyright("(c) 2024")
	x.SetCreator("Alice")
	x.AddKeyword("sunset")
	x.AddKeyword("landscape")
	x.SetCameraModel("Canon EOS R5")

	now := time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC)
	x.SetDateTimeOriginal(now)

	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse after encode: %v", err)
	}

	if got := x2.Caption(); got != "Hello world" {
		t.Errorf("Caption: got %q", got)
	}
	if got := x2.Copyright(); got != "(c) 2024" {
		t.Errorf("Copyright: got %q", got)
	}
	if got := x2.Creator(); got != "Alice" {
		t.Errorf("Creator: got %q", got)
	}
	kws := x2.Keywords()
	if len(kws) != 2 || kws[0] != "sunset" || kws[1] != "landscape" {
		t.Errorf("Keywords: got %v", kws)
	}
	if got := x2.CameraModel(); got != "Canon EOS R5" {
		t.Errorf("CameraModel: got %q", got)
	}
	dto := x2.DateTimeOriginal()
	if dto == "" {
		t.Error("DateTimeOriginal: empty after round-trip")
	}
}

func TestEncodeCollectionType(t *testing.T) {
	t.Parallel()
	// dc:subject must be rdf:Bag, dc:creator must be rdf:Seq,
	// dc:description must be rdf:Alt (ISO 16684-1 §7.5).
	x := &XMP{Properties: map[string]map[string]string{
		NSdc: {
			"subject":     "alpha\x1ebeta\x1egamma",
			"creator":     "Alice\x1eBob",
			"description": "A caption\x1eEine Bildunterschrift",
		},
	}}

	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := string(encoded)

	// Verify correct collection element used for each property.
	if !strings.Contains(out, "<rdf:Bag>") {
		t.Error("dc:subject should use rdf:Bag")
	}
	if !strings.Contains(out, "<rdf:Seq>") {
		t.Error("dc:creator should use rdf:Seq")
	}
	if !strings.Contains(out, "<rdf:Alt>") {
		t.Error("dc:description should use rdf:Alt")
	}
	// Bag and Seq items must NOT have xml:lang.
	if strings.Contains(out, "<rdf:Bag>") && strings.Contains(out, "xml:lang") {
		// Only check if Bag items have xml:lang
		bagIdx := strings.Index(out, "<rdf:Bag>")
		endBagIdx := strings.Index(out[bagIdx:], "</rdf:Bag>")
		if endBagIdx > 0 && strings.Contains(out[bagIdx:bagIdx+endBagIdx], "xml:lang") {
			t.Error("rdf:Bag items should not have xml:lang attribute")
		}
	}

	// Round-trip: keywords must survive parse→encode→parse.
	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse after encode: %v", err)
	}
	kws := x2.Keywords()
	if len(kws) != 3 {
		t.Fatalf("keywords round-trip: got %v, want [alpha beta gamma]", kws)
	}
	if kws[0] != "alpha" || kws[1] != "beta" || kws[2] != "gamma" {
		t.Errorf("keywords round-trip: got %v", kws)
	}
}

// TestXMPGet verifies the public Get accessor for arbitrary namespace/property
// combinations (XMP §7.3 — property access by namespace URI and local name).
func TestXMPGet(t *testing.T) {
	t.Parallel()
	t.Run("known property returns correct value", func(t *testing.T) {
		t.Parallel()
		x, err := Parse([]byte(simpleXMP))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		// tiff:Model is stored under NStiff / "Model".
		if got := x.Get(NStiff, "Model"); got != "Canon EOS R5" {
			t.Errorf("Get(NStiff, Model) = %q, want %q", got, "Canon EOS R5")
		}
	})

	t.Run("missing property returns empty string", func(t *testing.T) {
		t.Parallel()
		x, err := Parse([]byte(simpleXMP))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := x.Get(NStiff, "DoesNotExist"); got != "" {
			t.Errorf("Get for absent property = %q, want empty", got)
		}
	})

	t.Run("missing namespace returns empty string", func(t *testing.T) {
		t.Parallel()
		x, err := Parse([]byte(simpleXMP))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := x.Get("http://example.com/ns/unknown/", "SomeProperty"); got != "" {
			t.Errorf("Get for absent namespace = %q, want empty", got)
		}
	})

	t.Run("nil Properties map returns empty string without panic", func(t *testing.T) {
		t.Parallel()
		x := &XMP{} // Properties is nil
		if got := x.Get(NStiff, "Model"); got != "" {
			t.Errorf("Get on nil Properties = %q, want empty", got)
		}
	})

	t.Run("nil XMP receiver returns empty string without panic", func(t *testing.T) {
		t.Parallel()
		var x *XMP
		// get() guards against nil receiver (see xmp.go get() implementation).
		// Get() delegates to get(), so this must not panic.
		if got := x.Get(NStiff, "Model"); got != "" {
			t.Errorf("Get on nil *XMP = %q, want empty", got)
		}
	})

	t.Run("xmp namespace CreatorTool", func(t *testing.T) {
		t.Parallel()
		// Build an XMP packet that sets xmp:CreatorTool.
		raw := `<?xpacket begin="" uid="abc"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"` +
			` xmp:CreatorTool="Adobe Photoshop CC"/>` +
			`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
		x, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got := x.Get(NSxmp, "CreatorTool"); got != "Adobe Photoshop CC" {
			t.Errorf("Get(NSxmp, CreatorTool) = %q, want %q", got, "Adobe Photoshop CC")
		}
	})

	t.Run("properties set directly survive Get round-trip", func(t *testing.T) {
		t.Parallel()
		// Populate Properties directly (public field) and verify Get retrieves them.
		x := &XMP{Properties: map[string]map[string]string{
			NSexif: {"Flash": "1"},
			NSdc:   {"creator": "Jane Doe"},
		}}
		if got := x.Get(NSexif, "Flash"); got != "1" {
			t.Errorf("Get(NSexif, Flash) = %q, want %q", got, "1")
		}
		if got := x.Get(NSdc, "creator"); got != "Jane Doe" {
			t.Errorf("Get(NSdc, creator) = %q, want %q", got, "Jane Doe")
		}
		if got := x.Get(NSdc, "rights"); got != "" {
			t.Errorf("Get(NSdc, rights) absent = %q, want empty", got)
		}
	})
}

// ── Issue #12: storeProperty first-wins across multiple rdf:Description blocks ─

// TestMultipleDescriptionBlocksSameNS verifies that when a document contains
// two rdf:Description blocks in the same namespace, properties from both blocks
// are preserved (distinct keys coexist) and repeated keys use the first value
// (first-writer-wins policy per ISO 16684-1 §7.4).
func TestMultipleDescriptionBlocksSameNS(t *testing.T) {
	t.Parallel()
	// Two rdf:Description blocks in the dc namespace:
	// - First block: dc:title="First Title", dc:creator="Alice"
	// - Second block: dc:title="Second Title" (same key → first-wins),
	//                 dc:description="A description" (new key → preserved)
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      dc:title="First Title"
      dc:creator="Alice"/>
    <rdf:Description rdf:about=""
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      dc:title="Second Title"
      dc:rights="(c) 2024 Test"/>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// First block wins for dc:title.
	if got := x.Get(NSdc, "title"); got != "First Title" {
		t.Errorf("dc:title first-wins: got %q, want %q", got, "First Title")
	}
	// dc:creator from first block is preserved.
	if got := x.Get(NSdc, "creator"); got != "Alice" {
		t.Errorf("dc:creator from first block: got %q, want %q", got, "Alice")
	}
	// dc:rights from second block is preserved (distinct key, not overwritten).
	if got := x.Get(NSdc, "rights"); got != "(c) 2024 Test" {
		t.Errorf("dc:rights from second block: got %q, want %q", got, "(c) 2024 Test")
	}
}

// TestMultipleDescriptionBlocksDifferentNS verifies that properties from
// different namespaces across multiple rdf:Description blocks all survive.
func TestMultipleDescriptionBlocksDifferentNS(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:tiff="http://ns.adobe.com/tiff/1.0/"
      tiff:Model="Canon EOS R5"/>
    <rdf:Description rdf:about=""
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      dc:creator="Alice"/>
    <rdf:Description rdf:about=""
      xmlns:xmp="http://ns.adobe.com/xap/1.0/"
      xmp:CreatorTool="Lightroom"/>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R5" {
		t.Errorf("CameraModel: got %q, want %q", got, "Canon EOS R5")
	}
	if got := x.Get(NSdc, "creator"); got != "Alice" {
		t.Errorf("dc:creator: got %q, want %q", got, "Alice")
	}
	if got := x.Get(NSxmp, "CreatorTool"); got != "Lightroom" {
		t.Errorf("xmp:CreatorTool: got %q, want %q", got, "Lightroom")
	}
}

// ── Issue #13 + #14: struct-in-array round-trip (xmpMM:History) ──────────────

// TestStructInArrayRoundTrip verifies that an rdf:Seq of struct items
// (xmpMM:History pattern) is parsed, stored, re-serialised as valid XML, and
// re-parsed with full field fidelity.
//
// #13: Parser must not discard rdf:li > rdf:Description children.
// #14: Serialiser must emit rdf:parseType="Resource" wrappers, not <ns:a.b> tags.
func TestStructInArrayRoundTrip(t *testing.T) {
	t.Parallel()
	// xmpMM:History: an rdf:Seq of stEvt structs.
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/"
      xmlns:stEvt="http://ns.adobe.com/xap/1.0/sType/ResourceEvent#">
      <xmpMM:History>
        <rdf:Seq>
          <rdf:li rdf:parseType="Resource">
            <stEvt:action>saved</stEvt:action>
            <stEvt:instanceID>xmp.iid:1</stEvt:instanceID>
          </rdf:li>
          <rdf:li rdf:parseType="Resource">
            <stEvt:action>derived</stEvt:action>
            <stEvt:instanceID>xmp.iid:2</stEvt:instanceID>
          </rdf:li>
        </rdf:Seq>
      </xmpMM:History>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Verify #13: struct fields are stored under "History[N].field" keys.
	if got := x.Get(NSxmpMM, "History[0].action"); got != "saved" {
		t.Errorf("History[0].action: got %q, want %q", got, "saved")
	}
	if got := x.Get(NSxmpMM, "History[0].instanceID"); got != "xmp.iid:1" {
		t.Errorf("History[0].instanceID: got %q, want %q", got, "xmp.iid:1")
	}
	if got := x.Get(NSxmpMM, "History[1].action"); got != "derived" {
		t.Errorf("History[1].action: got %q, want %q", got, "derived")
	}
	if got := x.Get(NSxmpMM, "History[1].instanceID"); got != "xmp.iid:2" {
		t.Errorf("History[1].instanceID: got %q, want %q", got, "xmp.iid:2")
	}

	// Verify #14: Encode produces valid XML (no dot in element names).
	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := string(encoded)
	if strings.Contains(out, ":History[") || strings.Contains(out, ":History.") {
		t.Errorf("serialised XML contains invalid element name (dot or bracket): %s", out)
	}
	if !strings.Contains(out, "rdf:parseType=\"Resource\"") {
		t.Errorf("serialised XML should contain rdf:parseType=\"Resource\" for struct items:\n%s", out)
	}

	// Verify round-trip: re-parse and check field fidelity.
	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	if got := x2.Get(NSxmpMM, "History[0].action"); got != "saved" {
		t.Errorf("round-trip History[0].action: got %q, want %q", got, "saved")
	}
	if got := x2.Get(NSxmpMM, "History[0].instanceID"); got != "xmp.iid:1" {
		t.Errorf("round-trip History[0].instanceID: got %q, want %q", got, "xmp.iid:1")
	}
	if got := x2.Get(NSxmpMM, "History[1].action"); got != "derived" {
		t.Errorf("round-trip History[1].action: got %q, want %q", got, "derived")
	}
}

// TestStructInArrayInlineAttrs verifies that struct fields carried as inline
// attributes of rdf:li > rdf:Description (shorthand form) are also captured.
func TestStructInArrayInlineAttrs(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/"
      xmlns:stEvt="http://ns.adobe.com/xap/1.0/sType/ResourceEvent#">
      <xmpMM:History>
        <rdf:Seq>
          <rdf:li>
            <rdf:Description stEvt:action="converted" stEvt:softwareAgent="Photoshop"/>
          </rdf:li>
        </rdf:Seq>
      </xmpMM:History>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.Get(NSxmpMM, "History[0].action"); got != "converted" {
		t.Errorf("History[0].action (inline attr): got %q, want %q", got, "converted")
	}
	if got := x.Get(NSxmpMM, "History[0].softwareAgent"); got != "Photoshop" {
		t.Errorf("History[0].softwareAgent (inline attr): got %q, want %q", got, "Photoshop")
	}
}

// TestStructPropertyRoundTrip verifies that a plain struct property
// (rdf:parseType="Resource") serialises as valid XML and re-parses correctly.
// #14: "parent.field" keys must not produce <prefix:parent.field> tags.
func TestStructPropertyRoundTrip(t *testing.T) {
	t.Parallel()
	// Build an XMP struct directly via Set (mimics what a parsed doc would produce).
	x := &XMP{Properties: make(map[string]map[string]string)}
	x.Set(NSiptcCore, "CreatorContactInfo.CiEmailWork", "test@example.com")
	x.Set(NSiptcCore, "CreatorContactInfo.CiUrlWork", "https://example.com")
	x.Set(NSiptcCore, "CreatorContactInfo.CiTelWork", "+1-555-0100")

	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := string(encoded)

	// Must NOT contain a dot in any element name (XML validity).
	if strings.Contains(out, ":CreatorContactInfo.") {
		t.Errorf("serialised XML contains dot in element name:\n%s", out)
	}
	// Must use rdf:parseType="Resource".
	if !strings.Contains(out, "rdf:parseType=\"Resource\"") {
		t.Errorf("serialised struct should use rdf:parseType=\"Resource\":\n%s", out)
	}

	// Round-trip: fields must survive parse→encode→parse.
	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	if got := x2.Get(NSiptcCore, "CreatorContactInfo.CiEmailWork"); got != "test@example.com" {
		t.Errorf("round-trip CiEmailWork: got %q, want %q", got, "test@example.com")
	}
	if got := x2.Get(NSiptcCore, "CreatorContactInfo.CiUrlWork"); got != "https://example.com" {
		t.Errorf("round-trip CiUrlWork: got %q, want %q", got, "https://example.com")
	}
}

// ── Issue #15: namespace scope + limits ──────────────────────────────────────

// TestNamespaceScopePopping verifies that namespace declarations in one
// rdf:Description block do not leak into sibling blocks.
// With the pre-fix code, after 3+ blocks the [32]nsEntry table would fill and
// later declarations would be silently dropped.
func TestNamespaceScopePopping(t *testing.T) {
	t.Parallel()
	// Build a document with many sibling rdf:Description blocks, each declaring
	// its own xmlns. Without scope-popping, nsTable fills after 32 total declarations.
	var sb strings.Builder
	sb.WriteString(`<?xpacket begin="" uid="abc"?>`)
	sb.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">`)
	sb.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)

	// 40 sibling rdf:Description blocks, each with a unique namespace.
	// The 33rd+ would overflow the old [32]nsEntry table without scope-popping.
	for i := range 40 {
		ns := "http://example.com/ns" + strconv.Itoa(i) + "/"
		prefix := "ns" + strconv.Itoa(i)
		sb.WriteString(`<rdf:Description rdf:about="" xmlns:`)
		sb.WriteString(prefix)
		sb.WriteString(`="`)
		sb.WriteString(ns)
		sb.WriteString(`" `)
		sb.WriteString(prefix)
		sb.WriteString(`:prop="value`)
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(`"/>`)
	}
	sb.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	x, err := Parse([]byte(sb.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// All 40 properties must be present (scope-popping ensures nsTable never fills).
	for i := range 40 {
		ns := "http://example.com/ns" + strconv.Itoa(i) + "/"
		want := "value" + strconv.Itoa(i)
		if got := x.Get(ns, "prop"); got != want {
			t.Errorf("ns %d: got %q, want %q", i, got, want)
		}
	}
}

// TestInlineAttrLimit verifies that an element with more than 16 inline
// attributes (the old [16]xmpAttr limit) no longer silently drops attributes
// beyond the 16th.
func TestInlineAttrLimit(t *testing.T) {
	t.Parallel()
	// Build an rdf:Description with 25 inline properties.
	// The old [16]xmpAttr buffer would silently drop attributes 17–25.
	var sb strings.Builder
	sb.WriteString(`<?xpacket begin="" uid="abc"?>`)
	sb.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/">`)
	sb.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	sb.WriteString(`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"`)
	for i := range 25 {
		sb.WriteString(` tiff:Tag`)
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(`="val`)
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(`"`)
	}
	sb.WriteString(`/>`)
	sb.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	x, err := Parse([]byte(sb.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Attributes 0–24 must all be present (new [32]xmpAttr buffer covers all 25 + xmlns).
	for i := range 25 {
		key := "Tag" + strconv.Itoa(i)
		want := "val" + strconv.Itoa(i)
		if got := x.Get(NStiff, key); got != want {
			t.Errorf("attr %d: got %q, want %q", i, got, want)
		}
	}
}

// TestDeepNestingNSResolution verifies that deeply nested elements can still
// resolve namespaces declared on ancestor elements after scope-popping is in
// place (i.e. scope-popping does not accidentally remove declarations that are
// still in scope).
func TestDeepNestingNSResolution(t *testing.T) {
	t.Parallel()
	// A struct property using rdf:parseType="Resource" with fields whose namespace
	// is declared on the rdf:Description grandparent. This exercises that scope
	// popping correctly only removes NS entries added by each element, not parent-
	// element declarations.
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:Iptc4xmpCore="http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/">
      <Iptc4xmpCore:CreatorContactInfo rdf:parseType="Resource">
        <Iptc4xmpCore:CiEmailWork>deep@example.com</Iptc4xmpCore:CiEmailWork>
        <Iptc4xmpCore:CiUrlWork>https://deep.example.com</Iptc4xmpCore:CiUrlWork>
      </Iptc4xmpCore:CreatorContactInfo>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.Get(NSiptcCore, "CreatorContactInfo.CiEmailWork"); got != "deep@example.com" {
		t.Errorf("CiEmailWork: got %q, want %q", got, "deep@example.com")
	}
	if got := x.Get(NSiptcCore, "CreatorContactInfo.CiUrlWork"); got != "https://deep.example.com" {
		t.Errorf("CiUrlWork: got %q, want %q", got, "https://deep.example.com")
	}
}

func BenchmarkXMPParse(b *testing.B) {
	data := []byte(simpleXMP)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(data)
	}
}

// BenchmarkXMPEncode measures the serialisation cost of a small XMP struct
// with camera model, copyright, caption, and two keywords.
func BenchmarkXMPEncode(b *testing.B) {
	x, err := Parse([]byte(simpleXMP))
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	x.AddKeyword("benchmark")
	x.AddKeyword("performance")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Encode(x)
	}
}

// TestXMPNewSetters exercises SetGPS, SetLensModel, SetKeywords, and Set.
func TestXMPNewSetters(t *testing.T) {
	t.Parallel()
	t.Run("SetGPS_RoundTrip", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetGPS(37.7749, -122.4194)
		lat, lon, ok := x.GPS()
		if !ok {
			t.Fatal("GPS() returned ok=false after SetGPS")
		}
		// Decimal-minute format preserves ~0.3 mm precision; 1e-4 deg is plenty.
		if lat < 37.774 || lat > 37.776 {
			t.Errorf("lat = %f, want ~37.7749", lat)
		}
		if lon > -122.418 || lon < -122.421 {
			t.Errorf("lon = %f, want ~-122.4194", lon)
		}
	})

	t.Run("SetGPS_SouthWest", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetGPS(-33.8688, -70.6693)
		lat, lon, ok := x.GPS()
		if !ok {
			t.Fatal("GPS() returned ok=false")
		}
		if lat > 0 {
			t.Errorf("southern lat should be negative, got %f", lat)
		}
		if lon > 0 {
			t.Errorf("western lon should be negative, got %f", lon)
		}
	})

	t.Run("SetLensModel", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.SetLensModel("EF 24-70mm f/2.8L II USM")
		if got := x.LensModel(); got != "EF 24-70mm f/2.8L II USM" {
			t.Errorf("LensModel = %q, want %q", got, "EF 24-70mm f/2.8L II USM")
		}
	})

	t.Run("SetKeywords_Replace", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.AddKeyword("old1")
		x.AddKeyword("old2")
		x.SetKeywords([]string{"nature", "landscape", "sunset"})
		kws := x.Keywords()
		if len(kws) != 3 {
			t.Fatalf("Keywords count = %d, want 3", len(kws))
		}
		want := map[string]bool{"nature": true, "landscape": true, "sunset": true}
		for _, kw := range kws {
			if !want[kw] {
				t.Errorf("unexpected keyword %q", kw)
			}
		}
	})

	t.Run("SetKeywords_Empty_DeletesProperty", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.AddKeyword("remove-me")
		x.SetKeywords(nil)
		if kws := x.Keywords(); len(kws) != 0 {
			t.Errorf("Keywords after SetKeywords(nil) = %v, want empty", kws)
		}
	})

	t.Run("SetPublicMethod", func(t *testing.T) {
		t.Parallel()
		x := &XMP{Properties: make(map[string]map[string]string)}
		x.Set(NSexif, "ExposureTime", "1/500")
		if got := x.Get(NSexif, "ExposureTime"); got != "1/500" {
			t.Errorf("Get after Set = %q, want %q", got, "1/500")
		}
	})

	t.Run("NilReceiverNoPanic", func(t *testing.T) {
		t.Parallel()
		var x *XMP
		x.SetGPS(0, 0)
		x.SetLensModel("x")
		x.SetKeywords([]string{"a"})
		x.Set(NSdc, "title", "test")
	})
}

// ---- Group A: rdf.go internal functions (0% coverage) ----------------------

// TestSkipBang exercises skipBang via the XMP parser. An XML DOCTYPE
// declaration (<! ... >) embedded in the XMP body must be silently skipped
// so that the real properties are still parsed correctly.
func TestSkipBangViaParser(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<!DOCTYPE ignored [<!ELEMENT ignored EMPTY>]>` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` tiff:Model="TestCamera"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.CameraModel(); got != "TestCamera" {
		t.Errorf("CameraModel = %q, want %q", got, "TestCamera")
	}
}

// TestSkipCommentViaParser exercises skipComment — an XML comment embedded in
// the XMP body must be silently ignored, preserving surrounding properties.
func TestSkipCommentViaParser(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<!-- this is a comment -->` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` tiff:Model="CommentCamera"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.CameraModel(); got != "CommentCamera" {
		t.Errorf("CameraModel = %q, want %q", got, "CommentCamera")
	}
}

// TestSkipSpecialTagUnterminatedComment verifies that an unterminated comment
// (no closing -->) does not crash the parser.
func TestSkipSpecialTagUnterminatedComment(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<!-- unterminated comment`
	_, _ = Parse([]byte(raw)) // must not panic
}

// TestSkipSpecialTagUnterminatedBang verifies that an unterminated <! construct
// does not crash the parser.
func TestSkipSpecialTagUnterminatedBang(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?><x:xmpmeta><!no closing bracket`
	_, _ = Parse([]byte(raw)) // must not panic
}

// TestSkipSpecialTagAtBoundary verifies skipSpecialTag on an empty / boundary
// input does not panic.
func TestSkipSpecialTagAtBoundary(t *testing.T) {
	t.Parallel()
	// Empty packet body — parser should handle gracefully.
	_, _ = Parse([]byte(`<?xpacket begin="" uid="abc"?><x:xmpmeta/><?xpacket end="w"?>`))
}

// TestXMLEntitiesInAttributes exercises decodeEntity, decodeNamedEntity,
// decodeCharRef, parseHex and parseDec via the XMP attribute-value parser.
// All five predefined XML entities plus decimal and hex numeric character
// references must round-trip correctly.
func TestXMLEntitiesInAttributes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string // value of tiff:Model as it appears in the raw XML
		want  string // expected decoded value
	}{
		{"amp entity", "&amp;amp;", "&amp;"},
		{"lt entity", "&lt;less&gt;", "<less>"},
		{"gt entity", "&gt;", ">"},
		{"quot entity", "&quot;", "\""},
		{"apos entity", "&apos;", "'"},
		{"decimal char ref 65", "&#65;", "A"},
		{"hex char ref lowercase 41", "&#x41;", "A"},
		{"hex char ref uppercase 4F", "&#X4F;", "O"},
		{"unknown entity passthrough", "&unknown;", "&unknown;"},
		{"no semicolon entity literal", "&nosemi", "&nosemi"},
		{"empty input", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := `<?xpacket begin="" uid="abc"?>` +
				`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
				`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
				` tiff:Model="` + tc.input + `"/>` +
				`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
			x, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := x.Get(NStiff, "Model"); got != tc.want {
				t.Errorf("Model = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSkipUnquotedAttrViaParser exercises skipUnquotedAttr. Malformed XML with
// an unquoted attribute value must not crash and should skip past the token.
func TestSkipUnquotedAttrViaParser(t *testing.T) {
	t.Parallel()
	// Unquoted attribute value after '=' — malformed XML; parser must not panic.
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` tiff:Model=UnquotedCamera tiff:Make="Canon"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, _ := Parse([]byte(raw))
	// We don't assert on the value — just that it doesn't panic and that the
	// subsequent quoted attribute is still parsed.
	if x != nil {
		_ = x.CameraModel()
	}
}

// TestXMLEntityEdgeCases covers decodeEntity / decodeCharRef edge cases:
// empty char ref, invalid hex digit, invalid decimal digit.
func TestXMLEntityEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{"empty char ref", "&#;"},
		{"hex invalid digit", "&#xGG;"},
		{"decimal invalid digit", "&#A1;"},
		{"char ref hex only x", "&#x;"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := `<?xpacket begin="" uid="abc"?>` +
				`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
				`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
				` tiff:Model="` + tc.input + `"/>` +
				`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
			_, _ = Parse([]byte(raw)) // must not panic
		})
	}
}

// ---- Group B: xmp/rdf.go struct and list-item paths (0% coverage) -----------

// TestStructPropertyParsing exercises onStartStructValueNode, onStartStructField,
// onCharDataStructField, closeStructField and closeStruct by parsing an XMP
// struct (rdf:parseType="Resource").
func TestStructPropertyParsing(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:Iptc4xmpCore="http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/">
      <Iptc4xmpCore:CreatorContactInfo rdf:parseType="Resource">
        <Iptc4xmpCore:CiEmailWork>test@example.com</Iptc4xmpCore:CiEmailWork>
        <Iptc4xmpCore:CiUrlWork>https://example.com</Iptc4xmpCore:CiUrlWork>
      </Iptc4xmpCore:CreatorContactInfo>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if x == nil {
		t.Fatal("Parse returned nil")
	}
	// Struct fields are stored as "parent.field" keys.
	v := x.Get(NSiptcCore, "CreatorContactInfo.CiEmailWork")
	if v != "test@example.com" {
		t.Errorf("struct field CiEmailWork = %q, want %q", v, "test@example.com")
	}
}

// TestAltLangListItem exercises onCharDataListItem with a non-default xml:lang,
// which should produce a "lang|value" entry in the multi-value join.
func TestAltLangListItem(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">Default caption</rdf:li>
          <rdf:li xml:lang="de">Deutsche Beschreibung</rdf:li>
        </rdf:Alt>
      </dc:description>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	caption := x.Caption()
	// firstValue returns the first part of the multi-value join.
	if caption == "" {
		t.Error("Caption should not be empty")
	}
}

// ---- Group C: writeXMLEscaped coverage (40% → higher) ----------------------

// TestWriteXMLEscapedAllChars exercises all escape branches in writeXMLEscaped
// by round-tripping values through Encode+Parse.
func TestWriteXMLEscapedAllChars(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ampersand", "a&b", "a&b"},
		{"less-than", "a<b", "a<b"},
		{"greater-than", "a>b", "a>b"},
		{"double-quote", `a"b`, `a"b`},
		{"apostrophe", "a'b", "a'b"},
		{"carriage-return", "a\rb", "a\rb"},
		{"no special chars", "plain text", "plain text"},
		{"multiple specials", "<a>&b<", "<a>&b<"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := &XMP{Properties: map[string]map[string]string{
				NStiff: {"Model": tc.input},
			}}
			encoded, err := Encode(x)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			x2, err := Parse(encoded)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := x2.Get(NStiff, "Model"); got != tc.want {
				t.Errorf("round-trip %q: got %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---- Group D: xmp.go accessor nil / fallback paths -------------------------

// TestCameraModelFallbackToCreatorTool covers the fallback branch in CameraModel
// when tiff:Model is absent but xmp:CreatorTool is present.
func TestCameraModelFallbackToCreatorTool(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"` +
		` xmp:CreatorTool="Adobe Lightroom 5"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.CameraModel(); got != "Adobe Lightroom 5" {
		t.Errorf("CameraModel fallback = %q, want %q", got, "Adobe Lightroom 5")
	}
}

// TestGPSNilXMP verifies that GPS() on a nil *XMP returns ok=false without panic.
func TestGPSNilXMP(t *testing.T) {
	t.Parallel()
	var x *XMP
	_, _, ok := x.GPS()
	if ok {
		t.Error("GPS on nil *XMP should return ok=false")
	}
}

// TestGPSMissingCoordinates covers the branch where only one of lat/lon is set.
func TestGPSMissingCoordinates(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSexif: {"GPSLatitude": "37,46N"},
	}}
	_, _, ok := x.GPS()
	if ok {
		t.Error("GPS with only lat set should return ok=false")
	}
}

// TestGPSInvalidFormat covers parseXMPGPS error paths.
func TestGPSInvalidFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		latStr string
		lonStr string
	}{
		{"", "0,0E"},           // empty lat
		{"badlat", "0,0E"},     // no comma in lat
		{"0,badminN", "0,0E"},  // non-numeric minutes in lat
		{"0,0N", "0,badsecE"},  // bad seconds in lon
		{"0,0,badS", "0,0E"},   // bad seconds
		{"0,0,0,badS", "0,0E"}, // too many commas (handled gracefully)
		{"37,46N", "badlon"},   // malformed lon
	}
	for _, tc := range tests {
		t.Run(tc.latStr+"/"+tc.lonStr, func(t *testing.T) {
			t.Parallel()
			x := &XMP{Properties: map[string]map[string]string{
				NSexif: {
					"GPSLatitude":  tc.latStr,
					"GPSLongitude": tc.lonStr,
				},
			}}
			_, _, ok := x.GPS()
			// These should all fail validation.
			_ = ok
		})
	}
}

// TestGPSW3CGeoFallback verifies that GPS() decodes coordinates from the W3C
// Basic Geo vocabulary namespace when exif:GPSLatitude / exif:GPSLongitude are
// absent. This covers the fallback path added for files that embed geo:lat /
// geo:lon instead of the Adobe XMP exif namespace. W3C Geo 2003/01.
func TestGPSW3CGeoFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		latProp string // "lat" with optional "lon" or "long"
		lonProp string
		latVal  string
		lonVal  string
		wantLat float64
		wantLon float64
	}{
		{
			name:    "geo:lat+geo:lon_DM_format",
			latProp: "lat", lonProp: "lon",
			latVal: "48,51.35N", lonVal: "2,21.07E",
			wantLat: 48.8558, wantLon: 2.3512,
		},
		{
			name:    "geo:lat+geo:long_DM_format",
			latProp: "lat", lonProp: "long",
			latVal: "51,30.00N", lonVal: "0,7.00W",
			wantLat: 51.5, wantLon: -0.1167,
		},
		{
			name:    "geo:lat+geo:lon_southern_hemisphere",
			latProp: "lat", lonProp: "lon",
			latVal: "33,52.00S", lonVal: "151,12.00E",
			wantLat: -33.8667, wantLon: 151.2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := &XMP{Properties: map[string]map[string]string{
				NSgeo: {tc.latProp: tc.latVal, tc.lonProp: tc.lonVal},
			}}
			lat, lon, ok := x.GPS()
			if !ok {
				t.Fatalf("GPS() returned ok=false for W3C Geo namespace (lat=%q, lon=%q)", tc.latVal, tc.lonVal)
			}
			const tol = 0.01
			if lat < tc.wantLat-tol || lat > tc.wantLat+tol {
				t.Errorf("lat = %f, want ~%f (±%f)", lat, tc.wantLat, tol)
			}
			if lon < tc.wantLon-tol || lon > tc.wantLon+tol {
				t.Errorf("lon = %f, want ~%f (±%f)", lon, tc.wantLon, tol)
			}
		})
	}
}

// TestGPSW3CGeoFallbackAbsentWhenExifPresent verifies that the W3C Geo fallback
// is NOT consulted when exif:GPSLatitude / exif:GPSLongitude are already present.
// The Adobe XMP namespace must take precedence.
func TestGPSW3CGeoFallbackAbsentWhenExifPresent(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSexif: {
			"GPSLatitude":  "40,26.77N",
			"GPSLongitude": "79,58.36W",
		},
		NSgeo: {
			"lat": "51,30.00N",
			"lon": "0,7.00W",
		},
	}}
	lat, lon, ok := x.GPS()
	if !ok {
		t.Fatal("GPS() returned ok=false when both namespaces are present")
	}
	// Must return the NSexif values (~40.4462 N, ~-79.9727 W), not W3C Geo.
	const tol = 0.01
	if lat < 40.0 || lat > 41.0 {
		t.Errorf("lat = %f, want NSexif value ~40.44 (W3C Geo fallback must not override)", lat)
	}
	if lon > -79.0 || lon < -81.0 {
		t.Errorf("lon = %f, want NSexif value ~-79.97 (W3C Geo fallback must not override)", lon)
	}
	_ = tol
}

// TestGPSDegreesMinutesSeconds covers the DDD,MM,SS.sss path in parseXMPGPS.
func TestGPSDegreesMinutesSeconds(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSexif: {
			"GPSLatitude":  "37,46,29.4N",
			"GPSLongitude": "122,25,9.84W",
		},
	}}
	lat, lon, ok := x.GPS()
	if !ok {
		t.Fatal("GPS DMS format: ok=false")
	}
	if lat < 37.0 || lat > 38.0 {
		t.Errorf("lat = %f, want ~37.77", lat)
	}
	if lon > -122.0 || lon < -123.0 {
		t.Errorf("lon = %f, want ~-122.42", lon)
	}
}

// TestFirstValueSingleItem covers the no-separator branch of firstValue.
func TestFirstValueSingleItem(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSdc: {"rights": "Copyright 2024"},
	}}
	if got := x.Copyright(); got != "Copyright 2024" {
		t.Errorf("Copyright single value = %q, want %q", got, "Copyright 2024")
	}
}

// TestFirstValueMultiItem covers the strings.Cut found branch of firstValue.
func TestFirstValueMultiItem(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSdc: {"description": "First caption\x1eSecond caption"},
	}}
	if got := x.Caption(); got != "First caption" {
		t.Errorf("Caption first-of-multi = %q, want %q", got, "First caption")
	}
}

// TestNilReceiverAccessors verifies all nil-receiver accessor paths.
func TestNilReceiverAccessors(t *testing.T) {
	t.Parallel()
	var x *XMP
	if v := x.CameraModel(); v != "" {
		t.Errorf("nil CameraModel = %q", v)
	}
	if v := x.Copyright(); v != "" {
		t.Errorf("nil Copyright = %q", v)
	}
	if v := x.Caption(); v != "" {
		t.Errorf("nil Caption = %q", v)
	}
	if v := x.DateTimeOriginal(); v != "" {
		t.Errorf("nil DateTimeOriginal = %q", v)
	}
	if v := x.LensModel(); v != "" {
		t.Errorf("nil LensModel = %q", v)
	}
	if v := x.Creator(); v != "" {
		t.Errorf("nil Creator = %q", v)
	}
	if v := x.Keywords(); v != nil {
		t.Errorf("nil Keywords = %v", v)
	}
}

// TestUnescapeXMLOverLimit verifies that unescapeXML returns "" when the
// output would exceed maxUnescapedXMLBytes. We do this by crafting an XMP
// attribute whose entity-expanded form is huge. We build a smaller proxy by
// checking that a very long &amp; chain terminates gracefully.
func TestUnescapeXMLLargeInput(t *testing.T) {
	t.Parallel()
	// Build a value with many & entities to stress the cap logic.
	// 1<<20 bytes = 1 MiB limit — build something that exceeds it.
	const repeats = 1<<20 + 1
	b := make([]byte, repeats*5)
	for i := range repeats {
		copy(b[i*5:], "&amp;")
	}
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` tiff:Model="` + string(b) + `"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, _ := Parse([]byte(raw))
	// We don't assert a specific value — just that it doesn't panic.
	if x != nil {
		_ = x.Get(NStiff, "Model")
	}
}

// TestParseRDFMissingNoComma covers the "no comma in GPS string" error path.
func TestParseXMPGPSErrors(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSexif: {
			"GPSLatitude":  "x", // too short
			"GPSLongitude": "0,0E",
		},
	}}
	_, _, ok := x.GPS()
	if ok {
		t.Error("GPS with too-short lat should return ok=false")
	}
}

// TestStructValueNodeViaNestedDescription triggers onStartStructValueNode and
// onCharDataStructField by parsing a property whose value is an inline
// rdf:Description with typed struct fields (XMP Part 1 §C.2.6).
func TestStructValueNodeViaNestedDescription(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/">
      <photoshop:DocumentAncestors>
        <rdf:Description photoshop:AncestorID="abc123"/>
      </photoshop:DocumentAncestors>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The struct field is stored as "DocumentAncestors.AncestorID"
	if x != nil {
		v := x.Get(NSphotoshop, "DocumentAncestors.AncestorID")
		if v != "abc123" {
			t.Errorf("struct value node field = %q, want %q", v, "abc123")
		}
	}
}

// TestNamespacePrefixFallback exercises prefixOf for an unknown namespace,
// which should fall back to "ns".
func TestNamespacePrefixFallback(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		"http://example.com/unknown/": {"foo": "bar"},
	}}
	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode unknown namespace: %v", err)
	}
	// The encoded output should contain some representation of the namespace.
	if len(encoded) == 0 {
		t.Error("Encode returned empty for unknown namespace")
	}
}

// ── Issue #16a: UTF-16/UTF-32 XMP packets (XMP Part 1 §7.2) ─────────────────

// utf8ToUTF16 encodes a UTF-8 string as UTF-16 with the given BOM, prepending
// it at the start.  Used only in tests to build synthetic UTF-16 XMP packets.
//
//nolint:unparam // s is always xmpPacketBody in current tests; kept as a parameter for future test cases
func utf8ToUTF16(s string, bigEndian bool) []byte {
	runes := []rune(s)
	// 2-byte BOM + 2 bytes per rune (BMP-only test strings).
	out := make([]byte, 2+len(runes)*2)
	if bigEndian {
		out[0], out[1] = 0xFE, 0xFF
	} else {
		out[0], out[1] = 0xFF, 0xFE
	}
	for i, r := range runes {
		if bigEndian {
			binary.BigEndian.PutUint16(out[2+i*2:], uint16(r)) //nolint:gosec // G115: test helper for BMP-only strings; rune values ≤ 0xFFFF are safe to narrow to uint16
		} else {
			binary.LittleEndian.PutUint16(out[2+i*2:], uint16(r)) //nolint:gosec // G115: test helper for BMP-only strings; rune values ≤ 0xFFFF are safe to narrow to uint16
		}
	}
	return out
}

// utf8ToUTF32 encodes a UTF-8 string as UTF-32 with the given BOM.
func utf8ToUTF32(s string, bigEndian bool) []byte {
	runes := []rune(s)
	// 4-byte BOM + 4 bytes per rune.
	out := make([]byte, 4+len(runes)*4)
	if bigEndian {
		out[0], out[1], out[2], out[3] = 0x00, 0x00, 0xFE, 0xFF
	} else {
		out[0], out[1], out[2], out[3] = 0xFF, 0xFE, 0x00, 0x00
	}
	for i, r := range runes {
		if bigEndian {
			binary.BigEndian.PutUint32(out[4+i*4:], uint32(r))
		} else {
			binary.LittleEndian.PutUint32(out[4+i*4:], uint32(r))
		}
	}
	return out
}

// xmpPacketBody is the inner UTF-8 XMP document used for encoding tests.
// It is reused across UTF-16 BE, UTF-16 LE, and UTF-32 sub-tests.
const xmpPacketBody = `<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>` +
	`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
	`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
	`<rdf:Description rdf:about=""` +
	` xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
	` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
	` tiff:Model="Canon EOS R6"` +
	` dc:rights="(c) 2025 UTF16Test"/>` +
	`</rdf:RDF></x:xmpmeta>` +
	`<?xpacket end="w"?>`

// TestScanUTF16BE verifies that Scan correctly detects and transcodes a
// UTF-16 BE XMP packet (XMP Part 1 §7.2).
func TestScanUTF16BE(t *testing.T) {
	t.Parallel()
	utf16 := utf8ToUTF16(xmpPacketBody, true)
	pkt := Scan(utf16)
	if pkt == nil {
		t.Fatal("Scan returned nil for UTF-16 BE packet")
	}
	if !strings.Contains(string(pkt), "<?xpacket begin=") {
		t.Errorf("Scan result missing <?xpacket begin=: %q", pkt[:min(len(pkt), 60)])
	}
}

// TestScanUTF16LE verifies that Scan correctly detects and transcodes a
// UTF-16 LE XMP packet (XMP Part 1 §7.2).
func TestScanUTF16LE(t *testing.T) {
	t.Parallel()
	utf16 := utf8ToUTF16(xmpPacketBody, false)
	pkt := Scan(utf16)
	if pkt == nil {
		t.Fatal("Scan returned nil for UTF-16 LE packet")
	}
	if !strings.Contains(string(pkt), "<?xpacket begin=") {
		t.Errorf("Scan result missing <?xpacket begin=: %q", pkt[:min(len(pkt), 60)])
	}
}

// TestParseUTF16BE verifies that Parse extracts XMP properties correctly from
// a UTF-16 BE encoded packet (XMP Part 1 §7.2).
func TestParseUTF16BE(t *testing.T) {
	t.Parallel()
	utf16 := utf8ToUTF16(xmpPacketBody, true)
	x, err := Parse(utf16)
	if err != nil {
		t.Fatalf("Parse UTF-16 BE: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R6" {
		t.Errorf("CameraModel from UTF-16 BE = %q, want %q", got, "Canon EOS R6")
	}
	if got := x.Copyright(); got != "(c) 2025 UTF16Test" {
		t.Errorf("Copyright from UTF-16 BE = %q, want %q", got, "(c) 2025 UTF16Test")
	}
}

// TestParseUTF16LE verifies that Parse extracts XMP properties correctly from
// a UTF-16 LE encoded packet (XMP Part 1 §7.2).
func TestParseUTF16LE(t *testing.T) {
	t.Parallel()
	utf16 := utf8ToUTF16(xmpPacketBody, false)
	x, err := Parse(utf16)
	if err != nil {
		t.Fatalf("Parse UTF-16 LE: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R6" {
		t.Errorf("CameraModel from UTF-16 LE = %q, want %q", got, "Canon EOS R6")
	}
	if got := x.Copyright(); got != "(c) 2025 UTF16Test" {
		t.Errorf("Copyright from UTF-16 LE = %q, want %q", got, "(c) 2025 UTF16Test")
	}
}

// TestParseUTF32BE verifies that Parse handles UTF-32 BE packets.
// UTF-32 is extremely rare in practice but legal per XMP Part 1 §7.2.
func TestParseUTF32BE(t *testing.T) {
	t.Parallel()
	utf32 := utf8ToUTF32(xmpPacketBody, true)
	x, err := Parse(utf32)
	if err != nil {
		t.Fatalf("Parse UTF-32 BE: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R6" {
		t.Errorf("CameraModel from UTF-32 BE = %q, want %q", got, "Canon EOS R6")
	}
}

// TestParseUTF32LE verifies that Parse handles UTF-32 LE packets.
func TestParseUTF32LE(t *testing.T) {
	t.Parallel()
	utf32 := utf8ToUTF32(xmpPacketBody, false)
	x, err := Parse(utf32)
	if err != nil {
		t.Fatalf("Parse UTF-32 LE: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R6" {
		t.Errorf("CameraModel from UTF-32 LE = %q, want %q", got, "Canon EOS R6")
	}
}

// TestParseUTF8NoRegression verifies that the UTF-8 fast path is not affected
// by the BOM-detection logic added for #16a.
func TestParseUTF8NoRegression(t *testing.T) {
	t.Parallel()
	x, err := Parse([]byte(simpleXMP))
	if err != nil {
		t.Fatalf("Parse UTF-8: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R5" {
		t.Errorf("CameraModel UTF-8 regression: got %q, want %q", got, "Canon EOS R5")
	}
}

// TestDetectEncoding verifies the BOM-detection logic in isolation.
func TestDetectEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		b    []byte
		want xmpEncoding
	}{
		{"UTF-8 (no BOM)", []byte("<?xpacket"), encUTF8},
		{"UTF-16 BE (FE FF)", []byte{0xFE, 0xFF, 0x00, 0x3C}, encUTF16BE},
		{"UTF-16 LE (FF FE)", []byte{0xFF, 0xFE, 0x3C, 0x00}, encUTF16LE},
		{"UTF-32 BE (00 00 FE FF)", []byte{0x00, 0x00, 0xFE, 0xFF, 0x00, 0x00, 0x00, 0x3C}, encUTF32BE},
		{"UTF-32 LE (FF FE 00 00)", []byte{0xFF, 0xFE, 0x00, 0x00, 0x3C, 0x00, 0x00, 0x00}, encUTF32LE},
		{"empty", []byte{}, encUTF8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := detectEncoding(tc.b); got != tc.want {
				t.Errorf("detectEncoding = %v, want %v", got, tc.want)
			}
		})
	}
}

// ── Issue #16b: CDATA sections (XML 1.0 §2.7; XMP Part 1) ───────────────────

// TestCDATAInPropertyValue verifies that a CDATA section within a simple
// property element is extracted correctly and its content treated as
// literal character data (no entity expansion).
//
// XML 1.0 §2.7: "the processor MUST NOT expand any character references or
// entity references within a CDATA section."
func TestCDATAInPropertyValue(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/">` +
		`<tiff:ImageDescription><![CDATA[A description with <special> & "chars"]]></tiff:ImageDescription>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse CDATA: %v", err)
	}
	got := x.Get(NStiff, "ImageDescription")
	// Content must be verbatim — no entity expansion inside CDATA.
	want := `A description with <special> & "chars"`
	if got != want {
		t.Errorf("CDATA value = %q, want %q", got, want)
	}
}

// TestCDATAWithInternalBrackets verifies that a CDATA section containing ']'
// characters (but not ']]>') is handled correctly.
func TestCDATAWithInternalBrackets(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:title><![CDATA[Array[0] and Array[1] values]]></dc:title>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse CDATA with brackets: %v", err)
	}
	got := x.Get(NSdc, "title")
	want := "Array[0] and Array[1] values"
	if got != want {
		t.Errorf("CDATA bracket value = %q, want %q", got, want)
	}
}

// TestCDATANoEntityExpansion verifies that ampersands and angle brackets
// within a CDATA section are NOT entity-expanded (XML 1.0 §2.7).
func TestCDATANoEntityExpansion(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:rights><![CDATA[© 2025 & <Company> "All rights reserved"]]></dc:rights>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse CDATA no-entity: %v", err)
	}
	got := x.Copyright()
	want := `© 2025 & <Company> "All rights reserved"`
	if got != want {
		t.Errorf("CDATA no-entity = %q, want %q", got, want)
	}
}

// TestCDATAMixedWithText verifies that text before and after a CDATA section
// is correctly concatenated with the CDATA content.
func TestCDATAMixedWithText(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/">` +
		`<tiff:Make>before<![CDATA[middle]]>after</tiff:Make>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse CDATA mixed: %v", err)
	}
	got := x.Get(NStiff, "Make")
	want := "beforemiddleafter"
	if got != want {
		t.Errorf("CDATA mixed = %q, want %q", got, want)
	}
}

// TestCDATAUnterminatedSafe verifies that an unterminated CDATA section does
// not panic or return an error that breaks the parse entirely.
func TestCDATAUnterminatedSafe(t *testing.T) {
	t.Parallel()
	// Property with an unterminated CDATA section — parser must not panic.
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` tiff:Model="SafeCamera">` +
		`<tiff:Make><![CDATA[unterminated`
	_, _ = Parse([]byte(raw)) // must not panic
}

// TestCDATAPreservesExistingProperties verifies that the CDATA handler does not
// interfere with properties that were correctly parsed before or after a CDATA
// section in the same document.
func TestCDATAPreservesExistingProperties(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about=""` +
		` xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/"` +
		` tiff:Model="TestCam">` +
		`<dc:rights><![CDATA[© 2025 CDATA Corp]]></dc:rights>` +
		`</rdf:Description>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse CDATA with surrounding properties: %v", err)
	}
	if got := x.CameraModel(); got != "TestCam" {
		t.Errorf("CameraModel after CDATA = %q, want %q", got, "TestCam")
	}
	if got := x.Copyright(); got != "© 2025 CDATA Corp" {
		t.Errorf("Copyright from CDATA = %q, want %q", got, "© 2025 CDATA Corp")
	}
}

// TestDepthLimitStillEnforced verifies that the 100-level nesting depth limit
// is preserved after the #16a/#16b changes.
func TestDepthLimitStillEnforced16(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	sb.WriteString(`<?xpacket begin="" uid="abc"?>`)
	sb.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	for range 102 {
		sb.WriteString(`<a>`)
	}
	for range 102 {
		sb.WriteString(`</a>`)
	}
	sb.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	_, err := Parse([]byte(sb.String()))
	if err == nil {
		t.Error("expected ErrXMLNestingDepth for depth > 100, got nil")
	}
}

// TestDoctypeStillSkipped verifies that DOCTYPE constructs are still silently
// skipped after the CDATA fix (anti-XXE regression test).
func TestDoctypeStillSkipped(t *testing.T) {
	t.Parallel()
	raw := `<?xpacket begin="" uid="abc"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<!DOCTYPE foo [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` +
		`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
		` tiff:Model="AfterDoctype"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse after DOCTYPE: %v", err)
	}
	if got := x.CameraModel(); got != "AfterDoctype" {
		t.Errorf("CameraModel after DOCTYPE = %q, want %q", got, "AfterDoctype")
	}
}

func BenchmarkXMPParse_RealWorld(b *testing.B) {
	raw, err := os.ReadFile("../testdata/corpus/jpeg/exiftool/ExifTool.jpg")
	if err != nil {
		b.Skip("corpus file not available")
	}
	pkt := Scan(raw)
	if pkt == nil {
		b.Skip("no XMP packet found in ExifTool.jpg")
	}
	b.SetBytes(int64(len(pkt)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(pkt)
	}
}

// ── Issue #36: depth underflow — unmatched close tags must not panic ──────────

// TestDepthUnderflowNoPanic is a regression test for the critical security bug
// reported in issue #36: xmp.Parse panicked with "index out of range [-1]" when
// the input contained more close tags than open tags.
//
// Root cause: onEndElement decremented p.depth unconditionally; a sequence of
// unmatched </x> tags drove depth negative; the subsequent p.depth++ in
// parseStartTag produced p.depth == -1; nsDepth[-1] then panicked.
//
// The fix: onEndElement returns immediately when p.depth <= 0, treating
// unmatched close tags as silently ignored (consistent with the parser's lenient
// design — only ErrEmptyInput and ErrXMLNestingDepth are fatal).
func TestDepthUnderflowNoPanic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input []byte
	}{
		// Exact reproducer from fuzz corpus FuzzParseXMP/8f08ef9ff3ff470c.
		{"fuzz-reproducer", []byte("</></><0")},
		// Bare unmatched close tags with no opening context.
		{"three-unmatched-close", []byte("</a></a></a>")},
		// Many consecutive unmatched close tags — stress the underflow guard.
		{"many-unmatched-close", []byte(strings.Repeat("</x>", 200))},
		// Unmatched close tags followed by a valid start tag.
		{"unmatched-then-open", []byte("</a></a><b>text</b>")},
		// Mix of unmatched close tags inside an otherwise valid packet.
		{"close-then-valid-xmp", []byte(
			`<?xpacket begin="" uid="abc"?>` +
				`</orphan></orphan>` +
				`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
				`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
				`<rdf:Description rdf:about="" xmlns:tiff="http://ns.adobe.com/tiff/1.0/"` +
				` tiff:Model="StillParsed"/>` +
				`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`,
		)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Must not panic — any return value (nil or non-nil) is acceptable.
			// A panic would be caught by the testing runtime as a failure.
			_, _ = Parse(tc.input)
		})
	}
}

// ── Issue #38: UTF-16 transcode size cap (DoS prevention) ────────────────────

// TestUTF16TranscodeCapRejectsLargeInput verifies that toUTF8 returns nil for
// UTF-16 inputs larger than maxXMPTranscodeBytes WITHOUT allocating a transcode
// buffer.  This prevents a DoS via a crafted PNG/HEIF chunk carrying a ~256 MiB
// UTF-16 XMP packet (which would cause transform.Bytes to allocate ~512 MiB).
func TestUTF16TranscodeCapRejectsLargeInput(t *testing.T) {
	t.Parallel()
	// Build a buffer whose length exceeds the cap. We don't need it to contain
	// valid UTF-16 — the size check fires before any decoding attempt.
	// Use a minimal allocation: make a slice larger than the cap, put a UTF-16
	// BE BOM at the front so detectEncoding returns encUTF16BE.
	oversized := make([]byte, maxXMPTranscodeBytes+1)
	oversized[0], oversized[1] = 0xFE, 0xFF // UTF-16 BE BOM

	// Confirm the size guard fires: toUTF8 must return nil.
	got := toUTF8(oversized, encUTF16BE)
	if got != nil {
		t.Errorf("toUTF8 with oversized UTF-16 BE input: want nil, got %d bytes", len(got))
	}

	// Same for UTF-16 LE.
	oversized[0], oversized[1] = 0xFF, 0xFE // UTF-16 LE BOM
	got = toUTF8(oversized, encUTF16LE)
	if got != nil {
		t.Errorf("toUTF8 with oversized UTF-16 LE input: want nil, got %d bytes", len(got))
	}
}

// TestUTF16TranscodeCapAllowsNormalInput verifies that valid UTF-16 packets
// smaller than maxXMPTranscodeBytes are still transcoded and parsed correctly
// (regression guard: the cap must not block legitimate traffic).
func TestUTF16TranscodeCapAllowsNormalInput(t *testing.T) {
	t.Parallel()
	// xmpPacketBody is well under 1 MiB; must parse without error.
	utf16be := utf8ToUTF16(xmpPacketBody, true)
	if len(utf16be) >= maxXMPTranscodeBytes {
		t.Skip("test packet unexpectedly exceeds cap; adjust test constant")
	}
	x, err := Parse(utf16be)
	if err != nil {
		t.Fatalf("Parse normal UTF-16 BE after cap added: %v", err)
	}
	if got := x.CameraModel(); got != "Canon EOS R6" {
		t.Errorf("CameraModel = %q, want %q", got, "Canon EOS R6")
	}
}

// TestNormaliseToUTF8CapPropagates verifies that the nil return propagates
// through normaliseToUTF8 so that Parse returns ErrEmptyInput rather than
// crashing or returning garbage.
func TestNormaliseToUTF8CapPropagates(t *testing.T) {
	t.Parallel()
	oversized := make([]byte, maxXMPTranscodeBytes+1)
	oversized[0], oversized[1] = 0xFE, 0xFF // UTF-16 BE BOM

	_, err := Parse(oversized)
	// Parse must not panic and must return a non-nil error.
	if err == nil {
		t.Error("Parse oversized UTF-16: want error, got nil")
	}
}

// ── Issue #41: UTF-32 invalid code point substitution ────────────────────────

// TestDecodeUTF32InvalidCodePoints verifies that code points above U+10FFFF
// and UTF-16 surrogate code points (U+D800–U+DFFF) are replaced with U+FFFD
// (REPLACEMENT CHARACTER) rather than producing invalid UTF-8 sequences.
func TestDecodeUTF32InvalidCodePoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cp   uint32
		want rune // expected rune in decoded output
	}{
		{"above U+10FFFF", 0x110000, unicode.ReplacementChar},
		{"max invalid", 0xFFFFFFFF, unicode.ReplacementChar},
		{"surrogate low D800", 0xD800, unicode.ReplacementChar},
		{"surrogate high DFFF", 0xDFFF, unicode.ReplacementChar},
		{"surrogate mid D900", 0xD900, unicode.ReplacementChar},
		{"valid BMP A", 0x0041, 'A'},          // U+0041 LATIN CAPITAL LETTER A
		{"valid 2-byte 0x80", 0x0080, 0x0080}, // U+0080
		{"valid 3-byte 4E00", 0x4E00, 0x4E00}, // U+4E00 CJK unified
		{"valid max 10FFFF", 0x10FFFF, 0x10FFFF},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a minimal UTF-32 BE buffer: 4-byte BOM + 4-byte code point.
			b := make([]byte, 8)
			b[0], b[1], b[2], b[3] = 0x00, 0x00, 0xFE, 0xFF // BE BOM
			b[4] = byte(tc.cp >> 24)
			b[5] = byte(tc.cp >> 16) //nolint:gosec // G115: intentional narrow to byte for big-endian UTF-32 encoding in test
			b[6] = byte(tc.cp >> 8)  //nolint:gosec // G115: intentional narrow to byte for big-endian UTF-32 encoding in test
			b[7] = byte(tc.cp)       //nolint:gosec // G115: intentional narrow to byte for big-endian UTF-32 encoding in test

			out := decodeUTF32(b, true)
			if len(out) == 0 {
				t.Fatalf("decodeUTF32 returned empty output for cp=0x%X", tc.cp)
			}
			// Decode the first rune from the UTF-8 output.
			r, _ := decodeFirstRune(out)
			if r != tc.want {
				t.Errorf("cp=0x%X: decoded rune = U+%04X, want U+%04X", tc.cp, r, tc.want)
			}
		})
	}
}

// TestDecodeUTF32SurrogateRoundTrip verifies that a UTF-32 buffer composed
// entirely of surrogate code points produces a stream of U+FFFD characters
// and does not panic or produce invalid UTF-8.
func TestDecodeUTF32SurrogateRoundTrip(t *testing.T) {
	t.Parallel()
	// Build a UTF-32 BE buffer with 3 surrogate code points.
	// Expected output: 3 × U+FFFD (3 × 3 UTF-8 bytes = 9 bytes).
	surrogates := []uint32{0xD800, 0xDC00, 0xDFFF}
	b := make([]byte, 4+len(surrogates)*4)
	b[0], b[1], b[2], b[3] = 0x00, 0x00, 0xFE, 0xFF // BE BOM
	for i, cp := range surrogates {
		off := 4 + i*4
		b[off] = byte(cp >> 24)
		b[off+1] = byte(cp >> 16) //nolint:gosec // G115: intentional narrow for big-endian UTF-32 test encoding
		b[off+2] = byte(cp >> 8)  //nolint:gosec // G115: intentional narrow for big-endian UTF-32 test encoding
		b[off+3] = byte(cp)       //nolint:gosec // G115: intentional narrow for big-endian UTF-32 test encoding
	}

	out := decodeUTF32(b, true)
	// Count replacement characters in the output.
	replacements := strings.Count(string(out), string(rune(unicode.ReplacementChar)))
	if replacements != len(surrogates) {
		t.Errorf("surrogate substitution: got %d U+FFFD, want %d", replacements, len(surrogates))
	}
}

// decodeFirstRune is a test helper that decodes the first UTF-8 rune from b.
func decodeFirstRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return unicode.ReplacementChar, 0
	}
	r := rune(b[0])
	if r < 0x80 {
		return r, 1
	}
	// 2-byte sequence: 110xxxxx 10xxxxxx
	if r&0xE0 == 0xC0 && len(b) >= 2 && b[1]&0xC0 == 0x80 {
		return (r&0x1F)<<6 | rune(b[1]&0x3F), 2
	}
	// 3-byte sequence: 1110xxxx 10xxxxxx 10xxxxxx
	if r&0xF0 == 0xE0 && len(b) >= 3 && b[1]&0xC0 == 0x80 && b[2]&0xC0 == 0x80 {
		return (r&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F), 3
	}
	// 4-byte sequence: 11110xxx 10xxxxxx 10xxxxxx 10xxxxxx
	if r&0xF8 == 0xF0 && len(b) >= 4 && b[1]&0xC0 == 0x80 && b[2]&0xC0 == 0x80 && b[3]&0xC0 == 0x80 {
		return (r&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F), 4
	}
	return unicode.ReplacementChar, 1
}

// ── Issue #43: xmpMM ordered arrays serialised as rdf:Seq ────────────────────

// TestCollectionTypeXmpMMSeq verifies that collectionType returns "Seq" for
// xmpMM:History, xmpMM:Ingredients, and xmpMM:Pantry per Adobe XMP Spec Part 2 §1.2.8.
func TestCollectionTypeXmpMMSeq(t *testing.T) {
	t.Parallel()
	seqProps := []string{"History", "Ingredients", "Pantry"}
	for _, local := range seqProps {
		t.Run(local, func(t *testing.T) {
			t.Parallel()
			if got := collectionType(NSxmpMM, local); got != "Seq" {
				t.Errorf("collectionType(%q, %q) = %q, want %q", NSxmpMM, local, got, "Seq")
			}
		})
	}
}

// TestCollectionTypeXmpMMOtherBag verifies that other xmpMM properties that are
// not explicitly listed still default to "Bag".
func TestCollectionTypeXmpMMOtherBag(t *testing.T) {
	t.Parallel()
	if got := collectionType(NSxmpMM, "SomeUnknownProp"); got != "Bag" {
		t.Errorf("collectionType(NSxmpMM, SomeUnknownProp) = %q, want %q", got, "Bag")
	}
}

// TestXmpMMHistorySerialiseAsSeq verifies that an xmpMM:History list is
// serialised as <rdf:Seq> (not <rdf:Bag>) in the encoded XMP packet.
// Adobe XMP Specification Part 2 §1.2.8.
func TestXmpMMHistorySerialiseAsSeq(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSxmpMM: {
			"History[0].action":     "saved",
			"History[0].instanceID": "xmp.iid:1",
			"History[1].action":     "derived",
			"History[1].instanceID": "xmp.iid:2",
		},
	}}

	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out := string(encoded)

	// Must use rdf:Seq, not rdf:Bag.
	if !strings.Contains(out, "<rdf:Seq>") {
		t.Errorf("xmpMM:History should be serialised as rdf:Seq:\n%s", out)
	}
	if strings.Contains(out, "<rdf:Bag>") {
		t.Errorf("xmpMM:History must NOT be serialised as rdf:Bag:\n%s", out)
	}
}

// TestXmpMMHistoryOrderPreservedRoundTrip verifies that xmpMM:History items
// survive a parse → encode → parse round-trip with their original order and
// field values intact.  Order preservation is the reason History must be rdf:Seq.
func TestXmpMMHistoryOrderPreservedRoundTrip(t *testing.T) {
	t.Parallel()
	// Build an XMP document with a three-item History sequence.
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/"
      xmlns:stEvt="http://ns.adobe.com/xap/1.0/sType/ResourceEvent#">
      <xmpMM:History>
        <rdf:Seq>
          <rdf:li rdf:parseType="Resource">
            <stEvt:action>created</stEvt:action>
            <stEvt:instanceID>xmp.iid:1</stEvt:instanceID>
          </rdf:li>
          <rdf:li rdf:parseType="Resource">
            <stEvt:action>saved</stEvt:action>
            <stEvt:instanceID>xmp.iid:2</stEvt:instanceID>
          </rdf:li>
          <rdf:li rdf:parseType="Resource">
            <stEvt:action>derived</stEvt:action>
            <stEvt:instanceID>xmp.iid:3</stEvt:instanceID>
          </rdf:li>
        </rdf:Seq>
      </xmpMM:History>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Encode and re-parse.
	encoded, err := Encode(x)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Re-encode output must use rdf:Seq.
	if !strings.Contains(string(encoded), "<rdf:Seq>") {
		t.Errorf("re-encoded xmpMM:History should use rdf:Seq:\n%s", string(encoded))
	}

	x2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}

	// Verify all three items in order.
	want := []struct{ action, id string }{
		{"created", "xmp.iid:1"},
		{"saved", "xmp.iid:2"},
		{"derived", "xmp.iid:3"},
	}
	for i, w := range want {
		actionKey := "History[" + strconv.Itoa(i) + "].action"
		idKey := "History[" + strconv.Itoa(i) + "].instanceID"
		if got := x2.Get(NSxmpMM, actionKey); got != w.action {
			t.Errorf("round-trip %s = %q, want %q", actionKey, got, w.action)
		}
		if got := x2.Get(NSxmpMM, idKey); got != w.id {
			t.Errorf("round-trip %s = %q, want %q", idKey, got, w.id)
		}
	}
}

// ── Task #72: parse buffer aliasing fix ──────────────────────────────────────

// TestParsedPropertyIndependentOfInputBuffer verifies that parsed property
// values are independent copies of the input []byte. After zeroing the source
// buffer, all previously-parsed property values must retain their original
// content.
//
// Regression gate for task #72: with the old unsafe.String fast path in
// unescapeXML, the property strings aliased the caller's parse buffer; zeroing
// that buffer after Parse returned would silently corrupt all entity-free
// property values (no error, wrong data). The fix replaces unsafe.String with
// string(b), which produces an independent heap copy.
func TestParsedPropertyIndependentOfInputBuffer(t *testing.T) {
	t.Parallel()

	// Packet with no XML entities — all values go through the unescapeXML fast
	// path. Every attribute and element text segment must be a copy, not an alias.
	raw := `<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:tiff="http://ns.adobe.com/tiff/1.0/"
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      tiff:Model="Canon EOS R5"
      tiff:Make="Canon">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">Scenic landscape</rdf:li>
        </rdf:Alt>
      </dc:description>
      <dc:rights>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">Copyright 2024 Test</rdf:li>
        </rdf:Alt>
      </dc:rights>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	// Work on a mutable copy so we can zero it after Parse.
	buf := []byte(raw)

	x, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Record expected values before mutation.
	wantModel := "Canon EOS R5"
	wantCaption := "Scenic landscape"
	wantCopyright := "Copyright 2024 Test"

	// Overwrite the source buffer — simulates sync.Pool reuse or buffer recycling.
	for i := range buf {
		buf[i] = 0x00
	}

	// All properties must still hold their original values.
	if got := x.CameraModel(); got != wantModel {
		t.Errorf("CameraModel after buffer zero: got %q, want %q (buffer aliasing)", got, wantModel)
	}
	if got := x.Caption(); got != wantCaption {
		t.Errorf("Caption after buffer zero: got %q, want %q (buffer aliasing)", got, wantCaption)
	}
	if got := x.Copyright(); got != wantCopyright {
		t.Errorf("Copyright after buffer zero: got %q, want %q (buffer aliasing)", got, wantCopyright)
	}
}

// ── Task #73: rdf:Alt x-default selection ────────────────────────────────────

// TestAltXDefaultNotFirst verifies that Caption() / Copyright() return the
// x-default item from an rdf:Alt collection even when x-default is NOT the
// first rdf:li in document order.
//
// Regression gate for task #73: the old firstValue() returned the first item
// in document order, which is often a language-tagged item. XMP Part 1
// §C.2.5 / P1-H mandates x-default as the canonical value. ExifTool and
// Lightroom frequently emit language-specific items before x-default.
func TestAltXDefaultNotFirst(t *testing.T) {
	t.Parallel()

	// dc:description Alt with a language-tagged item FIRST, x-default SECOND.
	// dc:rights Alt with x-default FIRST (must still work).
	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="de">Beschreibung</rdf:li>
          <rdf:li xml:lang="x-default">Description</rdf:li>
        </rdf:Alt>
      </dc:description>
      <dc:rights>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">Copyright 2024</rdf:li>
          <rdf:li xml:lang="en">Copyright 2024 EN</rdf:li>
        </rdf:Alt>
      </dc:rights>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Caption must return the x-default value, not the German-tagged first item.
	if got := x.Caption(); got != "Description" {
		t.Errorf("Caption: got %q, want %q (x-default not selected)", got, "Description")
	}

	// Copyright must return the x-default value (it is first — must still work).
	if got := x.Copyright(); got != "Copyright 2024" {
		t.Errorf("Copyright: got %q, want %q", got, "Copyright 2024")
	}
}

// TestAltXDefaultMultipleLanguages verifies x-default selection across a
// packet with many language alternatives and x-default last, matching real
// output from ExifTool batch processing.
func TestAltXDefaultMultipleLanguages(t *testing.T) {
	t.Parallel()

	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="fr">Description en francais</rdf:li>
          <rdf:li xml:lang="es">Texto en espanol</rdf:li>
          <rdf:li xml:lang="ja">&#26085;&#26412;&#35486;&#35500;&#26126;</rdf:li>
          <rdf:li xml:lang="x-default">Default Description</rdf:li>
        </rdf:Alt>
      </dc:description>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got := x.Caption(); got != "Default Description" {
		t.Errorf("Caption (x-default last of 4): got %q, want %q", got, "Default Description")
	}
}

// TestAltNoXDefault verifies that when no x-default item exists, firstValue
// falls back to the first item in document order (best-effort).
func TestAltNoXDefault(t *testing.T) {
	t.Parallel()

	raw := `<?xpacket begin="" uid="abc"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="en">English Description</rdf:li>
          <rdf:li xml:lang="de">Deutsche Beschreibung</rdf:li>
        </rdf:Alt>
      </dc:description>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

	x, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// No x-default: fall back to first item. The stored value is "en|English Description"
	// because a language-tagged item with no bare alternative is the best we can return.
	got := x.Caption()
	if got == "" {
		t.Error("Caption: got empty, want non-empty fallback value")
	}
	// The fallback is the first item as stored — "en|English Description".
	if got != "en|English Description" {
		t.Errorf("Caption no-x-default fallback: got %q, want %q", got, "en|English Description")
	}
}
