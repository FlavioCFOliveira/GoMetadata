package format

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestDetect(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestAVIFMIF1Brand is the regression gate for finding #137.
// A file with major_brand='mif1' and 'avif' in compatible_brands is a valid AVIF
// file (libavif emits this pattern). Before the fix, detectHEIFBrand only inspected
// the 4-byte major brand, so 'mif1' fell through to FormatHEIF.
//
// ISO 23008-12 §B.4: a conformant AVIF reader MUST accept files with 'avif' in
// compatible_brands even when the major brand differs.
func TestAVIFMIF1Brand(t *testing.T) {
	// AVIF-brand-mif1-major-avif-compat: §B.4 — mif1 major + avif compatible → FormatAVIF.
	t.Parallel()

	tests := []struct {
		name         string
		major        [4]byte
		compatBrands [][4]byte
		wantFormat   FormatID
	}{
		{
			name:  "mif1-major-avif-compat",
			major: [4]byte{'m', 'i', 'f', '1'},
			compatBrands: [][4]byte{
				{'m', 'i', 'a', 'f'}, // MIAF brand
				{'a', 'v', 'i', 'f'}, // AVIF brand in compatible_brands
			},
			wantFormat: FormatAVIF,
		},
		{
			name:  "mif1-major-avis-compat",
			major: [4]byte{'m', 'i', 'f', '1'},
			compatBrands: [][4]byte{
				{'a', 'v', 'i', 's'}, // AVIF sequence
			},
			wantFormat: FormatAVIF,
		},
		{
			name:  "mif1-major-no-avif-compat",
			major: [4]byte{'m', 'i', 'f', '1'},
			compatBrands: [][4]byte{
				{'m', 'i', 'a', 'f'}, // MIAF only — not AVIF
			},
			wantFormat: FormatHEIF, // mif1 without avif compat → HEIF
		},
		{
			name:         "mif1-major-empty-compat",
			major:        [4]byte{'m', 'i', 'f', '1'},
			compatBrands: nil,
			wantFormat:   FormatHEIF,
		},
		{
			name:       "MA1B-major",
			major:      [4]byte{'M', 'A', '1', 'B'}, // MIAF baseline brand → AVIF
			wantFormat: FormatAVIF,
		},
		{
			name:       "MA1A-major",
			major:      [4]byte{'M', 'A', '1', 'A'}, // MIAF high-tier brand → AVIF
			wantFormat: FormatAVIF,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a synthetic ftyp box: size(4) + "ftyp"(4) + major(4) + minor_version(4) + compat[].
			totalSize := 16 + 4*len(tc.compatBrands)
			buf := make([]byte, totalSize)
			binary.BigEndian.PutUint32(buf[0:], uint32(totalSize)) //nolint:gosec // G115: test helper, bounded
			copy(buf[4:], "ftyp")
			copy(buf[8:], tc.major[:])
			// minor_version = 0 at buf[12:16]
			for i, cb := range tc.compatBrands {
				copy(buf[16+i*4:], cb[:])
			}
			got, err := Detect(bytes.NewReader(buf))
			if err != nil {
				t.Fatalf("Detect(%s): %v", tc.name, err)
			}
			if got != tc.wantFormat {
				t.Errorf("Detect(%s) = %v, want %v", tc.name, got, tc.wantFormat)
			}
		})
	}
}

func TestAVIFSupportsWrite(t *testing.T) {
	t.Parallel()
	if !SupportsWrite(FormatAVIF) {
		t.Error("SupportsWrite(FormatAVIF) = false, want true")
	}
}

func TestAVIFString(t *testing.T) {
	t.Parallel()
	if got := FormatAVIF.String(); got != "AVIF" {
		t.Errorf("FormatAVIF.String() = %q, want %q", got, "AVIF")
	}
}

