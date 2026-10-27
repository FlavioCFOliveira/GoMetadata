package gometadata

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr3"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// Write encodes only the metadata segments that were modified (task #387).
//
// A Metadata remembers which segments the Set* methods and MarkModified
// changed (Metadata.modified). Write turns that into a writePlan:
//
//   - encode: the segments written from the parsed m.EXIF, m.IPTC and m.XMP.
//   - carry: the segments that are not modified and that the image holds. A
//     JPEG leaves them in place byte for byte (jpeg.InjectKeeping); the other
//     containers receive the raw bytes Read kept, which their injectors write
//     back as they are (a TIFF-based container relocates its IFDs in every
//     write, see below).
//   - bypass: nothing is modified, so the output is the source image itself.
//
// Design reference only (no code copied): the Adobe XMP Toolkit tracks changes
// with a per-file needsUpdate flag (XMPFiles.cpp, XMPFiles::PutXMP /
// CloseFile) and writes a metadata block only when it changed
// (TIFF_Support.hpp, per-tag "changed" flags; PSIR_Support.hpp, a modified
// image resource list).
//
// What the output of a write is made of, per container:
//
//   - Nothing modified: byte-identical to the source in every container.
//   - JPEG: the modified segments are written after SOI, the others stay where
//     they are in the source, byte for byte (the IPTC digest included).
//   - PNG, WebP, HEIF/AVIF, CR3: the modified segments are encoded; the others
//     are written from the raw bytes Read kept, which equal the source's.
//   - TIFF, DNG, CR2, NEF, ARW, ORF, RW2: byte identity is not attainable
//     once anything is written. The EXIF IFDs are the container structure
//     itself: adding or growing an IPTC or XMP tag value moves every
//     out-of-line value and the image data after it, so the writer re-lays
//     the IFDs from the parsed EXIF whichever segment changed (TIFF 6.0 §2:
//     offsets are absolute). Metadata and image data are preserved, which
//     exiftool -a -G1 shows as the same tags.
type writePlan struct {
	encode Segment
	carry  Segment
	bypass bool
}

// jpegKeep returns the jpeg.Keep that holds the segments of s.
func (s Segment) jpegKeep() jpeg.Keep {
	var k jpeg.Keep
	if s&SegmentEXIF != 0 {
		k |= jpeg.KeepEXIF
	}
	if s&SegmentIPTC != 0 {
		k |= jpeg.KeepIPTC
	}
	if s&SegmentXMP != 0 {
		k |= jpeg.KeepXMP
	}
	return k
}

// planWrite decides which segments Write encodes and which it carries from r.
//
// A segment is encoded when it is marked modified and m holds it, or when m
// holds it and has no raw bytes for it (it was created, not read: there is
// nothing to carry). Every other segment that has raw bytes is carried, but only
// when r is the image m was read from: the raw bytes that Read kept must equal
// those of r. When they do not, m is being written to another image, and every
// segment is encoded (or, where m has only raw bytes, written as they are), the
// behaviour before task #387, so that Read and Write copy metadata between
// images.
func planWrite(r io.ReadSeeker, fmtID format.FormatID, m *Metadata) writePlan {
	enc, held := m.encodeSet()

	// A JPEG leaves in place every segment that is not encoded, whether m has
	// raw bytes for it or not: a Photoshop APP13 that holds no IPTC that Read
	// understands (a resource block with another signature) is still the
	// image's own, and so is an XMP APP1 that did not parse.
	carry := held &^ enc
	if fmtID == format.FormatJPEG {
		carry = segmentAll &^ enc
	}

	// The check reads r once. It is needed to carry a segment, and to copy r
	// when nothing is modified, where a segment that m lacks must be lacking in
	// r too.
	if carry == 0 && enc != 0 {
		return writePlan{encode: enc}
	}
	check := carry
	if enc == 0 {
		check = segmentAll
	}
	if !sourceHolds(r, fmtID, m, check) {
		return writePlan{encode: segmentAll}
	}
	return writePlan{encode: enc, carry: carry, bypass: enc == 0}
}

