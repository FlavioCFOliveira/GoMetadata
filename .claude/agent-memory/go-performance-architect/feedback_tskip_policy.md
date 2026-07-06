---
name: feedback_tskip_policy
description: t.Skip is only valid for corpus absence / OS limits / boundary constants; all other skips must be t.Fatal; synthetic fixture skips are always bugs
metadata:
  type: feedback
---

`t.Skip` is permitted only for three narrow categories (see docs/TESTING.md §2.1):
1. Corpus file absent on disk (conditional on `os.IsNotExist`)
2. OS/privilege limitation (running as root, `os.Symlink` unsupported, timezone data absent)
3. Boundary/capacity constant guard (fixture size exceeds a cap constant)

**Why:** Unconditional skips on synthetic fixtures or parser results hide real bugs. Example: `t.Skip("GPS() returned ok=false")` masked that `xmp.GPS()` didn't handle the W3C Geo namespace — a real gap in the library. Converting to `t.Fatal` exposed and forced fixing the bug.

**How to apply:** When reviewing or writing test code:
- If the skip fires on a synthetic fixture built by the test itself → `t.Fatal`; fix the fixture helper.
- If the skip fires on a parser assertion ("nil after reading synthetic JPEG") → `t.Fatal`; fix the parser.
- If the skip fires on a corpus file path → `t.Skipf` with download instructions.
- If the skip fires on an OS capability check → `t.Skipf` with explanation.

The policy is codified in `docs/TESTING.md`. The `CLAUDE.md` "Skipped tests are forbidden" rule now points to that document.

See also: [[feedback_xmp_parser_lenient]]
