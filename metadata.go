package imgmetadata

import (
	"github.com/flaviocfo/img-metadata/exif"
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
