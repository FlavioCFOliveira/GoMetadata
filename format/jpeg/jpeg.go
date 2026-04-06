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

// identXMPNote is the NUL-terminated namespace URI prefix for extended XMP
// inside APP1. Adobe XMP Specification Part 3 §1.1.4.
var identXMPNote = []byte("http://ns.adobe.com/xap/1.0/se/\x00") //nolint:gochecknoglobals // package-level constant bytes

// identPS is the Photoshop 3.0 signature in APP13 (EXIF §4.5.6).
var identPS = []byte("Photoshop 3.0\x00") //nolint:gochecknoglobals // package-level constant bytes

// APP1 segment capacity constants derived from the JPEG 16-bit length field.
// JPEG ISO/IEC 10918-1 §B.1.1.4: length field is 2 bytes and includes itself,
// so the maximum payload is 65535 − 2 = 65533 bytes.
//
// maxXMPPayload: max XMP packet bytes in a standard (non-extended) APP1.
// maxExtChunkSize: max chunk data per extended XMP APP1 (Adobe XMP Spec Part 3 §1.1.4).
//
//	Extended APP1 layout: identXMPNote(32) + GUID(32) + fullLen(4) + offset(4) + chunk
//	Overhead = 32 + 32 + 4 + 4 = 72 bytes → chunk data ≤ 65533 − 72 = 65461 bytes.
const (
	maxAPP1Payload  = 65533               // 65535 − 2 (length field)
	maxXMPPayload   = maxAPP1Payload - 29 // − len(identXMP)
	maxExtChunkSize = maxAPP1Payload - 72 // − len(identXMPNote)+GUID+fullLen+offset overhead
)

// extChunk holds one chunk of an extended XMP segment.
// Adobe XMP Specification Part 3 §1.1.4.
type extChunk struct {
	offset uint32
	data   []byte
}

// processAPP1Segment dispatches an APP1 segment payload to the appropriate
// metadata bucket (EXIF, standard XMP, or extended XMP).
// It returns updated values for rawEXIF, rawXMP, and the extended map;
// pass-through values are returned unchanged when not applicable.
func processAPP1Segment(data, rawEXIF, rawXMP []byte, extended map[string][]extChunk) ([]byte, []byte, map[string][]extChunk) {
	switch {
	case bytes.HasPrefix(data, identExif):
		// EXIF payload begins after the 6-byte "Exif\x00\x00" header.
		// Copy: data aliases scratch and must survive the next readSegment call.
		rawEXIF = append([]byte(nil), data[len(identExif):]...)

	case bytes.HasPrefix(data, identXMP):
		// Copy: same reason as rawEXIF.
		rawXMP = append([]byte(nil), data[len(identXMP):]...)

	case bytes.HasPrefix(data, identXMPNote):
		// Extended XMP chunk: GUID (32 bytes) + fullLength (4 bytes) +
		// offset (4 bytes) + chunk data. Adobe XMP Spec Part 3 §1.1.4.
		body := data[len(identXMPNote):]
		if len(body) >= 40 {
			guid := string(body[:32])
			fullLen := binary.BigEndian.Uint32(body[32:36])
			offset := binary.BigEndian.Uint32(body[36:40])
			_ = fullLen // used only for validation; assembly is offset-driven
			// Copy chunk data: body aliases scratch and must outlive this loop.
			chunkData := append([]byte(nil), body[40:]...)
			// Lazily initialise the map on first encounter.
			if extended == nil {
				extended = make(map[string][]extChunk)
			}
			extended[guid] = append(extended[guid], extChunk{
				offset: offset,
				data:   chunkData,
			})
		}
	}

	return rawEXIF, rawXMP, extended
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
	return append([]byte(nil), irb...)
}

