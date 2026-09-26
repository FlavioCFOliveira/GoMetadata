package gometadata

import (
	"errors"
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
	// #204: cfg is a stack value, not a heap-allocated *readConfig. Applying
	// opts is confined to the out-of-line, non-inlined applyReadOptions so
	// that the escape this indirection forces (see that function's doc
	// comment) is paid only when the caller actually supplies options. The
	// overwhelming majority of Read calls pass zero options, and for those,
	// cfg here never has its address taken by anything the compiler cannot
	// prove non-escaping (parseParsedMetadata and its helpers all take
	// *readConfig read-only and are confirmed non-leaking via
	// `go build -gcflags=-m`).
	var cfg readConfig
	if len(opts) > 0 {
		cfg = applyReadOptions(opts)
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
	// noCMT1Box is set when a CR3 file has no CMT1 sub-box (audit #138).
	// #238: pass the wanted-segments mask so extraction can skip work that
	// only feeds a segment the caller opted out of via WithoutIPTC/WithoutXMP
	// (currently meaningful for JPEG only: the 0x0425 IPTC digest and the
	// extended-XMP reassembly). rawEXIF and rawIPTC are always extracted
	// regardless — they must remain available for an unmodified Write to
	// pass through byte-for-byte.
	rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire, xmpTruncated, noCMT1Box, err := extractByFormat(r, fmtID, !cfg.lazyIPTC, !cfg.lazyXMP)
	if err != nil {
		return nil, err
	}

	m := &Metadata{
		format:             uint8(fmtID),
		rawEXIF:            rawEXIF,
		rawIPTC:            rawIPTC,
		rawXMP:             rawXMP,
		rawXMPWire:         rawXMPWire,
		rawEXIFIsWholeFile: tiffFamilyRawEXIFIsWholeFile(r, fmtID, rawEXIF),
		// rawIPTCDigest is populated only for JPEG (the only format whose IRB
		// carries a Photoshop 0x0425 digest resource). TIFF stores IPTC in tag
		// 0x83BB without an IRB wrapper, so no digest applies there.
		rawIPTCDigest: rawIPTCDigest,
	}

	// #208: compute the MWG §3.3.1 IPTC-trust-elevation decision exactly once,
	// here, and cache it. rawIPTC and rawIPTCDigest are already final at this
	// point and never change for the rest of m's lifetime, so every later
	// iptcTrustElevated() call (Copyright, Caption, Keywords, Creator) becomes
	// a plain field read instead of re-hashing rawIPTC with MD5 each time.
	m.iptcTrustElev = computeIPTCTrustElevated(m.rawIPTC, m.rawIPTCDigest)

	// #134: surface extended XMP truncation as a ParseWarning so the caller
	// can inspect it without aborting parsing. rawXMP still contains the main
	// (standard) XMP packet, which may be fully usable.
	if xmpTruncated {
		m.ParseWarnings = append(m.ParseWarnings, &ParseSegmentError{
			Segment: "XMP",
			Err:     jpeg.ErrExtendedXMPTruncated,
		})
	}

	// audit #138: when a CR3 file has a valid moov/UUID structure but no CMT1
	// sub-box, Extract returns ErrNoCMT1Box. Treat this as a non-fatal condition
	// so callers still receive XMP and other metadata from the file. The missing
	// CMT1 is surfaced as a ParseWarning rather than aborting the whole read.
	// rawEXIF is nil; rawXMP is populated if an "XMP " sub-box was found.
	if noCMT1Box {
		m.ParseWarnings = append(m.ParseWarnings, &ParseSegmentError{
			Segment: "EXIF",
			Err:     cr3.ErrNoCMT1Box,
		})
	}

	if err := parseParsedMetadata(m, rawEXIF, rawIPTC, rawXMP, &cfg); err != nil {
		return nil, err
	}

	return m, nil
}

// applyReadOptions builds a readConfig by applying opts and returns it by
// value.
//
// #204: kept out-of-line via go:noinline and called only when len(opts) > 0.
// ReadOption is a func(*readConfig) invoked indirectly (o(cfg)); the Go
// compiler cannot see through an indirect call to confirm the callee does
// not retain the pointer, so any *readConfig passed to an indirect call is
// conservatively heap-allocated (verified with `go build -gcflags=-m`, which
// reports "leaking param: c" for the loop body below). Confining that leak to
// this dedicated, never-inlined function means Read's own readConfig local
// stays on the stack for the zero-option fast path — the heap allocation
// here is paid only by callers who actually supply options.
//
//go:noinline
func applyReadOptions(opts []ReadOption) readConfig {
	var cfg readConfig
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
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

// nonStandardRAWMagic reports the RAW-container magic value at raw[2:4],
// read as a little-endian uint16 (matching ORF/RW2's own byte order), when
// raw carries a known non-standard classic-TIFF magic that exif.Parse would
// otherwise reject. ok is false for any other input, including standard TIFF/
// BigTIFF magic and big-endian files — those are parsed by exif.Parse without
// any extra option.
//
// #117: ORF/RW2 Extract functions return rawEXIF with the ORIGINAL magic so
// callers can write the bytes back unmodified. #286: rather than cloning the
// whole file to patch bytes[2:4] to standard TIFF magic (0x2A 0x00) before
// parsing — which left that clone permanently retained via every out-of-line
// IFDEntry.Value alias into it — parseEXIF passes the reported magic value to
// exif.AcceptRAWMagic and parses raw directly, with zero extra allocation.
//
// Known non-standard magics (both little-endian, bytes[0:2] = "II"):
//   - ORF IIRO: bytes[2:4] = 0x52 0x4F ('R', 'O') — Olympus DSLR / OM-D
//   - ORF IIRS: bytes[2:4] = 0x52 0x53 ('R', 'S') — Olympus compact
//   - RW2:      bytes[2:4] = 0x55 0x00             — Panasonic RAW
//
// ExifTool Olympus.pm / Panasonic RW2: these are the only bytes that diverge
// from standard classic TIFF.
func nonStandardRAWMagic(raw []byte) (magic uint16, ok bool) {
	if len(raw) < 4 || raw[0] != 0x49 || raw[1] != 0x49 {
		return 0, false // big-endian or too short: no non-standard magic possible
	}
	b2, b3 := raw[2], raw[3]
	if (b2 == 0x52 && (b3 == 0x4F || b3 == 0x53)) || // ORF IIRO/IIRS
		(b2 == 0x55 && b3 == 0x00) { // RW2 IIU\x00
		return uint16(b2) | uint16(b3)<<8, true
	}
	return 0, false
}

// parseEXIF attempts to parse rawEXIF into m.EXIF when raw is non-nil and not lazy.
// Returns a *ParseSegmentError on parse failure, nil on success or skip.
//
// On success, any lenient-parse warnings collected in e.Warnings (audit findings
// #126/#129/#130/#131/#132) are appended to m.ParseWarnings as individual
// *ParseSegmentError entries so that callers can inspect them at the top-level
// Metadata API without aborting parsing.
//
// #117/#286: ORF/RW2 rawEXIF carries the original non-standard magic.
// nonStandardRAWMagic detects it and exif.AcceptRAWMagic lets exif.Parse
// consume raw directly — no clone, no retained copy of the whole file.
func parseEXIF(m *Metadata, raw []byte, cfg *readConfig) *ParseSegmentError {
	if raw == nil || cfg.lazyEXIF {
		return nil
	}
	var opts []exif.ParseOption
	if cfg.skipMakerNote {
		opts = append(opts, exif.SkipMakerNote())
	}
	if magic, ok := nonStandardRAWMagic(raw); ok {
		opts = append(opts, exif.AcceptRAWMagic(magic))
	}
	e, err := exif.Parse(raw, opts...)
	if err != nil {
		return &ParseSegmentError{Segment: "EXIF", Err: err}
	}
	m.EXIF = e
	// Surface EXIF parser warnings as ParseWarnings at the top-level Metadata
	// API. Each warning string is wrapped in a *ParseSegmentError so it is
	// consistently typed and inspectable via errors.As.
	for _, w := range e.Warnings {
		m.ParseWarnings = append(m.ParseWarnings, &ParseSegmentError{
			Segment: "EXIF",
			Err:     fmt.Errorf("%s", w), //nolint:err113 // warning message is already descriptive; dynamic string wrapping is intentional here
		})
	}
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
//
// noCMT1Box is true when a CR3 file has a valid moov/UUID structure but no
// CMT1 sub-box (audit #138). rawEXIF is nil; rawXMP is still returned when
// an "XMP " sub-box was present. The caller converts it to a ParseWarning.
//
// tiffFamilyRawEXIFIsWholeFile reports whether rawEXIF, as just extracted for
// one of the five TIFF-family formats (TIFF, CR2, NEF, ARW, DNG — the ones
// where the TIFF byte stream is itself the EXIF container), happens to equal
// the ENTIRE source file rather than just format/tiff.Extract's metadata
// prefix (#289). This can legitimately happen: the extent scanner's own
// small-file whole-read bypass or large-fraction snap (see
// format/tiff/extent.go) converges on the whole file for some inputs.
//
// For every other format, this always returns false without touching r: the
// two extra Seek calls below are paid only by the five formats that can ever
// benefit from the answer.
//
// r's position is restored to exactly what it was when this function was
// called, so it never affects Read's own behaviour or any later use of r —
// any failure restoring it is treated as "answer unknown" (false), never as
// a fatal error: worst case, Write falls back to re-reading the source
// itself, which is always correct, just not maximally fast.
func tiffFamilyRawEXIFIsWholeFile(r io.ReadSeeker, fmtID format.FormatID, rawEXIF []byte) bool {
	switch fmtID {
	case format.FormatTIFF, format.FormatCR2, format.FormatNEF, format.FormatARW, format.FormatDNG:
	default:
		return false
	}
	if rawEXIF == nil {
		return false
	}
	cur, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return false
	}
	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return false
	}
	if _, err := r.Seek(cur, io.SeekStart); err != nil {
		return false
	}
	return int64(len(rawEXIF)) == end
}

// wantIPTC and wantXMP (#238) are forwarded to jpeg.ExtractFullSelective so
// the JPEG extractor can skip the 0x0425 IPTC digest and/or the extended-XMP
// reassembly when the caller has opted out of that segment. Other formats do
// not yet have an equivalent selective-extraction path (their raw-segment
// extraction has no comparable reassembly cost) and are unaffected.
func extractByFormat(r io.ReadSeeker, fmtID format.FormatID, wantIPTC, wantXMP bool) (rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire []byte, xmpTruncated, noCMT1Box bool, err error) {
	if fmtID == format.FormatJPEG {
		rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire, xmpTruncated, err = jpeg.ExtractFullSelective(r, wantIPTC, wantXMP)
		if err != nil {
			return nil, nil, nil, nil, nil, false, false, fmt.Errorf("gometadata: %w", err)
		}
		return rawEXIF, rawIPTC, rawIPTCDigest, rawXMP, rawXMPWire, xmpTruncated, false, nil
	}
	fn, ok := extractors[fmtID]
	if !ok {
		return nil, nil, nil, nil, nil, false, false, &UnsupportedFormatError{}
	}
	rawEXIF, rawIPTC, rawXMP, err = fn(r)
	if err != nil {
		// audit #138: cr3.ErrNoCMT1Box is a non-fatal partial-success: the
		// moov/UUID structure is valid but CMT1 is absent. rawXMP is still
		// populated when an "XMP " sub-box exists. Surface as ParseWarning
		// rather than aborting; return noCMT1Box=true so Read records the warning.
		if errors.Is(err, cr3.ErrNoCMT1Box) {
			return nil, rawIPTC, nil, rawXMP, nil, false, true, nil
		}
		return nil, nil, nil, nil, nil, false, false, fmt.Errorf("gometadata: %w", err)
	}
	return rawEXIF, rawIPTC, nil, rawXMP, nil, false, false, nil
}
