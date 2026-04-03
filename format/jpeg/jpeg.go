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
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/flaviocfo/img-metadata/internal/iobuf"
)

// JPEG marker bytes (ISO/IEC 10918-1, Annex B).
const (
	markerSOI  byte = 0xD8
	markerEOI  byte = 0xD9
	markerSOS  byte = 0xDA
	markerAPP1 byte = 0xE1
	markerAPP13 byte = 0xED
)

// identExif is the mandatory 6-byte prefix for EXIF inside APP1 (EXIF §4.5.4).
var identExif = []byte("Exif\x00\x00")

// identXMP is the NUL-terminated namespace URI prefix for XMP inside APP1.
// Adobe XMP Specification Part 3 §1.1.3.
var identXMP = []byte("http://ns.adobe.com/xap/1.0/\x00")

// identXMPNote is the NUL-terminated namespace URI prefix for extended XMP
// inside APP1. Adobe XMP Specification Part 3 §1.1.4.
var identXMPNote = []byte("http://ns.adobe.com/xap/1.0/se/\x00")

// identPS is the Photoshop 3.0 signature in APP13 (EXIF §4.5.6).
var identPS = []byte("Photoshop 3.0\x00")

// extChunk holds one chunk of an extended XMP segment.
// Adobe XMP Specification Part 3 §1.1.4.
type extChunk struct {
	offset uint32
	data   []byte
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

	// Read and verify SOI.
	soi := [2]byte{}
	if _, err = io.ReadFull(r, soi[:]); err != nil {
		return nil, nil, nil, fmt.Errorf("jpeg: read SOI: %w", err)
	}
	if soi[0] != 0xFF || soi[1] != markerSOI {
		return nil, nil, nil, fmt.Errorf("jpeg: not a JPEG file (SOI 0x%04X)", uint16(soi[0])<<8|uint16(soi[1]))
	}

	// extended collects chunks from extended XMP APP1 segments, keyed by GUID.
	// Adobe XMP Specification Part 3 §1.1.4.
	extended := make(map[string][]extChunk)

	for {
		marker, data, rerr := readSegment(r)
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			// Non-fatal: degrade gracefully on malformed marker streams.
			// Return whatever metadata we have collected so far.
			break
		}

		switch marker {
		case markerAPP1:
			switch {
			case bytes.HasPrefix(data, identExif):
				// EXIF payload begins after the 6-byte "Exif\x00\x00" header.
				rawEXIF = data[len(identExif):]
			case bytes.HasPrefix(data, identXMP):
				rawXMP = data[len(identXMP):]
			case bytes.HasPrefix(data, identXMPNote):
				// Extended XMP chunk: GUID (32 bytes) + fullLength (4 bytes) +
				// offset (4 bytes) + chunk data. Adobe XMP Spec Part 3 §1.1.4.
				body := data[len(identXMPNote):]
				if len(body) >= 40 {
					guid := string(body[:32])
					fullLen := binary.BigEndian.Uint32(body[32:36])
					offset := binary.BigEndian.Uint32(body[36:40])
					_ = fullLen // used only for validation; assembly is offset-driven
					extended[guid] = append(extended[guid], extChunk{
						offset: offset,
						data:   body[40:],
					})
				}
			}

		case markerAPP13:
			if bytes.HasPrefix(data, identPS) {
				rawIPTC = parseIRB(data[len(identPS):])
			}

		case markerSOS, markerEOI:
			// SOS/EOI: no more metadata segments follow.
			if rawXMP != nil && len(extended) > 0 {
				rawXMP = reassembleExtendedXMP(rawXMP, extended)
			}
			return rawEXIF, rawIPTC, rawXMP, nil
		}
	}

	if rawXMP != nil && len(extended) > 0 {
		rawXMP = reassembleExtendedXMP(rawXMP, extended)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
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
		return fmt.Errorf("jpeg: not a JPEG file")
	}
	if _, err := w.Write(soi[:]); err != nil {
		return err
	}

	// Write new metadata segments before any existing ones.
	if rawEXIF != nil {
		// APP1 length field is 16-bit; max payload = 65535 - 2 (length field).
		// EXIF §4.5.4, JPEG ISO/IEC 10918-1.
		if len(identExif)+len(rawEXIF)+2 > 65535 {
			return fmt.Errorf("jpeg: EXIF payload %d bytes exceeds APP1 segment limit; EXIF cannot be split", len(rawEXIF))
		}
		exifBuf := iobuf.Get(len(identExif) + len(rawEXIF))
		copy(*exifBuf, identExif)
		copy((*exifBuf)[len(identExif):], rawEXIF)
		writeErr := writeSegment(w, markerAPP1, *exifBuf)
		iobuf.Put(exifBuf)
		if writeErr != nil {
			return writeErr
		}
	}
	if rawXMP != nil {
		// XMP max payload = 65535 - 2 (length field) - len(identXMP).
		// Splitting into extended XMP requires XMP-level GUID injection which
		// is outside the scope of the format layer. Adobe XMP Spec Part 3 §1.1.4.
		if len(identXMP)+len(rawXMP)+2 > 65535 {
			return fmt.Errorf("jpeg: XMP payload %d bytes exceeds APP1 segment limit (65458 bytes); split into extended XMP not supported for writing", len(rawXMP))
		}
		xmpBuf := iobuf.Get(len(identXMP) + len(rawXMP))
		copy(*xmpBuf, identXMP)
		copy((*xmpBuf)[len(identXMP):], rawXMP)
		writeErr := writeSegment(w, markerAPP1, *xmpBuf)
		iobuf.Put(xmpBuf)
		if writeErr != nil {
			return writeErr
		}
	}
	if rawIPTC != nil {
		irb := buildIRB(rawIPTC)
		// APP13 length field is 16-bit; max payload = 65535 - 2 (length field).
		if len(identPS)+len(irb)+2 > 65535 {
			return fmt.Errorf("jpeg: IPTC IRB payload %d bytes exceeds APP13 segment limit", len(irb))
		}
		iptcBuf := iobuf.Get(len(identPS) + len(irb))
		copy(*iptcBuf, identPS)
		copy((*iptcBuf)[len(identPS):], irb)
		writeErr := writeSegment(w, markerAPP13, *iptcBuf)
		iobuf.Put(iptcBuf)
		if writeErr != nil {
			return writeErr
		}
	}

	// Copy remaining segments, skipping old metadata APP segments.
	for {
		marker, data, err := readSegment(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		// Skip segments we replaced (or removed when payload is nil).
		// Also skip extended XMP segments (identXMPNote) — they belong to the
		// old XMP data we have already replaced above.
		if marker == markerAPP1 {
			if bytes.HasPrefix(data, identExif) ||
				bytes.HasPrefix(data, identXMP) ||
				bytes.HasPrefix(data, identXMPNote) {
				continue
			}
		}
		if marker == markerAPP13 && bytes.HasPrefix(data, identPS) {
			continue
		}

		if marker == markerSOS {
			// Write SOS and then pass through the rest of the file verbatim.
			if err := writeSegment(w, markerSOS, data); err != nil {
				return err
			}
			_, err = io.Copy(w, r)
			return err
		}

		if marker == markerEOI {
			_, err = w.Write([]byte{0xFF, markerEOI})
			return err
		}

		if data == nil {
			// Standalone marker (no data).
			if _, err := w.Write([]byte{0xFF, marker}); err != nil {
				return err
			}
		} else {
			if err := writeSegment(w, marker, data); err != nil {
				return err
			}
		}
	}

	return nil
}

