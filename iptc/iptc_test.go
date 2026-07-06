package iptc

import (
	"bytes"
	"testing"
	"time"
	"unicode/utf8"
)

// buildIPTC builds a minimal IPTC IIM stream with the given datasets.
// Each item is [record, dataset, value...].
func buildIPTC(records []struct {
	rec uint8
	ds  uint8
	val []byte
}) []byte {
	var buf bytes.Buffer
	for _, r := range records {
		buf.WriteByte(0x1C)
		buf.WriteByte(r.rec)
		buf.WriteByte(r.ds)
		n := len(r.val)
		buf.WriteByte(byte(n >> 8)) //nolint:gosec // G115: test helper, intentional type cast
		buf.WriteByte(byte(n))      //nolint:gosec // G115: test helper, intentional type cast
		buf.Write(r.val)
	}
	return buf.Bytes()
}

func TestParseBasic(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2CopyrightNotice, []byte("Alice")},
		{2, DS2Caption, []byte("A sunset photo")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i.Copyright(); got != "Alice" {
		t.Errorf("Copyright: got %q, want %q", got, "Alice")
	}
	if got := i.Caption(); got != "A sunset photo" {
		t.Errorf("Caption: got %q, want %q", got, "A sunset photo")
	}
}

func TestParseUTF8Declaration(t *testing.T) {
	t.Parallel()
	// Build a stream with Record 1, Dataset 90 = ESC % G.
	var buf bytes.Buffer
	buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47}) // R1:D90 UTF-8
	buf.Write([]byte{0x1C, 0x02, DS2Caption, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'})
	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !i.isUTF8() {
		t.Error("expected isUTF8() = true after ESC % G declaration")
	}
	if got := i.Caption(); got != "Hello" {
		t.Errorf("Caption: got %q, want %q", got, "Hello")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2CopyrightNotice, []byte("Bob")},
		{2, DS2Caption, []byte("Mountain lake")},
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
	if got := i2.Copyright(); got != "Bob" {
		t.Errorf("Copyright after round-trip: got %q, want %q", got, "Bob")
	}
	if got := i2.Caption(); got != "Mountain lake" {
		t.Errorf("Caption after round-trip: got %q, want %q", got, "Mountain lake")
	}
}

func TestEncodePreservesUTF8Flag(t *testing.T) {
	t.Parallel()
	// Stream with UTF-8 declaration + a caption.
	var buf bytes.Buffer
	buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47})
	buf.Write([]byte{0x1C, 0x02, DS2Caption, 0x00, 0x03, 'C', 'a', 't'})

	i, err := Parse(buf.Bytes())
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
	if !i2.isUTF8() {
		t.Error("UTF-8 flag not preserved after encode/parse round-trip")
	}
}

func TestDatasetSizeCap(t *testing.T) {
	t.Parallel()
	// Build a stream with an extended-length dataset declaring 2 MiB size.
	// The actual data is short — the parser should stop cleanly.
	var buf bytes.Buffer
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Caption)
	// Extended length: 0x80 | 0x04 = use 4 bytes for length.
	buf.WriteByte(0x84)
	buf.WriteByte(0x00)
	// 4-byte length = 2 MiB
	buf.Write([]byte{0x00, 0x20, 0x00, 0x00})
	buf.Write([]byte{'H', 'i'}) // only 2 bytes of actual data

	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse should not error: %v", err)
	}
	// The oversized dataset should have been dropped; caption should be empty.
	if got := i.Caption(); got != "" {
		t.Errorf("expected empty caption after size cap, got %q", got)
	}
}

func TestKeywords(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Keywords, []byte("sunset")},
		{2, DS2Keywords, []byte("landscape")},
		{2, DS2Keywords, []byte("nature")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	kws := i.Keywords()
	if len(kws) != 3 {
		t.Fatalf("Keywords: got %d, want 3", len(kws))
	}
	if kws[0] != "sunset" || kws[1] != "landscape" || kws[2] != "nature" {
		t.Errorf("Keywords: got %v, want [sunset landscape nature]", kws)
	}
}

