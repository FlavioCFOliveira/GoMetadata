package exif

import (
	"encoding/binary"
	"fmt"
)

// encode serialises e to a raw EXIF byte stream beginning with the TIFF
// header. The caller is responsible for prepending the "Exif\x00\x00"
// identifier required by JPEG APP1 (EXIF §4.5.4).
func encode(e *EXIF) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("exif: cannot encode nil EXIF")
	}

	order := e.ByteOrder

	// Build IFD0 entries: strip existing sub-IFD pointers (we will re-add
	// them with freshly computed offsets).
	ifd0Entries := filterEntries(e.IFD0,
		TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer)

	// Reserve pointer entries so ifdTotalSize accounts for them correctly.
	// TypeLong / Count 1 → value fits inline (4 bytes); no value-area impact.
	zeroPtr := func(tag TagID) IFDEntry {
		v := make([]byte, 4) // placeholder; patched below
		return IFDEntry{Tag: tag, Type: TypeLong, Count: 1, Value: v, byteOrder: order}
	}
	if e.ExifIFD != nil {
		ifd0Entries = append(ifd0Entries, zeroPtr(TagExifIFDPointer))
	}
	if e.GPSIFD != nil {
		ifd0Entries = append(ifd0Entries, zeroPtr(TagGPSIFDPointer))
	}
	sortEntries(ifd0Entries)

	// Compute block sizes so we can fill pointer values before writing.
	// Layout: [8 bytes TIFF header][IFD0 block][ExifIFD block][GPS IFD block]
	const headerSize = uint32(8)
	ifd0Size := ifdTotalSize(ifd0Entries)
	exifStart := headerSize + ifd0Size
	exifSize := uint32(0)
	if e.ExifIFD != nil {
		exifSize = ifdTotalSize(e.ExifIFD.Entries)
	}
	gpsStart := exifStart + exifSize

	// Patch pointer values now that their targets are known.
	for i := range ifd0Entries {
		switch ifd0Entries[i].Tag {
		case TagExifIFDPointer:
			order.PutUint32(ifd0Entries[i].Value, exifStart)
		case TagGPSIFDPointer:
			order.PutUint32(ifd0Entries[i].Value, gpsStart)
		}
	}

	// --- Write ---

	// TIFF header (TIFF §2): byte order mark, magic 0x002A, IFD0 offset.
	hdr := make([]byte, 8)
	if order == binary.LittleEndian {
		hdr[0], hdr[1] = 'I', 'I'
	} else {
		hdr[0], hdr[1] = 'M', 'M'
	}
	order.PutUint16(hdr[2:], 0x002A)
	order.PutUint32(hdr[4:], headerSize) // IFD0 starts right after the header

	out := make([]byte, 0, headerSize+ifd0Size+exifSize)
	out = append(out, hdr...)
	out = writeIFD(out, ifd0Entries, order, uint32(len(out)))
	if e.ExifIFD != nil {
		out = writeIFD(out, e.ExifIFD.Entries, order, uint32(len(out)))
	}
	if e.GPSIFD != nil {
		out = writeIFD(out, e.GPSIFD.Entries, order, uint32(len(out)))
	}

	return out, nil
}
