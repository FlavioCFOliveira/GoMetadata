package tiff

// relocate_arw.go — Sony ARW-specific copy-and-relocate preprocessing (task #103).
//
// Problem: Sony ARW files embed two structures that use TIFF-absolute offsets
// which become stale after standard IFD re-encoding:
//
//   1. Sony MakerNote (ExifIFD tag 0x927C):
//      Unlike Canon (which uses blob-relative offsets), Sony MakerNote IFDs store
//      TIFF-absolute offsets in their out-of-line val_or_off fields.  After the
//      MakerNote blob is moved to a new position in the rebuilt TIFF stream those
//      pointers still reference the old absolute positions, causing all 34 MakerNote
//      IFD entries that have OOL data to be silently lost or misread.
//
//      Fix: after the main TIFF re-encoding (exif.Encode), locate the MakerNote
//      blob in finalTIFF, scan its IFD for all out-of-line val_or_off fields, and
//      rebase them with the delta = new_blob_abs − old_blob_abs.  Because all Sony
//      MakerNote OOL data is contained within the blob (confirmed by empirical
//      inspection of Sony DSLR-A500.arw), the relative offsets within the blob are
//      unchanged; only the base changes.
//
//   2. SR2Private IFD block (IFD0 tag 0xC634):
//      Sony ARW stores a private IFD block called "SR2Private" at a TIFF-absolute
//      offset.  The IFD0 tag 0xC634 holds this offset as 4 inline bytes (type=Byte,
//      count=4 — total=4, so the bytes live in the val_or_off field directly).
//
//      The SR2Private block contains:
//        a. An SR2 IFD at the start of the block with 10 entries.
//           Three entries have out-of-line value areas (0x7241, 0x7242, 0x7250)
//           that are captured verbatim in rawBytes — their OOL pointers must be
//           rebased after placement at the new file offset.
//        b. Two entries with TIFF-absolute offset values (not OOL pointers):
//           0x7200 (SR2SubIFDOffset): absolute file offset to the encrypted
//                  SR2SubIFD blob that follows the OOL value area.
//           0x7240 (IDC_IFD): absolute file offset to an empty IDC IFD that
//                  follows the encrypted blob.
//        c. An encrypted SR2SubIFD blob (length from 0x7201 entry).
//        d. An empty IDC_IFD (2 bytes count + 4 bytes next-IFD = 6 bytes total).
//
//      The entire SR2 block (from SR2 IFD start through IDC_IFD end) is extracted
//      verbatim, appended to the output after the main EXIF block (analogous to
//      SubIFD rawBytes), and its internal TIFF-absolute pointers are rebased.
//      The 0xC634 inline 4-byte value in IFD0 is then patched in finalTIFF to
//      point to the new SR2 IFD position.
//
// Algorithm (relocateTIFFFromParsedARW):
//
//   Step A — extract Sony ARW-specific info:
//     a1. Find the MakerNote (0x927C) in ExifIFD; record its original file offset
//         and the OOL entry positions within the blob for later rebasing.
//     a2. Find the SR2Private (0xC634) inline offset; read the full SR2 block;
//         record internal absolute-pointer positions for later rebasing.
//
//   Steps 2–11 — run the standard relocateTIFFFromParsed algorithm, with the
//     SR2 block appended as extra raw metadata bytes (analogous to SubIFD rawBytes)
//     BEFORE the image data blocks.
//
//   Step C (post-encode) — patch finalTIFF:
//     c1. Locate MakerNote blob in finalTIFF; compute new blob absolute position;
//         rebase all OOL val_or_off entries inside the blob.
//     c2. Locate 0xC634 entry in IFD0 of finalTIFF; overwrite the inline 4-byte
//         value with the new SR2 block absolute offset.
//     c3. Within the SR2 rawBytes (now placed at sr2Info.newOffset), rebase:
//         - OOL pointer fields (0x7241, 0x7242, 0x7250 val_or_off) to new positions.
//         - Absolute-offset tags (0x7200, 0x7240 val_or_off) to new positions.
//
// Sony ARW format references:
//   - ExifTool Sony.pm (canonical reference for Sony private IFD structures).
//   - Sony DSLR-A500.arw empirical analysis (task #103).
//   - TIFF 6.0 §2: IFD entry layout, inline vs. out-of-line values.
//   - EXIF §4.6.5, tag 0x927C (MakerNote).

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// Sentinel errors for the ARW-specific relocation subsystem.
var (
	// ErrSonyMakerNoteNotFound is returned when the MakerNote blob cannot be
	// located in finalTIFF during the post-encode patching step.
	ErrSonyMakerNoteNotFound = errors.New("tiff: Sony MakerNote not found in finalTIFF for offset rebase")

	// ErrSonySR2PatchFailed is returned when the 0xC634 patching step fails.
	ErrSonySR2PatchFailed = errors.New("tiff: Sony SR2Private patch failed")

	// ErrSonySR2BlockOutOfBounds is returned when the computed SR2 block extent
	// exceeds the source TIFF buffer length.
	ErrSonySR2BlockOutOfBounds = errors.New("tiff: Sony SR2Private block out of bounds")
)

// Sony ARW-specific tag IDs referenced during ARW relocation.
// Source: ExifTool Sony.pm.
const (
	// sonyTagSR2Private is the IFD0 tag that holds a TIFF-absolute offset to the
	// SR2 IFD block as 4 inline bytes (type=Byte, count=4).
	// TIFF 6.0 §2: for type=Byte, count=4, total=4 ≤ 4, so the bytes are stored
	// inline in the val_or_off field of the IFD entry.
	// ExifTool Sony.pm: tag 0xC634, "SR2Private".
	sonyTagSR2Private = exif.TagID(0xC634)

	// sonyTagMakerNote is the ExifIFD tag that holds the Sony MakerNote blob.
	// Same as exif.TagMakerNote (0x927C); duplicated here for clarity.
	sonyTagMakerNote = exif.TagID(0x927C)

	// sr2TagSubIFDOffset is the SR2 IFD entry that holds the absolute file offset
	// to the encrypted SR2SubIFD blob (TypeLong, Count=1, inline).
	// ExifTool Sony.pm: tag 0x7200, "SR2SubIFDOffset".
	sr2TagSubIFDOffset = exif.TagID(0x7200)

	// sr2TagSubIFDLength is the SR2 IFD entry that holds the byte length of the
	// encrypted SR2SubIFD blob (TypeLong, Count=1, inline).
	// ExifTool Sony.pm: tag 0x7201, "SR2SubIFDLength".
	sr2TagSubIFDLength = exif.TagID(0x7201)

	// sr2TagIDCIFD is the SR2 IFD entry that holds the absolute file offset to
	// the (empty) IDC_IFD (TypeLong, Count=1, inline).
	// ExifTool Sony.pm: tag 0x7240, "IDC_IFD".
	sr2TagIDCIFD = exif.TagID(0x7240)

	// sr2TagSubIFDKey is the SR2 IFD entry that holds the 32-bit key used to
	// decrypt the SR2SubIFD blob (TypeUndefined, Count=4, inline as a uint32).
	// ExifTool Sony.pm: tag 0x7221, "SR2SubIFDKey".
	// The decryption algorithm is a PRNG-seeded XOR stream cipher (Sony proprietary).
	sr2TagSubIFDKey = exif.TagID(0x7221)

	// idcIFDHeaderSize is the fixed size of the empty IDC_IFD:
	// count(2) + entries(0×12) + nextIFD(4) = 6 bytes.
	idcIFDHeaderSize = 6
)

