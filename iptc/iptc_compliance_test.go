package iptc

// Compliance tests for roadmap issues #17, #18, #19, #20.
// Each test group is annotated with the issue it validates.

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// #17 — Field-length enforcement
// ---------------------------------------------------------------------------

// TestTruncateToLimit verifies the UTF-8-safe truncation helper directly.
func TestTruncateToLimit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		limit  int
		want   string
		wantOK bool // result must be valid UTF-8
	}{
		{"ascii-within-limit", "hello", 10, "hello", true},
		{"ascii-at-limit", "hello", 5, "hello", true},
		{"ascii-over-limit", "hello world", 5, "hello", true},
		{"zero-limit-no-truncation", "hello", 0, "hello", true},
		// "café" in UTF-8 is 5 bytes: c a f 0xC3 0xA9
		// Limit=4 must not cut the 2-byte rune 0xC3 0xA9 → result is "caf" (3 bytes).
		{"utf8-cut-mid-rune", "café", 4, "caf", true},
		{"utf8-exact-rune-boundary", "café", 5, "café", true},
		{"utf8-well-within", "café", 10, "café", true},
		// 3-byte rune: "€" = 0xE2 0x82 0xAC; limit 2 cuts mid-rune → empty
		{"utf8-three-byte-rune-mid-cut", "€10", 2, "", true},
		// limit 3 lands exactly on the euro sign boundary
		{"utf8-three-byte-rune-exact", "€10", 3, "€", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateToLimit([]byte(tc.input), tc.limit)
			if string(got) != tc.want {
				t.Errorf("truncateToLimit(%q, %d) = %q, want %q", tc.input, tc.limit, got, tc.want)
			}
			if tc.wantOK && !utf8.Valid(got) {
				t.Errorf("truncateToLimit(%q, %d) = %q: not valid UTF-8", tc.input, tc.limit, got)
			}
		})
	}
}

// TestFieldLengthEnforcementSetCaption verifies that SetCaption truncates at
// the IIM §2.2.29 limit of 2000 bytes without cutting a UTF-8 rune.
func TestFieldLengthEnforcementSetCaption(t *testing.T) {
	t.Parallel()
	// 2001 ASCII bytes — should become 2000.
	long := strings.Repeat("a", 2001)
	i := new(IPTC)
	i.SetCaption(long)
	got := i.Caption()
	if len(got) != 2000 {
		t.Errorf("SetCaption: len = %d, want 2000", len(got))
	}

	// Exactly at limit: no truncation.
	exact := strings.Repeat("b", 2000)
	i.SetCaption(exact)
	if len(i.Caption()) != 2000 {
		t.Errorf("SetCaption at limit: len = %d, want 2000", len(i.Caption()))
	}

	// Below limit: unchanged.
	short := "short caption"
	i.SetCaption(short)
	if i.Caption() != short {
		t.Errorf("SetCaption below limit: got %q, want %q", i.Caption(), short)
	}
}

// TestFieldLengthEnforcementSetCopyright verifies that SetCopyright truncates
// at the IIM §2.2.28 limit of 128 bytes.
func TestFieldLengthEnforcementSetCopyright(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 200)
	i := new(IPTC)
	i.SetCopyright(long)
	if len(i.Copyright()) != 128 {
		t.Errorf("SetCopyright: len = %d, want 128", len(i.Copyright()))
	}
}

// TestFieldLengthEnforcementSetCreator verifies that SetCreator truncates at
// the IIM §2.2.25 limit of 32 bytes.
func TestFieldLengthEnforcementSetCreator(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("z", 50)
	i := new(IPTC)
	i.SetCreator(long)
	if len(i.Creator()) != 32 {
		t.Errorf("SetCreator: len = %d, want 32", len(i.Creator()))
	}
}

// TestFieldLengthEnforcementAddKeyword verifies that AddKeyword truncates each
// keyword at the IIM §2.2.17 limit of 64 bytes.
func TestFieldLengthEnforcementAddKeyword(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("k", 100)
	i := new(IPTC)
	i.AddKeyword(long)
	i.AddKeyword("short")
	kws := i.Keywords()
	if len(kws) != 2 {
		t.Fatalf("AddKeyword: got %d keywords, want 2", len(kws))
	}
	if len(kws[0]) != 64 {
		t.Errorf("AddKeyword long: len = %d, want 64", len(kws[0]))
	}
	if kws[1] != "short" {
		t.Errorf("AddKeyword short: got %q, want %q", kws[1], "short")
	}
}

