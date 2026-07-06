---
name: audit_findings_20260706_r5_gmw1_jpeg262_clearance
description: Focused clearance of commits 202e34a (GM-W1 TIFF write-path DoS cap), bacfa46 (JPEG 256MiB countingReader cap), 0d03bf5 (test de-parallelize) — CLEARED, 0 bypass/corruption/false-rejection
metadata:
  type: project
---

Focused (diff-only) clearance review at HEAD 0d03bf5, requested separately from
the round-4 full audit ([[audit_findings_20260706_r4_fresh_reverify]]). Reviewed
exactly 3 commits closing GM-W1 (a finding surfaced during round-4 that wasn't
captured in my own round-4 memory note — it must have been found by a parallel
review pass; go-performance-architect's own memory
`project_gmw1_imageblock_budget.md` and `project_task262_jpeg_maxfilesize.md`
have the fuller design rationale).

## GM-W1 (202e34a) — TIFF/DNG/CR2/NEF/ARW/ORF/RW2 write-path Count-driven alloc DoS
`extractParallelOffsetBlocks` (strips/tiles, tags 0x0111/0x0117/0x0144/0x0145)
and `enumerateSubIFDsAt` (SubIFDs, tag 0x014A) in `format/tiff/relocate.go`
allocated one heap object per attacker-declared Count element with no cap.
Fix: `maxImageBlocksPerOffsetEntry=65536` (per-entry), `maxSubIFDsPerEntry=1024`
(per-entry, checked at EVERY recursion depth since the recursive call re-enters
the same function), `maxAggregateImageBlocks=262144` shared `imageBlockBudget`
threaded through both `enumerateImageBlocks` and `enumerateSubIFDs` from a
single `newImageBlockBudget()` per relocate call (5 call sites: relocate.go,
relocate_arw.go, relocate_nef.go, relocate_orf.go, relocate_rw2.go — verified
via grep, exactly one budget object per site, shared not duplicated).

Verified NOT bypassable:
- Cap/budget checks read `n := int(offsetEntry.Count)` and run BEFORE
  `typeSize()`/`make()` — confirmed by reading the live function body, not the
  diff hunk alone (order: n==0 check, per-entry cap, budget.spend, THEN
  offElemSz/cntElemSz/make). SHORT vs LONG type does not matter since n comes
  from `.Count`, not from any type-dependent array-size math.
- Tile tags (0x0144/0x0145) go through the SAME `extractParallelOffsetBlocks`
  call and the SAME shared `budget` as strips — no separate unguarded path.
- Nested SubIFD recursion re-enters `enumerateSubIFDsAt` for every level, and
  the per-entry-cap + budget.spend check is at the TOP of that function, so
  every recursion level re-validates independently — no way to smuggle a huge
  count past depth 0 by hiding it at depth >0.
- IFD1 next-chain: `enumerateImageBlocks` loops `for ifd := e.IFD0; ifd != nil;
  ifd = ifd.Next`; this chain is already capped to `maxTraverseChainIFDs=512`
  by the READ-side `exif.traverseBudget` (round-3 fix, EXIF-IFDCHAIN-01), so
  the aggregate budget here specifically closes the residual case where up to
  512 chained IFDs each individually satisfy the 65536 per-entry cap
  (512*2*65536 >> 262144) — proven by the new
  `TestWrite_RejectsAggregateImageBlockBudget` test (5 IFDs x 65536 each,
  passes).
- size==0 "phantom" strips: cap is on `n` (element COUNT), not on the declared
  per-element byte size, so a Count of e.g. 10M zero-size strips is rejected
  identically to 10M full-size strips — this was literally the originally
  reported amplification vector and is now closed.
- 32-bit int-overflow angle probed: `int(offsetEntry.Count)` on a 32-bit
  platform could wrap Count=0xFFFFFFFF to n=-1, which would make
  `n > maxImageBlocksPerOffsetEntry` false (bypassing the per-entry cap) — but
  `imageBlockBudget.spend(n)` explicitly checks `n < 0` and rejects, so this
  potential 32-bit bypass is independently closed by the budget check, not
  just the per-entry check. Two independent guards, not one.
- No way to reset/multiply the aggregate budget: exactly one
  `newImageBlockBudget()` call per relocate entry point, shared by reference
  across both enumeration calls in that function — verified no second/stray
  budget construction exists anywhere in format/tiff.

Verified NOT over-eager (no false rejection):
- Ran the FULL real-world RAW corpus (142 files: DNG/ARW/NEF/ORF/RW2/CR2/CR3
  from exiftool/exiv2/metadata-extractor fixture sets) through
  `gometadata.Read` -> `gometadata.Write` at both HEAD and the pre-fix commit
  (7781cb0, via a temporary `git worktree`) — IDENTICAL result: 142 total, 127
  ok, 3 write-errors. The 3 write-errors (`IMG_1361*.dng`) are a PRE-EXISTING,
  unrelated "image block out of bounds" error (these fixtures are intentionally
  truncated test files whose StripByteCounts references bytes beyond the
  truncated file) — same exact error text at both commits, proving GM-W1
  introduces zero new rejections on real files.
- New positive-control unit tests (`TestWrite_LegitimateMultiStripStillRoundTrips`
  5000 strips, `TestWrite_LegitimateManySubIFDsStillRoundTrips` 50 SubIFDs) both
  pass, confirming the caps sit far above realistic legitimate values.

