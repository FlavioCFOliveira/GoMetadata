package webp

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Task #299: a write into a simple-format WebP (a VP8 or VP8L chunk without
// VP8X) must build the VP8X chunk from the image bitstream. The former code
// wrote a zero canvas (decoded as 1×1) and no ALPHA flag, which libwebp
// rejects with VP8_STATUS_BITSTREAM_ERROR because the canvas does not match
// the image (src/dec/webp_dec.c ParseHeadersInternal).
//
// testdata/simple_*.webp are 40×24 images written by cwebp 1.6 (libwebp)
// from an ffmpeg test pattern: simple_lossy.webp (VP8),
// simple_lossless_opaque.webp (VP8L, alpha_is_used = 0) and
// simple_lossless_alpha.webp (VP8L, alpha_is_used = 1).

// vp8xOf returns the VP8X payload and the first image chunk payload of a
// WebP stream.
func vp8xOf(t *testing.T, data []byte) (vp8x, image []byte) {
	t.Helper()
	for pos := 12; pos+8 <= len(data); {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4:]))
		if pos+8+size > len(data) {
			t.Fatalf("chunk %q at %d overruns the file", id, pos)
		}
		payload := data[pos+8 : pos+8+size]
		switch id {
		case "VP8X":
			vp8x = payload
		case "VP8 ", "VP8L":
			if image == nil {
				image = payload
			}
		}
		pos += 8 + size + size%2
	}
	return vp8x, image
}

func TestTask299SimpleFormatVP8XFromBitstream(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file      string
		wantAlpha bool
	}{
		{"simple_lossy.webp", false},
		{"simple_lossless_opaque.webp", false},
		{"simple_lossless_alpha.webp", true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			if vp8x, _ := vp8xOf(t, src); vp8x != nil {
				t.Fatal("fixture is not a simple-format file")
			}
			_, srcImage := vp8xOf(t, src)
			for _, md := range []struct {
				name      string
				exif, xmp []byte
			}{
				{"exif+xmp", []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"), []byte("<x:xmpmeta/>")},
				{"exif", []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"), nil},
				{"xmp", nil, []byte("<x:xmpmeta/>")},
			} {
				var out bytes.Buffer
				if err := Inject(bytes.NewReader(src), &out, md.exif, nil, md.xmp, true); err != nil {
					t.Fatalf("%s: Inject: %v", md.name, err)
				}
				vp8x, image := vp8xOf(t, out.Bytes())
				if len(vp8x) != vp8xChunkSize {
					t.Fatalf("%s: VP8X payload is %d bytes, want %d", md.name, len(vp8x), vp8xChunkSize)
				}
				if !bytes.Equal(image, srcImage) {
					t.Errorf("%s: image chunk changed", md.name)
				}
				flags := binary.LittleEndian.Uint32(vp8x)
				w := 1 + (int(vp8x[4]) | int(vp8x[5])<<8 | int(vp8x[6])<<16)
				h := 1 + (int(vp8x[7]) | int(vp8x[8])<<8 | int(vp8x[9])<<16)
				if w != 40 || h != 24 {
					t.Errorf("%s: VP8X canvas %dx%d, want 40x24 (the bitstream size)", md.name, w, h)
				}
				if got := flags&vp8xFlagAlpha != 0; got != tc.wantAlpha {
					t.Errorf("%s: ALPHA flag = %v, want %v", md.name, got, tc.wantAlpha)
				}
				if got := flags&vp8xFlagEXIF != 0; got != (md.exif != nil) {
					t.Errorf("%s: EXIF flag = %v", md.name, got)
				}
				if got := flags&vp8xFlagXMP != 0; got != (md.xmp != nil) {
					t.Errorf("%s: XMP flag = %v", md.name, got)
				}
				if flags&^(vp8xFlagAlpha|vp8xFlagEXIF|vp8xFlagXMP) != 0 {
					t.Errorf("%s: unexpected VP8X flags 0x%02x", md.name, flags)
				}
			}
		})
	}
}

// TestTask299ICCPAndALPHChunksSetFlags: an ICCP chunk sets the ICCP flag and
// an ALPH chunk sets the ALPHA flag (libwebp CreateVP8XChunk).
func TestTask299ICCPAndALPHChunksSetFlags(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/simple_lossy.webp")
	if err != nil {
		t.Fatal(err)
	}
	_, image := vp8xOf(t, src)
	chunks := []webpOriginalChunk{
		{id: fourCCICCP, data: []byte("icc")},
		{id: fourCCALPH, data: []byte{0}},
		{id: fourCCVP8, data: image},
	}
	payload, ok := newVP8XPayload(chunks, false, false)
	if !ok {
		t.Fatal("newVP8XPayload failed on a valid VP8 bitstream")
	}
	if flags := binary.LittleEndian.Uint32(payload[:]); flags != vp8xFlagICCP|vp8xFlagAlpha {
		t.Errorf("flags = 0x%02x, want ICCP|ALPHA", flags)
	}
}

