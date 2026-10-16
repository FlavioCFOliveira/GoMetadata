package gometadata

import (
	"bytes"
	"encoding/binary"
	"sync"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/internal/reconcile"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// NewMetadata returns an empty Metadata ready for writing to a file of the
// given format. All metadata fields are nil; use the convenience Set* methods
// to populate them — the Set* methods auto-create any required component when
// nil and the format supports it (task #88 AUTO-CREATE policy). Alternatively,
// assign m.EXIF, m.IPTC, or m.XMP directly before passing m to Write or
// WriteFile.
//
// FormatUnknown contract: when fmtID is FormatUnknown, the returned Metadata
// is valid for inspection but not for writing. Calling Set* on an Unknown-
// format Metadata is a documented no-op — no component is auto-created and
// the value is not stored, because the library cannot determine which metadata
// types the container supports. Write will return UnsupportedFormatError.
// If you intend to write metadata to a new file, always pass a concrete
// format ID.
func NewMetadata(fmtID format.FormatID) *Metadata {
	return &Metadata{format: uint8(fmtID)}
}

// Metadata holds all metadata extracted from an image.
// Any of the three embedded pointers may be nil if that metadata
// type was not present in the image.
//
// The convenience methods below read and write the fields that EXIF, IPTC
// and XMP share through one mapping table (package internal/reconcile,
// transcribed from the Adobe XMP Toolkit and the Metadata Working Group
// Guidelines v2.0, "MWG"). When the same field exists in more than one
// metadata type, the getters apply this policy:
//   - Camera data (model, make, lens, settings): EXIF wins, then XMP.
//   - Caption, copyright, creator and the capture date (MWG §4.2.3.3): EXIF
//     wins, falling back to XMP, then IPTC. Keywords, which EXIF does not
//     carry (MWG §5.1): XMP wins, falling back to IPTC.
//   - When the Photoshop IPTC digest shows that the IPTC was edited after the
//     XMP was written, an IPTC value wins over EXIF and XMP unless it is only
//     the XMP value truncated to the IPTC length limit (MWG §4.2.3.2 and
//     §4.2.3.3).
//   - A value made only of spaces or NUL characters counts as absent
//     (MWG §4.2.3.3).
//
// Each setter writes every form of its field that the format can carry, so
// that one call leaves EXIF, IPTC and XMP consistent. An XMP copy of an EXIF
// camera setting (for example tiff:Orientation) is updated when the XMP
// already has it, and never created (MWG §4.2.3.1).
//
// Concurrency contract:
//
//	Concurrent Set* calls on the same *Metadata are safe — they are
//	serialised by an internal mutex.
//	Concurrent Get* / Read calls on the same *Metadata (with no concurrent
//	Set* in flight) are also safe — getters are read-only.
//	Concurrent Set* and Get* / Write on the same *Metadata without external
//	synchronisation may see partially-updated state; callers that mix reads
//	and writes concurrently must provide their own synchronisation.
//
//	The mutex above guards Metadata's own Set* methods only. The embedded
//	EXIF, IPTC, and XMP objects are NOT independently safe for concurrent
//	mutation: calling m.EXIF.SetOrientation, m.IPTC.SetKeywords, or
//	m.XMP.SetCaption directly from multiple goroutines bypasses mu entirely
//	and races on the underlying IFD entry slices, dataset slices, or the XMP
//	Properties map (the latter can trigger "fatal error: concurrent map
//	writes", which the Go runtime cannot recover from). Mutate metadata either
//	through Metadata's own Set* methods (which serialise on mu), or hold your
//	own lock around any direct m.EXIF / m.IPTC / m.XMP mutation. See the
//	Thread-safety notes on exif.EXIF, iptc.IPTC, and xmp.XMP for the same
//	contract stated at the component level.
//
// Copy semantics:
//
//	Metadata contains a sync.Mutex and must not be copied by value after
//	first use. Always pass and store *Metadata (pointer), never Metadata.
type Metadata struct {
	// mu serialises concurrent Set* calls (finding #128).
	// Must not be copied after first use — always use *Metadata.
	mu sync.Mutex

	// EXIF, IPTC and XMP are the parsed metadata, each nil when absent. They
	// are not safe for concurrent mutation (see the concurrency contract
	// above).
	//
	// The entry values of EXIF (exif.IFDEntry.Value, EXIF.MakerNote) are
	// zero-copy views of the raw EXIF bytes that Read retains and that Write
	// uses as the source of everything it copies from the original file
	// (task #296). Treat them as read-only: change a value with the Set*
	// methods, or by assigning a new slice to IFDEntry.Value (and updating
	// IFDEntry.Count); never write into the bytes of an existing Value.
	EXIF *exif.EXIF
	IPTC *iptc.IPTC
	XMP  *xmp.XMP

	// ParseWarnings contains one entry for every metadata segment that was
	// present in the container but failed to parse. It is populated only in
	// best-effort mode (the default); when Strict() is active Read returns an
	// error immediately on the first parse failure and ParseWarnings is never
	// consulted. ParseWarnings is nil when all segments parsed successfully or
	// when no metadata is present.
	//
	// A non-nil ParseWarnings does not prevent the returned Metadata from being
	// used; the caller simply receives fewer fields than the raw container held.
	ParseWarnings []*ParseSegmentError

	// unexported: detected container format and original raw segments
	// retained so round-trip writes can reconstruct the file correctly.
	format  uint8
	rawEXIF []byte
	rawIPTC []byte
	rawXMP  []byte

	// rawEXIFIsWholeFile is true when rawEXIF, as extracted by Read, happens
	// to equal the ENTIRE source file rather than just a metadata prefix.
	// It is only ever set for the seven TIFF-family formats where the TIFF
	// byte stream is itself the EXIF container (TIFF, CR2, NEF, ARW, DNG,
	// ORF, RW2 — ORF/RW2 added by #293, which routed them through the same
	// scanner TIFF/CR2/NEF/ARW/DNG already used): format/tiff.Extract's
	// metadata-prefix scanner (#289) can converge on reading the whole file
	// for some inputs (a small file below its own whole-read threshold, or
	// one whose declared metadata legitimately spans a large fraction of
	// it), in which case rawEXIF is exactly as complete as a pre-#289/#293
	// whole-file read would have been. Computed once, cheaply, by comparing
	// len(rawEXIF) against the source's actual size (see
	// tiffFamilyRawEXIFIsWholeFile in read.go) — never assumed.
	//
	// Write's copy-and-relocate path (writeTIFF/writeTIFFCR2/writeTIFFARW/
	// writeTIFFNEF/writeTIFFORF/writeTIFFRW2) checks this flag to decide
	// whether it can reuse rawEXIF directly as its relocation base instead
	// of re-reading the full source from r: reusing is exactly as safe as
	// it was before #289/#293 removed the
	// general-case fast path, since no relocator ever mutates its base
	// buffer (see Batch E's finding recorded in project agent memory) —
	// false only means "re-read to be safe", never "this write is unsafe".
	rawEXIFIsWholeFile bool

	// rawIPTCDigest is the 16-byte MD5 value stored in Photoshop resource
	// 0x0425 ("IPTC Digest") inside the APP13 IRB, or nil when absent.
	// MWG Guidelines v2.0 §4.2.3.2: the digest is used at read time to determine
	// whether an IPTC value takes precedence over EXIF and XMP for conflicting
	// descriptive fields.
	//
	// nil  → digest resource absent; default EXIF > XMP > IPTC priority applies.
	// 16 B = all-zero → "unknown" sentinel; IPTC trust is elevated.
	// 16 B, non-zero  → compare to MD5(rawIPTC); match → default priority;
	//                   mismatch → IPTC trust is elevated.
	rawIPTCDigest []byte

	// rawXMPWire is non-nil when a JPEG carried extended XMP (Adobe XMP
	// Specification Part 3 §1.1.3.1). It holds the internal wire-frame
	// encoding produced by jpeg.ExtractFullSelective: the original standard
	// APP1 content and the assembled extended payload packed together.
	//
	// Write uses it only when m.XMP is nil (XMP not parsed, for example with
	// WithoutXMP): Inject then reproduces the original segmentation with the
	// original GUID. When m.XMP is non-nil, Write encodes m.XMP instead, and
	// a packet too large for one APP1 segment is split with a GUID derived
	// from the MD5 digest of the extended serialisation (task #295), so the
	// same XMP always produces the same bytes.
	rawXMPWire []byte

	// xmpOpaque is set by Read when the container carried an XMP packet that
	// the caller cannot edit through the model, because it is not well-formed
	// XML and so could not be parsed (the reason is in ParseWarnings; task
	// #327) or because the caller opted out of parsing it with WithoutXMP
	// (task #343). Write then carries rawXMP to the output byte for byte, and
	// the setters create no XMP of their own, because encoding a fresh XMP
	// from the setters' values would replace the packet and lose everything it
	// held. A caller that wants to replace the packet assigns m.XMP
	// explicitly.
	xmpOpaque bool

	// exifOpaque and iptcOpaque are the same guard for the EXIF and IPTC the
	// caller opted out of parsing with WithoutEXIF and WithoutIPTC (task
	// #343): the setters create no EXIF or IPTC of their own, which would
	// replace the original and lose everything it held, and Write carries the
	// original bytes.
	exifOpaque, iptcOpaque bool

	// iptcTrustElev caches the result of computeIPTCTrustElevated(rawIPTC,
	// rawIPTCDigest), computed once by Read (see read.go) immediately after
	// rawIPTC and rawIPTCDigest are known. iptcTrustElevated() below reads
	// this field directly instead of recomputing an MD5 digest of rawIPTC on
	// every call (task #208).
	//
	// Correctness: rawIPTC and rawIPTCDigest are set exactly once, at
	// construction (either by Read's struct literal or left at their nil zero
	// value by NewMetadata / a caller-built literal), and are never
	// reassigned afterward — grep confirms the only production write sites
	// are the Read() struct literal. Set* methods mutate m.IPTC (the parsed
	// *iptc.IPTC), never m.rawIPTC, so the cached decision remains valid for
	// the entire lifetime of a *Metadata. For any *Metadata not constructed
	// via Read (NewMetadata, or a struct literal in tests), rawIPTCDigest is
	// nil and iptcTrustElev correctly keeps its zero value (false), matching
	// computeIPTCTrustElevated's own nil-digest fast path exactly.
	iptcTrustElev bool
}

// Format returns the detected container format ID of the image.
func (m *Metadata) Format() format.FormatID { return format.FormatID(m.format) }

// RawEXIF returns a copy of the raw EXIF segment bytes as read from the container.
//
// For TIFF, CR2, NEF, ARW, DNG, ORF, and RW2 — formats where the TIFF byte
// stream is itself the EXIF container — RawEXIF returns only the METADATA
// PREFIX of that stream (#289 for TIFF/CR2/NEF/ARW/DNG; #293 for ORF/RW2):
// every IFD in IFD0's next-IFD chain, ExifIFD, GPSIFD, InteropIFD, and
// SubIFDs, and every out-of-line tag value within them (including MakerNote
// blobs, and the IPTC/XMP tag payloads RawIPTC/RawXMP also read from) — not
// strip/tile image-data blocks or embedded thumbnails/previews. This is a
// byte-for-byte prefix of the original file starting at offset 0, sized to
// whatever the file's own metadata occupies (typically a small fraction of
// a percent of real-world files); it is sufficient to reconstruct every
// field exif.Parse would report from the whole file, but it is NOT the
// whole file. A caller that needs the original bytes verbatim — for example
// to re-embed a thumbnail this package never reads — must keep its own copy
// of the source rather than relying on RawEXIF.
//
// For every other supported format (JPEG, PNG, WebP, HEIF/AVIF, CR3),
// RawEXIF already returned just the EXIF segment/box/chunk rather than the
// whole file, so this changes nothing for those formats.
//
// #139: returns bytes.Clone(m.rawEXIF) so that caller mutations of the returned
// slice cannot corrupt the internal relocation base used by subsequent Write calls.
// The raw EXIF bytes in TIFF-based formats share their backing array with every
// parsed IFDEntry.Value; an in-place mutation by the caller would silently corrupt
// all parsed EXIF values and, for any of these seven formats whose extent scan
// happened to read the whole file for this particular input (see
// rawEXIFIsWholeFile's own doc comment), the image-data source used during write
// relocation too.
func (m *Metadata) RawEXIF() []byte { return bytes.Clone(m.rawEXIF) }

// RawIPTC returns a copy of the raw IPTC IIM segment bytes as read from the container.
//
// #139: returns bytes.Clone(m.rawIPTC) for the same defensive-copy rationale as RawEXIF.
func (m *Metadata) RawIPTC() []byte { return bytes.Clone(m.rawIPTC) }

// RawXMP returns a copy of the raw XMP packet bytes as read from the container.
// When the image carried extended XMP, RawXMP returns the fully reassembled
// (merged) packet so callers always receive a single, self-contained document.
//
// #139: returns bytes.Clone(m.rawXMP) for the same defensive-copy rationale as RawEXIF.
func (m *Metadata) RawXMP() []byte { return bytes.Clone(m.rawXMP) }

// RawSegments returns read-only, zero-copy views of the raw EXIF, IPTC, and
// XMP segment bytes exactly as stored on m. Any of the three may be nil when
// that segment is absent from the container.
//
// Unlike RawEXIF, RawIPTC, and RawXMP, the returned slices alias m's own
// internal storage and perform no allocation. The caller MUST NOT modify,
// append to, or retain them past any subsequent mutation of m (a Set* call,
// or passing m to Write) — doing so is undefined behaviour and may corrupt
// m's internal relocation state or a concurrent Write call (see the #139
// mutation-safety rationale on RawEXIF).
//
// Use this accessor for read-only, allocation-sensitive callers (e.g.
// hashing or comparing raw bytes across many images in a loop). Use RawEXIF,
// RawIPTC, and RawXMP when you need an owned copy safe to mutate or retain
// independently of m.
func (m *Metadata) RawSegments() (rawEXIF, rawIPTC, rawXMP []byte) {
	return m.rawEXIF, m.rawIPTC, m.rawXMP
}

// iptcTrustElevated reports whether an edited IPTC value should take read
// priority over EXIF and XMP (MWG §4.2.3.2 and §4.2.3.3).
//
// The decision is computed once, by Read, and cached in m.iptcTrustElev
// (task #208); this method is a plain field read so that the MWG-02
// accessors (Copyright, Caption, Creator, Keywords, and the IPTC date fallback
// of DateTimeOriginal) never each pay their own MD5 hash of the raw IPTC
// stream. See computeIPTCTrustElevated for the full
// MWG §4.2.3.2 policy this value encodes.
func (m *Metadata) iptcTrustElevated() bool {
	return m.iptcTrustElev
}

// digestMatchFn is a seam over iptc.DigestMatch. computeIPTCTrustElevated
// calls it instead of iptc.DigestMatch directly so that tests can verify how
// many times the underlying MD5 computation runs (task #208). Production
// behaviour is unchanged: the var is initialised to iptc.DigestMatch itself
// and is never reassigned outside test code.
var digestMatchFn = iptc.DigestMatch //nolint:gochecknoglobals // test seam; identical to calling iptc.DigestMatch directly in production

// computeIPTCTrustElevated reports whether an edited IPTC value should take
// read priority over EXIF and XMP (MWG §4.2.3.2 and §4.2.3.3).
//
// MWG Guidelines v2.0 §4.2.3.2: the Photoshop resource 0x0425 stores an MD5
// digest of the raw 0x0404 IIM block at the time XMP was last written. If the
// digest matches the current IIM block, XMP was written after the last IPTC
// edit, so the default MWG-01 priority (EXIF > XMP > IPTC) applies. If the
// digest mismatches, or if the stored digest is the all-zero "unknown"
// sentinel, IPTC may have been edited independently of XMP, so IPTC trust is
// elevated for conflicting fields.
//
// When rawIPTCDigest is nil (the resource was absent), the default MWG-01
// priority is preserved unchanged.
//
// Called exactly once, by Read, immediately after rawIPTC and rawIPTCDigest
// are final; the result is cached in m.iptcTrustElev (task #208). This
// function performs at most one MD5 computation (via digestMatchFn), unlike
// the former per-accessor-call implementation, which hashed the entire raw
// IPTC stream again on every single Copyright/Caption/Keywords/Creator call.
func computeIPTCTrustElevated(rawIPTC, rawIPTCDigest []byte) bool {
	if len(rawIPTCDigest) != 16 {
		// No digest resource in the IRB — use the default MWG-01 priority.
		return false
	}
	var stored [16]byte
	copy(stored[:], rawIPTCDigest)
	// digestMatchFn handles the all-zero sentinel case (returns unknown=true).
	match, unknown := digestMatchFn(rawIPTC, stored)
	// Elevate IPTC when: all-zero sentinel OR computed hash ≠ stored hash.
	return unknown || !match
}

// Validate checks that m is in a consistent state suitable for writing.
// It returns a descriptive error when an obvious inconsistency is detected
// (e.g. unknown format, EXIF struct without IFD0). Write calls Validate
// automatically; callers may call it earlier for better error messages.
func (m *Metadata) Validate() error {
	if format.FormatID(m.format) == format.FormatUnknown {
		return &UnsupportedFormatError{}
	}
	if m.EXIF != nil && m.EXIF.IFD0 == nil {
		return ErrNilIFD0
	}
	if m.XMP != nil && m.XMP.Properties == nil {
		return ErrNilXMPProperties
	}
	return nil
}

// CameraModel returns the camera model string.
// Source priority: EXIF > XMP.
func (m *Metadata) CameraModel() string {
	if m.EXIF != nil {
		if v := m.EXIF.CameraModel(); v != "" {
			return v
		}
	}
	if m.XMP != nil {
		return m.XMP.CameraModel()
	}
	return ""
}

// GPS returns the GPS coordinates in decimal degrees (WGS-84).
// ok is false when no GPS data is present.
// Source priority: EXIF > XMP.
func (m *Metadata) GPS() (float64, float64, bool) {
	if m.EXIF != nil {
		if lat, lon, ok := m.EXIF.GPS(); ok {
			return lat, lon, true
		}
	}
	if m.XMP != nil {
		return m.XMP.GPS()
	}
	return 0, 0, false
}

// Copyright returns the copyright notice.
// Source priority (MWG §4.2.3.3, §5.6): EXIF > XMP > IPTC. When the Photoshop
// IPTC digest does not match, an edited IPTC value wins over both (see
// resolveText); a blank value counts as absent.
func (m *Metadata) Copyright() string {
	return m.resolveText(reconcile.Copyright)
}

// Caption returns the image description / caption.
// Source priority (MWG §4.2.3.3, §5.2): EXIF ImageDescription > XMP
// dc:description > IPTC Caption/Abstract. When the Photoshop IPTC digest does
// not match, an edited IPTC value wins over both (see resolveText); a blank
// value counts as absent.
func (m *Metadata) Caption() string {
	return m.resolveText(reconcile.Description)
}

// xmpDateLayouts lists the ISO 8601 time formats used by XMP DateTimeOriginal,
// tried in order from most specific (timezone offset) to least specific (local).
var xmpDateLayouts = [3]string{ //nolint:gochecknoglobals // constant time layouts
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
}

// DateTimeOriginal returns the original capture date/time.
// Source priority (MWG §4.2.3.3, §5.3): EXIF DateTimeOriginal, then XMP
// exif:DateTimeOriginal, then XMP photoshop:DateCreated, then IPTC 2:55 Date
// Created with 2:60 Time Created. When the Photoshop IPTC digest does not
// match and the IPTC date differs from the one predicted from
// photoshop:DateCreated, the IPTC date wins over all of them (the digest rule
// of Caption).
//
// The EXIF value includes the fraction of a second of SubSecTimeOriginal
// (tag 0x9291) and the offset of OffsetTimeOriginal (tag 0x9011). MWG timezone
// synthesis: when EXIF carries DateTimeOriginal without OffsetTimeOriginal,
// the method checks whether XMP exif:DateTimeOriginal carries a timezone
// offset (e.g. "+02:00") and, if so, applies it to the EXIF wall-clock
// date/time, so that a fully-qualified timestamp is reconstructed from split
// EXIF+XMP metadata (MWG §4.2.3.4, "Time-zone handling": the zone is taken
// from the same field, never added arbitrarily).
//
// When no timezone is available, the returned time.Time uses UTC.
func (m *Metadata) DateTimeOriginal() (time.Time, bool) {
	if t, ok := m.editedIIMDate(); ok {
		return t, true
	}
	if m.EXIF != nil {
		if t, ok := m.EXIF.DateTimeOriginal(); ok {
			return m.exifSubSecOriginal(m.applyXMPTimezone(t)), true
		}
	}
	if m.XMP != nil {
		if t, ok := parseXMPDateTimeOriginal(m.XMP.DateTimeOriginal()); ok {
			return t, true
		}
	}
	return m.resolveDateCreated()
}

// applyXMPTimezone applies MWG §4.2.3.4 timezone synthesis to t: when EXIF
// ExifIFD lacks OffsetTimeOriginal and XMP carries an explicit UTC offset, the
// wall-clock time from EXIF is re-expressed in the XMP timezone. t is returned
// unchanged when the EXIF offset is present, when XMP is absent, or when the
// XMP date string carries no timezone offset.
func (m *Metadata) applyXMPTimezone(t time.Time) time.Time {
	if m.EXIF == nil || m.EXIF.ExifIFD == nil {
		return t
	}
	if m.EXIF.ExifIFD.Get(exif.TagOffsetTimeOriginal) != nil {
		// EXIF already supplied an explicit offset — do not override it.
		return t
	}
	if m.XMP == nil {
		return t
	}
	loc := xmpTimezone(m.XMP.DateTimeOriginal())
	if loc == nil {
		return t
	}
	// Reconstruct the same wall-clock time in the synthesised timezone.
	// MWG §4.2.3.4: the EXIF wall-clock digits are authoritative; only the
	// timezone is taken from XMP.
	return time.Date(t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}

// xmpTimezone extracts a *time.Location from an XMP ISO 8601 date string when
// it carries an explicit timezone offset (e.g. "2024-06-15T10:30:00+02:00").
// Returns nil when the string has no offset, is empty, or uses the "Z" suffix
// (which would confirm UTC — not useful for synthesis on top of an existing
// UTC result that came from a no-offset EXIF tag).
//
// Only the "+HH:MM" / "-HH:MM" suffix form triggers synthesis; "Z" and
// offset-free strings are treated as "no information available".
func xmpTimezone(xmpDate string) *time.Location {
	if len(xmpDate) < len("2006-01-02T15:04:05+07:00") {
		return nil
	}
	// A "+"/"-" at position 19 is the start of a UTC offset (+HH:MM or -HH:MM).
	sign := xmpDate[len(xmpDate)-6]
	if sign != '+' && sign != '-' {
		// No explicit offset (no-TZ or "Z" suffix) — nothing to contribute.
		return nil
	}
	tzStr := xmpDate[len(xmpDate)-6:]
	t, err := time.Parse("-07:00", tzStr)
	if err != nil {
		return nil
	}
	_, offset := t.Zone()
	return time.FixedZone(tzStr, offset)
}

// ExposureTime returns the exposure time as [numerator, denominator] in seconds.
// Source: EXIF only (no equivalent in XMP/IPTC at this level).
func (m *Metadata) ExposureTime() (num, den uint32, ok bool) {
	if m.EXIF != nil {
		return m.EXIF.ExposureTime()
	}
	return 0, 0, false
}

// FNumber returns the F-number (aperture).
// Source: EXIF only.
func (m *Metadata) FNumber() (float64, bool) {
	if m.EXIF != nil {
		return m.EXIF.FNumber()
	}
	return 0, false
}

// ISO returns the ISO speed rating.
// Source: EXIF only.
func (m *Metadata) ISO() (uint, bool) {
	if m.EXIF != nil {
		return m.EXIF.ISO()
	}
	return 0, false
}

// FocalLength returns the focal length in millimetres.
// Source: EXIF only.
func (m *Metadata) FocalLength() (float64, bool) {
	if m.EXIF != nil {
		return m.EXIF.FocalLength()
	}
	return 0, false
}

// LensModel returns the lens model string.
// Source priority: EXIF (tag 0xA434) > XMP aux:Lens > XMP exifEX:LensModel
// (the XMP Toolkit mapping of tag 0xA434).
func (m *Metadata) LensModel() string {
	if m.EXIF != nil {
		if v := m.EXIF.LensModel(); v != "" {
			return v
		}
	}
	if m.XMP == nil {
		return ""
	}
	if v := m.XMP.LensModel(); v != "" {
		return v
	}
	p := &reconcile.Lookup(reconcile.LensModel).XMP[1]
	return m.XMP.Get(p.NS, p.Name)
}

// Orientation returns the image orientation (1–8 per EXIF §4.6.4).
// Source: EXIF only.
func (m *Metadata) Orientation() (uint16, bool) {
	if m.EXIF != nil {
		return m.EXIF.Orientation()
	}
	return 0, false
}

// ImageSize returns the pixel dimensions of the full-resolution image.
// Source: EXIF only (PixelXDimension / PixelYDimension, EXIF §4.6.5).
func (m *Metadata) ImageSize() (width, height uint32, ok bool) {
	if m.EXIF != nil {
		return m.EXIF.ImageSize()
	}
	return 0, 0, false
}

// Keywords returns the subject keywords.
// Source priority: XMP > IPTC (MWG §5.1). When the Photoshop IPTC digest
// shows that the IPTC was edited after the XMP was written, the IPTC keywords
// win unless each of them is only the XMP keyword truncated to the IPTC
// length limit (MWG §4.2.3.2).
func (m *Metadata) Keywords() []string {
	return m.resolveKeywords()
}

// Make returns the camera manufacturer string.
// Source priority: EXIF > XMP (tiff:Make).
func (m *Metadata) Make() string {
	if m.EXIF != nil && m.EXIF.IFD0 != nil {
		if e := m.EXIF.IFD0.Get(exif.TagMake); e != nil {
			if v := e.String(); v != "" {
				return v
			}
		}
	}
	if m.XMP != nil {
		return m.XMP.Get(xmp.NStiff, "Make")
	}
	return ""
}

// Software returns the software / firmware string used to produce the image.
// Source priority: EXIF > XMP (xmp:CreatorTool).
func (m *Metadata) Software() string {
	if m.EXIF != nil && m.EXIF.IFD0 != nil {
		if e := m.EXIF.IFD0.Get(exif.TagSoftware); e != nil {
			if v := e.String(); v != "" {
				return v
			}
		}
	}
	if m.XMP != nil {
		return m.XMP.Get(xmp.NSxmp, "CreatorTool")
	}
	return ""
}

// DateTime returns the general date/time the image was last changed (IFD0 DateTime).
// Source: EXIF only (tag 0x0132). ok is false when not present.
func (m *Metadata) DateTime() (time.Time, bool) {
	if m.EXIF == nil || m.EXIF.IFD0 == nil {
		return time.Time{}, false
	}
	e := m.EXIF.IFD0.Get(exif.TagDateTime)
	if e == nil {
		return time.Time{}, false
	}
	v := e.String()
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006:01:02 15:04:05", v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// WhiteBalance returns the white balance mode from ExifIFD tag 0xA403.
// 0 = auto, 1 = manual (EXIF §4.6.5). ok is false when not present.
func (m *Metadata) WhiteBalance() (uint16, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagWhiteBalance); e != nil {
			return e.Uint16(), true
		}
	}
	return 0, false
}

// Flash returns the flash status from ExifIFD tag 0x9209.
// Bit 0 = flash fired; see EXIF §4.6.5 for full bitmask meaning.
// ok is false when not present.
func (m *Metadata) Flash() (uint16, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagFlash); e != nil {
			return e.Uint16(), true
		}
	}
	return 0, false
}

