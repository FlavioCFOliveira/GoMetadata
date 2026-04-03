// Package rw2 implements metadata extraction for Panasonic RW2 files.
// RW2 is a TIFF variant with the byte order marker "IIU\x00" and uses
// non-standard IFD entry counts and offset encoding.
package rw2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/flaviocfo/img-metadata/format/tiff"
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

	if len(patched) < 8 {
		return patched, nil, nil, nil
	}

	order := binary.LittleEndian
	rawEXIF = patched

	ifd0Off := order.Uint32(patched[4:])
	// Best-effort extraction; RW2 IFD encoding may differ from standard TIFF.
	rawIPTC, rawXMP = extractTIFFTags(patched, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified RW2 stream to w by delegating to the TIFF writer.
// RW2 magic bytes are patched to standard TIFF LE before injection and
// restored in the output so the file remains a valid RW2.
// Note: RW2 uses non-standard IFD encoding; some entries may not survive the
// round-trip, but EXIF/IPTC/XMP metadata is correctly updated.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rw2: seek: %w", err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("rw2: read: %w", err)
	}
	if !bytes.HasPrefix(data, rw2Magic) {
		return fmt.Errorf("rw2: invalid magic bytes")
	}

	// Patch bytes 2-3 to standard TIFF LE magic so tiff.Inject works.
	patched := make([]byte, len(data))
	copy(patched, data)
	patched[2] = 0x2A
	patched[3] = 0x00

	// Buffer the TIFF output so we can restore the RW2 magic bytes.
	var buf bytes.Buffer
	if err := tiff.Inject(bytes.NewReader(patched), &buf, rawEXIF, rawIPTC, rawXMP); err != nil {
		return fmt.Errorf("rw2: inject: %w", err)
	}

	out := buf.Bytes()
	if len(out) < 4 {
		return fmt.Errorf("rw2: output too short")
	}
	// Restore RW2 magic ("IIU\x00") in the output.
	out[2] = rw2Magic[2]
	out[3] = rw2Magic[3]

	_, err = w.Write(out)
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
			// Guard against integer overflow: check before computing end.
			if uint64(off) > uint64(len(data)) || total > uint64(len(data))-uint64(off) {
				continue
			}
			v = data[uint64(off) : uint64(off)+total]
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
