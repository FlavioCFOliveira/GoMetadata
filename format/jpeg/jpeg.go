// Package jpeg implements extraction and injection of EXIF, IPTC, and XMP
// metadata segments within JPEG files.
//
// JPEG structure: SOI (FF D8) followed by a sequence of markers, each
// beginning with FF <marker> <length-2> <data>. This package handles:
//   - APP1 (FF E1) with "Exif\x00\x00" prefix → EXIF payload
//   - APP1 (FF E1) with XMP namespace URI prefix → XMP packet
//   - APP13 (FF ED) with "Photoshop 3.0\x00" prefix → IRB containing IPTC
//
// References: EXIF §4.5.4 (APP1), JPEG ISO/IEC 10918-1.
package jpeg

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// JPEG marker bytes (ISO/IEC 10918-1, Annex B).
const (
	markerSOI   byte = 0xD8
	markerEOI   byte = 0xD9
	markerSOS   byte = 0xDA
	markerAPP1  byte = 0xE1
	markerAPP13 byte = 0xED
)

// identExif is the mandatory 6-byte prefix for EXIF inside APP1 (EXIF §4.5.4).
var identExif = []byte("Exif\x00\x00") //nolint:gochecknoglobals // package-level constant bytes

// identXMP is the NUL-terminated namespace URI prefix for XMP inside APP1.
// Adobe XMP Specification Part 3 §1.1.3.
var identXMP = []byte("http://ns.adobe.com/xap/1.0/\x00") //nolint:gochecknoglobals // package-level constant bytes

// identXMPNote is the NUL-terminated APP1 segment identifier for extended XMP
// chunks. Adobe XMP Specification Part 3 §1.1.4.
//
// NOTE: this is the APP1 segment identifier ("http://ns.adobe.com/xmp/extension/\x00"),
// which is distinct from the xmpNote namespace URI
// ("http://ns.adobe.com/xap/1.0/se/Note/") used for the HasExtendedXMP property
// inside the main XMP packet.  The two strings must never be confused.
var identXMPNote = []byte("http://ns.adobe.com/xmp/extension/\x00") //nolint:gochecknoglobals // package-level constant bytes

// identPS is the Photoshop 3.0 signature in APP13 (EXIF §4.5.6).
var identPS = []byte("Photoshop 3.0\x00") //nolint:gochecknoglobals // package-level constant bytes

// APP1 segment capacity constants derived from the JPEG 16-bit length field.
// JPEG ISO/IEC 10918-1 §B.1.1.4: length field is 2 bytes and includes itself,
// so the maximum payload is 65535 − 2 = 65533 bytes.
//
// maxXMPPayload: max XMP packet bytes in a standard (non-extended) APP1.
// maxExtChunkSize: max chunk data per extended XMP APP1 (Adobe XMP Spec Part 3 §1.1.4).
//
//	Extended APP1 layout: identXMPNote(35) + GUID(32) + fullLen(4) + offset(4) + chunk
//	Overhead = 35 + 32 + 4 + 4 = 75 bytes → chunk data ≤ 65533 − 75 = 65458 bytes.
//
// maxExtendedXMPTotal: aggregate cap on all extended XMP chunk bytes accumulated
// for a single GUID during parsing. Adobe XMP Specification Part 3 §1.1.4 allows
// an extended XMP document up to 2^32−1 bytes on wire, but accepting that from an
// untrusted JPEG stream would allow a ~268 MiB memory exhaustion attack (#40).
// 16 MiB is generous for any real-world extended XMP use (Google Cardboard depth
// maps, panorama metadata) while bounding worst-case allocation to a safe level.
//
// maxExtendedXMPGUIDs: maximum number of distinct GUID keys accumulated in the
// extended map during a single Extract call. Adobe XMP Specification Part 3
// §1.1.3.1 states that a conforming file carries extended XMP under exactly ONE
// GUID per file. Accepting N distinct GUIDs each up to maxExtendedXMPTotal bytes
// would allow an adversary to aggregate N × 16 MiB of memory (e.g. 100 GUIDs →
// 1.6 GiB). We cap at 4 — four times the spec-mandated 1 — to be permissive with
// non-conforming files while bounding worst-case memory to 64 MiB.
const (
	maxAPP1Payload      = 65533               // 65535 − 2 (length field)
	maxXMPPayload       = maxAPP1Payload - 29 // − len(identXMP)
	maxExtChunkSize     = maxAPP1Payload - 75 // − len(identXMPNote)+GUID+fullLen+offset overhead
	maxExtendedXMPTotal = 16 << 20            // 16 MiB aggregate cap per GUID (#40 DoS mitigation)
	maxExtendedXMPGUIDs = 4                   // cap on distinct GUIDs per file (spec: 1; we allow 4)
)

// xmpWireMagic is the 8-byte magic that identifies an XMP wire-frame payload.
//
// A wire-frame payload is an internal encoding used when a JPEG carries
// extended XMP (Adobe XMP Specification Part 3 §1.1.4). It carries the
// ORIGINAL main XMP APP1 content and the ASSEMBLED extended XMP payload as a
// single byte slice that can be passed through the rawXMP channel without
// changing any public interface.
//
// Layout: [8-byte magic][4-byte mainLen BE][main bytes][ext bytes]
//
// The first byte 0x00 is not a valid start byte for any XMP packet (XMP
// packets start with '<?xpacket' or whitespace followed by '<'), so the
// magic is unambiguous in both directions.
//
// Wire frames are NEVER exposed to callers of the public API. RawXMP() always
// returns the reassembled (user-visible) form.
var xmpWireMagic = []byte("\x00XMPEXT\x00") //nolint:gochecknoglobals // package-level constant bytes

// encodeXMPWire encodes the original main XMP packet and the assembled
// extended XMP payload into a single wire-frame byte slice.
// Returns nil if either main or ext is nil (no framing needed).
func encodeXMPWire(main, ext []byte) []byte {
	if main == nil || ext == nil {
		return nil
	}
	mainLen := len(main)
	total := len(xmpWireMagic) + 4 + mainLen + len(ext)
	b := make([]byte, total)
	n := copy(b, xmpWireMagic)
	binary.BigEndian.PutUint32(b[n:], uint32(mainLen)) //nolint:gosec // G115: mainLen bounded by JPEG APP1 max (65533)
	n += 4
	n += copy(b[n:], main)
	copy(b[n:], ext)
	return b
}

