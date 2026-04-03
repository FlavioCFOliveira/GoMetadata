package imgmetadata

import (
	"fmt"
	"time"

	"github.com/flaviocfo/img-metadata/exif"
	"github.com/flaviocfo/img-metadata/format"
	"github.com/flaviocfo/img-metadata/iptc"
	"github.com/flaviocfo/img-metadata/xmp"
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
	format   uint8
	rawEXIF  []byte
	rawIPTC  []byte
	rawXMP   []byte
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
		return fmt.Errorf("imgmetadata: EXIF struct has nil IFD0; use exif.Parse to construct a valid EXIF")
	}
	if m.IPTC != nil && m.IPTC.Records == nil {
		return fmt.Errorf("imgmetadata: IPTC struct has nil Records map")
	}
	if m.XMP != nil && m.XMP.Properties == nil {
		return fmt.Errorf("imgmetadata: XMP struct has nil Properties map")
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
