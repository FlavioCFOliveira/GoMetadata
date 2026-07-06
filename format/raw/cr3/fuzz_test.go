package cr3

import (
	"bytes"
	"io"
	"testing"
)

func FuzzCR3Extract(f *testing.F) {
	// Seed: minimal CR3 with valid ISOBMFF structure.
	minTIFF := func() []byte {
		buf := make([]byte, 14)
		buf[0], buf[1] = 'I', 'I'
		buf[2], buf[3] = 0x2A, 0x00
		buf[4], buf[5], buf[6], buf[7] = 0x08, 0x00, 0x00, 0x00
		return buf
	}
	f.Add(buildMinimalCR3(minTIFF(), nil))

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated ftyp box.
	f.Add([]byte{0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p'})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzCR3Inject feeds arbitrary bytes as the source CR3 container and
// asserts that Inject never panics — it must return an error or write valid
// output, never crash. The no-panic contract is the primary correctness
// invariant for the Inject write path: locating moov (findMoovRange),
// rebuilding the Canon uuid box content (rebuildMoovContent/
// rebuildUUIDContent), and relocating stco/co64 chunk offsets by the moov
// size delta (relocateChunkOffsets).
//
// preserveUnknownSegments must be true for CR3 — passing false returns
// ErrPreserveUnknownSegmentsNotSupported before any parsing begins (ISOBMFF
// boxes are structurally mandatory; there is no "unknown optional segment"
// concept analogous to JPEG APPn). Every iteration below fixes it at true,
// mirroring the tiff/webp/heif/png/jpeg Inject fuzzers (task #258).
//
// rawIPTC is always nil: injectIntoMoov's own godoc states it is intentionally
// ignored because CR3 does not carry IPTC.
//
// CR3-EXTSIZE-01 regression lock-in: seeds 5-7 below reuse buildExtendedBox,
// buildExtendedUUIDBox, and ftypBox16 from extended_size_test.go — the exact
// byte sequences that exposed the HIGH-severity extended-box-size write bug
// (hardcoded +8/+24 header-length assumption instead of re-deriving the real
// ISO 14496-12 §4.2 header length via parseCR3BoxHeader). Keeping these as
// fuzz seeds means any future regression that reintroduces a hardcoded
// header-length offset will be caught by mutation as well as by the unit
// tests in extended_size_test.go.
func FuzzCR3Inject(f *testing.F) {
	// Seed 1: minimal, valid CR3 (ftyp + moov + uuid + CMT1) — exercises the
	// full rebuild/relocate path on well-formed input.
	f.Add(buildMinimalCR3(minimalTIFF(), nil))

	// Seed 2: minimal CR3 with both CMT1 and an XMP sub-box present.
	f.Add(buildMinimalCR3(minimalTIFF(), []byte(`<?xpacket begin=''?><x:xmpmeta xmlns:x='adobe:ns:meta/'/><?xpacket end='r'?>`)))

	// Seed 3: ftyp only, no moov box at all — exercises the "no moov found,
	// pass through unchanged" branch in Inject.
	f.Add(ftypBox16())

	// Seed 4: empty input — exercises the ReadAll/pass-through path when no
	// moov can be located in a zero-length buffer.
	f.Add([]byte{})

	// Seed 5: truncated ftyp box header.
	f.Add([]byte{0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p'})

	// Seed 6: CR3-EXTSIZE-01 variant A — the moov box itself uses the
	// extended (largesize) box encoding wrapping a normal-size Canon uuid box.
	// Regression: injectIntoMoov previously hardcoded the moov content offset
	// at +8 instead of the true +16, corrupting the rebuilt uuid content.
	{
		cmt1Box := buildBox("CMT1", minimalTIFF())
		uuidBox := buildUUIDBox(canonUUID, cmt1Box)
		extendedMoov := buildExtendedBox("moov", uuidBox)
		f.Add(append(ftypBox16(), extendedMoov...))
	}

	// Seed 7: CR3-EXTSIZE-01 variant B — a normal-size moov box wraps a
	// Canon uuid box that itself uses the extended box encoding, with CMT1,
	// CMT2, and an "XMP " sibling sub-box inside it.
	// Regression: rebuildMoovContent previously hardcoded the uuid content
	// offset at +24 (8-byte header + 16-byte UUID) instead of the true
	// +32 (16-byte extended header + 16-byte UUID), desynchronising the
	// sibling sub-box scan and silently dropping CMT2/XMP from the output.
	{
		cmt1Box := buildBox("CMT1", minimalTIFF())
		cmt2Box := buildBox("CMT2", []byte("fuzz-cr3-cmt2-payload"))
		xmpBox := buildBox("XMP ", []byte(`<?xpacket begin=''?><x:xmpmeta xmlns:x='adobe:ns:meta/'/><?xpacket end='r'?>`))
		uuidContent := append(append(append([]byte{}, cmt1Box...), cmt2Box...), xmpBox...)
		extendedUUID := buildExtendedUUIDBox(canonUUID, uuidContent)
		moovBox := buildBox("moov", extendedUUID)
		f.Add(append(ftypBox16(), moovBox...))
	}

	// Seed 8: both moov AND the Canon uuid box use extended-size encoding
	// simultaneously — combines variants A and B in a single file.
	{
		cmt1Box := buildBox("CMT1", minimalTIFF())
		extendedUUID := buildExtendedUUIDBox(canonUUID, cmt1Box)
		extendedMoov := buildExtendedBox("moov", extendedUUID)
		f.Add(append(ftypBox16(), extendedMoov...))
	}

	// Fixed metadata payloads used for all fuzz iterations. The fuzzer varies
	// the container bytes; the EXIF/XMP payloads are kept short and constant
	// so that Inject reaches the moov-rebuild logic on every iteration that
	// contains a locatable moov box.
	rawEXIF := minimalTIFF()
	rawXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, rawEXIF, nil, rawXMP, true)
		_ = err
	})
}
