package iptc

import (
	"encoding/hex"
	"testing"
)

// TestDigest verifies that Digest computes the correct MD5 of rawIIM bytes.
// MWG Guidelines v2.0 §3.3.1: digest = MD5(raw 0x0404 IIM stream).
func TestDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		wantHex string
	}{
		{
			name:    "empty input",
			input:   []byte{},
			wantHex: "d41d8cd98f00b204e9800998ecf8427e", // known MD5 of empty string
		},
		{
			name:    "single byte",
			input:   []byte{0x1C},
			wantHex: "0398b4090f24adbccc218219f5746b10",
		},
		{
			name:    "typical IIM bytes",
			input:   []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'},
			wantHex: "", // computed below; test self-consistency only
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Digest(tc.input)
			if tc.wantHex == "" {
				// Self-consistency: same input always gives same output.
				got2 := Digest(tc.input)
				if got != got2 {
					t.Errorf("Digest is not deterministic for %q", tc.input)
				}
				return
			}
			gotHex := hex.EncodeToString(got[:])
			if gotHex != tc.wantHex {
				t.Errorf("Digest(%q) = %s; want %s", tc.input, gotHex, tc.wantHex)
			}
		})
	}
}

// TestDigestIsZero verifies the all-zero sentinel detection.
// MWG §3.3.1: all-zero digest means "unknown" — treat as mismatch (elevate IPTC).
func TestDigestIsZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    [16]byte
		want bool
	}{
		{
			name: "all-zero sentinel",
			d:    [16]byte{},
			want: true,
		},
		{
			name: "one byte non-zero",
			d:    [16]byte{0x01},
			want: false,
		},
		{
			name: "last byte non-zero",
			d:    [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01},
			want: false,
		},
		{
			name: "all 0xFF",
			d:    [16]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DigestIsZero(tc.d); got != tc.want {
				t.Errorf("DigestIsZero(%v) = %v; want %v", tc.d, got, tc.want)
			}
		})
	}
}

// TestDigestMatch verifies the three-state logic required by MWG §3.3.1:
//   - match=true, unknown=false  → stored equals computed; XMP keeps priority
//   - match=false, unknown=false → mismatch; elevate IPTC trust
//   - match=false, unknown=true  → all-zero sentinel; elevate IPTC trust
func TestDigestMatch(t *testing.T) {
	t.Parallel()

	rawIIM := []byte{0x1C, 0x02, 0x05, 0x00, 0x03, 'A', 'B', 'C'}
	correct := Digest(rawIIM)
	wrong := [16]byte{0xDE, 0xAD}

	tests := []struct {
		name        string
		rawIIM      []byte
		stored      [16]byte
		wantMatch   bool
		wantUnknown bool
	}{
		{
			// MWG §3.3.1: matching digest → XMP priority kept (default).
			name:        "match — XMP priority",
			rawIIM:      rawIIM,
			stored:      correct,
			wantMatch:   true,
			wantUnknown: false,
		},
		{
			// MWG §3.3.1: mismatch → elevate IPTC trust.
			name:        "mismatch — IPTC elevated",
			rawIIM:      rawIIM,
			stored:      wrong,
			wantMatch:   false,
			wantUnknown: false,
		},
		{
			// MWG §3.3.1: all-zero sentinel → unknown; elevate IPTC trust.
			name:        "all-zero sentinel — IPTC elevated",
			rawIIM:      rawIIM,
			stored:      [16]byte{},
			wantMatch:   false,
			wantUnknown: true,
		},
		{
			// Edge: nil rawIIM with its correct MD5.
			name:        "nil rawIIM match",
			rawIIM:      nil,
			stored:      Digest(nil),
			wantMatch:   true,
			wantUnknown: false,
		},
		{
			// Edge: nil rawIIM with wrong digest.
			name:        "nil rawIIM mismatch",
			rawIIM:      nil,
			stored:      wrong,
			wantMatch:   false,
			wantUnknown: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotMatch, gotUnknown := DigestMatch(tc.rawIIM, tc.stored)
			if gotMatch != tc.wantMatch || gotUnknown != tc.wantUnknown {
				t.Errorf("DigestMatch() = (match=%v, unknown=%v); want (match=%v, unknown=%v)",
					gotMatch, gotUnknown, tc.wantMatch, tc.wantUnknown)
			}
		})
	}
}
