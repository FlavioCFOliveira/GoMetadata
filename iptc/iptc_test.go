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
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n))
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
	payload := []byte("caf\xC3\xA9") // "café" in UTF-8
	buf.WriteByte(byte(len(payload) >> 8))
	buf.WriteByte(byte(len(payload)))
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
	i := &IPTC{Records: make(map[uint8][]Dataset)}
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
	i := &IPTC{Records: make(map[uint8][]Dataset)}
	i.SetCopyright("(c) 2024 Test Corp")
	if got := i.Copyright(); got != "(c) 2024 Test Corp" {
		t.Errorf("SetCopyright: got %q, want %q", got, "(c) 2024 Test Corp")
	}
}

func TestSetCreator(t *testing.T) {
	i := &IPTC{Records: make(map[uint8][]Dataset)}
	i.SetCreator("Photographer X")
	if got := i.Creator(); got != "Photographer X" {
		t.Errorf("SetCreator: got %q, want %q", got, "Photographer X")
	}
}

func TestAddKeyword(t *testing.T) {
	i := &IPTC{Records: make(map[uint8][]Dataset)}
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
	i := &IPTC{Records: make(map[uint8][]Dataset)}
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
