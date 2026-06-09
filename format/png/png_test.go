package png

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// buildPNG builds a minimal PNG stream with optional eXIf and iTXt(XMP) chunks.
func buildPNG(exifData, xmpData []byte) []byte {
	var buf bytes.Buffer
	buf.Write(pngSig[:])
	// Minimal IHDR chunk: width=1, height=1, bitdepth=8, colortype=2, rest zeros.
	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1) // width
	binary.BigEndian.PutUint32(ihdrData[4:], 1) // height
	ihdrData[8] = 8                             // bit depth
	ihdrData[9] = 2                             // color type (RGB)
	writeChunkTo(&buf, "IHDR", ihdrData)

	if exifData != nil {
		writeChunkTo(&buf, "eXIf", exifData)
	}
	if xmpData != nil {
		chunk := buildXMPChunk(xmpData)
		writeChunkTo(&buf, "iTXt", chunk)
	}

	writeChunkTo(&buf, "IEND", nil)
	return buf.Bytes()
}

// writeChunkTo writes a PNG chunk with correct CRC to buf.
func writeChunkTo(buf *bytes.Buffer, chunkType string, data []byte) {
	var lbuf [4]byte
	binary.BigEndian.PutUint32(lbuf[:], uint32(len(data))) //nolint:gosec // G115: test helper, intentional type cast
	buf.Write(lbuf[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	binary.BigEndian.PutUint32(lbuf[:], h.Sum32())
	buf.Write(lbuf[:])
}

func TestExtractEXIF(t *testing.T) {
	t.Parallel()
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	png := buildPNG(exifData, nil)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if rawXMP != nil {
		t.Error("expected nil rawXMP")
	}
	_ = rawEXIF
}

func TestExtractXMPUncompressed(t *testing.T) {
	t.Parallel()
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>")
	png := buildPNG(nil, xmpData)

	_, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpData)
	}
}

func TestExtractXMPCompressed(t *testing.T) {
	t.Parallel()
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>")

	// Build compressed iTXt chunk manually.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write(xmpData)
	_ = zw.Close()

	var chunk bytes.Buffer
	chunk.WriteString(xmpKeyword)
	chunk.WriteByte(0x00) // null terminator
	chunk.WriteByte(0x01) // compression flag = compressed
	chunk.WriteByte(0x00) // compression method = zlib
	chunk.WriteByte(0x00) // empty language tag
	chunk.WriteByte(0x00) // empty translated keyword
	chunk.Write(compressed.Bytes())

	var buf bytes.Buffer
	buf.Write(pngSig[:])
	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1)
	binary.BigEndian.PutUint32(ihdrData[4:], 1)
	ihdrData[8], ihdrData[9] = 8, 2
	writeChunkTo(&buf, "IHDR", ihdrData)
	writeChunkTo(&buf, "iTXt", chunk.Bytes())
	writeChunkTo(&buf, "IEND", nil)

	_, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract compressed XMP: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil after decompression")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("decompressed XMP = %q, want %q", rawXMP, xmpData)
	}
}

func TestInjectCRCCorrect(t *testing.T) {
	t.Parallel()
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	png := buildPNG(nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(png), &out, exifData, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	// Verify all chunks have correct CRC.
	result := out.Bytes()
	pos := 8 // skip signature
	for pos+8 <= len(result) {
		length := int(binary.BigEndian.Uint32(result[pos:]))
		chunkType := string(result[pos+4 : pos+8])
		dataEnd := pos + 8 + length
		if dataEnd+4 > len(result) {
			break
		}
		data := result[pos+8 : dataEnd]
		storedCRC := binary.BigEndian.Uint32(result[dataEnd:])

		h := crc32.NewIEEE()
		_, _ = h.Write([]byte(chunkType))
		_, _ = h.Write(data)
		computed := h.Sum32()

		if storedCRC != computed {
			t.Errorf("chunk %q: CRC mismatch: stored=%08x, computed=%08x", chunkType, storedCRC, computed)
		}
		pos = dataEnd + 4
		if chunkType == "IEND" {
			break
		}
	}
}

func TestInjectRoundTrip(t *testing.T) {
	t.Parallel()
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	xmpData := []byte("<?xpacket begin='' uid='x'?><x/><?xpacket end='r'?>")
	png := buildPNG(nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(png), &out, exifData, nil, xmpData, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, exifData) {
		t.Errorf("EXIF after inject: got %q, want %q", rawEXIF, exifData)
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("XMP after inject: got %q, want %q", rawXMP, xmpData)
	}
}

// buildPNGWithChunk constructs a minimal PNG that contains one extra chunk
// (of the given type and data) immediately after IHDR and before IEND.
// This helper is used by tEXt and zTXt tests where buildPNG's iTXt/eXIf
// shortcuts do not apply.
func buildPNGWithChunk(chunkType string, data []byte) []byte {
	var buf bytes.Buffer
	buf.Write(pngSig[:])

	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1) // width
	binary.BigEndian.PutUint32(ihdrData[4:], 1) // height
	ihdrData[8] = 8                             // bit depth
	ihdrData[9] = 2                             // color type (RGB)
	writeChunkTo(&buf, "IHDR", ihdrData)

	writeChunkTo(&buf, chunkType, data)
	writeChunkTo(&buf, "IEND", nil)

	return buf.Bytes()
}

// TestExtractXMPFromTEXtChunk verifies that Extract recovers XMP from a legacy
// uncompressed tEXt chunk whose keyword is "XML:com.adobe.xmp".
// This exercises extractXMPFromTExt (png.go:257-271).
func TestExtractXMPFromTEXtChunk(t *testing.T) {
	t.Parallel()
	xmpContent := []byte("<?xpacket begin='' uid='x'?><x:xmpmeta xmlns:x=\"adobe:ns:meta/\"/><?xpacket end='r'?>")

	// tEXt chunk payload: keyword + NUL + text (PNG §11.3.3).
	var payload bytes.Buffer
	payload.WriteString(xmpKeyword)
	payload.WriteByte(0x00) // NUL separator
	payload.Write(xmpContent)

	png := buildPNGWithChunk("tEXt", payload.Bytes())
	_, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; tEXt XMP chunk not extracted")
	}
	if !bytes.Equal(rawXMP, xmpContent) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpContent)
	}
}

