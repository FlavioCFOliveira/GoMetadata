// Package tiff implements extraction and injection of metadata within TIFF
// container files. TIFF stores EXIF in a SubIFD (tag 0x8769), IPTC in tag
// 0x83BB, and XMP in tag 0x02BC (TIFF Technical Note 3).
package tiff

import (
	"bytes"
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
	// BigTIFF uses 43 (0x002B) and has a fundamentally different header layout
	// (8-byte IFD offsets, 16-byte header). Reject BigTIFF explicitly so that
	// the caller gets an actionable error rather than a silently misparsed result.
	magic := order.Uint16(data[2:])
	if magic != 0x002A {
		return nil, nil, nil, fmt.Errorf("tiff: unsupported magic 0x%04X (classic TIFF 0x002A required; BigTIFF 0x002B is not supported): %w",
			magic, ErrUnsupportedMagic)
	}

	// The whole TIFF data IS the EXIF payload (TIFF §2).
	rawEXIF = data

	ifd0Off := order.Uint32(data[4:])
	rawIPTC, rawXMP = extractTagValues(data, ifd0Off, order)
	return rawEXIF, rawIPTC, rawXMP, nil
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
			// When stored as TypeLong (the conventional encoding per ExifTool /
			// Adobe XMP Spec), the value area is padded to a 4-byte boundary
			// with zero bytes.  Strip those trailing zeros so callers always
			// receive the original unpadded IPTC content.
			//
			// This is safe because IPTC IIM content is self-framing: every
			// dataset begins with the tag marker 0x1C (IIM §1.6). Trailing zero
			// bytes are never a valid dataset prefix and are silently skipped by
			// the IIM scanner.  TypeUndefined / TypeByte encodings carry no
			// padding; trimming trailing zeros is harmless for those too.
			rawIPTC = bytes.TrimRight(v, "\x00")
			if len(rawIPTC) == 0 {
				rawIPTC = nil
			}
		case 0x02BC: // XMP
			rawXMP = v
		}
	}
	return rawIPTC, rawXMP
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