// sonySR2Info collects the Sony SR2Private block info and MakerNote info needed
// for the post-encode patching step.
type sonySR2Info struct {
	// sr2RawBytes is a verbatim copy of the full SR2 block extracted from base.
	// It is appended to finalTIFF after the main EXIF block (before image data).
	sr2RawBytes []byte

	// sr2SrcOffset is the original absolute position of the SR2 IFD block
	// in the source TIFF stream.  Used to rebase the internal OOL pointers.
	sr2SrcOffset uint32

	// sr2NewOffset is filled in by assignSR2Offset when the SR2 block is
	// appended to the output.
	sr2NewOffset uint32

	// sr2SubIFDOffset is the original absolute file offset to the encrypted
	// SR2SubIFD blob (value of SR2 IFD tag 0x7200).
	sr2SubIFDOffset uint32

	// sr2SubIFDLength is the byte length of the encrypted SR2SubIFD blob
	// (value of SR2 IFD tag 0x7201).
	sr2SubIFDLength uint32

	// sr2IDCIFDOffset is the original absolute file offset to the empty IDC_IFD
	// (value of SR2 IFD tag 0x7240).  May be 0 if the tag is absent.
	sr2IDCIFDOffset uint32

	// sr2SubIFDKey is the 32-bit decryption key for the encrypted SR2SubIFD blob.
	// It is stored in SR2 IFD tag 0x7221 (SR2SubIFDKey, TypeUndefined count=4,
	// inline as a little-endian uint32).
	// A value of 0 means the key was not found; the blob is copied verbatim in
	// that case.
	sr2SubIFDKey uint32

	// mnEntry is the exif.IFDEntry for tag 0x927C in the ExifIFD.
	// Used to locate the MakerNote blob's original position.
	mnEntry *exif.IFDEntry

	// mnSrcOffset is the original absolute position of the MakerNote blob in base.
	// All OOL val_or_off fields in the MakerNote IFD are TIFF-absolute; after
	// re-encoding they must be rebased by delta = new_mn_abs − mnSrcOffset.
	mnSrcOffset uint32

	// makerNoteOrder is the byte order of the outer TIFF (same as MakerNote IFD).
	// Sony MakerNote is a plain IFD, not an embedded TIFF with its own byte order.
	makerNoteOrder binary.ByteOrder
}

// extractSonySR2Info inspects the parsed EXIF for a Sony ARW MakerNote and
// SR2Private block, and returns a sonySR2Info describing both.
//
// base is the original TIFF byte stream.
// e is the parsed (and possibly mutated) EXIF struct.
// order is the outer TIFF byte order (all Sony ARW files are little-endian).
//
// Returns nil when neither a Sony MakerNote nor an SR2Private block is found.
// Returns a non-nil error only when the SR2 block is structurally unreadable or
// out of bounds.
//
//nolint:cyclop,gocyclo // Sony structure inspection; complexity is inherent to the multi-step spec lookup
func extractSonySR2Info(base []byte, e *exif.EXIF, order binary.ByteOrder) (*sonySR2Info, error) {
	if e == nil || e.IFD0 == nil {
		return nil, nil //nolint:nilnil // nil info means "no Sony-specific block found"
	}

	info := &sonySR2Info{makerNoteOrder: order}

	// ── Step A1: Sony MakerNote ──────────────────────────────────────────────
	// Find the MakerNote entry (0x927C) in the ExifIFD.
	if e.ExifIFD != nil {
		info.mnEntry = e.ExifIFD.Get(sonyTagMakerNote)
	}
	if info.mnEntry != nil {
		// MakerNoteOffset is set by exif.Parse when the MakerNote is an OOL entry.
		// It gives the original absolute file position of the blob.
		// EXIF 2.32 §4.6.5: MakerNote (0x927C) is TypeUndefined, OOL when count > 4.
		info.mnSrcOffset = e.MakerNoteOffset
		if info.mnSrcOffset == 0 {
			// MakerNoteOffset absent (< 4 bytes); search base for the blob.
			info.mnSrcOffset = findBlobInBase(base, info.mnEntry.Value)
		}
		if info.mnSrcOffset == 0 {
			// Could not locate the MakerNote blob — skip MakerNote rebasing.
			info.mnEntry = nil
		}
	}

	// ── Step A2: SR2Private block ────────────────────────────────────────────
	// Find the SR2Private (0xC634) entry in IFD0.
	sr2Entry := e.IFD0.Get(sonyTagSR2Private)
	if sr2Entry == nil {
		// No SR2Private; skip SR2 block relocation.
		// Return info only if MakerNote relocation is needed.
		if info.mnEntry == nil {
			return nil, nil //nolint:nilnil // no Sony-specific structures found
		}
		return info, nil
	}

	// 0xC634 is type=Byte, count=4, total=4 → inline.
	// The 4 inline bytes encode a TIFF-absolute offset to the SR2 IFD block.
	// Per TIFF 6.0 §2: for inline values the val_or_off field holds the value
	// bytes; the LE uint32 interpretation gives the offset.
	if len(sr2Entry.Value) < 4 {
		// Malformed entry; skip SR2 relocation.
		return info, nil
	}
	sr2IFDOffset := order.Uint32(sr2Entry.Value)
	if sr2IFDOffset == 0 || uint64(sr2IFDOffset)+2 > uint64(len(base)) {
		// Offset out of range; skip.
		return info, nil
	}

	// Parse the SR2 IFD to find SR2SubIFDOffset (0x7200), SR2SubIFDLength (0x7201),
	// and IDC_IFD (0x7240).
	sr2SubIFDOffset, sr2SubIFDLength, idcIFDOffset, ok := parseSR2IFDEntries(base, sr2IFDOffset, order)
	if !ok {
		// SR2 IFD is unreadable; skip.
		return info, nil
	}

	// Compute the full extent of the SR2 block:
	//   [sr2IFDOffset  ...  IDC_IFD end]
	//
	// The layout (empirically confirmed for Sony DSLR-A500.arw) is:
	//   SR2 IFD fixed block (2+10×12+4 = 126 bytes)
	//   SR2 IFD OOL value areas (0x7241, 0x7242, 0x7250)
	//   Alignment pad (0–1 bytes, to word boundary)
	//   Encrypted SR2SubIFD blob (sr2SubIFDOffset, length sr2SubIFDLength)
	//   Empty IDC_IFD (6 bytes: count=0 + nextIFD=0)
	//
	// ExifTool Sony.pm: SR2SubIFDOffset is the absolute file offset of the blob.
	// The total SR2 block ends at the close of the IDC_IFD.
	var sr2BlockEnd uint64
	if sr2SubIFDOffset > 0 && sr2SubIFDLength > 0 {
		// Preferred: end of encrypted blob + IDC_IFD (6 bytes).
		sr2BlockEnd = uint64(sr2SubIFDOffset) + uint64(sr2SubIFDLength)
		if idcIFDOffset > 0 {
			// IDC_IFD follows the blob; include its 6 bytes.
			idcEnd := uint64(idcIFDOffset) + idcIFDHeaderSize
			if idcEnd > sr2BlockEnd {
				sr2BlockEnd = idcEnd
			}
		}
	} else {
		// Fallback: end of SR2 IFD OOL value areas only.
		sr2BlockEnd = computeSR2IFDExtent(base, sr2IFDOffset, order)
	}

	if sr2BlockEnd <= uint64(sr2IFDOffset) {
		return info, nil // degenerate block
	}
	if sr2BlockEnd > uint64(len(base)) {
		return nil, fmt.Errorf("%w (sr2Start=%d blockEnd=%d baseLen=%d)",
			ErrSonySR2BlockOutOfBounds, sr2IFDOffset, sr2BlockEnd, len(base))
	}

	blockSize := sr2BlockEnd - uint64(sr2IFDOffset)
	rawBytes := make([]byte, blockSize)
	copy(rawBytes, base[sr2IFDOffset:sr2BlockEnd])

	info.sr2RawBytes = rawBytes
	info.sr2SrcOffset = sr2IFDOffset
	info.sr2SubIFDOffset = sr2SubIFDOffset
	info.sr2SubIFDLength = sr2SubIFDLength
	info.sr2IDCIFDOffset = idcIFDOffset
	info.sr2SubIFDKey = readSR2SubIFDKey(base, sr2IFDOffset, order)

	return info, nil
}