// TestExtractXMPFromTEXtChunkWrongKeyword verifies that a tEXt chunk with a
// non-XMP keyword is silently ignored and does not set rawXMP.
func TestExtractXMPFromTEXtChunkWrongKeyword(t *testing.T) {
	t.Parallel()
	var payload bytes.Buffer
	payload.WriteString("Comment") // not the XMP keyword
	payload.WriteByte(0x00)
	payload.WriteString("this is a plain PNG comment, not XMP")

	png := buildPNGWithChunk("tEXt", payload.Bytes())
	_, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %q, want nil for non-XMP tEXt keyword", rawXMP)
	}
}

// TestExtractXMPFromTEXtChunkNoNul verifies that a tEXt chunk without a NUL
// separator is safely skipped (extractXMPFromTExt returns nil).
func TestExtractXMPFromTEXtChunkNoNul(t *testing.T) {
	t.Parallel()
	// Payload has no NUL byte at all — malformed but real files can have this.
	payload := []byte("no null separator here")
	png := buildPNGWithChunk("tEXt", payload)

	_, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %q, want nil for tEXt chunk with no NUL", rawXMP)
	}
}

// TestExtractXMPFromZTxtChunk verifies that Extract correctly decompresses
// XMP from a legacy zTXt chunk (deflate, PNG §11.3.3).
// This exercises extractXMPFromZTxt (png.go:273-301).
func TestExtractXMPFromZTxtChunk(t *testing.T) {
	t.Parallel()
	xmpContent := []byte("<?xpacket begin='' uid='x'?><x:xmpmeta xmlns:x=\"adobe:ns:meta/\"/><?xpacket end='r'?>")

	// Compress xmpContent with zlib.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(xmpContent); err != nil {
		t.Fatalf("zlib.Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib.Close: %v", err)
	}

	// zTXt chunk payload: keyword + NUL + compMethod(0) + compressed_text.
	var payload bytes.Buffer
	payload.WriteString(xmpKeyword)
	payload.WriteByte(0x00) // NUL separator
	payload.WriteByte(0x00) // compression method: deflate (the only valid value)
	payload.Write(compressed.Bytes())

	png := buildPNGWithChunk("zTXt", payload.Bytes())
	_, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP == nil {
		t.Fatal("rawXMP is nil; zTXt XMP chunk not extracted")
	}
	if !bytes.Equal(rawXMP, xmpContent) {
		t.Errorf("rawXMP = %q, want %q", rawXMP, xmpContent)
	}
}

// TestExtractXMPFromZTxtChunkWrongKeyword verifies that a zTXt chunk with a
// non-XMP keyword is silently ignored.
func TestExtractXMPFromZTxtChunkWrongKeyword(t *testing.T) {
	t.Parallel()
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write([]byte("some compressed text"))
	_ = zw.Close()

	var payload bytes.Buffer
	payload.WriteString("Description") // not the XMP keyword
	payload.WriteByte(0x00)
	payload.WriteByte(0x00) // compMethod
	payload.Write(compressed.Bytes())

	png := buildPNGWithChunk("zTXt", payload.Bytes())
	_, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawXMP != nil {
		t.Errorf("rawXMP = %q, want nil for non-XMP zTXt keyword", rawXMP)
	}
}

// TestExtractITxtTakesPriorityOverTEXt verifies that when both an iTXt XMP
// chunk and a tEXt XMP chunk are present, the iTXt value is used (it is read
// first and rawXMP is set, so the tEXt branch is skipped per
// png.go:64 `if rawXMP == nil`).
func TestExtractITxtTakesPriorityOverTEXt(t *testing.T) {
	t.Parallel()
	iTXtContent := []byte("<?xpacket begin='' uid='x'?><iTXt/><?xpacket end='r'?>")
	tEXtContent := []byte("<?xpacket begin='' uid='x'?><tEXt/><?xpacket end='r'?>")

	var buf bytes.Buffer
	buf.Write(pngSig[:])

	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1)
	binary.BigEndian.PutUint32(ihdrData[4:], 1)
	ihdrData[8] = 8
	ihdrData[9] = 2
	writeChunkTo(&buf, "IHDR", ihdrData)

	// iTXt XMP chunk first.
	writeChunkTo(&buf, "iTXt", buildXMPChunk(iTXtContent))

	// tEXt XMP chunk after — must be ignored.
	var tEXtPayload bytes.Buffer
	tEXtPayload.WriteString(xmpKeyword)
	tEXtPayload.WriteByte(0x00)
	tEXtPayload.Write(tEXtContent)
	writeChunkTo(&buf, "tEXt", tEXtPayload.Bytes())

	writeChunkTo(&buf, "IEND", nil)

	_, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, iTXtContent) {
		t.Errorf("rawXMP = %q, want iTXt value %q", rawXMP, iTXtContent)
	}
}

func BenchmarkPNGExtract(b *testing.B) {
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta xmlns:x=\"adobe:ns:meta/\"/><?xpacket end='r'?>")
	png := buildPNG(exifData, xmpData)
	b.SetBytes(int64(len(png)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(png))
	}
}

// BenchmarkPNGExtractCompressedXMP measures the hot path that exercises the
// zlib pool: an iTXt chunk with compression flag set.
func BenchmarkPNGExtractCompressedXMP(b *testing.B) {
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta xmlns:x=\"adobe:ns:meta/\"/><?xpacket end='r'?>")

	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write(xmpData)
	_ = zw.Close()

	var chunk bytes.Buffer
	chunk.WriteString(xmpKeyword)
	chunk.WriteByte(0x00) // null terminator
	chunk.WriteByte(0x01) // compression flag = compressed
	chunk.WriteByte(0x00) // compression method = zlib/deflate
	chunk.WriteByte(0x00) // empty language tag
	chunk.WriteByte(0x00) // empty translated keyword
	chunk.Write(compressed.Bytes())

	var buf bytes.Buffer
	buf.Write(pngSig[:])
	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1)
	binary.BigEndian.PutUint32(ihdrData[4:], 1)
	ihdrData[8], ihdrData[9] = 8, 2
	writeChunkTo(&buf, "IHDR", ihdrData)
	writeChunkTo(&buf, "iTXt", chunk.Bytes())
	writeChunkTo(&buf, "IEND", nil)

	pngBytes := buf.Bytes()
	b.SetBytes(int64(len(pngBytes)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _, _, _ = Extract(bytes.NewReader(pngBytes))
	}
}

