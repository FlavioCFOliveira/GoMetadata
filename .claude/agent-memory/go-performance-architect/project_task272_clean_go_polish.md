---
name: project_task272_clean_go_polish
description: Task #272 Clean-GO final polish — DC array allowlist completion, IPTC doc hygiene, V-15 named test
metadata:
  type: project
---

Task #272 (Clean-GO sprint final polish, 2026-07-06), commits 3b72a26 + 8890788, HEAD before
was 3de8d2f.

**EDIT 1 (xmp/namespace.go, xmp/xmp_test.go):** `isCollectionProperty`/`collectionType` covered
only 5 of 11 array-typed dc: properties (creator/subject/title/description/rights). Added the
remaining 6 per Adobe XMP Spec Part 1 Appendix 8.3 (dc: namespace reference table — verified via
WebFetch against developer.adobe.com/xmp/docs/xmp-namespaces/dc/, not guessed):
dc:contributor=Bag(ProperName), dc:date=Seq(Date), dc:language=Bag(Locale),
dc:publisher=Bag(ProperName), dc:relation=Bag(Text), dc:type=Bag(open Choice/Text).
dc:format/identifier/source confirmed Simple (Text/MIMEType), correctly NOT added.
Tests: `TestCollectionTypeDCArrayProperties`, `TestSingleValueDCArrayPropertiesUseCollectionContainer`
(table-driven, one sub-test per new property, verified red-before/green-after via `git stash`).

**EDIT 2 (docs/conformance/iptc.md):** removed stale "(Known gap ... must drive a fix)" wording
on IIM-REC-02 (line ~33) and IRB-APP13-09 (line ~52) — both already fixed and covered by
`TestIIMREC02`/`TestIIMREC02RoundTrip` and `TestIRBAPP1309`/`TestROBUST15` in iptc/conformance_test.go.
Doc-only, rule semantics unchanged.

**EDIT 3 (format/tiff/conformance_bigtiff_write_test.go):** V-15 (uint64 arithmetic throughout
BigTIFF relocation) had no dedicated named sub-test — only exercised indirectly by S-41/S-42/R-18.
Added `TestConformance_V15_uint64_relocation_arithmetic` with two sub-test families:
- primitive: `readUint(elemSz=8, ...)` fed a value > math.MaxUint32 — the ONLY way to actually
  falsify a uint32-truncation regression, since this package's own maxFileSize cap (256 MiB) means
  no real fixture will ever legitimately need an offset that large. Confirmed load-bearing by
  deliberately injecting `uint64(uint32(order.Uint64(b)))` into readUint's elemSz==8 branch —
  primitive sub-test failed, fixture sub-tests (on real small files) still PASSED even with the bug
  injected. This proves real small BigTIFF fixtures cannot by themselves detect a uint32-truncation
  regression in this arithmetic — the primitive-value sub-test is not redundant with S-41/S-42/R-18.
- fixture: `assertV15LONG8OffsetsPreserved` helper, run over always-committed `testdata/big_cramps_le.tif`
  + `big_cramps_be.tif` (guarantees non-skip coverage in every checkout) plus corpus-gated
  `BigTIFFLong8.tif`/`BigTIFFLong8Tiles.tif` (skip gracefully, corpus gitignored).

Pipeline: gofmt/goimports/go vet/go build/go test -race -count=1 ./.../golangci-lint run ./... —
all clean, 0 lint issues, full suite green.
