package tiff

// relocate_orf.go — Olympus ORF-specific copy-and-relocate entry point (task #104).
//
// Problem: Olympus ORF files use non-standard TIFF magic bytes at positions [2:4]:
//   - IIRO (bytes 0-3: 0x49 0x49 0x52 0x4F) — used by Olympus DSLRs (E-series, OM-D)
//   - IIRS (bytes 0-3: 0x49 0x49 0x52 0x53) — used by older Olympus compacts
//     (C5050Z, C8080, SP350, SP500UZ)
//
// exif.Parse rejects both because it requires magic == 0x002A (TIFF 6.0 §2).
// The IFD0 offset and all internal structure are otherwise standard TIFF-compatible:
// IFD0 is at the standard offset stored in bytes [4:8].
//
// Additionally, the older Olympus compact cameras (C5050Z and similar) embed an
// "OLYMP"-format MakerNote (ExifIFD tag 0x927C) whose out-of-line IFD entry
// val_or_off fields carry TIFF-file-ABSOLUTE offsets (not offsets relative to the
// MakerNote blob start).  When the MakerNote blob is moved to a new position by
// exif.Encode, those internal pointers become stale.  Additionally, the MakerNote
// ThumbnailImage (tag 0x0100) points to a JPEG block that resides OUTSIDE the
// MakerNote blob itself; this external block must be registered as an imageBlock
// so it is not silently dropped during relocation.
//
// Newer Olympus / OM SYSTEM cameras use the "OLYMPUS\0" or "OM SYSTEM\0" MakerNote
// format where internal offsets are relative to the start of the MakerNote blob;
// those are safe to copy verbatim (no rebasing needed).
//
// Fix (task #104):
//
//  1. InjectWithEXIFORF patches bytes [2:4] to 0x2A 0x00 in a working copy of
//     the original bytes, delegates to relocateTIFFFromParsedORF, and restores
//     the caller-supplied ORF magic in the output bytes [0:4].
//
//  2. relocateTIFFFromParsedORF detects the MakerNote format (OLYMP vs OLYMPUS):
//     - For OLYMP-type (older Olympus compacts): extracts the MakerNote info,
//       registers external MakerNote image blocks, runs the standard relocator,
//       and rebases the MakerNote OOL pointers in the output.
//     - For all other formats: delegates directly to relocateTIFFFromParsed
//       (the verbatim-blob-copy path is safe for blob-relative MakerNotes).
//
// Spec references:
//   - TIFF 6.0 §2: TIFF header layout.
//   - ExifTool Olympus.pm: ORF magic bytes, OLYMP-type MakerNote header (6-byte
//     "OLYMP\x00" prefix, IFD at offset +8), OLYMPUS-type header, offset base rules.
//   - Empirical analysis of Olympus C5050Z ORF (task #104).
//   - task #104: ORF write un-gating.

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Sentinel errors for the ORF-specific relocation subsystem.
var (
	// ErrORFInvalidMagic is returned when the base bytes do not carry a
	// recognised Olympus ORF magic at bytes [0:4].
	ErrORFInvalidMagic = errors.New("tiff: ORF invalid magic bytes (expected IIRO or IIRS)")

	// ErrORFOutputTooShort is returned when the assembled ORF output is shorter
	// than the minimum 4 bytes needed to restore the ORF magic.
	ErrORFOutputTooShort = errors.New("tiff: ORF output too short to restore magic bytes")
)

// olympMakerNoteHeader is the 6-byte header prefix for the older Olympus OLYMP-type
// MakerNote.  Cameras using this format store TIFF-file-absolute offsets in all
// out-of-line IFD entry val_or_off fields.
//
// ExifTool Olympus.pm: MakerNote starts with b"OLYMP\x00" followed by a 2-byte
// version word (\x01\x00), then the IFD begins at offset +8 within the MakerNote
// blob.  All val_or_off fields in the MakerNote IFD reference positions within the
// enclosing TIFF file (not relative to the blob start).
//
// Models observed: C5050Z, C8080, SP350, SP500UZ.
const olympMNHeaderLen = 8 // "OLYMP\x00" (6) + version (2)

