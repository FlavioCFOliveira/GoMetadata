package format

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// magicLen is the maximum number of bytes needed to identify any supported format.
// 36 bytes covers the ISOBMFF ftyp box layout needed to scan compatible_brands:
//
//	offset 0-3:   box size
//	offset 4-7:   box type ("ftyp")
//	offset 8-11:  major_brand (4 bytes)
//	offset 12-15: minor_version (4 bytes)
//	offset 16-35: up to 5 compatible_brand entries (4 bytes each) — sufficient to
//	              detect 'avif'/'avis'/'av01' in compatible_brands for mif1-major AVIF.
//
// ISO 14496-12 §4.3; ISO 23008-12 §B.4 (AVIF brand requirements).
// All other formats require ≤ 12 bytes so the increase is free at call sites.
const magicLen = 36

// tiffScanSize is the number of bytes read for TIFF-variant refinement.
// Classic TIFF:  8 (header) + 2 (IFD count) + 64×12 (IFD entries) + 256 (Make value) = 1034 bytes.
// BigTIFF:      16 (header) + 8 (IFD count) + 64×20 (IFD entries) + 256 (Make value) = 1560 bytes.
// We use the larger value so the same pool buffer covers both cases.
const tiffScanSize = 1560

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
// byte-order mark "II" followed by classic TIFF magic value 0x002A.
// BigTIFF (magic 0x002B) is handled separately by isBigTIFFLittleEndian.
func isTIFFLittleEndian(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x2A && b[3] == 0x00
}

// isTIFFBigEndian reports whether b begins with the TIFF big-endian
// byte-order mark "MM" followed by classic TIFF magic value 0x002A.
// BigTIFF (magic 0x002B) is handled separately by isBigTIFFBigEndian.
func isTIFFBigEndian(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x4D && b[1] == 0x4D && b[2] == 0x00 && b[3] == 0x2A
}

// isBigTIFFLittleEndian reports whether b begins with the BigTIFF little-endian
// byte-order mark "II" followed by BigTIFF magic value 0x002B.
// BigTIFF spec (Aware Systems / libtiff) §2: magic = 43 (0x002B).
// Layout: 'I'(49) 'I'(49) 0x2B 0x00.
func isBigTIFFLittleEndian(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x2B && b[3] == 0x00
}

// isBigTIFFBigEndian reports whether b begins with the BigTIFF big-endian
// byte-order mark "MM" followed by BigTIFF magic value 0x002B.
// BigTIFF spec (Aware Systems / libtiff) §2: magic = 43 (0x002B).
// Layout: 'M'(4D) 'M'(4D) 0x00 0x2B.
func isBigTIFFBigEndian(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x4D && b[1] == 0x4D && b[2] == 0x00 && b[3] == 0x2B
}

// isORF reports whether b begins with an Olympus ORF magic marker.
// Two variants are recognised:
//   - IIRO (0x49 0x49 0x52 0x4F): used by Olympus DSLRs (E-series, OM-D line).
//   - IIRS (0x49 0x49 0x52 0x53): used by older Olympus compacts
//     (C5050Z, C8080, SP-series).
//
// Both share the same IFD structure; only bytes [2:4] differ.
// ExifTool Olympus.pm: ORFMagic = "IIRO" | "IIRS".
func isORF(b []byte) bool {
	return len(b) >= 4 &&
		b[0] == 0x49 && b[1] == 0x49 && b[2] == 0x52 &&
		(b[3] == 0x4F || b[3] == 0x53) // 'O' = IIRO, 'S' = IIRS
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
//
//nolint:cyclop,gocyclo // format-dispatch switch: adding new formats necessarily increases complexity
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
	// HEIF family: ftyp box brand determines the exact sub-format.
	// Pass the full buffer so detectHEIFBrand can inspect compatible_brands when
	// the major brand alone is ambiguous (e.g. 'mif1' can be either HEIF or AVIF).
	// ISO 14496-12 §4.3; ISO 23008-12 §B.4.
	if isHEIFFamily(b) {
		return detectHEIFBrand(b[8:])
	}
	// Standard TIFF magic (LE or BE). CR2 is distinguished inside detectTIFFVariant;
	// NEF/ARW/DNG require IFD inspection via refineTIFFVariant.
	if isTIFFLittleEndian(b) || isTIFFBigEndian(b) {
		return detectTIFFVariant(b)
	}
	// BigTIFF magic (LE or BE). BigTIFF spec (Aware Systems / libtiff) §2: magic
	// value 0x002B replaces 0x002A in classic TIFF. tiff.Extract handles both
	// classic and BigTIFF, so we return FormatTIFF here; Detect will call
	// refineTIFFVariant which reads the magic from the file and auto-selects the
	// correct IFD layout (BigTIFF-aware walk vs classic walk).
	if isBigTIFFLittleEndian(b) || isBigTIFFBigEndian(b) {
		return FormatTIFF
	}
	if isORF(b) {
		return FormatORF
	}
	if isRW2(b) {
		return FormatRW2
	}
	return FormatUnknown
}

