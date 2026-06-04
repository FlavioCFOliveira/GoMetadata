package webp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// buildWebP constructs a minimal WebP RIFF stream.
// vp8xFlags: if non-zero, a VP8X chunk is prepended with those flags.
// canvasW, canvasH: canvas dimensions for VP8X (stored as width-1, height-1).
func buildWebP(exifData, xmpData []byte, vp8xFlags uint32, canvasW, canvasH uint32) []byte {
	var body bytes.Buffer

	if vp8xFlags != 0 || exifData != nil || xmpData != nil {
		vp8xPayload := make([]byte, 10)
		binary.LittleEndian.PutUint32(vp8xPayload[0:], vp8xFlags)
		// Canvas: (width-1) in 3 bytes LE, (height-1) in 3 bytes LE
		if canvasW > 0 {
			w := canvasW - 1
			vp8xPayload[4] = byte(w)       //nolint:gosec // G115: test helper, intentional type cast
			vp8xPayload[5] = byte(w >> 8)  //nolint:gosec // G115: test helper, intentional type cast
			vp8xPayload[6] = byte(w >> 16) //nolint:gosec // G115: test helper, intentional type cast
		}
		if canvasH > 0 {
			h := canvasH - 1
			vp8xPayload[7] = byte(h)       //nolint:gosec // G115: test helper, intentional type cast
			vp8xPayload[8] = byte(h >> 8)  //nolint:gosec // G115: test helper, intentional type cast
			vp8xPayload[9] = byte(h >> 16) //nolint:gosec // G115: test helper, intentional type cast
		}
		writeRIFFChunk(&body, "VP8X", vp8xPayload)
	}

	// Minimal VP8 image data.
	vp8Data := []byte{0x30, 0x01, 0x00, 0x9d, 0x01, 0x2a, 0x01, 0x00, 0x01, 0x00}
	writeRIFFChunk(&body, "VP8 ", vp8Data)

	if exifData != nil {
		writeRIFFChunk(&body, "EXIF", exifData)
	}
	if xmpData != nil {
		writeRIFFChunk(&body, "XMP ", xmpData)
	}

	totalSize := 4 + body.Len()
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize)) //nolint:gosec // G115: test helper, intentional type cast
	copy(riffHdr[8:], "WEBP")

	var out bytes.Buffer
	out.Write(riffHdr)
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestExtractEXIF(t *testing.T) {
	t.Parallel()
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	webp := buildWebP(exifData, nil, 0x08, 0, 0)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("rawEXIF is nil")
	}
	if !bytes.Equal(rawEXIF, exifData) {
		t.Errorf("rawEXIF = %v, want %v", rawEXIF, exifData)
	}
	if rawXMP != nil {
		t.Error("expected nil rawXMP")
	}
}

func TestExtractXMP(t *testing.T) {
	t.Parallel()
	xmpData := []byte("<?xpacket begin='' uid='x'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>")
	webp := buildWebP(nil, xmpData, 0x04, 0, 0)

	_, _, rawXMP, err := Extract(bytes.NewReader(webp))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("rawXMP = %v, want %v", rawXMP, xmpData)
	}
}

