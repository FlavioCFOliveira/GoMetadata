package iptc

import (
	"sync"
	"testing"
)

// TestIPTCConcurrentRead is the regression test for task #60.
//
// Before the fix, (*Dataset).stringValue() performed an unsynchronised
// read-check-then-write sequence on d.decoded and d.decodedValue. Any two
// goroutines calling read accessors on the same *IPTC concurrently raced on
// those fields. go test -race reported a write conflict.
//
// After the fix (eager pre-decode in Parse), the decodedValue fields are set
// before Parse returns and are never written again by read accessors. Reads
// from multiple goroutines are therefore trivially race-free.
//
// This test:
//  1. Parses an IPTC stream with ISO-8859-1 keywords (non-ASCII bytes).
//  2. Spins 16 goroutines, each calling Keywords(), Caption(), and Copyright()
//     for 1000 iterations.
//  3. Asserts that every goroutine observes the same correct values.
//  4. Must pass `go test -race -count=1 ./iptc/...` with zero race violations.
func TestIPTCConcurrentRead(t *testing.T) {
	t.Parallel()

	// Build a stream with ISO-8859-1 keywords and a caption.
	// Keywords:
	//   "été"   in ISO-8859-1: 0xE9 0x74 0xE9
	//   "naïf"  in ISO-8859-1: 0x6E 0x61 0xEF 0x66
	//   "Küste" in ISO-8859-1: 0x4B 0xFC 0x73 0x74 0x65
	// Caption: "café"  in ISO-8859-1: 0x63 0x61 0x66 0xE9
	// Copyright: ASCII (no decode needed)
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Keywords, []byte{0xE9, 0x74, 0xE9}},             // "été" in ISO-8859-1
		{2, DS2Keywords, []byte{0x6E, 0x61, 0xEF, 0x66}},       // "naïf" in ISO-8859-1
		{2, DS2Keywords, []byte{0x4B, 0xFC, 0x73, 0x74, 0x65}}, // "Küste" in ISO-8859-1
		{2, DS2Caption, []byte{0x63, 0x61, 0x66, 0xE9}},        // "café" in ISO-8859-1
		{2, DS2CopyrightNotice, []byte("(c) Test Corp")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Verify correctness of decoded values before the concurrency test.
	wantKws := []string{"été", "naïf", "Küste"}
	wantCaption := "café"
	wantCopyright := "(c) Test Corp"

	kws := i.Keywords()
	if len(kws) != len(wantKws) {
		t.Fatalf("pre-check Keywords: got %d, want %d", len(kws), len(wantKws))
	}
	for j, want := range wantKws {
		if kws[j] != want {
			t.Errorf("pre-check Keywords[%d]: got %q, want %q", j, kws[j], want)
		}
	}
	if got := i.Caption(); got != wantCaption {
		t.Errorf("pre-check Caption: got %q, want %q", got, wantCaption)
	}
	if got := i.Copyright(); got != wantCopyright {
		t.Errorf("pre-check Copyright: got %q, want %q", got, wantCopyright)
	}

	const (
		goroutines = 16
		iterations = 1000
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range iterations {
				kws := i.Keywords()
				if len(kws) != len(wantKws) {
					t.Errorf("goroutine %d: Keywords len = %d, want %d", id, len(kws), len(wantKws))
					return
				}
				for j, want := range wantKws {
					if kws[j] != want {
						t.Errorf("goroutine %d: Keywords[%d] = %q, want %q", id, j, kws[j], want)
						return
					}
				}
				if got := i.Caption(); got != wantCaption {
					t.Errorf("goroutine %d: Caption = %q, want %q", id, got, wantCaption)
					return
				}
				if got := i.Copyright(); got != wantCopyright {
					t.Errorf("goroutine %d: Copyright = %q, want %q", id, got, wantCopyright)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestConcurrentKeywordsRead is the second regression test for task #60.
//
// This variant focuses narrowly on Keywords(), which iterates Record-2 and
// calls stringValue() on each keyword Dataset — the exact hot path that races.
// 16 goroutines call Keywords() for 1000 iterations each. Must pass -race.
func TestConcurrentKeywordsRead(t *testing.T) {
	t.Parallel()

	// Build an IPTC stream: 3 keywords with non-ASCII ISO-8859-1 bytes.
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Keywords, []byte{0xE9, 0x74, 0xE9}},             // "été"
		{2, DS2Keywords, []byte{0x6E, 0x61, 0xEF, 0x66}},       // "naïf"
		{2, DS2Keywords, []byte{0x4B, 0xFC, 0x73, 0x74, 0x65}}, // "Küste"
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantKws := []string{"été", "naïf", "Küste"}
	const (
		goroutines = 16
		iterations = 1000
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range iterations {
				kws := i.Keywords()
				if len(kws) != len(wantKws) {
					t.Errorf("goroutine %d: Keywords len = %d, want %d", id, len(kws), len(wantKws))
					return
				}
				for j, want := range wantKws {
					if kws[j] != want {
						t.Errorf("goroutine %d: Keywords[%d] = %q, want %q", id, j, kws[j], want)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()
}

// BenchmarkIPTCAccessorsNonASCII benchmarks Caption/Copyright/Keywords reads on
// a non-ASCII ISO-8859-1 stream. Used to measure the before/after cost of
// removing the lazy decode cache in favour of eager pre-decode in Parse.
func BenchmarkIPTCAccessorsNonASCII(b *testing.B) {
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		// ISO-8859-1 bytes — exercises the non-UTF-8 decode path.
		{2, DS2CopyrightNotice, []byte{0x28, 0x63, 0x29, 0x20, 0x54, 0x65, 0x73, 0x74}}, // "(c) Test" ASCII
		{2, DS2Caption, []byte{0x63, 0x61, 0x66, 0xE9}},                                 // "café" ISO-8859-1
		{2, DS2Keywords, []byte{0xE9, 0x74, 0xE9}},                                      // "été"
		{2, DS2Keywords, []byte{0x4B, 0xFC, 0x73, 0x74, 0x65}},                          // "Küste"
	})
	i, err := Parse(raw)
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = i.Caption()
		_ = i.Copyright()
		_ = i.Keywords()
	}
}
