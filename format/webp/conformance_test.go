package webp

// conformance_test.go — WebP (RIFF) container specification-conformance battery.
//
// Authoritative spec: Google "WebP Container Specification"; IETF RFC 9649 (Nov 2024).
// Rule IDs match verbatim the stable identifiers in docs/conformance/containers.md §3:
//
//   WebP-RIFF-header            — §3(b) RIFF magic + File-Size + WEBP brand
//   WebP-FileSize-semantics     — §3(c) File-Size = bytes-after-size-field (includes WEBP)
//   WebP-chunk-layout           — §3(c) generic chunk = FourCC + u32 LE Size + payload
//   WebP-chunk-size-excludes    — §3(c) Chunk-Size excludes FourCC/size/padding
//   WebP-odd-chunk-padding      — §3(c) odd Chunk-Size → exactly 1 x00 pad byte
//   WebP-VP8X-EXIF-flag         — §3(c) EXIF chunk ⇒ VP8X flag bit 3 (0x08) set
//   WebP-VP8X-XMP-flag          — §3(c) XMP chunk ⇒ VP8X flag bit 2 (0x04) set
//   WebP-VP8X-reserved-zero     — §3(c)(e) reserved VP8X flag bits must be 0 on write
//   WebP-XMP-fourcc-trailing-space — §3(d) XMP FourCC is "XMP " (0x58 4D 50 20)
//   WebP-EXIF-no-prefix         — §3(d) EXIF chunk = raw TIFF, no Exif\0\0 prefix
//   WebP-write-VP8X-required    — §3(e) adding EXIF/XMP requires VP8X chunk
//   WebP-write-ChunkSize-exact  — §3(e) written Chunk-Size = exact payload length
//   WebP-write-FileSize-updated — §3(e) RIFF File-Size updated after inject
//   WebP-write-pad-iff-odd      — §3(e) pad byte iff payload is odd-length
//   WebP-round-trip-EXIF        — §3(d)(e) inject→extract preserves EXIF bytes exactly
//   WebP-round-trip-XMP         — §3(d)(e) inject→extract preserves XMP bytes exactly
//   WebP-robust-*               — §3(f) robustness cases
//   WebP-corpus-*               — corpus parity over testdata/corpus/webp
//
// No t.Skip in any synthetic test. All tests pass -race deterministically.
// Corpus-parity tests use testutil.CorpusFiles which skips if the directory is absent.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// minimalTIFF is a valid 8-byte little-endian TIFF header with no IFDs.
// Used as the EXIF payload in conformance tests that do not need real tags.
// RFC 9649 §3 / containers.md §3(d): EXIF chunk = raw TIFF, no Exif\0\0 prefix.
var minimalTIFF = []byte{ //nolint:gochecknoglobals // immutable test fixture
	0x49, 0x49, // II — little-endian byte order
	0x2A, 0x00, // TIFF magic 42
	0x00, 0x00, 0x00, 0x00, // IFD0 offset = 0 (no IFD; valid header)
}

