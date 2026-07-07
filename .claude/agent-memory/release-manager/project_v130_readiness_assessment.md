---
name: v1.3.0 readiness assessment (pre-cut)
description: Release-readiness ASSESSMENT (not a cut) performed 2026-07-06 at HEAD 5e76f72, covering the unreleased security wave since v1.2.0 (#243-#263)
metadata:
  type: project
---

# v1.3.0 readiness assessment — 2026-07-06 (assessment only, no tag cut)

Requested as part of a whole-team production-readiness go/no-go call. This was an
ASSESSMENT, not an executed release — no commit/tag/push was made.

## Scope assessed
22 commits since v1.2.0 (HEAD 5e76f72): 9 security/reliability fixes (#243-#259,
#261, #262), 1 test hardening commit (#263), 7 perf commits (#198-#203, #240),
5 agent-memory chore commits.

## Gate results (all re-run fresh at HEAD, not cited from memory)
| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `golangci-lint run ./...` | PASS (0 issues) |
| `govulncheck ./...` | PASS (no vulnerabilities) |
| `go test -race ./...` | PASS (21 packages, 0 failures, 0 races) |
| `go mod tidy` | CLEAN (no changes) |

## Security clearance — already formally satisfied
Found existing security-auditor CLEARED verdicts in
`.claude/agent-memory/security-auditor/` covering the *entire* v1.2.0..HEAD range:
- `audit_findings_20260706_r4_fresh_reverify.md` — 29-target full fuzz sweep
  (~27.6M execs, 0 crashers) + independent re-verification of #255-#259, verdict
  READY, at HEAD 7781cb0.
- `audit_findings_20260706_r5_gmw1_jpeg262_clearance.md` — focused clearance of
  202e34a (#261), bacfa46 (#262), 0d03bf5 (#263), verdict CLEARED, at HEAD 0d03bf5.
- The only commit after 0d03bf5 is 5e76f72, which is agent-memory-only (verified
  via `git show 5e76f72 --stat` — zero `.go` files). Per this project's established
  pattern ("documentation-only commits after CLEARED do not require re-audit"),
  the security gate is satisfied for the full range up to current HEAD.

**Do not re-invoke security-auditor from scratch for this exact commit range** —
read the two files above first; they are recent (same day) and exhaustive
(corpus-diffed 142 RAW + 1718 JPEG files byte-identical pre/post-fix, full fuzz
sweep, bypass-angle analysis per commit).

## API stability — no breaking changes
Diffed exported symbols in root package + touched sub-packages between v1.2.0 and
HEAD: only additive changes found —
- New sentinel errors: root `ErrFileTooLarge`, `format/jpeg.ErrFileTooLarge`,
  `format/tiff.ErrTooManyImageBlocks`.
- Doc-comment additions clarifying thread-safety contracts on `Metadata` and
  `xmp.XMP` (no signature changes).
No renamed/removed identifiers. Recommendation: MINOR bump (v1.2.0 -> v1.3.0),
consistent with this project's own v1.1.0 precedent (MINOR bump for two new
sentinel errors, see `project_v110_release.md`).

## Documentation/release-hygiene gaps found (must fix before actual tag)
1. **CHANGELOG.md `## [Unreleased]` section is completely empty.** None of the
   22 commits since v1.2.0 are documented. Confirmed via
   `git log v1.2.0..HEAD --oneline -- CHANGELOG.md` (zero hits — the file was
   never touched in this range). This is a hard gap per this project's own
   Phase 4 workflow ("Never leave a category empty... never skip the changelog").
2. **SECURITY.md's "MakerNote parsers" fuzz-target table is stale/fictitious.**
   It lists 11 fuzz targets (`FuzzCanonParse`, `FuzzNikonParse`, `FuzzSonyParse`,
   `FuzzOlympusParse`, `FuzzPanasonicParser`, `FuzzLeicaParser`,
   `FuzzSamsungParser`, `FuzzDJIParser`, `FuzzFujifilmParse`, `FuzzSigmaParser`,
   `FuzzPentaxParse`) living in packages `exif/makernote/{vendor}/` — **none of
   these packages or functions exist anywhere in the repo** (verified via
   `go list ./...` and repo-wide grep). MakerNote parsing is consolidated into a
   single file, `exif/makernote_parse.go`, with no per-vendor fuzz target.
   The doc's "27 fuzz targets" total is therefore wrong on both sides: it
   includes 11 that don't exist, and omits all 9 new write-path `*Inject` fuzz
   targets added by #258 (`FuzzJPEGInject`, `FuzzPNGInject`, and 7 RAW-format
   `*Inject` targets). Actual current count: 29 real `func Fuzz*` targets.
   This is ironic given #258's own commit message explicitly cites closing this
   exact coverage gap. Needs a documentation-only fix (no `.go` change) before
   the release ships, since SECURITY.md is a trust document for the security
   wave being released.
3. **BENCHMARKS.md (root) and `benchmarks/BENCHMARKS.md` are stale.** Last
   touched 2026-06-10 at commit 8fae811 (#203), before the entire security wave.
   None of #255 (IFD-chain budget), #257 (uint64 offset arithmetic), #261 (TIFF
   alloc budget), #262 (JPEG 256 MiB cap) have benchmark deltas recorded, even
   though this project's Phase 3 workflow requires it and prior releases
   (v1.1.0, v1.2.0) always documented security-driven regressions with root
   cause. Needs a fresh `go test -bench=. -benchmem -count=3 ./...` run and
   BENCHMARKS.md update as part of the release cut.
4. README.md and doc.go were checked line-by-line against current code (API
   examples, supported-formats table, fuzz/corpus claims) and found accurate —
   **no changes needed** for these two files specifically.

## Verdict rendered
GO-WITH-CAVEATS: code is production-ready (all gates green, security fixes
substantive and independently cleared, zero breaking API changes -> MINOR bump
warranted), but the release process itself is not complete — CHANGELOG,
SECURITY.md fuzz tables, and BENCHMARKS.md must be updated before a v1.3.0 tag
is cut. None of the three are code-safety blockers; all are same-day-completable
documentation tasks.

**Why:** First whole-team go/no-go call after a same-day (2026-07-06) burst of
9 security/reliability fixes accumulated unreleased on main.
**How to apply:** When this release is actually cut (Phase 0-9 workflow), start
from this assessment: gates are already green, security clearance is already on
file (don't redo it), the only remaining work is CHANGELOG authorship,
SECURITY.md fuzz-table correction, and a fresh benchmark run/update.

## Update — 2026-07-06: the three documentation gaps are now closed

A follow-up docs-only task closed all three gaps listed above, at HEAD `0ebf5d4`, in commit
`3de8d2f` ("docs: changelog, security fuzz inventory, benchmarks for v1.3.0 readiness"). See
[[project_v130_docs_gap_closure]] for what was found and fixed (SECURITY.md was more broadly
wrong than just the "27" count) and for corrections to this assessment's own claims. **The repo
is now release-READY for a v1.3.0 cut** — only Phases 6-9 of the standard workflow (verify
nothing changed since `3de8d2f`, tag, push with confirmation, GitHub release) remain, plus a
final full `-bench=. -benchmem -count=3 ./...` sweep if a completist benchmark record is desired
(the docs-gap-closure commit intentionally used a time-boxed subset, not the full suite).

Related: [[project_release_patterns]], [[project_v120_release]], [[feedback-changelog-verify-constants]], [[project_v130_docs_gap_closure]]
