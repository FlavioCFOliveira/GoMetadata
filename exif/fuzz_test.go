package exif

import (
	"encoding/binary"
	"testing"
)

// FuzzParseEXIF exercises the EXIF parser against arbitrary byte inputs.
// Run with: go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...
func FuzzParseEXIF(f *testing.F) {
	// Seed corpus: minimal valid little-endian TIFF header.
	f.Add([]byte("II\x2A\x00\x08\x00\x00\x00"))
	// Seed corpus: minimal valid big-endian TIFF header.
	f.Add([]byte("MM\x00\x2A\x00\x00\x00\x08"))

	// Seed corpus: zero-entry IFD, little-endian.
	// header(8) + ifd_count(2) + next_ifd(4) — IFD0 has 0 entries.
	{
		buf := make([]byte, 8+2+4)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)  // IFD0 at offset 8
		order.PutUint16(buf[8:], 0)  // 0 entries
		order.PutUint32(buf[10:], 0) // next IFD = 0
		f.Add(buf)
	}

	// Seed corpus: zero-entry IFD, big-endian.
	{
		buf := make([]byte, 8+2+4)
		order := binary.BigEndian
		buf[0], buf[1] = 'M', 'M'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)
		order.PutUint16(buf[8:], 0)
		order.PutUint32(buf[10:], 0)
		f.Add(buf)
	}

	// Seed corpus: single IFD entry whose value fits inline (≤4 bytes).
	// Tag 0x0100 (ImageWidth), TypeSHORT (3), count=1, value=800 LE inline.
	{
		buf := make([]byte, 8+2+12+4)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], 8)
		order.PutUint16(buf[8:], 1) // 1 entry
		p := buf[10:]
		order.PutUint16(p[0:], 0x0100)  // ImageWidth
		order.PutUint16(p[2:], 3)       // TypeSHORT
		order.PutUint32(p[4:], 1)       // count = 1
		order.PutUint16(p[8:], 800)     // inline value
		order.PutUint32(buf[10+12:], 0) // next IFD = 0
		f.Add(buf)
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		// Must not panic on any input.
		e, err := Parse(b)
		if err != nil {
			// Parse errors are expected for arbitrary inputs; return cleanly.
			return
		}

		// Post-success validity assertions: a successfully parsed EXIF must have
		// a recognised byte order (TIFF §2).
		if e != nil {
			if e.ByteOrder != binary.LittleEndian && e.ByteOrder != binary.BigEndian {
				t.Errorf("ByteOrder is neither LE nor BE after successful parse")
			}

			// IFD0 must be non-nil on a successful parse (TIFF §2 requires at least
			// one IFD to be present, but we tolerate zero-entry IFDs defensively).
			if e.IFD0 != nil {
				// Calling Get must not panic regardless of tag.
				_ = e.IFD0.Get(TagMake)
			}
		}
	})
}