// parseSR2IFDEntries scans the SR2 IFD at sr2Off in base for the three key
// entries: SR2SubIFDOffset (0x7200), SR2SubIFDLength (0x7201), IDC_IFD (0x7240).
//
// Returns (sr2SubIFDOffset, sr2SubIFDLength, idcIFDOffset, true) on success.
// Returns (0, 0, 0, false) if the IFD cannot be read.
//
// All three tags are TypeLong (4), Count=1, inline.
// ExifTool Sony.pm confirms inline encoding for these tags.
func parseSR2IFDEntries(base []byte, sr2Off uint32, order binary.ByteOrder) (
	sr2SubIFDOffset, sr2SubIFDLength, idcIFDOffset uint32, ok bool,
) {
	if uint64(sr2Off)+2 > uint64(len(base)) {
		return 0, 0, 0, false
	}
	count := int(order.Uint16(base[sr2Off:]))
	pos := int(sr2Off) + 2
	if pos+count*12 > len(base) {
		return 0, 0, 0, false
	}

	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		tag := exif.TagID(order.Uint16(base[e:]))
		// All three tags are TypeLong (4), Count=1, value inline.
		entryType := order.Uint16(base[e+2:])
		entryCount := order.Uint32(base[e+4:])
		if entryType != 4 || entryCount != 1 { //nolint:mnd // 4=TypeLong per TIFF 6.0 §2
			continue
		}
		val := order.Uint32(base[e+8:])
		switch tag {
		case sr2TagSubIFDOffset:
			sr2SubIFDOffset = val
		case sr2TagSubIFDLength:
			sr2SubIFDLength = val
		case sr2TagIDCIFD:
			idcIFDOffset = val
		}
	}
	return sr2SubIFDOffset, sr2SubIFDLength, idcIFDOffset, true
}

// computeSR2IFDExtent returns the file offset one past the last byte of the SR2
// IFD's fixed block + all its out-of-line value areas.
//
// This is used as a fallback when SR2SubIFDOffset/Length are absent.
func computeSR2IFDExtent(base []byte, sr2Off uint32, order binary.ByteOrder) uint64 {
	if uint64(sr2Off)+2 > uint64(len(base)) {
		return 0
	}
	count := int(order.Uint16(base[sr2Off:]))
	// Fixed block: count(2) + entries(count×12) + nextIFD(4).
	fixedEnd := uint64(sr2Off) + 2 + uint64(count)*12 + 4 //nolint:gosec,mnd // G115: count is IFD entry count (< 65535); per TIFF 6.0 §2
	maxEnd := fixedEnd
	pos := int(sr2Off) + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		entryType := order.Uint16(base[e+2:])
		entryCount := order.Uint32(base[e+4:])
		sz := typeSize(entryType)
		if sz == 0 || entryCount == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCount)
		if total <= 4 {
			continue // inline
		}
		relOff := order.Uint32(base[e+8:])
		oolEnd := min(uint64(relOff)+total, uint64(len(base)))
		if oolEnd > maxEnd {
			maxEnd = oolEnd
		}
	}
	return maxEnd
}

// readSR2SubIFDKey scans the SR2 IFD at sr2Off for tag 0x7221 (SR2SubIFDKey)
// and returns its 32-bit inline value.  Returns 0 if not found.
//
// ExifTool Sony.pm: tag 0x7221 is TypeUndefined, Count=4, inline.
// The 4 bytes store the key as a little-endian uint32 (independent of TIFF byte order).
func readSR2SubIFDKey(base []byte, sr2Off uint32, order binary.ByteOrder) uint32 {
	if uint64(sr2Off)+2 > uint64(len(base)) {
		return 0
	}
	count := int(order.Uint16(base[sr2Off:]))
	pos := int(sr2Off) + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(base) {
			break
		}
		tag := exif.TagID(order.Uint16(base[e:]))
		if tag != sr2TagSubIFDKey {
			continue
		}
		// The key is stored in the val_or_off field as 4 inline bytes.
		// ExifTool Sony.pm reads it as a uint32 (always little-endian per Sony ARW).
		return binary.LittleEndian.Uint32(base[e+8:])
	}
	return 0
}

