---
name: GoMetadata release patterns
description: Recurring patterns observed across GoMetadata release cycles — remotes, hooks, benchmark format, CHANGELOG conventions
type: project
---

Repository has two remotes: `origin` (wg32 private server) and `Github` (github.com:FlavioCFOliveira/GoMetadata.git). Both must receive the branch push and tag push on every release.

Pre-commit hook runs `go test ./...` and `golangci-lint run` automatically — no need to add a separate lint step before commit; it is enforced by the hook.

BENCHMARKS.md uses two inconsistent run conventions across historical entries: earlier sections used `-benchtime=3s`, later ones use `-count=3`. This means ns/op values are not directly comparable between sections. Always note the run convention in the section header and caveat delta values accordingly.

The [Unreleased] comparison link at the bottom of CHANGELOG.md was incorrectly pointing to v1.0.2...HEAD instead of v1.0.3...HEAD before the v1.0.4 release. On every release, verify all comparison links are updated, not just the new entry.

The `benchmarks/results/` directory did not exist before v1.0.4. It was created as part of that release. Going forward, raw benchmark output is archived there as `v<version>.txt`.

README.md uses a dynamic `[![Release](...shields.io/github/v/release/...)]` badge — it self-updates when a tag is pushed to GitHub. No manual README version bump is needed on patch releases.

**v1.1.0 additions (2026-06-03):**
- `go mod tidy` before the release commit changed `go.mod`: `golang.org/x/text` was promoted from `// indirect` to a direct dependency. Run `go mod tidy` and include `go.mod` in the release commit if it changes; this is expected and correct.
- BENCHMARKS.md did not exist prior to v1.1.0 — it was created as part of that release cycle. Going forward, always create or update it with a delta table.
- Benchmark regressions above the 10% threshold were all security-driven in v1.1.0: `BenchmarkIFDEntryString` (+112%, bounds-checked formatting), `BenchmarkPNGExtract` (+41%, document-level cap check), `BenchmarkIPTCEncode` (+30%, receiver-copy race fix), XMP encode (+16-18%, `maxXMPDocumentBytes` overhead). Document root cause in BENCHMARKS.md; no block warranted.
- The security-auditor's clearance covers executable code. Documentation-only commits after the CLEARED decision do not require a re-audit. Always verify with `git log --oneline <cleared-commit>..HEAD` to confirm no `.go` files changed.

**Why:** Accumulated from the v1.0.4 release cycle (2026-04-08) and v1.1.0 release cycle (2026-06-03).
**How to apply:** Use as a checklist for future releases: check both remotes, verify all CHANGELOG links, note benchmark run convention, archive raw results, include go.mod if tidy changed it.
