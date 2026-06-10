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

	// BigTIFF spec §2; audit finding #107: refuse to re-encode a BigTIFF-sourced
	// EXIF as classic TIFF (magic 0x002A, 32-bit offsets). Doing so would silently
	// truncate every 64-bit IFD offset to 32 bits, corrupting any file whose
	// structures are positioned above 4 GiB. Return a clear, actionable error
	// before emitting any bytes so the caller can surface it rather than write
	// corrupt output.
	if e.BigTIFF {
		return nil, ErrBigTIFFEncodeNotSupported
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
	// task #199: bigEndian flag replaces binary.ByteOrder interface.
	isBig := order == binary.BigEndian
	if e.ExifIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagExifIFDPointer, Type: TypeLong, Count: 1, Value: exifPtrBuf[:], bigEndian: isBig})
	}
	if e.GPSIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagGPSIFDPointer, Type: TypeLong, Count: 1, Value: gpsPtrBuf[:], bigEndian: isBig})
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
// MakerNote bytes are preserved verbatim by exif.Encode. Offset correctness
// after relocation depends on the maker's offset convention:
//
//   - Blob-relative (safe, no rebasing needed): Canon (plain IFD at offset 0),
//     Panasonic ("Panasonic\0\0\0" prefix, IFD at offset 12, all val_or_off
//     relative to blob start), Nikon Type-3 (embedded TIFF header inside the
//     blob; all internal offsets relative to the embedded TIFF base which moves
//     with the blob), Olympus "OLYMPUS\0" format (IFD at offset 12, blob-relative
//     val_or_off), Pentax AOC / PENTAX.
//
//   - TIFF-absolute (offsets relative to the outer TIFF base — become stale when
//     the blob moves): Sony plain-IFD MakerNotes (DSLR-A series, ILCE, SLT —
//     plain IFD at offset 0, all OOL val_or_off are outer-TIFF-absolute), and
//     Olympus "OLYMP\0" MakerNotes (older compacts such as C5050Z — "OLYMP\0"
//     prefix, IFD at offset 8, all OOL val_or_off are outer-TIFF-absolute).
//     Nikon Type-1 (legacy D1, plain IFD at offset 0, big-endian, OOL offsets
//     are outer-TIFF-absolute) is a documented limitation: rebasing is not
//     implemented because Type-1 cameras are extremely rare and empirically
//     produce no OOL MakerNote entries in practice (ExifTool Nikon.pm confirms
//     Type-1 has no known OOL sub-structures; see relocate_makernote.go).
//
// Rebasing for Sony and Olympus OLYMP-type is performed by the TIFF relocators
// in format/tiff/relocate_makernote.go after exif.Encode places the blob.
// EXIF.MakerNoteOffset records the original TIFF offset so relocators can
// compute the movement delta.
//
// Reference: EXIF §4.6.5 tag 0x927C; ExifTool Sony.pm, Olympus.pm, Panasonic.pm,
// Nikon.pm; empirical analysis per #127.
func buildExifIFDEntries(e *EXIF, order binary.ByteOrder, interopPtrBuf *[4]byte) []IFDEntry {
	if e.ExifIFD == nil {
		return nil
	}

	// Strip existing InteropIFD pointer; we will re-add it with a freshly
	// computed offset when InteropIFD is present (EXIF §4.6.3, tag 0xA005
	// lives in ExifIFD, not IFD0).
	// task #199: bigEndian flag replaces binary.ByteOrder interface.
	isBig := order == binary.BigEndian
	entries := filterEntries(e.ExifIFD, 2, TagInteropIFDPointer)
	if e.InteropIFD != nil {
		entries = append(entries, IFDEntry{Tag: TagInteropIFDPointer, Type: TypeLong, Count: 1, Value: interopPtrBuf[:], bigEndian: isBig})
	}
	if e.MakerNote != nil && !hasEntry(entries, TagMakerNote) {
		entries = append(entries, IFDEntry{
			Tag:       TagMakerNote,
			Type:      TypeUndefined,
			Count:     uint32(len(e.MakerNote)), //nolint:gosec // G115: MakerNote length bounded by input
			Value:     e.MakerNote,
			bigEndian: isBig,
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
