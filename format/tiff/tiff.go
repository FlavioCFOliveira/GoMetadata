// Package tiff implements extraction and injection of metadata within TIFF
// container files. TIFF stores EXIF in a SubIFD (tag 0x8769), IPTC in tag
// 0x83BB, and XMP in tag 0x02BC (TIFF Technical Note 3).
package tiff

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// xmpWireFrameMagic is the 8-byte sentinel that identifies a JPEG extended-XMP
// wire-frame payload produced by jpeg.ExtractWithWire. This internal encoding
// MUST NOT be passed to any TIFF Inject entry point: it is only understood by
// jpeg.Inject and would produce a corrupt tag 0x02BC if written verbatim.
//
// Task #70 regression: JPEG extended-XMP wire-frame leak to non-JPEG containers.
// Task #118 regression: TIFF inject paths were missing this guard (PNG/WebP/HEIF had it).
//
// Value: 0x00 'X' 'M' 'P' 'E' 'X' 'T' 0x00 — same as jpeg.xmpWireMagic.
// xmpWireFrameMagic mirrors the constant in format/jpeg, format/png, format/webp, and format/heif.
var xmpWireFrameMagic = [8]byte{0x00, 'X', 'M', 'P', 'E', 'X', 'T', 0x00} //nolint:gochecknoglobals // package-level constant bytes; never mutated

// rejectWireFrameXMP returns ErrCorruptXMP when rawXMP begins with the JPEG
// extended-XMP wire-frame magic (xmpWireFrameMagic). All TIFF Inject entry
// points call this guard before proceeding.
//
// Task #118 regression: defence-in-depth mirror of the identical guard in
// png.Inject, webp.Inject, and heif.Inject (task #70).
func rejectWireFrameXMP(rawXMP []byte) error {
	if len(rawXMP) >= 8 && [8]byte(rawXMP[:8]) == xmpWireFrameMagic {
		return fmt.Errorf("tiff: rawXMP contains an internal JPEG wire-frame encoding that cannot be stored in a TIFF container: %w", ErrCorruptXMP)
	}
	return nil
}

