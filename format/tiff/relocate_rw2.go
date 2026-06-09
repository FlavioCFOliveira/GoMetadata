package tiff

// relocate_rw2.go — Panasonic RW2-specific copy-and-relocate serializer (task #104).
//
// Problem: Panasonic RW2 files have two non-standard features that require
// format-specific handling beyond the standard TIFF copy-and-relocate path:
//
// 1. Non-standard magic ("IIU\x00"):
//    exif.Parse requires magic == 0x002A (TIFF 6.0 §2).
//
// 2. 24-byte header with 16-byte device GUID at bytes [8:24]:
//    IFD0 is at offset 24 (stored in header bytes [4:8]).
//    exif.Encode always produces IFD0 at offset 8 (standard TIFF).
//    After standard relocation the GUID is lost and all absolute offsets
//    are off by 16.
//
// 3. RawDataOffset (tag 0x0118, TypeLong inline):
//    Stores the absolute file offset to the raw sensor data as an inline value.
//    The standard TIFF relocator does not treat this as an offset tag pair.
//    After relocation the inline value is stale; it must be patched post-encode.
//    The raw sensor data extends from this offset to the end of the file.
//
// 4. Sentinel StripOffsets (0x0111 = 0xFFFFFFFF):
//    RW2 stores StripOffsets = 0xFFFFFFFF as a sentinel meaning "raw data is
//    accessed via RawDataOffset (0x0118)".  The standard enumerator would attempt
//    to copy bytes from offset 0xFFFFFFFF; we must suppress this.
//
// 5. JpgFromRaw (tag 0x002E, TypeUndefined OOL):
//    An embedded preview JPEG.  exif.Parse populates entry.Value with the JPEG
//    bytes from base.  exif.Encode copies those bytes into the OOL area and sets
//    the val_or_off field to the new position within the skeleton.  After GUID
//    insertion (+16 shift), the OOL pointer is adjusted to the final RW2 position.
//    No standalone imageBlock registration is needed for 0x002E.
//
// Algorithm (relocateTIFFFromParsedRW2):
//
//   Step A — preprocessing:
//     A1. Save the 16-byte GUID from base[8:24].
//     A2. Patch bytes [2:4] to 0x2A 0x00 for exif.Parse.
//     A3. Remove the sentinel StripOffsets/StripByteCounts from the parsed IFD
//         so the standard enumerator skips the 0xFFFFFFFF sentinel value.
//     A4. Register the raw sensor data (0x0118 inline value → EOF) as a
//         standalone imageBlock (ifdPtr=nil, like NEF preview).
//
//   Steps 2-12 — run the standard relocateTIFFFromParsed variant (mirrors
//     nefRelocateWithPreview) with the raw sensor data standalone block injected.
//     JpgFromRaw (0x002E) is handled automatically by exif.Encode (its Value
//     bytes are preserved from exif.Parse; the new OOL offset is computed).
//
//   Step B — post-encode GUID insertion and offset rebasing:
//     B1. Patch the 0x0118 inline val_or_off in IFD0 of finalTIFF with
//         rawBlock.newOffset (the new absolute position of raw sensor data).
//     B2. Insert the 16-byte GUID at byte position 8, shifting IFD0 to offset 24.
//     B3. Update header bytes [4:8] to 24.
//     B4. Walk IFD0 and add +16 to every OOL val_or_off field (all absolute
//         offsets shifted by the 16-byte GUID insertion).
//     B5. Add +16 to the 0x0118 inline val_or_off (raw sensor data pointer).
//     B6. Restore RW2 magic "IIU\x00".
//
// Spec / reference:
//   - ExifTool Panasonic.pm: RW2 file structure, GUID, tag 0x002E, tag 0x0118.
//   - Empirical analysis of Panasonic DMC-GF1.rw2 (task #104).
//   - TIFF 6.0 §2: IFD entry layout.
//   - task #104: RW2 write un-gating.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// RW2-specific constants.
const (
	// rw2GUIDOffset is the byte position of the 16-byte device GUID in an RW2 file.
	// Bytes [0:8] are the standard TIFF header ("IIU\x00" + ifd0_off=24).
	// Bytes [8:24] are the device GUID.
	// IFD0 starts at byte 24.
	//
	// ExifTool Panasonic.pm: PanasonicRaw magic / GUID field at file offset 8.
	rw2GUIDOffset = 8

	// rw2GUIDLen is the byte length of the Panasonic device GUID.
	rw2GUIDLen = 16

	// rw2IFD0Offset is the IFD0 offset in a standard RW2 file (= GUID end).
	rw2IFD0Offset = 24 // rw2GUIDOffset + rw2GUIDLen

	// rw2TagRawDataOffset is the RW2 IFD0 tag that holds the absolute file
	// offset of the raw sensor data as an inline TypeLong value.
	// ExifTool Panasonic.pm: tag 0x0118, "RawDataOffset".
	rw2TagRawDataOffset = exif.TagID(0x0118)
)

