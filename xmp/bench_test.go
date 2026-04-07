package xmp

import (
	"strings"
	"testing"
)

// representativeXMP is a realistic XMP packet used across multiple benchmarks.
// It covers: inline attributes, rdf:Alt (dc:description), rdf:Bag (dc:subject),
// exif namespace GPS coordinates, and tiff:Model — the most common hot paths.
const representativeXMP = `<?xpacket begin="" uid="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/">
  <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
    <rdf:Description rdf:about=""
      xmlns:dc="http://purl.org/dc/elements/1.1/"
      xmlns:tiff="http://ns.adobe.com/tiff/1.0/"
      xmlns:exif="http://ns.adobe.com/exif/1.0/"
      xmlns:xmp="http://ns.adobe.com/xap/1.0/"
      tiff:Model="Canon EOS R5"
      tiff:Make="Canon"
      exif:GPSLatitude="37,46.494N"
      exif:GPSLongitude="122,25.164W"
      xmp:CreatorTool="Adobe Photoshop Lightroom">
      <dc:description>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">A scenic mountain landscape at sunset</rdf:li>
        </rdf:Alt>
      </dc:description>
      <dc:rights>
        <rdf:Alt>
          <rdf:li xml:lang="x-default">Copyright 2024 Test Photographer</rdf:li>
        </rdf:Alt>
      </dc:rights>
      <dc:subject>
        <rdf:Bag>
          <rdf:li>nature</rdf:li>
          <rdf:li>landscape</rdf:li>
          <rdf:li>sunset</rdf:li>
          <rdf:li>mountains</rdf:li>
          <rdf:li>outdoors</rdf:li>
        </rdf:Bag>
      </dc:subject>
      <dc:creator>
        <rdf:Seq>
          <rdf:li>Jane Photographer</rdf:li>
        </rdf:Seq>
      </dc:creator>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// BenchmarkRDFParse measures the cost of parsing a representative XMP packet
// with inline attributes, rdf:Alt, rdf:Bag, rdf:Seq, and exif GPS properties.
func BenchmarkRDFParse(b *testing.B) {
	data := []byte(representativeXMP)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(data)
	}
}

// BenchmarkXMPEncodeFullPacket measures the serialisation cost of an XMP struct with
// camera model, copyright, caption, GPS, and multiple keywords.
func BenchmarkXMPEncodeFullPacket(b *testing.B) {
	x := &XMP{Properties: make(map[string]map[string]string)}
	x.SetCameraModel("Canon EOS R5")
	x.SetCopyright("Copyright 2024 Benchmark")
	x.SetCaption("A benchmark caption")
	x.SetCreator("Benchmark Author")
	x.SetGPS(37.7749, -122.4194)
	for _, kw := range []string{"nature", "landscape", "sunset", "mountains", "benchmark"} {
		x.AddKeyword(kw)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Encode(x)
	}
}

// BenchmarkKeywords measures Keywords() on an XMP with 10 keywords.
// The hot path is the single-pass strings.IndexByte scan.
func BenchmarkKeywords(b *testing.B) {
	x := &XMP{Properties: make(map[string]map[string]string)}
	for _, kw := range []string{
		"nature", "landscape", "sunset", "mountains", "outdoors",
		"travel", "adventure", "photography", "wildlife", "benchmark",
	} {
		x.AddKeyword(kw)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = x.Keywords()
	}
}

// BenchmarkAddKeyword measures the cost of AddKeyword() for a sequence of
// keyword appends. Each iteration builds up the keyword list from scratch to
// measure the steady-state strings.Builder allocation pattern.
func BenchmarkAddKeyword(b *testing.B) {
	keywords := []string{
		"nature", "landscape", "sunset", "mountains", "outdoors",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		x := &XMP{Properties: make(map[string]map[string]string)}
		for _, kw := range keywords {
			x.AddKeyword(kw)
		}
	}
}

// BenchmarkGPSParse measures parseXMPGPS() for the decimal-minutes format.
// This is the most common GPS format used by camera manufacturers.
func BenchmarkGPSParse(b *testing.B) {
	inputs := []string{
		"37,46.494N",
		"122,25.164W",
		"51,30.000N",
		"0,7.400W",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		_, _ = parseXMPGPS(inputs[i%len(inputs)])
	}
}

// BenchmarkGPSEncode measures SetGPS() — the strconv.AppendFloat path.
func BenchmarkGPSEncode(b *testing.B) {
	x := &XMP{Properties: make(map[string]map[string]string)}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		x.SetGPS(37.7749295, -122.4194155)
	}
}

// BenchmarkEntityDecode measures unescapeXML for a string containing all five
// predefined XML entities. This exercises the decodeEntity switch path.
func BenchmarkEntityDecode(b *testing.B) {
	input := []byte("Tom &amp; Jerry &lt;show&gt; &quot;fun&quot; &apos;times&apos;")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = unescapeXML(input)
	}
}

// BenchmarkPacketScan measures Scan() on a realistic-sized XMP packet.
func BenchmarkPacketScan(b *testing.B) {
	// Embed the representative XMP inside a larger byte slice to simulate
	// a JPEG APP1 segment with leading non-XMP bytes.
	prefix := strings.Repeat("x", 256)
	suffix := strings.Repeat("x", 256)
	data := []byte(prefix + representativeXMP + suffix)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = Scan(data)
	}
}
