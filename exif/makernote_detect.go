package exif

// Adapted from metadata-extractor Source/com/drew/metadata/exif/ExifTiffHandler.java:ExifTiffHandler.processMakernote, Copyright 2002-2019 Drew Noakes and contributors, SPDX-License-Identifier: Apache-2.0

import (
	"bytes"
	"encoding/binary"
)

// MakerNote format detection.
//
// The reference implementation (metadata-extractor, ExifTiffHandler.
// processMakernote) selects the MakerNote layout from the leading bytes of the
// MakerNote itself — an ASCII signature such as "OLYMP\0", "Apple iOS\0" or
// "SONY CAM" — and consults the Make string only for layouts that have no
// signature (plain IFDs). GoMetadata used to dispatch on an exact Make string
// only, which left every vendor missing from that table (Apple, Kodak,
// Minolta, Epson/Agfa, Sanyo, Kyocera, Reconyx, upper-case NIKON, OM Digital)
// unparsed and made a mis-spelled Make silently disable a signed MakerNote.
//
// detectMakerNote keeps the order of the reference: signature checks first,
// then Make families (case-insensitive prefix tests, except the
// case-sensitive "SONY" of the reference). Every Make the former exact-Make
// table held is covered by a family, so no vendor parsed before is lost.
// Differences from the reference: every signature is tested before any Make
// (the reference interleaves some Make tests, e.g. MINOLTA and NIKON, between
// signatures), so a signed MakerNote is recognised whatever the Make says;
// layouts GoMetadata already parsed keep their existing parser (Nikon Type 3
// header scan, Fujifilm offset field, Olympus byte-order marker, Panasonic,
// Leica, Sigma, Casio); the "PENTAX \0" + byte-order layout, which the
// reference does not list, is kept with its IFD at offset 10; the binary,
// non-IFD layouts (Kodak "KDK", Reconyx) are recognised but yield no IFD,
// because their tag tables are outside this package's scope; and the offset
// base is applied: a layout whose value offsets count from the TIFF header is
// parsed against the enclosing TIFF stream (see mnBase and mnView).

// mnBase tells which origin the value offsets of a MakerNote IFD use.
type mnBase uint8

const (
	// mnBaseBlob: value offsets count from the first byte of the MakerNote
	// (the layout carries its own header: Olympus "OLYMPUS\0II", Apple, Ricoh,
	// Sanyo, Fujifilm, Nikon Type 3 relative to its embedded TIFF header).
	mnBaseBlob mnBase = iota
	// mnBaseTIFF: value offsets count from the start of the enclosing TIFF
	// stream (Canon, Sony, Casio, Kyocera, Panasonic, "OLYMP\0", plain-IFD
	// layouts). When the caller supplies that stream (makerNoteDispatch from
	// Parse) the IFD is read from it, as metadata-extractor's
	// processMakernote does (processIfd with tiffHeaderOffset as the base), so
	// a value stored outside the MakerNote is resolved (task #312); otherwise
	// only values that lie inside the MakerNote are.
	mnBaseTIFF
	// mnBaseNone: the layout is not an IFD (Kodak, Reconyx).
	mnBaseNone
)

// makerNoteParserFunc parses the MakerNote of v into an IFD, or returns nil.
type makerNoteParserFunc = func(v mnView, parent binary.ByteOrder) *IFD

// makerNoteFormat is the result of detectMakerNote. It is a small value type:
// detection allocates nothing.
type makerNoteFormat struct {
	// name is the vendor/layout label ("" when nothing was detected).
	name string
	// ifdOffset is the offset of the first IFD inside the MakerNote, or -1
	// when the IFD position is read from the data (Fujifilm, Nikon Type 3) or
	// the layout is not an IFD.
	ifdOffset int
	base      mnBase
	// parse parses the MakerNote; nil when nothing was detected.
	parse makerNoteParserFunc
}

// mnLayout identifies a detected MakerNote layout: an index into mnLayouts.
// Detection returns this one-byte value instead of the multi-word
// makerNoteFormat, which keeps the per-Parse dispatch cheap (struct returns
// through several calls measurably slowed it on arm64).
type mnLayout uint8

// mnNone is the zero layout: nothing detected.
const mnNone mnLayout = 0