// decodeXMPWire splits a wire-frame payload into the original main XMP packet
// and the assembled extended XMP payload.
// Returns (nil, nil, false) when raw does not begin with xmpWireMagic.
func decodeXMPWire(raw []byte) (main, ext []byte, ok bool) {
	magicLen := len(xmpWireMagic)
	if len(raw) < magicLen+4 || !bytes.HasPrefix(raw, xmpWireMagic) {
		return nil, nil, false
	}
	mainLen := int(binary.BigEndian.Uint32(raw[magicLen : magicLen+4]))
	body := raw[magicLen+4:]
	if mainLen > len(body) {
		return nil, nil, false
	}
	return body[:mainLen], body[mainLen:], true
}

// extChunk holds one chunk of an extended XMP segment.
// Adobe XMP Specification Part 3 §1.1.4.
type extChunk struct {
	offset uint32
	data   []byte
}

// appendExtendedXMPChunk adds one extended XMP chunk body to the accumulation
// maps, enforcing the maxExtendedXMPTotal aggregate cap per GUID (#40).
//
// body must be at least 40 bytes (32-byte GUID + 4-byte fullLen + 4-byte offset
// + chunk data). It is the caller's responsibility to verify this precondition.
//
// Returns updated extended and extSizes maps and the (possibly set) extTruncated
// flag. DoS mitigation: the wire fullLen field from the first chunk for a GUID
// is validated against maxExtendedXMPTotal; if it exceeds the cap the GUID is
// blacklisted. Subsequent chunks are individually bounded by the same cap.
// Adobe XMP Specification Part 3 §1.1.4 describes the wire fullLen field.
func appendExtendedXMPChunk(
	body []byte,
	extended map[string][]extChunk,
	extSizes map[string]uint64,
	extTruncated bool,
) (map[string][]extChunk, map[string]uint64, bool) {
	guid := string(body[:32])
	// fullLen is the wire-declared total size for this GUID's assembled payload.
	fullLen := uint64(binary.BigEndian.Uint32(body[32:36]))
	offset := binary.BigEndian.Uint32(body[36:40])
	chunkData := body[40:]

	// Lazily initialise maps on first encounter.
	if extended == nil {
		extended = make(map[string][]extChunk)
	}
	if extSizes == nil {
		extSizes = make(map[string]uint64)
	}

	// First-seen check: validate the declared total against the cap and enforce
	// the per-file GUID count cap.
	//
	// Adobe XMP Specification Part 3 §1.1.3.1: a conforming file uses exactly
	// one GUID. We allow up to maxExtendedXMPGUIDs (4) to tolerate non-conforming
	// files without permitting unbounded memory growth: an adversary crafting a
	// JPEG with many distinct GUIDs (each up to maxExtendedXMPTotal bytes) could
	// otherwise aggregate far beyond 16 MiB of allocations.
	if _, seen := extSizes[guid]; !seen {
		if len(extSizes) >= maxExtendedXMPGUIDs {
			// Too many distinct GUIDs: stop accumulating and mark truncated.
			return extended, extSizes, true // extTruncated
		}
		if fullLen > maxExtendedXMPTotal {
			return extended, extSizes, true // extTruncated
		}
		extSizes[guid] = 0 // mark as seen with zero bytes accumulated
	}

	// Per-chunk running total check: drop this chunk if it would exceed the cap.
	accumulated := extSizes[guid]
	chunkSize := uint64(len(chunkData))
	if accumulated+chunkSize > maxExtendedXMPTotal {
		return extended, extSizes, true // extTruncated
	}

	// Copy chunk data: body aliases scratch and must outlive this loop.
	extSizes[guid] = accumulated + chunkSize
	extended[guid] = append(extended[guid], extChunk{
		offset: offset,
		data:   bytes.Clone(chunkData),
	})
	return extended, extSizes, extTruncated
}

// processAPP1Segment dispatches an APP1 segment payload to the appropriate
// metadata bucket (EXIF, standard XMP, or extended XMP).
//
// It returns updated values for rawEXIF, rawXMP, the extended chunk map,
// the per-GUID accumulated byte counter map, and the extTruncated flag.
// Pass-through values are returned unchanged when the segment does not apply.
//
// Extended XMP DoS mitigation is delegated to appendExtendedXMPChunk (#40).
func processAPP1Segment(
	data, rawEXIF, rawXMP []byte,
	extended map[string][]extChunk,
	extSizes map[string]uint64,
	extTruncated bool,
) ([]byte, []byte, map[string][]extChunk, map[string]uint64, bool) {
	switch {
	case bytes.HasPrefix(data, identExif):
		// EXIF payload begins after the 6-byte "Exif\x00\x00" header.
		// TIFF §2: a valid TIFF stream requires at least 8 bytes (2-byte byte-order
		// mark + 2-byte magic number + 4-byte IFD0 offset). A shorter residual is
		// structurally corrupt and must not be returned as rawEXIF — callers rely on
		// the invariant that rawEXIF is nil or >= 8 bytes.
		if payload := data[len(identExif):]; len(payload) >= 8 {
			// Copy: data aliases scratch and must survive the next readSegment call.
			rawEXIF = bytes.Clone(payload)
		}

	case bytes.HasPrefix(data, identXMP):
		// Copy: same reason as rawEXIF.
		rawXMP = bytes.Clone(data[len(identXMP):])

	case bytes.HasPrefix(data, identXMPNote):
		// Extended XMP chunk: GUID (32 bytes) + fullLength (4 bytes) +
		// offset (4 bytes) + chunk data. Adobe XMP Spec Part 3 §1.1.4.
		if body := data[len(identXMPNote):]; len(body) >= 40 {
			extended, extSizes, extTruncated = appendExtendedXMPChunk(body, extended, extSizes, extTruncated)
		}
	}

	return rawEXIF, rawXMP, extended, extSizes, extTruncated
}

// processAPP13Segment checks a segment payload for the Photoshop IRB prefix and,
// if present, calls parseIRB to extract the IPTC IIM stream.
// Returns nil when the segment carries no recognisable IPTC data.
func processAPP13Segment(data []byte) []byte {
	if !bytes.HasPrefix(data, identPS) {
		return nil
	}
	// parseIRB returns a sub-slice of its input; copy since input aliases scratch.
	irb := parseIRB(data[len(identPS):])
	if irb == nil {
		return nil
	}
	return bytes.Clone(irb)
}

// xmpResult bundles the two forms of XMP returned by scanMetadataSegmentsWithWire.
// rawXMP is the user-visible reassembled form; rawXMPWire is the internal
// wire-frame encoding (non-nil only when extended XMP was present).
type xmpResult struct {
	rawXMP     []byte // reassembled, user-visible
	rawXMPWire []byte // wire-frame for lossless passthrough writes (may be nil)
}

