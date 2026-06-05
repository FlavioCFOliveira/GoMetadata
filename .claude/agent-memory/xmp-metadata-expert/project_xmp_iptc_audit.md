---
name: XMP/IPTC Spec Audit — 2026-04-03 (updated 2026-06-01, re-audit 2026-06-01)
description: Full re-audit of xmp/ and format/jpeg/ after hardening round; all 6 original fixes verified; residual issues catalogued
type: project
---

Re-audit conducted 2026-06-01 against ISO 16684-1, Adobe XMP Spec Parts 1-3.

## XMP Score: 9/10 (up from 7/10)

All 6 originally-flagged defects are FIXED. No new BLOCKER found.

### Fix verification summary

1. **ExtendedXMP APP1 identifier** — FIXED. `identXMPNote` at jpeg.go:49 is
   `"http://ns.adobe.com/xmp/extension/\x00"` (35 bytes + NUL). Correct per
   Adobe XMP Spec Part 3 §1.1.4. Wire-frame round-trip implemented via
   `encodeXMPWire` / `decodeXMPWire`; `rawXMPWire` plumbed through
   `ExtractWithWire`, `metadata.go`, and `Inject`.

2. **Struct and array-of-struct serialization** — FIXED. `classifyProps` +
   `writeStructProperty` + `writeStructInListProperty` in write.go correctly
   emit `rdf:parseType="Resource"` wrappers. No dots in element names.
   Round-trip test `TestStructInArrayRoundTrip` passes.

3. **storeProperty first-wins** — FIXED. `storeProperty` in rdf.go:1219-1228
   guards with `if x.Properties[ns][local] != ""` before write.
   `onCharDataSimple` and `onCharDataStructField` have matching guards.

4. **Namespace scope push/pop** — FIXED. `nsDepth [101]nsDepthEntry` stack
   and `popNSScope()` in `onEndElement` correctly restore `nsCount`.
   `TestNamespaceScopePopping` with 40 sibling blocks passes.

5. **UTF-16/UTF-32 BOM decode** — FIXED. `xmp/encoding.go` implements
   `detectEncoding`, `toUTF8`, `decodeUTF32`, `appendUTF8Rune`,
   `normaliseToUTF8`. Called from both `Parse` and `Scan`.

6. **CDATA sections** — FIXED. `isCDATA`, `parseCDATA`, `collectTextContent`
   in rdf.go handle interleaved CDATA. Character data delivered without entity
   expansion (correct per XML 1.0 §2.7).

7. **Depth underflow guard** — FIXED. `onEndElement` opens with
   `if p.depth <= 0 { return }` at rdf.go:192-194.

### Residual defects

#### MAJOR
- **appendUTF8Rune: no code-point range validation** — encoding.go:150-174.
  Code points > U+10FFFF and surrogate pairs U+D800–U+DFFF are silently emitted
  as multi-byte sequences. The `default` branch in the switch covers cp > 0xFFFF
  without bounding to 0x10FFFF, producing up to 4 bytes for cp up to 2^32-1.
  Crafted UTF-32 LE/BE input can inject ill-formed UTF-8 sequences into the
  parse buffer, causing downstream string/XML corruption. Should guard:
  `if cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF) { continue/replace }`.

#### MINOR (unchanged from April, not yet fixed)
- **`collectionType` incomplete** — xmpMM:History, xmpMM:Ingredients default to
  rdf:Bag instead of rdf:Seq. namespace.go:30-40.
- **Unknown namespace fallback prefix collision** — all unknown namespaces get
  prefix "ns"; two unknown namespaces in same document → invalid XML.
- **Missing type-support namespaces in prefixMap** — stEvt, stRef, stDim,
  stVer, xmpBJ, xmpTPg, xmpDM, xmpG, xmpGImg.
- **`xml:lang` without namespace check** — `onStartListItem` checks
  `a.loc == "lang"` without verifying `a.ns` is `""` or
  `"http://www.w3.org/XML/1998/namespace"`. Could match a custom attribute
  named "lang" in a non-xml namespace.
- **ExtendedXMP reassembly is string-search-based** — `reassembleExtendedXMP`
  uses `bytes.LastIndex` for `</rdf:RDF>`. Correct for well-formed docs but
  fragile if that literal appears in a text node.

#### NIT (unchanged)
- **`rdf:about` not validated** across multiple rdf:Description blocks.
- **`begin=` attribute value not validated** (empty string accepted alongside
  the correct UTF-8 BOM sentinel).
- **NSxmpNote naming** — correctly two separate strings but confusingly named.

**Why:** Research audit for library hardening QA.
**How to apply:** Items below are the new findings from 2026-06-04 re-audit.

## New findings 2026-06-04

#### HIGH
- **`unsafe.String` parse-buffer aliasing** — `unescapeXML` (rdf.go:1044) uses
  `unsafe.String(unsafe.SliceData(b), len(b))` for the no-entity fast path.
  The returned string aliases the caller's `[]byte`. If the caller modifies
  their byte slice after `Parse` returns, stored property strings are silently
  corrupted. Caller-owned reusable buffers (e.g. sync.Pool) trigger this.

#### MEDIUM
- **`parseHex`/`parseDec` no overflow check** — Both functions accumulate into
  a `rune` (int32) without bounding to ≤ U+10FFFF. Long inputs like
  `&#2147483648;` or `&#x80000000;` overflow the rune and pass a negative
  or wrong code point to `bld.WriteRune`. WriteRune emits U+FFFD for negatives,
  which is functionally safe but spec-wrong (XML §4.1).

- **Multiple unknown-NS prefix collision on Encode** — `prefixOf` returns `"ns"`
  for every unrecognised namespace URI (namespace.go:83). When two unknown
  namespaces are present, `Encode` emits two `<rdf:Description xmlns:ns="...">` blocks
  with the same prefix but different URIs, producing invalid XML. `Parse(Encode(x))`
  then incorrectly maps both blocks to the same namespace.

- **`rdf:Alt` x-default selection** — `firstValue` returns the first item in
  the joined string; if `x-default` is not the first item (producer order varies),
  `Caption()`/`Copyright()` return a language-tagged value instead of the
  canonical x-default. XMP §C.2.5/P1-H requires x-default as fallback.

#### NIT (updated, appendUTF8Rune fix confirmed present)
- appendUTF8Rune: code-point validation now present (decodeUTF32 guards before
  calling appendUTF8Rune). Previously flagged MAJOR is now FIXED as of the
  current codebase (confirmed 2026-06-04: lines 167-169 in encoding.go).
