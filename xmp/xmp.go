// Package xmp implements an XMP packet parser and writer.
//
// Compliance: ISO 16684-1:2019 (XMP specification Part 1) and
// Adobe XMP Specification Parts 1–3.
// Spec citations reference Part 1 as "XMP §<section>".
//
// This package operates on a raw XMP packet byte slice (the content between
// and including the <?xpacket …?> processing instructions). Locating that
// packet within a container (e.g., JPEG APP1) is the responsibility of the
// container layer (format/jpeg).
package xmp

import (
	"fmt"
	"strconv"
	"strings"
)

// XMP holds the parsed XMP properties organised by namespace URI.
type XMP struct {
	// Properties maps namespace URI → property local-name → value string.
	// Multi-valued properties (rdf:Alt, rdf:Seq, rdf:Bag) are joined with
	// the Unicode "record separator" U+001E so callers can split them.
	Properties map[string]map[string]string
}

// Parse parses a raw XMP packet.
// b may include the <?xpacket …?> wrapper; if absent, the bytes are
// treated as the xmpmeta/RDF body directly.
func Parse(b []byte) (*XMP, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("xmp: empty input")
	}

	x := &XMP{Properties: make(map[string]map[string]string)}

	// Locate the XMP packet; if no wrapper is found, treat the whole input
	// as the RDF body (XMP §7.3).
	body := Scan(b)
	if body == nil {
		body = b
	}

	if err := parseRDF(body, x); err != nil {
		return nil, err
	}

	return x, nil
}

// Encode serialises x to a padded XMP packet ready for embedding in a
// container segment. The packet is UTF-8 encoded with a read/write
// <?xpacket?> wrapper per XMP §7.
func Encode(x *XMP) ([]byte, error) {
	return encode(x)
}

// CameraModel returns the tiff:Model or xmp:CreatorTool property.
// tiff:Model is the canonical XMP property for the camera model (XMP §8.4).
func (x *XMP) CameraModel() string {
	if x == nil {
		return ""
	}
	if v := x.get(NStiff, "Model"); v != "" {
		return v
	}
	return x.get(NSxmp, "CreatorTool")
}

// GPS returns GPS coordinates from the exif:GPSLatitude / exif:GPSLongitude
// properties (XMP §8.4). The format is "DDD,MM.mmmR" or "DDD,MM,SS.sssR".
func (x *XMP) GPS() (lat, lon float64, ok bool) {
	if x == nil {
		return 0, 0, false
	}
	latStr := x.get(NSexif, "GPSLatitude")
	lonStr := x.get(NSexif, "GPSLongitude")
	if latStr == "" || lonStr == "" {
		return 0, 0, false
	}
	var err error
	lat, err = parseXMPGPS(latStr)
	if err != nil {
		return 0, 0, false
	}
	lon, err = parseXMPGPS(lonStr)
	if err != nil {
		return 0, 0, false
	}
	// Validate WGS-84 coordinate ranges (XMP §8.4).
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// Copyright returns dc:rights (XMP §8.3).
func (x *XMP) Copyright() string {
	if x == nil {
		return ""
	}
	return x.firstValue(NSdc, "rights")
}

// Caption returns dc:description (XMP §8.3).
func (x *XMP) Caption() string {
	if x == nil {
		return ""
	}
	return x.firstValue(NSdc, "description")
}

// get returns the property value for the given namespace URI and local name.
func (x *XMP) get(ns, local string) string {
	if x.Properties == nil {
		return ""
	}
	m, ok := x.Properties[ns]
	if !ok {
		return ""
	}
	return m[local]
}

// firstValue returns the first item of a (possibly multi-valued) property.
// Multi-valued properties are joined with U+001E; we return the substring
// before the first separator.
func (x *XMP) firstValue(ns, local string) string {
	v := x.get(ns, local)
	if v == "" {
		return ""
	}
	if i := strings.IndexByte(v, '\x1e'); i >= 0 {
		return v[:i]
	}
	return v
}

// parseXMPGPS parses an XMP GPS coordinate string into a signed decimal degree.
// Supported formats (XMP §8.4):
//
//	"DDD,MM.mmmR"      — degrees and decimal minutes
//	"DDD,MM,SS.sssR"   — degrees, minutes, and seconds
//
// where R is N/S (latitude) or E/W (longitude).
func parseXMPGPS(s string) (float64, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("xmp: GPS value too short: %q", s)
	}
	ref := string(s[len(s)-1])
	s = s[:len(s)-1]

	parts := strings.Split(s, ",")
	if len(parts) < 2 {
		return 0, fmt.Errorf("xmp: invalid GPS format: %q", s)
	}

	deg, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, fmt.Errorf("xmp: GPS degrees: %w", err)
	}

	var result float64
	if len(parts) == 2 {
		min, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, fmt.Errorf("xmp: GPS minutes: %w", err)
		}
		result = deg + min/60
	} else {
		min, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return 0, fmt.Errorf("xmp: GPS minutes: %w", err)
		}
		sec, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return 0, fmt.Errorf("xmp: GPS seconds: %w", err)
		}
		result = deg + min/60 + sec/3600
	}

	if ref == "S" || ref == "W" {
		result = -result
	}
	return result, nil
}
