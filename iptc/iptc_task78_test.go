package iptc

// Regression test for task #78 — "decode cache keyed on value bytes only,
// not the isUTF8 flag; reading under ISO-8859-1 then upgrading via AddKeyword
// left stale cached values".
//
// Assessment: MOOT after task #60 (eager pre-decode in Parse) + task #63
// (setUTF8IfNeeded in write-path helpers).
//
// Reasoning:
//   - At Parse time the UTF-8 flag is determined from the 1:90 dataset.
//   - After the full scan, setDecodedValue is called on every Dataset using
//     the final flag value (utf8 parameter). This is the ONLY point at which
//     decodedValue is written for Parse-time datasets.
//   - Read accessors (Caption, Keywords, etc.) return decodedValue directly —
//     they never write to it and never re-decode at read time.
//   - When AddKeyword/AddCreator/setRecord2 is called with a non-ASCII value,
//     setUTF8IfNeeded sets the internal UTF-8 flag, and the NEW dataset's
//     decodedValue is set with isUTF8=true (the correct value for a Go string).
//   - The previously-parsed datasets had their bytes decoded at Parse time with
//     the charset that was declared in the stream (ISO-8859-1 when 1:90 is
//     absent). That decode was correct: ISO-8859-1 bytes → correct UTF-8 string.
//     No re-decode is needed or desirable — re-decoding with isUTF8=true would
//     be wrong (it would return the raw ISO-8859-1 byte sequence as-is, which
//     is not valid UTF-8 for bytes > 0x7F).
//   - Therefore the flag upgrade from false→true does NOT produce stale values
//     for existing datasets. The old lazy-cache design (task #60 race) had the
//     additional defect that the cache key was (value bytes), not (value bytes,
//     isUTF8), so a flag flip could have triggered a re-decode under the wrong
//     charset. The eager pre-decode eliminates that class of bug entirely.
//
// The test below is a PINNING test: it documents the correct behaviour and
// prevents future regressions.

import (
	"testing"
	"unicode/utf8"
)

// TestDecodeCacheInvalidatedOnUTF8Upgrade is the regression/pinning test for
// task #78.
//
// Scenario:
//  1. Parse a stream with an ISO-8859-1 Caption and no 1:90 UTF-8 declaration.
//  2. Read Caption — must equal the correctly ISO-8859-1-decoded UTF-8 string.
//  3. Call AddKeyword with a non-ASCII value — this flips the internal UTF-8 flag
//     via setUTF8IfNeeded.
//  4. Read Caption again — must still equal the same correct string (no stale
//     value, no re-decode under the wrong charset).
//  5. Read the new keyword — must equal the original Go string (UTF-8, correct).
//
// The test is labelled "PINNING" because the bug (task #78) is moot: the eager
// pre-decode introduced in task #60 removed the lazy decode cache entirely, and
// task #63 ensured write-path helpers always decode with isUTF8=true. There is
// no execution path that could produce stale values.
func TestDecodeCacheInvalidatedOnUTF8Upgrade(t *testing.T) {
	t.Parallel()

	// Build a stream: Caption "café" in ISO-8859-1 (0x63 0x61 0x66 0xE9).
	// No 1:90 declaration → Parse decodes with isUTF8=false → ISO-8859-1 decoder
	// converts 0xE9 → 'é' → decodedValue = "café" (correct UTF-8).
	const (
		isoCaption = "\x63\x61\x66\xE9" // "café" in ISO-8859-1
	)

	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Caption, []byte(isoCaption)},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Step 2: read Caption before any write-path mutation.
	captionBefore := i.Caption()
	const wantCaption = "café"
	if captionBefore != wantCaption {
		t.Errorf("Caption before AddKeyword = %q, want %q", captionBefore, wantCaption)
	}
	if !utf8.ValidString(captionBefore) {
		t.Errorf("Caption before AddKeyword is not valid UTF-8: %q", captionBefore)
	}

	// UTF-8 flag must be unset before any write — the stream had no 1:90.
	if i.isUTF8() {
		t.Error("isUTF8() = true before AddKeyword, want false (no 1:90 declaration in stream)")
	}

	// Step 3: AddKeyword with a non-ASCII value — must flip the UTF-8 flag.
	const newKw = "naïf"
	i.AddKeyword(newKw)

	// UTF-8 flag must now be set.
	if !i.isUTF8() {
		t.Error("isUTF8() = false after AddKeyword(non-ASCII), want true")
	}

	// Step 4: read Caption again — must be unchanged (pinning: no stale value).
	captionAfter := i.Caption()
	if captionAfter != wantCaption {
		t.Errorf("Caption after AddKeyword = %q, want %q (stale-value regression, task #78)", captionAfter, wantCaption)
	}
	if !utf8.ValidString(captionAfter) {
		t.Errorf("Caption after AddKeyword is not valid UTF-8: %q", captionAfter)
	}

	// Step 5: the new keyword must be exactly the Go string supplied.
	kws := i.Keywords()
	if len(kws) != 1 {
		t.Fatalf("Keywords: got %d entries, want 1", len(kws))
	}
	if kws[0] != newKw {
		t.Errorf("Keywords[0] = %q, want %q", kws[0], newKw)
	}
	if !utf8.ValidString(kws[0]) {
		t.Errorf("Keywords[0] is not valid UTF-8: %q", kws[0])
	}
}

// TestDecodeCacheInvalidatedOnUTF8UpgradeAddCreator mirrors the scenario
// above but uses AddCreator instead of AddKeyword, and exercises the Caption +
// Creator accessors together. Covers the full set of write-path helpers that
// call setUTF8IfNeeded.
func TestDecodeCacheInvalidatedOnUTF8UpgradeAddCreator(t *testing.T) {
	t.Parallel()

	// Stream: Caption "naïf" in ISO-8859-1 (0x6E 0x61 0xEF 0x66),
	//         Copyright "© 2024" in ISO-8859-1 (0xA9 0x20 0x32 0x30 0x32 0x34).
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Caption, []byte{0x6E, 0x61, 0xEF, 0x66}},                     // "naïf" ISO-8859-1
		{2, DS2CopyrightNotice, []byte{0xA9, 0x20, 0x32, 0x30, 0x32, 0x34}}, // "© 2024" ISO-8859-1
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Before mutation: correct ISO-8859-1 decoded values.
	if got := i.Caption(); got != "naïf" {
		t.Errorf("Caption before AddCreator = %q, want %q", got, "naïf")
	}
	if got := i.Copyright(); got != "© 2024" {
		t.Errorf("Copyright before AddCreator = %q, want %q", got, "© 2024")
	}

	// Flip the UTF-8 flag via AddCreator.
	i.AddCreator("María García")

	if !i.isUTF8() {
		t.Error("isUTF8() = false after AddCreator(non-ASCII), want true")
	}

	// Previously decoded datasets must remain correct (pinning: no stale values).
	if got := i.Caption(); got != "naïf" {
		t.Errorf("Caption after AddCreator = %q, want %q (stale-value regression, task #78)", got, "naïf")
	}
	if got := i.Copyright(); got != "© 2024" {
		t.Errorf("Copyright after AddCreator = %q, want %q (stale-value regression, task #78)", got, "© 2024")
	}

	// The new creator must round-trip correctly.
	creators := i.AllCreators()
	if len(creators) != 1 {
		t.Fatalf("AllCreators: got %d, want 1", len(creators))
	}
	if creators[0] != "María García" {
		t.Errorf("AllCreators[0] = %q, want %q", creators[0], "María García")
	}
}