// sr2CryptBlob applies Sony's SR2SubIFD stream cipher to buf in-place.
// The cipher is symmetric (XOR-based): calling it twice with the same key restores the original.
//
// Algorithm from ExifTool Sony.pm sub Decrypt (lines 11451–11472):
//
//  1. Generate pad[0..126] from the 32-bit key using a PRNG.
//  2. Treat buf as big-endian uint32 words.
//  3. For each word j (i = 0x7f+j):
//     pad[i & 0x7f] = pad[(i+1)&0x7f] ^ pad[(i+65)&0x7f]
//     word[j] ^= pad[i & 0x7f]
//
// Note: only complete 4-byte words are processed; any trailing bytes are unchanged.
func sr2CryptBlob(buf []byte, key uint32) {
	// The seeding loop runs for indices 0..126 (0x7e).
	// The XOR loop starts at i=0x7f=127, which writes pad[127] on the first iteration.
	// Therefore pad must have 128 slots (indices 0..127).
	const padLen = 128
	var pad [padLen]uint32

	// ── Phase 1: seed pad[0..3] from key ────────────────────────────────────
	// ExifTool Sony.pm lines 11457–11462.
	for i := range 4 {
		lo := (key&0xffff)*0x0edd + 1
		hi := (key>>16)*0x0edd + (key&0xffff)*0x02e9 + (lo >> 16) //nolint:mnd // Sony PRNG constants
		pad[i] = ((hi & 0xffff) << 16) | (lo & 0xffff)
		key = pad[i]
	}
	// ── Phase 2: extend pad[3] and seed pad[4..126] ──────────────────────────
	pad[3] = (pad[3] << 1) | ((pad[0] ^ pad[2]) >> 31)
	for i := 4; i < padLen; i++ {
		pad[i] = ((pad[i-4] ^ pad[i-2]) << 1) | ((pad[i-3] ^ pad[i-1]) >> 31)
	}

	// ── Phase 3: XOR each big-endian uint32 word with the rolling pad ────────
	// ExifTool Sony.pm lines 11468–11470:
	//   for ($i=0x7f,$j=0; $j<$words; ++$i,++$j) {
	//     $data[$j] ^= $pad[$i & 0x7f] = $pad[($i+1)&0x7f] ^ $pad[($i+65)&0x7f];
	//   }
	words := len(buf) / 4
	for j := range words {
		i := (0x7f + j) & 0x7f //nolint:mnd // 0x7f = padLen-1; ExifTool starts at i=0x7f
		// Rolling pad update: store into pad[i] before XOR (Perl assignment-in-expression).
		newPad := pad[(i+1)&0x7f] ^ pad[(i+65)&0x7f] //nolint:mnd // 65 = ExifTool Decrypt constant
		pad[i] = newPad
		w := binary.BigEndian.Uint32(buf[j*4:])
		binary.BigEndian.PutUint32(buf[j*4:], w^newPad)
	}
}

// patchSR2SubIFDPointers decrypts the SR2SubIFD blob within rawSR2 (which has
// already been placed at newSR2BaseOff in the output), rebases all TIFF-absolute
// pointer fields at all IFD nesting levels, and re-encrypts the blob in-place.
//
// rawSR2 is the full SR2 block (SR2 IFD + OOL + encrypted blob + IDC IFD).
// The encrypted blob starts at (sr2SubIFDSrcOff − srcSR2BaseOff) within rawSR2.
//
// The rebasing algorithm walks all IFDs reachable within the SR2 block:
//  1. Root IFD (the decrypted blob itself): rebase each OOL val_or_off.
//  2. TypeLong OOL arrays may contain sub-IFD offsets (e.g., tag 0x74C0 = SR2DataIFD).
//     Rebase each element within [srcSR2BaseOff, srcSR2BaseOff+len(rawSR2)).
//  3. For each sub-IFD (referenced by 0x74C0 array elements), rebase its OOL entries.
//
// Pointer rebasing: new_abs = newSR2BaseOff + (old_abs − srcSR2BaseOff).
// Only values within the SR2 block address range are treated as pointers.
//
// ExifTool Sony.pm: Decrypt / WriteSR2 / ProcessSR2.
//
//nolint:cyclop // multi-level pointer rebasing + decrypt/re-encrypt is inherently complex
func patchSR2SubIFDPointers(
	rawSR2 []byte,
	srcSR2BaseOff, newSR2BaseOff uint32,
	sr2SubIFDSrcOff uint32, // original absolute offset of the encrypted blob
	sr2SubIFDLen uint32, // byte length of the blob
	key uint32,
	order binary.ByteOrder,
) {
	if key == 0 || sr2SubIFDLen == 0 {
		return // no key or empty blob; skip
	}

	// Convert sr2SubIFDSrcOff (absolute in source file) to offset within rawSR2.
	if uint64(sr2SubIFDSrcOff) < uint64(srcSR2BaseOff) {
		return // blob precedes the SR2 block — unexpected; skip
	}
	blobRelOff := sr2SubIFDSrcOff - srcSR2BaseOff
	blobEnd := uint64(blobRelOff) + uint64(sr2SubIFDLen)
	if blobEnd > uint64(len(rawSR2)) {
		return // blob extends beyond rawSR2 — skip
	}

	// Work on a slice of rawSR2 covering the encrypted blob.
	blob := rawSR2[blobRelOff:blobEnd]

	// ── Decrypt ──────────────────────────────────────────────────────────────
	sr2CryptBlob(blob, key)

	if len(blob) < 2 {
		sr2CryptBlob(blob, key) // re-encrypt even if too short
		return
	}

	// ── Rebase all IFD pointers in the decrypted blob ─────────────────────────
	// We perform a two-pass rebase:
	// Pass 1: walk the root IFD, collecting sub-IFD offsets from TypeLong OOL arrays.
	// Pass 2: walk each sub-IFD (SR2DataIFD), rebasing its OOL entries.
	// Both passes use the same rebasing formula.
	rebaseIFDInBlob(blob, rawSR2, srcSR2BaseOff, newSR2BaseOff, order, true)

	// ── Re-encrypt ───────────────────────────────────────────────────────────
	// The cipher is its own inverse: applying it again with the same key re-encrypts.
	sr2CryptBlob(blob, key)
}

