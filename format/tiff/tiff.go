// Package tiff implements extraction and injection of metadata within TIFF
// container files. TIFF stores EXIF in a SubIFD (tag 0x8769), IPTC in tag
// 0x83BB, and XMP in tag 0x02BC (TIFF Technical Note 3).
package tiff

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Extract reads metadata payloads from a TIFF container.
// rawEXIF is the entire TIFF byte stream (TIFF itself is the EXIF container).
// rawIPTC and rawXMP are read from the respective IFD0 tags.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: seek: %w", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("tiff: read: %w", err)
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
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, _ bool) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("tiff: seek: %w", err)
	}

	// Determine the base TIFF data to work with.
	var base []byte
	if rawEXIF != nil {
		base = rawEXIF
	} else {
		var err error
		base, err = io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("tiff: read: %w", err)
		}
	}

	// Pass-through: no metadata changes requested.
	if rawIPTC == nil && rawXMP == nil {
		if _, err := w.Write(base); err != nil {
			return fmt.Errorf("tiff: write: %w", err)
		}
		return nil
	}

	// Copy-and-relocate path: rebuild IFD structure with upserted metadata and
	// appended image-data blocks at corrected offsets (epic #33, tasks #92/#93).
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

// InjectWithEXIFNEF is the NEF-specific variant of InjectWithEXIF.
//
// It runs the Nikon-specific preprocessing step (extend MakerNote blob,
// enumerate PreviewIFD image block) before the standard TIFF copy-and-relocate
// algorithm, and patches the MakerNote-relative PreviewIFD offsets after encoding.
//
// This is the entry point used by gometadata.Write for FormatNEF.
// See relocate_nef.go for the full algorithm description.
func InjectWithEXIFNEF(originalBytes []byte, modifiedEXIF *exif.EXIF, rawIPTC, rawXMP []byte, w io.Writer) error {
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
	if int(ifd0Off)+2 > len(data) {
		return nil, nil
	}
	count := int(order.Uint16(data[ifd0Off:]))
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
func typeSize(t uint16) uint32 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
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
func typeSizeBigTIFF(t uint16) uint64 {
	switch t {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9, 11: // LONG, SLONG, FLOAT
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