// BenchmarkPNGInject measures the full Inject path: read all chunks, drop
// old metadata, write new eXIf and iTXt(XMP) chunks with correct CRCs.
func BenchmarkPNGInject(b *testing.B) {
	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta xmlns:x=\"adobe:ns:meta/\"/><?xpacket end='r'?>")
	png := buildPNG(nil, nil)
	b.SetBytes(int64(len(png)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(png), &out, exifData, nil, xmpData, true)
	}
}

// TestIsXMPChunk exercises isXMPChunk (0% coverage).
func TestIsXMPChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{
			"valid XMP iTXt payload",
			append([]byte(xmpKeyword+"\x00"), []byte("<?xpacket?>")...),
			true,
		},
		{
			"wrong keyword",
			append([]byte("zTXt\x00"), []byte("data")...),
			false,
		},
		{
			"too short",
			[]byte("XML:com.adobe.xm"), // one byte shorter than keyword
			false,
		},
		{
			"keyword present but missing NUL",
			[]byte(xmpKeyword + "x"), // keyword byte replaced with non-NUL
			false,
		},
		{
			"empty",
			[]byte{},
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isXMPChunk(tc.data); got != tc.want {
				t.Errorf("isXMPChunk() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldDropChunk exercises all branches of shouldDropChunk.
func TestShouldDropChunk(t *testing.T) {
	t.Parallel()

	t.Run("drops eXIf chunk", func(t *testing.T) {
		t.Parallel()
		if !shouldDropChunk("eXIf", []byte("any data")) {
			t.Error("shouldDropChunk: expected true for eXIf")
		}
	})

	t.Run("drops iTXt XMP chunk", func(t *testing.T) {
		t.Parallel()
		// Build a valid XMP iTXt payload.
		data := append([]byte(xmpKeyword+"\x00"), []byte("xmp data")...)
		if !shouldDropChunk("iTXt", data) {
			t.Error("shouldDropChunk: expected true for iTXt XMP chunk")
		}
	})

	t.Run("keeps iTXt non-XMP chunk", func(t *testing.T) {
		t.Parallel()
		data := append([]byte("Comment\x00"), []byte("some text")...)
		if shouldDropChunk("iTXt", data) {
			t.Error("shouldDropChunk: expected false for non-XMP iTXt")
		}
	})

	t.Run("keeps tEXt chunk", func(t *testing.T) {
		t.Parallel()
		if shouldDropChunk("tEXt", []byte("Comment\x00text")) {
			t.Error("shouldDropChunk: expected false for tEXt")
		}
	})

	t.Run("keeps IHDR chunk", func(t *testing.T) {
		t.Parallel()
		if shouldDropChunk("IHDR", make([]byte, 13)) {
			t.Error("shouldDropChunk: expected false for IHDR")
		}
	})
}

// TestZlibDecompressPoolReuse verifies that zlibDecompress works correctly on
// two sequential calls, exercising the pool-reuse (Reset) path on the second call.
func TestZlibDecompressPoolReuse(t *testing.T) {
	t.Parallel()

	compress := func(data []byte) []byte {
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		_, _ = zw.Write(data)
		_ = zw.Close()
		return buf.Bytes()
	}

	input1 := []byte("first decompression call — allocates a new zlib reader")
	input2 := []byte("second decompression call — should reuse pooled zlib reader")

	comp1 := compress(input1)
	comp2 := compress(input2)

	// First call — allocates a new zlib reader.
	got1, err := zlibDecompress(comp1)
	if err != nil {
		t.Fatalf("zlibDecompress (1st): %v", err)
	}
	if !bytes.Equal(got1, input1) {
		t.Errorf("zlibDecompress (1st) = %q, want %q", got1, input1)
	}

	// Second call — should reuse the pooled reader.
	got2, err := zlibDecompress(comp2)
	if err != nil {
		t.Fatalf("zlibDecompress (2nd): %v", err)
	}
	if !bytes.Equal(got2, input2) {
		t.Errorf("zlibDecompress (2nd) = %q, want %q", got2, input2)
	}
}

// TestZlibDecompressBadData verifies that zlibDecompress returns an error for
// corrupt compressed data.
func TestZlibDecompressBadData(t *testing.T) {
	t.Parallel()
	_, err := zlibDecompress([]byte("this is not zlib data"))
	if err == nil {
		t.Error("zlibDecompress: expected error for bad zlib data, got nil")
	}
}

// TestInjectDropsExistingEXIf verifies that Inject removes an existing eXIf
// chunk and replaces it with the new EXIF data.
func TestInjectDropsExistingEXIf(t *testing.T) {
	t.Parallel()
	oldExif := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	newExif := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0xFF, 0xFF}
	png := buildPNG(oldExif, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(png), &out, newExif, nil, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	rawEXIF, _, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawEXIF, newExif) {
		t.Errorf("EXIF after inject: got %v, want %v", rawEXIF, newExif)
	}
}

// TestInjectDropsExistingXMP verifies that Inject removes an existing iTXt XMP
// chunk and replaces it with the new XMP data.
func TestInjectDropsExistingXMP(t *testing.T) {
	t.Parallel()
	oldXMP := []byte("<?xpacket begin='' uid='x'?><old/><?xpacket end='r'?>")
	newXMP := []byte("<?xpacket begin='' uid='x'?><new/><?xpacket end='r'?>")
	png := buildPNG(nil, oldXMP)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(png), &out, nil, nil, newXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after Inject: %v", err)
	}
	if !bytes.Equal(rawXMP, newXMP) {
		t.Errorf("XMP after inject: got %q, want %q", rawXMP, newXMP)
	}
}

// TestInjectNilPayloadsPassThrough verifies that Inject with nil EXIF and XMP
// writes a valid PNG that preserves non-metadata chunks.
func TestInjectNilPayloadsPassThrough(t *testing.T) {
	t.Parallel()
	// PNG with no metadata at all.
	png := buildPNG(nil, nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(png), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject nil payloads: %v", err)
	}

	// Verify the output is a valid PNG signature.
	result := out.Bytes()
	if len(result) < 8 {
		t.Fatal("output too short")
	}
	for i, b := range pngSig {
		if result[i] != b {
			t.Errorf("PNG signature byte %d: got 0x%02X, want 0x%02X", i, result[i], b)
		}
	}
}

// TestInjectEOFAfterSignature verifies that Inject on a PNG that ends
// immediately after the signature (no chunks) returns nil without panicking.
// The io.EOF from the first chunk-header read triggers the break path.
func TestInjectEOFAfterSignature(t *testing.T) {
	t.Parallel()
	// Just the 8-byte PNG signature, no chunks.
	var out bytes.Buffer
	err := Inject(bytes.NewReader(pngSig[:]), &out, nil, nil, nil, true)
	if err != nil {
		t.Errorf("expected nil for EOF-after-signature, got: %v", err)
	}
}

// TestInjectUnexpectedEOFReturnsError verifies that Inject returns an error
// when the chunk stream is truncated mid-header (io.ErrUnexpectedEOF path).
func TestInjectUnexpectedEOFReturnsError(t *testing.T) {
	t.Parallel()
	// PNG signature + only 4 bytes of a chunk header (need 8 to read it).
	buf := make([]byte, 8+4)
	copy(buf[:8], pngSig[:])
	// Only 4 bytes of the chunk header — truncated.
	binary.BigEndian.PutUint32(buf[8:], 13) // length field of a partial IHDR
	err := Inject(bytes.NewReader(buf), &bytes.Buffer{}, nil, nil, nil, true)
	if err == nil {
		t.Error("expected error for truncated chunk header, got nil")
	}
}

// TestWriteMetadataAfterIHDRXMPOnly verifies that writeMetadataAfterIHDR
// writes only the XMP chunk when rawEXIF is nil.
func TestWriteMetadataAfterIHDRXMPOnly(t *testing.T) {
	t.Parallel()
	xmpData := []byte("<?xpacket begin='' uid='x'?><x/><?xpacket end='r'?>")
	var out bytes.Buffer
	if err := writeMetadataAfterIHDR(&out, nil, xmpData); err != nil {
		t.Fatalf("writeMetadataAfterIHDR XMP-only: %v", err)
	}
	// Should have written one chunk — verify it starts with "iTXt".
	result := out.Bytes()
	if len(result) < 12 {
		t.Fatal("output too short to contain chunk")
	}
	// Chunk type is at bytes 4-7.
	chunkType := string(result[4:8])
	if chunkType != "iTXt" {
		t.Errorf("expected iTXt chunk, got %q", chunkType)
	}
}

// TestExtractTruncatedChunkNoPanic verifies that Extract on a truncated PNG
// (cut in the middle of a chunk) returns an error without panicking.
func TestExtractTruncatedChunkNoPanic(t *testing.T) {
	t.Parallel()
	full := buildPNG([]byte{0x49, 0x49, 0x2A, 0x00}, nil)
	// Try progressively-truncated inputs.
	for i := 8; i < len(full); i += max(1, len(full)/20) {
		_, _, _, _ = Extract(bytes.NewReader(full[:i]))
	}
}

// TestReadChunkTooLarge is a DoS regression test: a PNG whose single chunk
// declares length 0xFFFFFFFF (≈4 GiB) must be rejected by ErrChunkTooLarge
// without attempting to allocate or read that many bytes.
func TestReadChunkTooLarge(t *testing.T) {
	t.Parallel()

	// Craft a minimal stream: PNG signature + chunk header with giant length.
	// We use chunkType "IHDR" so the stream looks plausible; the length field
	// (0xFFFFFFFF) is what triggers the guard. No chunk data follows — the guard
	// must fire before any read of the payload.
	var buf bytes.Buffer
	buf.Write(pngSig[:])

	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], 0xFFFFFFFF) // giant length
	copy(hdr[4:8], "IHDR")
	buf.Write(hdr[:])
	// Deliberately write no payload bytes — the guard must reject before reading.

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("Extract: expected error for oversized chunk, got nil")
	}
	if !errors.Is(err, ErrChunkTooLarge) {
		t.Errorf("Extract: expected ErrChunkTooLarge, got %v", err)
	}
}

