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
	"strings"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
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
	extFullLens map[string]uint64,
	extTruncated bool,
) (map[string][]extChunk, map[string]uint64, map[string]uint64, bool) {
	rawGUID := body[:32]
	// #135: validate that the GUID is exactly 32 hex characters and canonicalise to
	// uppercase before keying the map. Adobe XMP Specification Part 3 §1.1.4 defines
	// the GUID as a 32-character uppercase hex string; real-world writers may emit
	// lowercase. A case mismatch between the main packet's HasExtendedXMP attribute
	// and the chunk GUID would silently discard all extended properties without this
	// normalisation.
	if !isAllHex(rawGUID) {
		// GUID contains non-hex characters — discard this chunk silently.
		return extended, extSizes, extFullLens, extTruncated
	}
	guid := strings.ToUpper(string(rawGUID))
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
	if extFullLens == nil {
		extFullLens = make(map[string]uint64)
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
			return extended, extSizes, extFullLens, true // extTruncated
		}
		if fullLen > maxExtendedXMPTotal {
			return extended, extSizes, extFullLens, true // extTruncated
		}
		extSizes[guid] = 0 // mark as seen with zero bytes accumulated
		// #122: record the wire-declared total size so buildXMPResult can validate
		// that the assembled chunks cover exactly fullLen bytes with no gaps or
		// overlaps. Adobe XMP Specification Part 3 §1.1.4.
		extFullLens[guid] = fullLen
	}

	// Per-chunk running total check: drop this chunk if it would exceed the cap.
	accumulated := extSizes[guid]
	chunkSize := uint64(len(chunkData))
	if accumulated+chunkSize > maxExtendedXMPTotal {
		return extended, extSizes, extFullLens, true // extTruncated
	}

	// Copy chunk data: body aliases scratch and must outlive this loop.
	extSizes[guid] = accumulated + chunkSize
	extended[guid] = append(extended[guid], extChunk{
		offset: offset,
		data:   bytes.Clone(chunkData),
	})
	return extended, extSizes, extFullLens, extTruncated
}