// MakerNote layouts, named after their vendor and variant.
const (
	mnOlympusOld mnLayout = iota + 1
	mnOlympus
	mnOMSystem
	mnNikon1
	mnNikon3
	mnSonyCam
	mnSonyEricsson
	mnSigma
	mnKodak
	mnCasio2
	mnFujifilm
	mnKyocera
	mnLeica
	mnLeicaTIFF
	mnPanasonic
	mnPentaxAOC
	mnPentax
	mnSanyo
	mnRicoh
	mnApple
	mnReconyx
	mnMinolta
	mnNikon2
	mnOlympusMake
	mnCanon
	mnCasio
	mnSony
	mnPentaxPlain
	mnRicohMake
	mnLeicaMake
	mnSamsung
	mnDJI
	mnJVC
)

// mnLayouts describes every layout. Index mnNone is the zero format (no
// parser). Read-only after package initialisation.
//
//nolint:gochecknoglobals // read-only layout table, indexed by mnLayout
var mnLayouts = [...]makerNoteFormat{
	mnNone:         {},
	mnOlympusOld:   {"olympus-old", 8, mnBaseTIFF, parseOlympusOldMN},
	mnOlympus:      {"olympus", 12, mnBaseBlob, parseOlympusMN},
	mnOMSystem:     {"om-system", 14, mnBaseBlob, parseOMSystemMN},
	mnNikon1:       {"nikon-1", 8, mnBaseTIFF, parseNikonType1HeaderMN},
	mnNikon3:       {"nikon-3", -1, mnBaseBlob, parseNikonMN},
	mnSonyCam:      {"sony-cam", 12, mnBaseTIFF, parseSonyCamMN},
	mnSonyEricsson: {"sony-ericsson", 20, mnBaseTIFF, parseSemcMN},
	mnSigma:        {"sigma", 10, mnBaseTIFF, parseSigmaMN},
	mnKodak:        {"kodak", -1, mnBaseNone, parseBinaryMN},
	mnCasio2:       {"casio-2", 6, mnBaseTIFF, parseCasioMN},
	mnFujifilm:     {"fujifilm", -1, mnBaseBlob, parseFujifilmMN},
	mnKyocera:      {"kyocera", 22, mnBaseTIFF, parseKyoceraMN},
	mnLeica:        {"leica", 8, mnBaseBlob, parseLeicaMN},
	mnLeicaTIFF:    {"leica", 8, mnBaseTIFF, parseLeicaMN},
	mnPanasonic:    {"panasonic", 12, mnBaseTIFF, parsePanasonicMN},
	mnPentaxAOC:    {"pentax-aoc", 6, mnBaseBlob, parsePentaxMN},
	mnPentax:       {"pentax", 10, mnBaseBlob, parsePentaxMN},
	mnSanyo:        {"sanyo", 8, mnBaseBlob, parseSanyoMN},
	mnRicoh:        {"ricoh", 8, mnBaseBlob, parseRicohMN},
	mnApple:        {"apple", 14, mnBaseBlob, parseAppleMN},
	mnReconyx:      {"reconyx", -1, mnBaseNone, parseBinaryMN},
	mnMinolta:      {"minolta", 0, mnBaseTIFF, parsePlainIFDMN},
	mnNikon2:       {"nikon-2", 0, mnBaseTIFF, parseNikonMN},
	mnOlympusMake:  {"olympus", -1, mnBaseBlob, parseOlympusMN},
	mnCanon:        {"canon", 0, mnBaseTIFF, parseCanonMN},
	mnCasio:        {"casio", 0, mnBaseTIFF, parseCasioMN},
	mnSony:         {"sony", 0, mnBaseTIFF, parseSonyMN},
	mnPentaxPlain:  {"pentax", 0, mnBaseBlob, parsePentaxPlainMN},
	mnRicohMake:    {"ricoh", -1, mnBaseBlob, parsePentaxMN},
	mnLeicaMake:    {"leica", 0, mnBaseTIFF, parseLeicaMN},
	mnSamsung:      {"samsung", 0, mnBaseTIFF, parseSamsungMN},
	mnDJI:          {"dji", 0, mnBaseTIFF, parseDJIMN},
	mnJVC:          {"jvc", 4, mnBaseTIFF, parseJVCMN},
}

// asciiUpper folds an ASCII letter to upper case.
func asciiUpper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// eqFold reports whether s equals the upper-case ASCII literal lit, ignoring
// ASCII case. It accepts string and []byte so that the hot path (a Make taken
// from IFDEntry.Value) converts nothing.
func eqFold[T ~string | ~[]byte](s T, lit string) bool {
	if len(s) != len(lit) {
		return false
	}
	for i := range len(lit) {
		if asciiUpper(s[i]) != lit[i] {
			return false
		}
	}
	return true
}

// hasPrefixFold reports whether s starts with the upper-case ASCII literal
// prefix, ignoring ASCII case.
func hasPrefixFold[T ~string | ~[]byte](s T, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range len(prefix) {
		if asciiUpper(s[i]) != prefix[i] {
			return false
		}
	}
	return true
}

