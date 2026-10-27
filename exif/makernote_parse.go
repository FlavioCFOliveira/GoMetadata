package exif

// Adapted from metadata-extractor Source/com/drew/imaging/tiff/TiffReader.java:TiffReader.processIfd, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0
// Changes: only the byte-swapped entry-count heuristic is adapted, and it is applied to MakerNote IFDs only (mnView.ifdAt).
// Adapted from metadata-extractor Source/com/drew/metadata/exif/ExifTiffHandler.java:ExifTiffHandler.processMakernote, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0
// Changes: only the headerless Nikon branch (the IFD begins with the first MakerNote byte: Coolpix 775, E990, D1) is adapted, and the byte order of that IFD is taken from its entry count, with the heuristic above, instead of the guard on a big-endian count that metadata-extractor does not have (nikonType1, task #364).

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
)

// parseMakerNoteIFD attempts to parse the raw MakerNote bytes into an IFD.
// Returns nil when the format is unrecognised or parsing fails.
//
// The layout is detected from the MakerNote signature first and from cameraMake
// second (detectMakerNote, task #304). cameraMake is trimmed of
// leading/trailing whitespace before detection. Real-world camera files (e.g. Canon EOS bodies) sometimes
// store the Make field with a trailing space ("Canon "); trimming aligns
// our behaviour with ExifTool's MakerNote dispatch logic.
//
// Supported formats:
//   - Canon: plain IFD at offset 0, parent byte order (CIPA MakerNote §Canon)
//   - Nikon Type 3: embedded TIFF header at offset 10 within "Nikon\0" prefix
//   - Nikon headerless: plain IFD at offset 0, either byte order (Coolpix 775, E990, D1)
//   - Sony: plain IFD at offset 0, parent byte order
//   - Fujifilm: "FUJIFILM" prefix, LE IFD at the offset stored at [8..11]
//   - Olympus Type 2: "OLYMPUS\0" prefix, byte order at [8..9], IFD at 12
//   - Pentax AOC: "AOC\0" prefix, big-endian IFD at offset 6
//   - Pentax PENTAX: "PENTAX \0" prefix, byte order at [8..9], IFD at 12
//   - Panasonic: "Panasonic\0\0\0" prefix, LE IFD at offset 12
//   - Leica Type 0: plain IFD at offset 0, parent byte order
//   - Leica Type 1–5: "LEICA\0" prefix, IFD at offset 8
//   - DJI: plain IFD at offset 0, LE (drones and action cameras)
//   - Samsung: plain IFD at offset 0, parent byte order
//   - Sigma: "SIGMA\0\0\0" or "FOVEON\0\0" prefix, LE IFD at offset 10
//   - Casio: plain IFD at offset 0, parent byte order
//
// Detected by signature or Make family since task #304 (see
// makernote_detect.go): Olympus "OLYMP\0"/Epson/Agfa (IFD at 8), "OM SYSTEM",
// Nikon Type 1 header, "SONY CAM"/"SONY DSC", "SEMC MS", Kyocera, Sanyo,
// Ricoh, "Apple iOS" and Minolta; Kodak and Reconyx are recognised but are
// binary layouts without an IFD.
//
// Arena exclusion (task #198, performance audit 2026-06-10):
// The per-Parse lazy sub-IFD arena (parseArena in Parse) does NOT extend to
// MakerNote IFDs.  There are three reasons:
//
//  1. Buffer origin: every manufacturer parser calls traverse(mn.Value, off, order, nil, false)
//     where mn.Value is the raw MakerNote blob.  Some parsers (Nikon Type 3,
//     Fujifilm) further sub-slice that blob (e.g. tiff := b[tiffStart:]).
//     IFDEntry.Value fields in MakerNote IFDs alias a different buffer origin
//     from the main TIFF parse buffer; mixing them into the same entryBatch
//     arena would violate the cap-clamping safety contract.
//
//  2. Format detection is interleaved with parsing: the 18+ manufacturer paths
//     determine IFD count only after running format-detection heuristics (magic
//     prefix check, byte-order scan, offset lookup).  Pre-scanning to obtain an
//     entry count would require duplicating all that logic — significant
//     complexity for ~2 allocs/op gain on camera files.
//
//  3. Unknown IFD count at scan time: scanSubIFDs (the arena pre-scan) runs
//     before parseExifSubIFDs, before the Make tag is available to select a
//     parser.  Without knowing the manufacturer format we cannot predict the IFD
//     start offset or count.
//
// MakerNote IFDs therefore use the standard per-IFD allocation path (traverse →
// parseSingleIFD → *IFD + []IFDEntry per IFD).  BenchmarkMakerNoteDispatch
// holds steady at 6 allocs/op and ~280 ns/op, unchanged by the arena work.
func parseMakerNoteIFD(b []byte, cameraMake string, parentOrder binary.ByteOrder) *IFD {
	// Trim surrounding whitespace: real-world files (Canon, Nikon) sometimes store
	// Make values with trailing spaces. ExifTool normalises the value before its
	// own MakerNote dispatch; we do the same to avoid silent dispatch failures.
	trimmed := strings.TrimSpace(cameraMake)
	if parse := mnLayouts[detectLayout(b, trimmed)].parse; parse != nil {
		return parse(blobView(b), parentOrder)
	}
	return nil
}

