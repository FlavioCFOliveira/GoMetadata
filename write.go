package imgmetadata

import (
	"fmt"
	"io"
	"os"

	"github.com/flaviocfo/img-metadata/exif"
	"github.com/flaviocfo/img-metadata/format"
	"github.com/flaviocfo/img-metadata/format/heif"
	"github.com/flaviocfo/img-metadata/format/jpeg"
	"github.com/flaviocfo/img-metadata/format/png"
	"github.com/flaviocfo/img-metadata/format/raw/arw"
	"github.com/flaviocfo/img-metadata/format/raw/cr2"
	"github.com/flaviocfo/img-metadata/format/raw/cr3"
	"github.com/flaviocfo/img-metadata/format/raw/dng"
	"github.com/flaviocfo/img-metadata/format/raw/nef"
	"github.com/flaviocfo/img-metadata/format/raw/orf"
	"github.com/flaviocfo/img-metadata/format/raw/rw2"
	"github.com/flaviocfo/img-metadata/format/tiff"
	"github.com/flaviocfo/img-metadata/format/webp"
	"github.com/flaviocfo/img-metadata/iptc"
	xmppkg "github.com/flaviocfo/img-metadata/xmp"
)

// Write reads the image from r, applies the metadata in m, and writes the
// result to w. Image data and unmodified metadata segments are preserved
// byte-for-byte. r must support seeking (io.ReadSeeker).
func Write(r io.ReadSeeker, w io.Writer, m *Metadata, opts ...WriteOption) error {
	cfg := &writeConfig{preserveUnknownSegments: true}
	for _, o := range opts {
		o(cfg)
	}

	// Detect container format.
	fmtID, err := format.Detect(r)
	if err != nil {
		return fmt.Errorf("imgmetadata: format detection: %w", err)
	}
	if fmtID == format.FormatUnknown {
		return &UnsupportedFormatError{}
	}

	// Serialise modified metadata segments.
	var rawEXIF, rawIPTC, rawXMP []byte

	if m.EXIF != nil {
		rawEXIF, err = exif.Encode(m.EXIF)
		if err != nil {
			return fmt.Errorf("imgmetadata: encode EXIF: %w", err)
		}
	} else if m.rawEXIF != nil {
		// No modification: pass original raw bytes through.
		rawEXIF = m.rawEXIF
	}

	if m.IPTC != nil {
		rawIPTC, err = iptc.Encode(m.IPTC)
		if err != nil {
			return fmt.Errorf("imgmetadata: encode IPTC: %w", err)
		}
	} else if m.rawIPTC != nil {
		rawIPTC = m.rawIPTC
	}

	if m.XMP != nil {
		rawXMP, err = xmppkg.Encode(m.XMP)
		if err != nil {
			return fmt.Errorf("imgmetadata: encode XMP: %w", err)
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

	tmp, err := os.CreateTemp("", "imgmetadata-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

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
