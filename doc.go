// Package gometadata provides a unified API for reading and writing
// EXIF, IPTC, and XMP metadata from any image format.
//
// Format detection is performed automatically by inspecting magic bytes;
// file extensions are never used.
//
// # Supported formats
//
// The following container formats are fully supported for metadata reading:
//
//   - JPEG (read + write)
//   - TIFF (read + write; the copy-and-relocate serializer preserves image-data
//     blocks byte-identically — strips, tiles, main-image JPEG)
//   - PNG (read + write)
//   - HEIF / HEIC (read + write)
//   - AVIF (read + write)
//   - WebP (read + write)
//   - CR2 — Canon RAW v2, TIFF-based (read only)
//   - CR3 — Canon RAW v3, ISOBMFF-based (read + write; cr3.Inject relocates stco/co64 chunk-offset tables after moov rebuild)
//   - NEF — Nikon RAW, TIFF-based (read only)
//   - ARW — Sony RAW, TIFF-based (read only)
//   - DNG — Adobe Digital Negative, TIFF-based (read only; write is gated
//     pending bug #98: SubIFD out-of-line RATIONAL values are silently lost
//     after write — XResolution/YResolution 300→undef on real corpus files)
//   - ORF — Olympus RAW, TIFF-based (read only)
//   - RW2 — Panasonic RAW, TIFF-based (read only)
//
// # Out-of-scope formats
//
// The following RAW formats are present in the test corpus but are not yet
// implemented. Passing a file in one of these formats to Read, ReadFile,
// Write, or WriteFile will return an [UnsupportedFormatError] gracefully
// without panicking:
//
//   - CRW / CIFF — Canon RAW v1 (pre-CR2)
//   - RAF — Fujifilm RAW
//   - MRW — Minolta RAW
//   - IIQ — Phase One RAW
//   - X3F — Sigma/Foveon RAW
//   - SRW — Samsung RAW
//   - PEF — Pentax RAW (TIFF-encapsulated but distinct magic not yet registered)
//   - RWL — Leica RAW (DNG-like but distinct brand)
//
// Support for these formats may be added in a future minor release.
//
// # Basic usage
//
//	m, err := gometadata.ReadFile("photo.jpg")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Println(m.CameraModel())
//	lat, lon, ok := m.GPS()
package gometadata