// makerNoteDispatch performs MakerNote IFD dispatch directly from the raw
// IFDEntry.Value bytes of the Make tag, bypassing the heap allocation that
// (*IFDEntry).String() would incur.
//
// Trim semantics are byte-for-byte equivalent to the string-based path:
//   - the cut at the first NUL byte        ≡ (*IFDEntry).String()'s NUL termination
//   - bytes.TrimSpace(…)                  ≡ strings.TrimSpace applied inside parseMakerNoteIFD
//
// Detection (detectMakerNote) works on the []byte directly through generic
// ASCII-fold helpers, so no string is built and nothing is allocated; this
// replaces the string([]byte) map-key idiom of task #202 and is covered by
// TestMakerNoteDispatchZeroAllocLookup.
//
// The trimmed slice is transient and never retained beyond this call, so there
// is no aliasing risk with the task-#198 parse arena or the task-#240 entry pool.
//
// makerNoteDispatch parses makerNote on its own bytes; Parse calls
// makerNoteDispatchAt, which also resolves TIFF-relative value offsets.
//
// EXIF §4.6.4, tag 0x010F (Make): ASCII, NUL-terminated. Real-world cameras
// (Canon EOS bodies) sometimes write a trailing space before the NUL terminator.
// ExifTool normalises the Make value before its own MakerNote dispatch; we match
// that behaviour. task #202, performance audit 2026-06-10.
func makerNoteDispatch(makeValue []byte, parentOrder binary.ByteOrder, makerNote []byte) *IFD {
	return makerNoteDispatchAt(makeValue, parentOrder, makerNote, nil, 0)
}

// makerNoteDispatchAt is makerNoteDispatch for a MakerNote read from a TIFF
// stream: tiff is that stream and mnOff the offset of makerNote in it
// (tiff[mnOff:mnOff+len(makerNote)] is makerNote). When the detected layout
// counts its value offsets from the TIFF header (mnBaseTIFF) the IFD is read
// from tiff, so values the camera stored outside the MakerNote are found
// (task #312, metadata-extractor processMakernote). A nil tiff, or an mnOff
// that does not locate makerNote inside it, parses the MakerNote on its own
// bytes.
func makerNoteDispatchAt(makeValue []byte, parentOrder binary.ByteOrder, makerNote, tiff []byte, mnOff uint64) *IFD {
	// Task #304: detection is by MakerNote signature first, Make family second
	// (see detectMakerNote), so an empty or unknown Make no longer disables a
	// signed MakerNote. Detection works on the []byte directly and allocates
	// nothing (the former string([]byte) map-key idiom of task #202 is not
	// needed any more).
	l := detectLayout(makerNote, trimMake(makeValue))
	parse := mnLayouts[l].parse
	if parse == nil {
		return nil
	}
	return parse(makerNoteView(l, makerNote, tiff, mnOff), parentOrder)
}

// trimMake cuts a raw Make value at its first NUL byte (matching
// (*IFDEntry).String(), Task #386) and then strips surrounding ASCII
// whitespace (matching strings.TrimSpace in parseMakerNoteIFD; Canon EOS
// bodies emit "Canon "). Both steps return sub-slices: nothing is allocated.
func trimMake(makeValue []byte) []byte {
	if i := bytes.IndexByte(makeValue, 0); i >= 0 {
		makeValue = makeValue[:i]
	}
	return bytes.TrimSpace(makeValue)
}

// makerNoteView returns the view layout l parses: the TIFF stream as base for
// a TIFF-relative layout when tiff[mnOff:] holds makerNote, the MakerNote
// alone otherwise.
func makerNoteView(l mnLayout, makerNote, tiff []byte, mnOff uint64) mnView {
	if mnLayouts[l].base == mnBaseTIFF && mnOff > 0 && len(makerNote) > 0 &&
		mnOff <= uint64(len(tiff)) && uint64(len(makerNote)) <= uint64(len(tiff))-mnOff &&
		&tiff[mnOff] == &makerNote[0] {
		return mnView{base: tiff, at: int(mnOff), n: len(makerNote)} //nolint:gosec // G115: mnOff <= len(tiff)
	}
	return blobView(makerNote)
}

