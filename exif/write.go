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
	// Task #59: e.ByteOrder is a nil interface value when the caller constructs
	// an EXIF struct without setting ByteOrder (zero value for interface). Every
	// internal setter uses ifd0ByteOrder() which defaults to LittleEndian, so
	// a freshly-built EXIF is always LE unless the caller explicitly chose BE.
	// Default here to match that convention so Encode never panics on a nil
	// interface dereference (first triggered at order.PutUint32 in patchPointers).
	if order == nil {
		order = binary.LittleEndian
	}

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
// pre-allocates capacity for the IFD0, ExifIFD, GPS IFD, InteropIFD, and IFD1
// chain blocks (including any JPEG thumbnail data).
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

	// Include the IFD1 chain (thumbnail IFDs + embedded JPEG data) in the
	// capacity estimate to avoid reallocation when appending thumbnail bytes.
	ifd1ChainSize := uint32(0)
	if e.IFD0 != nil {
		for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
			ifd1ChainSize += ifdTotalSize(ifd.Entries)
			ifd1ChainSize += uint32(len(ifd.ThumbnailData)) //nolint:gosec // G115: thumbnail size bounded by parse-time validation
		}
	}

	out := make([]byte, 0, headerSize+ifd0Size+exifSize+gpsSize+interopSize+ifd1ChainSize)
	out = append(out, hdr[:]...)
	return out
}

// writeSubIFDs appends the ExifIFD, GPS IFD, InteropIFD, and IFD1 chain blocks
// to out in the order mandated by the TIFF layout (TIFF §2 / EXIF §4.5.4).
// Returns the extended slice.
//
// For IFDs in the IFD1 chain that carry a JPEG thumbnail (ThumbnailData != nil),
// the thumbnail bytes are appended immediately after the IFD block and the
// JPEGInterchangeFormat (0x0201) entry value is patched to the new offset.
// This fixes stale offset corruption that occurs when the TIFF is re-serialised
// to a different position in the byte stream (EXIF §4.5.5, TIFF §2).
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
	// Task #58: guard matches the identical nil-check in writeTIFFHeader (line 88).
	// e.IFD0 is nil when the caller sets only ExifIFD/GPSIFD without IFD0; the
	// IFD1 chain is unreachable in that case so skipping it is correct.
	if e.IFD0 == nil {
		return out
	}
	for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
		// For IFDs with JPEG thumbnail data: the thumbnail bytes are placed
		// immediately after the IFD block.  Compute the thumbnail offset now,
		// patch the entries, then append the data.
		//
		// EXIF §4.5.5: JPEGInterchangeFormat (0x0201, TypeLong, Count=1) holds
		// the absolute byte offset within the TIFF stream to the JPEG thumbnail.
		// JPEGInterchangeFormatLength (0x0202, TypeLong, Count=1) holds its byte
		// length.  Both are inline (4-byte) values in the IFD entry (TIFF §2:
		// value stored inline when total size ≤ 4 bytes).
		entries := ifd.Entries
		if ifd.ThumbnailData != nil {
			entries = patchThumbnailEntries(ifd.Entries, order,
				uint32(len(out))+ifdTotalSize(ifd.Entries), //nolint:gosec // G115: output offset bounded by buffer size
				uint32(len(ifd.ThumbnailData)),             //nolint:gosec // G115: thumbnail length bounded by parse-time validation
			)
		}

		nextPtr := uint32(0)
		if ifd.Next != nil {
			// nextPtr must skip past both the IFD block and any thumbnail data
			// that follows it before the next IFD begins.
			nextPtr = uint32(len(out)) + ifdTotalSize(entries) + uint32(len(ifd.ThumbnailData)) //nolint:gosec // G115: output offset bounded by buffer size
		}
		out = writeIFD(out, entries, order, uint32(len(out)), nextPtr) //nolint:gosec // G115: output offset bounded by buffer size

		// Append thumbnail bytes after the IFD block.
		if ifd.ThumbnailData != nil {
			out = append(out, ifd.ThumbnailData...)
		}
	}
	return out
}

// patchThumbnailEntries returns a shallow copy of entries with
// TagJPEGInterchangeFormat (0x0201) and TagJPEGInterchangeFormatLength (0x0202)
// patched to the provided newOffset and newLength values.
//
// Both tags are TypeLong / Count=1 (total size = 4 bytes, stored inline in the
// IFD entry field per TIFF §2).  The [4]byte backing arrays are stack-allocated
// in the caller's frame; the returned slice aliases them so no heap allocation
// is needed.
//
// EXIF §4.5.5: JPEGInterchangeFormat is the absolute TIFF-stream offset to the
// JPEG thumbnail; JPEGInterchangeFormatLength is its byte length.
func patchThumbnailEntries(entries []IFDEntry, order binary.ByteOrder, newOffset, newLength uint32) []IFDEntry {
	// Shallow-copy the slice so we don't mutate the IFD's original entries.
	patched := make([]IFDEntry, len(entries))
	copy(patched, entries)

	for i := range patched {
		switch patched[i].Tag {
		case TagJPEGInterchangeFormat:
			// Replace the stale offset with the new one.  Allocate a fresh
			// [4]byte value so the original IFDEntry.Value slice is not modified.
			v := make([]byte, 4)
			order.PutUint32(v, newOffset)
			patched[i].Value = v
		case TagJPEGInterchangeFormatLength:
			// Update length in case ThumbnailData was trimmed or replaced.
			v := make([]byte, 4)
			order.PutUint32(v, newLength)
			patched[i].Value = v
		}
	}
	return patched
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
// MakerNote bytes are preserved verbatim rather than re-serialising MakerNoteIFD.
// This is safe for manufacturers whose MakerNote offsets are relative to the
// MakerNote blob itself (Canon, Sony, Panasonic, Olympus, Pentax AOC …) because
// the blob is self-contained and can be moved without invalidating its internal
// references.  It is NOT safe for manufacturers whose offsets are relative to the
// parent TIFF start (e.g. Nikon bodies that embed an independent TIFF header inside
// the MakerNote): if the MakerNote block moves to a different TIFF offset on
// re-encode, those TIFF-relative offset values become stale.  Full offset rebasing
// is deferred to a future epic; EXIF.MakerNoteOffset records the original TIFF
// offset so callers can detect the movement.
// Reference: EXIF §4.6.5 tag 0x927C; MakerNote interoperability survey.
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