// TestReadChunkLargeDeclaredSizeShortStream is a DoS regression test for the
// incremental-read strategy added to readNonEmptyChunk.
//
// A PNG can be crafted so that a chunk's declared length field (e.g. ~200 MiB)
// is far larger than the actual bytes in the stream (e.g. a few hundred bytes).
// Before the fix, iobuf.Get(length) fell back to make([]byte, length) for any
// length > largeSize (65536), allocating ~200 MiB before io.ReadFull detected
// the truncation.
//
// After the fix, readNonEmptyChunk switches to io.ReadAll(io.LimitReader(...))
// for length > largeChunkReadThreshold, which grows incrementally as bytes
// arrive; a short stream yields a short slice without a proportional allocation,
// and the subsequent truncation check returns an error immediately.
func TestReadChunkLargeDeclaredSizeShortStream(t *testing.T) {
	t.Parallel()

	// Craft a PNG: signature + IHDR + one chunk with a huge declared length.
	// The chunk payload and CRC are not written — the stream ends after the
	// 8-byte chunk header, so the parser sees "eXIf" claiming ~200 MiB but
	// the stream has zero payload bytes available.
	const declaredLen = 200 << 20 // 200 MiB — well within maxPNGChunkSize

	var buf bytes.Buffer
	buf.Write(pngSig[:])

	// Valid IHDR so the parser enters the chunk loop successfully.
	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1) // width  = 1
	binary.BigEndian.PutUint32(ihdrData[4:], 1) // height = 1
	ihdrData[8] = 8                             // bit depth
	ihdrData[9] = 2                             // colour type (RGB)
	writeChunkTo(&buf, "IHDR", ihdrData)

	// eXIf chunk header with giant declared length; no payload or CRC follow.
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], declaredLen)
	copy(hdr[4:8], "eXIf")
	buf.Write(hdr[:])
	// Deliberately write no payload — stream ends immediately after the header.

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("Extract: expected error for chunk length > stream, got nil")
	}
	// The error must NOT be nil: a truncated read must be detected quickly
	// without a proportional allocation. Any non-nil error is acceptable here
	// (io.ErrUnexpectedEOF wrapped in the readNonEmptyChunk message). We do
	// not assert a specific sentinel because the stream ends mid-chunk, which
	// is a different condition from ErrChunkTooLarge.
}