func TestKeywordsEmpty(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Caption, []byte("no keywords here")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if kws := i.Keywords(); len(kws) != 0 {
		t.Errorf("Keywords: got %v, want empty", kws)
	}
}

func TestCreator(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Byline, []byte("Jane Doe")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i.Creator(); got != "Jane Doe" {
		t.Errorf("Creator: got %q, want %q", got, "Jane Doe")
	}
}

func TestCreatorEmpty(t *testing.T) {
	t.Parallel()
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Caption, []byte("no byline")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i.Creator(); got != "" {
		t.Errorf("Creator: got %q, want empty", got)
	}
}

// TestISO8859_1Decoding exercises the non-UTF-8 path in decodeString.
// The byte 0xE9 is 'é' in ISO-8859-1, which should be decoded to U+00E9.
func TestISO8859_1Decoding(t *testing.T) {
	t.Parallel()
	// No UTF-8 declaration → ISO-8859-1 assumed.
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		// "café" in ISO-8859-1: 'c'=0x63 'a'=0x61 'f'=0x66 'é'=0xE9
		{2, DS2Caption, []byte{0x63, 0x61, 0x66, 0xE9}},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := i.Caption()
	want := "café"
	if got != want {
		t.Errorf("Caption (ISO-8859-1): got %q, want %q", got, want)
	}
}

// TestISO8859_1VsUTF8 confirms the same high byte is treated differently
// depending on the coded character set declaration.
func TestISO8859_1VsUTF8(t *testing.T) {
	t.Parallel()
	// With UTF-8 declaration, raw bytes are returned as-is.
	var buf bytes.Buffer
	buf.Write([]byte{0x1C, 0x01, 0x5A, 0x00, 0x03, 0x1B, 0x25, 0x47}) // UTF-8 decl
	buf.WriteByte(0x1C)
	buf.WriteByte(0x02)
	buf.WriteByte(DS2Caption)
	payload := []byte("caf\xC3\xA9")       // "café" in UTF-8
	buf.WriteByte(byte(len(payload) >> 8)) //nolint:gosec // G115: test helper, intentional type cast
	buf.WriteByte(byte(len(payload)))      //nolint:gosec // G115: test helper, intentional type cast
	buf.Write(payload)

	i, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("Parse UTF-8 stream: %v", err)
	}
	if got := i.Caption(); got != "café" {
		t.Errorf("Caption (UTF-8 declared): got %q, want %q", got, "café")
	}
}

// ---------------------------------------------------------------------------
// Setter methods
// ---------------------------------------------------------------------------

func TestSetCaption(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCaption("First caption")
	if got := i.Caption(); got != "First caption" {
		t.Errorf("SetCaption: got %q, want %q", got, "First caption")
	}
	// Overwrite should replace, not append.
	i.SetCaption("Updated caption")
	if got := i.Caption(); got != "Updated caption" {
		t.Errorf("SetCaption (overwrite): got %q, want %q", got, "Updated caption")
	}
}

func TestSetCopyright(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCopyright("(c) 2024 Test Corp")
	if got := i.Copyright(); got != "(c) 2024 Test Corp" {
		t.Errorf("SetCopyright: got %q, want %q", got, "(c) 2024 Test Corp")
	}
}

func TestSetCreator(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCreator("Photographer X")
	if got := i.Creator(); got != "Photographer X" {
		t.Errorf("SetCreator: got %q, want %q", got, "Photographer X")
	}
}

func TestAddKeyword(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.AddKeyword("alpha")
	i.AddKeyword("beta")
	i.AddKeyword("gamma")
	kws := i.Keywords()
	if len(kws) != 3 {
		t.Fatalf("AddKeyword: got %d keywords, want 3", len(kws))
	}
	if kws[0] != "alpha" || kws[1] != "beta" || kws[2] != "gamma" {
		t.Errorf("AddKeyword: got %v, want [alpha beta gamma]", kws)
	}
}

