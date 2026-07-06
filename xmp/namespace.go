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

// arrayProperties is the single, spec-sourced table of every array-typed
// (rdf:Seq / rdf:Bag / rdf:Alt) top-level property this package recognises,
// keyed by namespace URI then local property name. It is the sole backing
// store for both collectionType and isCollectionProperty below, so the two
// functions cannot drift out of sync with each other or with the spec —
// exactly the failure mode that produced two independent defects fixed by
// this table (task #43's xmpMM entry and task #272's dc-only-scoped
// isCollectionProperty, both duplicated hand-written switches that had to be
// kept in sync manually and weren't).
//
// Every entry is verified against the "Value type" column of that schema's
// official property-reference table, cross-checked via exiv2.org's XMP tag
// pages (https://exiv2.org/tags-xmp-<ns>.html), which transcribe the Adobe
// XMP Specification Part 1/2 and IPTC Photo Metadata Standard 2025.1
// property tables verbatim: "bag X" → Bag, "seq X" (including the
// "Closed Choice of seq X" variant used by tiff:YCbCrSubSampling) → Seq,
// "Lang Alt" / "alt X" → Alt (task #273, verified 2026-07-07).
//
// A property NOT listed here is either a Simple (non-array) property, or an
// array-typed property in a namespace/local this table has not yet been
// extended to cover; for the latter case, effectiveContainerType (below)
// still preserves round-trip fidelity for any property that was actually
// parsed from a source document with a real rdf:Alt/Seq/Bag wrapper — see
// the XMP.containerTypes field doc in xmp.go. This table is the fallback
// authority for properties with no parse-time record, i.e. new properties
// created via Set()/the named setters.
var arrayProperties = map[string]map[string]string{ //nolint:gochecknoglobals // read-only spec-sourced lookup table; never mutated after init
	// Dublin Core (dc:) — Adobe XMP Specification Part 1, Appendix 8.3;
	// exiv2.org/tags-xmp-dc.html. dc:format, dc:identifier, dc:source are
	// Simple (Text/MIMEType) properties, not arrays, and are intentionally
	// absent (task #272).
	NSdc: {
		"creator": "Seq", "date": "Seq",
		"subject": "Bag", "contributor": "Bag", "language": "Bag",
		"publisher": "Bag", "relation": "Bag", "type": "Bag",
		"rights": "Alt", "description": "Alt", "title": "Alt",
	},
	// XMP Media Management (xmpMM:) — Adobe XMP Specification Part 2 §1.2.8;
	// exiv2.org/tags-xmp-xmpMM.html "Value type" column: History=seq
	// ResourceEvent, Ingredients=bag ResourceRef, Pantry=bag struct,
	// Versions=seq Version.
	//
	// Task #273 correction: Ingredients and Pantry were WRONGLY coded as
	// Seq prior to this fix (the removed #43 comment incorrectly cited both
	// as ordered arrays alongside History). Cross-checked against
	// adobe/xmp-docs, developer.adobe.com, and exiv2.org's xmpMM tag table
	// (three independent sources agree): only History is an ordered
	// sequence; Ingredients and Pantry are unordered bags. Versions was
	// missing entirely and fell through to the wrong "Bag" default; it is
	// added here as Seq.
	NSxmpMM: {
		"History": "Seq", "Versions": "Seq",
		"Ingredients": "Bag", "Pantry": "Bag",
	},
	// XMP Rights Management (xmpRights:) — exiv2.org/tags-xmp-xmpRights.html.
	NSxmpRights: {
		"Owner":      "Bag", // bag ProperName
		"UsageTerms": "Alt", // Lang Alt
	},
	// XMP Basic (xmp:) — exiv2.org/tags-xmp-xmp.html. Not currently written
	// by any named setter in this package, but read/round-trip correctness
	// still applies to any file that carries these properties.
	NSxmp: {
		"Identifier": "Bag", // bag Text
		"Thumbnails": "Alt", // alt Thumbnail
		"Advisory":   "Bag", // bag XPath
	},
	// TIFF (tiff:) — exiv2.org/tags-xmp-tiff.html. tiff:ImageDescription and
	// tiff:Copyright mirror dc:description/dc:rights (both Lang Alt): MWG
	// multi-schema writers (Lightroom, Bridge, many camera firmwares) keep
	// both pairs in lockstep, making these two properties high real-world
	// frequency rather than a corner case.
	NStiff: {
		"ImageDescription": "Alt", "Copyright": "Alt",
		"BitsPerSample": "Seq", "TransferFunction": "Seq",
		"YCbCrSubSampling": "Seq", // "Closed Choice of seq Integer"
		"WhitePoint":       "Seq", "PrimaryChromaticities": "Seq",
		"YCbCrCoefficients": "Seq", "ReferenceBlackWhite": "Seq",
	},
	// Exif (exif:) — exiv2.org/tags-xmp-exif.html.
	NSexif: {
		"UserComment":     "Alt",
		"ISOSpeedRatings": "Seq", "SubjectArea": "Seq", "SubjectLocation": "Seq",
	},
	// Photoshop (photoshop:) — exiv2.org/tags-xmp-photoshop.html.
	NSphotoshop: {
		"DocumentAncestors":      "Bag", // bag Ancestor
		"TextLayers":             "Seq", // seq Layer
		"SupplementalCategories": "Bag", // bag Text
	},
	// IPTC Core (Iptc4xmpCore:) — IPTC Photo Metadata Standard 2025.1;
	// exiv2.org/tags-xmp-iptc.html. AltTextAccessibility and
	// ExtDescrAccessibility are the accessibility extensions added to IPTC
	// Core in later revisions of the standard (both Lang Alt).
	NSiptcCore: {
		"Scene": "Bag", "SubjectCode": "Bag", // bag closed Choice of Text
		"AltTextAccessibility": "Alt", "ExtDescrAccessibility": "Alt",
	},
	// IPTC Extension (Iptc4xmpExt:) — IPTC Photo Metadata Standard 2025.1;
	// exiv2.org/tags-xmp-iptcExt.html. AOCreator and AOTitle are the
	// deprecated top-level legacy properties superseded by the
	// ArtworkOrObject bag-of-structs but still valid top-level properties to
	// read and round-trip from older files (verified from the raw property
	// table: both are direct rows, not fields nested inside
	// ArtworkOrObjectDetails).
	NSiptcExt: {
		"PersonInImage": "Bag", "PersonInImageWDetails": "Bag",
		"OrganisationInImageCode": "Bag", "OrganisationInImageName": "Bag",
		"ProductInImage": "Bag", "PropertyReleaseID": "Bag",
		"AboutCvTerm": "Bag", "CVterm": "Bag", "ModelAge": "Bag",
		"EventId": "Bag", "EmbdEncRightsExpr": "Bag", "Genre": "Bag",
		"ImageRegion": "Bag", "LinkedEncRightsExpr": "Bag", "RegistryId": "Bag",
		"LocationShown": "Bag", "LocationCreated": "Bag", "ArtworkOrObject": "Bag",
		"AOCreator": "Seq",
		"Event":     "Alt", "AOTitle": "Alt",
	},
}

