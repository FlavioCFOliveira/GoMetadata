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
//   - CR2 — Canon RAW v2, TIFF-based (read + write; uses the same copy-and-relocate
//     path as TIFF/DNG; the Canon MakerNote is copied verbatim and its
//     TIFF-relative offsets and footer are rebased; validated against the
//     corpus CR2 files)
//   - CR3 — Canon RAW v3, ISOBMFF-based (read + write; cr3.Inject relocates stco/co64 chunk-offset tables after moov rebuild)
//   - NEF — Nikon RAW, TIFF-based (read + write; Nikon MakerNote blob extended
//     to cover PreviewIFD and NikonScanIFD; validated against real Nikon D70 NEF)
//   - ARW — Sony RAW, TIFF-based (read + write; Sony MakerNote TIFF-absolute
//     offsets rebased; SR2Private block relocated verbatim; validated against
//     real Sony DSLR-A500 ARW)
//   - DNG — Adobe Digital Negative, TIFF-based (read + write; SubIFD relocation
//     preserves all out-of-line RATIONAL/SRATIONAL values; validated against
//     real Pentax QS1 DNG corpus file)
//   - ORF — Olympus RAW, TIFF-based (read + write; non-standard IIRO/IIRS magic
//     is patched before relocation and restored in output; validated against
//     Olympus E-M10 IIRO and C5050Z IIRS corpus files)
//   - RW2 — Panasonic RAW, TIFF-based (read + write; Panasonic 16-byte GUID
//     header preserved; IFD0 offsets rebased after GUID insertion; validated
//     against real Panasonic DMC-GF1 RW2)
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
//
// # Writing
//
// Write encodes only the metadata segments (EXIF, IPTC, XMP) that were
// modified and copies every other segment from the image it writes, byte for
// byte. A Read followed by a Write with no change gives back the image
// unchanged, and a Set* method that touches only some segments leaves the
// others as they are, with the IPTC digest of a JPEG included.
//
// The Set* methods mark the segments they change. An edit made through the
// exported fields is not seen until the segment is marked with
// [Metadata.MarkModified]:
//
//	m.IPTC.AddKeyword("sunset")
//	m.MarkModified(gometadata.SegmentIPTC)
//	err = gometadata.WriteFile("photo.jpg", m)
//
// A segment that Metadata holds and the image does not (for example an EXIF
// assigned to m.EXIF after reading an image with none) is written without a
// mark. When Write is given an image other than the one the Metadata was read
// from, it writes every segment, so Read and Write copy the metadata of one
// image to another.
//
// In a TIFF-based container (TIFF, DNG, CR2, NEF, ARW, ORF, RW2) the EXIF
// directories are the structure of the file: once anything is written, the
// directories are laid out again from the parsed EXIF and the offsets of the
// data that follows them move, so the output is not byte-identical to the
// source outside an unmodified write. The metadata and the image data are
// preserved.
//
// # Concurrency
//
// The Set* methods of [Metadata] are serialised by an internal mutex, and
// its getters may run concurrently with each other. The parsed components
// Metadata.EXIF, Metadata.IPTC and Metadata.XMP are not safe for concurrent
// use: mutating them directly from several goroutines, bypassing the Set*
// methods of Metadata, needs the caller's own synchronisation (see the
// concurrency contract of [Metadata]). [Metadata.MarkModified] is serialised
// like the Set* methods.
//
// # Parsed EXIF values
//
// Read does not copy EXIF values: every exif.IFDEntry.Value is a read-only
// view of the raw EXIF bytes that Write later copies the file from. Change a
// value through a Set* method or by assigning a new slice, never by writing
// into an existing Value.
package gometadata
