package tiff

// relocate_makernote.go — generic MakerNote OOL-offset rebasing for the standard
// TIFF relocation path (relocateTIFFFromParsed).
//
// Problem (#127): manufacturers that store MakerNote OOL val_or_off fields as
// TIFF-file-absolute offsets produce stale pointers after the MakerNote blob
// moves to a new position during TIFF re-encoding.
//
// Two maker families require post-encode rebasing in the standard TIFF path:
//
//  1. Sony (plain-IFD MakerNotes — DSLR-A series, ILCE, SLT, Cybershot):
//     A plain TIFF IFD at offset 0 within the blob; all OOL val_or_off fields
//     are outer-TIFF-absolute (ExifTool Sony.pm).
//     Note: Sony ARW files have a dedicated path (relocate_arw.go +
//     rebaseSonyMakerNote) that handles both the MakerNote and the SR2Private
//     block.  The function here provides equivalent rebasing when Sony-made files
//     are read as standard TIFF (e.g. embedded preview TIFFs in JPEG APP1).
//
//  2. Olympus "OLYMP\x00" (older compact cameras — C5050Z, C8080, SP350, SP500UZ):
//     "OLYMP\x00" prefix + 2-byte version (8-byte header), then a plain IFD.
//     All OOL val_or_off fields are outer-TIFF-absolute (ExifTool Olympus.pm).
//     Newer Olympus cameras use the "OLYMPUS\x00" format with blob-relative
//     offsets; those are safe to copy verbatim and are NOT handled here.
//
// Makers that are already correct and need NO rebasing:
//
//   - Canon:              plain IFD, blob-relative (offset 0 within blob)
//   - Panasonic:          "Panasonic\0\0\0" prefix, IFD at 12, blob-relative
//   - Olympus OLYMPUS\x00: newer format, blob-relative
//   - Nikon Type-3:       embedded TIFF header — internal offsets are relative to
//                          the embedded TIFF base at blob[tiffHdrOff], which moves
//                          with the blob, so they remain self-consistent.
//   - Pentax AOC / PENTAX: blob-relative
//
// Documented limitation — NOT rebased:
//
//   - Nikon Type-1 (legacy D1, plain IFD, big-endian): outer-TIFF-absolute offsets.
//     Rebasing is NOT implemented because: (a) Type-1 cameras are extremely rare,
//     (b) empirical analysis of real Type-1 files (ExifTool Nikon.pm) shows no
//     known OOL sub-structures in practice, and (c) the format is no longer
//     produced by any current or recent camera. Callers writing Nikon D1 files
//     should be aware that MakerNote OOL values may be unreadable after write.
//     This limitation does NOT corrupt image data or any other metadata.
//
// Algorithm (rebaseGenericMakerNote):
//
//  1. Detect whether the MakerNote blob is Sony-plain-IFD or Olympus-OLYMP-type.
//  2. Locate the MakerNote blob in finalTIFF via OOL entry scan of ExifIFD.
//  3. Compute delta = new_mn_abs − original_mn_abs (EXIF.MakerNoteOffset).
//  4. Walk the MakerNote IFD; for each OOL entry (total > 4 bytes):
//     a. Validate that old_voo falls within the original TIFF base space
//        (at least >= original_mn_abs; upper bound is the TIFF file length,
//        NOT the declared blob size — some Sony/Olympus models have OOL data
//        that was captured verbatim by exif.Parse but lies just past the declared
//        count; see #127 upper-bound-skip fix for rebaseSonyMakerNote).
//     b. new_voo = old_voo + delta
//     c. Overwrite the val_or_off field in finalTIFF at the entry position.
//
// Spec references:
//   - EXIF §4.6.5, tag 0x927C (MakerNote).
//   - ExifTool Sony.pm: Sony MakerNote OOL offset convention.
//   - ExifTool Olympus.pm: OLYMP-type MakerNote OOL offset convention.
//   - TIFF 6.0 §2: IFD entry layout — val_or_off holds absolute offset when
//     total byte size > 4 bytes.
//   - #127 audit finding: MakerNote OOL offset rebasing incomplete on write.

