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
		return nil, fmt.Errorf("gometadata: format detection: %w", err)
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
		var exifOpts []exif.ParseOption
		if cfg.skipMakerNote {
			exifOpts = []exif.ParseOption{exif.SkipMakerNote()}
		}
		if e, perr := exif.Parse(rawEXIF, exifOpts...); perr == nil {
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
		return nil, fmt.Errorf("gometadata: open file: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Read(f, opts...)
}

// extractByFormat dispatches to the correct container handler for raw segment extraction.
func extractByFormat(r io.ReadSeeker, fmtID format.FormatID) (rawEXIF, rawIPTC, rawXMP []byte, err error) {
	switch fmtID {
	case format.FormatJPEG:
		return wrapExtract(jpeg.Extract(r))
	case format.FormatTIFF:
		return wrapExtract(tiff.Extract(r))
	case format.FormatPNG:
		return wrapExtract(png.Extract(r))
	case format.FormatWebP:
		return wrapExtract(webp.Extract(r))
	case format.FormatHEIF:
		return wrapExtract(heif.Extract(r))
	case format.FormatAVIF:
		// AVIF uses the same ISOBMFF container as HEIF; delegate to the HEIF handler.
		return wrapExtract(heif.Extract(r))
	case format.FormatCR2:
		return wrapExtract(cr2.Extract(r))
	case format.FormatCR3:
		return wrapExtract(cr3.Extract(r))
	case format.FormatNEF:
		return wrapExtract(nef.Extract(r))
	case format.FormatARW:
		return wrapExtract(arw.Extract(r))
	case format.FormatDNG:
		return wrapExtract(dng.Extract(r))
	case format.FormatORF:
		return wrapExtract(orf.Extract(r))
	case format.FormatRW2:
		return wrapExtract(rw2.Extract(r))
	default:
		return nil, nil, nil, &UnsupportedFormatError{}
	}
}

// wrapExtract wraps errors from format-specific Extract calls with the library prefix.
func wrapExtract(rawEXIF, rawIPTC, rawXMP []byte, err error) ([]byte, []byte, []byte, error) {
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gometadata: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}