// olympMNHeaderPrefix is the fixed prefix of an OLYMP-type MakerNote.
var olympMNHeaderPrefix = [6]byte{'O', 'L', 'Y', 'M', 'P', 0x00} //nolint:gochecknoglobals // package-level constant array; never mutated

// olympMNTagThumbnailImage is the tag used by older Olympus cameras to embed
// a JPEG thumbnail in the MakerNote IFD.  Unlike the standard IFD1 thumbnail
// (tags 0x0201/0x0202), this thumbnail is stored directly as an Undefined-type
// value (count = JPEG byte length) at an absolute TIFF file offset.
//
// ExifTool Olympus.pm: MakerNote tag 0x0100 = ThumbnailImage.
const olympMNTagThumbnailImage = exif.TagID(0x0100)

// olympMakerNoteInfo holds the Olympus OLYMP-type MakerNote data extracted during
// the pre-relocation step.  It is analogous to sonySR2Info in relocate_arw.go.
type olympMakerNoteInfo struct {
	// mnEntry is the exif.IFDEntry for tag 0x927C in the ExifIFD.
	// Used to verify the MakerNote blob still occupies the same Value slice
	// after exif.Encode (so we can locate it in finalTIFF via MakerNote search).
	mnEntry *exif.IFDEntry

	// mnSrcOffset is the original absolute position of the MakerNote blob in base
	// (the full TIFF byte stream passed to the relocator).  All OLYMP-type OOL
	// val_or_off fields in the MakerNote IFD are TIFF-absolute; after re-encoding
	// they must be rebased by delta = new_mn_abs − mnSrcOffset.
	mnSrcOffset uint32

	// mnBlobSize is the byte length of the MakerNote blob (= mnEntry.Count).
	// Used for bounds checking during the rebase step.
	mnBlobSize uint32

	// thumbSrcOffset is the original absolute file offset of the MakerNote
	// ThumbnailImage JPEG data (= val_or_off of MakerNote IFD tag 0x0100).
	// It is 0 when the ThumbnailImage entry is absent or inline.
	thumbSrcOffset uint32

	// thumbSize is the byte length of the ThumbnailImage JPEG data
	// (= Count of MakerNote IFD tag 0x0100, which is TypeUndefined).
	// It is 0 when the ThumbnailImage entry is absent or inline.
	thumbSize uint32

	// order is the TIFF byte order (inherited from the outer EXIF).
	order binary.ByteOrder
}

// isORFMagic reports whether the 4-byte slice b carries a valid Olympus ORF magic.
// Accepts both IIRO (0x49 0x49 0x52 0x4F) and IIRS (0x49 0x49 0x52 0x53).
//
// ExifTool Olympus.pm: ORFMagic is "IIRO" or "IIRS".
func isORFMagic(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	// Bytes 0-1 are always "II" (little-endian byte-order marker).
	// Byte 2 is always 'R' (0x52).
	// Byte 3 is 'O' (0x4F) for IIRO or 'S' (0x53) for IIRS.
	return b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x52 &&
		(b[3] == 0x4F || b[3] == 0x53)
}

// isOLYMPMakerNote reports whether blob is an older Olympus OLYMP-type MakerNote.
//
// OLYMP-type MakerNotes begin with exactly "OLYMP\x00" (6 bytes).  Newer Olympus
// and OM SYSTEM cameras use "OLYMPUS\x00" (8 bytes) or "OM SYSTEM\x00" (10 bytes)
// with blob-relative offset bases — those do NOT need offset rebasing.
//
// ExifTool Olympus.pm: OlympusMakerNote detection logic.
func isOLYMPMakerNote(blob []byte) bool {
	if len(blob) < olympMNHeaderLen {
		return false
	}
	// Check 6-byte "OLYMP\x00" prefix.
	return blob[0] == olympMNHeaderPrefix[0] &&
		blob[1] == olympMNHeaderPrefix[1] &&
		blob[2] == olympMNHeaderPrefix[2] &&
		blob[3] == olympMNHeaderPrefix[3] &&
		blob[4] == olympMNHeaderPrefix[4] &&
		blob[5] == olympMNHeaderPrefix[5] &&
		// Additional check: byte 6 must be ≠ 'U' (0x55) to exclude "OLYMPUS\x00".
		// "OLYMPUS\x00" starts with 'O','L','Y','M','P','U','S','\x00' — byte 5 is 'U'
		// so the prefix check already excludes it (byte 5 = 'U' ≠ 0x00).
		// We keep the check explicit for clarity.
		blob[5] == 0x00
}

