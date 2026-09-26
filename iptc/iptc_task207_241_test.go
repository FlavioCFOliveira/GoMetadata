package iptc

// iptc_task207_241_test.go — regression and benchmark coverage for sprint 44
// (Performance and Efficiency Laboratory) tasks:
//
//   - #207: Encode skips the defensive slices.Clone + sort when a record's
//     datasets are already in ascending DataSet order, and encBufPool.Put is
//     guarded by a capacity cap mirroring internal/iobuf's discard policy.
//   - #241: Parse pre-sizes every non-empty i.Records[record] slice to its
//     exact final capacity via an allocation-free pre-count pass
//     (preCountDatasets), instead of growing via repeated append.

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// #241: Parse pre-sizing.
// ---------------------------------------------------------------------------

// buildIPTCKeywords returns a raw IIM stream containing n repeatable 2:25
// (Keywords) datasets, each with a distinct short value.
func buildIPTCKeywords(n int) []byte {
	records := make([]struct {
		rec uint8
		ds  uint8
		val []byte
	}, n)
	for idx := range records {
		records[idx] = struct {
			rec uint8
			ds  uint8
			val []byte
		}{2, DS2Keywords, []byte(fmt.Sprintf("kw%d", idx))}
	}
	return buildIPTC(records)
}

// TestParsePreSizesRecord2Capacity locks in task #241's exact-capacity claim:
// for both a small (<=5 datasets) and a larger (>12 datasets) record 2, the
// slice Parse returns must have cap() exactly equal to the number of
// datasets stored — proving preCountDatasets neither under-counts (which
// would force a reallocating regrowth) nor grossly over-counts (which would
// waste memory).
func TestParsePreSizesRecord2Capacity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		count int
	}{
		{"five_or_fewer", 5},
		{"more_than_twelve", 15},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := buildIPTCKeywords(tc.count)
			i, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := len(i.Records[2]); got != tc.count {
				t.Fatalf("len(Records[2]) = %d, want %d", got, tc.count)
			}
			if got := cap(i.Records[2]); got != tc.count {
				t.Errorf("cap(Records[2]) = %d, want exactly %d (task #241: exact pre-sized capacity, no regrowth, no over-allocation)", got, tc.count)
			}
		})
	}
}

// TestPreCountDatasetsMatchesActualStoredCount cross-checks preCountDatasets
// against Parse's own storage decisions across a mix of record numbers,
// including the non-storing 1:90 and 1:00/2:00 datasets, to verify the two
// functions agree exactly (not just as a safe upper bound) for well-formed
// input.
func TestPreCountDatasetsMatchesActualStoredCount(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{1, DS1CodedCharacterSet, []byte{0x1B, 0x25, 0x47}},  // 1:90 — not stored
		{1, DS1EnvelopeRecordVersion, []byte{0x00, 0x04}},    // 1:00 — not stored
		{2, DS2ApplicationRecordVersion, []byte{0x00, 0x04}}, // 2:00 — not stored
		{2, DS2Caption, []byte("caption")},
		{2, DS2CopyrightNotice, []byte("(c) Test")},
		{2, DS2Keywords, []byte("alpha")},
		{2, DS2Keywords, []byte("beta")},
		{3, DS2Caption, []byte("record 3 unused dataset id, just needs a valid record number")},
	})
	counts := preCountDatasets(raw)
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for rec := 1; rec < len(i.Records); rec++ {
		if got, want := len(i.Records[rec]), counts[rec]; got != want {
			t.Errorf("record %d: len(Records[%d]) = %d, preCountDatasets predicted %d", rec, rec, got, want)
		}
	}
}

// BenchmarkIPTCParseFewDatasets measures Parse on a stream with 5 datasets
// (all in record 2) — the "<=5 datasets" case cited by task #241's AC.
func BenchmarkIPTCParseFewDatasets(b *testing.B) {
	raw := buildIPTCKeywords(5)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(raw)
	}
}

// BenchmarkIPTCParseManyDatasets measures Parse on a stream with 20 datasets
// (all in record 2) — the ">12 datasets" case cited by task #241's AC, large
// enough that the former fixed cap of 12 would have forced at least one
// append-triggered regrowth.
func BenchmarkIPTCParseManyDatasets(b *testing.B) {
	raw := buildIPTCKeywords(20)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(raw)
	}
}

// BenchmarkIPTCParseUTF8Declared measures Parse on a stream that carries a
// 1:90 UTF-8 declaration ahead of the Record 2 datasets — the ordering IIM
// places no constraint on, and the reason Parse's eager-decode pass runs
// after the full scan rather than inline.
func BenchmarkIPTCParseUTF8Declared(b *testing.B) {
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{1, DS1CodedCharacterSet, []byte{0x1B, 0x25, 0x47}},
		{2, DS2CopyrightNotice, []byte("Test Corp")},
		{2, DS2Caption, []byte("café société — UTF-8 caption")},
	})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(raw)
	}
}

// ---------------------------------------------------------------------------
// #207: Encode sorted-input fast path.
// ---------------------------------------------------------------------------

// TestEncodeAlreadySortedInputPreservesOrder verifies that when every
// record's datasets are already in ascending DataSet-number order (including
// repeatable datasets sharing the same number, e.g. multiple Keywords), the
// sorted-input fast path (which iterates the original slice read-only,
// skipping slices.Clone + slices.SortStableFunc) produces output whose
// round-tripped order is identical to what the always-clone-and-sort path
// produced before task #207.
func TestEncodeAlreadySortedInputPreservesOrder(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		// Already ascending by DataSet number within record 2.
		{2, DS2Keywords, []byte("alpha")},
		{2, DS2Keywords, []byte("beta")},
		{2, DS2Keywords, []byte("gamma")},
		{2, DS2CopyrightNotice, []byte("Test Corp")},
		{2, DS2Caption, []byte("A sorted caption")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	encoded, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	i2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}

	wantKeywords := []string{"alpha", "beta", "gamma"}
	gotKeywords := i2.Keywords()
	if len(gotKeywords) != len(wantKeywords) {
		t.Fatalf("Keywords() round-trip len = %d, want %d", len(gotKeywords), len(wantKeywords))
	}
	for idx, want := range wantKeywords {
		if gotKeywords[idx] != want {
			t.Errorf("Keywords()[%d] = %q, want %q (order must be preserved for already-sorted ties)", idx, gotKeywords[idx], want)
		}
	}
	if got := i2.Copyright(); got != "Test Corp" {
		t.Errorf("Copyright() = %q, want %q", got, "Test Corp")
	}
	if got := i2.Caption(); got != "A sorted caption" {
		t.Errorf("Caption() = %q, want %q", got, "A sorted caption")
	}
}

// BenchmarkIPTCEncodeSorted measures Encode's cost when every record's
// datasets are already in ascending DataSet-number order — the input shape
// task #207 optimises for by skipping slices.Clone + slices.SortStableFunc
// entirely and iterating the receiver's own slice read-only.
func BenchmarkIPTCEncodeSorted(b *testing.B) {
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Keywords, []byte("benchmark")},
		{2, DS2Keywords, []byte("performance")},
		{2, DS2CopyrightNotice, []byte("Test Corp")},
		{2, DS2Caption, []byte("A test image caption for benchmarking purposes")},
	})
	i, err := Parse(raw)
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Encode(i)
	}
}
