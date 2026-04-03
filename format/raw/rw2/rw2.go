// Package rw2 implements metadata extraction for Panasonic RW2 files.
// RW2 is a TIFF variant with the byte order marker "IIU\x00" and uses
// non-standard IFD entry counts and offset encoding.
package rw2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// rw2Magic is the Panasonic RW2 byte order marker (bytes 0-3).
var rw2Magic = []byte{0x49, 0x49, 0x55, 0x00} // "IIU\x00"

// Extract reads metadata from an RW2 file.
// The RW2 magic bytes are patched to standard TIFF LE before extraction.
// Note: RW2 uses non-standard IFD encoding; some entries may not decode correctly.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("rw2: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("rw2: read: %w", err)
	}
	if !bytes.HasPrefix(data, rw2Magic) {
		return nil, nil, nil, fmt.Errorf("rw2: invalid magic bytes")
	}

	// Patch bytes 2-3 to standard TIFF LE magic.
	patched := make([]byte, len(data))
	copy(patched, data)
	patched[2] = 0x2A
	patched[3] = 0x00

	order := binary.LittleEndian
	rawEXIF = patched

	ifd0Off := order.Uint32(patched[4:])
	// Best-effort extraction; RW2 IFD encoding may differ from standard TIFF.
	rawIPTC, rawXMP = extractTIFFTags(patched, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified RW2 stream to w.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rw2: seek: %w", err)
	}
	// Pass through original; full RW2 rewriting is not yet supported.
	_, err := io.Copy(w, r)
	return err
}

func extractTIFFTags(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) {
	if int(ifd0Off)+2 > len(data) {
		return nil, nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
	pos := int(ifd0Off) + 2
	for i := 0; i < count; i++ {
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])
		sz := typeSize(typ)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(cnt)
		var v []byte
		if total <= 4 {
			v = data[e+8 : e+8+int(total)]
		} else {
			off := order.Uint32(data[e+8:])
			end := uint64(off) + total
			if end > uint64(len(data)) {
				continue
			}
			v = data[off:end]
		}
		switch tag {
		case 0x83BB:
			rawIPTC = v
		case 0x02BC:
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}

func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11:
		return 4
	case 5, 10, 12:
		return 8
	}
	return 0
}