func TestSettersRoundTrip(t *testing.T) {
	t.Parallel()
	i := new(IPTC)
	i.SetCaption("A test caption")
	i.SetCopyright("(c) Test Corp")
	i.SetCreator("Author Name")
	i.AddKeyword("key1")
	i.AddKeyword("key2")

	encoded, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	i2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := i2.Caption(); got != "A test caption" {
		t.Errorf("Caption round-trip: got %q", got)
	}
	if got := i2.Copyright(); got != "(c) Test Corp" {
		t.Errorf("Copyright round-trip: got %q", got)
	}
	if got := i2.Creator(); got != "Author Name" {
		t.Errorf("Creator round-trip: got %q", got)
	}
	kws := i2.Keywords()
	if len(kws) != 2 || kws[0] != "key1" || kws[1] != "key2" {
		t.Errorf("Keywords round-trip: got %v", kws)
	}
}

func TestIPTCExtendedLengthRoundTrip(t *testing.T) {
	t.Parallel()
	// Dataset with a value that exceeds 32,767 bytes; requires extended-length
	// encoding on write (IIM §1.6.2). Encoder previously truncated such values.
	large := make([]byte, 40000)
	for idx := range large {
		large[idx] = byte(idx % 251)
	}

	i := new(IPTC)
	i.Records[2] = append(i.Records[2], Dataset{Record: 2, DataSet: DS2Caption, Value: large})

	encoded, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	i2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse after extended-length encode: %v", err)
	}
	if len(i2.Records[2]) == 0 {
		t.Fatal("no datasets in record 2 after round-trip")
	}
	got := i2.Records[2][0].Value
	if len(got) != len(large) {
		t.Fatalf("value length: got %d, want %d", len(got), len(large))
	}
	for idx := range large {
		if got[idx] != large[idx] {
			t.Fatalf("value mismatch at byte %d: got %d, want %d", idx, got[idx], large[idx])
		}
	}
}

// ---------------------------------------------------------------------------
// P3-D: Record 1 and beyond-record-2 dataset coverage
// ---------------------------------------------------------------------------

// TestIPTCRecord1Parsing verifies that a Record 1 dataset is stored in
// IPTC.Records[1] after parsing.
func TestIPTCRecord1Parsing(t *testing.T) {
	t.Parallel()
	// Record 1, Dataset 20 = Supplemental Category (a valid IIM R1 dataset).
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{1, 20, []byte("test")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	datasets := i.Records[1]
	if len(datasets) == 0 {
		t.Fatalf("Records[1] is empty or missing; got records: %v", i.Records)
	}

	var found bool
	for _, ds := range datasets {
		if ds.Record == 1 && ds.DataSet == 20 && string(ds.Value) == "test" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Record 1 / Dataset 20 with value \"test\" not found in Records[1]: %v", datasets)
	}
}

// TestIPTCRecordsBeyond2 verifies that datasets for records other than 1 and 2
// (e.g. record 3) are stored in the correct Records bucket.
func TestIPTCRecordsBeyond2(t *testing.T) {
	t.Parallel()
	// Record 3 is the "Pre-ObjectData Descriptor" record in IIM.
	const rec3DS uint8 = 10 // arbitrary dataset within record 3
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{3, rec3DS, []byte("record3value")},
	})

	i, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	datasets := i.Records[3]
	if len(datasets) == 0 {
		t.Fatalf("Records[3] is empty or missing; got records: %v", i.Records)
	}

	var found bool
	for _, ds := range datasets {
		if ds.Record == 3 && ds.DataSet == rec3DS && string(ds.Value) == "record3value" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Record 3 / Dataset %d with value \"record3value\" not found in Records[3]: %v",
			rec3DS, datasets)
	}
}