// rw2MagicBytes is the Panasonic RW2 magic: "IIU\x00" (0x49 0x49 0x55 0x00).
var rw2MagicBytes = [4]byte{0x49, 0x49, 0x55, 0x00} //nolint:gochecknoglobals // package-level constant array; never mutated

// Sentinel errors for the RW2-specific relocation subsystem.
var (
	// ErrRW2InvalidMagic is returned when the base bytes do not carry the
	// Panasonic RW2 magic "IIU\x00" at bytes [0:4].
	ErrRW2InvalidMagic = errors.New("tiff: RW2 invalid magic bytes (expected IIU\\x00)")

	// ErrRW2RawDataOffsetOverflow is returned when the raw sensor data block
	// new offset would overflow uint32.
	ErrRW2RawDataOffsetOverflow = errors.New("tiff: RW2 raw sensor data new offset overflowed uint32")

	// ErrRW2OutputTooShort is returned when the assembled RW2 output is shorter
	// than the minimum valid header size (8 bytes).
	ErrRW2OutputTooShort = errors.New("tiff: RW2 output too short to insert GUID and rebase offsets")

	// ErrRW2IFD0OutOfBounds is returned when the IFD0 offset stored in the
	// header points beyond the end of the assembled output.
	ErrRW2IFD0OutOfBounds = errors.New("tiff: RW2 IFD0 offset out of bounds")
)

// isRW2Magic reports whether the 4-byte slice b carries the Panasonic RW2 magic.
func isRW2Magic(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == rw2MagicBytes[0] && b[1] == rw2MagicBytes[1] &&
		b[2] == rw2MagicBytes[2] && b[3] == rw2MagicBytes[3]
}

// relocateTIFFAsRW2 is the internal RW2 relocator entry point used by the tiff.go
// wrapper InjectWithEXIFRW2.
//
// originalBytes must carry a valid RW2 magic at bytes [0:4] (isRW2Magic must
// return true). The caller (writeTIFFRW2 in write.go) is responsible for
// restoring the real RW2 magic into the bytes before calling this function,
// since rw2.Extract patches bytes [2:4] to 0x2A 0x00 when it returns rawEXIF.
//
// This separation (internal relocator + thin tiff.go wrapper) matches the
// InjectWithEXIFNEF / InjectWithEXIFARW pattern.
func relocateTIFFAsRW2(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	// Make a working copy so we can mutate bytes [2:4] in-place without
	// touching the caller's buffer (rawEXIF may be shared).
	workBytes := make([]byte, len(originalBytes))
	copy(workBytes, originalBytes)
	return relocateTIFFFromParsedRW2(workBytes, modifiedEXIF, rawIPTC, rawXMP)
}

