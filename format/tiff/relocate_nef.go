package tiff

// relocate_nef.go — Nikon NEF-specific copy-and-relocate preprocessing (task #102).
//
// Problem: Nikon Type-3 MakerNote bodies (all modern Nikon DSLRs including the D70)
// embed a self-contained TIFF header inside the MakerNote blob.  Within that embedded
// TIFF, Nikon stores two sub-directories that point OUTSIDE the declared-size MakerNote
// value in the parent TIFF entry:
//
//   - PreviewIFD (MakerNote tag 0x0011): an IFD whose tag 0x0201/0x0202
//     (PreviewImageStart / PreviewImageLength) reference the preview JPEG stored
//     elsewhere in the file.  The 0x0201 value is a MakerNote-TIFF-relative offset
//     (relative to the MakerNote's internal TIFF header start, not the outer TIFF base).
//
//   - NikonScanIFD (MakerNote tag 0x0E10): an IFD typically containing 0 entries;
//     no image data to relocate.
//
// The standard relocateTIFFFromParsed does not enumerate these blocks because:
//   1. The MakerNote tag (0x927C) is an opaque "undefined" blob — its internal
//      structure is not visible to the outer TIFF layer.
//   2. The preview image data is referenced with MakerNote-relative offsets, not
//      outer-TIFF-absolute offsets, so it does not appear in any of the outer IFD
//      chains walked by enumerateImageBlocks / enumerateSubIFDs.
//
// Fix (task #102):
//
// relocateTIFFFromParsedNEF is a thin wrapper around the main relocation logic
// that runs a Nikon-specific preprocessing step:
//
//   Step A — MakerNote blob extension:
//     The outer TIFF's exif.IFDEntry for tag 0x927C carries a Value slice of
//     exactly the declared byte count (e.g. 9705 for D70).  The PreviewIFD and
//     NikonScanIFD live just beyond this boundary.  We extend the Value slice to
//     cover the full extent of all MakerNote-referenced IFDs and their OOL value
//     areas so that exif.Encode writes them into the output.
//
//   Step B — Preview image block registration:
//     We parse the extended MakerNote blob to locate PreviewIFD 0x0201/0x0202 and
//     translate their MakerNote-relative offsets to outer-TIFF-absolute offsets.
//     These become additional imageBlock records fed to the main relocation engine.
//
//   Step C — Post-encode patch:
//     After the main engine produces finalTIFF (the re-encoded IFD skeleton),
//     we locate the MakerNote value in the output, compute its new absolute base,
//     and overwrite the 0x0201 val-or-off field inside PreviewIFD with the
//     updated MakerNote-relative offset.
//
// Nikon Type-3 MakerNote format (ExifTool Nikon.pm):
//
//	[0..5]   "Nikon\x00"   6-byte magic prefix
//	[6..7]   version        (e.g. 0x02 0x10)
//	[8..9]   byte order     "II" or "MM"
//	[10..11] TIFF magic     0x2A00 (BE) or 0x002A (LE)
//	[12..15] IFD0 offset    relative to the embedded TIFF base at b[8]
//
// All value offsets within the embedded IFD are relative to b[8] (the MakerNote
// TIFF base, also called "mnTIFFBase" in this file).
//
// Key offset arithmetic:
//   makerNoteFileOff:    outer-TIFF-absolute position of the MakerNote blob
//   mnTIFFHdrOff:        byte offset of the embedded TIFF header within the blob
//                        (8 for version 0x0210, 10 for version 0x0200/D70)
//   mnTIFFBase:          makerNoteFileOff + mnTIFFHdrOff
//   PreviewIFD file off: mnTIFFBase + previewIFDRelOff (value of MakerNote tag 0x0011)
//   PreviewImage off:    mnTIFFBase + previewImageRelOff (value of PreviewIFD tag 0x0201)
//
// After relocation:
//   new_mn_file_off:       position of MakerNote blob in finalTIFF output
//   new_mn_tiff_base:      new_mn_file_off + mnTIFFHdrOff
//   new_preview_abs_off:   blk.newOffset (assigned by assignNewOffsets)
//   new_preview_rel_off:   new_preview_abs_off − new_mn_tiff_base
//
// Spec / reference:
//   - ExifTool Nikon.pm: Nikon Type-3 MakerNote internal structure.
//   - SPIKE #24: Nikon MakerNote is NOT move-safe for its external references.
//   - Task #102: implement correct relocation.
//   - TIFF 6.0 §2: IFD entry layout.
//   - EXIF §4.6.5, tag 0x927C (MakerNote).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Sentinel errors for the NEF-specific relocation subsystem.
var (
	// ErrNikonPreviewPositionMismatch is returned when the PreviewIFD offset/size
	// fields in the MakerNote blob have positions that precede the blob start.
	ErrNikonPreviewPositionMismatch = errors.New("tiff: Nikon PreviewIFD 0x0201/0x0202 positions precede MakerNote blob start")

	// ErrNikonPreviewOutOfBounds is returned when the preview image block
	// (referenced from PreviewIFD 0x0201/0x0202) extends beyond the base buffer.
	ErrNikonPreviewOutOfBounds = errors.New("tiff: Nikon PreviewIFD image block out of bounds")

	// ErrNikonPreviewOverflow is returned when the new preview image offset would
	// overflow uint32.
	ErrNikonPreviewOverflow = errors.New("tiff: Nikon preview image new offset overflowed uint32")

	// ErrNikonPatchFailed is returned when the post-encode patching of the
	// MakerNote PreviewIFD offsets fails.
	ErrNikonPatchFailed = errors.New("tiff: Nikon PreviewIFD patch failed")
)

