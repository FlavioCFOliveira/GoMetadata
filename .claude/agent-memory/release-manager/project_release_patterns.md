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

**v1.2.0 additions (2026-06-10):**
- `go mod tidy` was clean — no change to `go.mod` or `go.sum` for this release.
- Benchmark regressions were broadly larger than v1.1.0 due to the extensive reliability/security work: `BenchmarkMakerNoteDispatch` (+184%, MakerNote OOL rebasing), `BenchmarkIPTCParse` (+89%), `BenchmarkIPTCEncode` (+77%), `BenchmarkReadFile` (+51%, LimitReader OOM guard), `BenchmarkTIFFExtract` (+52%), RAW extracts (+25–30%, defensive raw-slice copy), JPEG inject (+45%, 8BIM sibling preservation), XMP encode (+29–38%, NS-URI/local-name XML escape). All documented with root cause; no block warranted.
- The `head -n -1` flag is not valid on macOS `head` (GNU syntax). Use `grep -v "^## \[v_next\]"` or write to a temp file and call `gh release create --notes-file` to avoid the issue.
- Security clearance for v1.2.0 was performed by reviewing audit memory files (2026-06-09 audit findings) and confirming all CRITICAL/HIGH/MEDIUM findings were addressed in the 94 commits since v1.1.0. No new `.go` files were changed by the release-manager; re-audit not required.
- v1.2.0 working tree had only untracked agent-memory files at release time — these are correctly excluded from staging (use explicit `git add` per file, never `git add -A`).
- New benchmarks introduced in v1.2.0 that had no v1.1.0 baseline: `BenchmarkParseBigTIFF_Simple`, `BenchmarkBigTIFFExtract`, `BenchmarkRelocateDNGLike`, `BenchmarkRelocateSingleStrip`, `BenchmarkRelocateMultiStrip`, `BenchmarkNEFExtractMakerNote`, `BenchmarkARWConformanceExtract`, `BenchmarkARWConformanceInject`, `BenchmarkIPTCAccessorsNonASCII`, `BenchmarkUnescapeXMLNoEntity`.

**Why:** Accumulated from v1.0.4 (2026-04-08), v1.1.0 (2026-06-03), and v1.2.0 (2026-06-10) release cycles.
**How to apply:** Use as a checklist for future releases: check both remotes, verify all CHANGELOG links, note benchmark run convention, archive raw results, include go.mod if tidy changed it; use temp file for GitHub release notes (not `head -n -1`).
