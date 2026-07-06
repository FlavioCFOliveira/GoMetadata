---
name: project_heif_iloc_offbyone_243
description: HEIF-ILOC-OFFBYONE-01 (task #243) fixed — iloc fixed-header guard was off by one in both parseIloc (read path) and parseIlocFull (Inject write path)
metadata:
  type: project
---

Finding HEIF-ILOC-OFFBYONE-01 (CRITICAL, CWE-125/193): `format/heif/heif.go`
`parseIloc` guarded its iloc body with `if len(ilocData) < 5` but then reads
two fixed size-nibble bytes — `ilocData[4]` (offset_size|length_size) and,
after `pos++`, `ilocData[5]` (base_offset_size|index_size). A 5-byte iloc
body (box size=13, the minimum legal iloc box under ISO 14496-12 §8.11.3)
passes the `< 5` guard, reads index 4 safely, then panics on index 5 —
`index out of range [5] with length 5`. No `recover()` exists anywhere in
production code, so this panic propagates out of the public `Extract` and
crashes the caller's process on any untrusted HEIF/HEIC/AVIF file.

**Fix:** widened the guard to `len(ilocData) < 6` in **both**:
- `parseIloc` (line ~1096, read path, used by `Extract`)
- `parseIlocFull` (line ~354, write path, used by `buildInjectComponents` →
  `Inject`) — this is a twin instance of the identical bug, found while
  fixing the reported one; not in the original finding but fixed under the
  regression-prevention policy since it is reachable with attacker-controlled
  input whenever a caller re-injects metadata into an existing file.

Downstream reads (`parseIinfItemCount`, `readIlocItemID`,
`readIlocSimpleExtents`, `parseIlocFullItem`) already had their own
`pos+N > len(data)` bounds checks, so no further guard widening was needed —
only the two fixed header bytes were unguarded.

**Why:** ISO 14496-12 §8.11.3 — both size-nibble bytes are mandatory fixed
fields in every iloc version, analogous to the #133 (extent_index) and #177
(construction_method) guards already in this file [[project_heif_reliability_fixes_1140781]].
**How to apply:** Any future iloc-related parser added to this file must
size its initial length guard to cover every fixed-position byte read before
the first variable-length/`pos+N` bounds-checked field — do not assume the
box's declared minimum size (5 bytes of body) equals the number of fixed
bytes actually dereferenced before the first internally-guarded read.

Regression gates: `TestHEIFIlocBody5BytesNoPanic` (read path, asserts all-nil
metadata) and `TestHEIFInjectIlocBody5BytesNoPanic` (write path, asserts no
panic) in `heif_conformance_test.go`. Both confirmed to reproduce the exact
`index out of range [5] with length 5` panic when the fix is reverted.
Fuzz seeds committed at `format/heif/testdata/fuzz/FuzzHEIFExtract/seed_iloc_body_5_bytes`
and `format/heif/testdata/fuzz/FuzzHEIFInject/seed_iloc_body_5_bytes` (41-byte
reproducer: `ftyp(heic)` + `meta` FullBox + `iloc` FullBox with a 5-byte body).
`go test -fuzz=FuzzHEIFExtract` and `-fuzz=FuzzHEIFInject` both ran 45s at
0 crashers post-fix.