// nikonMNPrefixMaxLen is the maximum number of bytes to scan within a Nikon
// Type-3 MakerNote blob when searching for the embedded TIFF header ("II"/"MM").
// The documented offset is 8 but real-world cameras may place it at 10.
const nikonMNPrefixMaxLen = 16

// findNikonMNTIFFHeader scans the first nikonMNPrefixMaxLen bytes of a Nikon
// Type-3 MakerNote blob for the embedded TIFF header byte-order marker ("II" or
// "MM") and returns the byte offset at which it starts, along with the byte
// order.
//
// The documented offset is 8 (after the 6-byte "Nikon\x00" prefix + 2-byte
// version), but some camera models (e.g. Nikon D70 with version 0x0200) place
// the TIFF header at offset 10 (with a 2-byte padding at [8..9]).
//
// Returns (0, nil) if the header is not found within the first nikonMNPrefixMaxLen bytes.
func findNikonMNTIFFHeader(blob []byte) (int, binary.ByteOrder) {
	end := min(nikonMNPrefixMaxLen, len(blob)-8) // need at least 8 bytes for the full header
	for off := 6; off < end; off++ {
		if off+8 > len(blob) {
			break
		}
		switch {
		case blob[off] == 'I' && blob[off+1] == 'I':
			if binary.LittleEndian.Uint16(blob[off+2:]) == 0x002A {
				return off, binary.LittleEndian
			}
		case blob[off] == 'M' && blob[off+1] == 'M':
			if binary.BigEndian.Uint16(blob[off+2:]) == 0x002A {
				return off, binary.BigEndian
			}
		}
	}
	return 0, nil
}

// Nikon MakerNote-internal tag IDs referenced during NEF relocation.
// These are Nikon proprietary tag numbers, not EXIF/TIFF standard tags.
// Source: ExifTool Nikon.pm.
const (
	// nikonTagPreviewIFD is the Nikon MakerNote tag that holds the offset of the
	// PreviewIFD within the MakerNote embedded TIFF (TypeLong, Count=1, inline).
	nikonTagPreviewIFD = exif.TagID(0x0011)

	// nikonTagNikonScanIFD is the Nikon MakerNote tag that holds the offset of the
	// NikonScanIFD within the MakerNote embedded TIFF (TypeLong, Count=1, inline).
	nikonTagNikonScanIFD = exif.TagID(0x0E10)
)

// nikonPreviewInfo collects everything the NEF preprocessing step learns about
// the Nikon PreviewIFD and its image block.  It is threaded through the
// relocation engine and used in the post-encode patching step.
type nikonPreviewInfo struct {
	// previewBlock is the imageBlock for the preview JPEG.
	// ifdPtr is nil (standalone block, not owned by any outer exif.IFD).
	// newOffset is filled in by assignNewOffsets.
	previewBlock *imageBlock

	// mnEntry is the exif.IFDEntry for tag 0x927C in the ExifIFD.
	// Its Value slice holds the (potentially extended) MakerNote blob that
	// exif.Encode writes into the output stream.
	mnEntry *exif.IFDEntry

	// previewOff201InBlob is the byte offset within the MakerNote blob
	// at which the PreviewIFD tag 0x0201 val-or-off field (4 bytes) resides.
	// Patched after encode.
	previewOff201InBlob int

	// previewLen202InBlob is the byte offset within the MakerNote blob
	// at which the PreviewIFD tag 0x0202 val-or-off field (4 bytes) resides.
	// Patched after encode.
	previewLen202InBlob int

	// mnOrder is the byte order of the embedded TIFF inside the MakerNote
	// (derived from the embedded TIFF header; may differ from the outer order).
	mnOrder binary.ByteOrder

	// previewImageSize is the declared size of the preview JPEG in bytes
	// (value of PreviewIFD tag 0x0202).  Written back verbatim during patching.
	previewImageSize uint32

	// mnTIFFHdrOff is the byte offset within the MakerNote blob at which the
	// embedded TIFF header ("II"/"MM") starts.  May be 8 (version 0x0210) or
	// 10 (version 0x0200, D70) depending on the camera model.
	// Used by patchNikonPreviewInFinalTIFF to compute the new TIFF base.
	mnTIFFHdrOff int
}

