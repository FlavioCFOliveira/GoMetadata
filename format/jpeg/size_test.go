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

// sosHeader is a start-of-scan segment of one component.
func sosHeader() []byte { return []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00} }

// dnl builds a define-number-of-lines segment.
func dnl(ld, nl uint16) []byte {
	b := binary.BigEndian.AppendUint16([]byte{0xFF, 0xDC}, ld)
	return binary.BigEndian.AppendUint16(b, nl)
}

// TestImageSizeDNL: a frame header with Y = 0 takes the number of lines from
// the DNL segment that ends the first scan (ITU-T T.81 B.2.2, p. 36; B.2.5,
// p. 45). JPEG-DNL-01.
func TestImageSizeDNL(t *testing.T) {
	t.Parallel()
	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	// entropy data with stuffed bytes, restart markers and a fill byte.
	ecs := []byte{0x12, 0xFF, 0x00, 0x34, 0xFF, 0xD0, 0x56, 0xFF, 0xD7, 0xFF, 0xFF, 0x00, 0x78}
	big := bytes.Repeat([]byte{0x55, 0xFF, 0x00}, 40000) // 120000 bytes: crosses read buffers
	tests := []struct {
		name string
		data []byte
		h    uint32
		err  error
	}{
		{"DNL after the scan", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, dnl(4, 200), eoi), 200, nil},
		{"DNL right after the scan header", cat(soi, sof(0xC1, 5, 0), sosHeader(), dnl(4, 7), eoi), 7, nil},
		{"tables between the frame header and the scan", cat(soi, sof(0xC1, 5, 0), seg(0xC4, 20), seg(0xDB, 65), sosHeader(), ecs, dnl(4, 9), eoi), 9, nil},
		{"progressive frame", cat(soi, sof(0xC2, 6, 0), sosHeader(), ecs, dnl(4, 65535), eoi), 65535, nil},
		{"larger Ld is accepted", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, dnl(6, 11), []byte{0, 0}, eoi), 11, nil},
		{"data across read buffers", cat(soi, sof(0xC0, 5, 0), sosHeader(), big, dnl(4, 1234), eoi), 1234, nil},
		{"fill bytes before the marker", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, []byte{0xFF, 0xFF}, dnl(4, 8)[1:], eoi), 8, nil},
		{"Y set: the DNL segment is not needed", cat(soi, sof(0xC0, 5, 3), sosHeader(), ecs, dnl(4, 200), eoi), 3, nil},
		{"no DNL segment", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, eoi), 0, ErrNoFrameHeader},
		{"EOI before the scan", cat(soi, sof(0xC0, 5, 0), eoi), 0, ErrNoFrameHeader},
		{"NL is 0", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, dnl(4, 0), eoi), 0, ErrNoFrameHeader},
		{"Ld below 4", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, dnl(3, 5), eoi), 0, ErrNoFrameHeader},
		{"DNL truncated", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, []byte{0xFF, 0xDC, 0x00}), 0, ErrNoFrameHeader},
		{"second scan first", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs, sosHeader(), dnl(4, 9)), 0, ErrNoFrameHeader},
		{"scan header length below 2", cat(soi, sof(0xC0, 5, 0), []byte{0xFF, 0xDA, 0x00, 0x01}, dnl(4, 9)), 0, ErrNoFrameHeader},
		{"stream ends in the data", cat(soi, sof(0xC0, 5, 0), sosHeader(), ecs), 0, ErrNoFrameHeader},
	}
	for _, tc := range tests {
		t.Run("JPEG-DNL-01/"+tc.name, func(t *testing.T) {
			t.Parallel()
			w, h, err := ImageSize(bytes.NewReader(tc.data))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || (w != 5 && w != 6) || h != tc.h {
				t.Errorf("ImageSize = %d x %d, %v; want a width of 5 or 6 and height %d", w, h, err, tc.h)
			}
		})
	}
}

// TestScanForDNLLimit: the search reads no more than its limit.
func TestScanForDNLLimit(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-DNL-01", func(t *testing.T) {
		t.Parallel()
		data := append(bytes.Repeat([]byte{0x55}, 100000), dnl(4, 5)...)
		if _, err := scanForDNL(bytes.NewReader(data), 50000); !errors.Is(err, ErrNoFrameHeader) {
			t.Errorf("err = %v, want ErrNoFrameHeader beyond the limit", err)
		}
		if h, err := scanForDNL(bytes.NewReader(data), 1<<20); err != nil || h != 5 {
			t.Errorf("scanForDNL = %d, %v; want 5", h, err)
		}
	})
}

// TestImageSizeDNLCorpus: beach.jpg (SOF1 with Y = 0, NL 200 in a DNL segment
// after the scan; ExifTool reports ImageHeight 0) has the width and the height
// of its DNL segment. JPEG-DNL-01.
func TestImageSizeDNLCorpus(t *testing.T) {
	t.Parallel()
	t.Run("JPEG-DNL-01", func(t *testing.T) {
		t.Parallel()
		const path = "../../testdata/corpus/jpeg/metadata-extractor/beach.jpg"
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
		}
		if err != nil {
			t.Fatal(err)
		}
		w, h, err := ImageSize(bytes.NewReader(data))
		if err != nil || w != 200 || h != 200 {
			t.Fatalf("ImageSize = %d x %d, %v; want 200 x 200", w, h, err)
		}
	})
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
	f.Add(append(append(append([]byte{0xFF, 0xD8}, sof(0xC0, 5, 0)...), sosHeader()...), append([]byte{0x12, 0xFF, 0x00, 0xFF, 0xD0}, append(dnl(4, 200), 0xFF, 0xD9)...)...))
	f.Fuzz(func(t *testing.T, b []byte) {
		w, h, err := ImageSize(bytes.NewReader(b))
		if err == nil && (w == 0 || h == 0) {
			t.Errorf("no error but %d x %d", w, h)
		}
	})
}

// BenchmarkImageSizeDNL measures the search for the DNL segment in 1 MiB of
// entropy-coded data (a frame header that declares zero lines), and the size of
// an ordinary frame header for comparison.
func BenchmarkImageSizeDNL(b *testing.B) {
	scan := bytes.Repeat([]byte{0x55, 0xFF, 0x00, 0xA3}, 256<<10)
	deferred := bytes.Join([][]byte{{0xFF, 0xD8}, sof(0xC0, 640, 0), sosHeader(), scan, dnl(4, 480), {0xFF, 0xD9}}, nil)
	plain := bytes.Join([][]byte{{0xFF, 0xD8}, sof(0xC0, 640, 480), sosHeader()}, nil)
	for _, bc := range []struct {
		name string
		data []byte
	}{{"FrameHeader", plain}, {"DNL1MiB", deferred}} {
		b.Run(bc.name, func(b *testing.B) {
			b.SetBytes(int64(len(bc.data)))
			b.ReportAllocs()
			for range b.N {
				if _, _, err := ImageSize(bytes.NewReader(bc.data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
