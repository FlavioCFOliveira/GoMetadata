---
name: feedback-example-output-size
description: ExampleWrite expected output size must be updated when auto-create adds new metadata segments to a file that previously lacked them
type: feedback
---

When SetCaption (or any setter) auto-creates new components (IPTC, XMP) for a file that previously had none, the serialised output grows and any hardcoded `// Output: output size: N bytes` in example tests breaks.

**Why:** Task #88 AUTO-CREATE policy means `SetCaption` on a JPEG with only EXIF now also creates IPTC and XMP, adding ~2479 bytes to `11-tests.jpg` (236594 → 239073 bytes).

**How to apply:** Whenever a policy change causes convenience setters to write more data to a file, re-measure the expected output size with a small go run snippet and update the `// Output:` comment in the affected example. Always run `go test -race ./...` before committing to catch stale example output sizes.