// isNikonType3Blob reports whether b is a Nikon Type-3 MakerNote blob.
// Nikon Type-3 blobs begin with the 6-byte sequence "Nikon\x00".
// The minimum length is 18 bytes: 6-byte magic prefix + version(2) + padding(2)
// + embedded TIFF header(8).
func isNikonType3Blob(b []byte) bool {
	return len(b) >= 18 &&
		b[0] == 'N' && b[1] == 'i' && b[2] == 'k' &&
		b[3] == 'o' && b[4] == 'n' && b[5] == 0x00
}

// extractNikonPreviewInfo inspects the parsed EXIF for a Nikon Type-3 MakerNote
// and, if found, returns a nikonPreviewInfo describing the PreviewIFD image block
// and how to patch the MakerNote after relocation.
//
// base is the original TIFF byte stream (offset-arithmetic is done against base).
// e is the parsed (and possibly mutated) EXIF struct.
//
// Side effect: when a Nikon Type-3 MakerNote with a PreviewIFD is detected,
// the function extends the MakerNote entry's Value slice in-place to cover the
// full MakerNote-referenced extent (the PreviewIFD + NikonScanIFD live beyond the
// declared byte count in the outer TIFF entry).  exif.Encode then writes the
// extended blob, preserving those structures in the output.
//
// Returns nil (no error) when no relevant Nikon MakerNote is found.
func extractNikonPreviewInfo(base []byte, e *exif.EXIF) (*nikonPreviewInfo, error) { //nolint:cyclop,gocyclo,funlen // Nikon structure traversal; complexity and length are inherent to the multi-step inspection
	if e == nil || e.ExifIFD == nil {
		return nil, nil //nolint:nilnil // nil info means "no Nikon preview found"; not an error
	}

	// MakerNote must be present and recorded by the parser.
	if !isNikonType3Blob(e.MakerNote) {
		return nil, nil //nolint:nilnil // not a Nikon Type-3 MakerNote; skip silently
	}

	// Locate the embedded TIFF header within the blob.
	// This scans for the actual "II"/"MM" + 0x002A sequence, handling cameras
	// that place the TIFF header at offset 8 (version 0x0210) or 10 (D70, 0x0200).
	mnTIFFHdrOff, mnOrder := findNikonMNTIFFHeader(e.MakerNote)
	if mnOrder == nil {
		return nil, nil //nolint:nilnil // no valid TIFF header in blob; skip
	}

	// Locate the MakerNote entry (tag 0x927C) in the ExifIFD so we can extend
	// its Value slice.
	mnEntry := e.ExifIFD.Get(exif.TagMakerNote)
	if mnEntry == nil {
		return nil, nil //nolint:nilnil // no MakerNote entry in ExifIFD
	}

	// MakerNoteOffset is the outer-TIFF-absolute file position of the blob.
	// This is set by exif.Parse via IFDEntry.rawOffset; exposed on EXIF.MakerNoteOffset.
	mnFilePos := e.MakerNoteOffset
	if mnFilePos == 0 {
		// Fallback: search base for the Nikon magic prefix.
		mnFilePos = findNikonBlobInBase(base, e.MakerNote)
		if mnFilePos == 0 {
			return nil, nil //nolint:nilnil // blob not found in base; skip
		}
	}
	if uint64(mnFilePos)+uint64(len(mnEntry.Value)) > uint64(len(base)) {
		return nil, nil //nolint:nilnil // blob extends beyond base; skip
	}

	// MakerNote TIFF base in the outer file:
	// All MakerNote-internal offsets are relative to this position.
	mnTIFFBase := mnFilePos + uint32(mnTIFFHdrOff) //nolint:gosec // G115: mnFilePos bounded by base; mnTIFFHdrOff <= 16

	// MakerNote IFD0 file offset.
	tiffHdr := e.MakerNote[mnTIFFHdrOff:]
	ifd0RelOff := mnOrder.Uint32(tiffHdr[4:])
	mnIFD0FileOff := mnTIFFBase + ifd0RelOff

	// Find PreviewIFD (tag 0x0011) in the MakerNote IFD0.
	previewIFDRelOff, hasPreview := findInlineIFDPointer(base, mnIFD0FileOff, nikonTagPreviewIFD, mnOrder)
	if !hasPreview {
		// No PreviewIFD; nothing Nikon-specific to do.
		return nil, nil //nolint:nilnil // no PreviewIFD in this MakerNote
	}

	// Optionally find NikonScanIFD (tag 0x0E10) to include its bytes in the extension.
	nikonScanRelOff, hasScan := findInlineIFDPointer(base, mnIFD0FileOff, nikonTagNikonScanIFD, mnOrder)

	// Parse the PreviewIFD to obtain 0x0201/0x0202 and their field positions.
	previewIFDFileOff := mnTIFFBase + previewIFDRelOff
	previewImgRelOff, previewImgSize, off201FilePos, len202FilePos, ok :=
		parsePreviewIFDEntries(base, previewIFDFileOff, mnOrder)
	if !ok || previewImgRelOff == 0 || previewImgSize == 0 {
		// PreviewIFD present but has no image data; skip.
		return nil, nil //nolint:nilnil // PreviewIFD with no image block
	}

	// Convert MakerNote-relative preview offset to outer-TIFF-absolute.
	previewImgFileOff := mnTIFFBase + previewImgRelOff

	// Compute the full extent of the MakerNote-referenced structures.
	// The preview IMAGE data is intentionally excluded (it is an imageBlock).
	fullExtent := computeMakerNoteExtent(base, mnTIFFBase, mnIFD0FileOff, previewIFDFileOff,
		mnOrder, nikonScanRelOff, hasScan)

	// Extend the MakerNote blob if needed so exif.Encode copies the full range.
	currentBlobEnd := mnFilePos + uint32(len(mnEntry.Value)) //nolint:gosec // G115: < len(base)
	if fullExtent > currentBlobEnd {
		newEnd := fullExtent
		if uint64(newEnd) > uint64(len(base)) {
			newEnd = uint32(len(base)) //nolint:gosec // G115: len(base) < 2^32
		}
		extended := make([]byte, newEnd-mnFilePos)
		copy(extended, base[mnFilePos:newEnd])
		// Replace the Value slice with the extended copy.
		// exif.Encode reads Value to write the MakerNote blob.
		mnEntry.Value = extended
		// The Count field must reflect the new size so that exif.Encode writes
		// the correct count/type combination for the OOL entry.
		mnEntry.Count = uint32(len(extended)) //nolint:gosec // G115: len < 2^32
	}

	// Compute blob-relative positions for patching (subtract mnFilePos from file abs pos).
	previewOff201InBlob := int(off201FilePos) - int(mnFilePos)
	previewLen202InBlob := int(len202FilePos) - int(mnFilePos)
	if previewOff201InBlob < 0 || previewLen202InBlob < 0 {
		return nil, fmt.Errorf("%w: (off201=%d off202=%d blobStart=%d)",
			ErrNikonPreviewPositionMismatch, off201FilePos, len202FilePos, mnFilePos)
	}

	// Create the preview imageBlock.
	// ifdPtr=nil marks it as a "standalone" block (not owned by any outer exif.IFD).
	// It is tracked separately via nikonPreviewInfo and excluded from
	// removeImageOffsetEntries (which requires a non-nil IFD pointer to locate entries).
	previewEnd := uint64(previewImgFileOff) + uint64(previewImgSize)
	if previewEnd > uint64(len(base)) {
		return nil, fmt.Errorf("%w (offset=%d size=%d baseLen=%d)",
			ErrNikonPreviewOutOfBounds, previewImgFileOff, previewImgSize, len(base))
	}

	blk := &imageBlock{
		srcOffset: previewImgFileOff,
		size:      previewImgSize,
		ifdPtr:    nil, // standalone
		entryTag:  exif.TagJPEGInterchangeFormat,
		index:     0,
	}

	return &nikonPreviewInfo{
		previewBlock:        blk,
		mnEntry:             mnEntry,
		previewOff201InBlob: previewOff201InBlob,
		previewLen202InBlob: previewLen202InBlob,
		mnOrder:             mnOrder,
		previewImageSize:    previewImgSize,
		mnTIFFHdrOff:        mnTIFFHdrOff,
	}, nil
}

