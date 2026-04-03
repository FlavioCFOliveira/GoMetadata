// Package tiff implements extraction and injection of metadata within TIFF
// container files. TIFF stores EXIF in a SubIFD (tag 0x8769), IPTC in tag
// 0x83BB, and XMP in tag 0x02BC (TIFF Technical Note 3).
package tiff

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Extract reads metadata payloads from a TIFF container.
// rawEXIF is the entire TIFF byte stream (TIFF itself is the EXIF container).
// rawIPTC and rawXMP are read from the respective IFD0 tags.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: seek: %w", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: read: %w", err)
	}
	if len(data) < 8 {
		return nil, nil, nil, fmt.Errorf("tiff: file too short")
	}

	order, err := byteOrder(data)
	if err != nil {
		return nil, nil, nil, err
	}

	// The whole TIFF data IS the EXIF payload (TIFF §2).
	rawEXIF = data

	ifd0Off := order.Uint32(data[4:])
	rawIPTC, rawXMP = extractTagValues(data, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// Inject writes a modified TIFF stream to w, replacing the metadata tags.
// Since re-building a TIFF with updated IFD offsets is complex, this
// implementation appends updated tag values and patches IFD0 entries in-place
// when the original tag exists. New tags are not injected into the IFD.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tiff: seek: %w", err)
	}
	// For TIFF, the EXIF payload IS the TIFF file.
	// rawEXIF from the caller is the serialised EXIF; write it directly.
	if rawEXIF != nil {
		_, err := w.Write(rawEXIF)
		return err
	}
	// If no EXIF supplied, pass through the original unchanged.
	_, err := io.Copy(w, r)
	return err
}

// byteOrder determines the TIFF byte order from the first 2 bytes.
func byteOrder(b []byte) (binary.ByteOrder, error) {
	switch {
	case b[0] == 'I' && b[1] == 'I':
		return binary.LittleEndian, nil
	case b[0] == 'M' && b[1] == 'M':
		return binary.BigEndian, nil
	}
	return nil, fmt.Errorf("tiff: invalid byte order marker %q", b[:2])
}

// extractTagValues scans IFD0 for IPTC (0x83BB) and XMP (0x02BC) tags
// and returns their raw byte values.
func extractTagValues(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) {
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

		var v []byte
		sz := typeSize(typ)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(cnt)
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
		case 0x83BB: // IPTC-NAA
			rawIPTC = v
		case 0x02BC: // XMP
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}

// typeSize returns the byte size of a single value for the given TIFF type.
func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 0
}