// rebaseIFDInBlob rebases all OOL val_or_off pointers in the IFD starting at
// blob[0], and optionally traverses sub-IFDs referenced by TypeLong OOL arrays.
//
// blob is the decrypted SR2SubIFD (starts at blob[0]).
// rawSR2 is the full SR2 block (blob is a sub-slice of it).
// srcOff / newOff are the original/new absolute file offsets of rawSR2.
// followSubIFDs: when true, also rebase OOL entries of sub-IFDs referenced
// by TypeLong OOL arrays whose values fall within the SR2 block.
//
//nolint:cyclop,gocyclo // IFD walking with optional sub-IFD recursion is inherently branchy
func rebaseIFDInBlob(blob, rawSR2 []byte, srcOff, newOff uint32, order binary.ByteOrder, followSubIFDs bool) {
	if len(blob) < 2 {
		return
	}
	entryCount := int(order.Uint16(blob[0:]))
	pos := 2

	for i := range entryCount {
		e := pos + i*12
		if e+12 > len(blob) {
			break
		}
		entryType := order.Uint16(blob[e+2:])
		entryCnt := order.Uint32(blob[e+4:])
		sz := typeSize(entryType)
		if sz == 0 || entryCnt == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCnt)
		if total <= 4 {
			continue // inline; no pointer to rebase
		}

		// OOL entry: val_or_off is a TIFF-absolute file offset.
		oldVOO := order.Uint32(blob[e+8:])
		if uint64(oldVOO) < uint64(srcOff) {
			continue // points before SR2 block — skip
		}
		relOff := oldVOO - srcOff
		if uint64(relOff)+total > uint64(len(rawSR2)) {
			continue // out of rawSR2 bounds — skip
		}
		// Rebase the OOL pointer.
		newVOO := newOff + relOff
		order.PutUint32(blob[e+8:], newVOO)

		if !followSubIFDs || sz != 4 {
			continue // only TypeLong arrays may carry sub-IFD offsets
		}

		// Scan each uint32 value in the OOL array.
		// Values that fall within [srcOff, srcOff+len(rawSR2)) are treated as
		// sub-IFD absolute offsets (e.g., 0x74C0 = SR2DataIFD pointer array).
		for j := range int(entryCnt) {
			valRawOff := int(relOff) + j*4
			if valRawOff+4 > len(rawSR2) {
				break
			}
			v := order.Uint32(rawSR2[valRawOff:])
			if uint64(v) < uint64(srcOff) || uint64(v) >= uint64(srcOff)+uint64(len(rawSR2)) {
				continue // not within the SR2 block; not a pointer
			}
			// Rebase this sub-IFD pointer value.
			subRelOff := v - srcOff
			newV := newOff + subRelOff
			order.PutUint32(rawSR2[valRawOff:], newV)

			// Navigate the sub-IFD and rebase its OOL entries too.
			// The sub-IFD is within rawSR2 at subRelOff.
			if uint64(subRelOff)+2 > uint64(len(rawSR2)) {
				continue
			}
			subIFDSlice := rawSR2[subRelOff:]
			// Sub-IFDs at this level do NOT follow further sub-IFDs (no 4th level).
			rebaseIFDInBlob(subIFDSlice, rawSR2, srcOff, newOff, order, false)
		}
	}
}

// findBlobInBase searches base for the first occurrence of the first 8 bytes of
// blob and returns the file-absolute offset.  Returns 0 if not found.
// Used as a fallback when MakerNoteOffset is unavailable.
func findBlobInBase(base, blob []byte) uint32 {
	const matchLen = 8
	if len(blob) < matchLen || len(base) < matchLen {
		return 0
	}
	prefix := blob[:matchLen]
	for i := range len(base) - matchLen + 1 {
		match := true
		for j, b := range prefix {
			if base[i+j] != b {
				match = false
				break
			}
		}
		if match {
			return uint32(i)
		}
	}
	return 0
}

// rebaseSonyMakerNote scans the MakerNote IFD embedded in finalTIFF and
// rewrites all out-of-line val_or_off fields with new TIFF-absolute offsets.
//
// Sony MakerNote (DSLR series) is a plain TIFF IFD with TIFF-absolute offsets —
// not a self-contained blob with internal-relative offsets (ExifTool Sony.pm).
//
// Algorithm:
//  1. Locate the MakerNote blob in finalTIFF (via findOOLEntryOffset scanning ExifIFD).
//  2. Compute delta = new_mn_abs − info.mnSrcOffset.
//  3. Scan the MakerNote IFD entries; for each OOL entry (total > 4):
//     new_voo = old_voo + delta
//     Overwrite the val_or_off field in the MakerNote blob within finalTIFF.
//
// The MakerNote blob is written into finalTIFF by exif.Encode; its new absolute
// position is found via findOOLEntryOffset on ExifIFD.
//
//nolint:cyclop,gocyclo // MakerNote rebase scans all IFD entries; complexity is inherent
func rebaseSonyMakerNote(finalTIFF []byte, info *sonySR2Info, order binary.ByteOrder) error {
	if info.mnEntry == nil {
		return nil // no MakerNote to rebase
	}

	// ── Locate ExifIFD in finalTIFF ──────────────────────────────────────────
	if len(finalTIFF) < 8 {
		return nil
	}
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return nil
	}

	// Find ExifIFD pointer (tag 0x8769) in IFD0.
	exifIFDOff, found := scanIFDForTagValOrOff(finalTIFF, ifd0Off, uint16(exif.TagExifIFDPointer), order)
	if !found {
		return nil // no ExifIFD; skip
	}

	exifStart := int(exifIFDOff)
	if exifStart+2 > len(finalTIFF) {
		return nil
	}

	// ── Locate MakerNote blob in finalTIFF ───────────────────────────────────
	// findOOLEntryOffset returns the absolute position of the MakerNote blob.
	newMNAbs, found := findOOLEntryOffset(finalTIFF, exifStart, uint16(sonyTagMakerNote), order)
	if !found {
		// MakerNote not found (inline or absent); nothing to rebase.
		return nil
	}

	// ── Compute rebasing delta ────────────────────────────────────────────────
	// delta is the signed displacement of the MakerNote blob in the new stream.
	// Sony MakerNote OOL offsets are TIFF-absolute; after moving the blob from
	// mnSrcOffset to newMNAbs they all shift by the same delta.
	//
	// Empirical evidence (Sony DSLR-A500.arw): all OOL data is within the blob:
	// max(OOL_end) == mnSrcOffset + len(mnEntry.Value).
	oldMNAbs := info.mnSrcOffset
	if uint32(newMNAbs) == oldMNAbs { //nolint:gosec // G115: newMNAbs is always < len(finalTIFF) < 2^32
		return nil // blob did not move; nothing to rebase
	}

	// Verify the blob fits in the output.
	mnBlobSize := len(info.mnEntry.Value)
	if newMNAbs+mnBlobSize > len(finalTIFF) {
		return fmt.Errorf("%w: MakerNote at %d+%d out of finalTIFF bounds (%d)",
			ErrSonyMakerNoteNotFound, newMNAbs, mnBlobSize, len(finalTIFF))
	}

	// ── Rebase all OOL entries in the MakerNote IFD ──────────────────────────
	// The MakerNote is a plain IFD starting at newMNAbs in finalTIFF.
	// Scan all entries; for each with total > 4 bytes, update val_or_off.
	if newMNAbs+2 > len(finalTIFF) {
		return nil
	}
	count := int(order.Uint16(finalTIFF[newMNAbs:]))
	pos := newMNAbs + 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(finalTIFF) {
			break
		}
		entryType := order.Uint16(finalTIFF[e+2:])
		entryCount := order.Uint32(finalTIFF[e+4:])
		sz := typeSize(entryType)
		if sz == 0 || entryCount == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCount)
		if total <= 4 {
			continue // inline; no pointer to rebase
		}
		// OOL: val_or_off is the TIFF-absolute offset of the value data.
		oldVOO := order.Uint32(finalTIFF[e+8:])
		// Safety: verify the old offset was within the original blob range.
		// Sony MakerNote OOL data is always within [mnSrcOffset, mnSrcOffset+mnBlobSize).
		if uint64(oldVOO) < uint64(oldMNAbs) ||
			uint64(oldVOO)+total > uint64(oldMNAbs)+uint64(mnBlobSize) {
			// OOL data outside original blob — skip (defensive; should not happen).
			continue
		}
		// Compute blob-relative offset (unchanged after move).
		relOff := oldVOO - oldMNAbs
		newVOO := uint32(newMNAbs) + relOff //nolint:gosec // G115: newMNAbs < len(finalTIFF) < 2^32
		order.PutUint32(finalTIFF[e+8:], newVOO)
	}
	return nil
}