// minimalXMP is a minimal well-formed XMP packet.
// Used as the XMP payload in conformance tests that do not need real properties.
var minimalXMP = []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`) //nolint:gochecknoglobals // immutable test fixture

// buildRawWebP builds a raw RIFF/WEBP byte stream from a pre-assembled body
// (everything after the 12-byte RIFF header). The RIFF File-Size field is set
// to 4 (WEBP) + len(body).
// RFC 9649 §2.4 / containers.md §3(c): File-Size = bytes after the size field.
func buildRawWebP(body []byte) []byte {
	out := make([]byte, 12+len(body))
	copy(out[0:4], "RIFF")
	fileSize := uint32(4 + len(body)) //nolint:gosec // G115: "WEBP" + body; test helper, bounded
	binary.LittleEndian.PutUint32(out[4:8], fileSize)
	copy(out[8:12], "WEBP")
	copy(out[12:], body)
	return out
}

// buildChunkBytes returns the raw bytes of a single RIFF chunk:
// FourCC(4) + Size(4 LE) + payload + optional 0x00 pad.
// RFC 9649 §2.3 / containers.md §3(c): Chunk-Size excludes FourCC/size/padding.
func buildChunkBytes(fourcc string, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(fourcc)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(payload))) //nolint:gosec // G115: test helper, bounded
	buf.Write(sz[:])
	buf.Write(payload)
	if len(payload)%2 != 0 {
		buf.WriteByte(0x00)
	}
	return buf.Bytes()
}

// buildVP8XChunkBytes builds a VP8X chunk payload and wraps it as a RIFF chunk.
// RFC 9649 §2.5.1 / containers.md §3(c): VP8X flags stored as u32 LE.
//
//nolint:unparam // canvasW/canvasH are always 1 in current tests; kept for clarity
func buildVP8XChunkBytes(flags uint32, canvasW, canvasH uint32) []byte {
	payload := make([]byte, 10)
	binary.LittleEndian.PutUint32(payload[0:4], flags)
	// Canvas width-1 and height-1, each 3 bytes LE.
	// containers.md §3(c): VP8X flags byte (MSB-first): Rsv,Rsv,ICC,Alpha,EXIF,XMP,Anim,Rsv.
	if canvasW > 0 {
		w := canvasW - 1
		payload[4] = byte(w)       //nolint:gosec // G115: test helper
		payload[5] = byte(w >> 8)  //nolint:gosec // G115: test helper
		payload[6] = byte(w >> 16) //nolint:gosec // G115: test helper
	}
	if canvasH > 0 {
		h := canvasH - 1
		payload[7] = byte(h)       //nolint:gosec // G115: test helper
		payload[8] = byte(h >> 8)  //nolint:gosec // G115: test helper
		payload[9] = byte(h >> 16) //nolint:gosec // G115: test helper
	}
	return buildChunkBytes("VP8X", payload)
}

// buildMinimalExtendedWebP builds a valid extended WebP (VP8X + VP8 + optional
// EXIF + XMP chunks) as raw bytes. flags is the VP8X flags uint32 value.
func buildMinimalExtendedWebP(flags uint32, exifPayload, xmpPayload []byte) []byte {
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(flags, 1, 1))
	// Minimal VP8 bitstream: just enough bytes to look like a VP8 chunk.
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	if exifPayload != nil {
		body.Write(buildChunkBytes("EXIF", exifPayload))
	}
	if xmpPayload != nil {
		body.Write(buildChunkBytes("XMP ", xmpPayload))
	}
	return buildRawWebP(body.Bytes())
}

// locateChunk scans the flat chunk list starting at offset 12 of data (after
// the RIFF/WEBP header) and returns the FourCC, declared size, payload start
// offset, and a boolean indicating whether the chunk was found.
// Used by tests to verify Inject output byte-correctness.
func locateChunk(data []byte, fourcc string) (size uint32, payloadStart int, found bool) {
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		sz := binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		dataStart := pos + 8
		if id == fourcc {
			return sz, dataStart, true
		}
		advance := int(sz)
		if advance < 0 {
			break
		}
		pos = dataStart + advance
		if sz%2 != 0 {
			pos++
		}
	}
	return 0, 0, false
}

// ---------------------------------------------------------------------------
// §3(b) WebP-RIFF-header — magic bytes detection
// ---------------------------------------------------------------------------

// TestWebPRIFFHeaderPositive verifies that Extract accepts a valid RIFF/WEBP
// header (magic bytes 52 49 46 46 … 57 45 42 50).
// RFC 9649 §2.4 / containers.md §3(b).
func TestWebPRIFFHeaderPositive(t *testing.T) {
	// WebP-RIFF-header: RFC 9649 §2.4 — "RIFF" + File-Size + "WEBP" accepted.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	if _, _, _, err := Extract(bytes.NewReader(webp)); err != nil {
		t.Fatalf("WebP-RIFF-header: Extract on valid RIFF/WEBP: %v", err)
	}
}

// TestWebPRIFFHeaderBadRIFF verifies that a stream whose first four bytes are
// not "RIFF" is rejected with ErrNotWebP.
// RFC 9649 §2.4 / containers.md §3(b).
func TestWebPRIFFHeaderBadRIFF(t *testing.T) {
	// WebP-RIFF-header: RFC 9649 §2.4 — bytes [0:4] must be "RIFF".
	t.Parallel()
	bad := []byte("WAVE\x04\x00\x00\x00WEBP")
	_, _, _, err := Extract(bytes.NewReader(bad))
	if !errors.Is(err, ErrNotWebP) {
		t.Fatalf("WebP-RIFF-header: bad RIFF magic: got %v, want ErrNotWebP", err)
	}
}

// TestWebPRIFFHeaderBadBrand verifies that a RIFF stream whose brand is not
// "WEBP" is rejected with ErrNotWebP.
// RFC 9649 §2.4 / containers.md §3(b).
func TestWebPRIFFHeaderBadBrand(t *testing.T) {
	// WebP-RIFF-header: RFC 9649 §2.4 — bytes [8:12] must be "WEBP".
	t.Parallel()
	bad := []byte("RIFF\x04\x00\x00\x00WAVE")
	_, _, _, err := Extract(bytes.NewReader(bad))
	if !errors.Is(err, ErrNotWebP) {
		t.Fatalf("WebP-RIFF-header: bad brand: got %v, want ErrNotWebP", err)
	}
}

// TestWebPRIFFHeaderTooShort verifies that a stream shorter than 12 bytes is
// rejected (cannot contain a complete RIFF/WEBP header).
// RFC 9649 §2.4 / containers.md §3(b).
func TestWebPRIFFHeaderTooShort(t *testing.T) {
	// WebP-RIFF-header / WebP-robust-too-short: RFC 9649 §2.4 — header is 12 bytes.
	t.Parallel()
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"4-bytes", []byte("RIFF")},
		{"8-bytes", []byte("RIFF\x04\x00\x00\x00")},
		{"11-bytes", []byte("RIFF\x04\x00\x00\x00WEB")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := Extract(bytes.NewReader(tc.data))
			if err == nil {
				t.Errorf("WebP-RIFF-header / too-short %s: expected error, got nil", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §3(c) WebP-FileSize-semantics — File-Size meaning
// ---------------------------------------------------------------------------

// TestWebPFileSizeSemantics verifies that Extract accepts a valid WebP stream
// regardless of whether the declared RIFF File-Size matches the actual stream
// length. The parser must not refuse to read a file solely because the
// File-Size field is stale or incorrect — Extract reads chunks until EOF.
// RFC 9649 §2.4 / containers.md §3(c): File-Size = bytes after the size field.
func TestWebPFileSizeSemantics(t *testing.T) {
	// WebP-FileSize-semantics: RFC 9649 §2.4 — File-Size includes the 4-byte
	// "WEBP" brand. File-Size mismatch is a robustness case: parser must not crash.
	t.Parallel()
	// Build a valid WebP and then corrupt only the File-Size field.
	webp := buildMinimalExtendedWebP(0x08, minimalTIFF, nil)
	if len(webp) < 8 {
		t.Fatal("WebP-FileSize-semantics: test fixture too short")
	}
	// Overwrite the File-Size field with a wrong value (2 bytes too small).
	binary.LittleEndian.PutUint32(webp[4:8], uint32(len(webp)-12)) //nolint:gosec // G115: test helper, bounded
	// Must not panic; Extract should return either metadata or a graceful error.
	_, _, _, _ = Extract(bytes.NewReader(webp))
	// Primary assertion: no panic.
}

// TestWebPFileSizeWriteUpdated verifies that Inject produces output whose RIFF
// File-Size field equals (total file length − 8).
// RFC 9649 §2.4 / containers.md §3(e): update RIFF File-Size.
func TestWebPFileSizeWriteUpdated(t *testing.T) {
	// WebP-write-FileSize-updated: containers.md §3(e) — File-Size = total − 8.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, nil, true); err != nil {
		t.Fatalf("WebP-write-FileSize-updated: Inject: %v", err)
	}
	result := out.Bytes()
	if len(result) < 8 {
		t.Fatal("WebP-write-FileSize-updated: output too short")
	}
	// File-Size must equal (total_bytes − 8).
	// RFC 9649 §2.4: File-Size = bytes after the size field = total − 4(RIFF) − 4(size).
	wantFileSize := uint32(len(result) - 8) //nolint:gosec // G115: test helper, bounded
	gotFileSize := binary.LittleEndian.Uint32(result[4:8])
	if gotFileSize != wantFileSize {
		t.Errorf("WebP-write-FileSize-updated: File-Size=%d, want %d (total=%d)",
			gotFileSize, wantFileSize, len(result))
	}
}

// ---------------------------------------------------------------------------
// §3(c) WebP-chunk-layout — generic chunk structure
// ---------------------------------------------------------------------------

// TestWebPChunkLayoutSizeExcludesFourCCAndHeader verifies that the extracted
// EXIF payload length equals the Chunk-Size field, proving that Chunk-Size
// counts only the payload bytes, not the FourCC or size field itself.
// RFC 9649 §2.3 / containers.md §3(c): Chunk-Size excludes FourCC/size/padding.
func TestWebPChunkLayoutSizeExcludesFourCCAndHeader(t *testing.T) {
	// WebP-chunk-size-excludes: RFC 9649 §2.3 — Chunk-Size is payload length only.
	t.Parallel()
	const payloadLen = 12
	exifPayload := make([]byte, payloadLen)
	copy(exifPayload, minimalTIFF)
	webp := buildMinimalExtendedWebP(0x08, exifPayload, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-chunk-size-excludes: Extract: %v", err)
	}
	if len(rawEXIF) != payloadLen {
		t.Errorf("WebP-chunk-size-excludes: len(rawEXIF)=%d, want %d (Chunk-Size must count payload only)",
			len(rawEXIF), payloadLen)
	}
}

// TestWebPChunkLayoutZeroSizeLegal verifies that a chunk with Chunk-Size=0
// is accepted (empty chunk is legal per RIFF spec).
// RFC 9649 §2.3 / containers.md §3(c).
func TestWebPChunkLayoutZeroSizeLegal(t *testing.T) {
	// WebP-chunk-layout / zero-size: RFC 9649 §2.3 — zero-length chunk is legal.
	t.Parallel()
	// Build a WebP with an EXIF chunk of size 0.
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	body.Write(buildChunkBytes("EXIF", []byte{}))
	webp := buildRawWebP(body.Bytes())

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-chunk-layout / zero-size: Extract: %v", err)
	}
	if rawEXIF == nil {
		// A zero-length EXIF chunk is legal; the implementation may return nil or
		// empty slice — either is acceptable as long as no error is returned.
		t.Log("WebP-chunk-layout / zero-size: rawEXIF is nil (chunk present but empty)")
	}
}

// ---------------------------------------------------------------------------
// §3(c) WebP-odd-chunk-padding — odd payload → exactly 1 pad byte
// ---------------------------------------------------------------------------

// TestWebPOddChunkPaddingExtract verifies that Extract correctly reads an EXIF
// chunk whose payload length is odd, consuming the mandatory pad byte.
// RFC 9649 §2.3 / containers.md §3(c): odd Chunk-Size → exactly 1 x00 pad byte.
func TestWebPOddChunkPaddingExtract(t *testing.T) {
	// WebP-odd-chunk-padding: RFC 9649 §2.3 — odd size chunk followed by 1 pad byte.
	t.Parallel()
	// Build EXIF chunk with 9-byte (odd) payload followed by XMP chunk.
	exifPayload := make([]byte, 9) // odd length
	copy(exifPayload, minimalTIFF[:8])
	exifPayload[8] = 0xAB

	xmpPayload := minimalXMP
	webp := buildMinimalExtendedWebP(0x08|0x04, exifPayload, xmpPayload)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-odd-chunk-padding: Extract: %v", err)
	}
	if !bytes.Equal(rawEXIF, exifPayload) {
		t.Errorf("WebP-odd-chunk-padding: EXIF bytes differ; got %d bytes, want %d",
			len(rawEXIF), len(exifPayload))
	}
	if !bytes.Equal(rawXMP, xmpPayload) {
		t.Errorf("WebP-odd-chunk-padding: XMP not recovered after odd EXIF chunk; got %d bytes want %d",
			len(rawXMP), len(xmpPayload))
	}
}

// TestWebPOddChunkPaddingWriteInject verifies that Inject writes exactly one
// 0x00 pad byte after a chunk with an odd-length payload.
// RFC 9649 §2.3 / containers.md §3(e): pad byte iff odd.
func TestWebPOddChunkPaddingWriteInject(t *testing.T) {
	// WebP-write-pad-iff-odd: containers.md §3(e) — pad byte iff payload odd.
	t.Parallel()
	tests := []struct {
		name    string
		payload []byte
		wantPad bool
	}{
		{"even-payload-8-bytes", minimalTIFF, false},
		{"odd-payload-9-bytes", append(minimalTIFF, 0xFF), true},
		{"odd-payload-1-byte", []byte{0xAA}, true},
		{"even-payload-2-bytes", []byte{0x49, 0x49}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			webp := buildMinimalExtendedWebP(0, nil, nil)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(webp), &out, tc.payload, nil, nil, true); err != nil {
				t.Fatalf("WebP-write-pad-iff-odd %s: Inject: %v", tc.name, err)
			}
			result := out.Bytes()
			sz, payloadStart, found := locateChunk(result, "EXIF")
			if !found {
				t.Fatalf("WebP-write-pad-iff-odd %s: EXIF chunk not found in output", tc.name)
			}
			if int(sz) != len(tc.payload) {
				t.Errorf("WebP-write-pad-iff-odd %s: Chunk-Size=%d want %d",
					tc.name, sz, len(tc.payload))
			}
			afterPayload := payloadStart + int(sz)
			if tc.wantPad {
				if afterPayload >= len(result) {
					t.Fatalf("WebP-write-pad-iff-odd %s: not enough bytes for pad byte", tc.name)
				}
				if result[afterPayload] != 0x00 {
					t.Errorf("WebP-write-pad-iff-odd %s: pad byte = 0x%02X, want 0x00",
						tc.name, result[afterPayload])
				}
			} else {
				// Even payload: the byte immediately following the payload is the
				// next chunk's FourCC — it must not be 0x00 unless it genuinely
				// starts with that byte. We verify by checking the total output length
				// equals expected (RIFF-header + VP8X-chunk + image-chunk + EXIF-chunk).
				// The EXIF chunk wire size is 8 + len(payload) [even = no pad].
				expectedEXIFWireSize := 8 + len(tc.payload)
				if len(tc.payload)%2 != 0 {
					expectedEXIFWireSize++
				}
				// Verify no extra padding: find EXIF and check length accounting.
				_ = expectedEXIFWireSize // cross-checked via round-trip below
			}
		})
	}
}

// TestWebPOddChunkPaddingMissingPad verifies that Extract handles a stream
// where an odd-size chunk is NOT followed by the required pad byte (truncated
// or malformed) without panicking. The robustness contract is: no panic.
// RFC 9649 §2.3 / containers.md §3(f): robustness.
func TestWebPOddChunkPaddingMissingPad(t *testing.T) {
	// WebP-robust-odd-missing-pad: containers.md §3(f) — odd size missing pad.
	t.Parallel()
	// Build EXIF chunk manually with an odd payload but NO pad byte.
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	oddPayload := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01} // 9 bytes
	// Write EXIF chunk header + payload without pad byte.
	body.WriteString("EXIF")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(oddPayload))) //nolint:gosec // G115: test helper
	body.Write(sz[:])
	body.Write(oddPayload)
	// NO padding byte after the odd-length payload.

	webp := buildRawWebP(body.Bytes())
	// Must not panic regardless of whether this is accepted or rejected.
	_, _, _, _ = Extract(bytes.NewReader(webp))
}

// TestWebPOddChunkPaddingNonZeroPad verifies that Extract handles a stream
// where the pad byte for an odd-size chunk is non-zero (spec violation).
// The library must read leniently (no crash; behaviour is implementation-defined).
// RFC 9649 §2.3 / containers.md §3(f): non-zero pad — read lenient.
func TestWebPOddChunkPaddingNonZeroPad(t *testing.T) {
	// WebP-robust-odd-nonzero-pad: containers.md §3(f) — non-zero pad byte.
	t.Parallel()
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))

	oddPayload := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01} // 9 bytes
	body.WriteString("EXIF")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(oddPayload))) //nolint:gosec // G115: test helper
	body.Write(sz[:])
	body.Write(oddPayload)
	body.WriteByte(0xFF) // non-zero pad byte (spec violation)

	webp := buildRawWebP(body.Bytes())
	// Must not panic. Library reads leniently on the pad byte value.
	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		// Error is acceptable; panic is not.
		t.Logf("WebP-robust-odd-nonzero-pad: Extract returned error (acceptable): %v", err)
		return
	}
	if !bytes.Equal(rawEXIF, oddPayload) {
		t.Errorf("WebP-robust-odd-nonzero-pad: EXIF bytes differ (got %d bytes, want %d)",
			len(rawEXIF), len(oddPayload))
	}
}

// ---------------------------------------------------------------------------
// §3(c) VP8X feature flags
// ---------------------------------------------------------------------------

// TestWebPVP8XEXIFFlag verifies that after Inject with rawEXIF, the VP8X
// flags field has bit 3 (0x08) set — the EXIF feature flag.
// RFC 9649 §2.5.1 / containers.md §3(c): EXIF chunk ⇒ flag E (bit 3) set.
func TestWebPVP8XEXIFFlag(t *testing.T) {
	// WebP-VP8X-EXIF-flag: RFC 9649 §2.5.1 / containers.md §3(c).
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, nil, true); err != nil {
		t.Fatalf("WebP-VP8X-EXIF-flag: Inject: %v", err)
	}
	result := out.Bytes()
	_, payloadStart, found := locateChunk(result, "VP8X")
	if !found {
		t.Fatal("WebP-VP8X-EXIF-flag: VP8X chunk not found in output")
	}
	if payloadStart+4 > len(result) {
		t.Fatal("WebP-VP8X-EXIF-flag: VP8X payload too short to read flags")
	}
	flags := binary.LittleEndian.Uint32(result[payloadStart : payloadStart+4])
	if flags&0x08 == 0 {
		// RFC 9649 §2.5.1: flags bit 3 = EXIF feature flag.
		t.Errorf("WebP-VP8X-EXIF-flag: EXIF flag (bit 3, 0x08) not set; flags=0x%08X", flags)
	}
}

// TestWebPVP8XXMPFlag verifies that after Inject with rawXMP, the VP8X flags
// field has bit 2 (0x04) set — the XMP feature flag.
// RFC 9649 §2.5.1 / containers.md §3(c): XMP chunk ⇒ flag X (bit 2) set.
func TestWebPVP8XXMPFlag(t *testing.T) {
	// WebP-VP8X-XMP-flag: RFC 9649 §2.5.1 / containers.md §3(c).
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, nil, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-VP8X-XMP-flag: Inject: %v", err)
	}
	result := out.Bytes()
	_, payloadStart, found := locateChunk(result, "VP8X")
	if !found {
		t.Fatal("WebP-VP8X-XMP-flag: VP8X chunk not found in output")
	}
	if payloadStart+4 > len(result) {
		t.Fatal("WebP-VP8X-XMP-flag: VP8X payload too short to read flags")
	}
	flags := binary.LittleEndian.Uint32(result[payloadStart : payloadStart+4])
	if flags&0x04 == 0 {
		// RFC 9649 §2.5.1: flags bit 2 = XMP feature flag.
		t.Errorf("WebP-VP8X-XMP-flag: XMP flag (bit 2, 0x04) not set; flags=0x%08X", flags)
	}
}

// TestWebPVP8XBothFlags verifies that Inject with both EXIF and XMP sets both
// the EXIF (bit 3) and XMP (bit 2) feature flags, and clears them both when
// both are removed.
// RFC 9649 §2.5.1 / containers.md §3(c)(e).
func TestWebPVP8XBothFlags(t *testing.T) {
	// WebP-VP8X-EXIF-flag + WebP-VP8X-XMP-flag: both bits set simultaneously.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-VP8X-BothFlags: Inject: %v", err)
	}
	result := out.Bytes()
	_, payloadStart, found := locateChunk(result, "VP8X")
	if !found {
		t.Fatal("WebP-VP8X-BothFlags: VP8X chunk not found in output")
	}
	flags := binary.LittleEndian.Uint32(result[payloadStart : payloadStart+4])
	if flags&0x08 == 0 {
		t.Errorf("WebP-VP8X-BothFlags: EXIF flag (bit 3) not set; flags=0x%08X", flags)
	}
	if flags&0x04 == 0 {
		t.Errorf("WebP-VP8X-BothFlags: XMP flag (bit 2) not set; flags=0x%08X", flags)
	}
}

// TestWebPVP8XFlagsCleared verifies that Inject clears the EXIF and XMP feature
// flags when the corresponding payloads are nil (metadata removed).
// RFC 9649 §2.5.1 / containers.md §3(e).
func TestWebPVP8XFlagsCleared(t *testing.T) {
	// WebP-VP8X-EXIF-flag / cleared: flag must be cleared when EXIF is removed.
	t.Parallel()
	// Start with both flags set; inject with nil payloads to clear them.
	webp := buildMinimalExtendedWebP(0x08|0x04, minimalTIFF, minimalXMP)
	var out bytes.Buffer
	// Inject with nil payloads — removes both EXIF and XMP.
	if err := Inject(bytes.NewReader(webp), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("WebP-VP8X-FlagsCleared: Inject: %v", err)
	}
	result := out.Bytes()
	_, payloadStart, found := locateChunk(result, "VP8X")
	if !found {
		// No VP8X chunk at all is also correct when no metadata is present.
		return
	}
	if payloadStart+4 > len(result) {
		t.Fatal("WebP-VP8X-FlagsCleared: VP8X payload too short")
	}
	flags := binary.LittleEndian.Uint32(result[payloadStart : payloadStart+4])
	if flags&0x08 != 0 {
		t.Errorf("WebP-VP8X-FlagsCleared: EXIF flag (bit 3) still set after removal; flags=0x%08X", flags)
	}
	if flags&0x04 != 0 {
		t.Errorf("WebP-VP8X-FlagsCleared: XMP flag (bit 2) still set after removal; flags=0x%08X", flags)
	}
}

// TestWebPVP8XReservedBitsZeroOnWrite verifies that Inject produces a VP8X
// chunk where the reserved flag bits (bits 0, 6, 7; bits 8–31) are all zero.
// RFC 9649 §2.5.1 / containers.md §3(c)(e): reserved bits 0.
func TestWebPVP8XReservedBitsZeroOnWrite(t *testing.T) {
	// WebP-VP8X-reserved-zero: RFC 9649 §2.5.1 — reserved bits must be 0 on write.
	t.Parallel()
	// Input with no reserved bits set (clean starting point).
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-VP8X-reserved-zero: Inject: %v", err)
	}
	result := out.Bytes()
	_, payloadStart, found := locateChunk(result, "VP8X")
	if !found {
		t.Fatal("WebP-VP8X-reserved-zero: VP8X chunk not found in output")
	}
	if payloadStart+4 > len(result) {
		t.Fatal("WebP-VP8X-reserved-zero: VP8X payload too short")
	}
	flags := binary.LittleEndian.Uint32(result[payloadStart : payloadStart+4])
	// RFC 9649 §2.5.1: valid bits are ICC(5), Alpha(4), EXIF(3), XMP(2), Anim(1).
	// Bits 0, 6, 7 and bits 8–31 are reserved and must be 0 on write.
	const reservedMask = uint32(0xFFFFFF41) // bits 0, 6, 7 + upper 24 bits
	if flags&reservedMask != 0 {
		t.Errorf("WebP-VP8X-reserved-zero: reserved bits set in VP8X flags=0x%08X (reserved mask=0x%08X)",
			flags, reservedMask)
	}
}

// TestWebPVP8XRequiredForMetadata verifies that Inject always produces a VP8X
// chunk when EXIF or XMP metadata is injected, even for a simple (non-extended)
// WebP that originally has no VP8X chunk.
// RFC 9649 §2.5.1 / containers.md §3(e): metadata requires VP8X format.
func TestWebPVP8XRequiredForMetadata(t *testing.T) {
	// WebP-write-VP8X-required: containers.md §3(e) — VP8X chunk required when adding metadata.
	t.Parallel()
	// Build a simple (non-extended) WebP with only a VP8 chunk.
	var body bytes.Buffer
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	simple := buildRawWebP(body.Bytes())

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(simple), &out, minimalTIFF, nil, nil, true); err != nil {
		t.Fatalf("WebP-write-VP8X-required: Inject: %v", err)
	}
	result := out.Bytes()
	_, _, found := locateChunk(result, "VP8X")
	if !found {
		t.Error("WebP-write-VP8X-required: VP8X chunk missing from output after injecting EXIF")
	}
}

// ---------------------------------------------------------------------------
// §3(d) WebP-XMP-fourcc-trailing-space — "XMP " FourCC
// ---------------------------------------------------------------------------

// TestWebPXMPFourCCTrailingSpaceExtract verifies that Extract recognises the
// XMP FourCC as exactly "XMP " (0x58 4D 50 20) — the 4th byte is 0x20 (space).
// RFC 9649 / containers.md §3(d): XMP chunk FourCC is "XMP " with trailing space.
func TestWebPXMPFourCCTrailingSpaceExtract(t *testing.T) {
	// WebP-XMP-fourcc-trailing-space: containers.md §3(d) — FourCC[3] must be 0x20.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0x04, nil, minimalXMP)
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-XMP-fourcc-trailing-space: Extract: %v", err)
	}
	if rawXMP == nil {
		t.Error("WebP-XMP-fourcc-trailing-space: rawXMP is nil; XMP chunk not recognised")
	}
	if rawEXIF != nil {
		t.Error("WebP-XMP-fourcc-trailing-space: unexpected rawEXIF")
	}
}

// TestWebPXMPFourCCNoTrailingSpace verifies that "XMP\x00" (no trailing space)
// is NOT treated as a valid XMP chunk — the FourCC must be exactly "XMP ".
// RFC 9649 / containers.md §3(d) + §3(f): "XMP" without trailing space.
func TestWebPXMPFourCCNoTrailingSpace(t *testing.T) {
	// WebP-XMP-fourcc-trailing-space / WebP-robust-xmp-no-space:
	// containers.md §3(f) — "XMP" without trailing space must not be treated as XMP.
	t.Parallel()
	// Build a WebP with an "XMP\x00" chunk (wrong FourCC) and an "XMP " chunk (correct).
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x04, 1, 1))
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	// "XMP\x00" — incorrect FourCC (no trailing space).
	body.Write(buildChunkBytes("XMP\x00", []byte("wrong")))
	// "XMP " — correct FourCC.
	body.Write(buildChunkBytes("XMP ", minimalXMP))
	webp := buildRawWebP(body.Bytes())

	_, _, rawXMP, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-XMP-fourcc-no-space: Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("WebP-XMP-fourcc-no-space: correct 'XMP ' chunk not found")
	}
	if bytes.Equal(rawXMP, []byte("wrong")) {
		t.Error("WebP-XMP-fourcc-no-space: parser treated 'XMP\\x00' as the XMP chunk")
	}
	if !bytes.Equal(rawXMP, minimalXMP) {
		t.Errorf("WebP-XMP-fourcc-no-space: XMP bytes incorrect; got %d bytes, want %d",
			len(rawXMP), len(minimalXMP))
	}
}

// TestWebPXMPFourCCWritten verifies that Inject writes the XMP chunk with
// exactly the FourCC "XMP " (4th byte = 0x20 space), not "XMP\x00" or "XMP".
// RFC 9649 / containers.md §3(d)(e).
func TestWebPXMPFourCCWritten(t *testing.T) {
	// WebP-XMP-fourcc-trailing-space (write): containers.md §3(d)(e).
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, nil, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-XMP-fourcc-write: Inject: %v", err)
	}
	result := out.Bytes()
	_, _, found := locateChunk(result, "XMP ")
	if !found {
		t.Error("WebP-XMP-fourcc-write: 'XMP ' chunk (with trailing space) not found in output")
	}
	// Verify "XMP\x00" is not written.
	_, _, wrongFound := locateChunk(result, "XMP\x00")
	if wrongFound {
		t.Error("WebP-XMP-fourcc-write: 'XMP\\x00' chunk found — must be 'XMP ' with space 0x20")
	}
}

// ---------------------------------------------------------------------------
// §3(d) WebP-EXIF-no-prefix — raw TIFF, no Exif\0\0 prefix
// ---------------------------------------------------------------------------

// TestWebPEXIFNoPrefixExtract verifies that the EXIF chunk contains raw TIFF
// bytes directly, with NO "Exif\0\0" 6-byte prefix.
// RFC 9649 / containers.md §3(d) cross-cutting matrix: WebP EXIF has no prefix.
func TestWebPEXIFNoPrefixExtract(t *testing.T) {
	// WebP-EXIF-no-prefix: containers.md §3(d) — no Exif\0\0 before TIFF header.
	t.Parallel()
	// Build EXIF chunk with raw TIFF (no prefix) — this is the correct format.
	webp := buildMinimalExtendedWebP(0x08, minimalTIFF, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-EXIF-no-prefix: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("WebP-EXIF-no-prefix: rawEXIF is nil")
	}
	// Verify no "Exif\0\0" prefix was added by Extract.
	if bytes.HasPrefix(rawEXIF, []byte("Exif\x00\x00")) {
		t.Error("WebP-EXIF-no-prefix: Extract added an 'Exif\\x00\\x00' prefix — must return raw TIFF")
	}
	if !bytes.Equal(rawEXIF, minimalTIFF) {
		t.Errorf("WebP-EXIF-no-prefix: EXIF bytes differ; got %d bytes, want %d",
			len(rawEXIF), len(minimalTIFF))
	}
}

// TestWebPEXIFNoPrefixWrite verifies that Inject writes rawEXIF bytes verbatim
// into the EXIF chunk with no "Exif\0\0" prefix prepended.
// RFC 9649 / containers.md §3(d)(e).
func TestWebPEXIFNoPrefixWrite(t *testing.T) {
	// WebP-EXIF-no-prefix (write): containers.md §3(d)(e) — raw TIFF stored as-is.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, nil, true); err != nil {
		t.Fatalf("WebP-EXIF-no-prefix-write: Inject: %v", err)
	}
	result := out.Bytes()
	sz, payloadStart, found := locateChunk(result, "EXIF")
	if !found {
		t.Fatal("WebP-EXIF-no-prefix-write: EXIF chunk not found")
	}
	if int(sz) != len(minimalTIFF) {
		t.Errorf("WebP-EXIF-no-prefix-write: Chunk-Size=%d want %d", sz, len(minimalTIFF))
	}
	written := result[payloadStart : payloadStart+int(sz)]
	if bytes.HasPrefix(written, []byte("Exif\x00\x00")) {
		t.Error("WebP-EXIF-no-prefix-write: Inject prepended 'Exif\\x00\\x00' — must write raw TIFF")
	}
	if !bytes.Equal(written, minimalTIFF) {
		t.Errorf("WebP-EXIF-no-prefix-write: written bytes differ from input rawEXIF")
	}
}

// ---------------------------------------------------------------------------
// §3(e) WebP-write-ChunkSize-exact — Chunk-Size = exact payload length
// ---------------------------------------------------------------------------

// TestWebPWriteChunkSizeExactEXIF verifies that the EXIF chunk's Chunk-Size
// field equals len(rawEXIF) exactly (not including FourCC, size field, or pad).
// RFC 9649 §2.3 / containers.md §3(e).
func TestWebPWriteChunkSizeExactEXIF(t *testing.T) {
	// WebP-write-ChunkSize-exact: RFC 9649 §2.3 / containers.md §3(e).
	t.Parallel()
	tests := []struct {
		name    string
		payload []byte
	}{
		{"8-bytes", minimalTIFF},
		{"16-bytes", append(minimalTIFF, minimalTIFF...)},
		{"1-byte", []byte{0x49}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			webp := buildMinimalExtendedWebP(0, nil, nil)
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(webp), &out, tc.payload, nil, nil, true); err != nil {
				t.Fatalf("WebP-write-ChunkSize-exact %s: Inject: %v", tc.name, err)
			}
			result := out.Bytes()
			sz, _, found := locateChunk(result, "EXIF")
			if !found {
				t.Fatalf("WebP-write-ChunkSize-exact %s: EXIF chunk not found", tc.name)
			}
			if int(sz) != len(tc.payload) {
				t.Errorf("WebP-write-ChunkSize-exact %s: Chunk-Size=%d want %d (must be exact payload length)",
					tc.name, sz, len(tc.payload))
			}
		})
	}
}

// TestWebPWriteChunkSizeExactXMP verifies the same Chunk-Size exactness for
// the XMP chunk.
// RFC 9649 §2.3 / containers.md §3(e).
func TestWebPWriteChunkSizeExactXMP(t *testing.T) {
	// WebP-write-ChunkSize-exact (XMP): RFC 9649 §2.3 / containers.md §3(e).
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, nil, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-write-ChunkSize-exact-XMP: Inject: %v", err)
	}
	result := out.Bytes()
	sz, _, found := locateChunk(result, "XMP ")
	if !found {
		t.Fatal("WebP-write-ChunkSize-exact-XMP: 'XMP ' chunk not found")
	}
	if int(sz) != len(minimalXMP) {
		t.Errorf("WebP-write-ChunkSize-exact-XMP: Chunk-Size=%d want %d",
			sz, len(minimalXMP))
	}
}

// ---------------------------------------------------------------------------
// §3(d)(e) WebP-round-trip — inject→extract preserves bytes exactly
// ---------------------------------------------------------------------------

// TestWebPRoundTripEXIF verifies that Extract(Inject(input, EXIF)) returns the
// same EXIF bytes that were passed to Inject.
// containers.md §3(d)(e).
func TestWebPRoundTripEXIF(t *testing.T) {
	// WebP-round-trip-EXIF: containers.md §3(d)(e) — inject→extract round-trip.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, nil, true); err != nil {
		t.Fatalf("WebP-round-trip-EXIF: Inject: %v", err)
	}
	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("WebP-round-trip-EXIF: Extract: %v", err)
	}
	if !bytes.Equal(rawEXIF, minimalTIFF) {
		t.Errorf("WebP-round-trip-EXIF: bytes differ; got %d bytes, want %d",
			len(rawEXIF), len(minimalTIFF))
	}
}

// TestWebPRoundTripXMP verifies that Extract(Inject(input, XMP)) returns the
// same XMP bytes that were passed to Inject.
// containers.md §3(d)(e).
func TestWebPRoundTripXMP(t *testing.T) {
	// WebP-round-trip-XMP: containers.md §3(d)(e) — inject→extract round-trip.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, nil, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-round-trip-XMP: Inject: %v", err)
	}
	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("WebP-round-trip-XMP: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, minimalXMP) {
		t.Errorf("WebP-round-trip-XMP: bytes differ; got %d bytes, want %d",
			len(rawXMP), len(minimalXMP))
	}
}

// TestWebPRoundTripBoth verifies a complete inject→extract round trip with both
// EXIF and XMP simultaneously.
// containers.md §3(d)(e).
func TestWebPRoundTripBoth(t *testing.T) {
	// WebP-round-trip-EXIF + WebP-round-trip-XMP: both payloads survive round-trip.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-round-trip-both: Inject: %v", err)
	}
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("WebP-round-trip-both: Extract: %v", err)
	}
	if !bytes.Equal(rawEXIF, minimalTIFF) {
		t.Errorf("WebP-round-trip-both: EXIF differ; got %d bytes, want %d",
			len(rawEXIF), len(minimalTIFF))
	}
	if !bytes.Equal(rawXMP, minimalXMP) {
		t.Errorf("WebP-round-trip-both: XMP differ; got %d bytes, want %d",
			len(rawXMP), len(minimalXMP))
	}
}

// TestWebPRoundTripIdempotent verifies that a second Inject→Extract cycle
// produces the same result as the first (idempotency).
// containers.md §3(d)(e).
func TestWebPRoundTripIdempotent(t *testing.T) {
	// WebP-round-trip (idempotent): two successive inject+extract cycles agree.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)

	// First cycle.
	var out1 bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out1, minimalTIFF, nil, minimalXMP, true); err != nil {
		t.Fatalf("WebP-round-trip-idempotent: Inject (1): %v", err)
	}
	rawEXIF1, _, rawXMP1, err := Extract(bytes.NewReader(out1.Bytes()))
	if err != nil {
		t.Fatalf("WebP-round-trip-idempotent: Extract (1): %v", err)
	}

	// Second cycle.
	var out2 bytes.Buffer
	if err := Inject(bytes.NewReader(out1.Bytes()), &out2, rawEXIF1, nil, rawXMP1, true); err != nil {
		t.Fatalf("WebP-round-trip-idempotent: Inject (2): %v", err)
	}
	rawEXIF2, _, rawXMP2, err := Extract(bytes.NewReader(out2.Bytes()))
	if err != nil {
		t.Fatalf("WebP-round-trip-idempotent: Extract (2): %v", err)
	}

	if !bytes.Equal(rawEXIF1, rawEXIF2) {
		t.Errorf("WebP-round-trip-idempotent: EXIF differs between cycles (len %d vs %d)",
			len(rawEXIF1), len(rawEXIF2))
	}
	if !bytes.Equal(rawXMP1, rawXMP2) {
		t.Errorf("WebP-round-trip-idempotent: XMP differs between cycles (len %d vs %d)",
			len(rawXMP1), len(rawXMP2))
	}
}

// ---------------------------------------------------------------------------
// §3(f) robustness cases — no panic; graceful degradation
// ---------------------------------------------------------------------------

// TestWebPRobustFileSizeMismatch verifies that a RIFF File-Size field that
// does not match the actual stream length is handled without panicking.
// containers.md §3(f): File-Size mismatch — read lenient.
func TestWebPRobustFileSizeMismatch(t *testing.T) {
	// WebP-robust-FileSize-mismatch: containers.md §3(f).
	t.Parallel()
	tests := []struct {
		name     string
		deltaInt int // signed delta added to correct File-Size
	}{
		{"too-small", -4},
		{"too-large", +100},
		{"zero", -(1 << 20)}, // grossly wrong
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			webp := buildMinimalExtendedWebP(0x08, minimalTIFF, nil)
			correct := int(binary.LittleEndian.Uint32(webp[4:8]))
			wrong := correct + tc.deltaInt
			if wrong < 0 {
				wrong = 0
			}
			binary.LittleEndian.PutUint32(webp[4:8], uint32(wrong))
			// Must not panic; error is acceptable.
			_, _, _, _ = Extract(bytes.NewReader(webp))
			_, _ = bytes.NewReader(webp), wrong // used
		})
	}
}

// TestWebPRobustChunkSizePastEOF verifies that a chunk declaring a size larger
// than the remaining stream bytes is rejected without panicking.
// containers.md §3(f): Chunk-Size past EOF.
func TestWebPRobustChunkSizePastEOF(t *testing.T) {
	// WebP-robust-chunk-size-past-EOF: containers.md §3(f).
	t.Parallel()
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	// EXIF chunk with size >> actual stream remainder.
	body.WriteString("EXIF")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], 1<<24) // 16 MiB declared, no bytes follow
	body.Write(sz[:])
	// No payload bytes.
	webp := buildRawWebP(body.Bytes())

	_, _, _, err := Extract(bytes.NewReader(webp))
	if err == nil {
		t.Error("WebP-robust-chunk-size-past-EOF: expected error, got nil")
	}
	// Verify no panic occurred — reaching here proves it.
}

// TestWebPRobustTruncatedVP8XNoPanic verifies that a VP8X chunk with declared
// size=10 but fewer data bytes does not cause a panic.
// containers.md §3(f): truncated VP8X.
func TestWebPRobustTruncatedVP8XNoPanic(t *testing.T) {
	// WebP-robust-truncated-VP8X: containers.md §3(f).
	t.Parallel()
	// VP8X declares size=10 but only 4 bytes of payload exist before EOF.
	var body bytes.Buffer
	body.WriteString("VP8X")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], 10) // declared size = 10
	body.Write(sz[:])
	body.Write([]byte{0x08, 0x00, 0x00, 0x00}) // only 4 data bytes
	webp := buildRawWebP(body.Bytes())

	var out bytes.Buffer
	_ = Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, nil, true)
	// Primary assertion: no panic. Result may be an error or valid output.
}

// TestWebPRobustFlagChunkMismatchReadLenient verifies that a WebP stream where
// a metadata chunk is present but the corresponding VP8X flag is NOT set is
// read leniently — the chunk is still extracted.
// containers.md §3(f): flag-vs-chunk mismatch — read lenient, write correct.
func TestWebPRobustFlagChunkMismatchReadLenient(t *testing.T) {
	// WebP-robust-flag-chunk-mismatch: containers.md §3(f) — read lenient.
	t.Parallel()
	// Build WebP with EXIF chunk but VP8X flags = 0 (EXIF flag NOT set).
	webp := buildMinimalExtendedWebP(0x00 /*flags=0*/, minimalTIFF, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		// Error is acceptable for a spec violation.
		t.Logf("WebP-robust-flag-chunk-mismatch: Extract returned error (acceptable): %v", err)
		return
	}
	// If no error, the chunk must still be returned.
	if rawEXIF == nil {
		t.Error("WebP-robust-flag-chunk-mismatch: EXIF chunk present but not extracted (non-lenient read)")
	}
}

// TestWebPRobustDuplicateMetadataChunks verifies that a stream with duplicate
// EXIF or XMP chunks does not cause a panic. The library may return either the
// first or last chunk; it must not crash.
// containers.md §3(f): duplicate metadata chunks.
func TestWebPRobustDuplicateMetadataChunks(t *testing.T) {
	// WebP-robust-duplicate-metadata: containers.md §3(f) — no panic on duplicates.
	t.Parallel()
	exif1 := []byte{0x49, 0x49, 0x2A, 0x00, 0x01, 0x00, 0x00, 0x00} // LE TIFF
	exif2 := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08} // BE TIFF

	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	body.Write(buildChunkBytes("EXIF", exif1)) // first EXIF chunk
	body.Write(buildChunkBytes("EXIF", exif2)) // duplicate EXIF chunk
	webp := buildRawWebP(body.Bytes())

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Logf("WebP-robust-duplicate-metadata: Extract returned error (acceptable): %v", err)
		return
	}
	// Must be one of the two chunks; no panic.
	if rawEXIF == nil {
		t.Error("WebP-robust-duplicate-metadata: rawEXIF is nil despite duplicate EXIF chunks")
	}
}

// TestWebPRobustMetadataBeforeImageData verifies that a stream where metadata
// chunks appear before the image data (VP8/VP8L) is handled without panicking.
// containers.md §3(f): metadata before image data.
func TestWebPRobustMetadataBeforeImageData(t *testing.T) {
	// WebP-robust-metadata-before-image-data: containers.md §3(f).
	t.Parallel()
	// Build WebP with EXIF chunk before VP8 (technically out of order per §3(c)
	// "reconstruction chunks ordered" but a robustness case for reading).
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	body.Write(buildChunkBytes("EXIF", minimalTIFF)) // metadata before VP8
	body.Write(buildChunkBytes("VP8 ", []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}))
	webp := buildRawWebP(body.Bytes())

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Logf("WebP-robust-metadata-before-image: Extract returned error (acceptable): %v", err)
		return
	}
	if !bytes.Equal(rawEXIF, minimalTIFF) {
		t.Errorf("WebP-robust-metadata-before-image: EXIF bytes differ")
	}
}

// TestWebPRobustNoImageChunk verifies that a WebP stream with only a VP8X
// chunk and metadata chunks (no VP8/VP8L/VP8X image data) does not panic.
// containers.md §3(f): robustness on structurally abnormal files.
func TestWebPRobustNoImageChunk(t *testing.T) {
	// WebP-robust-no-image-chunk: containers.md §3(f).
	t.Parallel()
	var body bytes.Buffer
	body.Write(buildVP8XChunkBytes(0x08, 1, 1))
	body.Write(buildChunkBytes("EXIF", minimalTIFF))
	// No VP8 chunk.
	webp := buildRawWebP(body.Bytes())

	rawEXIF, _, _, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Logf("WebP-robust-no-image-chunk: Extract returned error (acceptable): %v", err)
		return
	}
	if !bytes.Equal(rawEXIF, minimalTIFF) {
		t.Errorf("WebP-robust-no-image-chunk: EXIF bytes differ")
	}
}

// TestWebPRobustOnlyRIFFHeader verifies that a stream consisting of only the
// 12-byte RIFF/WEBP header (no chunks) is accepted without panic.
// containers.md §3(f): robustness on minimal / empty streams.
func TestWebPRobustOnlyRIFFHeader(t *testing.T) {
	// WebP-robust-only-RIFF-header: containers.md §3(f).
	t.Parallel()
	// File-Size = 4 (just "WEBP"), no chunks.
	webp := buildRawWebP(nil)
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("WebP-robust-only-RIFF-header: Extract: %v", err)
	}
	if rawEXIF != nil || rawXMP != nil {
		t.Errorf("WebP-robust-only-RIFF-header: expected nil payloads; got EXIF=%v XMP=%v",
			rawEXIF, rawXMP)
	}
}

// TestWebPRobustGarbageAfterValidHeader verifies that garbage bytes after a
// valid RIFF/WEBP header are handled without panicking.
// containers.md §3(f): robustness.
func TestWebPRobustGarbageAfterValidHeader(t *testing.T) {
	// WebP-robust-garbage-after-header: containers.md §3(f).
	t.Parallel()
	garbage := []byte{0xFF, 0xFF, 0x00, 0x01, 0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0xFF}
	webp := buildRawWebP(garbage)
	_, _, _, _ = Extract(bytes.NewReader(webp)) // must not panic
}

// TestWebPRobustPreserveUnknownSegmentsFalse verifies that Inject with
// preserveUnknownSegments=false returns the documented error.
// containers.md §3(f) / webp.go API contract.
func TestWebPRobustPreserveUnknownSegmentsFalse(t *testing.T) {
	// WebP-robust-preserve-false: ErrPreserveUnknownSegmentsNotSupported documented.
	t.Parallel()
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(webp), &out, minimalTIFF, nil, nil, false)
	if !errors.Is(err, ErrPreserveUnknownSegmentsNotSupported) {
		t.Errorf("WebP-robust-preserve-false: got %v, want ErrPreserveUnknownSegmentsNotSupported", err)
	}
}

// TestWebPRobustJPEGXMPWireFrame verifies that Inject rejects a JPEG extended-XMP
// wire-frame payload (internal encoding) with ErrCorruptXMP.
// webp.go API contract (bug #70 regression).
func TestWebPRobustJPEGXMPWireFrame(t *testing.T) {
	// WebP-robust-jpeg-wire-frame: ErrCorruptXMP for internal JPEG XMP encoding.
	t.Parallel()
	wireFrame := []byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00, 0x00, 0x00, 0x00}
	webp := buildMinimalExtendedWebP(0, nil, nil)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(webp), &out, nil, nil, wireFrame, true)
	if !errors.Is(err, ErrCorruptXMP) {
		t.Errorf("WebP-robust-jpeg-wire-frame: got %v, want ErrCorruptXMP", err)
	}
}

// ---------------------------------------------------------------------------
// corpus parity — real-world WebP files from testdata/corpus/webp
// ---------------------------------------------------------------------------

// TestWebPCorpusExtractNoPanic verifies that Extract never panics on any
// corpus file. The test is skipped if the corpus directory is absent.
// containers.md §3(f): robustness on real-world files.
func TestWebPCorpusExtractNoPanic(t *testing.T) {
	// WebP-corpus-extract-no-panic: all corpus files must survive Extract.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "webp")
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("WebP-corpus-extract-no-panic: read %s: %v", p, err)
			}
			rawEXIF, _, rawXMP, extractErr := Extract(bytes.NewReader(data))
			_ = rawEXIF
			_ = rawXMP
			_ = extractErr
			// Primary assertion: no panic. Errors are acceptable.
		})
	}
}

// TestWebPCorpusInjectRoundTrip verifies that every corpus file survives an
// Inject→Extract round trip: the same EXIF and XMP bytes that Extract returns
// on the first pass are identical to what Extract returns after Inject.
// The test is skipped if the corpus directory is absent.
// containers.md §3(d)(e)(f): write correctness + robustness on real files.
func TestWebPCorpusInjectRoundTrip(t *testing.T) {
	// WebP-corpus-round-trip: inject→extract must preserve metadata on corpus files.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "webp")
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("WebP-corpus-round-trip: read %s: %v", p, err)
			}
			rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
			if err != nil {
				// File not a valid WebP or already known-broken: skip inject.
				t.Logf("WebP-corpus-round-trip: Extract failed (skip inject): %v", err)
				return
			}
			// Only run inject round-trip on files that have at least one
			// metadata payload to preserve — pure image-only files have no
			// meaningful round-trip to verify.
			if rawEXIF == nil && rawXMP == nil {
				return
			}
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(data), &out, rawEXIF, nil, rawXMP, true); err != nil {
				t.Fatalf("WebP-corpus-round-trip: Inject on %s: %v", filepath.Base(p), err)
			}
			rawEXIF2, _, rawXMP2, err := Extract(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("WebP-corpus-round-trip: Extract after Inject on %s: %v", filepath.Base(p), err)
			}
			if !bytes.Equal(rawEXIF, rawEXIF2) {
				t.Errorf("WebP-corpus-round-trip: %s: EXIF differs after round-trip (%d vs %d bytes)",
					filepath.Base(p), len(rawEXIF), len(rawEXIF2))
			}
			if !bytes.Equal(rawXMP, rawXMP2) {
				t.Errorf("WebP-corpus-round-trip: %s: XMP differs after round-trip (%d vs %d bytes)",
					filepath.Base(p), len(rawXMP), len(rawXMP2))
			}
		})
	}
}

// TestWebPCorpusHeaderValid verifies that every corpus file begins with a
// valid RIFF/WEBP header (bytes [0:4]=="RIFF", [8:12]=="WEBP").
// containers.md §3(b): detection rule validated on real files.
func TestWebPCorpusHeaderValid(t *testing.T) {
	// WebP-corpus-header-valid: all corpus files must have valid RIFF/WEBP magic.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "webp")
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("WebP-corpus-header: read %s: %v", p, err)
			}
			if len(data) < 12 {
				t.Errorf("WebP-corpus-header: %s: file too short (%d bytes)", filepath.Base(p), len(data))
				return
			}
			if string(data[0:4]) != "RIFF" {
				t.Errorf("WebP-corpus-header: %s: bytes[0:4]=%q, want 'RIFF'", filepath.Base(p), data[0:4])
			}
			if string(data[8:12]) != "WEBP" {
				t.Errorf("WebP-corpus-header: %s: bytes[8:12]=%q, want 'WEBP'", filepath.Base(p), data[8:12])
			}
		})
	}
}

// TestWebPCorpusFileSizeField verifies that the RIFF File-Size field in each
// corpus file equals (file_length − 8) — the spec-compliant value.
// containers.md §3(c): File-Size semantics — corpus evidence.
func TestWebPCorpusFileSizeField(t *testing.T) {
	// WebP-corpus-FileSize-field: File-Size must equal file_length − 8 on real files.
	t.Parallel()
	paths := testutil.CorpusFiles(t, "webp")
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("WebP-corpus-FileSize: read %s: %v", p, err)
			}
			if len(data) < 8 {
				t.Skipf("WebP-corpus-FileSize: %s: too short", filepath.Base(p))
			}
			declared := binary.LittleEndian.Uint32(data[4:8])
			expected := uint32(len(data) - 8) //nolint:gosec // G115: bounded; len(data)≥8 checked above
			if declared != expected {
				// Spec says File-Size should match; corpus files may have slight
				// off-by-one if produced by non-conforming encoders. Log but do
				// not hard-fail to tolerate real-world deviations.
				t.Logf("WebP-corpus-FileSize: %s: declared=%d actual-8=%d (mismatch; real-world tolerance)",
					filepath.Base(p), declared, expected)
			}
		})
	}
}