// MakerNoteLayout describes where the IFD of a MakerNote starts and how its
// value offsets are based, which a writer needs to keep the MakerNote readable
// when it moves the MakerNote to another position.
type MakerNoteLayout struct {
	// IFDOffset is the offset of the MakerNote IFD from the first MakerNote
	// byte.
	IFDOffset int
	// ByteOrder is the byte order of the MakerNote IFD.
	ByteOrder binary.ByteOrder
	// TIFFRelative is true when the value offsets of the IFD count from the
	// start of the enclosing TIFF stream (Canon, Sony, Panasonic, Casio,
	// Olympus "OLYMP\0", and the other layouts metadata-extractor resolves
	// against the TIFF header). Those offsets change when the MakerNote moves.
	// It is false when they count from a point inside the MakerNote (Nikon
	// Type 3, Olympus "OLYMPUS\0", Fujifilm, Pentax, Apple, ...), so the
	// MakerNote can move unchanged.
	TIFFRelative bool
	// Base is the offset, from the first MakerNote byte, of the point that
	// the value offsets of a layout with TIFFRelative false count from: 0,
	// except for Nikon Type 3, whose offsets count from its embedded TIFF
	// header (8 or 10 bytes into the MakerNote). It is 0 when TIFFRelative.
	Base int

	// layout is the detected layout, for the rules that depend on the vendor
	// (see makernote_rebase.go).
	layout mnLayout
}

// MakerNoteLayout reports the layout of e.MakerNote, detected as Parse
// detects it (signature first, then the Make of IFD0). A MakerNote that no
// signature or Make identifies but that holds a plausible IFD at its first
// byte (a non-zero entry count whose entries fit in the MakerNote) is
// reported as a TIFF-relative IFD at offset 0 in the TIFF byte order: the
// layout ExifTool assumes for unknown MakerNotes (MakerNotes.pm,
// MakerNoteUnknown: an IFD with the default, TIFF-header base) and the one of
// most signature-less vendors (Canon, Sony, Samsung, Casio). ok is false when
// e has no MakerNote, when the MakerNote is not an IFD layout, or when its IFD
// cannot be parsed. It parses the MakerNote once and does not modify e.
func (e *EXIF) MakerNoteLayout() (layout MakerNoteLayout, ok bool) {
	if e == nil || len(e.MakerNote) == 0 {
		return MakerNoteLayout{}, false
	}
	var makeValue []byte
	if e.IFD0 != nil {
		if me := e.IFD0.Get(TagMake); me != nil {
			makeValue = me.Value
		}
	}
	order := e.ByteOrder
	if order == nil {
		order = binary.LittleEndian
	}
	return makerNoteLayoutOf(e.MakerNote, trimMake(makeValue), order)
}

// makerNoteLayoutOf is MakerNoteLayout for the MakerNote bytes mn of a camera
// of Make mk (already trimmed) in a stream of byte order order.
func makerNoteLayoutOf(mn, mk []byte, order binary.ByteOrder) (MakerNoteLayout, bool) {
	loc, base, l, ok := probeMakerNote(mn, mk, order)
	if !ok {
		return MakerNoteLayout{}, false
	}
	lay := MakerNoteLayout{IFDOffset: loc.off, ByteOrder: loc.order, TIFFRelative: base == mnBaseTIFF, layout: l}
	if !lay.TIFFRelative {
		lay.Base = loc.base
	}
	return lay, true
}

// probeMakerNote parses mn on its own bytes as Parse would for a camera of
// Make mk and returns where its IFD was found and its offset base; an
// undetected MakerNote holding a plausible IFD at offset 0 is reported as
// TIFF-relative (see MakerNoteLayout).
func probeMakerNote(mn, mk []byte, order binary.ByteOrder) (loc mnLoc, base mnBase, l mnLayout, ok bool) {
	l = detectLayout(mn, mk)
	f := mnLayouts[l]
	v := blobView(mn)
	v.loc = &loc
	switch {
	case l == mnNone:
		if parsePlausibleIFD(v, order) == nil {
			return loc, 0, l, false
		}
		f.base = mnBaseTIFF
	case f.parse == nil || f.base == mnBaseNone:
		return loc, 0, l, false
	case f.parse(v, order) == nil:
		// A layout the Make selects for a plain IFD at offset 0 whose parser
		// declines it is still an IFD of the TIFF byte order whose offsets
		// count from the TIFF header, when it is a plausible one: the layout of
		// an unidentified note. Only the repair of a moved MakerNote uses this;
		// Parse still reports no MakerNote IFD for it.
		if f.ifdOffset != 0 || parsePlausibleIFD(v, order) == nil {
			return loc, 0, l, false
		}
		f.base = mnBaseTIFF
	}
	return loc, f.base, l, loc.order != nil
}

// mnView is the MakerNote handed to a layout parser: its bytes, and the
// buffer its value offsets count from (task #312).
//
// For a layout whose offsets count from the MakerNote itself (mnBaseBlob),
// base is b and at is 0. For a layout whose offsets count from the TIFF
// header (mnBaseTIFF), base is the enclosing TIFF stream and at is the
// offset of the MakerNote in it, so that an out-of-line value is read where
// the camera stored it, inside or outside the MakerNote; this is
// metadata-extractor's model (ExifTiffHandler.processMakernote calls
// TiffReader.processIfd with tiffHeaderOffset as the base for these
// layouts). Signature, header and byte-order checks always read b.
//
// loc, when non-nil, receives the position and byte order of the IFD the
// parser accepted (MakerNoteLayout); ifdAt then only checks that the IFD can
// be read and does not build it. The read path leaves loc nil, so a view is
// passed by value and allocates nothing. The MakerNote is base[at:at+n]
// (see bytes): six words, so a view and a byte order fit the argument
// registers of the indirect parser call.
type mnView struct {
	base []byte
	at   int
	n    int
	loc  *mnLoc
}

