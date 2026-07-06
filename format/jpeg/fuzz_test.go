package jpeg

import (
	"bytes"
	"io"
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

		// rawXMP carries verbatim bytes extracted from the JPEG APP1 segment
		// following the "http://ns.adobe.com/xap/1.0/\x00" namespace prefix.
		// The JPEG parser does not validate XMP content; that is the role of
		// the xmp package. Any non-nil rawXMP is structurally acceptable here.
		// We assert only that the length is not obviously corrupt (non-negative),
		// which is always true for a []byte. No XMP-content assertions are made
		// at this layer, as crafted inputs can embed arbitrary bytes after the
		// namespace prefix.
		_ = rawXMP
	})
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// FuzzJPEGInject feeds arbitrary bytes as the source JPEG container and
// asserts that Inject never panics — it must return an error or write valid
// output, never crash. The no-panic contract is the primary correctness
// invariant for the Inject write path: APP1(EXIF)/APP1(XMP)/APP13(IPTC)
// segment rebuild, extended-XMP GUID generation and chunk splitting, and the
// Photoshop IRB sibling-resource pre-scan (extractOriginalIRB) that must
// tolerate a malformed or truncated original APP13 without panicking.
//
// preserveUnknownSegments is fixed at true for every iteration, mirroring the
// tiff/webp/heif/png Inject fuzzers (task #258): JPEG's false branch is a
// single additional filter in copyNonMetadataSegments that drops non-metadata
// APPn segments and is already covered by table-driven unit tests; pinning it
// at true lets the fuzzer budget go toward the structurally interesting
// container bytes instead of re-deriving that branch.
//
// Fixed short metadata payloads are used for rawEXIF/rawIPTC/rawXMP so the
// fuzzer focuses on structural variation in the container bytes rather than
// payload content.
func FuzzJPEGInject(f *testing.F) {
	// Seed 1: minimal JPEG (SOI + EOI) — no existing metadata segments;
	// exercises the "no origIRB, write fresh segments" path.
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xD9})

	// Seed 2: empty input — exercises the SOI read-error path.
	f.Add([]byte{})

	// Seed 3: SOI only (truncated, no EOI).
	f.Add([]byte{0xFF, 0xD8})

	// Seed 4: not a JPEG at all (no SOI marker) — exercises ErrNotJPEG.
	f.Add([]byte{0x00, 0x01, 0x02, 0x03})

	// Seed 5: truncated APP1 length field — exercises an early-return error
	// path while copying non-metadata segments.
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0x00, 0x00})

	// Seed 6: minimal JPEG with a complete APP1(EXIF) segment (LE TIFF,
	// 0 entries) — exercises the "replace existing EXIF" path.
	{
		tiffData := []byte{
			'I', 'I', 0x2A, 0x00, // LE magic
			0x08, 0x00, 0x00, 0x00, // IFD0 at 8
			0x00, 0x00, // 0 entries
			0x00, 0x00, 0x00, 0x00, // next IFD
		}
		f.Add(buildJPEG(tiffData, nil, nil))
	}

	// Seed 7: minimal JPEG with an APP13(IPTC) Photoshop IRB segment —
	// exercises the origIRB pre-scan and sibling-resource preservation path.
	{
		iptc := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'k', 'w', '1'}
		f.Add(buildJPEG(nil, iptc, nil))
	}

	// Seed 8: minimal JPEG carrying EXIF, IPTC, and XMP simultaneously —
	// exercises writeNewMetadataSegments with all three payloads present.
	{
		tiffData := []byte{
			'I', 'I', 0x2A, 0x00,
			0x08, 0x00, 0x00, 0x00,
			0x00, 0x00,
			0x00, 0x00, 0x00, 0x00,
		}
		iptc := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'k', 'w', '1'}
		xmpData := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`)
		f.Add(buildJPEG(tiffData, iptc, xmpData))
	}

	// Fixed metadata payloads used for all fuzz iterations. The fuzzer varies
	// the container bytes; the EXIF/IPTC/XMP payloads are kept short and
	// constant so that Inject reaches the segment-rebuild logic on every
	// iteration regardless of what the source container contains.
	rawEXIF := []byte{
		'I', 'I', 0x2A, 0x00,
		0x08, 0x00, 0x00, 0x00,
		0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	rawIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'f', 'z', '1'}
	rawXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, rawEXIF, rawIPTC, rawXMP, true)
		_ = err
	})
}