// extractOlympMakerNoteInfo inspects the parsed EXIF for an OLYMP-type MakerNote
// and returns an olympMakerNoteInfo when one is found.
//
// Returns nil (no error) when no OLYMP-type MakerNote is present — this is the
// normal case for newer Olympus / OM SYSTEM cameras which use the blob-relative
// "OLYMPUS\x00" format that is safe to copy verbatim.
//
// Returns a non-nil olympMakerNoteInfo when an OLYMP-type MakerNote is detected.
// The ThumbnailImage fields (thumbSrcOffset, thumbSize) are set only when the
// MakerNote IFD contains tag 0x0100 with a valid external OOL value.
//
//nolint:cyclop,gocyclo // OLYMP MakerNote detection and thumbnail scanning require multiple range/spec checks; complexity is inherent
func extractOlympMakerNoteInfo(base []byte, e *exif.EXIF, order binary.ByteOrder) *olympMakerNoteInfo {
	if e == nil || e.ExifIFD == nil {
		return nil
	}

	// Find the MakerNote entry (0x927C) in the ExifIFD.
	mnEntry := e.ExifIFD.Get(exif.TagMakerNote)
	if mnEntry == nil || len(mnEntry.Value) < olympMNHeaderLen {
		return nil
	}

	// Check if this is an OLYMP-type MakerNote (not "OLYMPUS\x00" or "OM SYSTEM\x00").
	if !isOLYMPMakerNote(mnEntry.Value) {
		return nil
	}

	// MakerNoteOffset is set by exif.Parse when the MakerNote is OOL (count > 4).
	// For any real MakerNote this is always OOL.
	mnSrcOffset := e.MakerNoteOffset
	if mnSrcOffset == 0 {
		// Fallback: search base for the blob by its first 8 bytes.
		mnSrcOffset = findBlobInBase(base, mnEntry.Value)
	}
	if mnSrcOffset == 0 {
		// Could not locate the blob in base; skip rebasing (safe: no rebasing =
		// stale offsets, same as before the fix, but at least no crash).
		return nil
	}

	info := &olympMakerNoteInfo{
		mnEntry:     mnEntry,
		mnSrcOffset: mnSrcOffset,
		mnBlobSize:  uint32(len(mnEntry.Value)), //nolint:gosec // G115: MakerNote blob size bounded by TIFF stream
		order:       order,
	}

	// Parse the MakerNote IFD to find the ThumbnailImage (tag 0x0100) entry.
	// The MakerNote IFD begins at mnSrcOffset+8 (after the 8-byte header).
	//
	// ExifTool Olympus.pm: OLYMP-type MakerNote IFD starts at blob[8].
	// The ThumbnailImage (0x0100) is TypeUndefined, Count=JPEG_byte_length, OOL.
	// Its val_or_off is a TIFF-file-absolute offset.  The JPEG data resides
	// OUTSIDE the MakerNote blob itself (at an earlier file offset in the original
	// C5050Z layout: [4096:15360]).
	ifdStart := mnSrcOffset + olympMNHeaderLen
	if uint64(ifdStart)+2 > uint64(len(base)) {
		return info // IFD is out of bounds; return info without thumbnail
	}
	mnCount := int(order.Uint16(base[ifdStart:]))
	pos := int(ifdStart) + 2
	for i := range mnCount {
		ep := pos + i*12
		if ep+12 > len(base) {
			break
		}
		tag := exif.TagID(order.Uint16(base[ep:]))
		if tag != olympMNTagThumbnailImage {
			continue
		}
		typ := order.Uint16(base[ep+2:])
		cnt := order.Uint32(base[ep+4:])
		if typ == 0 || cnt == 0 {
			break
		}
		sz := typeSize(typ)
		if sz == 0 {
			break
		}
		total := uint64(sz) * uint64(cnt)
		if total <= 4 {
			break // inline — no external data block
		}
		// OOL value: val_or_off is a TIFF-file-absolute offset to the JPEG bytes.
		thumbOff := order.Uint32(base[ep+8:])
		if thumbOff == 0 || uint64(thumbOff)+total > uint64(len(base)) {
			break
		}
		// Verify it is OUTSIDE the MakerNote blob (otherwise delta-rebase handles it).
		mnBlobEnd := uint64(mnSrcOffset) + uint64(info.mnBlobSize)
		if uint64(thumbOff) >= uint64(mnSrcOffset) && uint64(thumbOff) < mnBlobEnd {
			break // inside blob — regular OOL, no standalone block needed
		}
		info.thumbSrcOffset = thumbOff
		info.thumbSize = uint32(total) //nolint:gosec // G115: total bounded by len(base)
		break
	}

	return info
}

