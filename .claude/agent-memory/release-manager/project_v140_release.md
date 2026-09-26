---
name: v1.4.0-release
description: v1.4.0 release record (2026-09-26) — apidiff-caught breaking change fixed pre-tag, a genuine gosec nolint regression found and fixed, and a repeatable methodology for reproducing CI-pinned golangci-lint locally
type: project
---

Released 2026-09-26. Tag `v1.4.0` on `main`, previous `v1.3.0` (`cecc682`, 2026-07-07). MINOR bump
(new exported identifiers only, once the item below was fixed).

## The workflow that made this release different from v1.1.0/v1.2.0/v1.3.0

This was the first release cycle in this project where the **pre-tag `apidiff` check itself
caught a real problem** rather than just confirming a clean bill of health: `format/tiff`'s six
`InjectWithEXIF*` functions had their signature changed incompatibly by Sprint 44's streaming-write
refactor (`[]byte` → `io.ReadSeeker, []byte, bool, ...`). This is not internal — `format/tiff` is
not under `internal/`, so it is real public Go API, and `CONTRIBUTING.md`'s only compatibility
disclaimer ("Internal packages are not part of the public API") textually covers only the
`internal/` directory, not `format/*`/`exif`/`iptc`/`xmp`. I stopped and presented lettered options
(MAJOR bump / restore-compat / policy-amendment / don't-release) rather than deciding unilaterally
— the user chose "restore compatibility", which `go-performance-architect` did as thin `*Stream`
wrapper functions (commit `6abaf9d`). See [[project_v14_release_attempt_blocked]] for the original
finding.

**How to apply going forward:** always run the `apidiff` two-worktree procedure (documented there)
*before* creating a release branch, for every release, not just ones the user flags as
"probably has API changes" — this MINOR release would have shipped a silent breaking change
without it.

## A second, independent finding: a lost `//nolint:gosec` annotation is a real (if non-security) regression

After the apidiff fix, my own independent re-run of the full pre-flight (never just trusting the
coordinator's briefing — see [[feedback_verify_gate_claims_not_briefings]]) found that Sprint 44
had also dropped two `//nolint:gosec // G115: ...` justification comments (`exif/ifd.go`,
`format/tiff/relocate_bigtiff.go`) that existed at v1.3.0. Confirmed via `git diff v1.3.0..HEAD`
content-diffing every nolint comment per file (not just line-diffing, since many functions gained
a new parameter and shifted lines) — a script that diffs `grep -o '//nolint:.*'` sets per file
between two revisions is the reliable way to find this class of accidental-loss regression across
a large refactor; ~15 other files also showed nolint-text removed, but re-running golangci-lint
confirmed the tool no longer triggers on those (legitimate cleanup from the refactor), so
content-diffing nolint comments is necessary but *not* sufficient — always cross-check against an
actual lint run before treating a removed nolint as a regression.

The underlying integer conversions were still provably bounds-safe (verified by reading the
surrounding code); this was an annotation-hygiene regression, not a memory-safety bug. Reported to
the user as a two-line trivial fix requiring `go-performance-architect` (I have no `.go`-edit
authority); user had it fixed (commit `94b0a87`), and I independently re-verified the exact gosec
5→3 delta myself before proceeding.

## golangci-lint version drift is a real trap — always verify against actual CI history

The locally installed `golangci-lint` (v2.14.0 via Homebrew) gives **materially different**
results than the CI-pinned version (`v2.11.4`, `.github/workflows/ci.yml`), and even running
v2.11.4 locally via `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4` gives
different (worse) results depending on which Go **compiler** version builds/runs it — `GOTOOLCHAIN`
must be pinned to the module's own `toolchain` line (`go1.26.4` here) or golangci-lint's export-data
reader chokes on newer stdlib and, separately, `revive`'s `unhandled-error` rule fires on ~27 false
positives (`bytes.Buffer.Write`, `strings.Builder.WriteByte`, etc.) that do **not** reproduce in
real GitHub Actions CI.

**The decisive verification method**: use `gh run list -R <owner>/<repo>` and `gh run view <id>`
to pull the *actual* Lint job annotations for the last 2-3 release commits themselves. This
project's real CI history showed v1.1.0/v1.2.0/v1.3.0 **all shipped with the Lint CI job red**,
every time with the exact same 2-3 `gosec G115` findings in `write_unix.go`/`xmp/xmp_test.go` —
established, accepted, non-blocking precedent. That ground truth is what let me confidently
discount the ~27 "revive: unhandled-error" local-only findings as reproduction noise (they never
appear in real CI annotations) while still treating 2 *newly introduced* gosec findings as a real
regression worth fixing.

**How to apply:** never trust a local golangci-lint run at face value when it disagrees with a
past "shipped" state. Cross-check with `gh run view` on the actual release commits before deciding
whether a lint delta is a regression or reproduction noise from a mismatched local
linter/toolchain version.

## Benchmark capture: post-fuzz system noise reproduced again, same signature as v1.3.0

Ran the full `-bench=. -benchmem -count=3 ./...` sweep immediately after ~7 minutes of 10-worker
parallel fuzzing (29 targets) + two golangci-lint passes. Result: `ns/op` regressed broadly and
uniformly across nearly every package (some >100%) while `B/op`/`allocs/op` improved dramatically
and consistently (`format/tiff` B/op -81%, `format/raw/arw` -84%, `format/raw/cr2`/`dng` -97%,
`format/heif` allocs -66%). This is the *same* signature already documented in
[[project_v130_release]]'s memory: same-session CPU load inflates `ns/op` but never touches
`B/op`/`allocs/op` (deterministic for fixed input). Cross-checked against `v1.3.0`'s own
`benchmarks/BENCHMARKS.md` section, which independently discovered and documented the identical
methodology note for the same reason. **Confirmed pattern, not a one-off**: whenever this
session's own workflow runs fuzzing before benchmarking on the same machine, expect `ns/op` noise
and trust only `B/op`/`allocs/op`.

**Tooling note**: `benchstat` (already installed at `~/.local/bin/benchstat`) is the right tool for
computing medians/geomean across `-count=3` runs and doing the old-vs-new comparison — far more
reliable than hand-computing medians from raw `go test -bench` text output. Its CSV output
(`-format csv`) is parseable but has a header-shape quirk: a benchmark present in only one of the
two input files gets a *3-column* single-file sub-table instead of the normal 6-column
old/new/delta layout, so naive fixed-index CSV parsing silently misattributes the lone value to
the wrong (old vs new) column — must detect single-file mode from the filename header row and
branch accordingly.

## Full outcome

- apidiff: 0 incompatible changes (verified twice — once before the fix, once after).
- gofmt/vet/build: clean.
- `go test -race -count=1 ./...`: clean, full suite.
- govulncheck: 0 vulnerabilities affecting code (11 informational, stdlib/deps, not reachable).
- golangci-lint (CI-pinned v2.11.4 + go1.26.4): gosec 3 (all 3 pre-existing/accepted per CI
  history), 0 new. Local v2.14.0: 4 pre-existing issues, same as v1.3.0 baseline.
- Security review: full manual diff review (bounds/offset arithmetic, buffer aliasing, pooled
  buffers) + all 29 fuzz targets run 12-20s each, 0 crashes, 0 new corpus. No findings.
- Benchmarks: archived, see above.
- Tag `v1.4.0`, GitHub release created, `release/1.4.0` deleted locally and on both remotes after
  merge+backmerge.

Related: [[project_v14_release_attempt_blocked]], [[feedback_verify_gate_claims_not_briefings]],
[[project_v130_release]], [[project_release_patterns]]
