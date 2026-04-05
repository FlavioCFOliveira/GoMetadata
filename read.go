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

// extractors maps each FormatID to its Extract function.
var extractors = map[format.FormatID]func(io.ReadSeeker) ([]byte, []byte, []byte, error){ //nolint:gochecknoglobals // dispatch table: read-only after init, never mutated
	format.FormatJPEG: jpeg.Extract,
	format.FormatTIFF: tiff.Extract,
	format.FormatPNG:  png.Extract,
	format.FormatWebP: webp.Extract,
	format.FormatHEIF: heif.Extract,
	// AVIF uses the same ISOBMFF container as HEIF; delegate to the HEIF handler.
	format.FormatAVIF: heif.Extract,
	format.FormatCR2:  cr2.Extract,
	format.FormatCR3:  cr3.Extract,
	format.FormatNEF:  nef.Extract,
	format.FormatARW:  arw.Extract,
	format.FormatDNG:  dng.Extract,
	format.FormatORF:  orf.Extract,
	format.FormatRW2:  rw2.Extract,
}

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

	parseParsedMetadata(m, rawEXIF, rawIPTC, rawXMP, cfg)

	return m, nil
}

// parseIfPresent calls parse(raw) when raw is non-nil and lazy is false.
// Parse failures are non-fatal and silently ignored so callers still receive
// whatever segments were readable.
func parseIfPresent(raw []byte, lazy bool, parse func([]byte)) {
	if raw != nil && !lazy {
		parse(raw)
	}
}

// parseParsedMetadata parses each raw metadata segment into m unless the
// caller opted out via cfg. Parse failures are non-fatal: an unreadable
// segment is silently skipped so the caller still gets whatever was readable.
func parseParsedMetadata(m *Metadata, rawEXIF, rawIPTC, rawXMP []byte, cfg *readConfig) {
	parseIfPresent(rawEXIF, cfg.lazyEXIF, func(raw []byte) {
		var exifOpts []exif.ParseOption
		if cfg.skipMakerNote {
			exifOpts = []exif.ParseOption{exif.SkipMakerNote()}
		}
		if e, perr := exif.Parse(raw, exifOpts...); perr == nil {
			m.EXIF = e
		}
		// Non-fatal: an unreadable EXIF segment is not an error.
	})

	parseIfPresent(rawIPTC, cfg.lazyIPTC, func(raw []byte) {
		if i, perr := iptc.Parse(raw); perr == nil {
			m.IPTC = i
		}
	})

	parseIfPresent(rawXMP, cfg.lazyXMP, func(raw []byte) {
		if x, perr := xmppkg.Parse(raw); perr == nil {
			m.XMP = x
		}
	})
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
	fn, ok := extractors[fmtID]
	if !ok {
		return nil, nil, nil, &UnsupportedFormatError{}
	}
	return wrapExtract(fn(r))
}

// wrapExtract wraps errors from format-specific Extract calls with the library prefix.
func wrapExtract(rawEXIF, rawIPTC, rawXMP []byte, err error) ([]byte, []byte, []byte, error) {
	if err != nil {
		return nil, nil, nil, fmt.Errorf("gometadata: %w", err)
	}
	return rawEXIF, rawIPTC, rawXMP, nil
}
