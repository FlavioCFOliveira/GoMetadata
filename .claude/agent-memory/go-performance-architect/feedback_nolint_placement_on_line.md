---
name: feedback-nolint-placement-on-line
description: nolint directives fire only for gosec on the exact arithmetic line; an adjacent line with a constant expression does NOT trigger G115 even with the same conversion pattern
type: feedback
---

`gosec G115` fires on `uint32(len(x))` and `uint32(n + someInt)` only where the compiler can NOT prove the value is non-negative at compile time. Constants and very simple `+uint32(const)` expressions do not trigger it.

Consequence: adding a `//nolint:gosec // G115` directive on a line that gosec does NOT fire on causes `nolintlint` to complain "unused nolint directive". Always check whether gosec actually fires on THAT specific line before adding the annotation.

**Why:** Multiple compile cycles wasted because a nolint was added pre-emptively on `exifIFDOff + uint32(ifd0Sz)` (both operands are compile-time constants) — gosec did not fire there, but did fire on `cur += uint32(len(e.val) + 1)`.

**How to apply:** Run `golangci-lint run` with the specific file/function first. Only annotate the exact statement that the linter flags, not all similar-looking expressions in the same function.