// TestFormatIDStringOutOfRange covers the out-of-range branch in String().
// FormatID values beyond the end of the formatNames array should return "Unknown".
func TestFormatIDStringOutOfRange(t *testing.T) {
	t.Parallel()
	// 255 is well beyond the end of the formatNames array.
	f := FormatID(255)
	if got := f.String(); got != "Unknown" {
		t.Errorf("FormatID(255).String() = %q, want %q", got, "Unknown")
	}
}

func TestDetectMagic(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
			t.Parallel()
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

// TestSupportsWrite exercises SupportsWrite for known writable, non-writable,
// and unknown format IDs.
//
// Tasks #92/#93 (epic #33 Option A): FormatTIFF is writable as of this release.
// The copy-and-relocate serializer in format/tiff/relocate.go preserves
// image-data blocks (strips, tiles, main-image JPEG) at corrected absolute
// offsets, eliminating the SPIKE #6 corruption risk for plain TIFF files.
//
// Task #98 (SubIFD OOL value fix): FormatDNG is now writable. The SubIFD
// relocation path preserves ALL out-of-line value areas (RATIONAL
// XResolution/YResolution, etc.) by updating their valOrOff pointers to the
// new absolute positions after relocation.
//
// Task #95: FormatCR2 is now writable. CR2 uses standard LE TIFF magic (II*\0)
// and routes through the same writeTIFF copy-and-relocate path as FormatTIFF/DNG.
// Canon MakerNote blobs are copied verbatim (SPIKE #24: blob-relative offsets).
//
// Task #102: FormatNEF is now writable. The NEF-specific write path extends the
// Nikon Type-3 MakerNote blob to cover PreviewIFD and NikonScanIFD, enumerates
// the PreviewIFD image block, and patches MakerNote-relative offsets after encoding.
//
// Task #103: FormatARW is now writable. The ARW-specific write path rebases all
// Sony MakerNote TIFF-absolute OOL offsets and relocates the SR2Private (0xC634)
// block (encrypted SR2SubIFD + IDC_IFD) with internal pointer rebasing.
// Validated against a real Sony DSLR-A500 ARW: ImageDataHash IN==OUT, all 52
// MakerNote tags + SR2Private preserved.
//
// Task #104: FormatORF and FormatRW2 are now writable. ORF uses the ORF-specific
// write path (magic patching for IIRO/IIRS). RW2 uses the RW2-specific write path
// (GUID preservation + offset rebasing). Both validated against real corpus files.
//
// CR3 returns true as of task #91: cr3.Inject now implements stco/co64 offset
// relocation, walking every trak/stbl/{stco,co64} table inside the rebuilt
// moov and adding delta to each absolute offset >= the original moov end.
func TestSupportsWrite(t *testing.T) {
	t.Parallel()

	// Task #104: ORF and RW2 are now writable — ORF IIRO/IIRS magic patching and
	// RW2 GUID insertion + offset rebasing validated against real corpus files.
	writable := []FormatID{
		FormatJPEG, FormatTIFF, FormatDNG, FormatPNG, FormatHEIF, FormatAVIF, FormatWebP, FormatCR3,
		FormatCR2, FormatNEF, FormatARW, FormatORF, FormatRW2,
	}
	for _, f := range writable {
		if !SupportsWrite(f) {
			t.Errorf("SupportsWrite(%v) = false, want true", f)
		}
	}

	// Only FormatUnknown and out-of-range IDs should return false.
	notWritable := []FormatID{
		FormatUnknown,
	}
	for _, f := range notWritable {
		if SupportsWrite(f) {
			t.Errorf("SupportsWrite(%v) = true, want false", f)
		}
	}

	// An out-of-range FormatID should return false.
	if SupportsWrite(FormatID(255)) {
		t.Error("SupportsWrite(255) = true, want false")
	}
}

// TestDetectBigTIFF verifies that Detect recognises BigTIFF magic (0x002B) for
// both byte orders and returns FormatTIFF (BigTIFF routes to tiff.Extract,
// which handles both classic and BigTIFF internally).
//
// BigTIFF spec (Aware Systems / libtiff) §2: magic = 43 (0x002B).
// Task #54: prior to this fix, only classic magic 0x002A was recognised.
func TestDetectBigTIFF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		magic []byte
	}{
		{
			// BigTIFF little-endian: "II" + 0x2B 0x00 + offsetBytesize(2) + constant(2) + ifd0Off(8)
			name: "BigTIFF_LE",
			magic: []byte{
				0x49, 0x49, 0x2B, 0x00, // "II" + magic 0x002B (LE)
				0x08, 0x00, // offsetBytesize = 8 (LE)
				0x00, 0x00, // constant = 0
				0x10, 0x00, 0x00, 0x00, // IFD0 offset = 16 (LE uint64 low 32)
			},
		},
		{
			// BigTIFF big-endian: "MM" + 0x00 0x2B + offsetBytesize(2) + constant(2) + ifd0Off(8)
			name: "BigTIFF_BE",
			magic: []byte{
				0x4D, 0x4D, 0x00, 0x2B, // "MM" + magic 0x002B (BE)
				0x00, 0x08, // offsetBytesize = 8 (BE)
				0x00, 0x00, // constant = 0
				0x00, 0x00, 0x00, 0x00, // IFD0 offset = 16 (BE uint64 high 32)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Detect(bytes.NewReader(tc.magic))
			if err != nil {
				t.Fatalf("Detect(%s) error = %v", tc.name, err)
			}
			if got != FormatTIFF {
				t.Errorf("Detect(%s) = %v, want FormatTIFF", tc.name, got)
			}
		})
	}
}

