---
name: allocs-per-run-no-parallel
description: testing.AllocsPerRun panics when called inside a t.Parallel() test; use //nolint:paralleltest on these functions
metadata:
  type: feedback
---

`testing.AllocsPerRun` calls `runtime.ReadMemStats` and disables GC, which is incompatible with parallel test execution. If called inside a `t.Parallel()` test it panics with "testing: AllocsPerRun called during parallel test".

**Why:** Go 1.22+ enforces this constraint; `AllocsPerRun` needs exclusive control over the GC to get accurate counts.

**How to apply:** Any test that uses `testing.AllocsPerRun` must NOT call `t.Parallel()`. Suppress the `paralleltest` linter warning with `//nolint:paralleltest // testing.AllocsPerRun panics in parallel tests` on the function declaration line.