// mnLoc is the IFD position a MakerNote parser accepted: the offset of the
// IFD from the first MakerNote byte, and its byte order.
type mnLoc struct {
	off   int
	order binary.ByteOrder
	base  int // offset of the origin of the value offsets, when it is inside the MakerNote
}

// blobView returns the view of a MakerNote parsed on its own bytes: value
// offsets count from its first byte.
func blobView(b []byte) mnView { return mnView{base: b, n: len(b)} }

// bytes returns the MakerNote bytes, base[at:at+n].
func (v mnView) bytes() []byte { return v.base[v.at : v.at+v.n] }

// probeIFD is the non-nil result ifdAt returns to a probe (a view with loc
// set), which needs the IFD position only. Read-only; never returned by Parse.
var probeIFD = &IFD{} //nolint:gochecknoglobals // read-only sentinel

// ifdAt parses the IFD that starts off bytes into the MakerNote, with value
// offsets resolved against v.base. The IFD entry count must lie inside the
// MakerNote. It records the position and byte order in v.loc on success.
//
// Byte order: an entry count above 255 whose low byte, read in order, is
// zero is taken as a byte-swapped count and the other byte order is used,
// as metadata-extractor's TiffReader.processIfd does ("Some software modifies
// the byte order of the file, but misses some IFDs (such as makernotes)").
// Corpus files show both cases: Canon notes in "II" inside a re-saved "MM"
// TIFF ("Canon EOS REBEL T3i.jpg", 37 entries per ExifTool), and big-endian
// Panasonic and Sigma notes whose layouts are otherwise little-endian
// ("Panasonic DMC-LC40.jpg" 11, "Sigma DP1.jpg" 58). Without the swap such a
// count reads as thousands of entries and the IFD is garbage.
func (v mnView) ifdAt(off int, order binary.ByteOrder) *IFD {
	b := v.bytes()
	if off < 0 || len(b) < off+2 {
		return nil
	}
	order, count := countOrder(b[off], b[off+1], order)
	start := uint64(v.at) + uint64(off) //nolint:gosec // G115: at and off are non-negative buffer offsets
	if start > math.MaxUint32 {
		return nil
	}
	if v.loc != nil {
		// Probe: accept exactly when traverse would (parseSingleIFD rejects an
		// IFD whose first entry does not fit) without building the IFD.
		if count > 0 && uint64(len(v.base))-start-2 < 12 {
			return nil
		}
		*v.loc = mnLoc{off: off, order: order}
		return probeIFD
	}
	ifd, _, err := traverse(v.base, uint32(start), order, nil, false)
	if err != nil {
		return nil
	}
	return ifd
}

// countOrder applies the byte-swap heuristic of ifdAt to the two bytes b0, b1
// of an IFD entry count read in order, and returns the byte order to use and
// the count in it. The count read in order is above 0xFF with a zero low byte
// exactly when its most significant byte is non-zero and its least
// significant byte is zero; testing the two bytes avoids an interface call on
// the hot path.
func countOrder(b0, b1 byte, order binary.ByteOrder) (binary.ByteOrder, uint16) {
	msb, lsb := b0, b1
	if order != binary.BigEndian {
		msb, lsb = b1, b0
	}
	if msb != 0 && lsb == 0 {
		order = swapOrder(order)
		msb, lsb = lsb, msb
	}
	return order, uint16(msb)<<8 | uint16(lsb)
}

