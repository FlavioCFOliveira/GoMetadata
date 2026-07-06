---
name: scanattrs-infinite-loop
description: scanAttrs must exit on '<' as well as '>' and '/'; omitting '<' causes infinite loop when a malformed tag contains '<' in its attribute region
metadata:
  type: feedback
---

`scanAttrs` loop condition must be `b[pos] != '>' && b[pos] != '/' && b[pos] != '<'`. Without the `'<'` check: when isNameTerminator stops `scanName` at `<`, `parseSingleAttr` returns with `ok=false` and pos unchanged; `scanAttrs` never advances and loops forever.

**Why:** Found during finding #171 fix testing — the via-parse subtest timed out (30s) because the crafted `<dc:title<inject/>` input hit this loop.

**How to apply:** Any time you add a new name terminator byte to isNameTerminator, verify scanAttrs also exits on that same byte. The exit condition and the name terminator set must be consistent.