// TestEncodeExtendedLengthRoundTrip verifies that a dataset whose value exceeds
// 32767 bytes (0x8000) is correctly encoded using IIM §1.6.2 extended length
// encoding and round-trips back through Parse without data loss.
//
// Note: Encode may prepend a 1:90 UTF-8 declaration (8 bytes) when the value
// contains non-ASCII bytes (#19 auto-inject). The test scans for the Caption
// dataset marker rather than assuming a fixed byte offset.
func TestEncodeExtendedLengthRoundTrip(t *testing.T) {
	t.Parallel()
	// Build a value that requires extended length: 40 000 bytes.
	// Use bytes 0..127 only (ASCII) to avoid triggering the #19 UTF-8
	// auto-inject so the byte-layout assertions below remain straightforward.
	const valueLen = 40_000
	large := make([]byte, valueLen)
	for i := range large {
		large[i] = byte(i % 127) // ASCII-safe; no byte > 0x7E
	}

	i := new(IPTC)
	i.Records[2] = []Dataset{
		{Record: 2, DataSet: DS2Caption, Value: large},
	}

	encoded, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Scan for the Caption dataset (0x1C 0x02 DS2Caption) to find its position
	// independent of any prefix headers (e.g. 1:90 declaration).
	want3 := []byte{0x1C, 0x02, DS2Caption}
	dsStart := -1
	for j := 0; j+3 <= len(encoded); j++ {
		if encoded[j] == want3[0] && encoded[j+1] == want3[1] && encoded[j+2] == want3[2] {
			dsStart = j
			break
		}
	}
	if dsStart < 0 {
		t.Fatal("Caption dataset marker (0x1C 0x02 DS2Caption) not found in encoded output")
	}

	// Verify the extended length prefix in the encoded stream at dsStart.
	// After marker(0x1C) + record(1) + dataset(1) = 3 bytes:
	//   byte dsStart+3: 0x80 (bit 15 set, upper 7 bits of byte-count = 0)
	//   byte dsStart+4: 0x04 (lower 8 bits of byte-count = 4)
	//   bytes dsStart+5..+8: big-endian uint32 = valueLen
	if len(encoded) < dsStart+9 {
		t.Fatalf("encoded too short to contain extended header at dsStart=%d", dsStart)
	}
	if encoded[dsStart+3] != 0x80 {
		t.Errorf("encoded[%d] (size high) = 0x%02X, want 0x80", dsStart+3, encoded[dsStart+3])
	}
	if encoded[dsStart+4] != 0x04 {
		t.Errorf("encoded[%d] (size low / byte-count) = 0x%02X, want 0x04", dsStart+4, encoded[dsStart+4])
	}
	encodedLen := int(encoded[dsStart+5])<<24 | int(encoded[dsStart+6])<<16 |
		int(encoded[dsStart+7])<<8 | int(encoded[dsStart+8])
	if encodedLen != valueLen {
		t.Errorf("extended length field = %d, want %d", encodedLen, valueLen)
	}

	// Parse the encoded stream and verify the value survives round-trip.
	i2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	datasets := i2.Records[2]
	if len(datasets) == 0 {
		t.Fatal("Records[2] is empty after round-trip")
	}
	got := datasets[0].Value
	if len(got) != valueLen {
		t.Fatalf("round-trip value length = %d, want %d", len(got), valueLen)
	}
	for j, b := range got {
		if b != large[j] {
			t.Fatalf("value mismatch at byte %d: got 0x%02X, want 0x%02X", j, b, large[j])
		}
	}
}

