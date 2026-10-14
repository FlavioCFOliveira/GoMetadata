// Package cr3 implements metadata extraction and injection for Canon CR3
// files. CR3 is an ISOBMFF-based format (ftyp brand "crx ") whose moov box
// holds a Canon uuid box with the sub-boxes CMT1 (IFD0), CMT2 (Exif IFD),
// CMT3 (Canon MakerNote IFD) and CMT4 (GPS IFD), each a complete TIFF stream,
// and whose XMP packet is a top-level uuid box.
package cr3

// Adapted from evanoberholster/imagemeta meta/isobmff/crx.go:readUUIDBox,readCrxMoovBox, Copyright (c) 2019-2023 Evan Oberholster, SPDX-License-Identifier: MIT
//
// Changes from imagemeta: the UUIDs of the Canon metadata box and of the XMP
// packet box, and the mapping of CMT1-CMT4 to IFD0, Exif IFD, MakerNote IFD
// and GPS IFD, are applied to byte slices instead of a streaming reader;
// CMT3 and CMT4 are returned as raw TIFF streams (Segments) for the caller
// to parse; and the same layout is written back by Inject.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// Canon UUID: {85C0B687-820F-11E0-8111-F4CE462B6A48} stored as raw bytes.
var canonUUID = []byte{ //nolint:gochecknoglobals // package-level constant bytes
	0x85, 0xC0, 0xB6, 0x87, 0x82, 0x0F, 0x11, 0xE0,
	0x81, 0x11, 0xF4, 0xCE, 0x46, 0x2B, 0x6A, 0x48,
}

// xmpUUID identifies the top-level uuid box that carries the XMP packet of a
// CR3 file: {BE7ACFCB-97A9-42E8-9C71-999491E3AFAC} (imagemeta
// cr3XPacketUUID; ExifTool QuickTime "UUID-XMP"). The packet follows the
// 16-byte UUID directly (ISO 14496-12 §4.2 extended type).
var xmpUUID = []byte{ //nolint:gochecknoglobals // package-level constant bytes
	0xBE, 0x7A, 0xCF, 0xCB, 0x97, 0xA9, 0x42, 0xE8,
	0x9C, 0x71, 0x99, 0x94, 0x91, 0xE3, 0xAF, 0xAC,
}

