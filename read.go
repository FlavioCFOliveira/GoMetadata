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

// Read reads all metadata from r.
// The format is detected automatically from magic bytes; r must support
// seeking (io.ReadSeeker). A nil error means at least one metadata type
// was successfully parsed; individual fields may still be nil.
func Read(r io.ReadSeeker, opts ...ReadOption) (*Metadata, error) {
	cfg := &readConfig{}
	for _, o := range opts {
		o(cfg)
	}

	// Detect container format from magic bytes.
	fmtID, err := format.Detect(r)
	if err != nil {
		return nil, fmt.Errorf("imgmetadata: format detection: %w", err)
	}
	if fmtID == format.FormatUnknown {
		// Read first 12 bytes for the error message.
		var magic [12]byte
		if _, err2 := r.Seek(0, io.SeekStart); err2 == nil {
			_, _ = r.Read(magic[:]) // best-effort: populate magic for error context
		}
		return nil, &UnsupportedFormatError{Magic: magic}
	}

	// Extract raw metadata segments from the container.
	rawEXIF, rawIPTC, rawXMP, err := extractByFormat(r, fmtID)
	if err != nil {
		return nil, err
	}

	m := &Metadata{
		format:  uint8(fmtID),
		rawEXIF: rawEXIF,
		rawIPTC: rawIPTC,
		rawXMP:  rawXMP,
	}

	// Parse each segment unless the caller opted out.
	if rawEXIF != nil && !cfg.lazyEXIF {
		if e, perr := exif.Parse(rawEXIF); perr == nil {
			m.EXIF = e
		}
		// Non-fatal: an unreadable EXIF segment is not an error.
	}

	if rawIPTC != nil && !cfg.lazyIPTC {
		if i, perr := iptc.Parse(rawIPTC); perr == nil {
			m.IPTC = i
		}
	}

	if rawXMP != nil && !cfg.lazyXMP {
		if x, perr := xmppkg.Parse(rawXMP); perr == nil {
			m.XMP = x
		}
	}

	return m, nil
}

// ReadFile opens the file at path and reads all metadata from it.
// It is a convenience wrapper around Read.
func ReadFile(path string, opts ...ReadOption) (*Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f, opts...)
}

// extractByFormat dispatches to the correct container handler for raw segment extraction.
func extractByFormat(r io.ReadSeeker, fmtID format.FormatID) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	switch fmtID {
	case format.FormatJPEG:
		return jpeg.Extract(r)
	case format.FormatTIFF:
		return tiff.Extract(r)
	case format.FormatPNG:
		return png.Extract(r)
	case format.FormatWebP:
		return webp.Extract(r)
	case format.FormatHEIF:
		return heif.Extract(r)
	case format.FormatCR2:
		return cr2.Extract(r)
	case format.FormatCR3:
		return cr3.Extract(r)
	case format.FormatNEF:
		return nef.Extract(r)
	case format.FormatARW:
		return arw.Extract(r)
	case format.FormatDNG:
		return dng.Extract(r)
	case format.FormatORF:
		return orf.Extract(r)
	case format.FormatRW2:
		return rw2.Extract(r)
	default:
		return nil, nil, nil, &UnsupportedFormatError{}
	}
}
