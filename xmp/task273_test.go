package xmp

// task273_test.go — regression battery for task #273 (Clean-GO sprint):
// XMP round-trip container-type corruption.
//
// Root cause (fixed by the containerTypes field in xmp.go + recordContainerType
// in rdf.go + effectiveContainerType in namespace.go): Properties was a flat
// map with no per-property RDF container-type tag, so collectionType /
// isCollectionProperty (namespace.go) were the SOLE authority on every
// Encode() call for EVERY property present, regardless of how it got there.
// A plain Parse() → (unrelated field write) → Encode() round trip — no Set()
// call on the affected property required — silently downgraded any
// array-typed property NOT in the allowlist to a bare scalar, in ANY
// namespace including unknown/custom ones the allowlist can never
// enumerate. Two further defects shared the same blast radius:
//   - xmpMM:Ingredients/Pantry were wrongly coded as Seq instead of Bag, and
//     xmpMM:Versions was missing entirely.
//   - A single-item rdf:Alt property outside the (then-narrow) allowlist
//     took the writeSimpleProperty path with its raw "lang|value"-prefixed
//     storage form intact, leaking the internal "lang|" separator into the
//     visible output text.
//
// Sources cross-checked for every table entry added by this fix:
// exiv2.org/tags-xmp-<ns>.html (transcribes the Adobe XMP Specification
// Part 1/2 and IPTC Photo Metadata Standard 2025.1 property-reference
// tables' "Value type" column verbatim), adobe/xmp-docs, developer.adobe.com.

import (
	"regexp"
	"strings"
	"testing"
)

// ── (A) Round-trip container preservation for ANY namespace ────────────────

// TestTask273RoundTripPreservesSourceContainerType proves the architectural
// fix: a single-item array-typed property survives Parse → Encode → Parse
// with its ORIGINAL source container (rdf:Seq/Bag/Alt) intact — never
// downgraded to a bare scalar element — for properties both inside and
// outside the spec-sourced arrayProperties table, including a namespace this
// package has never heard of. This is the direct proof that container
// provenance, not the allowlist, is now the primary authority on Encode.
func TestTask273RoundTripPreservesSourceContainerType(t *testing.T) {
	t.Parallel()

	const customNS = "http://example.com/custom-ns/1.0/"

	raw := xmpDoc(
		`<rdf:Description rdf:about=""` +
			` xmlns:tiff="` + NStiff + `"` +
			` xmlns:exif="` + NSexif + `"` +
			` xmlns:xmpRights="` + NSxmpRights + `"` +
			` xmlns:cust="` + customNS + `">` +
			`<tiff:ImageDescription><rdf:Alt><rdf:li xml:lang="x-default">A single caption</rdf:li></rdf:Alt></tiff:ImageDescription>` +
			`<exif:ISOSpeedRatings><rdf:Seq><rdf:li>400</rdf:li></rdf:Seq></exif:ISOSpeedRatings>` +
			`<xmpRights:UsageTerms><rdf:Alt><rdf:li xml:lang="x-default">All rights reserved</rdf:li></rdf:Alt></xmpRights:UsageTerms>` +
			`<cust:Tags><rdf:Bag><rdf:li>onlyitem</rdf:li></rdf:Bag></cust:Tags>` +
			`</rdf:Description>`,
	)

	x := mustParse(t, raw)

	// Sanity: values are readable via the flat Properties map before any
	// round trip, exactly as they would be for a normal Get() call.
	if got := x.Get(NStiff, "ImageDescription"); got != "A single caption" {
		t.Fatalf("pre-encode Get(tiff:ImageDescription) = %q", got)
	}

	encoded := mustEncode(t, x)
	out := string(encoded)

	cases := []struct {
		name        string
		ctype       string
		bareScalar  string // must NOT appear
		wantElement string // must appear: local element open tag immediately followed by its container
	}{
		{
			name:        "tiff:ImageDescription (Alt, in spec table)",
			ctype:       "Alt",
			bareScalar:  "<tiff:ImageDescription>A single caption</tiff:ImageDescription>",
			wantElement: "<tiff:ImageDescription>\n    <rdf:Alt>",
		},
		{
			name:        "exif:ISOSpeedRatings (Seq, in spec table)",
			ctype:       "Seq",
			bareScalar:  "<exif:ISOSpeedRatings>400</exif:ISOSpeedRatings>",
			wantElement: "<exif:ISOSpeedRatings>\n    <rdf:Seq>",
		},
		{
			name:        "xmpRights:UsageTerms (Alt, in spec table)",
			ctype:       "Alt",
			bareScalar:  "<xmpRights:UsageTerms>All rights reserved</xmpRights:UsageTerms>",
			wantElement: "<xmpRights:UsageTerms>\n    <rdf:Alt>",
		},
	}
	for _, tc := range cases {
		if strings.Contains(out, tc.bareScalar) {
			t.Errorf("%s: encoded as bare scalar %q — container downgraded:\n%s", tc.name, tc.bareScalar, out)
		}
		if !strings.Contains(out, tc.wantElement) {
			t.Errorf("%s: missing expected %q:\n%s", tc.name, tc.wantElement, out)
		}
	}

	// The custom/unknown namespace property gets a generated prefix (ns0,
	// ns1, …), so match its container structurally rather than by literal
	// prefix. This is the case the spec-sourced allowlist can NEVER cover —
	// proof that source-container preservation, not the table, is doing the
	// work here.
	custRe := regexp.MustCompile(`:Tags>\s*<rdf:Bag>\s*<rdf:li>onlyitem</rdf:li>\s*</rdf:Bag>`)
	if !custRe.MatchString(out) {
		t.Errorf("custom-namespace cust:Tags: expected rdf:Bag container preserved, got:\n%s", out)
	}
	if strings.Contains(out, ">onlyitem</") && !custRe.MatchString(out) {
		t.Errorf("cust:Tags appears to have been downgraded to a bare scalar:\n%s", out)
	}

	// Re-parse the encoded output and confirm values and containers are
	// stable under a SECOND round trip (Parse → Encode → Parse → Encode).
	x2 := mustParse(t, encoded)
	if got := x2.Get(NStiff, "ImageDescription"); got != "A single caption" {
		t.Errorf("round-trip Get(tiff:ImageDescription) = %q", got)
	}
	if got := x2.Get(NSexif, "ISOSpeedRatings"); got != "400" {
		t.Errorf("round-trip Get(exif:ISOSpeedRatings) = %q", got)
	}
	if got := x2.Get(NSxmpRights, "UsageTerms"); got != "All rights reserved" {
		t.Errorf("round-trip Get(xmpRights:UsageTerms) = %q", got)
	}
	if got := x2.Get(customNS, "Tags"); got != "onlyitem" {
		t.Errorf("round-trip Get(custom:Tags) = %q", got)
	}

	encoded2 := mustEncode(t, x2)
	out2 := string(encoded2)
	for _, tc := range cases {
		if strings.Contains(out2, tc.bareScalar) {
			t.Errorf("second round trip: %s downgraded to bare scalar:\n%s", tc.name, out2)
		}
	}
	if !custRe.MatchString(out2) {
		t.Errorf("second round trip: cust:Tags lost its rdf:Bag container:\n%s", out2)
	}
}

