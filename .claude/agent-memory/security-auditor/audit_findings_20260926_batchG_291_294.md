---
name: audit-20260926-batchG-291-294
description: Sprint-44 Batch G (#291-#294) security pass — CLEARED, and the follow-up fix for the one CRITICAL finding (BigTIFF off+size overflow panic) — RE-VERIFIED FIXED, also CLEARED. Highest-risk batch of the sprint (TIFF-family write now streams from the source reader). exif.writeIFD/writeIFDBigTIFF two-pass restructuring, relocate_stream.go/relocate_extend.go, cr3.Inject streaming, extent.go growth-policy change (doubling->additive) all verified via corpus-wide byte-identical regression (3339 files) + targeted adversarial PoCs
metadata:
  type: project
---

**RE-AUDIT 2026-09-26 (narrow, delta-only): CLEARED.** The BigTIFF off+size overflow panic
reported below was fixed via a new shared `internal/boundscheck.Fits(off, width, n)` helper
(identical semantics to extent.go's own `fits`, which now delegates to it) applied at every site
the sweep found: relocate.go (`appendJPEGBlock`, `extractParallelOffsetBlocks`, `extractRawIFD` via
new `rawEntryValueEnd`), relocate_stream.go (`writeRelocated`'s wholeFile branch — `fits()` guards
`end := blk.srcOffset+blk.size` before it is computed), relocate_bigtiff.go (`ifdEntryTable`,
`readRawEntryAt`, `decodeOffsetArray`, `parseIFDAtBigTIFF` — plus a `count > maxU64/elemSz`
overflow-safe multiplication guard added everywhere `count*elemSz`/`sz*count` is computed from an
untrusted BigTIFF LONG8 field), and `exif/ifd.go`'s `extractJPEGThumbnail` (a Read-path instance
the sweep additionally found, guarding both the alias and copy return paths). Verified:
- Original PoC (BigTIFF StripOffsets/StripByteCounts LONG8 pair, off=MaxUint64-1/-2, size wrapping
  off+size to a small residual) no longer panics on Write, for all three variants tested (small
  wholeFile=true, large streaming wholeFile=false, and an alternate wrap-residual) — now returns a
  clean `ErrBlockOutOfBounds` error.
- A new, independently-constructed PoC targeting the Read-path instance (`extractJPEGThumbnail`,
  BigTIFF JPEGInterchangeFormat/Length LONG8 pair with the same off+size wrap) confirmed Read no
  longer panics either (returns successfully with ThumbnailData simply absent).
- `boundscheck.Fits` boundary semantics independently re-derived and confirmed correct: off+width==n
  is allowed (inclusive), width==0 at off==n is allowed (empty range at EOF), any off>n is rejected
  before the subtraction (no underflow), and the off>n short-circuit means a wrapped/huge off is
  always caught regardless of width.
- `format/tiff/security_bigtiff_overflow_test.go` (new regression test, 4 sub-tests: wholeFile x2,
  streaming x2) passes; `FuzzTIFFInject`'s BigTIFF seed corpus already covered huge-count and
  past-EOF-offset cases pre-existing.
- Broader sweep for remaining unchecked off+size/count*size on untrusted values across
  format/tiff, format/raw/*, exif, internal/iobuf: found one adjacent call site
  (`patchRawIFDOffsets`'s `for j := range entry.count` in relocate.go, re-reading a SubIFD's raw
  BigTIFF LONG8 count independently of the parsed/enumerated block list) that INITIALLY looked like
  an unbounded-iteration DoS, but on tracing is transitively protected: `entry.Count` in the parsed
  `exif.IFDEntry` is saturated via `min(entry.count, math.MaxUint32)` (relocate_bigtiff.go:357, a
  true saturating min, not a wrapping truncation) BEFORE `extractParallelOffsetBlocks`'s own
  `n > maxImageBlocksPerOffsetEntry (65536)` rejection runs — so the only entries that ever
  populate `si.blocks` (the gate for `patchRawIFDOffsets` to run at all) already have a true raw
  count <= 65536, which `patchRawIFDOffsets`'s later independent re-read of the SAME bytes will
  also see. No exploitable gap found; not flagged as a new issue.
- Corpus-wide regression: diffed the current tree's Write() output hashes for testdata/corpus/tiff
  and testdata/corpus/raw against the exact "after" baseline captured during the original Batch G
  audit (before this fix) — 0 differences, confirming zero behavior change on valid files.
- Fuzz (45s each, all PASS, 0 crashers): FuzzTIFFExtract, FuzzTIFFInject, FuzzParseEXIF, FuzzRead.
- go build/vet clean; go test ./... all green; go test -race on root/exif/format/tiff clean.

Verdict: CLEARED for commit.

Batch G audit 2026-09-25/26 (uncommitted diff on HEAD f9a1c9c): exif/ifd.go writeIFD/writeIFDBigTIFF
two-pass rewrite + AliasThumbnail (#293), new format/tiff/relocate_stream.go (writeRelocated,
writePassThrough) + relocate_extend.go (extendBase) + internal/iobuf/streamcopy.go (StreamCopyN)
for #291's stream-from-r write path, format/raw/cr3/cr3.go Inject streaming rewrite (#292),
format/tiff/extent.go growth-policy change doubling->additive (#293), orf/rw2 Extract via
tiff.ExtractWithMagic + rawEXIFIsWholeFile extended to ORF/RW2, format/png/png.go Extract
graceful-stop on overrunning chunks while Inject stays strict (#294).

**PRE-EXISTING CRITICAL FINDING (not a Batch G regression) — BigTIFF off+size overflow panic.**
`extractParallelOffsetBlocks` (relocate.go) and `writeRelocated`'s wholeFile==true path
(relocate_stream.go) both compute `end := off + size` as a raw, non-overflow-safe uint64 addition
(unlike extent.go's own `fits()` helper). A BigTIFF file with a StripOffsets/StripByteCounts pair
using LONG8 (8-byte) fields can set off=MaxUint64-1, size=10 so off+size wraps to 8, bypassing the
`end > fileLen` bounds check entirely. For the wholeFile==true path (small files, or any file where
extent.go's scanner converges on the whole file), this reaches `prefix[blk.srcOffset:end]` with
srcOffset far exceeding cap(prefix) — a Go runtime slice-bounds panic ("slice bounds out of range").
CONFIRMED via bisection across 3 worktree checkouts that this is NOT introduced by Batch G: PoC
panics identically at 0ebf5d4 ("feat(write): enable BigTIFF container writes via public API",
long before Sprint 44) and at every commit since. Batch G's NEW streaming path (wholeFile==false,
real large files) actually converts this exact scenario into a clean, non-panicking error
(`int64(hugeOffset)` becomes negative, `bytes.Reader.Seek` rejects it) — an incidental partial
mitigation, not a regression. Recommend a separate task using extent.go's `fits()` pattern in both
call sites; PoC and worktree-bisection commands are in this session's history for the fix owner.
This does NOT block Batch G's own clearance (no defect introduced by this diff), but is flagged
prominently per the "last line of defense" mandate.

**exif/ifd.go writeIFD/writeIFDBigTIFF two-pass rewrite — VERIFIED SOUND.** Pass 1 (fills entryBuf,
computes `sizeOff`/`valueAreaLen`) and pass 2 (appends OOL value bytes into `out` via
`slices.Grow` pre-sizing, replaying `curOff` from the same `valueOff` start) apply byte-for-byte
identical alignment/threshold arithmetic in the same entries-iteration order, so they stay in
lockstep by construction — confirmed by reading both loops side by side (same inline thresholds:
<=4 classic, <=8 BigTIFF; same odd-offset padding; same zero-fill-shortfall padding). The one
edge case where `len(e.Value) > total` (a caller-constructed IFDEntry with a Value longer than its
own declared Count*typeSize) causes the SAME misalignment in both old (single-pass) and new
(two-pass) code — confirmed pre-existing (matches Batch C's tracked "Len(Value)>Count*size
misplacement only via caller-built IFDEntry (LOW)" gap), not a Batch G regression, and not
reachable via untrusted file bytes (exif.Parse never produces such an entry).

**relocate_stream.go / relocate_extend.go / cr3.go streaming rewrites — VERIFIED SOUND.**
`writeRelocated`'s bounds check (`end > fileLen`) uses the TRUE file length (threaded through
enumerateIFDBlocks/extractParallelOffsetBlocks/appendJPEGBlock as `fileLen`, distinct from
`len(base)` which may be just a metadata prefix) consistently in both the wholeFile and streaming
branches. `iobuf.StreamCopyN` has exactly one Put per call on every return path, no
use-after-Put, `n<=0` is a safe no-op. `extendBase` clamps targetLen to fileLen defensively
regardless of caller arithmetic (fails closed: an overflowed targetLen from a caller can only
result in fetching LESS data than intended, never more, and downstream bounds checks the callers
already had degrade gracefully on insufficient data). cr3.go's Inject streaming reassembly
(`r[0:moovStart) + newMoovBox + r[moovEnd:fileLen)`) reproduces the old in-memory concatenation
exactly; moovStart/moovEnd/oldMoovSize threading confirmed consistent with the pre-#292
findMoovRange semantics by direct comparison.

**extent.go growth-policy change (doubling -> additive +64 KiB) — quantified, not blocking.**
Coordinator's "quadratic work" concern is legitimate in theory (arithmetic, not geometric, buffer
growth reintroduces O(passes^2) total-bytes-rescanned in the worst case, vs doubling's O(final
size)) but empirically bounded to a low constant in absolute wall-clock terms by the pre-existing
hard caps (maxExtentGrowthPasses=64, maxExtentIFDEntries=65535/IFD, maxExtentTraverseIFDs=512
chain length, maxFileSize=256 MiB): a purpose-built worst-case file (512 IFDs x ~40000 entries
each, 234 MiB, tuned so each +64KiB grow reveals ~1 more IFD) converges in 147ms — statistically
indistinguishable from the OLD doubling policy's 150ms on the SAME file (both hit the entry-count
cap fast regardless of growth strategy). A smaller, more surgically-tuned adversarial construction
(64 IFDs x 700 entries, spaced to maximize pass count) showed the clearest relative regression:
16ms (additive) vs 2.7ms (doubling) — a ~6x slowdown, but both trivially fast in absolute terms.
No construction tested pushed wall-clock time past ~150ms. Not a practical DoS vector; noted as a
LOW-severity, bounded performance regression for adversarial inputs specifically, not blocking.

**AliasThumbnail retention — VERIFIED SAFE.** Exactly one call site in the entire codebase
(read.go's parseEXIF, `opts = append(opts, exif.AliasThumbnail())`), applied only to `raw` ==
`m.rawEXIF` — a field retained unmodified for the *Metadata's entire lifetime (confirmed in Batch F:
no other assignment to m.rawEXIF exists anywhere) — exactly matching the documented safety
contract. No transient/pooled buffer ever receives this option.

**rawEXIFIsWholeFile extended to ORF/RW2 — VERIFIED SAFE.** orf.Extract/rw2.Extract now route
through tiff.ExtractWithMagic (the same #289 prefix scanner), so the flag's semantics (computed
once via streamSize-based length comparison, immutable post-construction) extend consistently; no
new desync path introduced (same "set once together in Read(), never reassigned" invariant
established in Batch F still holds after grepping for m.rawEXIF assignment sites).

**#294 PNG Extract graceful-stop — VERIFIED SAFE AND WELL-SCOPED.** `Extract`'s chunk loop now
treats io.ErrUnexpectedEOF (checkChunkTruncation's wrapped error) the same as io.EOF: stop and
return whatever was already collected, no error — restoring the PRE-#288 (ce1dc82) tolerant
behavior that #288 had accidentally tightened. Inject's own truncation check is untouched and
still rejects a truncated source outright (a materially different, unsafe operation: rebuilding a
file that silently drops trailing content). Confirmed via corpus-wide diff: exactly 37 PNG test
fixtures flip from Read-time failure to Write-time failure (Read now succeeds gracefully; the
later Write still correctly refuses) — matches the change's own stated intent precisely, no
partial/garbled rawEXIF or rawXMP is ever returned (checkChunkTruncation rejects BEFORE any
fn() callback runs for the overrunning chunk, so that chunk's data is simply never captured).

**Corpus-wide regression (the single highest-leverage check this session).** Read+SetCaption+Write
across the ENTIRE 3339-file real-world testdata/corpus (all formats), compared via git-stash
before/after SHA-256: the ONLY differences are (a) 3 already-known, pre-existing non-deterministic
JPEG extended-XMP-GUID files (exiv2-bug922 x3, Android Depth Map x2 — reconfirmed non-deterministic
by running the SAME "after" build twice and seeing the SAME 2 files diverge from themselves; a
third file, "Google Cardboard.jpg", newly observed in this class but confirmed via the same
double-run test to be the identical pre-existing non-determinism, not a Batch-G-specific issue),
and (b) exactly the 37 PNG READ_ERROR->WRITE_ERROR reclassifications #294 intentionally causes.
Zero other differences across every TIFF/CR2/NEF/ARW/DNG/ORF/RW2/CR3/HEIF/WebP/PNG/JPEG file in the
corpus — direct evidence that writeIFD/writeIFDBigTIFF, the streaming write-path rewrite, and the
extent.go growth-policy change all produce byte-identical output to the pre-Batch-G baseline for
every real file this project has.

Tooling: go build/vet clean; go test ./... all green; go test -race on root, exif, format/tiff,
format/png, format/raw/{arw,cr2,cr3,dng,nef,orf,rw2} clean; govulncheck not runnable (pre-existing
toolchain mismatch, unchanged). Fuzz (all PASS, 0 crashers): FuzzTIFFExtract/FuzzTIFFInject (45s
each), FuzzCR2/ARW/NEF/DNG/ORF/RW2 Inject (30s each), FuzzCR3Extract/FuzzCR3Inject (30s each,
~5.5-5.8M execs), FuzzPNGExtract/FuzzPNGInject (30s each, ~3.4M execs), FuzzParseEXIF (30s),
FuzzRead (40s, 2.1M execs). Re-ran Batch E's double-write harness (0 new mismatches beyond the
same 3 known non-deterministic JPEGs, 0 rawEXIF/input mutations) and concurrent-write harness (571
files x 8 concurrent Write goroutines from one shared *Metadata, independent readers, -race clean,
0 divergent outputs) against the current Batch G tree.

Verdict: CLEARED for commit. One pre-existing CRITICAL finding (BigTIFF off+size overflow panic,
bisected to 0ebf5d4) reported for separate remediation — explicitly NOT a Batch G regression and
does not block this batch.