// processAPP1Segment dispatches an APP1 segment payload to the appropriate
// metadata bucket (EXIF, standard XMP, or extended XMP).
//
// It returns updated values for rawEXIF, rawXMP, the extended chunk map,
// the per-GUID accumulated byte counter map, the per-GUID declared total map,
// and the extTruncated flag. Pass-through values are returned unchanged when
// the segment does not apply.
//
// Extended XMP DoS mitigation is delegated to appendExtendedXMPChunk (#40).
func processAPP1Segment(
	data, rawEXIF, rawXMP []byte,
	extended map[string][]extChunk,
	extSizes map[string]uint64,
	extFullLens map[string]uint64,
	extTruncated bool,
) ([]byte, []byte, map[string][]extChunk, map[string]uint64, map[string]uint64, bool) {
	switch {
	case bytes.HasPrefix(data, identExif):
		// EXIF payload begins after the 6-byte "Exif\x00\x00" header.
		// TIFF §2: a valid TIFF stream requires at least 8 bytes (2-byte byte-order
		// mark + 2-byte magic number + 4-byte IFD0 offset). A shorter residual is
		// structurally corrupt and must not be returned as rawEXIF — callers rely on
		// the invariant that rawEXIF is nil or >= 8 bytes.
		//
		// §1(f) first-wins policy: when multiple APP1 segments carry an Exif\x00\x00
		// prefix, only the first valid one is used. Subsequent EXIF APP1 segments are
		// ignored. ITU-T T.81 + containers.md §1(f): "multiple Exif\0\0 (use first)".
		if rawEXIF == nil {
			if payload := data[len(identExif):]; len(payload) >= 8 {
				// Copy: data aliases scratch and must survive the next readSegment call.
				rawEXIF = bytes.Clone(payload)
			}
		}

	case bytes.HasPrefix(data, identXMP):
		// §1(f) first-wins: only the first valid standard XMP APP1 is used.
		// Adobe XMP Specification Part 3 §1.1.3: a JPEG contains at most one
		// standard XMP packet; additional standard APP1 segments are non-conforming.
		if rawXMP == nil {
			// Copy: same reason as rawEXIF.
			rawXMP = bytes.Clone(data[len(identXMP):])
		}

	case bytes.HasPrefix(data, identXMPNote):
		// Extended XMP chunk: GUID (32 bytes) + fullLength (4 bytes) +
		// offset (4 bytes) + chunk data. Adobe XMP Spec Part 3 §1.1.4.
		if body := data[len(identXMPNote):]; len(body) >= 40 {
			extended, extSizes, extFullLens, extTruncated = appendExtendedXMPChunk(body, extended, extSizes, extFullLens, extTruncated)
		}
	}

	return rawEXIF, rawXMP, extended, extSizes, extFullLens, extTruncated
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
// truncated is set when extended XMP chunks were dropped due to size cap or
// structural invalidity; callers may convert this to a ParseWarning. (#134).
type xmpResult struct {
	rawXMP     []byte // reassembled, user-visible
	rawXMPWire []byte // wire-frame for lossless passthrough writes (may be nil)
	truncated  bool   // #134: extended XMP was capped or structurally invalid
}

// buildXMPResult constructs an xmpResult from the raw main packet and any
// extended chunks collected during segment scanning.
//
// When extended is nil or empty: rawXMP = main, rawXMPWire = nil.
// When extended chunks are present and valid: rawXMP = reassembled(main, ext),
// rawXMPWire = encodeXMPWire(main, assembledExt).
//
// extTruncated is forwarded to the returned xmpResult.truncated field.
// extFullLens carries the wire-declared total size per GUID for validation (#122).
//
// wantXMP controls whether the expensive reassembly (parse both packets,
// merge properties, re-encode) is performed at all (#238). When false (the
// caller opted out via WithoutXMP), rawXMP degrades to the main packet only;
// rawXMPWire — computed before reassembly either way — still carries the
// original main+extended payload, so a JPEG round-trip write stays byte
// stable (encodeXMP prefers rawXMPWire for JPEG destinations). extTruncated
// is still computed and returned regardless of wantXMP, so warnings for
// wanted segments are unaffected.
func buildXMPResult(rawXMP []byte, extended map[string][]extChunk, extFullLens map[string]uint64, extTruncated, wantXMP bool) xmpResult {
	if rawXMP == nil || len(extended) == 0 {
		return xmpResult{rawXMP: rawXMP, truncated: extTruncated}
	}

	guid, found := extractGUIDFromMain(rawXMP)
	if !found {
		return xmpResult{rawXMP: rawXMP, truncated: extTruncated}
	}

	// #135: canonicalize the GUID from the main packet to uppercase so it
	// matches the normalised keys stored by appendExtendedXMPChunk.
	// Adobe XMP Specification Part 3 §1.1.4: GUID is 32 uppercase hex chars.
	guid = strings.ToUpper(guid)

	chunks, ok := extended[guid]
	if !ok || len(chunks) == 0 {
		return xmpResult{rawXMP: rawXMP, truncated: extTruncated}
	}

	// #122: validate chunk layout before assembling. Adobe XMP Specification
	// Part 3 §1.1.4: chunks must cover [0, fullLen) contiguously with no gaps,
	// overlaps, or duplicated offsets. On any violation degrade gracefully to
	// the main (standard) XMP packet and mark truncated.
	declaredTotal, hasDeclared := extFullLens[guid]
	extBytes, valid := mergeExtendedChunksValidated(chunks, declaredTotal, hasDeclared)
	if !valid {
		// Chunk layout is corrupt; return main packet only and signal truncation.
		return xmpResult{rawXMP: rawXMP, truncated: true}
	}

	// Build wire-frame BEFORE any reassembly, so that the original raw bytes
	// are preserved verbatim for passthrough writes.
	wire := encodeXMPWire(rawXMP, extBytes)

	if !wantXMP {
		// #238: skip the reassembly parse/merge/encode entirely when the
		// caller does not want XMP. wire already carries the complete
		// main+extended payload for a byte-stable JPEG round trip.
		return xmpResult{rawXMP: rawXMP, rawXMPWire: wire, truncated: extTruncated}
	}

	// #123: Use xmp.Parse to merge the extended document into the main packet
	// in a prefix-agnostic way. The extended XMP document is a full XMP packet
	// that may bind the RDF namespace to any prefix (e.g. R:, RDF:); a literal
	// string search for "<rdf:Description" would silently drop all properties
	// when a non-canonical prefix is used. Parsing both packets and merging
	// Properties maps handles all namespace prefix assignments correctly.
	// Adobe XMP Specification Part 3 §1.1.4: the assembler must merge the
	// extended properties into the main XMP packet.
	//
	// Fall back to the byte-splice reassembler when xmp.Parse fails on either
	// packet (e.g. deeply malformed XML), so that the common "rdf:" prefix case
	// is always served.
	reassembled := reassembleExtendedXMPByParse(rawXMP, extBytes)
	if reassembled == nil {
		// xmp.Parse fallback: use the old byte-splice method.
		extMap := map[string][]extChunk{guid: {{offset: 0, data: extBytes}}}
		reassembled = reassembleExtendedXMP(rawXMP, extMap)
	}

	return xmpResult{rawXMP: reassembled, rawXMPWire: wire, truncated: extTruncated}
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
//
// iptcDigest is a 16-byte MD5 slice when the IRB contained a 0x0425 resource,
// or nil when absent. MWG §3.3.1.
//
// err is non-nil only for ErrFileTooLarge (task #262): either the cumulative
// bytes read from r exceeded maxFileSize (countingReader, limits.go), or the
// aggregate size of all accumulated Photoshop APP13 payloads did. All other
// read failures (EOF, malformed markers) are handled by the pre-existing
// graceful-degradation policy — the function returns whatever metadata was
// collected so far with err == nil, exactly as before this change.
//
// wantIPTC and wantXMP (#238) let the caller skip work that only feeds a
// segment it has opted out of via WithoutIPTC/WithoutXMP: the 0x0425 IPTC
// digest extraction and the extended-XMP reassembly, respectively. rawEXIF
// and rawIPTC themselves are always extracted regardless of these flags —
// they must remain available so an unmodified Write can pass them through
// byte-for-byte (CLAUDE.md §5).
func scanMetadataSegmentsWithWire(r io.Reader, scratchPtr *[]byte, wantIPTC, wantXMP bool) (rawEXIF, rawIPTC, iptcDigest []byte, xmp xmpResult, err error) {
	// extended collects chunks from extended XMP APP1 segments, keyed by GUID.
	// Adobe XMP Specification Part 3 §1.1.4.
	// Lazily initialised: most JPEGs do not contain extended XMP, so we avoid
	// the map allocation on the fast path.
	//
	// extSizes tracks accumulated byte totals per GUID for the #40 DoS cap.
	// extFullLens stores the wire-declared total size per GUID for #122 validation.
	// extTruncated is set true when any GUID's payload was capped or invalid.
	var extended map[string][]extChunk
	var extSizes map[string]uint64
	var extFullLens map[string]uint64
	var extTruncated bool
	var mainXMP []byte

	// IRB-APP13-09 / ROBUST-15 (iptc.md §2.4, §5): multiple APP13 "Photoshop 3.0"
	// segments must be concatenated, not overwritten. The spec states that when a
	// JPEG carries more than one APP13 segment all Photoshop payloads are
	// concatenated in order to form a single logical IRB. This allows IPTC data
	// split across segments (e.g. by legacy tools that split large APP13 payloads
	// at the 65533-byte limit) to be discovered correctly.
	//
	// We accumulate raw Photoshop payloads here; the 0x0404 IRB search runs once
	// over the concatenated bytes after all segments have been collected.
	//
	// app13Total independently bounds the aggregate size of app13Payloads at
	// maxFileSize (task #262): individual APP13 segments are already bounded to
	// 65533 bytes by the 16-bit JPEG length field, but nothing previously
	// bounded how MANY such segments could be accumulated, so a flood of
	// small-but-numerous Photoshop APP13 segments could otherwise grow
	// app13Payloads without limit before the overall countingReader cap (which
	// bounds total bytes READ, not bytes retained) had a chance to trip on a
	// file that interleaves large non-APP13 segments to stay under the read cap.
	var app13Payloads [][]byte
	var app13Total int64

	for {
		marker, data, rerr := readSegment(r, scratchPtr)
		if rerr != nil {
			if errors.Is(rerr, ErrFileTooLarge) {
				// #262: the input exceeds maxFileSize. Unlike ordinary
				// malformed-stream errors, this must propagate as a hard
				// failure rather than degrade gracefully.
				return nil, nil, nil, xmpResult{}, rerr
			}
			// EOF and malformed-stream errors: degrade gracefully and
			// return whatever metadata has been collected so far.
			break
		}

		switch marker {
		case markerAPP1:
			rawEXIF, mainXMP, extended, extSizes, extFullLens, extTruncated = processAPP1Segment(
				data, rawEXIF, mainXMP, extended, extSizes, extFullLens, extTruncated,
			)
		case markerAPP13:
			// Accumulate each Photoshop APP13 payload (after stripping the header);
			// do NOT search for IPTC yet — the 0x0404 block might be in a later
			// segment (IRB-APP13-09).
			if bytes.HasPrefix(data, identPS) {
				// Copy the IRB portion (data aliases scratch).
				irb := data[len(identPS):]
				if len(irb) > 0 {
					if app13Total+int64(len(irb)) > maxFileSize {
						return nil, nil, nil, xmpResult{}, fmt.Errorf(
							"jpeg: aggregate APP13 payload exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
					}
					app13Payloads = append(app13Payloads, bytes.Clone(irb))
					app13Total += int64(len(irb))
				}
			}
		case markerSOS, markerEOI:
			// SOS/EOI: no more metadata segments follow.
			rawIPTC, iptcDigest = extractIPTCAndDigestFromIRBPayloads(app13Payloads, wantIPTC)
			return rawEXIF, rawIPTC, iptcDigest, buildXMPResult(mainXMP, extended, extFullLens, extTruncated, wantXMP), nil
		}
	}

	rawIPTC, iptcDigest = extractIPTCAndDigestFromIRBPayloads(app13Payloads, wantIPTC)
	return rawEXIF, rawIPTC, iptcDigest, buildXMPResult(mainXMP, extended, extFullLens, extTruncated, wantXMP), nil
}

// parseIRBForIPTCAndDigest scans a single contiguous Photoshop IRB byte block
// and returns both the 0x0404 IPTC-NAA data and the 0x0425 IPTC-Digest data.
// Either return value may be nil when the corresponding resource is absent.
//
// MWG §3.3.1: the 0x0425 resource carries the 16-byte MD5 of the raw 0x0404
// IIM stream at the time XMP was last written.
func parseIRBForIPTCAndDigest(b []byte) (iptcData, digestData []byte) {
	pos := 0
	for pos < len(b) {
		resourceID, data, newPos, ok := parseIRBEntry(b, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		switch resourceID {
		case 0x0404:
			iptcData = data
		case 0x0425:
			// MWG §3.3.1: 0x0425 payload is exactly 16 bytes (MD5 digest).
			// Tolerate malformed blocks: copy only if exactly 16 bytes.
			if len(data) == 16 {
				digestData = data
			}
		}
		pos = newPos
		// Apply even-padding to data block (Adobe IRB spec §"Image Resources").
		if len(data)%2 != 0 {
			pos++
		}
	}
	return iptcData, digestData
}

// extractIPTCAndDigestFromIRBPayloads searches the concatenated Photoshop IRB
// payloads for resource 0x0404 (IPTC-NAA IIM) and resource 0x0425 (IPTC
// Digest) and returns both. Either return value may be nil when absent.
//
// wantDigest controls whether the 0x0425 digest resource is copied out at
// all (#238): when the caller has opted out of IPTC (WithoutIPTC), the digest
// is only ever consumed by the MWG-02 trust-elevation computation, which is
// itself a no-op when there is no parsed IPTC to prioritise — so extracting
// it would be pure waste.
//
// IRB-APP13-09: all APP13 Photoshop 3.0 payloads are treated as a single
// logical IRB stream; both resources may appear in any segment.
// MWG §3.3.1: the digest enables IPTC/XMP precedence reconciliation.
func extractIPTCAndDigestFromIRBPayloads(payloads [][]byte, wantDigest bool) (rawIPTC, iptcDigest []byte) {
	if len(payloads) == 0 {
		return nil, nil
	}
	// Fast path: single segment (the common case) — no concatenation needed.
	if len(payloads) == 1 {
		iptcData, digestData := parseIRBForIPTCAndDigest(payloads[0])
		// #215: payloads[0] was already cloned from pooled scratch when
		// accumulated (scanMetadataSegmentsWithWire) and is not retained
		// anywhere else once this function returns, so the 0x0404 sub-slice
		// it contains can be returned directly — cloning it again would be a
		// second, redundant copy of the same bytes. This never aliases
		// pooled scratch (bug #72 class): payloads[0] is an independently
		// owned clone, not scratch itself.
		if !wantDigest {
			digestData = nil
		} else if digestData != nil {
			digestData = bytes.Clone(digestData)
		}
		return iptcData, digestData
	}
	// Slow path: concatenate all payloads and search the combined stream.
	var totalLen int
	for _, p := range payloads {
		totalLen += len(p)
	}
	combined := make([]byte, 0, totalLen)
	for _, p := range payloads {
		combined = append(combined, p...)
	}
	iptcData, digestData := parseIRBForIPTCAndDigest(combined)
	if iptcData != nil {
		iptcData = bytes.Clone(iptcData)
	}
	if !wantDigest {
		digestData = nil
	} else if digestData != nil {
		digestData = bytes.Clone(digestData)
	}
	return iptcData, digestData
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
	rawEXIF, rawIPTC, _, xmpRes, err := extractFullInternal(r, true, true)
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
	rawEXIF, rawIPTC, _, xmpRes, err := extractFullInternal(r, true, true)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return rawEXIF, rawIPTC, xmpRes.rawXMP, xmpRes.rawXMPWire, nil
}

// ExtractFull reads the JPEG marker stream and returns raw payloads, the
// optional IPTC digest from Photoshop resource 0x0425, the optional XMP
// wire-frame, and whether the extended XMP was truncated.
//
// iptcDigest is a 16-byte slice when present, nil when absent.
// xmpTruncated is true when the assembled extended XMP payload was capped at
// maxExtendedXMPTotal (16 MiB) or had structurally invalid chunk layout (#134).
// When xmpTruncated is true rawXMP contains the main (standard) XMP packet only;
// callers that surface warnings should record ErrExtendedXMPTruncated.
//
// MWG Guidelines v2.0 §3.3.1: the caller should compare the digest to
// iptc.Digest(rawIPTC) to determine whether IPTC or XMP has read priority.
// Use Extract for callers that do not need the digest, the wire-frame, or the
// truncation flag.
func ExtractFull(r io.ReadSeeker) (rawEXIF, rawIPTC, iptcDigest, rawXMP, rawXMPWire []byte, xmpTruncated bool, err error) {
	return ExtractFullSelective(r, true, true)
}

// ExtractFullSelective is the selective-extraction variant of ExtractFull
// (#238). wantIPTC and wantXMP let a caller that will not parse a segment
// (WithoutIPTC / WithoutXMP) skip the work that only feeds that segment:
//   - wantIPTC=false: the 0x0425 IPTC digest is not extracted, so the
//     downstream MWG-02 trust computation (a no-op when there is no parsed
//     IPTC to prioritise) is also skipped.
//   - wantXMP=false: the extended-XMP reassembly (parse both packets, merge
//     properties, re-encode) is skipped; rawXMP degrades to the main packet
//     only, while rawXMPWire still carries the full main+extended payload so
//     a JPEG round-trip write remains byte-stable.
//
// rawEXIF and rawIPTC are always extracted regardless of the flags: both
// must remain available so an unmodified Write can pass them through
// byte-for-byte (CLAUDE.md §5). ExtractFull calls this with both flags true.
func ExtractFullSelective(r io.ReadSeeker, wantIPTC, wantXMP bool) (rawEXIF, rawIPTC, iptcDigest, rawXMP, rawXMPWire []byte, xmpTruncated bool, err error) {
	rawEXIF, rawIPTC, iptcDigest, xmpRes, err := extractFullInternal(r, wantIPTC, wantXMP)
	if err != nil {
		return nil, nil, nil, nil, nil, false, err
	}
	return rawEXIF, rawIPTC, iptcDigest, xmpRes.rawXMP, xmpRes.rawXMPWire, xmpRes.truncated, nil
}

// extractFullInternal is the shared implementation of Extract, ExtractWithWire, and ExtractFull.
func extractFullInternal(r io.ReadSeeker, wantIPTC, wantXMP bool) (rawEXIF, rawIPTC, iptcDigest []byte, xmp xmpResult, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, xmpResult{}, fmt.Errorf("jpeg: seek: %w", err)
	}

	// #262: wrap r so the total bytes read across the SOI check and the
	// marker scan are bounded by maxFileSize, mirroring the aggregate
	// input-size cap every sibling format package enforces. See limits.go
	// for why this preserves the streaming design and io.ReadSeeker
	// semantics instead of buffering the whole file.
	cr := getCountingReader(r)
	defer putCountingReader(cr)

	// Obtain a pooled scratch buffer first so the SOI read can reuse it,
	// avoiding the heap escape that occurs when a stack-allocated [2]byte
	// is passed to io.ReadFull via the io.Reader interface.
	scratchPtr := iobuf.Get(4096)
	defer func() { iobuf.Put(scratchPtr) }()

	// Read and verify SOI using the pooled scratch buffer.
	soi := (*scratchPtr)[:2]
	if _, err = io.ReadFull(cr, soi); err != nil {
		return nil, nil, nil, xmpResult{}, fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if soiErr := readSOI(soi); soiErr != nil {
		return nil, nil, nil, xmpResult{}, soiErr
	}

	rawEXIF, rawIPTC, iptcDigest, xmp, err = scanMetadataSegmentsWithWire(cr, scratchPtr, wantIPTC, wantXMP)
	if err != nil {
		return nil, nil, nil, xmpResult{}, err
	}
	return rawEXIF, rawIPTC, iptcDigest, xmp, nil
}

// writeEXIFSegment writes the EXIF APP1 segment to w.
// APP1 length field is 16-bit; JPEG ISO/IEC 10918-1 and EXIF §4.5.4.
//
// #216: the segment header, "Exif\x00\x00" prefix, and rawEXIF bytes are all
// built in one pooled buffer so the whole segment leaves in a single w.Write.
func writeEXIFSegment(w io.Writer, rawEXIF []byte) error {
	if len(identExif)+len(rawEXIF)+2 > 65535 {
		return fmt.Errorf("jpeg: EXIF payload %d bytes exceeds APP1 segment limit; EXIF cannot be split: %w", len(rawEXIF), ErrEXIFPayloadTooLarge)
	}
	bufPtr := iobuf.Get(segHeaderSize + len(identExif) + len(rawEXIF))
	b := *bufPtr
	copy(b[segHeaderSize:], identExif)
	copy(b[segHeaderSize+len(identExif):], rawEXIF)
	return writeHeaderedBuf(w, markerAPP1, bufPtr)
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
		// Fast path: XMP fits in a single APP1 segment. #216: header + prefix
		// + payload built in one pooled buffer, one w.Write.
		bufPtr := iobuf.Get(segHeaderSize + len(identXMP) + len(rawXMP))
		b := *bufPtr
		copy(b[segHeaderSize:], identXMP)
		copy(b[segHeaderSize+len(identXMP):], rawXMP)
		return writeHeaderedBuf(w, markerAPP1, bufPtr)
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
	bufPtr := iobuf.Get(segHeaderSize + totalLen)
	b := *bufPtr
	copy(b[segHeaderSize:], identXMP)
	copy(b[segHeaderSize+len(identXMP):], main)
	return writeHeaderedBuf(w, markerAPP1, bufPtr)
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

		// #216: segment header + extended-XMP header + chunk data built in
		// one pooled buffer, one w.Write. base is the offset of the
		// extended-XMP header within the buffer, after the 4-byte segment
		// header reserved at the front.
		bufPtr := iobuf.Get(segHeaderSize + extHdrSize + len(chunk))
		b := *bufPtr
		const base = segHeaderSize

		// identXMPNote (35 bytes: "http://ns.adobe.com/xmp/extension/\x00")
		copy(b[base:], identXMPNote)
		// GUID (32 bytes) immediately after identifier
		copy(b[base+len(identXMPNote):], guidBytes)
		// fullLength (4 bytes BE) at offset base+67 = base+35+32
		binary.BigEndian.PutUint32(b[base+67:base+71], fullLen)
		// offset (4 bytes BE) at offset base+71 = base+35+32+4
		binary.BigEndian.PutUint32(b[base+71:base+75], offset)
		// chunk data starts at offset base+75 = base+35+32+4+4
		copy(b[base+75:], chunk)

		if err := writeHeaderedBuf(w, markerAPP1, bufPtr); err != nil {
			return err
		}

		offset = chunkEnd
	}
	return nil
}

// writeIPTCSegmentRaw strips the 0x0404 (IPTC-NAA) resource from origIRB
// while preserving every other 8BIM sibling resource verbatim, and writes the
// result as an APP13 segment when any sibling resource remains (#174). No
// segment is emitted when the strip leaves nothing behind (origIRB carried
// only the 0x0404 block).
//
// #216, #218: the segment header, "Photoshop 3.0\x00" prefix, and the
// spliced IRB bytes are all built directly in one pooled buffer, so the
// whole segment leaves in a single w.Write with no single-use intermediate
// allocation for the IRB bytes themselves.
func writeIPTCSegmentRaw(w io.Writer, origIRB []byte) error {
	bufPtr := iobuf.Get(segHeaderSize + len(identPS) + len(origIRB))
	b := (*bufPtr)[:segHeaderSize+len(identPS)]
	copy(b[segHeaderSize:], identPS)
	b = appendSplicedIRB(b, origIRB, nil) // strip 0x0404, keep siblings
	if len(b) == segHeaderSize+len(identPS) {
		// Nothing survived the strip: no APP13 segment should be emitted.
		iobuf.Put(bufPtr)
		return nil
	}
	bodyLen := len(b) - segHeaderSize
	if bodyLen+2 > 65535 {
		iobuf.Put(bufPtr)
		return fmt.Errorf("jpeg: IRB sibling payload %d bytes exceeds APP13 segment limit: %w", bodyLen-len(identPS), ErrIPTCPayloadTooLarge)
	}
	*bufPtr = b
	return writeHeaderedBuf(w, markerAPP13, bufPtr)
}

// writeIPTCSegment wraps the IPTC IIM stream in a Photoshop IRB block and
// writes it as an APP13 segment. APP13 length field is 16-bit; EXIF §4.5.6.
//
// When origIRB is non-nil it is used as the base IRB: the 0x0404 resource
// block within it is replaced with one built from rawIPTC while every other
// 8BIM resource in origIRB is copied verbatim (CLAUDE.md §5: write operations
// must preserve all existing metadata not explicitly modified). When origIRB is
// nil a bare 0x0404-only IRB is built from rawIPTC.
//
// #216, #218: the segment header, "Photoshop 3.0\x00" prefix, and the
// spliced/built IRB bytes are all appended directly into one pooled buffer,
// so the whole segment leaves in a single w.Write.
func writeIPTCSegment(w io.Writer, rawIPTC, origIRB []byte) error {
	// Upper-bound estimate: identPS + origIRB (block replaced in place, not
	// grown) + the new 0x0404 wrapper (12-byte header + rawIPTC + 1 pad byte).
	bufPtr := iobuf.Get(segHeaderSize + len(identPS) + len(origIRB) + len(rawIPTC) + 13)
	b := (*bufPtr)[:segHeaderSize+len(identPS)]
	copy(b[segHeaderSize:], identPS)
	if origIRB != nil {
		b = appendSplicedIRB(b, origIRB, rawIPTC)
	} else {
		b = appendIRBBlock(b, rawIPTC)
	}
	bodyLen := len(b) - segHeaderSize
	if bodyLen+2 > 65535 {
		iobuf.Put(bufPtr)
		return fmt.Errorf("jpeg: IPTC IRB payload %d bytes exceeds APP13 segment limit: %w", bodyLen-len(identPS), ErrIPTCPayloadTooLarge)
	}
	*bufPtr = b
	return writeHeaderedBuf(w, markerAPP13, bufPtr)
}

// appendIRBBlock appends a minimal Photoshop IRB block (resource ID 0x0404,
// empty Pascal name) wrapping iptcData onto dst and returns the extended
// slice. Produces the same bytes as buildIRB without a single-use allocation
// when dst already has spare capacity (#218).
func appendIRBBlock(dst, iptcData []byte) []byte {
	size := len(iptcData)
	//nolint:gosec // G115: byte extraction from int size value; shifts are safe bit extractions
	dst = append(dst,
		'8', 'B', 'I', 'M', // 8BIM marker
		0x04, 0x04, // resource ID 0x0404
		0x00, 0x00, // empty pascal name (length 0 + padding byte)
		byte(size>>24), byte(size>>16), byte(size>>8), byte(size), // data length
	)
	dst = append(dst, iptcData...)
	if size%2 != 0 {
		dst = append(dst, 0x00) // pad data to even boundary
	}
	return dst
}

// buildIRB wraps a raw IPTC IIM stream in a minimal Photoshop IRB block
// (resource ID 0x0404) ready for embedding in APP13.
func buildIRB(iptcData []byte) []byte {
	size := len(iptcData)
	// 4 (8BIM) + 2 (ID) + 2 (empty pascal name) + 4 (data size) + data [+ padding]
	return appendIRBBlock(make([]byte, 0, 12+size+(size%2)), iptcData)
}

// appendSplicedIRB appends to dst an IRB byte sequence identical to origIRB
// except the 0x0404 (IPTC-NAA) resource block is replaced with a freshly
// built block wrapping newIPTCData; every other 8BIM block is copied
// verbatim in its original order and with its original padding. Returns the
// extended slice.
//
// When newIPTCData is nil the 0x0404 block is removed rather than replaced.
// All sibling resources are preserved in both cases. This behaviour is
// required by #174: a nil rawIPTC to Inject must strip only the IPTC data
// while keeping Photoshop siblings such as the 0x0425 digest resource.
//
// If origIRB contains no 0x0404 block and newIPTCData is non-nil, the new
// block is appended at the end.
//
// EXIF §4.5.6: each 8BIM block is 4 ('8BIM') + 2 (ID) + pascal-name + 4 (size)
// + data [+ 1 padding if data size is odd].
//
// #218: appends directly onto the caller's buffer (typically a pooled
// segment buffer) instead of allocating a new one, eliminating the
// single-use intermediate that spliceIPTCIntoIRB otherwise requires.
func appendSplicedIRB(dst, origIRB, newIPTCData []byte) []byte {
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
			// Replace the IPTC block with a freshly built one, or drop it
			// entirely when newIPTCData is nil (strip-only mode).
			if newIPTCData != nil {
				dst = appendIRBBlock(dst, newIPTCData)
			}
			replaced = true
		} else {
			// Copy the block verbatim — 8BIM header + data + any padding byte.
			// blockEnd is bounded by origIRB length (validated inside parseIRBEntry).
			if blockEnd > len(origIRB) {
				blockEnd = len(origIRB)
			}
			dst = append(dst, origIRB[entryStart:blockEnd]...)
		}

		pos = blockEnd
	}

	if !replaced && newIPTCData != nil {
		// No 0x0404 block was found in the original IRB and we have a
		// replacement: append the new one at the end. When newIPTCData is
		// nil (remove-only mode) there is nothing to append; dst already
		// holds the siblings-only result.
		dst = appendIRBBlock(dst, newIPTCData)
	}
	return dst
}

// spliceIPTCIntoIRB returns a new IRB byte slice built by appendSplicedIRB;
// see that function for the full replacement/removal semantics. Kept as a
// standalone, freshly-allocated entry point for callers (and tests) that do
// not have a pooled destination buffer to append into.
func spliceIPTCIntoIRB(origIRB, newIPTCData []byte) []byte {
	var newBlockLen int
	if newIPTCData != nil {
		newBlockLen = 12 + len(newIPTCData) + len(newIPTCData)%2
	}
	out := make([]byte, 0, len(origIRB)+newBlockLen)
	return appendSplicedIRB(out, origIRB, newIPTCData)
}

// writeNewMetadataSegments writes EXIF APP1, XMP APP1 (with extended-XMP
// splitting when the payload exceeds the single-segment limit), and IPTC
// APP13 segments to w. Returns the first error encountered.
//
// origIRB is the full Photoshop IRB block (the bytes after the "Photoshop
// 3.0\x00" header) from the source JPEG, or nil if the source had no APP13.
// When non-nil it is used by writeIPTCSegment to preserve sibling 8BIM
// resources while replacing only the 0x0404 block.
//
// #174: when rawIPTC is nil but origIRB is non-nil, the 0x0404 block is
// removed while all sibling 8BIM resources are preserved. The resulting APP13
// segment is only written when the stripped IRB is non-empty (i.e. when the
// original APP13 contained resources other than 0x0404). If the stripped IRB
// is empty (0x0404 was the only resource) no APP13 segment is emitted.
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
	if err := writeIPTCOrSiblings(w, rawIPTC, origIRB); err != nil {
		return err
	}
	return nil
}

