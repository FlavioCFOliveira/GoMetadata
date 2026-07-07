---
name: v1.3.0 docs-gap-closure record
description: What was found and fixed when closing the CHANGELOG/SECURITY.md/BENCHMARKS.md gaps identified by the v1.3.0 readiness assessment (2026-07-06, HEAD 0ebf5d4, commit 3de8d2f)
metadata:
  type: project
---

# v1.3.0 docs-gap-closure — 2026-07-06 (docs-only, no tag cut)

Executed the three follow-up tasks from [[project_v130_readiness_assessment]]. This was
docs-only: no `.go` file was touched, no version bumped, no tag created, nothing pushed.
Commit: `3de8d2f`. Files changed: `CHANGELOG.md`, `SECURITY.md`, `README.md`, `BENCHMARKS.md`,
`benchmarks/BENCHMARKS.md`, `benchmarks/results/HEAD-0ebf5d4-2026-07-06.txt`.

## Findings that went beyond the original assessment's description

1. **SECURITY.md's fuzz-target problem was bigger than "27 vs 29" and 11 fictitious MakerNote
   targets.** The "Container extractors" table only ever listed the `*Extract` half of each
   pair — `FuzzTIFFInject`, `FuzzHEIFInject`, and `FuzzWebPInject` existed in the repo (predating
   this sprint) but were **never documented at all**, and `internal/riff.FuzzRIFFRead` was also
   completely absent from the doc. The real fix required a full-table rewrite (End-to-end / Core
   parsers / Container extractors-and-injectors / RAW extractors-and-injectors / Internal), not a
   find-and-replace of the count. Always verify with
   `grep -rn '^func Fuzz' --include='*.go' .` and cross-check every existing table row against
   that output — don't assume a table missing only *some* targets is otherwise complete.
2. **README.md had the identical stale "27 fuzz targets" claim** (line 278) that the assessment's
   own point 4 said was "checked line-by-line ... found accurate." It was not checked against
   this specific line. Lesson: "found accurate" verdicts from a prior assessment are a starting
   point, not a substitute for re-grepping the exact figures you're about to certify in a new
   pass, especially single stray numbers embedded in prose tables.
3. **The root `BENCHMARKS.md`'s per-task sections are NOT strictly chronological** — tasks #201
   and #202 (dated 2026-06-10, same day as #198/#199/#200/#203/#240) are appended at the very end
   of the file, after the v1.0.x historical sections, and each section's "Before" baseline is
   whatever commit that specific task branched from at the time — not necessarily the
   file's own immediately-preceding section. When adding a new snapshot, don't assume the last
   section in the file is the most recent baseline for every benchmark name; grep for the
   benchmark's most recent literal occurrence and check its own stated baseline commit/task.
4. **`benchmarks/BENCHMARKS.md` (the release-delta-table copy) has a dangling, contentless
   trailing section header** (`## Summary — v1.1.0 vs v1.0.4` with nothing underneath — it's the
   literal last line of the file). Pre-existing, not caused by this task, out of scope to backfill
   (would require regenerating a full historical suite run). New content was inserted *before*
   this dangling header, not after, so the file still ends on the same (broken) line as before.
5. **A security-fix commit can *improve* a benchmark.** `202e34a` (#261, GM-W1 allocation-DoS
   cap) restructured `format/tiff`'s block-enumeration helpers around a shared budget struct,
   which — for well-formed, non-adversarial input — allocates *fewer* objects than the pre-fix
   code while closing the DoS. All three `BenchmarkRelocateXxx` benchmarks improved ~21-27%
   ns/op and -6 allocs/op as a side effect. Don't assume every security-wave delta is a cost;
   measure and attribute honestly in both directions.
6. **A correctness fix can look like a regression against an outdated baseline while being flat
   against the correct one.** `BenchmarkEXIFParse_Camera` showed +7.4% against the *v1.2.0*
   baseline recorded in `benchmarks/BENCHMARKS.md`, but only +0.9% against task #202's own
   baseline recorded in the *root* `BENCHMARKS.md` (task #202 landed between v1.2.0 and this
   measurement, and already captured the "true" post-perf-wave number). When two files in the
   same repo record different baselines for the same benchmark, say so explicitly rather than
   picking one number and presenting it as unambiguous.
7. **Always compute the median of your own `-count=3` samples precisely** (sort and take the
   middle value) rather than eyeballing/averaging from the raw `go test -bench` output — a first
   pass through this task used rough approximations for ~15 figures that were all within 1% of
   the true median but inconsistent with the "median of 3 runs" framing used in the prose; caught
   and corrected in a second pass before committing.

## SemVer confirmation method used

Diffed exported symbols between the last tag and HEAD directly from `git diff <tag>..HEAD -- '*.go' ':!*_test.go'`,
grepping for `^-(func |var Err|var [A-Z]|type [A-Z]|const [A-Z])` (removed) vs `^\+(same)` (added).
No `gorelease`/`apidiff` tool was available in this environment. Found only new exported
sentinels/methods, zero removed or signature-changed exported symbols across 93 changed `.go`
files — confirms MINOR bump is correct without needing the external tooling.

Related: [[project_v130_readiness_assessment]], [[project_release_patterns]], [[feedback-changelog-verify-constants]]
