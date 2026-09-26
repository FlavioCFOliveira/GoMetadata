---
name: project_sprint44_batchG_291_294
description: Sprint 44 Batch G (2026-09-26) — #294 PNG graceful truncation, #293 ORF/RW2 prefix routing + large-fraction-snap removal, #292 CR3 moov-only stream write, #291 TIFF-family header/blocks split streaming + manufacturer-blob on-demand fetch + exif.writeIFD double-buffer elimination + scanExtentPass map-reuse allocs/op fix + CRITICAL BigTIFF off+size overflow panic fix (internal/boundscheck shared helper); Sprint 44 cumulative benchmark vs ce1dc82 (geomean -71%ns/-86%B)
type: project
---

Batch G, four tasks, one coordinator, no commit during the session (report-only;
commit happens in a later message). Branch feature/44-performance-and-efficiency-laboratory-260925.
ALL FOUR TASKS COMPLETE AND FULLY VERIFIED, including a coordinator follow-up round that
closed two AC gaps the FIRST pass left open (see below) — see BENCHMARKS.md "Batch G"
section for the full numeric report; this memory captures the non-obvious FINDINGS and
DESIGN DECISIONS, not the numbers.

**#294 (png) — trivial, complete.** Extract treats an overrunning/truncated chunk
(io.ErrUnexpectedEOF) as graceful-stop-return-collected-metadata, same as io.EOF.
Inject unaffected. 24 PNG files differ from f9a1c9c in full-corpus digest, zero
non-PNG divergence.

**#293 (orf/rw2) — complete, INCLUDING the coordinator follow-up fix.** orf.Extract/
rw2.Extract route through new exported `tiff.ExtractWithMagic` instead of
internal/tiffscan. RawEXIF() is now a PREFIX for ORF/RW2 too. write.go's
writeTIFFORF/RW2 needed originalTIFFBytes' rawEXIFIsWholeFile-flag fix (caught by full
test suite when #293 broke them — an ANTICIPATED risk the task text itself warned about).

extent.go's `nextGrowthTarget` doubling→+64KiB-additive-margin fix (done EARLIER in
THIS SAME session) explains why f9a1c9c (mod-head, committed) and the current working
tree show DIFFERENT `rawE=` (RawEXIF() prefix length) values in every wcmp/pdump corpus
comparison — NOT a regression, the cumulative already-verified growBuffer fix showing up
in later sweeps. Always mask/strip the `rawE=` field before diffing wcmp output across
rounds (`sed -E 's/rawE=[0-9]+/rawE=X/'`), or every sweep looks like it has N differences
when really it's 0. **This same additive-margin design intentionally leaves modest unused
headroom on some files** (e.g. `raw/metadata-extractor/DJI Phantom 4 (1).dng`: true need
88,970 B, converges on pass 1, but the +64 KiB margin from `nextGrowthTarget` is never
"spent" since need doesn't grow further — final buffer 154,506 B vs doubling's own
131,072 B for the SAME file under the old policy). This is NOT a bug and NOT something
task #293's follow-up (below) changed — it is the accepted, already-disclosed trade-off
of choosing a fixed additive margin over geometric doubling, verified via a dedicated
pass-by-pass trace (`scanExtentPass` called directly in a throwaway test) before
concluding this. Don't re-litigate it without new evidence.