// findInlineIFDPointer scans the MakerNote IFD at ifd0FileOff in base for a tag
// with the given ID that holds a single TypeLong (uint32) inline IFD pointer.
// Returns (relOffset, true) when found; the returned offset is MakerNote-TIFF-relative.
func findInlineIFDPointer(base []byte, ifd0FileOff uint32, tag exif.TagID, order binary.ByteOrder) (uint32, bool) {
	if uint64(ifd0FileOff)+2 > uint64(len(base)) {
		return 0, false
	}
	count := int(order.Uint16(base[ifd0FileOff:]))
	pos := int(ifd0FileOff) + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		entryTag := exif.TagID(order.Uint16(base[e:]))
		if entryTag != tag {
			continue
		}
		// Must be TypeLong (4), Count=1 to be a valid IFD pointer (inline).
		if order.Uint16(base[e+2:]) != 4 || order.Uint32(base[e+4:]) != 1 {
			continue
		}
		return order.Uint32(base[e+8:]), true
	}
	return 0, false
}

// parsePreviewIFDEntries parses the PreviewIFD at previewIFDFileOff in base to
// extract the PreviewImageStart (0x0201) and PreviewImageLength (0x0202) values.
//
// Returns:
//   - previewImgRelOff: MakerNote-TIFF-relative offset of the preview JPEG (0x0201 value)
//   - previewImgSize:   byte count of the preview JPEG (0x0202 value)
//   - off201FilePos:    file-absolute position of the 0x0201 val-or-off field (for patching)
//   - len202FilePos:    file-absolute position of the 0x0202 val-or-off field (for patching)
//   - ok:               false if the IFD is unreadable
//
// Both 0x0201 and 0x0202 must be TypeLong (4), Count=1, inline.
func parsePreviewIFDEntries(base []byte, previewIFDFileOff uint32, order binary.ByteOrder) (
	previewImgRelOff, previewImgSize, off201FilePos, len202FilePos uint32, ok bool,
) {
	if uint64(previewIFDFileOff)+2 > uint64(len(base)) {
		return 0, 0, 0, 0, false
	}
	count := int(order.Uint16(base[previewIFDFileOff:]))
	pos := int(previewIFDFileOff) + 2
	if pos+count*12 > len(base) {
		return 0, 0, 0, 0, false
	}

	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		tag := exif.TagID(order.Uint16(base[e:]))
		// Must be TypeLong (4), Count=1, inline.
		if order.Uint16(base[e+2:]) != 4 || order.Uint32(base[e+4:]) != 1 {
			continue
		}
		valOrOff := order.Uint32(base[e+8:])
		fieldPos := uint32(e) + 8 //nolint:gosec // G115: e < len(base)

		switch tag {
		case exif.TagJPEGInterchangeFormat: // 0x0201
			previewImgRelOff = valOrOff
			off201FilePos = fieldPos
		case exif.TagJPEGInterchangeFormatLength: // 0x0202
			previewImgSize = valOrOff
			len202FilePos = fieldPos
		}
	}
	return previewImgRelOff, previewImgSize, off201FilePos, len202FilePos, true
}

