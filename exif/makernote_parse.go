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
func parseMakerNoteIFD(b []byte, make string, parentOrder binary.ByteOrder) *IFD {
	switch make {
	case "Canon":
		return parseCanonMakerNote(b, parentOrder)
	case "NIKON CORPORATION", "Nikon":
		return parseNikonMakerNote(b)
	case "SONY":
		return parseSonyMakerNote(b, parentOrder)
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
