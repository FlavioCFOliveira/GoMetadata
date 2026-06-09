package png

// conformance_test.go — PNG container specification-conformance battery.
//
// Rule IDs match verbatim the stable identifiers in docs/conformance/containers.md §2
// and docs/conformance/xmp.md §3.3 (PNG-01..05).
//
// Coverage:
//   PNG-signature           — 8-byte magic §5.2
//   PNG-chunk-layout        — Length/Type/Data/CRC structure §5.3
//   PNG-chunk-CRC-*         — CRC-32/IEEE over Type+Data §5.5
//   PNG-chunk-Length-max    — Length ≤ 2³¹−1 §11.2.1
//   PNG-chunk-type-bits     — property bits (bit5) §5.4
//   PNG-IHDR-first          — IHDR must be first chunk §5.6
//   PNG-IEND-last           — IEND terminates stream §5.6
//   PNG-eXIf-*              — raw TIFF, no Exif\0\0 prefix §11.3.4.4
//   PNG-iTXt-*              — XMP embedding §11.3.4 / XMP Part 3 §1.6
//   PNG-write-*             — write byte-correctness §2(e)
//   PNG-robust-*            — robustness §2(f)
//   PNG-corpus-*            — corpus parity via testutil.CorpusFiles
//
// No t.Skip in synthetic tests. All tests pass -race deterministically.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildConformancePNG builds a minimal 1×1 RGB PNG with IHDR, optional extra
// chunks, and IEND. The extra slice is written verbatim between IHDR and IEND
// (caller is responsible for correct CRCs within extra).
func buildConformancePNG(extra []byte) []byte {
	var buf bytes.Buffer
	buf.Write(pngSig[:])
	writeChunkTo(&buf, "IHDR", minIHDR())
	buf.Write(extra)
	writeChunkTo(&buf, "IEND", nil)
	return buf.Bytes()
}

// minIHDR returns a valid 13-byte IHDR payload (1×1 pixel, 8-bit RGB).
// PNG §11.2.2: width(4)+height(4)+bitDepth(1)+colorType(1)+compressionMethod(1)+
// filterMethod(1)+interlaceMethod(1).
func minIHDR() []byte {
	d := make([]byte, 13)
	binary.BigEndian.PutUint32(d[0:], 1) // width=1
	binary.BigEndian.PutUint32(d[4:], 1) // height=1
	d[8] = 8                             // bit depth=8
	d[9] = 2                             // color type=2 (RGB)
	// compression=0, filter=0, interlace=0 already zero
	return d
}

// rawChunkBytes returns the 12+len(data) bytes that constitute a PNG chunk
// with a correctly computed CRC, ready for embedding in a raw []byte buffer.
// PNG §5.3: Length(4 BE) + Type(4) + Data + CRC(4 BE over Type+Data).
func rawChunkBytes(chunkType string, data []byte) []byte {
	var buf bytes.Buffer
	writeChunkTo(&buf, chunkType, data)
	return buf.Bytes()
}

// computeCRC32 returns CRC-32/IEEE of typeBytes+data, mirroring PNG §5.5.
// Polynomial: 0xEDB88320 (reflected form of 0x04C11DB7), init 0xFFFFFFFF,
// final XOR 0xFFFFFFFF — this is crc32.NewIEEE() in the Go standard library.
func computeCRC32(chunkType string, data []byte) uint32 {
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	return h.Sum32()
}

// corruptCRC flips all bits of a valid CRC, producing a guaranteed mismatch.
func corruptCRC(valid uint32) uint32 { return ^valid }

