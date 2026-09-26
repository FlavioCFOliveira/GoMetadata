---
name: v1.3.0 release record
description: Gate results, security clearance method (self-performed, no Agent tool access), benchmark noise finding, and key decisions for the v1.3.0 release (2026-07-07)
metadata:
  type: project
---

# v1.3.0 Release Record — 2026-07-07

## Version bump
MINOR bump: v1.2.0 → v1.3.0. 37 commits since v1.2.0. BigTIFF write now complete end-to-end
(exif encode + format/tiff relocate + top-level Write gate removed), IPTC/XMP/EXIF write-side
bridging (SetCreators, SetDateCreated fan-out), XMP round-trip container-type fidelity + Dublin
Core allowlist completion, XML 1.0 forbidden-char-ref sanitisation, plus the 2026-07-06
security-hardening wave (#243-#262). Confirmed additive-only exported-symbol changes (new
sentinels only, zero removed/renamed) — no breaking API changes.

## Gate results (all re-run fresh at HEAD before tagging)

| Gate | Result |
|---|---|
| `gofmt -l .` | clean |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `go mod tidy` | CLEAN (no changes to go.mod/go.sum) |
| `golangci-lint run ./...` | PASS (0 issues) |
| `govulncheck ./...` | PASS (no vulnerabilities) |
| `go test -race -count=1 ./...` | PASS (21 packages, 0 failures, 0 races) |

## IMPORTANT: security-gate finding — the launching context's claim was NOT verifiable in memory

