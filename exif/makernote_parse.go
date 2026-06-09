package exif

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// makerNoteParsers maps EXIF Make strings to their MakerNote IFD parser.
// Each value is a function that takes the raw MakerNote bytes and the parent
// byte order and returns the parsed IFD, or nil on failure.
//
//nolint:gochecknoglobals // dispatch table: package-level read-only map populated at init and never mutated
var makerNoteParsers = map[string]func([]byte, binary.ByteOrder) *IFD{
	"Canon":                   parseCanonMakerNote,
	"NIKON CORPORATION":       func(b []byte, _ binary.ByteOrder) *IFD { return parseNikonMakerNote(b) },
	"Nikon":                   func(b []byte, _ binary.ByteOrder) *IFD { return parseNikonMakerNote(b) },
	"SONY":                    parseSonyMakerNote,
	"FUJIFILM":                func(b []byte, _ binary.ByteOrder) *IFD { return parseFujifilmMakerNote(b) },
	"OLYMPUS IMAGING CORP.":   func(b []byte, _ binary.ByteOrder) *IFD { return parseOlympusMakerNote(b) },
	"OLYMPUS CORPORATION":     func(b []byte, _ binary.ByteOrder) *IFD { return parseOlympusMakerNote(b) },
	"Olympus":                 func(b []byte, _ binary.ByteOrder) *IFD { return parseOlympusMakerNote(b) },
	"PENTAX Corporation":      func(b []byte, _ binary.ByteOrder) *IFD { return parsePentaxMakerNote(b) },
	"Ricoh":                   func(b []byte, _ binary.ByteOrder) *IFD { return parsePentaxMakerNote(b) },
	"RICOH":                   func(b []byte, _ binary.ByteOrder) *IFD { return parsePentaxMakerNote(b) },
	"Panasonic":               func(b []byte, _ binary.ByteOrder) *IFD { return parsePanasonicMakerNote(b) },
	"LEICA CAMERA AG":         parseLeicaMakerNote,
	"Leica Camera AG":         parseLeicaMakerNote,
	"LEICA":                   parseLeicaMakerNote,
	"Leica":                   parseLeicaMakerNote,
	"DJI":                     parseDJIMakerNote,
	"SAMSUNG":                 parseSamsungMakerNote,
	"SIGMA":                   func(b []byte, _ binary.ByteOrder) *IFD { return parseSigmaMakerNote(b) },
	"CASIO COMPUTER CO.,LTD.": parseCasioMakerNote,
	"Casio Computer Co.,Ltd.": parseCasioMakerNote,
	"CASIO":                   parseCasioMakerNote,
}

// parseMakerNoteIFD attempts to parse the raw MakerNote bytes into an IFD.
// Returns nil when the format is unrecognised or parsing fails.
//
// cameraMake is trimmed of leading/trailing whitespace before the dispatch
// table lookup. Real-world camera files (e.g. Canon EOS bodies) sometimes
// store the Make field with a trailing space ("Canon "); trimming aligns
// our behaviour with ExifTool's MakerNote dispatch logic.
//
// Supported formats:
//   - Canon: plain IFD at offset 0, parent byte order (CIPA MakerNote §Canon)
//   - Nikon Type 3: embedded TIFF header at offset 10 within "Nikon\0" prefix
//   - Nikon Type 1: plain IFD at offset 0, big-endian (legacy Nikon cameras)
//   - Sony: plain IFD at offset 0, parent byte order
//   - Fujifilm: "FUJIFILM" prefix, LE IFD at offset stored at [12..15]
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
func parseMakerNoteIFD(b []byte, cameraMake string, parentOrder binary.ByteOrder) *IFD {
	// Trim surrounding whitespace: real-world files (Canon, Nikon) sometimes store
	// Make values with trailing spaces. ExifTool normalises the value before its
	// own MakerNote dispatch; we do the same to avoid silent dispatch failures.
	trimmed := strings.TrimSpace(cameraMake)
	if fn, ok := makerNoteParsers[trimmed]; ok {
		return fn(b, parentOrder)
	}
	return nil
}