// TestTask299BitstreamHeaders checks vp8Info and vp8lInfo against the header
// rules of libwebp VP8GetInfo and VP8LGetInfo.
func TestTask299BitstreamHeaders(t *testing.T) {
	t.Parallel()
	// A VP8 key frame tag: key_frame=0 (bit 0), profile 0, show_frame=1
	// (bit 4), first partition length 2 (bits 5..23), start code, 14-bit
	// width 40 and height 24 with scale 0.
	vp8 := func(tag uint32, sig [3]byte, w, h uint16) []byte {
		b := []byte{byte(tag), byte(tag >> 8), byte(tag >> 16), sig[0], sig[1], sig[2]} //nolint:gosec // G115: deliberate truncation to the three low bytes
		b = binary.LittleEndian.AppendUint16(b, w)
		b = binary.LittleEndian.AppendUint16(b, h)
		return append(b, make([]byte, 16)...)
	}
	start := [3]byte{0x9d, 0x01, 0x2a}
	good := uint32(1<<4 | 2<<5)
	for _, tc := range []struct {
		name   string
		data   []byte
		ok     bool
		wantWH [2]int
	}{
		{"valid", vp8(good, start, 40, 24), true, [2]int{40, 24}},
		{"scale bits ignored", vp8(good, start, 40|0xC000, 24|0x4000), true, [2]int{40, 24}},
		{"inter frame", vp8(good|1, start, 40, 24), false, [2]int{}},
		{"bad profile", vp8(good|4<<1, start, 40, 24), false, [2]int{}},
		{"invisible frame", vp8(2<<5, start, 40, 24), false, [2]int{}},
		{"partition too long", vp8(1<<4|1000<<5, start, 40, 24), false, [2]int{}},
		{"bad start code", vp8(good, [3]byte{0x9d, 0x01, 0x2b}, 40, 24), false, [2]int{}},
		{"zero width", vp8(good, start, 0, 24), false, [2]int{}},
		{"truncated", vp8(good, start, 40, 24)[:9], false, [2]int{}},
	} {
		w, h, ok := vp8Info(tc.data)
		if ok != tc.ok || (ok && (w != tc.wantWH[0] || h != tc.wantWH[1])) {
			t.Errorf("vp8Info %s = %dx%d %v, want %v %v", tc.name, w, h, ok, tc.wantWH, tc.ok)
		}
	}

	vp8l := func(w, h int, alpha bool, version uint32) []byte {
		bits := uint32(w-1) | uint32(h-1)<<14 | version<<29 //nolint:gosec // G115: test values
		if alpha {
			bits |= 1 << 28
		}
		return binary.LittleEndian.AppendUint32([]byte{vp8lMagicByte}, bits)
	}
	for _, tc := range []struct {
		name  string
		data  []byte
		ok    bool
		w, h  int
		alpha bool
	}{
		{"valid alpha", vp8l(40, 24, true, 0), true, 40, 24, true},
		{"valid opaque", vp8l(16384, 1, false, 0), true, 16384, 1, false},
		{"version 1", vp8l(40, 24, false, 1), false, 0, 0, false},
		{"bad magic", append([]byte{0x2e}, vp8l(40, 24, false, 0)[1:]...), false, 0, 0, false},
		{"truncated", vp8l(40, 24, false, 0)[:4], false, 0, 0, false},
	} {
		w, h, alpha, ok := vp8lInfo(tc.data)
		if ok != tc.ok || (ok && (w != tc.w || h != tc.h || alpha != tc.alpha)) {
			t.Errorf("vp8lInfo %s = %dx%d alpha=%v %v, want %dx%d alpha=%v %v", tc.name, w, h, alpha, ok, tc.w, tc.h, tc.alpha, tc.ok)
		}
	}
}

// TestTask299CorpusFiles: the two corpus files of the task report, a VP8 and a
// VP8L (alpha) simple-format file, get a VP8X canvas equal to the bitstream
// size and an ALPHA flag equal to the source's alpha.
func TestTask299CorpusFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		file  string
		w, h  int
		alpha bool
	}{
		{"Photo (1).webp", 1024, 772, false},
		{"Alpha Lossless (1).webp", 400, 301, true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("..", "..", "testdata", "corpus", "webp", "metadata-extractor", tc.file)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, nil, nil, []byte("<x:xmpmeta/>"), true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			vp8x, _ := vp8xOf(t, out.Bytes())
			if len(vp8x) != vp8xChunkSize {
				t.Fatalf("VP8X payload is %d bytes", len(vp8x))
			}
			w := 1 + (int(vp8x[4]) | int(vp8x[5])<<8 | int(vp8x[6])<<16)
			h := 1 + (int(vp8x[7]) | int(vp8x[8])<<8 | int(vp8x[9])<<16)
			alpha := binary.LittleEndian.Uint32(vp8x)&vp8xFlagAlpha != 0
			if w != tc.w || h != tc.h || alpha != tc.alpha {
				t.Errorf("VP8X canvas %dx%d alpha=%v, want %dx%d alpha=%v", w, h, alpha, tc.w, tc.h, tc.alpha)
			}
		})
	}
}
