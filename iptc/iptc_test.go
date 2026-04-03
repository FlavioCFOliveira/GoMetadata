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
