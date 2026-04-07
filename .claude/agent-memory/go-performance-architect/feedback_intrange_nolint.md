---
name: intrange + modernize nolint pattern for binary parsers
description: Loops using i*12 as byte-slice offset multiplier cannot use for-range-int; need both intrange and modernize suppressed
type: feedback
---

Binary IFD/MakerNote parsers use `for i := 0; i < count; i++` where `i` appears as `base + i*12` (byte-slice offset). Converting to `for i := range count` is semantically identical but the linter `modernize` fires under a `rangeint` diagnostic, separate from `intrange`.

**Why:** `intrange` and `modernize` are two separate linters that both flag the same loop pattern. A `//nolint:intrange` alone leaves `modernize` firing.

**How to apply:** When a loop variable is used only as an offset multiplier (`i*12`), suppress both: `//nolint:intrange,modernize // binary parser: loop variable is a byte-slice offset multiplier`

Exception: `exif/ifd.go` uses `int(count)` cast which `modernize` does not flag — so only `//nolint:intrange` is needed there. Always check nolintlint for unused directives.

Also: the `min` builtin in production code can be shadowed by `func min(a, b int) int` in `fuzz_test.go` files (compiled only for tests). When the test build fails with type mismatch on `min`, revert to if/else and add a comment explaining why `min` cannot be used.
