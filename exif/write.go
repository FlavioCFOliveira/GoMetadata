package exif

import (
	"encoding/binary"
	"sort"
)

// serialise encodes e to a raw EXIF byte stream beginning with the TIFF
// header. The caller is responsible for prepending the "Exif\x00\x00"
// identifier required by JPEG APP1 (EXIF §4.5.4).
//
// Round-trip fidelity for IFD entries:
//   - Known-type entries (any TIFF type code with a defined byte size) whose
//     total value size is ≤ 4 bytes (inline) are perfectly preserved.
//   - Known-type entries whose total value size > 4 bytes (out-of-line) are
//     re-serialised into a fresh value area; their data is preserved exactly.
//   - Unknown-type entries (type codes not defined in TIFF 6.0 §2) are stored
//     during parsing as their raw 4-byte IFD field (see ifd.go traverse()).
//     On re-encode that 4-byte field is written back verbatim as an inline
//     value. If the original field was an offset into a private data blob, that
//     blob is NOT copied — the offset in the new file would be stale. This is
//     an inherent constraint: without knowing the type size we cannot locate or
//     copy the pointed-to data. Callers that embed private data using unknown
//     type codes must re-inject that data after calling Encode.
func serialise(e *EXIF) ([]byte, error) {
	if e == nil {
		return nil, ErrNilEXIF
	}

	order := e.ByteOrder

	// Stack-allocated arrays avoid one heap allocation per sub-IFD pointer.
	var exifPtrBuf, gpsPtrBuf, interopPtrBuf [4]byte

	ifd0Entries := buildIFD0Entries(e, order, &exifPtrBuf, &gpsPtrBuf)
	exifIFDEntries := buildExifIFDEntries(e, order, &interopPtrBuf)

	exifStart, gpsStart, interopStart, ifd1Start := computeIFDOffsets(e, ifd0Entries, exifIFDEntries)

	patchPointers(ifd0Entries, exifIFDEntries, order, exifStart, gpsStart, interopStart)

	out := writeTIFFHeader(e, order, ifd0Entries, exifIFDEntries)

	// IFD0: next-IFD pointer points to IFD1 if present.
	ifd0NextPtr := uint32(0)
	if e.IFD0 != nil && e.IFD0.Next != nil {
		ifd0NextPtr = ifd1Start
	}
	out = writeIFD(out, ifd0Entries, order, uint32(len(out)), ifd0NextPtr) //nolint:gosec // G115: output offset bounded by buffer size

	out = writeSubIFDs(out, e, exifIFDEntries, order)

	return out, nil
}

// writeTIFFHeader builds the initial output buffer containing the TIFF header
// (TIFF §2): byte order mark, magic 0x002A, and the IFD0 offset. It also
// pre-allocates capacity for the IFD0, ExifIFD, GPS IFD, and InteropIFD blocks.
func writeTIFFHeader(e *EXIF, order binary.ByteOrder, ifd0Entries, exifIFDEntries []IFDEntry) []byte {
	const headerSize = uint32(8)
	var hdr [8]byte
	if order == binary.LittleEndian {
		hdr[0], hdr[1] = 'I', 'I'
	} else {
		hdr[0], hdr[1] = 'M', 'M'
	}
	order.PutUint16(hdr[2:], 0x002A)
	order.PutUint32(hdr[4:], headerSize) // IFD0 starts right after the header

	ifd0Size := ifdTotalSize(ifd0Entries)
	exifSize := uint32(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSize(exifIFDEntries)
	}
	gpsSize := uint32(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSize(e.GPSIFD.Entries)
	}
	interopSize := uint32(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSize(e.InteropIFD.Entries)
	}

	out := make([]byte, 0, headerSize+ifd0Size+exifSize+gpsSize+interopSize)
	out = append(out, hdr[:]...)
	return out
}

// writeSubIFDs appends the ExifIFD, GPS IFD, InteropIFD, and IFD1 chain blocks
// to out in the order mandated by the TIFF layout (TIFF §2 / EXIF §4.5.4).
// Returns the extended slice.
func writeSubIFDs(out []byte, e *EXIF, exifIFDEntries []IFDEntry, order binary.ByteOrder) []byte {
	if e.ExifIFD != nil {
		out = writeIFD(out, exifIFDEntries, order, uint32(len(out)), 0) //nolint:gosec // G115: output offset bounded by buffer size
	}
	if e.GPSIFD != nil {
		out = writeIFD(out, e.GPSIFD.Entries, order, uint32(len(out)), 0) //nolint:gosec // G115: output offset bounded by buffer size
	}
	if e.InteropIFD != nil {
		out = writeIFD(out, e.InteropIFD.Entries, order, uint32(len(out)), 0) //nolint:gosec // G115: output offset bounded by buffer size
	}

	// Serialise the IFD1 chain (thumbnail IFDs, TIFF §2).
	for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
		nextPtr := uint32(0)
		if ifd.Next != nil {
			nextPtr = uint32(len(out)) + ifdTotalSize(ifd.Entries) //nolint:gosec // G115: output offset bounded by buffer size
		}
		out = writeIFD(out, ifd.Entries, order, uint32(len(out)), nextPtr) //nolint:gosec // G115: output offset bounded by buffer size
	}
	return out
}