// swapOrder returns the byte order opposite to order.
func swapOrder(order binary.ByteOrder) binary.ByteOrder {
	if order == binary.BigEndian {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

// plainMakerNote parses a MakerNote that is a plain IFD at offset 0 in the
// parent byte order: Canon, Sony (Alpha/ILCE/SLT/Cybershot, ExifTool
// Sony.pm), Samsung NX/Galaxy (ExifTool Samsung.pm). The payload has no
// magic prefix.
func plainMakerNote(v mnView, order binary.ByteOrder) *IFD {
	if len(v.bytes()) < 6 {
		return nil
	}
	return v.ifdAt(0, order)
}

// parseCanonMakerNote parses a Canon MakerNote on its own bytes.
//
// Canon MakerNote structure (Canon EOS FAQ / ExifTool source):
// The payload is a plain TIFF IFD starting at offset 0 with no magic prefix.
// Byte order is the same as the parent TIFF (CIPA MakerNote §Canon). Its value
// offsets count from the TIFF header (metadata-extractor processMakernote);
// Parse resolves them against the TIFF stream, this blob-only entry point
// resolves only those that fall inside b.
func parseCanonMakerNote(b []byte, order binary.ByteOrder) *IFD {
	return plainMakerNote(blobView(b), order)
}

// isNikonType3 reports whether b starts with the Nikon Type-3 magic prefix.
// Type-3 MakerNotes begin with the 6-byte sequence "Nikon\x00".
func isNikonType3(b []byte) bool {
	return len(b) >= 18 &&
		b[0] == 'N' && b[1] == 'i' && b[2] == 'k' &&
		b[3] == 'o' && b[4] == 'n' && b[5] == 0x00
}

// nikonType3TIFFHeaderMaxSearch is the maximum byte offset within a Nikon
// Type-3 MakerNote blob at which the embedded TIFF header ("II"/"MM" BOM)
// may begin. The documented layout places it at offset 8 (after the 6-byte
// magic prefix and 2-byte version), but some camera models (e.g. Nikon D70
// with version 0x0200) insert 2 padding bytes before the TIFF header,
// placing it at offset 10 instead.
//
// ExifTool Nikon.pm: D70 firmware quirk — TIFF header at blob[10].
const nikonType3TIFFHeaderMaxSearch = 16

// parseNikonType3 parses a Nikon Type-3 MakerNote (modern DSLRs and Coolpix).
//
// Layout (ExifTool Nikon.pm, standard version 0x0210):
//
//	[0..5]   "Nikon\0"    magic
//	[6..7]   version      2 bytes (e.g. 0x02 0x10)
//	[8..9]   byte order   "II" or "MM"
//	[10..11] TIFF magic   0x002A (LE) or 0x2A00 (BE)
//	[12..15] IFD offset   relative to the embedded TIFF base at b[8]
//
// D70 variant (version 0x0200): 2 extra padding bytes at [8..9], so the
// embedded TIFF header starts at offset 10.
//
// All value offsets within the embedded IFD are relative to the embedded
// TIFF header start position (b[tiffStart]). The function scans forward from
// offset 6 to nikonType3TIFFHeaderMaxSearch to locate the actual header.
//
// R-08 (exif-tiff conformance): internal offsets must be rebased relative to
// the embedded TIFF header start, not the outer TIFF base.
func parseNikonType3(b []byte, _ binary.ByteOrder) *IFD {
	return nikonType3(blobView(b))
}

// nikonType3 is parseNikonType3 on a view. The embedded TIFF header is the
// base of every value offset, whatever v.base is.
//
//nolint:cyclop,gocyclo // TIFF BOM scan with two byte-order branches; inherent to binary detection
func nikonType3(v mnView) *IFD {
	b := v.bytes()
	// Scan for the embedded TIFF header starting from offset 6 (right after
	// the 6-byte "Nikon\x00" prefix). This handles both offset 8 (standard)
	// and offset 10 (Nikon D70 version 0x0200 with 2-byte padding).
	//
	// ExifTool Nikon.pm: D70 firmware quirk; findNikonMNTIFFHeader in
	// format/tiff/relocate_nef.go uses the same scanning strategy for writes.
	end := min(nikonType3TIFFHeaderMaxSearch, len(b)-8) // need ≥ 8 bytes for a valid TIFF header

	tiffStart := -1
	var order binary.ByteOrder
	for off := 6; off <= end; off++ {
		if off+8 > len(b) {
			break
		}
		switch {
		case b[off] == 'I' && b[off+1] == 'I':
			if binary.LittleEndian.Uint16(b[off+2:]) == 0x002A {
				tiffStart = off
				order = binary.LittleEndian
			}
		case b[off] == 'M' && b[off+1] == 'M':
			if binary.BigEndian.Uint16(b[off+2:]) == 0x002A {
				tiffStart = off
				order = binary.BigEndian
			}
		}
		if tiffStart >= 0 {
			break
		}
	}

	if tiffStart < 0 {
		return nil // no valid embedded TIFF header found
	}

	tiff := b[tiffStart:]
	if len(tiff) < 8 {
		return nil
	}

	ifdOffset := order.Uint32(tiff[4:])
	ifd, _, err := traverse(tiff, ifdOffset, order, nil, false)
	if err != nil {
		return nil
	}
	if v.loc != nil && uint64(ifdOffset) <= uint64(len(tiff)) {
		*v.loc = mnLoc{off: tiffStart + int(ifdOffset), order: order, base: tiffStart}
	}
	return ifd
}

// parseNikonType1 parses a headerless Nikon MakerNote (Coolpix 775, E990, E5000
// and the D1): a plain IFD at offset 0 with no magic prefix, in either byte
// order.
//
// Task #364: the note was read as big-endian only, so the little-endian notes
// of the Coolpix 775, E900-E5000 and most other headerless cameras ("most are
// little-endian, but D1 is big", ExifTool MakerNotes.pm MakerNoteNikon3,
// ByteOrder 'Unknown') had no entries. The order is taken from the entry
// count, as metadata-extractor does for every IFD (TiffReader.processIfd,
// lines 125-136, Apache-2.0, 6f037943: "The entire test image repository
// doesn't contain a single IFD with more than 255 entries"; an IFD whose count
// has a non-zero high byte and a zero low byte is byte-swapped), and
// ExifTiffHandler.processMakernote (lines 474-501: "The IFD begins with the
// first Makernote byte (no ASCII name). This occurs with CoolPix 775, E990 and
// D1 models") sends the note through that routine. A count of 1 to 255 in
// exactly one of the two orders is the test: it is unambiguous because the
// count of an order is below 256 only when its high byte is 0, so both orders
// qualify only for the count 0, which is refused.
func parseNikonType1(b []byte, _ binary.ByteOrder) *IFD {
	return nikonType1(blobView(b))
}

// nikonType1 is parseNikonType1 on a view.
func nikonType1(v mnView) *IFD {
	b := v.bytes()
	if len(b) < 2 {
		return nil
	}
	var order binary.ByteOrder = binary.BigEndian
	count := binary.BigEndian.Uint16(b)
	if count == 0 || count >= 256 {
		order, count = binary.LittleEndian, binary.LittleEndian.Uint16(b)
		if count == 0 || count >= 256 {
			return nil
		}
	}
	return v.ifdAt(0, order)
}

// parseNikonMakerNote parses a Nikon MakerNote.
//
// Nikon uses two distinct MakerNote formats (ExifTool Nikon.pm):
//
//   - Type 1 (old Nikon D1 / Coolpix): plain IFD at offset 0, either byte order.
//     No magic prefix. Rare in practice.
//
//   - Type 3 (all modern Nikon DSLRs and Coolpix): embedded TIFF header.
//     Layout:
//     [0..5]  "Nikon\0"     magic (6 bytes)
//     [6..7]  version       2 bytes (e.g. 0x02 0x10)
//     [8..9]  byte order    "II" or "MM"
//     [10..11] TIFF magic   0x002A (LE) or 0x2A00 (BE)
//     [12..15] IFD offset   relative to start of the embedded TIFF (offset 8)
//
// Offsets within the embedded TIFF are relative to byte 8 of the MakerNote payload.
func parseNikonMakerNote(b []byte) *IFD {
	return nikonMakerNote(blobView(b))
}

// nikonMakerNote is parseNikonMakerNote on a view.
func nikonMakerNote(v mnView) *IFD {
	if isNikonType3(v.bytes()) {
		return nikonType3(v)
	}
	return nikonType1(v)
}

// parseSonyMakerNote parses a Sony MakerNote on its own bytes.
//
// Sony Alpha (ILCE/ILCA/SLT) and Cybershot MakerNote structure:
// Plain TIFF IFD at offset 0, parent byte order. No magic prefix.
// (Sony DSLR-A100 and later; ExifTool Sony.pm). Value offsets count from the
// TIFF header, as for Canon (see parseCanonMakerNote).
func parseSonyMakerNote(b []byte, order binary.ByteOrder) *IFD {
	return plainMakerNote(blobView(b), order)
}

// parseFujifilmMakerNote parses a Fujifilm MakerNote.
//
// Fujifilm MakerNote layout (metadata-extractor processMakernote: "the 4
// bytes after "FUJIFILM" in the makernote point to the start of the makernote
// IFD, though the offset is relative to the start of the makernote"):
//
//	[0..7]   "FUJIFILM" magic
//	[8..11]  LE uint32 IFD offset relative to b[0] (12 in every corpus file)
//	[12..]   IFD; its first entry, tag 0x0000, holds the version ("0130")
//
// The IFD uses little-endian byte order. All value offsets are relative to
// b[0]. Task #312: the offset used to be read from bytes 12–15 (the IFD entry
// count and the first tag), so every real Fujifilm MakerNote was parsed at a
// wrong position (corpus "FujiFilm.jpg": 4 entries instead of ExifTool's 15).
func parseFujifilmMakerNote(b []byte) *IFD {
	return fujifilmMakerNote(blobView(b))
}

// fujifilmMakerNote is parseFujifilmMakerNote on a view.
func fujifilmMakerNote(v mnView) *IFD {
	const minLen = 14 // 8 (magic) + 4 (IFD offset) + 2 (IFD count)
	b := v.bytes()
	if len(b) < minLen {
		return nil
	}
	if !bytes.HasPrefix(b, []byte("FUJIFILM")) {
		return nil
	}
	ifdOffset := binary.LittleEndian.Uint32(b[8:12])
	if uint64(ifdOffset) > uint64(len(b)) {
		return nil
	}
	return v.ifdAt(int(ifdOffset), binary.LittleEndian)
}

// parseOlympusMakerNote parses an Olympus Type 2 MakerNote.
//
// Olympus Type 2 MakerNote layout (ExifTool Olympus.pm):
//
//	[0..7]   "OLYMPUS\x00" magic
//	[8..9]   "II" (LE) or "MM" (BE) byte order
//	[10..11] version (ignored)
//	[12..]   IFD entries; value offsets relative to b[0]
func parseOlympusMakerNote(b []byte) *IFD {
	return olympusMakerNote(blobView(b))
}

// olympusMakerNote is parseOlympusMakerNote on a view.
func olympusMakerNote(v mnView) *IFD {
	const minLen = 14 // 8 (magic) + 2 (byte order) + 2 (version) + 2 (IFD count)
	b := v.bytes()
	if len(b) < minLen {
		return nil
	}
	if !bytes.HasPrefix(b, []byte("OLYMPUS\x00")) {
		return nil
	}
	var order binary.ByteOrder
	switch {
	case b[8] == 'I' && b[9] == 'I':
		order = binary.LittleEndian
	case b[8] == 'M' && b[9] == 'M':
		order = binary.BigEndian
	default:
		return nil
	}
	return v.ifdAt(12, order)
}

// parsePentaxAOC parses a Pentax AOC-format MakerNote.
//
// AOC format ("AOC\x00" prefix): big-endian IFD at offset 6.
// Used by all modern K-series and 645-series DSLRs (ExifTool Pentax.pm).
func parsePentaxAOC(b []byte) *IFD {
	return blobView(b).ifdAt(6, binary.BigEndian)
}

// parsePentaxPENTAX parses a Pentax PENTAX-format MakerNote.
//
// PENTAX format ("PENTAX \x00" prefix): byte order at [8..9], IFD at offset
// 10, immediately after the byte-order mark. Used by Pentax K-1 Mark II, Q-S1
// and Ricoh WG-series bodies (corpus files "Pentax K-1 Mark II.jpg",
// "Pentax QS1.jpg", "Ricoh WG20.jpg": the entry count at offset 10 equals the
// entry count ExifTool reports; task #304 corrected the former offset 12).
func parsePentaxPENTAX(b []byte) *IFD {
	return pentaxPENTAX(blobView(b))
}

// pentaxPENTAX is parsePentaxPENTAX on a view.
func pentaxPENTAX(v mnView) *IFD {
	b := v.bytes()
	// Audit finding #188: defence-in-depth guard; b[8] and b[9] would index
	// out of range if len(b) < 10. The public caller (parsePentaxMakerNote)
	// already checks len(b) >= 14, but this guard keeps the function safe when
	// called directly (e.g. from tests or future callers).
	if len(b) < 10 {
		return nil
	}
	var order binary.ByteOrder
	switch {
	case b[8] == 'I' && b[9] == 'I':
		order = binary.LittleEndian
	case b[8] == 'M' && b[9] == 'M':
		order = binary.BigEndian
	default:
		return nil
	}
	return v.ifdAt(10, order)
}

// parsePentaxMakerNote parses a Pentax MakerNote.
//
// Two sub-formats are handled (ExifTool Pentax.pm):
//
//   - AOC format ("AOC\x00" prefix): big-endian IFD at offset 6.
//     Used by all modern K-series and 645-series DSLRs.
//
//   - PENTAX format ("PENTAX \x00" prefix): byte order at [8..9], IFD at 10.
//     Used by older Samsung GX-series and early Pentax DSLRs.
func parsePentaxMakerNote(b []byte) *IFD {
	return pentaxMakerNote(blobView(b))
}

// pentaxMakerNote is parsePentaxMakerNote on a view.
func pentaxMakerNote(v mnView) *IFD {
	b := v.bytes()
	switch {
	case len(b) >= 8 && bytes.HasPrefix(b, []byte("AOC\x00")):
		return v.ifdAt(6, binary.BigEndian)
	case len(b) >= 14 && bytes.HasPrefix(b, []byte("PENTAX \x00")):
		return pentaxPENTAX(v)
	}
	return nil
}

// parsePanasonicMakerNote parses a Panasonic MakerNote on its own bytes.
//
// Panasonic MakerNote layout (ExifTool Panasonic.pm):
//
//	[0..11]  "Panasonic\x00\x00\x00"  12-byte magic prefix
//	[12..]   little-endian IFD
//
// Value offsets count from the TIFF header, not from the MakerNote
// (metadata-extractor processMakernote: "Offsets are relative to the start of
// the TIFF header"; corpus Panasonic_DMC-FZ30.jpg: tag 0x0021 at TIFF offset
// 1296, where ExifTool reads it). Parse resolves them against the TIFF stream.
func parsePanasonicMakerNote(b []byte) *IFD {
	return panasonicMakerNote(blobView(b))
}

// panasonicMakerNote is parsePanasonicMakerNote on a view.
func panasonicMakerNote(v mnView) *IFD {
	const magic = "Panasonic\x00\x00\x00"
	if len(v.bytes()) < len(magic)+2 || !bytes.HasPrefix(v.bytes(), []byte(magic)) {
		return nil
	}
	return v.ifdAt(12, binary.LittleEndian)
}

// parseLeicaWithPrefix parses a Leica MakerNote that carries the "LEICA\x00" prefix.
//
// Type 1–5 layout (ExifTool Leica.pm): "LEICA\x00" (6 bytes) + 2-byte sub-type,
// followed by a little-endian IFD at offset 8. Used by S2, M Monochrom, and
// later S-series cameras.
func parseLeicaWithPrefix(b []byte) *IFD {
	return blobView(b).ifdAt(8, binary.LittleEndian)
}

// parseLeicaMakerNote parses a Leica MakerNote.
//
// Two sub-formats are handled (ExifTool Leica.pm):
//
//   - Type 0: plain IFD at offset 0, parent byte order.
//     Used by M8, M9, X1, X2, and most rangefinder cameras.
//
//   - Type 1–5: "LEICA\x00" prefix (6 bytes) + 2-byte sub-type, IFD at offset 8.
//     Used by S2, M Monochrom, and later S-series.
func parseLeicaMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	return leicaMakerNote(blobView(b), parentOrder)
}

// leicaMakerNote is parseLeicaMakerNote on a view.
func leicaMakerNote(v mnView, parentOrder binary.ByteOrder) *IFD {
	b := v.bytes()
	if len(b) < 2 {
		return nil
	}
	// Detect "LEICA\x00" prefix.
	if len(b) >= 8 && b[0] == 'L' && b[1] == 'E' && b[2] == 'I' &&
		b[3] == 'C' && b[4] == 'A' && b[5] == 0x00 {
		return v.ifdAt(8, binary.LittleEndian)
	}
	// Type 0: plain IFD at offset 0, parent byte order.
	return v.ifdAt(0, parentOrder)
}

// parseDJIMakerNote parses a DJI drone MakerNote.
//
// DJI MakerNote is a plain TIFF IFD at offset 0, little-endian.
// Used by Phantom, Mavic, Mini, Air, and Zenmuse series (ExifTool DJI.pm).
func parseDJIMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	return djiMakerNote(blobView(b), parentOrder)
}