func TestInjectPreservesCanvasDimensions(t *testing.T) {
	t.Parallel()
	// Build a WebP with VP8X and specific canvas dimensions.
	const canvasW, canvasH = uint32(1024), uint32(768)
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	webp := buildWebP(exifData, nil, 0x08, canvasW, canvasH)

	newEXIF := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	result := out.Bytes()
	// Find VP8X chunk (it should start at byte 12 in a well-formed WebP).
	if len(result) < 12+8+10 {
		t.Fatal("output too short to contain VP8X")
	}
	// Scan for VP8X.
	pos := 12
	for pos+8 <= len(result) {
		chunkID := string(result[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(result[pos+4:]))
		if chunkID == "VP8X" && chunkSize >= 10 {
			payload := result[pos+8 : pos+8+10]
			// Canvas width: bytes 4-6 (3 bytes LE) = width-1
			w := uint32(payload[4]) | uint32(payload[5])<<8 | uint32(payload[6])<<16 + 1
			// Canvas height: bytes 7-9 (3 bytes LE) = height-1
			h := uint32(payload[7]) | uint32(payload[8])<<8 | uint32(payload[9])<<16 + 1
			if w != canvasW {
				t.Errorf("canvas width: got %d, want %d", w, canvasW)
			}
			if h != canvasH {
				t.Errorf("canvas height: got %d, want %d", h, canvasH)
			}
			return
		}
		pos += 8 + chunkSize
		if chunkSize%2 != 0 {
			pos++
		}
	}
	t.Error("VP8X chunk not found in output")
}

func TestInjectRoundTrip(t *testing.T) {
	t.Parallel()
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	webp := buildWebP(nil, nil, 0, 0, 0)

	newEXIF := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(webp), &out, newEXIF, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !bytes.Equal(rawEXIF, newEXIF) {
		t.Errorf("EXIF after inject: got %v, want %v", rawEXIF, newEXIF)
	}
	_ = exifData
}

func BenchmarkWebPExtract(b *testing.B) {
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	webp := buildWebP(exifData, nil, 0x08, 1920, 1080)
	b.SetBytes(int64(len(webp)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(webp))
	}
}

// TestExtractNotWebP verifies that Extract returns ErrNotWebP for a non-WebP file.
func TestExtractNotWebP(t *testing.T) {
	t.Parallel()
	notWebP := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}
	_, _, _, err := Extract(bytes.NewReader(notWebP))
	if err == nil {
		t.Error("Extract: expected ErrNotWebP, got nil")
	}
}

// TestExtractTooShort verifies that Extract returns an error on too-short input.
func TestExtractTooShort(t *testing.T) {
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{0x52, 0x49, 0x46, 0x46})) // just "RIFF"
	if err == nil {
		t.Error("Extract: expected error for too-short input, got nil")
	}
}

// TestInjectTooShort verifies that Inject returns ErrFileTooShort for <12 bytes.
func TestInjectTooShort(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := Inject(bytes.NewReader([]byte{0x01, 0x02}), &out, nil, nil, nil, true)
	if err == nil {
		t.Error("Inject: expected ErrFileTooShort, got nil")
	}
}

// TestReadPaddedChunkOddSize verifies that readPaddedChunk correctly advances
// past the odd-size padding byte (RIFF spec).
func TestReadPaddedChunkOddSize(t *testing.T) {
	t.Parallel()
	// Build a WebP with an odd-sized EXIF chunk (5 bytes).
	oddExif := []byte{0x49, 0x49, 0x2A, 0x00, 0x08} // 5 bytes — odd

	var body bytes.Buffer
	// Manually write VP8X chunk (10 bytes, even).
	vp8xPayload := make([]byte, 10)
	binary.LittleEndian.PutUint32(vp8xPayload[0:], 0x08) // EXIF flag
	writeRIFFChunk(&body, "VP8X", vp8xPayload)

	// Write EXIF chunk with odd size — RIFF padding byte follows.
	chunkHdr := make([]byte, 8)
	copy(chunkHdr[:4], "EXIF")
	binary.LittleEndian.PutUint32(chunkHdr[4:], uint32(len(oddExif))) //nolint:gosec // G115: safe test helper
	body.Write(chunkHdr)
	body.Write(oddExif)
	body.WriteByte(0x00) // padding byte

	totalSize := 4 + body.Len()
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize)) //nolint:gosec // G115: safe test helper
	copy(riffHdr[8:], "WEBP")

	var buf bytes.Buffer
	buf.Write(riffHdr)
	buf.Write(body.Bytes())

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract with odd-size EXIF: %v", err)
	}
	if !bytes.Equal(rawEXIF, oddExif) {
		t.Errorf("EXIF: got %v, want %v", rawEXIF, oddExif)
	}
}

// TestBuildVP8XFlagsNoOriginal verifies that buildVP8XFlags sets flags correctly
// when there is no original VP8X chunk (origVP8XData is nil).
func TestBuildVP8XFlagsNoOriginal(t *testing.T) {
	t.Parallel()
	flags := buildVP8XFlags(true, true, nil)
	if len(flags) < 4 {
		t.Fatal("buildVP8XFlags returned too few bytes")
	}
	f := binary.LittleEndian.Uint32(flags[0:4])
	if f&0x08 == 0 {
		t.Error("EXIF flag (bit 3) not set")
	}
	if f&0x04 == 0 {
		t.Error("XMP flag (bit 2) not set")
	}
}

