package format

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// magicLen is the maximum number of bytes needed to identify any supported format.
const magicLen = 12

// tiffScanSize is the number of bytes read for TIFF-variant refinement.
// 8 (TIFF header) + 2 (IFD count) + 64×12 (IFD entries) + 256 (Make value) = 1034 bytes.
const tiffScanSize = 1034

// tiffScanPool recycles the scan buffer used by refineTIFFVariant so that the
// 1 KiB allocation is amortised to zero after the first call.
var tiffScanPool = sync.Pool{ //nolint:gochecknoglobals // sync.Pool: reuse reduces GC pressure
	New: func() any {
		b := make([]byte, tiffScanSize)
		return &b
	},
}

// Detect reads up to magicLen bytes from r (without consuming them) and
// returns the detected FormatID. For TIFF-family files it reads additional
// bytes to distinguish NEF, ARW, and DNG from generic TIFF.
func Detect(r io.ReadSeeker) (FormatID, error) {
	var buf [magicLen]byte
	n, err := r.Read(buf[:])
	if err != nil && n == 0 {
		return FormatUnknown, fmt.Errorf("format: read magic bytes: %w", err)
	}

	fmtID := detectMagic(buf[:n])

	// FormatTIFF is a superset: NEF, ARW, and DNG all share the standard TIFF
	// magic and cannot be distinguished from the first 12 bytes alone.
	// Read up to tiffScanSize bytes to inspect IFD0 tags for a definitive match.
	if fmtID == FormatTIFF {
		fmtID = refineTIFFVariant(r)
	}

	// Seek back to 0 so the caller can re-read the file from the beginning.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return FormatUnknown, fmt.Errorf("format: seek reset: %w", err)
	}
	return fmtID, nil
}

// --------------------------------------------------------------------------
// Magic-byte predicates — one per format family.
// --------------------------------------------------------------------------

// isJPEG reports whether b begins with the JPEG SOI marker FF D8.
func isJPEG(b []byte) bool {
	return len(b) >= 2 && b[0] == 0xFF && b[1] == 0xD8
}

// isPNG reports whether b begins with the 8-byte PNG signature.
func isPNG(b []byte) bool {
	return len(b) >= 8 &&
		b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4E && b[3] == 0x47 &&
		b[4] == 0x0D && b[5] == 0x0A && b[6] == 0x1A && b[7] == 0x0A
}

// isWebP reports whether b carries a RIFF header with the "WEBP" brand.
// Layout: "RIFF" (4 bytes) + file-size (4 bytes) + "WEBP" (4 bytes).
func isWebP(b []byte) bool {
	return len(b) >= 12 &&
		b[0] == 0x52 && b[1] == 0x49 && b[2] == 0x46 && b[3] == 0x46 &&
		b[8] == 0x57 && b[9] == 0x45 && b[10] == 0x42 && b[11] == 0x50
}

// isHEIFFamily reports whether b contains an ISO Base Media File Format ftyp
// box at offset 4, which is the common marker for HEIF/HEIC/AVIF/CR3.
func isHEIFFamily(b []byte) bool {
	return len(b) >= 12 &&
		b[4] == 0x66 && b[5] == 0x74 && b[6] == 0x79 && b[7] == 0x70
}

// isTIFFLittleEndian reports whether b begins with the TIFF little-endian
// byte-order mark "II" followed by magic value 0x002A.
func isTIFFLittleEndian(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x2A && b[3] == 0x00
}

// isTIFFBigEndian reports whether b begins with the TIFF big-endian
// byte-order mark "MM" followed by magic value 0x002A.
func isTIFFBigEndian(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x4D && b[1] == 0x4D && b[2] == 0x00 && b[3] == 0x2A
}

// isORF reports whether b begins with the Olympus ORF marker "IIRO".
func isORF(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x52 && b[3] == 0x4F
}

// isRW2 reports whether b begins with the Panasonic RW2 marker "IIU\x00".
func isRW2(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x55 && b[3] == 0x00
}