// ExposureMode returns the exposure mode from ExifIFD tag 0xA402.
// 0 = auto, 1 = manual, 2 = auto bracket (EXIF §4.6.5). ok is false when not present.
func (m *Metadata) ExposureMode() (uint16, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagExposureMode); e != nil {
			return e.Uint16(), true
		}
	}
	return 0, false
}

// Altitude returns the GPS altitude in metres above (positive) or below
// (negative) sea level from the GPS IFD (EXIF §4.6.6 tags 0x0005/0x0006).
// ok is false when not present.
func (m *Metadata) Altitude() (float64, bool) {
	if m.EXIF == nil || m.EXIF.GPSIFD == nil {
		return 0, false
	}
	altEntry := m.EXIF.GPSIFD.Get(exif.TagGPSAltitude)
	if altEntry == nil {
		return 0, false
	}
	r := altEntry.Rational(0)
	if r[1] == 0 {
		return 0, false
	}
	alt := float64(r[0]) / float64(r[1])
	if ref := m.EXIF.GPSIFD.Get(exif.TagGPSAltitudeRef); ref != nil && len(ref.Value) > 0 && ref.Value[0] == 1 {
		alt = -alt
	}
	return alt, true
}

// SubjectDistance returns the distance to the subject in metres from ExifIFD
// tag 0x9206. ok is false when not present or denominator is zero.
func (m *Metadata) SubjectDistance() (float64, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagSubjectDistance); e != nil {
			r := e.Rational(0)
			if r[1] != 0 {
				return float64(r[0]) / float64(r[1]), true
			}
		}
	}
	return 0, false
}

