package jpeg

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzJPEGExtract(f *testing.F) {
	// Seed: minimal JPEG (SOI + EOI).
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xD9})

	// Seed: JPEG with a single APP1 marker and truncated length.
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0x00, 0x00})

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: SOI only.
	f.Add([]byte{0xFF, 0xD8})

	// Seed: minimal JPEG with a complete APP1(EXIF) segment (LE TIFF, 0 entries).
	// SOI + APP1 marker + length(14) + "Exif\x00\x00" + "II\x2A\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"
	// header(8) + ifd_count(2) + next_ifd(4) = 14 bytes TIFF payload.
	{
		tiff := []byte{
			'I', 'I', 0x2A, 0x00, // LE magic
			0x08, 0x00, 0x00, 0x00, // IFD0 at 8
			0x00, 0x00, // 0 entries
			0x00, 0x00, 0x00, 0x00, // next IFD
		}
		seed := buildJPEG(tiff, nil, nil)
		f.Add(seed)
	}

	// Seed: minimal JPEG with an APP13(IPTC) segment containing one dataset.
	{
		iptc := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'k', 'w', '1'}
		seed := buildJPEG(nil, iptc, nil)
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		// Extract signature: (rawEXIF, rawIPTC, rawXMP []byte, err error).
		rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(data))
		if err != nil {
			return
		}

		// Post-success assertions: validate structural invariants on non-nil outputs.

		// A non-empty EXIF payload must carry at least a TIFF header (8 bytes:
		// byte-order mark + magic + IFD0 offset). TIFF §2 mandates exactly these
		// 8 bytes before any IFD data. A zero-length slice means the APP1 existed
		// but the TIFF payload was absent or stripped by the parser; that is
		// allowed (the parser is lenient about truncated segments).
		if len(rawEXIF) > 0 && len(rawEXIF) < 8 {
			t.Errorf("rawEXIF too short after successful Extract: got %d bytes, want >= 8", len(rawEXIF))
		}

		// A valid XMP payload that was successfully extracted and returned as
		// non-nil must contain a recognised XMP structural marker.
		// (XMP specification Part 1 §7.3 — the xpacket PI or xmpmeta element.)
		// Only assert when the payload is non-empty: a zero-length slice is a
		// degenerate-but-benign result from a corrupt or stub APP1.
		if len(rawXMP) > 0 {
			s := string(rawXMP)
			if !strings.Contains(s, "<?xpacket") && !strings.Contains(s, "<rdf:") && !strings.Contains(s, "<x:xmpmeta") {
				t.Errorf("rawXMP does not contain a recognised XMP marker: %q", s[:min(64, len(s))])
			}
		}
	})
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
