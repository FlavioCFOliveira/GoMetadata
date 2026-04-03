package imgmetadata

import (
	"time"

	"github.com/flaviocfo/img-metadata/exif"
	"github.com/flaviocfo/img-metadata/format"
	"github.com/flaviocfo/img-metadata/iptc"
	"github.com/flaviocfo/img-metadata/xmp"
)

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
