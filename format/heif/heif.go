// Package heif implements extraction and injection of EXIF and XMP metadata
// within HEIF/HEIC files (ISO 23008-12 / ISO 14496-12 ISOBMFF).
//
// HEIF stores metadata as items referenced from the 'meta' box.
// The EXIF item has handler type 'Exif'; the XMP item has content type
// "application/rdf+xml". Item locations are resolved via the 'iloc' box.
package heif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// xmpWireFrameMagic is the 8-byte sentinel that identifies a JPEG extended-XMP
// wire-frame payload (defined in format/jpeg; duplicated here to avoid an import
// cycle). The wire-frame is an internal encoding used exclusively by jpeg.Inject;
// it must never reach the HEIF injector.
//
// Layout: [0x00]['X']['M']['P']['E']['X']['T'][0x00]
// The leading 0x00 is unambiguous: no valid XMP packet starts with a null byte.
var xmpWireFrameMagic = [8]byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00} //nolint:gochecknoglobals // package-level constant bytes

// ISOBMFF box types compared against parseHEIFBoxHeader's [4]byte result
// (ISO 14496-12 §4.2: the box type is a 4-byte code). Never mutated.
//
// Comparing [4]byte values is allocation-free, unlike the former
// string-keyed comparison, which allocated a fresh string for every box
// header parsed (task #228).
var (
	boxTypeMeta = [4]byte{'m', 'e', 't', 'a'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeMoov = [4]byte{'m', 'o', 'o', 'v'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeTrak = [4]byte{'t', 'r', 'a', 'k'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeUdta = [4]byte{'u', 'd', 't', 'a'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeIinf = [4]byte{'i', 'i', 'n', 'f'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeIloc = [4]byte{'i', 'l', 'o', 'c'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypePitm = [4]byte{'p', 'i', 't', 'm'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeInfe = [4]byte{'i', 'n', 'f', 'e'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeDinf = [4]byte{'d', 'i', 'n', 'f'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeDref = [4]byte{'d', 'r', 'e', 'f'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTypeIdat = [4]byte{'i', 'd', 'a', 't'} //nolint:gochecknoglobals // package-level constant box type; never mutated
)

// itemTypeExif and itemTypeMime identify a HEIF item by its 4-byte ISOBMFF
// item_type field (ISO 14496-12 §8.11.6). itemTypeMime is also the synthetic
// code parseInfeV0V1 returns when an infe v0/v1 box's NUL-terminated
// content_type equals "application/rdf+xml": infe v0/v1 has no formal
// item_type field, so ISO 23008-12 §6.2 identifies XMP items by content_type
// instead. Comparing these [4]byte values is allocation-free (task #228).
var (
	itemTypeExif = [4]byte{'E', 'x', 'i', 'f'} //nolint:gochecknoglobals // package-level constant item type; never mutated
	itemTypeMime = [4]byte{'m', 'i', 'm', 'e'} //nolint:gochecknoglobals // package-level constant item type; never mutated
)

// xmpContentType is the content_type of an XMP item (ISO 23008-12 Annex A.3;
// libavif AVIF_CONTENT_TYPE_XMP). A 'mime' item is the XMP item only when its
// content_type equals this string: 'mime' items also carry other MIME
// payloads, which must never be read or overwritten as XMP.
const xmpContentType = "application/rdf+xml"

// Extract navigates the ISOBMFF box hierarchy of r and extracts raw payloads.
// rawEXIF has the 4-byte TIFF-header offset prefix stripped before return.
//
// Fast path: reads only the first 64 KB to locate the meta box (iinf+iloc+pitm),
// then seeks directly to each item payload — no full-file allocation needed.
// Slow path fallback (meta box beyond 64 KB header window): reads the full file.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("heif: seek: %w", err)
	}

	// Read the first 64 KB: sufficient to contain the meta box in all typical
	// HEIF/HEIC files (iinf + iloc + pitm are compact; < 4 KB in practice).
	// largePool always provides a cap-65536 buffer so Get never allocates fresh.
	const headerWindow = 65536
	hdrPtr := iobuf.Get(headerWindow)
	hdr := *hdrPtr
	n, rerr := io.ReadFull(r, hdr[:headerWindow])
	if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) {
		iobuf.Put(hdrPtr)
		// io.EOF means the file is empty (n==0): no metadata, no error.
		// ISO 14496-12 §4: an empty or too-short file simply carries no boxes.
		if errors.Is(rerr, io.EOF) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("heif: read: %w", rerr)
	}
	hdr = hdr[:n]

	// Find the meta box within the header window.
	metaData, _ := findBox(hdr, boxTypeMeta, 0)
	if metaData != nil {
		// Fast path: meta box fully within header window.
		// Read item payloads by seeking rather than slicing a full in-memory copy.
		// extractFromMetaData reads from metaData (a subslice of *hdrPtr), so
		// iobuf.Put must be called AFTER extractFromMetaData returns — returning
		// hdrPtr to the pool first would allow another goroutine to overwrite the
		// buffer while extractFromMetaData is still reading it (data race).
		rawEXIF, rawXMP, err = extractFromMetaData(r, metaData)
		iobuf.Put(hdrPtr)
		return rawEXIF, nil, rawXMP, err
	}

	// Slow path fallback: meta box not found in first 64 KB (extremely rare —
	// HEIF spec places meta at the file head). Read the full file.
	iobuf.Put(hdrPtr)
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("heif: seek: %w", err)
	}
	// #140 fix: slow-path fallback — cap the full-file read to maxFileSize+1
	// bytes so that an oversized or infinite streaming reader cannot trigger
	// unbounded heap allocation. ErrFileTooLarge is returned when the limit is
	// exceeded, before any further parsing takes place.
	data, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("heif: read: %w", err)
	}
	if int64(len(data)) > maxFileSize {
		return nil, nil, nil, fmt.Errorf("heif: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	rawEXIF, rawXMP, err = parseHEIFMetadata(data)
	return rawEXIF, nil, rawXMP, err
}

// extractFromMetaData parses the meta box content and seeks r to read EXIF/XMP
// item payloads directly, avoiding a full file read.
// metaData must be the content of the meta box with the 4-byte FullBox
// version+flags already stripped (as returned by findBox).
// ISO 23008-12 §6.2 (primary item), §6.6.1 (EXIF item layout).
//
// Only the iloc entries of the two selected items are resolved
// (findIlocItem); the iloc box is never materialised into a per-item map.
func extractFromMetaData(r io.ReadSeeker, metaData []byte) (rawEXIF, rawXMP []byte, err error) {
	itemTypes := parseIinf(metaData)
	primaryID := parsePitm(metaData)

	bestEXIFID, exifFound := selectBestItem(itemTypes, primaryID, itemTypeExif)
	// The former string-typed target list also included "rdf+xml" (7 bytes) as
	// a candidate. ISO 14496-12 §8.11.6 fixes item_type at exactly 4 bytes, so
	// a 7-byte value could never equal any parsed item type; that candidate
	// was unreachable dead code even before the [4]byte conversion (task
	// #228) and is intentionally not carried forward.
	bestXMPID, xmpFound := selectBestItem(itemTypes, primaryID, itemTypeMime)
	if !exifFound && !xmpFound {
		return nil, nil, nil
	}

	ilocData := findInnerBox(metaData, boxTypeIloc)
	h, ok := parseIlocHeader(ilocData)
	if !ok {
		return nil, nil, nil
	}
	src := itemSource{r: r, metaContent: metaData}

	if exifFound {
		if it, extPos, found := findIlocItem(ilocData, &h, bestEXIFID); found {
			payload, e := readIlocItem(src, ilocData, &h, it, extPos)
			if e == nil {
				rawEXIF = extractExifFromData(payload)
			}
		}
	}
	if xmpFound {
		if it, extPos, found := findIlocItem(ilocData, &h, bestXMPID); found {
			rawXMP, err = readIlocItem(src, ilocData, &h, it, extPos)
		}
	}
	return rawEXIF, rawXMP, err
}

// parseHEIFMetadata parses the box hierarchy of a fully buffered file to find
// EXIF and XMP items (the Extract slow path). When multiple items of the same
// type exist, the primary item (per 'pitm' box) is preferred; otherwise the
// item with the lowest ID is selected.
// ISO 23008-12 §6.2 (primary item), §6.6.1 (EXIF item layout).
//
// Item payloads are read through extractFromMetaData into freshly allocated,
// exactly-sized slices, so the returned rawEXIF/rawXMP never retain data.
func parseHEIFMetadata(data []byte) (rawEXIF, rawXMP []byte, err error) {
	// Find the 'meta' box. It can be inside 'moov' or at top-level.
	metaData, err := findBox(data, boxTypeMeta, 0)
	if err != nil {
		return nil, nil, err
	}
	if metaData == nil {
		return nil, nil, nil
	}
	return extractFromMetaData(bytes.NewReader(data), metaData)
}

// parseHEIFBoxHeader reads an ISOBMFF box header starting at pos in data.
// It handles both the 8-byte base form and the 16-byte extended-size form
// (size == 1). Returns size, the 4-byte box type, headerLen, and whether the
// read succeeded. The box type is returned by value, so parsing a header
// never allocates (task #228).
//
// ISO 14496-12 §4.2: if size == 1, a 64-bit largesize field follows the type.
// If size == 0, the box extends to the end of the containing structure.
func parseHEIFBoxHeader(data []byte, pos int) (size uint64, typ [4]byte, headerLen uint64, valid bool) {
	if pos+8 > len(data) {
		return 0, [4]byte{}, 0, false
	}
	size = uint64(binary.BigEndian.Uint32(data[pos:]))
	typ = [4]byte(data[pos+4 : pos+8])
	headerLen = 8

	if size == 1 {
		if pos+16 > len(data) {
			return 0, [4]byte{}, 0, false
		}
		size = binary.BigEndian.Uint64(data[pos+8:])
		headerLen = 16
	}
	if size == 0 {
		// len(data)-pos is non-negative: guarded by pos+8 ≤ len(data) check above.
		size = uint64(len(data) - pos) //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
	}
	// Bounds check without casting pos: size must not exceed remaining bytes.
	if size > uint64(len(data)-pos) { //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
		return 0, [4]byte{}, 0, false
	}
	// ISO 14496-12 §4.2: a box whose declared size is smaller than its own
	// header length is unconditionally malformed — the payload slice arithmetic
	// data[pos+headerLen : pos+size] would panic (e.g. [8:4] for size=4).
	if size < headerLen {
		return 0, [4]byte{}, 0, false
	}
	return size, typ, headerLen, true
}

// selectBestItem selects the best item ID from itemTypes matching one of
// targetTypes. The primary item is preferred; among non-primary items the
// lowest ID is chosen for determinism.
// ISO 23008-12 §6.2 (primary item selection).
func selectBestItem(itemTypes map[uint16][4]byte, primaryID uint16, targetTypes ...[4]byte) (bestID uint16, found bool) {
	for id, typ := range itemTypes {
		if !slices.Contains(targetTypes, typ) {
			continue
		}
		if !found || id == primaryID || (bestID != primaryID && id < bestID) {
			bestID = id
			found = true
		}
	}
	return bestID, found
}

// extractExifFromData reads an HEIF EXIF item payload, validates its length,
// computes the skip offset, and returns the trimmed EXIF bytes.
// Returns nil if the data is too short or the skip offset is out of range.
//
// ISO 23008-12 §6.6.1: EXIF item begins with a 4-byte offset to the TIFF
// header within the item. Skip = value + 4 (to account for the prefix itself).
func extractExifFromData(data []byte) []byte {
	if len(data) < 4 {
		return nil
	}
	// Security audit HEIF-32BIT-01 (CWE-681 -> CWE-190 -> CWE-129): compute the
	// skip offset in uint64, not int, before comparing against len(data). On a
	// 32-bit platform (GOARCH=386/arm/mips/mipsle) int is 32-bit, so
	// int(uint32 value >= 2^31) wraps negative; a negative skip is never
	// > len(data), so the guard below would be silently bypassed and
	// data[skip:] would panic with a negative slice index. uint64 arithmetic
	// has no such wraparound on any platform. Mirrors the #74 fix in
	// format/detect.go's parseClassicTIFFIFD0 and the FIX 5 fix in
	// format/tiff/tiff.go's extractTagValues.
	skip := uint64(binary.BigEndian.Uint32(data[:4])) + 4
	if skip > uint64(len(data)) {
		return nil
	}
	// skip <= uint64(len(data)) and len(data) is a valid int on this
	// platform, so skip fits in int safely here.
	return data[skip:]
}

// parsePitm parses the 'pitm' FullBox from metaContent and returns the primary
// item ID. Returns 0 if the box is absent or cannot be parsed.
// ISO 14496-12 §8.11.4 (pitm box).
func parsePitm(metaContent []byte) uint16 {
	pitm := findInnerBox(metaContent, boxTypePitm)
	if pitm == nil {
		return 0
	}
	// FullBox: version(1) + flags(3); then item_ID.
	// version 0: item_ID is uint16; version 1: item_ID is uint32.
	if len(pitm) < 1 {
		return 0
	}
	version := pitm[0]
	pos := 4 // skip version + flags
	if version == 0 {
		if pos+2 > len(pitm) {
			return 0
		}
		return binary.BigEndian.Uint16(pitm[pos:])
	}
	// version 1
	if pos+4 > len(pitm) {
		return 0
	}
	id := binary.BigEndian.Uint32(pitm[pos:])
	if id > 0xFFFF {
		return 0
	}
	return uint16(id)
}

// isContainerBox reports whether typ is an ISOBMFF container box that may
// contain nested boxes (and therefore warrants recursive descent).
func isContainerBox(typ [4]byte) bool {
	return typ == boxTypeMoov || typ == boxTypeTrak || typ == boxTypeUdta
}

// findBox searches data for the first box of the given type, returning its data.
// It performs a shallow search at the top level and recurses into container boxes
// up to depth levels deep (max 32) to prevent stack exhaustion on crafted input.
func findBox(data []byte, boxType [4]byte, depth int) ([]byte, error) {
	if depth > 32 {
		return nil, ErrMaxNestingDepth
	}
	pos := 0
	for pos < len(data) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}

		boxData := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size

		if typ == boxType {
			// Skip the 4-byte version/flags for full boxes.
			if len(boxData) >= 4 {
				return boxData[4:], nil
			}
			return boxData, nil
		}

		// Recurse into container boxes.
		if isContainerBox(typ) {
			inner, err := findBox(boxData, boxType, depth+1)
			if err == nil && inner != nil {
				return inner, nil
			}
		}

		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil, nil
}

// parseIinfItemCount decodes the item_count field from iinfData (or ilocData)
// at pos, using version to determine whether it is a 2-byte or 4-byte field.
// Returns count, updated pos, and success.
//
// ISO 14496-12 §8.11.3 / §8.11.6: version < 2 uses uint16, version 2 uses uint32.
func parseIinfItemCount(data []byte, version byte, pos int) (count, newPos int, ok bool) {
	if version < 2 {
		if pos+2 > len(data) {
			return 0, pos, false
		}
		return int(binary.BigEndian.Uint16(data[pos:])), pos + 2, true
	}
	if pos+4 > len(data) {
		return 0, pos, false
	}
	// Security audit HEIF-32BIT-01 (CWE-681 -> CWE-190 -> CWE-129, class
	// consistency with extractExifFromData above): decode in uint64 first and
	// reject any item_count that would not fit in a non-negative int on a
	// 32-bit platform (GOARCH=386/arm/mips/mipsle), where int is 32-bit and
	// int(uint32 value >= 2^31) wraps negative. No legitimate ISOBMFF file
	// declares anywhere near this many items — both call sites additionally
	// cap at maxIlocItems (4096) before doing per-item work — so this changes
	// no observable behavior for well-formed input.
	raw := uint64(binary.BigEndian.Uint32(data[pos:]))
	if raw > uint64(math.MaxInt32) {
		return 0, pos, false
	}
	return int(raw), pos + 4, true
}

// parseIinf parses an 'iinf' box body and returns a map from item ID to its
// 4-byte item type (task #228: [4]byte throughout — no per-box string
// allocation).
func parseIinf(metaData []byte) map[uint16][4]byte {
	result := make(map[uint16][4]byte)
	iinfData := findInnerBox(metaData, boxTypeIinf)
	if iinfData == nil {
		return result
	}

	// iinf: version(1) + flags(3) + item_count(2 or 4)
	if len(iinfData) < 2 {
		return result
	}
	version := iinfData[0]
	pos := 4 // skip version + flags

	itemCount, pos, ok := parseIinfItemCount(iinfData, version, pos)
	if !ok {
		return result
	}
	_ = itemCount

	// Parse infe (item info entry) boxes.
	for pos < len(iinfData) {
		size, typ, _, ok := parseHEIFBoxHeader(iinfData, pos)
		if !ok || size < 8 {
			break
		}
		if typ == boxTypeInfe {
			infeData := iinfData[pos+8 : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
			id, itemType, ok := parseInfe(infeData)
			if ok {
				result[id] = itemType
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return result
}

// parseInfe parses an 'infe' box body and returns (item_ID, item_type, ok).
// Handles infe versions 0, 1, 2, and 3 per ISO 14496-12 §8.11.6.
func parseInfe(data []byte) (id uint16, typ [4]byte, ok bool) {
	if len(data) < 4 {
		return 0, [4]byte{}, false
	}
	version := data[0]
	// Skip version + flags (4 bytes total).
	pos := 4

	switch version {
	case 0, 1:
		return parseInfeV0V1(data, pos)
	case 2:
		return parseInfeV2V3(data, pos, 2)
	case 3:
		return parseInfeV2V3(data, pos, 3)
	}
	return 0, [4]byte{}, false
}

// parseInfeV0V1 handles infe version 0 and 1 parsing.
// infe v0/v1: item_ID(2) + item_protection_index(2) + item_name(NUL-term)
// + content_type(NUL-term) [+ content_encoding(NUL-term) for v1].
// The item type is derived from the content_type MIME string.
// ISO 14496-12 §8.11.6.
func parseInfeV0V1(data []byte, pos int) (id uint16, typ [4]byte, ok bool) {
	if pos+2 > len(data) {
		return 0, [4]byte{}, false
	}
	id = binary.BigEndian.Uint16(data[pos:])
	pos += 2
	// #106 fix: bounds-check before skipping item_protection_index(2).
	// ISO 14496-12 §8.11.6: item_protection_index is a mandatory 2-byte field.
	// Without this guard, a 2-byte body (item_ID only) panics on data[pos:] when
	// pos > len(data) (regression gate: TestHEIFRobustInfeV0V1Truncated).
	if pos+2 > len(data) {
		return id, [4]byte{}, false
	}
	pos += 2 // item_protection_index
	// Skip item_name (NUL-terminated string).
	nul := bytes.IndexByte(data[pos:], 0x00)
	if nul < 0 {
		return id, [4]byte{}, false
	}
	pos += nul + 1
	// Read content_type (NUL-terminated MIME string).
	nul2 := bytes.IndexByte(data[pos:], 0x00)
	var contentType string
	if nul2 >= 0 {
		contentType = string(data[pos : pos+nul2])
	} else {
		contentType = string(data[pos:])
	}
	// Map content type to the internal item type code.
	// Exif in v0/v1 has no formal item_type; the item_content_type is typically
	// empty or "image/jpeg". We cannot reliably identify Exif items in v0/v1.
	if contentType == "application/rdf+xml" {
		return id, itemTypeMime, true
	}
	return id, [4]byte{}, false
}

// parseInfeV2V3 handles infe version 2 and 3 parsing.
// v2: item_ID is uint16; v3: item_ID is uint32 (must fit uint16).
// Both versions include: item_protection_index(2) + item_type(4), then
// item_name (NUL-terminated) and, for item_type 'mime', content_type
// (NUL-terminated). ISO 14496-12 §8.11.6.2.
//
// A 'mime' item is reported as itemTypeMime (the XMP item) only when its
// content_type is "application/rdf+xml", the test libavif applies
// (avifDecoderFindMetadata). Any other 'mime' item is reported with the zero
// type: a valid item that carries no metadata this package handles.
func parseInfeV2V3(data []byte, pos int, version byte) (id uint16, typ [4]byte, ok bool) {
	if version == 2 {
		if pos+2 > len(data) {
			return 0, [4]byte{}, false
		}
		id = binary.BigEndian.Uint16(data[pos:])
		pos += 2
	} else {
		// version 3: item_ID is uint32
		if pos+4 > len(data) {
			return 0, [4]byte{}, false
		}
		rawID := binary.BigEndian.Uint32(data[pos:])
		if rawID > math.MaxUint16 {
			return 0, [4]byte{}, false // item ID exceeds uint16 range; malformed infe box
		}
		id = uint16(rawID)
		pos += 4
	}
	pos += 2 // item_protection_index
	if pos+4 > len(data) {
		return 0, [4]byte{}, false
	}
	// Direct array conversion from the slice: zero allocation, unlike the
	// former string(data[pos:pos+4]) conversion (task #228).
	typ = [4]byte(data[pos : pos+4])
	if typ == itemTypeMime && !isXMPContentType(data[pos+4:]) {
		return id, [4]byte{}, true
	}
	return id, typ, true
}

// isXMPContentType reports whether rest — the bytes of a 'mime' infe body
// after item_type — holds the item_name string followed by the
// content_type "application/rdf+xml". Both strings are NUL-terminated
// (ISO 14496-12 §8.11.6.2); an unterminated content_type running to the end
// of the box is accepted. The comparison does not allocate.
func isXMPContentType(rest []byte) bool {
	_, ct, found := bytes.Cut(rest, []byte{0x00}) // skip item_name
	if !found {
		return false
	}
	ct, _, _ = bytes.Cut(ct, []byte{0x00})
	return string(ct) == xmpContentType
}

// metaFullBoxMinSize is the minimum valid size for a meta FullBox:
// 4 bytes box-header-size + 4 bytes box-type + 4 bytes FullBox version/flags = 12 bytes.
// A meta box with size < 12 bytes cannot contain valid FullBox metadata.
// ISO 14496-12 §8.11.1: meta is a FullBox; its minimum declared size is 12.
const metaFullBoxMinSize = 12

// findMetaBoxAbs finds the 'meta' FullBox in the file and returns its
// absolute start, end, and the offset where its content begins (after header+version/flags).
// Returns found=false when the meta box is absent or smaller than metaFullBoxMinSize
// (an under-sized FullBox cannot contain usable content and would otherwise cause
// buildInjectComponents to slice data[12:8] → panic; regression gate:
// TestHEIFInjectMetaTooSmallForFullBox).
// ISO 14496-12 §8.11.1: meta FullBox minimum size = 12 bytes.
func findMetaBoxAbs(data []byte) (absStart, absEnd, contentOff int, found bool) {
	// Search at top level first.
	if s, e, ok := flatBoxRangeInFile(data, boxTypeMeta); ok {
		if e-s < metaFullBoxMinSize {
			// #169 fix: meta box too small to be a valid FullBox.
			// ISO 14496-12 §8.11.1: meta is a FullBox (header[8]+version/flags[4]=12 bytes minimum).
			// If size < 12, contentOff = s+12 would exceed e, causing data[s+12:e] to panic.
			return 0, 0, 0, false
		}
		return s, e, s + 8 + 4, true // +8 header, +4 FullBox version/flags
	}
	// Search inside moov.
	ms, me, ok := flatBoxRangeInFile(data, boxTypeMoov)
	if !ok {
		return 0, 0, 0, false
	}
	moovContent := data[ms+8 : me]
	if s, e, ok := flatBoxRangeInFile(moovContent, boxTypeMeta); ok {
		if e-s < metaFullBoxMinSize {
			// Same size guard for the moov-nested meta box.
			return 0, 0, 0, false
		}
		absS := ms + 8 + s
		absE := ms + 8 + e
		return absS, absE, absS + 8 + 4, true
	}
	return 0, 0, 0, false
}

// flatBoxRangeInFile performs a flat scan and returns start+end of the first
// box matching boxType. Returns (0, 0, false) if not found.
func flatBoxRangeInFile(data []byte, boxType [4]byte) (start, end int, found bool) {
	pos := 0
	for pos < len(data) {
		size, typ, _, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		if typ == boxType {
			return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// findInnerBox searches for a box of the given type within data (flat scan).
func findInnerBox(data []byte, boxType [4]byte) []byte {
	pos := 0
	for pos < len(data) {
		size, typ, headerLen, ok := parseHEIFBoxHeader(data, pos)
		if !ok {
			break
		}
		// A box whose size is smaller than its header is malformed; stop.
		if size < headerLen {
			break
		}
		if typ == boxType {
			boxData := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
			return boxData
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil
}