// detectHEIFBrand identifies the HEIF-family FormatID from the ftyp box payload
// starting at the major_brand field (file offset 8). The slice layout is:
//
//	b[0:4]  major_brand
//	b[4:8]  minor_version (ignored)
//	b[8:]   compatible_brands[] (4 bytes each, variable count)
//
// Recognised brands:
//   - CR3:  major 'crx '
//   - AVIF: major 'avif', 'avis', 'av01'; or major 'MA1A'/'MA1B' (MIAF §6.9);
//     or any major + compatible_brands containing 'avif'/'avis'/'av01'
//     (ISO 23008-12 §B.4: libavif emits major='mif1' + compat='avif')
//   - HEIF/HEIC: all other brands
//
// #137 fix: previously only the 4-byte major brand was inspected, causing
// files with major_brand='mif1' and 'avif' in compatible_brands (a valid and
// common libavif output) to be misidentified as HEIF.
// ISO 14496-12 §4.3; ISO 23008-12 §B.4 (AVIF brand requirements).
func detectHEIFBrand(b []byte) FormatID { //nolint:cyclop,gocyclo // brand detection: each brand check is a separate spec-derived rule; complexity is necessary and documented
	if len(b) < 4 {
		return FormatHEIF
	}
	major := b[0:4]

	// CR3 uses the 'crx ' brand.
	if major[0] == 0x63 && major[1] == 0x72 && major[2] == 0x78 {
		return FormatCR3
	}

	// isAVIFBrand reports whether the 4-byte brand slice is an AVIF brand.
	// AVIF brands per ISO 23008-12 §B.4:
	//   'avif' (0x61 0x76 0x69 0x66)
	//   'avis' (0x61 0x76 0x69 0x73) — AVIF image sequence
	//   'av01' (0x61 0x76 0x30 0x31) — older brand still seen in the wild
	// MIAF (ISO 23000-22) §6.9 application brands:
	//   'MA1A' (0x4D 0x41 0x31 0x41) — AVIF baseline constrained
	//   'MA1B' (0x4D 0x41 0x31 0x42) — AVIF high-tier constrained
	isAVIFBrand := func(brand []byte) bool {
		if len(brand) < 4 {
			return false
		}
		return (brand[0] == 0x61 && brand[1] == 0x76 &&
			(brand[2] == 0x69 || brand[2] == 0x30)) || // avif/avis/av01
			(brand[0] == 0x4D && brand[1] == 0x41 &&
				brand[2] == 0x31 && (brand[3] == 0x41 || brand[3] == 0x42)) // MA1A/MA1B
	}

	if isAVIFBrand(major) {
		return FormatAVIF
	}

	// Check compatible_brands (present at b[8:], 4 bytes each) when the major
	// brand alone is not AVIF. This handles the libavif pattern of
	// major_brand='mif1' with 'avif' in compatible_brands.
	// ISO 23008-12 §B.4: a conformant AVIF reader MUST accept files whose
	// compatible_brands list includes 'avif' even when the major brand differs.
	if len(b) >= 12 { // need at least major(4)+minor_version(4)+one_compat(4)
		compatBrands := b[8:] // skip major_brand[4] + minor_version[4]
		for i := 0; i+4 <= len(compatBrands); i += 4 {
			if isAVIFBrand(compatBrands[i : i+4]) { //nolint:gosec // G602: bounds guaranteed by loop condition i+4 <= len(compatBrands)
				return FormatAVIF
			}
		}
	}

	return FormatHEIF
}

// detectTIFFVariant distinguishes TIFF sub-formats (CR2, NEF, ARW, DNG)
// from generic TIFF by inspecting magic bytes.
// Falls back to FormatTIFF for unrecognised variants.
func detectTIFFVariant(b []byte) FormatID {
	// CR2: Canon stores a proprietary marker at bytes 8–11 of the TIFF header:
	//   bytes [8:9]  = 0x43 0x52 ("CR") — Canon RAW identifier
	//   byte  [10]   = 0x02             — CR2 major version (always 2)
	//   byte  [11]   = 0x00             — CR2 minor version
	//
	// Canon CR2 Specification §3.1: the "CR" ASCII tag at offset 8 plus the
	// version byte at offset 10 (must be 2) uniquely identify a CR2 file.
	//
	// #136 fix: check byte[10]==0x02 to prevent a generic TIFF whose first IFD
	// entry tag happens to be 0x5243 (bytes [8:10]="CR" in little-endian) from
	// being misclassified as FormatCR2. Real CR2 files always have version=2.
	if len(b) >= 11 && b[8] == 0x43 && b[9] == 0x52 && b[10] == 0x02 {
		return FormatCR2
	}
	// DNG, NEF, and ARW share the standard TIFF magic — refineTIFFVariant()
	// performs IFD inspection to distinguish them.
	return FormatTIFF
}