// patchSonySR2InFinalTIFF performs the post-encode patching for the SR2Private
// block:
//
//  1. Patches the 0xC634 inline bytes in IFD0 of finalTIFF to point to the new
//     SR2 IFD position (info.sr2NewOffset).
//
//  2. Rebases the OOL pointer fields (0x7241, 0x7242, 0x7250 val_or_off) and
//     the absolute-offset tags (0x7200, 0x7240 val_or_off) in the SR2 rawBytes.
//
// This function is called AFTER finalTIFF has been built and the SR2 rawBytes
// have been appended (so info.sr2NewOffset is valid).
//
//nolint:cyclop // SR2 patching requires scanning IFD0 and SR2 IFD; complexity is inherent
func patchSonySR2InFinalTIFF(finalTIFF []byte, sr2Bytes []byte, info *sonySR2Info, order binary.ByteOrder) error {
	if info.sr2RawBytes == nil || len(sr2Bytes) == 0 {
		return nil // no SR2 block to patch
	}

	// ── Step 1: patch 0xC634 inline bytes in IFD0 ────────────────────────────
	// 0xC634 is type=Byte, count=4, inline.  The 4 bytes in the val_or_off field
	// encode the TIFF-absolute offset to the SR2 IFD block.
	// After relocation, we write info.sr2NewOffset as the new 4-byte LE value.
	if len(finalTIFF) < 8 {
		return fmt.Errorf("%w: finalTIFF too short (%d bytes)", ErrSonySR2PatchFailed, len(finalTIFF))
	}
	ifd0Off := int(order.Uint32(finalTIFF[4:]))
	if ifd0Off+2 > len(finalTIFF) {
		return fmt.Errorf("%w: IFD0 offset %d out of finalTIFF bounds (%d)",
			ErrSonySR2PatchFailed, ifd0Off, len(finalTIFF))
	}

	entryCount := int(order.Uint16(finalTIFF[ifd0Off:]))
	pos := ifd0Off + 2
	foundC634 := false
	for i := range entryCount {
		e := pos + i*12
		if e+12 > len(finalTIFF) {
			break
		}
		tag := order.Uint16(finalTIFF[e:])
		if exif.TagID(tag) != sonyTagSR2Private {
			continue
		}
		// Overwrite the val_or_off field (bytes 8-11 of the entry) with the new
		// SR2 IFD absolute offset.
		// Per TIFF 6.0 §2 and Sony ARW convention: 0xC634 type=Byte, count=4,
		// total=4 ≤ 4, inline.  The 4 bytes in val_or_off ARE the tag value;
		// they encode the TIFF-absolute offset as LE uint32.
		order.PutUint32(finalTIFF[e+8:], info.sr2NewOffset)
		foundC634 = true
		break
	}
	if !foundC634 {
		// 0xC634 absent from re-encoded IFD0.  This can happen if exif.Encode
		// drops unknown-type entries.  Log a non-fatal condition: the SR2 block is
		// appended (and correct), but the IFD0 pointer is stale/missing.
		// We treat this as a non-fatal error to avoid silently corrupting files.
		return fmt.Errorf("%w: tag 0xC634 not found in re-encoded IFD0", ErrSonySR2PatchFailed)
	}

	// ── Step 2: rebase SR2 internal pointers within sr2Bytes ─────────────────
	// Compute the relocation delta: new_sr2_base − old_sr2_base.
	//   old_sr2_base = info.sr2SrcOffset (absolute position in the original file)
	//   new_sr2_base = info.sr2NewOffset (absolute position in finalTIFF output)
	//
	// For each OOL entry in the SR2 IFD:
	//   rel_off = old_voo − old_sr2_base  (offset within SR2 block)
	//   new_voo = new_sr2_base + rel_off
	//
	// For absolute-offset tags (0x7200 SR2SubIFDOffset, 0x7240 IDC_IFD):
	//   These are inline TypeLong entries whose VALUES are absolute file offsets
	//   pointing BEYOND the SR2 IFD fixed block (i.e., into the OOL area or beyond).
	//   They must be rebased by the same delta.
	//
	// TIFF 6.0 §2: inline values ≤ 4 bytes are NOT pointers (they are values stored
	// in place).  For the SR2 absolute-offset tags (0x7200, 0x7240) the "value"
	// happens to be a file offset — a Sony-specific convention (ExifTool Sony.pm).
	return patchSR2Bytes(sr2Bytes, info, order)
}