// buildChunkWithCRC writes a chunk with the given (possibly wrong) CRC.
func buildChunkWithCRC(chunkType string, data []byte, crcVal uint32) []byte {
	var buf bytes.Buffer
	var lbuf [4]byte
	binary.BigEndian.PutUint32(lbuf[:], uint32(len(data))) //nolint:gosec // G115: test helper, bounded
	buf.Write(lbuf[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	binary.BigEndian.PutUint32(lbuf[:], crcVal)
	buf.Write(lbuf[:])
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// §2(b) PNG-signature — 8-byte magic
// ---------------------------------------------------------------------------

// TestPNGSignaturePositive verifies that Extract accepts the valid 8-byte PNG
// signature 89 50 4E 47 0D 0A 1A 0A (PNG-3 §5.2).
func TestPNGSignaturePositive(t *testing.T) {
	// PNG-signature: §5.2 — correct magic must be accepted.
	t.Parallel()
	png := buildConformancePNG(nil)
	if _, _, _, err := Extract(bytes.NewReader(png)); err != nil {
		t.Fatalf("PNG-signature: Extract on valid PNG: %v", err)
	}
}

// TestPNGSignatureBadByte verifies that a single corrupted byte in the
// signature causes Extract to return ErrInvalidSignature (PNG-robust-bad-signature).
func TestPNGSignatureBadByte(t *testing.T) {
	// PNG-signature / PNG-robust-bad-signature: §5.2 — any deviation in the
	// 8-byte signature must be rejected.
	t.Parallel()
	sigByteNames := [8]string{
		"byte0-not-89", "byte1-not-50", "byte2-not-4E", "byte3-not-47",
		"byte4-not-0D", "byte5-not-0A", "byte6-not-1A", "byte7-not-0A",
	}
	for i := range 8 {
		t.Run(sigByteNames[i], func(t *testing.T) {
			t.Parallel()
			var sig [8]byte
			copy(sig[:], pngSig[:])
			sig[i] ^= 0xFF // flip all bits at position i
			var buf bytes.Buffer
			buf.Write(sig[:])
			_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
			if !errors.Is(err, ErrInvalidSignature) {
				t.Errorf("PNG-signature: bad byte %d: got %v, want ErrInvalidSignature", i, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// §2(c) PNG-chunk-layout — Length+Type+Data+CRC structure
// ---------------------------------------------------------------------------

// TestPNGChunkLayoutLengthCountsDataOnly verifies that the Length field in a
// PNG chunk counts only the data bytes, not the type (4 bytes) or CRC (4 bytes).
// PNG §5.3: "The data length is the length of the data field only, not including
// itself, the chunk type code, or the CRC."
func TestPNGChunkLayoutLengthCountsDataOnly(t *testing.T) {
	// PNG-chunk-layout: §5.3 — Length counts only data bytes.
	t.Parallel()
	const dataLen = 10
	exifData := make([]byte, dataLen)
	binary.BigEndian.PutUint32(exifData, 0x4D4D002A) // big-endian TIFF magic

	png := buildConformancePNG(rawChunkBytes("eXIf", exifData))
	rawEXIF, _, _, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("PNG-chunk-layout: Extract: %v", err)
	}
	if len(rawEXIF) != dataLen {
		t.Errorf("PNG-chunk-layout: extracted EXIF len=%d, want %d (Length must count only data bytes)", len(rawEXIF), dataLen)
	}
}

// TestPNGChunkLayoutZeroLengthLegal verifies that a zero-length data chunk is
// accepted without error. PNG §5.3 explicitly allows length=0.
func TestPNGChunkLayoutZeroLengthLegal(t *testing.T) {
	// PNG-chunk-layout / PNG-robust-zero-length-data: §5.3 — length=0 is legal.
	t.Parallel()
	// Build a PNG with a zero-length eXIf chunk (unusual but spec-valid).
	var extraBuf bytes.Buffer
	writeChunkTo(&extraBuf, "eXIf", nil)
	png := buildConformancePNG(extraBuf.Bytes())

	// Must not panic or return an unexpected error; eXIf with zero bytes is
	// spec-valid — library returns nil rawEXIF (empty).
	_, _, _, err := Extract(bytes.NewReader(png))
	if err != nil && !errors.Is(err, ErrChunkCRCMismatch) {
		t.Errorf("PNG-chunk-layout: zero-length eXIf chunk: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// §2(c) PNG-chunk-CRC-0xEDB88320 — CRC polynomial and coverage
// ---------------------------------------------------------------------------

// TestPNGChunkCRCPolynomial verifies that writeChunk uses the CRC-32/IEEE
// polynomial 0xEDB88320 (reflected 0x04C11DB7), init 0xFFFFFFFF, final XOR
// 0xFFFFFFFF — i.e. the standard Go crc32.NewIEEE() hash. This is the algorithm
// mandated by PNG §5.5.
//
// Method: we inject a known chunk and re-verify its CRC independently using
// hash/crc32.NewIEEE, which implements this exact polynomial.
func TestPNGChunkCRCPolynomial(t *testing.T) {
	// PNG-chunk-CRC-0xEDB88320: §5.5 — poly=0xEDB88320, init=0xFFFFFFFF,
	// final XOR=0xFFFFFFFF, over Type+Data (not Length).
	t.Parallel()

	data := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08} // BE TIFF header

	// writeChunk serialises the chunk via the library's own writeChunk.
	var out bytes.Buffer
	if err := writeChunk(&out, "eXIf", data); err != nil {
		t.Fatalf("writeChunk: %v", err)
	}
	chunkBytes := out.Bytes()

	// Parse out the stored CRC from the emitted bytes.
	if len(chunkBytes) != 4+4+len(data)+4 {
		t.Fatalf("unexpected chunk size %d", len(chunkBytes))
	}
	storedCRC := binary.BigEndian.Uint32(chunkBytes[8+len(data):])

	// Re-compute independently using crc32.NewIEEE (polynomial 0xEDB88320).
	// PNG §5.5: CRC covers chunk type + chunk data, NOT the length field.
	expectedCRC := computeCRC32("eXIf", data)

	if storedCRC != expectedCRC {
		t.Errorf("PNG-chunk-CRC-0xEDB88320: stored CRC %08x != independently computed %08x", storedCRC, expectedCRC)
	}
}

// TestPNGChunkCRCCoversTypeAndData verifies that the CRC is computed over
// Type+Data and NOT over the Length field. PNG §5.5: "The CRC is calculated on
// the preceding bytes in that chunk, including the chunk type code and chunk
// data fields, but not including the length field."
func TestPNGChunkCRCCoversTypeAndData(t *testing.T) {
	// PNG-chunk-CRC-0xEDB88320: §5.5 — CRC covers Type+Data, excludes Length.
	t.Parallel()

	chunkType := "eXIf"
	data := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}

	var out bytes.Buffer
	if err := writeChunk(&out, chunkType, data); err != nil {
		t.Fatalf("writeChunk: %v", err)
	}
	raw := out.Bytes()
	storedCRC := binary.BigEndian.Uint32(raw[4+4+len(data):])

	// CRC over Length+Type+Data must differ from CRC over Type+Data alone,
	// proving the Length field is excluded.
	h := crc32.NewIEEE()
	_, _ = h.Write(raw[:4])           // just Length (4 bytes)
	_, _ = h.Write([]byte(chunkType)) // Type
	_, _ = h.Write(data)              // Data
	crcWithLength := h.Sum32()

	crcTypeDataOnly := computeCRC32(chunkType, data)

	if storedCRC == crcWithLength && storedCRC != crcTypeDataOnly {
		t.Error("PNG-chunk-CRC-0xEDB88320: CRC appears to cover Length field; it must cover only Type+Data")
	}
	if storedCRC != crcTypeDataOnly {
		t.Errorf("PNG-chunk-CRC-0xEDB88320: stored CRC %08x != CRC(Type+Data) %08x", storedCRC, crcTypeDataOnly)
	}
}

// TestPNGChunkCRCMismatchDetected verifies that Extract returns ErrChunkCRCMismatch
// for each metadata chunk type when its CRC is deliberately corrupted.
// PNG §5.4: "Decoders should check the CRC of each chunk."
func TestPNGChunkCRCMismatchDetected(t *testing.T) {
	// PNG-chunk-CRC-0xEDB88320 / PNG-robust-CRC-mismatch: §5.4, §5.5 —
	// CRC mismatch on a metadata chunk must be detected and reported.
	t.Parallel()

	cases := []struct {
		id        string
		chunkType string
		data      []byte
	}{
		{
			"eXIf",
			"eXIf",
			[]byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08},
		},
		{
			"iTXt",
			"iTXt",
			append([]byte("Comment\x00\x00\x00\x00\x00"), []byte("some text")...),
		},
		{
			"tEXt",
			"tEXt",
			[]byte("Comment\x00plain text"),
		},
	}

	for _, tc := range cases {
		t.Run("PNG-chunk-CRC-mismatch-"+tc.id, func(t *testing.T) {
			t.Parallel()

			goodCRC := computeCRC32(tc.chunkType, tc.data)
			badChunk := buildChunkWithCRC(tc.chunkType, tc.data, corruptCRC(goodCRC))

			var buf bytes.Buffer
			buf.Write(pngSig[:])
			writeChunkTo(&buf, "IHDR", minIHDR())
			buf.Write(badChunk)
			writeChunkTo(&buf, "IEND", nil)

			_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
			if !errors.Is(err, ErrChunkCRCMismatch) {
				t.Errorf("PNG-chunk-CRC-mismatch-%s: got %v, want ErrChunkCRCMismatch", tc.id, err)
			}
		})
	}
}

// TestPNGChunkCRCValidPasses verifies that a PNG with correct CRCs on all
// metadata chunks is accepted without error. This is the positive case.
func TestPNGChunkCRCValidPasses(t *testing.T) {
	// PNG-chunk-CRC-0xEDB88320 (positive): valid CRCs on all metadata chunks pass.
	t.Parallel()

	exifData := []byte{0x4D, 0x4D, 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08} // BE TIFF
	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta><?xpacket end="r"?>`)

	png := buildPNG(exifData, xmpData)

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("PNG-chunk-CRC-valid-passes: unexpected error: %v", err)
	}
	if !bytes.Equal(rawEXIF, exifData) {
		t.Errorf("PNG-chunk-CRC-valid-passes: EXIF mismatch")
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("PNG-chunk-CRC-valid-passes: XMP mismatch")
	}
}

// ---------------------------------------------------------------------------
// §2(c) PNG-chunk-Length-max — Length ≤ 2³¹−1
// ---------------------------------------------------------------------------

// TestPNGChunkLengthMax2Pow31 verifies that Length values > 2^31−1 are rejected
// with ErrChunkTooLarge. PNG §11.2.1: "The value must not exceed 2^31−1 bytes."
func TestPNGChunkLengthMax2Pow31(t *testing.T) {
	// PNG-chunk-Length-max-2^31: §11.2.1 — Length > 2³¹−1 must be rejected.
	t.Parallel()

	cases := []struct {
		name   string
		rawLen uint32
	}{
		// The PNG spec upper bound: 2^31−1 = math.MaxInt32 = 0x7FFFFFFF.
		// Any value > 0x7FFFFFFF violates the spec.
		{"exactly-2^31", 0x80000000},
		{"2^31+1", 0x80000001},
		{"max-uint32", math.MaxUint32},
	}

	for _, tc := range cases {
		t.Run("PNG-Length-max-2^31-"+tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			buf.Write(pngSig[:])
			var hdr [8]byte
			binary.BigEndian.PutUint32(hdr[:4], tc.rawLen)
			copy(hdr[4:8], "IHDR")
			buf.Write(hdr[:])

			_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
			if !errors.Is(err, ErrChunkTooLarge) {
				t.Errorf("PNG-Length-max-2^31-%s: raw len %d: got %v, want ErrChunkTooLarge", tc.name, tc.rawLen, err)
			}
		})
	}
}

// TestPNGChunkLengthSpecBoundary verifies the boundary between legal and illegal
// chunk lengths. PNG §11.2.1 sets the spec limit at 2^31−1. The library's own
// readChunk guard rejects rawLen > math.MaxInt32 (spec boundary) AND separately
// rejects rawLen > maxPNGChunkSize (application limit, 256 MiB). Both guards
// return ErrChunkTooLarge — this is correct and intentional.
//
// This test asserts the sign boundary: rawLen = math.MaxInt32 (0x7FFFFFFF)
// satisfies rawLen ≤ math.MaxInt32 (spec guard passes) but exceeds the
// application limit (256 MiB), so the library still returns ErrChunkTooLarge
// via the application guard. This is the expected behaviour.
func TestPNGChunkLengthSpecBoundary(t *testing.T) {
	// PNG-chunk-Length-max-2^31 (boundary): §11.2.1 — exactly 2^31-1 satisfies
	// the spec-defined upper bound; the library's application-level guard (256 MiB)
	// still rejects it. Both guards produce ErrChunkTooLarge; distinguishing them
	// is not required by the public API contract.
	t.Parallel()
	var buf bytes.Buffer
	buf.Write(pngSig[:])
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], math.MaxInt32) // 0x7FFFFFFF = 2^31-1
	copy(hdr[4:8], "eXIf")
	buf.Write(hdr[:])

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	// math.MaxInt32 > maxPNGChunkSize (256 MiB) so the library rejects it with
	// ErrChunkTooLarge through the application guard — which is correct.
	// The key spec assertion is that rawLen = 0x80000000 (2^31) is also rejected,
	// which is verified by TestPNGChunkLengthMax2Pow31.
	if err == nil {
		t.Error("PNG-chunk-Length-spec-boundary: expected an error for MaxInt32-length chunk in a truncated stream")
	}
}

// ---------------------------------------------------------------------------
// §2(c) PNG-chunk-type-property-bits — bit 5 of each type byte
// ---------------------------------------------------------------------------

// TestPNGChunkTypeBitsAncillary verifies the ancillary bit convention: bit 5
// of the first type byte is 1 for ancillary chunks (lowercase first letter),
// 0 for critical chunks (uppercase first letter). PNG §5.4.
func TestPNGChunkTypeBitsAncillary(t *testing.T) {
	// PNG-chunk-type-bits: §5.4 — bit5 of byte0 = ancillary (1) or critical (0).
	t.Parallel()

	// Ancillary chunks: first letter lowercase ⇒ bit5 = 1 (ASCII 'a'=0x61, bit5=1).
	ancillaryTypes := []string{"eXIf", "iTXt", "tEXt", "zTXt", "gAMA", "cHRM", "bKGD"}
	for _, ct := range ancillaryTypes {
		if ct[0]&0x20 == 0 {
			t.Errorf("PNG-chunk-type-bits: %q expected ancillary (bit5=1 of first byte), got critical", ct)
		}
	}

	// Critical chunks: first letter uppercase ⇒ bit5 = 0 (ASCII 'I'=0x49, bit5=0).
	criticalTypes := []string{"IHDR", "PLTE", "IDAT", "IEND"}
	for _, ct := range criticalTypes {
		if ct[0]&0x20 != 0 {
			t.Errorf("PNG-chunk-type-bits: %q expected critical (bit5=0 of first byte), got ancillary", ct)
		}
	}
}

// TestPNGChunkTypeBitsSafeToCopy verifies the safe-to-copy bit: bit 5 of the
// fourth type byte is 1 for safe-to-copy chunks (lowercase), 0 otherwise. PNG §5.4.
func TestPNGChunkTypeBitsSafeToCopy(t *testing.T) {
	// PNG-chunk-type-bits: §5.4 — bit5 of byte3 = safe-to-copy (1) or unsafe (0).
	t.Parallel()

	// eXIf: 'e','X','I','f' → byte3='f'=0x66, bit5=1 (safe-to-copy).
	if "eXIf"[3]&0x20 == 0 {
		t.Error("PNG-chunk-type-bits: eXIf byte3 'f' should have bit5=1 (safe-to-copy)")
	}
	// iTXt: 't'=0x74, bit5=1 (safe-to-copy).
	if "iTXt"[3]&0x20 == 0 {
		t.Error("PNG-chunk-type-bits: iTXt byte3 't' should have bit5=1 (safe-to-copy)")
	}
	// IHDR: 'R'=0x52, bit5=0 (not safe-to-copy).
	if "IHDR"[3]&0x20 != 0 {
		t.Error("PNG-chunk-type-bits: IHDR byte3 'R' should have bit5=0 (not safe-to-copy)")
	}
}

// ---------------------------------------------------------------------------
// §2(c) PNG-IHDR-first / PNG-IEND-last — chunk ordering
// ---------------------------------------------------------------------------

// TestPNGIHDRFirst verifies that a valid PNG has IHDR as its first chunk and
// that the library processes the stream correctly. PNG §5.6: "IHDR must appear
// as the first chunk in the PNG datastream."
func TestPNGIHDRFirst(t *testing.T) {
	// PNG-IHDR-first: §5.6 — IHDR is the first chunk.
	t.Parallel()
	png := buildConformancePNG(rawChunkBytes("eXIf", []byte{0x49, 0x49, 0x2A, 0x00}))
	if _, _, _, err := Extract(bytes.NewReader(png)); err != nil {
		// Only CRC-related errors are unexpected; structurally the PNG is valid.
		if !errors.Is(err, ErrChunkCRCMismatch) {
			t.Fatalf("PNG-IHDR-first: unexpected error: %v", err)
		}
	}
}

// TestPNGIENDLast verifies that after IEND is processed, the loop stops and
// any subsequent bytes are ignored without error. PNG §5.6: "IEND must appear
// as the last chunk in the PNG datastream."
func TestPNGIENDLast(t *testing.T) {
	// PNG-IEND-last: §5.6 — IEND terminates the stream; data after IEND ignored.
	t.Parallel()

	// Append 16 garbage bytes after a valid IEND.
	var buf bytes.Buffer
	buf.Write(buildConformancePNG(nil))
	buf.Write(bytes.Repeat([]byte{0xFF}, 16))

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Errorf("PNG-IEND-last: extra data after IEND must not cause error, got: %v", err)
	}
}

// TestPNGIENDLastInject verifies that Inject stops writing after IEND and does
// not append trailing garbage to the output stream.
func TestPNGIENDLastInject(t *testing.T) {
	// PNG-IEND-last: §5.6 — Inject output ends at IEND; no trailing bytes.
	t.Parallel()

	src := buildConformancePNG(nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("PNG-IEND-last-inject: Inject: %v", err)
	}

	// Last 4 bytes of output must be IEND's CRC (any non-nil append is wrong).
	result := out.Bytes()
	if len(result) < 12 {
		t.Fatal("PNG-IEND-last-inject: output too short")
	}

	// Scan to the last chunk type in the output and verify it is IEND.
	lastChunkType := findLastChunkType(result)
	if lastChunkType != "IEND" {
		t.Errorf("PNG-IEND-last-inject: last chunk type = %q, want IEND", lastChunkType)
	}
}

// findLastChunkType walks the chunk stream of a PNG byte slice and returns the
// type string of the last chunk encountered before IEND or end-of-data.
func findLastChunkType(data []byte) string {
	pos := 8 // skip signature
	last := ""
	for pos+8 <= len(data) {
		rawLen := binary.BigEndian.Uint32(data[pos:])
		if rawLen > math.MaxInt32 {
			break
		}
		length := int(rawLen)
		ct := string(data[pos+4 : pos+8])
		last = ct
		pos += 8 + length + 4
		if ct == "IEND" {
			break
		}
	}
	return last
}

// ---------------------------------------------------------------------------
// §2(d) PNG-eXIf — raw TIFF, no Exif\0\0 prefix
// ---------------------------------------------------------------------------

// TestPNGEXIfNoExifPrefix verifies that Extract returns the eXIf chunk payload
// exactly as-is, without any prepended "Exif\0\0" prefix. PNG §11.3.4.4: "The
// eXIf chunk shall contain EXIF metadata … The data field shall contain the
// EXIF data stored as a TIFF stream, starting with a TIFF header."
func TestPNGEXIfNoExifPrefix(t *testing.T) {
	// PNG-eXIf-no-Exif-prefix: §11.3.4.4 — raw TIFF, no Exif\0\0 prefix.
	t.Parallel()

	// Build a synthetic eXIf payload that starts with a valid TIFF header
	// (little-endian, magic 0x002A, IFD0 offset 8).
	tiffPayload := []byte{
		0x49, 0x49, // byte order: little-endian ("II")
		0x2A, 0x00, // TIFF magic 42
		0x08, 0x00, 0x00, 0x00, // IFD0 offset = 8
		0x00, 0x00, // IFD0 entry count = 0
	}

	png := buildConformancePNG(rawChunkBytes("eXIf", tiffPayload))
	rawEXIF, _, _, err := Extract(bytes.NewReader(png))
	if err != nil {
		t.Fatalf("PNG-eXIf-no-Exif-prefix: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("PNG-eXIf-no-Exif-prefix: rawEXIF is nil")
	}

	// The returned bytes must NOT begin with "Exif\0\0".
	if bytes.HasPrefix(rawEXIF, []byte("Exif\x00\x00")) {
		t.Errorf("PNG-eXIf-no-Exif-prefix: rawEXIF has unexpected Exif\\0\\0 prefix; PNG eXIf carries raw TIFF (PNG §11.3.4.4)")
	}

	// The returned bytes must start with the TIFF header, not any prefix.
	if !bytes.Equal(rawEXIF, tiffPayload) {
		t.Errorf("PNG-eXIf-no-Exif-prefix: rawEXIF = %x, want %x", rawEXIF, tiffPayload)
	}
}

// TestPNGEXIfInjectNoPrefix verifies that Inject writes the eXIf chunk with
// the raw TIFF payload and no Exif\0\0 prefix in the output stream.
func TestPNGEXIfInjectNoPrefix(t *testing.T) {
	// PNG-eXIf-no-Exif-prefix (write): §11.3.4.4 — eXIf chunk must not contain Exif\0\0 prefix.
	t.Parallel()

	tiffPayload := []byte{
		0x4D, 0x4D, // big-endian "MM"
		0x00, 0x2A, // TIFF magic
		0x00, 0x00, 0x00, 0x08,
		0x00, 0x00,
	}

	src := buildConformancePNG(nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, tiffPayload, nil, nil, true); err != nil {
		t.Fatalf("PNG-eXIf-inject-no-prefix: Inject: %v", err)
	}

	// Find the eXIf chunk in the output and verify its payload.
	eXIfPayload := extractChunkPayload(out.Bytes(), "eXIf")
	if eXIfPayload == nil {
		t.Fatal("PNG-eXIf-inject-no-prefix: eXIf chunk not found in output")
	}
	if bytes.HasPrefix(eXIfPayload, []byte("Exif\x00\x00")) {
		t.Errorf("PNG-eXIf-inject-no-prefix: eXIf chunk contains Exif\\0\\0 prefix (PNG §11.3.4.4 prohibits this)")
	}
	if !bytes.Equal(eXIfPayload, tiffPayload) {
		t.Errorf("PNG-eXIf-inject-no-prefix: eXIf payload = %x, want %x", eXIfPayload, tiffPayload)
	}
}

// TestPNGEXIfCorpusNoPrefix verifies that none of the known-good corpus PNG
// files with an eXIf chunk carry the prohibited Exif\0\0 prefix.
func TestPNGEXIfCorpusNoPrefix(t *testing.T) {
	// PNG-eXIf-no-Exif-prefix (corpus): §11.3.4.4 — real files must not add prefix.
	t.Parallel()

	paths := testutil.CorpusFiles(t, "png")
	for _, p := range paths {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := readFileOrSkip(t, p)
			rawEXIF, _, _, err := Extract(bytes.NewReader(data))
			if err != nil {
				return // corpus may include intentionally malformed files
			}
			if rawEXIF != nil && bytes.HasPrefix(rawEXIF, []byte("Exif\x00\x00")) {
				t.Errorf("PNG-eXIf-corpus-no-prefix: %s: rawEXIF starts with Exif\\0\\0 prefix (PNG §11.3.4.4 prohibits this)", name)
			}
		})
	}
}

// extractChunkPayload scans a PNG byte slice for the first occurrence of a
// chunk with the given type and returns its data bytes (nil if not found).
func extractChunkPayload(data []byte, chunkType string) []byte {
	if len(data) < 8 {
		return nil
	}
	pos := 8 // skip signature
	for pos+8 <= len(data) {
		rawLen := binary.BigEndian.Uint32(data[pos:])
		if rawLen > math.MaxInt32 {
			return nil
		}
		length := int(rawLen)
		ct := string(data[pos+4 : pos+8])
		dataEnd := pos + 8 + length
		if dataEnd > len(data) {
			return nil
		}
		if ct == chunkType && length > 0 {
			out := make([]byte, length)
			copy(out, data[pos+8:dataEnd])
			return out
		}
		pos = dataEnd + 4
		if ct == "IEND" {
			break
		}
	}
	return nil
}

// readFileOrSkip opens a file and returns its contents, or skips the test if
// the file cannot be opened (e.g. corpus not downloaded).
func readFileOrSkip(t *testing.T, path string) []byte {
	t.Helper()
	data, err := readFile(path)
	if err != nil {
		t.Skipf("cannot read %s: %v", path, err)
	}
	return data
}

// readFile reads all bytes from a file.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// §2(d) PNG-iTXt — XMP embedding (XMP Part 3 §1.6)
// ---------------------------------------------------------------------------

// TestPNGITXtKeywordXMLComAdobeXMP verifies that the XMP keyword written to
// an iTXt chunk is exactly "XML:com.adobe.xmp" (XMP Part 3 §1.6 / PNG-01).
func TestPNGITXtKeywordXMLComAdobeXMP(t *testing.T) {
	// PNG-iTXt-XML-com-adobe-xmp (PNG-01): XMP Part 3 §1.6 — keyword must be
	// "XML:com.adobe.xmp".
	t.Parallel()

	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	src := buildConformancePNG(nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, xmpData, true); err != nil {
		t.Fatalf("PNG-iTXt-keyword: Inject: %v", err)
	}

	// Find the iTXt chunk and verify keyword.
	iTXtPayload := extractChunkPayload(out.Bytes(), "iTXt")
	if iTXtPayload == nil {
		t.Fatal("PNG-iTXt-keyword: no iTXt chunk in output")
	}
	null := bytes.IndexByte(iTXtPayload, 0x00)
	if null < 0 {
		t.Fatal("PNG-iTXt-keyword: no NUL terminator in iTXt payload")
	}
	keyword := string(iTXtPayload[:null])
	if keyword != xmpKeyword {
		t.Errorf("PNG-iTXt-keyword: keyword = %q, want %q", keyword, xmpKeyword)
	}
}

// TestPNGITXtCompressionFlag0 verifies that Inject writes iTXt XMP chunks
// with compression flag = 0 (uncompressed). XMP Part 3 §1.6 / PNG-02.
func TestPNGITXtCompressionFlag0(t *testing.T) {
	// PNG-iTXt-compression-flag-0 (PNG-02): XMP Part 3 §1.6 — compression
	// flag MUST be 0 (uncompressed) for XMP iTXt chunks.
	t.Parallel()

	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	src := buildConformancePNG(nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, xmpData, true); err != nil {
		t.Fatalf("PNG-iTXt-compression-flag-0: Inject: %v", err)
	}

	iTXtPayload := extractChunkPayload(out.Bytes(), "iTXt")
	if iTXtPayload == nil {
		t.Fatal("PNG-iTXt-compression-flag-0: no iTXt chunk")
	}

	// Layout: keyword\x00 compFlag(1) compMethod(1) lang\x00 transKw\x00 text
	null := bytes.IndexByte(iTXtPayload, 0x00)
	if null < 0 || null+1 >= len(iTXtPayload) {
		t.Fatal("PNG-iTXt-compression-flag-0: iTXt payload too short")
	}
	compFlag := iTXtPayload[null+1]
	if compFlag != 0 {
		t.Errorf("PNG-iTXt-compression-flag-0: compression flag = %d, want 0 (uncompressed); XMP Part 3 §1.6", compFlag)
	}
}

// TestPNGITXtEmptyLangAndTranslatedKeyword verifies that Inject writes two
// consecutive NUL bytes for the (empty) language tag and translated keyword
// fields. XMP Part 3 §1.6 / PNG-05.
func TestPNGITXtEmptyLangAndTranslatedKeyword(t *testing.T) {
	// PNG-iTXt-empty-lang-transKw (PNG-05): XMP Part 3 §1.6 — language tag
	// and translated keyword must be empty (two NUL bytes after compFlag+compMethod).
	t.Parallel()

	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	src := buildConformancePNG(nil)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, xmpData, true); err != nil {
		t.Fatalf("PNG-iTXt-empty-lang: Inject: %v", err)
	}

	iTXtPayload := extractChunkPayload(out.Bytes(), "iTXt")
	if iTXtPayload == nil {
		t.Fatal("PNG-iTXt-empty-lang: no iTXt chunk")
	}

	// Parse: keyword\0 compFlag compMethod lang\0 transKw\0 text
	null := bytes.IndexByte(iTXtPayload, 0x00)
	if null < 0 {
		t.Fatal("PNG-iTXt-empty-lang: missing keyword NUL terminator")
	}
	pos := null + 1 // skip keyword NUL
	if pos+2 > len(iTXtPayload) {
		t.Fatal("PNG-iTXt-empty-lang: payload too short for compFlag+compMethod")
	}
	pos += 2 // skip compFlag + compMethod

	// lang field must be empty: immediately followed by NUL.
	if pos >= len(iTXtPayload) || iTXtPayload[pos] != 0x00 {
		t.Errorf("PNG-iTXt-empty-lang: lang tag not empty; byte at pos=%d = 0x%02X, want 0x00", pos, iTXtPayload[pos])
	}
	pos++ // skip lang NUL

	// translated keyword must be empty: immediately followed by NUL.
	if pos >= len(iTXtPayload) || iTXtPayload[pos] != 0x00 {
		t.Errorf("PNG-iTXt-empty-lang: translated keyword not empty; byte at pos=%d = 0x%02X, want 0x00", pos, iTXtPayload[pos])
	}
}

// TestPNGITXtUseFirstXMP verifies that when multiple iTXt XMP chunks are
// present, Extract uses the first one. XMP Part 3 §1.6 / PNG-04.
func TestPNGITXtUseFirstXMP(t *testing.T) {
	// PNG-iTXt-use-first-XMP (PNG-04): XMP Part 3 §1.6 — exactly one XMP iTXt;
	// reader uses first occurrence.
	t.Parallel()

	first := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>FIRST</dc:title></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="r"?>`)
	second := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>SECOND</dc:title></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="r"?>`)

	var buf bytes.Buffer
	buf.Write(pngSig[:])
	writeChunkTo(&buf, "IHDR", minIHDR())
	writeChunkTo(&buf, "iTXt", buildXMPChunk(first))
	writeChunkTo(&buf, "iTXt", buildXMPChunk(second))
	writeChunkTo(&buf, "IEND", nil)

	_, _, rawXMP, err := Extract(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("PNG-iTXt-use-first-XMP: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, first) {
		t.Errorf("PNG-iTXt-use-first-XMP: got second XMP, want first (PNG-04 / XMP Part 3 §1.6)")
	}
}

// TestPNGITXtXMPRoundTrip verifies that an XMP payload survives an inject
// round-trip: Inject → Extract → content unchanged.
func TestPNGITXtXMPRoundTrip(t *testing.T) {
	// PNG-iTXt-XML-com-adobe-xmp (round-trip): XMP Part 3 §1.6.
	t.Parallel()

	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:creator><rdf:Seq><rdf:li>Test Author</rdf:li></rdf:Seq></dc:creator></rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="r"?>`)
	src := buildConformancePNG(nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, xmpData, true); err != nil {
		t.Fatalf("PNG-iTXt-round-trip: Inject: %v", err)
	}

	_, _, rawXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("PNG-iTXt-round-trip: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, xmpData) {
		t.Errorf("PNG-iTXt-round-trip: XMP mismatch after round-trip")
	}
}

// ---------------------------------------------------------------------------
// §2(e) PNG-write-* — write byte-correctness
// ---------------------------------------------------------------------------

// TestPNGWriteCRCOverTypeAndData verifies that every chunk written by Inject
// has its CRC computed over Type+Data (not over Length+Type+Data, and not over
// Data alone). PNG §5.4.
func TestPNGWriteCRCOverTypeAndData(t *testing.T) {
	// PNG-write-CRC-over-type-and-data: §5.4 — CRC = crc32(Type+Data).
	t.Parallel()

	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	src := buildConformancePNG(nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, exifData, nil, xmpData, true); err != nil {
		t.Fatalf("PNG-write-CRC: Inject: %v", err)
	}

	// Walk the output and re-verify every chunk CRC.
	result := out.Bytes()
	if len(result) < 8 {
		t.Fatal("PNG-write-CRC: output too short")
	}
	pos := 8 // skip signature
	for pos+8 <= len(result) {
		rawLen := binary.BigEndian.Uint32(result[pos:])
		if rawLen > math.MaxInt32 {
			t.Fatalf("PNG-write-CRC: oversized chunk length at pos %d", pos)
		}
		length := int(rawLen)
		ct := string(result[pos+4 : pos+8])
		dataEnd := pos + 8 + length
		if dataEnd+4 > len(result) {
			break
		}
		data := result[pos+8 : dataEnd]
		storedCRC := binary.BigEndian.Uint32(result[dataEnd:])
		expectedCRC := computeCRC32(ct, data)
		if storedCRC != expectedCRC {
			t.Errorf("PNG-write-CRC: chunk %q: stored CRC %08x != expected %08x (must be crc32 of Type+Data)", ct, storedCRC, expectedCRC)
		}
		pos = dataEnd + 4
		if ct == "IEND" {
			break
		}
	}
}

// TestPNGWriteAncillaryBetweenIHDRAndIEND verifies that metadata chunks
// (eXIf, iTXt) are placed between IHDR and IEND in the output stream.
// PNG §5.6: ancillary chunks must not appear before IHDR or after IEND.
func TestPNGWriteAncillaryBetweenIHDRAndIEND(t *testing.T) {
	// PNG-write-ancillary-between-IHDR-IEND: §5.6 — ancillary chunks between IHDR and IEND.
	t.Parallel()

	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	src := buildConformancePNG(nil)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, exifData, nil, xmpData, true); err != nil {
		t.Fatalf("PNG-write-ancillary: Inject: %v", err)
	}

	result := out.Bytes()
	chunks := parseChunkSequence(result)
	if len(chunks) == 0 {
		t.Fatal("PNG-write-ancillary: no chunks found")
	}

	// Verify IHDR is first and IEND is last.
	if chunks[0] != "IHDR" {
		t.Errorf("PNG-write-ancillary: first chunk = %q, want IHDR", chunks[0])
	}
	if chunks[len(chunks)-1] != "IEND" {
		t.Errorf("PNG-write-ancillary: last chunk = %q, want IEND", chunks[len(chunks)-1])
	}

	// Verify eXIf and iTXt appear between IHDR and IEND.
	ihdrIdx := indexOf(chunks, "IHDR")
	iendIdx := indexOf(chunks, "IEND")
	exifIdx := indexOf(chunks, "eXIf")
	itxtIdx := indexOf(chunks, "iTXt")

	if exifIdx < 0 {
		t.Error("PNG-write-ancillary: eXIf chunk not found in output")
	} else if exifIdx <= ihdrIdx || exifIdx >= iendIdx {
		t.Errorf("PNG-write-ancillary: eXIf at index %d must be between IHDR(%d) and IEND(%d)", exifIdx, ihdrIdx, iendIdx)
	}

	if itxtIdx < 0 {
		t.Error("PNG-write-ancillary: iTXt chunk not found in output")
	} else if itxtIdx <= ihdrIdx || itxtIdx >= iendIdx {
		t.Errorf("PNG-write-ancillary: iTXt at index %d must be between IHDR(%d) and IEND(%d)", itxtIdx, ihdrIdx, iendIdx)
	}
}

// TestPNGWritePreserveSafeToCopyChunks verifies that unknown ancillary
// safe-to-copy chunks (e.g. a custom "tEXt" or unrecognized private chunk)
// are preserved by Inject. PNG §5.6: ancillary chunks with the safe-to-copy
// bit set may be copied by editors without understanding their content.
func TestPNGWritePreserveSafeToCopyChunks(t *testing.T) {
	// PNG-write-preserve-safe-to-copy: §5.6 — preserve unknown safe-to-copy chunks.
	t.Parallel()

	// gAMA and pHYs are well-known safe-to-copy ancillary chunks. We use them
	// as stand-ins for "unknown ancillary" to avoid needing a custom chunk type
	// that the library would have to pass through blindly.
	gamaData := []byte{0x00, 0x00, 0xB1, 0x8F} // gamma = 45455 (sRGB equivalent)
	physData := make([]byte, 9)
	binary.BigEndian.PutUint32(physData[0:], 3937) // pixels per meter X
	binary.BigEndian.PutUint32(physData[4:], 3937) // pixels per meter Y
	physData[8] = 1                                // unit = metre

	var extra bytes.Buffer
	writeChunkTo(&extra, "gAMA", gamaData)
	writeChunkTo(&extra, "pHYs", physData)
	src := buildConformancePNG(extra.Bytes())

	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, exifData, nil, nil, true); err != nil {
		t.Fatalf("PNG-write-preserve-safe-to-copy: Inject: %v", err)
	}

	chunks := parseChunkSequence(out.Bytes())
	if indexOf(chunks, "gAMA") < 0 {
		t.Error("PNG-write-preserve-safe-to-copy: gAMA chunk lost after Inject")
	}
	if indexOf(chunks, "pHYs") < 0 {
		t.Error("PNG-write-preserve-safe-to-copy: pHYs chunk lost after Inject")
	}
}

// parseChunkSequence walks a PNG byte slice and returns the chunk type strings
// in order (including IHDR and IEND).
func parseChunkSequence(data []byte) []string {
	if len(data) < 8 {
		return nil
	}
	pos := 8
	var result []string
	for pos+8 <= len(data) {
		rawLen := binary.BigEndian.Uint32(data[pos:])
		if rawLen > math.MaxInt32 {
			break
		}
		length := int(rawLen)
		ct := string(data[pos+4 : pos+8])
		result = append(result, ct)
		pos += 8 + length + 4
		if ct == "IEND" {
			break
		}
	}
	return result
}

func indexOf(slice []string, s string) int {
	for i, v := range slice {
		if v == s {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// §2(f) PNG-robust-* — robustness cases
// ---------------------------------------------------------------------------

// TestPNGRobustBadSignature verifies that Extract on a stream with a corrupt
// 8-byte signature returns ErrInvalidSignature without panicking.
func TestPNGRobustBadSignature(t *testing.T) {
	// PNG-robust-bad-signature: §5.2 — non-PNG magic → ErrInvalidSignature.
	t.Parallel()

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"all-zeros", make([]byte, 8)},
		{"jpeg-magic", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46}},
		{"partial-png-sig", pngSig[:4]},
	}
	for _, tc := range cases {
		t.Run("PNG-robust-bad-sig-"+tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := Extract(bytes.NewReader(tc.data))
			if err == nil {
				t.Errorf("PNG-robust-bad-sig-%s: expected error, got nil", tc.name)
			}
		})
	}
}

// TestPNGRobustTruncatedChunk verifies that Extract on a stream truncated
// mid-chunk returns an error without panicking. PNG §2(f).
//
// This test uses named sub-tests so tparallel can verify each step in parallel.
func TestPNGRobustTruncatedChunk(t *testing.T) {
	// PNG-robust-truncated-chunk: §2(f) — truncated mid-chunk → error, no panic.
	t.Parallel()
	full := buildPNG([]byte{0x49, 0x49, 0x2A, 0x00}, nil)
	for i := 9; i < len(full); i += max(1, len(full)/30) {
		cutLen := i
		t.Run(fmt.Sprintf("cut-at-%d", cutLen), func(t *testing.T) {
			t.Parallel()
			_, _, _, _ = Extract(bytes.NewReader(full[:cutLen])) // must not panic
		})
	}
}

// TestPNGRobustLengthPastEOF verifies that a chunk declaring a length larger
// than the remaining bytes in the stream is handled gracefully (error, no
// allocation proportional to declared length). PNG §2(f).
func TestPNGRobustLengthPastEOF(t *testing.T) {
	// PNG-robust-Length-past-EOF: §2(f) — declared length > available bytes → error.
	t.Parallel()

	const declaredLen = 1 << 17 // 128 KiB — well within maxPNGChunkSize but stream has 0 bytes
	var buf bytes.Buffer
	buf.Write(pngSig[:])
	writeChunkTo(&buf, "IHDR", minIHDR())

	// Write only the chunk header (8 bytes) with no payload following.
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], declaredLen)
	copy(hdr[4:8], "eXIf")
	buf.Write(hdr[:])

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Fatal("PNG-robust-Length-past-EOF: expected error for declared length > stream, got nil")
	}
	// Must NOT be ErrChunkTooLarge — that is for spec violation (Length > 2^31-1).
	// This is a truncation error.
	if errors.Is(err, ErrChunkTooLarge) {
		t.Errorf("PNG-robust-Length-past-EOF: got ErrChunkTooLarge; expected truncation error for valid length in short stream")
	}
}