// --------------------------------------------------------------------------
// detectMagic — format identification from magic bytes alone (no I/O).
// --------------------------------------------------------------------------

// detectMagic identifies the format from magic bytes alone.
func detectMagic(b []byte) FormatID {
	if len(b) < 2 {
		return FormatUnknown
	}
	if isJPEG(b) {
		return FormatJPEG
	}
	if isPNG(b) {
		return FormatPNG
	}
	if isWebP(b) {
		return FormatWebP
	}
	// HEIF family: ftyp box brand at offset 8 determines the exact sub-format.
	if isHEIFFamily(b) {
		return detectHEIFBrand(b[8:12])
	}
	// Standard TIFF magic (LE or BE). CR2 is distinguished inside detectTIFFVariant;
	// NEF/ARW/DNG require IFD inspection via refineTIFFVariant.
	if isTIFFLittleEndian(b) || isTIFFBigEndian(b) {
		return detectTIFFVariant(b)
	}
	if isORF(b) {
		return FormatORF
	}
	if isRW2(b) {
		return FormatRW2
	}
	return FormatUnknown
}

// detectHEIFBrand maps the four-byte ftyp brand to a FormatID.
// Recognised brands (ISO 23008-12 and CR3 spec):
//   - CR3:  'crx '
//   - AVIF: 'avif', 'avis', 'av01' (ISO 23008-12 §B.4)
//   - HEIF/HEIC: all others (heic, mif1, msf1, etc.)
func detectHEIFBrand(brand []byte) FormatID {
	if len(brand) < 4 {
		return FormatHEIF
	}
	// CR3 uses the 'crx ' brand.
	if brand[0] == 0x63 && brand[1] == 0x72 && brand[2] == 0x78 {
		return FormatCR3
	}
	// AVIF brands per ISO 23008-12 §B.4:
	//   'avif' → brand[0..2] = 'a','v','i', brand[3] = 'f'
	//   'avis' → brand[0..2] = 'a','v','i', brand[3] = 's'
	//   'av01' → brand[0..1] = 'a','v', brand[2] = '0', brand[3] = '1'
	if brand[0] == 0x61 && brand[1] == 0x76 &&
		(brand[2] == 0x69 || brand[2] == 0x30) { // 'avi' → avif/avis; 'av0' → av01
		return FormatAVIF
	}
	return FormatHEIF
}

// detectTIFFVariant distinguishes TIFF sub-formats (CR2, NEF, ARW, DNG)
// from generic TIFF by inspecting magic bytes.
// Falls back to FormatTIFF for unrecognised variants.
func detectTIFFVariant(b []byte) FormatID {
	// CR2: Canon stores a "CR" marker at bytes 8–9 of the TIFF header reserved
	// area (CR2 specification §3.1). This is the only variant distinguishable
	// from magic bytes alone without IFD parsing.
	if len(b) >= 10 && b[8] == 0x43 && b[9] == 0x52 {
		return FormatCR2
	}
	// DNG, NEF, and ARW share the standard TIFF magic — refineTIFFVariant()
	// performs IFD inspection to distinguish them.
	return FormatTIFF
}

// --------------------------------------------------------------------------
// IFD helpers for TIFF-variant refinement.
// --------------------------------------------------------------------------

