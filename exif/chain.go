package exif

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
)

// chain.go — the IFDs that follow IFD0 (the thumbnail IFD, "1st IFD") and the
// sub-IFDs their own pointer tags address (task #319).
//
// Exif 3.1 §4.6.9.2, Table 21, p. 99 (1st IFD tag support levels): the Exif
// IFD pointer 0x8769 and the GPS Info IFD pointer 0x8825 are optional ("O")
// in IFD1; the Interoperability pointer 0xA005 is not listed for IFD1 (it is
// a tag of the Exif IFD, §4.6.3.3.1, p. 29); TIFF 6.0 §2 requires every
// offset to address data inside the stream. An IFD1 entry that holds the
// offset of an IFD in the SOURCE stream is therefore wrong in the stream
// Encode writes, where every block sits somewhere else.
//
// The rule Encode applies to a pointer entry of an IFD of the chain:
//
//  1. IFD1 has its own IFD for the tag (IFD.Sub, parsed from an offset that
//     differs from the IFD0 pointer): the IFD is written after the thumbnail
//     and the entry points to it, so no data is lost.
//  2. The entry holds the same source offset as the IFD0 pointer of the tag
//     (the same IFD): it points to the block written for IFD0's IFD, which
//     exists when EXIF.ExifIFD / GPSIFD is not nil.
//  3. Otherwise (the IFD could not be parsed, or IFD0 has none): the entry is
//     dropped, because it could only point at a stale position.
//
// The Interoperability pointer (0xA005) of an IFD of the chain is dropped by
// the same logic as case 3, as no IFD of the chain defines it.

// chainPointerTags are the pointer tags that an IFD of the chain may hold.
var chainPointerTags = [...]TagID{TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer} //nolint:gochecknoglobals // read-only

// attachChainSubIFDs parses the Exif and GPS IFDs that the IFDs of the chain
// after ifd0 address, and attaches them as IFD.Sub (see SubIFDs). An IFD that
// is the one IFD0 addresses with the same tag, or that cannot be parsed, is
// not attached. bigTIFF selects the BigTIFF traversal. The sub-IFD parse
// warnings are not reported: the entry that points to an unreadable IFD is
// dropped on write, and the IFD itself is not part of the model.
func attachChainSubIFDs(b []byte, ifd0 *IFD, order binary.ByteOrder, budget *traverseBudget, aliasThumbnail, bigTIFF bool) {
	for ifd := ifd0.Next; ifd != nil; ifd = ifd.Next {
		exifPtr, gpsPtr := ifd.Get(TagExifIFDPointer), ifd.Get(TagGPSIFDPointer)
		if exifPtr == nil && gpsPtr == nil {
			continue
		}
		var sub SubIFDs
		sub.Exif = parseDistinctSubIFD(b, ifd0, exifPtr, order, budget, aliasThumbnail, bigTIFF)
		sub.GPS = parseDistinctSubIFD(b, ifd0, gpsPtr, order, budget, aliasThumbnail, bigTIFF)
		if sub.Exif != nil {
			sub.Interop = parseDistinctSubIFD(b, nil, sub.Exif.Get(TagInteropIFDPointer), order, budget, aliasThumbnail, bigTIFF)
		}
		if sub.Exif != nil || sub.GPS != nil {
			ifd.Sub = &sub
		}
	}
}

