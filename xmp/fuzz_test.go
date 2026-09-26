package xmp

import "testing"

// fuzzArenaRatioBound bounds len(x.arena) minus the struct-in-list-key
// contribution (unbounded by design, tracked in #277 — see
// structInListKeyBytes), relative to len(input), for every successfully
// parsed document. Every other path measures below 1x; 8x leaves large
// headroom while still catching a regression in those paths.
const fuzzArenaRatioBound = 8

// structInListKeyBytes sums the byte length (key + value) of every
// struct-in-list property entry in x.Properties, identified via
// parseStructKey's isListStruct classification.
func structInListKeyBytes(x *XMP) int {
	total := 0
	for _, m := range x.Properties {
		for key, val := range m {
			if _, _, isListStruct, _ := parseStructKey(key); isListStruct {
				total += len(key) + len(val)
			}
		}
	}
	return total
}

// FuzzParseXMP exercises the XMP parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...
func FuzzParseXMP(f *testing.F) {
	f.Add([]byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"></x:xmpmeta><?xpacket end="w"?>`))
	f.Add([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`))

	f.Fuzz(func(t *testing.T, b []byte) {
		x, err := Parse(b)
		if err != nil || x == nil {
			return
		}
		nonStructInList := len(x.arena) - structInListKeyBytes(x)
		if nonStructInList < 0 {
			nonStructInList = 0
		}
		if limit := fuzzArenaRatioBound * len(b); nonStructInList > limit {
			t.Fatalf("XMP.arena (excluding struct-in-list keys, #277) grew to %d bytes for a %d-byte input (%.2fx, limit %dx)",
				nonStructInList, len(b), float64(nonStructInList)/float64(len(b)), fuzzArenaRatioBound)
		}
	})
}