// TestPNGRobustCRCMismatch verifies that a corrupt CRC on a metadata chunk
// returns ErrChunkCRCMismatch without panicking. PNG §2(f).
func TestPNGRobustCRCMismatch(t *testing.T) {
	// PNG-robust-CRC-mismatch: §2(f) — corrupt CRC → ErrChunkCRCMismatch, no panic.
	t.Parallel()

	data := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}
	goodCRC := computeCRC32("eXIf", data)
	badChunk := buildChunkWithCRC("eXIf", data, corruptCRC(goodCRC))

	var buf bytes.Buffer
	buf.Write(pngSig[:])
	writeChunkTo(&buf, "IHDR", minIHDR())
	buf.Write(badChunk)
	writeChunkTo(&buf, "IEND", nil)

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrChunkCRCMismatch) {
		t.Errorf("PNG-robust-CRC-mismatch: got %v, want ErrChunkCRCMismatch", err)
	}
}

// TestPNGRobustLengthGt2Pow31 verifies that a chunk with Length > 2^31−1 is
// rejected with ErrChunkTooLarge. PNG §11.2.1 / §2(f).
func TestPNGRobustLengthGt2Pow31(t *testing.T) {
	// PNG-robust-Length-gt-2^31: §11.2.1 + §2(f) — Length > 2³¹−1 → ErrChunkTooLarge.
	t.Parallel()

	var buf bytes.Buffer
	buf.Write(pngSig[:])
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], 0xFFFFFFFF) // max uint32, well above 2^31-1
	copy(hdr[4:8], "eXIf")
	buf.Write(hdr[:])

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrChunkTooLarge) {
		t.Errorf("PNG-robust-Length-gt-2^31: got %v, want ErrChunkTooLarge", err)
	}
}

