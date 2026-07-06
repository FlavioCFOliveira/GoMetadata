package tiff

// relocate_bigtiff.go — container-width-aware raw IFD scanning primitives
// (task #270, standalone BigTIFF container write).
//
// relocate.go's copy-and-relocate serializer (relocateTIFFFromParsed) and its
// helpers (patchSubIFDPointers, extractRawIFD, patchRawIFDOffsets,
// enumerateSubIFDs/enumerateSubIFDsAt) scan raw TIFF bytes directly — they
// cannot rely solely on the *exif.EXIF struct because (a) they need to patch
// value-or-offset fields in place after exif.Encode has already produced the
// output bytes, and (b) tag 0x014A (SubIFDs) can legitimately use a type code
// (13, "IFD") that collides with exif/type.go's EXIF-3.0 TypeUTF8 assignment
// for the SAME numeric code — see typeSize's doc comment in tiff.go.
//
// Classic TIFF and BigTIFF differ structurally in exactly four ways relevant
// to this raw scanning (BigTIFF spec §2, Aware Systems / libtiff; TIFF 6.0 §2):
//
//	                 classic (0x002A)   BigTIFF (0x002B)
//	header length     8 bytes            16 bytes
//	IFD0 offset field  @[4:8],  uint32    @[8:16], uint64
//	IFD entry count    uint16             uint64
//	IFD entry width    12 bytes           20 bytes
//	value-or-offset    4 bytes  @+8       8 bytes  @+12
//	inline threshold   ≤4 bytes           ≤8 bytes
//
// The functions below parameterise every one of these widths on a single
// bigTIFF bool, so relocate.go's existing classic-only logic can be extended
// to BigTIFF without duplicating the surrounding algorithms.
//
// IMPORTANT SCOPE NOTE: a MakerNote blob's OWN internal IFD (Sony plain-IFD,
// Olympus OLYMP-type — see relocate_makernote.go) is NEVER BigTIFF-shaped,
// regardless of the outer container: manufacturer MakerNote encodings predate
// BigTIFF and are defined independently of it (ExifTool Sony.pm, Olympus.pm).
// Only the OUTER container's own IFDs (IFD0, ExifIFD, SubIFDs) switch shape
// with the container's magic number.

