package tiffscan

// psir_test.go — tests for task #301: IPTC read from the Photoshop image
// resource block in tag 0x8649 when tag 0x83BB is absent, and the IPTC
// digest (resource 0x0425) read from the same block.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// irbResource builds one "8BIM" resource with an empty Pascal name.
func irbResource(id uint16, data []byte) []byte {
	b := []byte("8BIM")
	b = binary.BigEndian.AppendUint16(b, id)
	b = append(b, 0, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(data))) //nolint:gosec // G115: test sizes are small
	b = append(b, data...)
	if len(data)%2 != 0 {
		b = append(b, 0)
	}
	return b
}

var (
	psirIIM    = []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'p', 's', 'i', 'r'}                 //nolint:gochecknoglobals // read-only test fixture
	nativeIIM  = []byte{0x1C, 0x02, 0x78, 0x00, 0x04, 'n', 'a', 't', 'v'}                 //nolint:gochecknoglobals // read-only test fixture
	psirDigest = bytes.Repeat([]byte{0x5A}, 16)                                           //nolint:gochecknoglobals // read-only test fixture
	psirStream = append(irbResource(0x0425, psirDigest), irbResource(0x0404, psirIIM)...) //nolint:gochecknoglobals // read-only test fixture
)

type fixtureEntry = struct {
	tag, typ uint16
	value    []byte
}

// withHeader returns buildIFD0(entries) with a little-endian classic TIFF
// header naming IFD0 (buildIFD0 leaves the header zeroed).
func withHeader(entries []fixtureEntry) []byte {
	b := buildIFD0(entries)
	copy(b, "II*\x00")
	binary.LittleEndian.PutUint32(b[4:], fixtureIFD0Off)
	return b
}

func TestExtractTagValues_PSIRFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []fixtureEntry
		want    []byte
	}{
		{"IPTC only in 0x8649", []fixtureEntry{{0x8649, 7, psirStream}}, psirIIM},
		{"0x83BB wins over 0x8649", []fixtureEntry{{0x83BB, 7, nativeIIM}, {0x8649, 7, psirStream}}, nativeIIM},
		{"empty 0x83BB falls back", []fixtureEntry{{0x83BB, 4, []byte{0, 0, 0, 0}}, {0x8649, 1, psirStream}}, psirIIM},
		{"0x8649 without 0x0404", []fixtureEntry{{0x8649, 7, irbResource(0x0425, psirDigest)}}, nil},
		{"0x8649 not an IRB", []fixtureEntry{{0x8649, 7, []byte("not a resource block")}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			buf := buildIFD0(tc.entries)
			iptc, _ := ExtractTagValues(buf, fixtureIFD0Off, binary.LittleEndian, true)
			if !bytes.Equal(iptc, tc.want) {
				t.Errorf("rawIPTC = %x, want %x", iptc, tc.want)
			}
		})
	}
}

func TestWithPSIRFallback(t *testing.T) {
	t.Parallel()
	if got := WithPSIRFallback(nativeIIM, psirStream); !bytes.Equal(got, nativeIIM) {
		t.Errorf("native present: got %x", got)
	}
	if got := WithPSIRFallback(nil, psirStream); !bytes.Equal(got, psirIIM) {
		t.Errorf("native absent: got %x", got)
	}
	if got := WithPSIRFallback(nil, nil); got != nil {
		t.Errorf("both absent: got %x", got)
	}
	if got := WithPSIRFallback([]byte{}, irbResource(0x0425, psirDigest)); got == nil || len(got) != 0 {
		t.Errorf("no IPTC anywhere: got %x, want the empty native value", got)
	}
}