// buildXMPResult constructs an xmpResult from the raw main packet and any
// extended chunks collected during segment scanning.
//
// When extended is nil or empty: rawXMP = main, rawXMPWire = nil.
// When extended chunks are present: rawXMP = reassembled(main, ext),
// rawXMPWire = encodeXMPWire(main, assembledExt).
func buildXMPResult(rawXMP []byte, extended map[string][]extChunk) xmpResult {
	if rawXMP == nil || len(extended) == 0 {
		return xmpResult{rawXMP: rawXMP}
	}

	guid, found := extractGUIDFromMain(rawXMP)
	if !found {
		return xmpResult{rawXMP: rawXMP}
	}
	chunks, ok := extended[guid]
	if !ok || len(chunks) == 0 {
		return xmpResult{rawXMP: rawXMP}
	}

	extBytes := mergeExtendedChunks(chunks)

	// Build wire-frame BEFORE modifying the extended chunks map, so that the
	// original raw bytes are preserved verbatim for passthrough writes.
	wire := encodeXMPWire(rawXMP, extBytes)

	// Now reassemble the user-visible form.
	extMap := map[string][]extChunk{guid: {{offset: 0, data: extBytes}}}
	reassembled := reassembleExtendedXMP(rawXMP, extMap)

	return xmpResult{rawXMP: reassembled, rawXMPWire: wire}
}

// readSOI reads and validates the 2-byte JPEG SOI marker from soi.
// Returns an error if the bytes are not 0xFF 0xD8.
// JPEG ISO/IEC 10918-1 §B.1.1.3.
func readSOI(soi []byte) error {
	if soi[0] != 0xFF || soi[1] != markerSOI {
		return fmt.Errorf("jpeg: not a JPEG file (SOI 0x%04X): %w", uint16(soi[0])<<8|uint16(soi[1]), ErrNotJPEG)
	}
	return nil
}

// scanMetadataSegmentsWithWire reads the JPEG marker stream from r until
// SOS/EOI or read failure, collecting EXIF, IPTC, XMP, and extended-XMP
// payloads. It returns the reassembled XMP (for callers) and a wire-frame
// encoding (for lossless passthrough writes when extended XMP is present).
func scanMetadataSegmentsWithWire(r io.Reader, scratchPtr *[]byte) (rawEXIF, rawIPTC []byte, xmp xmpResult) {
	// extended collects chunks from extended XMP APP1 segments, keyed by GUID.
	// Adobe XMP Specification Part 3 §1.1.4.
	// Lazily initialised: most JPEGs do not contain extended XMP, so we avoid
	// the map allocation on the fast path.
	//
	// extSizes tracks accumulated byte totals per GUID for the #40 DoS cap.
	// extTruncated is set true when any GUID's payload was capped.
	var extended map[string][]extChunk
	var extSizes map[string]uint64
	var extTruncated bool
	var mainXMP []byte

	for {
		marker, data, rerr := readSegment(r, scratchPtr)
		if rerr != nil {
			// Both EOF and malformed-stream errors: degrade gracefully and
			// return whatever metadata has been collected so far.
			break
		}

		switch marker {
		case markerAPP1:
			rawEXIF, mainXMP, extended, extSizes, extTruncated = processAPP1Segment(
				data, rawEXIF, mainXMP, extended, extSizes, extTruncated,
			)
		case markerAPP13:
			if iptc := processAPP13Segment(data); iptc != nil {
				rawIPTC = iptc
			}
		case markerSOS, markerEOI:
			// SOS/EOI: no more metadata segments follow.
			_ = extTruncated // truncation is currently informational; callers get partial XMP
			return rawEXIF, rawIPTC, buildXMPResult(mainXMP, extended)
		}
	}

	_ = extTruncated // truncation is currently informational; callers get partial XMP
	return rawEXIF, rawIPTC, buildXMPResult(mainXMP, extended)
}

// Extract reads the JPEG marker stream from r and returns the raw payloads.
// rawEXIF: APP1 content after the "Exif\x00\x00" identifier (nil if absent).
// rawIPTC: the raw IIM byte stream extracted from the Photoshop IRB 8BIM
//
//	resource block 0x0404 inside APP13 (nil if absent).
//
// rawXMP:  the full XMP packet bytes from the XMP APP1 (nil if absent).
//
//	When the JPEG carries extended XMP, rawXMP is the reassembled
//	(merged) XMP document. Use ExtractWithWire for lossless passthrough.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	rawEXIF, rawIPTC, xmpRes, err := extractFull(r)
	if err != nil {
		return nil, nil, nil, err
	}
	return rawEXIF, rawIPTC, xmpRes.rawXMP, nil
}

// ExtractWithWire reads the JPEG marker stream and returns raw payloads plus
// an optional wire-frame encoding of the XMP segments.
//
// rawXMP is the user-visible reassembled XMP (identical to what Extract returns).
// rawXMPWire is non-nil only when the JPEG contains extended XMP; it carries the
// original main APP1 content and the assembled extended payload in an internal
// framing that Inject can use to rewrite the segments byte-stably.
//
// Callers outside the format/jpeg package should use Extract unless they need
// the wire-frame for passthrough writes.
func ExtractWithWire(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP, rawXMPWire []byte, err error) {
	rawEXIF, rawIPTC, xmpRes, err := extractFull(r)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return rawEXIF, rawIPTC, xmpRes.rawXMP, xmpRes.rawXMPWire, nil
}

// extractFull is the shared implementation of Extract and ExtractWithWire.
func extractFull(r io.ReadSeeker) (rawEXIF, rawIPTC []byte, xmp xmpResult, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, xmpResult{}, fmt.Errorf("jpeg: seek: %w", err)
	}

	// Obtain a pooled scratch buffer first so the SOI read can reuse it,
	// avoiding the heap escape that occurs when a stack-allocated [2]byte
	// is passed to io.ReadFull via the io.Reader interface.
	scratchPtr := iobuf.Get(4096)
	defer func() { iobuf.Put(scratchPtr) }()

	// Read and verify SOI using the pooled scratch buffer.
	soi := (*scratchPtr)[:2]
	if _, err = io.ReadFull(r, soi); err != nil {
		return nil, nil, xmpResult{}, fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if err := readSOI(soi); err != nil {
		return nil, nil, xmpResult{}, err
	}

	rawEXIF, rawIPTC, xmp = scanMetadataSegmentsWithWire(r, scratchPtr)
	return rawEXIF, rawIPTC, xmp, nil
}

