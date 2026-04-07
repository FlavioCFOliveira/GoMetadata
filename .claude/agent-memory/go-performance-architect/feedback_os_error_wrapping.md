---
name: os.IsNotExist vs errors.Is — Go 1.26 wrapping behaviour
description: os.IsNotExist and os.IsPermission do NOT unwrap %w-wrapped errors in Go 1.26; use errors.Is with sentinel values instead
type: feedback
---

`os.IsNotExist(fmt.Errorf("...: %w", err))` returns **false** in Go 1.26 even when `err` is a not-found error. Same for `os.IsPermission`.

**Why:** These legacy predicates (pre-errors.Is era) do not call `errors.Is` internally when the error is wrapped. `errors.Is(err, os.ErrNotExist)` correctly unwraps through `%w` chains.

**How to apply:** Whenever a function wraps an `os.Open`/`os.Stat` error with `fmt.Errorf("...: %w", err)` and tests check the error with `os.IsNotExist`/`os.IsPermission`, update the tests to use `errors.Is(err, os.ErrNotExist)` / `errors.Is(err, os.ErrPermission)` instead. Do not avoid wrapping the error — wrapping is correct; the predicate is wrong.
