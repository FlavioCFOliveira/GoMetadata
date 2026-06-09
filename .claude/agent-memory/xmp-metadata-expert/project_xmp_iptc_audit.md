---
name: XMP/IPTC Spec Audit — 2026-04-03 (updated 2026-06-09, reliability re-audit)
description: Full re-audit of xmp/ and format/jpeg/ after hardening round; all prior fixes verified; new findings catalogued 2026-06-09; conformance checklist produced 2026-06-09
type: project
---

Re-audit conducted 2026-06-09 against ISO 16684-1, Adobe XMP Spec Parts 1-3.

## Fixes confirmed present in current code (2026-06-09)

1. appendUTF8Rune: decodeUTF32 validates cp before calling appendUTF8Rune (encoding.go:167-169). FIXED.
2. unsafe.String aliasing: unescapeXML now uses string(b) (rdf.go:1067). FIXED.
3. parseHex/parseDec overflow: both check v > unicode.MaxRune and surrogate range (rdf.go:1211, 1240). FIXED.
4. rdf:Alt x-default: xDefaultValue replaces firstValue for Alt semantics (xmp.go:379). FIXED.
5. collectionType: xmpMM:History/Ingredients/Pantry now return Seq (namespace.go:48-53). FIXED.
6. onStartListItem xml:lang namespace check: checks a.ns=="" || a.ns==xml-NS (rdf.go:262). FIXED.

## Residual defects (2026-06-09 findings)

### HIGH
- **Unknown-NS prefix collision on Encode** — prefixOf returns "ns" for ALL unknown
  namespace URIs (namespace.go:83). If an XMP document contains two properties from
  two different unknown namespaces, Encode emits two `<rdf:Description xmlns:ns="uri1">`
  and `<rdf:Description xmlns:ns="uri2">` blocks — same prefix, different URIs.
  This produces invalid XML; Parse(Encode(x)) silently conflates both under the
  first URI to be parsed. CONFIRMED still present.

- **writeXMLEscaped does not filter XML-illegal C0 control characters** — write.go:399.
  XML 1.0 §2.2 forbids U+0000-U+0008, U+000B-U+000C, U+000E-U+001F, U+FFFE, U+FFFF
  in character data. writeXMLEscaped only escapes &, <, >, ", ', \r. A property value
  containing e.g. U+0001 is written verbatim, producing a malformed XML document
  that XML parsers (including encoding/xml) will reject on read-back.

### MEDIUM
- **mergeExtendedChunks: no gap/overlap/completeness validation** — jpeg.go:1212.
  Chunks are sorted by offset then concatenated blindly. If chunks are duplicated
  (same offset, same or different data), data is doubled. If chunks have gaps between
  them, the concatenated bytes contain garbage from uninitialized positions. The
  wire-declared fullLen is not compared against the sum of chunk lengths. Adobe XMP
  Spec Part 3 §1.1.4 requires readers to validate that total chunk data equals fullLen.

- **reassembleExtendedXMP: string-literal splice is fragile** — jpeg.go:1257-1265.
  Uses bytes.Index("<rdf:Description") and bytes.LastIndex("</rdf:RDF>") on the raw
  extended XMP bytes. If the extended document uses a different prefix for rdf: (e.g.
  xmlns:r="http://www.w3.org/1999/02/22-rdf-syntax-ns#"), the search misses. If
  "</rdf:RDF>" appears inside an attribute value or CDATA section, LastIndex picks
  the wrong boundary. Adobe XMP Spec Part 3 §1.1.4 does not constrain prefix naming.

- **TIFF Inject has no wire-frame guard** — format/tiff/tiff.go:94 (Inject), also
  InjectWithEXIF, InjectWithEXIFNEF, InjectWithEXIFARW, InjectWithEXIFORF, InjectWithEXIFRW2.
  PNG (png.go:267), WebP (webp.go:166), and HEIF (heif.go:234) all have explicit guards
  that reject a JPEG wire-frame rawXMP. TIFF Inject has no such guard. A wire-frame
  payload written to TIFF tag 0x02BC would silently embed the internal binary encoding
  as XMP data, corrupting the TIFF file.

- **Extended XMP GUID is not validated as hex before use as map key** — jpeg.go:1207.
  extractGUIDFromMain checks only that the value between quotes is exactly 32 bytes;
  it does not verify they are ASCII hex digits. Non-hex GUIDs are stored as map keys
  and matched against chunk-declared GUIDs (which also come from untrusted bytes).
  This is a correctness gap (spec §1.1.4 requires MD5 hex); in degenerate cases a
  crafted main packet can produce a GUID key that matches no chunk GUID, causing
  silent reassembly failure with no error returned.

### LOW
- **extTruncated flag is silently discarded** — jpeg.go:354, 359. When extended XMP
  chunks exceed the caps (maxExtendedXMPTotal, maxExtendedXMPGUIDs), extTruncated
  is set but callers never see it. The reassembled XMP is silently partial — the
  caller has no way to distinguish "complete XMP" from "XMP truncated at 16 MiB".
  A debug/warning channel or a returned bool would let callers log the truncation.

- **PNG buildXMPChunk writes XMP uncompressed regardless of size** — png.go:659.
  The iTXt XMP chunk is always written with compFlag=0. For very large XMP packets
  (rare but legal), this can produce a PNG that exceeds the 2^31-1 byte chunk limit.
  Also, the iTXt spec (PNG §11.3.4) recommends compressed text for large payloads.
  Functional impact is low since real XMP fits comfortably uncompressed.

## Conformance checklist produced 2026-06-09
Exhaustive normative-requirements checklist produced for use as test battery contract.
Covers: spec identifiers, packet wrapper, RDF model, namespaces, value types,
embedding per container (JPEG/ExtendedXMP/TIFF/PNG/HEIF), MWG reconciliation,
and robustness cases.

**Why:** Reliability audit for library hardening QA.
**How to apply:** Items above are the findings from 2026-06-09 re-audit. Items with
HIGH/MEDIUM severity should be addressed before the next release.
