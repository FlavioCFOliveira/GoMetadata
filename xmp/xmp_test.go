package xmp

import (
	"strings"
	"testing"
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
	v := x.get(NSdc, "subject")
	parts := strings.Split(v, "\x1e")
	if len(parts) != 3 {
		t.Errorf("expected 3 subject values, got %d: %v", len(parts), parts)
	}
}

func TestScanPacketBoundaryWithInternalPI(t *testing.T) {
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
	result := Scan([]byte("<not an xmp packet>"))
	if result != nil {
		t.Error("Scan should return nil when no packet is found")
	}
}

func TestScanMissingClosingPI(t *testing.T) {
	raw := "<?xpacket begin=\"\" uid=\"abc\"?><x:xmpmeta/>"
	result := Scan([]byte(raw))
	if result != nil {
		t.Error("Scan should return nil when closing PI is missing")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
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
	// Build deeply nested XML that exceeds the 100-level depth limit.
	var sb strings.Builder
	sb.WriteString(`<?xpacket begin="" uid="abc"?>`)
	sb.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	for i := 0; i < 110; i++ {
		sb.WriteString(`<a>`)
	}
	for i := 0; i < 110; i++ {
		sb.WriteString(`</a>`)
	}
	sb.WriteString(`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)

	_, err := Parse([]byte(sb.String()))
	if err == nil {
		t.Error("expected error for depth > 100, got nil")
	}
}

func BenchmarkXMPParse(b *testing.B) {
	data := []byte(simpleXMP)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Parse(data)
	}
}