// parseCanonMakerNote parses a Canon MakerNote.
//
// Canon MakerNote structure (Canon EOS FAQ / ExifTool source):
// The payload is a plain TIFF IFD starting at offset 0 with no magic prefix.
// Byte order is the same as the parent TIFF (CIPA MakerNote §Canon).
func parseCanonMakerNote(b []byte, order binary.ByteOrder) *IFD {
	if len(b) < 6 {
		return nil
	}
	ifd, _, err := traverse(b, 0, order)
	if err != nil {
		return nil
	}
	return ifd
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
//
//nolint:cyclop,gocyclo // TIFF BOM scan with two byte-order branches; inherent to binary detection
func parseNikonType3(b []byte, _ binary.ByteOrder) *IFD {
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
	ifd, _, err := traverse(tiff, ifdOffset, order)
	if err != nil {
		return nil
	}
	return ifd
}

// parseNikonType1 parses a Nikon Type-1 MakerNote (legacy D1 / early Coolpix).
//
// Type-1 MakerNotes are plain IFDs at offset 0, big-endian, with no magic
// prefix. A heuristic check (entry count > 0 and < 256) guards against
// false positives on non-Nikon data (ExifTool Nikon.pm).
func parseNikonType1(b []byte, _ binary.ByteOrder) *IFD {
	if len(b) < 2 {
		return nil
	}
	count := binary.BigEndian.Uint16(b)
	if count == 0 || count >= 256 {
		return nil
	}
	ifd, _, err := traverse(b, 0, binary.BigEndian)
	if err != nil {
		return nil
	}
	return ifd
}

// parseNikonMakerNote parses a Nikon MakerNote.
//
// Nikon uses two distinct MakerNote formats (ExifTool Nikon.pm):
//
//   - Type 1 (old Nikon D1 / Coolpix): plain IFD at offset 0, big-endian.
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
	if isNikonType3(b) {
		return parseNikonType3(b, binary.BigEndian)
	}
	return parseNikonType1(b, binary.BigEndian)
}

// parseSonyMakerNote parses a Sony MakerNote.
//
// Sony Alpha (ILCE/ILCA/SLT) and Cybershot MakerNote structure:
// Plain TIFF IFD at offset 0, parent byte order. No magic prefix.
// (Sony DSLR-A100 and later; ExifTool Sony.pm).
func parseSonyMakerNote(b []byte, order binary.ByteOrder) *IFD {
	if len(b) < 6 {
		return nil
	}
	ifd, _, err := traverse(b, 0, order)
	if err != nil {
		return nil
	}
	return ifd
}

// parseFujifilmMakerNote parses a Fujifilm MakerNote.
//
// Fujifilm MakerNote layout (ExifTool Fujifilm.pm §3.1):
//
//	[0..7]   "FUJIFILM" magic
//	[8..11]  version (e.g. "0100", ignored)
//	[12..15] LE uint32 IFD offset relative to b[0]
//
// The IFD uses little-endian byte order. All value offsets are relative to b[0].
func parseFujifilmMakerNote(b []byte) *IFD {
	const minLen = 16 // 8 (magic) + 4 (version) + 4 (offset)
	if len(b) < minLen {
		return nil
	}
	if !bytes.HasPrefix(b, []byte("FUJIFILM")) {
		return nil
	}
	ifdOffset := binary.LittleEndian.Uint32(b[12:16])
	ifd, _, err := traverse(b, ifdOffset, binary.LittleEndian)
	if err != nil {
		return nil
	}
	return ifd
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
	const minLen = 14 // 8 (magic) + 2 (byte order) + 2 (version) + 2 (IFD count)
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
	ifd, _, err := traverse(b, 12, order)
	if err != nil {
		return nil
	}
	return ifd
}

// parsePentaxAOC parses a Pentax AOC-format MakerNote.
//
// AOC format ("AOC\x00" prefix): big-endian IFD at offset 6.
// Used by all modern K-series and 645-series DSLRs (ExifTool Pentax.pm).
func parsePentaxAOC(b []byte) *IFD {
	ifd, _, err := traverse(b, 6, binary.BigEndian)
	if err != nil {
		return nil
	}
	return ifd
}

// parsePentaxPENTAX parses a Pentax PENTAX-format MakerNote.
//
// PENTAX format ("PENTAX \x00" prefix): byte order at [8..9], IFD at offset 12.
// Used by older Samsung GX-series and early Pentax DSLRs (ExifTool Pentax.pm).
func parsePentaxPENTAX(b []byte) *IFD {
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
	ifd, _, err := traverse(b, 12, order)
	if err != nil {
		return nil
	}
	return ifd
}