// DigitalZoomRatio returns the digital zoom ratio from ExifIFD tag 0xA404.
// 0 = not used. ok is false when not present.
func (m *Metadata) DigitalZoomRatio() (float64, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagDigitalZoomRatio); e != nil {
			r := e.Rational(0)
			if r[1] != 0 {
				return float64(r[0]) / float64(r[1]), true
			}
		}
	}
	return 0, false
}

// SceneType returns the scene capture type byte from ExifIFD tag 0xA301.
// 0 = directly photographed (EXIF §4.6.5). ok is false when not present.
func (m *Metadata) SceneType() (byte, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagSceneType); e != nil && len(e.Value) > 0 {
			return e.Value[0], true
		}
	}
	return 0, false
}

// ColorSpace returns the colour space from ExifIFD tag 0xA001.
// 1 = sRGB, 0xFFFF = uncalibrated (EXIF §4.6.5). ok is false when not present.
func (m *Metadata) ColorSpace() (uint16, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagColorSpace); e != nil {
			return e.Uint16(), true
		}
	}
	return 0, false
}

// MeteringMode returns the metering mode from ExifIFD tag 0x9207 (EXIF §4.6.5).
// ok is false when not present.
func (m *Metadata) MeteringMode() (uint16, bool) {
	if m.EXIF != nil && m.EXIF.ExifIFD != nil {
		if e := m.EXIF.ExifIFD.Get(exif.TagMeteringMode); e != nil {
			return e.Uint16(), true
		}
	}
	return 0, false
}

