package gometadata

import (
	"errors"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// NewMetadata returns an empty Metadata ready for writing to a file of the
// given format. All metadata fields are nil; populate m.EXIF, m.IPTC, or
// m.XMP before passing m to Write or WriteFile.
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
type Metadata struct {
	EXIF *exif.EXIF
	IPTC *iptc.IPTC
	XMP  *xmp.XMP

	// unexported: detected container format and original raw segments
	// retained so round-trip writes can reconstruct the file correctly.
	format  uint8
	rawEXIF []byte
	rawIPTC []byte
	rawXMP  []byte
}

// Format returns the detected container format ID of the image.
func (m *Metadata) Format() format.FormatID { return format.FormatID(m.format) }

// RawEXIF returns the raw EXIF segment bytes as read from the container.
func (m *Metadata) RawEXIF() []byte { return m.rawEXIF }

// RawIPTC returns the raw IPTC IIM segment bytes as read from the container.
func (m *Metadata) RawIPTC() []byte { return m.rawIPTC }

// RawXMP returns the raw XMP packet bytes as read from the container.
func (m *Metadata) RawXMP() []byte { return m.rawXMP }

// Validate checks that m is in a consistent state suitable for writing.
// It returns a descriptive error when an obvious inconsistency is detected
// (e.g. unknown format, EXIF struct without IFD0). Write calls Validate
// automatically; callers may call it earlier for better error messages.
func (m *Metadata) Validate() error {
	if format.FormatID(m.format) == format.FormatUnknown {
		return &UnsupportedFormatError{}
	}
	if m.EXIF != nil && m.EXIF.IFD0 == nil {
		return errors.New("gometadata: EXIF struct has nil IFD0; use exif.Parse to construct a valid EXIF")
	}
	if m.XMP != nil && m.XMP.Properties == nil {
		return errors.New("gometadata: XMP struct has nil Properties map")
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
func (m *Metadata) GPS() (lat, lon float64, ok bool) {
	if m.EXIF != nil {
		if lat, lon, ok = m.EXIF.GPS(); ok {
			return
		}
	}
	if m.XMP != nil {
		return m.XMP.GPS()
	}
	return 0, 0, false
}

// Copyright returns the copyright notice.
// Source priority: XMP > IPTC > EXIF.
func (m *Metadata) Copyright() string {
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
// Source priority: XMP > IPTC > EXIF.
func (m *Metadata) Caption() string {
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

// DateTimeOriginal returns the original capture date/time.
// Source priority: EXIF > XMP.
func (m *Metadata) DateTimeOriginal() (time.Time, bool) {
	if m.EXIF != nil {
		if t, ok := m.EXIF.DateTimeOriginal(); ok {
			return t, true
		}
	}
	if m.XMP != nil {
		if v := m.XMP.DateTimeOriginal(); v != "" {
			// XMP stores dates as ISO 8601: "YYYY-MM-DDTHH:MM:SS" with optional TZ.
			for _, layout := range []string{
				"2006-01-02T15:04:05-07:00",
				"2006-01-02T15:04:05Z",
				"2006-01-02T15:04:05",
			} {
				if t, err := time.Parse(layout, v); err == nil {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
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
// Source priority: XMP > IPTC.
func (m *Metadata) Keywords() []string {
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
	if m.EXIF != nil && m.EXIF.IFD0 != nil {
		if e := m.EXIF.IFD0.Get(exif.TagDateTime); e != nil {
			if v := e.String(); v != "" {
				if t, err := time.Parse("2006:01:02 15:04:05", v); err == nil {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
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
// Source priority: XMP > IPTC > EXIF.
func (m *Metadata) Creator() string {
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
// Write setters — write to all non-nil component structs that support the
// field. Components are never created; only existing ones are written.
// ---------------------------------------------------------------------------

// SetCaption writes s to all non-nil metadata components (EXIF, IPTC, XMP).
func (m *Metadata) SetCaption(s string) {
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

// SetCopyright writes s to all non-nil metadata components (EXIF, IPTC, XMP).
func (m *Metadata) SetCopyright(s string) {
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

// SetCreator writes s to all non-nil metadata components (EXIF, IPTC, XMP).
func (m *Metadata) SetCreator(s string) {
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

// SetCameraModel writes s to EXIF and XMP when those components are non-nil.
func (m *Metadata) SetCameraModel(s string) {
	if m.EXIF != nil {
		m.EXIF.SetCameraModel(s)
	}
	if m.XMP != nil {
		m.XMP.SetCameraModel(s)
	}
}

// SetGPS writes the WGS-84 decimal-degree coordinates to EXIF and XMP when
// those components are non-nil.
func (m *Metadata) SetGPS(lat, lon float64) {
	if m.EXIF != nil {
		m.EXIF.SetGPS(lat, lon)
	}
	if m.XMP != nil {
		m.XMP.SetGPS(lat, lon)
	}
}

// SetKeywords writes kws to IPTC and XMP when those components are non-nil.
func (m *Metadata) SetKeywords(kws []string) {
	if m.IPTC != nil {
		m.IPTC.SetKeywords(kws)
	}
	if m.XMP != nil {
		m.XMP.SetKeywords(kws)
	}
}

// SetLensModel writes s to EXIF and XMP when those components are non-nil.
func (m *Metadata) SetLensModel(s string) {
	if m.EXIF != nil {
		m.EXIF.SetLensModel(s)
	}
	if m.XMP != nil {
		m.XMP.SetLensModel(s)
	}
}

// SetMake writes s to EXIF when it is non-nil.
func (m *Metadata) SetMake(s string) {
	if m.EXIF != nil {
		m.EXIF.SetMake(s)
	}
}

// SetDateTimeOriginal writes t to EXIF and XMP when those components are
// non-nil.
func (m *Metadata) SetDateTimeOriginal(t time.Time) {
	if m.EXIF != nil {
		m.EXIF.SetDateTimeOriginal(t)
	}
	if m.XMP != nil {
		m.XMP.SetDateTimeOriginal(t)
	}
}

// SetExposureTime writes the rational exposure time to EXIF when non-nil.
func (m *Metadata) SetExposureTime(num, den uint32) {
	if m.EXIF != nil {
		m.EXIF.SetExposureTime(num, den)
	}
}

// SetFNumber writes the F-number to EXIF when non-nil.
func (m *Metadata) SetFNumber(f float64) {
	if m.EXIF != nil {
		m.EXIF.SetFNumber(f)
	}
}

// SetISO writes the ISO speed rating to EXIF when non-nil.
func (m *Metadata) SetISO(iso uint) {
	if m.EXIF != nil {
		m.EXIF.SetISO(iso)
	}
}

// SetFocalLength writes the focal length in millimetres to EXIF when non-nil.
func (m *Metadata) SetFocalLength(mm float64) {
	if m.EXIF != nil {
		m.EXIF.SetFocalLength(mm)
	}
}

// SetOrientation writes the orientation tag to EXIF when non-nil.
func (m *Metadata) SetOrientation(v uint16) {
	if m.EXIF != nil {
		m.EXIF.SetOrientation(v)
	}
}

// SetImageSize writes the pixel dimensions to EXIF when non-nil.
func (m *Metadata) SetImageSize(width, height uint32) {
	if m.EXIF != nil {
		m.EXIF.SetImageSize(width, height)
	}
}
