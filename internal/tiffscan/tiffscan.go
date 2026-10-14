// Package tiffscan provides the classic-TIFF IFD0 tag scanner shared by
// format/tiff, format/raw/orf, and format/raw/rw2. It extracts the raw
// IPTC-NAA (0x83BB, or the 0x0404 resource inside the Photoshop IRB of tag
// 0x8649) and XMP (0x02BC) payloads from IFD0 without a full exif.Parse.
//
// IPTC source precedence, derived from TIFF_MetaHandler::ProcessXMP: the
// native TIFF tag 0x83BB is the master copy and wins when it is present and
// non-empty; the Photoshop IRB copy (tag 0x8649, resource 0x0404) is the
// fallback. The Toolkit reads only 0x83BB; reading the IRB copy when 0x83BB
// is absent is an extension, because Photoshop-written TIFFs may carry IPTC
// only there (task #301). The IPTC digest (resource 0x0425 of the same IRB)
// is exposed through IPTCDigest.
package tiffscan

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FileHandlers/TIFF_Handler.cpp:TIFF_MetaHandler::ProcessXMP, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause

import (
	"encoding/binary"

	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
)

// Tag numbers read by this package.
const (
	tagXMP  = 0x02BC // XMP packet (Adobe XMP Part 3, TIFF)
	tagIPTC = 0x83BB // IPTC-NAA record (IIM stream)
	tagPSIR = 0x8649 // Photoshop Image Resource Block (8BIM stream)
)

// ExtractTagValues scans the classic-TIFF IFD0 entry table beginning at
// ifd0Off within data for tags 0x83BB (IPTC-NAA) and 0x02BC (XMP) and returns
// their raw value bytes; when tag 0x83BB is absent or empty, the IPTC comes
// from resource 0x0404 of the Photoshop IRB in tag 0x8649 (WithPSIRFallback,
// task #301). The returned slices alias data.
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
	var psir []byte

	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		if tag != tagIPTC && tag != tagXMP && tag != tagPSIR {
			continue // resolve values only for the three tags read here
		}
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
		case tagPSIR:
			psir = v
		case tagIPTC: // IPTC-NAA
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
		case tagXMP:
			rawXMP = v
		}
	}
	return WithPSIRFallback(rawIPTC, psir), rawXMP
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

// WithPSIRFallback applies the IPTC source precedence: iptc, the value of the
// native tag 0x83BB, wins when non-empty; otherwise the 0x0404 resource of
// psir, the value of tag 0x8649, is returned (nil when absent or empty).
// The result aliases its inputs.
func WithPSIRFallback(iptc, psir []byte) []byte {
	if len(iptc) > 0 || len(psir) == 0 {
		return iptc // fast path, inlined into the scanners
	}
	return psirIPTC(iptc, psir)
}

// psirIPTC is the slow path of WithPSIRFallback, kept out of line so the
// common case (no tag 0x8649, or tag 0x83BB present) costs one comparison.
//
//go:noinline
func psirIPTC(iptc, psir []byte) []byte {
	if fallback := irb.FindIPTC(psir); len(fallback) > 0 {
		return fallback
	}
	return iptc
}

// PSIRTag is the TIFF tag number of the Photoshop Image Resource Block.
const PSIRTag = tagPSIR

// IPTCDigest returns the 16-byte IPTC digest (Photoshop resource 0x0425, MWG
// Guidelines v2.0 §3.3.1) stored in the Photoshop IRB (tag 0x8649) of IFD0 of
// the classic TIFF or BigTIFF stream data, or nil when the stream carries no
// such resource. The result aliases data. data is a TIFF stream or its
// metadata prefix (format/tiff Extract's rawEXIF); the magic number is not
// checked beyond telling classic from BigTIFF (43), so TIFF-based RAW files
// with their own magic are accepted.
func IPTCDigest(data []byte) []byte {
	if len(data) < 8 {
		return nil
	}
	var order binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		return nil
	}
	var psir []byte
	if order.Uint16(data[2:]) == 43 { // BigTIFF
		psir = psirBigTIFF(data, order)
	} else {
		psir = psirClassic(data, order)
	}
	if len(psir) == 0 {
		return nil
	}
	_, digest := irb.IPTCAndDigest(psir)
	return digest
}

// psirClassic returns the value of tag 0x8649 in the classic IFD0 of data.
func psirClassic(data []byte, order binary.ByteOrder) []byte {
	ifd0 := uint64(order.Uint32(data[4:]))
	if ifd0+2 > uint64(len(data)) {
		return nil
	}
	count := uint64(order.Uint16(data[ifd0:]))
	for i := range count {
		e := ifd0 + 2 + i*12
		if e+12 > uint64(len(data)) {
			return nil
		}
		if order.Uint16(data[e:]) != tagPSIR {
			continue
		}
		sz := TypeSize(order.Uint16(data[e+2:]), true)
		total := uint64(sz) * uint64(order.Uint32(data[e+4:]))
		switch {
		case sz == 0:
			return nil
		case total <= 4:
			return data[e+8 : e+8+total]
		}
		off := uint64(order.Uint32(data[e+8:]))
		if off > uint64(len(data)) || total > uint64(len(data))-off {
			return nil
		}
		return data[off : off+total]
	}
	return nil
}

// bigTIFFTypeSize is the element size of the BigTIFF field types (BigTIFF
// spec §2), 0 for an unknown type.
func bigTIFFTypeSize(t uint16) uint64 {
	switch t {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11, 13:
		return 4
	case 5, 10, 12, 16, 17, 18:
		return 8
	}
	return 0
}

// psirBigTIFF returns the value of tag 0x8649 in the BigTIFF IFD0 of data.
func psirBigTIFF(data []byte, order binary.ByteOrder) []byte {
	if len(data) < 16 {
		return nil
	}
	ifd0 := order.Uint64(data[8:])
	if ifd0 > uint64(len(data)) || uint64(len(data))-ifd0 < 8 {
		return nil
	}
	// BigTIFF spec §2: 8-byte entry count, then 20-byte entries. Entries that
	// do not fit in data are not visited.
	count := min(order.Uint64(data[ifd0:]), (uint64(len(data))-ifd0-8)/20)
	for i := range count {
		e := ifd0 + 8 + i*20
		if order.Uint16(data[e:]) == tagPSIR {
			return bigTIFFValue(data, e, order)
		}
	}
	return nil
}

// bigTIFFValue returns the value of the BigTIFF entry at e (tag 2, type 2,
// count 8, value-or-offset 8; values of up to 8 bytes are inline), or nil
// when its type is unknown, its size overflows, or it lies outside data.
func bigTIFFValue(data []byte, e uint64, order binary.ByteOrder) []byte {
	sz := bigTIFFTypeSize(order.Uint16(data[e+2:]))
	cnt := order.Uint64(data[e+4:])
	if sz == 0 || cnt > ^uint64(0)/sz {
		return nil
	}
	total := sz * cnt
	if total <= 8 {
		return data[e+12 : e+12+total]
	}
	off := order.Uint64(data[e+12:])
	if off > uint64(len(data)) || total > uint64(len(data))-off {
		return nil
	}
	return data[off : off+total]
}