// Creator returns the author / creator name: the first creator of the list.
// Source priority (MWG §4.2.3.3, §5.7): EXIF Artist > XMP dc:creator > IPTC
// By-line. When the Photoshop IPTC digest does not match, an edited IPTC value
// wins over both (see resolveText); a blank value counts as absent. The EXIF
// Artist tag holds the list as one string; its first item is returned
// (MWG §5.7).
func (m *Metadata) Creator() string {
	return m.resolveText(reconcile.Creator)
}

// ---------------------------------------------------------------------------
// Auto-create helpers — task #88 (AUTO-CREATE policy)
//
// When a convenience Set* targets a component that is nil, the helpers below
// construct a minimal VALID component so that the value persists and the
// subsequent Write/Validate cycle succeeds. Construction is conditional on the
// detected container format: components are only created when the format's
// injector can actually carry them (format-appropriateness).
//
// Format capabilities (derived from format.SupportsWrite and the injector
// dispatch table in write.go):
//
//	JPEG                        → EXIF ✓  IPTC ✓  XMP ✓
//	TIFF                        → EXIF ✓  IPTC ✓  XMP ✓
//	DNG / CR2 / NEF / ARW /     → EXIF ✓  IPTC ✓  XMP ✓
//	  ORF / RW2 (TIFF-based RAW)
//	PNG                         → EXIF ✓  IPTC ✗¹ XMP ✓
//	WebP                        → EXIF ✓  IPTC ✗  XMP ✓
//	HEIF / AVIF                 → EXIF ✓  IPTC ✗  XMP ✓
//	CR3                         → EXIF ✓  IPTC ✗  XMP ✓
//	Unknown                     → auto-create disabled; Set* is a no-op.
//
// IPTC pathway for TIFF-based formats: the write path embeds IPTC as IFD0
// tag 0x83BB (IPTC-NAA, TypeLong). All TIFF-derived formats route through
// relocate.go / relocate_*.go which upsert this tag on write.
//
// ¹ PNG has no native IPTC chunk, so no IPTC component is created for a PNG
// that has none. A PNG that already carries IPTC in a "Raw profile type iptc"
// text chunk has it read into m.IPTC, and the write path rewrites that chunk
// in place (format/png).
//
// CR3 does NOT carry IPTC: CR3 uses an ISO BMFF container whose CMT1–CMT4
// UUID boxes carry only EXIF and XMP; there is no IPTC pathway.
//
// Callers that pass FormatUnknown to NewMetadata will never get a component
// auto-created; every Set* on such a Metadata is a documented no-op and the
// Write call will fail with UnsupportedFormatError.
// ---------------------------------------------------------------------------

