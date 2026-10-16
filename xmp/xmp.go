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
//
// Parse exposes the properties of a packet in XMP.Properties under flat
// keys: "name" for a simple value or an array of simple items, "parent.field"
// for a struct field, and "parent[N].field" for a field of a struct array
// item (see XMP.Properties). The composed keys ("parent.field",
// "parent[N].field") are limited to 4 × the document size + 4 KiB in total;
// keys past that limit are omitted from Properties, and Encode still writes
// their properties back. Everything that has no flat key — deeper structures
// and qualifiers — is kept internally and written back unchanged.
package xmp

// Adapted from Adobe XMP-Toolkit-SDK XMPCore/source/XMPMeta-GetSet.cpp:XMPMeta::SetLocalizedText,ChooseLocalizedText,AppendLangItem, Copyright 2003 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// See THIRD_PARTY_NOTICES.md for the licence note on this file. Only the
// x-default case of setDefaultText is adapted.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// XMP holds the parsed XMP properties organised by namespace URI.
//
// Thread-safety: not safe for concurrent use. Write-path helpers (SetCaption,
// SetCopyright, SetCreator, SetKeywords, putProp, and every other setter) are
// not designed to be called concurrently on the same *XMP — putProp writes
// directly into the Properties map, and two goroutines racing on the same
// inner map trigger "fatal error: concurrent map writes", which the Go
// runtime cannot recover from. Only read accessors (Caption, Copyright,
// Creator, Keywords, GPS, and similar) are safe to call concurrently with
// each other, provided no Set* call is in flight. Callers that need
// concurrent mutation must provide their own synchronisation (see
// gometadata.Metadata, whose Set* methods serialise on an internal mutex).
type XMP struct {
	// Properties maps namespace URI → property key → value string.
	//
	// Key conventions (unchanged since the first release):
	//   - a simple property is stored under its local name ("Model");
	//   - an array of simple items (rdf:Alt, rdf:Seq, rdf:Bag) is stored under
	//     its local name with the items joined by the Unicode record
	//     separator U+001E; an rdf:Alt item whose xml:lang is not x-default
	//     is written "lang|value";
	//   - a simple field of a struct is stored under "parent.field";
	//   - a simple field of a struct item of an array is stored under
	//     "parent[N].field", N counting items from 0.
	//
	// Struct fields are stored in the namespace of the top-level property.
	// A qualified value written with rdf:value (ISO 16684-1:2019 §7.9.2.5)
	// is a simple value with qualifiers, not a struct: it is stored as
	// "name" = value, and its qualifiers have no key. Deeper structures (a
	// struct inside a struct field, an array inside a struct) and qualifiers
	// other than xml:lang have no key either; Parse keeps them in an internal
	// tree and Encode writes them back unchanged, together with every
	// property whose keys were not modified.
	//
	// The composed keys ("parent.field", "parent[N].field") repeat the
	// property name, so their total size is limited to 4 × the document size
	// + 4 KiB. Keys past that limit are omitted from this map; Encode still
	// writes their properties back. Real documents stay far below the limit.
	Properties map[string]map[string]string

	// nodes is the retained part of the parsed XMP data model tree
	// (ISO 16684-1:2019 §6): nodes[0] is the root, its children are schema
	// nodes, and their children are the top-level properties that the flat
	// Properties view cannot reproduce exactly — properties with qualifiers,
	// nested structures, URI values, empty items, a container other than the
	// one Encode would choose, or keys that exceed the budget above (tasks
	// #303, #277). Every other property lives only in Properties. nodes is nil
	// when the whole document is representable by Properties, and for every
	// XMP value built by a caller rather than by Parse.
	//
	// Encode compares each retained property with its Properties entries:
	// an unmodified property is written from the tree, a modified one is
	// merged with the new entries, and a property whose entries were all
	// deleted is omitted. Deliberately unexported, like the rest of the
	// read-path provenance below: a caller edits Properties, never the tree.
	nodes nodeList

	// about is the rdf:about value of the top-level rdf:Description elements
	// ("" when absent), toolkit the x:xmptk attribute of x:xmpmeta, and
	// prefixes the namespace prefixes that the source document bound to
	// namespaces without a well-known prefix (nil when there are none).
	// Encode writes all three back so that a round trip keeps them.
	about    string
	toolkit  string
	prefixes map[string]string

	// arena is a compact, append-only, result-owned byte buffer holding a
	// copy of every string that Parse keeps — Properties keys and values,
	// retained node names and values, namespace URIs and prefixes — and
	// NOTHING else. Every such string is an unsafe.String view into a region
	// of arena, produced by intern or written directly (joined array values
	// and composed keys, see project.go).
	//
	// A string is copied into arena only at the moment Parse decides to keep
	// it, never eagerly while scanning: comments, whitespace, markup, unused
	// namespace declarations and ignored attributes never reach arena. A
	// namespace URI is copied once per Parse call however many nodes use it
	// (XMPARENA-NS-REINTERN-01), and composed keys are bounded by the budget
	// described on Properties (task #277), so arena stays proportional to the
	// input size.
	//
	// Safety invariants — ALL must hold for the lifetime of the returned *XMP:
	//  1. arena is never handed to a sync.Pool or any other buffer reuse
	//     mechanism — it is a plain heap slice owned exclusively by this *XMP.
	//  2. Bytes already appended to arena are never overwritten — growth via
	//     append() only ever extends a NEW region or (when reallocating)
	//     copies old content into a NEW backing array, never writes into an
	//     already-issued string's backing bytes. This is what makes it safe
	//     for a string produced by an EARLIER intern() call to keep pointing
	//     at an OLDER backing array after a LATER intern() call triggers
	//     growth/reallocation — see intern's doc.
	//  3. arena strictly outlives every string derived from it, via Go's
	//     normal GC interior-pointer keep-alive rules.
	//  4. Only intern and the projection helpers in project.go append to
	//     arena, and only during Parse.
	arena []byte
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
// XMP Part 1 §7.1 p. 9 and §7.2: a UTF-16 or UTF-32 packet is transcoded to
// UTF-8 before parsing; the encoding is recognised from the byte-order mark,
// or from the first bytes when there is none (for example 00 3C 00 3F is
// UTF-16BE).
//
// Parse returns an error that wraps ErrXMLNotWellFormed, and names the
// element or the namespace prefix, when the packet is not well-formed XML: a
// mismatched or stray end tag, a malformed start tag, an element that is never
// closed, or an element or attribute whose namespace prefix is not declared
// (Namespaces in XML 1.0 §5). It never returns a tree built from part of such
// a packet.
//
// The values of literal property elements are verbatim, white space
// included (XML 1.0 §2.10). The values of attributes are normalised as XML
// 1.0 §3.3.3 requires: each literal TAB, LF and CR becomes a space, while a
// character reference to one of them (&#9;) stays the character. A property that is one of the 35 registered
// aliases (see ResolveAlias) is folded into its base property, as the Adobe
// XMP Toolkit does (Adobe XMP Specification Part 2 §1.1.1): after Parse a
// document holds one value for it, at the base property.
//
// Parse rejects inputs larger than maxXMPDocumentBytes (16 MiB) after
// UTF-8 normalisation to prevent O(n) parsing of pathological documents.
func Parse(b []byte) (*XMP, error) {
	body, err := documentBody(b)
	if err != nil {
		return nil, err
	}

	x := &XMP{Properties: make(map[string]map[string]string, 8)}

	// body is read directly (transient, read-only, synchronous-only; see
	// transientAliasString's doc) and is NEVER retained by x. parseRDF sizes
	// x.arena once, from the content it decides to keep (see finish in
	// project.go), so the arena does not scale with comments, padding or
	// other content that is scanned but not stored.
	if err := parseRDF(body, x); err != nil {
		return nil, err
	}

	return x, nil
}

// documentBody returns the part of b that Parse reads: b normalised to
// UTF-8 (XMP Part 1 §7.2), limited to maxXMPDocumentBytes, and reduced to
// the XMP packet when b carries a <?xpacket?> wrapper (XMP Part 1 §7.3);
// without a wrapper the whole input is the document body.
func documentBody(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrEmptyInput
	}

	// Normalise to UTF-8 so that the BOM-based encoding path is handled once,
	// here, rather than duplicated across Scan and parseRDF.
	// normaliseToUTF8 is a zero-copy no-op for UTF-8 input.
	normalised := normaliseToUTF8(b)
	if normalised == nil {
		return nil, ErrEmptyInput
	}

	// Document-level size cap (post-normalisation, pre-RDF-parsing).
	// See maxXMPDocumentBytes for the design rationale.
	if len(normalised) > maxXMPDocumentBytes {
		return nil, ErrDocumentTooLarge
	}

	if body := Scan(normalised); body != nil {
		return body, nil
	}
	return normalised, nil
}