// Extract reads metadata payloads from a TIFF container.
// rawEXIF is the entire TIFF byte stream (TIFF itself is the EXIF container).
// rawIPTC and rawXMP are read from the respective IFD0 tags.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: seek: %w", err)
	}

	// #140 fix: cap the read to maxFileSize+1 bytes so that an oversized or
	// infinite streaming reader cannot trigger unbounded heap allocation.
	// If the reader delivers more than maxFileSize bytes the file is rejected
	// with ErrFileTooLarge before any further parsing takes place.
	data, err := io.ReadAll(io.LimitReader(r, maxFileSize+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: read: %w", err)
	}
	if int64(len(data)) > maxFileSize {
		return nil, nil, nil, fmt.Errorf("tiff: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if len(data) < 8 {
		return nil, nil, nil, ErrFileTooShort
	}

	order, err := byteOrder(data)
	if err != nil {
		return nil, nil, nil, err
	}

	// TIFF 6.0 §2: magic number is 42 (0x002A) for classic TIFF.
	// BigTIFF spec (Aware Systems / libtiff) §2: magic 43 (0x002B) for BigTIFF,
	// which uses a 16-byte header with 8-byte IFD offsets.
	magic := order.Uint16(data[2:])
	switch magic {
	case 0x002A:
		// Classic TIFF: 8-byte header, 32-bit IFD offsets, 12-byte entries.
		// The whole TIFF data IS the EXIF payload (TIFF §2).
		rawEXIF = data
		ifd0Off := order.Uint32(data[4:])
		rawIPTC, rawXMP = extractTagValues(data, ifd0Off, order)
		return rawEXIF, rawIPTC, rawXMP, nil

	case 0x002B:
		// BigTIFF: 16-byte header, 64-bit IFD offsets, 20-byte entries.
		// BigTIFF spec §2: bytes [4:6] = offset bytesize (must be 8),
		// bytes [6:8] = constant 0, bytes [8:16] = IFD0 offset (uint64).
		rawEXIF, rawIPTC, rawXMP, err = extractBigTIFF(data, order)
		return rawEXIF, rawIPTC, rawXMP, err

	default:
		return nil, nil, nil, fmt.Errorf("tiff: unsupported magic 0x%04X (expected 0x002A classic TIFF or 0x002B BigTIFF): %w",
			magic, ErrUnsupportedMagic)
	}
}

// Inject writes a modified TIFF stream to w, replacing the metadata tags.
//
// When rawIPTC or rawXMP is non-nil, Inject uses the copy-and-relocate path
// (relocateTIFF) to rebuild the IFD chain, upsert the new metadata payloads,
// and append every image-data block (strips, tiles, main-image JPEG) at a
// fresh absolute offset — preserving the pixel data byte-identically.
//
// When both rawIPTC and rawXMP are nil, the base bytes are written verbatim
// (pass-through path).
//
// Nil-means-delete semantics (footgun warning — task #149 regression):
//
//	A nil rawIPTC value means "remove the IPTC tag from the output". When
//	only XMP is being updated (rawXMP != nil, rawIPTC == nil) and the source
//	TIFF carries existing IPTC data, that IPTC data will be silently dropped
//	from the output. To preserve existing IPTC, pass the original rawIPTC
//	bytes from a prior tiff.Extract call. The top-level gometadata.Write path
//	handles this automatically by passing m.rawIPTC through encodeIPTC.
//
// Round-trip fidelity:
//   - All IFD entries with known TIFF type codes are faithfully preserved.
//   - Image-data blocks (StripOffsets, TileOffsets, JPEGInterchangeFormat for
//     non-thumbnail IFDs) are copied verbatim from the source and their offset
//     entries are patched to the new positions.
//   - SubIFDs (tag 0x014A) are recursively followed; their image blocks are
//     enumerated and relocated alongside the SubIFD structure (task #94).
//     This enables correct DNG write support (multi-SubIFD and tiled DNG).
//   - IFD1 JPEG thumbnails are handled by exif.Encode's patchThumbnailEntries.
//   - MakerNote blobs are copied verbatim (see relocate.go for safety note).
//   - Unknown-type IFD entries retain their 4-byte field; out-of-line data
//     referenced by unknown types is not copied (see exif.Encode docs).
//
// If exif.Parse fails, Inject returns the parse error rather than silently
// discarding the requested metadata.
//
// Note: the rawEXIF parameter is used as the base TIFF bytes for relocation.
// When rawEXIF was produced by exif.Encode (an IFD skeleton without image
// blocks), image block enumeration will fail with ErrBlockOutOfBounds. Callers
// that hold the original TIFF bytes AND a modified *exif.EXIF struct (e.g. the
// gometadata.Write path) must use InjectWithEXIF instead.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, _ bool) error { //nolint:gocyclo,cyclop // LimitReader guard added by #140 pushed complexity to 11; inherent nil-dispatch logic
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads before
	// proceeding. The wire-frame encoding (magic 0x00XMPEXT\x00) is specific to
	// JPEG APP1 and cannot be stored in TIFF tag 0x02BC. Writing it verbatim
	// produces a corrupt XMP blob. Mirror of the identical guard in png.Inject,
	// webp.Inject, and heif.Inject (task #70).
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tiff: seek: %w", err)
	}

	// Determine the base TIFF data to work with.
	var base []byte
	if rawEXIF != nil {
		base = rawEXIF
	} else {
		// #140 fix: cap the read to maxFileSize+1 bytes so that an oversized or
		// infinite streaming reader cannot trigger unbounded heap allocation.
		var err error
		base, err = io.ReadAll(io.LimitReader(r, maxFileSize+1))
		if err != nil {
			return fmt.Errorf("tiff: read: %w", err)
		}
		if int64(len(base)) > maxFileSize {
			return fmt.Errorf("tiff: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
		}
	}

	// Pass-through: no metadata changes requested.
	// #190 fix: treat zero-length slices the same as nil — an empty rawIPTC or
	// rawXMP must not trigger the copy-and-relocate path or write a zero-length
	// tag. encodeIPTC normalises empty→nil, but guard here for any direct callers.
	if len(rawIPTC) == 0 && len(rawXMP) == 0 {
		if _, err := w.Write(base); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	// Copy-and-relocate path: rebuild IFD structure with upserted metadata and
	// appended image-data blocks at corrected offsets (epic #33, tasks #92/#93).
	//
	// Task #149 regression: nil rawIPTC causes the 0x83BB IPTC tag to be omitted
	// from the rebuilt IFD (upsertIFD0Entry is only called when the value is
	// non-nil). Callers that only want to update XMP must pass the original
	// rawIPTC (from tiff.Extract) to avoid silently deleting IPTC.
	updated, err := relocateTIFF(base, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIF writes a modified TIFF stream to w using the ORIGINAL TIFF
// bytes for image-data relocation and a pre-built (already-mutated) *exif.EXIF
// struct for the IFD content.
//
// This is the correct entry point for the gometadata.Write path on TIFF-based
// containers. The standard Inject function receives rawEXIF from encodeEXIF,
// which calls exif.Encode and produces an IFD skeleton that lacks image blocks.
// Feeding that skeleton to the relocator causes ErrBlockOutOfBounds because the
// skeleton is shorter than the original strip/tile offsets stored in the IFD.
//
// InjectWithEXIF avoids this by separating concerns:
//   - originalBytes: the ORIGINAL TIFF file bytes (all image blocks at original
//     absolute offsets). Used only as the source for copying image data in
//     relocateTIFFFromParsed step 12.
//   - modifiedEXIF: the *exif.EXIF struct produced by exif.Parse(originalBytes)
//     and subsequently mutated by the caller (SetCopyright, SetGPS, etc.).
//     Its IFDs carry both the edited metadata AND the original image-block offsets
//     (StripOffsets/TileOffsets still point at originalBytes positions).
//   - rawIPTC, rawXMP: freshly encoded IPTC/XMP payloads to upsert into IFD0
//     (may be nil if unchanged).
//
// If modifiedEXIF is nil, InjectWithEXIF falls back to parsing originalBytes
// (same behaviour as Inject).
//
// fix(tiff): task #97 — real-file TIFF/DNG write produced ErrBlockOutOfBounds
// because encodeEXIF fed an IFD skeleton as the relocate base.
func InjectWithEXIF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := relocateTIFFFromParsed(originalBytes, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFCR2 is the CR2-specific variant of InjectWithEXIF.
//
// Canon CR2 files carry a proprietary 4-byte marker immediately after the
// standard 8-byte TIFF header, at bytes 8–11:
//
//	bytes 8–9:   0x43 0x52 ('C','R') — Canon CR2 signature
//	bytes 10–11: 0x02 0x00           — CR2 version (2.0)
//
// This is followed by IFD0, which therefore begins at offset 16 (not 8) in
// a well-formed CR2 file (Canon CR2 spec §3.1).
//
// The standard relocateTIFFFromParsed rebuilds the TIFF via exif.Encode, which
// always produces a standard 8-byte header with IFD0 at offset 8. This places
// IFD0 directly at the CR marker position, making the output unparseable: the
// first 2 bytes of IFD0 (the entry count) are interpreted as 0x4352 = 17235
// in big-endian or 0x5243 = 21059 in little-endian, causing exif.Parse to
// report "IFD at offset 8 could not be parsed".
//
// The fix inserts 8 bytes at position 8 (4 CR-marker bytes + 4 zero-pad bytes)
// and rebases all IFD absolute offsets by +8, so the standard 8-byte TIFF
// header is extended to a 16-byte CR2 header and IFD0 is at offset 16.
// This mirrors the insertRW2GUIDAndShiftOffsets approach (inserting 16 bytes
// for the RW2 GUID at position 8), but with delta=8 for the CR2 marker.
//
// Canon CR2 spec §3.1: bytes 8–9 identify the file as CR2; bytes 10–11 carry
// the version. IFD0 starts at offset 16. These bytes MUST be preserved
// verbatim on every write cycle so that standard CR2 readers and ExifTool
// continue to recognise the format.
//
// containers.md §8(e): "CR2: preserve CR 02 00 at offset 8; IFD0 at offset 16."
//
// This is the entry point used by gometadata.Write for FormatCR2.
func InjectWithEXIFCR2(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := relocateTIFFFromParsed(originalBytes, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}

	// Insert CR2 marker at position 8 and rebase all IFD offsets by +8.
	//
	// exif.Encode always places IFD0 at offset 8 (hardcoded in writeTIFFHeader:
	// headerSize = 8). After inserting 8 bytes at position 8, IFD0 moves to
	// offset 16, which is the canonical CR2 IFD0 position.
	//
	// Canon CR2 spec §3.1: TIFF header (8 bytes) + CR marker (4 bytes) + zero-pad
	// (4 bytes) = 16-byte total header; IFD0 immediately follows at offset 16.
	result, insertErr := insertCR2MarkerAndShiftOffsets(updated, originalBytes)
	if insertErr != nil {
		return insertErr
	}

	if _, err = w.Write(result); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// cr2MarkerLen is the length of the Canon CR2 proprietary header extension
// inserted at byte position 8 of the TIFF stream.
// Canon CR2 spec §3.1: 4-byte marker ('C','R',0x02,0x00) + 4-byte zero-pad = 8 bytes.
const cr2MarkerLen = 8

// cr2IFD0Offset is the canonical IFD0 offset in a well-formed CR2 file.
// Canon CR2 spec §3.1: standard 8-byte TIFF header + 8-byte CR2 marker = 16.
const cr2IFD0Offset = 16

// insertCR2MarkerAndShiftOffsets inserts the 8-byte CR2 marker at byte
// position 8 of finalTIFF, updates the IFD0 offset in the TIFF header from
// 8 to 16, and rebases all IFD absolute offsets by +8.
//
// The CR2 marker bytes are read from originalBytes[8:12] (the original file's
// CR2 marker: 0x43,0x52,0x02,0x00); bytes [12:16] are zeroed.
//
// The original TIFF header at [0:4] (byte-order + magic) is preserved.
//
// Algorithm:
//
//  1. Build output: hdr[0:8] + marker[0:8] + finalTIFF[8:].
//  2. Update header bytes [4:8] → 16 (new IFD0 offset).
//  3. Walk IFD0 (at offset 16) and all sub-IFDs, shifting every OOL val_or_off
//     and every inline sub-IFD pointer by +cr2MarkerLen (+8).
//
// This mirrors insertRW2GUIDAndShiftOffsets (delta=16 for RW2 GUID) but uses
// delta=8 for the 8-byte CR2 marker extension.
//
// Canon CR2 spec §3.1 / TIFF 6.0 §2 / containers.md §8(e).
func insertCR2MarkerAndShiftOffsets(finalTIFF, originalBytes []byte) ([]byte, error) {
	if len(finalTIFF) < 8 {
		return nil, fmt.Errorf("%w (%d bytes)", ErrCR2OutputTooShort, len(finalTIFF))
	}

	order, orderErr := byteOrder(finalTIFF)
	if orderErr != nil {
		return nil, fmt.Errorf("tiff: CR2 insert marker: %w", orderErr)
	}

	// Build 8-byte CR2 marker: bytes [8:12] from original + 4 zero bytes [12:16].
	// Canon CR2 spec §3.1: 0x43 'C', 0x52 'R', 0x02 version, 0x00 version minor.
	var marker [cr2MarkerLen]byte
	if len(originalBytes) >= 12 {
		copy(marker[0:4], originalBytes[8:12]) // CR2 signature + version
		// marker[4:8] remain zero (reserved, per Canon CR2 spec §3.1)
	} else {
		// Fallback: synthesise canonical marker when original is too short.
		marker[0] = 0x43 // 'C'
		marker[1] = 0x52 // 'R'
		marker[2] = 0x02 // version 2
		marker[3] = 0x00 // version minor 0
	}

	// Step 1: assemble output with marker inserted at position 8.
	//
	//   [0:8]   original TIFF header slot (II/MM + 0x2A 0x00 + IFD0_off=8 → 16)
	//   [8:16]  CR2 marker (4-byte CR signature + 4 zero bytes)
	//   [16:]   finalTIFF[8:] (IFD block + image data)
	out := make([]byte, len(finalTIFF)+cr2MarkerLen)
	copy(out[0:8], finalTIFF[0:8])            // preserve BOM + magic
	copy(out[8:8+cr2MarkerLen], marker[:])    // insert CR2 marker
	copy(out[8+cr2MarkerLen:], finalTIFF[8:]) // IFD block + image data

	// Step 2: update IFD0 offset in TIFF header from 8 to 16.
	// TIFF 6.0 §2: bytes [4:8] = IFD0 offset. exif.Encode wrote 8; we write 16.
	order.PutUint32(out[4:], uint32(cr2IFD0Offset)) // 16

	// Step 3: rebase all IFD absolute offsets by +cr2MarkerLen (+8).
	// IFD0 is now at position 16 in the output.
	ifd0Start := cr2IFD0Offset
	if ifd0Start+2 > len(out) {
		return nil, fmt.Errorf("%w (offset=%d, len=%d)", ErrCR2IFD0OutOfBounds, ifd0Start, len(out))
	}
	rebaseAllIFDsAfterCR2Marker(out, ifd0Start, order, 0)

	return out, nil
}

// rebaseAllIFDsAfterCR2Marker walks the IFD at ifdStart in out and shifts all
// TIFF-absolute file offsets by +cr2MarkerLen (+8) to account for the 8-byte
// CR2 marker inserted at file position 8.
//
// For each IFD entry:
//   - OOL entries (type×count > 4): shift val_or_off by +cr2MarkerLen.
//   - Inline sub-IFD pointer tags (0x8769 ExifIFD, 0x8825 GPS, 0xA005 Interop):
//     shift the inline value by +cr2MarkerLen AND recursively rebase the sub-IFD.
//
// Guards:
//   - depth limits recursion to maxSubIFDDepth (8) levels so a crafted CR2 that
//     chains sub-IFD pointers in a deep cycle cannot cause a stack overflow.
//     #172: unbounded recursive IFD walker depth guard.
//   - OOL offsets near math.MaxUint32 are left unchanged when adding cr2MarkerLen
//     would wrap to zero, preventing pointer corruption.
//     #176: uint32 overflow guard for OOL pointer rebasing.
//   - Only offsets ≥ 8 (the insertion point) are shifted, which covers all
//     offsets produced by exif.Encode (which starts placing values at offset 8+).
//
// TIFF 6.0 §2: IFD entries are 12 bytes: tag(2)+type(2)+count(4)+val_or_off(4).
// EXIF §4.6.3: sub-IFD pointer tags are TypeLong, Count=1, total=4 ≤ 4 → inline.
//
//nolint:cyclop,gocyclo // recursive IFD walk with OOL/inline/pointer dispatch mirrors rebaseAllIFDsAfterGUID; inherent TIFF §2 complexity
func rebaseAllIFDsAfterCR2Marker(out []byte, ifdStart int, order binary.ByteOrder, depth int) {
	// #172: depth guard — mirrors the maxSubIFDDepth cap used by enumerateSubIFDsAt.
	// A crafted CR2 chaining sub-IFD pointers more than 8 levels deep would drive
	// the recursion to ~bufferSize/cr2MarkerLen calls, causing a runtime stack overflow.
	// TIFF Extension §F: SubIFD nesting depth is bounded in practice; 8 is generous.
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
			// After CR2 marker insertion every such offset shifts by +cr2MarkerLen.
			// TIFF 6.0 §2: all offsets are measured from byte 0 of the TIFF stream.
			oldVOO := order.Uint32(out[e+8:])
			// Only rebase offsets at or beyond the insertion point (8).
			// exif.Encode never places values at offsets < 8.
			const insertionPoint = 8
			if oldVOO >= insertionPoint {
				// #176: guard against uint32 overflow: an OOL pointer near 4 GiB would
				// wrap to ~0 (pointing at the TIFF header) if added to cr2MarkerLen.
				// Skip such entries — they already point outside the valid region.
				if oldVOO <= math.MaxUint32-uint32(cr2MarkerLen) {
					order.PutUint32(out[e+8:], oldVOO+uint32(cr2MarkerLen))
				}
			}
			continue
		}

		// Inline entry (total ≤ 4): only sub-IFD pointer tags carry absolute file
		// offsets that need rebasing.
		//
		// EXIF §4.6.3: 0x8769 (ExifIFD), 0x8825 (GPS IFD), 0xA005 (InteropIFD)
		// are TypeLong, Count=1, total=4 ≤ 4 → stored inline. Their val_or_off
		// VALUE is an absolute file offset to the sub-IFD structure.
		switch entryTag {
		case exif.TagExifIFDPointer, exif.TagGPSIFDPointer, exif.TagInteropIFDPointer:
			// Rebase sub-IFD pointer and recursively rebase the sub-IFD.
			oldVal := order.Uint32(out[e+8:])
			const insertionPoint = 8
			if oldVal < insertionPoint {
				continue // guard: implausibly small offset (shouldn't happen)
			}
			// #176: guard against uint32 overflow for inline sub-IFD pointers.
			if oldVal > math.MaxUint32-uint32(cr2MarkerLen) {
				continue // would wrap: pointer already outside valid address space
			}
			newVal := oldVal + uint32(cr2MarkerLen)
			order.PutUint32(out[e+8:], newVal)
			// Recurse into the sub-IFD at its new position in out.
			// #172: pass depth+1 to enforce the recursion depth cap.
			subIFDStart := int(newVal)
			if subIFDStart+2 <= len(out) {
				rebaseAllIFDsAfterCR2Marker(out, subIFDStart, order, depth+1)
			}
		}
	}
}

// InjectWithEXIFNEF is the NEF-specific variant of InjectWithEXIF.
//
// It runs the Nikon-specific preprocessing step (extend MakerNote blob,
// enumerate PreviewIFD image block) before the standard TIFF copy-and-relocate
// algorithm, and patches the MakerNote-relative PreviewIFD offsets after encoding.
//
// This is the entry point used by gometadata.Write for FormatNEF.
// See relocate_nef.go for the full algorithm description.
func InjectWithEXIFNEF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := relocateTIFFFromParsedNEF(originalBytes, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFARW is the ARW-specific variant of InjectWithEXIF.
//
// It runs the Sony-specific preprocessing step (extract SR2Private block and
// MakerNote info) before the standard TIFF copy-and-relocate algorithm, and
// patches the following in the output after encoding:
//   - Sony MakerNote (0x927C) OOL offsets are rebased (Sony uses TIFF-absolute
//     offsets, unlike Canon which uses blob-relative).
//   - SR2Private (0xC634) inline 4-byte value is updated to point to the new SR2
//     block position; SR2 internal pointers are rebased.
//
// This is the entry point used by gometadata.Write for FormatARW.
// See relocate_arw.go for the full algorithm description.
func InjectWithEXIFARW(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := relocateTIFFFromParsedARW(originalBytes, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFORF is the ORF-specific variant of InjectWithEXIF.
//
// It patches the non-standard Olympus ORF magic bytes (IIRO "0x49 0x49 0x52 0x4F"
// or IIRS "0x49 0x49 0x52 0x53") to standard TIFF magic before relocation, and
// restores the original magic in the output.
//
// originalBytes must carry a valid ORF magic at bytes [0:4]; the caller
// (writeTIFFORF in write.go) is responsible for restoring the real magic
// into m.rawEXIF before calling this function, since orf.Extract patches
// bytes [2:4] to 0x2A 0x00.
//
// This is the entry point used by gometadata.Write for FormatORF.
// See relocate_orf.go for the full algorithm description.
func InjectWithEXIFORF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := relocateTIFFAsORF(originalBytes, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// InjectWithEXIFRW2 is the RW2-specific variant of InjectWithEXIF.
//
// It handles two Panasonic RW2 non-standard features before relocation:
//   - Non-standard "IIU\x00" magic (patched to 0x2A 0x00 for exif.Parse).
//   - 16-byte device GUID at bytes [8:24] (GUID is saved and re-inserted post-encode;
//     all absolute offsets in IFD0 are rebased by +16 after the GUID insertion).
//
// originalBytes must carry the valid RW2 magic "IIU\x00" at bytes [0:4]; the
// caller (writeTIFFRW2 in write.go) is responsible for restoring the real magic
// since rw2.Extract patches bytes [2:4] to 0x2A 0x00.
//
// This is the entry point used by gometadata.Write for FormatRW2.
// See relocate_rw2.go for the full algorithm description.
func InjectWithEXIFRW2(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
	// Task #118 regression: reject JPEG extended-XMP wire-frame payloads.
	if err := rejectWireFrameXMP(rawXMP); err != nil {
		return err
	}

	// Pass-through: no metadata changes requested and no EXIF edits.
	if modifiedEXIF == nil && rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(originalBytes); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	updated, err := relocateTIFFAsRW2(originalBytes, modifiedEXIF, rawIPTC, rawXMP)
	if err != nil {
		return err
	}
	if _, err = w.Write(updated); err != nil {
		return fmt.Errorf("tiff: write updated: %w", err)
	}
	return nil
}

// upsertIFD0Entry adds or replaces an entry in ifd for the given tag while
// maintaining the sorted-by-tag invariant required by IFD.Get (binary search).
//
// TIFF 6.0 §7: each tag in an IFD must appear exactly once and entries must be
// stored in ascending tag order. Violating this invariant causes filterEntries'
// binary search to misidentify present tags as absent, producing duplicate
// entries in the re-encoded output.
//
// For TypeLong (element size = 4 bytes), value is padded to the next 4-byte
// boundary with zero bytes and Count is set to len(paddedValue)/4. IPTC data
// is stored as TypeLong per Adobe XMP Spec and ExifTool convention; padding
// with zero bytes is safe because the IPTC parser scans for 0x1C tag markers
// and silently skips all other byte values (IIM §1.6).
//
// For all other types (e.g. TypeByte for XMP), Count equals len(value).
//
// Implementation: binary search locates the insertion point in O(log n).
// Replace in-place when the tag already exists; otherwise slices.Insert places
// the new entry at the correct sorted position in O(n) (one memmove).
func upsertIFD0Entry(ifd *exif.IFD, tag exif.TagID, typ exif.DataType, value []byte) {
	count := uint32(len(value)) //nolint:gosec // G115: IFD value length bounded by input
	if typ == exif.TypeLong {
		// TIFF 6.0 §2: Count = number of uint32 elements.
		// Round up to the next 4-byte boundary; writeIFD zero-fills the gap in
		// the value area.  The original (unpadded) bytes are kept in Value so
		// that the read-back via extractTagValues returns the unpadded bytes
		// after the caller trims the value to the IFD-declared byte length.
		count = uint32((len(value) + 3) / 4) //nolint:gosec // G115: IFD value length bounded by input
	}
	entry := exif.IFDEntry{
		Tag:   tag,
		Type:  typ,
		Count: count,
		Value: value,
	}

	// Binary search for the insertion point. Entries are expected to be sorted
	// (parseSingleIFD calls sortEntries; prior upsertIFD0Entry calls maintain
	// the invariant after this fix).
	n := len(ifd.Entries)
	lo, hi := 0, n
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if ifd.Entries[mid].Tag < tag {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	i := lo

	if i < n && ifd.Entries[i].Tag == tag {
		// Replace existing entry in-place; sorted order is preserved.
		ifd.Entries[i] = entry
		return
	}
	// Insert at position i to maintain sorted order.
	// slices.Insert is O(n) (one memmove) vs. append-then-sort O(n log n).
	ifd.Entries = slices.Insert(ifd.Entries, i, entry)
}

// byteOrder determines the TIFF byte order from the first 2 bytes.
func byteOrder(b []byte) (binary.ByteOrder, error) {
	switch {
	case b[0] == 'I' && b[1] == 'I':
		return binary.LittleEndian, nil
	case b[0] == 'M' && b[1] == 'M':
		return binary.BigEndian, nil
	}
	return nil, fmt.Errorf("tiff: invalid byte order marker %q: %w", b[:2], ErrInvalidByteOrder)
}

// extractTagValues scans IFD0 for IPTC (0x83BB) and XMP (0x02BC) tags
// and returns their raw byte values.
func extractTagValues(data []byte, ifd0Off uint32, order binary.ByteOrder) (rawIPTC, rawXMP []byte) { //nolint:gocyclo // IPTC trimming branch is inherent to TypeLong-vs-TypeUndefined handling; extracting a helper would reduce clarity
	// Security audit FIX 5 (CWE-681/190): compare in uint64, not int, before
	// converting ifd0Off to int. On a 32-bit platform (GOARCH=386/arm),
	// int(ifd0Off) for ifd0Off >= 2^31 is negative, which would let a bad
	// offset pass an int-typed bound check and then panic on data[ifd0Off:].
	// Mirrors the #74 fix in format/detect.go's parseClassicTIFFIFD0 and the
	// #45 fix in format/jpeg's parseIRBEntry.
	if uint64(ifd0Off)+2 > uint64(len(data)) {
		return nil, nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
	// ifd0Off ≤ uint64(len(data))-2, and len(data) is a valid int on this
	// platform, so ifd0Off < len(data) fits int safely here.
	pos := int(ifd0Off) + 2

	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])

		var v []byte
		sz := typeSize(typ)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(cnt)
		if total <= 4 {
			v = data[e+8 : e+8+int(total)]
		} else {
			off := order.Uint32(data[e+8:])
			// Guard against integer overflow: check before computing end.
			if uint64(off) > uint64(len(data)) || total > uint64(len(data))-uint64(off) {
				continue
			}
			v = data[uint64(off) : uint64(off)+total]
		}

		switch tag {
		case 0x83BB: // IPTC-NAA
			// ROBUST-16 (iptc.md §5): strip trailing 0x00 bytes ONLY for TypeLong
			// (typ == 4) because TypeLong IPTC is padded to a 4-byte boundary by
			// the writer (TIFF 6.0 §2: Count = number of uint32 elements). Those
			// padding bytes are structural artefacts, not IPTC data.
			//
			// For TypeByte (1) and TypeUndefined (7) do NOT strip: a valid IPTC
			// payload may legitimately end in 0x00 (e.g. a NUL-terminated text
			// field value). bytes.TrimRight on those types silently corrupted
			// payloads whose last dataset value ended with 0x00 (task #153).
			//
			// The IIM scanner naturally skips non-0x1C bytes (IIM §1.6), so any
			// residual TypeLong padding bytes are harmless after trimming only
			// the known structural zeros.
			if len(v) > 0 {
				if typ == 4 { // TypeLong: trim structural alignment padding
					rawIPTC = trimIPTCLongPadding(v)
				} else {
					rawIPTC = v // TypeByte / TypeUndefined: no trim (ROBUST-16)
				}
				if len(rawIPTC) == 0 {
					rawIPTC = nil
				}
			}
		case 0x02BC: // XMP
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}

// trimIPTCLongPadding trims trailing 0x00 alignment-padding bytes from an IPTC
// payload stored as TypeLong. TypeLong pads the value to the next 4-byte
// boundary; those trailing zeros are never valid IIM dataset prefixes (0x1C)
// and are safe to remove. This function is ONLY called for TypeLong (typ == 4);
// TypeByte and TypeUndefined payloads are not trimmed (ROBUST-16, task #153).
func trimIPTCLongPadding(v []byte) []byte {
	// Walk backwards from the end of v, stripping zero bytes until we hit a
	// non-zero byte or exhaust the slice.
	end := len(v)
	for end > 0 && v[end-1] == 0x00 {
		end--
	}
	return v[:end]
}

// typeSize returns the byte size of a single value for the given TIFF type.
//
// Type 13 ("IFD") intentionally diverges from exif/type.go's DataType table
// (task #270, format/tiff-only fix — does NOT touch the exif package).
//
// CIPA DC-008-2023 §4.6.3 (EXIF 3.0) assigns type code 13 to TypeUTF8, a NEW
// 1-byte-per-element EXIF-registry string type. Adobe TIFF 6.0 Extensions
// ("Adding New Fields", also codified in libtiff's tif_dir.h as TIFF_IFD)
// assigns the SAME numeric code 13 to IFD — a 4-byte pointer to a child IFD —
// predating EXIF 3.0 by decades. This is a genuine type-code collision between
// the two specifications' independent numbering spaces.
//
// exif/type.go correctly follows CIPA DC-008-2023 for its OWN EXIF-registry
// tag table (IFD0/ExifIFD/GPSIFD tags never legitimately used type 13 before
// EXIF 3.0, so the EXIF-3.0 interpretation is safe there). But tag 0x014A
// (SubIFDs) is a TIFF-Extension tag, not an EXIF-registry tag (TIFF Extension
// §F; Adobe DNG Spec §4), and real-world files legitimately declare it as
// type 13 meaning IFD — see the conformance fixture
// testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif, purpose-built to
// exercise exactly this. format/tiff's own generic raw-IFD scanners (which
// walk 0x014A arrays and SubIFD entries independently of exif.Parse) must
// interpret type 13 as IFD (4 bytes) for that tag family, matching the
// TIFF-Extension meaning, not the EXIF-3.0 one.
//
// #270 regression: exif.Parse (which correctly follows CIPA DC-008-2023 for
// type 13 in its own struct-based IFD model) mis-sizes a 0x014A/type=13
// entry's inline value to 1 byte instead of 4, silently truncating the SubIFD
// pointer. format/tiff's write-path relocator never relies on exif's parsed
// Value for 0x014A; it re-reads the raw bytes directly using this table (see
// findEntryInIFD / enumerateSubIFDs in relocate_bigtiff.go).
func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 13: // IFD — TIFF 6.0 Extensions / libtiff TIFF_IFD; see doc comment above.
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	}
	return 0
}

// typeSizeBigTIFF returns the byte size of a single value for a BigTIFF type.
// It extends typeSize with the three BigTIFF-only 64-bit types:
//
//	LONG8  (16) = uint64, 8 bytes — BigTIFF spec §3.3
//	SLONG8 (17) = int64,  8 bytes — BigTIFF spec §3.3
//	IFD8   (18) = uint64 IFD offset, 8 bytes — BigTIFF spec §3.3
//
// These types are only valid inside a BigTIFF container; classic TIFF parsers
// must reject them.  Returns 0 for any unknown type code (same sentinel as
// typeSize) to allow the caller to skip unrecognised entries gracefully.
//
// Type 13 ("IFD", 4 bytes) is handled identically to typeSize — see that
// function's doc comment for the EXIF-3.0-vs-TIFF-Extension type-code
// collision this deliberately resolves in favour of the TIFF-Extension
// meaning for format/tiff's own generic raw-IFD scanners.
func typeSizeBigTIFF(t uint16) uint64 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
		return 4
	case 13: // IFD — TIFF 6.0 Extensions / libtiff TIFF_IFD; see typeSize doc comment.
		return 4
	case 5, 10, 12: // RATIONAL, SRATIONAL, DOUBLE
		return 8
	case 16, 17, 18: // LONG8, SLONG8, IFD8 — BigTIFF spec §3.3
		return 8
	}
	return 0 // unknown type: caller skips the entry
}

// bigTIFFMinHeaderLen is the minimum length of a valid BigTIFF header.
// BigTIFF spec §2: 16 bytes = 2 (order) + 2 (magic) + 2 (offset-bytesize) +
// 2 (constant) + 8 (IFD0 offset).
const bigTIFFMinHeaderLen = 16

// bigTIFFOffsetBytesize is the only valid value for bytes [4:6] of the
// BigTIFF header.  Any other value means the file is invalid or uses a future
// variant not handled here.  BigTIFF spec §2.
const bigTIFFOffsetBytesize = 8

// bigTIFFMaxIFDEntries caps the entry count read from a single BigTIFF IFD to
// prevent DoS via a crafted count that would exhaust memory.  Real BigTIFF
// files never approach this — it is purely a safety bound.
//
// Classic TIFF uses uint16 (max 65535) for entry count; BigTIFF uses uint64.
// We apply the same 65535 cap here: if an IFD claims more entries than a
// classic TIFF header can even hold, the file is either corrupt or malicious.
const bigTIFFMaxIFDEntries = 65535

// extractBigTIFF parses the BigTIFF header, validates the offset-bytesize
// field, then scans IFD0 for IPTC (0x83BB) and XMP (0x02BC) payloads.
//
// BigTIFF spec §2 (Aware Systems / libtiff):
//
//	bytes  0-1: byte order marker ("II" or "MM")
//	bytes  2-3: magic = 43 (0x002B)
//	bytes  4-5: bytesize-of-offsets — MUST equal 8; reject any other value
//	bytes  6-7: constant = 0 — SHOULD equal 0 (reserved/padding)
//	bytes  8-15: IFD0 offset (uint64, in file byte order)
//
// Anti-DoS invariants carried over from the classic path:
//   - IFD entry count is capped at bigTIFFMaxIFDEntries.
//   - Every uint64 arithmetic step that could overflow is guarded before
//     the multiplication/addition is performed.
//   - All slice accesses are bounds-checked against len(data).
//   - No memory is allocated proportional to claimed counts until the
//     bounds are verified.
func extractBigTIFF(data []byte, order binary.ByteOrder) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	// Minimum 16-byte header required.
	if len(data) < bigTIFFMinHeaderLen {
		return nil, nil, nil, ErrFileTooShort
	}

	// BigTIFF spec §2: bytes [4:6] must be 8.
	offsetBytesize := order.Uint16(data[4:])
	if offsetBytesize != bigTIFFOffsetBytesize {
		return nil, nil, nil, fmt.Errorf("tiff: BigTIFF offset bytesize = %d, must be 8: %w",
			offsetBytesize, ErrUnsupportedMagic)
	}
	// bytes [6:7] should be 0 (reserved). We warn-and-continue rather than
	// reject, to handle any future minor variants that set this field.
	// (BigTIFF spec §2: "constant 0x0000"; validation is advisory.)

	// IFD0 offset is a uint64 at bytes [8:16].
	ifd0Off := order.Uint64(data[8:])

	// The whole data IS the EXIF payload (BigTIFF is itself a TIFF container).
	rawEXIF = data
	rawIPTC, rawXMP = extractTagValuesBigTIFF(data, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
}

// extractTagValuesBigTIFF scans a single BigTIFF IFD at ifd0Off within data
// for IPTC (0x83BB) and XMP (0x02BC) tags and returns their raw byte values.
//
// BigTIFF IFD layout (BigTIFF spec §2):
//
//	bytes 0-7:   entry count (uint64)
//	per entry (20 bytes each):
//	  bytes 0-1:   tag (uint16)
//	  bytes 2-3:   type (uint16)
//	  bytes 4-11:  count (uint64)
//	  bytes 12-19: value-or-offset (uint64)
//	    — inline when typeSizeBigTIFF(type)*count <= 8
//	    — otherwise: 64-bit file offset to the value data
//	after entries:
//	  bytes 0-7:   next-IFD offset (uint64); 0 = end of chain
//
// Anti-DoS: entry count is capped at bigTIFFMaxIFDEntries; every arithmetic
// step that could overflow uint64 is checked before the operation.
func extractTagValuesBigTIFF(data []byte, ifd0Off uint64, order binary.ByteOrder) (rawIPTC, rawXMP []byte) { //nolint:cyclop,gocyclo // BigTIFF IFD scan mirrors extractTagValues but with 8-byte fields; splitting reduces clarity
	// Guard: IFD offset + 8-byte count field must fit in data.
	if ifd0Off > uint64(len(data)) || uint64(len(data))-ifd0Off < 8 {
		return nil, nil
	}

	count := order.Uint64(data[ifd0Off:])
	// Cap the entry count to prevent DoS via huge count values.
	count = min(count, bigTIFFMaxIFDEntries)

	// Each BigTIFF entry is 20 bytes; validate the total entry area fits.
	// Use uint64 arithmetic; check for overflow before multiplication.
	const bigTIFFEntrySize = 20
	if count > (uint64(len(data))-ifd0Off-8)/bigTIFFEntrySize {
		// Entry list is truncated — clamp to what fits.
		count = (uint64(len(data)) - ifd0Off - 8) / bigTIFFEntrySize
	}

	pos := ifd0Off + 8 // first entry starts after the 8-byte count field

	for i := uint64(0); i < count; i++ { //nolint:intrange // BigTIFF parser: loop variable is a byte-slice offset multiplier
		e := pos + i*bigTIFFEntrySize
		// Safety: each entry is 20 bytes; confirmed by count clamping above.
		if e+bigTIFFEntrySize > uint64(len(data)) {
			break
		}

		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint64(data[e+4:])

		// Skip entries with unknown types — we cannot compute a valid value size.
		sz := typeSizeBigTIFF(typ)
		if sz == 0 {
			continue
		}

		// Guard against count*sz overflow before multiplying.
		// sz <= 8 and cnt is uint64; if cnt > MaxUint64/sz the product wraps.
		// Equivalent condition: cnt > maxUint64/sz.
		const maxUint64 = ^uint64(0)
		if sz != 0 && cnt > maxUint64/sz {
			continue // would overflow: entry is corrupt/malicious
		}
		total := sz * cnt

		var v []byte
		// BigTIFF spec §2: inline threshold is 8 bytes (vs 4 in classic TIFF).
		if total <= 8 {
			// Value fits in the 8-byte value-or-offset field (bytes e+12 to e+20).
			v = data[e+12 : e+12+total]
		} else {
			// Value is out-of-line; bytes [e+12:e+20] hold a uint64 file offset.
			off := order.Uint64(data[e+12:])
			// Guard: off + total must not overflow and must be within data.
			if off > uint64(len(data)) || total > uint64(len(data))-off {
				continue // out-of-bounds: skip entry
			}
			v = data[off : off+total]
		}

		switch tag {
		case 0x83BB: // IPTC-NAA
			// ROBUST-16 (iptc.md §5): type-aware trimming — only TypeLong (4)
			// gets structural-padding trimmed; TypeByte/Undefined are returned
			// as-is. Same logic as the classic-TIFF path. Task #153.
			if len(v) > 0 {
				if typ == 4 { // TypeLong: trim structural alignment padding
					rawIPTC = trimIPTCLongPadding(v)
				} else {
					rawIPTC = v // TypeByte / TypeUndefined: no trim (ROBUST-16)
				}
				if len(rawIPTC) == 0 {
					rawIPTC = nil
				}
			}
		case 0x02BC: // XMP
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
}
