// Package cr3 implements metadata extraction for Canon CR3 files.
// CR3 is an ISOBMFF-based format (ftyp brand "crx ") with Canon-specific
// boxes CMT1, CMT2, CMT3, CMT4 that contain EXIF IFDs.
package cr3

import (
	"encoding/binary"
	"fmt"
	"io"
)

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

	// Find the Canon UUID box inside moov.
	// Canon UUID: {85C0B687-820F-11E0-8111-F4CE462B6A48} stored as raw bytes.
	cannonUUID := []byte{
		0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0,
		0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48,
	}

	moovData := findBox(data, "moov")
	if moovData == nil {
		return nil, nil, nil, fmt.Errorf("cr3: no moov box found")
	}

	uuidData := findUUIDBox(moovData, cannonUUID)
	if uuidData == nil {
		// Fall back: search for CMT1 anywhere in the moov box.
		rawEXIF = findBox(moovData, "CMT1")
		rawXMP = findBox(moovData, "XMP ")
		return rawEXIF, nil, rawXMP, nil
	}

	rawEXIF = findBox(uuidData, "CMT1")
	rawXMP = findBox(uuidData, "XMP ")
	return rawEXIF, nil, rawXMP, nil
}

// Inject writes a modified CR3 stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("cr3: seek: %w", err)
	}
	// Pass through original; full CR3 box rewriting is not yet supported.
	_, err := io.Copy(w, r)
	return err
}

// findBox performs a flat search for the first box of the given type in data.
func findBox(data []byte, boxType string) []byte {
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
			if inner := findBox(boxData, boxType); inner != nil {
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