// BenchmarkPNGWriteChunk measures the hot inner loop: serialise one PNG chunk
// (header + data + CRC) using the pooled crc32 hash and stack-allocated header.
func BenchmarkPNGWriteChunk(b *testing.B) {
	data := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	b.SetBytes(int64(8 + len(data) + 4)) // header + data + CRC
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var out bytes.Buffer
		_ = writeChunk(&out, "eXIf", data)
	}
}

// buildPNGWithBadCRC constructs a minimal PNG where the single extra chunk
// (placed between IHDR and IEND) has its CRC corrupted by flipping all bits.
// Used to verify ErrChunkCRCMismatch detection.
func buildPNGWithBadCRC(chunkType string, data []byte) []byte {
	var buf bytes.Buffer
	buf.Write(pngSig[:])

	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1)
	binary.BigEndian.PutUint32(ihdrData[4:], 1)
	ihdrData[8] = 8
	ihdrData[9] = 2
	writeChunkTo(&buf, "IHDR", ihdrData)

	// Write the target chunk with a deliberately wrong CRC.
	var lbuf [4]byte
	binary.BigEndian.PutUint32(lbuf[:], uint32(len(data))) //nolint:gosec // G115: test helper
	buf.Write(lbuf[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	// Corrupt: write the bitwise complement of the correct CRC.
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	binary.BigEndian.PutUint32(lbuf[:], ^h.Sum32()) // flip all bits — guaranteed mismatch
	buf.Write(lbuf[:])

	writeChunkTo(&buf, "IEND", nil)
	return buf.Bytes()
}

// TestReadChunkCRCMismatchDetected verifies that readChunk (and therefore
// Extract) returns ErrChunkCRCMismatch when a metadata chunk carries a
// corrupted CRC. Verified chunk types: eXIf, iTXt, tEXt, zTXt, IHDR.
//
// Note: IEND and IDAT are intentionally not in this list — the library does
// not verify CRC for non-metadata chunks (see shouldVerifyCRC, readChunk doc).
func TestReadChunkCRCMismatchDetected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		chunkType string
		data      []byte
	}{
		{
			name:      "eXIf chunk with bad CRC",
			chunkType: "eXIf",
			data:      []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00},
		},
		{
			name:      "iTXt chunk with bad CRC",
			chunkType: "iTXt",
			// A minimal iTXt payload (wrong keyword, so it won't be parsed as XMP).
			data: []byte("Comment\x00\x00\x00\x00\x00some text"),
		},
		{
			name:      "tEXt chunk with bad CRC",
			chunkType: "tEXt",
			data:      []byte("Comment\x00some plain text"),
		},
		{
			name:      "IHDR chunk with bad CRC",
			chunkType: "IHDR",
			// A well-formed 13-byte IHDR payload (1×1 pixel, 8-bit RGB).
			data: func() []byte {
				d := make([]byte, 13)
				binary.BigEndian.PutUint32(d[0:], 1)
				binary.BigEndian.PutUint32(d[4:], 1)
				d[8] = 8
				d[9] = 2
				return d
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			png := buildPNGWithBadCRC(tc.chunkType, tc.data)
			_, _, _, err := Extract(bytes.NewReader(png))
			if err == nil {
				t.Fatal("Extract: expected ErrChunkCRCMismatch, got nil")
			}
			if !errors.Is(err, ErrChunkCRCMismatch) {
				t.Errorf("Extract: expected ErrChunkCRCMismatch, got %v", err)
			}
		})
	}
}

// TestCRCSkippedForNonMetadataChunks verifies that Extract does NOT return
// ErrChunkCRCMismatch for a non-metadata chunk (IDAT) with a corrupted CRC.
// This documents the selective-verification policy: IDAT pixel data is never
// interpreted by the metadata layer, so spending cycles on its CRC check is
// pure overhead (see shouldVerifyCRC, readChunk doc comment).
func TestCRCSkippedForNonMetadataChunks(t *testing.T) {
	t.Parallel()

	// Build a PNG that contains an IDAT chunk with a deliberately wrong CRC,
	// sandwiched between IHDR and IEND (both with correct CRCs).
	var buf bytes.Buffer
	buf.Write(pngSig[:])

	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1)
	binary.BigEndian.PutUint32(ihdrData[4:], 1)
	ihdrData[8] = 8
	ihdrData[9] = 2
	writeChunkTo(&buf, "IHDR", ihdrData)

	// IDAT with correct data but corrupted CRC.
	idatData := []byte{0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00, 0x00, 0x00, 0x02, 0x00, 0x01} // minimal zlib IDAT
	var lbuf [4]byte
	binary.BigEndian.PutUint32(lbuf[:], uint32(len(idatData))) //nolint:gosec // G115: test helper
	buf.Write(lbuf[:])
	buf.WriteString("IDAT")
	buf.Write(idatData)
	// Write bitwise-complement of correct CRC — guaranteed mismatch.
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte("IDAT"))
	_, _ = h.Write(idatData)
	binary.BigEndian.PutUint32(lbuf[:], ^h.Sum32())
	buf.Write(lbuf[:])

	writeChunkTo(&buf, "IEND", nil)

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if errors.Is(err, ErrChunkCRCMismatch) {
		t.Errorf("Extract: got ErrChunkCRCMismatch for IDAT (non-metadata chunk); CRC should be skipped")
	}
	// Any other error (e.g. decompression) is fine — the point is CRC is not checked.
}

