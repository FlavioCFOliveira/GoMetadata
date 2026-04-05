package format

import (
	"bytes"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name  string
		magic []byte
		want  FormatID
	}{
		// JPEG: SOI marker FF D8
		{"JPEG", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01}, FormatJPEG},

		// PNG: 89 50 4E 47 0D 0A 1A 0A
		{"PNG", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}, FormatPNG},

		// WebP: RIFF????WEBP
		{"WebP", []byte{0x52, 0x49, 0x46, 0x46, 0x12, 0x34, 0x56, 0x78, 0x57, 0x45, 0x42, 0x50}, FormatWebP},

		// TIFF little-endian: "II" 0x2A 0x00
		{"TIFF LE", []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, FormatTIFF},

		// TIFF big-endian: "MM" 0x00 0x2A
		{"TIFF BE", []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00}, FormatTIFF},

		// CR2: TIFF LE with "CR" at bytes 8–9
		{"CR2", []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x43, 0x52, 0x02, 0x00}, FormatCR2},

		// ORF: "IIRO" little-endian Olympus marker
		{"ORF", []byte{0x49, 0x49, 0x52, 0x4F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, FormatORF},

		// RW2: "IIU\x00"
		{"RW2", []byte{0x49, 0x49, 0x55, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, FormatRW2},

		// HEIF/HEIC: ftyp box with heic brand
		{"HEIF", []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70, 0x68, 0x65, 0x69, 0x63}, FormatHEIF},

		// CR3: ftyp box with crx  brand (Canon ISOBMFF RAW)
		{"CR3", []byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70, 0x63, 0x72, 0x78, 0x20}, FormatCR3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Detect(bytes.NewReader(tc.magic))
			if err != nil {
				t.Fatalf("Detect() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Detect() = %v (%d), want %v (%d)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestDetectUnknown(t *testing.T) {
	unknown := [][]byte{
		{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		{0x25, 0x50, 0x44, 0x46, 0x2D, 0x31, 0x2E, 0x34, 0x00, 0x00, 0x00, 0x00}, // PDF
		{'G', 'I', 'F', '8', '9', 'a', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},       // GIF
	}
	for _, b := range unknown {
		got, err := Detect(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("Detect() unexpected error: %v", err)
		}
		if got != FormatUnknown {
			t.Errorf("Detect(%x) = %v, want FormatUnknown", b, got)
		}
	}
}

func TestDetectTruncated(t *testing.T) {
	cases := [][]byte{
		{},
		{0xFF},
		{0xFF, 0xD8},
	}
	for _, b := range cases {
		// Must not panic; result may be Unknown or JPEG (for 0xFF 0xD8).
		_, err := Detect(bytes.NewReader(b))
		_ = err // short reads may return an error but must not panic
	}
}

func TestDetectSeekReset(t *testing.T) {
	// Detect must leave the reader at position 0 after detection.
	magic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	r := bytes.NewReader(magic)

	if _, err := Detect(r); err != nil {
		t.Fatalf("Detect: %v", err)
	}

	// Reader should still be at position 0 so the caller can re-read.
	pos, _ := r.Seek(0, 1)
	if pos != 0 {
		t.Errorf("reader position after Detect = %d, want 0", pos)
	}
}

func TestAVIFDetect(t *testing.T) {
	// All AVIF brands (ISO 23008-12 §B.4) must resolve to FormatAVIF.
	brands := []struct {
		name  string
		brand [4]byte
	}{
		{"avif", [4]byte{'a', 'v', 'i', 'f'}},
		{"avis", [4]byte{'a', 'v', 'i', 's'}},
		{"av01", [4]byte{'a', 'v', '0', '1'}},
	}
	for _, b := range brands {
		// Build a minimal ftyp box: size(4) + "ftyp"(4) + brand(4)
		magic := []byte{
			0x00, 0x00, 0x00, 0x14, // box size
			0x66, 0x74, 0x79, 0x70, // "ftyp"
			b.brand[0], b.brand[1], b.brand[2], b.brand[3],
		}
		got, err := Detect(bytes.NewReader(magic))
		if err != nil {
			t.Fatalf("Detect(%s): %v", b.name, err)
		}
		if got != FormatAVIF {
			t.Errorf("Detect(%s) = %v, want FormatAVIF", b.name, got)
		}
	}
}

func TestAVIFSupportsWrite(t *testing.T) {
	if !SupportsWrite(FormatAVIF) {
		t.Error("SupportsWrite(FormatAVIF) = false, want true")
	}
}

func TestAVIFString(t *testing.T) {
	if got := FormatAVIF.String(); got != "AVIF" {
		t.Errorf("FormatAVIF.String() = %q, want %q", got, "AVIF")
	}
}

func TestDetectMagic(t *testing.T) {
	// Test detectMagic directly for edge cases.
	if got := detectMagic([]byte{0xFF}); got != FormatUnknown {
		t.Errorf("detectMagic(1 byte) = %v, want FormatUnknown", got)
	}
	if got := detectMagic(nil); got != FormatUnknown {
		t.Errorf("detectMagic(nil) = %v, want FormatUnknown", got)
	}
}

// buildTIFFWithMakeTag builds a minimal little-endian TIFF with a single
// Make entry (tag 0x010F) pointing to the given make string. The result
// is a complete, structurally valid TIFF byte slice.
func buildTIFFWithMakeTag(makeStr string) []byte {
	makeBytes := []byte(makeStr + "\x00") // NUL-terminated ASCII
	makeLen := uint32(len(makeBytes))     //nolint:gosec // G115: test helper, intentional type cast

	// Layout: header(8) + IFD_count(2) + 1_entry(12) + next_IFD(4) + value_area
	valueOffset := uint32(8 + 2 + 12 + 4)
	total := int(valueOffset) + len(makeBytes)
	buf := make([]byte, total)

	// TIFF header: "II" + 0x002A + IFD0 at offset 8.
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = 0x08 // IFD0 offset = 8 (LE)

	// IFD: 1 entry.
	buf[8], buf[9] = 0x01, 0x00 // entry count = 1

	// Entry: tag=0x010F (Make), type=2 (ASCII), count=makeLen, offset=valueOffset.
	buf[10], buf[11] = 0x0F, 0x01 // tag 0x010F LE
	buf[12], buf[13] = 0x02, 0x00 // TypeASCII
	buf[14] = byte(makeLen)       //nolint:gosec // G115: test helper, intentional type cast
	buf[18] = byte(valueOffset)   // value offset (LE, fits in 1 byte)

	// next-IFD pointer = 0.
	// value area.
	copy(buf[valueOffset:], makeBytes)

	return buf
}

// buildTIFFWithDNGTag builds a minimal little-endian TIFF with a DNGVersion
// tag (0xC612), which is the canonical DNG marker (Adobe DNG Spec §6).
func buildTIFFWithDNGTag() []byte {
	// DNGVersion value fits inline (4 bytes: major.minor.patch.patch2).
	buf := make([]byte, 8+2+12+4)
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = 0x08

	buf[8], buf[9] = 0x01, 0x00 // 1 entry

	// Entry: tag=0xC612 (DNGVersion), type=1 (BYTE), count=4, inline value.
	buf[10], buf[11] = 0x12, 0xC6 // tag 0xC612 LE
	buf[12], buf[13] = 0x01, 0x00 // TypeByte
	buf[14] = 0x04                // count = 4
	// value [18..21] = 0x01,0x04,0x00,0x00 (DNG 1.4)
	buf[18] = 0x01
	buf[19] = 0x04

	return buf
}

// TestRefineTIFFVariant verifies that Detect correctly identifies DNG, NEF,
// and ARW from TIFF-magic files by reading IFD0 tags.
func TestRefineTIFFVariant(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want FormatID
	}{
		{
			name: "DNG via DNGVersion tag",
			data: buildTIFFWithDNGTag(),
			want: FormatDNG,
		},
		{
			name: "NEF via Make=NIKON CORPORATION",
			data: buildTIFFWithMakeTag("NIKON CORPORATION"),
			want: FormatNEF,
		},
		{
			name: "NEF via Make=Nikon",
			data: buildTIFFWithMakeTag("Nikon"),
			want: FormatNEF,
		},
		{
			name: "ARW via Make=SONY",
			data: buildTIFFWithMakeTag("SONY"),
			want: FormatARW,
		},
		{
			name: "Generic TIFF: unknown make",
			data: buildTIFFWithMakeTag("Unknown Camera Co"),
			want: FormatTIFF,
		},
		{
			name: "Generic TIFF: no make tag",
			data: func() []byte {
				// Minimal TIFF with 0 entries.
				buf := make([]byte, 14)
				buf[0], buf[1] = 'I', 'I'
				buf[2], buf[3] = 0x2A, 0x00
				buf[4] = 0x08
				return buf
			}(),
			want: FormatTIFF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Detect(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Detect() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Detect() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDetectSeekAfterRefinement verifies that Detect leaves the reader at
// position 0 even after TIFF-variant refinement reads additional bytes.
func TestDetectSeekAfterRefinement(t *testing.T) {
	data := buildTIFFWithDNGTag()
	r := bytes.NewReader(data)
	if _, err := Detect(r); err != nil {
		t.Fatalf("Detect: %v", err)
	}
	pos, _ := r.Seek(0, 1)
	if pos != 0 {
		t.Errorf("reader position after Detect = %d, want 0", pos)
	}
}
