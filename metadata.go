package gometadata

import (
	"bytes"
	"encoding/binary"
	"sync"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
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
// When the same field exists in more than one metadata type, the
// convenience methods below apply a documented resolution policy:
//   - Camera data (model, make, lens, settings): EXIF wins.
//   - Descriptive and rights data (caption, copyright, keywords): XMP wins,
//     falling back to IPTC, then EXIF.
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
// Copy semantics:
//
//	Metadata contains a sync.Mutex and must not be copied by value after
//	first use. Always pass and store *Metadata (pointer), never Metadata.
type Metadata struct {
	// mu serialises concurrent Set* calls (finding #128).
	// Must not be copied after first use — always use *Metadata.
	mu sync.Mutex

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

	// rawIPTCDigest is the 16-byte MD5 value stored in Photoshop resource
	// 0x0425 ("IPTC Digest") inside the APP13 IRB, or nil when absent.
	// MWG Guidelines v2.0 §3.3.1: the digest is used at read time to determine
	// whether IPTC or XMP takes precedence for conflicting descriptive fields.
	//
	// nil  → digest resource absent; default XMP-over-IPTC priority applies.
	// 16 B = all-zero → "unknown" sentinel; IPTC trust is elevated.
	// 16 B, non-zero  → compare to MD5(rawIPTC); match → XMP priority;
	//                   mismatch → IPTC trust is elevated.
	rawIPTCDigest []byte

	// rawXMPWire is non-nil when the image carried extended XMP (Adobe XMP
	// Specification Part 3 §1.1.4) and the XMP was not modified by the caller.
	// It holds the internal wire-frame encoding produced by jpeg.ExtractWithWire:
	// the original main APP1 content and the assembled extended payload packed
	// together. Inject uses this to reproduce the original segmentation without
	// regenerating the GUID, guaranteeing that rawXMP is byte-stable across an
	// unmodified round-trip.
	//
	// rawXMPWire is always nil when m.XMP is non-nil (user modified the XMP);
	// in that case encodeMetadata re-encodes from the struct and the extended
	// split path uses a freshly generated GUID.
	rawXMPWire []byte
}

// Format returns the detected container format ID of the image.
func (m *Metadata) Format() format.FormatID { return format.FormatID(m.format) }

// RawEXIF returns a copy of the raw EXIF segment bytes as read from the container.
//
// #139: returns bytes.Clone(m.rawEXIF) so that caller mutations of the returned
// slice cannot corrupt the internal relocation base used by subsequent Write calls.
// The raw EXIF bytes in TIFF-based formats share their backing array with every
// parsed IFDEntry.Value; an in-place mutation by the caller would silently corrupt
// all parsed EXIF values AND the image-data source used during write relocation.
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

// iptcTrustElevated reports whether IPTC should take read priority over XMP
// for fields where both are present and carry different values.
//
// MWG Guidelines v2.0 §3.3.1: the Photoshop resource 0x0425 stores an MD5
// digest of the raw 0x0404 IIM block at the time XMP was last written. If the
// digest matches the current IIM block, XMP was written after the last IPTC
// edit, so XMP remains authoritative (the default MWG-01 priority). If the
// digest mismatches, or if the stored digest is the all-zero "unknown"
// sentinel, IPTC may have been edited independently of XMP, so IPTC trust is
// elevated for conflicting fields.
//
// When rawIPTCDigest is nil (the resource was absent), the default XMP-over-
// IPTC priority (MWG-01) is preserved unchanged.
func (m *Metadata) iptcTrustElevated() bool {
	if len(m.rawIPTCDigest) != 16 {
		// No digest resource in the IRB — use default MWG-01 (XMP priority).
		return false
	}
	var stored [16]byte
	copy(stored[:], m.rawIPTCDigest)
	// DigestMatch handles the all-zero sentinel case (returns unknown=true).
	match, unknown := iptc.DigestMatch(m.rawIPTC, stored)
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
// Source priority: XMP > IPTC > EXIF (MWG-01).
// Exception — MWG §3.3.1 (MWG-02): when the Photoshop 0x0425 IPTC digest
// mismatches (or is the all-zero sentinel), IPTC trust is elevated and the
// priority for conflicting values becomes IPTC > XMP > EXIF.
//
//nolint:cyclop,gocyclo,nestif // MWG-02 digest-conditional branching is inherent; splitting would obscure the priority logic
func (m *Metadata) Copyright() string {
	if m.iptcTrustElevated() {
		if m.IPTC != nil {
			if v := m.IPTC.Copyright(); v != "" {
				return v
			}
		}
		if m.XMP != nil {
			if v := m.XMP.Copyright(); v != "" {
				return v
			}
		}
		if m.EXIF != nil {
			return m.EXIF.Copyright()
		}
		return ""
	}
	if m.XMP != nil {
		if v := m.XMP.Copyright(); v != "" {
			return v
		}
	}
	if m.IPTC != nil {
		if v := m.IPTC.Copyright(); v != "" {
			return v
		}
	}
	if m.EXIF != nil {
		return m.EXIF.Copyright()
	}
	return ""
}

// Caption returns the image description / caption.
// Source priority: XMP > IPTC > EXIF (MWG-01).
// Exception — MWG §3.3.1 (MWG-02): IPTC digest mismatch elevates IPTC trust
// so the priority becomes IPTC > XMP > EXIF for conflicting values.
//
//nolint:cyclop,gocyclo,nestif // MWG-02 digest-conditional branching is inherent; splitting would obscure the priority logic
func (m *Metadata) Caption() string {
	if m.iptcTrustElevated() {
		if m.IPTC != nil {
			if v := m.IPTC.Caption(); v != "" {
				return v
			}
		}
		if m.XMP != nil {
			if v := m.XMP.Caption(); v != "" {
				return v
			}
		}
		if m.EXIF != nil {
			return m.EXIF.Caption()
		}
		return ""
	}
	if m.XMP != nil {
		if v := m.XMP.Caption(); v != "" {
			return v
		}
	}
	if m.IPTC != nil {
		if v := m.IPTC.Caption(); v != "" {
			return v
		}
	}
	if m.EXIF != nil {
		return m.EXIF.Caption()
	}
	return ""
}

// xmpDateLayouts lists the ISO 8601 time formats used by XMP DateTimeOriginal,
// tried in order from most specific (timezone offset) to least specific (local).
var xmpDateLayouts = [3]string{ //nolint:gochecknoglobals // constant time layouts
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
}

// DateTimeOriginal returns the original capture date/time.
// Source priority: EXIF > XMP.
//
// MWG timezone synthesis: when EXIF carries DateTimeOriginal without
// OffsetTimeOriginal (EXIF 2.31+, tag 0x9011), the method checks whether XMP
// exif:DateTimeOriginal carries a timezone offset (e.g. "+02:00") and, if so,
// applies it to the EXIF wall-clock date/time. This follows the Metadata
// Working Group (MWG) Guidelines §2.2.1 recommendation for reconstructing a
// fully-qualified timestamp from split EXIF+XMP metadata.
//
// When neither EXIF OffsetTimeOriginal nor an XMP timezone is available, the
// returned time.Time uses UTC (as before).
func (m *Metadata) DateTimeOriginal() (time.Time, bool) {
	if m.EXIF != nil {
		if t, ok := m.EXIF.DateTimeOriginal(); ok {
			return m.applyXMPTimezone(t), true
		}
	}
	if m.XMP != nil {
		if v := m.XMP.DateTimeOriginal(); v != "" {
			// XMP stores dates as ISO 8601: "YYYY-MM-DDTHH:MM:SS" with optional TZ.
			for _, layout := range xmpDateLayouts {
				if t, err := time.Parse(layout, v); err == nil {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// applyXMPTimezone applies MWG §2.2.1 timezone synthesis to t: when EXIF
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
	// MWG §2.2.1: the EXIF wall-clock digits are authoritative; only the
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
// Source priority: EXIF > XMP.
func (m *Metadata) LensModel() string {
	if m.EXIF != nil {
		if v := m.EXIF.LensModel(); v != "" {
			return v
		}
	}
	if m.XMP != nil {
		if v := m.XMP.LensModel(); v != "" {
			return v
		}
	}
	return ""
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
// Source priority: XMP > IPTC (MWG-01 / MWG-07).
// Exception — MWG §3.3.1 (MWG-02): IPTC digest mismatch elevates IPTC trust
// so the priority becomes IPTC > XMP for conflicting values.
func (m *Metadata) Keywords() []string {
	if m.iptcTrustElevated() {
		if m.IPTC != nil {
			if kw := m.IPTC.Keywords(); len(kw) > 0 {
				return kw
			}
		}
		if m.XMP != nil {
			return m.XMP.Keywords()
		}
		return nil
	}
	if m.XMP != nil {
		if kw := m.XMP.Keywords(); len(kw) > 0 {
			return kw
		}
	}
	if m.IPTC != nil {
		return m.IPTC.Keywords()
	}
	return nil
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

// Creator returns the author / creator name.
// Source priority: XMP > IPTC > EXIF (MWG-01 / MWG-04).
// Exception — MWG §3.3.1 (MWG-02): IPTC digest mismatch elevates IPTC trust
// so the priority becomes IPTC > XMP > EXIF for conflicting values.
//
//nolint:cyclop,gocyclo,nestif // MWG-02 digest-conditional branching is inherent; splitting would obscure the priority logic
func (m *Metadata) Creator() string {
	if m.iptcTrustElevated() {
		if m.IPTC != nil {
			if v := m.IPTC.Creator(); v != "" {
				return v
			}
		}
		if m.XMP != nil {
			if v := m.XMP.Creator(); v != "" {
				return v
			}
		}
		if m.EXIF != nil {
			return m.EXIF.Creator()
		}
		return ""
	}
	if m.XMP != nil {
		if v := m.XMP.Creator(); v != "" {
			return v
		}
	}
	if m.IPTC != nil {
		if v := m.IPTC.Creator(); v != "" {
			return v
		}
	}
	if m.EXIF != nil {
		return m.EXIF.Creator()
	}
	return ""
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
//	PNG                         → EXIF ✓  IPTC ✗  XMP ✓
//	WebP                        → EXIF ✓  IPTC ✗  XMP ✓
//	HEIF / AVIF                 → EXIF ✓  IPTC ✗  XMP ✓
//	CR3                         → EXIF ✓  IPTC ✗  XMP ✓
//	Unknown                     → auto-create disabled; Set* is a no-op.
//
// IPTC pathway for TIFF-based formats: the write path embeds IPTC as IFD0
// tag 0x83BB (IPTC-NAA, TypeLong). All TIFF-derived formats route through
// relocate.go / relocate_*.go which upsert this tag on write.
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
// — the CMT UUID boxes carry only EXIF and XMP).
// FormatUnknown also returns false.
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
	if m.EXIF != nil || !m.canCarryEXIF() {
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
	if m.IPTC != nil || !m.canCarryIPTC() {
		return
	}
	m.IPTC = new(iptc.IPTC)
}

// ensureXMP constructs a minimal valid *xmp.XMP when m.XMP is nil and the
// detected format can carry XMP. The Properties map must be non-nil to satisfy
// the Validate invariant (m.XMP != nil ⟹ m.XMP.Properties != nil).
func (m *Metadata) ensureXMP() {
	if m.XMP != nil || !m.canCarryXMP() {
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
// Components that the format cannot carry (e.g. IPTC for PNG, CR3) are never
// constructed, and the setter silently skips writing to that component.
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

// SetCreator writes s to all metadata components that can carry a creator
// (EXIF, IPTC, XMP). Components are auto-created when nil and the format
// supports them. Concurrent calls are safe.
func (m *Metadata) SetCreator(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureIPTC()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetCreator(s)
	}
	if m.IPTC != nil {
		m.IPTC.SetCreator(s)
	}
	if m.XMP != nil {
		m.XMP.SetCreator(s)
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
// dropped. Concurrent calls are safe.
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

// SetLensModel writes s to EXIF and XMP. Components are auto-created when nil
// and the format supports them. Concurrent calls are safe.
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
}

// SetMake writes s to EXIF. EXIF is auto-created when nil and the format
// supports it. Concurrent calls are safe.
func (m *Metadata) SetMake(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetMake(s)
	}
}

// SetDateTimeOriginal writes t to EXIF and XMP. Components are auto-created
// when nil and the format supports them. Concurrent calls are safe.
func (m *Metadata) SetDateTimeOriginal(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	m.ensureXMP()
	if m.EXIF != nil {
		m.EXIF.SetDateTimeOriginal(t)
	}
	if m.XMP != nil {
		m.XMP.SetDateTimeOriginal(t)
	}
}

// SetExposureTime writes the rational exposure time to EXIF. EXIF is
// auto-created when nil and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetExposureTime(num, den uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetExposureTime(num, den)
	}
}

// SetFNumber writes the F-number to EXIF. EXIF is auto-created when nil and
// the format supports it. Concurrent calls are safe.
func (m *Metadata) SetFNumber(f float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetFNumber(f)
	}
}

// SetISO writes the ISO speed rating to EXIF. EXIF is auto-created when nil
// and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetISO(iso uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetISO(iso)
	}
}

// SetFocalLength writes the focal length in millimetres to EXIF. EXIF is
// auto-created when nil and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetFocalLength(mm float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetFocalLength(mm)
	}
}

// SetOrientation writes the orientation tag to EXIF. EXIF is auto-created
// when nil and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetOrientation(v uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetOrientation(v)
	}
}

// SetImageSize writes the pixel dimensions to EXIF. EXIF is auto-created when
// nil and the format supports it. Concurrent calls are safe.
func (m *Metadata) SetImageSize(width, height uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureEXIF()
	if m.EXIF != nil {
		m.EXIF.SetImageSize(width, height)
	}
}