// parseAtOffset parses a MakerNote IFD that starts at off within the
// MakerNote, using order. It is the common tail of every layout that needs no
// vendor-specific validation.
func parseAtOffset(v mnView, off int, order binary.ByteOrder) *IFD {
	return v.ifdAt(off, order)
}

// parsePlausibleIFD is parseAtOffset at offset 0 for layouts that have no signature to
// vouch for them (Make-only detection of Minolta and signature-less Pentax):
// the IFD is accepted only when its entry count is non-zero and every entry
// fits inside the MakerNote, so an arbitrary blob is not reinterpreted as an
// IFD.
func parsePlausibleIFD(v mnView, order binary.ByteOrder) *IFD {
	b := v.bytes()
	if len(b) < 2 {
		return nil
	}
	n := int(order.Uint16(b))
	if n == 0 || 2+n*12 > len(b) {
		return nil
	}
	return parseAtOffset(v, 0, order)
}

// Layouts whose IFD sits at a fixed offset behind a signature. The offsets
// follow metadata-extractor's processMakernote; every function is a plain
// function value (no captures), so none allocates.
func parseOlympusOldMN(v mnView, parent binary.ByteOrder) *IFD { return parseAtOffset(v, 8, parent) }
func parseNikonType1HeaderMN(v mnView, parent binary.ByteOrder) *IFD {
	return parseAtOffset(v, 8, parent)
}
func parseSonyCamMN(v mnView, parent binary.ByteOrder) *IFD { return parseAtOffset(v, 12, parent) }
func parseKyoceraMN(v mnView, parent binary.ByteOrder) *IFD { return parseAtOffset(v, 22, parent) }
func parseJVCMN(v mnView, parent binary.ByteOrder) *IFD     { return parseAtOffset(v, 4, parent) }
func parseSanyoMN(v mnView, parent binary.ByteOrder) *IFD   { return parseAtOffset(v, 8, parent) }
func parsePlainIFDMN(v mnView, parent binary.ByteOrder) *IFD {
	return parsePlausibleIFD(v, parent)
}

// parseSemcMN: Sony Ericsson "SEMC MS\0\0\0\0\0" header, big-endian IFD at 20.
func parseSemcMN(v mnView, _ binary.ByteOrder) *IFD { return parseAtOffset(v, 20, binary.BigEndian) }

// parseRicohMN: "Ricoh" header, big-endian IFD at 8.
func parseRicohMN(v mnView, _ binary.ByteOrder) *IFD { return parseAtOffset(v, 8, binary.BigEndian) }

// parseAppleMN: "Apple iOS\0" header, big-endian IFD at 14.
func parseAppleMN(v mnView, _ binary.ByteOrder) *IFD { return parseAtOffset(v, 14, binary.BigEndian) }

// parseOMSystemMN: "OM SYSTEM\0\0\0" + "II"/"MM" byte-order mark, IFD at 14.
func parseOMSystemMN(v mnView, _ binary.ByteOrder) *IFD {
	b := v.bytes()
	if len(b) < 14 {
		return nil
	}
	switch {
	case b[12] == 'I' && b[13] == 'I':
		return parseAtOffset(v, 14, binary.LittleEndian)
	case b[12] == 'M' && b[13] == 'M':
		return parseAtOffset(v, 14, binary.BigEndian)
	}
	return nil
}

// parseBinaryMN is the parser of recognised layouts that are not IFDs (Kodak
// "KDK" blocks, Reconyx fixed records): there is no IFD to return.
func parseBinaryMN(_ mnView, _ binary.ByteOrder) *IFD { return nil }

func parseNikonMN(v mnView, _ binary.ByteOrder) *IFD       { return nikonMakerNote(v) }
func parseOlympusMN(v mnView, _ binary.ByteOrder) *IFD     { return olympusMakerNote(v) }
func parseFujifilmMN(v mnView, _ binary.ByteOrder) *IFD    { return fujifilmMakerNote(v) }
func parsePanasonicMN(v mnView, _ binary.ByteOrder) *IFD   { return panasonicMakerNote(v) }
func parsePentaxMN(v mnView, _ binary.ByteOrder) *IFD      { return pentaxMakerNote(v) }
func parseSigmaMN(v mnView, _ binary.ByteOrder) *IFD       { return sigmaMakerNote(v) }
func parsePentaxPlainMN(v mnView, p binary.ByteOrder) *IFD { return parsePentaxOrPlain(v, p) }

