package tiff

// Nikon Capture IPTC copy (task #322). The Nikon Capture and Capture NX
// software store a copy of the IPTC-IIM record in the Nikon MakerNote, in tag
// 0x0E01 ("Nikon Capture Data"). A NEF write that replaces the IIM of tag
// 0x83BB would leave that copy with the old values, so it is rewritten with
// the IIM that is written (the policy chosen for #322: update the copies, as
// ExifTool does).
//
// The layout of the data is not published by Nikon. It is described here from
// the format notes of ExifTool (lib/Image/ExifTool/NikonCapture.pm; a study
// reference only: it is GPL/Artistic licensed and no code is used) and was
// checked against the corpus file testdata/corpus/raw/exiftool/Nikon.nef. All
// integers of the Nikon Capture data are little endian, whatever the byte
// order of the MakerNote:
//
//	data   = header (18) | size S (4) | entry... | optional padding
//	entry  = tag (4) | 14 bytes of its own | size n+4 (4) | n bytes of data
//
// The first entry tag of a block is 0x7A86A940 (in the header). S counts the
// four bytes of the size field and all entries, so the entries end at offset
// 18+S; some writers (ViewNX 2.1.1) make S count the header as well, and then
// the entries end at the end of the data. The IPTC record is the entry with
// tag 0x9EF5F6E0; its data is an IIM stream followed, as Nikon Capture writes
// it, by zero bytes up to a fixed capacity.
//
// Rewrite policy, which keeps every other byte of the MakerNote and of the
// Nikon Capture data (and every size and offset inside them) valid:
//   - the new IIM fits the capacity of the entry: it is written in place,
//     followed by zero bytes, and no size changes;
//   - it does not fit: the data block is rebuilt with the larger entry (its
//     size, the block size S, and the count of tag 0x0E01 change), and the
//     block is moved to the end of the MakerNote, with the offset of tag
//     0x0E01 updated, so that no other offset of the MakerNote moves; the old
//     bytes are cleared.
// A MakerNote or data block that does not have the expected layout is left
// as it is.