// --------------------------------------------------------------------------
// IFD helpers for TIFF-variant refinement.
// --------------------------------------------------------------------------

// findMakeTagInIFD iterates over count classic-TIFF IFD0 entries (12 bytes
// each) starting at pos in data, looking for TagDNGVersion (0xC612) and
// TagMake (0x010F).
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

// findMakeTagInIFDBigTIFF iterates over count BigTIFF IFD0 entries (20 bytes
// each) starting at pos in data, looking for TagDNGVersion (0xC612) and
// TagMake (0x010F).
//
// BigTIFF IFD entry layout (BigTIFF spec §2):
//
//	bytes  0-1:  tag  (uint16)
//	bytes  2-3:  type (uint16)
//	bytes  4-11: count (uint64)
//	bytes 12-19: value-or-offset (uint64); inline when typeSz*count <= 8
//
// If TagDNGVersion is found the file is DNG (Adobe DNG Spec §6).
// Otherwise makeRaw carries the raw ASCII Make bytes (may be nil).
func findMakeTagInIFDBigTIFF(data []byte, order binary.ByteOrder, count, pos int) (makeRaw []byte, isDNG bool) {
	for i := 0; i < count; i++ { //nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier
		e := pos + i*20
		if e+20 > len(data) {
			break
		}
		tag := order.Uint16(data[e:])
		typ := order.Uint16(data[e+2:])
		cnt := order.Uint64(data[e+4:])

		switch tag {
		case 0xC612: // TagDNGVersion — present only in DNG files (Adobe DNG Spec §6).
			return nil, true

		case 0x010F: // TagMake — ASCII string (TIFF §8, type 2 = TypeASCII).
			if typ != 2 { // TypeASCII
				break
			}
			total := cnt // ASCII: 1 byte per character
			if total == 0 {
				break
			}
			// BigTIFF spec §2: inline threshold is 8 bytes.
			if total <= 8 {
				// Inline: value is in bytes [e+12 : e+12+total].
				// e+20 ≤ len(data) is already verified above; total ≤ 8 ensures
				// e+12+total ≤ e+20 ≤ len(data).
				makeRaw = data[e+12 : e+12+int(total)]
			} else {
				// Out-of-line: bytes [e+12:e+20] hold a uint64 file offset.
				off := order.Uint64(data[e+12:])
				end := off + total
				if off > uint64(len(data)) || total > uint64(len(data))-off {
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
// count, the byte position of the first IFD0 entry, the raw data slice,
// whether the file is BigTIFF, the pool pointer (caller must return it via
// tiffScanPool.Put), and whether parsing succeeded. On failure the pool buffer
// is returned automatically and the returned bp is nil.
//
// Supports both classic TIFF (8-byte header, uint16 entry count, uint32 IFD
// offset) and BigTIFF (16-byte header, uint64 entry count, uint64 IFD offset).
// BigTIFF spec (Aware Systems / libtiff) §2.
//
//nolint:cyclop // BigTIFF vs classic dual-path header parsing; complexity is intrinsic to the two-format dispatch
func parseTIFFScanHeader(r io.ReadSeeker) (order binary.ByteOrder, count, pos int, data []byte, bigTIFF bool, bp *[]byte, ok bool) {
	bp = tiffScanPool.Get().(*[]byte) //nolint:forcetypeassert,revive // tiffScanPool.New always stores *[]byte; pool invariant
	data = *bp
	n, _ := io.ReadFull(r, data)
	if n < 10 {
		tiffScanPool.Put(bp)
		return nil, 0, 0, nil, false, nil, false
	}
	data = data[:n]

	// Parse byte order from the TIFF/BigTIFF header (TIFF §2; BigTIFF spec §2).
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		tiffScanPool.Put(bp)
		return nil, 0, 0, nil, false, nil, false
	}

	// BigTIFF spec §2: magic value 0x002B (43) distinguishes BigTIFF from
	// classic TIFF (magic 0x002A = 42). The header layout differs:
	//   Classic:  magic(2) + ifd0Off32(4) at bytes [2:8]
	//   BigTIFF:  magic(2) + offsetBytesize(2) + constant(2) + ifd0Off64(8) at bytes [2:16]
	magic := order.Uint16(data[2:])
	bigTIFF = magic == 0x002B

	if bigTIFF {
		count, pos, ok = parseBigTIFFIFD0(data, order, n)
		if !ok {
			tiffScanPool.Put(bp)
			return nil, 0, 0, nil, false, nil, false
		}
	} else {
		count, pos, ok = parseClassicTIFFIFD0(data, order)
		if !ok {
			tiffScanPool.Put(bp)
			return nil, 0, 0, nil, false, nil, false
		}
	}

	return order, count, pos, data, bigTIFF, bp, true
}

// parseBigTIFFIFD0 extracts the IFD0 entry count and first-entry position from
// a BigTIFF scan buffer. Returns ok=false if the header or IFD is malformed.
//
// BigTIFF spec §2: 16-byte header; IFD0 offset is uint64 at bytes [8:16].
func parseBigTIFFIFD0(data []byte, order binary.ByteOrder, n int) (count, pos int, ok bool) {
	// BigTIFF header requires at least 16 bytes.
	if n < 16 {
		return 0, 0, false
	}
	// bytes [4:6]: offsetBytesize must be 8 (BigTIFF spec §2).
	if order.Uint16(data[4:]) != 8 {
		return 0, 0, false
	}
	// IFD0 offset is a uint64 at bytes [8:16].
	ifd0Off := order.Uint64(data[8:])

	// Guard: IFD0 offset + 8-byte count field must fit in data.
	if ifd0Off > uint64(len(data)) || uint64(len(data))-ifd0Off < 8 {
		return 0, 0, false
	}

	// BigTIFF IFD entry count is uint64; cap at 512 for the refiner
	// (same sanity limit as classic TIFF).
	rawCount := order.Uint64(data[ifd0Off:])
	if rawCount > 512 {
		return 0, 0, false
	}
	// ifd0Off ≤ uint64(len(data))-8 ≤ tiffScanSize-8 = 1552, so ifd0Off+8
	// fits in int on any platform. The +8 offset points past the count field.
	return int(rawCount), int(ifd0Off) + 8, true //nolint:gosec // G115: ifd0Off ≤ tiffScanSize (1560) — safe int cast
}

// parseClassicTIFFIFD0 extracts the IFD0 entry count and first-entry position
// from a classic TIFF scan buffer. Returns ok=false if the header is malformed.
//
// Classic TIFF §2: IFD0 offset is uint32 at bytes [4:8].
func parseClassicTIFFIFD0(data []byte, order binary.ByteOrder) (count, pos int, ok bool) {
	ifd0Off32 := order.Uint32(data[4:])
	// Use uint64 arithmetic to avoid int truncation on 32-bit platforms
	// (GOARCH=386/arm): int(uint32 >= 2^31) is negative, which would cause the
	// guard to pass while the subsequent slice panics. (task #74)
	if uint64(ifd0Off32)+2 > uint64(len(data)) {
		return 0, 0, false
	}

	entryCount := int(order.Uint16(data[ifd0Off32:]))
	if entryCount > 512 {
		return 0, 0, false
	}
	// ifd0Off32 ≤ len(data)-2 ≤ tiffScanSize, so +2 fits in int on all platforms.
	return entryCount, int(ifd0Off32) + 2, true
}

// refineTIFFVariant reads IFD0 from r to distinguish DNG, NEF, and ARW from
// a generic TIFF file. Handles both classic TIFF (magic 0x002A) and BigTIFF
// (magic 0x002B); the magic is re-read from the file so no information is lost.
// r must be positioned at the start of the file.
// Returns FormatTIFF when the variant cannot be determined.
func refineTIFFVariant(r io.ReadSeeker) FormatID {
	// Seek to start — Detect may have left the reader after the initial read.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return FormatTIFF
	}

	order, count, pos, data, bigTIFF, bp, ok := parseTIFFScanHeader(r)
	if !ok {
		return FormatTIFF
	}

	var makeRaw []byte
	var isDNG bool
	if bigTIFF {
		// BigTIFF spec §2: 20-byte IFD entries with uint64 count and uint64
		// value-or-offset fields. Use the BigTIFF-aware tag scanner.
		makeRaw, isDNG = findMakeTagInIFDBigTIFF(data, order, count, pos)
	} else {
		// Classic TIFF §2: 12-byte IFD entries.
		makeRaw, isDNG = findMakeTagInIFD(data, order, count, pos)
	}

	// mapMakeToFormat must be called before tiffScanPool.Put(bp): makeRaw is a
	// subslice of the pool buffer (*bp). Putting bp back before reading makeRaw
	// would allow another goroutine to overwrite the buffer concurrently.
	format := FormatDNG
	if !isDNG {
		format = mapMakeToFormat(makeRaw)
	}
	tiffScanPool.Put(bp)
	return format
}