// ISOBMFF box types compared against parseCR3BoxHeader's [4]byte result
// (ISO 14496-12 §4.2: the box type is a 4-byte code). Never mutated.
var (
	boxMoov = [4]byte{'m', 'o', 'o', 'v'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxTrak = [4]byte{'t', 'r', 'a', 'k'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxMdia = [4]byte{'m', 'd', 'i', 'a'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxMinf = [4]byte{'m', 'i', 'n', 'f'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxStbl = [4]byte{'s', 't', 'b', 'l'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxUdta = [4]byte{'u', 'd', 't', 'a'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxStco = [4]byte{'s', 't', 'c', 'o'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxCo64 = [4]byte{'c', 'o', '6', '4'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxUUID = [4]byte{'u', 'u', 'i', 'd'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxCMT1 = [4]byte{'C', 'M', 'T', '1'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxCMT2 = [4]byte{'C', 'M', 'T', '2'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxCMT3 = [4]byte{'C', 'M', 'T', '3'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	boxCMT4 = [4]byte{'C', 'M', 'T', '4'} //nolint:gochecknoglobals // package-level constant box type; never mutated
	// boxXMP is a non-standard "XMP " sub-box of the Canon uuid box that
	// GoMetadata versions before task #300 wrote; it is still read as a
	// fallback and kept in step on write, but never created.
	boxXMP = [4]byte{'X', 'M', 'P', ' '} //nolint:gochecknoglobals // package-level constant box type; never mutated
)

// parseCR3BoxHeader reads an ISOBMFF box header at data[pos:] and returns the
// resolved box size, 4-byte box type, header length in bytes, and whether
// the parse succeeded. The box type is returned by value, so parsing a header
// never allocates.
//
// ISOBMFF (ISO 14496-12) §4.2:
//   - Normal box:   4-byte size + 4-byte type          → headerLen = 8
//   - Extended box: size==1, followed by 8-byte largesize → headerLen = 16
//   - size==0 means the box extends to end-of-container → size = len(data)-pos
func parseCR3BoxHeader(data []byte, pos int) (size uint64, typ [4]byte, headerLen uint64, ok bool) {
	if pos+8 > len(data) {
		return 0, [4]byte{}, 0, false
	}
	size = uint64(binary.BigEndian.Uint32(data[pos:]))
	typ = [4]byte(data[pos+4 : pos+8])
	headerLen = 8

	if size == 1 {
		// Extended 64-bit size immediately follows the 8-byte base header.
		if pos+16 > len(data) {
			return 0, [4]byte{}, 0, false
		}
		size = binary.BigEndian.Uint64(data[pos+8:])
		headerLen = 16
	}

	if size == 0 {
		// len(data)-pos is non-negative: guarded by pos+8 ≤ len(data) check above.
		size = uint64(len(data) - pos) //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
	}

	// ISOBMFF (ISO 14496-12) §4.2: a well-formed box must be large enough to
	// contain its own header. If size < headerLen the box is malformed — slicing
	// data[pos+headerLen : pos+size] would panic (bounds out of range).
	if size < headerLen {
		return 0, [4]byte{}, 0, false
	}

	// Bounds check: box must not extend beyond the containing slice.
	if size > uint64(len(data)-pos) { //nolint:gosec // G115: len(data)-pos is non-negative (guarded above)
		return 0, [4]byte{}, 0, false
	}

	return size, typ, headerLen, true
}

// maxCR3TopLevelBoxScans bounds the number of top-level ISOBMFF box headers
// readTopLevelBox will read while searching for moov before giving up.
//
// #286: a defence-in-depth guard against a crafted file that pads itself with
// an excessive number of minimal (8-byte) top-level boxes ahead of any moov
// box, which would otherwise force one Read+Seek round trip per box — up to
// maxFileSize/8 iterations, each a real syscall for an os.File-backed
// io.ReadSeeker. Real CR3 files carry a handful of top-level boxes (ftyp,
// moov, mdat, rarely a few more); this ceiling is generous headroom, not a
// real-world limit.
const maxCR3TopLevelBoxScans = 4096

// seekFileLen returns the total length of r without reading any of its
// content, by seeking to the end and back to the start. r's position after a
// successful call is 0.
func seekFileLen(r io.ReadSeeker) (int64, error) {
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("cr3: seek to end: %w", err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("cr3: seek to start: %w", err)
	}
	return end, nil
}

// cr3BoxHeaderAt reads the top-level ISOBMFF box header at r's current
// position (which must equal pos) into hdr, and resolves its size — including
// the ISO 14496-12 §4.2 size==1 extended-size and size==0 "extends to EOF"
// cases — and 4-byte type, without reading the box's payload.
//
// ok is false whenever the header is truncated, extends past fileLen, or
// declares an internally inconsistent size (size < headerLen or size beyond
// the remaining file length). This is never reported as an error: a
// truncated trailing box degrades gracefully to "no more boxes", mirroring
// the in-memory parser's (parseCR3BoxHeader's) handling of the same
// conditions.
func cr3BoxHeaderAt(r io.ReadSeeker, hdr *[16]byte, pos, fileLen int64) (size uint64, typ [4]byte, headerLen int64, ok bool) {
	if _, err := io.ReadFull(r, hdr[:8]); err != nil {
		return 0, [4]byte{}, 0, false
	}
	size = uint64(binary.BigEndian.Uint32(hdr[:4]))
	typ = [4]byte{hdr[4], hdr[5], hdr[6], hdr[7]}
	headerLen = 8

	if size == 1 {
		// ISO 14496-12 §4.2: extended 64-bit size immediately follows the
		// 8-byte base header.
		if pos+16 > fileLen {
			return 0, [4]byte{}, 0, false
		}
		if _, err := io.ReadFull(r, hdr[8:16]); err != nil {
			return 0, [4]byte{}, 0, false
		}
		size = binary.BigEndian.Uint64(hdr[8:16])
		headerLen = 16
	}
	if size == 0 {
		// ISO 14496-12 §4.2: size==0 means "extends to EOF" — only valid for
		// the last box in the stream.
		size = uint64(fileLen - pos) //nolint:gosec // G115: fileLen-pos >= 8 > 0, guarded by the caller's loop condition
	}

	// Bounds/consistency check, done as a single int64 subtraction (proven
	// non-negative by the caller's loop invariant pos+8<=fileLen) converted
	// once to uint64, so a crafted 64-bit size can never overflow the
	// subsequent int64 arithmetic in readTopLevelBox (mirrors
	// parseCR3BoxHeader's in-memory guards).
	if size < uint64(headerLen) || size > uint64(fileLen-pos) { //nolint:gosec // G115: fileLen-pos >= 8 > 0, guarded by the caller's loop condition
		return 0, [4]byte{}, 0, false
	}
	return size, typ, headerLen, true
}

// topLevelLayout records the top-level boxes of a CR3 file that metadata
// lives in. Ranges are absolute byte offsets, header included; an absent box
// has start == end == 0.
type topLevelLayout struct {
	moov               []byte // moov payload (header excluded); nil when absent
	moovStart, moovEnd int64
	xmpStart, xmpEnd   int64  // the first top-level XMP uuid box
	xmp                []byte // its payload after the 16-byte UUID; read only on request
}

// hasXMPBox reports whether a top-level XMP uuid box was found.
func (l *topLevelLayout) hasXMPBox() bool { return l.xmpEnd > l.xmpStart }

// scanTopLevel scans the top-level ISOBMFF boxes of r (positioned at 0) for
// the first moov box, whose payload it reads, and the first uuid box with the
// XMP UUID, whose payload it reads when wantXMP is set. Every other box has
// only its header read; its payload is skipped via Seek (#286: the read
// volume stays proportional to the metadata, not to the raw image data in
// mdat). The scan stops once both boxes are found, at the first malformed
// header, or after maxCR3TopLevelBoxScans boxes. A non-nil error is returned
// only for an I/O failure.
func scanTopLevel(r io.ReadSeeker, fileLen int64, wantXMP bool) (topLevelLayout, error) {
	var lay topLevelLayout
	var hdr [16]byte
	pos := int64(0)
	for scans := 0; scans < maxCR3TopLevelBoxScans && pos+8 <= fileLen; scans++ {
		size, typ, headerLen, ok := cr3BoxHeaderAt(r, &hdr, pos, fileLen)
		if !ok {
			return lay, nil
		}
		end := pos + int64(size) //nolint:gosec // G115: size <= fileLen-pos, fits int64
		if err := lay.visit(r, typ, pos, end, end-pos-headerLen, wantXMP); err != nil {
			return lay, err
		}
		if (lay.moov != nil && lay.hasXMPBox()) || end+8 > fileLen {
			return lay, nil // done, or no room for another box header
		}
		if _, err := r.Seek(end, io.SeekStart); err != nil {
			return lay, fmt.Errorf("cr3: seek past %s box: %w", typ, err)
		}
		pos = end
	}
	return lay, nil
}

// visit records the top-level box [start, end) of type typ, whose payload of
// payloadLen bytes starts at r's position, when it is the first moov box or
// the first XMP uuid box.
func (l *topLevelLayout) visit(r io.ReadSeeker, typ [4]byte, start, end, payloadLen int64, wantXMP bool) error {
	switch {
	case typ == boxMoov && l.moov == nil:
		return l.readMoov(r, start, end, payloadLen)
	case typ == boxUUID && !l.hasXMPBox() && payloadLen >= 16:
		return l.readXMPBox(r, start, end, payloadLen, wantXMP)
	default:
		return nil
	}
}

// readMoov reads the payload of the moov box [start, end) at r's position.
func (l *topLevelLayout) readMoov(r io.ReadSeeker, start, end, payloadLen int64) error {
	l.moov = make([]byte, payloadLen)
	if _, err := io.ReadFull(r, l.moov); err != nil {
		l.moov = nil
		return fmt.Errorf("cr3: read moov box payload: %w", err)
	}
	l.moovStart, l.moovEnd = start, end
	return nil
}

// readXMPBox reads the 16-byte UUID of the uuid box [start, end) at r's
// position and, when it is the XMP UUID, records the box and (when wantXMP is
// set) reads its payload.
func (l *topLevelLayout) readXMPBox(r io.ReadSeeker, start, end, payloadLen int64, wantXMP bool) error {
	var id [16]byte
	if _, err := io.ReadFull(r, id[:]); err != nil {
		return fmt.Errorf("cr3: read uuid box: %w", err)
	}
	if !bytes.Equal(id[:], xmpUUID) {
		return nil
	}
	l.xmpStart, l.xmpEnd = start, end
	if !wantXMP {
		return nil
	}
	l.xmp = make([]byte, payloadLen-16)
	if _, err := io.ReadFull(r, l.xmp); err != nil {
		return fmt.Errorf("cr3: read XMP uuid box payload: %w", err)
	}
	return nil
}

// Segments holds the metadata payloads of a CR3 file.
type Segments struct {
	// EXIF is the CMT1 TIFF stream (IFD0), with CMT2 appended when the
	// ExifIFD pointer of IFD0 points beyond CMT1 (see mergeCMT).
	EXIF []byte
	// MakerNote is the CMT3 TIFF stream: its first IFD is the Canon
	// MakerNote IFD. nil when absent.
	MakerNote []byte
	// GPS is the CMT4 TIFF stream: its first IFD is the GPS IFD. nil when
	// absent.
	GPS []byte
	// XMP is the packet of the top-level XMP uuid box, or, when the file has
	// none, of a legacy "XMP " sub-box of the Canon uuid box. nil when absent.
	XMP []byte
}

// Extract reads metadata from a CR3 file by navigating the ISOBMFF box tree.
// rawEXIF is Segments.EXIF and rawXMP is Segments.XMP (see ExtractSegments);
// rawIPTC is always nil (CR3 carries no IPTC).
//
// If the moov/UUID structure is present but no CMT1 sub-box is found, Extract
// returns (nil, nil, rawXMP, ErrNoCMT1Box), with rawXMP still populated.
//
// # audit finding #138
//
// ErrNoCMT1Box lets callers distinguish "no EXIF" from a broken container parse.
// lclevy canon_cr3: CMT1 carries IFD0; its absence means no EXIF is present.
func Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	seg, err := ExtractSegments(r)
	return seg.EXIF, nil, seg.XMP, err
}

// ExtractSegments reads every metadata payload of a CR3 file: the CMT1-CMT4
// TIFF streams of the Canon uuid box in moov (imagemeta readCrxMoovBox:
// CMT1 = IFD0, CMT2 = Exif IFD, CMT3 = MakerNote IFD, CMT4 = GPS IFD) and the
// XMP packet of the top-level XMP uuid box (task #300).
//
// When the moov box has no Canon uuid box, the CMT sub-boxes are searched in
// the moov box itself. When the file has no top-level XMP uuid box, a legacy
// "XMP " sub-box is read instead. If no CMT1 sub-box exists, the returned
// Segments carry everything else and the error is ErrNoCMT1Box.
//
// #286: only ftyp, moov and the XMP uuid box are read — typically tens of KB
// — instead of the whole file, which is mostly raw sensor data in mdat. Every
// returned slice is freshly allocated at its exact size, so none retains the
// moov buffer.
func ExtractSegments(r io.ReadSeeker) (Segments, error) {
	var seg Segments
	fileLen, err := seekFileLen(r)
	if err != nil {
		return seg, err
	}
	// #140: reject an oversized input before any content is read.
	if fileLen > maxFileSize {
		return seg, fmt.Errorf("cr3: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}

	lay, err := scanTopLevel(r, fileLen, true)
	if err != nil {
		return seg, err
	}
	if lay.moov == nil {
		return seg, ErrNoMoovBox
	}

	var cb canonBoxes
	if canon := findUUIDBox(lay.moov, canonUUID); canon != nil {
		cb = scanCanonBoxes(canon)
	} else {
		// Fall back: search for the sub-boxes anywhere in the moov box.
		cb = canonBoxes{
			cmt: [4][]byte{findBox(lay.moov, boxCMT1, 0), findBox(lay.moov, boxCMT2, 0), findBox(lay.moov, boxCMT3, 0), findBox(lay.moov, boxCMT4, 0)},
			xmp: findBox(lay.moov, boxXMP, 0),
		}
	}
	// lay.xmp is already an exact-size allocation of its own.
	seg.XMP = lay.xmp
	if seg.XMP == nil {
		seg.XMP = bytes.Clone(cb.xmp)
	}
	seg.MakerNote = bytes.Clone(cb.cmt[2])
	seg.GPS = bytes.Clone(cb.cmt[3])
	if cb.cmt[0] == nil {
		// audit #138: surface missing CMT1 as a sentinel error so callers can
		// distinguish no-EXIF from a broken container.
		return seg, ErrNoCMT1Box
	}
	seg.EXIF = bytes.Clone(mergeCMT(cb.cmt[0], cb.cmt[1]))
	return seg, nil
}

// canonBoxes holds the payloads of the first CMT1-CMT4 and "XMP " sub-boxes
// of the Canon uuid box (nil when absent).
type canonBoxes struct {
	cmt [4][]byte // CMT1, CMT2, CMT3, CMT4
	xmp []byte
}

// scanCanonBoxes collects the canonBoxes of the Canon uuid payload canon in
// one flat walk over its children.
func scanCanonBoxes(canon []byte) canonBoxes {
	var cb canonBoxes
	pos := 0
	for pos+8 <= len(canon) {
		size, typ, headerLen, ok := parseCR3BoxHeader(canon, pos)
		if !ok {
			break
		}
		payload := canon[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		pos += int(size)                                     //nolint:gosec // G115: ISOBMFF box size bounded by slice length
		if typ == boxXMP && cb.xmp == nil {
			cb.xmp = payload
		} else if i := cmtIndex(typ); i >= 0 && cb.cmt[i] == nil {
			cb.cmt[i] = payload
		}
	}
	return cb
}

// cmtIndex returns 0-3 for the box types CMT1-CMT4 and -1 for any other.
func cmtIndex(typ [4]byte) int {
	if typ[0] != 'C' || typ[1] != 'M' || typ[2] != 'T' || typ[3] < '1' || typ[3] > '4' {
		return -1
	}
	return int(typ[3] - '1')
}

// ParseSubIFD parses the first IFD of a CMT TIFF stream (Segments.MakerNote
// or Segments.GPS). Each CMT box is a complete TIFF stream (TIFF 6.0 §2:
// byte order, magic 42, IFD offset), so its value offsets are relative to the
// start of the box. The entries' out-of-line values alias b.
func ParseSubIFD(b []byte) (*exif.IFD, binary.ByteOrder, error) {
	order, ifdOff, ok := tiffHeader(b)
	if !ok {
		return nil, nil, ErrBadCMTStream
	}
	ifd, _, ok := exif.ParseIFDAt(b, ifdOff, order)
	if !ok {
		return nil, nil, ErrBadCMTStream
	}
	return ifd, order, nil
}

// tiffHeader decodes the 8-byte TIFF header of b (TIFF 6.0 §2).
func tiffHeader(b []byte) (order binary.ByteOrder, ifdOff uint32, ok bool) {
	if len(b) < 8 {
		return nil, 0, false
	}
	switch {
	case b[0] == 'I' && b[1] == 'I':
		order = binary.LittleEndian
	case b[0] == 'M' && b[1] == 'M':
		order = binary.BigEndian
	default:
		return nil, 0, false
	}
	if order.Uint16(b[2:]) != 0x002A {
		return nil, 0, false
	}
	return order, order.Uint32(b[4:]), true
}

// getExifIFDOffset detects byte order from cmt1's TIFF header and returns the
// value of the ExifIFD pointer tag (0x8769) in IFD0. Returns 0 if cmt1 is
// too short, the byte-order mark is unrecognised, or the tag is absent.
//
// TIFF 6.0 §2: "II" = little-endian, "MM" = big-endian.
func getExifIFDOffset(cmt1 []byte) uint32 {
	if len(cmt1) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch {
	case cmt1[0] == 'I' && cmt1[1] == 'I':
		order = binary.LittleEndian
	case cmt1[0] == 'M' && cmt1[1] == 'M':
		order = binary.BigEndian
	default:
		return 0
	}
	ifd0Off := order.Uint32(cmt1[4:8]) //nolint:gosec // G602: len(cmt1) >= 8 guaranteed by guard above
	return findExifIFDOffset(cmt1, ifd0Off, order)
}

// mergeCMT combines CMT1 (IFD0 TIFF stream) with CMT2 (Exif IFD bytes) into
// a single contiguous buffer that exif.Parse can traverse.
//
// In CR3 files, the ExifIFD pointer stored in CMT1's IFD0 points to a byte
// offset relative to the start of CMT1. If that offset falls beyond CMT1's
// length, the Exif IFD data lives in CMT2. Appending CMT2 to CMT1 makes the
// pointer valid so the EXIF parser can follow it without modification.
//
// If cmt2 is nil or the ExifIFD pointer lies within CMT1, cmt1 is returned
// unchanged (zero copy).
func mergeCMT(cmt1, cmt2 []byte) []byte {
	if cmt2 == nil || len(cmt1) < 8 {
		return cmt1
	}
	exifIFDOffset := getExifIFDOffset(cmt1)
	// If the ExifIFD offset is within CMT1, no merge needed.
	//
	// Security audit FIX 5 (CWE-681/190): compare in uint64, not int. On a
	// 32-bit platform (GOARCH=386/arm), int(exifIFDOffset) for an offset
	// >= 2^31 is negative, which would make this comparison true for ANY
	// len(cmt1) — silently skipping the CMT1+CMT2 merge and leaving the
	// ExifIFD pointer dangling past the end of cmt1. Mirrors the identical
	// fix in findExifIFDOffset below.
	if exifIFDOffset == 0 || uint64(exifIFDOffset) < uint64(len(cmt1)) {
		return cmt1
	}
	// ExifIFD pointer extends into CMT2: concatenate.
	merged := make([]byte, len(cmt1)+len(cmt2))
	copy(merged, cmt1)
	copy(merged[len(cmt1):], cmt2)
	return merged
}

// hasIFD0Tag reports whether the IFD at ifd0Off in buf has an entry for tag.
func hasIFD0Tag(buf []byte, ifd0Off uint32, order binary.ByteOrder, tag uint16) bool {
	_, found := findIFD0Tag(buf, ifd0Off, order, tag)
	return found
}

// findExifIFDOffset walks IFD0 in buf (starting at ifd0Off) looking for tag
// 0x8769 (ExifIFD) and returns its LONG value (the offset). Returns 0 if not
// found or if buf is too short to parse.
func findExifIFDOffset(buf []byte, ifd0Off uint32, order binary.ByteOrder) uint32 {
	v, _ := findIFD0Tag(buf, ifd0Off, order, 0x8769)
	return v
}

// findIFD0Tag walks the IFD at ifd0Off in buf looking for tag and returns the
// 4-byte value/offset field of its entry. found is false when the tag is
// absent or buf is too short to parse.
func findIFD0Tag(buf []byte, ifd0Off uint32, order binary.ByteOrder, want uint16) (value uint32, found bool) {
	// Security audit FIX 5 (CWE-681/190): compare in uint64, not int, before
	// converting ifd0Off to int. On a 32-bit platform (GOARCH=386/arm),
	// int(ifd0Off) for ifd0Off >= 2^31 is negative, which would let a bad
	// offset pass an int-typed bound check and then panic on buf[pos:].
	// Mirrors the #74 fix in format/detect.go's parseClassicTIFFIFD0 and the
	// #45 fix in format/jpeg's parseIRBEntry.
	if uint64(ifd0Off)+2 > uint64(len(buf)) {
		return 0, false
	}
	count := order.Uint16(buf[ifd0Off:])
	// ifd0Off ≤ uint64(len(buf))-2, and len(buf) is a valid int on this
	// platform, so ifd0Off < len(buf) fits int safely here.
	pos := int(ifd0Off) + 2
	for range int(count) {
		if pos+12 > len(buf) {
			break
		}
		if order.Uint16(buf[pos:]) == want {
			// TIFF 6.0 §2: the last 4 bytes of an entry hold the value or
			// its offset; IFD pointers are LONG, count 1, so this is the
			// pointer itself.
			return order.Uint32(buf[pos+8:]), true
		}
		pos += 12
	}
	return 0, false
}

// tagGPSIFDPointer is the IFD0 tag that points to the GPS IFD
// (CIPA DC-008 §4.6.3, Table 13).
const tagGPSIFDPointer = 0x8825

// splitEXIF separates the GPS IFD from rawEXIF, the single TIFF stream the
// caller encodes, into the two TIFF streams a CR3 file stores them in: cmt1
// (IFD0 and any Exif IFD, for CMT1) and cmt4 (the GPS IFD as the first IFD
// of its own stream, for CMT4). imagemeta readCrxMoovBox: CMT1 = IFD0,
// CMT4 = GPS IFD. Without this split, a GPS IFD read from CMT4 would be
// written a second time inside CMT1, behind a GPS pointer the source never
// had.
//
// When rawEXIF has no GPS IFD, cmt1 is rawEXIF itself and cmt4 is nil.
func splitEXIF(rawEXIF []byte) (cmt1, cmt4 []byte, err error) {
	order, ifdOff, ok := tiffHeader(rawEXIF)
	if !ok || !hasIFD0Tag(rawEXIF, ifdOff, order, tagGPSIFDPointer) {
		return rawEXIF, nil, nil
	}
	e, err := exif.Parse(rawEXIF, exif.SkipMakerNote())
	if err != nil {
		return nil, nil, fmt.Errorf("cr3: split the GPS IFD from the EXIF stream: %w", err)
	}
	if e.GPSIFD == nil {
		return rawEXIF, nil, nil
	}
	gps := e.GPSIFD
	e.GPSIFD = nil // exif.Encode emits no GPS pointer without a GPS IFD
	if cmt1, err = exif.Encode(e); err != nil {
		return nil, nil, fmt.Errorf("cr3: encode CMT1: %w", err)
	}
	if cmt4, err = exif.Encode(&exif.EXIF{ByteOrder: e.ByteOrder, IFD0: gps}); err != nil {
		return nil, nil, fmt.Errorf("cr3: encode CMT4: %w", err)
	}
	return cmt1, cmt4, nil
}

// sameFirstIFD reports whether the first IFDs of the TIFF streams a and b
// hold the same entries: both are re-encoded with b's byte order by
// exif.Encode, which sorts entries and lays out values canonically, and the
// encodings are compared. It lets Inject keep an unchanged CMT4 box byte for
// byte instead of replacing it with an equivalent re-encoding.
func sameFirstIFD(a, b []byte) bool {
	ia, _, errA := ParseSubIFD(a)
	ib, order, errB := ParseSubIFD(b)
	if errA != nil || errB != nil {
		return false
	}
	ea, errA := exif.Encode(&exif.EXIF{ByteOrder: order, IFD0: ia})
	eb, errB := exif.Encode(&exif.EXIF{ByteOrder: order, IFD0: ib})
	return errA == nil && errB == nil && bytes.Equal(ea, eb)
}

// rebuildUUIDContent iterates the sub-boxes of the Canon UUID box payload and
// reconstructs it with CMT1 replaced by cmt1 (if non-nil), CMT4 replaced by
// cmt4 (if non-nil and its GPS IFD differs from the existing one), and the
// legacy "XMP " sub-box replaced by rawXMP (if both exist). Other sub-boxes,
// including CMT2 and CMT3, are copied unchanged. hadXMP reports whether an
// "XMP " sub-box was present.
//
// A missing CMT1 is appended when cmt1 is non-nil (audit #175: writing EXIF
// to a UUID box that only carries CMT2/CMT3/CMT4 must not discard it). A
// missing CMT4 is inserted right after the last CMT box when cmt4 is non-nil.
// A missing "XMP " sub-box is never created: CR3 stores XMP in the top-level
// XMP uuid box (task #300), which Inject writes.
//
// lclevy canon_cr3: CMT1 is the mandatory IFD0 TIFF stream inside the Canon UUID.
func rebuildUUIDContent(uuidContent, cmt1, cmt4, rawXMP []byte) (newContent []byte, hadXMP bool) {
	b := uuidRebuild{cmt1: cmt1, cmt4: cmt4, rawXMP: rawXMP, afterCMT: -1}
	pos := 0
	for pos+8 <= len(uuidContent) {
		size, typ, headerLen, ok := parseCR3BoxHeader(uuidContent, pos)
		if !ok {
			break
		}
		b.add(typ, uuidContent[pos:pos+int(size)], int(headerLen)) //nolint:gosec // G115: ISOBMFF box size bounded by file size; headerLen is 8 or 16
		pos += int(size)                                           //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return b.finish(), b.hadXMP
}

// uuidRebuild accumulates the output of rebuildUUIDContent.
type uuidRebuild struct {
	buf                bytes.Buffer
	cmt1, cmt4, rawXMP []byte
	hadCMT1, hadCMT4   bool
	hadXMP             bool
	afterCMT           int // buf length right after the last CMT box written
}

// add writes the output for one source sub-box of type typ whose header is
// headerLen bytes long.
func (b *uuidRebuild) add(typ [4]byte, box []byte, headerLen int) {
	switch typ {
	case boxCMT1:
		b.hadCMT1 = true
		b.buf.Write(replacedBox("CMT1", b.cmt1, box))
		b.afterCMT = b.buf.Len()
	case boxCMT4:
		b.hadCMT4 = true
		if b.cmt4 != nil && sameFirstIFD(box[headerLen:], b.cmt4) {
			b.buf.Write(box) // unchanged GPS IFD: keep the source bytes
		} else {
			b.buf.Write(replacedBox("CMT4", b.cmt4, box))
		}
		b.afterCMT = b.buf.Len()
	case boxCMT2, boxCMT3:
		b.buf.Write(box)
		b.afterCMT = b.buf.Len()
	case boxXMP:
		b.hadXMP = true
		b.buf.Write(replacedBox("XMP ", b.rawXMP, box))
	default:
		b.buf.Write(box)
	}
}

// finish inserts a missing CMT4 after the last CMT box and appends a
// missing CMT1, and returns the rebuilt content.
func (b *uuidRebuild) finish() []byte {
	out := b.buf.Bytes()
	if b.cmt4 != nil && !b.hadCMT4 {
		at := b.afterCMT
		if at < 0 {
			at = len(out)
		}
		out = slices.Insert(out, at, buildBox("CMT4", b.cmt4)...)
	}
	// audit #175: insert a new CMT1 sub-box when cmt1 is provided but the
	// original UUID content had no CMT1.
	if b.cmt1 != nil && !b.hadCMT1 {
		out = append(out, buildBox("CMT1", b.cmt1)...)
	}
	return out
}

// replacedBox returns a new box of type typ holding payload when payload is
// non-nil, and the source box orig otherwise.
func replacedBox(typ string, payload, orig []byte) []byte {
	if payload == nil {
		return orig
	}
	return buildBox(typ, payload)
}

// findMoovRange returns the start and end byte positions of the first moov box
// in data. start is the index of the first byte of the box header; end is the
// first byte after the box. Returns (0,0,false) if no moov box is found.
func findMoovRange(data []byte) (start, end int, found bool) {
	pos := 0
	for pos+8 <= len(data) {
		size, typ, _, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		if typ == boxMoov {
			return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// chunkShift maps an absolute offset of the source file to the output file
// after up to two box replacements: an offset at or beyond the old end of a
// replaced box moves by that box's size change. ISO 14496-12 §8.7.5: chunk
// offsets are absolute file offsets, so every chunk stored after a resized
// box moves with it.
type chunkShift struct {
	regions [2]shiftRegion
	n       int
}

// shiftRegion is one replaced region of a chunkShift.
type shiftRegion struct {
	end   int64 // old end of the replaced region
	delta int64 // its size change
}

// add records a replaced region; at most two are supported (Inject edits
// the moov box and the XMP uuid box). A zero delta is not recorded.
func (s *chunkShift) add(end, delta int64) {
	if delta != 0 && s.n < len(s.regions) {
		s.regions[s.n] = shiftRegion{end: end, delta: delta}
		s.n++
	}
}

// apply returns the output-file position of source offset o. Every
// threshold is compared with the SOURCE offset: comparing a partially shifted
// value would skip a later region when an earlier one shrank.
func (s *chunkShift) apply(o int64) int64 {
	shifted := o
	for _, r := range s.regions[:s.n] {
		if o >= r.end {
			shifted += r.delta
		}
	}
	return shifted
}

// relocateChunkOffsets walks moovBytes in-place and moves every stco/co64
// entry by shift.
//
// ISO 14496-12 §8.7.5 (ChunkOffsetBox 'stco' / ChunkLargeOffsetBox 'co64'):
// FullBox version+flags(4), entry_count(4), then entry_count chunk offsets of
// 4 bytes (stco) or 8 bytes (co64).
//
// moovBytes is the complete rebuilt moov box (including its 8-byte header),
// freshly allocated by buildBox — no aliasing with the original data. For
// stco, an offset that would exceed math.MaxUint32 returns ErrStcoOverflow
// rather than being truncated.
func relocateChunkOffsets(moovBytes []byte, shift *chunkShift) error {
	if len(moovBytes) < 8 || shift.n == 0 {
		return nil
	}
	// moovBytes includes the 8-byte moov box header; pass the content slice.
	// audit #191: start at depth=0; cap recursion at 32 levels (mirrors findBox).
	return relocateInContainer(moovBytes[8:], shift, 0)
}

// relocateInContainer recursively walks ISOBMFF boxes in the container content
// slice (data). Only container box types that lead to stco/co64 are recursed
// into: trak, mdia, minf, stbl. stco and co64 boxes are patched in-place.
//
// data must be exactly the content payload of the enclosing container box,
// so each recursion level only scans within its own box boundary.
//
// audit #191: the guard `depth > 32` mirrors the cap used by findBox —
// defence-in-depth against crafted ISOBMFF structures with pathological
// nesting. ISO 14496-12 §4.2: box hierarchy is structurally bounded by box
// sizes.
func relocateInContainer(data []byte, shift *chunkShift, depth int) error {
	if depth > 32 {
		return nil
	}
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		// content is the box payload (after the header bytes), strictly bounded.
		content := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by slice length

		switch typ {
		case boxTrak, boxMdia, boxMinf, boxStbl:
			if err := relocateInContainer(content, shift, depth+1); err != nil {
				return err
			}
		case boxStco:
			if err := relocateStco(content, shift); err != nil {
				return err
			}
		case boxCo64:
			if err := relocateCo64(content, shift); err != nil {
				return err
			}
		}

		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by slice length
	}
	return nil
}

// relocateStco patches stco entries (uint32) in-place.
// content is the stco box payload: version (1B) + flags (3B) + entry_count (4B) + entries (N×4B).
func relocateStco(content []byte, shift *chunkShift) error {
	if len(content) < 8 {
		return nil // too small to hold version/flags + entry_count
	}
	entryCount := binary.BigEndian.Uint32(content[4:])
	const entryStart = 8
	if uint64(entryCount) > uint64(len(content)-entryStart)/4 { //nolint:gosec // G115: len(content) >= 8
		return nil
	}
	for i := range int(entryCount) {
		off := entryStart + i*4
		orig := int64(binary.BigEndian.Uint32(content[off:]))
		relocated := shift.apply(orig)
		if relocated < 0 || relocated > math.MaxUint32 {
			return fmt.Errorf("cr3: stco offset %d relocated to %d: %w", orig, relocated, ErrStcoOverflow)
		}
		binary.BigEndian.PutUint32(content[off:], uint32(relocated))
	}
	return nil
}

// relocateCo64 patches co64 entries (uint64) in-place.
// content is the co64 box payload: version (1B) + flags (3B) + entry_count (4B) + entries (N×8B).
func relocateCo64(content []byte, shift *chunkShift) error {
	if len(content) < 8 {
		return nil
	}
	entryCount := binary.BigEndian.Uint32(content[4:])
	const entryStart = 8
	if uint64(entryCount) > uint64(len(content)-entryStart)/8 { //nolint:gosec // G115: len(content) >= 8
		return nil
	}
	for i := range int(entryCount) {
		off := entryStart + i*8
		raw := binary.BigEndian.Uint64(content[off:])
		if raw > math.MaxInt64 {
			continue // beyond any file this package accepts; left unchanged
		}
		orig := int64(raw)
		relocated := shift.apply(orig)
		if relocated < 0 {
			return fmt.Errorf("cr3: co64 offset %d relocated to %d: %w", orig, relocated, ErrStcoOverflow)
		}
		binary.BigEndian.PutUint64(content[off:], uint64(relocated))
	}
	return nil
}

// rebuildMoovContent replaces the Canon UUID box inside moovContent with a new
// UUID box containing the rebuilt CMTx payloads. Returns the new moov content
// slice. If no Canon UUID box is found, moovContent is returned unchanged.
func rebuildMoovContent(moovContent, cmt1, cmt4, rawXMP []byte) []byte {
	uuidStart, uuidEnd, hasUUID := flatUUIDBoxRange(moovContent, canonUUID)
	if !hasUUID {
		return moovContent
	}
	// CR3-EXTSIZE-01 fix: re-derive the Canon uuid box's ACTUAL header length
	// instead of assuming the normal 8-byte header. ISO 14496-12 §4.2: when a
	// box's 32-bit size field is 1, the real size lives in an 8-byte largesize
	// field immediately after the type, making the header 16 bytes, not 8.
	// flatUUIDBoxRange already parsed a well-formed box header at uuidStart,
	// so re-parsing it here is guaranteed to succeed.
	_, _, uuidHeaderLen, ok := parseCR3BoxHeader(moovContent, uuidStart)
	if !ok {
		// Unreachable in practice; degrade gracefully rather than panic.
		return moovContent
	}
	// uuidData is the UUID payload: everything after the box header (8 or 16
	// bytes) plus the mandatory 16-byte UUID field. flatUUIDBoxRange guarantees
	// size >= headerLen+16, so this slice cannot underflow.
	uuidData := moovContent[uuidStart+int(uuidHeaderLen)+16 : uuidEnd] //nolint:gosec // G115: uuidHeaderLen is 8 or 16; bounds guaranteed by flatUUIDBoxRange's size>=headerLen+16 check
	newUUIDContent, _ := rebuildUUIDContent(uuidData, cmt1, cmt4, rawXMP)
	newUUIDBox := buildUUIDBox(canonUUID, newUUIDContent)
	// Rebuild moovContent: prefix + newUUIDBox + suffix.
	capacity := len(moovContent) - (uuidEnd - uuidStart) + len(newUUIDBox)
	newContent := make([]byte, 0, capacity)
	newContent = append(newContent, moovContent[:uuidStart]...)
	newContent = append(newContent, newUUIDBox...)
	newContent = append(newContent, moovContent[uuidEnd:]...)
	return newContent
}

// boxEdit replaces the bytes [start, end) of the source file with data
// (start == end inserts data at start).
type boxEdit struct {
	start, end int64
	data       []byte
}

// Inject reads the CR3 from r, writes the metadata payloads into the boxes a
// CR3 file keeps them in, relocates all trak/stbl stco/co64 chunk-offset
// table entries to account for the boxes that changed size, and writes the
// result to w.
//
//   - rawEXIF: IFD0 (and any Exif IFD) go to the CMT1 sub-box of the Canon
//     uuid box; a GPS IFD goes to the CMT4 sub-box, which is kept byte for
//     byte when its GPS IFD is unchanged (splitEXIF, sameFirstIFD). CMT2 and
//     CMT3 are never rewritten. A source CMT4 is kept when rawEXIF has no
//     GPS IFD.
//   - rawXMP: the packet goes to the top-level XMP uuid box (task #300),
//     replacing the existing one or, when there is none, inserted right
//     after moov; a legacy "XMP " sub-box of the Canon uuid box written by
//     earlier GoMetadata versions is kept in step.
//   - rawIPTC is ignored: CR3 does not carry IPTC.
//
// #292: only the moov box (and the XMP uuid box) is rebuilt in memory; every
// other byte of the file — ftyp, mdat and anything else — is streamed from r
// to w verbatim through a fixed-size pooled buffer (iobuf.StreamCopyN),
// mirroring ExifTool's WriteQuickTime.pl model ("rewrite the moov atom, copy
// mdat straight through").
//
// Offset relocation (ISO 14496-12 §8.7.3 / §8.7.5): every chunk offset O of
// every trak → mdia → minf → stbl → {stco, co64} box inside the rebuilt moov
// is moved by the size change of each edited box whose old end is at or
// before O (chunkShift). stco entries are uint32; an entry that would exceed
// MaxUint32 returns ErrStcoOverflow rather than being truncated.
//
// preserveUnknownSegments must be true; passing false returns
// ErrPreserveUnknownSegmentsNotSupported because CR3 ISOBMFF boxes are
// structurally mandatory and cannot be selectively stripped.
//
// If all payloads are nil, or no moov box is found, the source is streamed
// through unchanged.
func Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error { //nolint:gocyclo,cyclop // #292: sequential error-checked streaming steps (size cap, pass-through, box locate/rebuild, edit-list reassembly) are inherent in the streaming design; each branch is a distinct, independently-tested failure mode
	if !preserveUnknownSegments {
		return ErrPreserveUnknownSegmentsNotSupported
	}

	// #140/#286/#292: reject an oversized input before any content is read
	// or streamed — seekFileLen never reads the file's content.
	fileLen, err := seekFileLen(r)
	if err != nil {
		return err
	}
	if fileLen > maxFileSize {
		return fmt.Errorf("cr3: input exceeds %d bytes: %w", maxFileSize, ErrFileTooLarge)
	}

	// All payloads nil: no box changes size, so the file is streamed through.
	if rawEXIF == nil && rawIPTC == nil && rawXMP == nil {
		return streamWholeFile(r, w, fileLen)
	}

	lay, err := scanTopLevel(r, fileLen, false)
	if err != nil {
		return err
	}
	if lay.moov == nil {
		// No moov box — file is corrupt/incomplete. Pass through unchanged.
		return streamWholeFile(r, w, fileLen)
	}

	cmt1, cmt4, err := splitEXIF(rawEXIF)
	if err != nil {
		return err
	}
	var editBuf [2]boxEdit
	edits, moovIdx := planEdits(editBuf[:0], &lay, buildBox("moov", rebuildMoovContent(lay.moov, cmt1, cmt4, rawXMP)), rawXMP)

	var shift chunkShift
	for _, e := range edits {
		shift.add(e.end, int64(len(e.data))-(e.end-e.start))
	}
	// The moov edit's data is the freshly built moov box: patch it in place.
	if err := relocateChunkOffsets(edits[moovIdx].data, &shift); err != nil {
		return fmt.Errorf("cr3: stco/co64 offset relocation: %w", err)
	}
	return streamEdits(r, w, fileLen, edits)
}

// planEdits appends to dst the box edits of Inject in file order — the moov
// box replaced by newMoov and, when rawXMP is non-nil, the top-level XMP uuid
// box replaced (or inserted right after moov when the file has none) — and
// returns them with the index of the moov edit.
func planEdits(dst []boxEdit, lay *topLevelLayout, newMoov, rawXMP []byte) (edits []boxEdit, moovIdx int) {
	moov := boxEdit{start: lay.moovStart, end: lay.moovEnd, data: newMoov}
	if rawXMP == nil {
		return append(dst, moov), 0
	}
	xmp := boxEdit{start: lay.moovEnd, end: lay.moovEnd, data: buildUUIDBox(xmpUUID, rawXMP)}
	if lay.hasXMPBox() {
		xmp.start, xmp.end = lay.xmpStart, lay.xmpEnd
	}
	if xmp.start < moov.start {
		return append(dst, xmp, moov), 1
	}
	return append(dst, moov, xmp), 0
}

// streamEdits writes r to w with each edit applied: the bytes between edits
// are streamed verbatim through a pooled buffer (iobuf.StreamCopyN); the
// edited boxes are written from memory. edits must be sorted by start and
// non-overlapping.
func streamEdits(r io.ReadSeeker, w io.Writer, fileLen int64, edits []boxEdit) error {
	pos := int64(0)
	for _, e := range edits {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return fmt.Errorf("cr3: seek: %w", err)
		}
		if err := iobuf.StreamCopyN(r, w, e.start-pos); err != nil {
			return fmt.Errorf("cr3: copy unchanged bytes: %w", err)
		}
		if _, err := w.Write(e.data); err != nil {
			return fmt.Errorf("cr3: write box: %w", err)
		}
		pos = e.end
	}
	if _, err := r.Seek(pos, io.SeekStart); err != nil {
		return fmt.Errorf("cr3: seek: %w", err)
	}
	if err := iobuf.StreamCopyN(r, w, fileLen-pos); err != nil {
		return fmt.Errorf("cr3: copy mdat and trailing bytes: %w", err)
	}
	return nil
}

// streamWholeFile copies all fileLen bytes of r, from its current position
// reset to 0, to w via a pooled buffer — never allocating memory
// proportional to fileLen. Used by Inject's two pass-through paths (all
// payloads nil; no moov box found).
func streamWholeFile(r io.ReadSeeker, w io.Writer, fileLen int64) error {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("cr3: seek: %w", err)
	}
	if err := iobuf.StreamCopyN(r, w, fileLen); err != nil {
		return fmt.Errorf("cr3: copy: %w", err)
	}
	return nil
}

// buildBox constructs an ISOBMFF box: [4-byte size][4-byte type][content].
func buildBox(boxType string, content []byte) []byte {
	size := 8 + len(content)
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:], uint32(size)) //nolint:gosec // G115: ISOBMFF box size bounded by content length
	copy(box[4:8], boxType)
	copy(box[8:], content)
	return box
}

// buildUUIDBox constructs a uuid box: [8-byte header][16-byte UUID][content].
func buildUUIDBox(uuid []byte, content []byte) []byte {
	size := 8 + 16 + len(content)
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:], uint32(size)) //nolint:gosec // G115: ISOBMFF box size bounded by content length
	copy(box[4:8], "uuid")
	copy(box[8:24], uuid)
	copy(box[24:], content)
	return box
}

// flatUUIDBoxRange finds the Canon UUID box in data (flat scan).
// Returns start and end of the full uuid box (header included).
func flatUUIDBoxRange(data []byte, uuid []byte) (start, end int, found bool) {
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		// ISOBMFF (ISO 14496-12) §4.2: a uuid box payload must accommodate both
		// the header and the mandatory 16-byte UUID field. The size>=headerLen+16
		// guard mirrors the identical check already present in findUUIDBox below;
		// without it, rebuildMoovContent's slice
		// (moovContent[uuidStart+headerLen+16 : uuidEnd]) could underflow on a
		// box whose declared size is smaller than headerLen+16.
		if typ == boxUUID && size >= headerLen+16 && pos+int(headerLen)+16 <= len(data) { //nolint:gosec // G115: headerLen is 8 or 16
			if matchesUUID(data[pos+int(headerLen):], uuid) { //nolint:gosec // G115: headerLen is 8 or 16
				return pos, pos + int(size), true //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return 0, 0, false
}

// findBox performs a search for the first box of the given type in data,
// recursing into container boxes up to depth levels deep (max 32) to
// prevent stack exhaustion on crafted ISOBMFF input.
func findBox(data []byte, boxType [4]byte, depth int) []byte {
	if depth > 32 {
		return nil
	}
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		boxData := data[pos+int(headerLen) : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
		if typ == boxType {
			return boxData
		}
		// Recurse into container boxes.
		if typ == boxMoov || typ == boxTrak || typ == boxUdta || typ == boxMdia {
			if inner := findBox(boxData, boxType, depth+1); inner != nil {
				return inner
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil
}

// findUUIDBox searches for a 'uuid' box whose UUID matches the given bytes.
func findUUIDBox(data []byte, uuid []byte) []byte {
	pos := 0
	for pos+8 <= len(data) {
		size, typ, headerLen, ok := parseCR3BoxHeader(data, pos)
		if !ok {
			break
		}
		// ISOBMFF (ISO 14496-12) §4.2: a uuid box payload must accommodate
		// both the header and the 16-byte UUID field before the content slice.
		// size >= headerLen is guaranteed by parseCR3BoxHeader; also require
		// size >= headerLen+16 so that data[pos+headerLen+16 : pos+size] is safe.
		if typ == boxUUID && size >= headerLen+16 && pos+int(headerLen)+16 <= len(data) { //nolint:gosec // G115: headerLen is 8 or 16
			if matchesUUID(data[pos+int(headerLen):], uuid) { //nolint:gosec // G115: headerLen is 8 or 16
				return data[pos+int(headerLen)+16 : pos+int(size)] //nolint:gosec // G115: ISOBMFF box size bounded by file size
			}
		}
		pos += int(size) //nolint:gosec // G115: ISOBMFF box size bounded by file size
	}
	return nil
}

func matchesUUID(data, uuid []byte) bool {
	if len(data) < 16 || len(uuid) < 16 {
		return false
	}
	for i := range 16 {
		if data[i] != uuid[i] {
			return false
		}
	}
	return true
}