// rebaseOlympMakerNote rebases all OLYMP-type MakerNote OOL val_or_off entries in
// finalTIFF after the standard TIFF re-encoding step.
//
// Algorithm (analogous to rebaseSonyMakerNote in relocate_arw.go):
//
//  1. Locate the ExifIFD pointer (0x8769) in IFD0 of finalTIFF.
//  2. Locate the MakerNote blob offset within ExifIFD (tag 0x927C, OOL entry).
//  3. Compute delta = new_mn_abs − info.mnSrcOffset.
//  4. Walk the OLYMP MakerNote IFD (at new_mn_abs + olympMNHeaderLen) and
//     call rebaseOlympMNEntry for each entry.
//
// Must be called AFTER exif.Encode (step 9) and AFTER assignNewOffsets sets
// thumbBlock.newOffset.
//
//nolint:gocyclo // MakerNote IFD scan requires several range/bounds checks; complexity is inherent
func rebaseOlympMakerNote(
	finalTIFF []byte,
	info *olympMakerNoteInfo,
	thumbBlock *imageBlock, // may be nil if no external ThumbnailImage block
	order binary.ByteOrder,
) {
	if info == nil || info.mnEntry == nil || len(finalTIFF) < 8 {
		return
	}

	// Locate IFD0 → ExifIFD pointer (0x8769) → MakerNote blob in finalTIFF.
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return
	}

	exifIFDOff, found := scanIFDForTagValOrOff(finalTIFF, ifd0Off, uint16(exif.TagExifIFDPointer), order)
	if !found {
		return
	}
	exifStart := int(exifIFDOff)
	if exifStart+2 > len(finalTIFF) {
		return
	}

	// findOOLEntryOffset locates the MakerNote blob by scanning the ExifIFD for
	// the OOL val_or_off of tag 0x927C.
	newMNAbs, found := findOOLEntryOffset(finalTIFF, exifStart, uint16(exif.TagMakerNote), order)
	if !found {
		return
	}

	if uint32(newMNAbs) == info.mnSrcOffset { //nolint:gosec // G115: newMNAbs < len(finalTIFF) < 2^32
		return // blob did not move; nothing to rebase
	}

	mnBlobSize := int(info.mnBlobSize)
	if newMNAbs+mnBlobSize > len(finalTIFF) {
		return // out of bounds; skip (defensive)
	}

	// Walk the OLYMP MakerNote IFD.  The 8-byte header (OLYMP\x00 + version) is
	// at newMNAbs[0:8]; the IFD begins at newMNAbs+8.
	// ExifTool Olympus.pm: OLYMP-type IFD is at blob_start + 8.
	ifdInBlob := newMNAbs + olympMNHeaderLen
	if ifdInBlob+2 > len(finalTIFF) {
		return
	}
	mnCount := int(order.Uint16(finalTIFF[ifdInBlob:]))
	pos := ifdInBlob + 2
	mnBlobEnd := uint64(info.mnSrcOffset) + uint64(mnBlobSize) //nolint:gosec // G115: mnBlobSize bounded by MakerNote blob size

	for i := range mnCount {
		e := pos + i*12
		if e+12 > len(finalTIFF) {
			break
		}
		rebaseOlympMNEntry(finalTIFF, e, info, thumbBlock, mnBlobEnd, newMNAbs, order)
	}
}

