package gometadata

// ReadOption configures a Read or ReadFile call.
type ReadOption func(*readConfig)

type readConfig struct {
	// lazyXMP skips XMP parsing (useful when only EXIF/IPTC are needed).
	lazyXMP bool
	// lazyIPTC skips IPTC parsing.
	lazyIPTC bool
	// lazyEXIF skips EXIF parsing.
	lazyEXIF bool
	// skipMakerNote skips MakerNote IFD parsing inside EXIF.
	skipMakerNote bool
	// strict causes Read to return a ParseSegmentError immediately when a
	// segment is present but fails to parse, instead of silently skipping it.
	strict bool
}

// WithoutXMP skips XMP parsing, reducing allocations when XMP is not needed.
func WithoutXMP() ReadOption { return func(c *readConfig) { c.lazyXMP = true } }

// WithoutIPTC skips IPTC parsing.
func WithoutIPTC() ReadOption { return func(c *readConfig) { c.lazyIPTC = true } }

// WithoutEXIF skips EXIF parsing.
func WithoutEXIF() ReadOption { return func(c *readConfig) { c.lazyEXIF = true } }

// WithoutMakerNote skips manufacturer-specific MakerNote IFD parsing.
// The raw MakerNote bytes are still retained for round-trip writes; only the
// decoded EXIF.MakerNoteIFD field is omitted. Use this when extension tags
// are not needed and you want to minimise parse latency on camera RAW files.
func WithoutMakerNote() ReadOption { return func(c *readConfig) { c.skipMakerNote = true } }

// Strict enables strict parse mode. In strict mode, Read returns a
// *ParseSegmentError immediately if a metadata segment is present in the
// container (raw bytes were successfully extracted) but the format parser
// fails to decode it. The error identifies the failing segment ("EXIF",
// "IPTC", or "XMP") and wraps the underlying parser error.
//
// Default behaviour (without Strict) is best-effort: parse failures are
// non-fatal. The segment is left nil on the returned Metadata and the failure
// is recorded in Metadata.ParseWarnings so callers can inspect it later
// without aborting.
//
// Lazy options (WithoutEXIF, WithoutIPTC, WithoutXMP) take precedence over
// Strict: a lazy segment is never parsed and therefore never fails.
func Strict() ReadOption { return func(c *readConfig) { c.strict = true } }

// WriteOption configures a Write or WriteFile call.
type WriteOption func(*writeConfig)

type writeConfig struct {
	// preserveUnknownSegments retains APP segments not understood by this library.
	preserveUnknownSegments bool
}

// PreserveUnknownSegments controls whether segments that this library does not
// recognise as metadata are passed through unchanged.
//
// When true (the default), all non-metadata segments are copied verbatim to the
// output. This is the safe default and is byte-identical to the output of
// previous releases.
//
// When false:
//   - JPEG: APPn segments (APP0–APP15) that are not one of the three recognised
//     metadata segments (EXIF APP1, XMP APP1, Photoshop APP13) are stripped from
//     the output. Structural markers (SOF, DQT, DHT), SOS, and compressed image
//     data are never affected. Use this to remove unknown application payloads
//     that may carry sensitive data (e.g. GPS embedded in a proprietary APPn).
//   - PNG, WebP, HEIF/AVIF, CR3: returns an error wrapping
//     ErrPreserveUnknownSegmentsNotSupported for that format, because those
//     containers do not have an equivalent concept of "unknown optional segment"
//     — every non-metadata chunk or box is required for correct decoding.
//   - TIFF-based formats (TIFF, DNG, CR2, NEF, ARW, ORF, RW2): the dedicated
//     write paths for these formats do not use the injectors map and therefore
//     do not consult preserveUnknownSegments; image data is always preserved
//     byte-identically via the copy-and-relocate algorithm.
//
// Default: true.
func PreserveUnknownSegments(v bool) WriteOption {
	return func(c *writeConfig) { c.preserveUnknownSegments = v }
}
