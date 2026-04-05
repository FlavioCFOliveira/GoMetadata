package iptc

import (
	"bytes"
	"testing"
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
	i := new(IPTC)
	i.SetCopyright("(c) 2024 Test Corp")
	if got := i.Copyright(); got != "(c) 2024 Test Corp" {
		t.Errorf("SetCopyright: got %q, want %q", got, "(c) 2024 Test Corp")
	}
}

func TestSetCreator(t *testing.T) {
	i := new(IPTC)
	i.SetCreator("Photographer X")
	if got := i.Creator(); got != "Photographer X" {
		t.Errorf("SetCreator: got %q, want %q", got, "Photographer X")
	}
}

func TestAddKeyword(t *testing.T) {
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
func TestEncodeExtendedLengthRoundTrip(t *testing.T) {
	// Build a value that requires extended length: 40 000 bytes.
	const valueLen = 40_000
	large := make([]byte, valueLen)
	for i := range large {
		large[i] = byte(i & 0xFF)
	}

	i := new(IPTC)
	i.Records[2] = []Dataset{
		{Record: 2, DataSet: DS2Caption, Value: large},
	}

	encoded, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Verify the extended length prefix in the encoded stream.
	// After marker(0x1C) + record(1) + dataset(1) = 3 bytes offset:
	//   byte 3: 0x80 (bit 15 set, upper 7 bits of byte-count = 0)
	//   byte 4: 0x04 (lower 8 bits of byte-count = 4)
	//   bytes 5-8: big-endian uint32 = valueLen
	if len(encoded) < 9 {
		t.Fatalf("encoded length %d too short to contain extended header", len(encoded))
	}
	if encoded[0] != 0x1C {
		t.Errorf("encoded[0] = 0x%02X, want 0x1C (tag marker)", encoded[0])
	}
	if encoded[3] != 0x80 {
		t.Errorf("encoded[3] (size high) = 0x%02X, want 0x80", encoded[3])
	}
	if encoded[4] != 0x04 {
		t.Errorf("encoded[4] (size low / byte-count) = 0x%02X, want 0x04", encoded[4])
	}
	encodedLen := int(encoded[5])<<24 | int(encoded[6])<<16 | int(encoded[7])<<8 | int(encoded[8])
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
	for i := 0; i < b.N; i++ {
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
	for n := 0; n < b.N; n++ {
		_, _ = Encode(i)
	}
}

// BenchmarkIPTCAccessors measures the cost of repeated Caption/Copyright/Keywords
// reads on a parsed IPTC struct. The decode cache means the ISO-8859-1 → UTF-8
// conversion is paid only on the first call; subsequent calls return the cached
// string with zero extra allocations.
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
	for n := 0; n < b.N; n++ {
		_ = i.Caption()
		_ = i.Copyright()
		_ = i.Keywords()
	}
}

// TestIPTCSetKeywords verifies that SetKeywords replaces existing keywords and
// that a round-trip (Encode → Parse) preserves them exactly.
func TestIPTCSetKeywords(t *testing.T) {
	t.Run("ReplaceExisting", func(t *testing.T) {
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
		var i *IPTC
		i.SetKeywords([]string{"a", "b"}) // must not panic
	})
}