// TestFieldLengthEnforcementUTF8Boundary verifies that truncation never splits
// a multi-byte UTF-8 rune.
func TestFieldLengthEnforcementUTF8Boundary(t *testing.T) {
	t.Parallel()
	// Build a 130-byte string of 2-byte runes (e.g. 65 × "é" = 130 bytes).
	// The limit for Copyright is 128 bytes = 64 "é" chars.
	s := strings.Repeat("é", 65) // 65 × 2 = 130 bytes
	i := new(IPTC)
	i.SetCopyright(s)
	got := i.Copyright()
	if !utf8.ValidString(got) {
		t.Errorf("SetCopyright: result not valid UTF-8: %q", got)
	}
	if len(got) != 128 {
		t.Errorf("SetCopyright UTF-8 boundary: len = %d, want 128 (64×é)", len(got))
	}
	// Should be exactly 64 "é" runes.
	if got != strings.Repeat("é", 64) {
		t.Errorf("SetCopyright UTF-8 boundary: content mismatch")
	}
}

// TestFieldLengthEnforcementRoundTrip verifies that truncated values survive
// an Encode → Parse round-trip correctly.
func TestFieldLengthEnforcementRoundTrip(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCopyright(strings.Repeat("c", 200)) // truncated to 128
	i.SetCaption(strings.Repeat("d", 3000))  // truncated to 2000

	enc, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	i2, err := Parse(enc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(i2.Copyright()) != 128 {
		t.Errorf("Copyright after round-trip: len = %d, want 128", len(i2.Copyright()))
	}
	if len(i2.Caption()) != 2000 {
		t.Errorf("Caption after round-trip: len = %d, want 2000", len(i2.Caption()))
	}
}

// ---------------------------------------------------------------------------
// #18 — Recoverable parse: continue past malformed datasets
// ---------------------------------------------------------------------------

// TestParseRecoverAfterOversizedDataset verifies that when a dataset declares
// a length > 1 MiB, the parser skips it (sets Truncated=true) and continues
// to recover valid datasets that follow it in the same stream.
func TestParseRecoverAfterOversizedDataset(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	// Dataset 1: Caption with declared length > 1 MiB (2 MiB), no actual data.
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Caption)
	// Extended length: 4-byte length value = 2 MiB (2097152).
	buf.WriteByte(0x84)                       // 0x80 | 0x04 = extended, 4 bytes follow
	buf.WriteByte(0x00)                       // count low
	buf.Write([]byte{0x00, 0x20, 0x00, 0x00}) // 2 MiB

	// Dataset 2: Copyright with a valid, short value — must be recovered.
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2CopyrightNotice)
	buf.WriteByte(0x00)
	buf.WriteByte(0x05)
	buf.WriteString("Alice")

	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if !i.Truncated {
		t.Error("Truncated should be true after oversized dataset")
	}
	// The oversized caption must be absent.
	if got := i.Caption(); got != "" {
		t.Errorf("Caption: expected empty after skip, got %q", got)
	}
	// The valid copyright after the bad dataset must be recovered.
	if got := i.Copyright(); got != "Alice" {
		t.Errorf("Copyright after bad dataset: got %q, want %q", got, "Alice")
	}
}