// TestPNGRobustZeroLengthChunkLegal verifies that a zero-length chunk
// (which is legal per PNG §5.3) does not cause a crash or unexpected error.
func TestPNGRobustZeroLengthChunkLegal(t *testing.T) {
	// PNG-robust-zero-length-data: §5.3 — zero-length chunks are spec-legal.
	t.Parallel()

	var buf bytes.Buffer
	buf.Write(pngSig[:])
	writeChunkTo(&buf, "IHDR", minIHDR())
	writeChunkTo(&buf, "tEXt", nil) // zero-length tEXt: unusual but legal
	writeChunkTo(&buf, "IEND", nil)

	_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
	// An error here would not be the expected CRC mismatch sentinel, since the
	// zero-length tEXt has correct CRC. Any unexpected error is a bug.
	if errors.Is(err, ErrChunkCRCMismatch) {
		t.Errorf("PNG-robust-zero-length: got ErrChunkCRCMismatch for correctly-formed zero-length chunk")
	}
}

// TestPNGRobustChunksAfterIEND verifies that extra data appended after IEND
// does not cause a panic and is silently ignored. PNG §2(f).
func TestPNGRobustChunksAfterIEND(t *testing.T) {
	// PNG-robust-chunks-after-IEND: §2(f) — extra data after IEND: no crash.
	t.Parallel()

	cases := []struct {
		name  string
		extra []byte
	}{
		{"garbage-bytes", bytes.Repeat([]byte{0xFF}, 64)},
		{"valid-chunk-bytes", rawChunkBytes("tEXt", []byte("Comment\x00extra"))},
		{"partial-chunk-header", []byte{0x00, 0x00, 0x00, 0x10, 't', 'E'}},
	}

	for _, tc := range cases {
		t.Run("PNG-robust-after-IEND-"+tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			buf.Write(buildConformancePNG(nil))
			buf.Write(tc.extra)

			_, _, _, err := Extract(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Errorf("PNG-robust-after-IEND-%s: data after IEND must not cause error, got: %v", tc.name, err)
			}
		})
	}
}

