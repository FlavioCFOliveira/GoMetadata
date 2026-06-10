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

// maxXMPDocumentBytes is the maximum number of UTF-8 bytes that Parse accepts
// for a single XMP document body (post-normalisation, pre-RDF-parsing).
//
// Design rationale (Task #51 — empirical measurement):
// Real-world XMP packets span from a few hundred bytes (camera EXIF-only) to
// roughly 4 MiB for extreme cases (photo-sphere/depth-map XMP with embedded
// thumbnail XMP packets). The corpus maximum across all JPEG, TIFF, PNG, and
// HEIF files in the GoMetadata test suite is 4,763 bytes. Adobe XMP with rich
// xmpMM:History chains from professional workflows may reach a few hundred KiB.
// Google Camera depth-map XMP (panorama / portrait) peaks around 1–2 MiB.
// Setting the cap at 16 MiB is:
//   - Consistent with maxXMPTranscodeBytes (UTF-16 input cap), so a UTF-8 document
//     can never exceed the budget implied by the encoding layer.
//   - Generous enough to never reject legitimate metadata from any known producer.
//   - Bounded enough to prevent a crafted multi-megabyte "many small properties"
//     document from walking parseRDF's O(n) byte scanner without any limit.
//
// ISO 16684-1 imposes no document size limit; this is a local security policy.
// The per-attribute maxUnescapedXMLBytes (1 MiB) and the transcode cap are
// complementary: they bound individual allocations; this cap bounds total input.
const maxXMPDocumentBytes = 16 << 20 // 16 MiB

