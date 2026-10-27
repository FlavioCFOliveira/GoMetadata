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

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FileHandlers/JPEG_Handler.cpp:JPEG_MetaHandler::ProcessXMP, Copyright 2004 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// The adapted part is the removal of xmpNote:HasExtendedXMP from the standard
// XMP packet when extended XMP chunks are present but none matches its GUID
// (stripExtendedLink, task #332). See THIRD_PARTY_NOTICES.md.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
	irbpkg "github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	"github.com/FlavioCFOliveira/GoMetadata/internal/trailer"
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
// which is distinct from the xmpNote namespace URI (xmp.NSxmpNote,
// "http://ns.adobe.com/xmp/note/") of the HasExtendedXMP property inside the
// main XMP packet. The two strings must never be confused.
var identXMPNote = []byte("http://ns.adobe.com/xmp/extension/\x00") //nolint:gochecknoglobals // package-level constant bytes

// identPS is the Photoshop 3.0 signature in APP13 (EXIF §4.5.6).
var identPS = []byte("Photoshop 3.0\x00") //nolint:gochecknoglobals // package-level constant bytes

// APP1 segment capacity constants derived from the JPEG 16-bit length field.
// JPEG ISO/IEC 10918-1 §B.1.1.4: length field is 2 bytes and includes itself,
// so the maximum payload is 65535 − 2 = 65533 bytes.
//
// maxXMPPayload: max XMP packet bytes the writer puts in a standard
// (non-extended) APP1 segment. Adobe XMP Specification Part 3 (2020) §1.1.3.1,
// p. 14: "If the XMP packet is not split across multiple APP1 sections, the
// size of the XMP packet can be at most 65502 bytes." (task #351). The segment
// itself could hold 65504 bytes (65533 − len(identXMP) = 65504), which is
// where earlier releases set the limit; the writer follows the specification,
// which is 2 bytes stricter, so that no packet it writes can exceed what a
// conforming reader accepts. The reader applies no such limit: it accepts the
// packet of any APP1 segment, so files written with up to 65504 bytes still
// read.
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
	maxXMPPayload       = 65502               // XMP Part 3 §1.1.3.1, p. 14
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

// xmpKeepMagic is the 8-byte magic of the "keep extended XMP" frame (task
// #357), the second internal encoding that crosses the rawXMP channel, next to
// the wire-frame (xmpWireMagic). Layout: [8-byte magic][32-byte GUID][standard
// XMP packet]. It asks Inject to carry over, byte for byte and in place, the
// extended XMP segments of the source whose GUID is the one given, and to link
// the standard packet to them. Like the wire-frame it is never exposed to
// callers of the public API, and its first byte 0x00 is not a valid start for
// an XMP packet.
var xmpKeepMagic = []byte("\x00XMPKEEP") //nolint:gochecknoglobals // package-level constant bytes

// EncodeKeptExtended returns the frame that asks Inject to keep the extended
// XMP segments of GUID guid (see ExtractFullSelectiveKept) next to the
// standard XMP packet packet. guid must be the 32 hexadecimal digits that
// ExtractFullSelectiveKept returned. The frame is only for Inject of a JPEG:
// every other container rejects it.
func EncodeKeptExtended(packet []byte, guid string) []byte {
	b := make([]byte, 0, len(xmpKeepMagic)+guidLen+len(packet))
	b = append(b, xmpKeepMagic...)
	b = append(b, guid...)
	return append(b, packet...)
}

// decodeKeptExtended splits a keep frame into the standard packet and the
// GUID. ok is false when raw is not a keep frame.
func decodeKeptExtended(raw []byte) (packet []byte, guid string, ok bool) {
	if len(raw) < len(xmpKeepMagic)+guidLen || !bytes.HasPrefix(raw, xmpKeepMagic) {
		return nil, "", false
	}
	rest := raw[len(xmpKeepMagic):]
	return rest[guidLen:], string(rest[:guidLen]), true
}