// TestDetectSeekAfterRefinement verifies that Detect leaves the reader at
// position 0 even after TIFF-variant refinement reads additional bytes.
func TestDetectSeekAfterRefinement(t *testing.T) {
	t.Parallel()
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

// buildTIFFWithInlineMakeTag builds a minimal TIFF where the Make value is
// stored inline (string ≤ 4 bytes including NUL terminator) in the IFD
// value-or-offset field. Exercises the `total <= 4` branch in findMakeTagInIFD.
func buildTIFFWithInlineMakeTag(makeStr string) []byte {
	// Layout: header(8) + IFD_count(2) + 1 entry(12) + next-IFD(4) = 26 bytes.
	buf := make([]byte, 26)
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = 0x08 // IFD0 offset = 8

	buf[8], buf[9] = 0x01, 0x00 // 1 entry

	cnt := len(makeStr)           // count = length of the make string (no NUL needed for ≤4)
	buf[10], buf[11] = 0x0F, 0x01 // tag 0x010F (Make)
	buf[12], buf[13] = 0x02, 0x00 // TypeASCII
	buf[14] = byte(cnt)           //nolint:gosec // G115: test helper
	copy(buf[18:22], makeStr)     // inline value in 4-byte field
	return buf
}

// TestFindMakeTagInIFDInline verifies that findMakeTagInIFD reads the Make
// tag correctly when it is stored inline (string ≤ 4 bytes).
func TestFindMakeTagInIFDInline(t *testing.T) {
	t.Parallel()
	// "IXY" is 3 bytes — stored inline. mapMakeToFormat won't match it exactly,
	// so the result is FormatTIFF.
	data := buildTIFFWithInlineMakeTag("IXY")
	got, err := Detect(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Detect inline make: %v", err)
	}
	if got != FormatTIFF {
		t.Errorf("Detect inline make = %v, want FormatTIFF", got)
	}
}

// TestFindMakeTagInIFDNonASCIIType verifies that findMakeTagInIFD skips a Make
// tag whose type is not ASCII (typ != 2), exercising the `break` path.
func TestFindMakeTagInIFDNonASCIIType(t *testing.T) {
	t.Parallel()
	// Build a TIFF where Make (0x010F) has type=SHORT (3) instead of ASCII.
	buf := make([]byte, 26)
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = 0x08
	buf[8], buf[9] = 0x01, 0x00   // 1 entry
	buf[10], buf[11] = 0x0F, 0x01 // tag 0x010F (Make)
	buf[12], buf[13] = 0x03, 0x00 // type=SHORT (not ASCII)
	buf[14] = 0x01                // count=1

	got, err := Detect(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Detect non-ASCII make type: %v", err)
	}
	if got != FormatTIFF {
		t.Errorf("Detect non-ASCII make type = %v, want FormatTIFF", got)
	}
}

// TestParseTIFFScanHeaderBigEndian verifies that parseTIFFScanHeader correctly
// parses a big-endian ("MM") TIFF header so refineTIFFVariant can read it.
func TestParseTIFFScanHeaderBigEndian(t *testing.T) {
	t.Parallel()
	// Minimal big-endian TIFF: "MM" + 0x002A + IFD0 at offset 8 + 0 entries.
	buf := make([]byte, 14)
	buf[0], buf[1] = 'M', 'M'
	buf[2], buf[3] = 0x00, 0x2A                             // magic BE
	buf[4], buf[5], buf[6], buf[7] = 0x00, 0x00, 0x00, 0x08 // IFD0 offset=8 BE
	buf[8], buf[9] = 0x00, 0x00                             // 0 entries

	got, err := Detect(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Detect BE TIFF: %v", err)
	}
	if got != FormatTIFF {
		t.Errorf("Detect BE TIFF = %v, want FormatTIFF", got)
	}
}

// TestRefineTIFFVariantCountTooHigh verifies that parseTIFFScanHeader returns
// false (and refineTIFFVariant falls back to FormatTIFF) when the IFD0 entry
// count exceeds the 512-entry sanity limit.
func TestRefineTIFFVariantCountTooHigh(t *testing.T) {
	t.Parallel()
	// Build a minimal TIFF with count=600 (>512) — should be treated as unknown.
	buf := make([]byte, 14)
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = 0x08
	// IFD0 entry count = 600 in LE.
	buf[8] = byte(600 & 0xFF)
	buf[9] = byte((600 >> 8) & 0xFF)

	got, err := Detect(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Detect with count=600: %v", err)
	}
	if got != FormatTIFF {
		t.Errorf("Detect with count=600 = %v, want FormatTIFF", got)
	}
}

// TestParseTIFFScanHeaderIFD0OffsetBeyondData verifies that parseTIFFScanHeader
// returns false when the IFD0 offset points beyond the available data.
// This exercises the int(ifd0Off)+2 > len(data) guard.
func TestParseTIFFScanHeaderIFD0OffsetBeyondData(t *testing.T) {
	t.Parallel()
	// Build a 10-byte LE TIFF with IFD0 offset = 0xFF00 (far beyond the file).
	buf := make([]byte, 10)
	buf[0], buf[1] = 'I', 'I'
	buf[2], buf[3] = 0x2A, 0x00
	buf[4] = 0x00 // IFD0 offset = 0xFF00 in LE
	buf[5] = 0xFF
	buf[6] = 0x00
	buf[7] = 0x00
	// Only 10 bytes total; ifd0Off=0xFF00 is way beyond data → parseTIFFScanHeader returns false.

	got, err := Detect(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("Detect with OOB IFD0 offset: %v", err)
	}
	// Must fall back to FormatTIFF or FormatUnknown — not crash.
	if got != FormatTIFF && got != FormatUnknown {
		t.Errorf("Detect with OOB IFD0 offset = %v, want FormatTIFF or FormatUnknown", got)
	}
}

// TestDetectOutOfScopeFormats verifies that magic bytes from RAW formats that
// are outside the library's current scope (doc.go §Out-of-scope formats)
// are detected as FormatUnknown without panicking. All of these return
// UnsupportedFormatError when passed to the top-level Read/Write functions.
//
// Magic byte sources:
//
//   - CRW / CIFF: starts with "II" followed by 0x1A 0x00 (little-endian CIFF marker)
//   - RAF:        "FUJIFILMCCD-RAW " (16-byte ASCII header)
//   - MRW:        "\x00MRM" (Minolta big-endian marker)
//   - IIQ:        "IIQPhaseOne" or the Phase One 0x49 49 header variant recognised as
//     a plain TIFF by detectMagic; the test uses the IIQ-specific marker
//   - X3F:        "FOVb" (Sigma/Foveon)
//   - SRW:        Samsung RAW uses standard TIFF magic — returns FormatTIFF or
//     FormatUnknown depending on IFD content; not tested separately (it
//     falls through to the TIFF path and is treated as TIFF/Unknown)
//   - PEF:        standard TIFF magic with Pentax Make; falls through to TIFF
//   - RWL:        standard TIFF magic with Leica Make; falls through to TIFF
//
// The formats that share standard TIFF magic (SRW, PEF, RWL) are not included
// below because detectMagic legitimately returns FormatTIFF for them — they are
// disambiguated by IFD content, which this library does not yet handle. The
// formats with unique magic bytes are tested to confirm FormatUnknown.
func TestDetectOutOfScopeFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		magic []byte
	}{
		{
			// CRW / CIFF: Canon RAW v1. Magic: "II" + 0x1A 0x00 (CIFF little-endian).
			// Distinct from TIFF because byte 2 is 0x1A, not 0x2A.
			// Reference: Canon CIFF specification §2.
			name:  "CRW/CIFF",
			magic: []byte{0x49, 0x49, 0x1A, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			// RAF: Fujifilm RAW. First 8 bytes are the ASCII string "FUJIFILM".
			// Reference: libraw / ExifTool source.
			name:  "RAF",
			magic: []byte{'F', 'U', 'J', 'I', 'F', 'I', 'L', 'M', 'C', 'C', 'D', '-'},
		},
		{
			// MRW: Minolta RAW. Magic: 0x00 'M' 'R' 'M'.
			// Reference: ExifTool lib/Image/ExifTool/MinoltaRaw.pm.
			name:  "MRW",
			magic: []byte{0x00, 0x4D, 0x52, 0x4D, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		{
			// X3F: Sigma/Foveon RAW. Magic: "FOVb".
			// Reference: ExifTool lib/Image/ExifTool/SigmaRaw.pm.
			name:  "X3F",
			magic: []byte{0x46, 0x4F, 0x56, 0x62, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Detect(bytes.NewReader(tc.magic))
			if err != nil {
				t.Fatalf("Detect(%s): unexpected error: %v", tc.name, err)
			}
			if got != FormatUnknown {
				t.Errorf("Detect(%s) = %v, want FormatUnknown (format is out-of-scope)", tc.name, got)
			}
		})
	}
}

// TestCR2DetectionRequiresValidation is the regression gate for finding #136.
//
// Prior to the fix, detectTIFFVariant returned FormatCR2 for any TIFF where
// bytes [8:10] happened to be 0x43 0x52 ("CR"), regardless of the version byte
// at offset 10. A generic little-endian TIFF whose first-IFD tag encodes as
// 0x43 0x52 in the IFD count field (bytes [8:9]) would be misclassified as CR2.
//
// Canon CR2 Specification §3.1: bytes[8:9]="CR", byte[10]=0x02 (major version).
// The fix requires byte[10]==0x02 before accepting FormatCR2.
func TestCR2DetectionRequiresValidation(t *testing.T) {
	t.Parallel()

	// (a) Classic TIFF LE with bytes[8:10]=="CR" but version byte[10]=0x00.
	// This must NOT be detected as CR2 — it is a generic TIFF.
	t.Run("fake-CR-bytes-no-version", func(t *testing.T) {
		t.Parallel()
		// 12-byte TIFF header: II + 0x2A 0x00 + IFD0_at_8 | bytes[8:10]="CR", [10]=0x00
		buf := []byte{
			0x49, 0x49, // "II" LE byte-order marker
			0x2A, 0x00, // classic TIFF magic = 42 (0x002A LE)
			0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
			0x43, 0x52, // "CR" at bytes [8:9]
			0x00, 0x00, // version = 0x00, NOT 0x02 — not a real CR2
		}
		got, err := Detect(bytes.NewReader(buf))
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		// Must return FormatTIFF (after IFD scan fallback), NOT FormatCR2.
		if got == FormatCR2 {
			t.Errorf("#136 regression: Detect = FormatCR2, want FormatTIFF (fake CR bytes with version=0x00)")
		}
	})

	// (b) Valid CR2 header: bytes[8:9]=="CR", byte[10]=0x02, byte[11]=0x00.
	// This MUST be detected as CR2.
	t.Run("valid-CR2-version-2", func(t *testing.T) {
		t.Parallel()
		// 12-byte CR2 header: II + 0x2A 0x00 + IFD0_at_8 | "CR" + version 2
		buf := []byte{
			0x49, 0x49, // "II" LE
			0x2A, 0x00, // TIFF magic
			0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
			0x43, 0x52, // "CR" — Canon CR2 identifier
			0x02, 0x00, // version 2.0 — canonical CR2 marker
		}
		got, err := Detect(bytes.NewReader(buf))
		if err != nil {
			t.Fatalf("Detect: %v", err)
		}
		if got != FormatCR2 {
			t.Errorf("#136 regression: Detect = %v, want FormatCR2 for valid CR2 header", got)
		}
	})
}

// TestDetectTIFFHighIFD0Offset is the 32-bit-safe regression test for task #74.
//
// parseTIFFScanHeader reads IFD0 offset as a uint32 and historically used
// `int(ifd0Off)+2 > len(data)` as the bounds guard. On a 32-bit platform
// (GOARCH=386/arm, int=32 bits), an IFD0 offset >= 0x80000000 becomes negative
// after the int cast; the guard passes; and `data[ifd0Off:]` panics with
// slice-bounds-out-of-range. After the fix, the comparison is done in uint64.
//
// On 64-bit platforms the test is still meaningful: the tiffScanSize buffer
// (1034 bytes) is always smaller than 0x80000000, so the uint64 guard fires and
// Detect returns FormatTIFF (the fallback) without panicking.
func TestDetectTIFFHighIFD0Offset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		order   binary.ByteOrder
		magic   [2]byte
		ifd0Off uint32
	}{
		// LE TIFF with IFD0 offset == 0x80000000
		{"LE 0x80000000", binary.LittleEndian, [2]byte{'I', 'I'}, 0x80000000},
		// BE TIFF with IFD0 offset == 0x80000000
		{"BE 0x80000000", binary.BigEndian, [2]byte{'M', 'M'}, 0x80000000},
		// LE TIFF with IFD0 offset == 0xFFFFFFFF (max uint32)
		{"LE 0xFFFFFFFF", binary.LittleEndian, [2]byte{'I', 'I'}, 0xFFFFFFFF},
		// BE TIFF with IFD0 offset == 0xFFFFFFFE
		{"BE 0xFFFFFFFE", binary.BigEndian, [2]byte{'M', 'M'}, 0xFFFFFFFE},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a 12-byte buffer: TIFF magic (4 bytes) + 42 marker (2 bytes) +
			// IFD0 offset (4 bytes) + 2 padding bytes. The IFD0 offset is always
			// beyond the buffer, so parseTIFFScanHeader must reject it gracefully.
			buf := make([]byte, 12)
			buf[0] = tc.magic[0]
			buf[1] = tc.magic[1]
			tc.order.PutUint16(buf[2:], 0x002A)
			tc.order.PutUint32(buf[4:], tc.ifd0Off)

			// Detect must not panic; it should return FormatTIFF (the safe fallback
			// when refineTIFFVariant cannot read a valid IFD0) without panicking.
			got, err := Detect(bytes.NewReader(buf))
			if err != nil {
				t.Fatalf("Detect() unexpected error: %v", err)
			}
			if got != FormatTIFF {
				t.Errorf("Detect() = %v, want FormatTIFF for unreadable IFD0 offset 0x%08X", got, tc.ifd0Off)
			}
		})
	}
}