// computeMakerNoteExtent computes the file offset one past the last byte of all
// structures that must be included in the extended MakerNote blob.
//
// Included: MakerNote IFD0 fixed block + OOL values, PreviewIFD fixed block + OOL
// values (XResolution, YResolution, etc.), and optionally NikonScanIFD.
// Excluded: the preview JPEG itself (that is an imageBlock, not blob data).
func computeMakerNoteExtent(
	base []byte,
	mnTIFFBase, mnIFD0FileOff, previewIFDFileOff uint32,
	order binary.ByteOrder,
	nikonScanRelOff uint32,
	hasScan bool,
) uint32 {
	var maxEnd uint32
	maxEnd = ifdExtentInMN(base, mnIFD0FileOff, mnTIFFBase, order, maxEnd)
	maxEnd = ifdExtentInMN(base, previewIFDFileOff, mnTIFFBase, order, maxEnd)
	if hasScan {
		maxEnd = ifdExtentInMN(base, mnTIFFBase+nikonScanRelOff, mnTIFFBase, order, maxEnd)
	}
	return maxEnd
}

// ifdExtentInMN returns the file offset one past the last byte occupied by the IFD
// at ifdFileOff and its out-of-line value areas.
//
// OOL offsets within this IFD are ifdTIFFBase-relative (for the MakerNote embedded
// TIFF, ifdTIFFBase = mnTIFFBase).
// cur is the running maximum; it is updated and returned.
//
// Tag 0x0201 (JPEGInterchangeFormat) OOL areas are intentionally excluded because
// the preview image is tracked as an imageBlock.
func ifdExtentInMN(base []byte, ifdFileOff, ifdTIFFBase uint32, order binary.ByteOrder, cur uint32) uint32 { //nolint:cyclop,gocyclo // IFD scanning requires bounds-checking and tag-filtering branches; complexity is inherent to binary parsing
	if uint64(ifdFileOff)+2 > uint64(len(base)) {
		return cur
	}
	count := int(order.Uint16(base[ifdFileOff:]))
	// Fixed block: count(2) + entries(count×12) + nextIFD(4).
	fixedEnd := uint32(int(ifdFileOff) + 2 + count*12 + 4) //nolint:gosec // G115: bounded by file size
	if fixedEnd > cur {
		cur = fixedEnd
	}

	pos := int(ifdFileOff) + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		tag := exif.TagID(order.Uint16(base[e:]))
		// Skip the preview image offset tag — its data is an imageBlock.
		if tag == exif.TagJPEGInterchangeFormat {
			continue
		}
		entryType := order.Uint16(base[e+2:])
		entryCount := order.Uint32(base[e+4:])
		sz := typeSize(entryType)
		if sz == 0 || entryCount == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCount)
		if total <= 4 {
			continue // inline value; no OOL area
		}
		// OOL: val-or-off is ifdTIFFBase-relative.
		relOff := order.Uint32(base[e+8:])
		absOff := ifdTIFFBase + relOff
		oolEnd := absOff + uint32(total) //nolint:gosec // G115: bounded by entry
		if uint64(oolEnd) > uint64(len(base)) {
			oolEnd = uint32(len(base)) //nolint:gosec // G115: len(base) < 2^32
		}
		if oolEnd > cur {
			cur = oolEnd
		}
	}
	return cur
}

