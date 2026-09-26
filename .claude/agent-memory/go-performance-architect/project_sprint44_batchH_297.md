---
name: project_sprint44_batchH_297
description: Sprint 44 Batch H (2026-09-26), task #297 — single-copy iobuf.StreamCopyN for *bytes.Buffer sink / *bytes.Reader-full-remainder source, allocation-free exif.ParseOption construction via //go:noinline + fixed-size array; all AC targets met (≥30% Write speedup, allocs/op unchanged, 10/39 Read allocs restored); no commit
type: project
---

Batch H, one task (#297), coordinator-provided round-3 prototype at
`scratchpad/profiling-r3/srcx` (generic `io.CopyN` when sink implements `io.ReaderFrom` —
measured -31% to -48% Write time in-memory but 31-38% SLOWER file-to-file on darwin,
`(*os.File).ReadFrom` falls back to `io.Copy`'s own small buffer). Baseline for this
round is `a03b320` (Batch G, committed since — confirmed `git diff a03b320 -- <file>`
empty for every file I hadn't yet touched this round; HEAD was ALREADY a03b320 when I
checked, meaning the coordinator's own "Batch G committed" message was already true by
the time I looked — don't assume a stale HEAD, always `git log -1`/`git diff a03b320`
before trusting an earlier "not yet committed" assumption from a prior turn).

**`iobuf.StreamCopyN` (relocate_stream.go's/cr3.go's single production caller) — narrowed,
not generalized.** Two SPECIFIC endpoint-type checks, not an interface check:
- `w` is `*bytes.Buffer`: `(*bytes.Buffer).ReadFrom` reads straight into its own backing
  array. MUST bound it to exactly `n` bytes (ReadFrom itself reads to EOF unbounded) via
  `io.LimitedReader` — but constructing one fresh per call and passing `&lr` to `ReadFrom`
  ALWAYS heap-escapes (confirmed via `go build -gcflags="-m -m"`:
  `(*bytes.Buffer).ReadFrom`'s OWN escape summary treats its `io.Reader` parameter as
  escaping, even though ReadFrom never retains it past the call — this is NOT something a
  local, non-escaping stack value can dodge, since the ESCAPE DECISION lives in the
  STDLIB's own compiled escape summary for a method taking an INTERFACE parameter, not
  something inlining/local-scope tricks on MY side can influence). Fix: POOL the
  `*io.LimitedReader` (`limitedReaderPool`, mirroring iobuf.go's own `Get`/`Put` []byte
  pattern) — Get, set `.R`/`.N`, use, clear `.R`, Put. This is the CORRECT reading of
  "reusable limited reader" — I initially tried a local stack value first and had to
  MEASURE (not assume) that it still escaped before reaching for pooling.
- `r` is `*bytes.Reader` AND `n == r.Len()` (the reader's OWN remaining length, not "any
  n up to it"): `(*bytes.Reader).WriteTo` writes `r.s[r.i:]` — a zero-copy slice — in ONE
  `w.Write` call. Deliberately NOT generalized to `n < r.Len()`: bytes.Reader exposes NO
  public way to bound WriteTo to fewer bytes without copying (unexported s/i fields);
  `io.LimitReader`-wrapping loses WriteTo (LimitedReader has no WriteTo method);
  `io.NewSectionReader` ALSO has no WriteTo method in this Go version (1.27.1) — checked
  BOTH tradeoffs the task literally named before rejecting them, not by assumption. This
  narrower condition still covers this project's own dominant real pattern ("stream
  everything from here to EOF" — CR3 mdat+trailer, RW2 RawDataOffset-to-EOF) and composes
  correctly across MULTIPLE sequential calls against the same reader (proven by
  `TestStreamCopyNBytesReaderSourcePartial`: block 1 partial-copy falls back to pooled,
  block 2 exactly-remaining takes the fast path, same underlying reader).
- Short-read/error semantics preserved deliberately: `(*bytes.Buffer).ReadFrom` treats
  `io.EOF` as NORMAL termination and never returns it (unlike `io.ReadFull`, which
  StreamCopyN's pooled path already uses and whose convention callers expect) — added an
  explicit `if err == nil && written != n { err = io.ErrUnexpectedEOF }` post-check on
  BOTH fast-path branches to match.

**`parseEXIF` (read.go) +1 alloc/op — root cause was INLINING defeating a Go-compiler
optimisation, confirmed via `go build -gcflags="-m -m"`, not guessed.** `exif.SkipMakerNote`/
`AcceptRAWMagic`/`AliasThumbnail` are each a single closure literal (non-capturing except
AcceptRAWMagic, which captures `magic` by value) that Go normally represents as a static,
non-allocating value when the ENCLOSING function is opaque/non-inlined. Once
`go build -gcflags="-m -m"` showed "inlining call to exif.AliasThumbnail" etc. (these
3 one-line functions are trivially inlinable), the SAME diagnostic showed "func literal
escapes to heap in parseEXIF" for all 3 — inlining exposes the closure literal's
construction to escape analysis running on the COMBINED (caller+inlined-callee) body,
which reaches a DIFFERENT, worse conclusion for "closure fed into append/variadic" than
compiling the ORIGINAL function in isolation does. Fix (both, together, not either alone):
1. `//go:noinline` on all 3 option constructors (exif/exif.go) — restores the
   static-closure optimisation at ITS OWN un-inlined call site. Verified via
   `-gcflags="-m -m"` that this alone already removes ALL 3 "escapes to heap" diagnostics
   AND the `opts` slice's own "does not escape" already held even before this change (the
   SLICE backing array was never the problem — only the closures fed INTO it were).
2. Fixed-size `[3]exif.ParseOption` array + direct indexed assignment (not
   `append(nil, ...)`) in parseEXIF itself, per the coordinator's own explicit
   instruction — a structurally-guaranteed non-escaping construction, not one relying
   solely on escape analysis continuing to prove a growing nil-slice doesn't escape in
   some FUTURE Go compiler version. Belt-and-suspenders on top of (1), not redundant with
   it: (1) fixes the closures, (2) fixes the slice-growth PATTERN's own fragility.
No public API change: `exif.ParseOption`'s type and every constructor's signature
untouched — considered exporting an internal-config-bypass entry point instead (would
have been simpler) and REJECTED it specifically because it would add new exported surface,
which the task explicitly forbade.

**AC verification, evidence not assumption.** e2e harness (`scratchpad/profiling-r2/mod-cur`,
replace-directive pointing at the LIVE working tree — reused rather than rebuilt, since
r2's own e2e_test.go is byte-identical to r3's) vs a fresh `mod-a03b320` (new, replace
pointing at r3's own `src` snapshot — confirmed byte-identical to `a03b320` via
`git show a03b320:<file> | diff` on 2 files before trusting it). `count=6`:
Write/{cr2,nef,dng,orf,rw2,cr3} all 37.5-51.0% faster (target ≥30%), allocs/op IDENTICAL
to a03b320 on every one ("all samples are equal"); new `BenchmarkWriteFileSink` (copied
from r3's own `wf_test.go` prototype, *os.File both ends) shows NO significant regression
on any of 15 samples (target ≤+3%); `BenchmarkRead/tiff`=10, `BenchmarkRead/jpeg_exiftool`=39
allocs/op, exact AC match. Full corpus (3,281 files) `wcmp` vs a03b320: 24 diff lines,
zero unexplained (same pre-existing 7-file JPEG XMP non-determinism already known from
Batch G — reproduces on UNMODIFIED a03b320 too). `pdump`: zero diff (this task never
touches parsed content, only write-copy mechanism and read-side allocation shape).

**Regression tests added to the COMMITTED suite** (not just the scratch harness):
`internal/iobuf/streamcopy_test.go` — `TestStreamCopyNBytesReaderSourceFullRemainder`,
`TestStreamCopyNBytesReaderSourcePartial` (correctness of both fast paths + their
composition), `TestStreamCopyNBytesReaderSourceNoAddedAllocation`,
`TestStreamCopyNBytesBufferSinkNoAddedAllocation` (AllocsPerRun, <=1 alloc — the ONE
being the test's OWN `bytes.NewReader` call, not StreamCopyN); root package
`read_task297_test.go` — `TestParseEXIFAllocsPerRun` (coarse whole-Read ceiling, 15,
measured 7.0 at fix time — a REGRESSION guard, not an exact pin, since exif/xmp/iptc
parsing all contribute to the same number and aren't this task's own concern).

Full gate: build/vet/full suite/-race (whole repo)/staticcheck/golangci-lint (4
pre-existing baseline, down from Batch G's 5 — one more whack-a-mole nolint self-resolved
via a `--fix` run)/govulncheck all clean. Fuzz 60s each, zero crashers: `FuzzRead` (root),
`FuzzTIFFInject`, `FuzzCR2Inject`, `FuzzNEFInject`, `FuzzARWInject`, `FuzzDNGInject`,
`FuzzORFInject`, `FuzzRW2Inject`, `FuzzCR3Inject` (every format whose write path reaches
`iobuf.StreamCopyN`).

**Sprint 44 cumulative table (BENCHMARKS.md) refreshed for #297's inclusion**: overall
geomean ns/op -73.54% (was -71.25% pre-#297), B/op -85.68% (was -85.63%), allocs/op
-27.07% (was -25.12%) — Write's own gains for the 7 TIFF-family+CR3 formats jumped from
32-75% to 62-86% time reduction with #297 folded in, since the single-copy fast path
removes the SECOND of the two copies #291's own streaming architecture had already
reduced everything to. New raw benchstat: `scratchpad/profiling-r2/raw/final_ce1dc82_vs_batchH.txt`
(supersedes the pre-#297 `..._batchG.txt` in the same directory — same directory, don't
confuse the two when reading back later).

**No commit made** (explicit instruction, matching every other task this sprint).

See also [[project_sprint44_batchG_291_294]] for the shared e2e harness / wcmp / benchstat
methodology this task reuses without re-deriving.
