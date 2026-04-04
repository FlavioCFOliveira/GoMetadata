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

// Write reads the image from r, applies the metadata in m, and writes the
// result to w. Image data and unmodified metadata segments are preserved
// byte-for-byte. r must support seeking (io.ReadSeeker).
func Write(r io.ReadSeeker, w io.Writer, m *Metadata, opts ...WriteOption) error {
	// Guard against structurally broken metadata that would panic in encoders.
	if m.EXIF != nil && m.EXIF.IFD0 == nil {
		return fmt.Errorf("gometadata: EXIF struct has nil IFD0")
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

	// Serialise modified metadata segments.
	var rawEXIF, rawIPTC, rawXMP []byte

	if m.EXIF != nil {
		rawEXIF, err = exif.Encode(m.EXIF)
		if err != nil {
			return fmt.Errorf("gometadata: encode EXIF: %w", err)
		}
	} else if m.rawEXIF != nil {
		// No modification: pass original raw bytes through.
		rawEXIF = m.rawEXIF
	}

	if m.IPTC != nil {
		rawIPTC, err = iptc.Encode(m.IPTC)
		if err != nil {
			return fmt.Errorf("gometadata: encode IPTC: %w", err)
		}
	} else if m.rawIPTC != nil {
		rawIPTC = m.rawIPTC
	}

	if m.XMP != nil {
		rawXMP, err = xmppkg.Encode(m.XMP)
		if err != nil {
			return fmt.Errorf("gometadata: encode XMP: %w", err)
		}
	} else if m.rawXMP != nil {
		rawXMP = m.rawXMP
	}

	return injectByFormat(r, w, fmtID, rawEXIF, rawIPTC, rawXMP)
}

// WriteFile reads the image at path, applies the metadata in m, and writes
// the result back to the same file atomically. It is a convenience wrapper
// around Write.
func WriteFile(path string, m *Metadata, opts ...WriteOption) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "gometadata-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	// Preserve original file permissions before writing any data.
	if err := tmp.Chmod(fi.Mode()); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}

	if err := Write(f, tmp, m, opts...); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// injectByFormat dispatches to the correct container handler for segment injection.
func injectByFormat(r io.ReadSeeker, w io.Writer, fmtID format.FormatID, rawEXIF, rawIPTC, rawXMP []byte) error {
	switch fmtID {
	case format.FormatJPEG:
		return jpeg.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatTIFF:
		return tiff.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatPNG:
		return png.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatWebP:
		return webp.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatHEIF:
		return heif.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatAVIF:
		// AVIF uses the same ISOBMFF container as HEIF; delegate to the HEIF handler.
		return heif.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatCR2:
		return cr2.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatCR3:
		return cr3.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatNEF:
		return nef.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatARW:
		return arw.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatDNG:
		return dng.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatORF:
		return orf.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	case format.FormatRW2:
		return rw2.Inject(r, w, rawEXIF, rawIPTC, rawXMP)
	default:
		return &UnsupportedFormatError{}
	}
}
