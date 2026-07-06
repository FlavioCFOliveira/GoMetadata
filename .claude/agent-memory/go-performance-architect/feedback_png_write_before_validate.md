---
name: feedback_png_write_before_validate
description: PNG Inject must validate input signature before writing to w — partial write corruption pattern
metadata:
  type: feedback
---

In any format injector that writes a signature/header to w before reading the input, validate the input FIRST. Writing to w unconditionally and then returning an error leaves w in a corrupt state when the caller is an in-place writer.

**Why:** audit finding #181 — Inject wrote pngSig to w before reading/comparing the input signature, so passing a JPEG produced `out[:8]==pngSig` despite returning ErrInvalidSignature. Fixed in commit 1f506be.

**How to apply:** In any Inject-style function: (1) read input sig, (2) compare, (3) return error if mismatch, (4) only then write sig to w. This is the pattern already used by Extract; Inject diverged from it.
