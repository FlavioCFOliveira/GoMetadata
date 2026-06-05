package gometadata

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/format/heif"
	"github.com/FlavioCFOliveira/GoMetadata/format/jpeg"
	"github.com/FlavioCFOliveira/GoMetadata/format/png"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/arw"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr2"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/cr3"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/dng"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/nef"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/orf"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/rw2"
	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
	"github.com/FlavioCFOliveira/GoMetadata/format/webp"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	xmppkg "github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// injectors maps each FormatID to its Inject function.
//
// The preserveUnknownSegments bool is the last parameter: when false, each
// injector either drops non-essential segments (JPEG) or returns
// ErrPreserveUnknownSegmentsNotSupported (PNG, WebP, HEIF/AVIF, CR3).
// TIFF-based formats are gated earlier by isTIFFBased and never reach this map.
//
//nolint:gochecknoglobals // dispatch table: read-only after init, never mutated
var injectors = map[format.FormatID]func(io.ReadSeeker, io.Writer, []byte, []byte, []byte, bool) error{
	format.FormatJPEG: jpeg.Inject,
	format.FormatTIFF: tiff.Inject,
	format.FormatPNG:  png.Inject,
	format.FormatWebP: webp.Inject,
	// AVIF uses the same ISOBMFF container as HEIF; delegate to the HEIF handler.
	format.FormatHEIF: heif.Inject,
	format.FormatAVIF: heif.Inject,
	format.FormatCR2:  cr2.Inject,
	format.FormatCR3:  cr3.Inject,
	format.FormatNEF:  nef.Inject,
	format.FormatARW:  arw.Inject,
	format.FormatDNG:  dng.Inject,
	format.FormatORF:  orf.Inject,
	format.FormatRW2:  rw2.Inject,
}

// Write reads the image from r, applies the metadata in m, and writes the
// result to w. Image data and unmodified metadata segments are preserved
// byte-for-byte. r must support seeking (io.ReadSeeker).
//
// Write calls m.Validate before performing any I/O. A non-nil error from
// Validate is returned unchanged so callers can inspect it with errors.Is.
func Write(r io.ReadSeeker, w io.Writer, m *Metadata, opts ...WriteOption) error {
	// Validate structural consistency before any I/O. This covers nil IFD0,
	// nil XMP.Properties, and unknown format, replacing the previous inline guard.
	if err := m.Validate(); err != nil {
		return err
	}

	cfg := &writeConfig{preserveUnknownSegments: true}
	for _, o := range opts {
		o(cfg)
	}

	// Detect container format.
	fmtID, err := format.Detect(r)
	if err != nil {
		return fmt.Errorf("gometadata: format detection: %w", err)
	}
	if fmtID == format.FormatUnknown {
		return &UnsupportedFormatError{}
	}

	// Epic #33 (SPIKE #6 Option A): TIFF-based containers store image data
	// (strips, tiles, JPEG thumbnails) at absolute TIFF-stream offsets.
	// Rebuilding the IFD block without relocating that image data corrupts the
	// file.
	//
	// FormatTIFF is un-gated as of tasks #92/#93: tiff.Inject now uses the
	// copy-and-relocate path which enumerates every image-data block and appends
	// it at a corrected offset.
	//
	// FormatDNG is un-gated as of task #94: the copy-and-relocate path now
	// recursively follows SubIFDs (tag 0x014A), enumerates their strip/tile
	// blocks, and relocates them alongside the SubIFD structures. DNG stores its
	// full-resolution image data in SubIFDs — this task implements that path.
	// See format/tiff/relocate.go (enumerateSubIFDs, patchRawIFDOffsets) for details.
	//
	// The five remaining RAW variants (CR2, NEF, ARW, ORF, RW2) remain gated
	// because they require manufacturer-specific offset handling (task #95).
	if isTIFFBased(fmtID) {
		return ErrWriteNotSupported
	}

	rawEXIF, rawIPTC, rawXMP, err := encodeMetadata(m, fmtID)
	if err != nil {
		return err
	}

	return injectByFormat(r, w, fmtID, rawEXIF, rawIPTC, rawXMP, cfg.preserveUnknownSegments)
}

// WriteFile reads the image at path, applies the metadata in m, and writes
// the result back to the same file atomically. It is a convenience wrapper
// around Write.
//
// The temporary file is created in the same directory as path so that the
// final os.Rename is always an intra-filesystem operation. This prevents
// EXDEV errors that occur when the system's default temp directory ($TMPDIR)
// lives on a different filesystem (e.g., in containers or NAS mounts).
func WriteFile(path string, m *Metadata, opts ...WriteOption) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("gometadata: open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("gometadata: stat file: %w", err)
	}

	// Place the temp file in the same directory as path so the eventual rename
	// is guaranteed to be atomic (same filesystem, no EXDEV).
	tmp, err := os.CreateTemp(filepath.Dir(path), "gometadata-*")
	if err != nil {
		return fmt.Errorf("gometadata: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// renamed tracks whether os.Rename has successfully moved tmpName to path.
	// The deferred cleanup removes the temp file only when it is still on disk
	// (i.e., rename has not yet occurred or has failed). os.Remove on a
	// non-existent path returns an error that we silently ignore, making this
	// safe to call unconditionally.
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpName)
		}
	}()

	// Preserve original file permissions before writing any data.
	if err := tmp.Chmod(fi.Mode()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("gometadata: chmod temp file: %w", err)
	}

	if err := Write(f, tmp, m, opts...); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gometadata: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("gometadata: rename temp file: %w", err)
	}
	renamed = true
	return nil
}