// TestParseRecoverAfterTruncatedValue verifies that when a dataset's declared
// length extends past the end of the buffer, it is skipped (Truncated=true)
// and the scanner continues re-scanning from the post-header position, so
// valid datasets that happen to be reachable by re-scanning are recovered.
//
// Construction: a Caption header declares 100 bytes of value but the actual
// bytes following the header contain a valid Copyright dataset. The parser
// must skip this Caption (length=100 would require 105 total bytes but the
// buffer is shorter) and then re-scan from newPos. On re-scan it finds 0x1C
// at the start of the embedded Copyright and parses it correctly.
func TestParseRecoverAfterTruncatedValue(t *testing.T) {
	t.Parallel()

	// Build the Copyright dataset bytes we want to recover.
	var copyrightDS bytes.Buffer
	copyrightDS.WriteByte(0x1C)
	copyrightDS.WriteByte(0x02)
	copyrightDS.WriteByte(DS2CopyrightNotice)
	copyrightDS.WriteByte(0x00)
	copyrightDS.WriteByte(0x03)
	copyrightDS.WriteString("Bob")
	copyrightBytes := copyrightDS.Bytes() // 8 bytes

	var buf bytes.Buffer
	// Caption header: declares 100 bytes value. The full dataset would require
	// 5 (header) + 100 (value) = 105 bytes; we provide only 5+8=13 total, so
	// newPos+length = 5+100 = 105 > len(buf) = 13 → skip condition fires.
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Caption)
	buf.WriteByte(0x00)
	buf.WriteByte(0x64) // length = 100
	// The "value" bytes here are actually the Copyright dataset; the scanner
	// will re-find the 0x1C after advancing past the header.
	buf.Write(copyrightBytes)

	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if !i.Truncated {
		t.Error("Truncated should be true after length-exceeds-buffer skip")
	}
	// Caption was skipped (declared length exceeded remaining buffer).
	if got := i.Caption(); got != "" {
		t.Errorf("Caption: expected empty after skip, got %q", got)
	}
	// Copyright was recovered by re-scanning from newPos.
	if got := i.Copyright(); got != "Bob" {
		t.Errorf("Copyright after truncated dataset: got %q, want %q", got, "Bob")
	}
}

// TestParseNoTruncatedFlagOnCleanStream verifies that IPTC.Truncated is false
// for a well-formed stream.
func TestParseNoTruncatedFlagOnCleanStream(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2CopyrightNotice, []byte("Clean Corp")},
		{2, DS2Caption, []byte("All good here")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if i.Truncated {
		t.Error("Truncated should be false for a clean, well-formed stream")
	}
}

// TestParseRecoverMultipleBadDatasets verifies recovery after a sequence of
// multiple malformed datasets interspersed with valid ones.
func TestParseRecoverMultipleBadDatasets(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	// Bad 1: oversized.
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Caption)
	buf.WriteByte(0x84)
	buf.WriteByte(0x00)
	buf.Write([]byte{0x00, 0x20, 0x00, 0x00}) // 2 MiB

	// Good 1: Keywords "alpha".
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Keywords)
	buf.WriteByte(0x00)
	buf.WriteByte(0x05)
	buf.WriteString("alpha")

	// Bad 2: declares 20 bytes, only 5 bytes follow before end.
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Headline)
	buf.WriteByte(0x00)
	buf.WriteByte(0x14) // 20
	buf.WriteString("short")

	// Good 2: Copyright "Corp".
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2CopyrightNotice)
	buf.WriteByte(0x00)
	buf.WriteByte(0x04)
	buf.WriteString("Corp")

	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !i.Truncated {
		t.Error("Truncated should be true")
	}
	kws := i.Keywords()
	if len(kws) != 1 || kws[0] != "alpha" {
		t.Errorf("Keywords after recovery: got %v, want [alpha]", kws)
	}
	if got := i.Copyright(); got != "Corp" {
		t.Errorf("Copyright after recovery: got %q, want %q", got, "Corp")
	}
}

// ---------------------------------------------------------------------------
// #19 — Auto-inject UTF-8 declaration for non-ASCII writes
// ---------------------------------------------------------------------------

// TestNonASCIIWriteRoundTrip verifies that writing a string with non-ASCII
// characters into a fresh *IPTC (no 1:90 declaration) and encoding it
// produces a stream that, when parsed, returns the original string intact
// (no mojibake). Encode must auto-inject the 1:90 declaration.
func TestNonASCIIWriteRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value string
	}{
		{"cafe", "café"},
		{"japanese", "日本語"},
		{"emoji-adjacent", "naïve"},
		{"mixed-ascii-non-ascii", "Photo © 2024"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			i := new(IPTC)
			i.SetCaption(tc.value)

			enc, err := Encode(i)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}

			// Verify that the encoded stream contains the 1:90 ESC % G declaration.
			utf8Decl := []byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47}
			if !bytes.Contains(enc, utf8Decl) {
				t.Errorf("Encode: 1:90 UTF-8 declaration not found in output for %q", tc.value)
			}

			i2, err := Parse(enc)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := i2.Caption(); got != tc.value {
				t.Errorf("Caption round-trip: got %q, want %q", got, tc.value)
			}
		})
	}
}

