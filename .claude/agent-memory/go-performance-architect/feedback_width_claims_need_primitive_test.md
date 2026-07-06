---
name: feedback_width_claims_need_primitive_test
description: When a spec rule claims a value is carried at a wider width (e.g. uint64) than any bounded real fixture can exercise, add a primitive-level out-of-range test, not just fixture round-trips
metadata:
  type: feedback
---

For any conformance rule whose claim is "this value is carried in a wider type than strictly
needed for files this package will ever actually produce/accept" (e.g. V-15: BigTIFF relocation
arithmetic stays uint64 even though `maxFileSize` bounds every real value to well under 4 GiB),
fixture-based round-trip tests on real (necessarily small/bounded) files CANNOT detect a regression
that narrows the type — the narrowed value is still correct for small numbers.

**Why:** verified directly during task #272 (commit 8890788): deliberately injected
`uint64(uint32(order.Uint64(b)))` into `readUint`'s elemSz==8 branch in
`format/tiff/relocate.go`. The fixture-based sub-tests (`big_cramps_le/be.tif`,
`BigTIFFLong8.tif`) still PASSED with the bug present — only a primitive-level sub-test that feeds
a value deliberately above `math.MaxUint32` caught it.

**How to apply:** whenever asked to write a "1:1 named test" for a rule whose text names both a
real-fixture scenario and an internal-arithmetic-width guarantee, write BOTH: (1) the fixture-based
round-trip proving the feature works on real files, AND (2) a hermetic primitive-level test that
feeds a synthetic value exceeding the narrower type's range directly into the named low-level
function (e.g. `readUint`, `decodeOffsetArray`) to prove the width claim itself, independent of
what any bounded real file could ever contain. Confirm the primitive test is load-bearing by
temporarily injecting the narrowing bug and re-running before trusting it as a regression guard —
see [[project_task272_clean_go_polish]].
