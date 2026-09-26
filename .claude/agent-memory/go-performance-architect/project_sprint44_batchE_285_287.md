---
name: sprint44-batchE-285-287
description: Sprint 44 Batch E (2026-09-25) — tiff/raw write clone removal + presizing (#285), cr3/orf/rw2 read retention via exif.AcceptRAWMagic + moov-only CR3 Extract (#286), xmp name-terminator LUT (#287). Discovered fidelity-improving 1-byte divergence on 3 adversarial RW2 POC fixtures, AND that pre-sizing ARW's final ~25MB buffer was measurably SLOWER than natural append growth (reverted for that one call site; see finding #3.5).
metadata:
  type: project
---

Batch E (#285, #286, #287) implemented in one merged pass on
feature/44-performance-and-efficiency-laboratory-260925 (2026-09-25), uncommitted at
write time, following a dedicated profiling round (round 1, HEAD ce1dc82) whose harness
and prototypes are referenced in `profiling-r1-sprint44` memory. Full gate green: build/
vet/`test -race` (whole repo), `golangci-lint run ./...` (0 new issues; same 6
pre-existing issues as HEAD — `exif/bench_test.go` G404, 3× `examples/*` modernize,
2× nolintlint in `exif/ifd.go`/`format/tiff/relocate_bigtiff.go`), `staticcheck ./...`
clean, `~/go/bin/govulncheck ./...` clean. 12 fuzz targets (`FuzzCR3Extract`,
`FuzzCR3Inject`, `FuzzTIFFInject`, `FuzzCR2Inject`, `FuzzNEFInject`, `FuzzARWInject`,
`FuzzORFInject`, `FuzzRW2Inject`, `FuzzDNGInject`, `FuzzParseEXIF`, `FuzzParseXMP`,
`FuzzRead`) each 60 s clean, zero crashers.

**Key technical findings:**

1. **#285's real unlock was proving no relocator mutates `base`** — `write.go`'s six
   `writeTIFF*` functions each did `bytes.Clone(m.rawEXIF)` (or an equivalent `make+copy`
   for ORF/RW2) "defensively," but a grep for every `base[i] =` write site across
   `format/tiff/relocate*.go` found exactly two: the ORF/RW2 in-place magic patches
   (`base[2]=0x2A; base[3]=0x00`) — nothing else in the whole TIFF/CR2/ARW/NEF/ORF/RW2
   relocation family ever writes into its `base`/`originalBytes` parameter. Once #286
   removed those two patches (see below), `RawEXIF()` already cloning for external callers
   was sufficient to make aliasing `m.rawEXIF` directly safe everywhere. **Always grep for
   actual index-assignment writes into the parameter before assuming a clone is load-bearing
   — a "defensive copy" comment is not proof the copy is still necessary.**

2. **Pre-sizing an IFD encode is USUALLY a pure mechanical swap (`exif.Encode`→
   `EncodedSize`+`EncodeInto`+`relocatedLen`/`finalCap`), but it is NOT a universal win —
   verify per format, per call site, with an interleaved benchmark, not just by analogy to
   a sibling format that benefited.** ARW's SKELETON pass (step 6) benefits from
   `exif.EncodedSize` exactly like NEF/ORF/RW2 (pure layout arithmetic, no bytes written,
   strictly cheaper than a full `Encode`). But ARW's FINAL pass (step 9) was measured
   SLOWER with `EncodeInto(make([]byte,0,N), e)` than with plain `exif.Encode(e)` (natural
   `append` growth) — see finding #3.5 below for the full isolation and root cause. The
   general shape (maker inserting an extra segment between SubIFDs and image blocks, e.g.
   ARW's SR2Private) still needs its own length function (`arwRelocatedLen`, independent of
   `computeSubIFDsSize`/`sr2ActualSize`'s pointer-*value* arithmetic — Batch C already
   documented those two have a pre-existing, out-of-scope, odd-`ifdEnd`-parity edge case)
   — but that function's result can be used PURELY as a post-hoc invariant check
   (`len(finalTIFF) != finalLen`) without ever feeding a `make()` call, which is exactly
   ARW's final shape after the fix.

3.5. **A single large `make([]byte, 0, N)` (tens of MB) can cost MORE wall-clock time than
   reaching the same size via `append`'s incremental doubling growth, even though it
   allocates less TOTAL memory and does fewer `mallocgc` calls.** Measured on ARW write
   (Sony ILCE-7M3.arw, ~25 MB): reverting `relocateTIFFFromParsedARW`'s FINAL-pass
   `EncodeInto(make([]byte,0,finalCap(finalLen)),e)` back to plain `exif.Encode(e)` (skeleton
   pass still uses `EncodedSize`) improved ns/op from −21.93% to −30.96% vs HEAD (n=8,
   p=0.000 both ways) — i.e. the "optimization" cost ~9 percentage points of the ns/op win,
   despite REDUCING B/op further (fewer allocs too, since the wasted growth-then-copy work
   nominally allocates more total bytes over time). Root cause, found via `samply` (Go's
   built-in pprof is biased on macOS): `runtime.memclrNoHeapPointers` self-time in the
   focused `gometadata.Write` call tree was **11.45%** with the pre-sized buffer vs **0.20%**
   with natural growth, for the IDENTICAL workload. A `make([]byte, 0, N)` call requests N
   bytes from the allocator regardless of the requested `len`, and if the runtime cannot
   prove those bytes are already OS-zero-filled fresh pages (e.g. a busy benchmark loop
   reusing the same heap region across iterations, so the span is "dirty" from the PRIOR
   iteration's write), it unconditionally memclrs the WHOLE N bytes up front in one big
   chunk; whether the resulting decision is genuinely cheaper is apparently workload/
   allocator-state dependent, but it was NOT cheaper here. **Practical rule this batch
   adopted: after ANY presizing change to a large (multi-MB), file-size-proportional final
   buffer, verify with an interleaved benchmark on a REAL large fixture, not just smaller
   allocs/B/op numbers — B/op and ns/op can move in OPPOSITE directions.** The isolation
   methodology that found this: three `git worktree`s (unmodified HEAD; HEAD + ONLY the
   write.go clone-removal, mimicking the round-1 prototype's exact scope; HEAD + clone-
   removal + full EncodedSize/EncodeInto presizing) benchmarked pairwise with `benchstat
   -count=8`, isolating the ONE variable (the final-pass presizing) that the round-1
   prototype never touched for this format (its own `git diff` shows zero changes to
   `relocate_arw.go`) — which is also why the round-1 prototype's own ARW number (−30%) and
   this batch's INITIAL full-presizing number (−21.3%) disagreed: they were measuring
   different code. Whether the same effect leaves headroom on NEF/ORF/RW2/TIFF's own final
   buffers (all of which DO benefit from presizing today, comfortably past their targets)
   was not investigated — worth a follow-up profiling pass, not assumed safe by analogy.

3. **A "presize + `EncodeInto`" fix does not automatically fix a SEPARATE post-relocate
   buffer-doubling step.** RW2's `insertRW2GUIDAndShiftOffsets` and CR2's
   `insertCR2MarkerAndShiftOffsets` each build a whole NEW `len(finalTIFF)+delta`-sized
   buffer purely to shift bytes right by a small fixed header-insertion delta (16 / 8
   bytes) — this is independent of and NOT fixed by pre-sizing the `exif.EncodeInto` step
   that comes before it. Missing this cost RW2's write B/op an extra ~2× file size in the
   first pass of this batch (caught only by comparing measured ratios against the file
   size, not by tests — "measure to decide" in action). Fix pattern (applied to both):
   reserve `delta` extra bytes of spare CAPACITY (not length) in the upstream
   `exif.EncodeInto(make([]byte, 0, finalCap(finalLen)+delta), e)` call, then in the
   insertion step, `if cap(finalTIFF) >= len(finalTIFF)+delta { out =
   finalTIFF[:len(finalTIFF)+delta]; copy(out[insertPoint+delta:], finalTIFF[insertPoint:]);
   copy(out[insertPoint:insertPoint+delta], newBytes) } else { /* old make+copy fallback */
   }`. Go's `copy()` is memmove-based and explicitly spec'd to support overlapping
   source/destination, so the in-place shift-right is safe regardless of copy order. This
   collapses "two full-file allocations" to "one, with `delta` bytes of harmless slack" —
   ~74% B/op reduction for RW2 write, on top of the EncodeInto presizing. **Any format
   that inserts a small fixed-size header/marker AFTER the main relocate step should be
   checked for this exact pattern.**

4. **`exif.AcceptRAWMagic(magic uint16)` (new internal-use `ParseOption`, `exif/exif.go`)**
   is the #286 read-side unlock: ORF ("IIRO"/"IIRS") and RW2 ("IIU\x00") differ from
   standard classic TIFF ONLY in `b[2:4]`; nothing else in the classic-TIFF `Parse` branch
   ever re-reads those two bytes after the initial magic dispatch. The switch was converted
   from `switch magic { case 0x002A: ...}` to a boolean-condition
   `switch { case magic==0x002A || (cfg.extraMagic!=0 && magic==cfg.extraMagic): ...}` —
   the explicit `!=0` guard is load-bearing: without it, a caller that never opts in (the
   default zero value) would silently accept a corrupt file whose magic happens to be
   `0x0000`. This eliminates BOTH: (a) `read.go`'s `patchRawEXIFForParse` full-file clone
   (replaced by `nonStandardRAWMagic` detecting the real magic value and passing it
   straight to `AcceptRAWMagic`, parsing `raw` directly — the clone used to be retained
   FOREVER via every OOL `IFDEntry.Value` alias into it, which was the actual "2× file"
   retention bug), and (b) `format/tiff/relocate_orf.go`/`relocate_rw2.go`'s in-place
   `base[2]=0x2A;base[3]=0x00` patch in their `e==nil` fallback branch (which in turn let
   `relocateTIFFAsORF`/`relocateTIFFAsRW2` drop their own defensive `workBytes` clone —
   see finding #1). Treated as "internal use" per CLAUDE.md's own carve-out ("internal
   packages... may be as complex as needed") rather than a Go `internal/` package, since
   `exif` already exposes several cross-package-only helpers (`EncodedSize`, `EncodeInto`,
   `ParseIFDAt`) for exactly this kind of format/tiff-internal consumption; "no public API
   change" in the task brief was read as "no change to `exif.Parse`'s own default,
   no-option behaviour / no top-level `gometadata` API change," not "zero new exported
   identifiers anywhere in the module."

5. **`cr3.Extract`'s moov-only read requires `Read`+`Seek`-based header walking, not a
   single `io.ReadAll`.** New `readTopLevelBox`/`cr3BoxHeaderAt` (split into two functions
   specifically to keep gocyclo ≤10 — the combined version hit 13) walk top-level ISOBMFF
   boxes via 8/16-byte header reads + `Seek` past each non-matching box's payload, capped
   at `maxCR3TopLevelBoxScans` (4096) as a defence-in-depth bound against a crafted file
   padded with many minimal boxes before `moov` (each would otherwise cost a real syscall
   round trip on an `os.File`-backed reader). File length is learned via
   `Seek(0,SeekEnd)+Seek(0,SeekStart)` (zero bytes read) BEFORE any parsing, preserving the
   exact `TestExtractFileTooLarge` contract (a `*bytes.Reader` of `capBytesOOM+1` zero
   bytes must still yield `ErrFileTooLarge`, checked before any box-tree logic runs). The
   returned `rawEXIF`/`rawXMP` are `bytes.Clone`d out of the (already moov-sized, not
   file-sized) scratch buffer — necessary because moov itself is not guaranteed small in
   every real file (embedded previews), so relying on "moov is already small" alone would
   not bound retention; the explicit clone is what the dedicated retention test asserts.
   `cr3.Inject` (needs the WHOLE file — every untouched byte is copied through verbatim in
   `injectIntoMoov`) is a trivial `io.ReadAll(io.LimitReader(...))`→`iobuf.ReadAll` swap,
   mapping `iobuf.ErrTooLarge`→the package's own `ErrFileTooLarge`.

6. **A "prototype" that only swaps `io.ReadAll`→`iobuf.ReadAll` on `Extract` does NOT
   satisfy a "stop reading the whole file" requirement** — `iobuf.ReadAll` still reads
   the ENTIRE remaining stream for a seekable reader (one exact-size allocation instead of
   geometric growth, but still whole-file). The profiling-r1 prototype's `cr3.go` diff did
   exactly this trivial swap for `Extract` (not the moov-only redesign), which is also why
   it broke `TestExtractFileTooLarge` (returned bare `iobuf.ErrTooLarge` instead of the
   package's wrapped `ErrFileTooLarge`) — a warning sign that the prototype was a partial,
   not equivalent, attempt. Always re-derive the read-shape (whole-file vs targeted) from
   the task's OWN stated AC (B/op ≤ 256 KiB), not from what the prototype happened to touch.

7. **Retention tests must build their own large, out-of-scope padding vector and assert a
   generously-separated bound, not a razor-thin exact figure** — mirrored
   `xmp/xmpbuf_retain01_test.go`'s `heapAlloc()` (double `runtime.GC()` +
   `runtime.ReadMemStats`) + "N retained objects, heap growth must stay far below ONE
   padding unit's size" pattern exactly for `format/raw/cr3/task286_retention_test.go`
   (`TestCR3ExtractRetentionBounded`): a synthetic CR3 with an 8 MiB `mdat` box, 10
   independently-retained `Extract` results. Measured 0 bytes heap growth after the fix vs
   75,571,424 bytes (≈10×8 MiB) before it when the same test file was run against a HEAD
   worktree — this before/after cross-check (copy the new test into a `git worktree` of
   HEAD, confirm it FAILS there) is the standard way in this codebase to prove a new
   regression test is load-bearing, not vacuous (see also
   `feedback_width_claims_need_primitive_test`).

8. **Discovered, in-scope, fidelity-IMPROVING 1-byte divergence on 3 adversarial fixtures**
   (`testdata/corpus/raw/exiv2/issue_839_poc{,_2,_3}.rw2`): these deliberately crafted
   files (named after a real exiv2 GitHub issue) declare an IFD entry (tag `0x0148`) whose
   OOL value offset points back to file offset 0 — i.e. the entry's 48-byte value ALIASES
   the file's own TIFF header, including the magic bytes at [2:4]. Under HEAD's
   patch-then-parse approach, that entry's parsed `Value` incorrectly contained the
   *synthetic* patched magic (`0x2A`) instead of the file's true on-disk byte (`0x55`);
   the #286 direct-parse approach preserves the real on-disk byte. Root-caused via a
   standalone diagnostic program parsing the SAME bytes both ways
   (`exif.Parse(patchedClone)` vs `exif.Parse(raw, AcceptRAWMagic(...))`) and diffing
   every `IFD0.Entries[i].Value` — this is the ONLY reliable way to isolate a 1-byte
   divergence buried inside a 2992-byte re-encoded output; a plain hex diff of the two
   OUTPUTS only tells you WHERE the difference is (byte offset 248, via `cmp -l`), not WHY.
   Confirmed via a full 571-file corpus golden-hash comparison (`gm.Read`+`SetCaption`+
   `SetCopyright`+`gm.Write`, current vs HEAD worktree) that this is the ONLY divergence in
   the entire TIFF/DNG/RAW corpus. Reported transparently rather than "fixed" (there is
   nothing to fix — the new behaviour is strictly more spec-compliant) or silently ignored.

**Retained-slice-ownership / offset-math changes for the security audit:**
- `write.go`'s six `writeTIFF*` functions now alias `m.rawEXIF` directly as
  `originalBytes` (no clone). Safe ONLY because (a) `RawEXIF()` already clones for every
  external caller, and (b) no relocate function writes into its `base` parameter (verified
  exhaustively, see finding #1) — a FUTURE relocate change that adds ANY `base[i]=`/
  `order.PutUint32(base[...` write would silently corrupt `m.rawEXIF` for the lifetime of
  the `*Metadata`, with no compiler or test signal short of the two new
  `TestRelocateTIFFFromParsedORF_DoesNotMutateBase`/`_RW2_DoesNotMutateBase` regression
  gates (which only cover ORF/RW2, the two formats that ever needed such a write) — a
  reviewer adding a new in-place `base` mutation to `relocate.go`/`relocate_nef.go`/
  `relocate_arw.go` should add an equivalent non-mutation test.
- `relocate.go`'s final `exif.EncodeInto` call reserves `cr2MarkerLen` (8) extra bytes of
  capacity UNCONDITIONALLY for every TIFF/DNG/CR2 write (not just CR2), and
  `insertCR2MarkerAndShiftOffsets`/`insertRW2GUIDAndShiftOffsets` reuse `finalTIFF`'s own
  backing array via `finalTIFF[:len(finalTIFF)+delta]` when capacity allows. This is an
  in-place mutation of a slice `relocateTIFFFromParsed`/`relocateTIFFFromParsedRW2` just
  built (never caller-owned data), so it is safe by construction — but any code that
  captured a reference to `finalTIFF` BEFORE this reslice-and-shift (there is none today)
  would see its trailing bytes overwritten.
- `cr3.Extract`'s `readTopLevelBox`/`cr3BoxHeaderAt` trust the caller-supplied `fileLen`
  (from `seekFileLen`, itself just `Seek(SeekEnd)`+`Seek(SeekStart)`) for all bounds
  checks; an `io.ReadSeeker` whose `Seek(SeekEnd)` lies about the true remaining length
  would not be caught by any check in these functions (mirrors the pre-existing trust
  boundary already documented for `iobuf.ReadAll`'s own Seek-based sizing).
- `exif.AcceptRAWMagic`'s `cfg.extraMagic` is caller-trusted: `format/tiff` passes
  `binary.LittleEndian.Uint16(base[2:4])` (a value already independently validated by
  `isORFMagic`/`isRW2Magic` before the call), and `read.go` passes a value gated by
  `nonStandardRAWMagic`'s own byte-pattern check — no caller passes an unvalidated
  attacker-controlled magic value blind.

Files: `exif/exif.go` (+`AcceptRAWMagic`, +`parseConfig.extraMagic`, switch condition
change), `exif/task286_acceptrawmagic_test.go` (new), `read.go` (`patchRawEXIFForParse`→
`nonStandardRAWMagic`, `parseEXIF`), `write.go` (six `writeTIFF*` clone removals,
`writeTIFFORF`/`writeTIFFRW2` simplified — no more separate magic re-read from `r`),
`format/tiff/relocate.go` (cr2MarkerLen capacity reserve), `format/tiff/tiff.go`
(`insertCR2MarkerAndShiftOffsets` in-place reuse), `format/tiff/relocate_nef.go`/
`relocate_arw.go`/`relocate_orf.go`/`relocate_rw2.go` (EncodedSize+EncodeInto presizing,
`arwRelocatedLen` new helper, RW2 GUID in-place reuse, ORF/RW2 magic-patch removal,
invariant-length checks), `format/tiff/task285_286_test.go` (new),
`format/raw/cr3/cr3.go` (`Extract` redesign, `Inject` iobuf.ReadAll swap, new
`seekFileLen`/`cr3BoxHeaderAt`/`readTopLevelBox`), `format/raw/cr3/task286_retention_test.go`
(new), `xmp/rdf.go` (`nameTerminatorLUT`), `xmp/task287_test.go` (new), `BENCHMARKS.md`
(new "Batch E" section).

See also [[project_sprint44_batchC_219_227_237]] (the `EncodedSize`/`relocatedLen`
pattern this batch extends to NEF/ARW/ORF/RW2), [[project_sprint44_batchD_228_234]]
(sibling "presize instead of double-build" and in-place-buffer-reuse patterns for
HEIF/PNG/WebP — same family of optimisation, different formats),
[[feedback_width_claims_need_primitive_test]] (the "confirm the test fails against the
old code" methodology reused here for the CR3 retention test and the ORF/RW2
non-mutation tests).
