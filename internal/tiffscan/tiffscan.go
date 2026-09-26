// Package tiffscan provides the classic-TIFF IFD0 tag scanner shared by
// format/tiff, format/raw/orf, and format/raw/rw2. It extracts the raw
// IPTC-NAA (0x83BB) and XMP (0x02BC) payloads from IFD0 without a full
// exif.Parse.
package tiffscan

import "encoding/binary"

// ExtractTagValues scans the classic-TIFF IFD0 entry table beginning at
// ifd0Off within data for tags 0x83BB (IPTC-NAA) and 0x02BC (XMP) and returns
// their raw value bytes. The returned slices alias data.
//
// data is the full container buffer; bytes [0:4] (byte order and magic) are
// never read, so a TIFF-based RAW file (ORF, RW2) may be passed with its
// original, non-standard magic. ifd0Off is the absolute offset of IFD0.
//
// type13IsIFD selects the element size for type code 13. When true, type 13
// is the TIFF Extensions IFD type (4 bytes per element); when false, entries
// declaring type 13 are skipped as unrecognised. format/tiff passes true to
// match its own type table; format/raw/orf and format/raw/rw2 pass false.
//
// TIFF 6.0 §2: classic IFD entries are 12 bytes (tag, type, count,
// value-or-offset); a value is stored inline when its total byte size is
// ≤ 4 bytes, otherwise value-or-offset holds an absolute offset to the value.
func ExtractTagValues(data []byte, ifd0Off uint32, order binary.ByteOrder, type13IsIFD bool) (rawIPTC, rawXMP []byte) { //nolint:gocyclo // IPTC trimming branch is inherent to TypeLong-vs-TypeUndefined handling; extracting a helper would reduce clarity
	// CWE-681/190: compare in uint64 before converting ifd0Off to int. On a
	// 32-bit platform int(ifd0Off) for ifd0Off >= 2^31 is negative and would
	// pass an int-typed bound check, then panic on data[ifd0Off:].
	if uint64(ifd0Off)+2 > uint64(len(data)) {
		return nil, nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
	// ifd0Off ≤ len(data)-2 and len(data) is a valid int, so the conversion
	// below cannot overflow.
	pos := int(ifd0Off) + 2

	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])

		sz := TypeSize(typ, type13IsIFD)
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
		case 0x83BB: // IPTC-NAA
			// ROBUST-16 (iptc.md §5): strip trailing 0x00 bytes ONLY for
			// TypeLong (typ == 4), whose value is padded to a 4-byte boundary
			// (TIFF 6.0 §2: Count = number of uint32 elements). For TypeByte (1)
			// and TypeUndefined (7) the payload is returned unchanged: a valid
			// IPTC payload may legitimately end in 0x00.
			if len(v) > 0 {
				if typ == 4 {
					rawIPTC = TrimIPTCLongPadding(v)
				} else {
					rawIPTC = v
				}
				if len(rawIPTC) == 0 {
					rawIPTC = nil
				}
			}
		case 0x02BC: // XMP
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}

// TrimIPTCLongPadding trims trailing 0x00 alignment-padding bytes from an
// IPTC payload stored as TypeLong. TypeLong pads the value to the next 4-byte
// boundary; those trailing zeros are never valid IIM dataset prefixes (0x1C).
// Callers must use it only for TypeLong payloads; TypeByte and TypeUndefined
// payloads are never trimmed (ROBUST-16). The result aliases v.
func TrimIPTCLongPadding(v []byte) []byte {
	end := len(v)
	for end > 0 && v[end-1] == 0x00 {
		end--
	}
	return v[:end]
}

// TypeSize returns the byte size of a single value for the given classic TIFF
// type code, or 0 for an unrecognised type.
// Type 13 is recognised as a 4-byte IFD pointer only when type13IsIFD is true
// (TIFF 6.0 Extensions / libtiff TIFF_IFD).
func TypeSize(t uint16, type13IsIFD bool) uint32 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	case 13: // IFD (TIFF 6.0 Extensions)
		if type13IsIFD {
			return 4
		}
	}
	return 0
}
