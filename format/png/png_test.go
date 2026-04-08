package png

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
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
	if err := Inject(bytes.NewReader(png), &out, exifData, nil, nil); err != nil {
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
	if err := Inject(bytes.NewReader(png), &out, exifData, nil, xmpData); err != nil {
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
		_ = Inject(bytes.NewReader(png), &out, exifData, nil, xmpData)
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
	if err := Inject(bytes.NewReader(png), &out, newExif, nil, nil); err != nil {
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
	if err := Inject(bytes.NewReader(png), &out, nil, nil, newXMP); err != nil {
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
	if err := Inject(bytes.NewReader(png), &out, nil, nil, nil); err != nil {
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
	err := Inject(bytes.NewReader(pngSig[:]), &out, nil, nil, nil)
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
	err := Inject(bytes.NewReader(buf), &bytes.Buffer{}, nil, nil, nil)
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
