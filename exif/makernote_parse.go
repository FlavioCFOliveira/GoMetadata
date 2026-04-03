package exif

import "encoding/binary"

// parseMakerNoteIFD attempts to parse the raw MakerNote bytes into an IFD.
// Returns nil when the format is unrecognised or parsing fails.
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
func parseMakerNoteIFD(b []byte, make string, parentOrder binary.ByteOrder) *IFD {
	switch make {
	case "Canon":
		return parseCanonMakerNote(b, parentOrder)
	case "NIKON CORPORATION", "Nikon":
		return parseNikonMakerNote(b)
	case "SONY":
		return parseSonyMakerNote(b, parentOrder)
	case "FUJIFILM":
		return parseFujifilmMakerNote(b)
	case "OLYMPUS IMAGING CORP.", "OLYMPUS CORPORATION", "Olympus":
		return parseOlympusMakerNote(b)
	case "PENTAX Corporation", "Ricoh", "RICOH":
		return parsePentaxMakerNote(b)
	case "Panasonic":
		return parsePanasonicMakerNote(b)
	case "LEICA CAMERA AG", "Leica Camera AG", "LEICA", "Leica":
		return parseLeicaMakerNote(b, parentOrder)
	case "DJI":
		return parseDJIMakerNote(b, parentOrder)
	case "SAMSUNG":
		return parseSamsungMakerNote(b, parentOrder)
	case "SIGMA":
		return parseSigmaMakerNote(b)
	case "CASIO COMPUTER CO.,LTD.", "Casio Computer Co.,Ltd.", "CASIO":
		return parseCasioMakerNote(b, parentOrder)
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
	ifd, err := traverse(b, 0, order)
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
//       [0..5]  "Nikon\0"     magic (6 bytes)
//       [6..7]  version       2 bytes (e.g. 0x02 0x10)
//       [8..9]  byte order    "II" or "MM"
//       [10..11] TIFF magic   0x002A (LE) or 0x2A00 (BE)
//       [12..15] IFD offset   relative to start of the embedded TIFF (offset 8)
//
// Offsets within the embedded TIFF are relative to byte 8 of the MakerNote payload.
func parseNikonMakerNote(b []byte) *IFD {
	// Type 3: detect "Nikon\0" prefix.
	if len(b) >= 18 &&
		b[0] == 'N' && b[1] == 'i' && b[2] == 'k' &&
		b[3] == 'o' && b[4] == 'n' && b[5] == 0x00 {

		// The embedded TIFF header starts at offset 8.
		tiffStart := 8
		tiff := b[tiffStart:]

		if len(tiff) < 8 {
			return nil
		}

		var order binary.ByteOrder
		switch {
		case tiff[0] == 'I' && tiff[1] == 'I':
			order = binary.LittleEndian
		case tiff[0] == 'M' && tiff[1] == 'M':
			order = binary.BigEndian
		default:
			return nil
		}

		magic := order.Uint16(tiff[2:])
		if magic != 0x002A {
			return nil
		}

		ifdOffset := order.Uint32(tiff[4:])
		ifd, err := traverse(tiff, ifdOffset, order)
		if err != nil {
			return nil
		}
		return ifd
	}

	// Type 1: plain IFD at offset 0, big-endian.
	// Only attempt if the payload looks like an IFD (entry count < 256 is a heuristic).
	if len(b) >= 2 {
		count := binary.BigEndian.Uint16(b)
		if count > 0 && count < 256 {
			ifd, err := traverse(b, 0, binary.BigEndian)
			if err != nil {
				return nil
			}
			return ifd
		}
	}
	return nil
}

// parseSonyMakerNote parses a Sony MakerNote.
//
// Sony Alpha (ILCE/ILCA/SLT) and Cybershot MakerNote structure:
// Plain TIFF IFD at offset 0, parent byte order. No magic prefix.
// (Sony DSLR-A100 and later; ExifTool Sony.pm)
func parseSonyMakerNote(b []byte, order binary.ByteOrder) *IFD {
	if len(b) < 6 {
		return nil
	}
	ifd, err := traverse(b, 0, order)
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
	if string(b[:8]) != "FUJIFILM" {
		return nil
	}
	ifdOffset := binary.LittleEndian.Uint32(b[12:16])
	ifd, err := traverse(b, ifdOffset, binary.LittleEndian)
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
	if string(b[:8]) != "OLYMPUS\x00" {
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
	ifd, err := traverse(b, 12, order)
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
	case len(b) >= 8 && string(b[:4]) == "AOC\x00":
		// AOC format: big-endian, IFD at offset 6.
		ifd, err := traverse(b, 6, binary.BigEndian)
		if err != nil {
			return nil
		}
		return ifd

	case len(b) >= 14 && string(b[:8]) == "PENTAX \x00":
		// PENTAX prefix format: byte order at [8..9], IFD at offset 12.
		var order binary.ByteOrder
		switch {
		case b[8] == 'I' && b[9] == 'I':
			order = binary.LittleEndian
		case b[8] == 'M' && b[9] == 'M':
			order = binary.BigEndian
		default:
			return nil
		}
		ifd, err := traverse(b, 12, order)
		if err != nil {
			return nil
		}
		return ifd
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
	if string(b[:len(magic)]) != magic {
		return nil
	}
	ifd, err := traverse(b, 12, binary.LittleEndian)
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
		ifd, err := traverse(b, 8, binary.LittleEndian)
		if err != nil {
			return nil
		}
		return ifd
	}
	// Type 0: plain IFD at offset 0, parent byte order.
	ifd, err := traverse(b, 0, parentOrder)
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
	ifd, err := traverse(b, 0, binary.LittleEndian)
	if err != nil {
		ifd, err = traverse(b, 0, parentOrder)
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
	ifd, err := traverse(b, 0, parentOrder)
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
	case string(b[:8]) == "SIGMA\x00\x00\x00":
	case string(b[:8]) == "FOVEON\x00\x00":
	default:
		return nil
	}
	ifd, err := traverse(b, 10, binary.LittleEndian)
	if err != nil {
		return nil
	}
	return ifd
}

// parseCasioMakerNote parses a Casio MakerNote.
//
// Casio MakerNote is a plain TIFF IFD at offset 0, parent byte order.
// Used by Casio Exilim and older Casio camera series (ExifTool Casio.pm).
func parseCasioMakerNote(b []byte, parentOrder binary.ByteOrder) *IFD {
	if len(b) < 6 {
		return nil
	}
	ifd, err := traverse(b, 0, parentOrder)
	if err != nil {
		return nil
	}
	return ifd
}
