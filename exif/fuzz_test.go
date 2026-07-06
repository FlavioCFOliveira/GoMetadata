package exif

import (
	"encoding/binary"
	"os"
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

	// --- BigTIFF seeds (task #54 final pass) ---

	// Seed: minimal BigTIFF LE header only (no IFD entries).
	// BigTIFF spec §2: magic 0x002B, offset-bytesize=8.
	{
		buf := make([]byte, 16+8+8) // header + count(8) + next(8)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
		order.PutUint16(buf[4:], 8)      // offset-bytesize=8
		order.PutUint16(buf[6:], 0)      // constant 0
		order.PutUint64(buf[8:], 16)     // IFD0 at offset 16
		order.PutUint64(buf[16:], 0)     // count = 0
		// next-IFD = 0 (already zero)
		f.Add(buf)
	}

	// Seed: minimal BigTIFF BE header only.
	{
		buf := make([]byte, 16+8+8)
		order := binary.BigEndian
		buf[0], buf[1] = 'M', 'M'
		order.PutUint16(buf[2:], 0x002B)
		order.PutUint16(buf[4:], 8)
		order.PutUint16(buf[6:], 0)
		order.PutUint64(buf[8:], 16)
		order.PutUint64(buf[16:], 0)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with one Make entry (6-byte ASCII "Canon\x00", inline).
	// BigTIFF inline threshold = 8, so 6-byte ASCII is stored inline.
	{
		const (
			hdrSize  = 16
			cntSize  = 8
			entSize  = 20
			nextSize = 8
		)
		buf := make([]byte, hdrSize+cntSize+entSize+nextSize)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002B)
		order.PutUint16(buf[4:], 8)
		order.PutUint16(buf[6:], 0)
		order.PutUint64(buf[8:], hdrSize)
		order.PutUint64(buf[hdrSize:], 1) // 1 entry
		p := hdrSize + cntSize
		order.PutUint16(buf[p:], 0x010F) // Make
		order.PutUint16(buf[p+2:], 2)    // TypeASCII
		order.PutUint64(buf[p+4:], 6)    // count = 6 ("Canon\x00")
		copy(buf[p+12:], "Canon\x00")    // inline value (6 bytes ≤ 8)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with bad offset-bytesize (should be rejected cleanly).
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 4) // invalid: not 8
		binary.LittleEndian.PutUint64(buf[8:], 16)
		f.Add(buf)
	}

	// Seed: BigTIFF LE with IFD0 offset pointing beyond EOF.
	{
		buf := make([]byte, 16)
		buf[0], buf[1] = 'I', 'I'
		binary.LittleEndian.PutUint16(buf[2:], 0x002B)
		binary.LittleEndian.PutUint16(buf[4:], 8)
		binary.LittleEndian.PutUint64(buf[8:], 0xFFFFFFFFFFFFFF00) // way past EOF
		f.Add(buf)
	}

	// Seed: BigTIFF LE with one entry using LONG8 type (type code 16).
	// StripOffsets as LONG8, count=1, value=0x10 (inline, 8 bytes).
	{
		const (
			hdrSize  = 16
			cntSize  = 8
			entSize  = 20
			nextSize = 8
		)
		buf := make([]byte, hdrSize+cntSize+entSize+nextSize)
		order := binary.LittleEndian
		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002B)
		order.PutUint16(buf[4:], 8)
		order.PutUint16(buf[6:], 0)
		order.PutUint64(buf[8:], hdrSize)
		order.PutUint64(buf[hdrSize:], 1)
		p := hdrSize + cntSize
		order.PutUint16(buf[p:], 0x0111)  // StripOffsets
		order.PutUint16(buf[p+2:], 16)    // LONG8
		order.PutUint64(buf[p+4:], 1)     // count=1
		order.PutUint64(buf[p+12:], 0x10) // inline value=16
		f.Add(buf)
	}

	// Seed: the committed real-world BigTIFF fixture (produced by
	// `tiffcp -8`), added directly as fuzz-corpus material (task #264). This
	// gives the mutator a structurally rich, spec-conformant BigTIFF starting
	// point — 22 IFD0 entries spanning inline SHORT/RATIONAL/LONG8 values and
	// a large out-of-line UNDEFINED (ICC profile) blob — rather than only the
	// minimal synthetic seeds above.
	if data, err := os.ReadFile("testdata/BigTIFF_LE.tif"); err == nil {
		f.Add(data)
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

			// task #264: fuzz the Encode round-trip too, not just Parse. Any
			// successfully parsed EXIF (classic or BigTIFF) must Encode without
			// panicking, and a successful Encode must produce output that
			// re-Parses without panicking and never silently changes container
			// provenance (BigTIFF must stay BigTIFF — audit finding #107).
			// Deep per-tag equality is intentionally NOT asserted here: that is
			// covered precisely by the deterministic round-trip tests in
			// bigtiff_write_test.go and TestConformance_R14_bigtiff_roundtrip_fidelity;
			// this fuzz target's job is to catch crashes and format-downgrade
			// regressions across the full space of malformed/edge-case inputs.
			encoded, encErr := Encode(e)
			if encErr == nil {
				e2, reErr := Parse(encoded)
				if reErr == nil && e2 != nil && e.BigTIFF != e2.BigTIFF {
					t.Errorf("Encode round-trip changed BigTIFF provenance: before=%v after=%v", e.BigTIFF, e2.BigTIFF)
				}
			}
		}
	})
}