// writeEXIFSegment writes the EXIF APP1 segment to w.
// APP1 length field is 16-bit; JPEG ISO/IEC 10918-1 and EXIF §4.5.4.
func writeEXIFSegment(w io.Writer, rawEXIF []byte) error {
	if len(identExif)+len(rawEXIF)+2 > 65535 {
		return fmt.Errorf("jpeg: EXIF payload %d bytes exceeds APP1 segment limit; EXIF cannot be split: %w", len(rawEXIF), ErrEXIFPayloadTooLarge)
	}
	exifBuf := iobuf.Get(len(identExif) + len(rawEXIF))
	copy(*exifBuf, identExif)
	copy((*exifBuf)[len(identExif):], rawEXIF)
	writeErr := writeSegment(w, markerAPP1, *exifBuf)
	iobuf.Put(exifBuf)
	return writeErr
}

// writeXMPSegments writes a standard XMP APP1 when the payload fits within
// maxXMPPayload, or falls back to the multi-segment extended-XMP path.
// Adobe XMP Specification Part 3 §1.1.4.
//
// When rawXMP begins with xmpWireMagic (a wire-frame encoding), the function
// decodes the original main XMP and the assembled extended payload and writes
// them using the passthrough path — main as a standard APP1, extended as
// re-chunked extended APP1 segments — preserving semantic content without
// repackaging into a new random-GUID stub. This ensures that round-trip
// writes of unmodified extended XMP are byte-stable (same reassembled content
// before and after).
func writeXMPSegments(w io.Writer, rawXMP []byte) error {
	// Wire-frame passthrough: preserve original main+extended without regenerating GUID.
	if main, ext, ok := decodeXMPWire(rawXMP); ok {
		return writeXMPWirePassthrough(w, main, ext)
	}

	if len(rawXMP) <= maxXMPPayload {
		// Fast path: XMP fits in a single APP1 segment.
		xmpBuf := iobuf.Get(len(identXMP) + len(rawXMP))
		copy(*xmpBuf, identXMP)
		copy((*xmpBuf)[len(identXMP):], rawXMP)
		writeErr := writeSegment(w, markerAPP1, *xmpBuf)
		iobuf.Put(xmpBuf)
		return writeErr
	}
	// Slow path: split into extended XMP segments.
	return writeExtendedXMP(w, rawXMP)
}

// writeXMPWirePassthrough writes the original main XMP APP1 and the assembled
// extended XMP payload as extended APP1 chunks, re-using the GUID embedded in
// the main packet. This path is taken when rawXMP was not modified (wire-frame
// passthrough from ExtractWithWire).
//
// The extended payload is re-chunked at maxExtChunkSize bytes per chunk
// (deterministic, same as the original write path). Since the GUID is taken
// from the main packet (not regenerated), and the extended bytes are
// identical to what was extracted, the reassembled XMP produced by a
// subsequent Extract call is guaranteed to equal the one produced from the
// original file.
func writeXMPWirePassthrough(w io.Writer, main, ext []byte) error {
	// Write the original main XMP APP1 verbatim.
	if err := writeRawXMPSegment(w, main); err != nil {
		return err
	}

	// Re-chunk ext using the GUID from main. The GUID is already present in
	// main, so readers will correctly locate the extended chunks.
	guid, ok := extractGUIDFromMain(main)
	if !ok || len(ext) == 0 {
		// No extended payload (or malformed main); write just the main. This
		// branch should not occur in practice for a well-formed wire-frame.
		return nil
	}
	return writeExtendedChunks(w, []byte(guid), ext)
}

// writeRawXMPSegment writes the main XMP APP1 segment using the original main
// packet bytes. Prepends identXMP and writes as a standard APP1.
func writeRawXMPSegment(w io.Writer, main []byte) error {
	totalLen := len(identXMP) + len(main)
	if totalLen+2 > 65535 {
		// Guard: a valid main packet is always < maxXMPPayload, so this cannot
		// trigger for well-formed wire frames.
		return fmt.Errorf("jpeg: XMP wire passthrough: main packet (%d bytes) exceeds APP1 limit: %w",
			len(main), ErrXMPStubTooLarge)
	}
	xmpBuf := iobuf.Get(totalLen)
	copy(*xmpBuf, identXMP)
	copy((*xmpBuf)[len(identXMP):], main)
	err := writeSegment(w, markerAPP1, *xmpBuf)
	iobuf.Put(xmpBuf)
	return err
}

// writeExtendedChunks splits ext into extended APP1 chunks and writes them.
// guid must be exactly 32 ASCII hex characters (per Adobe XMP Spec Part 3 §1.1.4).
func writeExtendedChunks(w io.Writer, guidBytes, ext []byte) error {
	fullLen := uint32(len(ext)) //nolint:gosec // G115: XMP payload size bounded by input
	offset := uint32(0)

	// Pre-allocate the fixed-size extended APP1 header once.
	// Header = identXMPNote(35) + GUID(32) + fullLen(4) + offset(4) = 75 bytes.
	const extHdrSize = 75
	for offset < fullLen {
		chunkEnd := offset + uint32(maxExtChunkSize) // min builtin shadowed by test-only helper in fuzz_test.go; cannot use min here
		if chunkEnd > fullLen {
			chunkEnd = fullLen
		}
		chunk := ext[offset:chunkEnd]

		extBuf := iobuf.Get(extHdrSize + len(chunk))
		b := *extBuf

		// identXMPNote (35 bytes: "http://ns.adobe.com/xmp/extension/\x00")
		copy(b, identXMPNote)
		// GUID (32 bytes) immediately after identifier
		copy(b[len(identXMPNote):], guidBytes)
		// fullLength (4 bytes BE) at offset 67 = 35 + 32
		binary.BigEndian.PutUint32(b[67:71], fullLen)
		// offset (4 bytes BE) at offset 71 = 35 + 32 + 4
		binary.BigEndian.PutUint32(b[71:75], offset)
		// chunk data starts at offset 75 = 35 + 32 + 4 + 4
		copy(b[75:], chunk)

		writeErr := writeSegment(w, markerAPP1, b)
		iobuf.Put(extBuf)
		if writeErr != nil {
			return writeErr
		}

		offset = chunkEnd
	}
	return nil
}