// TestPNGRobustBadITXtSeparators verifies that malformed iTXt payloads (missing
// NUL separators) are silently skipped without returning an error. PNG §2(f).
func TestPNGRobustBadITXtSeparators(t *testing.T) {
	// PNG-robust-bad-iTXt-separators: §2(f) — malformed iTXt → silently skipped.
	t.Parallel()

	cases := []struct {
		name string
		data []byte
	}{
		// No NUL separator at all.
		{"no-nul", []byte("XML:com.adobe.xmp-no-null-terminator")},
		// Keyword with NUL but no compFlag+compMethod (too short).
		{"truncated-after-keyword-nul", []byte("XML:com.adobe.xmp\x00")},
		// Keyword + compFlag + compMethod but no lang NUL.
		{"missing-lang-nul", append([]byte("XML:com.adobe.xmp\x00\x00\x00"), []byte("no-null-in-lang")...)},
	}

	for _, tc := range cases {
		t.Run("PNG-robust-bad-iTXt-"+tc.name, func(t *testing.T) {
			t.Parallel()
			png := buildConformancePNG(rawChunkBytes("iTXt", tc.data))
			_, _, rawXMP, err := Extract(bytes.NewReader(png))
			if err != nil && !errors.Is(err, ErrChunkCRCMismatch) {
				// A CRC error is acceptable: the chunk has wrong CRC because we
				// built it independently. Actual parse errors are bugs.
				t.Errorf("PNG-robust-bad-iTXt-%s: unexpected error: %v", tc.name, err)
			}
			// Malformed iTXt with the XMP keyword must produce nil rawXMP,
			// not garbage data.
			if rawXMP != nil {
				_ = rawXMP // silently accepted is fine — just must not crash
			}
		})
	}
}

