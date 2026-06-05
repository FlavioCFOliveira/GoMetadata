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
// FormatTIFF returns true as of tasks #92/#93 (epic #33 Option A). The
// format/tiff copy-and-relocate serializer enumerates every image-data block
// (strips, tiles, main-image JPEG) and appends it at a fresh absolute offset,
// preserving pixel data byte-identically.
//
// FormatDNG returns true (bug #98 fixed). The SubIFD relocation path now
// preserves ALL out-of-line value areas in each SubIFD by updating their
// valOrOff pointers to the new absolute positions after the SubIFD block is
// relocated. Previously only strip/tile offset arrays were re-pointed; the fix
// extends this to every OOL entry (RATIONAL, SRATIONAL, DOUBLE, long ASCII,
// etc.), preventing XResolution/YResolution and similar fields from becoming
// "undef" after write. Validated against a real Pentax QS1 DNG corpus file.
//
// CR3 returns true. CR3 uses an ISOBMFF container; cr3.Inject rebuilds the
// Canon UUID box with the new CMTx payloads and then walks every
// trak/stbl/{stco,co64} table inside the rebuilt moov, adding delta to each
// absolute offset that pointed at or beyond the original moov end. This
// relocation pass was implemented in task #91.
//
// FormatCR2 returns true as of task #95. CR2 uses standard LE TIFF magic
// (II*\0) and routes through the same writeTIFF copy-and-relocate path as
// FormatTIFF/FormatDNG. MakerNote blobs are copied verbatim; per SPIKE #24
// Canon MakerNotes use blob-relative (self-relative) offsets, so verbatim
// copying is safe. Validated against a real Canon EOS 350D CR2 corpus file:
// ImageDataHash IN==OUT, all MakerNote/SubIFD tags preserved.
//
// FormatNEF returns true as of task #102. The NEF-specific write path extends the
// Nikon Type-3 MakerNote blob to cover PreviewIFD and NikonScanIFD (which live
// beyond the declared byte count in the outer TIFF entry), enumerates the PreviewIFD
// image block (preview JPEG referenced via a MakerNote-TIFF-relative offset), and
// patches the MakerNote-relative 0x0201 offset after re-encoding. Validated against
// a real Nikon D70 NEF corpus file: ImageDataHash IN==OUT, all metadata preserved.
//
// FormatARW returns false (task #95 empirical validation failed, 2026-06-05).
// Real-corpus tests revealed 52 Sony MakerNote tags lost and SR2Private IFD
// corruption. ARW requires deeper Sony-specific SubIFD/MakerNote handling.
//
// FormatORF and FormatRW2 return false. They use non-standard magic bytes
// (ORF: IIRS; RW2: IIU\0) and require format-specific outer-framing work
// before the TIFF relocator can process them safely. Write and WriteFile
// return ErrWriteNotSupported for those formats.
func SupportsWrite(f FormatID) bool {
	switch f {
	case FormatJPEG, FormatTIFF, FormatDNG, FormatPNG, FormatHEIF, FormatAVIF, FormatWebP, FormatCR3,
		FormatCR2, FormatNEF:
		return true
	case FormatARW:
		// Task #95 empirical validation failed (2026-06-05): real-corpus tests
		// found 52 Sony MakerNote tags lost and SR2Private IFD corruption.
		return false
	case FormatORF, FormatRW2:
		// ORF/RW2 use non-standard TIFF magic and require format-specific
		// outer-framing work before copy-and-relocate can apply.
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
