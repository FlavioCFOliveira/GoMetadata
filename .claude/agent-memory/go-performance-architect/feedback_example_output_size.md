---
name: feedback-example-output-size
description: ExampleWrite expected output size must be updated when auto-create adds new metadata segments to a file that previously lacked them
type: feedback
---

When SetCaption (or any setter) auto-creates new components (IPTC, XMP) for a file that previously had none, the serialised output grows and any hardcoded `// Output: output size: N bytes` in example tests breaks.

**Why:** Task #88 AUTO-CREATE policy means `SetCaption` on a JPEG with only EXIF now also creates IPTC and XMP, adding ~2479 bytes to `11-tests.jpg` (236594 → 239073 bytes).

**How to apply:** Whenever a policy change or layout fix causes the encoded output to grow or shrink, re-measure the expected output size with a small go run snippet and update the `// Output:` comment in the affected example. Always run `go test -race ./...` before committing to catch stale example output sizes. As of task #99 (TIFF word-alignment), the size grew from 239073 to 239074 bytes (one padding byte added to word-align IPTC after odd-length XMP).