// parseDistinctSubIFD parses the IFD that the pointer entry ptr addresses,
// unless ifd0 (when not nil) holds the same pointer value for the same tag,
// in which case the pointer addresses the IFD of IFD0. It returns nil when
// there is nothing to attach.
func parseDistinctSubIFD(b []byte, ifd0 *IFD, ptr *IFDEntry, order binary.ByteOrder, budget *traverseBudget, aliasThumbnail, bigTIFF bool) *IFD {
	off, ok := readBigTIFFSubIFDOffset(ptr)
	if !ok || off == 0 {
		return nil
	}
	if ifd0 != nil {
		if base, ok0 := readBigTIFFSubIFDOffset(ifd0.Get(ptr.Tag)); ok0 && base == off {
			return nil // the IFD IFD0 addresses: shared, written once
		}
	}
	var (
		sub *IFD
		err error
	)
	if bigTIFF {
		sub, _, err = traverseBigTIFF(b, off, order, budget, aliasThumbnail)
	} else {
		if off > math.MaxUint32 {
			return nil
		}
		sub, _, err = traverse(b, uint32(off), order, budget, aliasThumbnail)
	}
	if err != nil || sub == nil {
		return nil
	}
	sub.Next = nil // only the IFD itself is carried, as for the IFDs of IFD0
	return sub
}

// chainBlocks is one IFD of the chain as Encode writes it: its entries (with
// the pointer entries kept or dropped by the rule at the top of this file) and
// the sub-IFD blocks that follow its thumbnail.
type chainBlocks struct {
	entries []IFDEntry
	// exif, gps, interop are the entries of the attached IFDs, written in
	// this order after the thumbnail; nil when the IFD has none.
	exif, gps, interop []IFDEntry
	// exifTo, gpsTo select what the Exif and GPS pointer entries of entries
	// address: chainOwn (the block written here), chainShared (the block of
	// IFD0), or chainNone (no such entry).
	exifTo, gpsTo chainTarget
	patched       bool // entries is a copy this call may modify
}

type chainTarget uint8

const (
	chainNone chainTarget = iota
	chainOwn
	chainShared
)

// chainEntries builds the chainBlocks of ifd, an IFD of the chain after IFD0
// of e. When ifd has no thumbnail and none of the pointer tags, entries is
// ifd.Entries itself (no copy).
func chainEntries(e *EXIF, ifd *IFD) chainBlocks {
	var cb chainBlocks
	if ifd.ThumbnailData == nil && ifd.thumbnailStrips() == nil && !chainHasPointers(ifd) {
		cb.entries = ifd.Entries
		return cb
	}
	cb.patched = true
	cb.entries = chainBaseEntries(ifd)
	big := e.ifd0BigEndian()
	cb.exifTo = chainPointerTarget(e, ifd, TagExifIFDPointer, ifdOf(ifd.Sub, TagExifIFDPointer), e.ExifIFD)
	cb.gpsTo = chainPointerTarget(e, ifd, TagGPSIFDPointer, ifdOf(ifd.Sub, TagGPSIFDPointer), e.GPSIFD)
	if cb.exifTo != chainNone {
		cb.entries = insertPointer(cb.entries, TagExifIFDPointer, big)
	}
	if cb.gpsTo != chainNone {
		cb.entries = insertPointer(cb.entries, TagGPSIFDPointer, big)
	}
	if cb.exifTo == chainOwn {
		cb.exif = attachedEntries(ifd.Sub.Exif, ifd.Sub.Interop != nil, big)
		if ifd.Sub.Interop != nil {
			cb.interop = ifd.Sub.Interop.Entries
		}
	}
	if cb.gpsTo == chainOwn {
		cb.gps = ifd.Sub.GPS.Entries
	}
	return cb
}

// isUncarriedStrip reports whether tag is StripOffsets or StripByteCounts of an
// ifd whose strips were not carried (SubIFDs.stripsNotCarried): Encode omits it.
func isUncarriedStrip(ifd *IFD, tag TagID) bool {
	return ifd.Sub != nil && ifd.Sub.stripsNotCarried && (tag == TagStripOffsets || tag == TagStripByteCounts)
}

