package xmp

import "strconv"

// Well-known XMP namespace URIs and their conventional prefixes
// (XMP Part 1 §B, Adobe XMP Specification Appendix A).
const (
	NSxmp       = "http://ns.adobe.com/xap/1.0/"
	NSxmpRights = "http://ns.adobe.com/xap/1.0/rights/"
	NSxmpMM     = "http://ns.adobe.com/xap/1.0/mm/"
	NSdc        = "http://purl.org/dc/elements/1.1/"
	NSphotoshop = "http://ns.adobe.com/photoshop/1.0/"
	NSexif      = "http://ns.adobe.com/exif/1.0/"
	NStiff      = "http://ns.adobe.com/tiff/1.0/"
	NSaux       = "http://ns.adobe.com/exif/1.0/aux/"
	NSiptcCore  = "http://iptc.org/std/Iptc4xmpCore/1.0/xmlns/"
	NSiptcExt   = "http://iptc.org/std/Iptc4xmpExt/2008-02-29/"
	NSrdf       = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	NSx         = "adobe:ns:meta/"
	// NScc is the Creative Commons namespace (creativecommons.org/ns).
	NScc = "http://creativecommons.org/ns#"
	// NSpdf is the PDF namespace (Adobe PDF 1.3 property set).
	NSpdf = "http://ns.adobe.com/pdf/1.3/"
	// NSxmpNote is the XMP namespace for extended XMP notes.
	// Adobe XMP Specification Part 3 §1.1.4.
	NSxmpNote = "http://ns.adobe.com/xap/1.0/se/Note/"
	// NSgeo is the W3C Basic Geo vocabulary namespace for GPS coordinates.
	// W3C Geo 2003/01: http://www.w3.org/2003/01/geo/wgs84_pos#
	// Properties: lat (latitude, decimal degrees) and long/lon (longitude, decimal degrees).
	// The coordinate format is "DDD,MM.mmmR" or "DDD,MM,SS.sssR", same as NSexif.
	NSgeo = "http://www.w3.org/2003/01/geo/wgs84_pos#"
)

// collectionType returns the RDF collection element name (Bag, Seq, or Alt)
// for the given namespace URI and local property name per ISO 16684-1 §7.5.
// Defaults to "Bag" for unrecognised properties.
//
// xmpMM ordered sequences (#43):
// Adobe XMP Specification Part 2 §1.2.8 specifies that xmpMM:History,
// xmpMM:Ingredients, and xmpMM:Pantry are ordered arrays (rdf:Seq), not bags.
// History records the sequence of operations applied to a document; Ingredients
// and Pantry record ordered lists of referenced/embedded resources.  Emitting
// them as rdf:Bag would violate the spec and break interoperability with
// Photoshop, Bridge, and ExifTool.
func collectionType(ns, local string) string {
	if ns == NSdc {
		switch local {
		case "creator":
			return "Seq"
		case "rights", "description", "title":
			return "Alt"
		}
	}
	// #43: Adobe XMP Specification Part 2 §1.2.8 — xmpMM ordered arrays.
	if ns == NSxmpMM {
		switch local {
		case "History", "Ingredients", "Pantry":
			return "Seq"
		}
	}
	return "Bag"
}

// prefixMap maps well-known XMP namespace URIs to their canonical prefix strings.
// XMP Part 1 §B, Adobe XMP Specification Appendix A.
var prefixMap = map[string]string{ //nolint:gochecknoglobals // read-only namespace→prefix lookup table per XMP Part 1 §B; never mutated
	NSxmp:       "xmp",
	NSxmpRights: "xmpRights",
	NSxmpMM:     "xmpMM",
	NSdc:        "dc",
	NSphotoshop: "photoshop",
	NSexif:      "exif",
	NStiff:      "tiff",
	NSaux:       "aux",
	NSiptcCore:  "Iptc4xmpCore",
	NSiptcExt:   "Iptc4xmpExt",
	NSrdf:       "rdf",
	NSx:         "x",
	NScc:        "cc",
	NSpdf:       "pdf",
	NSxmpNote:   "xmpNote",
	// NSgeo: the W3C Basic Geo (WGS84 lat/long) vocabulary conventionally
	// binds to the "geo" prefix (http://www.w3.org/2003/01/geo/). Without
	// this entry, uniquePrefixFor treats NSgeo as an unknown namespace and
	// assigns a generated nsN prefix — spec-legal (XMP resolves properties
	// by namespace URI, not prefix; ISO 16684-1 §7.4) but not the
	// conventional binding that xmp.GPS()'s W3C Geo fallback expects to see
	// on write, and confusing for tools/readers that key off "geo:lat".
	NSgeo: "geo",
}

// uniquePrefixFor returns a prefix for uri that is not already in used.
// For well-known URIs the canonical prefix is returned (it must already be in
// used from the pre-population step in serialise, so no collision is possible).
// For unknown URIs a generated prefix nsN (ns0, ns1, …) is assigned, where N is
// the current value of counter.  The chosen prefix is registered in used before
// returning so subsequent calls with a different unknown URI get a distinct value.
//
// NS-03 / XMP Part 1 §6 / XML Namespaces: the serialiser MUST NOT bind two
// distinct namespace URIs to the same XML prefix within a single document.
func uniquePrefixFor(uri string, used map[string]struct{}, counter *int) string {
	if p, ok := prefixMap[uri]; ok {
		// Canonical prefix — already pre-registered in used; return as-is.
		return p
	}
	// Generate ns0, ns1, … until we find one that is not in use.
	// In practice a document with more than a handful of unknown namespaces is
	// extremely rare; the loop terminates in O(1) almost always.
	for {
		p := "ns" + strconv.Itoa(*counter)
		*counter++
		if _, taken := used[p]; !taken {
			used[p] = struct{}{}
			return p
		}
	}
}
