---
name: feedback-nolint-intrange-range2
description: Using `for i := range 2` already satisfies intrange linter — do NOT add a nolint:intrange directive
type: feedback
---

Using `for i := range N` (Go 1.22+ integer range) already satisfies the `intrange` linter. Adding `//nolint:intrange` to such a loop causes nolintlint to fire with "directive is unused". Only add `//nolint:intrange` when the loop has the classic `for i := 0; i < N; i++` form and cannot be converted (e.g. because `i` is used as an offset multiplier in a way that `range N` doesn't cleanly support).

**Why:** Learned from exif_test.go #39 regression test — converted to `range 2`, the nolint comment became stale and triggered a separate lint error.

**How to apply:** When the linter suggests converting to integer range and you do so, drop any `//nolint:intrange` directive on that same line.
