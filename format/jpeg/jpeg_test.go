package jpeg

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildJPEG builds a minimal JPEG stream with optional APP1(EXIF) and APP13(IPTC) segments.
func buildJPEG(exifData, iptcData, xmpData []byte) []byte {
	var buf bytes.Buffer

	// SOI
	buf.Write([]byte{0xFF, 0xD8})

	if exifData != nil {
		// APP1 with Exif header
		payload := append([]byte("Exif\x00\x00"), exifData...)
		length := uint16(len(payload) + 2)
		buf.Write([]byte{0xFF, 0xE1})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(payload)
	}

	if xmpData != nil {
		// APP1 with XMP namespace
		ns := "http://ns.adobe.com/xap/1.0/\x00"
		payload := append([]byte(ns), xmpData...)
		length := uint16(len(payload) + 2)
		buf.Write([]byte{0xFF, 0xE1})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(payload)
	}

	if iptcData != nil {
		// APP13 with Photoshop IRB wrapper
		// Photoshop IRB: "Photoshop 3.0\0" + 8BIM + resource ID 0x0404 + ...
		var irb bytes.Buffer
		irb.WriteString("Photoshop 3.0\x00")
		irb.WriteString("8BIM")
		irb.Write([]byte{0x04, 0x04}) // IPTC resource ID
		irb.Write([]byte{0x00, 0x00}) // pascal string (empty name)
		// Resource data size (4 bytes BE)
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], uint32(len(iptcData)))
		irb.Write(sz[:])
		irb.Write(iptcData)
		if len(iptcData)%2 != 0 {
			irb.WriteByte(0x00)
		}

		length := uint16(irb.Len() + 2)
		buf.Write([]byte{0xFF, 0xED})
		var lbuf [2]byte
		binary.BigEndian.PutUint16(lbuf[:], length)
		buf.Write(lbuf[:])
		buf.Write(irb.Bytes())
	}

	// Minimal SOS + EOI
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})

	return buf.Bytes()
}

// minimalTIFFBytes builds a 3-entry TIFF suitable as EXIF payload.
func minimalTIFFBytes() []byte {
	order := binary.LittleEndian
	buf := make([]byte, 8+2+1*12+4)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], 1)
	p := 10
	order.PutUint16(buf[p:], 0x0100) // ImageWidth
	order.PutUint16(buf[p+2:], 4)    // LONG
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 800)
	return buf
}

func TestExtractEXIF(t *testing.T) {
	tiffData := minimalTIFFBytes()
	jpeg := buildJPEG(tiffData, nil, nil)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("rawEXIF is nil")
	}
	if rawIPTC != nil {
		t.Error("expected nil rawIPTC")
	}
	if rawXMP != nil {
		t.Error("expected nil rawXMP")
	}
	_ = rawEXIF
}

func TestExtractIPTC(t *testing.T) {
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(nil, iptcData, nil)

	_, rawIPTC, _, err := Extract(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if rawIPTC == nil {
		t.Fatal("rawIPTC is nil")
	}
	if !bytes.Equal(rawIPTC, iptcData) {
		t.Errorf("rawIPTC = %q, want %q", rawIPTC, iptcData)
	}
}

func TestInjectRoundTrip(t *testing.T) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)

	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(jpeg), &out, tiffData, newIPTC, nil); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	_, gotIPTC, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Extract after inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, newIPTC) {
		t.Errorf("IPTC after inject: got %q, want %q", gotIPTC, newIPTC)
	}
}

func BenchmarkJPEGExtract(b *testing.B) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)
	b.SetBytes(int64(len(jpeg)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = Extract(bytes.NewReader(jpeg))
	}
}

func BenchmarkJPEGInject(b *testing.B) {
	tiffData := minimalTIFFBytes()
	iptcData := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'H', 'e', 'l', 'l', 'o'}
	jpeg := buildJPEG(tiffData, iptcData, nil)
	newIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'N', 'e', 'w'}
	b.SetBytes(int64(len(jpeg)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(jpeg), &out, tiffData, newIPTC, nil)
	}
}