import (
	"bytes"
	"encoding/binary"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

const (
	// nikonTagCaptureData is the MakerNote tag of the Nikon Capture data.
	nikonTagCaptureData exif.TagID = 0x0E01
	// nikonCaptureBlockTag is the tag at the start of the Nikon Capture data.
	nikonCaptureBlockTag = 0x7A86A940
	// nikonCaptureIPTCTag is the tag of the IPTC entry.
	nikonCaptureIPTCTag = 0x9EF5F6E0
	// nikonCaptureHeader is the size of the block header, size field excluded.
	nikonCaptureHeader = 18
	// nikonCaptureEntryHeader is the size of an entry header, size field included.
	nikonCaptureEntryHeader = 22
)

// rewriteNikonCaptureIPTC rewrites the IPTC entry of the Nikon Capture data
// of the Nikon MakerNote of e with rawIPTC, when e has both; otherwise it
// does nothing. The MakerNote value of e is replaced by a modified copy: the
// original may alias the caller's buffer.
func rewriteNikonCaptureIPTC(e *exif.EXIF, rawIPTC []byte) {
	if e == nil || e.ExifIFD == nil || len(rawIPTC) == 0 {
		return
	}
	mn := e.ExifIFD.Get(exif.TagMakerNote)
	if mn == nil || !isNikonType3Blob(mn.Value) {
		return
	}
	if blob, ok := rewriteNikonMakerNoteIPTC(mn.Value, rawIPTC); ok {
		mn.Value = blob
		mn.Count = uint32(len(blob)) //nolint:gosec // G115: a MakerNote is bounded by the 32-bit TIFF offsets
	}
}

// rewriteNikonMakerNoteIPTC returns a copy of the Nikon Type-3 MakerNote blob
// whose Nikon Capture IPTC entry holds iim, and true; or (nil, false) when
// the blob has no such entry, holds it already, or is malformed.
func rewriteNikonMakerNoteIPTC(blob, iim []byte) ([]byte, bool) {
	hdr, order := findNikonMNTIFFHeader(blob)
	if order == nil {
		return nil, false
	}
	ent, off, n, ok := findNikonCaptureEntry(blob, hdr, order)
	if !ok {
		return nil, false
	}
	data := blob[off : off+n]
	epos, capacity, ok := findNikonCaptureIPTC(data)
	if !ok || bytes.HasPrefix(data[epos:epos+capacity], iim) {
		return nil, false
	}
	out := bytes.Clone(blob)
	if len(iim) <= capacity {
		d := out[off+uint64(epos) : off+uint64(epos+capacity)] //nolint:gosec // G115: epos and capacity are non-negative and inside data
		clear(d)
		copy(d, iim)
		return out, true
	}

	// Grow: rebuild the data block with the larger entry and move it to the
	// end of the MakerNote.
	delta := len(iim) - capacity
	nd := make([]byte, 0, len(data)+delta)
	nd = append(nd, data[:epos]...)
	nd = append(nd, iim...)
	nd = append(nd, data[epos+capacity:]...)
	// The entry size and the block size S both count the bytes that were added.
	binary.LittleEndian.PutUint32(nd[epos-4:], uint32(len(iim)+4))                                                              //nolint:gosec // G115: bounded by the MakerNote size
	binary.LittleEndian.PutUint32(nd[nikonCaptureHeader:], binary.LittleEndian.Uint32(data[nikonCaptureHeader:])+uint32(delta)) //nolint:gosec // G115: bounded by the MakerNote size
	return placeNikonCaptureData(out, ent, off, n, nd, hdr, order)
}

// findNikonCaptureEntry returns, for the IFD of the Nikon Type-3 MakerNote
// blob (embedded TIFF header at hdr, byte order order), the position of the
// IFD entry of tag 0x0E01, and the offset and size of the data it names.
// Value offsets are relative to the embedded TIFF header.
func findNikonCaptureEntry(blob []byte, hdr int, order binary.ByteOrder) (ent int, off, n uint64, ok bool) {
	ifd := uint64(hdr) + uint64(order.Uint32(blob[hdr+4:])) //nolint:gosec // G115: hdr is a small non-negative offset
	if ifd+2 > uint64(len(blob)) {
		return 0, 0, 0, false
	}
	count := uint64(order.Uint16(blob[ifd:]))
	for i := range count {
		at := ifd + 2 + 12*i
		if at+12 > uint64(len(blob)) {
			return 0, 0, 0, false
		}
		if exif.TagID(order.Uint16(blob[at:])) != nikonTagCaptureData {
			continue
		}
		n = uint64(order.Uint32(blob[at+4:]))
		off = uint64(hdr) + uint64(order.Uint32(blob[at+8:])) //nolint:gosec // G115: hdr is a small non-negative offset
		if n <= 4 || off+n > uint64(len(blob)) {
			return 0, 0, 0, false
		}
		return int(at), off, n, true //nolint:gosec // G115: at < len(blob)
	}
	return 0, 0, 0, false
}

// placeNikonCaptureData moves the Nikon Capture data of the MakerNote blob out
// (entry at ent, old data at [off, off+n)) to its end as nd, updates the
// entry, and clears the old data.
func placeNikonCaptureData(out []byte, ent int, off, n uint64, nd []byte, hdr int, order binary.ByteOrder) ([]byte, bool) {
	if len(out)&1 == 1 {
		out = append(out, 0) // value offsets are word aligned (TIFF 6.0 §2)
	}
	newOff := uint64(len(out)) - uint64(hdr) //nolint:gosec // G115: hdr is a small non-negative offset
	if newOff > 0xFFFFFFFF || uint64(len(nd)) > 0xFFFFFFFF {
		return nil, false
	}
	clear(out[off : off+n])
	out = append(out, nd...)
	order.PutUint32(out[ent+4:], uint32(len(nd))) //nolint:gosec // G115: checked above
	order.PutUint32(out[ent+8:], uint32(newOff))
	return out, true
}

// nikonCaptureBlockEnd returns the end of the entries of the Nikon Capture
// data block: 18+S, or the end of the data when S counts the header too; false
// when the block is too short, does not start with its tag, or S fits neither.
func nikonCaptureBlockEnd(data []byte) (int64, bool) {
	if len(data) < nikonCaptureEntryHeader || binary.LittleEndian.Uint32(data) != nikonCaptureBlockTag {
		return 0, false
	}
	size := int64(binary.LittleEndian.Uint32(data[nikonCaptureHeader:]))
	end := int64(len(data))
	pad := end - size - nikonCaptureHeader
	switch {
	case pad >= 0:
		return size + nikonCaptureHeader, true
	case pad == -nikonCaptureHeader:
		return end, true
	}
	return 0, false
}

// findNikonCaptureIPTC returns the offset and the capacity of the data of the
// IPTC entry of the Nikon Capture data block, and true; or false when the
// block is malformed or has no IPTC entry. The data of the entry is
// data[at:at+capacity]; the size field that precedes it is data[at-4:at].
func findNikonCaptureIPTC(data []byte) (at, capacity int, ok bool) {
	end, ok := nikonCaptureBlockEnd(data)
	if !ok {
		return 0, 0, false
	}
	pos := int64(nikonCaptureEntryHeader)
	found := false
	for pos+nikonCaptureEntryHeader < end {
		tag := binary.LittleEndian.Uint32(data[pos:])
		n := int64(binary.LittleEndian.Uint32(data[pos+nikonCaptureHeader:])) - 4
		if n < 0 || pos+nikonCaptureEntryHeader+n > end {
			return 0, 0, false
		}
		if tag == nikonCaptureIPTCTag && !found {
			at, capacity, found = int(pos+nikonCaptureEntryHeader), int(n), true
		}
		pos += nikonCaptureEntryHeader + n
	}
	// Some writers (Capture NX 2) make the block size four bytes too small.
	if pos != end && pos != end-4 {
		return 0, 0, false
	}
	return at, capacity, found
}