// buildIFD0Entries assembles the IFD0 entry slice for encoding.
// It strips existing sub-IFD pointer tags, conditionally appends placeholder
// entries for ExifIFD and GPS IFD (using the caller-supplied stack buffers),
// and returns a sorted slice. The placeholder values are patched later by
// patchPointers once the target offsets are known.
func buildIFD0Entries(e *EXIF, order binary.ByteOrder, exifPtrBuf, gpsPtrBuf *[4]byte) []IFDEntry {
	entries := filterEntries(e.IFD0, 2,
		TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer)

	// Reserve pointer entries so ifdTotalSize accounts for them correctly.
	// TypeLong / Count 1 → value fits inline (4 bytes); no value-area impact.
	if e.ExifIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagExifIFDPointer, Type: TypeLong, Count: 1, Value: exifPtrBuf[:], byteOrder: order})
	}
	if e.GPSIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagGPSIFDPointer, Type: TypeLong, Count: 1, Value: gpsPtrBuf[:], byteOrder: order})
	}
	sortEntries(entries)
	return entries
}

// buildExifIFDEntries assembles the ExifIFD entry slice for encoding.
// Returns nil when e.ExifIFD is nil. It strips the existing InteropIFD pointer,
// re-adds a placeholder (using interopPtrBuf) when InteropIFD is present,
// preserves raw MakerNote bytes if they are not already present in the entries,
// and returns a sorted slice. Placeholder values are patched later by patchPointers.
//
// MakerNote bytes are preserved verbatim rather than re-serialising MakerNoteIFD
// because MakerNote offsets are often relative to the parent TIFF start, making
// them non-portable when moved. EXIF §4.6.3 / MakerNote interoperability notes.
func buildExifIFDEntries(e *EXIF, order binary.ByteOrder, interopPtrBuf *[4]byte) []IFDEntry {
	if e.ExifIFD == nil {
		return nil
	}

	// Strip existing InteropIFD pointer; we will re-add it with a freshly
	// computed offset when InteropIFD is present (EXIF §4.6.3, tag 0xA005
	// lives in ExifIFD, not IFD0).
	entries := filterEntries(e.ExifIFD, 2, TagInteropIFDPointer)
	if e.InteropIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagInteropIFDPointer, Type: TypeLong, Count: 1, Value: interopPtrBuf[:], byteOrder: order})
	}
	if e.MakerNote != nil && !hasEntry(entries, TagMakerNote) {
		entries = append(entries, IFDEntry{
			Tag:       TagMakerNote,
			Type:      TypeUndefined,
			Count:     uint32(len(e.MakerNote)), //nolint:gosec // G115: MakerNote length bounded by input
			Value:     e.MakerNote,
			byteOrder: order,
		})
	}
	sortEntries(entries)
	return entries
}

// computeIFDOffsets derives the byte offset at which each sub-IFD block begins
// within the final encoded output.
//
// Layout (TIFF §2 / EXIF §4.5.4):
//
//	[8-byte TIFF header][IFD0 block][ExifIFD block][GPS IFD block][InteropIFD block][IFD1 chain…]
//
// Returns exifStart, gpsStart, interopStart, ifd1Start (all absolute offsets
// from the beginning of the TIFF data, i.e. from byte 0 of the encoded output).
func computeIFDOffsets(e *EXIF, ifd0Entries, exifIFDEntries []IFDEntry) (exifStart, gpsStart, interopStart, ifd1Start uint32) {
	const headerSize = uint32(8)

	ifd0Size := ifdTotalSize(ifd0Entries)
	exifStart = headerSize + ifd0Size

	exifSize := uint32(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSize(exifIFDEntries)
	}
	gpsStart = exifStart + exifSize

	gpsSize := uint32(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSize(e.GPSIFD.Entries)
	}
	interopStart = gpsStart + gpsSize

	interopSize := uint32(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSize(e.InteropIFD.Entries)
	}
	ifd1Start = interopStart + interopSize

	return exifStart, gpsStart, interopStart, ifd1Start
}

// patchPointers writes the now-known target offsets into the placeholder
// IFDEntry.Value slices that were reserved by buildIFD0Entries and
// buildExifIFDEntries. Because Value slices point into the stack-allocated
// [4]byte arrays passed to the build helpers, this is a direct in-place
// write with no allocation.
//
// Entries are sorted by tag (invariant maintained by buildIFD0Entries and
// buildExifIFDEntries), so binary search locates each pointer in O(log n).
func patchPointers(ifd0Entries, exifIFDEntries []IFDEntry, order binary.ByteOrder, exifStart, gpsStart, interopStart uint32) {
	patchEntry := func(entries []IFDEntry, tag TagID, val uint32) {
		i := sort.Search(len(entries), func(i int) bool { return entries[i].Tag >= tag })
		if i < len(entries) && entries[i].Tag == tag {
			order.PutUint32(entries[i].Value, val)
		}
	}
	patchEntry(ifd0Entries, TagExifIFDPointer, exifStart)
	patchEntry(ifd0Entries, TagGPSIFDPointer, gpsStart)
	patchEntry(exifIFDEntries, TagInteropIFDPointer, interopStart)
}