// parsePentaxOrPlain handles a Pentax/Asahi MakerNote: the AOC and PENTAX
// signatures when present, otherwise a plain IFD at offset 0 (PENTAX Optio 330
// and 430 write no signature; metadata-extractor processMakernote).
func parsePentaxOrPlain(v mnView, parent binary.ByteOrder) *IFD {
	if ifd := pentaxMakerNote(v); ifd != nil {
		return ifd
	}
	if bytes.HasPrefix(v.bytes(), []byte("AOC\x00")) || bytes.HasPrefix(v.bytes(), []byte("PENTAX \x00")) {
		return nil // signed but unparsable: do not reinterpret as a plain IFD
	}
	return parsePlausibleIFD(v, parent)
}

// detectBySignature identifies a MakerNote from its leading bytes. The order
// of the cases is the order of metadata-extractor's processMakernote.
//
//nolint:cyclop,gocyclo // one flat signature table; each case is a single prefix test
func detectBySignature(mn []byte) mnLayout {
	// Fast reject: every signature below starts with one of these bytes, and
	// the most common MakerNotes (Canon, Sony, Samsung, DJI, Casio: plain
	// IFDs) start with an entry count instead, so they skip the whole table.
	if len(mn) < 4 {
		return mnNone
	}
	switch mn[0] {
	case 'A', 'E', 'F', 'J', 'K', 'L', 'N', 'O', 'P', 'Q', 'R', 'S', 'r':
	default:
		return mnNone
	}
	has := func(prefix string) bool { return len(mn) >= len(prefix) && string(mn[:len(prefix)]) == prefix }
	switch {
	case has("OLYMP\x00") || has("EPSON") || has("AGFA"):
		// Epson and Agfa use the Olympus layout. Value offsets count from the
		// TIFF header.
		return mnOlympusOld
	case has("OLYMPUS\x00"):
		// "OLYMPUS\0II" / "OLYMPUS\0MM": offsets relative to the MakerNote.
		return mnOlympus
	case has("OM SYSTEM\x00\x00\x00"):
		return mnOMSystem
	case has("Nikon\x00"):
		// Type 1 (version byte 1): IFD at 8. Type 3 (version byte 2): embedded
		// TIFF header, located by parseNikonType3.
		if len(mn) > 6 && mn[6] == 1 {
			return mnNikon1
		}
		return mnNikon3
	case has("SONY CAM") || has("SONY DSC") || has("SONY MOBILE"):
		// "SONY MOBILE" (Xperia): ExifTool MakerNotes.pm, MakerNoteSony,
		// condition /^SONY (DSC|CAM|MOBILE)/, Start => $valuePtr + 12.
		return mnSonyCam
	case has("SEMC MS\x00\x00\x00\x00\x00"):
		return mnSonyEricsson
	case has("SIGMA\x00\x00\x00") || has("FOVEON\x00\x00"):
		return mnSigma
	case has("KDK"):
		return mnKodak
	case has("QVC\x00\x00\x00"):
		return mnCasio2
	case has("FUJIFILM"):
		return mnFujifilm
	case has("KYOCERA"):
		return mnKyocera
	case has("LEICA\x00"):
		// "LEICA\0" + version: versions 1, 4, 5, 6 and 7 (X1, X VARIO, X2,
		// T, X) count offsets from the MakerNote; the other variants count
		// them from the TIFF header (processMakernote, Leica branch).
		if len(mn) > 7 && mn[7] == 0 && (mn[6] == 1 || (mn[6] >= 4 && mn[6] <= 7)) {
			return mnLeica
		}
		return mnLeicaTIFF
	case has("Panasonic\x00\x00\x00"):
		return mnPanasonic
	case has("AOC\x00"):
		return mnPentaxAOC
	case has("PENTAX \x00"):
		return mnPentax
	case has("SANYO\x00"):
		// Every version: ExifTool MakerNotes.pm, MakerNoteSanyo, Validate
		// /^SANYO/, Start => $valuePtr + 8 (metadata-extractor tests version
		// 1.0 only; the corpus holds 2.1).
		return mnSanyo
	case has("JVC "):
		// ExifTool MakerNotes.pm, MakerNoteJVC: Start => $valuePtr + 4.
		return mnJVC
	case len(mn) >= 5 && eqFold(mn[:5], "RICOH"):
		return mnRicoh
	case has("Apple iOS\x00"):
		return mnApple
	case len(mn) >= 9 && (eqFold(mn[:9], "RECONYXUF") || eqFold(mn[:9], "RECONYXH2")):
		return mnReconyx
	}
	return mnNone
}