// Parse parses a raw XMP packet.
// b may include the <?xpacket …?> wrapper; if absent, the bytes are
// treated as the xmpmeta/RDF body directly.
//
// XMP Part 1 §7.2: if b begins with a UTF-16 or UTF-32 BOM, it is
// automatically transcoded to UTF-8 before parsing.
//
// Parse rejects inputs larger than maxXMPDocumentBytes (16 MiB) after
// UTF-8 normalisation to prevent O(n) parsing of pathological documents.
func Parse(b []byte) (*XMP, error) {
	if len(b) == 0 {
		return nil, ErrEmptyInput
	}

	// Normalise to UTF-8 so that the BOM-based encoding path is handled once,
	// here, rather than duplicated across Scan and parseRDF.
	// normaliseToUTF8 is a zero-copy no-op for UTF-8 input.
	b = normaliseToUTF8(b)
	if b == nil {
		return nil, ErrEmptyInput
	}

	// Document-level size cap (post-normalisation, pre-RDF-parsing).
	// See maxXMPDocumentBytes for the design rationale.
	if len(b) > maxXMPDocumentBytes {
		return nil, ErrDocumentTooLarge
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
	return serialise(x)
}

// CameraModel returns the tiff:Model or xmp:CreatorTool property.
// tiff:Model is the canonical XMP property for the camera model (XMP §8.4).
func (x *XMP) CameraModel() string {
	if x == nil {
		return ""
	}
	if v := x.getProp(NStiff, "Model"); v != "" {
		return v
	}
	return x.getProp(NSxmp, "CreatorTool")
}

// GPS returns GPS coordinates from available XMP namespaces, checked in order:
//  1. exif:GPSLatitude / exif:GPSLongitude (NSexif, XMP §8.4) — canonical Adobe XMP GPS.
//  2. geo:lat / geo:long (NSgeo, W3C Basic Geo 2003/01) — alternative GPS encoding.
//
// Both use the "DDD,MM.mmmR" or "DDD,MM,SS.sssR" coordinate format.
// Returns ok=false when neither namespace contains both coordinates or
// when the values cannot be parsed.
func (x *XMP) GPS() (lat, lon float64, ok bool) { //nolint:cyclop,gocyclo // branching mirrors the two-namespace fallback + bounds checks; extracting sub-functions would obscure the priority logic
	if x == nil {
		return 0, 0, false
	}

	// Primary: Adobe XMP exif namespace (XMP §8.4).
	latStr := x.getProp(NSexif, "GPSLatitude")
	lonStr := x.getProp(NSexif, "GPSLongitude")

	// Fallback: W3C Basic Geo vocabulary (geo:lat / geo:long, and also geo:lon
	// which is a common alias). W3C Geo 2003/01.
	if latStr == "" || lonStr == "" {
		latStr = x.getProp(NSgeo, "lat")
		// W3C Geo spec defines "long"; some producers use "lon" as an alias.
		lonStr = x.getProp(NSgeo, "long")
		if lonStr == "" {
			lonStr = x.getProp(NSgeo, "lon")
		}
	}

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
	return x.getProp(NSexif, "DateTimeOriginal")
}

// LensModel returns the aux:Lens property (Adobe XMP Auxiliary namespace).
func (x *XMP) LensModel() string {
	if x == nil {
		return ""
	}
	return x.getProp(NSaux, "Lens")
}

// Keywords returns the dc:subject values (XMP §8.3).
// dc:subject is an unordered bag; each item is returned as a separate string.
//
// This implementation avoids strings.Split's []string allocation by doing a
// single-pass scan with strings.IndexByte. strings.Count pre-sizes the result
// slice in O(n) without allocating.
func (x *XMP) Keywords() []string {
	if x == nil {
		return nil
	}
	v := x.getProp(NSdc, "subject")
	if v == "" {
		return nil
	}
	// Multi-valued properties are joined with U+001E (record separator).
	result := make([]string, 0, strings.Count(v, "\x1e")+1)
	for {
		i := strings.IndexByte(v, '\x1e')
		if i < 0 {
			if v != "" {
				result = append(result, v)
			}
			break
		}
		if i > 0 {
			result = append(result, v[:i])
		}
		v = v[i+1:]
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
	return x.getProp(ns, local)
}

// SetCaption sets dc:description to s (XMP §8.3).
func (x *XMP) SetCaption(s string) { x.putProp(NSdc, "description", s) }

// SetCopyright sets dc:rights to s (XMP §8.3).
func (x *XMP) SetCopyright(s string) { x.putProp(NSdc, "rights", s) }

// SetCreator sets dc:creator to s (XMP §8.3).
func (x *XMP) SetCreator(s string) { x.putProp(NSdc, "creator", s) }

// AddKeyword appends kw to dc:subject (XMP §8.3).
// Multiple keywords are stored joined with U+001E (record separator), matching
// the convention used by Keywords() and the RDF encoder.
//
// strings.Builder with pre-grown capacity replaces the two-step string
// concatenation `existing+"\x1e"+kw`, which would allocate an intermediate
// string for `existing+"\x1e"` before allocating the final result.
func (x *XMP) AddKeyword(kw string) {
	existing := x.getProp(NSdc, "subject")
	if existing == "" {
		x.putProp(NSdc, "subject", kw)
		return
	}
	var sb strings.Builder
	sb.Grow(len(existing) + 1 + len(kw))
	sb.WriteString(existing)
	sb.WriteByte('\x1e')
	sb.WriteString(kw)
	x.putProp(NSdc, "subject", sb.String())
}

// SetCameraModel sets tiff:Model to s (XMP §8.4).
func (x *XMP) SetCameraModel(s string) { x.putProp(NStiff, "Model", s) }

// SetDateTimeOriginal sets exif:DateTimeOriginal to t formatted as RFC 3339
// (XMP §8.4 / ISO 8601).
func (x *XMP) SetDateTimeOriginal(t time.Time) {
	x.putProp(NSexif, "DateTimeOriginal", t.Format(time.RFC3339))
}

// SetGPS writes exif:GPSLatitude and exif:GPSLongitude in the XMP
// "DDD,MM.mmmmmmR" format (XMP §8.4 / Adobe XMP Spec Part 2).
// lat is negative for South, lon is negative for West.
func (x *XMP) SetGPS(lat, lon float64) {
	if x == nil {
		return
	}
	x.putProp(NSexif, "GPSLatitude", formatGPSCoord(lat, 'N', 'S'))
	x.putProp(NSexif, "GPSLongitude", formatGPSCoord(lon, 'E', 'W'))
}

// formatGPSCoord serialises a decimal-degree GPS coordinate to the XMP
// "DDD,MM.mmmmmmR" format without using fmt.Sprintf.
//
// Using strconv.AppendFloat into a [32]byte stack buffer avoids the reflection
// and heap allocation that fmt.Sprintf would incur. The final string(buf[:n])
// conversion is the only allocation.
func formatGPSCoord(coord float64, posRef, negRef byte) string {
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
	// Build into a stack-allocated [32]byte buffer.
	// Worst case: "180,59.999999W" = 14 bytes — 32 bytes is ample.
	var raw [32]byte
	b := raw[:0]
	b = strconv.AppendFloat(b, deg, 'f', 0, 64)
	b = append(b, ',')
	b = strconv.AppendFloat(b, decMin, 'f', 6, 64)
	b = append(b, ref)
	return string(b)
}

// SetLensModel sets aux:Lens to s (Adobe XMP Auxiliary namespace).
func (x *XMP) SetLensModel(s string) {
	if x == nil {
		return
	}
	x.putProp(NSaux, "Lens", s)
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
	x.putProp(NSdc, "subject", strings.Join(kws, "\x1e"))
}

// Set is the public equivalent of the private set() method.
// It writes value to Properties[ns][local], initialising maps as needed.
// ns is a namespace URI (use the NS* constants from this package).
func (x *XMP) Set(ns, local, value string) {
	if x == nil {
		return
	}
	x.putProp(ns, local, value)
}

// putProp writes value to Properties[ns][local], initialising inner maps as needed.
func (x *XMP) putProp(ns, local, value string) {
	if x.Properties == nil {
		x.Properties = make(map[string]map[string]string)
	}
	if x.Properties[ns] == nil {
		x.Properties[ns] = make(map[string]string)
	}
	x.Properties[ns][local] = value
}

// getProp returns the property value for the given namespace URI and local name.
func (x *XMP) getProp(ns, local string) string {
	if x.Properties == nil {
		return ""
	}
	m, ok := x.Properties[ns]
	if !ok {
		return ""
	}
	return m[local]
}

// xDefaultValue returns the canonical value from a U+001E-joined multi-item
// string produced by an rdf:Alt collection.
//
// XMP Part 1 §C.2.5 / P1-H: the x-default language alternative is the
// language-independent canonical value and MUST be preferred over any
// language-tagged item when both are present. Producers such as ExifTool and
// Adobe Lightroom frequently place x-default last in document order, so
// "return the first item" is not spec-compliant.
//
// Storage convention (established by onCharDataListItem in rdf.go):
//   - A bare item (x-default or unlabelled) is stored without a prefix: "value"
//   - A language-tagged item is stored as "lang|value": "en|value"
//
// This function scans all U+001E-separated items and returns the first bare
// item (no '|' character). If none is found, it returns the first item
// verbatim (which may be "lang|value" for an all-tagged collection).
//
// Task #73 / spec-correctness fix: replaces the previous firstValue behaviour
// which blindly returned the first item regardless of whether it was x-default.
func xDefaultValue(v string) string {
	// Empty: nothing to select.
	if v == "" {
		return ""
	}

	// Single-item fast path: if there is no U+001E separator the whole string is
	// one item. Return it regardless of whether it is bare or "lang|value" — there
	// is no choice.
	firstItem, rest, multi := strings.Cut(v, "\x1e")
	if !multi {
		return v // single item, bare or "lang|value"
	}

	// Multiple items: scan for the first bare item (no '|').
	// A bare item is x-default or unlabelled per onCharDataListItem convention.
	// firstItem is the first item in document order; keep it as the fallback.
	if strings.IndexByte(firstItem, '|') < 0 {
		// First item is already bare — return it immediately.
		return firstItem
	}

	// Scan remaining items with strings.Cut for clarity and no extra allocations.
	for rest != "" {
		var item string
		item, rest, _ = strings.Cut(rest, "\x1e")
		if strings.IndexByte(item, '|') < 0 {
			return item // bare item found — x-default or unlabelled
		}
	}

	// No bare item found — fall back to the first item in document order.
	return firstItem
}

// firstValue returns the canonical value of a (possibly multi-valued) property.
//
// For rdf:Alt properties (dc:description, dc:rights, …) the stored value is a
// U+001E-joined list whose items are either bare (x-default / unlabelled) or
// prefixed with "lang|" for language-tagged alternatives.
// Per XMP Part 1 §C.2.5 / P1-H, the x-default item is the canonical value
// and must be returned regardless of its position in document order.
// xDefaultValue implements this selection logic.
//
// For rdf:Seq / rdf:Bag properties the items are always bare, so the first
// item (which xDefaultValue also returns as a bare item) is correct.
func (x *XMP) firstValue(ns, local string) string {
	v := x.getProp(ns, local)
	if v == "" {
		return ""
	}
	return xDefaultValue(v)
}

// parseXMPGPS parses an XMP GPS coordinate string into a signed decimal degree.
// Supported formats (XMP §8.4):
//
//	"DDD,MM.mmmR"      — degrees and decimal minutes
//	"DDD,MM,SS.sssR"   — degrees, minutes, and seconds
//
// where R is N/S (latitude) or E/W (longitude).
//
// This implementation uses strings.IndexByte instead of strings.Split to avoid
// allocating a []string slice for the comma-delimited parts.
func parseXMPGPS(s string) (float64, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("xmp: GPS value too short: %q: %w", s, ErrGPSValueTooShort)
	}
	ref := s[len(s)-1]
	s = s[:len(s)-1]

	degStr, rest, ok := strings.Cut(s, ",")
	if !ok {
		return 0, fmt.Errorf("xmp: invalid GPS format: %q: %w", s, ErrInvalidGPSFormat)
	}

	deg, err := strconv.ParseFloat(degStr, 64)
	if err != nil {
		return 0, fmt.Errorf("xmp: GPS degrees: %w", err)
	}

	minsStr, secStr, hasSeconds := strings.Cut(rest, ",")

	var result float64
	if !hasSeconds {
		// "DDD,MM.mmmR" — decimal minutes format.
		mins, minsErr := strconv.ParseFloat(minsStr, 64)
		if minsErr != nil {
			return 0, fmt.Errorf("xmp: GPS minutes: %w", minsErr)
		}
		result = deg + mins/60
	} else {
		// "DDD,MM,SS.sssR" — degrees, minutes, seconds format.
		mins, minsErr := strconv.ParseFloat(minsStr, 64)
		if minsErr != nil {
			return 0, fmt.Errorf("xmp: GPS minutes: %w", minsErr)
		}
		sec, secErr := strconv.ParseFloat(secStr, 64)
		if secErr != nil {
			return 0, fmt.Errorf("xmp: GPS seconds: %w", secErr)
		}
		result = deg + mins/60 + sec/3600
	}

	if ref == 'S' || ref == 'W' {
		result = -result
	}
	return result, nil
}
