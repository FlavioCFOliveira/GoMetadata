---
name: gosec-g115-inconsistent
description: gosec G115 fires inconsistently on uint32(int) conversions in test files depending on build-graph cache state; add nolint:gosec for len() and locally-computed int-offset conversions, but not for const-derived ints.
type: feedback
---

gosec G115 ("integer overflow conversion int -> uint32") fires inconsistently on test-helper functions across packages, depending on linter cache state.

**Why:** The linter's build graph reuse causes some packages to be checked in isolation where type-widening triggers G115 and others where it does not. The pattern is: `uint32(len(slice))`, `uint32(localIntVar)` computed from `len()` or `len()+const` tends to fire; `uint32(constInt)` does not.

**How to apply:** In test-helper functions that build binary byte buffers, add `//nolint:gosec // G115: test-helper, bounded by buf` on:
- `uint32(len(someSlice))` 
- `uint32(intVarDerivedFromLen)` (e.g. `uint32(xmpOff)` where `xmpOff = const + len(data)`)

Do NOT add nolint on `uint32(constInt)` where the const is a fixed layout offset (e.g. `uint32(ifd0Off)` where ifd0Off=8) — gosec does not fire on those and the nolint directive will cause nolintlint to complain about unused directives.

Also note: running golangci-lint multiple times on the same change can surface different files with G115 depending on cache. When adding the nolint directive, confirm gosec actually fires by running lint with `--no-cache` or after `go clean -testcache`.

Related: [[gosec-test-permissions]]