// relocateTIFFFromParsedRW2 is the RW2-specific TIFF copy-and-relocate implementation.
//
// base must carry valid RW2 magic at bytes [0:4] (isRW2Magic must be true).
// base is mutated in-place (bytes [2:4] are patched to 0x2A 0x00 for parsing).
// Callers must pass a writable copy.
//
//nolint:cyclop,gocyclo,funlen // RW2-specific algorithm requires GUID handling, standalone block, and IFD patching; inherent complexity
func relocateTIFFFromParsedRW2(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	if !isRW2Magic(base) {
		return nil, fmt.Errorf("rw2: %w", ErrRW2InvalidMagic)
	}

	// ── Step A1: save 16-byte GUID ───────────────────────────────────────────
	// The GUID is at bytes [8:24] in the original file.  It is preserved verbatim
	// in the output at the same position.
	//
	// ExifTool Panasonic.pm: GUID = 16-byte device identifier at file offset 8.
	if len(base) < rw2GUIDOffset+rw2GUIDLen {
		return nil, fmt.Errorf("rw2: %w (%d bytes)", ErrRW2OutputTooShort, len(base))
	}
	var guid [rw2GUIDLen]byte
	copy(guid[:], base[rw2GUIDOffset:rw2GUIDOffset+rw2GUIDLen])

	// ── Step A2: patch bytes [2:4] to standard TIFF magic ────────────────────
	// TIFF 6.0 §2: magic must be 0x002A for classic TIFF. exif.Parse requires
	// this.  The IFD0 offset at bytes [4:8] = 24 is a valid TIFF offset value.
	base[2] = 0x2A
	base[3] = 0x00

	// Parse the magic-patched base when no pre-parsed struct is provided.
	if e == nil {
		var parseErr error
		e, parseErr = exif.Parse(base)
		if parseErr != nil {
			return nil, fmt.Errorf("rw2: parse for relocation: %w", parseErr)
		}
	}

	order := e.ByteOrder
	if order == nil {
		order = binary.LittleEndian // RW2 is always little-endian
	}

	// Ensure IFD0 exists.
	if e.IFD0 == nil {
		e.IFD0 = &exif.IFD{}
	}

	// ── Step 2: upsert metadata tags in IFD0 ─────────────────────────────────
	if rawIPTC != nil {
		upsertIFD0Entry(e.IFD0, exif.TagIPTC, exif.TypeLong, rawIPTC)
	}
	if rawXMP != nil {
		upsertIFD0Entry(e.IFD0, exif.TagXMP, exif.TypeByte, rawXMP)
	}
	// Step 2.5: clear IFD0.ThumbnailData before block enumeration.
	e.IFD0.ThumbnailData = nil

	// ── Step A3: remove sentinel StripOffsets/StripByteCounts ────────────────
	// RW2 StripOffsets = 0xFFFFFFFF is a sentinel (not a real image block).
	// Remove it so the standard enumerator does not try to copy bytes from
	// offset 0xFFFFFFFF.
	//
	// ExifTool Panasonic.pm: StripOffsets sentinel = 0xFFFFFFFF.
	removeRW2SentinelStrips(e.IFD0, order)

	// ── Step A4: register RawSensorData (0x0118) as a standalone imageBlock ──
	// tag 0x0118 is TypeLong Count=1 inline; its VALUE is the absolute file offset
	// of the raw sensor data (extends to EOF).  Register as a standalone block
	// (ifdPtr=nil) so assignNewOffsets assigns it a new absolute position.
	rawDataBlock := extractRW2RawDataBlock(base, e.IFD0, order)

	// ── Step 3: enumerate image blocks from the main IFD chain ───────────────
	mainIFDBlocks, err := enumerateImageBlocks(base, e, order)
	if err != nil {
		return nil, fmt.Errorf("rw2: enumerate image blocks: %w", err)
	}

	// Collect all blocks: main IFD + raw sensor data standalone.
	var allBlocks []*imageBlock
	allBlocks = append(allBlocks, mainIFDBlocks...)
	if rawDataBlock != nil {
		allBlocks = append(allBlocks, rawDataBlock)
	}

	// ── Step 4: enumerate SubIFDs ────────────────────────────────────────────
	subIFDs, subBlocks, subErr := enumerateSubIFDs(base, e.IFD0, order)
	if subErr != nil {
		return nil, fmt.Errorf("rw2: enumerate SubIFDs: %w", subErr)
	}
	allBlocks = append(allBlocks, subBlocks...)

	// ── Step 5: remove stale offset entries from main IFDs ───────────────────
	mainBlocks := filterMainBlocks(allBlocks, subIFDs)
	mainBlocks = filterNonNilIFDBlocks(mainBlocks) // exclude rawDataBlock (ifdPtr=nil)
	removeImageOffsetEntries(mainBlocks)

	// ── Step 6: insert placeholders and encode to learn the IFD size ─────────
	offsetValueSlices := insertPlaceholders(mainBlocks, order)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("rw2: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint32(len(skeleton)) //nolint:gosec // G115: len bounded by TIFF stream

	// ── Step 7: assign new absolute offsets ──────────────────────────────────
	subIFDsSize := computeSubIFDsSize(subIFDs)
	imageStart := ifdEnd + subIFDsSize
	assignNewOffsets(allBlocks, imageStart)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	if rawDataBlock != nil && rawDataBlock.newOffset == math.MaxUint32 {
		return nil, fmt.Errorf("rw2: %w", ErrRW2RawDataOffsetOverflow)
	}

	// ── Step 8a: update placeholder value bytes ───────────────────────────────
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// ── Step 8b: patch SubIFD raw bytes ──────────────────────────────────────
	patchSubIFDImageOffsets(subIFDs, allBlocks, order)

	// ── Step 9: re-encode → finalTIFF ────────────────────────────────────────
	// exif.Encode produces: "II" + 0x2A 0x00 + IFD0_off=8 + IFD block.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("rw2: encode final: %w", finalErr)
	}

	// ── Step B1: patch 0x0118 inline val_or_off in finalTIFF ─────────────────
	// The 0x0118 entry is inline (TypeLong Count=1 total=4).  Its value was the
	// original raw sensor data offset; it is now stale.  Patch it with the new
	// absolute position assigned by assignNewOffsets.
	//
	// Patch the 0x0118 inline val_or_off in IFD0 of finalTIFF with the new raw
	// data offset.  exif.Encode preserves the stale original value (667648); we
	// overwrite it here before GUID insertion.  The GUID insertion step adds +16.
	if rawDataBlock != nil {
		if err := patchRW2RawDataOffsetInFinalTIFF(finalTIFF, rawDataBlock, order); err != nil {
			return nil, fmt.Errorf("rw2: patch RawDataOffset in finalTIFF: %w", err)
		}
	}

	// ── Step 10: patch the 0x014A SubIFDs pointer array ──────────────────────
	if len(subIFDs) > 0 {
		if pErr := patchSubIFDPointers(finalTIFF, subIFDs, order); pErr != nil {
			return nil, fmt.Errorf("rw2: patch SubIFD pointers: %w", pErr)
		}
	}

	// ── Step 11: append SubIFD raw bytes ─────────────────────────────────────
	for _, si := range subIFDs {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		finalTIFF = append(finalTIFF, si.rawBytes...)
	}

	// ── Step 12: append image block bytes from source ─────────────────────────
	for _, blk := range allBlocks {
		end := uint64(blk.srcOffset) + uint64(blk.size)
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("rw2: image block offset=%d size=%d: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds)
		}
		finalTIFF = append(finalTIFF, base[blk.srcOffset:end]...)
	}

	// ── Step B2-B6: insert GUID, update header, shift offsets, restore magic ──
	return insertRW2GUIDAndShiftOffsets(finalTIFF, guid, rawDataBlock, order)
}

