package xmp

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
	NScc  = "http://creativecommons.org/ns#"
	// NSpdf is the PDF namespace (Adobe PDF 1.3 property set).
	NSpdf = "http://ns.adobe.com/pdf/1.3/"
	// NSxmpNote is the XMP namespace for extended XMP notes.
	// Adobe XMP Specification Part 3 §1.1.4.
	NSxmpNote = "http://ns.adobe.com/xap/1.0/se/Note/"
)

// collectionType returns the RDF collection element name (Bag, Seq, or Alt)
// for the given namespace URI and local property name per ISO 16684-1 §7.5.
// Defaults to "Bag" for unrecognised properties.
func collectionType(ns, local string) string {
	if ns == NSdc {
		switch local {
		case "creator":
			return "Seq"
		case "rights", "description", "title":
			return "Alt"
		}
	}
	return "Bag"
}

// prefixMap maps well-known XMP namespace URIs to their canonical prefix strings.
// XMP Part 1 §B, Adobe XMP Specification Appendix A.
var prefixMap = map[string]string{ //nolint:gochecknoglobals
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
}

// prefixOf returns the conventional namespace prefix for a URI.
// Falls back to "ns" for unknown namespaces.
func prefixOf(uri string) string {
	if p, ok := prefixMap[uri]; ok {
		return p
	}
	return "ns"
}