// findNikonBlobInBase searches base for the first occurrence of the Nikon Type-3
// magic prefix "Nikon\x00" that aligns with the given blob's first 6 bytes.
// Returns the file-absolute offset if found, or 0.
// This is a fallback for the rare case where EXIF.MakerNoteOffset is unavailable.
func findNikonBlobInBase(base, blob []byte) uint32 {
	const prefixLen = 6
	if len(blob) < prefixLen || len(base) < prefixLen {
		return 0
	}
	prefix := blob[:prefixLen]
	for i := range len(base) - prefixLen + 1 {
		match := true
		for j, b := range prefix {
			if base[i+j] != b {
				match = false
				break
			}
		}
		if match {
			return uint32(i) // i < len(base)-prefixLen+1 ≤ len(base); file buffers fit in uint32
		}
	}
	return 0
}

// patchNikonPreviewInFinalTIFF locates the MakerNote value inside finalTIFF and
// overwrites the PreviewIFD 0x0201 / 0x0202 val-or-off fields with the
// new MakerNote-relative preview offset and the (unchanged) preview size.
//
// Algorithm:
//  1. Walk IFD0 in finalTIFF to find the ExifIFD pointer (tag 0x8769).
//  2. Within ExifIFD, find the MakerNote entry (tag 0x927C).
//  3. The MakerNote entry's val-or-off gives the blob position in finalTIFF.
//  4. new_mn_tiff_base = blob_pos + nikonMNTIFFHeaderOff.
//  5. new_preview_rel = info.previewBlock.newOffset − new_mn_tiff_base.
//  6. Patch blob[info.previewOff201InBlob] ← new_preview_rel.
//  7. Patch blob[info.previewLen202InBlob] ← info.previewImageSize (unchanged).
func patchNikonPreviewInFinalTIFF(finalTIFF []byte, info *nikonPreviewInfo, order binary.ByteOrder) error {
	if len(finalTIFF) < 8 {
		return fmt.Errorf("%w: finalTIFF too short (%d bytes)", ErrNikonPatchFailed, len(finalTIFF))
	}

	// IFD0 offset from TIFF header bytes 4-7.
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return fmt.Errorf("%w: IFD0 offset %d out of bounds (finalTIFF len=%d)",
			ErrNikonPatchFailed, ifd0Off, len(finalTIFF))
	}

	// Find ExifIFD pointer (tag 0x8769) in IFD0.
	exifIFDOff, found := scanIFDForTagValOrOff(finalTIFF, ifd0Off, uint16(exif.TagExifIFDPointer), order)
	if !found {
		return fmt.Errorf("%w: ExifIFD pointer (tag 0x8769) not found in IFD0", ErrNikonPatchFailed)
	}

	exifStart := int(exifIFDOff)
	if exifStart+2 > len(finalTIFF) {
		return fmt.Errorf("%w: ExifIFD at %d out of finalTIFF bounds (len=%d)",
			ErrNikonPatchFailed, exifStart, len(finalTIFF))
	}

	// Find MakerNote (tag 0x927C) in ExifIFD.
	mnBlobOff, found := findOOLEntryOffset(finalTIFF, exifStart, uint16(exif.TagMakerNote), order)
	if !found {
		return fmt.Errorf("%w: MakerNote (tag 0x927C) not found or inline in ExifIFD", ErrNikonPatchFailed)
	}

	// mnBlobOff is the absolute position of the MakerNote blob within finalTIFF.
	// The embedded TIFF header starts at info.mnTIFFHdrOff bytes into the blob.
	newMNTIFFBase := uint32(mnBlobOff) + uint32(info.mnTIFFHdrOff) //nolint:gosec // G115: mnBlobOff < len(finalTIFF)

	newPreviewAbsOff := info.previewBlock.newOffset
	if newPreviewAbsOff < newMNTIFFBase {
		return fmt.Errorf("%w: preview abs offset 0x%08X < MakerNote TIFF base 0x%08X",
			ErrNikonPatchFailed, newPreviewAbsOff, newMNTIFFBase)
	}
	newPreviewRelOff := newPreviewAbsOff - newMNTIFFBase

	// Patch 0x0201 and 0x0202 in the finalTIFF blob.
	pos201 := mnBlobOff + info.previewOff201InBlob
	pos202 := mnBlobOff + info.previewLen202InBlob

	if pos201+4 > len(finalTIFF) || pos202+4 > len(finalTIFF) {
		return fmt.Errorf("%w: patch positions out of finalTIFF bounds (pos201=%d pos202=%d len=%d)",
			ErrNikonPatchFailed, pos201, pos202, len(finalTIFF))
	}

	info.mnOrder.PutUint32(finalTIFF[pos201:], newPreviewRelOff)
	info.mnOrder.PutUint32(finalTIFF[pos202:], info.previewImageSize)
	return nil
}