// djiMakerNote is parseDJIMakerNote on a view.
func djiMakerNote(v mnView, parentOrder binary.ByteOrder) *IFD {
	if len(v.bytes()) < 6 {
		return nil
	}
	// DJI cameras use little-endian; fall back to parent order.
	if ifd := v.ifdAt(0, binary.LittleEndian); ifd != nil {
		return ifd
	}
	return v.ifdAt(0, parentOrder)
}

// parseSamsungMakerNote parses a Samsung MakerNote on its own bytes.
//
// Samsung NX and Galaxy camera MakerNote is a plain TIFF IFD at offset 0,
// parent byte order (ExifTool Samsung.pm).
func parseSamsungMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	return plainMakerNote(blobView(b), parentOrder)
}

// parseSigmaMakerNote parses a Sigma MakerNote.
//
// Sigma MakerNote layout (ExifTool Sigma.pm):
//
//	[0..7]   "SIGMA\x00\x00\x00" or "FOVEON\x00\x00" magic
//	[8..9]   version (2 bytes, ignored)
//	[10..]   little-endian IFD
func parseSigmaMakerNote(b []byte) *IFD {
	return sigmaMakerNote(blobView(b))
}

// sigmaMakerNote is parseSigmaMakerNote on a view.
func sigmaMakerNote(v mnView) *IFD {
	b := v.bytes()
	if len(b) < 10 {
		return nil
	}
	switch {
	case bytes.HasPrefix(b, []byte("SIGMA\x00\x00\x00")):
	case bytes.HasPrefix(b, []byte("FOVEON\x00\x00")):
	default:
		return nil
	}
	return v.ifdAt(10, binary.LittleEndian)
}

