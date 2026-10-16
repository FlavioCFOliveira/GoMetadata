package jpeg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
)

// sof builds a frame header segment with the marker code m.
func sof(m byte, width, height uint16) []byte {
	b := []byte{0xFF, m, 0x00, 0x11, 0x08}
	b = binary.BigEndian.AppendUint16(b, height)
	b = binary.BigEndian.AppendUint16(b, width)
	return append(b, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1)
}

// seg builds an APPn-style segment holding n payload bytes.
func seg(m byte, n int) []byte {
	b := binary.BigEndian.AppendUint16([]byte{0xFF, m}, uint16(n+2)) //nolint:gosec // G115: test sizes are small
	return append(b, make([]byte, n)...)
}

func TestImageSize(t *testing.T) {
	t.Parallel()
	soi := []byte{0xFF, 0xD8}
	sos := []byte{0xFF, 0xDA, 0x00, 0x02}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	tests := []struct {
		name string
		data []byte
		w, h uint32
		err  error
	}{
		{"baseline", cat(soi, sof(0xC0, 640, 480), sos), 640, 480, nil},
		{"progressive", cat(soi, sof(0xC2, 4000, 3000), sos), 4000, 3000, nil},
		{"after APP1 and DQT", cat(soi, seg(0xE1, 300), seg(0xDB, 65), sof(0xC0, 1, 2), sos), 1, 2, nil},
		{"fill bytes before the marker", cat(soi, []byte{0xFF, 0xFF, 0xFF}, sof(0xC0, 9, 8)[1:], sos), 9, 8, nil},
		{"DHT, JPG and DAC are not frame headers", cat(soi, seg(0xC4, 20), seg(0xC8, 4), seg(0xCC, 4), sof(0xC1, 7, 6), sos), 7, 6, nil},
		{"SOS first", cat(soi, sos, sof(0xC0, 5, 5)), 0, 0, ErrNoFrameHeader},
		{"EOI first", cat(soi, []byte{0xFF, 0xD9}), 0, 0, ErrNoFrameHeader},
		{"truncated frame header", cat(soi, sof(0xC0, 5, 5)[:8]), 0, 0, ErrNoFrameHeader},
		{"zero height (DNL)", cat(soi, sof(0xC0, 5, 0), sos), 0, 0, ErrNoFrameHeader},
		{"segment length past the end", cat(soi, []byte{0xFF, 0xE1, 0xFF, 0xFF}, make([]byte, 10)), 0, 0, ErrNoFrameHeader},
		{"not at a marker", cat(soi, []byte{0x00, 0x01, 0x02}), 0, 0, ErrNoFrameHeader},
		{"not a JPEG", []byte("GIF89a"), 0, 0, ErrNotJPEG},
		{"empty", nil, 0, 0, io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, h, err := ImageSize(bytes.NewReader(tc.data))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || w != tc.w || h != tc.h {
				t.Errorf("ImageSize = %d x %d, %v; want %d x %d", w, h, err, tc.w, tc.h)
			}
		})
	}
}

func TestImageSizeCorpus(t *testing.T) {
	t.Parallel()
	const path = "../../testdata/corpus/jpeg/metadata-extractor/Google Cardboard.jpg"
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	w, h, err := ImageSize(bytes.NewReader(data))
	if err != nil || w == 0 || h == 0 {
		t.Fatalf("ImageSize = %d x %d, %v", w, h, err)
	}
}

func FuzzImageSize(f *testing.F) {
	f.Add(append([]byte{0xFF, 0xD8}, sof(0xC0, 640, 480)...))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x04, 1, 2, 0xFF, 0xC0, 0x00, 0x08})
	f.Fuzz(func(t *testing.T, b []byte) {
		w, h, err := ImageSize(bytes.NewReader(b))
		if err == nil && (w == 0 || h == 0) {
			t.Errorf("no error but %d x %d", w, h)
		}
	})
}