// chainBaseEntries copies the entries of ifd without its pointer tags (they
// are re-added when they have a target), and gives the thumbnail tags their
// 4-byte placeholder values, patched once the position is known (Exif 3.1
// §4.5.5 / TIFF 6.0 §2: both are inline LONG values). StripOffsets of a
// thumbnail with carried strips becomes a LONG entry with one placeholder per
// strip (Exif 3.1 §4.6.5.2.1, p. 39: SHORT or LONG, StripsPerImage values).
func chainBaseEntries(ifd *IFD) []IFDEntry {
	entries := make([]IFDEntry, 0, len(ifd.Entries)+2)
	for _, ent := range ifd.Entries {
		if isUncarriedStrip(ifd, ent.Tag) {
			continue
		}
		if !slices.Contains(chainPointerTags[:], ent.Tag) {
			entries = append(entries, ent)
		}
	}
	if ifd.ThumbnailData != nil {
		for _, t := range [...]TagID{TagJPEGInterchangeFormat, TagJPEGInterchangeFormatLength} {
			if i, ok := slices.BinarySearchFunc(entries, t, cmpEntryTag); ok {
				entries[i].Value = make([]byte, 4)
			}
		}
	}
	if ifd.thumbnailStrips() != nil {
		if i, ok := slices.BinarySearchFunc(entries, TagStripOffsets, cmpEntryTag); ok {
			entries[i].Type = TypeLong
			entries[i].Count = uint32(len(ifd.thumbnailStrips())) //nolint:gosec // G115: at most maxThumbnailStrips
			entries[i].Value = make([]byte, 4*len(ifd.thumbnailStrips()))
		}
	}
	return entries
}

// thumbnailEnd returns the absolute end of the thumbnail payload of ifd that
// starts at start: the JPEG thumbnail data, then the strips of an uncompressed
// thumbnail, each starting on an even offset (TIFF 6.0 §2 requires even offsets
// for IFDs and field values; strips are kept even too so that every reader that
// assumes word-aligned image data reads them). The last strip is not padded:
// the pad before the next IFD is the caller's.
func thumbnailEnd(ifd *IFD, start uint64) uint64 {
	p := start + uint64(len(ifd.ThumbnailData))
	for _, s := range ifd.thumbnailStrips() {
		p += p & 1
		p += uint64(len(s))
	}
	return p
}

// ifdOf returns the sub-IFD of s for the pointer tag, or nil.
func ifdOf(s *SubIFDs, tag TagID) *IFD {
	switch {
	case s == nil:
		return nil
	case tag == TagExifIFDPointer:
		return s.Exif
	case tag == TagGPSIFDPointer:
		return s.GPS
	}
	return nil
}

// chainPointerTarget applies the rule at the top of this file to the pointer
// entry of ifd for tag: own is the IFD attached to ifd for the tag, ifd0Block
// the IFD of IFD0 (nil when IFD0 has none, so nothing is written for it).
func chainPointerTarget(e *EXIF, ifd *IFD, tag TagID, own, ifd0Block *IFD) chainTarget {
	if own != nil {
		return chainOwn
	}
	if ifd0Block == nil || e.IFD0 == nil {
		return chainNone
	}
	off, ok := readBigTIFFSubIFDOffset(ifd.Get(tag))
	if !ok || off == 0 {
		return chainNone
	}
	if base, ok0 := readBigTIFFSubIFDOffset(e.IFD0.Get(tag)); ok0 && base == off {
		return chainShared
	}
	return chainNone
}

// cmpEntryTag compares an entry with a tag, for slices.BinarySearchFunc.
func cmpEntryTag(e IFDEntry, t TagID) int {
	switch {
	case e.Tag < t:
		return -1
	case e.Tag > t:
		return 1
	}
	return 0
}

// insertPointer inserts a 4-byte LONG pointer placeholder entry for tag into
// the sorted entries.
func insertPointer(entries []IFDEntry, tag TagID, big bool) []IFDEntry {
	i, _ := slices.BinarySearchFunc(entries, tag, cmpEntryTag)
	return slices.Insert(entries, i, IFDEntry{Tag: tag, Type: TypeLong, Count: 1, Value: make([]byte, 4), bigEndian: big})
}