// TestParseInvalidRecordNoPanic is a regression test for the OOB index panic
// discovered by fuzzing. Any dataset whose record byte falls outside [1, 9]
// must be silently skipped; Parse must return a non-nil *IPTC with no error,
// and valid datasets elsewhere in the same stream must be preserved.
//
// PoC input: {0x1C, 0x30, 0x30, 0x00, 0x00} — record=0x30=48 (out of range).
func TestParseInvalidRecordNoPanic(t *testing.T) {
	t.Parallel()

	// Each case embeds the bad record in a stream that also contains a valid
	// Record-2 caption so we can verify continuation after the skip.
	validSuffix := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2Caption, []byte("after-bad-record")},
	})

	cases := []struct {
		name   string
		record byte
	}{
		{"record-0", 0x00},
		{"record-10", 0x0A},
		{"record-48", 0x30},
		{"record-255", 0xFF},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Craft a stream: bad-record dataset first, then the valid caption.
			bad := make([]byte, 0, 5+len(validSuffix))
			bad = append(bad, 0x1C, tc.record, 0x30, 0x00, 0x00)
			stream := append(bad, validSuffix...)

			i, err := Parse(stream)
			if err != nil {
				t.Fatalf("Parse(%02X record): unexpected error: %v", tc.record, err)
			}
			if i == nil {
				t.Fatalf("Parse(%02X record): returned nil *IPTC", tc.record)
			}
			// Valid dataset after the bad record must be preserved.
			if got := i.Caption(); got != "after-bad-record" {
				t.Errorf("Parse(%02X record): Caption = %q, want %q", tc.record, got, "after-bad-record")
			}
		})
	}

	// Also verify the exact fuzz-corpus PoC: {0x1C, 0x30, 0x30, 0x00, 0x00}.
	t.Run("fuzz-corpus-poc", func(t *testing.T) {
		t.Parallel()
		i, err := Parse([]byte{0x1C, 0x30, 0x30, 0x00, 0x00})
		if err != nil {
			t.Fatalf("Parse(fuzz PoC): unexpected error: %v", err)
		}
		if i == nil {
			t.Fatal("Parse(fuzz PoC): returned nil *IPTC")
		}
	})
}

func BenchmarkIPTCParse(b *testing.B) {
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2CopyrightNotice, []byte("Test Corp")},
		{2, DS2Caption, []byte("A test image caption for benchmarking purposes")},
	})
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Parse(raw)
	}
}

// BenchmarkIPTCEncode measures the serialisation cost of a small IPTC struct
// with copyright, caption, and two keywords.
func BenchmarkIPTCEncode(b *testing.B) {
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2CopyrightNotice, []byte("Test Corp")},
		{2, DS2Caption, []byte("A test image caption for benchmarking purposes")},
		{2, DS2Keywords, []byte("benchmark")},
		{2, DS2Keywords, []byte("performance")},
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