// rebaseOlympMNEntry rebases a single OOL entry in the OLYMP-type MakerNote IFD.
//
// entryPos is the byte offset of the 12-byte IFD entry in finalTIFF.
// mnBlobEnd is the exclusive upper bound of the original MakerNote blob in the
// source file: info.mnSrcOffset + info.mnBlobSize.
// newMNAbs is the new absolute position of the MakerNote blob in finalTIFF.
//
// ExifTool Olympus.pm: MakerNote tag 0x0100 = ThumbnailImage (older Olympus compacts).
//
//nolint:cyclop,gocyclo // OLYMP entry rebase requires OOL/inline discrimination plus two cases; complexity is inherent
func rebaseOlympMNEntry(
	finalTIFF []byte,
	entryPos int,
	info *olympMakerNoteInfo,
	thumbBlock *imageBlock,
	mnBlobEnd uint64,
	newMNAbs int,
	order binary.ByteOrder,
) {
	entryTag := exif.TagID(order.Uint16(finalTIFF[entryPos:]))
	entryType := order.Uint16(finalTIFF[entryPos+2:])
	entryCount := order.Uint32(finalTIFF[entryPos+4:])

	sz := typeSize(entryType)
	if sz == 0 || entryCount == 0 {
		return
	}
	total := uint64(sz) * uint64(entryCount)
	if total <= 4 {
		return // inline entry; no pointer to rebase
	}

	// OOL entry: finalTIFF[entryPos+8:entryPos+12] is a TIFF-absolute file offset.
	oldVOO := order.Uint32(finalTIFF[entryPos+8:])

	// Case 1: ThumbnailImage (tag 0x0100) whose data is OUTSIDE the MakerNote blob.
	// Its new absolute offset is assigned by assignNewOffsets (thumbBlock.newOffset).
	if entryTag == olympMNTagThumbnailImage && thumbBlock != nil && info.thumbSrcOffset > 0 {
		if oldVOO == info.thumbSrcOffset ||
			uint64(oldVOO) < uint64(info.mnSrcOffset) ||
			uint64(oldVOO) >= mnBlobEnd {
			order.PutUint32(finalTIFF[entryPos+8:], uint32(thumbBlock.newOffset)) //nolint:gosec // G115: ORF is always classic TIFF; bounded by maxFileSize, always < 2^32
			return
		}
	}

	// Case 2: OOL entries whose data is WITHIN the original blob.
	// Rebase by delta: new_voo = old_voo + (new_mn_abs - old_mn_abs).
	// TIFF 6.0 §2: all offsets are measured from byte 0 of the TIFF stream.
	if uint64(oldVOO) >= uint64(info.mnSrcOffset) && uint64(oldVOO) < mnBlobEnd {
		newVOO := uint32(newMNAbs) + (oldVOO - info.mnSrcOffset) //nolint:gosec // G115: newMNAbs < len(finalTIFF) < 2^32
		order.PutUint32(finalTIFF[entryPos+8:], newVOO)
	}
	// Values outside the blob range are left unchanged (defensive guard).
}