**CLOSED (coordinator follow-up): extent.go's 10% large-fraction-snap heuristic was
REMOVED from `clampNeed`, not retuned.** The original finding (large fraction ≠ still
growing) was correct, but a naive fix ("pick a bigger/different threshold") would have
just moved the miscalibration elsewhere without evidence. Built a proper A/B simulation
harness instead: a throwaway `_test.go` in `format/tiff` (same package, so it can call
unexported `scanExtentPass`/`growBuffer` directly) that replays the REAL growth loop
under 6 candidate `clampNeed` policies (current / no-fraction-check / absolute-remaining-
bytes at 1/2/4/8 MiB) against every one of the 114 real TIFF-family corpus files above
`smallFileWholeReadThreshold` (4 MiB — files at or below that size bypass the scanner
entirely via a plain whole-file read, so they can never exercise this code path; the
ORIGINAL finding's own cited example, `tiff/metadata-extractor/
m1-8110934bb3b18d0e87ccc1ddfc5f0107.tif` at 1.02 MB, is now IRRELEVANT to this function
for that exact reason — it never reaches `clampNeed` at all today). Result: removing the
snap entirely regressed ZERO files' pass count or final buffer size while shrinking the
corpus-wide average converged buffer 42%; every absolute-margin variant up to 4 MiB
produced IDENTICAL results to removing it outright (no file in this corpus has a gap in
that range), and 8 MiB was measurably WORSE for a real file (Canon EOS 350D.CR2:
773,969 B → 7,797,386 B for one saved pass). Chose the simplest, most-conservative
option the evidence supports: delete the check, don't replace it with a new constant.
TG-7.ORF: 13,150,700 B (100%) → 1,580,032 B (12.0%), unchanged 2 passes. Regression tests
added: `format/tiff/task293_test.go` (`TestClampNeedNoLargeFractionSnap`, unit-level, 5
cases incl. the exact ORF-shape numbers; `TestExtractORFDoesNotOverreadOnStableLargeFraction`,
corpus-gated). **Methodology note: delete the throwaway A/B-simulation test file after
extracting its findings into the doc comment — it is not meant to ship** (this repo's
established pattern for "trace, decide, delete" diagnostics; see
[[feedback_corpuswide_bench_and_cpu_contention]]).

Read/orf's ns/op after this fix is RIGHT AT the 60 µs AC boundary (60.9 µs mean over
`count=6`; a fixed-iteration `-benchtime=2000x` run measured 55.2 µs; `-benchtime=200x`
measured 59.6–62.4 µs) — a CPU profile (`-cpuprofile`, 2000 fixed iterations) attributes
the real time to `runtime.memclrNoHeapPointers` inside `growBuffer`'s `make()` and the
underlying read syscall for the ~1.5 MB Olympus MakerNote blob this camera embeds as one
opaque out-of-line value — i.e. genuinely allocation/read-bound, not wasted work, no
further reduction possible without excluding real declared metadata (forbidden by the
100%-EXIF-compliant mandate). Reported to the coordinator as "essentially met, marginal
depending on measurement mode" rather than claimed as a clean pass.

**exif.AliasThumbnail()** (new ParseOption, exif/exif.go) — `read.go`'s `parseEXIF`
passes it UNCONDITIONALLY because `raw` there is ALWAYS `m.rawEXIF`. Aliases
(`b[jifOff:end:end]`, cap-clamped) instead of copying the EXIF §4.5.5 JPEG thumbnail.
Every MakerNote-internal `traverse(...)` call (~18 sites) and exported `ParseIFDAt` pass
`false` explicitly (deliberate conservative scoping). Real win: ARW B/op 935,341 ->
602,233. NEF barely moved (Nikon's own PreviewIFD thumbnail lives inside the MakerNote
sub-structure, not reached by this scoping decision) — KNOWN, DOCUMENTED gap, not a bug.

**Housekeeping, not a code bug (x2 this session):** (1) a stray untracked
`format/tiff/testdata/corpus` SYMLINK broke `exif.TestEncodedSizeMatchesEncode` and
spuriously corrupted 2 `TestConformance_R18_bigtiff_roundtrip_fidelity_corpus`
sub-tests — reproduced identically on `git stash` (present on f9a1c9c too). Removed. (2) A
stray untracked `zzgoldenD/` directory, self-documented in its own header comment as
"Temporary scratch tool (batch D golden capture); deleted after use," left over from an
earlier Batch D session despite that comment — removed as obvious intended cleanup, not
new scope. Neither is related to the #282/#283/#284 backlog bugs — do not conflate.

**NEW FINDING this round, reported not fixed (out of scope, Scope Discipline):
pre-existing Write non-determinism on 7 corpus JPEGs with very large embedded XMP.**
Discovered while re-verifying the FULL 3,281-file corpus (not just the 559-file
TIFF-family subset prior rounds checked) — `exiv2-bug922.jpg` (+ `_2`/`_3` copies),
`Android Depth Map.jpg`, `Google Cardboard.jpg` (+ `_2` copy): running the SAME unmodified
binary 3 times on the SAME file produces 3 DIFFERENT Write SHA-256 hashes. Confirmed this
reproduces IDENTICALLY on a pristine f9a1c9c build too (not introduced by anything this
session) — almost certainly a map-iteration-order issue in XMP serialisation for
properties these Google Photo-Sphere/Depth-Map files carry (large embedded base64
GPano/GDepth payloads). Reported to the coordinator, not fixed. **Any future full-corpus
wcmp comparison must exclude these 7 filenames (or run wcmp-cur/wcmp-head 3× and diff
each against itself first) or it will look like N differences when the truth is "0
explainable + 7 known-nondeterministic."**

**#292 (cr3) — complete, exceeds AC by ~100x on B/op.** Inject rebuilds ONLY moov in
memory, streams ftyp/pre-moov bytes and mdat+trailing via new shared
`iobuf.StreamCopyN` (internal/iobuf/streamcopy.go — fixed 64KiB pooled buffer). No
changes this round.

**#291 (tiff/raw) — NOW complete for ALL 7 formats, including the coordinator follow-up
closure of NEF/ARW/ORF/RW2's whole-file gap.**

Design (unchanged from first pass): `relocateTIFFFromParsed` (and NEF/ARW/ORF/RW2's own
siblings) return `(header []byte, blocks []*imageBlock, err error)`. New shared caller
`writeRelocated` (relocate_stream.go) streams blocks from `r` or slices a whole-file
buffer directly.

**CRITICAL BUG (first pass, still relevant): `enumerateIFDBlocks`/
`extractParallelOffsetBlocks`/`appendJPEGBlock`'s bounds checks must compare against
`fileLen` (a real `Seek(SeekEnd)`), never `len(base)`** — a real image block routinely
extends far past a metadata-only prefix by design. This class of bug recurred in EVERY
manufacturer-specific extractor touched this round (see below) — always grep for
`len(base)` comparisons first when threading a new fileLen-aware code path.

**CLOSED (coordinator follow-up): NEF/ARW/ORF/RW2 now stream too, via on-demand fetch of
exactly their own manufacturer-specific blob — NOT by reading the whole file.** New file
`format/tiff/relocate_extend.go`: `extendBase(base, r, fileLen, targetLen) ([]byte,
error)` grows a prefix to cover exactly `targetLen`, fetching ONLY the delta
`[len(base), targetLen)` via ONE `Seek+ReadFull`; returns `base` unchanged (zero alloc)
when already sufficient; safe with `nil r` in that common case. Per-format fix:
- **RW2 (RawDataOffset, "extends to EOF"):** the raw sensor DATA was ALREADY a standalone
  `*imageBlock` (streams via the existing shared mechanism) — the ONLY bug was
  `extractRW2RawDataBlock` sizing via `len(base)` instead of `fileLen`. Purest case, zero
  `extendBase` calls needed, just a fileLen-threading fix identical in kind to the
  CRITICAL BUG above.
- **ARW (SR2Private, 0xC634):** an INLINE 4-byte pointer (no declared byte count for
  extent.go to extend into) to a ~37 KB blob copied verbatim into the header. Two-step
  `extendBase`: first a generous fixed margin (`sonySR2IFDFetchMargin`, 4096 B) to read
  the SR2 IFD's own small fixed block + OOL areas, then a precise extend once
  `SR2SubIFDLength` is known.
- **NEF (Nikon MakerNote-embedded PreviewIFD):** PreviewIFD is nested ONE LEVEL DEEPER
  inside the MakerNote's own internal structure than extent.go's scanner ever walks (it
  treats the whole MakerNote as one opaque declared-range value). The PREVIEW IMAGE DATA
  itself is already a standalone `imageBlock` (streams normally) — only the small
  PreviewIFD/NikonScanIFD directory structure needed fetching
  (`nikonMNExtensionFetchMargin`, 8192 B), then a precise second extend.
- **ORF (Olympus OLYMP-type MakerNote ThumbnailImage, 0x0100):** same pattern as NEF —
  the MakerNote's own internal IFD scan reads absolute file positions directly, so is
  defensively extended (`olympMNIFDFetchMargin`, 4096 B); the thumbnail JPEG DATA itself
  is a standalone `imageBlock`, never fetched into `base`.

All 4 extractors' own signatures gained `(r io.ReadSeeker, fileLen uint64)` and now return
a 3rd value (`newBase []byte`) alongside `(info, err)`; every internal bounds check that
used to compare against `uint64(len(base))` now compares against `fileLen`. `write.go`'s
`writeTIFFNEF/ARW/ORF/RW2` switched from `originalTIFFBytes` (always whole-file) to
`tiffPrefixBytes` (the same prefix-or-whole-file resolver TIFF/CR2/DNG already used);
`originalTIFFBytes` became dead code (zero callers) and was removed.

**NEW FINDING/FIX this round: `exif.writeIFD`/`writeIFDBigTIFF` were double-buffering the
out-of-line value area** — discovered via `go tool pprof -alloc_space` on
`BenchmarkWrite/rw2` showing 49.6% of B/op attributed to a single line,
`valueArea = append(valueArea, e.Value...)`, ONLY because #291's other fixes had already
shrunk everything else to metadata-prefix scale (this was invisible before, dwarfed by
whole-file buffers). Root cause: build a separate `valueArea` slice, then
`append(out, valueArea...)` copies it into the real output — paying for the value area's
bytes TWICE. First attempted a narrower fix (pre-size `valueArea`'s capacity via an
arithmetic pre-pass instead of letting it grow via unbounded `append`) — this closed part
of the gap but NOT all of it, since the double-copy itself (not just append's doubling
overshoot) was the larger cost for a big value area (e.g. ORF's ~1.5 MB Olympus
MakerNote). Second, more invasive fix: restructured both functions into two per-entry
passes over the SAME entries slice — pass 1 fills the fixed-size entry records (12/20
bytes each) and computes the value area's EXACT final length (same alignment/sizing
arithmetic, now used for entryBuf-filling AND length computation in one walk); pass 2
`slices.Grow`s `out` once by that exact length and appends every out-of-line value
DIRECTLY into `out`, no intermediate buffer at all. This function is the SHARED encoder
every format's `Write` reaches via `write.go`'s `encodeEXIF` → `exif.Encode` whenever
`m.EXIF` is modified (not just the 7 TIFF-family containers) — confirmed via
`grep -rln "exif\.Encode"` that ONLY `format/tiff/*` calls it directly, but `write.go`'s
own `encodeEXIF` is the universal dispatch point, so ANY format's benchmark that calls
`SetCaption`/`SetCopyright` before `Write` exercises this path. **Given that blast
radius, verification went beyond the touched-file fuzz list**: re-ran the full
3,281-file corpus `wcmp` byte-identical sweep (not just the 559-file TIFF-family subset)
AND added `FuzzJPEGInject`/`FuzzPNGInject`/`FuzzWebPInject`/`FuzzHEIFInject` to the fuzz
sweep (60s each, on top of the 14 targets the coordinator's own list named) purely
because this function's reach is wider than the files that were literally edited — this
is the right instinct whenever a change touches a genuinely shared/central function; don't
scope verification to "files I edited" when the function itself fans out further.

Regression risk for this specific class of change (rewriting WHERE bytes get written, not
just pre-sizing a buffer) is meaningfully higher than a pure capacity pre-computation —
treat any future touch to `writeIFD`/`writeIFDBigTIFF` as requiring the SAME full-corpus
byte-identical bar, not a spot-check, given how many formats and tests depend on it being
exactly right.

Full verification for the FINAL state (all 3 fixes: extendBase manufacturer-blob fetch,
extent.go large-fraction-snap removal, writeIFD double-buffer elimination): build/vet/
full test suite/-race (whole repo)/staticcheck/golangci-lint (5 pre-existing baseline,
down from 6 — one gci-format issue self-resolved)/govulncheck all clean; full 3,281-file
corpus `wcmp` SHA-256 + `pdump` parsed-digest vs f9a1c9c: zero unexplained divergence
(only the 74 already-explained #294 PNG lines + the 7 known-nondeterministic JPEGs above);
18 fuzz targets 60s each, zero crashers. Final AC status: **Write B/op ≤2 MiB — ALL 7
TIFF-family formats now pass** (max Write/orf 1.457 MiB, down from 3.05 MiB after the
double-buffer fix alone); **NEF/ARW Write ns/op ≤0.6× f9a1c9c — both pass** (0.403×,
0.487×); **Read/rw2 ≤60µs/≤2MiB — both pass** (26.2µs, 773 KiB); **Read/orf ≤2MiB —
passes** (1.577 MiB), **ns/op median 59.5µs (n=10, meets ≤60µs), 95% CI [58.78, 60.53]µs
(upper bound marginally exceeds 60µs)** — see the precise-attribution round below for how
these numbers were refined.

**CLOSED (coordinator second follow-up, same session): NEF/ARW/DNG Read allocs/op vs
ce1dc82 precisely attributed via diff_base pprof, and the avoidable part removed.** The
coordinator correctly rejected my FIRST attribution ("xmp.Parse/exif.parseSingleIFD's
general allocation pattern") as unproven — I had eyeballed a noisy, unfocused
`alloc_objects` profile from a SINGLE run at low iteration count without a proper
`-diff_base` comparison against the SPECIFIC baseline (ce1dc82, not f9a1c9c) the AC
targets (20/24/32) were originally measured against, and without `memprofilerate=1`
(default sampling misses/under-counts small per-op deltas). Redone properly: built a
FRESH `ce1dc82` worktree binary (`mod-base`, already existed from an earlier round —
verified byte-identical to `ce1dc82` via `git show ce1dc82:<file> | diff` first) and a
fresh current-tree binary, both with `-test.memprofilerate=1 -test.benchtime=100x`
(exact per-allocation sampling, not the default 512 KiB-rate subsample), then
`go tool pprof -alloc_objects -focus="GoMetadata" -diff_base=base.prof cur.prof` — this
`-focus` step is essential, since without it the diff is dominated by testing/runtime/GC
framework noise (visible as `testing.(*B).ResetTimer` showing double-digit allocation
percentages, an impossible attribution that's the tell you need `-focus`).

Found: `scanExtentPass` allocates a FRESH `*extentScan{..., seenIFD: make(map[uint64]bool,
16)}` on EVERY growth pass inside `scanMetadataExtent`'s loop — 808 objects / 100
iterations = 8.08/op for NEF, by far the largest identified new cost (NEF/ARW typically
converge in 2-3 passes; each pass paid for a fresh struct+map). Fixed: hoist ONE
`*extentScan` above `scanMetadataExtent`'s loop, have `scanExtentPass` take it as a param
and RESET (not reallocate) its fields each call — `clear(scan.seenIFD)` specifically,
never `scan.seenIFD = make(...)` again. **Correctness note, not just perf**: clearing
between passes (rather than either persisting stale entries OR skipping the clear
entirely) is REQUIRED — a pass that exits early (`walkIFD` returns `ok=false`, buffer too
small) must let the NEXT pass, with a bigger buffer, revisit that exact IFD offset from
scratch; a stale "seen" entry surviving from the aborted prior pass would silently
under-report `need`. Verified via the full regression bar this project's "any change to a
correctness-sensitive shared scan/encode function" pattern requires (see #291's writeIFD
entry above for the same standard): build/vet/full suite/-race/lint/govulncheck/
staticcheck, full 3,281-file `wcmp`+`pdump` byte-identical (zero unexplained divergence,
same known 136-line PNG+7-JPEG-nondeterminism baseline), and — since this touches
`scanMetadataExtent`, the Extract entry point shared by ALL 7 TIFF-family formats, not
just the 3 the coordinator asked about — all 7 Extract-side fuzz targets (`FuzzTIFFExtract`
/`CR2`/`NEF`/`ARW`/`DNG`/`ORF`/`RW2Extract`), 60s each, zero crashers (these 7 had been
explicitly deferred from the PRIOR fuzz round's own note as "covered by corpus wcmp
instead" — this round's code change specifically touches what they exercise, so deferring
again would have been wrong).

Result: NEF 26→22, ARW 28→24 (EXACT parity with ce1dc82), DNG 39→35, RW2 21→17 (bonus,
not asked about — same fix, same code path), ORF 27.2→24 (ditto). **Remaining delta
(NEF +2, DNG +2-3, ARW +0) is INHERENT, not further reducible without a real safety
trade-off**: `growBuffer`/`readInitialPrefix`'s own `make()` calls (+1/op each) ARE the
metadata-prefix architecture (the price of the 90-99% B/op cut); `scanMetadataExtent`'s
now-ONE-TIME `*extentScan`+map allocation (~4/op) is inherent to cycle-safe IFD-chain
walking. **Considered and REJECTED: replacing `seenIFD map[uint64]bool` with a
linear-scan `[]uint64` to avoid the map allocation entirely** — `seenIFD` is shared across
the IFD0 chain AND every ExifIFD/GPSIFD/InteropIFD/SubIFD branch reached in ONE scan, so
its worst-case size is the PRODUCT of several independent caps (`maxExtentTraverseIFDs`
512 × however many SubIFD array elements a crafted file declares), not a small constant —
a linear scan against that adversarial worst case risks O(n²) CPU, a correctness/safety
regression this project's own priority order (correct → safe → fast) forbids trading for
a marginal further allocs/op win. ARW's own net-zero delta gets a bonus assist from
`exif.AliasThumbnail`'s pre-existing −2.02/op (Sony's PreviewImage thumbnail is aliased,
not copied, from the top-level IFD chain this format's own metadata reaches) — NOT
something this round changed, just an existing win that happens to fully offset this
round's own architecture-inherent cost for ARW specifically.

**Methodology lesson for next time an allocs/op delta needs attribution against a named
historical baseline**: don't eyeball a single unfocused memprofile at default sampling
rate and guess the call site from whatever's biggest in `-top`. Use
`-test.memprofilerate=1` (exact counts) + `-focus=<module path>` (strip
testing/runtime/GC noise) + `-diff_base=<the ACTUAL cited baseline's profile>` (not a
different, more convenient baseline) from the start — it is barely more work and gives an
answer precise enough to name the exact struct field (`seenIFD`) responsible, not just a
function name.

Also re-measured Read/orf ns/op properly per coordinator request: `count=10` (n≥8), sorted
samples 57.6-61.2µs, **median 59.5µs** (meets ≤60µs AC), 95% CI (t, df=9)
**[58.78, 60.53]µs** (upper bound marginally exceeds 60µs — reported honestly as "meets on
median, boundary on the tail," not oversold as a clean pass).

**CLOSED (coordinator security-audit follow-up, same session): CRITICAL BigTIFF
off+size overflow panic, bisected to 0ebf5d4 (pre-Sprint-44).** A security audit of
Batch G cleared the batch itself but surfaced a pre-existing bug in the SAME area of
code this batch's own changes touched: `extractParallelOffsetBlocks` (relocate.go) and
`writeRelocated`'s wholeFile==true branch (relocate_stream.go) both computed
`end := off + size` as a raw uint64 addition before `end > fileLen`. BigTIFF
StripOffsets/StripByteCounts as LONG8 (8-byte) fields let `off=MaxUint64-1, size=10`
wrap to `8`, passing the bounds check and then panicking on
`prefix[blk.srcOffset:end]` — `slice bounds out of range [18446744073709551614:8]`,
confirmed by temporarily reverting the fix and re-running the new regression test.

**Grepping broadly (as instructed) surfaced FOUR MORE genuinely exploitable sites in
`format/tiff/relocate_bigtiff.go`** (`ifdEntryTable`, `decodeOffsetArray`,
`parseIFDAtBigTIFF` — the latter two ALSO needed an overflow-safe multiplication guard,
`count > maxU64/elemSz`, since BigTIFF's own count field is itself an attacker-controlled
8-byte value, not just the offset) — these had NEVER been routed through extent.go's
`fits` at all, unlike extent.go's own read-side scanner which was hardened during its
OWN FuzzTIFFExtract-driven development. **AND a genuinely MORE severe, independently
discovered instance on the READ path**: `exif.extractJPEGThumbnail`'s
`end := jifOff + uint64(jifLen)` for a `TypeLong8` `JPEGInterchangeFormat` entry — reachable
from ANY top-level `Read`/`ReadFile` of a crafted BigTIFF file, no `Write` call required,
which is a WIDER blast radius than the originally-reported write-path bug. Cross-checked
`parseIFDEntryBigTIFF`/`traverseBigTIFF` (exif's OWN read-path BigTIFF entry parser) and
confirmed it is ALREADY correctly hardened (`maxBigTIFFCount = 1<<30` cap on `cnt`,
explicitly citing "prevents the count*sz overflow check below from needing to handle
wrap-around" — task #54's own read-support hardening pass) — so this class of bug is
specifically confined to code written OUTSIDE that original hardening effort (the
write-path relocate helpers, plus this one overlooked thumbnail-extraction function).

**Fix: one new shared package, `internal/boundscheck` (`Fits(off, width, n uint64)
bool`), not a second hand-derived copy.** `exif` cannot import `format/tiff` (the reverse
already holds), so extent.go's own private `fits` — used 12+ times, extensively
commented, cross-referenced by FuzzTIFFExtract's own found-during-development history —
was kept as a one-line delegate (`func fits(off, width, n uint64) bool { return
boundscheck.Fits(off, width, n) }`) rather than rewritten, so NOTHING in extent.go's own
call sites/comments needed to change. `exif/ifd.go` imports the same package directly.
This is the correct response whenever a coordinator says "reuse X, don't duplicate" AND
the fix needs to cross a package boundary that the ORIGINAL helper's own package can't
import: promote the shared logic to a new, dependency-free leaf package both sides CAN
import, rather than either duplicating the 3-line function or (worse) inventing an
import-cycle workaround.

**Found via my OWN test: `t.Parallel()` on TWO sub-tests sharing ONE `*exif.EXIF`
races** — `exif.EXIF` is documented (#245, XMPCONC-01) as unsafe for concurrent
mutation, and `relocateTIFFFromParsed`/`upsertIFD0Entry` mutates `e.IFD0.Entries` in
place. My first draft of the regression test parsed `e` ONCE outside the
`wholeFile`-true/false sub-test loop, then ran both sub-tests in parallel against the
SAME `e` — caught by `go test -race`, not by inspection. Fix: parse a fresh
`*exif.EXIF` INSIDE each parallel sub-test. General lesson: any test that calls
`t.Parallel()` on sub-tests sharing a parsed `*exif.EXIF`/`*xmp.XMP` fixture from an
outer scope needs its OWN independent parse per goroutine if any of those goroutines
mutate it (directly or via a relocate/encode call) — checked this by RUNNING `-race`,
not by remembering the rule; the mutation is often several call-frames deep
(`InjectWithEXIF` -> `relocateTIFFFromParsed` -> `upsertIFD0Entry`) and easy to miss by
inspection alone.

Verification for the final state (all fixes, whole session): full corpus (3,281 files)
`wcmp` byte-identical vs f9a1c9c (same known 136-line baseline, zero new divergence); 7
TIFF-family `*Inject` fuzz targets 60s each (not just `FuzzTIFFInject` — the shared
relocate.go/relocate_bigtiff.go functions this touches are used by all 7), zero
crashers; full-repo `-race` clean (including the fixed test-race above).

**Sprint 44 cumulative benchmark (coordinator-requested, ce1dc82 vs this session's
final tree, `count=6`, full `e2e` harness matrix):** overall geomean ns/op -71.25%,
B/op -85.63% (benchstat-flagged: some `AccessOnly` rows are exactly 0 B/op, invalidating
a strict geomean, but the delta itself is still benchstat's real computed number).
Per-format Read/Write time+B/op table and the full raw benchstat output are in
BENCHMARKS.md's own "Sprint 44 cumulative" section — raw data at
`scratchpad/profiling-r2/raw/final_ce1dc82_vs_batchG.txt` (session-local scratch, not
committed). TIFF-family RAW/DNG/CR2 + CR3 show 65-99.9% Read/Write time and B/op
reduction (cumulative effect of #289/#291/#292/#293 and their coordinator follow-ups);
JPEG/PNG/WebP show smaller wins (Sprint 44 didn't target their read/write paths as
heavily); HEIF/AVIF show `~` (no significant difference, correctly — Sprint 44 never
touched them).

See also [[feedback_corpuswide_bench_and_cpu_contention]],
[[project_sprint44_batchF_288_289]] for the growth-policy and corpus-sweep
methodology this batch continues.
