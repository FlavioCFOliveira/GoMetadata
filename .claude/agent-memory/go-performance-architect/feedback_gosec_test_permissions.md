---
name: gosec-test-permissions
description: gosec G306/G302 fires on os.WriteFile/os.Chmod in test helpers; suppress with //nolint:gosec on the return line, not the assignment
type: feedback
---

gosec reports G306 ("Expect WriteFile permissions to be 0600 or less") and G302 ("Expect file permissions to be 0600 or less") on test helpers that use 0o644 for image files or 0o555/0o755 for chmod-based error injection. These are false positives in test code.

**Why:** gosec's permission linter doesn't understand test context. 0o644 is the correct mode for a test image file; 0o555 is used intentionally to force rename failures.

**How to apply:** Place `//nolint:gosec // G306: <reason>` or `//nolint:gosec // G302: <reason>` on the same line as the `os.WriteFile` / `os.Chmod` call (not on a preceding assignment). The nolint directive must be on the offending statement line.

Also: wrapcheck fires on `return c.r.Read(p)` / `return c.r.Seek(...)` in io.Reader/io.Seeker adapters. Suppress with `//nolint:wrapcheck` on the `return` line itself (not on a preceding `n, err :=` line — that triggers an "unused directive" error from nolintlint).
