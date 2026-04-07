// file: roundtrip_test.go

// Package gometadata_test contains end-to-end round-trip tests that exercise
// the public Read → modify → Write → Read cycle for every supported container
// format and metadata type combination.
//
// Each test case:
//  1. Builds a minimal valid container image in memory.
//  2. Calls Read to parse metadata.
//  3. Modifies metadata fields via the format-native setters.
//  4. Calls Write to produce a new image.
//  5. Calls Read again on the output and asserts the values survived.
package gometadata_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// ---------------------------------------------------------------------------
// Container builders — produce minimal but structurally valid images in memory.
// These mirror the private helpers used in format/*_test.go files but live here
// so the external test package has no compile-time dependency on unexported
// symbols.
// ---------------------------------------------------------------------------

// rtBuildJPEG returns a minimal JPEG byte stream.
// exifData is the raw TIFF payload (without the "Exif\x00\x00" prefix);
// pass nil to omit the APP1 EXIF segment.
func rtBuildJPEG(exifData []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	if exifData != nil {
		// APP1 with Exif header (JEITA CP-3451 §4.5.4).
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, intentional type cast
		buf.Write([]byte{0xFF, 0xE1})
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], length)
		buf.Write(lb[:])
		buf.Write(payload)
	}

	// Minimal SOS + EOI: SOS marker, length=2, then EOI.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// rtMinimalTIFF builds a tiny but valid TIFF/EXIF payload (little-endian,
// one IFD0 entry: ImageDescription = "ok\x00").
func rtMinimalTIFF() []byte {
	const desc = "ok\x00"
	order := binary.LittleEndian

	// Layout: header(8) + ifd_count(2) + entry(12) + next_ifd(4) + value_area
	valueOff := uint32(8 + 2 + 12 + 4)
	buf := make([]byte, int(valueOff)+len(desc))

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	order.PutUint16(buf[8:], 1) // 1 entry

	// Tag 0x010E ImageDescription (EXIF §4.6.3), TypeASCII.
	p := buf[10:]
	order.PutUint16(p[0:], 0x010E)            // tag
	order.PutUint16(p[2:], 0x0002)            // TypeASCII
	order.PutUint32(p[4:], uint32(len(desc))) // count
	order.PutUint32(p[8:], valueOff)          // value offset

	order.PutUint32(buf[10+12:], 0) // next IFD = 0

	copy(buf[valueOff:], desc)
	return buf
}

// rtPNGWriteChunk appends a PNG chunk with correct CRC to buf.
func rtPNGWriteChunk(buf *bytes.Buffer, chunkType string, data []byte) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data))) //nolint:gosec // G115: test helper, intentional type cast
	buf.Write(hdr[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	binary.BigEndian.PutUint32(hdr[:], h.Sum32())
	buf.Write(hdr[:])
}

// rtBuildPNG returns a minimal PNG byte stream (8-byte sig + IHDR + IEND).
// xmpData is injected as an iTXt chunk with keyword "XML:com.adobe.xmp" when
// non-nil. exifData is injected as an eXIf chunk when non-nil.
func rtBuildPNG(exifData, xmpData []byte) []byte {
	// PNG signature (PNG §5.2).
	sig := [8]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	var buf bytes.Buffer
	buf.Write(sig[:])

	// Minimal IHDR: 1×1 RGB8.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1) // width
	binary.BigEndian.PutUint32(ihdr[4:], 1) // height
	ihdr[8] = 8                             // bit depth
	ihdr[9] = 2                             // colour type RGB
	rtPNGWriteChunk(&buf, "IHDR", ihdr)

	if exifData != nil {
		rtPNGWriteChunk(&buf, "eXIf", exifData)
	}
	if xmpData != nil {
		// iTXt chunk with keyword "XML:com.adobe.xmp" (XMP Part 3 §1.1.4).
		const kw = "XML:com.adobe.xmp"
		var chunk bytes.Buffer
		chunk.WriteString(kw)
		chunk.WriteByte(0x00) // null terminator after keyword
		chunk.WriteByte(0x00) // compression flag = not compressed
		chunk.WriteByte(0x00) // compression method (ignored when not compressed)
		chunk.WriteByte(0x00) // empty language tag
		chunk.WriteByte(0x00) // empty translated keyword
		chunk.Write(xmpData)
		rtPNGWriteChunk(&buf, "iTXt", chunk.Bytes())
	}

	rtPNGWriteChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

// rtBuildWebP returns a minimal RIFF/WebP byte stream.
// vp8xFlags should include 0x08 for EXIF and/or 0x04 for XMP when those
// chunks are present (VP8X feature flags per WebP specification §Extended).
func rtBuildWebP(exifData, xmpData []byte, vp8xFlags uint32) []byte {
	var body bytes.Buffer

	// VP8X chunk is required when EXIF or XMP chunks are present.
	if exifData != nil || xmpData != nil || vp8xFlags != 0 {
		payload := make([]byte, 10)
		binary.LittleEndian.PutUint32(payload[0:], vp8xFlags)
		// canvas: 1×1 (stored as width-1, height-1 in 3 bytes each)
		// All zero bytes = 0+1 = 1 pixel.
		rtWriteWebPChunk(&body, "VP8X", payload)
	}

	// Minimal VP8 lossy bitstream stub (10 bytes) so the container is recognised.
	vp8stub := []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}
	rtWriteWebPChunk(&body, "VP8 ", vp8stub)

	if exifData != nil {
		rtWriteWebPChunk(&body, "EXIF", exifData)
	}
	if xmpData != nil {
		rtWriteWebPChunk(&body, "XMP ", xmpData)
	}

	// RIFF header: "RIFF" + 4-byte body size (body includes "WEBP") + "WEBP".
	totalBodySize := uint32(4 + body.Len()) //nolint:gosec // G115: test helper, intentional type cast
	var out bytes.Buffer
	out.WriteString("RIFF")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], totalBodySize)
	out.Write(sz[:])
	out.WriteString("WEBP")
	out.Write(body.Bytes())
	return out.Bytes()
}