// resolveKeptExtended applies a keep frame (rawXMP) to the source in r: when
// the source has an extended XMP segment of the frame's GUID, it returns the
// standard packet linked to that GUID (xmpNote:HasExtendedXMP, Part 3 §1.1.3.1,
// p. 14) and the GUID to keep; otherwise the packet as it is and no GUID, so
// that a packet never links to extended XMP that is not in the file (the
// source may be another file than the one the metadata was read from). A
// rawXMP that is not a keep frame is returned unchanged at no cost. The
// standard packet must still fit one APP1 segment: the file carries one
// ExtendedXMP, whose GUID the standard packet holds (Part 3 §1.1.3.1, p. 14:
// a reader incorporates only the extended segments of that GUID), so a packet
// that needs a second one of its own cannot be written next to the segments
// that are kept (ErrExtendedXMPConflict) and none is dropped silently.
func resolveKeptExtended(r io.ReadSeeker, scratch *[]byte, rawXMP []byte) (xmp []byte, keepGUID string, err error) {
	packet, guid, ok := decodeKeptExtended(rawXMP)
	if !ok {
		return rawXMP, "", nil
	}
	found, err := hasExtendedSegment(r, scratch, guid)
	if err != nil || !found {
		return packet, "", err
	}
	linked, err := xmppkg.WithExtendedLink(packet, guid)
	if err != nil {
		return nil, "", fmt.Errorf("jpeg: link standard XMP to the extended XMP kept from the source: %w", err)
	}
	if head, tail := trimPacketPadding(linked); len(head)+len(tail) > maxXMPPayload {
		return nil, "", fmt.Errorf("jpeg: the XMP to write (%d bytes) does not fit one segment: %w", len(linked), ErrExtendedXMPConflict)
	}
	return linked, guid, nil
}

// hasExtendedSegment reports whether the JPEG in r has an extended XMP APP1
// segment (Part 3 §1.1.4) whose GUID is guid, before its image data. r is left
// at an unspecified position. err is non-nil only for a Seek failure or
// ErrFileTooLarge.
func hasExtendedSegment(r io.ReadSeeker, scratch *[]byte, guid string) (bool, error) {
	if _, err := r.Seek(2, io.SeekStart); err != nil {
		return false, fmt.Errorf("jpeg: seek: %w", err)
	}
	for {
		marker, data, err := readSegment(r, scratch)
		if err != nil {
			if errors.Is(err, ErrFileTooLarge) {
				return false, err
			}
			return false, nil
		}
		switch {
		case isExtendedSegmentOf(marker, data, guid):
			return true, nil
		case marker == markerSOS, marker == markerEOI:
			return false, nil
		}
	}
}

