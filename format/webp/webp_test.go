package webp

import (
	"bytes"
	"encoding/binary"
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
			vp8xPayload[4] = byte(w)
			vp8xPayload[5] = byte(w >> 8)
			vp8xPayload[6] = byte(w >> 16)
		}
		if canvasH > 0 {
			h := canvasH - 1
			vp8xPayload[7] = byte(h)
			vp8xPayload[8] = byte(h >> 8)
			vp8xPayload[9] = byte(h >> 16)
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
	binary.LittleEndian.PutUint32(riffHdr[4:], uint32(totalSize))
	copy(riffHdr[8:], "WEBP")

	var out bytes.Buffer
	out.Write(riffHdr)
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestExtractEXIF(t *testing.T) {
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

func TestInjectPreservesCanvasDimensions(t *testing.T) {
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
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = Extract(bytes.NewReader(webp))
	}
}
