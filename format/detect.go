package format

import "io"

// magicLen is the maximum number of bytes needed to identify any supported format.
const magicLen = 12

// Detect reads up to magicLen bytes from r (without consuming them) and
// returns the detected FormatID.
func Detect(r io.ReadSeeker) (FormatID, error) {
	var buf [magicLen]byte
	n, err := r.Read(buf[:])
	if err != nil && n == 0 {
		return FormatUnknown, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return FormatUnknown, err
	}
	return detectMagic(buf[:n]), nil
}

// detectMagic identifies the format from magic bytes alone (no I/O).
func detectMagic(b []byte) FormatID {
	if len(b) < 2 {
		return FormatUnknown
	}
	switch {
	// JPEG: SOI marker FF D8
	case b[0] == 0xFF && b[1] == 0xD8:
		return FormatJPEG

	// PNG: 89 50 4E 47 0D 0A 1A 0A
	case len(b) >= 8 &&
		b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4E && b[3] == 0x47 &&
		b[4] == 0x0D && b[5] == 0x0A && b[6] == 0x1A && b[7] == 0x0A:
		return FormatPNG

	// RIFF-based (WebP): "RIFF????WEBP"
	case len(b) >= 12 &&
		b[0] == 0x52 && b[1] == 0x49 && b[2] == 0x46 && b[3] == 0x46 &&
		b[8] == 0x57 && b[9] == 0x45 && b[10] == 0x42 && b[11] == 0x50:
		return FormatWebP

	// HEIF/HEIC: ftyp box — check brand at offset 8
	case len(b) >= 12 && b[4] == 0x66 && b[5] == 0x74 && b[6] == 0x79 && b[7] == 0x70:
		return detectHEIFBrand(b[8:12])

	// TIFF little-endian: "II" 0x2A 0x00
	case b[0] == 0x49 && b[1] == 0x49 && len(b) >= 4 && b[2] == 0x2A && b[3] == 0x00:
		return detectTIFFVariant(b)

	// TIFF big-endian: "MM" 0x00 0x2A
	case b[0] == 0x4D && b[1] == 0x4D && len(b) >= 4 && b[2] == 0x00 && b[3] == 0x2A:
		return detectTIFFVariant(b)

	// Olympus ORF: "IIRO" (little-endian Olympus marker)
	case b[0] == 0x49 && b[1] == 0x49 && len(b) >= 4 && b[2] == 0x52 && b[3] == 0x4F:
		return FormatORF

	// Panasonic RW2: "IIU\x00"
	case b[0] == 0x49 && b[1] == 0x49 && len(b) >= 4 && b[2] == 0x55 && b[3] == 0x00:
		return FormatRW2
	}
	return FormatUnknown
}

// detectHEIFBrand maps the four-byte ftyp brand to a FormatID.
func detectHEIFBrand(brand []byte) FormatID {
	// All ISOBMFF-based RAW and still-image formats use HEIF detection.
	// CR3 uses the 'crx ' brand.
	if len(brand) >= 4 && brand[0] == 0x63 && brand[1] == 0x72 && brand[2] == 0x78 {
		return FormatCR3
	}
	return FormatHEIF
}

// detectTIFFVariant distinguishes TIFF sub-formats (CR2, NEF, ARW, DNG)
// from generic TIFF by inspecting the first few IFD entries.
// Falls back to FormatTIFF for unrecognised variants.
func detectTIFFVariant(b []byte) FormatID {
	// CR2: Canon stores a "CR" marker at bytes 8–9 of the TIFF header reserved
	// area (CR2 specification §3.1). This is the only variant distinguishable
	// from magic bytes alone without IFD parsing.
	if len(b) >= 10 && b[8] == 0x43 && b[9] == 0x52 {
		return FormatCR2
	}
	// DNG, NEF, and ARW share the standard TIFF magic and cannot be reliably
	// distinguished from generic TIFF using only the first 12 bytes. The
	// dispatcher may refine the format after full IFD parsing if needed.
	return FormatTIFF
}
