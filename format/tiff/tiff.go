// Package tiff implements extraction and injection of metadata within TIFF
// container files. TIFF stores EXIF in a SubIFD (tag 0x8769), IPTC in tag
// 0x83BB, and XMP in tag 0x02BC (TIFF Technical Note 3).
package tiff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
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
		return nil, nil, nil, errors.New("tiff: file too short")
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
// When rawIPTC or rawXMP is non-nil, the TIFF is parsed and IFD0 is updated
// with the new values before re-encoding (affects CR2, NEF, ARW, DNG via delegation).
//
// Round-trip fidelity: all IFD entries whose TIFF type code is defined in
// TIFF 6.0 §2 are faithfully preserved, including private tags with known
// types. Entries using undefined/proprietary type codes retain their 4-byte
// IFD field but any out-of-line data they referenced is not copied (see
// exif.Encode documentation). If exif.Parse fails (e.g. because the caller
// passed a non-standard TIFF variant that cannot be decoded), Inject returns
// the parse error rather than silently discarding the requested metadata.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tiff: seek: %w", err)
	}

	// Determine the base TIFF data to work with.
	var base []byte
	if rawEXIF != nil {
		base = rawEXIF
	} else {
		var err error
		base, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("tiff: read: %w", err)
		}
	}

	// If no IPTC or XMP updates, write the base bytes directly.
	if rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(base); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := buildUpdatedTIFF(base, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// buildUpdatedTIFF parses base as a TIFF stream, upserts IPTC and XMP entries
// in IFD0 for any non-nil payload, and re-encodes the result.
// If parsing fails we cannot safely inject metadata; the error is returned so
// the caller knows the update was not applied rather than silently losing it.
func buildUpdatedTIFF(base []byte, rawIPTC, rawXMP []byte) ([]byte, error) {
	e, err := exif.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("tiff: parse for metadata injection: %w", err)
	}

	if rawIPTC != nil {
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeUndefined, rawIPTC)
	}
	if rawXMP != nil {
		upsertIFD0Entry(e.IFD0, exif.TagXMP, exif.TypeUndefined, rawXMP)
	}

	updated, err := exif.Encode(e)
	if err != nil {
		return nil, fmt.Errorf("tiff: encode: %w", err)
	}
	return updated, nil
}

// upsertIFD0Entry adds or replaces an entry in ifd for the given tag.
func upsertIFD0Entry(ifd *exif.IFD, tag exif.TagID, typ exif.DataType, value []byte) {
	entry := exif.IFDEntry{
		Tag:   tag,
		Type:  typ,
		Count: uint32(len(value)), //nolint:gosec // G115: IFD value length bounded by input
		Value: value,
	}
	for i, e := range ifd.Entries {
		if e.Tag == tag {
			ifd.Entries[i] = entry
			return
		}
	}
	ifd.Entries = append(ifd.Entries, entry)
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
			// Guard against integer overflow: check before computing end.
			if uint64(off) > uint64(len(data)) || total > uint64(len(data))-uint64(off) {
				continue
			}
			v = data[uint64(off) : uint64(off)+total]
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