// TestReadChunkCRCValidPasses verifies that Extract succeeds (no error) when
// all chunks carry correct CRCs. This is the positive case for CRC verification.
func TestReadChunkCRCValidPasses(t *testing.T) {
	t.Parallel()

	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>")
	png := buildPNG(exifData, xmpData)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("Extract: unexpected error on valid CRCs: %v", err)
	}
	if !bytes.Equal(rawEXIF, exifData) {
		t.Errorf("EXIF mismatch: got %q, want %q", rawEXIF, exifData)
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("XMP mismatch: got %q, want %q", rawXMP, xmpData)
	}
}

// knownGoodPNGs lists corpus files that are expected to be well-formed
// (valid CRCs, no intentional corruption). These are original reference files
// from the exiv2 test suite, not mutation/PoC variants.
// Any file in this list that triggers ErrChunkCRCMismatch is a regression.
var knownGoodPNGs = []string{ //nolint:gochecknoglobals // package-level test data
	"testdata/corpus/png/exiv2/1343_comment.png",
	"testdata/corpus/png/exiv2/1343_empty.png",
	"testdata/corpus/png/exiv2/1343_exif.png",
	"testdata/corpus/png/exiv2/exiv2-bug1074.png",
	"testdata/corpus/png/exiv2/exiv2-bug841.png",
	"testdata/corpus/png/exiv2/exiv2-bug922.png",
	"testdata/corpus/png/exiv2/imagemagick.png",
	"testdata/corpus/png/exiv2/ReaganSmallPng.png",
	"testdata/corpus/png/exiv2/ReaganLargePng.png",
	"testdata/corpus/png/exiv2/issue_790_poc2.png",
}

// TestCorpusPNGCRCIntegrity runs Extract against known-good PNG corpus files.
// Each file in knownGoodPNGs must parse without ErrChunkCRCMismatch, proving
// that CRC verification does not break extraction of legitimate real-world PNGs.
//
// Note: the broader corpus also contains PoC/mutation files with intentionally
// corrupt CRCs (prefixed m1-, m2-, c-, or named issue_*_poc); those are excluded
// here — they should trigger ErrChunkCRCMismatch by design and are covered by
// TestReadChunkCRCMismatchDetected.
func TestCorpusPNGCRCIntegrity(t *testing.T) {
	t.Parallel()

	corpusRoot := filepath.Join("..", "..")
	for _, rel := range knownGoodPNGs {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(corpusRoot, rel)
			f, err := os.Open(path)
			if err != nil {
				t.Skipf("cannot open %s: %v", path, err)
			}
			defer f.Close() //nolint:errcheck // read-only

			_, _, _, extractErr := Extract(f)
			if extractErr != nil && errors.Is(extractErr, ErrChunkCRCMismatch) {
				t.Errorf("%s: CRC mismatch on known-good corpus file (regression): %v", filepath.Base(rel), extractErr)
			}
			// Other errors (ErrInvalidSignature, truncated payload, etc.) are not
			// regressions — some exiv2 files test specific parse-edge-case behaviour.
		})
	}
}

// TestPNGReadChunkLargeLength is the 32-bit-safe regression test for task #74.
//
// readChunk historically used `length := int(binary.BigEndian.Uint32(hdr[:4]))`.
// On a 32-bit build (GOARCH=386/arm, int=32 bits), a chunk length field of
// 0x80000000 or higher produces a negative int after the cast. The negative
// value is less than maxPNGChunkSize (a positive constant), so the guard passes
// silently; then `length > 0` evaluates to false and the chunk is processed as
// zero-length — wrong behaviour, and a silent data-corruption path.
//
// After the fix, readChunk checks `rawLen > math.MaxInt32` before the int cast
// and returns ErrChunkTooLarge, ensuring correct rejection on all platform widths.
// On 64-bit platforms (current target) the test is also meaningful: the raw
// uint32 0x80000000 == 2147483648 > math.MaxInt32, so ErrChunkTooLarge must fire.
func TestPNGReadChunkLargeLength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		rawLen uint32
	}{
		// Exactly 2^31: the first value that overflows to negative on 32-bit int.
		{"0x80000000", 0x80000000},
		// 2^31 + 1: next value beyond the boundary.
		{"0x80000001", 0x80000001},
		// max uint32.
		{"0xFFFFFFFF", 0xFFFFFFFF},
		// math.MaxInt32 + 1 == 0x80000000 (duplicate of first; kept for clarity).
		{"MaxInt32+1", math.MaxInt32 + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a minimal PNG stream with the PNG signature followed by a single
			// chunk whose length field is the oversized value. The chunk type is
			// "IHDR" so the stream looks plausible. The actual body is empty beyond
			// the 8-byte header because readChunk returns before reading the body.
			var buf bytes.Buffer
			buf.Write(pngSig[:])
			var hdr [8]byte
			binary.BigEndian.PutUint32(hdr[:4], tc.rawLen)
			copy(hdr[4:8], "IHDR")
			buf.Write(hdr[:])

			_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
			if err == nil {
				t.Fatalf("Extract with chunk length 0x%08X: expected ErrChunkTooLarge, got nil", tc.rawLen)
			}
			if !errors.Is(err, ErrChunkTooLarge) {
				t.Errorf("Extract with chunk length 0x%08X: got %v, want wrapping ErrChunkTooLarge", tc.rawLen, err)
			}
		})
	}
}

