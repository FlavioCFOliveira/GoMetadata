// Package cr3 implements metadata extraction for Canon CR3 files.
// CR3 is an ISOBMFF-based format (ftyp brand "crx ") with Canon-specific
// boxes CMT1, CMT2, CMT3, CMT4 that contain EXIF IFDs.
package cr3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Canon UUID: {85C0B687-820F-11E0-8111-F4CE462B6A48} stored as raw bytes.
var canonUUID = []byte{ //nolint:gochecknoglobals // package-level constant bytes
	0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0,
	0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48,
}

// Extract reads metadata from a CR3 file by navigating the ISOBMFF box tree.
// CMT1 contains IFD0 (TIFF header + entries); CMT2 contains the Exif IFD that
// IFD0's ExifIFD pointer (tag 0x8769) addresses. Both are merged into rawEXIF
// so that exif.Parse receives a contiguous buffer covering both IFDs.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("cr3: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cr3: read: %w", err)
	}

	moovData := findBox(data, "moov", 0)
	if moovData == nil {
		return nil, nil, nil, errors.New("cr3: no moov box found")
	}

	uuidData := findUUIDBox(moovData, canonUUID)
	if uuidData == nil {
		// Fall back: search for CMT1/CMT2 anywhere in the moov box.
		cmt1 := findBox(moovData, "CMT1", 0)
		cmt2 := findBox(moovData, "CMT2", 0)
		rawXMP = findBox(moovData, "XMP ", 0)
		return mergeCMT(cmt1, cmt2), nil, rawXMP, nil
	}

	cmt1 := findBox(uuidData, "CMT1", 0)
	cmt2 := findBox(uuidData, "CMT2", 0)
	rawXMP = findBox(uuidData, "XMP ", 0)
	return mergeCMT(cmt1, cmt2), nil, rawXMP, nil
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
	// Parse the TIFF byte-order mark to determine endianness.
	// TIFF 6.0 §2: "II" = little-endian, "MM" = big-endian.
	var exifIFDOffset uint32
	switch {
	case cmt1[0] == 'I' && cmt1[1] == 'I': // little-endian
		if len(cmt1) < 8 {
			return cmt1
		}
		// IFD0 offset is at bytes 4–7 (TIFF header).
		ifd0Off := binary.LittleEndian.Uint32(cmt1[4:8])
		exifIFDOffset = findExifIFDOffset(cmt1, ifd0Off, binary.LittleEndian)
	case cmt1[0] == 'M' && cmt1[1] == 'M': // big-endian
		if len(cmt1) < 8 {
			return cmt1
		}
		ifd0Off := binary.BigEndian.Uint32(cmt1[4:8])
		exifIFDOffset = findExifIFDOffset(cmt1, ifd0Off, binary.BigEndian)
	default:
		return cmt1
	}
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
	for i := 0; i < int(count); i++ {
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

// Inject writes a modified CR3 stream to w by rebuilding the Canon UUID box
// with updated CMT1 (EXIF) and XMP  payloads. All other boxes are preserved
// unchanged. Box sizes in the parent chain (UUID → moov → file) are updated.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("cr3: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("cr3: read: %w", err)
	}

	// Find the moov box in the flat file data.
	moovStart, moovEnd, found := flatBoxRange(data, "moov")
	if !found {
		_, err = w.Write(data)
		if err != nil {
			return fmt.Errorf("cr3: write chunk: %w", err)
		}
		return nil
	}
	moovContent := data[moovStart+8 : moovEnd]

	// Find the Canon UUID box within moov.
	uuidStart, uuidEnd, found := flatUUIDBoxRange(moovContent, canonUUID)
	if !found {
		_, err = w.Write(data)
		if err != nil {
			return fmt.Errorf("cr3: write chunk data: %w", err)
		}
		return nil
	}
	// uuidContent is the payload after the 8-byte box header + 16-byte UUID.
	uuidContent := moovContent[uuidStart+8+16 : uuidEnd]

	// Rebuild the UUID content: iterate sub-boxes and replace CMT1/XMP  as needed.
	var newUUIDContent bytes.Buffer
	hadXMP := false
	pos := 0
	for pos+8 <= len(uuidContent) {
		size := uint64(binary.BigEndian.Uint32(uuidContent[pos:]))
		typ := string(uuidContent[pos+4 : pos+8])
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(uuidContent) {
				break
			}
			size = binary.BigEndian.Uint64(uuidContent[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(uuidContent) - pos)
		}
		if uint64(pos)+size > uint64(len(uuidContent)) {
			break
		}
		boxPayload := uuidContent[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size

		switch typ {
		case "CMT1":
			if rawEXIF != nil {
				newUUIDContent.Write(buildBox("CMT1", rawEXIF))
			} else {
				newUUIDContent.Write(uuidContent[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		case "XMP ":
			hadXMP = true
			if rawXMP != nil {
				newUUIDContent.Write(buildBox("XMP ", rawXMP))
			} else {
				newUUIDContent.Write(uuidContent[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		default:
			_ = boxPayload
			newUUIDContent.Write(uuidContent[pos : pos+int(size)]) //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}

	// If the UUID box didn't have an XMP  sub-box but we have rawXMP, append it.
	if !hadXMP && rawXMP != nil {
		newUUIDContent.Write(buildBox("XMP ", rawXMP))
	}

	// Build the new UUID box: 8-byte header + 16-byte Canon UUID + new content.
	newUUIDBox := buildUUIDBox(canonUUID, newUUIDContent.Bytes())

	// Splice: replace the old UUID box in moov content with the new one.
	newMoovContent := make([]byte, 0, len(moovContent)-uuidEnd+len(newUUIDBox)+uuidStart)
	newMoovContent = append(newMoovContent, moovContent[:uuidStart]...)
	newMoovContent = append(newMoovContent, newUUIDBox...)
	newMoovContent = append(newMoovContent, moovContent[uuidEnd:]...)

	// Build the new moov box.
	newMoovBox := buildBox("moov", newMoovContent)

	// Write: data before moov + new moov + data after moov.
	var out bytes.Buffer
	out.Write(data[:moovStart])
	out.Write(newMoovBox)
	out.Write(data[moovEnd:])
	_, err = w.Write(out.Bytes())
	if err != nil {
		return fmt.Errorf("cr3: write box: %w", err)
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

// flatBoxRange finds the first box of the given type in data (flat scan).
// Returns the start and end (exclusive) of the full box (header + content).
func flatBoxRange(data []byte, boxType string) (start, end int, found bool) {
	pos := 0
	for pos+8 <= len(data) {
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
			break
		}
		if typ == boxType {
			return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		_ = headerLen
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// flatUUIDBoxRange finds the Canon UUID box in data (flat scan).
// Returns start and end of the full uuid box (header included).
func flatUUIDBoxRange(data []byte, uuid []byte) (start, end int, found bool) {
	pos := 0
	for pos+8 <= len(data) {
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
			break
		}
		if typ == "uuid" && pos+int(headerLen)+16 <= len(data) {
			if matchesUUID(data[pos+int(headerLen):], uuid) {
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
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
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
		size := uint64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		headerLen := uint64(8)
		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = binary.BigEndian.Uint64(data[pos+8:])
			headerLen = 16
		}
		if size == 0 {
			size = uint64(len(data) - pos)
		}
		if uint64(pos)+size > uint64(len(data)) {
			break
		}
		if typ == "uuid" && pos+int(headerLen)+16 <= len(data) {
			if matchesUUID(data[pos+int(headerLen):], uuid) {
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
	for i := 0; i < 16; i++ {
		if data[i] != uuid[i] {
			return false
		}
	}
	return true
}