// rtWriteWebPChunk appends a RIFF chunk (4-byte FourCC + 4-byte LE size + data
// + optional padding byte for odd-sized payloads).
func rtWriteWebPChunk(buf *bytes.Buffer, fourCC string, data []byte) {
	buf.WriteString(fourCC)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(data))) //nolint:gosec // G115: test helper, intentional type cast
	buf.Write(sz[:])
	buf.Write(data)
	if len(data)%2 != 0 {
		buf.WriteByte(0x00) // RIFF alignment padding
	}
}

// ---------------------------------------------------------------------------
// Test cases
// ---------------------------------------------------------------------------

// TestRoundTripIPTC_JPEG verifies that IPTC caption, copyright, and keywords
// written to a JPEG survive a complete Read→Write→Read cycle.
func TestRoundTripIPTC_JPEG(t *testing.T) {
	t.Parallel()
	const (
		wantCaption   = "sunset over the bay"
		wantCopyright = "(c) 2024 Test Corp"
	)
	wantKeywords := []string{"nature", "sunset"}

	// Step 1: build a minimal JPEG with a valid (but minimal) EXIF segment so
	// Read() can correctly detect the format.
	img := rtBuildJPEG(rtMinimalTIFF())

	// Step 2: read — we expect no parsing error.
	m, err := gometadata.Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read initial: %v", err)
	}

	// Step 3: attach and populate an IPTC struct.
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption(wantCaption)
	m.IPTC.SetCopyright(wantCopyright)
	for _, kw := range wantKeywords {
		m.IPTC.AddKeyword(kw)
	}

	// Step 4: write.
	var out bytes.Buffer
	if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Step 5: read the output back.
	m2, err := gometadata.Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}

	// Step 6: assert values survived.
	if got := m2.Caption(); got != wantCaption {
		t.Errorf("Caption: got %q, want %q", got, wantCaption)
	}
	if got := m2.Copyright(); got != wantCopyright {
		t.Errorf("Copyright: got %q, want %q", got, wantCopyright)
	}
	got := m2.Keywords()
	if len(got) != len(wantKeywords) {
		t.Errorf("Keywords: got %v, want %v", got, wantKeywords)
	} else {
		for i, kw := range wantKeywords {
			if got[i] != kw {
				t.Errorf("Keywords[%d]: got %q, want %q", i, got[i], kw)
			}
		}
	}
}

// TestRoundTripXMP_JPEG verifies that XMP caption, copyright, and keywords
// written to a JPEG survive a complete Read→Write→Read cycle.
func TestRoundTripXMP_JPEG(t *testing.T) {
	t.Parallel()
	const (
		wantCaption   = "mountain lake at dawn"
		wantCopyright = "(c) 2024 Photographer"
	)
	wantKeywords := []string{"landscape", "mountain"}

	img := rtBuildJPEG(rtMinimalTIFF())

	m, err := gometadata.Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read initial: %v", err)
	}

	// Attach a fresh XMP struct and populate it.
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCaption(wantCaption)
	m.XMP.SetCopyright(wantCopyright)
	for _, kw := range wantKeywords {
		m.XMP.AddKeyword(kw)
	}

	var out bytes.Buffer
	if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := gometadata.Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}

	if got := m2.Caption(); got != wantCaption {
		t.Errorf("Caption: got %q, want %q", got, wantCaption)
	}
	if got := m2.Copyright(); got != wantCopyright {
		t.Errorf("Copyright: got %q, want %q", got, wantCopyright)
	}
	got := m2.Keywords()
	if len(got) != len(wantKeywords) {
		t.Errorf("Keywords: got %v, want %v", got, wantKeywords)
	} else {
		for i, kw := range wantKeywords {
			if got[i] != kw {
				t.Errorf("Keywords[%d]: got %q, want %q", i, got[i], kw)
			}
		}
	}
}

