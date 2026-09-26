package png

// task288_test.go — regression battery for task #288 (Sprint 44, Batch F):
// Extract skips chunks it does not interpret via a bounds-checked Seek
// instead of reading and discarding them; Inject copies every chunk it does
// not modify through byte-identical, including the chunk's ORIGINAL CRC
// trailer — never recomputing or "repairing" it (USER DECISION, matching
// ExifTool/Exiv2's documented behaviour).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// buildTruncatedIgnoredChunkPNG builds a PNG whose only content after the
// signature is a single ignored (non-metadata) chunk — chunkType/data — that
// declares dataLen bytes of payload but is truncated after tail bytes have
// been written, omitting the rest of the payload and the mandatory 4-byte
// CRC trailer entirely. This mirrors testdata/corpus/png/exiv2/
// issue_790_poc2.png's exact shape: an "iCCP" chunk claiming length 10, with
// the file ending immediately after the 10th data byte and zero bytes left
// for the CRC.
func buildTruncatedIgnoredChunkPNG(chunkType string, dataLen int, tail []byte) []byte {
	var buf bytes.Buffer
	buf.Write(pngSig[:])
	var lbuf [4]byte
	binary.BigEndian.PutUint32(lbuf[:], uint32(dataLen)) //nolint:gosec // G115: test helper, small constant
	buf.Write(lbuf[:])
	buf.WriteString(chunkType)
	buf.Write(tail)
	return buf.Bytes()
}

// TestExtractSkipsIgnoredChunksBoundsChecked proves the general form of the
// #288 regression the corpus file issue_790_poc2.png exercises: a chunk type
// Extract ignores (skips via Seek, never reads) whose declared length
// overruns the actual remaining stream must still be rejected as truncated,
// not silently tolerated because Seek succeeds past EOF on most
// io.ReadSeeker implementations.
func TestExtractSkipsIgnoredChunksBoundsChecked(t *testing.T) {
	t.Parallel()

	// "iCCP" is never inspected by Extract (extractIgnores returns true for
	// it), so this chunk is skipped, not read — exactly issue_790_poc2.png's
	// shape: 10 bytes declared, only 10 bytes present, and the mandatory
	// 4-byte CRC trailer entirely missing.
	data := buildTruncatedIgnoredChunkPNG("iCCP", 10, []byte("kevwozere\x00"))

	_, _, _, err := Extract(bytes.NewReader(data))
	if err == nil {
		t.Fatal("Extract: expected a truncation error for an ignored chunk whose declared length overruns EOF, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Extract: expected an error wrapping io.ErrUnexpectedEOF, got: %v", err)
	}
}

// TestExtractIssue790Poc2Rejected pins the exact corpus fixture named in the
// #288 acceptance criteria: Extract must reject it (any error), not silently
// return a successful, empty result. Before #288's bounds-checked skip, this
// exact file was silently ACCEPTED (err == nil) because the truncated
// "iCCP" chunk's CRC-trailer read returned a bare io.EOF that Extract's loop
// could not distinguish from a clean end of stream.
func TestExtractIssue790Poc2Rejected(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "testdata", "corpus", "png", "exiv2", "issue_790_poc2.png")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("cannot open %s: %v", path, err)
	}

	_, _, _, err = Extract(bytes.NewReader(data))
	if err == nil {
		t.Fatal("Extract(issue_790_poc2.png): expected a rejection error, got nil (truncation regression)")
	}
	t.Logf("Extract(issue_790_poc2.png) correctly rejected: %v", err)
}

// TestInjectSkipsTruncatedEXIfBoundsChecked mirrors
// TestExtractSkipsIgnoredChunksBoundsChecked for Inject's own eXIf-skip path
// (#288): a truncated eXIf chunk (the one type Inject always drops without
// reading) must still be rejected, not silently tolerated by an unchecked
// Seek past EOF.
func TestInjectSkipsTruncatedEXIfBoundsChecked(t *testing.T) {
	t.Parallel()

	data := buildTruncatedIgnoredChunkPNG("eXIf", 10, []byte("kevwozere\x00"))

	var out bytes.Buffer
	err := Inject(bytes.NewReader(data), &out, []byte("new-exif"), nil, nil, true)
	if err == nil {
		t.Fatal("Inject: expected a truncation error for a truncated eXIf chunk, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Inject: expected an error wrapping io.ErrUnexpectedEOF, got: %v", err)
	}
}