// findMakeTagInIFD iterates over count IFD0 entries starting at pos in data,
// looking for TagDNGVersion (0xC612) and TagMake (0x010F).
//
// If TagDNGVersion is found the file is definitely DNG (Adobe DNG Spec §6):
// isDNG is set to true and makeRaw is nil.
//
// Otherwise makeRaw carries the raw ASCII bytes of the Make tag value (may be
// nil when the tag is absent or unreadable), and isDNG is false.
func findMakeTagInIFD(data []byte, order binary.ByteOrder, count, pos int) (makeRaw []byte, isDNG bool) {
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])

		switch tag {
		case 0xC612: // TagDNGVersion — present only in DNG files (Adobe DNG Spec §6).
			return nil, true

		case 0x010F: // TagMake — ASCII string identifying camera manufacturer (TIFF §8).
			if typ != 2 { // TypeASCII
				break
			}
			total := uint64(cnt) // ASCII: 1 byte per character
			if total == 0 {
				break
			}
			if total <= 4 {
				// e+8 is non-negative: loop guard ensures e+12 ≤ len(data).
				// total ≤ 4 here, so (e+8)+int(total) cannot overflow int.
				end := e + 8 + int(total)
				if end > len(data) {
					break
				}
				makeRaw = data[e+8 : end]
			} else {
				off := order.Uint32(data[e+8:])
				end := uint64(off) + total
				if end > uint64(len(data)) {
					break
				}
				makeRaw = data[off:end]
			}
		}
	}
	return makeRaw, false
}

// mapMakeToFormat maps trimmed Make bytes to the appropriate RAW FormatID.
// Returns FormatNEF for Nikon, FormatARW for Sony, and FormatTIFF for all
// other values (including nil/empty, which means no Make tag was found).
func mapMakeToFormat(makeBytes []byte) FormatID {
	trimmed := bytes.TrimRight(makeBytes, "\x00 ")
	switch {
	case bytes.Equal(trimmed, []byte("NIKON CORPORATION")), bytes.Equal(trimmed, []byte("Nikon")):
		return FormatNEF
	case bytes.Equal(trimmed, []byte("SONY")):
		return FormatARW
	default:
		return FormatTIFF
	}
}

// --------------------------------------------------------------------------
// refineTIFFVariant — IFD0 inspection to distinguish DNG, NEF, ARW from TIFF.
// --------------------------------------------------------------------------

// parseTIFFScanHeader reads up to tiffScanSize bytes from r (which must be
// positioned at file offset 0) and returns the byte order, the IFD0 entry
// count, the byte position of the first IFD0 entry, the raw data slice, the
// pool pointer (caller must return it via tiffScanPool.Put), and whether
// parsing succeeded. On failure the pool buffer is returned automatically and
// the returned bp is nil.
func parseTIFFScanHeader(r io.ReadSeeker) (order binary.ByteOrder, count, pos int, data []byte, bp *[]byte, ok bool) {
	bp = tiffScanPool.Get().(*[]byte) //nolint:forcetypeassert,revive // tiffScanPool.New always stores *[]byte; pool invariant
	data = *bp
	n, _ := io.ReadFull(r, data)
	if n < 10 {
		tiffScanPool.Put(bp)
		return nil, 0, 0, nil, nil, false
	}
	data = data[:n]

	// Parse byte order from the TIFF header (TIFF §2).
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		tiffScanPool.Put(bp)
		return nil, 0, 0, nil, nil, false
	}

	ifd0Off := order.Uint32(data[4:])
	if int(ifd0Off)+2 > len(data) {
		tiffScanPool.Put(bp)
		return nil, 0, 0, nil, nil, false
	}

	count = int(order.Uint16(data[ifd0Off:]))
	if count > 512 {
		tiffScanPool.Put(bp)
		return nil, 0, 0, nil, nil, false
	}
	pos = int(ifd0Off) + 2
	return order, count, pos, data, bp, true
}

// refineTIFFVariant reads IFD0 from r to distinguish DNG, NEF, and ARW from
// a generic TIFF file. r must be positioned at the start of the file.
// Returns FormatTIFF when the variant cannot be determined.
func refineTIFFVariant(r io.ReadSeeker) FormatID {
	// Seek to start — Detect may have left the reader after the initial read.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return FormatTIFF
	}

	order, count, pos, data, bp, ok := parseTIFFScanHeader(r)
	if !ok {
		return FormatTIFF
	}

	makeRaw, isDNG := findMakeTagInIFD(data, order, count, pos)
	tiffScanPool.Put(bp)

	if isDNG {
		return FormatDNG
	}
	return mapMakeToFormat(makeRaw)
}