// TestRoundTripCaption_JPEG tests that a caption set via IPTC on a JPEG
// survives a round-trip even when no other metadata was present initially.
func TestRoundTripCaption_JPEG(t *testing.T) {
	t.Parallel()
	const wantCaption = "simple caption test"

	// A bare JPEG with no metadata at all.
	img := rtBuildJPEG(nil)

	m, err := gometadata.Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read initial: %v", err)
	}

	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption(wantCaption)

	var out bytes.Buffer
	if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := gometadata.Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got := m2.Caption(); got != wantCaption {
		t.Errorf("Caption: got %q, want %q", got, wantCaption)
	}
}

// TestRoundTripXMP_PNG verifies that XMP caption and copyright written to a
// PNG survive a complete Read→Write→Read cycle.
// IPTC is not supported by PNG containers; only XMP is tested here.
func TestRoundTripXMP_PNG(t *testing.T) {
	t.Parallel()
	const (
		wantCaption   = "forest trail in autumn"
		wantCopyright = "(c) 2024 Nature Photographer"
	)

	// Build a PNG with an EXIF segment so Read() can detect the format and
	// produce a valid Metadata with format=FormatPNG.
	exifData := []byte{
		'I', 'I', 0x2A, 0x00, // little-endian TIFF magic
		0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
		0x00, 0x00, // 0 entries
		0x00, 0x00, 0x00, 0x00, // next IFD = 0
	}
	img := rtBuildPNG(exifData, nil)

	m, err := gometadata.Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read initial PNG: %v", err)
	}

	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCaption(wantCaption)
	m.XMP.SetCopyright(wantCopyright)

	var out bytes.Buffer
	if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write PNG: %v", err)
	}

	m2, err := gometadata.Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write PNG: %v", err)
	}
	if got := m2.Caption(); got != wantCaption {
		t.Errorf("PNG Caption: got %q, want %q", got, wantCaption)
	}
	if got := m2.Copyright(); got != wantCopyright {
		t.Errorf("PNG Copyright: got %q, want %q", got, wantCopyright)
	}
}

// TestRoundTripXMP_WebP verifies that XMP caption and copyright written to a
// WebP survive a complete Read→Write→Read cycle.
// IPTC is not natively carried by WebP containers; only XMP is tested here.
func TestRoundTripXMP_WebP(t *testing.T) {
	t.Parallel()
	const (
		wantCaption   = "ocean waves at high tide"
		wantCopyright = "(c) 2024 Marine Photos"
	)

	// Build a WebP with a minimal EXIF chunk so Read() produces FormatWebP.
	// VP8X flag 0x08 = EXIF present.
	exifData := []byte{
		'I', 'I', 0x2A, 0x00,
		0x08, 0x00, 0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	img := rtBuildWebP(exifData, nil, 0x08)

	m, err := gometadata.Read(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("Read initial WebP: %v", err)
	}

	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCaption(wantCaption)
	m.XMP.SetCopyright(wantCopyright)

	var out bytes.Buffer
	if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write WebP: %v", err)
	}

	m2, err := gometadata.Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write WebP: %v", err)
	}
	if got := m2.Caption(); got != wantCaption {
		t.Errorf("WebP Caption: got %q, want %q", got, wantCaption)
	}
	if got := m2.Copyright(); got != wantCopyright {
		t.Errorf("WebP Copyright: got %q, want %q", got, wantCopyright)
	}
}

