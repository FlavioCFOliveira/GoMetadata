---
name: feedback_godox_in_strings
description: godox linter fires on BUG/TODO/FIXME keywords inside t.Errorf strings and comments; use neutral phrasing instead
metadata:
  type: feedback
---

The `godox` linter fires on the keywords `BUG`, `TODO`, and `FIXME` anywhere in Go source text — including inside string literals passed to `t.Errorf`, `fmt.Errorf`, etc., not just in comments.

**Why:** godox scans all string tokens and comment tokens for these keywords; it does not distinguish between comments and runtime strings.

**How to apply:** In error messages for regression tests, use phrasing like `"task #N regression: ..."` instead of `"BUG #N: ..."`. In doc comments, use `"See task #N"` instead of `"Bug reference: ..."` or `"FIXME:"`.