// removeRW2SentinelStrips removes the sentinel StripOffsets/StripByteCounts from
// ifd0 when StripOffsets = 0xFFFFFFFF (Panasonic RW2 convention).
//
// ExifTool Panasonic.pm: StripOffsets = 0xFFFFFFFF means the raw data is
// accessed via RawDataOffset (0x0118) instead of standard strip layout.
// The standard enumerator would try to copy bytes from offset 0xFFFFFFFF
// which is out-of-bounds; removing the sentinel prevents this.
func removeRW2SentinelStrips(ifd0 *exif.IFD, order binary.ByteOrder) {
	stripEntry := ifd0.Get(exif.TagStripOffsets)
	if stripEntry == nil {
		return
	}
	// Sentinel: TypeLong, Count=1, inline value = 0xFFFFFFFF.
	if stripEntry.Type != exif.TypeLong || stripEntry.Count != 1 || len(stripEntry.Value) < 4 {
		return
	}
	val := order.Uint32(stripEntry.Value[:4])
	if val == 0xFFFFFFFF {
		removeEntryFromIFD(ifd0, exif.TagStripOffsets)
		removeEntryFromIFD(ifd0, exif.TagStripByteCounts)
	}
}

// extractRW2RawDataBlock extracts a standalone imageBlock for the raw sensor data
// from the RW2 IFD0 tag 0x0118 (RawDataOffset).
//
// Tag 0x0118 is TypeLong Count=1 inline; its value is the absolute file offset
// to the raw sensor data.  The raw data extends from this offset to EOF.
//
// Returns nil when the 0x0118 entry is absent or its offset is 0.
// The val_or_off position within base is not returned; patching uses IFD scan
// (patchRW2RawDataOffsetInFinalTIFF).
func extractRW2RawDataBlock(base []byte, ifd0 *exif.IFD, order binary.ByteOrder) *imageBlock {
	rawEntry := ifd0.Get(rw2TagRawDataOffset)
	if rawEntry == nil || len(rawEntry.Value) < 4 {
		return nil
	}
	rawOff := order.Uint32(rawEntry.Value[:4])
	if rawOff == 0 || uint64(rawOff) >= uint64(len(base)) {
		return nil
	}
	rawSize := uint32(len(base)) - rawOff //nolint:gosec // G115: len(base) < 2^32
	return &imageBlock{
		srcOffset: rawOff,
		size:      rawSize,
		ifdPtr:    nil, // standalone (not owned by any exif.IFD entry)
		entryTag:  rw2TagRawDataOffset,
		index:     0,
	}
}