// relocateTIFFAsORF is the internal ORF relocator entry point used by the tiff.go
// wrapper InjectWithEXIFORF.
//
// originalBytes must carry a valid ORF magic at bytes [0:4] (isORFMagic must
// return true). The caller (writeTIFFORF in write.go) is responsible for
// restoring the real ORF magic into the bytes before calling this function,
// since orf.Extract patches bytes [2:4] to 0x2A 0x00 when it returns rawEXIF.
//
// This separation (internal relocator + thin tiff.go wrapper) matches the
// InjectWithEXIFNEF / InjectWithEXIFARW pattern.
func relocateTIFFAsORF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	// Make a working copy so we can patch bytes [2:4] in-place without
	// mutating the caller's buffer (rawEXIF may be shared).
	workBytes := make([]byte, len(originalBytes))
	copy(workBytes, originalBytes)
	return relocateTIFFFromParsedORF(workBytes, modifiedEXIF, rawIPTC, rawXMP)
}

// relocateTIFFFromParsedORF patches the ORF magic, runs the TIFF copy-and-relocate
// algorithm (with Olympus OLYMP-type MakerNote rebasing when needed), and restores
// the original ORF magic in the output.
//
// base must carry a valid ORF magic at bytes [0:4] (isORFMagic must return true).
// base is mutated in-place (bytes [2:4] are patched); callers must pass a
// writable copy — InjectWithEXIFORF always does this.
//
// For newer Olympus cameras (OLYMPUS\x00 or OM SYSTEM MakerNote formats) the
// MakerNote uses blob-relative offsets and is safe to copy verbatim; the function
// delegates to the standard relocateTIFFFromParsed.
//
// For older Olympus compact cameras (OLYMP\x00 MakerNote format) the MakerNote
// uses TIFF-file-absolute offsets; the function uses the ORF-specific ARW-analogous
// path: it registers the external ThumbnailImage as a standalone imageBlock and
// rebases all MakerNote OOL pointers in the output.
func relocateTIFFFromParsedORF(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	if !isORFMagic(base) {
		return nil, fmt.Errorf("orf: %w", ErrORFInvalidMagic)
	}

	// Save the original 4-byte ORF magic for restoration in the output.
	var origMagic [4]byte
	copy(origMagic[:], base[0:4])

	// Patch bytes [2:4] to standard TIFF LE magic (0x2A 0x00).
	//
	// TIFF 6.0 §2: magic must be 0x002A for classic TIFF. exif.Parse requires
	// this. The IFD0 offset at bytes [4:8] is already a valid standard TIFF offset.
	base[2] = 0x2A
	base[3] = 0x00

	// Parse the magic-patched base when no pre-parsed struct is provided.
	if e == nil {
		var parseErr error
		e, parseErr = exif.Parse(base)
		if parseErr != nil {
			return nil, fmt.Errorf("orf: parse for relocation: %w", parseErr)
		}
	}

	order := e.ByteOrder
	if order == nil {
		order = binary.LittleEndian // ORF is always little-endian
	}

	// Detect the MakerNote format to choose the right relocation path.
	//
	// extractOlympMakerNoteInfo returns nil for newer OLYMPUS\x00 / OM SYSTEM\x00
	// cameras whose MakerNotes use blob-relative offsets (safe to copy verbatim).
	// It returns non-nil only for the older OLYMP\x00 format with file-absolute offsets.
	mnInfo := extractOlympMakerNoteInfo(base, e, order)
	if mnInfo == nil {
		// Newer Olympus or no MakerNote: standard path (verbatim blob copy is safe).
		finalTIFF, err := relocateTIFFFromParsed(base, e, rawIPTC, rawXMP)
		if err != nil {
			return nil, fmt.Errorf("orf: relocate: %w", err)
		}
		if len(finalTIFF) < 4 {
			return nil, fmt.Errorf("orf: %w (%d bytes)", ErrORFOutputTooShort, len(finalTIFF))
		}
		copy(finalTIFF[0:4], origMagic[:])
		return finalTIFF, nil
	}

	// OLYMP-type MakerNote path: run the ARW-analogous relocation.
	finalTIFF, err := orfRelocateWithOLYMP(base, e, rawIPTC, rawXMP, mnInfo, order)
	if err != nil {
		return nil, fmt.Errorf("orf: %w", err)
	}

	// Restore the original ORF magic in the output bytes [0:4].
	//
	// exif.Encode produces a standard TIFF header: "II" + 0x2A 0x00 + IFD0_off.
	// Replace bytes [0:4] with the saved ORF magic (IIRO or IIRS) so the output
	// is recognised as a valid Olympus ORF by all ORF-aware tools and cameras.
	if len(finalTIFF) < 4 {
		return nil, fmt.Errorf("orf: %w (%d bytes)", ErrORFOutputTooShort, len(finalTIFF))
	}
	copy(finalTIFF[0:4], origMagic[:])

	return finalTIFF, nil
}