// TestInjectPreservesOriginalCRCEvenWhenInvalid is the #288 USER DECISION
// regression gate: a chunk Inject copies through unchanged must retain its
// ORIGINAL CRC trailer byte-for-byte, even when that CRC does not match the
// chunk's data. Inject must never "repair" it — matching ExifTool/Exiv2's
// documented behaviour of leaving foreign chunks exactly as found.
func TestInjectPreservesOriginalCRCEvenWhenInvalid(t *testing.T) {
	t.Parallel()

	textData := []byte("Comment\x00hello world")
	src := buildPNGWithBadCRC("tEXt", textData)

	// Locate the exact 8(header)+len(data)+4(bad CRC) byte span for the tEXt
	// chunk within src, so the assertion below can compare it byte-for-byte
	// against the corresponding span in Inject's output.
	tEXtSpan := src[8+13+12 : 8+13+12+8+len(textData)+4] // after IHDR(8+13+12 bytes), before IEND

	var out bytes.Buffer
	// No metadata changes requested: rawEXIF/rawXMP both nil, so the only
	// chunks written are whatever the source PNG already had, copied through.
	if err := Inject(bytes.NewReader(src), &out, nil, nil, nil, true); err != nil {
		t.Fatalf("Inject: unexpected error: %v", err)
	}

	if !bytes.Contains(out.Bytes(), tEXtSpan) {
		t.Errorf("Inject output does not contain the tEXt chunk's original bytes (header+data+CRC) unchanged.\n"+
			"expected span: % x\noutput:        % x", tEXtSpan, out.Bytes())
	}

	// Sanity: prove the CRC really is invalid, so this test is not vacuously
	// passing because the "bad" CRC happened to be valid.
	storedCRC := binary.BigEndian.Uint32(tEXtSpan[len(tEXtSpan)-4:])
	if err := verifyCRC32([4]byte{'t', 'E', 'X', 't'}, textData, storedCRC); err == nil {
		t.Fatal("test setup error: buildPNGWithBadCRC produced a chunk whose CRC is actually valid")
	}
}

// TestInjectPreservesOriginalCRCOnUnchangedIHDR proves the same "never
// repair" policy applies to IHDR: Inject copies its bytes through unchanged
// (it is not a chunk the library modifies) and must keep whatever CRC the
// source file carried, valid or not.
func TestInjectPreservesOriginalCRCOnUnchangedIHDR(t *testing.T) {
	t.Parallel()

	ihdrData := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdrData[0:], 1)
	binary.BigEndian.PutUint32(ihdrData[4:], 1)
	ihdrData[8] = 8
	ihdrData[9] = 2
	src := buildPNGWithBadCRC("IHDR", ihdrData)
	ihdrSpan := src[8 : 8+8+len(ihdrData)+4]

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, []byte("exif-data"), nil, nil, true); err != nil {
		t.Fatalf("Inject: unexpected error: %v", err)
	}

	if !bytes.Contains(out.Bytes(), ihdrSpan) {
		t.Errorf("Inject output does not contain IHDR's original bytes (including its invalid CRC) unchanged.\n"+
			"expected span: % x", ihdrSpan)
	}
}

// TestInjectNonXMPITXtPreservesOriginalCRC proves that an iTXt chunk Inject
// inspects (to decide whether it carries XMP) but decides to KEEP — because
// its keyword is not "XML:com.adobe.xmp" — is written back byte-identical,
// original CRC included, exercising the readChunkBody/writeVerbatimChunk
// path distinct from copyChunkVerbatim's streaming path.
func TestInjectNonXMPITXtPreservesOriginalCRC(t *testing.T) {
	t.Parallel()

	// iTXt payload for a "Comment" keyword (not XMP): keyword\x00 + compFlag +
	// compMethod + lang\x00 + transKw\x00 + text.
	itxtData := append([]byte("Comment\x00\x00\x00\x00\x00"), []byte("hello")...)
	src := buildPNGWithBadCRC("iTXt", itxtData)
	itxtSpan := src[8+13+12 : 8+13+12+8+len(itxtData)+4]

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, nil, nil, []byte("<xmp/>"), true); err != nil {
		t.Fatalf("Inject: unexpected error: %v", err)
	}

	if !bytes.Contains(out.Bytes(), itxtSpan) {
		t.Errorf("Inject output does not contain the non-XMP iTXt chunk's original bytes (including its invalid CRC) unchanged.\n"+
			"expected span: % x", itxtSpan)
	}
}