// writeIPTCSegment wraps the IPTC IIM stream in a Photoshop IRB block and
// writes it as an APP13 segment. APP13 length field is 16-bit; EXIF §4.5.6.
//
// When origIRB is non-nil it is used as the base IRB: the 0x0404 resource
// block within it is replaced with one built from rawIPTC while every other
// 8BIM resource in origIRB is copied verbatim (CLAUDE.md §5: write operations
// must preserve all existing metadata not explicitly modified). When origIRB is
// nil a bare 0x0404-only IRB is built from rawIPTC.
func writeIPTCSegment(w io.Writer, rawIPTC, origIRB []byte) error {
	var irb []byte
	if origIRB != nil {
		irb = spliceIPTCIntoIRB(origIRB, rawIPTC)
	} else {
		irb = buildIRB(rawIPTC)
	}
	if len(identPS)+len(irb)+2 > 65535 {
		return fmt.Errorf("jpeg: IPTC IRB payload %d bytes exceeds APP13 segment limit: %w", len(irb), ErrIPTCPayloadTooLarge)
	}
	iptcBuf := iobuf.Get(len(identPS) + len(irb))
	copy(*iptcBuf, identPS)
	copy((*iptcBuf)[len(identPS):], irb)
	writeErr := writeSegment(w, markerAPP13, *iptcBuf)
	iobuf.Put(iptcBuf)
	return writeErr
}

// spliceIPTCIntoIRB returns a new IRB byte slice that is identical to origIRB
// except the 0x0404 (IPTC-NAA) resource block is replaced with a freshly built
// block wrapping newIPTCData. All other 8BIM blocks are appended verbatim in
// their original order and with their original padding.
//
// If origIRB contains no 0x0404 block the new block is appended at the end.
// If origIRB is malformed (parseIRBEntry fails before a replacement position is
// found), the function falls back to buildIRB(newIPTCData) to ensure the
// essential IPTC data is never lost.
//
// EXIF §4.5.6: each 8BIM block is 4 ('8BIM') + 2 (ID) + pascal-name + 4 (size)
// + data [+ 1 padding if data size is odd].
func spliceIPTCIntoIRB(origIRB, newIPTCData []byte) []byte {
	newBlock := buildIRB(newIPTCData) // the replacement 0x0404 block

	// Pre-allocate a conservative capacity. The result is at most
	// len(origIRB) + len(newBlock) (old 0x0404 replaced, not just appended).
	out := make([]byte, 0, len(origIRB)+len(newBlock))

	replaced := false
	pos := 0
	for pos < len(origIRB) {
		entryStart := pos
		resourceID, data, newPos, ok := parseIRBEntry(origIRB, pos)
		if !ok {
			if newPos == pos {
				// Signature mismatch: advance one byte (scan-forward miss).
				pos++
				continue
			}
			// Structural failure: stop processing; emit what we have so far.
			break
		}

		// Compute the end of the full encoded block including even-padding.
		// We use the raw bytes from origIRB directly rather than re-encoding,
		// preserving non-standard pascal names, reserved fields, etc.
		blockEnd := newPos
		if len(data)%2 != 0 {
			blockEnd++ // skip the even-padding byte
		}

		if resourceID == 0x0404 {
			// Replace the IPTC block with the freshly built one.
			out = append(out, newBlock...)
			replaced = true
		} else {
			// Copy the block verbatim — 8BIM header + data + any padding byte.
			// blockEnd is bounded by origIRB length (validated inside parseIRBEntry).
			if blockEnd > len(origIRB) {
				blockEnd = len(origIRB)
			}
			out = append(out, origIRB[entryStart:blockEnd]...)
		}

		pos = blockEnd
	}

	if !replaced {
		// No 0x0404 block was found in the original IRB: append the new one.
		out = append(out, newBlock...)
	}
	return out
}

// writeNewMetadataSegments writes EXIF APP1, XMP APP1 (with extended-XMP
// splitting when the payload exceeds the single-segment limit), and IPTC
// APP13 segments to w. Returns the first error encountered.
//
// origIRB is the full Photoshop IRB block (the bytes after the "Photoshop
// 3.0\x00" header) from the source JPEG, or nil if the source had no APP13.
// When non-nil it is used by writeIPTCSegment to preserve sibling 8BIM
// resources while replacing only the 0x0404 block.
func writeNewMetadataSegments(w io.Writer, rawEXIF, rawIPTC, rawXMP, origIRB []byte) error {
	if rawEXIF != nil {
		if err := writeEXIFSegment(w, rawEXIF); err != nil {
			return err
		}
	}
	if rawXMP != nil {
		if err := writeXMPSegments(w, rawXMP); err != nil {
			return err
		}
	}
	if rawIPTC != nil {
		if err := writeIPTCSegment(w, rawIPTC, origIRB); err != nil {
			return err
		}
	}
	return nil
}

// isOldMetadataSegment reports whether a marker+data pair is a metadata
// segment that Inject should strip (EXIF APP1, standard XMP APP1, extended
// XMP APP1, or Photoshop APP13). It is a pure predicate with no side effects.
func isOldMetadataSegment(marker byte, data []byte) bool {
	if marker == markerAPP1 {
		return bytes.HasPrefix(data, identExif) ||
			bytes.HasPrefix(data, identXMP) ||
			bytes.HasPrefix(data, identXMPNote)
	}
	if marker == markerAPP13 {
		return bytes.HasPrefix(data, identPS)
	}
	return false
}

// writeMarker writes a standalone JPEG marker (FF <marker>) to w.
func writeMarker(w io.Writer, marker byte) error {
	if _, err := w.Write([]byte{0xFF, marker}); err != nil {
		return fmt.Errorf("jpeg: write marker: %w", err)
	}
	return nil
}

// writePassThroughSegment writes a single non-metadata segment to w.
// Standalone markers (nil data) are written as FF <marker>; segments with
// data are written with the standard length-prefixed format.
func writePassThroughSegment(w io.Writer, marker byte, data []byte) error {
	if data == nil {
		return writeMarker(w, marker)
	}
	return writeSegment(w, marker, data)
}

// writeSOS writes the SOS segment and then copies the remaining compressed
// image data from r to w verbatim.
func writeSOS(r io.Reader, w io.Writer, data []byte) error {
	if err := writeSegment(w, markerSOS, data); err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("jpeg: copy image data: %w", err)
	}
	return nil
}

// copyNonMetadataSegments reads segments from r, skips old metadata APP
// segments, and passes the rest through to w. It terminates on SOS (copying
// the compressed stream verbatim) or EOI.
func copyNonMetadataSegments(r io.Reader, w io.Writer, scratch *[]byte) error {
	for {
		marker, data, err := readSegment(r, scratch)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		// Skip segments we replaced (or removed when payload is nil).
		if isOldMetadataSegment(marker, data) {
			continue
		}

		switch marker {
		case markerSOS:
			return writeSOS(r, w, data)
		case markerEOI:
			return writeMarker(w, markerEOI)
		default:
			if err := writePassThroughSegment(w, marker, data); err != nil {
				return err
			}
		}
	}
}

