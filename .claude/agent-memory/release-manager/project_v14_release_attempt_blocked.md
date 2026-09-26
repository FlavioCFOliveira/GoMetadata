---
name: v1.4-release-attempt-blocked-apidiff
description: 2026-09-26 release attempt (v1.3.0..develop) halted at the pre-flight API-diff gate — format/tiff's six InjectWithEXIF* exported functions changed signature incompatibly. RESOLVED — see [[project_v140_release]] for the final, completed release record.
type: project
---

**RESOLVED 2026-09-26**: the coordinator had `go-performance-architect` restore the six v1.3.0
signatures as thin wrappers over new `*Stream` variants (commit `6abaf9d` on `release/1.4.0`,
verified by me independently — apidiff re-run showed zero incompatible changes across all 17
public packages). The release proceeded as v1.4.0. Full final record: [[project_v140_release]].

On 2026-09-26, a release was requested for `develop` (HEAD `1751a6c`, last tag `v1.3.0` at
`cecc682`). Per the task's explicit instruction ("if there are incompatible changes, STOP and
report instead of releasing"), the API diff was checked with
`go run golang.org/x/exp/cmd/apidiff@latest` before any branch/tag work, using a `git worktree
add --detach <tmp> v1.3.0` to get two source trees for the same import paths (apidiff needs
`-w old.export`/`-w new.export` built from two separate checkouts, then a final compare — it
cannot diff two revisions of one working tree directly).

**Finding:** `format/tiff` package — six exported functions changed signature incompatibly:
`InjectWithEXIF`, `InjectWithEXIFARW`, `InjectWithEXIFCR2`, `InjectWithEXIFNEF`,
`InjectWithEXIFORF`, `InjectWithEXIFRW2` went from
`func([]byte, *exif.EXIF, []byte, []byte, io.Writer) error` to
`func(io.ReadSeeker, []byte, bool, *exif.EXIF, []byte, []byte, io.Writer) error`. Root cause:
sprint 44's "stream image data on write" work (commit `a03b320`, tasks #291/#292) — switching
the TIFF-family write path from full-buffer to streaming-from-source. This is real and
confirmed by reading `format/tiff/tiff.go` in both trees, not an apidiff artifact.

Also found (all backward-compatible additions, no blocker): root package
`(*Metadata).RawSegments`; `exif.AcceptRAWMagic`, `exif.AliasThumbnail`, `exif.EncodeInto`,
`exif.EncodedSize`; `format/jpeg.ExtractFullSelective`; `format/tiff.ExtractWithMagic`. `iptc`,
`xmp`, `format`, `format/heif`, `format/png`, `format/webp`, and every `format/raw/*` package had
zero exported-API diff.

**Why this matters — the ambiguity that blocked the release:** the top-level `Metadata`
`Write`/`WriteFile` entry point (the one CONTRIBUTING.md calls "the single public entry point")
is completely unaffected — same signature, same contract; CHANGELOG's `[Unreleased]` section
already documents the streaming change purely as an internal performance win with "No output
byte ever changes." But `format/tiff` is not under `internal/`, and CONTRIBUTING.md's compatibility
disclaimer ("Internal packages are not part of the public API and may change without notice")
textually applies only to the `internal/` directory in its repo-layout table — it does not
disclaim `format/*`, `exif`, `iptc`, or `xmp` as unstable. Since those packages export capitalized
identifiers and are freely importable by any external consumer, a literal SemVer reading requires
a MAJOR bump for this change, or a compatibility shim, or an explicit documented policy change
placing `format/*` outside the semver contract (analogous to `internal/`) before treating it as
non-blocking. This is a project-level policy decision, not a release-mechanics one — it was
escalated to the user rather than resolved unilaterally, per this project's Decision Policy
(present lettered options + a recommendation, ask, don't guess).

**State left behind:** no branch/tag/commit was created; the `git worktree` used for the old
checkout was removed (`git worktree remove --force`) after the diff was captured; `develop` is
still exactly at `1751a6c`, clean, nothing pushed. No pre-flight test/lint/vuln/bench/security
work was performed for this attempt since the API-diff gate is explicitly the first stop-or-go
checkpoint in this task's instructions.

**How to apply:** In any future GoMetadata release, run the apidiff worktree procedure above
*before* creating the release branch. If a sub-package (`format/*`, `exif`, `iptc`, `xmp`) shows
an incompatible change, do not treat "CONTRIBUTING.md only disclaims internal/" as license to
wave it through as a MINOR/PATCH — present the user with the MAJOR-bump / compatibility-shim /
policy-amendment options explicitly, as this project's own Decision Policy requires for exactly
this kind of ambiguity.

Related: [[project_release_patterns]], [[project_v130_release]]