// collectionType returns the RDF collection element name (Bag, Seq, or Alt)
// for the given namespace URI and local property name per ISO 16684-1 §7.5,
// per the spec-sourced arrayProperties table above. Defaults to "Bag" for
// unrecognised properties — the historical default from before this table
// existed, kept for the properties that reach this function via the
// "value contains \x1e but has no spec table entry" fallback path in
// write.go (see effectiveContainerType).
func collectionType(ns, local string) string {
	if m, ok := arrayProperties[ns]; ok {
		if ct, ok := m[local]; ok {
			return ct
		}
	}
	return "Bag"
}

// isCollectionProperty reports whether (ns, local) is a property whose XMP
// schema type is a structured array (rdf:Alt / rdf:Seq / rdf:Bag) per
// ISO 16684-1 §7.5, regardless of how many values it currently holds, per
// the spec-sourced arrayProperties table above.
//
// An array-typed property MUST always be serialised using its collection
// container — even when it holds exactly one item — unlike a Simple (Text)
// property, for which the compact `<prefix:local>value</prefix:local>` form
// is correct. This is one of the two inputs to effectiveContainerType, which
// write.go's classification loop uses instead of "does the in-memory value
// contain the internal U+001E separator", because a value can be array-typed
// and still hold exactly one item (e.g. a caption with only an x-default
// entry; RECONCILE-05, docs/conformance/iptc.md §4).
func isCollectionProperty(ns, local string) bool {
	m, ok := arrayProperties[ns]
	if !ok {
		return false
	}
	_, ok = m[local]
	return ok
}

// specTableMatches reports whether the spec-sourced arrayProperties table
// already records (ns, local) with exactly container kind ctype.
//
// Used by onStartCollection (rdf.go) to decide whether recording a
// parse-time container override is actually necessary: if the table already
// agrees with what the source document used, effectiveContainerType would
// fall back to the identical answer anyway, so recording it is redundant.
// Skipping the redundant record keeps the zero/low-alloc parse fast path
// intact for the overwhelming majority of real-world XMP — standard
// dc:/tiff:/exif:/xmpMM:/… properties written per spec — while still
// recording (and thus correctly preserving on round trip) any property
// whose container the table has never heard of, or gets wrong for this
// specific document (task #273).
func specTableMatches(ns, local, ctype string) bool {
	m, ok := arrayProperties[ns]
	if !ok {
		return false
	}
	ct, ok := m[local]
	return ok && ct == ctype
}

// effectiveContainerType determines whether the top-level property (ns,
// local) must be serialised using an RDF collection container (rdf:Alt/Seq/
// Bag) and, if so, which kind. local is the parent/top-level property name —
// for struct-in-list properties this is the bracket-free parent (e.g.
// "History"), matching how recordContainerType (rdf.go) and
// writeStructInListProperty (write.go) key their lookups.
//
// Priority order (task #273 / ISO 16684-1 §7.5 round-trip fidelity):
//  1. x.containerTypes — the container kind actually observed for this exact
//     property when x was produced by Parse. This takes priority over the
//     spec table so that ANY namespace — including unknown/custom ones the
//     table below can never enumerate — round-trips with its original
//     container structure preserved exactly, closing the class of
//     corruption where a plain Parse → unrelated field write → Encode
//     silently downgraded an untouched array property to a bare scalar.
//  2. arrayProperties (via isCollectionProperty/collectionType) — for
//     properties with no parse-time record, i.e. properties newly created
//     by Set()/the named setters, which have no source document to
//     preserve and must fall back to the spec-sourced default.
//
// Returns ctype == "" and isColl == false when neither source identifies
// (ns, local) as a collection.
func (x *XMP) effectiveContainerType(ns, local string) (ctype string, isColl bool) {
	if x != nil && x.containerTypes != nil {
		if m, ok := x.containerTypes[ns]; ok {
			if ct, ok := m[local]; ok {
				return ct, true
			}
		}
	}
	if isCollectionProperty(ns, local) {
		return collectionType(ns, local), true
	}
	return "", false
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