// TestReadPaddedChunkTooLarge is a DoS regression test: a WebP whose EXIF chunk
// declares size 0xFFFFFFFF (≈4 GiB) must be rejected by ErrChunkTooLarge
// without attempting to allocate or read that many bytes.
func TestReadPaddedChunkTooLarge(t *testing.T) {
	t.Parallel()

	// Craft a minimal RIFF/WEBP stream with a VP8X chunk (so the file header is
	// valid) followed by an EXIF chunk whose declared size is 0xFFFFFFFF.
	// No payload bytes follow — the guard must fire before any read attempt.
	var body bytes.Buffer

	// VP8X chunk (10 bytes, EXIF flag set) — gives the parser a valid first chunk.
	vp8xPayload := make([]byte, 10)
	binary.LittleEndian.PutUint32(vp8xPayload[0:], 0x08) // EXIF feature bit
	writeRIFFChunk(&body, "VP8X", vp8xPayload)

	// EXIF chunk header with giant declared size; no payload bytes written.
	var exifHdr [8]byte
	copy(exifHdr[:4], "EXIF")
	binary.LittleEndian.PutUint32(exifHdr[4:], 0xFFFFFFFF)
	body.Write(exifHdr[:])

	totalSize := 4 + body.Len() // "WEBP" + chunks
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize)) //nolint:gosec // G115: test helper, intentional type cast
	copy(riffHdr[8:], "WEBP")

	var buf bytes.Buffer
	buf.Write(riffHdr)
	buf.Write(body.Bytes())

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("Extract: expected error for oversized chunk, got nil")
	}
	if !errors.Is(err, ErrChunkTooLarge) {
		t.Errorf("Extract: expected ErrChunkTooLarge, got %v", err)
	}
}

// TestReadPaddedChunkSizeLargerThanStream is a DoS regression test for the
// stream-availability guard added to readPaddedChunk.
//
// A WebP file can be crafted so that a chunk's declared size (e.g. ~200 MiB)
// is far larger than the actual bytes in the stream (e.g. 50 bytes total).
// Before the fix, readPaddedChunk would call make([]byte, chunk.Size) —
// allocating ~200 MiB — before io.ReadFull detected the truncation.
//
// After the fix, the Seek-based availability check detects the mismatch and
// returns ErrChunkTooLarge without performing any proportional allocation.
func TestReadPaddedChunkSizeLargerThanStream(t *testing.T) {
	t.Parallel()

	// Declare EXIF chunk size as ~200 MiB; write no payload bytes at all.
	// The total file is tiny (~30 bytes), so the stream-availability guard
	// must fire before any allocation proportional to the declared size.
	const declaredSize = 200 << 20 // 200 MiB

	var body bytes.Buffer

	// VP8X chunk (10 bytes, EXIF flag set) — valid first chunk so the parser
	// reaches the EXIF chunk header before encountering the oversized size.
	vp8xPayload := make([]byte, 10)
	binary.LittleEndian.PutUint32(vp8xPayload[0:], 0x08) // EXIF feature bit
	writeRIFFChunk(&body, "VP8X", vp8xPayload)

	// EXIF chunk header with a large declared size; no payload bytes follow.
	var exifHdr [8]byte
	copy(exifHdr[:4], "EXIF")
	binary.LittleEndian.PutUint32(exifHdr[4:], declaredSize)
	body.Write(exifHdr[:])

	totalSize := 4 + body.Len() // "WEBP" + chunks
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize)) //nolint:gosec // G115: test helper
	copy(riffHdr[8:], "WEBP")

	var buf bytes.Buffer
	buf.Write(riffHdr)
	buf.Write(body.Bytes())

	// The stream is ~50 bytes; declared size is 200 MiB.
	// The availability guard must reject before any large allocation.
	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("Extract: expected error for chunk size > stream, got nil")
	}
	if !errors.Is(err, ErrChunkTooLarge) {
		t.Errorf("Extract: expected ErrChunkTooLarge, got %v", err)
	}
}