// orfRelocateWithOLYMP runs the TIFF copy-and-relocate algorithm with
// Olympus OLYMP-type MakerNote post-encode rebasing.
//
// This is the ORF analogue of arwRelocateWithSR2 (relocate_arw.go).
// The main differences from the standard relocateTIFFFromParsed path are:
//
//  1. The MakerNote ThumbnailImage (tag 0x0100 in the MakerNote IFD) points to a
//     JPEG block that lives OUTSIDE the MakerNote blob.  It must be registered as a
//     standalone imageBlock (ifdPtr=nil, like NEF preview) so that assignNewOffsets
//     places it in the output.
//
//  2. After step 9 (re-encode), rebaseOlympMakerNote is called to patch all
//     MakerNote OOL val_or_off fields: in-blob values are delta-rebased; the
//     external ThumbnailImage val_or_off is replaced with thumbBlock.newOffset.
//
// All other steps are identical to relocateTIFFFromParsed.
//
//nolint:cyclop,gocyclo,funlen // mirrors relocateTIFFFromParsed with Olympus OLYMP additions; splitting reduces clarity
func orfRelocateWithOLYMP(
	base []byte,
	e *exif.EXIF,
	rawIPTC, rawXMP []byte,
	mnInfo *olympMakerNoteInfo,
	order binary.ByteOrder,
) ([]byte, error) {
	// Step 2: upsert metadata tags in IFD0.
	if e.IFD0 == nil {
		e.IFD0 = &exif.IFD{}
	}
	if rawIPTC != nil {
		// Adobe XMP Spec / ExifTool convention: IPTC-NAA (0x83BB) as TypeLong.
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeLong, rawIPTC)
	}
	if rawXMP != nil {
		// Adobe XMP Spec (TIFF Technical Note 3): XMP (0x02BC) as TypeByte.
		upsertIFD0Entry(e.IFD0, exif.TagXMP, exif.TypeByte, rawXMP)
	}

	// Step 2.5: clear IFD0.ThumbnailData before block enumeration.
	//
	// exif.Parse sets IFD0.ThumbnailData when IFD0 contains both 0x0201
	// (JPEGInterchangeFormat) and 0x0202 (JPEGInterchangeFormatLength), because
	// extractJPEGThumbnail runs on every IFD, not just the IFD1 chain.
	// The Olympus C5050Z does NOT use this pattern for its main preview, but the
	// guard is applied unconditionally (consistent with ARW and NEF paths).
	e.IFD0.ThumbnailData = nil

	// Step 3: enumerate image blocks from the main IFD chain.
	// GM-W1: budget is shared with the SubIFD enumeration below so the
	// cumulative image-block + SubIFD count for this write is bounded.
	budget := newImageBlockBudget()
	blocks, err := enumerateImageBlocks(base, e, order, false, budget)
	if err != nil {
		return nil, fmt.Errorf("enumerate image blocks: %w", err)
	}

	// Step 3.5: register the MakerNote ThumbnailImage as a standalone imageBlock.
	//
	// The OLYMP-type MakerNote (older Olympus compacts, e.g. C5050Z) stores a JPEG
	// thumbnail at an absolute file offset referenced ONLY from the MakerNote IFD
	// tag 0x0100.  This block is NOT part of the standard TIFF strip/tile/JPEG
	// chain enumerated by enumerateImageBlocks.  Without explicit registration it
	// would be silently dropped during relocation (task #104 bug: output 14 KB smaller
	// than input — exactly the thumbnail size).
	//
	// The block is registered as a standalone imageBlock (ifdPtr=nil) following the
	// same pattern as the NEF preview block in relocate_nef.go and the RW2 raw
	// sensor block in relocate_rw2.go.
	//
	// ExifTool Olympus.pm: MakerNote tag 0x0100 = ThumbnailImage (OLYMP-type only).
	var thumbBlock *imageBlock
	if mnInfo.thumbSrcOffset > 0 && mnInfo.thumbSize > 0 {
		thumbBlock = &imageBlock{
			srcOffset: uint64(mnInfo.thumbSrcOffset),
			size:      uint64(mnInfo.thumbSize),
			ifdPtr:    nil, // standalone; not owned by any exif.IFD entry
			entryTag:  olympMNTagThumbnailImage,
			index:     0,
		}
	}

	// Step 4: enumerate SubIFDs (tag 0x014A).
	subIFDs, subBlocks, subErr := enumerateSubIFDs(base, e, order, budget)
	if subErr != nil {
		return nil, fmt.Errorf("enumerate SubIFDs: %w", subErr)
	}
	blocks = append(blocks, subBlocks...)

	// Step 5: remove stale image-data offset entries from main IFDs only.
	mainBlocks := filterMainBlocks(blocks, subIFDs)
	mainBlocks = filterNonNilIFDBlocks(mainBlocks) // exclude thumbBlock (ifdPtr=nil)
	removeImageOffsetEntries(mainBlocks)

	// Step 6: re-insert placeholder entries and encode to learn the IFD size.
	offsetValueSlices := insertPlaceholders(mainBlocks)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("encode placeholder: %w", skelErr)
	}
	ifdEnd := uint64(len(skeleton))

	// Step 7: assign new absolute offsets.
	subIFDsSize := computeSubIFDsSize(subIFDs)
	imageStart := ifdEnd + subIFDsSize
	// Include the thumbBlock in the allBlocks slice for assignNewOffsets.
	allBlocks := blocks
	if thumbBlock != nil {
		allBlocks = append(allBlocks, thumbBlock)
	}
	assignNewOffsets(allBlocks, imageStart)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	// Step 8a: update placeholder value bytes (main-IFD blocks).
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// Step 8b: patch SubIFD raw bytes.
	patchSubIFDImageOffsets(subIFDs, allBlocks, false, order)

	// Step 9: re-encode → finalTIFF.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("encode final: %w", finalErr)
	}

	// Step 9.5 (OLYMP-specific): rebase MakerNote OOL pointers in finalTIFF.
	//
	// Must be done AFTER exif.Encode (which places the MakerNote at its new
	// absolute position) and AFTER assignNewOffsets (which sets thumbBlock.newOffset).
	// Must be done BEFORE appending the image blocks so that finalTIFF length is
	// correct for bounds checks in rebaseOlympMakerNote.
	//
	// Failure to rebase produces exiftool warning:
	//   "[minor] Possibly incorrect maker notes offsets (fix by N?)"
	//   "[minor] Suspicious MakerNotes offset for ThumbnailImage"
	rebaseOlympMakerNote(finalTIFF, mnInfo, thumbBlock, order)

	// Step 10: patch the 0x014A SubIFDs pointer array in finalTIFF.
	if len(subIFDs) > 0 {
		if pErr := patchSubIFDPointers(finalTIFF, subIFDs, false, order); pErr != nil {
			return nil, fmt.Errorf("patch SubIFD pointers: %w", pErr)
		}
	}

	// Step 11: append SubIFD raw bytes.
	for _, si := range subIFDs {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		finalTIFF = append(finalTIFF, si.rawBytes...)
	}

	// Step 12: append all image block bytes from source (main IFD blocks + thumbBlock).
	for _, blk := range allBlocks {
		end := blk.srcOffset + blk.size
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("image block offset=%d size=%d: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds)
		}
		finalTIFF = append(finalTIFF, base[blk.srcOffset:end]...)
	}

	return finalTIFF, nil
}