// parseCasioMakerNote parses a Casio MakerNote.
//
// Two sub-formats are handled (ExifTool Casio.pm):
//
//   - QV-series (Format 1): "QVC\x00" (4-byte prefix) + 2-byte version, then
//     a big-endian IFD at offset 6. Used by older QV-series cameras (e.g.
//     QV-3000EX). Audit finding #144.
//
//   - Exilim / modern Casio (Format 2): plain IFD at offset 0, parent byte order.
//     Used by Casio Exilim and other non-QV-series cameras.
func parseCasioMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	return casioMakerNote(blobView(b), parentOrder)
}

// casioMakerNote is parseCasioMakerNote on a view.
func casioMakerNote(v mnView, parentOrder binary.ByteOrder) *IFD {
	b := v.bytes()
	if len(b) < 6 {
		return nil
	}
	// Casio QV-series Format 1: "QVC\x00" prefix + 2 version bytes + big-endian
	// IFD at offset 6. ExifTool Casio.pm: QVC magic, big-endian, IFD at 6.
	// Audit finding #144.
	if bytes.HasPrefix(b, []byte("QVC\x00")) {
		if len(b) < 8 {
			return nil
		}
		return v.ifdAt(6, binary.BigEndian)
	}
	// Format 2: plain IFD at offset 0, parent byte order.
	return v.ifdAt(0, parentOrder)
}
