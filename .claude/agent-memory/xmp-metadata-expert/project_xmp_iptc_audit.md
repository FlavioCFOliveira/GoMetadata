---
name: XMP/IPTC Spec Audit — 2026-04-03 (updated 2026-06-09, 2026-07-06 final-cert re-audit, 2026-07-06 DC-fix re-confirm, 2026-07-07 severity correction)
description: Full re-audit of xmp/ and format/jpeg/ after hardening round; all prior HIGH/MEDIUM findings from 2026-06-09 confirmed FIXED at HEAD as of 2026-07-06; DC array-allowlist gap CLOSED by commit 3b72a26 (task #272); 2026-07-07 pass found the sibling gap is MUCH larger and MORE severe than first assessed — read the 2026-07-07 section below before citing the "LOW, Set()-only" framing further down, which is superseded
type: project
---

## 2026-07-07 severity correction (supersedes the 2026-07-06 "LOW, Set()-only" framing below)

The 2026-07-06 note below classified the non-DC array-coverage gap as LOW
severity because "no named setter reaches these, only the low-level Set()
escape hatch." That reachability analysis was INCOMPLETE — it had not
checked whether xmp.Properties preserves the source file's original RDF
container type across a round trip. **It does not**: Properties is a flat
`map[string]map[string]string` (xmp/xmp.go) with no per-property
container-type tag, so `collectionType`/`isCollectionProperty`
(xmp/namespace.go) are consulted on EVERY Encode() call for EVERY property
present, regardless of how it got there. A plain Parse() → (unrelated field
write) → Encode() round trip — no explicit Set() call on the affected
property required — silently corrupts every uncovered array-typed property
present in the source file. Direct violation of CLAUDE.md's "existing
metadata not explicitly modified is preserved exactly" requirement, not a
narrow edge case.

Two additional findings, more severe than "missing coverage":
1. **xmpMM:Ingredients / xmpMM:Pantry are actively WRONG today, not just
   uncovered.** namespace.go's #43 fix cites "Adobe XMP Spec Part 2 §1.2.8"
   for coding History/Ingredients/Pantry all as Seq. Cross-checked against
   adobe/xmp-docs GitHub table, developer.adobe.com, and exiv2.org's xmpMM
   tag table (3 independent sources agree): only History is seq
   ResourceEvent; Ingredients is **bag** ResourceRef; Pantry is **bag**
   struct. xmpMM:Versions (seq Version) is missing entirely and falls to
   the wrong Bag default. The #43 comment in namespace.go is incorrect for
   Ingredients/Pantry and needs correcting, with a regression test proving
   the old (wrong) Seq output no longer appears.
2. **Lang-Alt "lang|value" internal-separator leak into visible XML text.**
   rdf.go's onCharDataListItem (line ~482) stores non-"x-default"-language
   Alt items as "lang|value" (e.g. "fr|Bonjour") with no other tag.
   write.go's dispatch (line ~116-129) only strips this prefix inside
   writeMultiValuedProperty's Alt branch; a single-item Alt property NOT in
   isCollectionProperty takes the writeSimpleProperty path with the RAW
   still-prefixed value, so the literal string "fr|Bonjour" — internal
   implementation-detail syntax — gets written into the actual metadata
   value in the output file. Affects every Lang-Alt property outside
   dc:/xmpMM: (tiff:ImageDescription, tiff:Copyright, exif:UserComment,
   xmpRights:UsageTerms, Iptc4xmpExt:Event) and is worse than "wrong
   container" — it's visible garbage in the metadata value itself.

**Full corrected property list** (namespace → property → correct container,
cross-checked against adobe/xmp-docs GitHub tables + developer.adobe.com +
exiv2.org tag tables):
- xmpMM: Ingredients→Bag (was wrongly Seq), Pantry→Bag (was wrongly Seq),
  Versions→Seq (missing entirely)
- xmpRights: Owner→Bag, UsageTerms→Alt
- tiff: ImageDescription→Alt, Copyright→Alt (both HIGH real-world frequency
  — MWG multi-schema writes by Lightroom/Bridge/many cameras keep dc: and
  tiff: copies in lockstep), BitsPerSample/TransferFunction/
  YCbCrSubSampling→Seq(Integer), PrimaryChromaticities/
  ReferenceBlackWhite/WhitePoint/YCbCrCoefficients→Seq(Rational)
- exif: UserComment→Alt, ISOSpeedRatings/SubjectArea/SubjectLocation→
  Seq(Integer)
- xmp: (XMP Basic, namespace not currently written by any named setter)
  Identifier→Bag(Text), Thumbnails→Alt(Thumbnail struct)
- photoshop: DocumentAncestors→Bag, TextLayers→Seq, SupplementalCategories→
  Bag
- Iptc4xmpCore: Scene→Bag, SubjectCode→Bag
- Iptc4xmpExt: PersonInImage/PersonInImageWDetails/OrganisationInImageCode/
  OrganisationInImageName/ProductInImage/PropertyReleaseID/AboutCvTerm/
  CVterm/ModelAge/EventId/EmbdEncRightsExpr/Genre/ImageRegion/
  LinkedEncRightsExpr/RegistryId/LocationShown/LocationCreated/
  ArtworkOrObject→Bag; Event→Alt

Confirmed CLEAN (no array-typed properties at all, verified this round):
pdf:, aux:, cc:, geo:.

Reachability: named setters touch none of the above (grep of xmp.go's
public methods confirms only dc:/tiff:Model/exif DateTimeOriginal+GPS/
photoshop:DateCreated/aux:Lens are touched by named setters — none overlap
this list). Set() reaches all of them, AND — the corrected part — a plain
read-modify-write round trip reaches all of them too, with no Set() call on
the affected property needed, because of the flat-map architecture above.

Recommendation given to go-performance-architect (not a unilateral
decision): fix is the same mechanical collectionType/isCollectionProperty
extension pattern as the DC fix, EXCEPT Ingredients/Pantry need a
correction + regression test (not just addition), and given this is the
second reactive per-namespace gap found in as many audit passes, a single
comprehensive spec-sourced table covering every registered namespace in one
pass was suggested as preferable to further incremental patching.

## 2026-07-06 DC-fix re-confirmation (commit 3b72a26, task #272)

Re-verified via Read of xmp/namespace.go, xmp/write.go, xmp/xmp.go,
xmp/xmp_test.go + WebFetch of developer.adobe.com/xmp/docs/xmp-namespaces/dc/
(mirrors XMP Spec Part 1 Appendix 8.3).

**The 2026-07-06 "5/8 DC coverage" finding is now CLOSED.** `collectionType`/
`isCollectionProperty` in xmp/namespace.go cover all 11 array-typed dc:
properties with correct containers (creator/date→Seq;
subject/contributor/language/publisher/relation/type→Bag;
title/description/rights→Alt) — cross-checked value-for-value against
Adobe's authoritative dc: table and matched exactly. dc:format/identifier/
source/coverage (Simple, not arrays) correctly excluded.
TestCollectionTypeDCArrayProperties + TestSingleValueDCArrayPropertiesUse
CollectionContainer (xmp/xmp_test.go:313-397) directly assert Encode()-level
single-value → correct-container behaviour for the 6 newly added properties,
including a negative assertion against the bare-scalar form. Solid, correct,
spec-aligned.

### NEW finding (2026-07-06, LOW, broader-scope sibling of the closed DC gap)
`isCollectionProperty`/`collectionType` are hard-scoped to `ns == NSdc` and
`ns == NSxmpMM` only (namespace.go:56-88, 117-132). Every OTHER namespace
constant the library declares/prefix-binds (NSxmpRights, NSphotoshop,
NSiptcCore, NSiptcExt, …) falls through to `isCollectionProperty`→false and
`collectionType`→default "Bag" regardless of the correct spec container.
Confirmed via Adobe's authoritative property tables that these namespaces DO
contain array-typed properties not covered:
- xmpRights:Owner (Bag ProperName), xmpRights:UsageTerms (Alt)
- photoshop:DocumentAncestors (Bag), photoshop:TextLayers (Seq),
  photoshop:SupplementalCategories (Bag)
- Iptc4xmpCore:Scene/SubjectCode (Bag) and multiple Iptc4xmpExt bag
  properties (not exhaustively enumerated this round — verify at next audit)

Two distinct symptoms in xmp/write.go:114-237:
1. Single-value case (no \x1e separator): `isCollectionProperty==false` →
   `writeSimpleProperty` emits a bare scalar — identical §7.5 violation the
   DC fix just closed, just in a different namespace.
2. Multi-value case (\x1e separator IS present, so the array-detection
   heuristic correctly fires): `writeMultiValuedProperty` still calls
   `collectionType(ns, local)`, which returns the wrong container for
   Alt-typed non-dc/non-xmpMM properties (e.g. xmpRights:UsageTerms with
   multiple language variants gets wrapped in rdf:Bag instead of rdf:Alt).
   This is arguably worse than symptom 1 because it fires even when the
   separator-based heuristic already correctly identifies the value as an
   array.

Reachability: LOW, same profile as the pre-fix DC gap. Grep of xmp/xmp.go's
named setters (SetCaption, SetCopyright, SetCreator, SetCreators,
AddKeyword, SetKeywords, SetCameraModel, SetDateTimeOriginal, SetDateCreated,
SetGPS, SetLensModel) confirms zero named setters touch NSxmpRights,
NSiptcCore, or NSiptcExt at all, and photoshop is touched only for
DateCreated (a simple Date, not an array). The only path to trigger either
symptom is the low-level public `Set(ns, local, value)` escape hatch; no
existing test exercises it.

Fix is the same mechanical pattern as 3b72a26: extend collectionType's
switch with `case NSxmpRights: switch local { "Owner": return "Bag";
"UsageTerms": return "Alt" }`, `case NSphotoshop: switch local {
"DocumentAncestors", "SupplementalCategories": return "Bag";
"TextLayers": return "Seq" }`, plus Iptc4xmpCore/Iptc4xmpExt bag properties
(enumerate exhaustively before implementing — this round only spot-checked
xmpRights and photoshop against Adobe's authoritative tables), and mirror
each case into isCollectionProperty.

## 2026-07-06 final-certification re-audit (commits bc35f99, e81d364, HEAD 3de8d2f)

Verified via static code review (no Bash/go test execution available to this
agent in that session — verification was via Read/Grep of source + existing
test assertions + cross-checking for conflicting/regressed tests).

### All 2026-06-09 findings now CONFIRMED FIXED
- HIGH "Unknown-NS prefix collision on Encode" — FIXED. xmp/write.go's
  serialise() now pre-populates `usedPrefixes` with every well-known prefix
  that will appear in the packet, then `uniquePrefixFor` (namespace.go)
  assigns ns0/ns1/… only from the remaining unused pool per call, so two
  distinct unknown namespaces in one document always get distinct prefixes.
- HIGH "writeXMLEscaped does not filter XML-illegal C0 control characters" —
  FIXED. write.go's writeXMLEscaped now replaces XML 1.0 §2.2 forbidden C0
  ranges (U+0000-08, 0B-0C, 0E-1F) and U+FFFE/U+FFFF with U+FFFD.
- NSgeo prefix fix (bc35f99): `prefixMap[NSgeo] = "geo"` added in namespace.go;
  regression test TestEncodeGeoNamespaceUsesConventionalPrefix in
  xmp/xmp_test.go asserts xmlns:geo=, geo:long, absence of xmlns:ns, and
  round-trip through Parse→GPS(). Confirmed present and correct.
- isCollectionProperty fix (e81d364): namespace.go now has an explicit
  isCollectionProperty(ns, local) allowlist (creator/subject/description/
  rights/title for dc:, History/Ingredients/Pantry for xmpMM:) consulted in
  write.go's topProps loop so a single-item array-typed property is still
  serialised via its rdf:Seq/Bag/Alt container, never as a bare scalar
  element. Directly exercised by TestConformance_RECONCILE05/
  plain-to-xdefault-on-write (metadata_conformance_test.go) which proves the
  single-item dc:description case emits `<rdf:Alt><rdf:li xml:lang=
  "x-default">…`. No test anywhere in the repo asserts a bare-scalar Encode()
  form for these five properties — all bare-scalar dc: fixtures found are
  Parse-side (read-tolerance, RDF-04 shorthand-equivalence) fixtures, not
  Encode-side assertions, so the fix does not conflict with existing tests.

### RETRACTED 2026-06-09 finding
- "PNG buildXMPChunk writes XMP uncompressed regardless of size" — this is
  NOT a defect. docs/conformance/xmp.md rule PNG-02 ("iTXt compression flag
  MUST be 0") correctly documents that Adobe XMP Spec Part 3 §1.6 MANDATES
  uncompressed iTXt specifically for the XMP chunk in PNG — this overrides
  the generic PNG §11.3.4 recommendation to compress large text chunks,
  which only applies to ordinary tEXt/iTXt text metadata, not the
  XML:com.adobe.xmp packet. My 2026-06-09 note conflated the two rules.
  compFlag=0-always is the spec-correct implementation. Do not re-raise this.

### NEW finding (2026-07-06, LOW severity, narrow scope)
- **isCollectionProperty/collectionType DC-schema coverage gap**: the
  allowlist added by e81d364 covers only 5 of the 8 array-typed Dublin Core
  properties defined in Adobe XMP Spec Part 1 Appendix 8.3 (confirmed against
  exiv2's dc: tag table as a cross-check). Covered: dc:creator (seq
  ProperName), dc:subject (bag Text, via default-Bag fallback),
  dc:description/dc:rights/dc:title (Lang Alt). NOT covered: dc:contributor
  (bag ProperName), dc:date (seq Date), dc:language (bag Locale),
  dc:publisher (bag ProperName), dc:relation (bag Text), dc:type (bag Open
  Choice). If a caller uses the public, generic `x.Set(NSdc, "contributor",
  "Alice")` (or date/language/publisher/relation/type) with a single value,
  Encode emits a bare scalar element — the identical class of ISO 16684-1
  §7.5 violation that e81d364 just fixed for the other five properties, just
  out of that fix's scope.
  - Reachability is LOW: no high-level named setter exists for any of these
    six properties anywhere in the codebase (grep across the whole repo
    found zero uses of these six local names outside namespace.go/write.go
    itself); the only path is the low-level `Set()` escape hatch; no
    existing test exercises them; MWG/IPTC reconciliation never touches them.
  - Fix is trivial and mechanical: add the six missing (ns, local) → Bag/Seq
    cases to both collectionType and isCollectionProperty in
    xmp/namespace.go, mirroring the existing pattern exactly.
  - This is a pre-existing gap discovered during the 2026-07-06
    certification audit, not a regression introduced by bc35f99/e81d364.

## Fixes confirmed present from earlier rounds (retained from 2026-06-09 note)
1. appendUTF8Rune: decodeUTF32 validates cp before calling appendUTF8Rune (encoding.go:167-169). FIXED.
2. unsafe.String aliasing: unescapeXML now uses string(b) (rdf.go:1067). FIXED.
3. parseHex/parseDec overflow: both check v > unicode.MaxRune and surrogate range (rdf.go:1211, 1240). FIXED.
4. rdf:Alt x-default: xDefaultValue replaces firstValue for Alt semantics (xmp.go:379). FIXED.
5. collectionType: xmpMM:History/Ingredients/Pantry now return Seq (namespace.go:48-53). FIXED.
6. onStartListItem xml:lang namespace check: checks a.ns=="" || a.ns==xml-NS (rdf.go:262). FIXED.

## Residual MEDIUM/LOW items from 2026-06-09 not re-verified this round
(Not in scope of the 2026-07-06 task; re-check before next certification pass.)
- mergeExtendedChunks: no gap/overlap/completeness validation (jpeg.go).
- reassembleExtendedXMP: string-literal splice fragile on non-default rdf: prefix (jpeg.go).
- TIFF Inject wire-frame guard status unconfirmed this round (format/tiff/tiff.go).
- Extended XMP GUID hex validation status unconfirmed this round (jpeg.go).
- extTruncated flag discarded — status unconfirmed this round (jpeg.go).

**Why:** Reliability/compliance audits for library hardening QA and production-readiness certification gates.
**How to apply:** The DC-schema coverage gap above is the one actionable finding from the 2026-07-06 round; everything else from 2026-06-09 HIGH is closed. Re-verify the "not re-verified this round" MEDIUM/LOW list at the next audit pass — do not assume they are still open or still closed without re-checking.
