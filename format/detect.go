package format

import (
	"bytes"
	"encoding/binary"
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
		return FormatUnknown, err
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
		return FormatUnknown, err
	}
	return fmtID, nil
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

// refineTIFFVariant reads IFD0 from r to distinguish DNG, NEF, and ARW from
// a generic TIFF file. r must be positioned at the start of the file.
// Returns FormatTIFF when the variant cannot be determined.
func refineTIFFVariant(r io.ReadSeeker) FormatID {
	// Seek to start — Detect may have left the reader after the initial read.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return FormatTIFF
	}

	bp := tiffScanPool.Get().(*[]byte)
	data := *bp
	n, _ := io.ReadFull(r, data)
	if n < 10 {
		tiffScanPool.Put(bp)
		return FormatTIFF
	}
	data = data[:n]

	// Parse byte order from the TIFF header (TIFF §2).
	var order binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		tiffScanPool.Put(bp)
		return FormatTIFF
	}

	ifd0Off := order.Uint32(data[4:])
	if int(ifd0Off)+2 > len(data) {
		tiffScanPool.Put(bp)
		return FormatTIFF
	}

	count := int(order.Uint16(data[ifd0Off:]))
	if count < 0 || count > 512 {
		tiffScanPool.Put(bp)
		return FormatTIFF
	}
	pos := int(ifd0Off) + 2

	var makeRaw []byte
	for i := 0; i < count; i++ {
		e := pos + i*12
		if e+12 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint32(data[e+4:])

		switch tag {
		case 0xC612: // DNGVersion — present only in DNG files (Adobe DNG Spec §6).
			tiffScanPool.Put(bp)
			return FormatDNG

		case 0x010F: // Make — ASCII string identifying camera manufacturer (TIFF §8).
			if typ != 2 { // TypeASCII
				break
			}
			total := uint64(cnt) // ASCII: 1 byte per character
			if total == 0 {
				break
			}
			if total <= 4 {
				end := uint64(e+8) + total
				if end > uint64(len(data)) {
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

	// Map Make bytes to specific RAW format without allocating a string.
	make_ := bytes.TrimRight(makeRaw, "\x00 ")
	var result FormatID
	switch {
	case bytes.Equal(make_, []byte("NIKON CORPORATION")), bytes.Equal(make_, []byte("Nikon")):
		result = FormatNEF
	case bytes.Equal(make_, []byte("SONY")):
		result = FormatARW
	default:
		result = FormatTIFF
	}
	tiffScanPool.Put(bp)
	return result
}