// patchRW2RawDataOffsetInFinalTIFF locates the 0x0118 entry in IFD0 of finalTIFF
// and overwrites its inline val_or_off field with rawDataBlock.newOffset.
//
// This is the pre-GUID-insertion patch: the value written here is the absolute
// offset in the standard-TIFF finalTIFF coordinate space (IFD0 at 8).
// The +16 shift for GUID insertion is applied later in insertRW2GUIDAndShiftOffsets.
//
// TIFF 6.0 §2: IFD0 starts at the offset stored in header bytes [4:8].
// In finalTIFF (before GUID insertion), IFD0 is at offset 8.
func patchRW2RawDataOffsetInFinalTIFF(finalTIFF []byte, rawDataBlock *imageBlock, order binary.ByteOrder) error {
	if len(finalTIFF) < 8 {
		return fmt.Errorf("rw2: %w (%d bytes)", ErrRW2OutputTooShort, len(finalTIFF))
	}
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return fmt.Errorf("rw2: patch RawDataOffset: %w (offset=%d, len=%d)", ErrRW2IFD0OutOfBounds, ifd0Off, len(finalTIFF))
	}
	ifdCount := int(order.Uint16(finalTIFF[ifd0Off:]))
	ifdPos := ifd0Off + 2

	for i := range ifdCount {
		e := ifdPos + i*12
		if e+12 > len(finalTIFF) {
			break
		}
		tag := exif.TagID(order.Uint16(finalTIFF[e:]))
		if tag != rw2TagRawDataOffset {
			continue
		}
		// Found 0x0118: overwrite the val_or_off field with the new raw data offset.
		// This is an inline entry (TypeLong Count=1 total=4); the val_or_off IS the value.
		order.PutUint32(finalTIFF[e+8:], rawDataBlock.newOffset)
		return nil
	}
	// 0x0118 not found in finalTIFF IFD0 — this can happen if the original RW2
	// had no 0x0118 tag or if exif.Encode dropped it.  Non-fatal: skip.
	return nil
}