// canCarryEXIF reports whether the detected format can carry an EXIF segment
// on write. All writable formats support EXIF.
func (m *Metadata) canCarryEXIF() bool {
	return format.SupportsWrite(format.FormatID(m.format))
}

// canCarryIPTC reports whether the detected format can carry an IPTC segment
// on write.
//
// IPTC is supported by JPEG (APP13 / Photoshop IRB envelope) and by all
// TIFF-derived formats: TIFF, DNG, CR2, NEF, ARW, ORF, and RW2. The
// TIFF-based write path embeds IPTC as IFD0 tag 0x83BB (IPTC-NAA).
//
// Formats without an IPTC pathway: PNG, WebP, HEIF, AVIF, and CR3 (ISO BMFF
// — the CMT UUID boxes carry only EXIF and XMP). For PNG this means only that
// no new IPTC component is created: IPTC already present in a "Raw profile
// type iptc" text chunk is read into m.IPTC, and the write path rewrites that
// chunk in place (format/png). FormatUnknown also returns false.
//
// Finding #110: prior to this fix, only JPEG and TIFF returned true, causing
// DNG/CR2/NEF/ARW/ORF/RW2 to silently drop IPTC on Set* calls.
func (m *Metadata) canCarryIPTC() bool {
	switch format.FormatID(m.format) {
	case format.FormatJPEG,
		format.FormatTIFF,
		format.FormatDNG,
		format.FormatCR2,
		format.FormatNEF,
		format.FormatARW,
		format.FormatORF,
		format.FormatRW2:
		return true
	default:
		return false
	}
}

