// Package cr3 implements metadata extraction for Canon CR3 files.
// CR3 is an ISOBMFF-based format (ftyp brand "crx ") with Canon-specific
// boxes CMT1, CMT2, CMT3, CMT4 that contain EXIF IFDs.
package cr3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// Canon UUID: {85C0B687-820F-11E0-8111-F4CE462B6A48} stored as raw bytes.
var canonUUID = []byte{ //nolint:gochecknoglobals // package-level constant bytes
	0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0,
	0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48,
}

// parseCR3BoxHeader reads an ISOBMFF box header at data[pos:] and returns the
// resolved box size, 4-byte type string, header length in bytes, and whether
// the parse succeeded.
//
// ISOBMFF (ISO 14496-12) §4.2:
//   - Normal box:   4-byte size + 4-byte type          → headerLen = 8
//   - Extended box: size==1, followed by 8-byte largesize → headerLen = 16
//   - size==0 means the box extends to end-of-container → size = len(data)-pos
func parseCR3BoxHeader(data []byte, pos int) (size uint64, typ string, headerLen uint64, ok bool) {
	if pos+8 > len(data) {
		return 0, "", 0, false
	}
	size = uint64(binary.BigEndian.Uint32(data[pos:]))
	typ = string(data[pos+4 : pos+8])
	headerLen = 8

	if size == 1 {
		// Extended 64-bit size immediately follows the 8-byte base header.
		if pos+16 > len(data) {
			return 0, "", 0, false
		}
		size = binary.BigEndian.Uint64(data[pos+8:])
		headerLen = 16
	}

	if size == 0 {
		// len(data)-pos is non-negative: guarded by pos+8 ≤ len(data) check above.
		size = uint64(len(data) - pos) //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
	}

	// ISOBMFF (ISO 14496-12) §4.2: a well-formed box must be large enough to
	// contain its own header. If size < headerLen the box is malformed — slicing
	// data[pos+headerLen : pos+size] would panic (bounds out of range).
	if size < headerLen {
		return 0, "", 0, false
	}

	// Bounds check: box must not extend beyond the containing slice.
	if size > uint64(len(data)-pos) { //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
		return 0, "", 0, false
	}

	return size, typ, headerLen, true
}

