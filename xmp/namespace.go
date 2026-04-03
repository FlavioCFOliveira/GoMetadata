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
)

// prefixOf returns the conventional namespace prefix for a URI.
// Falls back to "ns" for unknown namespaces.
func prefixOf(uri string) string {
	switch uri {
	case NSxmp:
		return "xmp"
	case NSxmpRights:
		return "xmpRights"
	case NSxmpMM:
		return "xmpMM"
	case NSdc:
		return "dc"
	case NSphotoshop:
		return "photoshop"
	case NSexif:
		return "exif"
	case NStiff:
		return "tiff"
	case NSaux:
		return "aux"
	case NSiptcCore:
		return "Iptc4xmpCore"
	case NSiptcExt:
		return "Iptc4xmpExt"
	case NSrdf:
		return "rdf"
	case NSx:
		return "x"
	}
	return "ns"
}