// insertRW2GUIDAndShiftOffsets performs the RW2-specific post-relocation
// transformation on finalTIFF:
//
//  1. Insert the 16-byte GUID at byte position 8.
//  2. Update header bytes [4:8] from 8 to 24.
//  3. Walk ALL IFDs reachable from IFD0 (IFD0, ExifIFD, GPS IFD, InteropIFD) and
//     add +16 to every OOL val_or_off field and every inline sub-IFD pointer.
//  4. Add +16 to the 0x0118 inline val_or_off (raw sensor data pointer).
//  5. Restore RW2 magic "IIU\x00".
//
// Why we must walk ALL IFDs (not just IFD0):
//
//	exif.Encode produces a standard TIFF where ALL IFDs are placed at absolute
//	offsets starting at 8.  When we insert 16 bytes at position 8, every single
//	absolute offset in the entire file shifts by +16.  This includes:
//	  - IFD0 OOL entry val_or_off fields (the value area pointers).
//	  - Sub-IFD inline pointer entries: 0x8769 (ExifIFD), 0x8825 (GPS IFD),
//	    0xA005 (InteropIFD) are TypeLong, Count=1 (total=4 ≤ 4) → "inline"
//	    in the TIFF sense, but their VALUES are absolute file offsets.
//	  - ExifIFD OOL entry val_or_off fields.
//	  - GPS IFD OOL entry val_or_off fields.
//	  - InteropIFD OOL entry val_or_off fields.
//
//	Task #104 bug: only IFD0 OOL entries were shifted.  The ExifIFD pointer
//	(0x8769) and all ExifIFD-internal OOL pointers were missed, causing:
//	  - "Bad format (N) for ExifIFD entry 0" — 0x8769 pointer off by 16.
//	  - "Value for ExifIFD tag ... overlaps IFD" — ExifIFD OOL pointers off by 16.
//	  - 4 ExifIFD tags (ExposureTime, FNumber, ExposureBiasValue, FocalLength,
//	    DateTimeOriginal, CreateDate) unreadable due to stale OOL offsets.
//
//	The fix: use rebaseAllIFDsAfterGUID to walk ALL IFDs and shift ALL
//	file-absolute pointers consistently.
//
// finalTIFF is mutated: the standard 8-byte TIFF header is extended by 16 bytes
// (GUID insertion), and the returned slice is the complete RW2 output.
func insertRW2GUIDAndShiftOffsets(
	finalTIFF []byte,
	guid [rw2GUIDLen]byte,
	rawDataBlock *imageBlock, // may be nil
	order binary.ByteOrder,
) ([]byte, error) {
	if len(finalTIFF) < 8 {
		return nil, fmt.Errorf("%w (%d bytes)", ErrRW2OutputTooShort, len(finalTIFF))
	}

	// ── Step B2: build output with GUID inserted at position 8 ───────────────
	//
	// Original finalTIFF layout:
	//   [0:8]   TIFF header (II + 0x2A 0x00 + IFD0_off=8)
	//   [8:]    IFD block + image data
	//
	// Desired RW2 layout:
	//   [0:8]   RW2 header slot (rewritten below)
	//   [8:24]  16-byte GUID
	//   [24:]   IFD block + image data (all absolute offsets += 16)
	out := make([]byte, len(finalTIFF)+rw2GUIDLen)
	copy(out[0:8], finalTIFF[0:8])                             // copy original 8-byte header slot
	copy(out[rw2GUIDOffset:rw2GUIDOffset+rw2GUIDLen], guid[:]) // insert GUID
	copy(out[rw2GUIDOffset+rw2GUIDLen:], finalTIFF[8:])        // copy rest

	// ── Step B3: update IFD0 offset in header to 24 ──────────────────────────
	// exif.Encode wrote IFD0 at offset 8.  After inserting 16 bytes, IFD0 is now
	// at offset 24.  Update header bytes [4:8].
	order.PutUint32(out[4:], uint32(rw2IFD0Offset)) // 24

	// ── Step B4: rebase ALL IFDs reachable from IFD0 ─────────────────────────
	// Walk IFD0 and all sub-IFDs (ExifIFD, GPS IFD, InteropIFD), shifting every
	// TIFF-absolute OOL pointer by +16.  Also shift inline sub-IFD pointer entries
	// (0x8769, 0x8825, 0xA005) and the RW2-specific 0x0118 inline raw-data pointer.
	ifd0Start := int(rw2IFD0Offset) // 24
	if ifd0Start+2 > len(out) {
		return nil, fmt.Errorf("rw2: insert GUID: %w (offset=%d, len=%d)", ErrRW2IFD0OutOfBounds, ifd0Start, len(out))
	}
	rebaseAllIFDsAfterGUID(out, ifd0Start, rawDataBlock, order, 0)

	// ── Step B5: restore RW2 magic ────────────────────────────────────────────
	// Replace the standard TIFF header bytes [0:4] with RW2 magic "IIU\x00".
	// Bytes 0-1 are already "II" (little-endian byte-order marker, preserved).
	out[0] = rw2MagicBytes[0] // 'I'
	out[1] = rw2MagicBytes[1] // 'I'
	out[2] = rw2MagicBytes[2] // 'U'
	out[3] = rw2MagicBytes[3] // 0x00

	return out, nil
}

