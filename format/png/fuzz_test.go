package png

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func FuzzPNGExtract(f *testing.F) {
	// Seed with a valid minimal PNG (signature + IHDR + IEND).
	seed := buildPNG(nil, nil)
	f.Add(seed)

	// Seed with PNG containing EXIF.
	exifSeed := buildPNG([]byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}, nil)
	f.Add(exifSeed)

	// Seed with PNG containing XMP.
	xmpSeed := buildPNG(nil, []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>"))
	f.Add(xmpSeed)

	// Seed with empty input.
	f.Add([]byte{})

	// Seed with just the PNG signature.
	f.Add(pngSig[:])

	// Seed with PNG signature followed by a chunk whose length field is 0x80000000
	// (task #74 regression seed). On a 32-bit build, int(0x80000000) == -2147483648;
	// the MaxInt32 guard must fire and return ErrChunkTooLarge before any slice OOB.
	{
		buf := make([]byte, 0, 8+8)
		buf = append(buf, pngSig[:]...)
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[:4], 0x80000000)
		copy(hdr[4:8], "IHDR")
		buf = append(buf, hdr[:]...)
		f.Add(buf)
	}

	// Seed with chunk length == 0xFFFFFFFF (max uint32, task #74).
	{
		buf := make([]byte, 0, 8+8)
		buf = append(buf, pngSig[:]...)
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[:4], 0xFFFFFFFF)
		copy(hdr[4:8], "IHDR")
		buf = append(buf, hdr[:]...)
		f.Add(buf)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzPNGInject feeds arbitrary bytes as the source PNG container and
// asserts that Inject never panics — it must return an error or write valid
// output, never crash. The no-panic contract is the primary correctness
// invariant for the Inject write path: chunk-stream re-walk (readChunk),
// eXIf/iTXt(XMP) chunk rebuild, and the synthetic-IEND fallback for a source
// that ends without one.
//
// preserveUnknownSegments must be true for PNG — passing false returns
// ErrPreserveUnknownSegmentsNotSupported before any parsing begins (see the
// Inject godoc). Fuzzing that early-return gate with a constant `false` would
// add no coverage, so every iteration below fixes it at true, mirroring the
// tiff/webp/heif Inject fuzzers (task #258).
//
// Fixed short metadata payloads are used for rawEXIF/rawXMP so the fuzzer
// focuses on structural variation in the container bytes. rawIPTC is always
// nil because PNG does not natively support IPTC (Inject ignores it).
func FuzzPNGInject(f *testing.F) {
	// Seed 1: minimal valid PNG (signature + IHDR + IEND) — no existing
	// metadata chunks.
	f.Add(buildPNG(nil, nil))

	// Seed 2: PNG already carrying an eXIf chunk — exercises the "replace
	// existing EXIF" path.
	f.Add(buildPNG([]byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}, nil))

	// Seed 3: PNG already carrying an XMP iTXt chunk — exercises the
	// "replace existing XMP" path.
	f.Add(buildPNG(nil, []byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>")))

	// Seed 4: PNG carrying both EXIF and XMP.
	f.Add(buildPNG(
		[]byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00},
		[]byte("<?xpacket begin='' uid='x'?><xmpmeta/><?xpacket end='r'?>"),
	))

	// Seed 5: empty input — exercises the signature read-error path.
	f.Add([]byte{})

	// Seed 6: just the PNG signature, no chunks at all — exercises the
	// truncated-input / synthetic-IEND fallback path.
	f.Add(pngSig[:])

	// Seed 7: PNG signature followed by a chunk with a corrupt/mismatched
	// length field (task #74 style regression seed) — exercises readChunk's
	// bounds guard on the write path.
	{
		buf := make([]byte, 0, 8+8)
		buf = append(buf, pngSig[:]...)
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[:4], 0x80000000)
		copy(hdr[4:8], "IHDR")
		buf = append(buf, hdr[:]...)
		f.Add(buf)
	}

	// Seed 8: not a PNG at all (wrong signature) — exercises ErrInvalidSignature.
	f.Add([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07})

	// Fixed metadata payloads used for all fuzz iterations. The fuzzer varies
	// the container bytes; the EXIF/XMP payloads are kept short and constant
	// so that Inject reaches the chunk-rebuild logic on every iteration.
	rawEXIF := []byte{
		'I', 'I', 0x2A, 0x00, // LE TIFF header
		0x08, 0x00, 0x00, 0x00, // IFD0 at offset 8
		0x00, 0x00, // 0 entries
		0x00, 0x00, 0x00, 0x00, // next-IFD = 0
	}
	rawXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, rawEXIF, nil, rawXMP, true)
		_ = err
	})
}