// isExtendedSegmentOf reports whether the segment is an extended XMP APP1
// segment whose GUID is guid (compared without regard to case, like the
// reader, which canonicalises it to upper case).
func isExtendedSegmentOf(marker byte, data []byte, guid string) bool {
	if marker != markerAPP1 || guid == "" || !bytes.HasPrefix(data, identXMPNote) {
		return false
	}
	body := data[len(identXMPNote):]
	return len(body) >= guidLen && strings.EqualFold(string(body[:guidLen]), guid)
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
	// keptGUID is the GUID of extended XMP segments that the standard packet
	// links to and that were NOT merged into rawXMP (their layout is invalid,
	// or the assembled document cannot be merged): a write that rewrites the
	// standard packet carries those segments over verbatim (task #357; see
	// EncodeKeptExtended). Empty when there are none.
	// A pointer, not a string: the result is copied up the call chain of every
	// read, and the field is set only in the rare unmergeable case.
	keptGUID *string
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
		// Extended chunks exist but none has the GUID of the main packet:
		// HasExtendedXMP "must only be in the file" (Part 3 §1.1.3.1, p. 15),
		// so it is removed, as JPEG_Handler.cpp:JPEG_MetaHandler::ProcessXMP
		// does (task #332).
		return xmpResult{rawXMP: stripExtendedLink(rawXMP, wantXMP), truncated: extTruncated}
	}

	// #122: validate chunk layout before assembling. Adobe XMP Specification
	// Part 3 §1.1.4: chunks must cover [0, fullLen) contiguously with no gaps,
	// overlaps, or duplicated offsets. On any violation degrade gracefully to
	// the main (standard) XMP packet and mark truncated.
	declaredTotal, hasDeclared := extFullLens[guid]
	extBytes, valid := mergeExtendedChunksValidated(chunks, declaredTotal, hasDeclared)
	if !valid {
		// Chunk layout is corrupt; return main packet only (without the link
		// to the extended XMP that was not recomposed) and signal truncation.
		kept := guid // a copy of its own: guid itself stays on the stack in the other paths
		return xmpResult{rawXMP: stripExtendedLink(rawXMP, wantXMP), truncated: true, keptGUID: &kept}
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

	// #123, #278: xmp.ParseExtended recomposes both documents into one XMP
	// data model tree (Adobe XMP Specification Part 3 §1.1.3.1), resolving
	// namespace prefixes by URI — the extended document may bind the RDF
	// namespace to any prefix (e.g. R:, RDF:) — and keeping nested
	// structures of the extended document intact.
	//
	// Fall back to the byte-splice reassembler when either document cannot be
	// parsed or the extended document holds no property (e.g. a bare
	// rdf:Description fragment), so that the common "rdf:" prefix case is
	// always served.
	reassembled := reassembleExtendedXMPByParse(rawXMP, extBytes)
	if reassembled == nil {
		extMap := map[string][]extChunk{guid: {{offset: 0, data: extBytes}}}
		var merged bool
		reassembled, merged = spliceExtendedXMP(rawXMP, extMap)
		if !merged {
			// #350: the extended content could not be merged. The packet
			// returned must not point at it: the link is removed, as the XMP
			// Toolkit does before it tries the merge and keeps the standard
			// XMP when the merge fails (JPEG_Handler.cpp:ProcessXMP, which
			// deletes xmpNote:HasExtendedXMP first and ignores a failed
			// MergeFromJPEG; Part 3 §1.1.3.1, p. 15: the property exists
			// only in the file). The caller is told through truncated, so a
			// ParseWarning reports that the extended content was dropped; it
			// stays in the wire-frame, so an unmodified write keeps it.
			kept := guid // a copy of its own, as above
			return xmpResult{rawXMP: stripExtendedLink(rawXMP, true), rawXMPWire: wire, truncated: true, keptGUID: &kept}
		}
	}

	return xmpResult{rawXMP: reassembled, rawXMPWire: wire, truncated: extTruncated}
}

// spliceExtendedXMP is the fallback of buildXMPResult: it splices the
// extended content into the standard packet main as reassembleExtendedXMP
// does, removes the link property from the result (Part 3 §1.1.3.1, p. 15)
// and reports whether that gave a well-formed packet that holds the extended
// content. It returns main and false when the splice changed nothing or its
// result cannot be parsed, so that a malformed extended document is never
// written into the packet.
func spliceExtendedXMP(main []byte, extended map[string][]extChunk) (packet []byte, merged bool) {
	spliced := reassembleExtendedXMP(main, extended)
	if bytes.Equal(spliced, main) {
		return main, false
	}
	stripped, err := xmppkg.StripExtendedLink(spliced)
	if err != nil {
		return main, false
	}
	return stripped, true
}