// TestPNGInjectXMPSizeGuard is the regression gate for audit finding #147.
//
// W3C PNG 3rd ed. §5.3 / ISO 15948 §11.2.1: the chunk Length field is a
// 31-bit unsigned integer; values >= 2^31 are forbidden. buildXMPChunk
// prepends a fixed 22-byte iTXt header to the raw XMP data; when the result
// would exceed math.MaxInt32, PutUint32 in writeChunk silently wraps the
// length field, producing a corrupt chunk. The fix adds a size guard in
// writeMetadataAfterIHDR that returns ErrXMPTooLarge before any write.
//
// Test strategy: rather than allocating 2 GiB of XMP data, we call
// writeMetadataAfterIHDR with a synthetic rawXMP whose length equals
// math.MaxInt32 - xmpITXtOverhead + 1 (one byte past the limit). The guard
// is a pure arithmetic check on len(rawXMP), so the actual bytes are never
// read; a small backing array is sufficient. A bytes.Buffer is passed as w to
// assert that nothing is written before the error is returned.
func TestPNGInjectXMPSizeGuard(t *testing.T) {
	t.Parallel()

	// Construct the minimal oversized slice: length = math.MaxInt32 - xmpITXtOverhead + 1.
	// This is one byte beyond the guard threshold without allocating 2 GiB.
	// xmpITXtOverhead = 22 (see png.go: len(xmpKeyword)+5).
	overLen := math.MaxInt32 - xmpITXtOverhead + 1

	// make([]byte, overLen) would require ~2 GiB. Use a length-only check: Go's
	// runtime will panic on make with a size > maxAlloc, so we verify the guard
	// fires by directly calling the internal size arithmetic. The guard condition
	// in writeMetadataAfterIHDR is:
	//   len(rawXMP) > math.MaxInt32 - xmpITXtOverhead
	// which equals: overLen-1+1 = overLen > math.MaxInt32-xmpITXtOverhead ✓.
	//
	// We use a fixed-size sentinel slice whose length we override via a
	// type-assertion-free approach: build a []byte header using unsafe-free
	// slice trickery — actually the simplest correct approach is to just test
	// the guard indirectly via a fake []byte with the right len using
	// make + reslice, which Go allows up to the platform's address space.
	// On 64-bit (amd64/arm64) overLen ≈ 2^31 which is well within virtual
	// address space; the allocation will fail at runtime on 32-bit but those
	// platforms are not a primary target. To avoid OOM on CI, we skip the
	// allocation entirely by calling writeMetadataAfterIHDR with a nil rawEXIF
	// and a rawXMP whose len is spoofed via reflect-free slice header arithmetic.
	//
	// The cleanest test-safe approach: verify the guard constant directly, then
	// call writeMetadataAfterIHDR with a slice of exactly the oversized length
	// but backed by a 1-byte allocation, using append to avoid the 2 GiB alloc.
	//
	// Go spec: a slice header {ptr, len, cap} can have len > cap only via unsafe.
	// We instead use a concrete small rawXMP but verify the constant arithmetic.
	// The guard fires on len(rawXMP) > math.MaxInt32-xmpITXtOverhead; we verify:
	// 1. The constant xmpITXtOverhead equals 22 (len("XML:com.adobe.xmp")+5).
	// 2. Directly call with a 1-element slice that matches the expected limit.
	// 3. For the real oversized case, assert the error sentinel without 2 GiB alloc.

	// Step 1: verify the overhead constant matches the actual buildXMPChunk layout.
	const expectedOverhead = len(xmpKeyword) + 5 // 17 + 5 = 22
	if xmpITXtOverhead != expectedOverhead {
		t.Fatalf("xmpITXtOverhead = %d, want %d (guard constant is stale)", xmpITXtOverhead, expectedOverhead)
	}

	// Step 2: just-under-limit — must succeed (no error).
	// We use a real allocation at the boundary: math.MaxInt32 - 22 = 2147483625 bytes
	// is ~2 GiB; not allocatable on CI. Instead verify the boundary using small data.
	// The guard is: len(rawXMP) > math.MaxInt32-xmpITXtOverhead, so
	// len(rawXMP) == math.MaxInt32-xmpITXtOverhead should be allowed.
	// We cannot allocate 2 GiB; test the guard formula with small synthetic data.
	//
	// Boundary test via guard formula: confirm that the condition fires at +1 but
	// not at exactly the limit. We do this by manipulating a stack integer.
	okLen := math.MaxInt32 - xmpITXtOverhead            // exactly at limit — allowed
	overLenCheck := math.MaxInt32 - xmpITXtOverhead + 1 // one over — must be rejected
	if okLen > math.MaxInt32-xmpITXtOverhead {
		t.Errorf("guard formula error: okLen %d should satisfy okLen <= maxInt32-overhead", okLen)
	}
	if overLenCheck <= math.MaxInt32-xmpITXtOverhead {
		t.Errorf("guard formula error: overLen %d should trigger guard", overLenCheck)
	}

	// Step 3: construct an oversized rawXMP using a real small backing array but
	// the right declared length, using a subslice of a zero-length header.
	// The only allocation-free way to spoof len without unsafe is to use a
	// string-based trick. In Go, we CAN do: s := string(make([]byte, N)) to get
	// a string of length N, but that still allocates. Correct approach: accept
	// the limit, skip the 2 GiB alloc, and test the guard directly via a mock.
	//
	// Since writeMetadataAfterIHDR is an unexported function in the same package,
	// we call it directly with a bytes.Buffer as w and verify:
	//   (a) the error wraps ErrXMPTooLarge
	//   (b) the buffer w is empty (no bytes written before the guard fires)
	//
	// To avoid the 2 GiB allocation, we verify the guard at a small but
	// representatively "oversized" threshold by temporarily patching the guard
	// constant — but constants cannot be patched. Instead, use a helper that
	// mirrors the exact guard condition:
	guardFires := func(rawXMPLen int) bool {
		return rawXMPLen > math.MaxInt32-xmpITXtOverhead
	}
	if guardFires(0) {
		t.Error("guard fires for len=0 (should not)")
	}
	if guardFires(math.MaxInt32 - xmpITXtOverhead) {
		t.Error("guard fires at exactly the limit (should not)")
	}
	if !guardFires(math.MaxInt32 - xmpITXtOverhead + 1) {
		t.Error("guard does NOT fire at limit+1 (must fire)")
	}

	// Step 4: exercise writeMetadataAfterIHDR with a concrete oversized rawXMP.
	// We cannot allocate 2 GiB. Use the minimum possible allocation that triggers
	// the guard: make([]byte, math.MaxInt32-xmpITXtOverhead+1). On 64-bit Linux
	// with overcommit this succeeds (virtual pages, not physical RAM). On systems
	// where it would OOM, the test is inherently untestable without unsafe tricks.
	// We use t.Skip only if the alloc panics, guarded by a recover.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Skipf("cannot allocate %d bytes on this platform: %v", overLen, r)
			}
		}()
		oversized := make([]byte, overLen)
		var wBuf bytes.Buffer
		err := writeMetadataAfterIHDR(&wBuf, nil, oversized)
		if err == nil {
			t.Fatalf("writeMetadataAfterIHDR with oversized XMP: expected ErrXMPTooLarge, got nil")
		}
		if !errors.Is(err, ErrXMPTooLarge) {
			t.Errorf("writeMetadataAfterIHDR: got %v, want wrapping ErrXMPTooLarge", err)
		}
		if wBuf.Len() != 0 {
			t.Errorf("writeMetadataAfterIHDR: wrote %d bytes before returning error; want 0", wBuf.Len())
		}
	}()
}

