---
name: feedback-copyloopvar-go122
description: In Go 1.22+ (this project uses Go 1.26), loop variable captures are safe; `tc := tc` and `ord := ord` shadowing inside for-range loops is unnecessary and triggers the copyloopvar linter.
metadata:
  type: feedback
---

Do not add `tc := tc` / `ord := ord` / `order := order` loop-variable copies inside `for` loops. In Go 1.22+ each iteration gets its own variable; the copy is redundant. The `copyloopvar` linter will flag every unnecessary copy as an issue.

**Why:** Go 1.22 changed loop variable semantics so copies are no longer needed for t.Parallel() safety. This project targets Go 1.26.

**How to apply:** When writing table-driven parallel subtests, pass the variable directly into the subtest closure — do not shadow it first. The pattern `for _, tc := range cases { t.Run(tc.name, func(t *testing.T) { t.Parallel(); ...use tc directly... }) }` is correct and lint-clean.