// stripExtendedLink returns the main XMP packet main without its
// xmpNote:HasExtendedXMP property (task #332). It returns main unchanged when
// the caller does not want XMP (#238), or when main cannot be parsed: the
// property is then left in the raw packet rather than losing the packet.
func stripExtendedLink(main []byte, wantXMP bool) []byte {
	if !wantXMP {
		return main
	}
	stripped, err := xmppkg.StripExtendedLink(main)
	if err != nil {
		return main
	}
	return stripped
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
// or nil when absent. MWG §4.2.3.2, p. 26.
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
// and returns the 0x0404 IPTC-NAA data and the 0x0425 IPTC-Digest data; the
// shared parser lives in internal/irb. Either result may be nil.
//
// MWG §4.2.3.2, p. 26: the 0x0425 resource (PSIR 1061) carries the 16-byte MD5
// of the entire IPTC-IIM block at the time XMP was last written.
func parseIRBForIPTCAndDigest(b []byte) (iptcData, digestData []byte) {
	return irbpkg.IPTCAndDigest(b)
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
// MWG §4.2.3.2, pp. 26-28: the digest enables IPTC/XMP precedence reconciliation.
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
// MWG Guidelines v2.0 §4.2.3.2, pp. 27-28: the caller should compare the digest to
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
	rawEXIF, rawIPTC, iptcDigest, rawXMP, rawXMPWire, xmpTruncated, _, err = ExtractFullSelectiveKept(r, wantIPTC, wantXMP)
	return rawEXIF, rawIPTC, iptcDigest, rawXMP, rawXMPWire, xmpTruncated, err
}

// ExtractFullSelectiveKept is ExtractFullSelective that also returns keptGUID,
// the GUID of the extended XMP segments that the standard packet links to and
// that could not be merged into rawXMP (an invalid chunk layout, or an
// assembled document that cannot be merged; the cases that set xmpTruncated
// with a matching GUID). It is empty when the extended XMP was merged or there
// is none. A caller that rewrites the XMP passes the packet it writes through
// EncodeKeptExtended, so that Inject carries those segments over verbatim
// (task #357).
func ExtractFullSelectiveKept(r io.ReadSeeker, wantIPTC, wantXMP bool) (rawEXIF, rawIPTC, iptcDigest, rawXMP, rawXMPWire []byte, xmpTruncated bool, keptGUID string, err error) {
	rawEXIF, rawIPTC, iptcDigest, xmpRes, err := extractFullInternal(r, wantIPTC, wantXMP)
	if err != nil {
		return nil, nil, nil, nil, nil, false, "", err
	}
	if xmpRes.keptGUID != nil {
		keptGUID = *xmpRes.keptGUID
	}
	return rawEXIF, rawIPTC, iptcDigest, xmpRes.rawXMP, xmpRes.rawXMPWire, xmpRes.truncated, keptGUID, nil
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

// writeXMPSegments writes rawXMP to w as one standard XMP APP1 segment when
// it fits, and otherwise as a standard packet plus extended XMP segments.
// Adobe XMP Specification Part 3 §1.1.3 (pp. 14-15), task #332:
//
//  1. A packet of at most maxXMPPayload bytes is written as is.
//  2. Otherwise its packet padding is removed (§1.1.3.2, p. 15: "try to
//     reduce or eliminate the packet-padding whitespace"); when the packet
//     then fits it is still written whole, as one segment.
//  3. Otherwise the XMP is partitioned by xmp.PackageForJPEG, the port of the
//     Adobe XMP Toolkit's XMPUtils::PackageForJPEG: a compact standard packet
//     with the properties that matter most, and the rest as extended XMP
//     under the GUID that the standard packet carries. A packet that cannot
//     be parsed, or whose standard portion cannot be made to fit, is stored
//     whole as extended XMP behind a stub standard packet (writeExtendedXMP).
//
// When rawXMP begins with xmpWireMagic (a wire-frame encoding), the function
// decodes the original main XMP and the assembled extended payload and writes
// them using the passthrough path — main as a standard APP1, extended as
// re-chunked extended APP1 segments under the original GUID. This ensures
// that round-trip writes of unmodified extended XMP are byte-stable (same
// reassembled content before and after).
func writeXMPSegments(w io.Writer, rawXMP []byte) error {
	// Wire-frame passthrough: preserve original main+extended without regenerating GUID.
	if main, ext, ok := decodeXMPWire(rawXMP); ok {
		return writeXMPWirePassthrough(w, main, ext)
	}

	if len(rawXMP) <= maxXMPPayload {
		return writeXMPParts(w, rawXMP, nil)
	}
	if head, tail := trimPacketPadding(rawXMP); len(head)+len(tail) <= maxXMPPayload {
		return writeXMPParts(w, head, tail)
	}
	return writePartitionedXMP(w, rawXMP)
}

// writeXMPParts writes head followed by tail as one standard XMP APP1
// segment (a nil tail writes head alone).
//
// #216: header + prefix + payload built in one pooled buffer, one w.Write.
func writeXMPParts(w io.Writer, head, tail []byte) error {
	bufPtr := iobuf.Get(segHeaderSize + len(identXMP) + len(head) + len(tail))
	b := *bufPtr
	copy(b[segHeaderSize:], identXMP)
	n := copy(b[segHeaderSize+len(identXMP):], head)
	copy(b[segHeaderSize+len(identXMP)+n:], tail)
	return writeHeaderedBuf(w, markerAPP1, bufPtr)
}

// trimPacketPadding splits an XMP packet around its padding: head is the
// packet up to the white space that precedes the closing <?xpacket end?>
// processing instruction, and tail starts at that instruction. A packet
// without one is returned whole as head.
func trimPacketPadding(packet []byte) (head, tail []byte) {
	i := bytes.LastIndex(packet, xpacketEnd)
	if i < 0 {
		return packet, nil
	}
	j := i
	for j > 0 && isXMLSpace(packet[j-1]) {
		j--
	}
	return packet[:j], packet[i:]
}

// writePartitionedXMP writes rawXMP, which does not fit one APP1 segment even
// without padding, as a standard packet and extended XMP segments partitioned
// by xmp.PackageForJPEG (Part 3 §1.1.3.2, p. 15).
func writePartitionedXMP(w io.Writer, rawXMP []byte) error {
	x, err := xmppkg.Parse(rawXMP)
	if err != nil {
		return writeExtendedXMP(w, rawXMP)
	}
	pkg, err := xmppkg.PackageForJPEG(x)
	if err != nil {
		return writeExtendedXMP(w, rawXMP)
	}
	if err := writeRawXMPSegment(w, pkg.Standard); err != nil {
		return err
	}
	if pkg.Extended == nil {
		return nil
	}
	return writeExtendedChunks(w, []byte(pkg.GUID), pkg.Extended)
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
	b = appendSplicedIRB(b, origIRB, nil, false) // strip 0x0404, keep siblings
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
func writeIPTCSegment(w io.Writer, rawIPTC, origIRB []byte, createDigest bool) error {
	// Upper-bound estimate: identPS + origIRB (block replaced in place, not
	// grown) + the new 0x0404 wrapper (12-byte header + rawIPTC + 1 pad byte)
	// + a new 0x0425 digest resource (12-byte header + 16 bytes).
	bufPtr := iobuf.Get(segHeaderSize + len(identPS) + len(origIRB) + len(rawIPTC) + 13 + 28)
	b := (*bufPtr)[:segHeaderSize+len(identPS)]
	copy(b[segHeaderSize:], identPS)
	if origIRB != nil || createDigest {
		b = appendSplicedIRB(b, origIRB, rawIPTC, createDigest)
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
	return irbpkg.AppendBlock(dst, irbpkg.ResourceIPTC, iptcData)
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
// verbatim. When newIPTCData is nil the 0x0404 block is removed and all
// siblings survive (#174). The 0x0425 digest is created or rewritten to the
// MD5 of newIPTCData according to irbpkg.AppendSpliced (#284, #323);
// createDigest is true when XMP is written together with the IPTC (MWG
// Guidelines v2.0 §4.2.3.2, p. 29).
//
// #218: appends directly onto the caller's buffer (typically a pooled
// segment buffer) instead of allocating a new one.
func appendSplicedIRB(dst, origIRB, newIPTCData []byte, createDigest bool) []byte {
	return irbpkg.AppendSpliced(dst, origIRB, newIPTCData, createDigest)
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
	return appendSplicedIRB(out, origIRB, newIPTCData, false)
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
	if err := writeIPTCOrSiblings(w, rawIPTC, origIRB, rawXMP != nil); err != nil {
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
// withXMP is true when an XMP packet is written in the same pass; it lets the
// IPTC digest be created (task #323, MWG Guidelines v2.0 §4.2.3.2, p. 29).
//
// This helper exists solely to satisfy the nestif linter; the logic matches
// the comment in writeNewMetadataSegments (#174).
func writeIPTCOrSiblings(w io.Writer, rawIPTC, origIRB []byte, withXMP bool) error {
	if rawIPTC != nil {
		return writeIPTCSegment(w, rawIPTC, origIRB, withXMP)
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

// writeSOSOrTrailers is writeSOS, or writeSOSTrailers when plan lists trailers
// to rewrite (task #322; r is then the seekable reader the plan was made from).
func writeSOSOrTrailers(r io.Reader, w io.Writer, data []byte, plan *trailer.Plan) error {
	if rs, ok := r.(io.ReadSeeker); ok && plan != nil {
		// w is the counting writer startTrailerPlan returned with the plan.
		if out, isOut := w.(*trailer.OutWriter); isOut {
			return writeSOSTrailers(rs, w, out, data, plan)
		}
	}
	return writeSOS(r, w, data)
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
//
// plan (task #322) lists the trailer ranges after the image data that are
// rewritten instead of copied; it is nil for a JPEG without such a trailer,
// and then r need not be seekable. keepGUID (task #357) names the extended
// XMP segments that are copied instead of dropped; it is empty in the common
// case. keep (task #387) names the metadata segments that are copied in place
// instead of dropped; it is 0 for Inject.
func copyNonMetadataSegments(r io.Reader, w io.Writer, scratch *[]byte, preserveUnknownSegments bool, plan *trailer.Plan, keepGUID string, keep Keep) error {
	for {
		marker, data, err := readSegment(r, scratch)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		if dropsSegmentKeeping(marker, data, preserveUnknownSegments, keepGUID, keep) {
			continue
		}

		switch marker {
		case markerSOS:
			return writeSOSOrTrailers(r, w, data, plan)
		case markerEOI:
			return writeMarker(w, markerEOI)
		default:
			if err := writePassThroughSegment(w, marker, data); err != nil {
				return err
			}
		}
	}
}

// dropsSegment reports whether Inject leaves a source segment out of the
// output.
//
//   - An extended XMP segment of keepGUID, the GUID that the standard packet
//     written links to, is copied as it is, in its place (task #357).
//   - A segment Inject replaced (or removes when its payload is nil) is
//     dropped: isOldMetadataSegment.
//   - When preserveUnknownSegments is false, any other APPn segment is dropped
//     too: isOldMetadataSegment already covers EXIF APP1, XMP APP1 (standard and
//     extended) and Photoshop APP13, so what remains is "unknown" to the
//     library (e.g. APP0/JFIF, APP2/ICC, APP12/Ducky, APP14/Adobe DCT). JPEG
//     ISO/IEC 10918-1 §B.2.4.6: APPn markers are application extension
//     segments; no APPn is required for image decoding.
func dropsSegment(marker byte, data []byte, preserveUnknownSegments bool, keepGUID string) bool {
	if keepGUID != "" && isExtendedSegmentOf(marker, data, keepGUID) {
		return false
	}
	return isOldMetadataSegment(marker, data) || (!preserveUnknownSegments && isAppMarker(marker))
}

// dropsSegmentKeeping is dropsSegment for a write that keeps the metadata
// segments in keep in place (task #387): those are never dropped.
func dropsSegmentKeeping(marker byte, data []byte, preserveUnknownSegments bool, keepGUID string, keep Keep) bool {
	if keep != 0 && keepsSegment(marker, data, keep) {
		return false
	}
	return dropsSegment(marker, data, preserveUnknownSegments, keepGUID)
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

// prepareInject runs the passes of Inject that precede the main copy pass and
// leaves cr at the start of the file: the original Photoshop IRB (resolveOrigIRB,
// #174, #217) and the trailers that hold an IPTC copy or absolute offsets
// (startTrailerPlan, #322), whose plan is nil, with w returned as it is, when
// there are none. A keep frame in rawXMP (#357) is resolved against the source
// first: xmp is the standard packet to write and keepGUID the GUID of the
// extended XMP segments to keep (see resolveKeptExtended).
func prepareInject(cr io.ReadSeeker, w io.Writer, scratch *[]byte, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool, keep Keep) (origIRB []byte, plan *trailer.Plan, out io.Writer, exifOut, xmp []byte, keepGUID string, err error) {
	// #357: a keep frame in rawXMP asks to carry over the extended XMP segments
	// of the source that were not merged; it starts with 0x00, which no XMP
	// packet does, so any other payload skips the call. Resolved against this
	// source before another pass moves the reader.
	if len(rawXMP) > 0 && rawXMP[0] == 0 {
		if rawXMP, keepGUID, err = resolveKeptExtended(cr, scratch, rawXMP); err != nil {
			return nil, nil, w, nil, nil, "", err
		}
	}
	// Task #387: an IPTC kept in place keeps its Photoshop resources with it,
	// so no sibling is carried into a new APP13 segment.
	if keep&KeepIPTC == 0 {
		if origIRB, err = resolveOrigIRB(cr, scratch, rawIPTC); err != nil {
			return nil, nil, w, nil, nil, "", err
		}
	}
	plan, out, err = startTrailerPlan(cr, w, scratch, rawIPTC)
	if err != nil {
		return nil, nil, w, nil, nil, "", err
	}
	// #362: a MakerNote offset to data outside the EXIF stream follows the data
	// it addresses. Only an Olympus MakerNote holds one, and its signature
	// starts with 'O': the single-byte search keeps the common stream from
	// paying for more (a call with this many arguments from Inject itself cost
	// 5% on the 300 ns Inject benchmark; here the arguments are at hand).
	exifOut = rawEXIF
	if bytes.IndexByte(rawEXIF, 'O') >= 0 {
		if exifOut, err = shiftExternalOffsets(cr, scratch, rawEXIF, rawIPTC, rawXMP, origIRB, preserveUnknownSegments, keepGUID, keep, plan); err != nil {
			return nil, nil, w, nil, nil, "", err
		}
	}
	// Seek back to the start for the main copy pass.
	if _, err := cr.Seek(0, io.SeekStart); err != nil {
		return nil, nil, w, nil, nil, "", fmt.Errorf("jpeg: seek: %w", err)
	}
	return origIRB, plan, out, exifOut, rawXMP, keepGUID, nil
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
//
// The IPTC digest (resource 0x0425) follows the policy of irb.AppendSpliced:
// when rawXMP is written together with rawIPTC it is created, or rewritten to
// the MD5 of rawIPTC, a stale digest included (MWG Guidelines v2.0 §4.2.3.2,
// p. 29); without rawXMP, or with a nil rawIPTC, it is left as it is (#323). An AFCP
// trailer and a FotoStation trailer after the image data hold copies of the
// IPTC: they are rewritten with rawIPTC (left as they are when it is nil), and
// the AFCP offsets follow the trailer to its position in the output (#322; see
// trailer.go). Oversized XMP is partitioned into a standard packet and
// extended XMP (#332; see writeXMPSegments).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	return injectSegments(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments, 0)
}

// Keep is a set of the metadata segments of a JPEG that InjectKeeping leaves
// as they are in the source (task #387).
type Keep uint8

// The segments a Keep can hold.
const (
	// KeepEXIF is every EXIF APP1 segment.
	KeepEXIF Keep = 1 << iota
	// KeepIPTC is every Photoshop APP13 segment (the IPTC and the other
	// Photoshop resources, the IPTC digest included).
	KeepIPTC
	// KeepXMP is every XMP APP1 segment, the standard packet and the extended
	// XMP segments.
	KeepXMP
)

// InjectKeeping is Inject for a write that changes only some of the metadata
// segments (task #387): the segments in keep stay in the output exactly as
// the source has them, byte for byte and at the same place; the others are
// replaced by the payload given, or removed when it is nil, as Inject does.
//
// The payload of a kept segment is not written. A caller passes it anyway
// when it holds it: Inject semantics apply to the whole write whenever a kept
// segment cannot stay in place, which InjectKeeping detects and which is
// reported by nothing but the output. That happens for the EXIF: when its
// stream holds MakerNote offsets to data outside it (exif.ExternalOffsets;
// Olympus), the position of that data depends on the size of every segment
// before it, so the EXIF is written from rawEXIF, with the offsets rewritten
// for the new layout as in Inject, even when it is in keep.
//
// A kept IPTC is not rewritten, so the IPTC digest is left as the source has
// it, whatever is written to XMP (MWG Guidelines v2.0 §4.2.3.2, p. 29: a
// digest that no longer matches tells a reader the IPTC was edited after the
// XMP). The AFCP and FotoStation trailers are left as they are too.
func InjectKeeping(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool, keep Keep) error {
	if keep&KeepEXIF != 0 && bytes.IndexByte(rawEXIF, 'O') >= 0 && len(exif.ExternalOffsets(rawEXIF)) > 0 {
		keep &^= KeepEXIF
	}
	if keep&KeepEXIF != 0 {
		rawEXIF = nil
	}
	if keep&KeepIPTC != 0 {
		rawIPTC = nil
	}
	if keep&KeepXMP != 0 {
		rawXMP = nil
	}
	return injectSegments(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments, keep)
}

// keepsSegment reports whether the source segment marker+data is one that keep
// leaves in place.
func keepsSegment(marker byte, data []byte, keep Keep) bool {
	switch marker {
	case markerAPP1:
		return keep&KeepEXIF != 0 && bytes.HasPrefix(data, identExif) ||
			keep&KeepXMP != 0 && (bytes.HasPrefix(data, identXMP) || bytes.HasPrefix(data, identXMPNote))
	case markerAPP13:
		return keep&KeepIPTC != 0 && bytes.HasPrefix(data, identPS)
	}
	return false
}

// injectSegments is Inject and InjectKeeping: rawEXIF, rawIPTC and rawXMP are
// nil for a segment in keep.
func injectSegments(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool, keep Keep) error {
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
	// #357: a keep frame in rawXMP asks to carry over the extended XMP segments
	// of the source that were not merged; resolve it against this source before
	// any pass moves the reader.
	origIRB, plan, w, rawEXIF, rawXMP, keepGUID, err := prepareInject(cr, w, scratch, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments, keep)
	if err != nil {
		return err
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
	return copyNonMetadataSegments(cr, w, scratch, preserveUnknownSegments, plan, keepGUID, keep)
}

// parseIRBEntry delegates to irbpkg.ParseEntry; see there for the recoverable-miss
// (newPos == pos) versus terminal-failure (newPos > pos) contract. EXIF §4.5.6.
func parseIRBEntry(b []byte, pos int) (resourceID uint16, data []byte, newPos int, ok bool) {
	return irbpkg.ParseEntry(b, pos)
}

// parseIRB extracts the IPTC IIM stream (resource 0x0404) from a Photoshop IRB
// block, or nil when absent (EXIF §4.5.6.2).
func parseIRB(b []byte) []byte {
	return irbpkg.FindIPTC(b)
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

// reassembleExtendedXMPByParse recomposes the standard XMP packet mainBytes
// and the extended XMP serialisation extBytes into one XMP packet.
//
// Adobe XMP Specification Part 3 §1.1.3.1: "A JPEG reader must recompose the
// StandardXMP and ExtendedXMP into a single data model tree containing all of
// the XMP for the JPEG file, and remove the xmpNote:HasExtendedXMP property."
// xmp.ParseExtended builds that tree — nested structures, qualifiers and
// array forms of the extended portion included (task #278) — and resolves
// namespace prefixes by URI, so any prefix binding of the rdf namespace works
// (#123). A top-level property present in both portions takes the extended
// value, as a whole property, as the Adobe XMP Toolkit does
// (XMPUtils::MergeFromJPEG).
//
// Returns nil when either document cannot be parsed or when the extended
// document holds no property — for example a bare rdf:Description fragment
// that relies on the namespace declarations of the main packet. The caller
// then falls back to the byte-splice reassembler, which handles such
// fragments.
func reassembleExtendedXMPByParse(mainBytes, extBytes []byte) []byte {
	x, err := xmppkg.ParseExtended(mainBytes, extBytes)
	if err != nil {
		return nil
	}
	encoded, err := xmppkg.Encode(x)
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
// before its first rdf:Description element (or, without one, before its
// closing </rdf:RDF> tag). The parser keeps the first occurrence of a
// property, so the extended value of a property that both portions carry
// wins, as in xmp.ParseExtended and XMPUtils::MergeFromJPEG of the Adobe XMP
// Toolkit (task #278).
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

	// Splice extraDescs into main before its first rdf:Description, so that
	// the extended properties come first, or before its </rdf:RDF> close tag.
	mainCloseRDF := bytes.LastIndex(main, []byte("</rdf:RDF>"))
	if mainCloseRDF < 0 {
		return main
	}
	at := mainCloseRDF
	if first := bytes.Index(main, []byte("<rdf:Description")); first >= 0 && first < mainCloseRDF {
		at = first
	}

	result := make([]byte, 0, len(main)+len(extraDescs))
	result = append(result, main[:at]...)
	result = append(result, extraDescs...)
	result = append(result, main[at:]...)
	return result
}

// isStandalone reports whether m is a marker that has no length / data field.
func isStandalone(m byte) bool {
	return m == markerSOI || m == markerEOI ||
		(m >= 0xD0 && m <= 0xD7) || // RST0–RST7
		m == 0x01 // TEM
}
