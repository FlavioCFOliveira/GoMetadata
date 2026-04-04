package exif

import (
	"encoding/binary"
	"errors"
)

// encode serialises e to a raw EXIF byte stream beginning with the TIFF
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
func encode(e *EXIF) ([]byte, error) {
	if e == nil {
		return nil, errors.New("exif: cannot encode nil EXIF")
	}

	order := e.ByteOrder

	// Build IFD0 entries: strip existing sub-IFD pointers (we will re-add
	// them with freshly computed offsets).
	ifd0Entries := filterEntries(e.IFD0,
		TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer)

	// Reserve pointer entries so ifdTotalSize accounts for them correctly.
	// TypeLong / Count 1 → value fits inline (4 bytes); no value-area impact.
	// Stack-allocated arrays avoid one heap allocation per sub-IFD pointer.
	var exifPtrBuf, gpsPtrBuf, interopPtrBuf [4]byte // placeholders; patched below
	if e.ExifIFD != nil {
		ifd0Entries = append(ifd0Entries, IFDEntry{Tag: TagExifIFDPointer, Type: TypeLong, Count: 1, Value: exifPtrBuf[:], byteOrder: order})
	}
	if e.GPSIFD != nil {
		ifd0Entries = append(ifd0Entries, IFDEntry{Tag: TagGPSIFDPointer, Type: TypeLong, Count: 1, Value: gpsPtrBuf[:], byteOrder: order})
	}
	sortEntries(ifd0Entries)

	// Build ExifIFD entries: strip existing InteropIFD pointer and re-add
	// with a freshly computed offset when InteropIFD is present (EXIF §4.6.3,
	// tag 0xA005 lives in ExifIFD, not IFD0).
	var exifIFDEntries []IFDEntry
	if e.ExifIFD != nil {
		exifIFDEntries = filterEntries(e.ExifIFD, TagInteropIFDPointer)
		if e.InteropIFD != nil {
			exifIFDEntries = append(exifIFDEntries, IFDEntry{Tag: TagInteropIFDPointer, Type: TypeLong, Count: 1, Value: interopPtrBuf[:], byteOrder: order})
		}
		// Preserve raw MakerNote bytes verbatim when the ExifIFD no longer
		// contains a TagMakerNote entry (e.g., it was removed during re-parse).
		// We do NOT re-serialise MakerNoteIFD because MakerNote offsets are often
		// relative to the parent TIFF start, making them non-portable when moved.
		if e.MakerNote != nil && !hasEntry(exifIFDEntries, TagMakerNote) {
			exifIFDEntries = append(exifIFDEntries, IFDEntry{
				Tag:       TagMakerNote,
				Type:      TypeUndefined,
				Count:     uint32(len(e.MakerNote)), //nolint:gosec // G115: MakerNote length bounded by input
				Value:     e.MakerNote,
				byteOrder: order,
			})
		}
		sortEntries(exifIFDEntries)
	}

	// Compute block sizes so we can fill pointer values before writing.
	// Layout: [8 bytes TIFF header][IFD0][ExifIFD][GPS IFD][InteropIFD][IFD1...]
	const headerSize = uint32(8)
	ifd0Size := ifdTotalSize(ifd0Entries)
	exifStart := headerSize + ifd0Size
	exifSize := uint32(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSize(exifIFDEntries)
	}
	gpsStart := exifStart + exifSize
	gpsSize := uint32(0)
	if e.GPSIFD != nil {
		gpsSize = ifdTotalSize(e.GPSIFD.Entries)
	}
	interopStart := gpsStart + gpsSize
	interopSize := uint32(0)
	if e.InteropIFD != nil {
		interopSize = ifdTotalSize(e.InteropIFD.Entries)
	}

	// IFD1 (thumbnail) starts after InteropIFD (TIFF §2 next-IFD pointer chain).
	ifd1Start := interopStart + interopSize

	// Patch IFD0 sub-IFD pointer values now that their targets are known.
	for i := range ifd0Entries {
		switch ifd0Entries[i].Tag {
		case TagExifIFDPointer:
			order.PutUint32(ifd0Entries[i].Value, exifStart)
		case TagGPSIFDPointer:
			order.PutUint32(ifd0Entries[i].Value, gpsStart)
		}
	}

	// Patch ExifIFD InteropIFD pointer.
	for i := range exifIFDEntries {
		if exifIFDEntries[i].Tag == TagInteropIFDPointer {
			order.PutUint32(exifIFDEntries[i].Value, interopStart)
		}
	}

	// --- Write ---

	// TIFF header (TIFF §2): byte order mark, magic 0x002A, IFD0 offset.
	var hdr [8]byte
	if order == binary.LittleEndian {
		hdr[0], hdr[1] = 'I', 'I'
	} else {
		hdr[0], hdr[1] = 'M', 'M'
	}
	order.PutUint16(hdr[2:], 0x002A)
	order.PutUint32(hdr[4:], headerSize) // IFD0 starts right after the header

	out := make([]byte, 0, headerSize+ifd0Size+exifSize+gpsSize+interopSize)
	out = append(out, hdr[:]...)

	// IFD0: next-IFD pointer points to IFD1 if present.
	ifd0NextPtr := uint32(0)
	if e.IFD0 != nil && e.IFD0.Next != nil {
		ifd0NextPtr = ifd1Start
	}
	out = writeIFD(out, ifd0Entries, order, uint32(len(out)), ifd0NextPtr) //nolint:gosec // G115: output offset bounded by buffer size

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

	return out, nil
}