// Extract reads metadata from a CR3 file by navigating the ISOBMFF box tree.
// CMT1 contains IFD0 (TIFF header + entries); CMT2 contains the Exif IFD that
// IFD0's ExifIFD pointer (tag 0x8769) addresses. Both are merged into rawEXIF
// so that exif.Parse receives a contiguous buffer covering both IFDs.
//
// If the moov/UUID structure is present but no CMT1 sub-box is found, Extract
// returns (nil, nil, rawXMP, ErrNoCMT1Box). rawXMP is still populated when a
// "XMP " sub-box exists, so callers can use XMP even when EXIF is absent.
//
// # audit finding #138
//
// ErrNoCMT1Box lets callers distinguish "no EXIF" from a broken container parse.
// lclevy canon_cr3: CMT1 carries IFD0; its absence means no EXIF is present.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("cr3: seek: %w", err)
	}
	// #140 fix: cap the full-file read to maxFileSize+1 bytes so that an
	// oversized or infinite streaming reader cannot trigger unbounded heap
	// allocation. ErrFileTooLarge is returned when the limit is exceeded,
	// before any ISOBMFF parsing takes place.
	data, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cr3: read: %w", err)
	}
	if int64(len(data)) > maxFileSize {
		return nil, nil, nil, fmt.Errorf("cr3: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}

	moovData := findBox(data, "moov", 0)
	if moovData == nil {
		return nil, nil, nil, ErrNoMoovBox
	}

	uuidData := findUUIDBox(moovData, canonUUID)
	if uuidData == nil {
		// Fall back: search for CMT1/CMT2 anywhere in the moov box.
		cmt1 := findBox(moovData, "CMT1", 0)
		cmt2 := findBox(moovData, "CMT2", 0)
		rawXMP = findBox(moovData, "XMP ", 0)
		// audit #138: surface missing CMT1 as a sentinel error so callers can
		// distinguish no-EXIF from a broken container.
		if cmt1 == nil {
			return nil, nil, rawXMP, ErrNoCMT1Box
		}
		return mergeCMT(cmt1, cmt2), nil, rawXMP, nil
	}

	cmt1 := findBox(uuidData, "CMT1", 0)
	cmt2 := findBox(uuidData, "CMT2", 0)
	rawXMP = findBox(uuidData, "XMP ", 0)
	// audit #138: surface missing CMT1 as a sentinel error.
	if cmt1 == nil {
		return nil, nil, rawXMP, ErrNoCMT1Box
	}
	return mergeCMT(cmt1, cmt2), nil, rawXMP, nil
}

// getExifIFDOffset detects byte order from cmt1's TIFF header and returns the
// value of the ExifIFD pointer tag (0x8769) in IFD0. Returns 0 if cmt1 is
// too short, the byte-order mark is unrecognised, or the tag is absent.
//
// TIFF 6.0 §2: "II" = little-endian, "MM" = big-endian.
func getExifIFDOffset(cmt1 []byte) uint32 {
	if len(cmt1) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch {
	case cmt1[0] == 'I' && cmt1[1] == 'I':
		order = binary.LittleEndian
	case cmt1[0] == 'M' && cmt1[1] == 'M':
		order = binary.BigEndian
	default:
		return 0
	}
	ifd0Off := order.Uint32(cmt1[4:8]) //nolint:gosec // G602: len(cmt1) >= 8 guaranteed by guard above
	return findExifIFDOffset(cmt1, ifd0Off, order)
}

// mergeCMT combines CMT1 (IFD0 TIFF stream) with CMT2 (Exif IFD bytes) into
// a single contiguous buffer that exif.Parse can traverse.
//
// In CR3 files, the ExifIFD pointer stored in CMT1's IFD0 points to a byte
// offset relative to the start of CMT1. If that offset falls beyond CMT1's
// length, the Exif IFD data lives in CMT2. Appending CMT2 to CMT1 makes the
// pointer valid so the EXIF parser can follow it without modification.
//
// If cmt2 is nil or the ExifIFD pointer lies within CMT1, cmt1 is returned
// unchanged (zero copy).
func mergeCMT(cmt1, cmt2 []byte) []byte {
	if cmt2 == nil || len(cmt1) < 8 {
		return cmt1
	}
	exifIFDOffset := getExifIFDOffset(cmt1)
	// If the ExifIFD offset is within CMT1, no merge needed.
	if exifIFDOffset == 0 || int(exifIFDOffset) < len(cmt1) {
		return cmt1
	}
	// ExifIFD pointer extends into CMT2: concatenate.
	merged := make([]byte, len(cmt1)+len(cmt2))
	copy(merged, cmt1)
	copy(merged[len(cmt1):], cmt2)
	return merged
}

// findExifIFDOffset walks IFD0 in buf (starting at ifd0Off) looking for tag
// 0x8769 (ExifIFD) and returns its LONG value (the offset). Returns 0 if not
// found or if buf is too short to parse.
func findExifIFDOffset(buf []byte, ifd0Off uint32, order binary.ByteOrder) uint32 {
	if int(ifd0Off)+2 > len(buf) {
		return 0
	}
	count := order.Uint16(buf[ifd0Off:])
	pos := int(ifd0Off) + 2
	for range int(count) {
		if pos+12 > len(buf) {
			break
		}
		tag := order.Uint16(buf[pos:])
		if tag == 0x8769 { // ExifIFD pointer
			// type must be LONG (4), count must be 1; value is the 4-byte offset.
			return order.Uint32(buf[pos+8:])
		}
		pos += 12
	}
	return 0
}

// rebuildUUIDContent iterates the sub-boxes of the Canon UUID box payload and
// reconstructs the content with CMT1 replaced by rawEXIF (if non-nil) and
// "XMP " replaced by rawXMP (if non-nil). Other sub-boxes are copied unchanged.
// hadXMP reports whether an "XMP " sub-box was present in the original content.
//
// audit #175: if rawEXIF is non-nil and no CMT1 sub-box exists in the original
// content, a new CMT1 box is appended after the loop. This mirrors the XMP
// add-if-absent pattern in rebuildMoovContent and ensures that writing EXIF to a
// UUID box that only carries CMT2/CMT3/CMT4 does not silently discard rawEXIF.
//
// lclevy canon_cr3: CMT1 is the mandatory IFD0 TIFF stream inside the Canon UUID.
func rebuildUUIDContent(uuidContent, rawEXIF, rawXMP []byte) (newContent []byte, hadXMP bool) {
	var buf bytes.Buffer
	var hadCMT1 bool
	pos := 0
	for pos+8 <= len(uuidContent) {
		size, typ, _, ok := parseCR3BoxHeader(uuidContent, pos)
		if !ok {
			break
		}
		switch typ {
		case "CMT1":
			hadCMT1 = true
			if rawEXIF != nil {
				buf.Write(buildBox("CMT1", rawEXIF))
			} else {
				buf.Write(uuidContent[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		case "XMP ":
			hadXMP = true
			if rawXMP != nil {
				buf.Write(buildBox("XMP ", rawXMP))
			} else {
				buf.Write(uuidContent[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		default:
			buf.Write(uuidContent[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	// audit #175: insert a new CMT1 sub-box when rawEXIF is provided but the
	// original UUID content had no CMT1. Without this, Inject silently discards
	// rawEXIF for CR3 files whose UUID box carries only CMT2/CMT3/CMT4.
	if rawEXIF != nil && !hadCMT1 {
		buf.Write(buildBox("CMT1", rawEXIF))
	}
	return buf.Bytes(), hadXMP
}

// findMoovRange returns the start and end byte positions of the first moov box
// in data. start is the index of the first byte of the box header; end is the
// first byte after the box. Returns (0,0,false) if no moov box is found.
func findMoovRange(data []byte) (start, end int, found bool) {
	pos := 0
	for pos+8 <= len(data) {
		size, typ, _, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		if typ == "moov" {
			return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// relocateChunkOffsets walks moovBytes in-place and adjusts every stco/co64
// entry whose absolute file offset is >= oldMoovEnd by adding delta.
//
// ISO 14496-12 §8.7.3 (ChunkOffsetBox / stco):
//   - FullBox header: 4-byte size + "stco" + 1-byte version + 3-byte flags = 12 bytes total
//   - entry_count: uint32 at offset 12
//   - entries: entry_count × uint32 starting at offset 16
//
// ISO 14496-12 §8.7.5 (ChunkLargeOffsetBox / co64):
//   - FullBox header: 4-byte size + "co64" + 1-byte version + 3-byte flags = 12 bytes total
//   - entry_count: uint32 at offset 12
//   - entries: entry_count × uint64 starting at offset 16
//
// moovBytes is the complete rebuilt moov box (including its 8-byte header).
// All stco/co64 boxes are patched in-place because moovBytes is freshly
// allocated by buildBox — no aliasing with the original data.
//
// oldMoovEnd is the absolute end offset of the original moov box in the file
// (= moovStart + original moov size). Any chunk offset >= oldMoovEnd points at
// or beyond the region that was shifted by delta; those entries are incremented.
// Offsets before moovStart (e.g., into ftyp) are unchanged.
//
// For stco: if the relocated value would exceed math.MaxUint32, the function
// returns ErrStcoOverflow rather than silently truncating the offset.
func relocateChunkOffsets(moovBytes []byte, oldMoovEnd int, delta int64) error {
	if len(moovBytes) < 8 {
		return nil
	}
	// moovBytes includes the 8-byte moov box header; pass the content slice.
	// audit #191: start at depth=0; cap recursion at 32 levels (mirrors findBox).
	return relocateInContainer(moovBytes[8:], int64(oldMoovEnd), delta, 0)
}

// relocateInContainer recursively walks ISOBMFF boxes in the container content
// slice (data). Only container box types that lead to stco/co64 are recursed
// into: trak, mdia, minf, stbl. stco and co64 boxes are patched in-place.
//
// data must be exactly the content payload of the enclosing container box
// (the header bytes must already have been stripped by the caller). This
// ensures each recursion level only scans within its own box boundary.
//
// depth is the current recursion depth. audit #191: the guard `depth > 32`
// mirrors the cap used by findBox — providing defence-in-depth against crafted
// ISOBMFF structures with pathological nesting. Valid CR3 files never reach
// this limit; the guard is purely protective.
// ISO 14496-12 §4.2: box hierarchy is structurally bounded by box sizes.
func relocateInContainer(data []byte, oldMoovEnd int64, delta int64, depth int) error {
	// audit #191: cap recursion at 32 levels to mirror findBox and prevent
	// stack exhaustion on adversarially crafted ISOBMFF containers.
	if depth > 32 {
		return nil
	}
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		// content is the box payload (after the header bytes), strictly bounded.
		content := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length

		switch typ {
		case "trak", "mdia", "minf", "stbl":
			// Recurse into container boxes using the box payload slice only.
			// This ensures the recursive scan is strictly bounded within this box.
			if err := relocateInContainer(content, oldMoovEnd, delta, depth+1); err != nil {
				return err
			}
		case "stco":
			// ISO 14496-12 §8.7.3: FullBox (version 1B + flags 3B) + entry_count (4B) + entries (N×4B).
			// Pass content (the stco payload after the box header) for in-place patching.
			if err := relocateStco(content, oldMoovEnd, delta); err != nil {
				return err
			}
		case "co64":
			// ISO 14496-12 §8.7.5: FullBox (version 1B + flags 3B) + entry_count (4B) + entries (N×8B).
			if err := relocateCo64(content, oldMoovEnd, delta); err != nil {
				return err
			}
		}

		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return nil
}

// relocateStco patches stco entries (uint32) in-place.
// content is the stco box payload: version (1B) + flags (3B) + entry_count (4B) + entries (N×4B).
// Entries >= oldMoovEnd are incremented by delta; overflow returns ErrStcoOverflow.
func relocateStco(content []byte, oldMoovEnd int64, delta int64) error {
	// FullBox: version (1B) + flags (3B) = 4 bytes; entry_count follows at offset 4.
	if len(content) < 8 {
		// Box too small to contain version/flags + entry_count — skip gracefully.
		return nil
	}
	entryCount := binary.BigEndian.Uint32(content[4:])
	entryStart := 8
	// Guard: entry array must fit within content.
	if int(entryCount) > (len(content)-entryStart)/4 {
		return nil
	}
	for i := range int(entryCount) {
		off := entryStart + i*4
		orig := int64(binary.BigEndian.Uint32(content[off:]))
		if orig >= oldMoovEnd {
			relocated := orig + delta
			if relocated < 0 || relocated > math.MaxUint32 {
				return fmt.Errorf("cr3: stco offset %d + delta %d = %d: %w", orig, delta, relocated, ErrStcoOverflow)
			}
			binary.BigEndian.PutUint32(content[off:], uint32(relocated))
		}
	}
	return nil
}

// relocateCo64 patches co64 entries (uint64) in-place.
// content is the co64 box payload: version (1B) + flags (3B) + entry_count (4B) + entries (N×8B).
// Entries >= oldMoovEnd are incremented by delta; since uint64 is large enough for
// any file size, no overflow check is needed beyond sign safety.
func relocateCo64(content []byte, oldMoovEnd int64, delta int64) error {
	// FullBox: version (1B) + flags (3B) = 4 bytes; entry_count follows at offset 4.
	if len(content) < 8 {
		return nil
	}
	entryCount := binary.BigEndian.Uint32(content[4:])
	entryStart := 8
	// Guard: entry array must fit within content.
	if int(entryCount) > (len(content)-entryStart)/8 {
		return nil
	}
	for i := range int(entryCount) {
		off := entryStart + i*8
		orig := int64(binary.BigEndian.Uint64(content[off:])) //nolint:gosec // G115: uint64→int64 safe for file offsets < 2^63
		if orig >= oldMoovEnd {
			relocated := orig + delta
			if relocated < 0 {
				return fmt.Errorf("cr3: co64 offset %d + delta %d = %d underflows: %w", orig, delta, relocated, ErrStcoOverflow)
			}
			binary.BigEndian.PutUint64(content[off:], uint64(relocated))
		}
	}
	return nil
}

// rebuildMoovContent replaces the Canon UUID box inside moovContent with a new
// UUID box containing the rebuilt CMTx payloads. Returns the new moov content
// slice. If no Canon UUID box is found, moovContent is returned unchanged.
func rebuildMoovContent(moovContent, rawEXIF, rawXMP []byte) []byte {
	uuidStart, uuidEnd, hasUUID := flatUUIDBoxRange(moovContent, canonUUID)
	if !hasUUID {
		return moovContent
	}
	// uuidData is the UUID payload: everything after the 8-byte header + 16-byte UUID.
	const uuidHeaderLen = 8 + 16
	uuidData := moovContent[uuidStart+uuidHeaderLen : uuidEnd]
	newUUIDContent, hadXMP := rebuildUUIDContent(uuidData, rawEXIF, rawXMP)
	// Append a new "XMP " sub-box if XMP was not already present but is now provided.
	if !hadXMP && rawXMP != nil {
		newUUIDContent = append(newUUIDContent, buildBox("XMP ", rawXMP)...)
	}
	newUUIDBox := buildUUIDBox(canonUUID, newUUIDContent)
	// Rebuild moovContent: prefix + newUUIDBox + suffix.
	capacity := len(moovContent) - (uuidEnd - uuidStart) + len(newUUIDBox)
	newContent := make([]byte, 0, capacity)
	newContent = append(newContent, moovContent[:uuidStart]...)
	newContent = append(newContent, newUUIDBox...)
	newContent = append(newContent, moovContent[uuidEnd:]...)
	return newContent
}

// Inject reads the CR3 from r, replaces the Canon UUID sub-boxes with the
// provided metadata payloads, relocates all trak/stbl stco/co64 chunk-offset
// table entries to account for the change in moov size, and writes the result
// to w.
//
// Offset relocation algorithm (ISO 14496-12 §8.7.3 / §8.7.5):
//
//  1. Parse the flat ISOBMFF box stream to locate moovStart and moovEnd.
//  2. Rebuild the Canon UUID box and the enclosing moov box with the new CMTx
//     payloads; compute delta = len(newMoovBox) - (moovEnd - moovStart).
//  3. Walk every trak → mdia → minf → stbl → {stco, co64} box inside the
//     rebuilt moov. For each chunk offset O:
//     - If O >= oldMoovEnd: set O = O + delta  (the byte it references shifted).
//     - If O < oldMoovEnd: leave unchanged    (points before the shifted region).
//  4. stco entries are uint32; if O + delta > MaxUint32, return ErrStcoOverflow
//     rather than truncate silently.
//  5. Reassemble: data[:moovStart] + newMoovBox + data[moovEnd:].
//
// preserveUnknownSegments must be true; passing false returns
// ErrPreserveUnknownSegmentsNotSupported because CR3 ISOBMFF boxes are
// structurally mandatory and cannot be selectively stripped.
//
// If all payloads are nil the source is passed through unchanged (no moov
// rebuild, no stco/co64 relocation needed).
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	// Reject PreserveUnknownSegments(false) for CR3: ISOBMFF boxes are
	// structurally mandatory. There is no concept of "unknown optional segment"
	// in ISOBMFF analogous to JPEG's APPn segments.
	if !preserveUnknownSegments {
		return ErrPreserveUnknownSegmentsNotSupported
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("cr3: seek: %w", err)
	}
	// #140 fix: cap the full-file read to maxFileSize+1 bytes so that an
	// oversized or infinite streaming reader cannot trigger unbounded heap
	// allocation. ErrFileTooLarge is returned when the limit is exceeded,
	// before any ISOBMFF parsing takes place.
	data, readErr := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if readErr != nil {
		return fmt.Errorf("cr3: read: %w", readErr)
	}
	if int64(len(data)) > maxFileSize {
		return fmt.Errorf("cr3: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}

	// All payloads nil: pass through unchanged.
	// moov size does not change, so stco/co64 tables remain valid.
	if rawEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return writeAll(w, data)
	}

	// Locate the moov box in the flat file stream.
	moovStart, moovEnd, found := findMoovRange(data)
	if !found {
		// No moov box — file is corrupt/incomplete. Pass through unchanged.
		return writeAll(w, data)
	}

	out, err := injectIntoMoov(data, moovStart, moovEnd, rawEXIF, rawXMP)
	if err != nil {
		return err
	}
	return writeAll(w, out)
}

// injectIntoMoov rebuilds the moov box at data[moovStart:moovEnd] with the new
// metadata payloads, relocates stco/co64 offsets, and returns the reassembled
// file bytes. rawIPTC is intentionally ignored: CR3 does not carry IPTC.
func injectIntoMoov(data []byte, moovStart, moovEnd int, rawEXIF, rawXMP []byte) ([]byte, error) {
	// moovContent is the moov box payload (everything after the 8-byte header).
	moovContent := data[moovStart+8 : moovEnd]
	newMoovContent := rebuildMoovContent(moovContent, rawEXIF, rawXMP)
	newMoovBox := buildBox("moov", newMoovContent)

	// Compute the size delta between the new and old moov boxes.
	// delta > 0: moov grew; mdat shifted forward.
	// delta < 0: moov shrank; mdat shifted backward.
	oldMoovSize := moovEnd - moovStart
	delta := int64(len(newMoovBox)) - int64(oldMoovSize)

	// Relocate stco/co64 offsets inside newMoovBox.
	// We patch in-place because newMoovBox was just freshly allocated by buildBox.
	// oldMoovEnd is the absolute file position where mdat begins.
	if delta != 0 {
		if err := relocateChunkOffsets(newMoovBox, moovEnd, delta); err != nil {
			return nil, fmt.Errorf("cr3: stco/co64 offset relocation: %w", err)
		}
	}

	// Reassemble: ftyp (and any pre-moov boxes) + relocated moov + mdat (verbatim).
	// data[moovEnd:] contains mdat and any subsequent boxes; their bytes are intact,
	// only their position in the file has shifted by delta — handled by the patched
	// stco/co64 tables.
	totalLen := len(data) - oldMoovSize + len(newMoovBox)
	out := make([]byte, 0, totalLen)
	out = append(out, data[:moovStart]...)
	out = append(out, newMoovBox...)
	out = append(out, data[moovEnd:]...)
	return out, nil
}

// writeAll writes b to w, wrapping any error with the cr3 prefix.
func writeAll(w io.Writer, b []byte) error {
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("cr3: write: %w", err)
	}
	return nil
}

// buildBox constructs an ISOBMFF box: [4-byte size][4-byte type][content].
func buildBox(boxType string, content []byte) []byte {
	size := 8 + len(content)
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:], uint32(size)) //nolint:gosec // G115: ISOBMFF box size bounded by content length
	copy(box[4:8], boxType)
	copy(box[8:], content)
	return box
}

// buildUUIDBox constructs a uuid box: [8-byte header][16-byte UUID][content].
func buildUUIDBox(uuid []byte, content []byte) []byte {
	size := 8 + 16 + len(content)
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:], uint32(size)) //nolint:gosec // G115: ISOBMFF box size bounded by content length
	copy(box[4:8], "uuid")
	copy(box[8:24], uuid)
	copy(box[24:], content)
	return box
}

// flatUUIDBoxRange finds the Canon UUID box in data (flat scan).
// Returns start and end of the full uuid box (header included).
func flatUUIDBoxRange(data []byte, uuid []byte) (start, end int, found bool) {
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		if typ == "uuid" && pos+int(headerLen)+16 <= len(data) { //nolint:gosec // G115: headerLen is 8 or 16
			if matchesUUID(data[pos+int(headerLen):], uuid) { //nolint:gosec // G115: headerLen is 8 or 16
				return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// findBox performs a search for the first box of the given type in data,
// recursing into container boxes up to depth levels deep (max 32) to
// prevent stack exhaustion on crafted ISOBMFF input.
func findBox(data []byte, boxType string, depth int) []byte {
	if depth > 32 {
		return nil
	}
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		boxData := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
		if typ == boxType {
			return boxData
		}
		// Recurse into container boxes.
		if typ == "moov" || typ == "trak" || typ == "udta" || typ == "mdia" {
			if inner := findBox(boxData, boxType, depth+1); inner != nil {
				return inner
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil
}

// findUUIDBox searches for a 'uuid' box whose UUID matches the given bytes.
func findUUIDBox(data []byte, uuid []byte) []byte {
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		// ISOBMFF (ISO 14496-12) §4.2: a uuid box payload must accommodate
		// both the header and the 16-byte UUID field before the content slice.
		// size >= headerLen is guaranteed by parseCR3BoxHeader; also require
		// size >= headerLen+16 so that data[pos+headerLen+16 : pos+size] is safe.
		if typ == "uuid" && size >= headerLen+16 && pos+int(headerLen)+16 <= len(data) { //nolint:gosec // G115: headerLen is 8 or 16
			if matchesUUID(data[pos+int(headerLen):], uuid) { //nolint:gosec // G115: headerLen is 8 or 16
				return data[pos+int(headerLen)+16 : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil
}

func matchesUUID(data, uuid []byte) bool {
	if len(data) < 16 || len(uuid) < 16 {
		return false
	}
	for i := range 16 {
		if data[i] != uuid[i] {
			return false
		}
	}
	return true
}
