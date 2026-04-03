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
