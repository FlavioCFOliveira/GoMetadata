---
name: project_heif_reliability_fixes_1140781
description: HEIF/AVIF reliability audit findings #106/#133/#137/#169/#177 fixed in commit 1140781; panic gates, construction_method, compatible_brands, magicLen
metadata:
  type: project
---

Commit 1140781 (`fix(heif): harden infe/iloc/meta parsing + AVIF brand detection`) fixed 5 audit findings:

**#169 [CRITICAL]** `findMetaBoxAbs` now rejects meta boxes < 12 bytes (minimum FullBox size). Added `metaFullBoxMinSize = 12` constant. Previously `contentOff = absStart+12 > metaAbsEnd` → `data[12:8]` panic in `buildInjectComponents`.

**Why:** ISO 14496-12 §8.11.1 meta is FullBox; header[8]+version/flags[4]=12 minimum.
**How to apply:** Any future change to findMetaBoxAbs or buildInjectComponents must preserve the `e-s < metaFullBoxMinSize` guard.

**#106 [CRITICAL]** `parseInfeV0V1`: added `if pos+2 > len(data) { return id, "" }` before `pos += 2` for `item_protection_index`. Previously unconditional advance caused `bytes.IndexByte(data[pos:], 0)` to panic when body had only item_ID.

**#177 [MEDIUM]** `parseIlocItemSimple`: now reads `constructMethod` for iloc v1/v2 with bounds check. If `constructMethod != 0`, returns `itemLoc{}` (zero) and advances past remaining fields. Previously field was consumed but silently discarded — method-1/2 items were mis-resolved as file-absolute.

**#133 [LOW]** `readIlocSimpleExtents`: added `if pos+indexSize > len(ilocData) { return ..., false }` for extent_index. `parseIlocItemSimple` now propagates `ok` from that call instead of `_ = ok`. Truncated extent → item omitted, not zero-offset recorded.

**#137 [LOW]** `detectHEIFBrand` refactored to accept full buffer from offset 8 (not just 4-byte major brand). Scans `compatible_brands` (b[8:]) for `avif`/`avis`/`av01`. Handles libavif's `mif1`+`avif` pattern. Added MA1A/MA1B major brand support. `magicLen` increased from 12 to 36 bytes.

Gates: `TestHEIFInjectMetaTooSmallForFullBox`, `TestHEIFRobustInfeV0V1Truncated`, `TestHEIFRobustIlocConstructionMethod1`, `TestReadIlocSimpleExtentsTruncatedIndex`, `TestAVIFMIF1Brand`. All in `heif_conformance_test.go` and `detect_test.go`.