// TestASCIIOnlyNoDeclarationInjected verifies that Encode does NOT inject
// the 1:90 declaration when all dataset values are pure ASCII.
func TestASCIIOnlyNoDeclarationInjected(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCaption("pure ascii caption")
	i.SetCopyright("(c) Test Corp 2024")
	i.AddKeyword("landscape")

	enc, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	utf8Decl := []byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47}
	if bytes.Contains(enc, utf8Decl) {
		t.Error("Encode: 1:90 declaration unexpectedly injected for ASCII-only content")
	}
}

// TestExistingUTF8DeclarationPreserved verifies that when a parsed stream
// already has the 1:90 declaration, it is preserved on round-trip even if
// all values happen to be ASCII.
func TestExistingUTF8DeclarationPreserved(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47}) // 1:90
	buf.Write([]byte{0x1C, 0x02, DS2Caption, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'})

	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	enc, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	utf8Decl := []byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47}
	if !bytes.Contains(enc, utf8Decl) {
		t.Error("Encode: 1:90 declaration not preserved for stream that originally had it")
	}
	i2, err := Parse(enc)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	if !i2.isUTF8() {
		t.Error("isUTF8() = false after round-trip with existing declaration")
	}
}

// TestLegacyStreamNonASCIIRoundTrip simulates the exact mojibake scenario:
// a fresh *IPTC (legacy stream, no 1:90), SetCaption("café"), encode, re-parse,
// Caption() must return "café" not garbage.
func TestLegacyStreamNonASCIIRoundTrip(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	// No 1:90 declaration — simulates a legacy stream.
	if i.isUTF8() {
		t.Fatal("fresh IPTC must not have UTF-8 flag set")
	}
	i.SetCaption("café")
	enc, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	i2, err := Parse(enc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i2.Caption(); got != "café" {
		t.Errorf("Caption legacy round-trip: got %q, want %q", got, "café")
	}
}

// ---------------------------------------------------------------------------
// #20 — By-line repeatable + date/time accessors
// ---------------------------------------------------------------------------

// TestAllCreatorsMultiple verifies that multiple By-line datasets are all
// returned by AllCreators and that Creator() returns only the first.
func TestAllCreatorsMultiple(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Byline, []byte("Alice")},
		{2, DS2Byline, []byte("Bob")},
		{2, DS2Byline, []byte("Carol")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	all := i.AllCreators()
	if len(all) != 3 {
		t.Fatalf("AllCreators: got %d, want 3", len(all))
	}
	if all[0] != "Alice" || all[1] != "Bob" || all[2] != "Carol" {
		t.Errorf("AllCreators: got %v, want [Alice Bob Carol]", all)
	}
	// Creator() must return only the first.
	if got := i.Creator(); got != "Alice" {
		t.Errorf("Creator: got %q, want %q", got, "Alice")
	}
}

// TestAllCreatorsEmpty verifies that AllCreators returns nil when no By-line
// datasets are present.
func TestAllCreatorsEmpty(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	if all := i.AllCreators(); all != nil {
		t.Errorf("AllCreators on empty IPTC: got %v, want nil", all)
	}
}

// TestAddCreatorAppendsAndTruncates verifies that AddCreator appends to
// existing By-line entries and enforces the 32-byte IIM limit.
func TestAddCreatorAppendsAndTruncates(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCreator("Alice")
	i.AddCreator("Bob")
	i.AddCreator(strings.Repeat("X", 50)) // over limit: truncated to 32

	all := i.AllCreators()
	if len(all) != 3 {
		t.Fatalf("AllCreators: got %d, want 3", len(all))
	}
	if all[0] != "Alice" {
		t.Errorf("AllCreators[0]: got %q, want %q", all[0], "Alice")
	}
	if all[1] != "Bob" {
		t.Errorf("AllCreators[1]: got %q, want %q", all[1], "Bob")
	}
	if len(all[2]) != 32 {
		t.Errorf("AllCreators[2]: len = %d, want 32 (truncated)", len(all[2]))
	}
}

// TestAllCreatorsRoundTrip verifies that multiple By-line entries survive an
// Encode → Parse round-trip without loss or reordering.
func TestAllCreatorsRoundTrip(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.AddCreator("Alice")
	i.AddCreator("Bob")
	i.AddCreator("Carol")
	i.SetCaption("Group shot")

	enc, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	i2, err := Parse(enc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	all := i2.AllCreators()
	if len(all) != 3 {
		t.Fatalf("AllCreators after round-trip: got %d, want 3", len(all))
	}
	if all[0] != "Alice" || all[1] != "Bob" || all[2] != "Carol" {
		t.Errorf("AllCreators after round-trip: got %v, want [Alice Bob Carol]", all)
	}
	if got := i2.Caption(); got != "Group shot" {
		t.Errorf("Caption after multi-creator round-trip: got %q", got)
	}
}

// TestSetCreatorReplacesFirstOnly verifies that SetCreator replaces only the
// first By-line entry and leaves additional ones intact (retrocompatible
// behaviour).
func TestSetCreatorReplacesFirstOnly(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.AddCreator("Alice")
	i.AddCreator("Bob")
	i.SetCreator("Eve") // replaces first (Alice → Eve)

	all := i.AllCreators()
	if len(all) != 2 {
		t.Fatalf("AllCreators: got %d, want 2", len(all))
	}
	if all[0] != "Eve" {
		t.Errorf("AllCreators[0]: got %q, want %q", all[0], "Eve")
	}
	if all[1] != "Bob" {
		t.Errorf("AllCreators[1]: got %q, want %q", all[1], "Bob")
	}
}

// TestDateCreatedAccessor verifies that DateCreated returns the CCYYMMDD string
// for dataset 2:55.
func TestDateCreatedAccessor(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2DateCreated, []byte("20240315")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i.DateCreated(); got != "20240315" {
		t.Errorf("DateCreated: got %q, want %q", got, "20240315")
	}
}

// TestTimeCreatedAccessor verifies that TimeCreated returns the HHMMSS±HHMM
// string for dataset 2:60.
func TestTimeCreatedAccessor(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2TimeCreated, []byte("143000+0100")},
	})
	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i.TimeCreated(); got != "143000+0100" {
		t.Errorf("TimeCreated: got %q, want %q", got, "143000+0100")
	}
}