func TestIPTCDigest_Classic(t *testing.T) {
	t.Parallel()
	buf := withHeader([]fixtureEntry{{0x83BB, 7, nativeIIM}, {0x8649, 7, psirStream}})
	if got := IPTCDigest(buf); !bytes.Equal(got, psirDigest) {
		t.Errorf("IPTCDigest = %x, want %x", got, psirDigest)
	}
	big := bytes.Clone(buf)
	big[0], big[1] = 'M', 'M' // order mismatch: entries no longer parse as 0x8649
	if got := IPTCDigest(big); got != nil {
		t.Errorf("byte-swapped stream: got %x, want nil", got)
	}
	for name, in := range map[string][]byte{
		"empty":        nil,
		"short":        []byte("II*\x00"),
		"bad order":    append([]byte("XX*\x00"), buf[4:]...),
		"no 0x8649":    withHeader([]fixtureEntry{{0x83BB, 7, nativeIIM}}),
		"no digest":    withHeader([]fixtureEntry{{0x8649, 7, irbResource(0x0404, psirIIM)}}),
		"ifd0 outside": append(append([]byte(nil), buf[:4]...), 0xFF, 0xFF, 0x00, 0x00),
	} {
		if got := IPTCDigest(in); got != nil {
			t.Errorf("%s: got %x, want nil", name, got)
		}
	}
	// Out-of-line value pointing past the end.
	trunc := bytes.Clone(buf)
	binary.LittleEndian.PutUint32(trunc[fixtureIFD0Off+2+12+8:], uint32(len(trunc))) //nolint:gosec // G115: test
	if got := IPTCDigest(trunc); got != nil {
		t.Errorf("truncated 0x8649 value: got %x, want nil", got)
	}
}

// bigTIFFWithPSIR builds a little-endian BigTIFF stream whose IFD0 holds one
// UNDEFINED tag 0x8649 entry with an out-of-line value (BigTIFF spec §2:
// 8-byte count, 20-byte entries, 8-byte value/offset field).
func bigTIFFWithPSIR(psir []byte) []byte {
	o := binary.LittleEndian
	b := make([]byte, 16+8+20+8, 16+8+20+8+len(psir))
	copy(b, "II")
	o.PutUint16(b[2:], 43)
	o.PutUint16(b[4:], 8)
	o.PutUint64(b[8:], 16)
	o.PutUint64(b[16:], 1)
	o.PutUint16(b[24:], 0x8649)
	o.PutUint16(b[26:], 7)
	o.PutUint64(b[28:], uint64(len(psir)))
	o.PutUint64(b[36:], uint64(len(b)))
	return append(b, psir...)
}

func TestIPTCDigest_BigTIFF(t *testing.T) {
	t.Parallel()
	b := bigTIFFWithPSIR(psirStream)
	if got := IPTCDigest(b); !bytes.Equal(got, psirDigest) {
		t.Errorf("BigTIFF IPTCDigest = %x, want %x", got, psirDigest)
	}
	// Count overflowing the element-size multiplication must be rejected.
	bad := bytes.Clone(b)
	binary.LittleEndian.PutUint16(bad[26:], 16) // LONG8
	binary.LittleEndian.PutUint64(bad[28:], ^uint64(0))
	if got := IPTCDigest(bad); got != nil {
		t.Errorf("overflowing count: got %x, want nil", got)
	}
	if got := IPTCDigest(b[:20]); got != nil {
		t.Errorf("truncated BigTIFF: got %x, want nil", got)
	}
}

// FuzzIPTCDigest: arbitrary TIFF-like input never panics, and a non-nil
// digest is always exactly 16 bytes inside the input.
func FuzzIPTCDigest(f *testing.F) {
	f.Add(withHeader([]fixtureEntry{{0x8649, 7, psirStream}}))
	f.Add(bigTIFFWithPSIR(psirStream))
	f.Add([]byte("MM\x00*\x00\x00\x00\x08"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if d := IPTCDigest(data); d != nil && len(d) != 16 {
			t.Fatalf("digest of %d bytes", len(d))
		}
		if len(data) >= 8 {
			_, _ = ExtractTagValues(data, binary.LittleEndian.Uint32(data[4:]), binary.LittleEndian, true)
		}
	})
}