import (
	"encoding/binary"
	"math"
	"sort"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Structural width constants for classic TIFF and BigTIFF IFDs. See the
// package doc comment above for the spec citations.
const (
	classicHeaderLen       = 8
	classicIFD0OffsetPos   = 4
	classicIFD0OffsetWidth = 4
	classicEntryWidth      = 12
	classicCountWidth      = 2
	classicValFieldWidth   = 4
	classicInlineThreshold = 4

	bigTIFFIFD0OffsetPos   = 8
	bigTIFFIFD0OffsetWidth = 8
	bigTIFFEntryWidth      = 20
	bigTIFFCountWidth      = 8
	bigTIFFValFieldWidth   = 8
	bigTIFFInlineThreshold = 8
)

// ifdWidths returns the structural byte widths for a TIFF IFD, selected
// according to whether the container is classic TIFF or BigTIFF.
func ifdWidths(bigTIFF bool) (countWidth, entryWidth, valFieldWidth int) {
	if bigTIFF {
		return bigTIFFCountWidth, bigTIFFEntryWidth, bigTIFFValFieldWidth
	}
	return classicCountWidth, classicEntryWidth, classicValFieldWidth
}

// inlineThreshold returns the maximum total value byte size (elemSz*count)
// that is stored inline in the value-or-offset field, rather than out-of-line
// at an absolute file offset.
//
// TIFF 6.0 §2: classic threshold is 4 bytes. BigTIFF spec §2: threshold is 8
// bytes (matches the wider value-or-offset field).
func inlineThreshold(bigTIFF bool) uint64 {
	if bigTIFF {
		return bigTIFFInlineThreshold
	}
	return classicInlineThreshold
}

// elemSizeFor returns the byte size of one element of TIFF type code t, using
// format/tiff's own local type table (typeSize/typeSizeBigTIFF in tiff.go —
// see typeSize's doc comment for why this table deliberately diverges from
// exif/type.go for type code 13). Returns 0 for unknown types.
func elemSizeFor(t uint16, bigTIFF bool) uint64 {
	if bigTIFF {
		return typeSizeBigTIFF(t)
	}
	return uint64(typeSize(t))
}

// readIFD0Offset reads the absolute offset of IFD0 from the TIFF/BigTIFF
// header located at the start of buf. Returns false if buf is too short.
//
// TIFF 6.0 §2: classic header bytes [4:8] hold a uint32 IFD0 offset.
// BigTIFF spec §2: BigTIFF header bytes [8:16] hold a uint64 IFD0 offset.
func readIFD0Offset(buf []byte, bigTIFF bool, order binary.ByteOrder) (uint64, bool) {
	pos, width := classicIFD0OffsetPos, classicIFD0OffsetWidth
	if bigTIFF {
		pos, width = bigTIFFIFD0OffsetPos, bigTIFFIFD0OffsetWidth
	}
	if pos+width > len(buf) {
		return 0, false
	}
	if bigTIFF {
		return order.Uint64(buf[pos:]), true
	}
	return uint64(order.Uint32(buf[pos:])), true
}

// rawIFDEntry describes one IFD entry read directly from a byte buffer,
// independent of container width. valField aliases buf (zero-copy) and is
// exactly 4 bytes (classic) or 8 bytes (BigTIFF) wide.
type rawIFDEntry struct {
	pos      uint64 // absolute byte position of the entry within buf
	tag      uint16
	typ      uint16
	count    uint64
	valField []byte
}

// ifdEntryTable reads the entry count of the IFD located at ifdOff within buf
// and returns the byte position at which the first entry begins.
//
// TIFF 6.0 §2: classic count field is uint16. BigTIFF spec §2: BigTIFF count
// field is uint64.
func ifdEntryTable(buf []byte, ifdOff uint64, bigTIFF bool, order binary.ByteOrder) (count, entriesStart uint64, ok bool) {
	countWidth, _, _ := ifdWidths(bigTIFF)
	countWidth64 := uint64(countWidth) //nolint:gosec // G115: countWidth is the compile-time constant 2 or 8 from ifdWidths, never negative
	if ifdOff+countWidth64 > uint64(len(buf)) {
		return 0, 0, false
	}
	if bigTIFF {
		count = order.Uint64(buf[ifdOff:])
	} else {
		count = uint64(order.Uint16(buf[ifdOff:]))
	}
	return count, ifdOff + countWidth64, true
}

// readRawEntryAt reads one IFD entry at absolute byte position pos within buf.
func readRawEntryAt(buf []byte, pos uint64, bigTIFF bool, order binary.ByteOrder) (rawIFDEntry, bool) {
	_, entryWidth, valFieldWidth := ifdWidths(bigTIFF)
	entryWidth64 := uint64(entryWidth) //nolint:gosec // G115: entryWidth is the compile-time constant 12 or 20 from ifdWidths, never negative
	if pos+entryWidth64 > uint64(len(buf)) {
		return rawIFDEntry{}, false
	}
	e := buf[pos:]
	tag := order.Uint16(e)
	typ := order.Uint16(e[2:])
	var count uint64
	var valPos int
	if bigTIFF {
		count = order.Uint64(e[4:])
		valPos = 12
	} else {
		count = uint64(order.Uint32(e[4:]))
		valPos = 8
	}
	return rawIFDEntry{
		pos:      pos,
		tag:      tag,
		typ:      typ,
		count:    count,
		valField: e[valPos : valPos+valFieldWidth],
	}, true
}

// findEntryInIFD scans the IFD at ifdOff within buf for the first entry with
// the given tag. Returns the entry and true when found. Iteration is capped
// at bigTIFFMaxIFDEntries (also the classic uint16 ceiling) to bound the scan
// on a crafted/corrupt entry count, mirroring the same defensive cap used by
// the read-path BigTIFF parser (tiff.go extractTagValuesBigTIFF).
func findEntryInIFD(buf []byte, ifdOff uint64, tag uint16, bigTIFF bool, order binary.ByteOrder) (rawIFDEntry, bool) {
	count, pos, ok := ifdEntryTable(buf, ifdOff, bigTIFF, order)
	if !ok {
		return rawIFDEntry{}, false
	}
	_, entryWidth, _ := ifdWidths(bigTIFF)
	entryWidth64 := uint64(entryWidth) //nolint:gosec // G115: entryWidth is the compile-time constant 12 or 20 from ifdWidths, never negative
	if count > bigTIFFMaxIFDEntries {
		count = bigTIFFMaxIFDEntries
	}
	for i := range count {
		entry, ok := readRawEntryAt(buf, pos+i*entryWidth64, bigTIFF, order)
		if !ok {
			break
		}
		if entry.tag == tag {
			return entry, true
		}
	}
	return rawIFDEntry{}, false
}

// fieldAsU64 interprets a raw value-or-offset field (4 or 8 bytes, per
// bigTIFF) as a single unsigned integer — either the inline value of a
// scalar entry or the absolute file offset of an out-of-line value area.
func fieldAsU64(valField []byte, bigTIFF bool, order binary.ByteOrder) uint64 {
	if bigTIFF {
		return order.Uint64(valField)
	}
	return uint64(order.Uint32(valField))
}

// putFieldU64 writes v into a value-or-offset field, using the container's
// native pointer width.
//
// v is always bounded by maxFileSize (256 MiB) for any value this package
// itself computes, so the classic uint32 narrowing below never truncates in
// practice; it exists only because classic TIFF's wire format is 4 bytes wide.
func putFieldU64(valField []byte, bigTIFF bool, order binary.ByteOrder, v uint64) {
	if bigTIFF {
		order.PutUint64(valField, v)
		return
	}
	order.PutUint32(valField, uint32(v)) //nolint:gosec // G115: classic TIFF path; v bounded by maxFileSize, always < 2^32
}

// decodeOffsetArray reads count elements of elemSz bytes each from an entry
// described by valField, handling both the inline (values packed
// left-justified in valField) and out-of-line (valField holds an absolute
// pointer to the array in buf) cases.
//
// TIFF 6.0 §2 / BigTIFF spec §2: a value is stored inline when its total byte
// size (elemSz*count) fits within the value-or-offset field; otherwise the
// field holds an absolute file offset to the array. Values smaller than the
// field width are left-justified within it (starting at the lowest-address
// byte), independent of file byte order — the same convention exif.Encode
// already uses for BigTIFF writes (see exif/write.go serialiseBigTIFF).
func decodeOffsetArray(buf, valField []byte, count, elemSz uint64, bigTIFF bool, order binary.ByteOrder) ([]uint64, bool) { //nolint:gocyclo // inline/OOL branching plus a 4-way element-width switch; splitting further would obscure the single decode algorithm
	if count == 0 || elemSz == 0 {
		return nil, false
	}
	total := count * elemSz
	var src []byte
	if total <= inlineThreshold(bigTIFF) {
		src = valField
	} else {
		off := fieldAsU64(valField, bigTIFF, order)
		if off+total > uint64(len(buf)) {
			return nil, false
		}
		src = buf[off : off+total]
	}
	out := make([]uint64, count)
	for i := range count {
		p := i * elemSz
		if p+elemSz > uint64(len(src)) {
			return nil, false
		}
		switch elemSz {
		case 1:
			out[i] = uint64(src[p])
		case 2:
			out[i] = uint64(order.Uint16(src[p:]))
		case 4:
			out[i] = uint64(order.Uint32(src[p:]))
		case 8:
			out[i] = order.Uint64(src[p:])
		default:
			return nil, false
		}
	}
	return out, true
}

// parseIFDAtBigTIFF parses a single BigTIFF-shaped IFD at absolute offset off
// within base into a *exif.IFD, without going through exif.ParseIFDAt (which
// is classic-TIFF-only: uint16 count, 12-byte entries, 4-byte fields).
//
// A SubIFD (tag 0x014A) nested inside a BigTIFF file inherits the file-wide
// 20-byte-entry/8-byte-field format: BigTIFF is a whole-file container
// decision (a single magic number for the entire stream), not a per-IFD one —
// BigTIFF spec §2 (Aware Systems / libtiff).
//
// Deliberately minimal: builds only exif.IFDEntry{Tag,Type,Count,Value}. The
// returned IFD's Next/ThumbnailData are left unset — enumerateSubIFDsAt
// clears ThumbnailData unconditionally immediately after calling this
// function and never follows a SubIFD's Next pointer, so both fields would be
// discarded anyway.
//
// Entries whose declared type is unknown to elemSizeFor, or whose value area
// falls outside base, are skipped leniently — the same policy exif.Parse
// itself uses for unrecognised entries (see the "unknown type" comments
// throughout exif/ifd.go).
func parseIFDAtBigTIFF(base []byte, off uint64, order binary.ByteOrder) (*exif.IFD, bool) {
	count, entriesStart, ok := ifdEntryTable(base, off, true, order)
	if !ok {
		return nil, false
	}
	if count > bigTIFFMaxIFDEntries {
		count = bigTIFFMaxIFDEntries
	}
	entries := make([]exif.IFDEntry, 0, count)
	for i := range count {
		entry, ok := readRawEntryAt(base, entriesStart+i*bigTIFFEntryWidth, true, order)
		if !ok {
			break
		}
		elemSz := elemSizeFor(entry.typ, true)
		if elemSz == 0 {
			continue // unknown type: skip leniently (mirrors exif.Parse)
		}
		total := elemSz * entry.count
		var value []byte
		if total <= bigTIFFInlineThreshold {
			value = append([]byte(nil), entry.valField[:total]...)
		} else {
			valOff := fieldAsU64(entry.valField, true, order)
			if valOff+total > uint64(len(base)) {
				continue // OOL value out of bounds: skip leniently
			}
			value = append([]byte(nil), base[valOff:valOff+total]...)
		}
		entryCount := min(entry.count, math.MaxUint32) // defensive clamp; exif.IFDEntry.Count is uint32
		entries = append(entries, exif.IFDEntry{
			Tag:   exif.TagID(entry.tag),
			Type:  exif.DataType(entry.typ),
			Count: uint32(entryCount), //nolint:gosec // G115: clamped to math.MaxUint32 above
			Value: value,
		})
	}
	// TIFF 6.0 §2 mandates ascending tag order for conformant files; sort
	// defensively so exif.IFD.Get's binary search is correct even for a
	// malformed/adversarial SubIFD.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Tag < entries[j].Tag })
	return &exif.IFD{Entries: entries}, true
}
