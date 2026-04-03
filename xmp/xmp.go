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
	"math"
	"strconv"
	"strings"
	"time"
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

// DateTimeOriginal returns the exif:DateTimeOriginal property (XMP §8.4)
// as a raw string in ISO 8601 format. Returns "" when not present.
func (x *XMP) DateTimeOriginal() string {
	if x == nil {
		return ""
	}
	return x.get(NSexif, "DateTimeOriginal")
}

// LensModel returns the aux:Lens property (Adobe XMP Auxiliary namespace).
func (x *XMP) LensModel() string {
	if x == nil {
		return ""
	}
	return x.get(NSaux, "Lens")
}

// Keywords returns the dc:subject values (XMP §8.3).
// dc:subject is an unordered bag; each item is returned as a separate string.
func (x *XMP) Keywords() []string {
	if x == nil {
		return nil
	}
	v := x.get(NSdc, "subject")
	if v == "" {
		return nil
	}
	// Multi-valued properties are joined with U+001E (record separator).
	parts := strings.Split(v, "\x1e")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// Creator returns the first dc:creator value (XMP §8.3).
func (x *XMP) Creator() string {
	if x == nil {
		return ""
	}
	return x.firstValue(NSdc, "creator")
}

// Get returns the property value for the given namespace URI and local name.
// Returns "" when the property is absent or when x is nil.
func (x *XMP) Get(ns, local string) string {
	if x == nil {
		return ""
	}
	return x.get(ns, local)
}

// SetCaption sets dc:description to s (XMP §8.3).
func (x *XMP) SetCaption(s string) { x.set(NSdc, "description", s) }

// SetCopyright sets dc:rights to s (XMP §8.3).
func (x *XMP) SetCopyright(s string) { x.set(NSdc, "rights", s) }

// SetCreator sets dc:creator to s (XMP §8.3).
func (x *XMP) SetCreator(s string) { x.set(NSdc, "creator", s) }

// AddKeyword appends kw to dc:subject (XMP §8.3).
// Multiple keywords are stored joined with U+001E (record separator), matching
// the convention used by Keywords() and the RDF encoder.
func (x *XMP) AddKeyword(kw string) {
	existing := x.get(NSdc, "subject")
	if existing == "" {
		x.set(NSdc, "subject", kw)
	} else {
		x.set(NSdc, "subject", existing+"\x1e"+kw)
	}
}

// SetCameraModel sets tiff:Model to s (XMP §8.4).
func (x *XMP) SetCameraModel(s string) { x.set(NStiff, "Model", s) }

// SetDateTimeOriginal sets exif:DateTimeOriginal to t formatted as RFC 3339
// (XMP §8.4 / ISO 8601).
func (x *XMP) SetDateTimeOriginal(t time.Time) {
	x.set(NSexif, "DateTimeOriginal", t.Format(time.RFC3339))
}

// SetGPS writes exif:GPSLatitude and exif:GPSLongitude in the XMP
// "DDD,MM.mmmmmmR" format (XMP §8.4 / Adobe XMP Spec Part 2).
// lat is negative for South, lon is negative for West.
func (x *XMP) SetGPS(lat, lon float64) {
	if x == nil {
		return
	}
	formatCoord := func(coord float64, posRef, negRef string) string {
		abs := coord
		if abs < 0 {
			abs = -abs
		}
		deg := math.Floor(abs)
		decMin := (abs - deg) * 60
		ref := posRef
		if coord < 0 {
			ref = negRef
		}
		return fmt.Sprintf("%.0f,%.6f%s", deg, decMin, ref)
	}
	x.set(NSexif, "GPSLatitude", formatCoord(lat, "N", "S"))
	x.set(NSexif, "GPSLongitude", formatCoord(lon, "E", "W"))
}

// SetLensModel sets aux:Lens to s (Adobe XMP Auxiliary namespace).
func (x *XMP) SetLensModel(s string) {
	if x == nil {
		return
	}
	x.set(NSaux, "Lens", s)
}

// SetKeywords replaces dc:subject entirely with kws (XMP §8.3).
// Items are joined with the U+001E record separator, matching the convention
// used by Keywords() and the RDF encoder. When kws is empty the property is
// deleted so that Keywords() returns nil after a round-trip.
func (x *XMP) SetKeywords(kws []string) {
	if x == nil {
		return
	}
	if len(kws) == 0 {
		// Delete the property entirely so Keywords() returns nil.
		if x.Properties != nil {
			delete(x.Properties[NSdc], "subject")
		}
		return
	}
	x.set(NSdc, "subject", strings.Join(kws, "\x1e"))
}

// Set is the public equivalent of the private set() method.
// It writes value to Properties[ns][local], initialising maps as needed.
// ns is a namespace URI (use the NS* constants from this package).
func (x *XMP) Set(ns, local, value string) {
	if x == nil {
		return
	}
	x.set(ns, local, value)
}

// set writes value to Properties[ns][local], initialising inner maps as needed.
func (x *XMP) set(ns, local, value string) {
	if x.Properties == nil {
		x.Properties = make(map[string]map[string]string)
	}
	if x.Properties[ns] == nil {
		x.Properties[ns] = make(map[string]string)
	}
	x.Properties[ns][local] = value
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