// BenchmarkIPTCAccessors measures the cost of repeated Caption/Copyright/Keywords
// reads on a parsed IPTC struct. Values are pre-decoded eagerly by Parse, so
// read accessors return the decodedValue string field directly with zero extra
// allocations per call and no synchronisation overhead (task #60 fix).
func BenchmarkIPTCAccessors(b *testing.B) {
	raw := buildIPTC([]struct {
		rec uint8
		ds  uint8
		val []byte
	}{
		{2, DS2CopyrightNotice, []byte("Test Corp")},
		{2, DS2Caption, []byte("A test image caption for benchmarking purposes")},
		{2, DS2Keywords, []byte("nature")},
		{2, DS2Keywords, []byte("landscape")},
		{2, DS2Keywords, []byte("benchmark")},
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

// TestIPTCSetKeywords verifies that SetKeywords replaces existing keywords and
// that a round-trip (Encode → Parse) preserves them exactly.
func TestIPTCSetKeywords(t *testing.T) {
	t.Parallel()
	t.Run("ReplaceExisting", func(t *testing.T) {
		t.Parallel()
		raw := buildIPTC([]struct {
			rec uint8
			ds  uint8
			val []byte
		}{
			{2, DS2Keywords, []byte("old1")},
			{2, DS2Keywords, []byte("old2")},
		})
		i, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}

		i.SetKeywords([]string{"nature", "landscape", "sunset"})

		// Verify in-memory state before encode.
		kws := i.Keywords()
		if len(kws) != 3 {
			t.Fatalf("Keywords count = %d, want 3 (before encode)", len(kws))
		}

		// Round-trip.
		enc, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		i2, err := Parse(enc)
		if err != nil {
			t.Fatalf("Parse (round-trip): %v", err)
		}
		kws2 := i2.Keywords()
		if len(kws2) != 3 {
			t.Fatalf("Keywords count after round-trip = %d, want 3", len(kws2))
		}
		want := map[string]bool{"nature": true, "landscape": true, "sunset": true}
		for _, kw := range kws2 {
			if !want[kw] {
				t.Errorf("unexpected keyword %q after round-trip", kw)
			}
		}
	})

	t.Run("EmptySliceRemovesAll", func(t *testing.T) {
		t.Parallel()
		raw := buildIPTC([]struct {
			rec uint8
			ds  uint8
			val []byte
		}{
			{2, DS2Keywords, []byte("remove-me")},
		})
		i, _ := Parse(raw)
		i.SetKeywords([]string{})
		if kws := i.Keywords(); len(kws) != 0 {
			t.Errorf("Keywords after SetKeywords([]) = %v, want empty", kws)
		}
	})

	t.Run("PreservesOtherDatasets", func(t *testing.T) {
		t.Parallel()
		raw := buildIPTC([]struct {
			rec uint8
			ds  uint8
			val []byte
		}{
			{2, DS2Caption, []byte("my caption")},
			{2, DS2Keywords, []byte("old")},
		})
		i, _ := Parse(raw)
		i.SetKeywords([]string{"new"})
		if got := i.Caption(); got != "my caption" {
			t.Errorf("Caption changed after SetKeywords: got %q, want %q", got, "my caption")
		}
		kws := i.Keywords()
		if len(kws) != 1 || kws[0] != "new" {
			t.Errorf("Keywords = %v, want [new]", kws)
		}
	})

	t.Run("NilReceiverNoPanic", func(t *testing.T) {
		t.Parallel()
		var i *IPTC
		i.SetKeywords([]string{"a", "b"}) // must not panic
	})
}

// TestIPTCSetDateCreated covers iptc.IPTC.SetDateCreated: dataset 2:55/2:60
// encoding, sub-second truncation, the UTC "+0000" (never "Z") offset form,
// and the year-overflow guard (IPTC/XMP reconciliation spec §0.3).
func TestIPTCSetDateCreated(t *testing.T) {
	t.Parallel()

	t.Run("BasicEncoding", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		ts := time.Date(2026, 6, 15, 14, 30, 22, 500_000_000, time.FixedZone("+0100", 3600))
		i.SetDateCreated(ts)
		if got := i.DateCreated(); got != "20260615" {
			t.Errorf("DateCreated() = %q, want %q", got, "20260615")
		}
		if got := i.TimeCreated(); got != "143022+0100" {
			t.Errorf("TimeCreated() = %q, want %q", got, "143022+0100")
		}
	})

	t.Run("UTCZeroOffsetNeverZ", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
		i.SetDateCreated(ts)
		if got := i.TimeCreated(); got != "030405+0000" {
			t.Errorf("TimeCreated() = %q, want %q (explicit +0000, never Z)", got, "030405+0000")
		}
	})

	t.Run("ReplacesExisting", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetDateCreated(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
		i.SetDateCreated(time.Date(2025, 12, 31, 23, 59, 59, 0, time.UTC))
		if got := i.DateCreated(); got != "20251231" {
			t.Errorf("DateCreated() = %q, want %q (replace, not append)", got, "20251231")
		}
		if got := i.TimeCreated(); got != "235959+0000" {
			t.Errorf("TimeCreated() = %q, want %q", got, "235959+0000")
		}
	})

	t.Run("YearOverflowGuardNoOp", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetDateCreated(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		if got := i.DateCreated(); got != "" {
			t.Errorf("DateCreated() = %q, want empty (year 10000 overflows 8-byte CCYYMMDD)", got)
		}
		if got := i.TimeCreated(); got != "" {
			t.Errorf("TimeCreated() = %q, want empty (guard is a complete no-op)", got)
		}
	})

	t.Run("YearOverflowGuardDoesNotClobberExisting", func(t *testing.T) {
		t.Parallel()
		// A prior valid SetDateCreated must survive an out-of-range follow-up
		// call: the guard is a no-op, not a clear.
		i := new(IPTC)
		i.SetDateCreated(time.Date(2024, 6, 21, 12, 0, 0, 0, time.UTC))
		i.SetDateCreated(time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC))
		if got := i.DateCreated(); got != "20240621" {
			t.Errorf("DateCreated() = %q, want %q (out-of-range call must not clobber)", got, "20240621")
		}
	})

	t.Run("NilReceiverNoPanic", func(t *testing.T) {
		t.Parallel()
		var i *IPTC
		i.SetDateCreated(time.Now()) // must not panic
	})
}