// scanIFDForTagValOrOff scans the IFD at ifdStart in buf for the first entry with
// the given tag and returns the uint32 in its val-or-off field.
func scanIFDForTagValOrOff(buf []byte, ifdStart int, tag uint16, order binary.ByteOrder) (uint32, bool) {
	if ifdStart+2 > len(buf) {
		return 0, false
	}
	count := int(order.Uint16(buf[ifdStart:]))
	pos := ifdStart + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(buf) {
			break
		}
		if order.Uint16(buf[e:]) == tag {
			return order.Uint32(buf[e+8:]), true
		}
	}
	return 0, false
}

// findOOLEntryOffset scans the IFD at ifdStart in buf for the first entry with the
// given tag whose value is out-of-line (total size > 4).  Returns the blob-absolute
// offset (the value stored in the val-or-off field) and true when found.
// Returns (0, false) for inline values or missing tags.
func findOOLEntryOffset(buf []byte, ifdStart int, tag uint16, order binary.ByteOrder) (int, bool) {
	if ifdStart+2 > len(buf) {
		return 0, false
	}
	count := int(order.Uint16(buf[ifdStart:]))
	pos := ifdStart + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(buf) {
			break
		}
		if order.Uint16(buf[e:]) != tag {
			continue
		}
		entryType := order.Uint16(buf[e+2:])
		entryCount := order.Uint32(buf[e+4:])
		sz := typeSize(entryType)
		if sz == 0 {
			return 0, false
		}
		total := uint64(sz) * uint64(entryCount)
		if total <= 4 {
			return 0, false // inline — not what we want
		}
		blobOff := int(order.Uint32(buf[e+8:]))
		if blobOff < 0 || blobOff >= len(buf) {
			return 0, false
		}
		return blobOff, true
	}
	return 0, false
}

// relocateTIFFFromParsedNEF is the NEF-specific entry point for the TIFF
// copy-and-relocate serializer.
//
// It runs Nikon-specific preprocessing (Steps A+B) to extend the MakerNote blob
// and enumerate the PreviewIFD image block, then runs the modified relocation
// algorithm that includes Step C (post-encode patching of the MakerNote-relative
// preview offset).
//
// When no Nikon Type-3 MakerNote with a PreviewIFD is detected, it falls back
// to the standard relocateTIFFFromParsed path.
func relocateTIFFFromParsedNEF(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	if e == nil {
		var parseErr error
		e, parseErr = exif.Parse(base)
		if parseErr != nil {
			return nil, fmt.Errorf("nef: parse for relocation: %w", parseErr)
		}
	}

	order := e.ByteOrder
	if order == nil {
		order = binary.BigEndian // NEF files use big-endian (MM magic)
	}

	// Steps A+B: extend MakerNote blob and register PreviewIFD image block.
	info, err := extractNikonPreviewInfo(base, e)
	if err != nil {
		return nil, fmt.Errorf("nef: extract Nikon preview info: %w", err)
	}

	if info == nil {
		// No Nikon-specific preprocessing needed; use the standard path.
		return relocateTIFFFromParsed(base, e, rawIPTC, rawXMP)
	}

	// Step C (and the rest of the main algorithm) is handled here.
	return nefRelocateWithPreview(base, e, rawIPTC, rawXMP, info, order)
}

