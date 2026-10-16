package exif

import (
	"strconv"

	"github.com/FlavioCFOliveira/GoMetadata/internal/boundscheck"
)

// thumbnail_strips.go — the strips of an uncompressed thumbnail (task #368).
//
// Exif 3.1 §4.5.8 (pp. 25-26): a thumbnail is recorded in the 1st IFD, either
// compressed (Compression 6, JPEGInterchangeFormat and its length) or, when the
// primary image allows it (Table 3, p. 26), uncompressed "in conformance with
// Baseline TIFF Rev. 6.0 RGB Full Color Images or TIFF Rev. 6.0 Extensions YCbCr
// Images": the image data is then a set of strips addressed by StripOffsets
// (0x0111) and StripByteCounts (0x0117), whose counts are StripsPerImage, or
// SamplesPerPixel x StripsPerImage when PlanarConfiguration is 2 (§4.6.5.2.1
// and §4.6.5.2.3, pp. 39-40; both tags are SHORT or LONG).
//
// Parse used to read none of this, so Encode wrote StripOffsets with the offsets
// of the SOURCE stream while the strip data (up to 51948 bytes for the Kodak
// ProBack of the corpus) was dropped: the thumbnail was lost and its offsets
// were stale. Parse with CarryThumbnailStrips now reads the strips; Encode
// writes them after the IFD (chain.go) and rewrites StripOffsets as LONG values
// (an offset that does not fit a SHORT is the reason both types are allowed, so
// LONG is always safe to write).
//
// Reference (design, Apache-2.0): Apache Commons Imaging
// (apache/commons-imaging@439b7722) formats/tiff/write/TiffOutputDirectory.java
// getOutputItems (lines 757-794: StripOffsets rewritten as a LONG array of the
// strip count, StripByteCounts as LONG, one output item per strip) and
// TiffOutputSummary.java updateOffsets (lines 63-72: each offset is set to the
// final position of its strip). ExifTool does not rewrite strip data in the
// same way (it edits in place), so it is no reference here.

const (
	// maxThumbnailStrips bounds the number of strips carried for one thumbnail.
	// A 160x120 RGB thumbnail (Exif 3.1 §4.5.8 sample, Annex A.1) has 96 strips
	// at most; a strip count above this limit does not describe a thumbnail.
	maxThumbnailStrips = 4096
	// maxThumbnailStripBytes bounds the strip data carried for one thumbnail
	// (1 MiB). The data is bounded by the parsed bytes already; the limit keeps
	// a multi-page TIFF image from being taken for a thumbnail.
	maxThumbnailStripBytes = 1 << 20
)

// attachThumbnailStrips reads the strips of the uncompressed thumbnail of every
// IFD of the chain after IFD0 of e, when CarryThumbnailStrips was requested.
// An IFD with a JPEG thumbnail, or without StripOffsets, is left alone. A
// thumbnail whose strips cannot be carried is left as parsed and reported in
// e.Warnings: Encode then writes its entries unchanged.
//
// The strips of all the IFDs together are bounded by the length of b, as the
// JPEG thumbnails are (traverseBudget.chargeThumbnail): strips are disjoint
// ranges of b, so a valid file never needs more, and without the bound the
// IFDs of a chain addressing one range would be carried, and written by Encode,
// once each (security audit F-06a).
func attachThumbnailStrips(b []byte, e *EXIF, alias bool) {
	remaining := len(b)
	for ifd := e.IFD0.Next; ifd != nil; ifd = ifd.Next {
		if ifd.ThumbnailData != nil || ifd.Get(TagStripOffsets) == nil {
			continue
		}
		strips, reason := readThumbnailStrips(b, ifd, alias, remaining)
		for _, s := range strips {
			remaining -= len(s)
		}
		if strips == nil {
			e.Warnings = append(e.Warnings, "exif: thumbnail image data not carried: "+reason)
			continue
		}
		if ifd.Sub == nil {
			ifd.Sub = &SubIFDs{}
		}
		ifd.Sub.ThumbnailStrips = strips
	}
}

// readThumbnailStrips returns the strips that the StripOffsets and
// StripByteCounts entries of ifd address inside b, or nil and the reason.
//
// limit bounds the total strip bytes (the allowance left to the chain).
func readThumbnailStrips(b []byte, ifd *IFD, alias bool, limit int) (strips [][]byte, reason string) {
	offE, cntE := ifd.Get(TagStripOffsets), ifd.Get(TagStripByteCounts)
	n, reason := stripCount(offE, cntE)
	if reason != "" {
		return nil, reason
	}
	strips = make([][]byte, n)
	var total uint64
	for i := range n {
		off, size, ok := stripExtent(offE, cntE, i)
		if !ok {
			return nil, "a strip offset or byte count is not an integer of a supported type"
		}
		total += size // size is bounded below before the next addition can overflow
		if size > maxThumbnailStripBytes || total > maxThumbnailStripBytes {
			return nil, "the strip data exceeds " + strconv.Itoa(maxThumbnailStripBytes) + " bytes"
		}
		if total > uint64(max(limit, 0)) {
			return nil, "the strip data of the IFD chain exceeds the size of the Exif data"
		}
		if !boundscheck.Fits(off, size, uint64(len(b))) {
			return nil, "a strip lies outside the Exif data"
		}
		end := off + size // Fits proved this cannot overflow
		if alias {
			strips[i] = b[off:end:end]
			continue
		}
		strips[i] = append(make([]byte, 0, size), b[off:end]...)
	}
	return strips, ""
}

// stripCount returns the number of strips that the StripOffsets entry offE and
// the StripByteCounts entry cntE (nil when missing) both hold, or the reason it
// cannot be carried.
func stripCount(offE, cntE *IFDEntry) (n int, reason string) {
	if cntE == nil {
		return 0, "StripByteCounts is missing"
	}
	n = int(offE.Count)
	if n <= 0 || n > maxThumbnailStrips || uint32(n) != cntE.Count {
		return 0, "strip count " + strconv.FormatUint(uint64(offE.Count), 10) + " is zero, too large or differs from the byte count entries"
	}
	return n, ""
}

// stripExtent returns the offset and the byte count of strip i.
func stripExtent(offE, cntE *IFDEntry, i int) (off, size uint64, ok bool) {
	off, ok1 := elementUint(offE, i)
	size, ok2 := elementUint(cntE, i)
	return off, size, ok1 && ok2
}

// elementUint returns element i of the SHORT, LONG or LONG8 entry e, in the
// byte order of the entry.
func elementUint(e *IFDEntry, i int) (uint64, bool) {
	if e.Type != TypeShort && e.Type != TypeLong && e.Type != TypeLong8 {
		return 0, false
	}
	sz := int(typeSizeBigTIFF(e.Type)) //nolint:gosec // G115: 2, 4 or 8
	if i < 0 || (i+1)*sz > len(e.Value) {
		return 0, false
	}
	ord, v := e.order(), e.Value[i*sz:]
	switch sz {
	case 2:
		return uint64(ord.Uint16(v)), true
	case 4:
		return uint64(ord.Uint32(v)), true
	}
	return ord.Uint64(v), true
}