Testing: `go build`/`go vet` clean; `go test -race ./format/tiff/... ./format/raw/...`
green; 3 consecutive full-module `go test -race -count=1 ./...` runs green (no
flake, confirming 0d03bf5's fix for the TotalAlloc-measurement flake actually
worked); `FuzzTIFFInject` 2.8M execs/30s, 0 crashers (exercises the exact
relocate.go code path changed here).

## #262 (bacfa46) — JPEG countingReader 256 MiB aggregate cap
Adds `maxFileSize`/`ErrFileTooLarge` (matching sibling packages) to the one
container package that lacked it. INFO-severity hardening, not a live
vulnerability (APP segments already 16-bit length-bounded).

Verified the Seek-resets-budget design is NOT a bypass:
- Grepped every `.Seek(` call site in format/jpeg/*.go (non-test): all 4
  call sites use LITERAL offsets (0 or 2), never a value derived from parsed
  file content. `extractFullInternal` seeks the RAW reader `r` (not `cr`)
  once, BEFORE wrapping in `cr` — so `cr` itself is never seeked in that
  function (single continuous budget for the whole SOI+scan pass).
  `Inject` seeks `cr` exactly twice (both literal 0), and internally
  `extractOriginalIRB` does one more literal `Seek(2, ...)` on the same `cr`
  — 3 resets total, all fixed, all bounded in number by the CODE (not by
  attacker input), so an attacker cannot cause additional resets to multiply
  the effective allowed read volume beyond what real Inject logic requires.
  Each reset just means "the next logical pass over the (finite,
  attacker-fixed-size) stream gets its own maxFileSize allowance" — it cannot
  let a file bigger than N*maxFileSize (N = fixed small constant, 2-3) pass
  through, and in practice the SECOND pass (the main copy, which must consume
  the entire file including image data) independently bounds the whole file
  to maxFileSize on its own.
- `remainingFitsBudget()` calls `c.r.Seek(...)` directly on the wrapped reader
  (bypassing `countingReader.Seek`), so its 3 probing Seeks do NOT reset the
  budget — confirmed by reading the code, this is intentional (a peek, not a
  logical pass boundary).
- `maxFileSize` is a package-level `var` (test-overridable) but never mutated
  in production paths; all tests that mutate it correctly omit `t.Parallel()`
  with `//nolint:paralleltest` (same pattern as sibling packages) — no data
  race risk.

Verified no corruption / correct output:
- All 9 new `oom_gate_test.go` tests pass under `-race`.
- Full real-world corpus check: 1718 JPEGs from testdata/corpus, `Read`->`Write`
  round-tripped at HEAD and at pre-fix commit 7781cb0 (git worktree) —
  IDENTICAL OK/PARSEERR/WRITEERR counts (1513/17/188) and IDENTICAL file-name
  sets in each bucket. Output length matched exactly for every file in both
  runs. 7 files (`exiv2-bug922*.jpg` x3, `Android Depth Map*.jpg` x2, `Google
  Cardboard*.jpg` x2) differ in SHA-256 hash despite identical length —
  investigated and confirmed this is PRE-EXISTING, UNRELATED non-determinism:
  running the CURRENT (post-fix) code twice in a row on the same input
  produces 3 different hashes each time (verified with a 3x-repeat harness);
  byte-diffing two runs shows the differing bytes are exactly the
  xmp-extended `HasExtendedXMP` GUID (a random per-encode identifier, Adobe
  XMP Spec Part 3 §1.1.4) — nothing to do with bacfa46/202e34a/0d03bf5. Filed
  as an observation, not a finding, since it's out of scope for this diff and
  pre-dates it.
- `FuzzJPEGExtract` 11M execs/30s, `FuzzJPEGInject` 10.9M execs/30s, both 0
  crashers.

## 0d03bf5 — test-only de-parallelization
Single test file, zero production code touched. Removes `t.Parallel()` from
the 3 GM-W1 tests that measure process-global `runtime.MemStats.TotalAlloc`
(contaminated by concurrent sibling allocation, especially under `-race`).
Confirmed via 3 consecutive full-module `-race` runs, all green.

## Environment note (not a finding)
`FuzzTIFFExtract`'s exec counter plateaus after ~3-6s in this sandbox (e.g.
stops at 407,927 execs and sits at "0/sec" for the rest of a 30s budget) while
`FuzzJPEGExtract`/`FuzzJPEGInject`/`FuzzTIFFInject` all continue accumulating
execs normally for the full duration. Reproduced identically on the pre-fix
commit (7781cb0) via a temporary worktree — confirmed pre-existing environment/
coordinator quirk specific to that one fuzz target's corpus, NOT a hang or DoS
introduced by GM-W1. No crash, no timeout report, consistent PASS either way.
Worth knowing before assuming a fuzz run "isn't working" for that target
specifically in this sandbox.

## Verdict
CLEARED. All three commits reviewed are correct: GM-W1's caps reject before
any Count-proportional allocation on every reachable path (per-entry, tile,
nested-recursion, chained-IFD, size==0, and 32-bit-overflow angles all
independently closed), introduce zero new rejections on 142 real RAW corpus
files (byte-identical pre/post behavior), and the JPEG countingReader's
Seek-reset design is safe by construction (fixed literal offsets only, bounded
reset count) with zero output corruption across 1718 real JPEGs. Test-only
de-parallelization commit has no production impact. See
[[project_gmw1_imageblock_budget]] and [[project_task262_jpeg_maxfilesize]]
(go-performance-architect memory) for the original design rationale this
review independently verified.