// Encode serialises x to a padded XMP packet ready for embedding in a
// container segment. The packet is UTF-8 encoded with a read/write
// <?xpacket?> wrapper per XMP §7.
//
// For an x returned by Parse, every property whose Properties entries were
// not modified is written exactly as parsed — nested structures,
// qualifiers, URI values, empty arrays and the source's array containers
// included — together with rdf:about, x:xmptk and the prefixes of
// namespaces without a well-known prefix. A modified entry changes only the
// value it names; deleting every entry of a property removes it. Encode does
// not modify x and is safe to call concurrently with other readers of x.
func Encode(x *XMP) ([]byte, error) {
	return serialise(x)
}

// CameraModel returns the tiff:Model property (XMP Part 1 §8.4, p. 27), or ""
// when the document has none. xmp:CreatorTool is not a fallback: it names the
// software that created the file (the alias target of tiff:Software), not the
// camera.
func (x *XMP) CameraModel() string {
	if x == nil {
		return ""
	}
	return x.getProp(NStiff, "Model")
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

// Creators returns all dc:creator values in document order (XMP §8.3;
// ISO 16684-1 §7.5 — dc:creator is an ordered rdf:Seq). Mirrors Keywords()'s
// U+001E-split logic exactly; order is preserved because both the internal
// join and the rdf:Seq encoder (collectionType) iterate in slice/string
// order (RECONCILE-04).
func (x *XMP) Creators() []string {
	if x == nil {
		return nil
	}
	v := x.getProp(NSdc, "creator")
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

// Get returns the property value for the given namespace URI and local name.
// Returns "" when the property is absent or when x is nil.
func (x *XMP) Get(ns, local string) string {
	if x == nil {
		return ""
	}
	return x.getProp(ns, local)
}

// SetCaption sets the default (x-default) text of dc:description to s
// (XMP §8.3). The other language alternatives of the property are kept, as
// described at setDefaultText.
func (x *XMP) SetCaption(s string) { x.setDefaultText(NSdc, "description", s) }

// SetCopyright sets the default (x-default) text of dc:rights to s
// (XMP §8.3). The other language alternatives of the property are kept, as
// described at setDefaultText.
func (x *XMP) SetCopyright(s string) { x.setDefaultText(NSdc, "rights", s) }

// setDefaultText sets the x-default item of the language alternative (ns,
// local) to value and leaves its other items alone, following the x-default
// case of XMPMeta::SetLocalizedText (Adobe XMP Toolkit, XMPCore/source/
// XMPMeta-GetSet.cpp, line 889) and XMP Part 1 §8.2.2.4 (language
// alternatives; x-default is the item that applications use when they have no
// better match):
//
//   - no item: the property becomes the single x-default item;
//   - an x-default item exists: the Toolkit first swaps it with the first item
//     (ChooseLocalizedText finds it by an exact match), then gives the new
//     value to every other item that holds the old x-default text — a
//     translation that merely repeated it — and to x-default itself;
//   - no x-default item (every item has a language): the new x-default item
//     is inserted first (AppendLangItem) and the others stay as they are.
//
// The flat form of the property is the one of Properties: items joined with
// U+001E, the x-default item bare and every other item as "lang|value". An
// item written "x-default|value" is read as x-default too.
func (x *XMP) setDefaultText(ns, local, value string) {
	old := x.getProp(ns, local)
	if old == "" {
		x.putProp(ns, local, value)
		return
	}
	items := strings.Split(old, "\x1e")

	xd, oldDefault := findDefaultItem(items)
	if xd < 0 {
		// No x-default item: it is inserted first and the items stay.
		x.putProp(ns, local, value+"\x1e"+old)
		return
	}

	// ChooseLocalizedText finds the x-default item by an exact match and
	// SetLocalizedText swaps it with the first item. Every other item that
	// holds the old x-default text gets the new text, then x-default does.
	items[0], items[xd] = items[xd], items[0]
	for i := 1; i < len(items); i++ {
		if lang, val, tagged := cutAltItem(items[i]); tagged && val == oldDefault {
			items[i] = lang + "|" + value
		} else if !tagged && items[i] == oldDefault {
			items[i] = value
		}
	}
	items[0] = value
	x.putProp(ns, local, strings.Join(items, "\x1e"))
}

// findDefaultItem returns the index of the x-default item of items (the first
// bare item, or the first "x-default|…") and its text, or -1.
func findDefaultItem(items []string) (index int, text string) {
	for i, item := range items {
		lang, val, tagged := cutAltItem(item)
		if !tagged {
			return i, item
		}
		if lang == "x-default" {
			return i, val
		}
	}
	return -1, ""
}

// cutAltItem splits the flat form of a language alternative item, "lang|value",
// into its language and value. An item whose text before the first '|' is not
// a language tag (RFC 5646: ASCII letters, digits and '-') is a bare item that
// merely contains a '|', such as the x-default text "a | b", and is not split.
func cutAltItem(item string) (lang, val string, tagged bool) {
	lang, val, tagged = strings.Cut(item, "|")
	if !tagged || !isLangTag(lang) {
		return "", item, false
	}
	return lang, val, true
}

// isLangTag reports whether s is non-empty and made of ASCII letters, digits
// and '-' only, the characters of a language tag (RFC 5646).
func isLangTag(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

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

// SetDateCreated sets photoshop:DateCreated to t formatted as RFC 3339
// (Adobe XMP Specification Part 2 §1.2.7; IPTC Photo Metadata Mapping
// Guidelines 2025.1 — photoshop:DateCreated is the XMP equivalent of IIM
// 2:55 Date Created + 2:60 Time Created, RECONCILE-03).
//
// Do not confuse this with xmp:CreateDate, which records the digitization
// date (the XMP analogue of EXIF 0x9004 DateTimeDigitized) — a distinct
// field that this method never writes. See the IPTC/XMP reconciliation
// spec §0.1.
func (x *XMP) SetDateCreated(t time.Time) {
	x.putProp(NSphotoshop, "DateCreated", t.Format(time.RFC3339))
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

// SetCreators replaces dc:creator entirely with creators, in order (full
// replace, mirrors SetKeywords — RECONCILE-04). Passing an empty (or nil)
// slice deletes the property so Creators() returns nil after a round-trip.
//
// A creator string containing U+001E (this package's internal multi-value
// separator) has that byte stripped before storage; an embedded U+001E
// would otherwise corrupt the split performed by Creators() on the next
// read (IPTC/XMP reconciliation spec §4, defensive requirements).
func (x *XMP) SetCreators(creators []string) {
	if x == nil {
		return
	}
	if len(creators) == 0 {
		// Delete the property entirely so Creators() returns nil.
		if x.Properties != nil {
			delete(x.Properties[NSdc], "creator")
		}
		return
	}
	cleaned := make([]string, len(creators))
	for idx, c := range creators {
		cleaned[idx] = strings.ReplaceAll(c, "\x1e", "")
	}
	x.putProp(NSdc, "creator", strings.Join(cleaned, "\x1e"))
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
// Storage convention (established by joinItems in project.go):
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
	// A bare item is x-default or unlabelled per the joinItems convention.
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