// TestInjectTruncatedVP8XNoPanic is the regression test for the OOB-slice panic
// triggered by origVP8XData[:10] when the VP8X chunk is truncated.
//
// Reproduction: build a RIFF/WEBP whose VP8X chunk *declares* size=10 but
// whose data region is only 4 bytes (truncated file). Copy the bytes into an
// exactly-sized backing array (cap == len == buffer length) so no spare
// capacity hides the bug. Call Inject with rawEXIF to force a VP8X rebuild.
//
// Before the fix: copy(vp8xData, origVP8XData[:10]) panics with
// "slice bounds out of range [:10] with capacity 4".
// After the fix: Inject returns an error or valid output without panicking.
func TestInjectTruncatedVP8XNoPanic(t *testing.T) {
	t.Parallel()

	// Craft the file: RIFF header (12 bytes) + VP8X chunk header (8 bytes) +
	// 4 data bytes only (declared size is 10 — truncated).
	//
	// Layout:
	//   [0..3]   "RIFF"
	//   [4..7]   file body size LE = 4 ("WEBP") + 8 (chunk hdr) + 4 (data) = 16
	//   [8..11]  "WEBP"
	//   [12..15] "VP8X"
	//   [16..19] uint32 LE = 10  (declared chunk size — larger than actual data)
	//   [20..23] 4 data bytes    (truncated: only 4 of the 10 declared bytes present)
	const totalLen = 12 + 8 + 4 // 24 bytes
	raw := []byte{
		'R', 'I', 'F', 'F',
		16, 0, 0, 0, // body size: "WEBP"(4) + VP8X header(8) + 4 data bytes = 16
		'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X',
		10, 0, 0, 0, // declared size = 10
		0x01, 0x02, 0x03, 0x04, // only 4 data bytes — truncated
	}

	// Copy into a backing array whose capacity equals its length so that any
	// attempt at origVP8XData[:10] on a 4-byte slice panics immediately.
	// bytes.Buffer.Bytes() over-allocates; only make+copy guarantees cap==len.
	out := make([]byte, totalLen)
	copy(out, raw)

	rawEXIF := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}

	// Use a recover-based wrapper so a panic is reported as a test failure
	// rather than terminating the test binary (proving the fix prevents the crash).
	didPanic := func() (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				t.Errorf("Inject panicked with truncated VP8X: %v", r)
			}
		}()
		var buf bytes.Buffer
		_ = Inject(bytes.NewReader(out), &buf, rawEXIF, nil, nil, true)
		return false
	}()

	if didPanic {
		t.Fatal("Inject must not panic on a truncated VP8X chunk")
	}
}

// buildTruncatedVP8XWebP constructs a WebP where the VP8X chunk declares
// size=10 in its header but only actualDataBytes of VP8X data are present
// before the next chunk (VP8). The next-chunk FourCC and size bytes are
// written immediately after the truncated VP8X data so they are within the
// file boundary — making dataEnd-dataStart == 10 possible in collectOriginalChunks.
// nextChunkFirstByte is the first byte of the adjacent chunk's size field,
// used to make contamination detectable (e.g. 0xFF means an unmistakable value).
func buildTruncatedVP8XWebP(actualDataBytes int, vp8xFlags uint32, nextChunkFirstByte byte) []byte {
	var body bytes.Buffer

	// VP8X chunk: declare size=10 but only write actualDataBytes.
	body.Write([]byte{'V', 'P', '8', 'X'})
	var vp8xHdrSize [4]byte
	binary.LittleEndian.PutUint32(vp8xHdrSize[:], 10) // DECLARED size = 10
	body.Write(vp8xHdrSize[:])
	vp8xPartial := make([]byte, actualDataBytes)
	binary.LittleEndian.PutUint32(vp8xPartial[:min(4, actualDataBytes)], vp8xFlags)
	body.Write(vp8xPartial)
	// Do NOT write the remaining bytes — the next chunk follows immediately.

	// VP8 image chunk (minimal). Its FourCC bytes will be at position
	// dataStart+actualDataBytes inside the flat original[] slice.
	// Use nextChunkFirstByte as the first byte of the VP8 payload so the test
	// can detect whether it leaked into the canvas region.
	vp8Payload := []byte{nextChunkFirstByte, 0x01, 0x00, 0x9d}
	body.Write([]byte{'V', 'P', '8', ' '})
	var vp8Size [4]byte
	binary.LittleEndian.PutUint32(vp8Size[:], uint32(len(vp8Payload))) //nolint:gosec // G115: test helper
	body.Write(vp8Size[:])
	body.Write(vp8Payload)

	totalSize := 4 + body.Len()
	riffHdr := make([]byte, 12)
	copy(riffHdr[:4], "RIFF")
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize)) //nolint:gosec // G115: test helper
	copy(riffHdr[8:], "WEBP")

	var out bytes.Buffer
	out.Write(riffHdr)
	out.Write(body.Bytes())
	return out.Bytes()
}

