---
name: project_geo_prefix_and_iptc_overflow_fixes
description: Two disjoint production-readiness fixes (2026-07-06) — xmp geo: prefix registration and iptc.Encode extended-length overflow guard
metadata:
  type: project
---

Two independent GO-with-caveats fixes closed on 2026-07-06 (commits bc35f99, e092770).

**Fix 1 — xmp/namespace.go**: `prefixMap` had no entry for `NSgeo`
(`http://www.w3.org/2003/01/geo/wgs84_pos#`), so `uniquePrefixFor` treated it
as unknown and Encode emitted a generated `nsN:` prefix instead of the
conventional `geo:` binding. Fixed by adding `NSgeo: "geo"` to `prefixMap`.
Regression test `TestEncodeGeoNamespaceUsesConventionalPrefix` in
xmp/xmp_test.go (verified red before / green after the one-line fix).

**Fix 2 — iptc/iptc.go**: `Encode`'s extended-length branch (IIM 4.2
§1.6.2) wrote `len(ds.Value)` into a 4-byte BE field with no upper bound —
a Dataset.Value ≥ 2^32 bytes (unreachable via Parse→Encode because of
Parse's `maxIPTCTotalBytes`/`maxIPTCDatasets` caps, but reachable via direct
public-struct construction) would silently wrap the length field while
still writing every byte. Fixed with a new sentinel `ErrDatasetValueTooLarge`
(iptc/errors.go) and a bound check `uint64(n) > maxDatasetValueLen` in the
per-dataset loop in `Encode`.

**Reusable pattern — test-overridable size-cap var**: `maxDatasetValueLen`
is declared as `var ... uint64 = math.MaxUint32 //nolint:gochecknoglobals //
test-overridable cap; never mutated in production paths` — this exact
comment idiom already existed for `maxFileSize` in the root package and
`format/webp`/`format/jpeg` oom_gate_test.go files. Use this pattern
whenever a defensive bound needs a regression test but the real boundary
value would require an infeasible allocation (here: 4 GiB) to exercise
directly. Test helper `setMaxDatasetValueLenForTest(t, limit)` +
`t.Cleanup` restores the original value; test must carry
`//nolint:paralleltest` since it mutates package state. See
iptc/dataset_value_too_large_test.go for the full pattern (rejection test +
positive-control-at-boundary test, both using the real production `Encode`
path, no unsafe tricks, no multi-GiB allocation).

**Why avoided unsafe.Slice trick**: considered constructing a slice with a
lying `len()` via `unsafe.Slice(&byte, hugeLen)` to test the guard without
allocation, but the codebase has zero precedent for `unsafe.Slice` (only
`unsafe.Sizeof` appears once, in exif/task199_byteorder_flag_test.go) and
the test-overridable-var pattern was already an established, safer,
zero-lint-risk idiom in this repo — always prefer the existing convention
over introducing a new one for a one-off test.
