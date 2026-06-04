// Package format provides image container detection and metadata
// extraction/injection for all supported formats.
package format

import "io"

// FormatID identifies the container format of an image file.
//
//nolint:revive // legacy name: renaming to ID would break the public API
type FormatID uint8

// FormatUnknown and related constants enumerate all image container formats
// supported by the library. Format detection is by magic bytes, never by
// file extension (CLAUDE.md §1).
const (
	FormatUnknown FormatID = iota
	FormatJPEG
	FormatTIFF
	FormatPNG
	FormatHEIF // includes HEIC and other non-AVIF ISOBMFF image brands
	FormatWebP
	FormatCR2
	FormatCR3
	FormatNEF
	FormatARW
	FormatDNG
	FormatORF
	FormatRW2
	FormatAVIF // AVIF (AV1 Image File Format, ISO 23008-12)
)

// formatNames maps FormatID iota values to their human-readable names.
// Array indices must stay in sync with the iota block above.
var formatNames = [...]string{ //nolint:gochecknoglobals // read-only lookup table indexed by FormatID iota; never mutated
	FormatUnknown: "Unknown",
	FormatJPEG:    "JPEG",
	FormatTIFF:    "TIFF",
	FormatPNG:     "PNG",
	FormatHEIF:    "HEIF",
	FormatWebP:    "WebP",
	FormatCR2:     "CR2",
	FormatCR3:     "CR3",
	FormatNEF:     "NEF",
	FormatARW:     "ARW",
	FormatDNG:     "DNG",
	FormatORF:     "ORF",
	FormatRW2:     "RW2",
	FormatAVIF:    "AVIF",
}

// String returns a human-readable name for the format.
func (f FormatID) String() string {
	if int(f) >= len(formatNames) || formatNames[f] == "" {
		return "Unknown"
	}
	return formatNames[f]
}

// SupportsWrite reports whether the library can inject metadata into files of
// the given format without corrupting the image data.
//
// TIFF-based containers (TIFF, CR2, NEF, ARW, DNG, ORF, RW2) return false
// because writing requires image-data relocation that is not yet implemented
// (roadmap Option A, epic #33). Write and WriteFile return
// ErrWriteNotSupported for these formats.
//
// CR3 also returns false. Although CR3 uses an ISOBMFF container (not TIFF),
// the trak/stbl chunk-offset tables (stco/co64) inside moov store absolute
// file offsets into the mdat box(es). Replacing the CMT1 EXIF payload with
// a re-encoded stream of a different size shifts mdat by delta bytes and
// invalidates every stco/co64 entry, silently corrupting image and preview
// data. Full CR3 write support requires a stco/co64 offset-relocation pass
// that is deferred to a follow-up (see cr3.ErrWriteNotSupported). This
// flag is consistent with the gate inside cr3.Inject.
func SupportsWrite(f FormatID) bool {
	switch f {
	case FormatJPEG, FormatPNG, FormatHEIF, FormatAVIF, FormatWebP:
		return true
	case FormatTIFF, FormatCR2, FormatCR3, FormatNEF, FormatARW, FormatDNG, FormatORF, FormatRW2:
		// SPIKE #6: TIFF-based write is blocked until Option A is implemented.
		// CR3: stco/co64 offset relocation not yet implemented (task #56 safe gate).
		return false
	case FormatUnknown:
		return false
	}
	return false
}

// Container is the interface that every format-specific handler must satisfy.
// It is the only boundary between the container layer and the dispatcher.
//
// Extract reads raw metadata payloads from r without parsing them.
// Any of the returned slices may be nil if that metadata type is absent.
//
// Inject reads the original image from r, replaces the metadata payloads
// with rawEXIF, rawIPTC, and rawXMP respectively (nil means remove), and
// writes the result to w. Image data and unrelated segments are preserved
// byte-for-byte.
type Container interface {
	Extract(r io.ReadSeeker) (rawEXIF, rawIPTC, rawXMP []byte, err error)
	Inject(r io.ReadSeeker, w io.Writer, rawEXIF, rawIPTC, rawXMP []byte) error
}