// extractOriginalIRB performs a pre-scan of the JPEG in r to locate the first
// Photoshop APP13 segment and return a copy of its IRB bytes (the content after
// the "Photoshop 3.0\x00" header). Returns nil when no APP13 is present or the
// segment does not carry the Photoshop prefix.
//
// The caller is responsible for seeking r back to the desired position after
// this call. scratch is used as an internal read buffer and must not be nil.
func extractOriginalIRB(r io.ReadSeeker, scratch *[]byte) []byte {
	// Seek past the SOI (already validated by the caller — 2 bytes).
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		return nil
	}
	for {
		marker, data, err := readSegment(r, scratch)
		if err != nil {
			return nil
		}
		switch marker {
		case markerAPP13:
			if bytes.HasPrefix(data, identPS) {
				// Copy the IRB bytes (data[len(identPS):]) so they survive
				// beyond the next readSegment call that would overwrite scratch.
				irb := data[len(identPS):]
				if len(irb) == 0 {
					return nil
				}
				out := make([]byte, len(irb))
				copy(out, irb)
				return out
			}
		case markerSOS, markerEOI:
			return nil
		}
	}
}

// Inject reads the JPEG marker stream from r, replaces the relevant APP
// segments with the provided payloads, and writes the result to w.
// A nil payload means the corresponding segment is removed.
// The SOS segment and compressed image data are passed through unchanged.
//
// rawXMP may be a wire-frame payload (produced by ExtractWithWire) when the
// XMP was not modified; in that case the original main and extended APP1
// segments are reproduced byte-stably without regenerating the GUID.
//
// When the source JPEG carries a Photoshop APP13 segment that contains 8BIM
// resources in addition to the 0x0404 IPTC block (e.g. IPTC digest 0x0425,
// thumbnail 0x040C, ICC clipping path 0x040F), Inject preserves all sibling
// resources verbatim and only replaces the 0x0404 block with rawIPTC. When the
// source has no APP13, or when rawIPTC is nil, the behaviour is unchanged.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}

	// Pre-scan to capture the original Photoshop IRB (all 8BIM blocks) so that
	// sibling resources are preserved when we write the new APP13 below.
	// We use a pooled buffer for this scan only; it is returned before the
	// second seek so that the main copy loop can reuse its own buffer cleanly.
	var origIRB []byte
	if rawIPTC != nil {
		preScratch := iobuf.Get(4096)
		origIRB = extractOriginalIRB(r, preScratch)
		iobuf.Put(preScratch)
	}

	// Seek back to the start for the main copy pass.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}

	// Read and write SOI.
	soi := [2]byte{}
	if _, err := io.ReadFull(r, soi[:]); err != nil {
		return fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if soi[0] != 0xFF || soi[1] != markerSOI {
		return ErrNotJPEG
	}
	if _, err := w.Write(soi[:]); err != nil {
		return fmt.Errorf("jpeg: write segment: %w", err)
	}

	// Write new metadata segments before any existing ones.
	if err := writeNewMetadataSegments(w, rawEXIF, rawIPTC, rawXMP, origIRB); err != nil {
		return err
	}

	// Copy remaining segments, skipping old metadata APP segments.
	// Use a pooled scratch buffer: data is consumed immediately within each
	// loop iteration and never stored, so no copying is needed here.
	injectScratch := iobuf.Get(4096)
	defer iobuf.Put(injectScratch)

	return copyNonMetadataSegments(r, w, injectScratch)
}

