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
// seeking (io.ReadSeeker).
//
// A nil error means the container format was recognised and its metadata
// segments were successfully extracted. The individual EXIF, IPTC, and XMP
// fields on the returned Metadata may still be nil — this happens when the
// container carries no metadata, or when a segment was present but failed to
// parse in best-effort mode (the default). Check ParseWarnings for partial-
// failure details; ParseWarnings is nil when all present segments parsed
// without error and is non-nil when at least one parse warning was recorded.
//
// To distinguish "parsed successfully" from "no metadata in file", inspect
// m.EXIF, m.IPTC, and m.XMP directly — a nil value means that type was absent
// (or failed to parse in best-effort mode).
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
	// For JPEG, rawXMPWire carries the original main+extended segmentation for
	// lossless passthrough writes when the XMP is not modified.
	// rawIPTCDigest carries the 16-byte MD5 from Photoshop resource 0x0425 when
	// present (JPEG only; nil for all other formats). MWG §3.3.1.
	// xmpTruncated is set when extended XMP was capped or had invalid layout (#134).
	rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire, xmpTruncated, err := extractByFormat(r, fmtID)
	if err != nil {
		return nil, err
	}

	m := &Metadata{
		format:     uint8(fmtID),
		rawEXIF:    rawEXIF,
		rawIPTC:    rawIPTC,
		rawXMP:     rawXMP,
		rawXMPWire: rawXMPWire,
		// rawIPTCDigest is populated only for JPEG (the only format whose IRB
		// carries a Photoshop 0x0425 digest resource). TIFF stores IPTC in tag
		// 0x83BB without an IRB wrapper, so no digest applies there.
		rawIPTCDigest: rawIPTCDigest,
	}

	// #134: surface extended XMP truncation as a ParseWarning so the caller
	// can inspect it without aborting parsing. rawXMP still contains the main
	// (standard) XMP packet, which may be fully usable.
	if xmpTruncated {
		m.ParseWarnings = append(m.ParseWarnings, &ParseSegmentError{
			Segment: "XMP",
			Err:     jpeg.ErrExtendedXMPTruncated,
		})
	}

	if err := parseParsedMetadata(m, rawEXIF, rawIPTC, rawXMP, cfg); err != nil {
		return nil, err
	}

	return m, nil
}

// applyOrWarn is the single dispatch point for a segment parse result.
// When warn is non-nil: strict mode returns it as an error immediately;
// best-effort mode appends it to m.ParseWarnings and continues.
func applyOrWarn(m *Metadata, warn *ParseSegmentError, strict bool) error {
	if warn == nil {
		return nil
	}
	if strict {
		return warn
	}
	m.ParseWarnings = append(m.ParseWarnings, warn)
	return nil
}

// parseParsedMetadata parses each raw metadata segment into m unless the
// caller opted out via cfg.
//
// Behaviour depends on cfg.strict:
//   - Strict mode: the first parse failure is returned immediately; subsequent
//     segments are not attempted.
//   - Best-effort mode (default): all segments are attempted; failures are
//     recorded in m.ParseWarnings and the caller receives whichever segments
//     parsed successfully.
//
// Lazy options (WithoutEXIF, WithoutIPTC, WithoutXMP) take precedence over
// Strict: a lazy segment is never parsed and therefore never fails.
func parseParsedMetadata(m *Metadata, rawEXIF, rawIPTC, rawXMP []byte, cfg *readConfig) error {
	if err := applyOrWarn(m, parseEXIF(m, rawEXIF, cfg), cfg.strict); err != nil {
		return err
	}
	if err := applyOrWarn(m, parseIPTC(m, rawIPTC, cfg), cfg.strict); err != nil {
		return err
	}
	return applyOrWarn(m, parseXMP(m, rawXMP, cfg), cfg.strict)
}

// parseEXIF attempts to parse rawEXIF into m.EXIF when raw is non-nil and not lazy.
// Returns a *ParseSegmentError on parse failure, nil on success or skip.
func parseEXIF(m *Metadata, raw []byte, cfg *readConfig) *ParseSegmentError {
	if raw == nil || cfg.lazyEXIF {
		return nil
	}
	var opts []exif.ParseOption
	if cfg.skipMakerNote {
		opts = []exif.ParseOption{exif.SkipMakerNote()}
	}
	e, err := exif.Parse(raw, opts...)
	if err != nil {
		return &ParseSegmentError{Segment: "EXIF", Err: err}
	}
	m.EXIF = e
	return nil
}

// parseIPTC attempts to parse rawIPTC into m.IPTC when raw is non-nil and not lazy.
// Returns a *ParseSegmentError on parse failure, nil on success or skip.
func parseIPTC(m *Metadata, raw []byte, cfg *readConfig) *ParseSegmentError {
	if raw == nil || cfg.lazyIPTC {
		return nil
	}
	i, err := iptc.Parse(raw)
	if err != nil {
		return &ParseSegmentError{Segment: "IPTC", Err: err}
	}
	m.IPTC = i
	return nil
}

// parseXMP attempts to parse rawXMP into m.XMP when raw is non-nil and not lazy.
// Returns a *ParseSegmentError on parse failure, nil on success or skip.
func parseXMP(m *Metadata, raw []byte, cfg *readConfig) *ParseSegmentError {
	if raw == nil || cfg.lazyXMP {
		return nil
	}
	x, err := xmppkg.Parse(raw)
	if err != nil {
		return &ParseSegmentError{Segment: "XMP", Err: err}
	}
	m.XMP = x
	return nil
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

// extractByFormat dispatches to the correct container handler for raw segment
// extraction. For JPEG it calls ExtractFull so that both extended XMP
// (byte-stable passthrough) and the Photoshop 0x0425 IPTC digest are surfaced.
// MWG §3.3.1: the digest is used by iptcTrustElevated() in metadata.go.
//
// xmpTruncated is true when the JPEG extended XMP was capped or had invalid
// chunk layout (#134). The caller converts it to a ParseWarning so it is
// visible in Metadata.ParseWarnings without aborting parsing.
func extractByFormat(r io.ReadSeeker, fmtID format.FormatID) (rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire []byte, xmpTruncated bool, err error) {
	if fmtID == format.FormatJPEG {
		rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire, xmpTruncated, err = jpeg.ExtractFull(r)
		if err != nil {
			return nil, nil, nil, nil, nil, false, fmt.Errorf("gometadata: %w", err)
		}
		return rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire, xmpTruncated, nil
	}
	fn, ok := extractors[fmtID]
	if !ok {
		return nil, nil, nil, nil, nil, false, &UnsupportedFormatError{}
	}
	rawEXIF, rawIPTC, rawXMP, err = fn(r)
	if err != nil {
		return nil, nil, nil, nil, nil, false, fmt.Errorf("gometadata: %w", err)
	}
	return rawEXIF, rawIPTC, nil, rawXMP, nil, false, nil
}
