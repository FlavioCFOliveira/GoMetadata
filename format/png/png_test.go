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
	binary.BigEndian.PutUint32(ihdrData[0:], 1)  // width
	binary.BigEndian.PutUint32(ihdrData[4:], 1)  // height
	ihdrData[8] = 8                               // bit depth
	ihdrData[9] = 2                               // color type (RGB)
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
	binary.BigEndian.PutUint32(lbuf[:], uint32(len(data)))
	buf.Write(lbuf[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	h := crc32.NewIEEE()
	h.Write([]byte(chunkType))
	h.Write(data)
	binary.BigEndian.PutUint32(lbuf[:], h.Sum32())
	buf.Write(lbuf[:])
}

func TestExtractEXIF(t *testing.T) {
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
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>")

	// Build compressed iTXt chunk manually.
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write(xmpData)
	zw.Close()

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
		h.Write([]byte(chunkType))
		h.Write(data)
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
	ihdrData[8] = 8                              // bit depth
	ihdrData[9] = 2                              // color type (RGB)
	writeChunkTo(&buf, "IHDR", ihdrData)

	writeChunkTo(&buf, chunkType, data)
	writeChunkTo(&buf, "IEND", nil)

	return buf.Bytes()
}

// TestExtractXMPFromTEXtChunk verifies that Extract recovers XMP from a legacy
// uncompressed tEXt chunk whose keyword is "XML:com.adobe.xmp".
// This exercises extractXMPFromTExt (png.go:257-271).
func TestExtractXMPFromTEXtChunk(t *testing.T) {
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
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write([]byte("some compressed text"))
	zw.Close()

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
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = Extract(bytes.NewReader(png))
	}
}

// BenchmarkPNGExtractCompressedXMP measures the hot path that exercises the
// zlib pool: an iTXt chunk with compression flag set.
func BenchmarkPNGExtractCompressedXMP(b *testing.B) {
	xmpData := []byte("<?xpacket begin='' uid='x'?><xmpmeta xmlns:x=\"adobe:ns:meta/\"/><?xpacket end='r'?>")

	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	zw.Write(xmpData)
	zw.Close()

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
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = Extract(bytes.NewReader(pngBytes))
	}
}