// writeExtendedXMP splits rawXMP across a main APP1 and one or more extended
// APP1 segments, per Adobe XMP Specification Part 3 §1.1.4.
//
// Strategy:
//  1. Generate a random 32-hex-character GUID via crypto/rand.
//  2. Build a minimal "main" XMP document that contains only the
//     xmpNote:HasExtendedXMP property set to the GUID. This document is
//     guaranteed to be far smaller than the 65504-byte limit.
//  3. Write the main XMP as a standard APP1 segment.
//  4. Write rawXMP verbatim as the extended payload, split into chunks of at
//     most maxExtChunkSize bytes. Each chunk becomes one extended APP1 segment.
//
// The xmpNote namespace URI is http://ns.adobe.com/xap/1.0/se/Note/ per the
// Adobe XMP Specification Part 3 §1.1.4.
func writeExtendedXMP(w io.Writer, rawXMP []byte) error {
	// Step 1: generate GUID.
	var guidRaw [16]byte
	if _, err := rand.Read(guidRaw[:]); err != nil {
		return fmt.Errorf("jpeg: extended XMP: generate GUID: %w", err)
	}
	guid := hex.EncodeToString(guidRaw[:]) // 32 hex characters

	// Step 2: build the minimal main XMP document.
	// The document is a self-contained, valid XMP packet that carries only the
	// xmpNote:HasExtendedXMP attribute. Readers merge the extended payload on
	// top of this stub. The literal template is faster and simpler than
	// invoking the xmp package from the format layer.
	//
	// xmpNote namespace: http://ns.adobe.com/xap/1.0/se/Note/ (XMP Spec Part 3 §1.1.4)
	mainXMP := []byte(
		`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
			`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="GoMetadata">` +
			`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description rdf:about=""` +
			` xmlns:xmpNote="http://ns.adobe.com/xap/1.0/se/Note/"` +
			` xmpNote:HasExtendedXMP="` + guid + `"/>` +
			`</rdf:RDF></x:xmpmeta>` +
			`<?xpacket end="w"?>`,
	)
	if len(mainXMP) > maxXMPPayload {
		// This cannot happen in practice — the template is ~200 bytes — but
		// guard defensively so the error is actionable rather than silent.
		return fmt.Errorf("jpeg: extended XMP: main XMP stub (%d bytes) exceeds APP1 limit: %w", len(mainXMP), ErrXMPStubTooLarge)
	}

	// Step 3: write main APP1.
	mainBuf := iobuf.Get(len(identXMP) + len(mainXMP))
	copy(*mainBuf, identXMP)
	copy((*mainBuf)[len(identXMP):], mainXMP)
	writeErr := writeSegment(w, markerAPP1, *mainBuf)
	iobuf.Put(mainBuf)
	if writeErr != nil {
		return writeErr
	}

	// Step 4: split rawXMP into extended APP1 chunks.
	return writeExtendedChunks(w, []byte(guid), rawXMP)
}

// skipPascalString advances pos past a Pascal-string name field in a
// Photoshop IRB entry. The field is 1-byte length + length bytes of name,
// padded to an even total. Returns (newPos, true) on success or (pos+1, false)
// when the buffer is too short. EXIF §4.5.6.
func skipPascalString(b []byte, pos int) (int, bool) {
	if pos >= len(b) {
		return pos + 1, false
	}
	nameLen := int(b[pos])
	pos++ // consume length byte
	pos += nameLen
	if (nameLen+1)%2 != 0 {
		pos++ // even-padding byte
	}
	return pos, true
}

// parseIRBEntry validates the "8BIM" signature at b[pos], reads the resource
// ID, Pascal-string name, and data block, and returns the resource ID, data
// slice, new position, and a success flag.
//
// Two distinct failure modes:
//   - signature mismatch: returns (0, nil, pos, false) — newPos == pos signals
//     this; the caller may advance by 1 to scan forward.
//   - structural failure (truncated data, bad size): returns with newPos > pos;
//     the caller treats this as terminal.
//
// IRB format: "8BIM" + 2-byte resource ID + Pascal string name + 4-byte size + data.
// EXIF §4.5.6.
func parseIRBEntry(b []byte, pos int) (resourceID uint16, data []byte, newPos int, ok bool) {
	if pos+4 > len(b) {
		return 0, nil, pos + 1, false // terminal: not enough bytes even for signature
	}

	// Check "8BIM" signature; return pos unchanged on mismatch so the caller
	// can distinguish a scan-forward miss from a structural error.
	if b[pos] != '8' || b[pos+1] != 'B' || b[pos+2] != 'I' || b[pos+3] != 'M' {
		return 0, nil, pos, false // signature mismatch — caller advances by 1
	}
	pos += 4

	if pos+2 > len(b) {
		return 0, nil, pos + 1, false
	}
	resourceID = binary.BigEndian.Uint16(b[pos:])
	pos += 2

	// Skip the Pascal-string name field (1-byte length + name + even padding).
	pos, ok = skipPascalString(b, pos)
	if !ok {
		return 0, nil, pos, false
	}

	if pos+4 > len(b) {
		return 0, nil, pos + 1, false
	}
	// #45 (32-bit safety): read as uint64 before any arithmetic to prevent the
	// negative-int wrap-around on 32-bit platforms where int is 32 bits.
	// binary.BigEndian.Uint32 returns uint32 (max ~4 GiB); on a 32-bit platform
	// casting directly to int would produce a negative value for sizes ≥ 2 GiB,
	// bypassing the bounds check below and panicking on the slice expression.
	// We validate using uint64 arithmetic throughout, then convert to int only
	// after confirming the value fits within the buffer (which is at most
	// MaxInt32 bytes on 32-bit, so the conversion is always safe at that point).
	dataSizeU64 := uint64(binary.BigEndian.Uint32(b[pos:]))
	pos += 4

	// Bounds check entirely in uint64 to be safe on 32-bit platforms.
	// pos is a non-negative int (it has been advanced past the fixed-size header
	// fields without exceeding len(b)); the cast is safe.
	posU64 := uint64(pos) //nolint:gosec // G115: pos is always non-negative (bounded by len(b))
	if posU64+dataSizeU64 > uint64(len(b)) {
		return 0, nil, pos + 1, false
	}
	// Safe to convert: dataSizeU64 ≤ len(b)−pos ≤ len(b) ≤ MaxInt (both 32 and 64-bit).
	dataSize := int(dataSizeU64) //nolint:gosec // G115: safe; bounded by uint64 check above
	return resourceID, b[pos : pos+dataSize], pos + dataSize, true
}

// parseIRB extracts the IPTC IIM stream from a Photoshop IRB block.
// IRB format: "8BIM" + 2-byte resource ID + Pascal string name + 4-byte size + data.
// Resource ID 0x0404 is the IPTC-NAA resource (EXIF §4.5.6.2).
func parseIRB(b []byte) []byte {
	pos := 0
	for pos < len(b) {
		resourceID, data, newPos, ok := parseIRBEntry(b, pos)
		if !ok {
			if newPos == pos {
				// Signature mismatch: advance one byte to scan forward.
				pos++
				continue
			}
			// Structural failure (truncated data, bad bounds): terminal.
			break
		}

		if resourceID == 0x0404 {
			return data
		}

		pos = newPos
		// Apply even-padding to data block (EXIF §4.5.6).
		if len(data)%2 != 0 {
			pos++
		}
	}
	return nil
}

// buildIRB wraps a raw IPTC IIM stream in a minimal Photoshop IRB block
// (resource ID 0x0404) ready for embedding in APP13.
func buildIRB(iptcData []byte) []byte {
	size := len(iptcData)
	// 4 (8BIM) + 2 (ID) + 2 (empty pascal name) + 4 (data size) + data [+ padding]
	buf := make([]byte, 0, 12+size+(size%2))
	// Photoshop IRB header: 8BIM marker, resource ID 0x0404, empty pascal name,
	// then 4-byte big-endian data length. G115: byte shifts are safe bit extractions.
	//nolint:gosec // G115: byte extraction from int size value; shifts are safe bit extractions
	buf = append(buf,
		'8', 'B', 'I', 'M', // 8BIM marker
		0x04, 0x04, // resource ID 0x0404
		0x00, 0x00, // empty pascal name (length 0 + padding byte)
		byte(size>>24), byte(size>>16), byte(size>>8), byte(size), // data length
	)
	buf = append(buf, iptcData...)
	if size%2 != 0 {
		buf = append(buf, 0x00) // pad data to even boundary
	}
	return buf
}

// skipFillBytes reads consecutive 0xFF fill bytes from r into hdr[1], advancing
// past padding bytes until hdr[1] holds a non-0xFF marker byte.
// JPEG ISO/IEC 10918-1 §B.1.1.2: fill bytes are allowed before any marker.
func skipFillBytes(r io.Reader, hdr []byte) error {
	for hdr[1] == 0xFF {
		if _, err := io.ReadFull(r, hdr[1:]); err != nil {
			return fmt.Errorf("jpeg: read fill byte: %w", err)
		}
	}
	return nil
}

// readSegment reads one JPEG marker segment from r into *scratch, growing it
// if necessary. For standalone markers (SOI, EOI, RST*), data is nil.
// Returns (0, nil, io.EOF) at end of file.
//
// The returned data slice aliases *scratch and is only valid until the next
// call to readSegment. Callers that need to retain data past the next call
// must copy it (e.g. append([]byte(nil), data...)).
func readSegment(r io.Reader, scratch *[]byte) (marker byte, data []byte, err error) {
	// Ensure scratch has room for at least the 4-byte header (2-byte marker +
	// 2-byte length). iobuf.Get guarantees at least 4096 bytes on the first
	// call; we only reallocate when a payload exceeds the current capacity.
	if len(*scratch) < 4 {
		*scratch = make([]byte, 4096)
	}
	hdr := (*scratch)[:2]

	if _, err = io.ReadFull(r, hdr); err != nil {
		return 0, nil, fmt.Errorf("jpeg: read segment header: %w", err)
	}
	if hdr[0] != 0xFF {
		return 0, nil, fmt.Errorf("jpeg: expected marker prefix 0xFF, got 0x%02X: %w", hdr[0], ErrInvalidMarkerPrefix)
	}
	// Skip fill bytes (consecutive 0xFF).
	if skipErr := skipFillBytes(r, hdr); skipErr != nil {
		return 0, nil, skipErr
	}
	marker = hdr[1]

	// Standalone markers carry no length or data.
	if isStandalone(marker) {
		return marker, nil, nil
	}

	lenB := (*scratch)[2:4]
	if _, err = io.ReadFull(r, lenB); err != nil {
		return 0, nil, fmt.Errorf("jpeg: read length for marker 0x%02X: %w", marker, err)
	}
	length := int(binary.BigEndian.Uint16(lenB))
	if length < 2 {
		return 0, nil, fmt.Errorf("jpeg: marker 0x%02X has invalid length %d: %w", marker, length, ErrInvalidMarkerLength)
	}

	need := length - 2
	if need > len(*scratch) {
		*scratch = make([]byte, need)
	}
	data = (*scratch)[:need]
	if _, err = io.ReadFull(r, data); err != nil {
		return 0, nil, fmt.Errorf("jpeg: truncated data for marker 0x%02X: %w", marker, err)
	}
	return marker, data, nil
}

// writeSegment writes a JPEG marker segment to w.
// Returns an error if the total segment length (data + 2-byte length field)
// would exceed the 16-bit field maximum of 65535. JPEG ISO/IEC 10918-1 §B.1.1.4.
func writeSegment(w io.Writer, marker byte, data []byte) error {
	length := len(data) + 2 // length field includes its own 2 bytes
	if length > 65535 {
		return fmt.Errorf("jpeg: segment 0x%02X payload %d bytes exceeds 65535-byte APP segment limit: %w", marker, len(data), ErrSegmentTooLarge)
	}
	hdr := [4]byte{0xFF, marker, byte(length >> 8), byte(length)} //nolint:gosec // G115: JPEG segment length ≤ 65535 per format spec
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("jpeg: write segment header: %w", err)
	}
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("jpeg: write segment body: %w", err)
		}
	}
	return nil
}

// extractGUIDFromMain locates the HasExtendedXMP attribute in the main XMP
// packet and returns the 32-hex-character GUID value.
// Returns ("", false) if the attribute is absent or malformed.
func extractGUIDFromMain(main []byte) (guid string, ok bool) {
	const marker = "HasExtendedXMP"
	// The GUID value follows the property name as either an attribute
	// (HasExtendedXMP="<GUID>") or element content (HasExtendedXMP><GUID></...).
	// In both cases we scan past up to 5 bytes for the opening quote character.
	_, rest, found := bytes.Cut(main, []byte(marker))
	if !found {
		return "", false
	}
	qi := bytes.IndexAny(rest, `"'`)
	if qi < 0 || qi > 5 {
		return "", false
	}
	quote := rest[qi]
	rest = rest[qi+1:]
	end := bytes.IndexByte(rest, quote)
	if end != 32 { // GUID must be exactly 32 hex characters
		return "", false
	}
	return string(rest[:32]), true
}

// mergeExtendedChunks sorts chunks by their byte offset and concatenates their
// data fields into a single contiguous extended XMP byte slice.
func mergeExtendedChunks(chunks []extChunk) []byte {
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].offset < chunks[j].offset
	})

	var totalLen int
	for _, c := range chunks {
		totalLen += len(c.data)
	}
	extBytes := make([]byte, 0, totalLen)
	for _, c := range chunks {
		extBytes = append(extBytes, c.data...)
	}
	return extBytes
}