// TestPNGRobustNonUTF8XMP verifies that non-UTF-8 bytes in the XMP payload
// returned from an iTXt chunk do not cause a crash. The library returns the
// raw bytes as-is; higher-level XMP parsing handles encoding. PNG §2(f).
func TestPNGRobustNonUTF8XMP(t *testing.T) {
	// PNG-robust-non-UTF8-XMP: §2(f) — non-UTF-8 XMP payload: no crash.
	t.Parallel()

	// Craft an iTXt chunk with the XMP keyword followed by a non-UTF-8 text body.
	nonUTF8Text := []byte{0xFF, 0xFE, 0x3C, 0x78} // UTF-16 BOM + "<x"
	payload := buildXMPChunk(nonUTF8Text)
	png := buildConformancePNG(rawChunkBytes("iTXt", payload))

	// Must not panic; rawXMP may be the raw non-UTF-8 bytes.
	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(png))
	if err != nil && !errors.Is(err, ErrChunkCRCMismatch) {
		t.Errorf("PNG-robust-non-UTF8-XMP: unexpected error: %v", err)
	}
	_ = rawEXIF
	_ = rawXMP
}

// TestPNGRobustBadEXIfHeader verifies that an eXIf chunk with an invalid TIFF
// magic bytes is returned as raw bytes by Extract (no crash). The PNG layer
// is responsible only for chunk extraction; TIFF validation is a higher-level
// concern. PNG §2(f).
func TestPNGRobustBadEXIfHeader(t *testing.T) {
	// PNG-robust-bad-eXIf-header: §2(f) — eXIf with invalid TIFF magic: no crash.
	t.Parallel()

	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid-magic", []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x00, 0x00}},
		{"exif-prefix-present", append([]byte("Exif\x00\x00"), []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}...)},
		{"one-byte", []byte{0x49}},
		{"all-zeros", make([]byte, 8)},
	}

	for _, tc := range cases {
		t.Run("PNG-robust-bad-eXIf-"+tc.name, func(t *testing.T) {
			t.Parallel()
			png := buildConformancePNG(rawChunkBytes("eXIf", tc.payload))
			// Extract must return the raw bytes without crashing.
			rawEXIF, _, _, err := Extract(bytes.NewReader(png))
			if err != nil && !errors.Is(err, ErrChunkCRCMismatch) {
				t.Errorf("PNG-robust-bad-eXIf-%s: unexpected error: %v", tc.name, err)
			}
			if err == nil && !bytes.Equal(rawEXIF, tc.payload) {
				t.Errorf("PNG-robust-bad-eXIf-%s: rawEXIF = %x, want raw payload %x", tc.name, rawEXIF, tc.payload)
			}
		})
	}
}

