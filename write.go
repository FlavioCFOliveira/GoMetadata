package gometadata

import (
	"fmt"
	"io"
	"os"

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
//nolint:gochecknoglobals // dispatch table: read-only after init, never mutated
var injectors = map[format.FormatID]func(io.ReadSeeker, io.Writer, []byte, []byte, []byte) error{
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
func Write(r io.ReadSeeker, w io.Writer, m *Metadata, opts ...WriteOption) error {
	// Guard against structurally broken metadata that would panic in encoders.
	if m.EXIF != nil && m.EXIF.IFD0 == nil {
		return ErrNilIFD0Write
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

	rawEXIF, rawIPTC, rawXMP, err := encodeMetadata(m)
	if err != nil {
		return err
	}

	return injectByFormat(r, w, fmtID, rawEXIF, rawIPTC, rawXMP)
}

// WriteFile reads the image at path, applies the metadata in m, and writes
// the result back to the same file atomically. It is a convenience wrapper
// around Write.
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

	tmp, err := os.CreateTemp("", "gometadata-*")
	if err != nil {
		return fmt.Errorf("gometadata: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Preserve original file permissions before writing any data.
	if err := tmp.Chmod(fi.Mode()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("gometadata: chmod temp file: %w", err)
	}

	if err := Write(f, tmp, m, opts...); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("gometadata: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("gometadata: rename temp file: %w", err)
	}
	return nil
}

// encodeMetadata serialises each modified metadata segment. If a segment was
// not modified (m.EXIF/IPTC/XMP is nil) the original raw bytes are passed
// through unchanged. Returns the first encoding error encountered.
func encodeMetadata(m *Metadata) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	if m.EXIF != nil {
		rawEXIF, err = exif.Encode(m.EXIF)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("gometadata: encode EXIF: %w", err)
		}
	} else if m.rawEXIF != nil {
		rawEXIF = m.rawEXIF
	}

	if m.IPTC != nil {
		rawIPTC, err = iptc.Encode(m.IPTC)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("gometadata: encode IPTC: %w", err)
		}
	} else if m.rawIPTC != nil {
		rawIPTC = m.rawIPTC
	}

	if m.XMP != nil {
		rawXMP, err = xmppkg.Encode(m.XMP)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("gometadata: encode XMP: %w", err)
		}
	} else if m.rawXMP != nil {
		rawXMP = m.rawXMP
	}

	return rawEXIF, rawIPTC, rawXMP, nil
}

// injectByFormat dispatches to the correct container handler for segment injection.
func injectByFormat(r io.ReadSeeker, w io.Writer, fmtID format.FormatID, rawEXIF, rawIPTC, rawXMP []byte) error {
	fn, ok := injectors[fmtID]
	if !ok {
		return &UnsupportedFormatError{}
	}
	return wrapInject(fn(r, w, rawEXIF, rawIPTC, rawXMP))
}

// wrapInject wraps errors from format-specific Inject calls with the library prefix.
func wrapInject(err error) error {
	if err != nil {
		return fmt.Errorf("gometadata: %w", err)
	}
	return nil
}
