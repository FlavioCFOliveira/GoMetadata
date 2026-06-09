---
name: project_heif_avif_conformance_battery
description: HEIF/HEIC and AVIF conformance batteries (tasks #159 and #160) in format/heif/
metadata:
  type: project
---

Tasks #159 (HEIF) and #160 (AVIF) delivered two conformance test files in `format/heif/`:

- `heif_conformance_test.go` — ISO BMFF §4 + HEIF §5 battery (10 BMFF-box tests, 4 brand tests, 6 EXIF-item tests, 3 XMP-item tests, 1 cdsc test, 5 write tests, 12 robustness tests, 3 corpus tests)
- `avif_conformance_test.go` — AVIF §6 battery (4 brand tests, 4 EXIF-item tests, 3 XMP-item tests, 2 meta tests, 2 write tests, 6 robustness tests, 3 corpus tests)

**CRITICAL fix included:** `heif.go Extract()` now returns `(nil, nil, nil, nil)` for empty input. Root cause: `io.ReadFull` returns `(0, io.EOF)` (not `io.ErrUnexpectedEOF`) when the reader is empty. The guard `!errors.Is(rerr, io.ErrUnexpectedEOF)` did not catch EOF, so the empty-file path was wrapped as an error. Fixed by adding an explicit `errors.Is(rerr, io.EOF)` early-return branch. Confirmed: `TestHEIFRobustEmptyFile` and `TestAVIFRobustEmptyFile` now pass.

**Audit #106 infe OOB:** `TestHEIFRobustInfeOOB` and `TestAVIFRobustInfeOOB` confirmed no panic on truncated iinf — the existing `parseIinf`/`parseInfe` bounds-checks already cover this.

**Lint notes:**
- `gocritic appendCombine` fires when 2+ consecutive `append(slice, ...)` calls can be merged; combine into one `append(slice, a, b, c, ...)` call
- `nolintlint "unused directive"` means gosec does NOT actually fire on that line; remove the nolint annotation entirely
- `unparam` on test helper functions: use `//nolint:unparam` on the function signature line with a clear reason
- `errcheck` on `defer f.Close()`: use `t.Cleanup(func() { _ = f.Close() })` instead

**Why:** ISO BMFF §4 / HEIF §5 / AVIF §6 spec compliance + audit finding #106 critical panic.

**How to apply:** When writing ISOBMFF conformance tests, use the `bmffBox`, `bmffFullBox`, `bmffFtyp`, `bmffInfeV2`, `bmffIinf`, `bmffIloc` helpers defined in heif_conformance_test.go. For corpus tests use `t.Cleanup(func() { _ = f.Close() })` not `defer f.Close()`.
