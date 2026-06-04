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
	if err := Inject(bytes.NewReader(webp), &out, newEXIF, nil, nil); err != nil {
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
	if err := Inject(bytes.NewReader(webp), &out, newEXIF, nil, nil); err != nil {
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
	err := Inject(bytes.NewReader([]byte{0x01, 0x02}), &out, nil, nil, nil)
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
		_ = Inject(bytes.NewReader(out), &buf, rawEXIF, nil, nil)
		return false
	}()

	if didPanic {
		t.Fatal("Inject must not panic on a truncated VP8X chunk")
	}
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
		_ = Inject(bytes.NewReader(webp), &out, exifData, nil, xmpData)
	}
}