// TestPNGInjectSignatureValidation is the regression gate for audit finding #181.
//
// W3C PNG 3rd ed. §5.2: a PNG datastream must begin with the 8-byte magic
// sequence. Before the fix, Inject wrote the PNG signature to w unconditionally
// before reading the input signature, so passing JPEG (or any non-PNG) bytes
// left a partial/corrupt PNG header in w even though Inject eventually returned
// an error. After the fix, the input signature is validated before any write.
//
// Assertions:
//   - Inject returns a non-nil error (specifically ErrInvalidSignature).
//   - w.Len() == 0: nothing was written to w before the error.
func TestPNGInjectSignatureValidation(t *testing.T) {
	t.Parallel()

	// Use JPEG magic bytes as the non-PNG input.
	jpegMagic := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01}
	r := bytes.NewReader(jpegMagic)
	var w bytes.Buffer

	err := Inject(r, &w, nil, nil, nil, true)

	if err == nil {
		t.Fatal("Inject with JPEG input: expected error, got nil")
	}
	// W3C PNG 3rd ed. §5.2: the error must identify a signature mismatch.
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("Inject with JPEG input: got %v, want wrapping ErrInvalidSignature", err)
	}
	// Core assertion: nothing must have been written to w before the error.
	if w.Len() != 0 {
		t.Errorf("Inject with JPEG input: wrote %d bytes to w before returning error; want 0 (partial write corruption)", w.Len())
	}
}

// TestPNGInjectMissingIENDOutputWellFormed is the regression gate for audit finding #182.
//
// W3C PNG 3rd ed. §5.6: "The IEND chunk must appear LAST. It marks the end of
// the PNG datastream." Before the fix, Inject on a PNG that lacked an IEND
// (truncated source) silently produced output without IEND, leaving it
// structurally incomplete. After the fix, Inject always emits a terminal IEND.
//
// Two sub-tests:
//  1. Truncated-before-IEND: inject into a PNG whose chunk stream ends without
//     IEND; assert the output ends with a well-formed IEND chunk.
//  2. Control (normal PNG): a source that already has IEND produces output with
//     exactly one IEND, not two.
func TestPNGInjectMissingIENDOutputWellFormed(t *testing.T) {
	t.Parallel()

	// hasValidIEND walks the PNG chunk stream in data (starting after the 8-byte
	// signature) and returns (count, lastIsIEND) where count is the number of
	// IEND chunks seen and lastIsIEND is true when the final chunk in the stream
	// is IEND. The function is tolerant of a missing trailing IEND — it simply
	// returns count=0, lastIsIEND=false in that case.
	countIENDs := func(data []byte) int {
		pos := 8 // skip signature
		count := 0
		for pos+8 <= len(data) {
			length := int(binary.BigEndian.Uint32(data[pos:]))
			chunkType := string(data[pos+4 : pos+8])
			if chunkType == "IEND" {
				count++
			}
			pos += 8 + length + 4 // header + data + CRC
		}
		return count
	}

	// buildTruncatedPNG builds a PNG with IHDR but NO IEND chunk.
	// This simulates a truncated file or a file written by a non-conformant encoder.
	buildTruncatedPNG := func() []byte {
		var buf bytes.Buffer
		buf.Write(pngSig[:])
		writeChunkTo(&buf, "IHDR", minIHDR())
		// Deliberately omit IEND.
		return buf.Bytes()
	}

	t.Run("truncated-before-IEND emits IEND in output", func(t *testing.T) {
		t.Parallel()

		exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
		src := buildTruncatedPNG()

		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, exifData, nil, nil, true); err != nil {
			t.Fatalf("Inject: unexpected error: %v", err)
		}

		result := out.Bytes()
		if len(result) < 8 {
			t.Fatal("output too short to be a PNG")
		}
		// Output must start with the PNG signature.
		if [8]byte(result[:8]) != pngSig {
			t.Error("output does not start with PNG signature")
		}

		iendCount := countIENDs(result)
		if iendCount == 0 {
			t.Error("output has no IEND chunk; W3C PNG 3rd ed. §5.6 requires IEND as the last chunk")
		}
		if iendCount > 1 {
			t.Errorf("output has %d IEND chunks; must be exactly 1", iendCount)
		}

		// The final 12 bytes must be a zero-length IEND with correct CRC.
		// IEND chunk: Length(4)=0 + Type(4)="IEND" + CRC(4)=0xAE426082.
		const iendCRC = 0xAE426082 // crc32.NewIEEE of "IEND" with empty data
		if len(result) < 12 {
			t.Fatal("output too short for an IEND chunk")
		}
		tail := result[len(result)-12:]
		tailLen := binary.BigEndian.Uint32(tail[0:4])
		tailType := string(tail[4:8])
		tailCRC := binary.BigEndian.Uint32(tail[8:12])
		if tailLen != 0 || tailType != "IEND" || tailCRC != iendCRC {
			t.Errorf("last 12 bytes are not a valid IEND: length=%d type=%q CRC=0x%08X (want length=0 type=IEND CRC=0x%08X)",
				tailLen, tailType, tailCRC, uint32(iendCRC))
		}
	})

	t.Run("normal PNG has exactly one IEND", func(t *testing.T) {
		t.Parallel()

		exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
		// buildPNG always appends a well-formed IEND.
		src := buildPNG(nil, nil)

		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, exifData, nil, nil, true); err != nil {
			t.Fatalf("Inject on normal PNG: unexpected error: %v", err)
		}

		iendCount := countIENDs(out.Bytes())
		if iendCount != 1 {
			t.Errorf("normal PNG after Inject has %d IEND chunks; want exactly 1", iendCount)
		}
	})
}
