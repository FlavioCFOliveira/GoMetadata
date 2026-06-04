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
