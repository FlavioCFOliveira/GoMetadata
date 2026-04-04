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

// WriteOption configures a Write or WriteFile call.
type WriteOption func(*writeConfig)

type writeConfig struct {
	// preserveUnknownSegments retains APP segments not understood by this library.
	preserveUnknownSegments bool
}

// PreserveUnknownSegments keeps APP or chunk segments that this library does
// not recognise, passing them through unchanged. Default: true.
func PreserveUnknownSegments(v bool) WriteOption {
	return func(c *writeConfig) { c.preserveUnknownSegments = v }
}