// rebaseAllIFDsAfterGUID walks the IFD starting at ifdStart in out and shifts
// all TIFF-absolute file offsets by +rw2GUIDLen (16) to account for the GUID
// inserted at file position 8.
//
// For each IFD entry:
//   - OOL entries (total > 4): shift val_or_off by +16.
//   - Inline sub-IFD pointer tags (0x8769, 0x8825, 0xA005): shift the inline
//     value by +16 AND recursively rebase the pointed-to sub-IFD.
//   - Tag 0x0118 (RawDataOffset): shift the inline value by +16.
//
// depth limits recursion to maxSubIFDDepth (8) levels:
//   - #172: a crafted RW2 chaining those pointers forward drives recursion to
//     ~bufferSize/delta (≈16 B per level for RW2) levels, causing a runtime
//     fatal stack overflow that defer/recover cannot catch.
//
// OOL pointers near math.MaxUint32 are left unchanged:
//   - #176: adding rw2GUIDLen (16) to an OOL pointer near 4 GiB wraps it to ~0
//     (pointing at the TIFF header), corrupting the file. Skip such entries.
//
// TIFF 6.0 §2: IFD entries are 12 bytes: tag(2)+type(2)+count(4)+val_or_off(4).
// EXIF §4.6.3: sub-IFD pointers (0x8769, 0x8825, 0xA005) are TypeLong, Count=1,
// total=4 ≤ 4, stored inline with the val_or_off field holding the sub-IFD offset.
//
//nolint:cyclop,gocyclo // recursive IFD walk with OOL/inline/pointer dispatch is inherent to the TIFF §2 format
func rebaseAllIFDsAfterGUID(out []byte, ifdStart int, rawDataBlock *imageBlock, order binary.ByteOrder, depth int) {
	// #172: depth guard — a crafted RW2 can chain sub-IFD pointers far enough to
	// exhaust the Go stack (~1 MB default). Cap at maxSubIFDDepth to match the
	// enumerateSubIFDsAt guard. real RW2 files have at most 3 levels (IFD0,
	// ExifIFD, InteropIFD).
	if depth >= maxSubIFDDepth {
		return
	}
	if ifdStart+2 > len(out) {
		return
	}
	ifdCount := int(order.Uint16(out[ifdStart:]))
	ifdPos := ifdStart + 2

	for i := range ifdCount {
		e := ifdPos + i*12
		if e+12 > len(out) {
			break
		}
		entryTag := exif.TagID(order.Uint16(out[e:]))
		entryType := order.Uint16(out[e+2:])
		entryCount := order.Uint32(out[e+4:])

		elemSz := typeSize(entryType)
		if elemSz == 0 || entryCount == 0 {
			continue
		}
		total := uint64(elemSz) * uint64(entryCount)

		if total > 4 {
			// OOL entry: val_or_off is a TIFF-absolute pointer to the value area.
			// After GUID insertion every such offset shifts by +rw2GUIDLen.
			// TIFF 6.0 §2: all offsets are measured from byte 0 of the TIFF stream.
			oldVOO := order.Uint32(out[e+8:])
			// Guard: only rebase offsets that were at or beyond the original IFD start
			// (offset 8 in the pre-insertion space = rw2GUIDOffset).
			// exif.Encode never places values at offsets < 8.
			if oldVOO >= rw2GUIDOffset {
				// #176: guard against uint32 overflow: an OOL pointer near 4 GiB
				// would wrap to ~0 (pointing at the TIFF header) if added to rw2GUIDLen.
				// Skip such entries — they already point outside the valid region.
				if oldVOO <= math.MaxUint32-uint32(rw2GUIDLen) {
					order.PutUint32(out[e+8:], oldVOO+uint32(rw2GUIDLen))
				}
			}
			continue
		}

		// Inline entry (total ≤ 4): only specific tags carry absolute file offsets.
		//
		// (A) Sub-IFD pointer tags: 0x8769 (ExifIFD), 0x8825 (GPS IFD), 0xA005 (InteropIFD).
		//     Their inline values are absolute file offsets to IFD structures.
		//     Shift the pointer AND recursively rebase the sub-IFD's entries.
		//
		// (B) Tag 0x0118 (RawDataOffset): inline absolute file offset to raw sensor data.
		//     Was patched by patchRW2RawDataOffsetInFinalTIFF; now add +16 for GUID.
		//
		// EXIF §4.6.3 / TIFF 6.0 §2: sub-IFD pointer tags are always TypeLong, Count=1.
		switch entryTag {
		case exif.TagExifIFDPointer, exif.TagGPSIFDPointer, exif.TagInteropIFDPointer:
			// Rebase sub-IFD pointer and recursively rebase the sub-IFD.
			oldVal := order.Uint32(out[e+8:])
			if oldVal < rw2GUIDOffset {
				continue // guard: implausibly small offset
			}
			// #176: guard against uint32 overflow for inline sub-IFD pointers.
			if oldVal > math.MaxUint32-uint32(rw2GUIDLen) {
				continue // would wrap: pointer already outside valid address space
			}
			newVal := oldVal + uint32(rw2GUIDLen)
			order.PutUint32(out[e+8:], newVal)
			// Recurse into the sub-IFD at its new position.
			// The sub-IFD was at oldVal in the pre-GUID space; it is now at newVal.
			// #172: pass depth+1 to enforce the recursion depth cap.
			subIFDStart := int(newVal)
			if subIFDStart+2 <= len(out) {
				rebaseAllIFDsAfterGUID(out, subIFDStart, nil, order, depth+1) // no rawDataBlock in sub-IFDs
			}
		case rw2TagRawDataOffset:
			if rawDataBlock != nil {
				oldVal := order.Uint32(out[e+8:])
				if oldVal >= rw2GUIDOffset {
					// #176: guard against uint32 overflow for raw data pointer.
					if oldVal <= math.MaxUint32-uint32(rw2GUIDLen) {
						order.PutUint32(out[e+8:], oldVal+uint32(rw2GUIDLen))
					}
				}
			}
		}
	}
}