// nefRelocateWithPreview runs the TIFF copy-and-relocate algorithm with the
// Nikon PreviewIFD image block injected.
//
// This is a modified variant of relocateTIFFFromParsed that:
//  1. Appends info.previewBlock to the main image-blocks list.
//  2. Calls patchNikonPreviewInFinalTIFF after step 9 (re-encode).
//
// All other steps are identical to relocateTIFFFromParsed.
func nefRelocateWithPreview( //nolint:cyclop,gocyclo,funlen // mirrors relocateTIFFFromParsed with Nikon additions; splitting reduces clarity
	base []byte,
	e *exif.EXIF,
	rawIPTC, rawXMP []byte,
	info *nikonPreviewInfo,
	order binary.ByteOrder,
) ([]byte, error) {
	// Step 2: upsert metadata tags in IFD0.
	if e.IFD0 == nil {
		e.IFD0 = &exif.IFD{}
	}
	if rawIPTC != nil {
		// Adobe XMP Spec / ExifTool convention: IPTC-NAA (0x83BB) as TypeLong.
		// upsertIFD0Entry pads value to 4-byte boundary; Count = nLongs.
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeLong, rawIPTC)
	}
	if rawXMP != nil {
		// Adobe XMP Spec (TIFF Technical Note 3): XMP (0x02BC) as TypeByte.
		upsertIFD0Entry(e.IFD0, exif.TagXMP, exif.TypeByte, rawXMP)
	}

	// Step 3: enumerate image blocks from the main IFD chain.
	mainIFDBlocks, err := enumerateImageBlocks(base, e, order)
	if err != nil {
		return nil, fmt.Errorf("nef: enumerate image blocks: %w", err)
	}

	// Inject the Nikon PreviewIFD image block into the blocks list.
	// It is placed BEFORE the SubIFD blocks so that assignNewOffsets lays
	// the preview data out in the same relative order as the original file
	// (preview → full-res JPEG in SubIFD[0] → raw in SubIFD[1]).
	allBlocks := append(mainIFDBlocks, info.previewBlock)

	// Step 4: parse SubIFDs (tag 0x014A).
	subIFDs, subBlocks, subErr := enumerateSubIFDs(base, e.IFD0, order)
	if subErr != nil {
		return nil, fmt.Errorf("nef: enumerate SubIFDs: %w", subErr)
	}
	allBlocks = append(allBlocks, subBlocks...)

	// Step 5: remove stale image-data offset entries from main IFDs only.
	// filterMainBlocks returns blocks not owned by a SubIFD parsed IFD.
	// filterNonNilIFDBlocks further excludes the preview block (ifdPtr=nil),
	// which has no corresponding exif.IFD entry to remove.
	mainBlocks := filterMainBlocks(allBlocks, subIFDs)
	mainBlocks = filterNonNilIFDBlocks(mainBlocks)
	removeImageOffsetEntries(mainBlocks)

	// Step 6: re-insert placeholder entries and encode to learn the structure size.
	offsetValueSlices := insertPlaceholders(mainBlocks, order)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("nef: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint32(len(skeleton)) //nolint:gosec // G115: len bounded by TIFF stream

	// Step 7: assign new absolute offsets.
	subIFDsSize := computeSubIFDsSize(subIFDs)
	imageStart := ifdEnd + subIFDsSize
	assignNewOffsets(allBlocks, imageStart)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	if info.previewBlock.newOffset == math.MaxUint32 {
		return nil, fmt.Errorf("%w", ErrNikonPreviewOverflow)
	}

	// Step 8a: update placeholder value bytes (main-IFD blocks).
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// Step 8b: patch SubIFD raw bytes.
	patchSubIFDImageOffsets(subIFDs, allBlocks, order)

	// Step 9: re-encode → finalTIFF.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("nef: encode final: %w", finalErr)
	}

	// Step 9.5 (Nikon-specific): patch PreviewIFD 0x0201/0x0202 in the MakerNote
	// blob now embedded in finalTIFF.
	if pErr := patchNikonPreviewInFinalTIFF(finalTIFF, info, order); pErr != nil {
		return nil, fmt.Errorf("nef: patch Nikon preview offset in finalTIFF: %w", pErr)
	}

	// Step 10: patch 0x014A SubIFDs pointer array.
	if len(subIFDs) > 0 {
		if pErr := patchSubIFDPointers(finalTIFF, subIFDs, order); pErr != nil {
			return nil, fmt.Errorf("nef: patch SubIFD pointers: %w", pErr)
		}
	}

	// Step 11: append SubIFD raw bytes.
	// TIFF 6.0 §2: each SubIFD block must start at a word (even) boundary.
	// assignSubIFDOffsets already reserved space for the 0x00 pad byte;
	// insert it here to keep finalTIFF and the assigned offsets in sync.
	for _, si := range subIFDs {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		finalTIFF = append(finalTIFF, si.rawBytes...)
	}

	// Step 12: append image block bytes from source.
	for _, blk := range allBlocks {
		end := uint64(blk.srcOffset) + uint64(blk.size)
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("nef: image block offset=%d size=%d: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds)
		}
		finalTIFF = append(finalTIFF, base[blk.srcOffset:end]...)
	}

	return finalTIFF, nil
}

// filterNonNilIFDBlocks returns a copy of blocks that excludes entries with a nil
// ifdPtr.  Used to exclude the Nikon preview block from removeImageOffsetEntries.
func filterNonNilIFDBlocks(blocks []*imageBlock) []*imageBlock {
	out := make([]*imageBlock, 0, len(blocks))
	for _, blk := range blocks {
		if blk.ifdPtr != nil {
			out = append(out, blk)
		}
	}
	return out
}