// ── (C) Lang-Alt internal-separator leak ────────────────────────────────────

// TestTask273LangAltNoInternalSeparatorLeak proves that the internal
// "lang|value" storage convention used by onCharDataListItem (rdf.go) for
// non-x-default rdf:Alt items never leaks into the serialised output text,
// for a single-item Alt collection in a namespace that (at the time of the
// original defect) was NOT in the isCollectionProperty allowlist.
func TestTask273LangAltNoInternalSeparatorLeak(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ns     string
		local  string
		prefix string
		lang   string
		value  string
	}{
		{
			name: "tiff:ImageDescription single fr item", ns: NStiff, local: "ImageDescription",
			prefix: "tiff", lang: "fr", value: "Bonjour",
		},
		{
			name: "exif:UserComment single de item", ns: NSexif, local: "UserComment",
			prefix: "exif", lang: "de", value: "Hallo Welt",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := xmpDoc(
				`<rdf:Description rdf:about="" xmlns:` + tc.prefix + `="` + tc.ns + `">` +
					`<` + tc.prefix + `:` + tc.local + `>` +
					`<rdf:Alt><rdf:li xml:lang="` + tc.lang + `">` + tc.value + `</rdf:li></rdf:Alt>` +
					`</` + tc.prefix + `:` + tc.local + `>` +
					`</rdf:Description>`,
			)
			x := mustParse(t, raw)

			// Confirm the internal storage form really is "lang|value" before
			// Encode — otherwise this test would not be exercising the leak path.
			if got := x.Get(tc.ns, tc.local); got != tc.lang+"|"+tc.value {
				t.Fatalf("internal storage form = %q, want %q|%q", got, tc.lang, tc.value)
			}

			out := string(mustEncode(t, x))

			leaked := tc.lang + "|" + tc.value
			if strings.Contains(out, leaked) {
				t.Errorf("%s: internal separator leaked into output as %q:\n%s", tc.name, leaked, out)
			}
			wantLang := `xml:lang="` + tc.lang + `"`
			if !strings.Contains(out, wantLang) {
				t.Errorf("%s: missing %s in output:\n%s", tc.name, wantLang, out)
			}
			wantValue := ">" + tc.value + "<"
			if !strings.Contains(out, wantValue) {
				t.Errorf("%s: missing value %q in output:\n%s", tc.name, wantValue, out)
			}

			// Round-trip: re-parsed value must still resolve correctly (lang tag
			// correctly reattached, not swallowed into the value).
			x2 := mustParse(t, []byte(out))
			if got := x2.Get(tc.ns, tc.local); got != tc.lang+"|"+tc.value {
				t.Errorf("%s: round-trip storage form = %q, want %q|%q", tc.name, got, tc.lang, tc.value)
			}
		})
	}
}

