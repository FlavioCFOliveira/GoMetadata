---
name: allocs-per-run-no-parallel
description: testing.AllocsPerRun panics when called inside a t.Parallel() test; use //nolint:paralleltest on these functions
metadata:
  type: feedback
---

`testing.AllocsPerRun` calls `runtime.ReadMemStats` and disables GC, which is incompatible with parallel test execution. If called inside a `t.Parallel()` test it panics with "testing: AllocsPerRun called during parallel test".

**Why:** Go 1.22+ enforces this constraint; `AllocsPerRun` needs exclusive control over the GC to get accurate counts.

**How to apply:** Any test that uses `testing.AllocsPerRun` must NOT call `t.Parallel()`. Suppress the `paralleltest` linter warning with `//nolint:paralleltest // testing.AllocsPerRun panics in parallel tests` on the function declaration line.

**Extends to any timing/allocation-sensitive test, not just `AllocsPerRun` literally** (task #255, EXIF-IFDCHAIN-01 lint cleanup): a parent test with subtests that assert wall-clock ceilings or cumulative-allocation bounds should also skip `t.Parallel()` on every subtest to avoid flaky measurements under CPU contention. Doing so satisfies `tparallel` (which only complains when a parent test omits `t.Parallel()` while ITS OWN subtests call it) but then trips the opposite-direction linter `paralleltest` (which wants every test/subtest to call `t.Parallel()`). Resolve by putting a single `//nolint:paralleltest // <rationale>` on the top-level test function — this suppresses the whole call tree (parent + subtests) in one directive; do not try to silence `tparallel` instead, since removing `t.Parallel()` already satisfies it.