// canCarryXMP reports whether the detected format can carry an XMP packet
// on write. All writable formats support XMP.
func (m *Metadata) canCarryXMP() bool {
	return format.SupportsWrite(format.FormatID(m.format))
}

// ensureEXIF constructs a minimal valid *exif.EXIF when m.EXIF is nil and the
// detected format can carry EXIF. The constructed EXIF has a non-nil IFD0 and
// ByteOrder = binary.LittleEndian so that m.Validate() and exif.Encode() succeed.
//
// EXIF validity invariant: m.EXIF != nil ⟹ m.EXIF.IFD0 != nil (Validate §).
func (m *Metadata) ensureEXIF() {
	if m.EXIF != nil || m.exifOpaque || !m.canCarryEXIF() {
		return
	}
	m.EXIF = &exif.EXIF{
		ByteOrder: binary.LittleEndian,
		IFD0:      &exif.IFD{},
	}
}

// ensureIPTC constructs a minimal valid *iptc.IPTC when m.IPTC is nil and the
// detected format can carry IPTC. new(iptc.IPTC) is sufficient: the zero value
// of IPTC (with its [10][]Dataset array) is ready for use by all write-path
// helpers in the iptc package.
func (m *Metadata) ensureIPTC() {
	if m.IPTC != nil || m.iptcOpaque || !m.canCarryIPTC() {
		return
	}
	m.IPTC = new(iptc.IPTC)
}