// TestIPTCSetCreators covers iptc.IPTC.SetCreators: full-replace semantics,
// order preservation, 32-octet UTF-8-safe truncation, the UTF-8 flag, and
// U+001E stripping (IPTC/XMP reconciliation spec §4, defensive requirements).
func TestIPTCSetCreators(t *testing.T) {
	t.Parallel()

	t.Run("OrderPreserved", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"Alice", "Bob", "Carol"})
		got := i.AllCreators()
		want := []string{"Alice", "Bob", "Carol"}
		if len(got) != len(want) {
			t.Fatalf("AllCreators() len = %d, want %d", len(got), len(want))
		}
		for idx, w := range want {
			if got[idx] != w {
				t.Errorf("AllCreators()[%d] = %q, want %q", idx, got[idx], w)
			}
		}
	})

	t.Run("FullReplaceNotAppend", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"A", "B"})
		i.SetCreators([]string{"C"})
		got := i.AllCreators()
		if len(got) != 1 || got[0] != "C" {
			t.Errorf("AllCreators() = %v, want [C] (full replace, not append)", got)
		}
	})

	t.Run("EmptySliceRemovesAll", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"Alice", "Bob"})
		i.SetCreators([]string{})
		if got := i.AllCreators(); got != nil {
			t.Errorf("AllCreators() = %v, want nil", got)
		}
	})

	t.Run("NilSliceTreatedSameAsEmpty", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"Alice", "Bob"})
		i.SetCreators(nil)
		if got := i.AllCreators(); got != nil {
			t.Errorf("AllCreators() = %v, want nil", got)
		}
	})

	t.Run("PreservesOtherDatasets", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCaption("my caption")
		i.SetCreators([]string{"Alice"})
		if got := i.Caption(); got != "my caption" {
			t.Errorf("Caption changed after SetCreators: got %q, want %q", got, "my caption")
		}
	})

	t.Run("TruncatesAtUTF8RuneBoundary", func(t *testing.T) {
		t.Parallel()
		// 32-octet limit (IIM §2.2.25); "café" repeated forces a multi-byte
		// rune to straddle the boundary if truncation is not rune-safe.
		long := ""
		for len(long) < 40 {
			long += "café "
		}
		i := new(IPTC)
		i.SetCreators([]string{long})
		got := i.AllCreators()
		if len(got) != 1 {
			t.Fatalf("AllCreators() len = %d, want 1", len(got))
		}
		if len(got[0]) > 32 {
			t.Errorf("AllCreators()[0] length = %d, want <= 32", len(got[0]))
		}
		if !utf8.ValidString(got[0]) {
			t.Errorf("AllCreators()[0] = %q is not valid UTF-8 after truncation", got[0])
		}
	})

	t.Run("StripsEmbeddedRecordSeparator", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"Alice\x1eBob"})
		got := i.AllCreators()
		if len(got) != 1 || got[0] != "AliceBob" {
			t.Errorf("AllCreators() = %v, want [AliceBob] (U+001E stripped)", got)
		}
	})

	t.Run("SetsUTF8FlagForNonASCII", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"café"})
		if !i.isUTF8() {
			t.Error("isUTF8() = false after SetCreators with non-ASCII content")
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		t.Parallel()
		i := new(IPTC)
		i.SetCreators([]string{"Alice", "Bob", "Carol"})
		encoded, err := Encode(i)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		i2, err := Parse(encoded)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got := i2.AllCreators()
		want := []string{"Alice", "Bob", "Carol"}
		if len(got) != len(want) {
			t.Fatalf("AllCreators() round-trip len = %d, want %d", len(got), len(want))
		}
		for idx, w := range want {
			if got[idx] != w {
				t.Errorf("AllCreators() round-trip [%d] = %q, want %q", idx, got[idx], w)
			}
		}
	})

	t.Run("NilReceiverNoPanic", func(t *testing.T) {
		t.Parallel()
		var i *IPTC
		i.SetCreators([]string{"a", "b"}) // must not panic
	})
}