// writeIPTCOrSiblings handles the IPTC / Photoshop-sibling write decision.
//
//   - rawIPTC non-nil  → write APP13 with rawIPTC spliced into origIRB (or bare).
//   - rawIPTC nil, origIRB non-nil → strip 0x0404, write APP13 with remaining siblings.
//   - rawIPTC nil, origIRB nil  → no APP13 emitted.
//
// This helper exists solely to satisfy the nestif linter; the logic matches
// the comment in writeNewMetadataSegments (#174).
func writeIPTCOrSiblings(w io.Writer, rawIPTC, origIRB []byte) error {
	if rawIPTC != nil {
		return writeIPTCSegment(w, rawIPTC, origIRB)
	}
	if origIRB == nil {
		return nil
	}
	// rawIPTC is nil but the source had Photoshop siblings: strip only 0x0404
	// and preserve the rest. writeIPTCSegmentRaw strips the 0x0404 block
	// internally and emits nothing when no sibling resources remain.
	// Adobe Photoshop IRB spec §"Image Resources": all non-IPTC resources must
	// survive a nil-IPTC write (#174).
	return writeIPTCSegmentRaw(w, origIRB)
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
//
// #216: the 2-byte marker is written into a pooled buffer instead of a stack
// composite literal, which would otherwise escape to the heap on every call
// through the io.Writer interface boundary.
func writeMarker(w io.Writer, marker byte) error {
	bufPtr := iobuf.Get(2)
	b := *bufPtr
	b[0], b[1] = 0xFF, marker
	_, err := w.Write(b)
	iobuf.Put(bufPtr)
	if err != nil {
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
	return writeSegmentCopy(w, marker, data)
}

// writeSOS writes the SOS segment and then copies the remaining compressed
// image data from r to w verbatim.
func writeSOS(r io.Reader, w io.Writer, data []byte) error {
	if err := writeSegmentCopy(w, markerSOS, data); err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("jpeg: copy image data: %w", err)
	}
	return nil
}

// isAppMarker reports whether m is an APPn marker (APP0–APP15, markers
// 0xE0–0xEF). These segments carry optional application-specific data and are
// never required for image decoding.
//
// JPEG ISO/IEC 10918-1 §B.2.4.6: APPn markers are application extension
// segments. Only the metadata-carrying APP1 (EXIF/XMP) and APP13 (Photoshop
// IPTC) segments are explicitly handled by this library; all others are
// "unknown" application payloads from the library's perspective.
func isAppMarker(m byte) bool {
	return m >= 0xE0 && m <= 0xEF
}

// copyNonMetadataSegments reads segments from r, skips old metadata APP
// segments, and passes the rest through to w. It terminates on SOS (copying
// the compressed stream verbatim) or EOI.
//
// When preserveUnknownSegments is false, APPn segments (APP0–APP15) that are
// not the recognised metadata segments (EXIF APP1, XMP APP1, IPTC APP13) are
// also dropped. Structural markers, DQT, DHT, SOF, SOS, and compressed image
// data are never dropped regardless of this flag.
func copyNonMetadataSegments(r io.Reader, w io.Writer, scratch *[]byte, preserveUnknownSegments bool) error {
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

		// When preserveUnknownSegments is false, drop any APPn segment that
		// is not a recognised metadata segment. isOldMetadataSegment already
		// handles EXIF APP1, XMP APP1 (standard and extended), and
		// Photoshop APP13 — so any remaining APPn here is "unknown" to the
		// library (e.g. APP0/JFIF, APP2/ICC, APP12/Ducky, APP14/Adobe DCT).
		//
		// JPEG ISO/IEC 10918-1 §B.2.4.6: APPn markers are application
		// extension segments; no APPn is required for image decoding.
		if !preserveUnknownSegments && isAppMarker(marker) {
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

// irbHasSibling reports whether an IRB payload contains any 8BIM resource
// other than 0x0404 (IPTC-NAA). It performs no allocation. Used by
// probeIRBHasSiblings (#217) to decide whether extractOriginalIRB's
// preserving clone pass is actually necessary.
func irbHasSibling(b []byte) bool {
	pos := 0
	for pos < len(b) {
		resourceID, data, newPos, ok := parseIRBEntry(b, pos)
		if !ok {
			if newPos == pos {
				pos++
				continue
			}
			break
		}
		if resourceID != 0x0404 {
			return true
		}
		pos = newPos
		if len(data)%2 != 0 {
			pos++
		}
	}
	return false
}

// probeIRBHasSiblings performs a single zero-copy read pass over the JPEG's
// Photoshop APP13 segments to determine whether any of them carries an 8BIM
// resource besides 0x0404 (a sibling that must survive #174, e.g. the 0x0425
// IPTC digest, a 0x040C thumbnail, or a 0x040F clipping path).
//
// #217: Inject's pre-scan exists solely to preserve such siblings when
// rawIPTC replaces the 0x0404 block; when no sibling is present the
// preserving clone pass (extractOriginalIRB) would produce exactly the bytes
// a bare buildIRB(rawIPTC) already produces, so it is skipped entirely. This
// probe answers that question with no allocation beyond the caller's
// existing scratch buffer: each segment's payload is inspected in place and
// discarded before the next readSegment call.
//
// The caller is responsible for seeking r back to the desired position after
// this call. err is non-nil only for a Seek failure or ErrFileTooLarge (task
// #262), mirroring extractOriginalIRB.
func probeIRBHasSiblings(r io.ReadSeeker, scratch *[]byte) (hasSiblings bool, err error) {
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		return false, fmt.Errorf("jpeg: seek: %w", err)
	}
	for {
		marker, data, rerr := readSegment(r, scratch)
		if rerr != nil {
			if errors.Is(rerr, ErrFileTooLarge) {
				return false, rerr
			}
			return false, nil
		}
		switch marker {
		case markerAPP13:
			if bytes.HasPrefix(data, identPS) && irbHasSibling(data[len(identPS):]) {
				return true, nil
			}
		case markerSOS, markerEOI:
			return false, nil
		}
	}
}

// extractOriginalIRB performs a pre-scan of the JPEG in r to locate ALL
// Photoshop APP13 segments and returns the concatenated IRB bytes (the content
// after each "Photoshop 3.0\x00" header). Returns nil when no APP13 is present
// or no segment carries the Photoshop prefix.
//
// Adobe Photoshop IRB specification / IPTC IRB-APP13-09: when a JPEG carries
// more than one APP13 Photoshop segment, all payloads are concatenated in order
// to form a single logical IRB. This function mirrors the read-path
// app13Payloads accumulation in scanMetadataSegmentsWithWire so that sibling
// 8BIM resources split across multiple APP13 segments are all preserved when
// Inject rewrites the file (#174).
//
// The caller is responsible for seeking r back to the desired position after
// this call. scratch is used as an internal read buffer and must not be nil.
//
// err is non-nil for a Seek failure, or for ErrFileTooLarge (task #262):
// either the cumulative bytes read from r since the caller's last Seek
// exceeded maxFileSize (countingReader, limits.go), or the aggregate size of
// all accumulated Photoshop APP13 payloads did. All other read failures
// (EOF, malformed markers) degrade gracefully exactly as before this change
// (err == nil, whatever payloads were collected so far are returned).
func extractOriginalIRB(r io.ReadSeeker, scratch *[]byte) ([]byte, error) { //nolint:cyclop,gocyclo // multi-APP13 accumulation requires the extra branches; complexity is essential not accidental
	// Seek past the SOI (already validated by the caller — 2 bytes). A failure
	// here is surfaced rather than swallowed: the caller's immediately
	// preceding Seek(0, ...) just succeeded on the same reader, so a failure
	// on this second Seek signals a genuinely broken io.ReadSeeker rather than
	// a benign, ignorable condition.
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		return nil, fmt.Errorf("jpeg: seek: %w", err)
	}
	// #174: accumulate payloads from ALL Photoshop APP13 segments, not just
	// the first. The logical IRB is the concatenation of all APP13 payloads.
	//
	// app13Total independently bounds the aggregate size of payloads at
	// maxFileSize (task #262), mirroring the identical guard in
	// scanMetadataSegmentsWithWire.
	var payloads [][]byte
	var app13Total int64
	for {
		marker, data, err := readSegment(r, scratch)
		if err != nil {
			if errors.Is(err, ErrFileTooLarge) {
				return nil, err
			}
			break
		}
		switch marker {
		case markerAPP13:
			if bytes.HasPrefix(data, identPS) {
				irb := data[len(identPS):]
				if len(irb) > 0 {
					if app13Total+int64(len(irb)) > maxFileSize {
						return nil, fmt.Errorf(
							"jpeg: aggregate APP13 payload exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
					}
					payloads = append(payloads, bytes.Clone(irb))
					app13Total += int64(len(irb))
				}
			}
		case markerSOS, markerEOI:
			goto done
		}
	}
done:
	if len(payloads) == 0 {
		return nil, nil
	}
	if len(payloads) == 1 {
		return payloads[0], nil
	}
	// Concatenate all payloads into one logical IRB.
	var total int
	for _, p := range payloads {
		total += len(p)
	}
	combined := make([]byte, 0, total)
	for _, p := range payloads {
		combined = append(combined, p...)
	}
	return combined, nil
}

// resolveOrigIRB determines the original Photoshop IRB bytes that Inject
// should use to preserve sibling 8BIM resources (#174) when rawIPTC replaces
// or removes the 0x0404 block. r must be positioned so that Seek(2,
// io.SeekStart) lands just past the SOI marker (extractOriginalIRB and
// probeIRBHasSiblings both seek there themselves).
//
// #217: when rawIPTC is a replacement (non-nil), the full preserving clone
// pass (extractOriginalIRB) is only necessary if the source IRB actually
// carries a resource besides 0x0404 — otherwise splicing would produce
// exactly the bytes buildIRB(rawIPTC) already produces on its own.
// probeIRBHasSiblings answers that question with a single zero-copy pass, so
// the clone pass runs only when it is actually needed. When rawIPTC is nil
// (remove-or-strip mode) the clone pass always runs: the stripped result
// must be known to decide whether any APP13 survives at all.
func resolveOrigIRB(r io.ReadSeeker, scratch *[]byte, rawIPTC []byte) ([]byte, error) {
	if rawIPTC == nil {
		return extractOriginalIRB(r, scratch)
	}
	hasSiblings, err := probeIRBHasSiblings(r, scratch)
	if err != nil {
		return nil, err
	}
	if !hasSiblings {
		return nil, nil
	}
	return extractOriginalIRB(r, scratch)
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
// When preserveUnknownSegments is false, APPn segments (APP0–APP15) that are
// not one of the three recognised metadata segments (EXIF APP1, XMP APP1,
// Photoshop APP13) are stripped from the output. This allows callers to remove
// unknown application payloads that may carry sensitive data. Structural
// markers (SOF, DQT, DHT), SOS, and compressed image data are never affected.
// The default (true) behaviour is byte-identical to previous releases.
//
// When the source JPEG carries a Photoshop APP13 segment that contains 8BIM
// resources in addition to the 0x0404 IPTC block (e.g. IPTC digest 0x0425,
// thumbnail 0x040C, ICC clipping path 0x040F), Inject preserves all sibling
// resources verbatim and only replaces the 0x0404 block with rawIPTC. When
// rawIPTC is nil and the source has a Photoshop APP13, the 0x0404 block is
// removed while all other 8BIM sibling resources are preserved (#174). When the
// source has no APP13 at all, no APP13 is emitted in the output.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	// #262: wrap r so the total bytes read across the IRB pre-scan, the SOI
	// check, and the main copy pass (including the passthrough io.Copy of the
	// compressed image data in writeSOS) are bounded by maxFileSize, mirroring
	// the aggregate input-size cap every sibling format package enforces. cr
	// resets its budget on every Seek, so the pre-scan and the main pass are
	// each independently bounded rather than sharing one combined budget — see
	// limits.go for why this is both safe and necessary to avoid false
	// positives on legitimate large files.
	cr := getCountingReader(r)
	defer putCountingReader(cr)

	if _, err := cr.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}

	// #216: acquire one pooled scratch buffer up front and reuse it across
	// the pre-scan, the SOI read, and the main copy pass. Each phase fully
	// consumes whatever it reads from scratch before the next phase begins,
	// so a single buffer is safely reused throughout, eliminating both the
	// separate stack-allocated SOI array and the extra Get/Put pair two
	// independently scoped scratch buffers would require.
	scratch := iobuf.Get(4096)
	defer iobuf.Put(scratch)

	// #174, #217: determine the original Photoshop IRB so sibling 8BIM
	// resources survive when rawIPTC replaces or removes the 0x0404 block,
	// skipping the preserving clone pass when it provably cannot be needed.
	// See resolveOrigIRB for the full rationale.
	origIRB, err := resolveOrigIRB(cr, scratch, rawIPTC)
	if err != nil {
		return err
	}

	// Seek back to the start for the main copy pass.
	if _, err := cr.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("jpeg: seek: %w", err)
	}

	// Read and write SOI using the pooled scratch buffer (#216): avoids the
	// stack-array escape that occurs when a fixed-size array is passed to
	// io.Reader/io.Writer through the interface boundary.
	soi := (*scratch)[:2]
	if _, err := io.ReadFull(cr, soi); err != nil {
		return fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if soi[0] != 0xFF || soi[1] != markerSOI {
		return ErrNotJPEG
	}
	if _, err := w.Write(soi); err != nil {
		return fmt.Errorf("jpeg: write segment: %w", err)
	}

	// Write new metadata segments before any existing ones.
	if err := writeNewMetadataSegments(w, rawEXIF, rawIPTC, rawXMP, origIRB); err != nil {
		return err
	}

	// Copy remaining segments, skipping old metadata APP segments.
	return copyNonMetadataSegments(cr, w, scratch, preserveUnknownSegments)
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

	// Step 3: write main APP1. #216: header + prefix + stub built in one
	// pooled buffer, one w.Write.
	bufPtr := iobuf.Get(segHeaderSize + len(identXMP) + len(mainXMP))
	b := *bufPtr
	copy(b[segHeaderSize:], identXMP)
	copy(b[segHeaderSize+len(identXMP):], mainXMP)
	if err := writeHeaderedBuf(w, markerAPP1, bufPtr); err != nil {
		return err
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
	// Bounds check after advancing by nameLen: a crafted/corrupt Pascal-name
	// length byte can push pos past the end of the buffer. Without this check
	// the function would return (pos, true) for an overrun, letting the caller
	// interpret a garbage position as valid and eventually triggering a terminal
	// break in parseIRB that silently drops all subsequent 8BIM blocks
	// (including a valid 0x0404 IPTC block). Returning false here allows
	// parseIRBEntry to signal a recoverable miss. EXIF §4.5.6.
	if pos > len(b) {
		return pos, false
	}
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
//   - signature mismatch / recoverable miss: returns (0, nil, entryPos, false) —
//     newPos == entryPos (the pos at call time) signals this; the caller may
//     advance by 1 to scan forward. This includes an overflowing Pascal-name,
//     which is treated as a recoverable miss so that later 8BIM blocks are not
//     silently dropped.
//   - structural failure (truncated data, bad size): returns with newPos > pos;
//     the caller treats this as terminal.
//
// IRB format: "8BIM" + 2-byte resource ID + Pascal string name + 4-byte size + data.
// EXIF §4.5.6.
func parseIRBEntry(b []byte, pos int) (resourceID uint16, data []byte, newPos int, ok bool) {
	entryPos := pos // saved so we can signal a recoverable miss (newPos == entryPos)

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
	// If the declared name length overflows the buffer, skipPascalString returns
	// ok=false. We treat this as a recoverable miss (return entryPos) rather than
	// a terminal failure so that parseIRB continues scanning for subsequent valid
	// 8BIM blocks (including the 0x0404 IPTC resource). EXIF §4.5.6.
	pos, ok = skipPascalString(b, pos)
	if !ok {
		return 0, nil, entryPos, false
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
			// #151: clamp pos so a single non-IPTC block with odd-length data and no
			// trailing pad byte does not advance pos past len(b). The for-guard
			// (pos < len(b)) already prevents an out-of-bounds read, but leaving pos
			// one beyond len(b) is semantically incorrect and mirrors the identical
			// clamp in spliceIPTCIntoIRB. Adobe Photoshop IRB spec §"Image Resources".
			if pos > len(b) {
				pos = len(b)
			}
		}
	}
	return nil
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
		// Return the current pooled buffer before overwriting *scratch so it is
		// not orphaned. Assigning *scratch to a local variable and taking its
		// address causes the variable to escape to the heap (sync.Pool stores
		// the pointer externally), which makes the Put safe: the pool holds a
		// valid reference to the old backing array until the next Get recycles it.
		// Without this Put the original 4096-byte pooled buffer is silently
		// abandoned, depleting the pool under sustained load. (#77)
		//
		// #214: the replacement buffer is drawn from iobuf's large pool
		// instead of a bare make([]byte, need). Every APP1/APP13/APP2 segment
		// over 4 KiB (EXIF, XMP, ICC profiles — common in real-world photos)
		// grows scratch on every readSegment call; sourcing the growth from
		// the pool lets that buffer be reused across calls instead of
		// allocating fresh heap memory each time.
		old := *scratch
		iobuf.Put(&old)
		*scratch = *iobuf.Get(need)
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
//
//nolint:unparam // #216 moved every production hot-path caller to writeHeaderedBuf/writeSegmentCopy; writeSegment remains a correct, spec-tested two-write generic segment writer, exercised only by fixed-marker tests today.
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

// segHeaderSize is the length in bytes of a JPEG marker segment header
// (0xFF, marker, 2-byte length). JPEG ISO/IEC 10918-1 §B.1.1.4.
const segHeaderSize = 4

// stampSegmentHeader writes the 4-byte JPEG segment header into buf[0:4].
// buf must be at least segHeaderSize bytes; buf[segHeaderSize:] is treated as
// the segment body for the purpose of the length calculation. Returns
// ErrSegmentTooLarge if the encoded length would exceed the 16-bit JPEG
// segment length field. JPEG ISO/IEC 10918-1 §B.1.1.4.
func stampSegmentHeader(buf []byte, marker byte) error {
	bodyLen := len(buf) - segHeaderSize
	length := bodyLen + 2 // length field includes its own 2 bytes
	if length > 65535 {
		return fmt.Errorf("jpeg: segment 0x%02X payload %d bytes exceeds 65535-byte APP segment limit: %w", marker, bodyLen, ErrSegmentTooLarge)
	}
	buf[0] = 0xFF
	buf[1] = marker
	buf[2] = byte(length >> 8)
	buf[3] = byte(length) //nolint:gosec // G115: length <= 65535, fits in a byte after the shift/truncation above
	return nil
}

// writeHeaderedBuf stamps the JPEG segment header into (*bufPtr)[0:segHeaderSize]
// and writes the entire buffer (header + body) to w in a single Write call,
// then returns bufPtr to the pool exactly once regardless of outcome.
//
// #216: callers that already hold a pooled buffer with segHeaderSize bytes of
// head-room reserved before the body use this instead of writeSegment, which
// otherwise requires a second, stack-allocated header array that escapes to
// the heap on every call through the io.Writer interface boundary.
func writeHeaderedBuf(w io.Writer, marker byte, bufPtr *[]byte) error {
	buf := *bufPtr
	if err := stampSegmentHeader(buf, marker); err != nil {
		iobuf.Put(bufPtr)
		return err
	}
	_, err := w.Write(buf)
	iobuf.Put(bufPtr)
	if err != nil {
		return fmt.Errorf("jpeg: write segment: %w", err)
	}
	return nil
}

// writeSegmentCopy writes marker+data as a single JPEG segment, copying data
// into a freshly pooled header+body buffer so the header and body leave in
// one w.Write call. Used for pass-through segments (#216) where data aliases
// the caller's scratch buffer and was not originally built with head-room for
// the header.
func writeSegmentCopy(w io.Writer, marker byte, data []byte) error {
	bufPtr := iobuf.Get(segHeaderSize + len(data))
	copy((*bufPtr)[segHeaderSize:], data)
	return writeHeaderedBuf(w, marker, bufPtr)
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

// isAllHex reports whether b consists entirely of ASCII hex digits (0-9, a-f, A-F).
// Used to validate the 32-character GUID in extended XMP APP1 segments.
// Adobe XMP Specification Part 3 §1.1.4: GUID is a 32-character uppercase hex string.
func isAllHex(b []byte) bool {
	for _, c := range b {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// mergeExtendedChunks sorts chunks by their byte offset and concatenates their
// data fields into a single contiguous extended XMP byte slice.
// This function does NOT validate chunk layout; use mergeExtendedChunksValidated
// when the declared total size is known.
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

// mergeExtendedChunksValidated sorts chunks by offset, validates that they form
// a contiguous, non-overlapping sequence starting at offset 0 and covering
// exactly declaredTotal bytes (when hasDeclaredTotal is true), and returns the
// assembled bytes and a validity flag.
//
// Validation rules (Adobe XMP Specification Part 3 §1.1.4, #122):
//   - First chunk must have offset == 0.
//   - Each subsequent chunk must start exactly where the previous one ended
//     (chunk[i].offset == chunk[i-1].offset + len(chunk[i-1].data)).
//   - When hasDeclaredTotal is true, the assembled length must equal declaredTotal.
//
// On any violation, returns (nil, false): buildXMPResult must degrade to the
// main packet only rather than returning corrupted or doubled reassembled bytes.
func mergeExtendedChunksValidated(chunks []extChunk, declaredTotal uint64, hasDeclaredTotal bool) ([]byte, bool) {
	if len(chunks) == 0 {
		return nil, false
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].offset < chunks[j].offset
	})

	// First chunk must start at offset 0.
	if chunks[0].offset != 0 {
		return nil, false
	}

	// Each subsequent chunk must be contiguous with the previous one.
	var totalLen int
	for i, c := range chunks {
		if i > 0 {
			expectedOffset := chunks[i-1].offset + uint32(len(chunks[i-1].data)) //nolint:gosec // G115: chunk offset/size bounded by maxExtendedXMPTotal (16 MiB)
			if c.offset != expectedOffset {
				// Gap or overlap detected: reject the assembled payload.
				return nil, false
			}
		}
		totalLen += len(c.data)
	}

	// When the wire-declared total is known, assembled length must match exactly.
	// Adobe XMP Specification Part 3 §1.1.4: fullLength is the total size of the
	// extended XMP document; every byte must be accounted for.
	if hasDeclaredTotal && uint64(totalLen) != declaredTotal {
		return nil, false
	}

	extBytes := make([]byte, 0, totalLen)
	for _, c := range chunks {
		extBytes = append(extBytes, c.data...)
	}
	return extBytes, true
}

// reassembleExtendedXMPByParse merges the extended XMP document into the main
// XMP packet using xmp.Parse for prefix-agnostic property extraction.
//
// Adobe XMP Specification Part 3 §1.1.4: after assembling the extended payload
// the reader must merge its properties into the main packet. The extended packet
// is a full XMP document that may use any namespace prefix binding (not
// necessarily "rdf:"); a literal string search for "<rdf:Description" fails
// silently when a non-canonical prefix is used. This function parses both
// documents with xmp.Parse and merges the Properties maps, which is correct
// regardless of prefix assignment. (#123)
//
// Merge policy: properties already present in main are not overwritten (main
// wins on conflict), consistent with Adobe XMP Specification Part 3 §1.1.4
// which states that the extended packet extends rather than replaces the main.
//
// Returns nil when either packet fails to parse OR when the extended packet
// yields no properties after parsing. The latter case means extBytes is not a
// proper full-XMP packet (e.g. it is a raw RDF fragment); the caller must fall
// back to the byte-splice reassembler, which handles such fragments correctly.
func reassembleExtendedXMPByParse(mainBytes, extBytes []byte) []byte {
	mainXMP, err := xmppkg.Parse(mainBytes)
	if err != nil {
		return nil
	}
	extXMP, err := xmppkg.Parse(extBytes)
	if err != nil {
		return nil
	}
	// If the extended packet parsed but carries no properties, it is most likely
	// not a proper full XMP document (e.g. a raw rdf:Description fragment that
	// xmp.Parse accepted without error due to its lenient parser). Signal the
	// caller to use the byte-splice fallback, which handles RDF fragments.
	if len(extXMP.Properties) == 0 {
		return nil
	}

	// Merge: add extended properties that are absent from the main packet.
	// main wins on conflict (Adobe XMP Spec Part 3 §1.1.4).
	for ns, props := range extXMP.Properties {
		for local, val := range props {
			if mainXMP.Get(ns, local) == "" {
				mainXMP.Set(ns, local, val)
			}
		}
	}

	encoded, err := xmppkg.Encode(mainXMP)
	if err != nil {
		return nil
	}
	return encoded
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
