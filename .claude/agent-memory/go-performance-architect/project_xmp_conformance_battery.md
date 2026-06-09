---
name: project-xmp-conformance-battery
description: XMP conformance battery (task #154) — 67 rules, defects fixed, key design notes
metadata:
  type: project
---

XMP conformance battery implemented in `xmp/conformance_test.go` (task #154, commit 2d3866e).
All 67 rules from `docs/conformance/xmp.md` pass under `-race` with 0 lint issues.

**Why:** Reliability audit 2026-06-09 identified XMP conformance gaps; battery proves
spec compliance is structural, not accidental.

**How to apply:** Use rule IDs (PW-01..07, RDF-01..08, NS-01..04, VT-01..08, JPEG-01..08,
TIFF-01..03, PNG-01..05, HEIF-01..03, MWG-01..09, ROB-01..12) as test anchors for any
future XMP write or parse change.

Key design decisions and gotchas:
- **U+001E is dual-use**: It is both an XML 1.0 §2.2 forbidden char (0x0E–0x1F range) AND
  the library's internal multi-value property delimiter. A property value containing 0x1E
  is routed through `writeMultiValuedProperty`, not `writeSimpleProperty`. VT-08 test
  intentionally excludes 0x1E from the forbidden input — the encoding path is correct but
  the round-trip assertion doesn't apply to the delimiter byte.
- **ROB-10 fix** (`writeXMLEscaped` in `xmp/write.go`): rewritten with 3-byte lookahead
  to catch U+FFFE (EF BF BE) and U+FFFF (EF BF BF); C0 range (0x01–0x08, 0x0B, 0x0C,
  0x0E–0x1F) replaced with U+FFFD. Loop changed from `for i := range len(s)` to
  `for i := 0; i < len(s); i++` to support `i += 2` for the 3-byte lookahead.
- **ROB-11/NS-03 fix** (`uniquePrefixFor` in `xmp/namespace.go`): `serialise()` now
  pre-populates `usedPrefixes` with all canonical well-known prefixes, then calls
  `uniquePrefixFor` (not `prefixOf`) per namespace. `prefixOf` is removed as unused.
- **NUL (U+0000)** has single-byte 0x00 encoding — the outer byte loop `b <= 0x08` catches
  it correctly because 0x00 <= 0x08. Tested separately in VT-08/NUL.
