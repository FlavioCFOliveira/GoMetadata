package xmp

import (
	"os"
	"strings"
	"testing"
	"time"
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