// maybeReassembleXMP returns the reassembled XMP when extended chunks are
// present, or rawXMP unchanged when there is nothing to merge.
// Centralising this condition removes the duplicated &&-branch that would
// otherwise appear at every early-return point in Extract.
func maybeReassembleXMP(rawXMP []byte, extended map[string][]extChunk) []byte {
	if rawXMP != nil && len(extended) > 0 {
		return reassembleExtendedXMP(rawXMP, extended)
	}
	return rawXMP
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

// scanMetadataSegments reads the JPEG marker stream from r until SOS/EOI or
// read failure, collecting EXIF, IPTC, XMP, and extended-XMP payloads.
func scanMetadataSegments(r io.Reader, scratchPtr *[]byte) (rawEXIF, rawIPTC, rawXMP []byte) {
	// extended collects chunks from extended XMP APP1 segments, keyed by GUID.
	// Adobe XMP Specification Part 3 §1.1.4.
	// Lazily initialised: most JPEGs do not contain extended XMP, so we avoid
	// the map allocation on the fast path.
	var extended map[string][]extChunk

	for {
		marker, data, rerr := readSegment(r, scratchPtr)
		if rerr != nil {
			// Both EOF and malformed-stream errors: degrade gracefully and
			// return whatever metadata has been collected so far.
			break
		}

		switch marker {
		case markerAPP1:
			rawEXIF, rawXMP, extended = processAPP1Segment(data, rawEXIF, rawXMP, extended)
		case markerAPP13:
			if iptc := processAPP13Segment(data); iptc != nil {
				rawIPTC = iptc
			}
		case markerSOS, markerEOI:
			// SOS/EOI: no more metadata segments follow.
			return rawEXIF, rawIPTC, maybeReassembleXMP(rawXMP, extended)
		}
	}

	return rawEXIF, rawIPTC, maybeReassembleXMP(rawXMP, extended)
}

// Extract reads the JPEG marker stream from r and returns the raw payloads.
// rawEXIF: APP1 content after the "Exif\x00\x00" identifier (nil if absent).
// rawIPTC: the raw IIM byte stream extracted from the Photoshop IRB 8BIM
//
//	resource block 0x0404 inside APP13 (nil if absent).
//
// rawXMP:  the full XMP packet bytes from the XMP APP1 (nil if absent).
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("jpeg: seek: %w", err)
	}

	// Obtain a pooled scratch buffer first so the SOI read can reuse it,
	// avoiding the heap escape that occurs when a stack-allocated [2]byte
	// is passed to io.ReadFull via the io.Reader interface.
	scratchPtr := iobuf.Get(4096)
	defer func() { iobuf.Put(scratchPtr) }()

	// Read and verify SOI using the pooled scratch buffer.
	soi := (*scratchPtr)[:2]
	if _, err = io.ReadFull(r, soi); err != nil {
		return nil, nil, nil, fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if err := readSOI(soi); err != nil {
		return nil, nil, nil, err
	}

	rawEXIF, rawIPTC, rawXMP = scanMetadataSegments(r, scratchPtr)
	return rawEXIF, rawIPTC, rawXMP, nil
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
func writeXMPSegments(w io.Writer, rawXMP []byte) error {
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

// writeIPTCSegment wraps the IPTC IIM stream in a Photoshop IRB block and
// writes it as an APP13 segment. APP13 length field is 16-bit; EXIF §4.5.6.
func writeIPTCSegment(w io.Writer, rawIPTC []byte) error {
	irb := buildIRB(rawIPTC)
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

// writeNewMetadataSegments writes EXIF APP1, XMP APP1 (with extended-XMP
// splitting when the payload exceeds the single-segment limit), and IPTC
// APP13 segments to w. Returns the first error encountered.
func writeNewMetadataSegments(w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
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
		if err := writeIPTCSegment(w, rawIPTC); err != nil {
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

// Inject reads the JPEG marker stream from r, replaces the relevant APP
// segments with the provided payloads, and writes the result to w.
// A nil payload means the corresponding segment is removed.
// The SOS segment and compressed image data are passed through unchanged.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
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
	if err := writeNewMetadataSegments(w, rawEXIF, rawIPTC, rawXMP); err != nil {
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
	// Extended APP1 layout (Adobe XMP Spec Part 3 §1.1.4):
	//   identXMPNote (32 bytes) | GUID (32 bytes) | fullLength (4 bytes BE) |
	//   offset (4 bytes BE) | chunk data
	fullLen := uint32(len(rawXMP)) //nolint:gosec // G115: XMP payload size bounded by input
	offset := uint32(0)
	guidBytes := []byte(guid) // 32 ASCII bytes

	// Pre-allocate the fixed-size extended APP1 header once.
	// Header = identXMPNote(32) + GUID(32) + fullLen(4) + offset(4) = 72 bytes.
	const extHdrSize = 72
	for offset < fullLen {
		chunkEnd := offset + uint32(maxExtChunkSize) // min builtin shadowed by test-only helper in fuzz_test.go; cannot use min here
		if chunkEnd > fullLen {
			chunkEnd = fullLen
		}
		chunk := rawXMP[offset:chunkEnd]

		extBuf := iobuf.Get(extHdrSize + len(chunk))
		b := *extBuf

		// identXMPNote
		copy(b, identXMPNote)
		// GUID
		copy(b[len(identXMPNote):], guidBytes)
		// fullLength (4 bytes BE)
		binary.BigEndian.PutUint32(b[64:68], fullLen)
		// offset (4 bytes BE)
		binary.BigEndian.PutUint32(b[68:72], offset)
		// chunk data
		copy(b[72:], chunk)

		writeErr = writeSegment(w, markerAPP1, b)
		iobuf.Put(extBuf)
		if writeErr != nil {
			return writeErr
		}

		offset = chunkEnd
	}

	return nil
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
	// binary.BigEndian.Uint32 returns uint32; on 64-bit platforms the int cast
	// is always non-negative. The subsequent bounds check catches truncation.
	dataSize := int(binary.BigEndian.Uint32(b[pos:]))
	pos += 4

	if pos+dataSize > len(b) {
		return 0, nil, pos + 1, false
	}

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