// ── (B) xmpMM Ingredients/Pantry/Versions regressions ──────────────────────

// TestTask273XmpMMIngredientsPantryEmitBag is a regression gate proving that
// xmpMM:Ingredients and xmpMM:Pantry are now serialised as rdf:Bag — and,
// critically, that rdf:Seq (the old, wrong container) no longer appears
// anywhere in their output.
func TestTask273XmpMMIngredientsPantryEmitBag(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSxmpMM: {
			"Ingredients[0].instanceID": "xmp.iid:ingredient-1",
			"Pantry[0].instanceID":      "xmp.iid:pantry-1",
		},
	}}
	out := string(mustEncode(t, x))

	if !strings.Contains(out, "<rdf:Bag>") {
		t.Fatalf("expected xmpMM:Ingredients/Pantry to use rdf:Bag:\n%s", out)
	}
	if strings.Contains(out, "<rdf:Seq>") {
		t.Errorf("task #273 regression: xmpMM:Ingredients/Pantry must NOT be serialised as rdf:Seq:\n%s", out)
	}

	x2 := mustParse(t, []byte(out))
	if got := x2.Get(NSxmpMM, "Ingredients[0].instanceID"); got != "xmp.iid:ingredient-1" {
		t.Errorf("round-trip Ingredients[0].instanceID = %q", got)
	}
	if got := x2.Get(NSxmpMM, "Pantry[0].instanceID"); got != "xmp.iid:pantry-1" {
		t.Errorf("round-trip Pantry[0].instanceID = %q", got)
	}
}

// TestTask273XmpMMVersionsEmitsSeq is a regression gate proving that the
// previously-missing xmpMM:Versions table entry now emits rdf:Seq instead of
// silently falling through to the "Bag" default.
func TestTask273XmpMMVersionsEmitsSeq(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{
		NSxmpMM: {"Versions[0].modifier": "photographer"},
	}}
	out := string(mustEncode(t, x))

	if !strings.Contains(out, "<rdf:Seq>") {
		t.Errorf("xmpMM:Versions should be serialised as rdf:Seq:\n%s", out)
	}
	if strings.Contains(out, "<rdf:Bag>") {
		t.Errorf("xmpMM:Versions must NOT be serialised as rdf:Bag:\n%s", out)
	}
}

// ── Comprehensive spec-table coverage ────────────────────────────────────────