// attachedEntries returns the entries of the attached Exif IFD x with the
// Interoperability pointer set up as in the IFD0 case (buildExifIFDEntries): a
// placeholder when withInterop, none otherwise.
func attachedEntries(x *IFD, withInterop, big bool) []IFDEntry {
	out := make([]IFDEntry, 0, len(x.Entries)+1)
	for _, ent := range x.Entries {
		if ent.Tag != TagInteropIFDPointer {
			out = append(out, ent)
		}
	}
	if withInterop {
		out = insertPointer(out, TagInteropIFDPointer, big)
	}
	return out
}

// chainLayout is where the blocks of one chained IFD land in the output.
type chainLayout struct {
	thumb, exif, gps, interop, end uint64 // absolute offsets
}

// planChain computes the layout of cb at output offset pos. The thumbnail data
// follows the IFD block; the attached IFDs follow the thumbnail (padded to a
// word boundary when anything follows it, TIFF 6.0 §2: IFD offsets are even).
func planChain(cb *chainBlocks, ifd *IFD, pos uint64, bigTIFF bool) chainLayout {
	var l chainLayout
	l.thumb = pos + ifdWrittenLen(cb.entries, pos, bigTIFF, false)
	p := thumbnailEnd(ifd, l.thumb)
	more := ifd.Next != nil || cb.exif != nil || cb.gps != nil
	if more && p&1 == 1 {
		p++
	}
	l.exif = p
	if cb.exifTo == chainOwn {
		p += ifdWrittenLen(cb.exif, p, bigTIFF, false)
	}
	l.gps = p
	if cb.gpsTo == chainOwn {
		p += ifdWrittenLen(cb.gps, p, bigTIFF, false)
	}
	l.interop = p
	if cb.interop != nil {
		p += ifdWrittenLen(cb.interop, p, bigTIFF, false)
	}
	l.end = p
	return l
}

// patchChain writes the offsets of the layout l into the placeholder values of
// cb: the thumbnail tags, the pointers of the IFD, and the Interoperability
// pointer of the attached Exif IFD. ifd0Exif and ifd0GPS are the offsets of
// the blocks of IFD0 (chainShared). It returns false when an offset does not
// fit the 32-bit pointer fields (BigTIFF only).
func patchChain(cb *chainBlocks, ifd *IFD, l chainLayout, order binary.ByteOrder, ifd0Exif, ifd0GPS uint64) bool {
	ok := true
	put := func(entries []IFDEntry, tag TagID, v uint64) {
		if v > math.MaxUint32 {
			ok = false
			return
		}
		if i, found := slices.BinarySearchFunc(entries, tag, cmpEntryTag); found {
			order.PutUint32(entries[i].Value, uint32(v))
		}
	}
	if ifd.ThumbnailData != nil {
		put(cb.entries, TagJPEGInterchangeFormat, l.thumb)
		put(cb.entries, TagJPEGInterchangeFormatLength, uint64(len(ifd.ThumbnailData)))
	}
	if ifd.thumbnailStrips() != nil && !patchStripOffsets(cb.entries, ifd, l.thumb, order) {
		ok = false
	}
	put2 := func(to chainTarget, tag TagID, own, shared uint64) {
		switch to {
		case chainOwn:
			put(cb.entries, tag, own)
		case chainShared:
			put(cb.entries, tag, shared)
		case chainNone:
		}
	}
	put2(cb.exifTo, TagExifIFDPointer, l.exif, ifd0Exif)
	put2(cb.gpsTo, TagGPSIFDPointer, l.gps, ifd0GPS)
	if cb.interop != nil {
		put(cb.exif, TagInteropIFDPointer, l.interop)
	}
	return ok
}