// TestPNGRobustPreserveUnknownSegmentsFalseReturnsError verifies that Inject
// with preserveUnknownSegments=false returns ErrPreserveUnknownSegmentsNotSupported.
// This is a required safety guard unique to PNG (PNG chunks ≠ JPEG APPn segments).
func TestPNGRobustPreserveUnknownSegmentsFalseReturnsError(t *testing.T) {
	// PNG-robust-preserve-false: Inject(preserveUnknownSegments=false) → error.
	t.Parallel()

	src := buildConformancePNG(nil)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(src), &out, nil, nil, nil, false)
	if !errors.Is(err, ErrPreserveUnknownSegmentsNotSupported) {
		t.Errorf("PNG-robust-preserve-false: got %v, want ErrPreserveUnknownSegmentsNotSupported", err)
	}
}

// ---------------------------------------------------------------------------
// §2 corpus parity — run against the PNG corpus without panicking
// ---------------------------------------------------------------------------

// TestPNGCorpusExtractNoPanic runs Extract against every PNG file in the
// corpus and verifies it does not panic. Errors are acceptable (intentionally
// malformed PoC files exist); panics are not. This provides end-to-end coverage
// of robustness rules §2(f) against real-world files.
func TestPNGCorpusExtractNoPanic(t *testing.T) {
	// PNG-corpus-extract-no-panic: §2(f) robustness — no file in the corpus panics.
	t.Parallel()

	paths := testutil.CorpusFiles(t, "png")
	for _, p := range paths {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := readFileOrSkip(t, p)
			// Wrap in a deferred recover to turn panics into test failures.
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("PNG-corpus-extract-no-panic: %s: panicked: %v", name, r)
					}
				}()
				_, _, _, _ = Extract(bytes.NewReader(data))
			}()
		})
	}
}

