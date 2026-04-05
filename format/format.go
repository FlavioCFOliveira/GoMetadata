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
// the given format (i.e., Write and WriteFile will not return UnsupportedFormatError).
func SupportsWrite(f FormatID) bool {
	switch f {
	case FormatJPEG, FormatTIFF, FormatPNG, FormatHEIF, FormatAVIF, FormatWebP,
		FormatCR2, FormatCR3, FormatNEF, FormatARW, FormatDNG, FormatORF, FormatRW2:
		return true
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
