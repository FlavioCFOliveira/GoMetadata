package iptc

import (
	"bytes"
	"strings"
	"testing"
)

// TestSetDecodedValueEndsAtFirstNUL covers task #386: the decoded text of a
// dataset ends at the first NUL byte, in both character sets, and a value of
// NUL bytes only is empty. The raw Value is never changed.
func TestSetDecodedValueEndsAtFirstNUL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		utf8  bool
		want  string
	}{
		{"no NUL", "caption", false, "caption"},
		{"terminator", "caption\x00", false, "caption"},
		{"padding and garbage", "caption\x00\x00junk", false, "caption"},
		{"only NULs", "\x00\x00", false, ""},
		{"single NUL", "\x00", true, ""},
		{"leading NUL", "\x00abc", true, ""},
		{"ISO-8859-1 before the NUL", "caf\xe9\x00x", false, "café"},
		{"ISO-8859-1 after the NUL is not decoded", "ab\x00\xe9", false, "ab"},
		{"UTF-8 before the NUL", "caf\xc3\xa9\x00x", true, "café"},
		{"empty", "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Dataset{Record: 2, DataSet: DS2Caption, Value: []byte(tc.value)}
			d.setDecodedValue(tc.utf8)
			if d.decodedValue != tc.want {
				t.Errorf("decodedValue = %q, want %q", d.decodedValue, tc.want)
			}
			if string(d.Value) != tc.value {
				t.Errorf("Value changed to %q", d.Value)
			}
		})
	}
}

// TestEncodeKeepsNULBytes checks that a value with NUL bytes is written back as
// it was read (task #386: raw bytes untouched, write output unchanged).
func TestEncodeKeepsNULBytes(t *testing.T) {
	t.Parallel()
	value := "caption\x00\x00junk"
	stream := append([]byte{0x1C, 2, DS2Caption, 0, byte(len(value))}, value...) //nolint:gosec // G115: tiny test value
	parsed, err := Parse(stream)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := parsed.Caption(); got != "caption" {
		t.Fatalf("Caption() = %q, want %q", got, "caption")
	}
	out, err := Encode(parsed)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Contains(out, []byte(value)) {
		t.Errorf("Encode output lost the NUL bytes: %q", out)
	}
}

// FuzzIPTCTextHasNoNUL checks that no text accessor returns a NUL for any
// stream (task #386).
func FuzzIPTCTextHasNoNUL(f *testing.F) {
	f.Add([]byte{0x1C, 2, 120, 0, 3, 'a', 0, 'b'})
	f.Add([]byte{0x1C, 2, 25, 0, 2, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, err := Parse(b)
		if err != nil || p == nil {
			return
		}
		texts := append(p.Keywords(), p.Caption(), p.Copyright(), p.Creator(), p.DateCreated(), p.TimeCreated())
		texts = append(texts, p.AllCreators()...)
		for _, s := range texts {
			if strings.IndexByte(s, 0) >= 0 {
				t.Fatalf("accessor returned %q with a NUL", s)
			}
		}
	})
}

// TestCutAtNULMatchesIndexByte checks the word-at-a-time scan against
// bytes.IndexByte for every length up to 40 with a NUL at every position and
// with none.
func TestCutAtNULMatchesIndexByte(t *testing.T) {
	t.Parallel()
	for n := range 41 {
		for nul := -1; nul < n; nul++ {
			v := bytes.Repeat([]byte{0xFF}, n) // high bytes: no false positive from 0x80 bits
			for i := range v {
				v[i] = byte('a' + i%26)
			}
			if nul >= 0 {
				v[nul] = 0
			}
			want := v
			if i := bytes.IndexByte(v, 0); i >= 0 {
				want = v[:i]
			}
			if got := cutAtNUL(v); !bytes.Equal(got, want) {
				t.Fatalf("n=%d nul=%d: cutAtNUL = %q, want %q", n, nul, got, want)
			}
		}
	}
	// 0x80 and 0x01 bytes must not look like zeros.
	for _, b := range []byte{0x01, 0x7F, 0x80, 0x81, 0xFF} {
		v := bytes.Repeat([]byte{b}, 33)
		if got := cutAtNUL(v); len(got) != 33 {
			t.Errorf("byte %#x: cutAtNUL cut a value without a NUL at %d", b, len(got))
		}
	}
}