// ensureXMP constructs a minimal valid *xmp.XMP when m.XMP is nil and the
// detected format can carry XMP. The Properties map must be non-nil to satisfy
// the Validate invariant (m.XMP != nil ⟹ m.XMP.Properties != nil).
//
// It constructs nothing when the image carried an XMP packet that is not
// loaded (xmpOpaque): the packet is written back unchanged, and the XMP part
// of a setter is skipped, so that no metadata is lost silently. The packet is
// not loaded when it could not be parsed (task #327; XMP Part 1 §7.1 requires
// a well-formed packet, and an ill-formed one cannot be edited faithfully;
// Read reports it in ParseWarnings) or when the caller read with WithoutXMP
// (task #343).
func (m *Metadata) ensureXMP() {
	if m.XMP != nil || m.xmpOpaque || !m.canCarryXMP() {
		return
	}
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
}

// ---------------------------------------------------------------------------
// Write setters — AUTO-CREATE policy (task #88) + mutex guard (finding #128).
//
// Each setter holds m.mu for its entire body (covering ensure* AND the
// sub-struct mutation). This makes concurrent Set* calls safe.
//
// ensure* helpers are called while the lock is held; they are unexported and
// non-recursive — they must never acquire m.mu themselves.
//
// Components that the format cannot carry (e.g. IPTC for CR3, or for a PNG
// without a "Raw profile type iptc" chunk) are never constructed, and the
// setter silently skips writing to that component. A component that already
// exists is always written, even when the format could not create it: IPTC
// read from a PNG raw profile chunk is updated by the setters.
// For FormatUnknown, no component is ever created — see NewMetadata for the
// documented contract.
//
// Setter signatures are unchanged (void) — this is a non-breaking change.
// ---------------------------------------------------------------------------

// SetCaption writes s to all metadata components that can carry a caption
// (EXIF, IPTC, XMP). If a component is nil but the detected format supports
// it, the component is auto-created before writing. Concurrent calls are safe.
//
// For FormatUnknown this is a documented no-op — no component is auto-created
// and no value is stored (see NewMetadata for the full contract).
func (m *Metadata) SetCaption(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureIPTC()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetCaption(s)
	}
	if m.IPTC != nil {
		m.IPTC.SetCaption(s)
	}
	if m.XMP != nil {
		m.XMP.SetCaption(s)
	}
}

// SetCopyright writes s to all metadata components that can carry a copyright
// notice (EXIF, IPTC, XMP). Components are auto-created when nil and the
// format supports them. Concurrent calls are safe.
func (m *Metadata) SetCopyright(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureIPTC()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetCopyright(s)
	}
	if m.IPTC != nil {
		m.IPTC.SetCopyright(s)
	}
	if m.XMP != nil {
		m.XMP.SetCopyright(s)
	}
}

// SetCreator sets the creator list to the single creator s, in every
// metadata component that can carry it: EXIF Artist (0x013B), IPTC 2:80
// By-line and XMP dc:creator. Like SetCreators with a one-item list, it
// replaces every existing creator in all three components, so that they stay
// consistent (MWG §5.7). Components are auto-created when nil and the format
// supports them. Concurrent calls are safe.
//
// The EXIF Artist value follows the MWG list convention: s is enclosed in
// quotes when it contains "; " or begins with a quote, so that a reader
// splitting the Artist list at "; " gets s back as one creator.
func (m *Metadata) SetCreator(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureIPTC()
	m.ensureXMP()
	one := [1]string{s}
	if m.EXIF != nil {
		m.EXIF.SetCreator(reconcile.JoinArtist(one[:]))
	}
	if m.IPTC != nil {
		m.IPTC.SetCreator(s)
		keepFirstRecord2(m.IPTC, iptc.DS2Byline)
	}
	if m.XMP != nil {
		m.XMP.SetCreator(s) // replaces dc:creator with the one-item list
	}
}

// SetCreators writes an ordered list of creators to IPTC (2:80 By-line,
// repeatable, order preserved), XMP (dc:creator, ordered rdf:Seq, order
// preserved — RECONCILE-04), and EXIF (0x013B Artist, the list joined with
// "; " as MWG §5.7 prescribes: an item that contains "; " or begins with a
// quote is enclosed in quotes, and its quotes are doubled). Components are
// auto-created when nil and the format supports them. Concurrent calls are
// safe.
//
// SetCreators is a FULL REPLACE across all three backends. Empty (or nil)
// creators removes dc:creator, every 2:80 By-line entry and the EXIF Artist
// tag (MWG §4.2.3.1: a value removed from one form MUST be removed from the
// others).
func (m *Metadata) SetCreators(creators []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureIPTC()
	m.ensureXMP()
	if m.EXIF != nil {
		if len(creators) > 0 {
			m.EXIF.SetCreator(reconcile.JoinArtist(creators))
		} else {
			removeEXIFTag(m.EXIF.IFD0, exif.TagArtist)
		}
	}
	if m.IPTC != nil {
		m.IPTC.SetCreators(creators)
	}
	if m.XMP != nil {
		m.XMP.SetCreators(creators)
	}
}

// SetCameraModel writes s to EXIF and XMP. Components are auto-created when
// nil and the format supports them. Concurrent calls are safe.
func (m *Metadata) SetCameraModel(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetCameraModel(s)
	}
	if m.XMP != nil {
		m.XMP.SetCameraModel(s)
	}
}

// SetGPS writes the WGS-84 decimal-degree coordinates to EXIF and XMP.
// Components are auto-created when nil and the format supports them.
// Concurrent calls are safe.
func (m *Metadata) SetGPS(lat, lon float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetGPS(lat, lon)
	}
	if m.XMP != nil {
		m.XMP.SetGPS(lat, lon)
	}
}