// parseIRB extracts the IPTC IIM stream from a Photoshop IRB block.
// IRB format: "8BIM" + 2-byte resource ID + Pascal string name + 4-byte size + data.
// Resource ID 0x0404 is the IPTC-NAA resource (EXIF §4.5.6.2).
func parseIRB(b []byte) []byte {
	pos := 0
	for pos+12 <= len(b) {
		// Locate the "8BIM" signature.
		if b[pos] != '8' || b[pos+1] != 'B' || b[pos+2] != 'I' || b[pos+3] != 'M' {
			pos++
			continue
		}
		pos += 4

		if pos+2 > len(b) {
			break
		}
		resourceID := binary.BigEndian.Uint16(b[pos:])
		pos += 2

		// Pascal string name: 1-byte length + n bytes, padded to even total.
		if pos >= len(b) {
			break
		}
		nameLen := int(b[pos])
		pos++
		pos += nameLen
		if (nameLen+1)%2 != 0 {
			pos++ // padding byte
		}

		if pos+4 > len(b) {
			break
		}
		dataSize := int(binary.BigEndian.Uint32(b[pos:]))
		pos += 4

		if dataSize < 0 || pos+dataSize > len(b) {
			break
		}

		if resourceID == 0x0404 {
			return b[pos : pos+dataSize]
		}

		pos += dataSize
		if dataSize%2 != 0 {
			pos++ // pad data to even boundary
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
	buf = append(buf, '8', 'B', 'I', 'M')
	buf = append(buf, 0x04, 0x04) // resource ID 0x0404
	buf = append(buf, 0x00, 0x00) // empty pascal name (length 0 + padding byte)
	buf = append(buf,
		byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
	buf = append(buf, iptcData...)
	if size%2 != 0 {
		buf = append(buf, 0x00) // pad data to even boundary
	}
	return buf
}

// readSegment reads one JPEG marker segment from r.
// For standalone markers (SOI, EOI, RST*), data is nil.
// Returns (0, nil, io.EOF) at end of file.
func readSegment(r io.Reader) (marker byte, data []byte, err error) {
	hdr := [2]byte{}
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0] != 0xFF {
		return 0, nil, fmt.Errorf("jpeg: expected marker prefix 0xFF, got 0x%02X", hdr[0])
	}
	// Skip fill bytes (consecutive 0xFF).
	for hdr[1] == 0xFF {
		if _, err = io.ReadFull(r, hdr[1:]); err != nil {
			return 0, nil, err
		}
	}
	marker = hdr[1]

	// Standalone markers carry no length or data.
	if isStandalone(marker) {
		return marker, nil, nil
	}

	lenB := [2]byte{}
	if _, err = io.ReadFull(r, lenB[:]); err != nil {
		return 0, nil, fmt.Errorf("jpeg: read length for marker 0x%02X: %w", marker, err)
	}
	length := int(binary.BigEndian.Uint16(lenB[:]))
	if length < 2 {
		return 0, nil, fmt.Errorf("jpeg: marker 0x%02X has invalid length %d", marker, length)
	}

	data = make([]byte, length-2)
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
		return fmt.Errorf("jpeg: segment 0x%02X payload %d bytes exceeds 65535-byte APP segment limit", marker, len(data))
	}
	hdr := [4]byte{0xFF, marker, byte(length >> 8), byte(length)}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(data) > 0 {
		_, err := w.Write(data)
		return err
	}
	return nil
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
	// Locate the HasExtendedXMP property name in the main packet.
	const marker = "HasExtendedXMP"
	idx := bytes.Index(main, []byte(marker))
	if idx < 0 {
		return main
	}

	// The GUID value follows the property name as either an attribute
	// (HasExtendedXMP="<GUID>") or element content (HasExtendedXMP><GUID></...).
	// In both cases we scan past up to 5 bytes for the opening quote character.
	rest := main[idx+len(marker):]
	qi := bytes.IndexAny(rest, `"'`)
	if qi < 0 || qi > 5 {
		return main
	}
	quote := rest[qi]
	rest = rest[qi+1:]
	end := bytes.IndexByte(rest, quote)
	if end != 32 { // GUID must be exactly 32 hex characters
		return main
	}
	guid := string(rest[:32])

	chunks, ok := extended[guid]
	if !ok || len(chunks) == 0 {
		return main
	}

	// Sort chunks by offset so concatenation produces the correct byte sequence.
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].offset < chunks[j].offset
	})

	// Concatenate chunk data into the complete extended XMP packet bytes.
	var totalLen int
	for _, c := range chunks {
		totalLen += len(c.data)
	}
	extBytes := make([]byte, 0, totalLen)
	for _, c := range chunks {
		extBytes = append(extBytes, c.data...)
	}

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