// encodeMetadata serialises each modified metadata segment. If a segment was
// not modified (m.EXIF/IPTC/XMP is nil) the original raw bytes are passed
// through unchanged. Returns the first encoding error encountered.
//
// fmtID is the detected destination container format. It is forwarded to
// encodeXMP so that the JPEG extended-XMP wire-frame is never handed to a
// non-JPEG injector.
func encodeMetadata(m *Metadata, fmtID format.FormatID) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if rawEXIF, err = encodeEXIF(m); err != nil {
		return nil, nil, nil, err
	}
	if rawIPTC, err = encodeIPTC(m); err != nil {
		return nil, nil, nil, err
	}
	if rawXMP, err = encodeXMP(m, fmtID); err != nil {
		return nil, nil, nil, err
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}

// encodeEXIF returns the EXIF bytes to write: freshly encoded when m.EXIF is
// non-nil, or the original raw bytes when the caller did not modify EXIF.
func encodeEXIF(m *Metadata) ([]byte, error) {
	if m.EXIF != nil {
		raw, err := exif.Encode(m.EXIF)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode EXIF: %w", err)
		}
		return raw, nil
	}
	return m.rawEXIF, nil
}

// encodeIPTC returns the IPTC bytes to write: freshly encoded when m.IPTC is
// non-nil, or the original raw bytes when the caller did not modify IPTC.
func encodeIPTC(m *Metadata) ([]byte, error) {
	if m.IPTC != nil {
		raw, err := iptc.Encode(m.IPTC)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode IPTC: %w", err)
		}
		return raw, nil
	}
	return m.rawIPTC, nil
}

// encodeXMP returns the XMP bytes to write: freshly encoded when m.XMP is
// non-nil, the wire-frame encoding when the image carried extended XMP and the
// XMP was not modified (guarantees byte-stable round-trips for JPEG), or the
// original raw bytes otherwise.
//
// fmtID is the destination container format. The wire-frame encoding is only
// valid for JPEG: jpeg.Inject is the only injector that knows how to decode it
// (via writeXMPSegments → decodeXMPWire). For every other container format the
// wire-frame is bypassed and rawXMP (the reassembled, user-visible packet) is
// returned instead, preventing a corrupt XMP blob from being written to
// PNG/WebP/HEIF/AVIF and other non-JPEG containers.
//
// See task #70: Extended-XMP wire-frame leaks to PNG/WebP/HEIF inject.
func encodeXMP(m *Metadata, fmtID format.FormatID) ([]byte, error) {
	if m.XMP != nil {
		raw, err := xmppkg.Encode(m.XMP)
		if err != nil {
			return nil, fmt.Errorf("gometadata: encode XMP: %w", err)
		}
		return raw, nil
	}
	// The wire-frame encoding (rawXMPWire) carries the original JPEG main APP1
	// content and the assembled extended XMP payload packed together. Only
	// jpeg.Inject can decode it — passing it to any other injector would write
	// the raw wire-frame bytes verbatim as the XMP packet, producing a corrupt
	// non-XMP blob in the destination container.
	//
	// Return rawXMPWire only when the destination format is JPEG; fall back to
	// rawXMP (the fully reassembled, user-visible packet) for all other formats.
	if m.rawXMPWire != nil && fmtID == format.FormatJPEG {
		return m.rawXMPWire, nil
	}
	return m.rawXMP, nil
}

// injectByFormat dispatches to the correct container handler for segment injection.
// preserveUnknownSegments is forwarded to the format-specific injector: JPEG
// honours it by dropping unknown APPn segments when false; PNG, WebP, HEIF/AVIF,
// and CR3 return ErrPreserveUnknownSegmentsNotSupported when false; TIFF-based
// formats are gated before this function is ever reached.
func injectByFormat(r io.ReadSeeker, w io.Writer, fmtID format.FormatID, rawEXIF, rawIPTC, rawXMP []byte, preserveUnknownSegments bool) error {
	fn, ok := injectors[fmtID]
	if !ok {
		return &UnsupportedFormatError{}
	}
	return wrapInject(fn(r, w, rawEXIF, rawIPTC, rawXMP, preserveUnknownSegments))
}

// wrapInject wraps errors from format-specific Inject calls with the library prefix.
func wrapInject(err error) error {
	if err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}

// isTIFFBased reports whether fmtID is a TIFF-based container format that
// still requires the write gate (ErrWriteNotSupported).
//
// FormatTIFF was removed from this gate in tasks #92/#93 (epic #33): the
// copy-and-relocate serializer handles strip, tile, and main-image JPEG
// offset relocation for plain TIFF files.
//
// FormatDNG was removed from this gate in task #94: the copy-and-relocate
// path now recursively follows SubIFDs (tag 0x014A) and relocates their
// image blocks alongside the SubIFD structures, covering the canonical DNG
// layout (IFD0 thumbnail + one or more SubIFDs containing full-res image).
//
// The five remaining RAW variants (CR2, NEF, ARW, ORF, RW2) remain gated
// until task #95 (manufacturer-specific offset handling) is complete.
func isTIFFBased(fmtID format.FormatID) bool {
	switch fmtID {
	case format.FormatCR2,
		format.FormatNEF,
		format.FormatARW,
		format.FormatORF,
		format.FormatRW2:
		return true
	default:
		return false
	}
}