// SetKeywords writes kws to IPTC and XMP. Components are auto-created when nil
// and the format supports them. For formats that cannot carry IPTC (PNG, WebP,
// HEIF, AVIF, CR3), only XMP is populated so the keywords are not silently
// dropped, unless m.IPTC already exists: a PNG whose "Raw profile type iptc"
// chunk was read has a non-nil m.IPTC, which is updated as well. Concurrent
// calls are safe.
func (m *Metadata) SetKeywords(kws []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureIPTC()
	m.ensureXMP()
	if m.IPTC != nil {
		m.IPTC.SetKeywords(kws)
	}
	if m.XMP != nil {
		m.XMP.SetKeywords(kws)
	}
}

// SetLensModel writes s to EXIF (0xA434 LensModel) and XMP aux:Lens, and to
// XMP exifEX:LensModel when the XMP already has that property. Components are
// auto-created when nil and the format supports them. Concurrent calls are
// safe.
func (m *Metadata) SetLensModel(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetLensModel(s)
	}
	if m.XMP != nil {
		m.XMP.SetLensModel(s)
	}
	m.mirrorToXMP(reconcile.LensModel)
}

// SetMake writes s to EXIF (0x010F Make), and to XMP tiff:Make when the XMP
// already has that property. EXIF is auto-created when nil and the format
// supports it. Concurrent calls are safe.
func (m *Metadata) SetMake(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetMake(s)
		m.mirrorToXMP(reconcile.Make)
	}
}

// SetDateTimeOriginal writes t to EXIF, IPTC, and XMP. Components are
// auto-created when nil and the format supports them. Concurrent calls are
// safe.
//
// This is the single write-side bridge for "when was this taken" across all
// three backends (RECONCILE-03, MWG §5.3):
//   - EXIF ExifIFD 0x9003 DateTimeOriginal (EXIF §4.6.5), with 0x9011
//     OffsetTimeOriginal set to the offset of t and 0x9291
//     SubSecTimeOriginal removed (see exif.EXIF.SetDateTimeOriginal), so
//     that the EXIF form names the same instant as the IPTC and XMP forms.
//   - IPTC dataset 2:55 Date Created + 2:60 Time Created (IIM §2.2.23), via
//     IPTC.SetDateCreated. Sub-second precision is dropped (IIM has no
//     sub-second field). If t.Year() is outside [0, 9999], the IPTC write is
//     a complete no-op (IPTC.SetDateCreated's year-overflow guard) while
//     EXIF and XMP still proceed normally.
//   - Two distinct XMP properties: exif:DateTimeOriginal (mirrors the EXIF
//     tag) and photoshop:DateCreated (the XMP equivalent of IIM 2:55+2:60,
//     per the IPTC Photo Metadata Mapping Guidelines 2025.1). Both describe
//     the same real-world moment; writing both maximises reader
//     interoperability — Lightroom/Bridge honour photoshop:DateCreated,
//     other tools honour exif:DateTimeOriginal.
//
// xmp:CreateDate (digitization date, the XMP analogue of EXIF
// 0x9004 DateTimeDigitized) is a distinct field and is deliberately NOT
// written here — see the IPTC/XMP reconciliation spec §0.1.
//
// For formats that cannot carry IPTC (PNG, WebP, HEIF, AVIF, CR3), m.IPTC
// is not created and only EXIF+XMP are written — the same fallback pattern
// used by SetKeywords. A PNG whose "Raw profile type iptc" chunk was read has
// a non-nil m.IPTC, which is updated as well.
func (m *Metadata) SetDateTimeOriginal(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureIPTC()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetDateTimeOriginal(t)
	}
	if m.IPTC != nil {
		m.IPTC.SetDateCreated(t)
	}
	if m.XMP != nil {
		m.XMP.SetDateTimeOriginal(t) // exif:DateTimeOriginal
		m.XMP.SetDateCreated(t)      // photoshop:DateCreated (RECONCILE-03)
	}
}

// SetExposureTime writes the rational exposure time to EXIF (0x829A), and to
// XMP exif:ExposureTime when the XMP already has that property. EXIF is
// auto-created when nil and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetExposureTime(num, den uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetExposureTime(num, den)
		m.mirrorToXMP(reconcile.ExposureTime)
	}
}

// SetFNumber writes the F-number to EXIF (0x829D), and to XMP exif:FNumber
// when the XMP already has that property. EXIF is auto-created when nil and
// the format supports it. Concurrent calls are safe.
func (m *Metadata) SetFNumber(f float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetFNumber(f)
		m.mirrorToXMP(reconcile.FNumber)
	}
}

// SetISO writes the ISO speed rating to EXIF (0x8827), and to XMP
// exif:ISOSpeedRatings and exifEX:PhotographicSensitivity when the XMP
// already has them. EXIF is auto-created when nil and the format supports it.
// Concurrent calls are safe.
func (m *Metadata) SetISO(iso uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetISO(iso)
		m.mirrorToXMP(reconcile.ISO)
	}
}

// SetFocalLength writes the focal length in millimetres to EXIF (0x920A),
// and to XMP exif:FocalLength when the XMP already has that property. EXIF is
// auto-created when nil and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetFocalLength(mm float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetFocalLength(mm)
		m.mirrorToXMP(reconcile.FocalLength)
	}
}

// SetOrientation writes the orientation tag to EXIF (0x0112, the only form
// of the orientation, MWG §5.4), and to XMP tiff:Orientation when the XMP
// already has that property. EXIF is auto-created when nil and the format
// supports it. Concurrent calls are safe.
func (m *Metadata) SetOrientation(v uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetOrientation(v)
		m.mirrorToXMP(reconcile.Orientation)
	}
}

// SetImageSize writes the pixel dimensions to EXIF (0xA002 and 0xA003), and
// to XMP exif:PixelXDimension and exif:PixelYDimension when the XMP already
// has them. EXIF is auto-created when nil and the format supports it.
// Concurrent calls are safe.
func (m *Metadata) SetImageSize(width, height uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetImageSize(width, height)
		m.mirrorToXMP(reconcile.PixelXDimension)
		m.mirrorToXMP(reconcile.PixelYDimension)
	}
}