import (
	"bytes"
	"encoding/binary"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// olympMNPrefix is the 6-byte prefix that identifies an older Olympus OLYMP-type
// MakerNote (cameras such as C5050Z, C8080, SP350, SP500UZ).
//
// ExifTool Olympus.pm: OlympusMakerNote detection — "OLYMP\x00" prefix (6 bytes)
// followed by a 2-byte version, then the IFD beginning at offset 8 within the blob.
// All OOL val_or_off fields carry TIFF-file-absolute offsets.
//
// Distinguish from "OLYMPUS\x00" (8-byte prefix, IFD at offset 12, blob-relative).
var olympMNPrefix = [6]byte{'O', 'L', 'Y', 'M', 'P', 0x00} //nolint:gochecknoglobals // package-level constant; never mutated

// isOlympTypeMakerNote returns true when blob begins with "OLYMP\x00" (6 bytes),
// which identifies the older Olympus OLYMP-type format that uses TIFF-absolute offsets.
//
// ExifTool Olympus.pm: "OLYMP\x00" magic (6 bytes) + 2-byte version; IFD at +8.
// This is distinct from "OLYMPUS\x00" (newer format, blob-relative, move-safe).
func isOlympTypeMakerNote(blob []byte) bool {
	return len(blob) >= 8 &&
		blob[0] == olympMNPrefix[0] &&
		blob[1] == olympMNPrefix[1] &&
		blob[2] == olympMNPrefix[2] &&
		blob[3] == olympMNPrefix[3] &&
		blob[4] == olympMNPrefix[4] &&
		blob[5] == olympMNPrefix[5]
}

// isSonyPlainIFDMakerNote returns true when blob is a Sony plain-IFD MakerNote.
//
// Sony MakerNote (DSLR-A / ILCE / SLT / Cybershot) is a plain TIFF IFD at offset 0,
// with NO magic prefix. All OOL val_or_off fields are TIFF-file-absolute.
// (ExifTool Sony.pm: parseMakerNote — no magic header; plain IFD at offset 0.)
//
// Detection heuristic: no recognised prefix (not "Nikon\x00", "OLYMP\x00",
// "OLYMPUS\x00", "Panasonic\x00\x00\x00", "FUJIFILM", "PENTAX \x00", "AOC\x00",
// "QVC\x00", "SIGMA\x00", "FOVEON\x00", "LEICA\x00"); starts with a plausible
// IFD entry count (> 0 and < 4096); and the caller has confirmed Make = "SONY".
//
// Note: Sony detection in rebaseGenericMakerNote is done by checking that the blob
// does NOT have any of the known prefixes that indicate a different maker format.
// This is safe because if a blob lacks all known prefixes it is either Sony (plain
// IFD) or an unknown format — both should be treated the same way for rebasing:
// attempt to scan as a plain IFD and only rebase entries whose old_voo is within
// the plausible TIFF range.
func isSonyPlainIFDMakerNote(blob []byte, order binary.ByteOrder) bool {
	if len(blob) < 2 {
		return false
	}
	// Must NOT start with any of the known magic prefixes.
	known := [][]byte{
		[]byte("Nikon\x00"),
		[]byte("OLYMP\x00"),     // OLYMP-type (handled separately)
		[]byte("OLYMPUS\x00"),   // newer Olympus (blob-relative, safe)
		[]byte("OM SYSTEM\x00"), // OM SYSTEM cameras (blob-relative, safe)
		[]byte("Panasonic\x00"),
		[]byte("FUJIFILM"),
		[]byte("PENTAX \x00"),
		[]byte("AOC\x00"),
		[]byte("QVC\x00"),
		[]byte("SIGMA\x00"),
		[]byte("FOVEON\x00"),
		[]byte("LEICA\x00"),
	}
	for _, prefix := range known {
		if bytes.HasPrefix(blob, prefix) {
			return false
		}
	}
	// Plausible IFD entry count: > 0 and < 4096 to filter noise.
	count := order.Uint16(blob)
	return count > 0 && count < 4096
}

// makerNoteIFDOffset returns the byte offset WITHIN the MakerNote blob at which
// the IFD data begins, and the byte order to use for parsing.
//
// For OLYMP-type blobs the IFD is at offset 8 (after the 8-byte header).
// For Sony plain-IFD blobs the IFD is at offset 0 (no header).
// Returns (0, nil) when the blob format is not handled by this function.
//
// ExifTool Olympus.pm: OLYMP-type IFD at blob[8].
// ExifTool Sony.pm: plain IFD at blob[0].
func makerNoteIFDOffset(blob []byte, parentOrder binary.ByteOrder) (ifdOff int, order binary.ByteOrder) {
	if isOlympTypeMakerNote(blob) {
		// OLYMP-type: IFD starts at byte 8 within the blob.
		// Byte order is inherited from the outer TIFF (Olympus compacts are LE).
		return 8, parentOrder
	}
	if isSonyPlainIFDMakerNote(blob, parentOrder) {
		// Sony: plain IFD at byte 0, same byte order as the outer TIFF.
		return 0, parentOrder
	}
	return 0, nil
}

// rebaseGenericMakerNote rebases all OOL val_or_off fields in a Sony or Olympus
// OLYMP-type MakerNote that has moved to a new absolute position in finalTIFF.
//
// This function is called from relocateTIFFFromParsed (step 9.5) to handle makers
// that use TIFF-file-absolute OOL offsets. It is a companion to rebaseSonyMakerNote
// (in relocate_arw.go, which handles ARW-specific Sony rebasing) and
// rebaseOlympMakerNote (in relocate_orf.go, which handles ORF-specific rebasing).
//
// Parameters:
//   - finalTIFF: the re-encoded TIFF output (produced by exif.Encode in step 9).
//   - e: the original parsed EXIF struct (provides MakerNoteOffset and MakerNote blob).
//   - order: the outer TIFF byte order.
//
// The function is a no-op when:
//   - e.MakerNote is nil or empty.
//   - The MakerNote format is not Sony plain-IFD or Olympus OLYMP-type.
//   - The blob did not move (new position == MakerNoteOffset).
//   - The MakerNote OOL entry cannot be found in the re-encoded ExifIFD.
//
// TIFF 6.0 §2: val_or_off holds an absolute file offset when total byte size > 4.
// ExifTool Sony.pm, Olympus.pm: both formats store outer-TIFF-absolute offsets.
// #127: fix for MakerNote OOL offset rebasing incomplete on write.
//
//nolint:cyclop,gocyclo // IFD scan with inline/OOL discrimination is inherently branchy; complexity matches rebaseSonyMakerNote
func rebaseGenericMakerNote(finalTIFF []byte, e *exif.EXIF, order binary.ByteOrder) {
	if e == nil || len(e.MakerNote) == 0 || e.ExifIFD == nil {
		return
	}
	if len(finalTIFF) < 8 {
		return
	}

	// Determine IFD offset within blob and byte order for this maker.
	ifdOff, mnOrder := makerNoteIFDOffset(e.MakerNote, order)
	if mnOrder == nil {
		// Not a maker format that requires rebasing by this function.
		return
	}

	// ── Locate the ExifIFD in finalTIFF ──────────────────────────────────────
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return
	}
	exifIFDOff, found := scanIFDForTagValOrOff(finalTIFF, ifd0Off, uint16(exif.TagExifIFDPointer), order)
	if !found {
		return // no ExifIFD pointer; skip
	}
	exifStart := int(exifIFDOff)
	if exifStart+2 > len(finalTIFF) {
		return
	}

	// ── Locate MakerNote blob in finalTIFF ───────────────────────────────────
	// findOOLEntryOffset returns the absolute position of the MakerNote blob
	// within finalTIFF (i.e., the value stored in the val_or_off field of the
	// MakerNote entry in ExifIFD — an absolute TIFF stream offset).
	newMNAbs, found := findOOLEntryOffset(finalTIFF, exifStart, uint16(exif.TagMakerNote), order)
	if !found {
		// MakerNote inline or absent; nothing to rebase.
		return
	}

	// ── Compute rebasing delta ────────────────────────────────────────────────
	// oldMNAbs is the original absolute file position of the MakerNote blob.
	// EXIF.MakerNoteOffset is set by exif.Parse (ifd.go rawOffset) when the
	// MakerNote entry is OOL (count > 4).
	oldMNAbs := e.MakerNoteOffset
	if oldMNAbs == 0 {
		// Fallback: search base for the blob (rare, defensive).
		// For this function we use the entry from e.MakerNote as fingerprint.
		// If it cannot be found, skip rebasing (better than corrupting offsets).
		return
	}
	if uint32(newMNAbs) == oldMNAbs { //nolint:gosec // G115: newMNAbs < len(finalTIFF) < 2^32
		// Blob did not move; no rebasing needed.
		return
	}

	// delta is the signed displacement of the MakerNote blob in the new stream.
	// For OLYMP-type and Sony plain-IFD, all OOL val_or_off values are absolute;
	// after the blob moves by delta, each pointer shifts by the same amount.
	//
	// #127: use int64 arithmetic for delta to handle both positive and negative
	// displacement without uint32 wraparound (the new position may be either
	// before or after the original in pathological cases, though in practice
	// TIFF re-encoding always grows the header).
	delta := int64(newMNAbs) - int64(oldMNAbs)

	// ── Walk the MakerNote IFD and rebase OOL entries ────────────────────────
	// The MakerNote IFD is at finalTIFF[newMNAbs + ifdOff].
	// For OLYMP-type:    ifdOff = 8  (skip "OLYMP\x00" header + 2-byte version)
	// For Sony plain-IFD: ifdOff = 0
	ifdStart := newMNAbs + ifdOff
	if ifdStart+2 > len(finalTIFF) {
		return
	}
	count := int(mnOrder.Uint16(finalTIFF[ifdStart:]))
	pos := ifdStart + 2

	for i := range count {
		ep := pos + i*12
		if ep+12 > len(finalTIFF) {
			break
		}
		entryType := mnOrder.Uint16(finalTIFF[ep+2:])
		entryCount := mnOrder.Uint32(finalTIFF[ep+4:])
		sz := typeSize(entryType)
		if sz == 0 || entryCount == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCount)
		if total <= 4 {
			continue // inline value; no pointer to rebase
		}

		// OOL entry: val_or_off is a TIFF-absolute offset.
		oldVOO := mnOrder.Uint32(finalTIFF[ep+8:])

		// Safety: only rebase if the old pointer was within the plausible range
		// of the original TIFF. Specifically, it must be >= oldMNAbs (the blob
		// could not have pointed to something before its own start) and fit within
		// the declared val_or_off space.
		//
		// #127 upper-bound fix: the upper bound is intentionally NOT clamped to
		// oldMNAbs + len(e.MakerNote). Some Sony and Olympus OLYMP-type cameras
		// store OOL value data that extends slightly past the declared MakerNote
		// blob count in the outer IFD entry, yet those bytes are faithfully copied
		// by exif.Parse into the Value slice (it reads the full extent). Clamping
		// to the declared blob size causes those entries to be silently skipped
		// (the original bug in rebaseSonyMakerNote lines 728-729 for extra-OOL
		// blocks). We relax the upper bound to len(finalTIFF) — any TIFF-absolute
		// offset within the original TIFF is a valid OOL pointer for this maker.
		//
		// ExifTool Sony.pm: all Sony MakerNote OOL data is contiguous with or
		// immediately following the IFD blob.
		if uint64(oldVOO) < uint64(oldMNAbs) {
			// Pointer precedes the MakerNote blob start — either a stale/zero
			// entry or a known-bad file. Skip to avoid corrupting unrelated data.
			continue
		}

		// Compute new absolute offset by applying the delta.
		newVOO64 := int64(oldVOO) + delta
		if newVOO64 < 0 || uint64(newVOO64)+total > uint64(len(finalTIFF)) {
			// New pointer would be out of bounds; skip (defensive guard).
			continue
		}
		mnOrder.PutUint32(finalTIFF[ep+8:], uint32(newVOO64)) //nolint:gosec // G115: newVOO64 > 0 and fits in uint32
	}
}