// appendThumbnail appends the thumbnail payload of ifd (the JPEG data, then the
// strips, each on an even offset, see thumbnailEnd) to out, which ends where the
// payload starts (l.thumb), and the pad that the IFD after it needs.
func appendThumbnail(out []byte, ifd *IFD, l chainLayout) []byte {
	if ifd.ThumbnailData == nil && ifd.thumbnailStrips() == nil {
		return out
	}
	out = append(out, ifd.ThumbnailData...)
	p := l.thumb + uint64(len(ifd.ThumbnailData))
	for _, s := range ifd.thumbnailStrips() {
		if p&1 == 1 {
			out = append(out, 0x00)
			p++
		}
		out = append(out, s...)
		p += uint64(len(s))
	}
	if p&1 == 1 && l.exif > p {
		out = append(out, 0x00)
	}
	return out
}

// patchStripOffsets sets the placeholder values of StripOffsets in entries to
// the positions of the strips of ifd, which follow the JPEG thumbnail data that
// starts at thumb (see thumbnailEnd). It returns false when a position does not
// fit the 32-bit offset (BigTIFF only).
func patchStripOffsets(entries []IFDEntry, ifd *IFD, thumb uint64, order binary.ByteOrder) bool {
	i, found := slices.BinarySearchFunc(entries, TagStripOffsets, cmpEntryTag)
	if !found {
		return true
	}
	p := thumb + uint64(len(ifd.ThumbnailData))
	for k, s := range ifd.thumbnailStrips() {
		p += p & 1
		if p > math.MaxUint32 {
			return false
		}
		order.PutUint32(entries[i].Value[4*k:], uint32(p))
		p += uint64(len(s))
	}
	return true
}

// writeChainIFD appends one IFD of the chain after IFD0 to out, with its
// thumbnail and attached sub-IFDs (see the rule at the top of this file), and
// returns the extended slice. ifd0Exif and ifd0GPS are the offsets of the
// blocks written for IFD0's Exif and GPS IFDs. For a BigTIFF stream an offset
// that does not fit the 32-bit pointer fields is reported as
// ErrBigTIFFPointerOverflow (BigTIFF spec §2; task #264 R-16).
func writeChainIFD(out []byte, e *EXIF, ifd *IFD, order binary.ByteOrder, bigTIFF bool, ifd0Exif, ifd0GPS uint64) ([]byte, error) {
	cb := chainEntries(e, ifd)
	pos := uint64(len(out))
	l := planChain(&cb, ifd, pos, bigTIFF)
	if cb.patched && !patchChain(&cb, ifd, l, order, ifd0Exif, ifd0GPS) {
		return nil, fmt.Errorf("%w: IFD1 pointer offset %d / thumbnail length %d", ErrBigTIFFPointerOverflow, l.end, len(ifd.ThumbnailData))
	}
	next := uint64(0)
	if ifd.Next != nil {
		next = l.end
	}
	out = writeIFDAt(out, cb.entries, order, bigTIFF, pos, next)
	out = appendThumbnail(out, ifd, l)
	if cb.exifTo == chainOwn {
		out = writeIFDAt(out, cb.exif, order, bigTIFF, uint64(len(out)), 0)
	}
	if cb.gpsTo == chainOwn {
		out = writeIFDAt(out, cb.gps, order, bigTIFF, uint64(len(out)), 0)
	}
	if cb.interop != nil {
		out = writeIFDAt(out, cb.interop, order, bigTIFF, uint64(len(out)), 0)
	}
	return out, nil
}

// writeIFDAt writes one IFD block with writeIFD (classic) or writeIFDBigTIFF.
func writeIFDAt(out []byte, entries []IFDEntry, order binary.ByteOrder, bigTIFF bool, start, next uint64) []byte {
	if bigTIFF {
		return writeIFDBigTIFF(out, entries, order, start, next)
	}
	return writeIFD(out, entries, order, uint32(start), uint32(next)) //nolint:gosec // G115: a classic stream's offsets are bounded by its 32-bit offset fields
}
