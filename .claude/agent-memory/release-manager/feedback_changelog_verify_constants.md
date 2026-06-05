---
name: feedback-changelog-verify-constants
description: Always verify cap values, error names, and mechanisms against the actual source before writing CHANGELOG entries — never infer from design comments
type: feedback
---

When writing CHANGELOG entries that describe a constant value (e.g. a cap in MiB), error sentinel name, or fix mechanism, always read the actual constant declaration or function body from the source file. Do not infer values from design-rationale comments, task descriptions, or the diff summary alone.

**Why:** In the v1.1.0 release, four factual errors were introduced into the CHANGELOG by inferring values rather than reading the source:
- `maxXMPDocumentBytes` was written as "50 MiB, configurable" when the constant is `16 << 20` (16 MiB, compile-time constant).
- The ExtendedXMP GUID cap was described as "64 GUIDs rejected with ErrSegmentTooLarge" when the cap is 4 distinct GUIDs (each 16 MiB → 64 MiB aggregate), excess are dropped silently, and ErrSegmentTooLarge is an unrelated write-path error.
- The `iptc.Encode` fix was described as "copies the dataset slice before sorting" when the actual fix stops appending the 1:90 CodedCharacterSet marker to the receiver's Records[0] and emits it to output only.
- FormatCapability was described as "referenced from SECURITY.md" when SECURITY.md does not mention it.

**How to apply:** For every cap value, error name, or fix mechanism in a CHANGELOG entry, run `grep -n` or `Read` the relevant source file to confirm the exact constant, exact error variable name, and exact code path before committing the entry.

Related: [[v1.1.0-release-record]]
