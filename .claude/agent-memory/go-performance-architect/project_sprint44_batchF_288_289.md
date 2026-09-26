---
name: sprint44-batchF-288-289
description: Sprint 44 Batch F (2026-09-25/26) — PNG skip-ignored-chunks/copy-verbatim-with-CRC-preservation (#288), TIFF/CR2/NEF/ARW/DNG metadata-prefix read via a new extent scanner (#289). Two real bugs found via fuzz/golden-hash gates (overflow panic, JIF-thumbnail data loss), then a coordinator-requested follow-up review found a DoS-class 268MB allocation bug and a real corpus-wide Read regression via a full-corpus per-file benchmark that the 5-fixture harness never exercised — both fixed with a fraction-based growth policy + small-file whole-read bypass. Key lesson: harness-fixture-only benchmarking is not sufficient evidence for a "no regression" claim; a per-file corpus sweep found problems 5 fixtures could not.
metadata:
  type: project
---

Batch F (#288, #289) implemented in one merged pass on
feature/44-performance-and-efficiency-laboratory-260925 (2026-09-25), uncommitted at
write time (explicit "Do NOT commit" instruction). Full gate green: build/vet/`test -race`
(whole repo), `golangci-lint run ./...` (0 new issues beyond the same 6 pre-existing —
`exif/bench_test.go` G404, 3× `examples/*` modernize, 2× nolintlint in
`exif/ifd.go`/`format/tiff/relocate_bigtiff.go`), `staticcheck ./...` clean,
`~/go/bin/govulncheck ./...` clean (0 vulnerabilities affecting code). 12 fuzz targets
(`FuzzPNGExtract`, `FuzzPNGInject`, `FuzzTIFFExtract`, `FuzzTIFFInject`, `FuzzCR2Extract`,
`FuzzCR2Inject`, `FuzzNEFExtract`, `FuzzNEFInject`, `FuzzARWExtract`, `FuzzARWInject`,
`FuzzDNGExtract`, `FuzzDNGInject`) each 60 s clean on the final code.

**Key technical findings:**

1. **#288's CRC-preservation policy is a deliberate, spec-matching design decision, not a
   vulnerability.** Inject copies every unchanged chunk's header+data+ORIGINAL CRC trailer
   verbatim, uninspected, and NEVER repairs a bad CRC on a chunk it did not itself write —
   matching ExifTool/Exiv2. This means an attacker-supplied file with a deliberately-invalid
   CRC on a pass-through chunk propagates that same invalid CRC into Inject's output
   unchanged. This is intended: HEAD (pre-#288) was silently *repairing* bad CRCs on
   unmodified chunks by re-reading+re-verifying+re-writing every chunk it touched, which is
   itself the spec deviation (a reader/writer must not "fix" data it didn't produce). Proven
   via an automated byte-level diff tool (not manual spot-checking): of the full PNG corpus,
   114 files' `Write` output SHA-256 differs from HEAD, and all 114 differ **only** in a
   4-byte CRC trailer, with the corresponding input chunk's original CRC independently
   confirmed invalid in all 114 cases.

2. **`io.ReadFull`'s bare `io.EOF` is indistinguishable from a clean end-of-stream when the
   CRC read starts at exactly 0 remaining bytes** — this is how HEAD's `Extract` silently
   accepted `testdata/corpus/png/exiv2/issue_790_poc2.png` (a truncated-iCCP-chunk file)
   instead of rejecting it, returning `(nil,nil,nil,nil)`. #288's fix computes `streamSize`
   once up front and bounds-checks `pos+length+4 > total` *before* any `Seek`/read for a
   chunk being skipped, so truncation is caught independent of how the reader signals EOF.
   Mirrors Exiv2's `pngimage.cpp` pattern (check declared length against known stream size
   before trusting it, never rely on a short-read error alone).

3. **CRITICAL — integer-overflow panic in a NEW extent scanner, found by fuzzing before any
   benchmark was recorded.** A crafted BigTIFF `ifd0Off = 0xFFFFFFFFFFFFFFFF` made a naive
   `off+countW > len(buf)` bounds check overflow and wrap to a small value, incorrectly
   passing, then panic slicing `buf[off:]`. Fix: a `fits(off, width, n uint64) bool` helper
   (`off > n` first, then `width <= n-off` — no addition that can itself overflow), applied
   at *every* point in the new `format/tiff/extent.go` where an offset is read from
   untrusted file content (IFD bounds, entry bounds, OOL value pointer, ExifIFD/GPSIFD/
   InteropIFD pointer, SubIFDs array element). `FuzzTIFFExtract` found this crash within
   the first second of running against the pre-fix code — re-run 60 s clean afterward. This
   is the same overflow-safety pattern already established in relocate.go's own bounds
   checks; extending a scanner to walk untrusted offsets must always use this pattern from
   the first line, never add-then-compare.

4. **CRITICAL — embedded-JPEG-thumbnail data loss found ONLY by the golden-hash gate, not
   by an exhaustive tag-value parity test.** #289's extent scanner deliberately excludes
   "image data a metadata value merely points to" (`StripOffsets`, `TileOffsets`,
   `JPEGInterchangeFormat`) — correct for `StripOffsets`/`TileOffsets` (never materialised
   into any `exif.EXIF` struct field; `enumerateImageBlocks` always re-reads them fresh
   from the full file at write time regardless of what the prefix contains), but **wrong**
   for `JPEGInterchangeFormat`(0x0201)/`JPEGInterchangeFormatLength`(0x0202): `exif.Parse`'s
   own `extractJPEGThumbnail` (exif/ifd.go) *actively slices those declared bytes into
   `IFD.ThumbnailData` during parsing itself*, for every IFD it materialises, and
   `exif.Encode` re-embeds that field verbatim on write. This is the ONE field in
   `exif.IFD`/`exif.EXIF` that behaves this way — Entries/Next are the only other fields,
   and both are already fully handled by the generic offset-walking logic. Omitting the
   thumbnail bytes from the prefix made `extractJPEGThumbnail`'s own bounds check
   (`jifLen64 > uint64(len(b))`) fail *silently* — no error, no crash, `ThumbnailData` just
   ends up `nil` — so this was invisible to an exhaustive `bytes.Equal` tag-value parity
   test across every `IFDEntry` (which explicitly and deliberately excludes `ThumbnailData`
   from comparison, correctly assuming it's "image data expected to legitimately differ")
   AND invisible to `go test ./...`/`-race`/fuzzing. It was caught **only** because the
   golden-hash gate compares full `Write` output SHA-256 against HEAD: `ThumbnailData ==
   nil` makes `enumerateImageBlocks` treat the declared JPEG range as a generic relocatable
   image block instead of trusting `exif.Encode` to have already embedded it — the same,
   uncorrectly-uncorrupted thumbnail bytes end up at a *different* file offset, which is
   invisible to any check that doesn't diff full write-output bytes. **Lesson: a "does the
   struct have the right values" parity test and a "does the write output match byte-for-
   byte" golden test catch genuinely different classes of bug — neither subsumes the
   other, and a field explicitly excluded from a parity test's scope needs an explicit,
   separate justification for why excluding it is safe, not just "it's image data."**
   Fix: extend the scan to include a JIF pair's declared byte range whenever both tags
   are present on an IFD, mirroring `extractJPEGThumbnail`'s own semantics exactly
   (reads offset as `TypeLong8`(8B, BigTIFF)/`TypeLong`(4B) via the entry's own declared
   width, matches EXIF §4.5.5 / task #141's BigTIFF-awareness precedent).

5. **The JIF-inclusion fix from #4 must be scoped to ONLY the IFDs `exif.Parse` actually
   materialises as `*exif.IFD` objects — IFD0, its own `.Next` chain, and the
   ExifIFD/GPSIFD/InteropIFD pointer targets — and explicitly EXCLUDE `SubIFDs` (0x014A)
   children.** `exif.Parse` never materialises a SubIFD as a `*exif.IFD` at all;
   `enumerateSubIFDs` (relocate.go) re-scans the full file directly at write time instead,
   entirely independent of what the read-time prefix contains. Applying the naive,
   unscoped version of fix #4 (grow for a JIF pair on *any* IFD reached during the walk,
   including via `TagSubIFDs`/`walkSubIFDArray`) was itself caught by the very AC this
   batch had to satisfy: `raw/metadata-extractor/Nikon D810.nef` carries a multi-megabyte
   medium-resolution preview inside a `SubIFDs` child (its top-level IFD chain's own
   `ThumbnailData` was `nil` on both sides either way — confirmed by direct inspection
   before concluding this), and the unscoped fix ballooned that file's prefix from ~253 KB
   to ~2.7 MB, blowing past `BenchmarkRead/nef`'s "B/op ≤1 MiB" target with zero
   round-trip benefit (nothing ever reads that SubIFD's declared thumbnail back into any
   `ThumbnailData` field). Implementation: `walkChain`/`walkIFD` take a
   `materializesThumbnail bool` parameter — `true` at the top-level `scanExtentPass` entry
   point and at the ExifIFD/GPSIFD/InteropIFD pointer-triggered recursion, `false` at the
   SubIFD-array-triggered recursion — and the JIF-driven `s.grow()` call is gated on it.
   **Lesson: when a fix generalises "this field matters for every X", verify the SAME
   traversal code doesn't ALSO reach objects that only superficially look like an X
   (a SubIFD is textually another IFD, but is not one exif.Parse ever turns into the
   struct the fix is protecting) — a benchmark AC caught this one, but it could just as
   easily have been a silent perf regression nobody measured.**

6. **`scanMetadataExtent`'s incremental grow-and-rescan loop can pathologically do
   MANY near-full-size reallocations for a file whose declared metadata legitimately
   spans (near) 100% of it** — found via `BenchmarkRead/tiff` on the synthetic fixture
   `tiff/metadata-extractor/Epson PerfectionV800.tiff` (819,606 B total, prefix also
   819,606 B once #289 fully "works": no separate image-data section exists to skip).
   Root cause: one pass's newly-revealed IFD only reveals the NEXT pass's requirement by
   a further few hundred bytes (each additional IFD nesting level "unlocks" the next),
   and `growBuffer` does a full `make(need)+copy(old)` on every pass — 3 successive
   near-full-size reallocations summed to ~2.5× the file's own size in B/op. **Pure
   geometric/doubling growth does NOT fix this**: doubling a small `len(buf)` (e.g. the
   65 KB initial prefix) doesn't reach anywhere near the ~816 KB the first real pass
   needs, so the first substantial grow still costs a full near-final-size allocation
   regardless of a doubling policy; doubling only helps SUBSEQUENT passes, and this file's
   subsequent passes were already small increments relative to what doubling from the
   PREVIOUS (already-large) buffer would produce. The fix that actually worked (single
   grow instead of three): since the caller already knows the real file's total size
   (`seekFileSize`, computed once in `Extract` before scanning), snap the grow target
   straight to the full file size whenever fewer than one initial-prefix-chunk's worth
   (`extentInitialPrefixSize`, 64 KiB) of the file would remain unread afterward — "don't
   leave a tiny straggler for a future pass to chase." This is a targeted heuristic for
   "we're already deep into what's effectively 100% metadata," not a general growth-policy
   change, and does NOT weaken the real-camera-file benefit (a genuinely small metadata
   fraction leaves a remaining-unread portion far larger than 64 KiB, so the snap never
   fires for those files).

7. **The `fits(off, width, n)` overflow-safe bounds-check pattern (finding #3) and the
   "verify no relocator mutates `base` before aliasing a buffer across Read/Write"
   discipline (Batch E finding #1) are now both load-bearing conventions in
   `format/tiff/extent.go` and should be the default starting point for any future
   offset-walking code added to this package** — not something to rediscover per task.

8. **`internal/testutil.CorpusFiles(t, subdir)` resolves `testdata/corpus/<subdir>`
   relative to the PACKAGE's own directory, not the repo root** — `format/tiff` has no
   `testdata/corpus` of its own committed (correctly: corpus is gitignored everywhere,
   downloaded via `make testdata`/`testdata/download.sh` which writes into the REPO-ROOT
   `testdata/corpus/` only). This means EVERY corpus-gated test in EVERY package
   (including `format/tiff`'s own pre-existing `conformance_test.go`) skips, not runs,
   in a dev checkout that hasn't had `testdata -> ../../testdata` (or similar) manually
   symlinked into each package directory — this is a pre-existing, environment-wide,
   universal condition, not specific to any one test. To VERIFY a new corpus-gated test
   actually exercises real files (not just "compiles and skips cleanly"), temporarily
   symlink `format/tiff/testdata/corpus -> ../../../testdata/corpus`, run the test, then
   remove the symlink before finishing (never commit it) — this is how
   `TestExtractPrefixParityWithWholeFile` (490 PASS/19 SKIP/0 FAIL) was actually proven to
   work against the real corpus in this session, while the version any CI/fresh-checkout
   run would see legitimately just skips per docs/TESTING.md §2.1's "corpus file absent"
   category (which the task's own AC text explicitly names as an acceptable outcome:
   "corpus-gated skip allowed").

## Follow-up round (coordinator review, 2026-09-26) — #289 write-cost isolation + corpus-wide Read regression

The coordinator flagged two gaps in the 5-fixture-harness-only evidence above and asked for
them to be closed before the security audit. Both were real, substantive findings requiring
further code changes — this is the single strongest piece of evidence in this whole
project that **a fixed small set of e2e harness fixtures is not sufficient to claim "no
regression"; a corpus-wide per-file sweep is required whenever a change makes
data-dependent trade-offs** (this design makes MANY: how many growth passes a file's IFD
chain needs, how close its metadata is to 100% of the file, etc. — properties that vary
per-file in ways 5 fixtures cannot sample).

9. **`write.go`'s pre-existing `readAllCapped` used stdlib
   `io.ReadAll(io.LimitReader(r, maxFileSize+1))`, not `iobuf.ReadAll`** — geometric,
   unknown-final-size buffer growth across a variable number of internal `Read` calls,
   vs. `iobuf.ReadAll`'s single `Seek(SeekEnd)`-sized `make`+one `ReadFull`. This was
   PRE-EXISTING code (not introduced by #289), but #289 made it run on every TIFF-family
   `Write` (removing the `m.rawEXIF`-reuse fast path) instead of only as a rare fallback —
   exposing an inefficiency that had always been there but never mattered until now.
   **Lesson: removing a fast path that bypassed some code entirely can expose that code's
   OWN pre-existing quality problems — always check what the newly-exercised code actually
   does, not just whether it's "correct."** Fix: change `readAllCapped`'s signature from
   `io.Reader` to `io.ReadSeeker` (verify EVERY call site already has one — grep before
   changing a shared helper's signature) and delegate to `iobuf.ReadAll`. Halved Write's
   B/op regression across every TIFF-family format and brought it to almost EXACTLY 2.0×
   HEAD's original B/op — i.e. "one more full exact-size read, nothing beyond it" — verified
   by computing the ratio itself (e.g. cr2: 43.77Mi ÷ 21.91Mi = 1.997×), not by code
   inspection alone. Verify claims like "this is now exactly one read" with an actual number,
   not just "the code looks right."

10. **CRITICAL — a corpus-wide per-file `Read` benchmark (using `testing.Benchmark` called
    directly from a `main()`, one call per corpus file, HEAD vs current) found a DoS-class
    allocation bug the 5-fixture e2e harness could never have exercised**: a 325-byte
    deliberately-malformed `exiv2` crash-test fixture allocated **~268 MB per `Read`**
    (≈`maxFileSize`, ~825,000× its own size) because `scanMetadataExtent` clamped a
    corrupt/adversarial `need` to `maxFileSize` (256 MiB) BEFORE calling `growBuffer`, which
    unconditionally `make([]byte, need)`s regardless of whether the subsequent read turns
    out short. Fix: clamp `need` to the file's OWN real, already-known `fileSize` FIRST,
    before the much looser `maxFileSize` fallback — a strictly tighter, always-safe bound
    (`fileSize <= maxFileSize` is already enforced by the caller) that costs nothing and
    eliminates the whole class (several more corpus fixtures had the identical pattern).
    **Lesson: `maxFileSize`-style constants sized for "the biggest file we'll ever accept"
    are the WRONG clamp for "how much should THIS operation, on THIS file, ever allocate" —
    always prefer the tightest bound actually known at the call site (here, the real
    `fileSize`, known via `Seek(SeekEnd)` before scanning ever starts) over a generic
    package-wide ceiling.**

11. **A second, non-adversarial corpus-wide finding: real, well-formed TIFF files whose IFD
    chain is only discoverable one link at a time can need MANY small-increment growth
    passes**, each a full `make`+`copy` in the pre-fix design, costing up to ~13× a file's
    own size in total B/op (`tiff/exampletiffs/mri.tif`, 230,578 B) purely from
    reallocation overhead, not from genuinely needing that much data. THREE iterations of
    growth-policy design were needed, in order, each REJECTED by evidence from the SAME
    benchmark before landing on the one that worked:
    - **Rejected: flat larger growth factor (8x, then 16x) for every pass.** Fixed the known
      pathological files, but a larger multiplier applied uniformly from the tiny 64 KiB
      initial buffer forces AT LEAST `64 KiB × factor` bytes on the first necessary grow
      regardless of the file's actual need — 16x pushed NEF's B/op to 1.07 MiB, breaking
      the "B/op ≤ 1 MiB" AC for a file whose real need was ~253 KB the whole time.
    - **Rejected: escalate the growth factor based on PASS NUMBER (any 2nd-or-later pass
      gets a large factor).** Directly mis-fired on `raw/metadata-extractor/Nikon D810.nef`:
      NEF genuinely needs 3 small-increment passes to resolve its IFD chain, but its `need`
      stays a STABLE ~0.62% of the 40.7 MB file across all of them — pass count alone cannot
      distinguish "many passes because the file's metadata is genuinely scattered widely"
      from "many passes because of tiny-increment discovery even though the true metadata
      is small." Ballooned NEF's B/op to 4.18 MiB.
    - **Adopted: escalate based on the FRACTION of `fileSize` a pass's `need` accounts for**
      (≥10% → jump straight to `fileSize`). This is the signal that actually discriminates
      the two patterns: NEF's fraction never climbs (never fires); a real multi-IFD TIFF's
      fraction climbs across passes (15% → 15% → 31% for
      `m1-8110934bb3b18d0e87ccc1ddfc5f0107.tif`) and fires as soon as it crosses. Combined
      with the existing tail-snap (absolute-terms "close to the end" case) and plain
      doubling (fallback bound), this closed the gap for every file in the corpus without
      re-breaking any RAW-format AC.
    - **Added on top: a size-based bypass.** The corpus benchmark showed every file whose
      extent-scan cost exceeded a plain whole-file read was ≤ ~2.6 MiB — files this small
      cost little to read in full regardless of their own metadata size, so
      `smallFileWholeReadThreshold` (4 MiB, comfortable headroom) skips the extent scanner
      entirely for them via the pre-#289 `extractWholeFile` path. Real camera RAW files
      (22–41 MB in this repo's fixtures) are unaffected.
    **Lesson: when tuning a growth/backoff/retry policy against one or two known-bad
    examples, ALWAYS re-run the full evidence set (here, all 500 corpus files) after each
    change — a fix that resolves the known cases can easily regress a DIFFERENT case the
    original investigation never looked at (NEF was never one of the "problem files" until
    the pass-count-based fix broke it).**

12. **A benchmark run taken while OTHER CPU-intensive background jobs (fuzz targets, another
    corpus benchmark) were still running produced dramatically wrong numbers** — a RoundTrip
    measurement showed 55-70% regressions across the board; re-run with zero other processes
    active, it was mostly net-neutral (`~`, p>0.05) with only a small +6.96% for arw. A
    corpus-wide Read distribution measured under the same contention showed p90=2.75x,
    max=14.1x; re-run cleanly, p90=1.05x, max=1.17x. **Lesson: always verify no other
    `go test`/fuzz/benchmark process is running (`pgrep`) before trusting a "regression"
    number that looks surprising or inconsistent with a prior measurement — CPU contention
    from a SEPARATE, unrelated background job in this same session is a far more likely
    explanation than a real code-level regression, and the fix is simply to re-measure with
    a clean machine, not to chase a phantom performance bug.**

**Final corpus-wide result (500 files, clean measurement)**: ns/op ratio (cur/HEAD) p50=1.011,
p90=1.054, p99=1.110, max=1.173; B/op ratio p50=1.0000, p90=1.0001, p99=1.0096, max=1.014.
Zero of 500 files exceed 1.20x in either metric.

## Follow-up round 2 (coordinator review, 2026-09-26) — reuse m.rawEXIF for Write when it already equals the whole file

13. **When #289's own extent scanner converges on reading the whole file (the ≤4 MiB
    small-file bypass, or the ≥10%-of-fileSize large-fraction snap), `m.rawEXIF` already IS
    the complete source — Write re-reading it from `r` anyway (the correct, but
    conservative, general-case behaviour from Follow-up 1) is unnecessary work HEAD never
    paid either.** Implemented the reliable signal the coordinator suggested directly:
    a NEW unexported `Metadata.rawEXIFIsWholeFile bool` field (zero public API surface
    change — an unexported field on an already-exported struct is invisible to external
    callers), set once by `Read` via `tiffFamilyRawEXIFIsWholeFile` (`read.go`), which
    compares `len(rawEXIF)` against the actual source size obtained via a single
    `Seek(SeekEnd)` (restoring the reader's position afterward) — **computed from the
    outcome, never assumed from which internal code path fired**. This is a materially
    simpler and more robust design than trying to have `format/tiff.Extract` itself
    report "did I take the whole-file path" as an extra return value: the LENGTH-equality
    check is correct regardless of which of scanMetadataExtent's several converge-on-whole-
    file mechanisms triggered (small-file bypass, large-fraction snap, tail-snap, or even a
    coincidental read that happens to need 100% of a file for unrelated reasons), and adds
    zero signature changes to any format-package function (checked call-site count first:
    `tiff.Extract` alone has 5 callers across 4 wrapper packages + 2 test files — changing
    its signature would have meant touching all of them for no additional correctness
    benefit over the length-comparison approach).

14. **Reuse safety is inherited, not re-derived**: `write.go`'s new `originalTIFFBytes`
    helper returns `m.rawEXIF` directly (no clone, no second read) when the flag is set,
    relying on the SAME "no relocator mutates its base/originalBytes parameter" invariant
    Batch E's own finding #1 established and this round re-verified still holds (no
    `relocate*.go` files were touched in this whole Sprint 44 Batch F sequence). This is
    the exact pre-#289 general-case fast path, now correctly SCOPED to only the inputs
    where the underlying assumption (m.rawEXIF is the whole file) is actually verified true,
    rather than unconditionally assumed as it was pre-#289.

15. **Result confirmed the two named regressions were fully attributable to the "no reuse"
    gap, not to anything else**: RoundTrip/tiff went from +57.17% to **−2.73%** (i.e. now
    net-BETTER than HEAD, not merely "no regression"), with B/op and allocs/op landing at
    EXACTLY HEAD's values (not just "close" — `~`, p=1.000 for allocs). Write/tiff's B/op
    and ns/op are similarly bit-for-bit identical to HEAD. cr2/nef/arw/dng (22–41 MB, always
    on the "prefix, not whole file" side of the threshold) show unchanged Write/RoundTrip
    numbers from Follow-up 1 — correctly: there is nothing to reuse for them, `m.rawEXIF`
    genuinely is just a prefix, and re-reading from `r` remains the only correct option.

16. **ARW's residual +6.96% RoundTrip ns/op did not reproduce on repeat clean measurement**:
    re-run twice more (both confirmed via `pgrep` to have zero other CPU-intensive processes
    running), one showed `~` (p=1.000), the other +4.76% (p=0.035) — while B/op stayed
    essentially flat (~1.0–1.03%) across all three runs. This pattern (stable B/op, unstable
    ns/op ranging 0–7%) is the signature of ordinary system-level measurement noise around a
    true value near 0%, not a reproducible code-level regression — confirmed by checking
    that NOTHING in ARW's write path changed in this round (it was on the "re-read from r"
    branch before AND after this fix, unaffected either way, identical in code terms to
    cr2/nef/dng which show no comparable instability) and that ARW's prefix size (849.2Ki)
    is not an outlier relative to the other formats (NEF's prefix, 817.6Ki, is comparable in
    absolute terms and shows no similar ns/op noise). **Lesson: when a coordinator or
    reviewer asks "why is X still regressed", the first check is always "does X actually
    reproduce on a clean re-measurement, and does its allocation profile (B/op) match its
    timing profile (ns/op)?" — a flat B/op with an unstable ns/op across repeated clean
    runs means noise, not a hidden cost centre; report it as such rather than inventing an
    explanation the evidence doesn't support.**
17. **Correctly identified that `tiff.Extract`'s own signature did not need to change** to
    implement this feature, by checking the actual call-site count (`grep -rn
    "tiff\.Extract("`) BEFORE designing the fix — a habit worth repeating whenever a
    "should I add a return value to this shared function" question comes up: count the
    blast radius first, then choose the design that minimises it while still being fully
    correct (here, a post-hoc length comparison at the call site, entirely in the root
    package, touching zero format-package files).