// encodeSet returns the segments to encode from the parsed fields of m (enc) and
// the segments the image held when m was read (held: those with raw bytes).
//
// A segment is encoded when it is marked modified and m holds it, or when m
// holds it and has no raw bytes for it (it was created, not read: there is
// nothing to carry). A segment m does not hold cannot be encoded.
func (m *Metadata) encodeSet() (enc, held Segment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A non-nil raw slice means the image holds the segment, even when it is empty.
	if m.rawEXIF != nil {
		held |= SegmentEXIF
	}
	if m.rawIPTC != nil {
		held |= SegmentIPTC
	}
	if m.rawXMP != nil || m.rawXMPWire != nil {
		held |= SegmentXMP
	}
	var parsed Segment // the segments m has a parsed value for
	if m.EXIF != nil {
		parsed |= SegmentEXIF
	}
	if m.IPTC != nil {
		parsed |= SegmentIPTC
	}
	if m.XMP != nil {
		parsed |= SegmentXMP
	}
	return (m.modified | ^held) & parsed, held
}

// sourceHolds reports whether the segments of check in r have the same raw
// bytes as the ones m holds, which says that m was read from r (or from a copy
// of it). Any failure to read r counts as "no": the write then runs as it did
// before task #387 and reports the failure itself.
func sourceHolds(r io.ReadSeeker, fmtID format.FormatID, m *Metadata, check Segment) bool {
	// Read parsed what the caller asked for, and WithoutIPTC and WithoutXMP make
	// the JPEG extractor skip the digest and the extended XMP merge, which
	// changes the raw XMP: look at r the way Read did, first in full, then
	// without what the caller opted out of.
	if sourceMatches(r, fmtID, m, check, true, true) {
		return true
	}
	if m.iptcOpaque || m.xmpOpaque {
		return sourceMatches(r, fmtID, m, check, !m.iptcOpaque, !m.xmpOpaque)
	}
	return false
}

// sourceMatches extracts the raw segments of r like Read (wantIPTC and wantXMP
// are Read's lazyIPTC and lazyXMP inverted) and compares those of check with
// m's.
func sourceMatches(r io.ReadSeeker, fmtID format.FormatID, m *Metadata, check Segment, wantIPTC, wantXMP bool) bool {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return false
	}
	rawEXIF, rawIPTC, rawXMP, rawXMPWire, err := extractRaw(r, fmtID, wantIPTC, wantXMP)
	if err != nil {
		return false
	}
	if check&SegmentEXIF != 0 && !bytes.Equal(rawEXIF, m.rawEXIF) {
		return false
	}
	if check&SegmentIPTC != 0 && !bytes.Equal(rawIPTC, m.rawIPTC) {
		return false
	}
	return check&SegmentXMP == 0 || (bytes.Equal(rawXMP, m.rawXMP) && bytes.Equal(rawXMPWire, m.rawXMPWire))
}

// extractRaw is extractByFormat reduced to the raw segments sourceMatches
// compares. It differs in what it leaves out: the CR3 sub-streams, the
// extended-XMP GUID and the digest, which cost allocations a comparison does not
// need.
func extractRaw(r io.ReadSeeker, fmtID format.FormatID, wantIPTC, wantXMP bool) (rawEXIF, rawIPTC, rawXMP, rawXMPWire []byte, err error) {
	switch fmtID {
	case format.FormatJPEG:
		rawEXIF, rawIPTC, _, rawXMP, rawXMPWire, _, err = jpeg.ExtractFullSelective(r, wantIPTC, wantXMP)
	case format.FormatCR3:
		var seg cr3.Segments
		seg, err = cr3.ExtractSegments(r)
		if errors.Is(err, cr3.ErrNoCMT1Box) {
			err = nil // Read treats a missing CMT1 as a warning, with the XMP still read
		}
		rawEXIF, rawXMP = seg.EXIF, seg.XMP
	default:
		fn, ok := extractors[fmtID]
		if !ok {
			return nil, nil, nil, nil, &UnsupportedFormatError{}
		}
		rawEXIF, rawIPTC, rawXMP, err = fn(r)
	}
	return rawEXIF, rawIPTC, rawXMP, rawXMPWire, err
}

// copyThrough writes the whole of r to w: the output of a write in which no
// segment is modified. It is capped at maxFileSize like every read of r.
func copyThrough(r io.ReadSeeker, w io.Writer) error {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("gometadata: seek: %w", err)
	}
	if size > maxFileSize {
		return fmt.Errorf("gometadata: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("gometadata: seek: %w", err)
	}
	if err := iobuf.StreamCopyN(r, w, size); err != nil {
		return fmt.Errorf("gometadata: copy: %w", err)
	}
	return nil
}