// TestInjectVP8XTruncatedChunkNoCrossChunkRead is the acceptance-criteria
// regression for rmp task #57.
//
// Scenario: a WebP file declares VP8X size=10 but only 5 data bytes exist
// before the adjacent VP8 chunk whose FourCC starts with 0xFF as first byte.
// The 10 bytes available at dataStart are: 5 VP8X bytes + 5 bytes from the VP8
// chunk header (FourCC + first byte of size). Without the fix, dataEnd-dataStart
// equals 10 (the clamp does not help), so origVP8XData contains those 5
// contaminating bytes, and canvas bytes [4:10] will contain VP8 chunk header
// bytes instead of zeros.
//
// The fix must ensure canvas bytes [4:10] are all zero when the VP8X data is
// genuinely shorter than 10 bytes.
//
// Fail-before-fix: canvas bytes contain VP8 FourCC bytes ('V','P','8',' ') +
// first size byte rather than zeros.
// Pass-after-fix: canvas bytes [4:10] are all zero; flags byte = 0x08 (EXIF).
func TestInjectVP8XTruncatedChunkNoCrossChunkRead(t *testing.T) {
	t.Parallel()

	// 5 real VP8X data bytes; VP8 chunk starts immediately after.
	// nextChunkFirstByte=0xFF is used to make any leak obvious (0xFF in canvas).
	input := buildTruncatedVP8XWebP(5, 0x00, 0xFF)

	// Inject an XMP payload to force VP8X rebuild with the XMP flag set.
	xmpPayload := []byte("<x:xmpmeta/>")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(input), &out, nil, nil, xmpPayload, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	result := out.Bytes()
	// Locate the VP8X chunk in the output.
	pos := 12
	for pos+8 <= len(result) {
		chunkID := string(result[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(result[pos+4:]))
		if chunkID == "VP8X" {
			if chunkSize < 10 {
				t.Fatalf("VP8X output chunk size %d < 10", chunkSize)
			}
			payload := result[pos+8 : pos+8+10]

			// flags [0:4]: only XMP bit (0x04) must be set; no contamination.
			flags := binary.LittleEndian.Uint32(payload[0:4])
			if flags != 0x04 {
				t.Errorf("VP8X flags = 0x%08X, want 0x00000004 (XMP-only)", flags)
			}

			// Canvas bytes [4:10]: must all be zero — VP8X was truncated, so no
			// dimension information is available; buildVP8XFlags must not copy
			// bytes from the adjacent VP8 chunk into this region.
			for i := 4; i < 10; i++ {
				if payload[i] != 0x00 {
					t.Errorf("canvas byte [%d] = 0x%02X, want 0x00 (cross-chunk leak)", i, payload[i])
				}
			}
			return
		}
		pos += 8 + chunkSize
		if chunkSize%2 != 0 {
			pos++
		}
	}
	t.Error("VP8X chunk not found in Inject output")
}

// TestInjectPreservesVP8XFeatureFlagsAndCanvas verifies that when a valid
// VP8X chunk (10 real bytes, within file bounds) is present, Inject preserves
// all feature flag bits OTHER than EXIF/XMP (e.g. ICC=0x20, Alpha=0x10,
// Animation=0x02) and the full canvas dimensions, while correctly setting
// only the requested EXIF/XMP bits.
func TestInjectPreservesVP8XFeatureFlagsAndCanvas(t *testing.T) {
	t.Parallel()

	// Original VP8X: ICC (0x20) + Alpha (0x10) bits set; canvas 800x600.
	const (
		origFlags = uint32(0x20 | 0x10) // ICC + Alpha
		canvasW   = uint32(800)
		canvasH   = uint32(600)
		wantFlags = uint32(0x20 | 0x10 | 0x04) // ICC + Alpha + XMP (added)
	)

	input := buildWebP(nil, nil, origFlags, canvasW, canvasH)

	xmpPayload := []byte("<x:xmpmeta/>")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(input), &out, nil, nil, xmpPayload, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	result := out.Bytes()
	pos := 12
	for pos+8 <= len(result) {
		chunkID := string(result[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(result[pos+4:]))
		if chunkID == "VP8X" {
			if chunkSize < 10 {
				t.Fatalf("VP8X output chunk size %d < 10", chunkSize)
			}
			payload := result[pos+8 : pos+8+10]

			gotFlags := binary.LittleEndian.Uint32(payload[0:4])
			if gotFlags != wantFlags {
				t.Errorf("VP8X flags = 0x%08X, want 0x%08X", gotFlags, wantFlags)
			}

			// Canvas: 3-byte LE width-1, height-1.
			gotW := uint32(payload[4]) | uint32(payload[5])<<8 | uint32(payload[6])<<16 + 1
			gotH := uint32(payload[7]) | uint32(payload[8])<<8 | uint32(payload[9])<<16 + 1
			if gotW != canvasW {
				t.Errorf("canvas width = %d, want %d", gotW, canvasW)
			}
			if gotH != canvasH {
				t.Errorf("canvas height = %d, want %d", gotH, canvasH)
			}
			return
		}
		pos += 8 + chunkSize
		if chunkSize%2 != 0 {
			pos++
		}
	}
	t.Error("VP8X chunk not found in Inject output")
}

// BenchmarkWebPInject measures the full Inject path: rebuild the RIFF body
// with updated EXIF and XMP chunks using the pooled bytes.Buffer.
func BenchmarkWebPInject(b *testing.B) {
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	xmpData := []byte("<?xpacket begin='' uid='x'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>")
	webp := buildWebP(exifData, nil, 0x08, 1920, 1080)
	b.SetBytes(int64(len(webp)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(webp), &out, exifData, nil, xmpData, true)
	}
}

// TestCollectOriginalChunksLargeSize is the 32-bit-safe regression test for
// task #74 (WebP path).
//
// collectOriginalChunks historically used `size := int(binary.LittleEndian.Uint32(...))`.
// On a 32-bit platform (GOARCH=386/arm, int=32 bits), a RIFF chunk size field of
// 0x80000000 or higher becomes negative after the int cast. The subsequent
// `dataStart+size` expression then underflows, `dataEnd` is clamped to a wrong
// offset by min(), and subsequent chunks are silently dropped or miscounted.
//
// After the fix, collectOriginalChunks checks `rawSize > math.MaxInt32` and breaks
// early, treating the rest of the stream as unreadable. The test exercises Inject
// (which calls collectOriginalChunks internally) with such a stream and verifies
// that it completes without panicking. Inject is expected to succeed (writing a
// valid output with only the provided metadata), never panic.
func TestCollectOriginalChunksLargeSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rawSize uint32
	}{
		{"0x80000000", 0x80000000},
		{"0x80000001", 0x80000001},
		{"0xFFFFFFFF", 0xFFFFFFFF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a RIFF/WEBP stream with a VP8X chunk (valid) followed by a chunk
			// whose declared size is the oversized value. The oversized chunk body is
			// empty — we only write the 8-byte RIFF chunk header so the stream is
			// short enough to fit in a test buffer, but the declared size is huge.
			var buf bytes.Buffer
			// Write a VP8X chunk (10 bytes payload, flags for EXIF).
			vp8xPayload := make([]byte, 10)
			binary.LittleEndian.PutUint32(vp8xPayload[0:], 0x08) // EXIF flag
			writeRIFFChunk(&buf, "VP8X", vp8xPayload)
			// Write a chunk with oversized declared size but no actual body bytes
			// (simulates a truncated or adversarial stream).
			var oversized [8]byte
			copy(oversized[:4], "VP8 ")
			binary.LittleEndian.PutUint32(oversized[4:], tc.rawSize)
			buf.Write(oversized[:])

			// Wrap in a RIFF/WEBP envelope.
			body := buf.Bytes()
			totalSize := 4 + len(body)
			stream := make([]byte, 0, 12+len(body))
			stream = append(stream, 'R', 'I', 'F', 'F')
			var sizeBuf [4]byte
			binary.LittleEndian.PutUint32(sizeBuf[:], uint32(totalSize)) //nolint:gosec // G115: test helper, intentional type cast
			stream = append(stream, sizeBuf[:]...)
			stream = append(stream, 'W', 'E', 'B', 'P')
			stream = append(stream, body...)

			// Inject must not panic; the result may be an error or a valid output.
			exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
			var out bytes.Buffer
			_ = Inject(bytes.NewReader(stream), &out, exifData, nil, nil, true)
			// Primary assertion: we reached here without panicking.
		})
	}
}