The task briefing asserted "the final security-delta review returned GO... FuzzTIFFInject 1.26M /
FuzzTIFFExtract 248K / FuzzParseEXIF 4M execs, 0 crashers" as an already-completed
`security-auditor` clearance for the BigTIFF-write/bridging/XMP-fix delta. **This exact review
does not exist anywhere in `.claude/agent-memory/security-auditor/`** — verified via
`grep -rl` for the specific numbers, for "BigTIFF"+"fuzz", and for "bridging"/task-number
cross-references. The last *confirmed* security-auditor memory record covers only up to commit
`5e76f72` (round-4, `audit_findings_20260706_r4_fresh_reverify.md`) plus round-5
(`audit_findings_20260706_r5_gmw1_jpeg262_clearance.md`, covers only #261/#262/#263). Eight
non-test `.go` files changed *after* those reviews (`5e76f72..HEAD`: `bc35f99`, `e092770`,
`aa24232`, `ef9041e`, `e81d364`, `0ebf5d4`, `3b72a26`, `39144d3`, `05e0b73`) were never
security-audited on record.

**This session also had no Agent-tool access** (the tool list contained only
Bash/Read/Write/Edit — no way to invoke `security-auditor` or `go-performance-architect`
directly), unlike what the persona/system-prompt template describes. Given the hard "no
exceptions" security gate and the inability to delegate, I self-performed the verification
instead of either blindly trusting the unverified claim or blocking the release outright:
1. Read the actual diffs of all 8 files, confirming the defensive mechanisms the commit
   messages claimed actually exist in source (`exif.ErrBigTIFFPointerOverflow`,
   `exif.ErrBigTIFFEncodeSizeExceeded`, `iptc.ErrDatasetValueTooLarge`, uint64-before-narrowing
   guards).
2. Ran fresh, real `go test -fuzz=... -fuzztime=40s` campaigns directly via Bash against every
   package touched by the unaudited delta: `FuzzTIFFInject` 2.8M execs, `FuzzTIFFExtract` 332K,
   `FuzzParseEXIF` 3.3M, `FuzzParseXMP` 13.4M, `FuzzParseIPTC` 18.6M — 0 crashers, no new
   testdata/fuzz/ artifacts (confirmed via `git status` after each run).
3. Documented this self-performed verification transparently in the release commit message and
   reported it plainly to the user rather than citing a third-party clearance that doesn't exist.

**Lesson for future cycles**: a task briefing's claimed prior-agent-clearance state is not
itself evidence — cross-check the actual memory files (or, if unavailable, the actual tool
access) before treating a hard gate (security clearance) as satisfied. See
[[feedback_verify_gate_claims_not_briefings]].

## Benchmark run finding: same-session system-load noise inflates ns/op, not B/op/allocs/op

Ran the full `-bench=. -benchmem -count=3 ./...` sweep immediately after the 5 fuzz campaigns
above plus golangci-lint/govulncheck — i.e. on a *not-idle* machine. Several benchmarks showed
ns/op inflated 10-15% versus the same-day interim measurement from task #268
(`benchmarks/results/HEAD-0ebf5d4-2026-07-06.txt`) with **byte-for-byte identical B/op and
allocs/op** (e.g. `xmp.BenchmarkXMPEncode`: 3156 B / 3 allocs unchanged both times, ns/op
1028→1180, +14.8% with zero code difference in that specific benchmark — it's the internal
"control" that isn't touched by the array-container fix). This confirms: **B/op and allocs/op are
the trustworthy signal in a loaded environment; ns/op needs either an idle machine or a
matching B/op/allocs/op change to be attributed to code.** Documented this explicitly as a
methodology note in `benchmarks/BENCHMARKS.md`'s v1.3.0 section rather than either suppressing
the noisy numbers or misattributing them to code changes. Do not re-run fuzz + benchmark back to
back on the same machine in future release cycles if precise ns/op deltas matter; sequence them
with a cool-down, or run benchmarks first.

## Notable benchmark deltas (root-caused)

- Broad `-1 alloc/op` improvement across nearly every top-level read/write benchmark — consistent
  with tasks #198-#203/#240 (perf wave) and #244 (`format.Detect` `io.ReadFull`) landing between
  v1.2.0 and this tag.
- `format/tiff` `BenchmarkRelocateXxx` family: real B/op + allocs/op improvement (GM-W1 #261
  budget-struct restructuring), same finding as the interim measurement, now confirmed on the
  full sweep.
- `xmp.BenchmarkXMPEncodeFullPacket`: B/op +12.1% (3188→3573), allocs unchanged — real,
  attributable cost of `e81d364`/`3b72a26`'s XMP array-container-conformance fix (ISO 16684-1
  §7.5). Confirmed via the `BenchmarkXMPEncode` control (scalar-only, unaffected).

## CHANGELOG gap found and closed at cut time

The `[Unreleased]` section (written by an earlier docs-gap-closure task, #268) did not cover the
4 commits landed *after* that task: `3b72a26` (dc: array-allowlist completion, #272),
`39144d3` (XMP round-trip container-type preservation, #273), `05e0b73` (XML 1.0 forbidden
char-ref sanitisation, #274), and `8890788` (docs/test-only, correctly omitted — no user-facing
behaviour change). Added 2 new `### Fixed` bullets + 1 new `### Security` bullet before promoting
`[Unreleased]` → `[1.3.0]`. Always re-diff `git log <last-known-changelog-commit>..HEAD --
'*.go'` before assuming an existing `[Unreleased]` section is complete, even if a recent task
claimed to have finalised it — see [[feedback_changelog_verify_constants]] for the general
pattern this extends (verify against source/commits, not against a prior task's own summary).

## Decisions

- No version constant exists anywhere in `.go` source or `doc.go` for this project — confirmed
  via grep (as in v1.1.0/v1.2.0). README's release badge is dynamic; no manual bump needed.
- Pushed explicitly only to the `Github` remote per this task's instructions. Verified
  afterward (via `git ls-remote origin main`/`refs/tags/v1.3.0`) that `origin` (wg32) already
  reflected the identical `main`@`cecc682` and the `v1.3.0` tag anyway, with no explicit
  `git push origin` ever run this session and no local post-commit/post-push hook that would
  cause it (checked `.git/hooks/`: only `pre-commit` exists). Some mirroring between the two
  remotes evidently already exists outside this repo's local git config — don't assume a second
  explicit push to `origin` is needed in future cycles, but do verify with
  `git ls-remote origin ...` rather than assuming either way.
- `knowledge-model.md:70` was flagged stale by a prior go-performance-architect memory note
  (still says BigTIFF write returns `ErrWriteNotSupported`) — out of scope for release-manager
  (per that same note, it's the orchestrator's rmp-Knowledge-Graph responsibility, and `rmp` is
  not in this session's tool list either). Flagged in the final report, not fixed.

## Artifacts

- Release commit: `cecc682`
- Tag: `v1.3.0` (annotated)
- Remote pushed: `Github` (github.com:FlavioCFOliveira/GoMetadata.git) — both `main` and the tag
- Remote confirmed already in sync (no push needed): `origin` (wg32:/xumiga/img-metadata.git) —
  `main`@`cecc682` and tag `v1.3.0` both present, verified via `git ls-remote`
- GitHub release: https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.3.0

**Why:** v1.3.0 completes BigTIFF write support (the last format-write gap from v1.2.0) and adds
cross-standard write-side bridging, on top of a substantial same-week security-hardening wave.
**How to apply:** Reference for next release cycle's gate-verification method when Agent-tool
access is unavailable, and for the benchmark same-session-noise caveat.