// patchSR2Bytes rebases all TIFF-absolute pointers within the SR2 IFD block and
// patches the internal pointers of the encrypted SR2SubIFD blob.
//
// The block is a verbatim copy of the original file bytes starting at srcOff.
// After being placed at newOff in the output, every TIFF-absolute pointer within
// the block must be updated.
//
// Two classes of entries are handled:
//
// (A) Standard OOL entries (total size > 4): val_or_off is a TIFF-absolute pointer
//
//	to the value area.  The value area is included verbatim in the rawBytes by
//	computeSR2IFDExtent / extractSonySR2Info.  Update the pointer:
//	  new_voo = newOff + (old_voo − srcOff)
//
// (B) Inline absolute-offset tags (0x7200, 0x7240): these are TypeLong, Count=1,
//
//	total=4 ≤ 4 → inline.  Their "value" is a TIFF-absolute file offset stored
//	in the val_or_off field.  It points OUTSIDE the SR2 IFD fixed block (into the
//	OOL area or the encrypted blob).  Rebase with the same delta.
//	  new_voo = newOff + (old_voo − srcOff)
//
// After rebasing the SR2 IFD entries, patchSR2SubIFDPointers is called to
// decrypt the SR2SubIFD blob, rebase its internal TIFF-absolute pointers, and
// re-encrypt the blob.  This preserves SR2DataIFD navigability and all calibration
// data (BlackLevel, WB_RGGBLevels, ColorMatrix, etc.) embedded in the blob.
//
//nolint:cyclop,gocyclo // IFD scanning + two-class dispatch + blob patching is inherent to the binary format
func patchSR2Bytes(rawBytes []byte, info *sonySR2Info, order binary.ByteOrder) error {
	srcOff := info.sr2SrcOffset
	newOff := info.sr2NewOffset
	if len(rawBytes) < 2 {
		return nil
	}
	count := int(order.Uint16(rawBytes[0:]))
	pos := 2
	for i := range count {
		e := pos + i*12
		if e+12 > len(rawBytes) {
			break
		}
		tag := exif.TagID(order.Uint16(rawBytes[e:]))
		entryType := order.Uint16(rawBytes[e+2:])
		entryCount := order.Uint32(rawBytes[e+4:])
		sz := typeSize(entryType)
		if sz == 0 || entryCount == 0 {
			continue
		}
		total := uint64(sz) * uint64(entryCount)

		if total > 4 {
			// Class A: OOL entry — val_or_off is a TIFF-absolute pointer.
			// The value bytes are captured verbatim in rawBytes; only the pointer changes.
			oldVOO := order.Uint32(rawBytes[e+8:])
			if uint64(oldVOO) < uint64(srcOff) {
				// Value area precedes the SR2 block start — not captured.
				continue
			}
			relOff := oldVOO - srcOff
			if uint64(relOff)+total > uint64(len(rawBytes)) {
				continue // out of rawBytes bounds
			}
			newVOO := newOff + relOff
			order.PutUint32(rawBytes[e+8:], newVOO)
			continue
		}

		// Class B: inline entry — check for Sony absolute-offset tags.
		// These are TypeLong (4), Count=1, inline (total=4 ≤ 4).
		// Their value IS a TIFF-absolute offset, not a regular scalar.
		// ExifTool Sony.pm: only 0x7200 and 0x7240 carry offset values.
		if tag != sr2TagSubIFDOffset && tag != sr2TagIDCIFD {
			continue // regular inline scalar; no rebasing needed
		}
		if entryType != 4 || entryCount != 1 { //nolint:mnd // TypeLong=4; these tags are always TypeLong Count=1
			continue
		}
		oldVOO := order.Uint32(rawBytes[e+8:])
		if uint64(oldVOO) < uint64(srcOff) {
			// Offset precedes SR2 block start — unexpected; skip.
			continue
		}
		relOff := oldVOO - srcOff
		// Offset points outside the SR2 block (encrypted blob or IDC_IFD data
		// may extend beyond what we captured) — rebase with delta regardless.
		// This path is taken for 0x7200 and 0x7240 whose targets are within
		// the SR2 block rawBytes — we guard defensively elsewhere.
		newVOO := newOff + relOff
		order.PutUint32(rawBytes[e+8:], newVOO)
	}

	// ── Patch encrypted SR2SubIFD blob internal pointers ─────────────────────
	// The encrypted blob contains TIFF-absolute pointers that must be rebased.
	// We decrypt, rebase, and re-encrypt using the Sony PRNG-XOR stream cipher.
	// ExifTool Sony.pm ProcessSR2/WriteSR2: Decrypt(\$buff, 0, $length, $key).
	if info.sr2SubIFDKey != 0 && info.sr2SubIFDOffset != 0 && info.sr2SubIFDLength > 0 {
		patchSR2SubIFDPointers(
			rawBytes,
			srcOff,
			newOff,
			info.sr2SubIFDOffset, // original absolute offset of the blob in the source file
			info.sr2SubIFDLength, // byte length of the encrypted blob
			info.sr2SubIFDKey,
			order,
		)
	}

	return nil
}

// relocateTIFFFromParsedARW is the ARW-specific entry point for the TIFF
// copy-and-relocate serializer.
//
// It runs Sony-specific preprocessing (Step A) to extract the MakerNote info
// and SR2Private block, then runs a modified relocation algorithm that:
//   - appends the SR2 block as extra raw metadata bytes (like SubIFD rawBytes)
//   - patches 0xC634 in the finalTIFF output to point to the new SR2 position
//   - rebases the internal SR2 pointers
//   - rebases all OOL val_or_off entries in the Sony MakerNote IFD
//
// When no Sony-specific structures are detected, it falls back to the standard
// relocateTIFFFromParsed path.
func relocateTIFFFromParsedARW(base []byte, e *exif.EXIF, rawIPTC, rawXMP []byte) ([]byte, error) {
	if e == nil {
		var parseErr error
		e, parseErr = exif.Parse(base)
		if parseErr != nil {
			return nil, fmt.Errorf("arw: parse for relocation: %w", parseErr)
		}
	}

	order := e.ByteOrder
	if order == nil {
		order = binary.LittleEndian // Sony ARW is always little-endian
	}

	// Step A: extract Sony-specific info (MakerNote + SR2Private block).
	info, err := extractSonySR2Info(base, e, order)
	if err != nil {
		return nil, fmt.Errorf("arw: extract Sony SR2 info: %w", err)
	}

	if info == nil || (info.mnEntry == nil && info.sr2RawBytes == nil) {
		// No Sony-specific preprocessing needed; use the standard path.
		return relocateTIFFFromParsed(base, e, rawIPTC, rawXMP)
	}

	// Run the ARW-specific relocation with Sony post-encode patches.
	return arwRelocateWithSR2(base, e, rawIPTC, rawXMP, info, order)
}

