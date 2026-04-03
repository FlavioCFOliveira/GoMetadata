package xmp

import "testing"

// FuzzParseXMP exercises the XMP parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...
func FuzzParseXMP(f *testing.F) {
	f.Add([]byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`))
	f.Add([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`))

	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = Parse(b)
	})
}