// reassembleExtendedXMP merges extended XMP chunks into the main XMP packet
// per Adobe XMP Specification Part 3 §1.1.4.
//
// The main XMP packet carries a HasExtendedXMP property whose value is the
// 32-hex-character MD5 GUID of the corresponding extended segments. This
// function locates that GUID, sorts the matching chunks by their byte offset,
// concatenates their data into a complete extended XMP document, and splices
// the inner rdf:Description elements from that document into the main packet
// immediately before its closing </rdf:RDF> tag.
//
// If any step fails (missing marker, GUID not found, malformed packet) the
// function returns main unchanged — graceful degradation is required because
// we cannot know in advance whether all extended segments are present.
func reassembleExtendedXMP(main []byte, extended map[string][]extChunk) []byte {
	guid, ok := extractGUIDFromMain(main)
	if !ok {
		return main
	}

	chunks, ok := extended[guid]
	if !ok || len(chunks) == 0 {
		return main
	}

	extBytes := mergeExtendedChunks(chunks)

	// Extract the rdf:Description elements from the extended XMP packet.
	// The extended packet is a self-contained XMP document; we want only the
	// RDF content between <rdf:Description and </rdf:RDF>.
	descStart := bytes.Index(extBytes, []byte("<rdf:Description"))
	closeRDFExt := bytes.LastIndex(extBytes, []byte("</rdf:RDF>"))
	if descStart < 0 || closeRDFExt < 0 || descStart >= closeRDFExt {
		return main // graceful degradation
	}
	extraDescs := extBytes[descStart:closeRDFExt]

	// Splice extraDescs into main immediately before its </rdf:RDF> close tag.
	mainCloseRDF := bytes.LastIndex(main, []byte("</rdf:RDF>"))
	if mainCloseRDF < 0 {
		return main
	}

	result := make([]byte, 0, len(main)+len(extraDescs))
	result = append(result, main[:mainCloseRDF]...)
	result = append(result, extraDescs...)
	result = append(result, main[mainCloseRDF:]...)
	return result
}

// isStandalone reports whether m is a marker that has no length / data field.
func isStandalone(m byte) bool {
	return m == markerSOI || m == markerEOI ||
		(m >= 0xD0 && m <= 0xD7) || // RST0–RST7
		m == 0x01 // TEM
}