// TestDateTimeCreatedAbsent verifies that DateCreated and TimeCreated return
// empty strings when the datasets are not present.
func TestDateTimeCreatedAbsent(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	if got := i.DateCreated(); got != "" {
		t.Errorf("DateCreated on empty IPTC: got %q, want empty", got)
	}
	if got := i.TimeCreated(); got != "" {
		t.Errorf("TimeCreated on empty IPTC: got %q, want empty", got)
	}
}

// TestDateTimeCreatedRoundTrip verifies that date/time values survive an
// Encode → Parse round-trip.
func TestDateTimeCreatedRoundTrip(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.setRecord2(DS2DateCreated, []byte("20240315"))
	i.setRecord2(DS2TimeCreated, []byte("143000+0100"))

	enc, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	i2, err := Parse(enc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i2.DateCreated(); got != "20240315" {
		t.Errorf("DateCreated after round-trip: got %q, want %q", got, "20240315")
	}
	if got := i2.TimeCreated(); got != "143000+0100" {
		t.Errorf("TimeCreated after round-trip: got %q, want %q", got, "143000+0100")
	}
}

// TestAllCreatorsNilReceiver verifies that AllCreators on a nil *IPTC returns
// nil without panicking.
func TestAllCreatorsNilReceiver(t *testing.T) {
	t.Parallel()
	var i *IPTC
	if got := i.AllCreators(); got != nil {
		t.Errorf("AllCreators(nil): got %v, want nil", got)
	}
}