func parseCasioMN(v mnView, parent binary.ByteOrder) *IFD   { return casioMakerNote(v, parent) }
func parseLeicaMN(v mnView, parent binary.ByteOrder) *IFD   { return leicaMakerNote(v, parent) }
func parseCanonMN(v mnView, parent binary.ByteOrder) *IFD   { return plainMakerNote(v, parent) }
func parseSonyMN(v mnView, parent binary.ByteOrder) *IFD    { return plainMakerNote(v, parent) }
func parseSamsungMN(v mnView, parent binary.ByteOrder) *IFD { return plainMakerNote(v, parent) }
func parseDJIMN(v mnView, parent binary.ByteOrder) *IFD     { return djiMakerNote(v, parent) }

// detectByMake identifies a signature-less MakerNote from the Make string:
// case-insensitive family prefixes, as in metadata-extractor, so "NIKON
// CORPORATION", "Nikon" and "NIKON" all select Nikon. mk has been trimmed
// of NULs and whitespace by the caller. The first letter selects the family
// group, so each Make costs one or two prefix tests.
//
//nolint:cyclop,gocyclo // one flat family table; each case is a single prefix test
func detectByMake[T ~string | ~[]byte](mk T) mnLayout {
	if len(mk) == 0 {
		return mnNone
	}
	switch asciiUpper(mk[0]) {
	case 'A':
		if hasPrefixFold(mk, "ASAHI") {
			return mnPentaxPlain
		}
	case 'C':
		switch {
		case eqFold(mk, "CANON"):
			return mnCanon
		case hasPrefixFold(mk, "CASIO"):
			return mnCasio
		}
	case 'D':
		if eqFold(mk, "DJI") {
			return mnDJI
		}
	case 'F':
		if hasPrefixFold(mk, "FUJIFILM") {
			return mnFujifilm
		}
	case 'L':
		if hasPrefixFold(mk, "LEICA") {
			// No "LEICA\0" signature (matched earlier): plain IFD at offset 0.
			return mnLeicaMake
		}
	case 'M':
		if hasPrefixFold(mk, "MINOLTA") {
			// Minolta cameras write a valid Olympus-style IFD that starts at
			// the first MakerNote byte.
			return mnMinolta
		}
	case 'N':
		if hasPrefixFold(mk, "NIKON") {
			// No "Nikon\0" header: the IFD starts at the first MakerNote byte
			// (Nikon Type 2: Coolpix 775, E990, D1) and offsets count from the
			// TIFF header.
			return mnNikon2
		}
	case 'O':
		if hasPrefixFold(mk, "OLYMPUS") || hasPrefixFold(mk, "OM DIGITAL") {
			return mnOlympusMake
		}
	case 'P':
		switch {
		case hasPrefixFold(mk, "PENTAX"):
			// No AOC/PENTAX signature (matched earlier): plain IFD at offset
			// 0, offsets relative to the MakerNote (PENTAX Optio 330/430).
			return mnPentaxPlain
		case hasPrefixFold(mk, "PANASONIC"):
			return mnPanasonic
		}
	case 'R':
		if hasPrefixFold(mk, "RICOH") {
			return mnRicohMake
		}
	case 'S':
		switch {
		case len(mk) >= 4 && string(mk[:4]) == "SONY":
			// Case-sensitive, as in the reference: Sony Mobile phones write
			// Make "Sony" and a "SONY MOBILE" header that is not a plain IFD,
			// so a case-insensitive match would parse the header as entries.
			return mnSony
		case eqFold(mk, "SAMSUNG"):
			return mnSamsung
		case hasPrefixFold(mk, "SIGMA"):
			return mnSigma
		}
	}
	return mnNone
}

// detectLayout selects the layout of the MakerNote blob mn written by a
// camera whose Make is mk (already trimmed of NULs and whitespace; may be
// empty): signature first, then Make family. It does not allocate.
func detectLayout[T ~string | ~[]byte](mn []byte, mk T) mnLayout {
	if l := detectBySignature(mn); l != mnNone {
		return l
	}
	if l := detectByMake(mk); l != mnNone {
		return l
	}
	// Reconyx HyperFire records carry no text signature: Make "RECONYX" and
	// the 0xF101 little-endian version word identify them.
	if hasPrefixFold(mk, "RECONYX") && len(mn) >= 2 && binary.LittleEndian.Uint16(mn) == 0xF101 {
		return mnReconyx
	}
	return mnNone
}

// detectMakerNote returns the format description of the layout detectLayout
// selects; its parse field is nil when nothing was detected.
func detectMakerNote[T ~string | ~[]byte](mn []byte, mk T) makerNoteFormat {
	return mnLayouts[detectLayout(mn, mk)]
}
