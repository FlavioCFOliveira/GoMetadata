package iptc

// Regression tests for task #63 (SetKeywords UTF-8 flag) and task #71
// (zero-length dataset allocation bomb).

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Task #63 — SetKeywords omits UTF-8 flag → mojibake on in-memory read-back
// ---------------------------------------------------------------------------

// TestSetKeywordsNonASCIIInMemoryRead is the primary regression test for
// task #63. It verifies that calling SetKeywords with non-ASCII strings and
// then reading back via Keywords() returns correct UTF-8 values WITHOUT an
// Encode/Parse round-trip (which would have accidentally rescued the bug via
// Encode's needsUTF8Declaration path).
//
// Before fix: Keywords()[0] returned "cafÃ©" (ISO-8859-1 mis-decode of UTF-8
// bytes 0xC3 0xA9) because SetKeywords did not set Records[0] (the UTF-8 flag)
// and decodedValue was stored with isUTF8=false → decodeString ran the
// ISO-8859-1 decoder on already-valid UTF-8 bytes.
//
// After fix: setUTF8IfNeeded is called for each keyword value; when any value
// contains bytes > 0x7F the flag is set before decodedValue is computed, so
// the accessor returns the original string unchanged.
func TestSetKeywordsNonASCIIInMemoryRead(t *testing.T) {
	t.Parallel()

	i := new(IPTC)
	i.SetKeywords([]string{"café", "naïve"})

	kws := i.Keywords()
	if len(kws) != 2 {
		t.Fatalf("Keywords: got %d entries, want 2", len(kws))
	}

	// "café" must come back as "café", not "cafÃ©" (the ISO-8859-1 mis-decode).
	if kws[0] != "café" {
		t.Errorf("Keywords[0] = %q, want %q (mojibake regression)", kws[0], "café")
	}
	if kws[1] != "naïve" {
		t.Errorf("Keywords[1] = %q, want %q (mojibake regression)", kws[1], "naïve")
	}

	// Additional sanity: values must be valid UTF-8.
	for idx, kw := range kws {
		if !utf8.ValidString(kw) {
			t.Errorf("Keywords[%d] = %q is not valid UTF-8", idx, kw)
		}
	}
}

// TestSetKeywordsNonASCIIUTF8Flag verifies:
//  1. isUTF8() returns true after SetKeywords is called with non-ASCII keywords.
//  2. Keywords() returns the exact original strings (no mojibake).
//  3. Each returned string is valid UTF-8.
//
// Complements TestSetKeywordsNonASCIIInMemoryRead by asserting the internal
// flag state that drives the read-back decode path.
func TestSetKeywordsNonASCIIUTF8Flag(t *testing.T) {
	t.Parallel()

	i := new(IPTC)
	i.SetKeywords([]string{"αβγ", "日本語"})

	// The UTF-8 flag must be set so that Keywords() uses string(value) directly.
	if !i.isUTF8() {
		t.Error("isUTF8() = false after SetKeywords with non-ASCII keywords, want true")
	}

	kws := i.Keywords()
	if len(kws) != 2 {
		t.Fatalf("Keywords: got %d entries, want 2", len(kws))
	}
	if kws[0] != "αβγ" {
		t.Errorf("Keywords[0] = %q, want %q", kws[0], "αβγ")
	}
	if kws[1] != "日本語" {
		t.Errorf("Keywords[1] = %q, want %q", kws[1], "日本語")
	}

	// Each value must be non-empty and valid UTF-8.
	for idx, kw := range kws {
		if len(kw) == 0 {
			t.Errorf("Keywords[%d] is empty", idx)
		}
		if !utf8.ValidString(kw) {
			t.Errorf("Keywords[%d] = %q is not valid UTF-8", idx, kw)
		}
	}
}

// TestSetKeywordsASCIINoUTF8Flag confirms that setting only ASCII keywords
// does NOT spuriously set the UTF-8 flag. This guards against over-triggering.
func TestSetKeywordsASCIINoUTF8Flag(t *testing.T) {
	t.Parallel()

	i := new(IPTC)
	i.SetKeywords([]string{"nature", "landscape", "sunset"})

	// ASCII-only keywords must not set the UTF-8 flag.
	if i.isUTF8() {
		t.Error("isUTF8() = true after ASCII-only SetKeywords, want false")
	}

	kws := i.Keywords()
	if len(kws) != 3 {
		t.Fatalf("Keywords: got %d entries, want 3", len(kws))
	}
	if kws[0] != "nature" || kws[1] != "landscape" || kws[2] != "sunset" {
		t.Errorf("Keywords = %v, want [nature landscape sunset]", kws)
	}
}

// ---------------------------------------------------------------------------
// Task #71 — zero-length dataset allocation bomb
// ---------------------------------------------------------------------------

// TestIPTCZeroLengthDatasetBomb is the regression test for task #71.
//
// Before fix: a stream of N zero-length datasets (5 bytes each on wire)
// contributed 0 to totalBytes, so maxIPTCTotalBytes never fired, and N Dataset
// structs (~67 bytes each) were allocated without limit. 1,000,000 datasets =
// ~65 MB heap for a 5 MB input stream (13× amplification).
//
// After fix: storeDataset tracks a dataset count separate from the byte
// aggregate; once maxIPTCDatasets (65536) is reached, parsing stops with
// Truncated=true. The total heap from struct allocations is bounded at ~4 MiB.
//
// The test uses 1,000,000 zero-length Caption entries (5 MB input) and
// asserts that the resulting IPTC contains at most maxIPTCDatasets entries and
// Truncated is true. It must complete without OOM.
func TestIPTCZeroLengthDatasetBomb(t *testing.T) {
	t.Parallel()

	const numDatasets = 1_000_000

	// Each dataset: 0x1C (marker) + 0x02 (rec 2) + 0x78 (DS 120, Caption) +
	// 0x00 0x00 (length = 0). No value bytes follow.
	var buf bytes.Buffer
	buf.Grow(numDatasets * 5)
	entry := []byte{0x1C, 0x02, 0x78, 0x00, 0x00}
	for range numDatasets {
		buf.Write(entry)
	}

	result, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Parse returned nil *IPTC")
	}

	total := 0
	for rec := range result.Records {
		total += len(result.Records[rec])
	}

	if total > maxIPTCDatasets {
		t.Errorf("total stored datasets = %d, want <= %d (DoS cap not enforced)", total, maxIPTCDatasets)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false after dataset bomb, want true")
	}
}