// TestRoundTripTableDriven is a comprehensive table-driven test covering the
// most important format+metadata-type combinations in a compact form.
func TestRoundTripTableDriven(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name   string
		image  func() []byte                          // build the container
		modify func(*gometadata.Metadata)             // populate metadata
		assert func(*testing.T, *gometadata.Metadata) // assert values
	}

	cases := []testCase{
		{
			name:  "JPEG+IPTC+caption",
			image: func() []byte { return rtBuildJPEG(nil) },
			modify: func(m *gometadata.Metadata) {
				m.IPTC = new(iptc.IPTC)
				m.IPTC.SetCaption("table-caption")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				if got := m.Caption(); got != "table-caption" {
					t.Errorf("Caption: got %q, want %q", got, "table-caption")
				}
			},
		},
		{
			name:  "JPEG+IPTC+copyright",
			image: func() []byte { return rtBuildJPEG(nil) },
			modify: func(m *gometadata.Metadata) {
				m.IPTC = new(iptc.IPTC)
				m.IPTC.SetCopyright("(c) 2024")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				if got := m.Copyright(); got != "(c) 2024" {
					t.Errorf("Copyright: got %q, want %q", got, "(c) 2024")
				}
			},
		},
		{
			name:  "JPEG+IPTC+keywords",
			image: func() []byte { return rtBuildJPEG(nil) },
			modify: func(m *gometadata.Metadata) {
				m.IPTC = new(iptc.IPTC)
				m.IPTC.AddKeyword("alpha")
				m.IPTC.AddKeyword("beta")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				kws := m.Keywords()
				if len(kws) != 2 || kws[0] != "alpha" || kws[1] != "beta" {
					t.Errorf("Keywords: got %v, want [alpha beta]", kws)
				}
			},
		},
		{
			name:  "JPEG+XMP+caption",
			image: func() []byte { return rtBuildJPEG(nil) },
			modify: func(m *gometadata.Metadata) {
				m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
				m.XMP.SetCaption("xmp-caption")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				if got := m.Caption(); got != "xmp-caption" {
					t.Errorf("Caption: got %q, want %q", got, "xmp-caption")
				}
			},
		},
		{
			name:  "JPEG+XMP+keyword",
			image: func() []byte { return rtBuildJPEG(nil) },
			modify: func(m *gometadata.Metadata) {
				m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
				m.XMP.AddKeyword("kw")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				kws := m.Keywords()
				if len(kws) != 1 || kws[0] != "kw" {
					t.Errorf("Keywords: got %v, want [kw]", kws)
				}
			},
		},
		{
			// PNG does not carry IPTC — XMP only.
			name:  "PNG+XMP+caption",
			image: func() []byte { return rtBuildPNG(nil, nil) },
			modify: func(m *gometadata.Metadata) {
				m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
				m.XMP.SetCaption("png-xmp-caption")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				if got := m.Caption(); got != "png-xmp-caption" {
					t.Errorf("Caption: got %q, want %q", got, "png-xmp-caption")
				}
			},
		},
		{
			// WebP does not carry IPTC — XMP only.
			name:  "WebP+XMP+copyright",
			image: func() []byte { return rtBuildWebP(nil, nil, 0) },
			modify: func(m *gometadata.Metadata) {
				m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
				m.XMP.SetCopyright("(c) webp")
			},
			assert: func(t *testing.T, m *gometadata.Metadata) {
				t.Helper()
				if got := m.Copyright(); got != "(c) webp" {
					t.Errorf("Copyright: got %q, want %q", got, "(c) webp")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			img := tc.image()

			m, err := gometadata.Read(bytes.NewReader(img))
			if err != nil {
				t.Fatalf("Read initial: %v", err)
			}

			tc.modify(m)

			var out bytes.Buffer
			if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
				t.Fatalf("Write: %v", err)
			}

			m2, err := gometadata.Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("Read after Write: %v", err)
			}

			tc.assert(t, m2)
		})
	}
}

// TestRoundTripPreservesExistingEXIF verifies that when only IPTC is modified,
// the original EXIF segment is still present and non-nil in the output.
//
// The raw EXIF bytes may differ slightly after a round-trip because Write()
// re-encodes the parsed EXIF struct (e.g., alignment or entry ordering may
// change). The invariant we enforce is that rawEXIF remains present and
// parseable, not byte-for-byte identical to the input.
func TestRoundTripPreservesExistingEXIF(t *testing.T) {
	t.Parallel()
	tiff := rtMinimalTIFF()
	img := rtBuildJPEG(tiff)

	// Read without EXIF parsing so m.EXIF stays nil and Write() passes the
	// original raw bytes through without re-encoding them. This exercises the
	// "preserve original bytes when no struct modification was made" path.
	m, err := gometadata.Read(bytes.NewReader(img), gometadata.WithoutEXIF())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	originalEXIF := m.RawEXIF()
	if originalEXIF == nil {
		t.Fatal("expected non-nil rawEXIF in initial read")
	}

	// Modify only IPTC; leave m.EXIF = nil so Write() passes rawEXIF through.
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("preserve test")

	var out bytes.Buffer
	if err := gometadata.Write(bytes.NewReader(img), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	m2, err := gometadata.Read(bytes.NewReader(out.Bytes()), gometadata.WithoutEXIF())
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}

	// Because m.EXIF was nil, Write() passed rawEXIF through unmodified.
	// The bytes must be identical.
	if !bytes.Equal(m2.RawEXIF(), originalEXIF) {
		t.Errorf("RawEXIF changed after IPTC-only write (pass-through expected): before=%d bytes, after=%d bytes",
			len(originalEXIF), len(m2.RawEXIF()))
	}

	// IPTC must also have survived.
	if got := m2.Caption(); got != "preserve test" {
		t.Errorf("Caption after IPTC-only write: got %q, want %q", got, "preserve test")
	}
}
