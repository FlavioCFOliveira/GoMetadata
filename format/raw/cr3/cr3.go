// Package cr3 implements metadata extraction for Canon CR3 files.
// CR3 is an ISOBMFF-based format (ftyp brand "crx ") with Canon-specific
// boxes CMT1, CMT2, CMT3, CMT4 that contain EXIF IFDs.
package cr3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Canon UUID: {85C0B687-820F-11E0-8111-F4CE462B6A48} stored as raw bytes.
var canonUUID = []byte{
	0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0,
	0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48,
}

// Extract reads metadata from a CR3 file by navigating the ISOBMFF box tree.
// CMT1 contains IFD0 (TIFF header + entries), CMT2 contains the Exif IFD.
// The returned rawEXIF is the CMT1 content, which is a valid TIFF stream.
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
		return nil, nil, nil, fmt.Errorf("cr3: no moov box found")
	}

	uuidData := findUUIDBox(moovData, canonUUID)
	if uuidData == nil {
		// Fall back: search for CMT1 anywhere in the moov box.
		rawEXIF = findBox(moovData, "CMT1", 0)
		rawXMP = findBox(moovData, "XMP ", 0)
		return rawEXIF, nil, rawXMP, nil
	}

	rawEXIF = findBox(uuidData, "CMT1", 0)
	rawXMP = findBox(uuidData, "XMP ", 0)
	return rawEXIF, nil, rawXMP, nil
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
		return err
	}
	moovContent := data[moovStart+8 : moovEnd]

	// Find the Canon UUID box within moov.
	uuidStart, uuidEnd, found := flatUUIDBoxRange(moovContent, canonUUID)
	if !found {
		_, err = w.Write(data)
		return err
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
		boxPayload := uuidContent[pos+int(headerLen) : pos+int(size)]

		switch typ {
		case "CMT1":
			if rawEXIF != nil {
				newUUIDContent.Write(buildBox("CMT1", rawEXIF))
			} else {
				newUUIDContent.Write(uuidContent[pos : pos+int(size)])
			}
		case "XMP ":
			hadXMP = true
			if rawXMP != nil {
				newUUIDContent.Write(buildBox("XMP ", rawXMP))
			} else {
				newUUIDContent.Write(uuidContent[pos : pos+int(size)])
			}
		default:
			_ = boxPayload
			newUUIDContent.Write(uuidContent[pos : pos+int(size)])
		}
		pos += int(size)
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
	return err
}

// buildBox constructs an ISOBMFF box: [4-byte size][4-byte type][content].
func buildBox(boxType string, content []byte) []byte {
	size := 8 + len(content)
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:], uint32(size))
	copy(box[4:8], boxType)
	copy(box[8:], content)
	return box
}

// buildUUIDBox constructs a uuid box: [8-byte header][16-byte UUID][content].
func buildUUIDBox(uuid []byte, content []byte) []byte {
	size := 8 + 16 + len(content)
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:], uint32(size))
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
			return pos, pos + int(size), true
		}
		_ = headerLen
		pos += int(size)
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
				return pos, pos + int(size), true
			}
		}
		pos += int(size)
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
		boxData := data[pos+int(headerLen) : pos+int(size)]
		if typ == boxType {
			return boxData
		}
		// Recurse into container boxes.
		if typ == "moov" || typ == "trak" || typ == "udta" || typ == "mdia" {
			if inner := findBox(boxData, boxType, depth+1); inner != nil {
				return inner
			}
		}
		pos += int(size)
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
				return data[pos+int(headerLen)+16 : pos+int(size)]
			}
		}
		pos += int(size)
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