// TestTask273ArrayPropertyTableEntries exercises every (ns, local) entry
// added to namespace.go's arrayProperties table by task #273 (excluding dc:
// and the xmpMM entries, which have dedicated tests above / from task #272).
// For each entry it proves, end-to-end through the public API:
//  1. collectionType/isCollectionProperty agree with the table.
//  2. A single value set via the public Set() (i.e. with NO parse-time
//     container record at all) is still serialised through its rdf:Seq/Bag/
//     Alt container, never as a bare scalar — proving the spec-table
//     fallback path (priority 2 in effectiveContainerType) works
//     independently of the containerTypes/Parse-provenance path.
func TestTask273ArrayPropertyTableEntries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ns, local, ctype string
	}{
		// xmpRights — exiv2.org/tags-xmp-xmpRights.html.
		{NSxmpRights, "Owner", "Bag"},
		{NSxmpRights, "UsageTerms", "Alt"},
		// xmp (XMP Basic) — exiv2.org/tags-xmp-xmp.html.
		{NSxmp, "Identifier", "Bag"},
		{NSxmp, "Thumbnails", "Alt"},
		{NSxmp, "Advisory", "Bag"},
		// tiff — exiv2.org/tags-xmp-tiff.html.
		{NStiff, "ImageDescription", "Alt"},
		{NStiff, "Copyright", "Alt"},
		{NStiff, "BitsPerSample", "Seq"},
		{NStiff, "TransferFunction", "Seq"},
		{NStiff, "YCbCrSubSampling", "Seq"},
		{NStiff, "WhitePoint", "Seq"},
		{NStiff, "PrimaryChromaticities", "Seq"},
		{NStiff, "YCbCrCoefficients", "Seq"},
		{NStiff, "ReferenceBlackWhite", "Seq"},
		// exif — exiv2.org/tags-xmp-exif.html.
		{NSexif, "UserComment", "Alt"},
		{NSexif, "ISOSpeedRatings", "Seq"},
		{NSexif, "SubjectArea", "Seq"},
		{NSexif, "SubjectLocation", "Seq"},
		// photoshop — exiv2.org/tags-xmp-photoshop.html.
		{NSphotoshop, "DocumentAncestors", "Bag"},
		{NSphotoshop, "TextLayers", "Seq"},
		{NSphotoshop, "SupplementalCategories", "Bag"},
		// Iptc4xmpCore — exiv2.org/tags-xmp-iptc.html.
		{NSiptcCore, "Scene", "Bag"},
		{NSiptcCore, "SubjectCode", "Bag"},
		{NSiptcCore, "AltTextAccessibility", "Alt"},
		{NSiptcCore, "ExtDescrAccessibility", "Alt"},
		// Iptc4xmpExt — exiv2.org/tags-xmp-iptcExt.html.
		{NSiptcExt, "PersonInImage", "Bag"},
		{NSiptcExt, "PersonInImageWDetails", "Bag"},
		{NSiptcExt, "OrganisationInImageCode", "Bag"},
		{NSiptcExt, "OrganisationInImageName", "Bag"},
		{NSiptcExt, "ProductInImage", "Bag"},
		{NSiptcExt, "PropertyReleaseID", "Bag"},
		{NSiptcExt, "AboutCvTerm", "Bag"},
		{NSiptcExt, "CVterm", "Bag"},
		{NSiptcExt, "ModelAge", "Bag"},
		{NSiptcExt, "EventId", "Bag"},
		{NSiptcExt, "EmbdEncRightsExpr", "Bag"},
		{NSiptcExt, "Genre", "Bag"},
		{NSiptcExt, "ImageRegion", "Bag"},
		{NSiptcExt, "LinkedEncRightsExpr", "Bag"},
		{NSiptcExt, "RegistryId", "Bag"},
		{NSiptcExt, "LocationShown", "Bag"},
		{NSiptcExt, "LocationCreated", "Bag"},
		{NSiptcExt, "ArtworkOrObject", "Bag"},
		{NSiptcExt, "AOCreator", "Seq"},
		{NSiptcExt, "AOTitle", "Alt"},
		{NSiptcExt, "Event", "Alt"},
	}

	for _, tc := range tests {
		t.Run(tc.ns+"/"+tc.local, func(t *testing.T) {
			t.Parallel()
			if got := collectionType(tc.ns, tc.local); got != tc.ctype {
				t.Errorf("collectionType(%q, %q) = %q, want %q", tc.ns, tc.local, got, tc.ctype)
			}
			if !isCollectionProperty(tc.ns, tc.local) {
				t.Errorf("isCollectionProperty(%q, %q) = false, want true", tc.ns, tc.local)
			}

			// End-to-end: a single value set via the public Set() API (no
			// parse-time provenance at all) must still serialise through the
			// rdf:Seq/Bag/Alt container, never as a bare scalar.
			x := &XMP{}
			const value = "single-item-value"
			x.Set(tc.ns, tc.local, value)

			out := string(mustEncode(t, x))
			prefix, ok := prefixMap[tc.ns]
			if !ok {
				t.Fatalf("no canonical prefix registered for %q", tc.ns)
			}
			bareScalar := "<" + prefix + ":" + tc.local + ">" + value + "</" + prefix + ":" + tc.local + ">"
			if strings.Contains(out, bareScalar) {
				t.Errorf("%s:%s encoded as bare scalar %q; want rdf:%s container:\n%s",
					prefix, tc.local, bareScalar, tc.ctype, out)
			}
			if !strings.Contains(out, "<rdf:"+tc.ctype+">") {
				t.Errorf("%s:%s missing expected rdf:%s container:\n%s", prefix, tc.local, tc.ctype, out)
			}
		})
	}
}
