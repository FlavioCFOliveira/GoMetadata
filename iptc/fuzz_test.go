package iptc

import "testing"

// FuzzParseIPTC exercises the IPTC parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseIPTC -fuzztime=60s ./iptc/...
//
// Task #241 invariant: preCountDatasets must never under-count the number of
// Dataset structs Parse actually stores in a given record (over-counting is
// safe; under-counting would force Parse's make([]Dataset, 0, counts[rec])
// to regrow via append, which is still correct but defeats the whole point of
// pre-sizing). The additional assertions below fail loudly the moment any
// future edit to Parse's scanner or storeDataset's skip logic drifts out of
// sync with preCountDatasets's mirrored copy of that logic.
func FuzzParseIPTC(f *testing.F) {
	// Seed: minimal IPTC marker (0x1C) + record 2, dataset 120, length 0.
	f.Add([]byte{0x1C, 0x02, 0x78, 0x00, 0x00})

	f.Fuzz(func(t *testing.T, b []byte) {
		counts := preCountDatasets(b)
		i, err := Parse(b)
		if err != nil {
			// Parse's documented contract (see its doc comment) is to always
			// return a nil error; a non-nil error here is a contract violation,
			// not an expected fuzz finding.
			t.Fatalf("Parse returned non-nil error: %v", err)
		}
		for rec := 1; rec < len(i.Records); rec++ {
			got := len(i.Records[rec])
			want := counts[rec]
			if got > want {
				t.Fatalf("record %d: Parse stored %d datasets but preCountDatasets predicted only %d (under-count)", rec, got, want)
			}
			// No regrowth: Parse pre-sizes non-empty records to exactly
			// counts[rec] via make([]Dataset, 0, counts[rec]). As long as
			// preCountDatasets did not under-count (checked above), cap()
			// must still equal that original request — growslice never
			// triggered.
			if want > 0 && cap(i.Records[rec]) != want {
				t.Fatalf("record %d: cap=%d, want exactly %d (preCountDatasets under-counted, forcing regrowth)", rec, cap(i.Records[rec]), want)
			}
		}
	})
}