// arwRelocateWithSR2 runs the TIFF copy-and-relocate algorithm with Sony-specific
// post-encode patches for the MakerNote and SR2Private block.
//
// This is a modified variant of relocateTIFFFromParsed that:
//  1. Appends the SR2 rawBytes block after the main EXIF block (before image data).
//  2. After step 9 (re-encode), calls rebaseSonyMakerNote and patchSonySR2InFinalTIFF.
//
// All other steps are identical to relocateTIFFFromParsed.
//
//nolint:cyclop,gocyclo,funlen // mirrors relocateTIFFFromParsed with Sony additions; splitting reduces clarity
func arwRelocateWithSR2(
	base []byte,
	e *exif.EXIF,
	rawIPTC, rawXMP []byte,
	info *sonySR2Info,
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
	// extractJPEGThumbnail runs on every IFD, not just the IFD1 chain.  Sony ARW
	// files store a large (~736 KB) preview JPEG in IFD0 via these two tags
	// (PreviewImageStart / PreviewImageLength, TIFF-absolute offset).
	//
	// enumerateIFDBlocks skips the JPEG block when ThumbnailData != nil, because
	// it assumes exif.Encode will handle it — but exif.Encode only processes
	// ThumbnailData for IFD0.Next (the IFD1 chain), never for IFD0 itself.
	// The result is that the 736 KB preview block is silently dropped.
	//
	// Fix: clear IFD0.ThumbnailData so that enumerateIFDBlocks enumerates the
	// preview as a standard imageBlock (offset+size from base).  The 0x0201 and
	// 0x0202 entries remain in IFD0.Entries and are handled by the standard
	// removeImageOffsetEntries → insertPlaceholders → updatePlaceholders flow.
	// exif.Encode is not affected because it never reads IFD0.ThumbnailData.
	//
	// EXIF §4.5.5 / TIFF 6.0 §8.1: JPEGInterchangeFormat (0x0201) in IFD0 (not
	// IFD1) is a preview JPEG, not the IFD1 thumbnail; relocation must treat it
	// as an opaque image block.
	e.IFD0.ThumbnailData = nil

	// Step 3: enumerate image blocks from the main IFD chain.
	blocks, err := enumerateImageBlocks(base, e, order)
	if err != nil {
		return nil, fmt.Errorf("arw: enumerate image blocks: %w", err)
	}

	// Step 4: parse SubIFDs (tag 0x014A).
	subIFDs, subBlocks, subErr := enumerateSubIFDs(base, e.IFD0, order)
	if subErr != nil {
		return nil, fmt.Errorf("arw: enumerate SubIFDs: %w", subErr)
	}
	blocks = append(blocks, subBlocks...)

	// Step 5: remove stale image-data offset entries from main IFDs.
	mainBlocks := filterMainBlocks(blocks, subIFDs)
	removeImageOffsetEntries(mainBlocks)

	// Step 6: re-insert placeholder entries and encode to learn the structure size.
	offsetValueSlices := insertPlaceholders(mainBlocks, order)

	skeleton, skelErr := exif.Encode(e)
	if skelErr != nil {
		return nil, fmt.Errorf("arw: encode placeholder: %w", skelErr)
	}
	ifdEnd := uint32(len(skeleton)) //nolint:gosec // G115: len bounded by TIFF stream

	// Step 7: assign new absolute offsets.
	// SubIFD blocks come first, then the SR2 block, then image data.
	subIFDsSize := computeSubIFDsSize(subIFDs)
	assignSubIFDOffsets(subIFDs, ifdEnd)

	// Assign SR2 offset: immediately after SubIFDs (word-aligned).
	// TIFF 6.0 §2: data items must start at word (even) boundaries.
	// Compute the exact SR2 start and block size now (not a worst-case estimate)
	// so that imageStart is exact and assignNewOffsets writes correct pointers.
	var sr2ActualSize uint32
	if info.sr2RawBytes != nil {
		sr2Start := ifdEnd + subIFDsSize
		if sr2Start&1 == 1 {
			sr2Start++
			sr2ActualSize = 1 + uint32(len(info.sr2RawBytes)) //nolint:gosec // G115: SR2 block < 2^32
		} else {
			sr2ActualSize = uint32(len(info.sr2RawBytes)) //nolint:gosec // G115: SR2 block < 2^32
		}
		info.sr2NewOffset = sr2Start
	}
	imageStart := ifdEnd + subIFDsSize + sr2ActualSize
	assignNewOffsets(blocks, imageStart)

	// Step 8a: update placeholder value bytes (main-IFD blocks).
	updatePlaceholders(mainBlocks, offsetValueSlices, order)

	// Step 8b: patch SubIFD raw bytes.
	patchSubIFDImageOffsets(subIFDs, blocks, order)

	// Step 9: re-encode → finalTIFF.
	finalTIFF, finalErr := exif.Encode(e)
	if finalErr != nil {
		return nil, fmt.Errorf("arw: encode final: %w", finalErr)
	}

	// Step 9.5 (Sony-specific): rebase Sony MakerNote OOL offsets.
	// Must be done BEFORE appending the SR2 block so that finalTIFF length is
	// correct for bounds checks.
	if rebaseErr := rebaseSonyMakerNote(finalTIFF, info, order); rebaseErr != nil {
		return nil, fmt.Errorf("arw: rebase Sony MakerNote offsets: %w", rebaseErr)
	}

	// Step 10: patch the 0x014A SubIFDs pointer array in finalTIFF.
	if len(subIFDs) > 0 {
		if pErr := patchSubIFDPointers(finalTIFF, subIFDs, order); pErr != nil {
			return nil, fmt.Errorf("arw: patch SubIFD pointers: %w", pErr)
		}
	}

	// Step 11: append SubIFD raw bytes.
	for _, si := range subIFDs {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		finalTIFF = append(finalTIFF, si.rawBytes...)
	}

	// Step 11.5 (Sony-specific): append SR2 private block raw bytes.
	// The SR2 block is placed after SubIFDs and before image data, word-aligned.
	var sr2BytesForOutput []byte
	if info.sr2RawBytes != nil {
		if len(finalTIFF)&1 == 1 {
			finalTIFF = append(finalTIFF, 0x00)
		}
		// Verify assigned offset matches current finalTIFF length.
		// If they differ (due to extra alignment pads), recompute sr2NewOffset.
		if uint32(len(finalTIFF)) != info.sr2NewOffset { //nolint:gosec // G115: len < 2^32
			info.sr2NewOffset = uint32(len(finalTIFF)) //nolint:gosec // G115: len < 2^32
		}
		// Make a working copy of the SR2 bytes for in-place patching.
		sr2BytesForOutput = make([]byte, len(info.sr2RawBytes))
		copy(sr2BytesForOutput, info.sr2RawBytes)

		// Patch 0xC634 in finalTIFF and rebase SR2 internal pointers.
		if pErr := patchSonySR2InFinalTIFF(finalTIFF, sr2BytesForOutput, info, order); pErr != nil {
			return nil, fmt.Errorf("arw: patch Sony SR2Private: %w", pErr)
		}
		finalTIFF = append(finalTIFF, sr2BytesForOutput...)
	}

	// Step 12: append image block bytes from source.
	for _, blk := range blocks {
		end := uint64(blk.srcOffset) + uint64(blk.size)
		if end > uint64(len(base)) {
			return nil, fmt.Errorf("arw: image block offset=%d size=%d: %w",
				blk.srcOffset, blk.size, ErrBlockOutOfBounds)
		}
		finalTIFF = append(finalTIFF, base[blk.srcOffset:end]...)
	}

	return finalTIFF, nil
}