// parsePentaxMakerNote parses a Pentax MakerNote.
//
// Two sub-formats are handled (ExifTool Pentax.pm):
//
//   - AOC format ("AOC\x00" prefix): big-endian IFD at offset 6.
//     Used by all modern K-series and 645-series DSLRs.
//
//   - PENTAX format ("PENTAX \x00" prefix): byte order at [8..9], IFD at 12.
//     Used by older Samsung GX-series and early Pentax DSLRs.
func parsePentaxMakerNote(b []byte) *IFD {
	switch {
	case len(b) >= 8 && bytes.HasPrefix(b, []byte("AOC\x00")):
		return parsePentaxAOC(b)
	case len(b) >= 14 && bytes.HasPrefix(b, []byte("PENTAX \x00")):
		return parsePentaxPENTAX(b)
	}
	return nil
}

// parsePanasonicMakerNote parses a Panasonic MakerNote.
//
// Panasonic MakerNote layout (ExifTool Panasonic.pm):
//
//	[0..11]  "Panasonic\x00\x00\x00"  12-byte magic prefix
//	[12..]   little-endian IFD; value offsets relative to b[0]
func parsePanasonicMakerNote(b []byte) *IFD {
	const magic = "Panasonic\x00\x00\x00"
	if len(b) < len(magic)+2 {
		return nil
	}
	if !bytes.HasPrefix(b, []byte(magic)) {
		return nil
	}
	ifd, _, err := traverse(b, 12, binary.LittleEndian)
	if err != nil {
		return nil
	}
	return ifd
}

// parseLeicaWithPrefix parses a Leica MakerNote that carries the "LEICA\x00" prefix.
//
// Type 1–5 layout (ExifTool Leica.pm): "LEICA\x00" (6 bytes) + 2-byte sub-type,
// followed by a little-endian IFD at offset 8. Used by S2, M Monochrom, and
// later S-series cameras.
func parseLeicaWithPrefix(b []byte) *IFD {
	ifd, _, err := traverse(b, 8, binary.LittleEndian)
	if err != nil {
		return nil
	}
	return ifd
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
	if len(b) < 2 {
		return nil
	}
	// Detect "LEICA\x00" prefix.
	if len(b) >= 8 && b[0] == 'L' && b[1] == 'E' && b[2] == 'I' &&
		b[3] == 'C' && b[4] == 'A' && b[5] == 0x00 {
		return parseLeicaWithPrefix(b)
	}
	// Type 0: plain IFD at offset 0, parent byte order.
	ifd, _, err := traverse(b, 0, parentOrder)
	if err != nil {
		return nil
	}
	return ifd
}

// parseDJIMakerNote parses a DJI drone MakerNote.
//
// DJI MakerNote is a plain TIFF IFD at offset 0, little-endian.
// Used by Phantom, Mavic, Mini, Air, and Zenmuse series (ExifTool DJI.pm).
func parseDJIMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	if len(b) < 6 {
		return nil
	}
	// DJI cameras use little-endian; fall back to parent order.
	ifd, _, err := traverse(b, 0, binary.LittleEndian)
	if err != nil {
		ifd, _, err = traverse(b, 0, parentOrder)
		if err != nil {
			return nil
		}
	}
	return ifd
}

// parseSamsungMakerNote parses a Samsung MakerNote.
//
// Samsung NX and Galaxy camera MakerNote is a plain TIFF IFD at offset 0,
// parent byte order (ExifTool Samsung.pm).
func parseSamsungMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	if len(b) < 6 {
		return nil
	}
	ifd, _, err := traverse(b, 0, parentOrder)
	if err != nil {
		return nil
	}
	return ifd
}

// parseSigmaMakerNote parses a Sigma MakerNote.
//
// Sigma MakerNote layout (ExifTool Sigma.pm):
//
//	[0..7]   "SIGMA\x00\x00\x00" or "FOVEON\x00\x00" magic
//	[8..9]   version (2 bytes, ignored)
//	[10..]   little-endian IFD; value offsets relative to b[0]
func parseSigmaMakerNote(b []byte) *IFD {
	if len(b) < 10 {
		return nil
	}
	switch {
	case bytes.HasPrefix(b, []byte("SIGMA\x00\x00\x00")):
	case bytes.HasPrefix(b, []byte("FOVEON\x00\x00")):
	default:
		return nil
	}
	ifd, _, err := traverse(b, 10, binary.LittleEndian)
	if err != nil {
		return nil
	}
	return ifd
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
		ifd, _, err := traverse(b, 6, binary.BigEndian)
		if err != nil {
			return nil
		}
		return ifd
	}
	// Format 2: plain IFD at offset 0, parent byte order.
	ifd, _, err := traverse(b, 0, parentOrder)
	if err != nil {
		return nil
	}
	return ifd
}
