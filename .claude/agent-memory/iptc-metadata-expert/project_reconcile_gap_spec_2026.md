---
name: project-reconcile-gap-spec-2026
description: Spec delivered 2026-07-06 to close docs/conformance/iptc.md §4 RECONCILE-01..05 gap (date bridging, byline sequencing) — key corrections and design decisions
metadata:
  type: project
---

## Context

2026-07-06: go-performance-architect (via orchestrator) asked for an implementation-ready
spec to close the last IPTC conformance gap: RECONCILE-01..05 in docs/conformance/iptc.md §4
had no tests, and the original RECONCILE-02 text ("XMP always wins") was WRONG — the actual
implemented+tested behaviour is MWG v2.0 §3.3.1 digest-based reconciliation (see
[[project_mwg02_iptc_digest]]), proven by TestConformance_MWG02.

**Why:** closing this gap moves IPTC conformance from GO-with-caveats to clean GO.

## Key corrections made to the original ask (important — don't re-derive these)

1. **`xmp:CreateDate` != `photoshop:DateCreated`.** The task prompt conflated them. Verified
   via IPTC Photo Metadata Mapping Guidelines (WebFetch): IIM 2:55/2:60 (Date/Time Created) map
   to `photoshop:DateCreated` ONLY. `xmp:CreateDate` is the digitization-date field (≈ EXIF
   0x9004 DateTimeDigitized; in practice also associated with legacy IIM 2:62/2:63 Digital
   Creation Date/Time, per ExifTool/Adobe convention — medium-high confidence, could not
   independently re-verify against MWG's primary PDF text this session since it's image-scanned
   and web.archive.org / exiftool.org fetches were blocked). Any future date-bridging work must
   NOT write xmp:CreateDate as part of the DateCreated bridge.
2. **`IPTC.SetCreator` vs `XMP.SetCreator` have different replace semantics** (IIM: first-only,
   preserves rest; XMP: full property replace). A new plural `SetCreators`/`IPTC.SetCreators`
   must not be built by composing the existing singular setters — needs its own full-replace
   path on both sides, mirroring the existing `SetKeywords`/`IPTC.SetKeywords` pattern exactly.
3. **Year-overflow edge case**: IIM 2:55 is fixed 8-octet CCYYMMDD. `time.Time.Format("2006")`
   for year <0 or >9999 breaks the fixed width. Any `IPTC.SetDateCreated` implementation MUST
   guard `t.Year()` into [0,9999] and no-op the IPTC write (not EXIF/XMP) outside that range.
4. IIM 2:60 TimeCreated exact Go recipe: `t.Format("150405-0700")` — the `-0700` layout (no
   "Z0700") is required because IIM has no zone-letter form, only a signed 4-digit offset
   (renders "+0000" for UTC). 2:55: `t.Format("20060102")`.
5. MWG-02 digest elevation (`iptcTrustElevated`) is deliberately NOT extended to date fields
   in this spec — it's only consulted today by Caption/Copyright/Creator/Keywords. Flagged as
   a possible separate follow-up, not bundled into this gap-closure.
6. MWG list-handling convention for flattening a multi-value dc:creator/By-line list into the
   single-string EXIF Artist (0x013B) tag: join with `"; "` (semicolon+space). Corroborated by
   multiple secondary sources + ExifTool MWG.pm reference implementation (ListToString/
   StringToList); could not pin an exact MWG PDF section number this session (same PDF
   text-extraction limitation as above) — cite generically as "MWG Guidelines v2.0, list
   handling for single-string EXIF fields" rather than inventing a section number.

## Deliverable shape (full spec was returned as agent message, not written to a file)

- Corrected RECONCILE-01..05 rule text (pasteable into docs/conformance/iptc.md §4).
- New setters specified: `iptc.IPTC.SetDateCreated(t)`, `iptc.IPTC.SetCreators([]string)`,
  `xmp.XMP.SetDateCreated(t)`, `xmp.XMP.SetCreators([]string)`, `xmp.XMP.Creators() []string`
  (new getter — none existed before, `Creator()` only returns first item via firstValue).
  Top-level: recommended extending existing `Metadata.SetDateTimeOriginal` to also write IPTC
  (Option A, my recommendation) rather than adding a separately-named SetDateCreated (Option B) —
  flagged as an open decision needing sign-off, not silently forced.
  New `Metadata.SetCreators([]string)` — writes IPTC (ordered), XMP (ordered rdf:Seq — encoder
  already supports Seq for dc:creator via `collectionType` in xmp/namespace.go, no encoder
  change needed), EXIF Artist (flattened "; "-joined).
- Field constraints table (2:55 max 8/non-rep, 2:60 max 11/non-rep, 2:80 max 32/repeatable,
  EXIF 0x9003 fixed 20B, EXIF 0x013B unbounded, photoshop:DateCreated n/a, dc:creator
  ordered-Seq n/a).
- Test list: TestConformance_RECONCILE01/03/04/05 (new, named subtests = rule IDs verbatim,
  per project convention) + TestConformance_MWG02 already satisfies RECONCILE-02 (comment-only
  update, no new test). Plus prerequisite unit tests in iptc/iptc_test.go and xmp/xmp_test.go.

**How to apply:** If asked to review/audit the implementation of this spec later, check these
five points were honoured, especially #1 (xmp:CreateDate must stay untouched) and #3 (year
guard) — these are the two most likely places for a spec-faithful-looking but subtly wrong
implementation to slip through.