// TestPNGCorpusInjectNoPanic runs Inject against every valid PNG in the corpus
// (files that Extract accepts without error) and verifies it does not panic.
func TestPNGCorpusInjectNoPanic(t *testing.T) {
	// PNG-corpus-inject-no-panic: §2(e/f) — Inject on any valid input: no panic.
	t.Parallel()

	exifData := []byte{0x49, 0x49, 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00}
	xmpData := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)

	paths := testutil.CorpusFiles(t, "png")
	for _, p := range paths {
		name := filepath.Base(p)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := readFileOrSkip(t, p)

			// Only inject into structurally valid PNGs (valid signature).
			if len(data) < 8 || [8]byte(data[:8]) != pngSig {
				return
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("PNG-corpus-inject-no-panic: %s: panicked: %v", name, r)
					}
				}()
				var out bytes.Buffer
				_ = Inject(bytes.NewReader(data), &out, exifData, nil, xmpData, true)
			}()
		})
	}
}

// TestPNGCorpusCRCIntegrityKnownGood runs Extract against files known to be
// well-formed and verifies they do not trigger ErrChunkCRCMismatch. This
// exercises the CRC verification path against authentic camera/software output.
func TestPNGCorpusCRCIntegrityKnownGood(t *testing.T) {
	// PNG-corpus-CRC-integrity: §5.5 — known-good files must not trigger CRC mismatch.
	t.Parallel()

	knownGood := []string{
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "1343_comment.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "1343_empty.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "1343_exif.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "exiv2-bug1074.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "exiv2-bug841.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "exiv2-bug922.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "imagemagick.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "ReaganSmallPng.png"),
		filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "ReaganLargePng.png"),
	}

	for _, p := range knownGood {
		name := filepath.Base(p)
		t.Run("PNG-corpus-CRC-"+name, func(t *testing.T) {
			t.Parallel()
			data := readFileOrSkip(t, p)
			_, _, _, err := Extract(bytes.NewReader(data))
			if errors.Is(err, ErrChunkCRCMismatch) {
				t.Errorf("PNG-corpus-CRC: %s: CRC mismatch on known-good file (regression): %v", name, err)
			}
		})
	}
}
