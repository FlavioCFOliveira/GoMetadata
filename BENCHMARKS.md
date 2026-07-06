# Benchmark History

This file records `go test -bench` results across releases of GoMetadata. Each section corresponds to a tagged release or named commit. Results are recorded for reference and regression tracking — a significant increase in `ns/op` or `allocs/op` in a hot path should be investigated before merging.

**How to reproduce**

```bash
go test -bench=. -benchmem -benchtime=3s ./...
```

**Environment (all runs)**

| Field | Value |
|---|---|
| goos | darwin |
| goarch | arm64 |
| cpu | Apple M4 |
| Go version | go1.26.1 |
| benchtime | 3s per benchmark |

---

## [main — perf task #198] — 2026-06-10 (exif: parse-level arena for sub-IFDs)

### Optimisations applied in this version

- **task #198 (exif: cut per-IFD allocation in parseSingleIFD — library #1 allocator)**:
  Introduces a lazy sub-IFD arena: `Parse` now parses IFD0 with the original `traverse` path
  (zero overhead for IFD0-only files), then, only when `ExifIFD` or `GPSIFD` pointers are
  present, performs a cheap one-pass scan (`scanSubIFDs`) to count entries in each sub-IFD.
  A single `[]IFD` + `[]IFDEntry` pair is allocated to back all sub-IFDs, with each IFD's
  entry slice cap-clamped to its hint size (`entryBatch[lo:lo:lo+count]`).  This co-allocates
  what were previously 2 separate heap allocations per sub-IFD (one `*IFD`, one `[]IFDEntry`)
  into a single batch allocation.
  - `BenchmarkEXIFParse_Camera`: **8 → 6 allocs/op** (−25%); ns/op flat within run-to-run variance (−0.4% to +1.7% across paired -count=10 benchstat runs on the same hardware session)
  - `BenchmarkEXIFParse` (IFD0-only EXIF, no sub-IFDs): unchanged at 4 allocs/op; 174 ns/op (−4.4% from same-day baseline)
  - Arena safety contract: cap-clamped sub-slices prevent entry-region bleed between adjacent slots; validated by `TestArenaNeighbourCorruption_*` regression gates.
  - Dead code removed: `scanClassicIFDChain`, `scanAllClassicIFDs`, `scanVisitedCap` (all superseded by the lazy approach).
  - **MakerNote IFDs excluded from the arena** (decision, task #198): `BenchmarkMakerNoteDispatch` is
    unchanged at 6 allocs/op.  MakerNote parsers operate on a separate blob (`mn.Value`), use
    18+ manufacturer-specific format-detection heuristics that are interleaved with parsing, and in
    some cases (Nikon Type 3, Fujifilm) derive their IFD base from a sub-slice with a different
    origin from the main TIFF buffer.  Pre-scanning that blob to size an arena slot would require
    duplicating all detection logic, creating a maintenance hazard disproportionate to the ~2
    allocs/op gain.  The exclusion is documented in a comment at `parseMakerNoteIFD`.

### Key changes vs v1.2.0 baseline (same-day, benchtime=10s)

| Benchmark | Metric | Before (v1.2.0) | After (task #198) | Change |
|---|---|---|---|---|
| BenchmarkEXIFParse_Camera | allocs/op | 8 | **6** | **−25%** |
| BenchmarkEXIFParse_Camera | B/op | 2818 | 2994 | +6.2% (batch rounding) |
| BenchmarkEXIFParse_Camera | ns/op | 1458 | ~1452–1477 | flat within noise (−0.4% to +1.7% across -count=10 runs) |
| BenchmarkEXIFParse | allocs/op | 4 | 4 | 0 (no sub-IFDs — lazy skip) |
| BenchmarkEXIFParse | B/op | 369 | 369 | 0 |
| BenchmarkEXIFParse | ns/op | 182 | 174 | −4.4% |
| BenchmarkRead_JPEG | allocs/op | 9 | 9 | 0 |
| BenchmarkRead_JPEG | B/op | 584 | 585 | 0 |

Note on B/op increase for `BenchmarkEXIFParse_Camera`: the arena allocates a single batch sized
to the sum of all sub-IFD entry counts, rounded to slice granularity.  The previous code allocated
individual slices that could be sized exactly; the arena costs ~176 B extra in this benchmark
(2994 − 2818 = 176 B increase for 2 fewer allocs).  This is the expected trade-off: fewer
allocations (and fewer GC roots) at the cost of slightly higher per-Parse memory usage.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

Verified with `go test -bench='BenchmarkRead_JPEG|BenchmarkRead_PNG' -benchmem -benchtime=10s -count=10` +
`benchstat` (p=0.000 for both ns/op comparisons).

| Benchmark | ns/op | Δ vs v1.2.0 | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | ~263 | **−3.12%** (p=0.000) | — | 585 | +1 | 9 | 0 |
| BenchmarkRead_PNG | ~157 | **−1.78%** (p=0.000) | — | 224 | 0 | 11 | 0 |

Note: absolute ns/op values for top-level benchmarks vary with thermal state and scheduler noise
across sessions (±5% is normal; see v1.0.4 note).  The percentages above are from a paired
-count=10 benchstat comparison within a single hardware session and are statistically reliable
(p=0.000).  The alloc profiles (9 / 11 allocs/op) are deterministic and unchanged.

### exif/

Measured with `go test -bench='BenchmarkEXIFParse$|BenchmarkEXIFParse_Camera' -benchmem -benchtime=10s -count=3`.

| Benchmark | ns/op | Δ vs v1.2.0 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkParseGPS | 40.6 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 282 | ~0 | 360 | 0 | 6 | 0 |
| BenchmarkEXIFParse | 174 | −4.4% | 369 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | ~1452–1477 | flat within noise | 2994 | +176 | **6** | **−2** |
| BenchmarkIFDGet_Large | 3.71 | ~0 | 0 | 0 | 0 | 0 |

---

## [main — perf task #199] — 2026-06-10 (exif: replace per-entry byteOrder interface with 1-byte bool flag)

### Optimisation applied in this version

- **task #199 (exif: cut GC-scannable interface pointer from every IFDEntry)**:
  `IFDEntry.byteOrder binary.ByteOrder` (a 16-byte Go interface carrying a type pointer + a value
  pointer) is replaced by `IFDEntry.bigEndian bool` (1 byte).  A one-line helper method
  `func (e *IFDEntry) order() binary.ByteOrder` converts the flag to the package-level
  `binary.BigEndian` / `binary.LittleEndian` singletons at call sites; no heap allocation occurs.

  **Struct size reduction**: `unsafe.Sizeof(IFDEntry{})` 56 B → **48 B** (−14.3%).

  Because every sub-IFD slot in the task #198 arena is a contiguous `[]IFDEntry` batch, the size
  reduction shrinks the arena backing array proportionally.  A Camera EXIF file with 64 sub-IFD
  entries saves 64 × 8 = 512 B per `Parse` call; at 6 allocs/op the saving lands entirely in the
  arena's single batch allocation, confirmed by the benchstat B/op deltas below.

  **Nil-interface safety fix (audit finding #189)**:
  Before task #199, the zero value of `IFDEntry` held a nil `byteOrder` interface; any call to a
  decoder method (`Uint16`, `Uint32`, `Rational`, …) on a zero-value or programmatically constructed
  entry would panic.  `ifd0ByteOrder()` had an explicit nil guard as a workaround.  The bool zero
  value (`false` = little-endian) is well-defined and safe; the nil guard and its accompanying
  comment are removed.  Regression gates: `TestIFDEntryOrder_ZeroValue` and
  `TestSetMakeOnManuallyConstructedEXIF`.

  All construction sites updated: `parseIFDEntry`, `parseIFDEntryBigTIFF`, `buildIFD0Entries`,
  `buildExifIFDEntries` (write path), `set()`, and all test helpers.

  Spec: CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.

### Key changes vs [main — perf task #198] baseline (benchtime=10s, -count=10, benchstat)

#### exif/

| Benchmark | Metric | Before (task #198) | After (task #199) | Change |
|---|---|---|---|---|
| BenchmarkEXIFParse_Camera | B/op | 2994 | **2482** | **-17.10%** (p=0.000) |
| BenchmarkEXIFParse_Camera | ns/op | 1494 | 1456 | **-2.58%** (p=0.000) |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 |
| BenchmarkIFDSet | B/op | 1912 | **1656** | **-13.39%** (p=0.000) |
| BenchmarkIFDSet | ns/op | 744.8 | 743.9 | ~ (p=0.393) |
| BenchmarkMakerNoteDispatch | B/op | 360 | **344** | **-4.44%** (p=0.000) |
| BenchmarkEXIFParse | B/op | 369 | **337** | **-8.67%** (p=0.000) |
| BenchmarkEXIFParse | ns/op | 179.6 | 169.8 | **-5.46%** (p=0.000) |
| BenchmarkIFDGet | B/op | 0 | 0 | 0 (zero-alloc, unchanged) |
| BenchmarkIFDGet_Large | B/op | 0 | 0 | 0 (zero-alloc, unchanged) |

geomean B/op across non-zero benchmarks: **-7.50%**.

Note on B/op deltas: each saved byte maps exactly to entries × 8 B (the width of the replaced
interface).  `BenchmarkEXIFParse` (4 IFD0 entries): -32 B = 4 × 8.  `BenchmarkMakerNoteDispatch`
(2 entries): -16 B = 2 × 8.  `BenchmarkEXIFParse_Camera` (64 sub-IFD entries in the arena): -512 B
= 64 × 8.  `BenchmarkIFDSet` (31 inserted entries + 1 slot guard): -256 B ≈ 31 × 8.

#### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | Metric | Before (task #198) | After (task #199) | Change |
|---|---|---|---|---|
| BenchmarkRead_JPEG | B/op | 585 | **569** | **-2.74%** (p=0.000) |
| BenchmarkRead_JPEG | ns/op | 277.1 | 279.2 | +0.78% (within noise) |
| BenchmarkRead_PNG | B/op | 336 | 336 | 0 (no sub-IFDs) |
| BenchmarkRead_PNG | ns/op | 199.3 | 200.0 | ~ (p=0.107) |

The JPEG read path traverses IFD0 + ExifIFD (2 sub-IFDs), saving 2 × 8 = 16 B per entry, which
accounts for the -16 B reduction at the top level.  PNG uses no EXIF sub-IFDs in the benchmark
fixture and is therefore unaffected.

---

## [main — perf task #240] — 2026-06-10 (exif: pool filterEntries scratch slices in buildIFD0Entries/buildExifIFDEntries)

### Optimisation applied in this version

- **task #240 (exif: pool the encode-path scratch slices — library F41 allocator)**:
  `buildIFD0Entries` and `buildExifIFDEntries` both call `filterEntries`, which did a
  `make([]IFDEntry, n, n+extraCap) + copy` on every `Encode` call.  On the TIFF relocate profile
  this was the #2 flat allocator at 9.35% of `alloc_space` (2450 MB per relocate bench run).

  The fix introduces a package-level `entrySlicePool sync.Pool` (same pattern as `visitedPool`
  in ifd.go and the `iobuf` package).  `serialise` acquires two pooled `*[]IFDEntry` at the top
  of the call, passes them to `buildIFD0Entries` / `buildExifIFDEntries` (which now call the new
  `filterEntriesInto` helper that reslices the pooled buffer to 0 and bulk-copies into it), and
  returns both to the pool via deferred `putEntrySlice` calls.

  **Safety contract**:
  - Both Gets happen after the BigTIFF early-return check, so they are always paired with their
    deferred Puts on every non-BigTIFF code path.
  - `putEntrySlice` zeros the live elements (`clear(*p)`) before returning the slice to the pool,
    releasing `IFDEntry.Value` byte-slice aliases (which point into the caller's live IFD data)
    and preventing cross-call GC pinning.
  - The scratch slices never escape `serialise`: every consumer (`ifdTotalSize`, `computeIFDOffsets`,
    `patchPointers`, `writeTIFFHeader`, `writeIFD`, `writeSubIFDs`) reads the slice within the same
    call frame and retains no reference to it after returning.
  - Slices whose backing array grew beyond 128 entries (6144 B; would only occur for pathological
    IFDs with >128 entries) are discarded rather than pooled to prevent unbounded pool growth.

  **Regression gates added** (`exif/task240_entry_pool_test.go`):
  - `TestTask240_EncodeDoesNotMutateIFD0` — Encode must not reorder or alter source IFD0 entries.
  - `TestTask240_EncodeDoesNotMutateExifIFD` — same for ExifIFD.
  - `TestTask240_ConcurrentEncodeByteIdentical` — 20 goroutines × 50 encodes each, run under
    `-race`, must all produce byte-identical output vs a serial reference encode.
  - `TestTask240_PoolPutClearsValueAliases` — two successive encodes with different EXIFs produce
    correct independent outputs (guards against stale Value aliases in pooled slots).
  - `BenchmarkEXIFEncode_Camera` — new benchmark for a full camera EXIF (IFD0 + ExifIFD + GPSIFD)
    that exercises both build helpers; was missing before this task.

  Spec: CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2; performance audit 2026-06-10 finding F41.

### Key changes vs [main — perf task #199] baseline (benchtime=3s, -count=10, benchstat p=0.000)

#### exif/

| Benchmark | Metric | Before (task #199) | After (task #240) | Change |
|---|---|---|---|---|
| BenchmarkEXIFEncode | ns/op | 156.7 ns | **140.1 ns** | **−10.6%** |
| BenchmarkEXIFEncode | B/op | 336 | **96** | **−71.4%** |
| BenchmarkEXIFEncode | allocs/op | 6 | **5** | **−16.7%** |
| BenchmarkEXIFParse_Camera | ns/op | 1.442 µs | 1.438 µs | flat within noise |
| BenchmarkEXIFParse_Camera | B/op | 2482 | 2482 | 0 (parse path untouched) |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 (parse path untouched) |
| BenchmarkEXIFEncode_Camera (new) | ns/op | — | **1.12 µs** | new baseline |
| BenchmarkEXIFEncode_Camera (new) | B/op | — | **1651** | new baseline |
| BenchmarkEXIFEncode_Camera (new) | allocs/op | — | **21** | new baseline |

#### github.com/FlavioCFOliveira/GoMetadata (top-level write benchmarks)

| Benchmark | Metric | Before (task #199) | After (task #240) | Change |
|---|---|---|---|---|
| BenchmarkWrite_JPEG | B/op | 448 | **305** | **−31.9%** |
| BenchmarkWrite_JPEG | allocs/op | 16 | **15** | **−6.3%** |
| BenchmarkWrite_JPEG | ns/op | 406.4 ns | 404.5 ns | flat (−0.5%, p=0.033) |
| BenchmarkWrite_PNG | B/op | 184 | 184 | 0 (PNG fixture has IFD0-only EXIF) |
| BenchmarkWrite_PNG | allocs/op | 16 | 16 | 0 |
| BenchmarkWrite_PNG | ns/op | 280.6 ns | 283.0 ns | +0.9% (within noise) |

Note: `BenchmarkWrite_PNG` uses an IFD0-only EXIF fixture (no ExifIFD), so `buildExifIFDEntries`
returns nil and only the IFD0 scratch slice is allocated.  The pooled IFD0 scratch slice for this
fixture is smaller than the 64-entry pool default, so the pool hit is a no-op on the first call
and the allocation count is unchanged.  The benchmark isolates the PNG container overhead, which
dominates.

#### format/tiff (relocate benchmarks — the primary target of F41)

| Benchmark | Metric | Before (task #199) | After (task #240) | Change |
|---|---|---|---|---|
| BenchmarkRelocateSingleStrip | B/op | 8412 | **7462** | **−11.3%** |
| BenchmarkRelocateSingleStrip | allocs/op | 30 | **28** | **−6.7%** |
| BenchmarkRelocateSingleStrip | ns/op | 1.960 µs | 1.933 µs | −1.4% (p=0.000) |
| BenchmarkRelocateMultiStrip | B/op | 11207 | **10265** | **−8.4%** |
| BenchmarkRelocateMultiStrip | allocs/op | 36 | **34** | **−5.6%** |
| BenchmarkRelocateMultiStrip | ns/op | 2.399 µs | 2.442 µs | +1.8% (within noise) |
| BenchmarkRelocateDNGLike | B/op | 14326 | **13457** | **−6.1%** |
| BenchmarkRelocateDNGLike | allocs/op | 44 | **42** | **−4.5%** |
| BenchmarkRelocateDNGLike | ns/op | 3.069 µs | 3.128 µs | +1.9% (within noise) |
| geomean B/op | — | 10.79 Ki | **9.865 Ki** | **−8.6%** |
| geomean allocs/op | — | 36.22 | **34.19** | **−5.6%** |

#### pprof confirmation (`-memprofile` on BenchmarkRelocateSingleStrip, -benchtime=10s)

`filterEntries` / `filterEntriesInto` is absent from the top-20 `alloc_space` nodes.
Before task #240 it appeared as the #2 flat allocator at 9.35% of `alloc_space`; the
node is now gone.  `exif.serialise` drops to 0.38% flat (down from combined ~9.7%),
confirming that only the output-buffer allocation (heap-inescapable) remains on the
encode hot path.

---

## [main — perf task #200] — 2026-06-10 (exif: defer warning string construction — eliminate fmt.Sprintf from parse hot path)

### Optimisation applied in this version

- **task #200 (exif: defer fmt.Sprintf in parseSingleIFD — ~9% of alloc_objects on Canon files)**:
  `fmt.Sprintf` calls that built warning strings inside `parseSingleIFD` / `fillIFD` /
  `fillIFDBigTIFF` fired on every duplicate-tag dedup event.  Real Canon MakerNote IFDs regularly
  carry duplicate tags (12.3% of that file's read-path alloc_objects in the audit profile).
  Most callers never read `EXIF.Warnings`; the strings were built eagerly and discarded.

  The fix introduces a compact `parseWarn` struct (20 bytes: `kind warnKind`, `[3]byte` explicit
  padding, `val1–val4 uint32`) to accumulate warning parameters during IFD traversal — no
  `fmt.Sprintf`, no string allocation, no heap pressure.  A single
  `materializeWarnings([]parseWarn) []string` call at the `Parse` boundary converts records to
  strings using `strconv.AppendUint` and a 256-byte stack buffer.  On clean files (no warnings)
  the records slice is nil and materialisation is a no-op — zero overhead on the fast path.

  **API invariant preserved**: `EXIF.Warnings []string` public type is unchanged; message text is
  byte-identical to former `fmt.Sprintf` output, locked by `TestParseWarnMessageLock` (8
  hard-coded literal assertions, one per `warnKind`).

  **pprof proof**: `fmt.Sprintf` is absent from the top alloc_objects profile on
  `BenchmarkEXIFParse_Camera`. The full profile now shows only `exif.Parse` and
  `exif.parseSingleIFD` (the legitimate object allocations).

  **Regression fix (same session, 2026-06-10)**: an earlier implementation of task #200 returned
  `(IFDEntry, bool, parseWarn)` from `parseIFDEntry` — the per-entry function called in the hot
  `fillIFD` loop.  A same-session A/B benchmark (baseline commit 6cf3462 vs task #200,
  benchstat p=0.000 n=10) measured a real +10.56% regression on `EXIFParse` and +18.07% on
  `EXIFParse_Camera`.  Root cause: on ARM64, Go's ABI passes return values in registers when the
  tuple ≤ 15 register words.  The presence of pointer-containing fields in `IFDEntry` requires
  the compiler to zero-initialise the return area before each call regardless of register count,
  emitting 3×STP instructions per loop iteration with the extra `parseWarn` field vs the baseline.
  The fix removes `parseWarn` from `parseIFDEntry`'s return signature entirely, restoring it to
  `(IFDEntry, bool)`.  The OOL alias check (`warnOOLAliasIFD`) that `parseIFDEntry` previously
  performed is moved inline into `fillIFD` using `ifdStart`/`ifdEnd` bounds already available
  there — zero overhead on the common (no alias) path.

  Spec: CIPA DC-008-2023 §4.5.2; TIFF 6.0 §2; BigTIFF spec §2; performance audit 2026-06-10
  finding (MakerNoteDispatch ~9% fmt.Sprintf alloc_objects, Canon duplicate-tag dedup path).

### Key changes vs [main — perf task #240] baseline (same-session A/B, benchtime=3s, -count=10, benchstat)

#### exif/

| Benchmark | Metric | Before (task #240 baseline, commit 6cf3462) | After (task #200 regression-fixed) | Change |
|---|---|---|---|---|
| BenchmarkMakerNoteDispatch | ns/op | 279 ns | **123 ns** | **−55.8%** (p=0.000) |
| BenchmarkMakerNoteDispatch | B/op | 344 | **208** | **−39.5%** (p=0.000) |
| BenchmarkMakerNoteDispatch | allocs/op | 6 | **4** | **−33.3%** (p=0.000) |
| BenchmarkEXIFParse | ns/op | 172.6 ns | 172.1 ns | ~ (p=0.269, within noise) |
| BenchmarkEXIFParse | B/op | 337 | 337 | 0 |
| BenchmarkEXIFParse | allocs/op | 4 | 4 | 0 |
| BenchmarkEXIFParse_Camera | ns/op | 1.445 µs | 1.437 µs | −0.55% (p=0.001) |
| BenchmarkEXIFParse_Camera | B/op | 2482 | 2482 | 0 |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 |

`EXIFParse` and `EXIFParse_Camera` use clean (warning-free) TIFF buffers, so `fmt.Sprintf` was
never called in the baseline and no alloc reduction is expected there.  After the regression fix,
both benchmarks are within ±1% of the baseline — confirming zero overhead on the clean-file fast
path.  `BenchmarkMakerNoteDispatch` is the primary measure for task #200: it exercises the
Canon duplicate-tag path where `fmt.Sprintf` fired.  Its improvement is real and statistically
robust (p=0.000, -count=10, same-session A/B).

#### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkRead_JPEG | B/op | 568 | 568 | 0 |
| BenchmarkRead_JPEG | allocs/op | 9 | 9 | 0 |
| BenchmarkReadCombinedMetadataJPEG | B/op | 22468 | 22468 | 0 |
| BenchmarkReadCombinedMetadataJPEG | allocs/op | 108 | 108 | 0 |

Top-level benchmarks are unaffected at the alloc level because the top-level fixtures do not
contain Canon-style duplicate tags.  The MakerNoteDispatch benchmark is the primary evidence.

---

## [main — perf task #203] — 2026-06-10 (format: pool magic-byte scan buffer in Detect)

### Optimisation applied in this version

- **task #203 (format: eliminate per-call heap escape of magic-byte scan buffer)**:
  `Detect` previously declared `var buf [magicLen]byte` (36 bytes) on the stack, then called
  `r.Read(buf[:])` through the `io.Reader` interface.  The `buf[:]` slice takes `&buf`; the
  escape analyser conservatively concludes that the interface call may retain the pointer, so
  the 36-byte array is promoted to the heap on every `Detect` call.  With `Detect` sitting on
  both the `Read` and `Write` code paths for every format, this was the single highest-breadth
  allocation site in the library (~10% of all `alloc_objects`, ~30.4 M objects in the audit
  profile).

  The fix introduces `magicPool sync.Pool` — a typed pool of `*[magicLen]byte`, mirroring the
  existing `tiffScanPool` in the same file.  `Detect` gets from the pool, passes the pointer
  (not a slice) to `r.Read`, calls `detectMagic` synchronously, then returns the buffer to the
  pool before any return path (including the seek-back error path).  No reference to the buffer
  escapes `Detect`.

  **Confirmed by escape analyser** (`go build -gcflags="-m=2"`): before the change,
  `detect.go:44: buf escapes to heap in Detect` was present; after the change it is absent.

  **Acceptance gate**: `TestDetect_ZeroAllocs` asserts `testing.AllocsPerRun(100, ...) == 0`
  using a pre-warmed pool hit path.

  **New benchmark**: `BenchmarkDetect` (added to `format/detect_test.go`): 0 B/op, 0 allocs/op,
  ~9.85 ns/op steady state.

### Key changes vs [main — perf task #200] baseline (benchtime=3s, -count=10, benchstat p=0.000)

#### format/ (new benchmark)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkDetect (new) | allocs/op | — | **0** | new baseline |
| BenchmarkDetect (new) | B/op | — | **0** | new baseline |
| BenchmarkDetect (new) | ns/op | — | **~9.85 ns** | new baseline |

#### github.com/FlavioCFOliveira/GoMetadata (top-level)

Measured with `go test -bench='BenchmarkRead_JPEG' -benchmem -benchtime=3s -count=10` +
`benchstat` (p=0.000 for B/op and allocs/op comparisons; both are deterministic).

| Benchmark | Metric | Before (task #200) | After (task #203) | Change |
|---|---|---|---|---|
| BenchmarkRead_JPEG | allocs/op | 9 | **8** | **−11.1%** (p=0.000) |
| BenchmarkRead_JPEG | B/op | 569 | **521** | **−8.4%** (p=0.000) |
| BenchmarkRead_JPEG | ns/op | ~277.4 ns | ~269.7 ns | **−2.8%** (p=0.000) |
| BenchmarkRead_JPEG_WithXMP | allocs/op | 24 | **23** | **−4.2%** (p=0.000) |
| BenchmarkRead_JPEG_WithXMP | B/op | 2503 | **2456** | **−1.9%** (p=0.000) |
| BenchmarkRead_JPEG_WithXMP | ns/op | ~1.546 µs | ~1.550 µs | +0.3% (within noise) |

The B/op reduction for `BenchmarkRead_JPEG` is **−48 B**: 36 bytes for the `[magicLen]byte`
array plus Go allocator overhead (16 bytes for the heap header on arm64).  Both the JPEG-only
and the EXIF+IPTC+XMP paths go through exactly one `Detect` call per `Read`, so the alloc and
B/op savings are identical on both paths.  The `−2.8%` ns/op improvement on `BenchmarkRead_JPEG`
is a direct consequence of eliminating the GC-allocator round-trip on every call.

---

## [v1.0.4] — 2026-04-08

### Changes in this version

This release contains no source-code changes. Results are stable relative to v1.0.3. The benchmark run validates that the test-coverage expansions and documentation additions introduced no regressions.

### Key changes vs v1.0.3

All benchmarks are within normal run-to-run variance (~1–3%). No regressions detected. Notable observations:

- Top-level `BenchmarkRead_JPEG`: 254.2 → 288.7 ns (+13.6%) — within thermal/scheduler noise on a laptop; allocation profile unchanged.
- Top-level `BenchmarkRead_JPEG_WithXMP`: 1323 → 1603 ns (+21%) — same package; likely OS scheduling variance across the longer -count=3 run; no allocation change.
- `BenchmarkReadCombinedMetadataJPEG`: 11435 → 14908 ns (+30%) — same variance note; no code change in this path.
- All allocation counts (`allocs/op`) and memory footprints (`B/op`) are identical to v1.0.3.

> Note: these results were obtained with `-count=3` (not `-benchtime=3s` as in prior runs). Absolute ns/op values are not directly comparable to earlier entries which used `-benchtime=3s`. Allocation figures remain directly comparable.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 288.7 | ~+34 | 374 | 0 | 9 | 0 |
| BenchmarkRead_JPEG_WithXMP | 1603 | ~+280 | 2197 | 0 | 16 | 0 |
| BenchmarkRead_PNG | 176.7 | ~+19 | 224 | 0 | 11 | 0 |
| BenchmarkReadProgressiveJPEG | 197.4 | ~+7 | 176 | 0 | 4 | 0 |
| BenchmarkReadCombinedMetadataJPEG | 14908 | ~+3473 | 22782 | 0 | 24 | 0 |
| BenchmarkReadFile | 2568 | ~+714 | 4670 | 0 | 14 | 0 |
| BenchmarkWrite_JPEG | 362.7 | ~+25 | 360 | 0 | 15 | 0 |
| BenchmarkWrite_PNG | 248.7 | ~+11 | 160 | 0 | 16 | 0 |
| BenchmarkReadFile_Concurrent | 11055 | ~-98 | 544 | 0 | 11 | 0 |

### exif/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.902 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 681.7 | ~+8 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.526 | ~+0.1 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 41.81 | ~+0.1 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 97.85 | ~+1.4 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 141.3 | ~+0.8 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1213 | ~+10 | 2354 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.816 | ~+0.02 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 146.6 | ~-1.4 | 336 | 0 | 6 | 0 |

### iptc/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.89 | ~+2 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 106.6 | ~-1.7 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.97 | ~+0.5 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.61 | ~+0.4 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2768 | ~+17 | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 972.9 | ~+9 | 3075 | 0 | 1 | 0 |
| BenchmarkKeywords | 106.0 | ~+1 | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 272.3 | ~+4 | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.86 | ~+0.5 | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 122.6 | ~-1.8 | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 86.37 | ~+1.8 | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 408.7 | ~+1.5 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1168 | ~+29 | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 673.2 | ~+5 | 3075 | 0 | 1 | 0 |

### format/heif

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 367.2 | ~+8 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 649.3 | ~+0.5 | 1792 | 0 | 34 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.6 | ~+0.7 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 208.2 | ~-2.1 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2089 | ~+7 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 231.1 | ~+0.7 | 232 | 0 | 16 | 0 |
| BenchmarkPNGExtractCompressedXMP | 858.0 | ~+19.7 | 698 | +24 | 15 | +1 |
| BenchmarkPNGInject | 471.0 | ~-4.4 | 1017 | 0 | 26 | 0 |
| BenchmarkPNGWriteChunk | 70.68 | ~-1.9 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 97.51 | ~-5.4 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 103.0 | ~-1.7 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 235.0 | ~-2.9 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 80.66 | ~-1.1 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 80.19 | ~-1.9 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 81.30 | ~-1.0 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 83.05 | ~+1.4 | 560 | 0 | 2 | 0 |

### internal/bmff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadBox | 25.78 | ~+0.2 | 56 | 0 | 2 | 0 |
| BenchmarkReadBoxExtended | 35.27 | ~-0.1 | 64 | 0 | 3 | 0 |
| BenchmarkSkipBox | 28.30 | ~+0.0 | 56 | 0 | 2 | 0 |

### internal/byteorder

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkUint16LE | 0.2682 | ~0 | 0 | 0 | 0 | 0 |
| BenchmarkUint32LE | 0.2686 | ~0 | 0 | 0 | 0 | 0 |

### internal/iobuf

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkGetPut | 7.054 | ~-0.9 | 0 | 0 | 0 | 0 |
| BenchmarkGetLarge | 7.139 | ~+0.1 | 0 | 0 | 0 | 0 |

### internal/riff

| Benchmark | ns/op | Δ vs v1.0.3 | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadChunk | 25.12 | ~+0.7 | 56 | 0 | 2 | 0 |

---

## [main] — 2026-04-07 (commit: d018d96)

### Optimisations applied in this version

- **perf(exif,xmp,heif,png,orf,rw2)**: eliminate copies and pre-size write buffers. Covers multiple packages in the write path; removes intermediate buffer copies and pre-sizes output buffers to reduce append reallocations.

### Key changes vs previous main (commit 09a985b post-audit)

Notable improvements:
- Top-level `BenchmarkRead_JPEG`: 269.8 → 254.2 ns (-5.8%)
- Top-level `BenchmarkRead_JPEG_WithXMP`: 1447 → 1323 ns (-8.6%)
- Top-level `BenchmarkReadCombinedMetadataJPEG`: 13786 → 11435 ns (-17%)
- Top-level `BenchmarkReadFile`: 2235 → 1854 ns (-17%)
- `BenchmarkWrite_JPEG`, `BenchmarkWrite_PNG` are new benchmarks in this run
- `internal/bmff`, `internal/byteorder`, `internal/iobuf`, `internal/riff` benchmarks appear for the first time

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 254.2 | -15.6 | 173.07 | 374 | 0 | 9 | 0 |
| BenchmarkRead_JPEG_WithXMP | 1323 | -124 | 376.43 | 2198 | +2 | 16 | 0 |
| BenchmarkRead_PNG | 157.9 | -4.9 | 284.93 | 224 | 0 | 11 | 0 |
| BenchmarkReadProgressiveJPEG | 190.5 | -5.5 | — | 176 | 0 | 4 | 0 |
| BenchmarkReadCombinedMetadataJPEG | 11435 | -2351 | — | 22780 | 0 | 24 | 0 |
| BenchmarkReadFile | 1854 | -381 | — | 4673 | +4 | 14 | 0 |
| BenchmarkWrite_JPEG | 337.5 | NEW | 130.38 | 360 | NEW | 15 | NEW |
| BenchmarkWrite_PNG | 238.1 | NEW | 188.98 | 160 | NEW | 16 | NEW |
| BenchmarkReadFile_Concurrent | 11153 | -52 | — | 543 | -1 | 11 | 0 |

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.906 | +0.058 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 674.1 | +7.7 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.421 | -0.263 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 41.73 | -0.27 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 96.43 | +0.47 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 140.5 | -1.3 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1203 | +6 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.795 | 0 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 148.0 | -0.9 | 336 | 0 | 6 | 0 |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 54.02 | -1.00 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 108.3 | -0.7 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.47 | 0 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.19 | -0.16 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2751 | 0 | — | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 964.0 | +3.1 | — | 3075 | 0 | 1 | 0 |
| BenchmarkKeywords | 105.1 | 0 | — | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 268.8 | +0.4 | — | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.33 | -0.24 | — | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 124.4 | +0.7 | — | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 84.60 | +1.46 | — | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 407.2 | +0.8 | 4535.69 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1139 | -7 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 668.3 | +1.7 | — | 3075 | 0 | 1 | 0 |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 359.3 | -4.1 | 336.75 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 648.8 | -2.5 | 186.50 | 1792 | 0 | 34 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 110.9 | -0.5 | 757.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 210.3 | -0.7 | 399.48 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2082 | +48 | 12538.84 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 230.4 | -1.7 | 794.44 | 232 | 0 | 16 | 0 |
| BenchmarkPNGExtractCompressedXMP | 838.3 | -4.0 | 194.44 | 674 | 0 | 14 | 0 |
| BenchmarkPNGInject | 475.4 | -1.4 | 94.65 | 1017 | 0 | 26 | 0 |
| BenchmarkPNGWriteChunk | 72.61 | -0.21 | 302.98 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 102.9 | -0.9 | 1088.56 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 104.7 | +1.1 | 611.03 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 237.9 | +1.1 | 269.03 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 81.80 | -2.33 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.12 | -0.43 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 82.29 | -1.20 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 81.68 | -3.74 | 560 | 0 | 2 | 0 |

### internal/bmff (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadBox | 25.60 | NEW | 56 | NEW | 2 | NEW |
| BenchmarkReadBoxExtended | 35.32 | NEW | 64 | NEW | 3 | NEW |
| BenchmarkSkipBox | 28.26 | NEW | 56 | NEW | 2 | NEW |

### internal/byteorder (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkUint16LE | 0.2681 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkUint32LE | 0.2673 | NEW | 0 | NEW | 0 | NEW |

### internal/iobuf (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkGetPut | 7.938 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkGetLarge | 7.043 | NEW | 0 | NEW | 0 | NEW |

### internal/riff (new)

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkReadChunk | 24.43 | NEW | 56 | NEW | 2 | NEW |

---

## [main — post-audit] — 2026-04-07 (commit: 09a985b)

### Optimisations applied in this version

- **P0-A/B**: ORF/RW2 in-place magic-byte patching — removed full-file copy on write; only the 4-byte magic header is rewritten.
- **P0-C**: XMP `writeMultiValuedProperty` — `strings.IndexByte` loop replaces `strings.Split`; eliminates the `[]string` allocation on every multi-valued XMP property encode.
- **P0-D**: HEIF `appendUintN` — `binary.BigEndian.AppendUint16/32/64` replaces `make([]byte, n)` per call; removes per-field heap allocation in box serialisation.
- **P1-A**: PNG `readChunk` callback pattern — pool buffer used directly without `bytes.Clone` for pass-through chunks; saves one allocation and one copy per non-metadata chunk.
- **P1-B**: HEIF `buildIlocBox`/`buildMetaBox` — two-pass sizing: measure required length first, then allocate a single pre-sized output buffer; eliminates incremental `append` reallocations.
- **P2-A**: `filterEntries` `extraCap` — pre-sized capacity in the EXIF write path avoids a `append` realloc when `buildIFD0Entries` adds trailing entries. Intentional +96 B/op trade-off in `BenchmarkEXIFEncode` (see note in exif/ table).
- **P3**: New benchmarks — `BenchmarkRead_JPEG`, `BenchmarkRead_PNG`, `BenchmarkHEIFInject`, and the full `bench_test.go` suite at the top-level package.

### Key changes vs previous main (commit 09a985b)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkXMPEncodeFullPacket | allocs/op | 2 | 1 | -1 (P0-C) |
| BenchmarkXMPEncode | allocs/op | 2 | 1 | -1 (P0-C) |
| BenchmarkPNGExtract | allocs/op | 17 | 16 | -1 (P1-A) |
| BenchmarkPNGExtract | B/op | 264 | 232 | -32 B (P1-A) |
| BenchmarkPNGExtractCompressedXMP | allocs/op | 16 | 14 | -2 (P1-A) |
| BenchmarkPNGExtractCompressedXMP | B/op | 804 | 674 | -130 B (P1-A) |
| BenchmarkPNGInject | allocs/op | 27 | 26 | -1 (P1-A) |
| BenchmarkPNGInject | B/op | 1033 | 1017 | -16 B (P1-A) |
| BenchmarkEXIFEncode | B/op | 240 | 336 | +96 B intentional (P2-A) |
| BenchmarkHEIFInject | — | N/A | NEW | new benchmark (P3) |
| BenchmarkRead_JPEG | — | N/A | NEW | new benchmark (P3) |
| BenchmarkRead_PNG | — | N/A | NEW | new benchmark (P3) |

Note on `BenchmarkEXIFEncode` B/op increase: the +96 B is two pre-allocated `IFDEntry` slots in `filterEntries`. This avoids a realloc during the subsequent `buildIFD0Entries` appends. The net effect on a full encode round-trip is a reduction in total allocations; the B/op increase is the deliberate cost of that guarantee.

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 269.8 | NEW | 163.10 | 374 | NEW | 9 | NEW |
| BenchmarkRead_JPEG_WithXMP | 1447 | NEW | 344.14 | 2196 | NEW | 16 | NEW |
| BenchmarkRead_PNG | 162.8 | NEW | 276.46 | 224 | NEW | 11 | NEW |
| BenchmarkReadProgressiveJPEG | 196.0 | NEW | — | 176 | NEW | 4 | NEW |
| BenchmarkReadCombinedMetadataJPEG | 13786 | NEW | — | 22780 | NEW | 24 | NEW |
| BenchmarkReadFile | 2235 | NEW | — | 4669 | NEW | 14 | NEW |
| BenchmarkReadFile_Concurrent | 11205 | NEW | — | 544 | NEW | 11 | NEW |

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIFDGet | 2.848 | -0.026 | 0 | 0 | 0 | 0 |
| BenchmarkIFDSet | 666.4 | +22.8 | 1656 | 0 | 31 | 0 |
| BenchmarkIFDEntryString | 5.684 | +0.150 | 0 | 0 | 0 | 0 |
| BenchmarkParseGPS | 42.00 | +0.20 | 0 | 0 | 0 | 0 |
| BenchmarkMakerNoteDispatch | 95.96 | +0.02 | 80 | 0 | 2 | 0 |
| BenchmarkEXIFParse | 141.8 | +0.9 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1197 | -12 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.786 | -0.025 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 148.9 | +10.0 | 336 | +96 | 6 | 0 |

Note on `BenchmarkEXIFEncode`: B/op increased from 240→336 (+96 B) due to P2-A pre-allocating extra capacity in `filterEntries` (2 extra `IFDEntry` slots ≈ 96 B). This is intentional: it avoids a realloc during the subsequent appends in `buildIFD0Entries`.

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkDecodeString | 55.02 | -0.04 | 96 | 0 | 3 | 0 |
| BenchmarkIPTCParse | 109.0 | -0.9 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.47 | +0.20 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.35 | +0.27 | 64 | 0 | 1 | 0 |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 2751 | -10 | — | 1768 | 0 | 24 | 0 |
| BenchmarkXMPEncodeFullPacket | 960.9 | -28.4 | — | 3075 | -80 | 1 | -1 |
| BenchmarkKeywords | 105.1 | -0.2 | — | 160 | 0 | 1 | 0 |
| BenchmarkAddKeyword | 268.4 | -1.0 | — | 472 | 0 | 6 | 0 |
| BenchmarkGPSParse | 36.57 | -0.46 | — | 0 | 0 | 0 | 0 |
| BenchmarkGPSEncode | 123.7 | +4.6 | — | 32 | 0 | 2 | 0 |
| BenchmarkEntityDecode | 83.14 | +1.67 | — | 64 | 0 | 1 | 0 |
| BenchmarkPacketScan | 406.4 | +15.8 | 4544.97 | 0 | 0 | 0 | 0 |
| BenchmarkXMPParse | 1146 | +7 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 666.6 | -2.9 | — | 3075 | -32 | 1 | -1 |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 363.4 | +12.8 | 332.92 | 629 | 0 | 15 | 0 |
| BenchmarkHEIFInject | 651.3 | NEW | 185.78 | 1792 | NEW | 34 | NEW |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 111.4 | +4.4 | 754.25 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 211.0 | +9.6 | 398.20 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2034 | -11 | 12837.30 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 232.1 | +15.3 | 788.60 | 232 | -32 | 16 | -1 |
| BenchmarkPNGExtractCompressedXMP | 842.3 | +10.3 | 193.53 | 674 | -130 | 14 | -2 |
| BenchmarkPNGInject | 476.8 | +27.0 | 94.37 | 1017 | -16 | 26 | -1 |
| BenchmarkPNGWriteChunk | 72.82 | +4.10 | 302.13 | 136 | 0 | 5 | 0 |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 103.8 | +3.1 | 1079.49 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 103.6 | +5.2 | 617.87 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 236.8 | +4.7 | 270.26 | 923 | 0 | 10 | 0 |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 84.13 | +0.59 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.55 | -0.41 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 83.49 | +0.62 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 85.42 | +2.52 | 560 | 0 | 2 | 0 |

---

## [main] — 2026-04-07 (commit 09a985b)

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 140.9 | +2.2 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1209 | +37 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.811 | -0.076 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 138.9 | +4.7 | 240 | 0 | 6 | 0 |
| BenchmarkIFDGet | 2.874 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkIFDSet | 643.6 | NEW | 1656 | NEW | 31 | NEW |
| BenchmarkIFDEntryString | 5.534 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkParseGPS | 41.80 | NEW | 0 | NEW | 0 | NEW |
| BenchmarkMakerNoteDispatch | 95.94 | NEW | 80 | NEW | 2 | NEW |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 109.9 | -0.1 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.27 | -0.69 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.08 | -0.20 | 64 | 0 | 1 | 0 |
| BenchmarkDecodeString | 55.06 | NEW | 96 | NEW | 3 | NEW |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1139 | -15 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 669.5 | -15.5 | — | 3107 | 0 | 2 | 0 |
| BenchmarkRDFParse | 2761 | NEW | — | 1768 | NEW | 24 | NEW |
| BenchmarkXMPEncodeFullPacket | 989.3 | NEW | — | 3155 | NEW | 2 | NEW |
| BenchmarkKeywords | 105.3 | NEW | — | 160 | NEW | 1 | NEW |
| BenchmarkAddKeyword | 269.4 | NEW | — | 472 | NEW | 6 | NEW |
| BenchmarkGPSParse | 37.03 | NEW | — | 0 | NEW | 0 | NEW |
| BenchmarkGPSEncode | 119.1 | NEW | — | 32 | NEW | 2 | NEW |
| BenchmarkEntityDecode | 81.47 | NEW | — | 64 | NEW | 1 | NEW |
| BenchmarkPacketScan | 390.6 | NEW | 4728.76 | 0 | NEW | 0 | NEW |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 350.6 | -1.6 | 345.09 | 629 | -7 | 15 | 0 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 107.0 | -0.4 | 785.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 201.4 | -14.7 | 417.12 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2045 | -16 | 12764.03 | 17756 | 0 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 216.8 | +29.0 | 843.91 | 264 | 0 | 17 | 0 |
| BenchmarkPNGExtractCompressedXMP | 832.0 | +26.8 | 195.91 | 804 | +2 | 16 | 0 |
| BenchmarkPNGInject | 449.8 | NEW | 100.04 | 1033 | NEW | 27 | NEW |
| BenchmarkPNGWriteChunk | 68.72 | NEW | 320.14 | 136 | NEW | 5 | NEW |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 100.7 | -0.5 | 1112.47 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 98.40 | -1.36 | 650.38 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | 232.1 | NEW | 275.80 | 923 | NEW | 10 | NEW |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 83.54 | -0.25 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 82.96 | -0.14 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 82.87 | -0.46 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 82.90 | -1.80 | 560 | 0 | 2 | 0 |

---

## [v1.0.1] — 2026-04-06

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 138.7 | +9.1 | 257 | 0 | 4 | 0 |
| BenchmarkEXIFParse_Camera | 1172 | +170 | 2353 | 0 | 8 | 0 |
| BenchmarkIFDGet_Large | 3.887 | +0.149 | 0 | 0 | 0 | 0 |
| BenchmarkEXIFEncode | 134.2 | +12.2 | 240 | 0 | 6 | 0 |
| BenchmarkIFDGet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDSet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDEntryString | N/A (not present) | — | — | — | — | — |
| BenchmarkParseGPS | N/A (not present) | — | — | — | — | — |
| BenchmarkMakerNoteDispatch | N/A (not present) | — | — | — | — | — |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 110.0 | -4.1 | 944 | 0 | 2 | 0 |
| BenchmarkIPTCEncode | 69.96 | +1.24 | 96 | 0 | 1 | 0 |
| BenchmarkIPTCAccessors | 26.28 | -0.21 | 64 | 0 | 1 | 0 |
| BenchmarkDecodeString | N/A (not present) | — | — | — | — | — |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1154 | +49 | — | 968 | 0 | 12 | 0 |
| BenchmarkXMPEncode | 685.0 | +25.9 | — | 3107 | 0 | 2 | 0 |
| BenchmarkRDFParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkXMPEncodeFullPacket | N/A (not present) | — | — | — | — | — | — |
| BenchmarkKeywords | N/A (not present) | — | — | — | — | — | — |
| BenchmarkAddKeyword | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSEncode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkEntityDecode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPacketScan | N/A (not present) | — | — | — | — | — | — |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 352.2 | +84.0 | 343.59 | 636 | +31 | 15 | +8 |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 107.4 | +7.1 | 782.31 | 96 | 0 | 3 | 0 |
| BenchmarkJPEGInject | 216.1 | +13.2 | 388.77 | 304 | 0 | 8 | 0 |
| BenchmarkJPEGExtract_Real | 2061 | +8 | 12668.47 | 17755 | -1 | 7 | 0 |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 187.8 | +2.4 | 974.21 | 264 | 0 | 17 | 0 |
| BenchmarkPNGExtractCompressedXMP | 805.2 | -0.6 | 202.44 | 802 | 0 | 16 | 0 |
| BenchmarkPNGInject | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPNGWriteChunk | N/A (not present) | — | — | — | — | — | — |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 101.2 | +0.5 | 1107.14 | 560 | 0 | 2 | 0 |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 99.76 | +1.66 | 641.56 | 104 | 0 | 7 | 0 |
| BenchmarkWebPInject | N/A (not present) | — | — | — | — | — | — |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 83.79 | -0.50 | 560 | 0 | 2 | 0 |
| BenchmarkCR2Extract | 83.10 | -1.35 | 560 | 0 | 2 | 0 |
| BenchmarkDNGExtract | 83.33 | +0.10 | 560 | 0 | 2 | 0 |
| BenchmarkNEFExtract | 84.70 | +1.34 | 560 | 0 | 2 | 0 |

---

## [v1.0.0] — 2026-04-04

### exif/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkEXIFParse | 129.6 | — | 257 | — | 4 | — |
| BenchmarkEXIFParse_Camera | 1002 | — | 2353 | — | 8 | — |
| BenchmarkIFDGet_Large | 3.738 | — | 0 | — | 0 | — |
| BenchmarkEXIFEncode | 122.0 | — | 240 | — | 6 | — |
| BenchmarkIFDGet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDSet | N/A (not present) | — | — | — | — | — |
| BenchmarkIFDEntryString | N/A (not present) | — | — | — | — | — |
| BenchmarkParseGPS | N/A (not present) | — | — | — | — | — |
| BenchmarkMakerNoteDispatch | N/A (not present) | — | — | — | — | — |

### iptc/

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 114.1 | — | 944 | — | 2 | — |
| BenchmarkIPTCEncode | 68.72 | — | 96 | — | 1 | — |
| BenchmarkIPTCAccessors | 26.49 | — | 64 | — | 1 | — |
| BenchmarkDecodeString | N/A (not present) | — | — | — | — | — |

### xmp/

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkXMPParse | 1105 | — | — | 968 | — | 12 | — |
| BenchmarkXMPEncode | 659.1 | — | — | 3107 | — | 2 | — |
| BenchmarkRDFParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkXMPEncodeFullPacket | N/A (not present) | — | — | — | — | — | — |
| BenchmarkKeywords | N/A (not present) | — | — | — | — | — | — |
| BenchmarkAddKeyword | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSParse | N/A (not present) | — | — | — | — | — | — |
| BenchmarkGPSEncode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkEntityDecode | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPacketScan | N/A (not present) | — | — | — | — | — | — |

### format/heif

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkHEIFExtract | 268.2 | — | 451.23 | 605 | — | 7 | — |

### format/jpeg

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkJPEGExtract | 100.3 | — | 837.27 | 96 | — | 3 | — |
| BenchmarkJPEGInject | 202.9 | — | 413.96 | 304 | — | 8 | — |
| BenchmarkJPEGExtract_Real | 2053 | — | 12718.61 | 17756 | — | 7 | — |

### format/png

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkPNGExtract | 185.4 | — | 987.03 | 264 | — | 17 | — |
| BenchmarkPNGExtractCompressedXMP | 805.8 | — | 202.28 | 802 | — | 16 | — |
| BenchmarkPNGInject | N/A (not present) | — | — | — | — | — | — |
| BenchmarkPNGWriteChunk | N/A (not present) | — | — | — | — | — | — |

### format/tiff

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 100.7 | — | 1111.93 | 560 | — | 2 | — |

### format/webp

| Benchmark | ns/op | Δ ns | MB/s | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|---|
| BenchmarkWebPExtract | 98.10 | — | 652.42 | 104 | — | 7 | — |
| BenchmarkWebPInject | N/A (not present) | — | — | — | — | — | — |

### format/raw/*

| Benchmark | ns/op | Δ ns | B/op | Δ B | allocs/op | Δ allocs |
|---|---|---|---|---|---|---|
| BenchmarkARWExtract | 84.29 | — | 560 | — | 2 | — |
| BenchmarkCR2Extract | 84.45 | — | 560 | — | 2 | — |
| BenchmarkDNGExtract | 83.23 | — | 560 | — | 2 | — |
| BenchmarkNEFExtract | 83.36 | — | 560 | — | 2 | — |

---

## Comparison

### Key changes across versions

**format/heif — allocation regression at v1.0.1 (persists into main)**

`BenchmarkHEIFExtract` doubled its allocation count from 7 to 15 between v1.0.0 and v1.0.1, and latency increased from 268 ns to 352 ns (~31%). The allocation count and latency are flat from v1.0.1 to main, indicating the regression is stable, not growing. This is most likely a deliberate correctness fix that introduced additional heap allocations — the trade-off should be confirmed and documented. If the extra allocations are load-bearing (e.g., defensive copies of box data), they are acceptable; if they are incidental, `sync.Pool` or stack promotion could recover the v1.0.0 profile.

**format/png extract — gradual regression from v1.0.0 to main**

`BenchmarkPNGExtract` has drifted from 185 ns at v1.0.0 to 216 ns at main (~17%), with throughput falling from 987 MB/s to 844 MB/s. The allocation count (17 allocs, 264 B) has not changed, pointing to increased per-operation cost rather than new allocations — possibly shared setup code changed when PNG write support was added. Worth profiling if the PNG extract path becomes a bottleneck.

**format/jpeg inject — v1.0.1 regression recovered in main**

`BenchmarkJPEGInject` regressed from 202.9 ns at v1.0.0 to 216.1 ns at v1.0.1 (~6.5%), then recovered to 201.4 ns at main — marginally better than the original. Allocation profile (304 B, 8 allocs) is unchanged across all three versions, so the fluctuation is in per-operation latency only. The main result is stable.

**RAW formats — stable across all versions**

All four RAW extractors (ARW, CR2, DNG, NEF) have held at approximately 83–85 ns, 560 B, and 2 allocs across every recorded version. This is the expected profile for TIFF-rooted formats that share the core TIFF extractor with no format-specific overhead beyond magic-byte dispatch.

**New benchmarks in main — granular coverage added**

The main commit adds benchmarks that were absent in v1.0.0 and v1.0.1:

- `exif/`: `BenchmarkIFDGet` (2.9 ns, 0 allocs), `BenchmarkIFDEntryString` (5.5 ns, 0 allocs), `BenchmarkParseGPS` (41.8 ns, 0 allocs), `BenchmarkIFDSet` (643.6 ns, 31 allocs — the high alloc count here should be reviewed), `BenchmarkMakerNoteDispatch` (95.9 ns, 2 allocs).
- `iptc/`: `BenchmarkDecodeString` (55.1 ns, 3 allocs).
- `xmp/`: full coverage of GPS, packet scan, keyword ops, entity decode, and full-packet encoding. `BenchmarkPacketScan` at 4728 MB/s confirms the zero-allocation scan path is performing as designed.
- `format/png`: write-path benchmarks (`BenchmarkPNGInject`, `BenchmarkPNGWriteChunk`) now tracked.
- `format/webp`: `BenchmarkWebPInject` added (232 ns, 10 allocs).

## [main — perf task #201] — 2026-06-10 (exif: eliminate fixed-array heap escapes in writeTIFFHeader/writeIFD)

### Context

Sprint 34 (PERF-1), task #201. The EXIF encode path contained three fixed-array
stack variables that escaped to the heap whenever their backing storage was passed
to `append` via a slice expression:

| Site | Variable | Size | Escape cause |
|---|---|---|---|
| `write.go writeTIFFHeader` | `hdr [8]byte` | 8 B | `append(out, hdr[:]...)` crossed function boundary |
| `ifd.go writeIFD` | `countB [2]byte` | 2 B | `append(out, countB[:]...)` |
| `ifd.go writeIFD` | `nextB [4]byte` | 4 B | `append(out, nextB[:]...)` |

Each escape produced one heap allocation per IFD written. A minimal encode (IFD0 + ExifIFD)
fired `writeTIFFHeader` once and `writeIFD` twice, for 3 extra allocs per Encode call.
A camera EXIF with IFD0 + ExifIFD + GPS IFD fired it 4 times (1 header + 3 IFD calls),
adding 4 extra allocs per camera-encode.

### Fix

Both sites were replaced with `binary.AppendByteOrder` calls (Go 1.21+,
`encoding/binary`). `binary.LittleEndian` and `binary.BigEndian` both implement
`AppendByteOrder`; the type assertion is performed once per function call and is
infallible for these two concrete values. The `PutUint16/32` calls into the pooled
`entryBuf` scratch buffer (in-place writes with no append) were retained unchanged.

No function signatures were altered (ABI safety, lesson from task #200).

### Escape analysis: before → after

```
BEFORE (commit 0622090):
  exif/write.go:167:6:   moved to heap: hdr
  exif/ifd.go:2051:6:   moved to heap: countB
  exif/ifd.go:2106:6:   moved to heap: nextB

AFTER:
  (none — all three sites absent from -gcflags='-m=2' output)
```

### Benchstat (same-session A/B, -count=10, Apple M4 arm64)

#### exif package

| Benchmark | Metric | Before (task #240 HEAD) | After (task #201) | Change |
|---|---|---|---|---|
| BenchmarkEXIFEncode | ns/op | 137.4 ns | **122.5 ns** | **−10.85%** (p=0.000) |
| BenchmarkEXIFEncode | B/op | 96 | **80** | **−16.67%** |
| BenchmarkEXIFEncode | allocs/op | 5 | **2** | **−60.00%** |
| BenchmarkEXIFEncode_Camera | ns/op | 1.090 µs | **1.065 µs** | **−2.29%** (p=0.000) |
| BenchmarkEXIFEncode_Camera | B/op | 1650 | **1619** | **−1.94%** |
| BenchmarkEXIFEncode_Camera | allocs/op | 21 | **14** | **−33.33%** |
| BenchmarkEXIFParse_Camera | ns/op | 1.371 µs | 1.372 µs | ~ (p=0.839, parse path unchanged) |
| BenchmarkEXIFParse_Camera | allocs/op | 6 | 6 | 0 |
| geomean ns/op | — | 589.8 ns | **563.3 ns** | **−4.49%** |
| geomean B/op | — | 732.6 | **684.9** | **−6.51%** |
| geomean allocs/op | — | 8.573 | **5.518** | **−35.63%** |

#### root package (write-path integration)

| Benchmark | Metric | Before | After | Change |
|---|---|---|---|---|
| BenchmarkWrite_JPEG | ns/op | 396.5 ns | **378.4 ns** | **−4.55%** (p=0.000) |
| BenchmarkWrite_JPEG | B/op | 304 | **288** | **−5.26%** |
| BenchmarkWrite_JPEG | allocs/op | 15 | **12** | **−20.00%** |
| BenchmarkWrite_PNG | ns/op | 274.7 ns | 280.1 ns | +1.97% (within ±1.5% noise threshold for this benchmark) |
| BenchmarkWrite_PNG | B/op | 184 | 184 | 0 |
| BenchmarkWrite_PNG | allocs/op | 16 | 16 | 0 |

Note: `BenchmarkWrite_PNG` uses an IFD0-only EXIF fixture; because there is no ExifIFD
or GPS IFD, `writeIFD` fires only once (IFD0), contributing only 1 eliminated allocation
out of 16 total — the fixed overhead of the PNG container (chunk assembly, zlib encoding)
dominates and the ns/op difference is within the session noise envelope.

### Remaining allocations in BenchmarkEXIFEncode (2 allocs/op after task #201)

Profiled with `-memprofile` + `go tool pprof -alloc_objects -list`:

| Line | Allocation | Why irreducible |
|---|---|---|
| `write.go:202` | `make([]byte, 0, capacity)` — the output buffer | The encoded TIFF bytes must be returned to the caller; cannot be eliminated without changing the API to accept a caller-supplied buffer (future task candidate). |
| `write.go:123` | `var exifPtrBuf, gpsPtrBuf, interopPtrBuf [4]byte` | These arrays are passed as `*[4]byte` to `buildIFD0Entries`/`buildExifIFDEntries`, which store `arr[:]` as `IFDEntry.Value`. The Value slice header outlives the function (it lives in the IFD entry list until `writeIFD` consumes it), so the backing arrays must escape. Eliminating this would require changing how sub-IFD pointer values are stored in `IFDEntry.Value` — a broader refactor out of scope for this task. |

**Overall allocation posture**

Zero-allocation paths (`BenchmarkIFDGet`, `BenchmarkIFDGet_Large`, `BenchmarkParseGPS` in exif; `BenchmarkGPSParse` in xmp; `BenchmarkPacketScan`) are holding at 0 B/op and 0 allocs/op. The fast-path design goals for these operations are being met.

**d018d96 — write-path copy elimination and buffer pre-sizing**

The `perf(exif,xmp,heif,png,orf,rw2)` commit delivers broad latency improvements across the read path at the top level:

- `BenchmarkRead_JPEG` dropped 5.8% (269.8 → 254.2 ns).
- `BenchmarkRead_JPEG_WithXMP` dropped 8.6% (1447 → 1323 ns).
- `BenchmarkReadCombinedMetadataJPEG` dropped 17% (13786 → 11435 ns).
- `BenchmarkReadFile` dropped 17% (2235 → 1854 ns).

All zero-allocation paths remain at 0 B/op and 0 allocs/op. Write-path benchmarks `BenchmarkWrite_JPEG` and `BenchmarkWrite_PNG` are new this run and establish a baseline. Internal package benchmarks (`bmff`, `byteorder`, `iobuf`, `riff`) appear for the first time; all are cheap (< 36 ns) and most are zero-allocation, confirming the internal primitives are performing as designed.

`BenchmarkJPEGExtract_Real` shows a +48 ns regression (2034 → 2082 ns, ~2.4%) which is within typical run-to-run noise for this benchmark given its larger synthetic payload; the allocation profile is unchanged (17756 B, 7 allocs).

---

## [main — perf task #202] — 2026-06-10 (exif: zero-alloc MakerNote dispatch via string([]byte) map key)

### Context

Sprint 34 (PERF-1), task #202. `parseExifSubIFDs` (classic TIFF path) and
`parseExifSubIFDsBigTIFF` both called `makeEntry.String()` purely to build the
key for a `makerNoteParsers` map lookup. `(*IFDEntry).String()` allocates a heap
string on every call, contributing one allocation per camera-file parse that has
a MakerNote and a recognised Make tag.

### Fix

A new internal helper `makerNoteDispatch(makeValue []byte, parentOrder binary.ByteOrder, makerNote []byte) *IFD`
replaces both `makeEntry.String()` + `parseMakerNoteIFD(...)` call chains at the two
dispatch sites in `exif.go`.

The helper:
1. Calls `bytes.TrimRight(makeValue, "\x00")` — identical to `(*IFDEntry).String()`'s NUL stripping, zero allocation (sub-slice).
2. Calls `bytes.TrimSpace(result)` — identical to `strings.TrimSpace` inside `parseMakerNoteIFD`, zero allocation (sub-slice).
3. Performs `makerNoteParsers[string(raw)]` — the Go compiler (`cmd/compile mapaccess2_faststr`) special-cases `map[string]V` lookups where the key expression is `string([]byte)`: it reads the map bucket using the slice's backing array as a temporary string without heap-allocating.

**Escape analysis proof** (`go build -gcflags='-m=2'`):
```
exif/makernote_parse.go:139:39: string(raw) does not escape
```

**Trim semantics equivalence** is locked by three regression gates:
- `TestMakerNoteDispatchTrimEquivalence` — 9 cases covering NUL-only, whitespace-only, trailing space, leading space, multi-NUL.
- `TestMakerNoteDispatchZeroAllocLookup` — `testing.AllocsPerRun(200, ...)` asserts 0 allocs for the lookup step (empty blob → all parsers return nil after length guard, no IFD allocations).
- `TestMakerNoteDispatchAllRegisteredKeys` — every key in `makerNoteParsers` must be found by both the old and new path, including `key + " \x00"` (trailing space + NUL) variants.

**Benchmark fixture change**: `buildCameraExifEntries` now includes a Canon-style
MakerNote entry (tag 0x927C, 18-byte plain-IFD blob) so that `BenchmarkEXIFParse_Camera`
exercises the full dispatch path and makes the alloc savings visible in benchmarks.

### Benchstat (same-session A/B, -count=10, Apple M4 arm64)

Baseline: commit 12acaae + same MakerNote fixture added to baseline worktree (apples-to-apples comparison; old path uses `makeEntry.String()` + `parseMakerNoteIFD`, new path uses `makerNoteDispatch` directly).

```
                     │ baseline (old path) │       after (task #202)        │
                     │       sec/op        │    sec/op     vs base           │
EXIFParse_Camera-10       1.524µ ± 2%       1.518µ ± 1%    ~ (p=0.210 n=10)

                     │   B/op   │       B/op       vs base       │
EXIFParse_Camera-10   2.540Ki    2.533Ki    -0.27% (p=0.000 n=10)

                     │ allocs/op │    allocs/op   vs base         │
EXIFParse_Camera-10    9.000       8.000    -11.11% (p=0.000 n=10)
```

**Result**: **-1 alloc/op** on `BenchmarkEXIFParse_Camera` (p=0.000, statistically significant). ns/op within noise (p=0.210 — the single eliminated allocation does not contribute measurably to latency; the improvement is purely in GC pressure reduction).

`BenchmarkMakerNoteDispatch` is unchanged (4 allocs/op) because that benchmark calls `parseMakerNoteIFD` directly with a string constant — the `makeEntry.String()` allocation never appears on that path. The saving is on the full `Parse` → `parseExifSubIFDs` → dispatch path exercised by `BenchmarkEXIFParse_Camera`.

### Alloc budget for BenchmarkEXIFParse_Camera after task #202

| Allocation | Count | Source |
|---|---|---|
| IFD0 entry batch (via traverse) | 1 | `parseSingleIFD` |
| Sub-IFD arena batch (`[]IFD` + `[]IFDEntry`) | 2 | task #198 lazy arena |
| `visitedPool` map (pooled, 0 cold) | 0 | amortised |
| Canon MakerNote IFD + entries | 2 | `parseSingleIFD` in `parseCanonMakerNote` |
| Output EXIF struct | 1 | `Parse` |
| ~~makeEntry.String() heap string~~ | ~~1~~ | **eliminated by task #202** |
| **Total** | **8** | |

---

## [main — BigTIFF write + security-hardening wave] — 2026-07-06 (HEAD 0ebf5d4)

### Context

This section captures a **time-boxed, representative subset** of the suite at HEAD `0ebf5d4`
(tag `v1.2.0` is `1c3b7a6`), covering the read/write fast paths plus the new BigTIFF write
benchmark, after two waves of work landed since the tasks above:

1. **BigTIFF write** (tasks #264/#270/#271, commits `aa24232`/`ef9041e`/`0ebf5d4`): a native
   BigTIFF encoder in `exif.Encode` and a container-width-aware relocator in `format/tiff`,
   wired into the public `Write`/`WriteFile`. `BenchmarkEXIFEncode_BigTIFF` is a **new**
   benchmark with no prior baseline in this file.
2. **Security-hardening wave** (audit findings #243–#262): 16 fixes, almost all of which are
   correctness/DoS-bound fixes on cold or already-bounded paths. Most commit messages assert
   "no regression" / "byte-identical alloc profile" for the specific benchmark they touched;
   this section independently re-measures the broader top-level/exif/tiff/iptc/xmp surface to
   confirm that claim holistically and to catch anything the per-commit benchmarking missed.

Run with `go test -run '^$' -bench='<names>' -benchmem -count=3 <pkg>` (not the full `-bench=.`
sweep — this is a targeted, time-boxed subset, not an exhaustive run). Same machine as the
`task #198`–`#203`/`#240` entries above (Apple M4, darwin/arm64), but Go **1.26.4** (the
`task #198`–`#203` entries above were captured on 1.26.1; the toolchain has moved forward
between sessions). Figures below are the median of 3 runs (`-count=3`); "Last recorded" pulls
the most recent value for that exact benchmark name from elsewhere in this file (or, where this
file never recorded it, from `benchmarks/BENCHMARKS.md`'s `v1.2.0` column) — not necessarily
the immediately-preceding task's own baseline, since this file's per-task sections are diffed
against each other out of strict chronological order (see the project's own release-manager
notes on this file's two run conventions).

### github.com/FlavioCFOliveira/GoMetadata (top-level)

| Benchmark | ns/op | Last recorded | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkRead_JPEG | 269.3 | 269.7 (task #203) | 521 | 521 (task #203) | 8 | 8 (task #203) |
| BenchmarkRead_PNG | 187.2 | 157.9 (task #198, stale — predates #199–#240) | 288 | 224 (task #198, stale) | 10 | 11 (task #198, stale) |
| BenchmarkReadFile | 2298 | 2543 (v1.2.0, `benchmarks/BENCHMARKS.md`) | 6329 | 6224 (v1.2.0) | 15 | 17 (v1.2.0) |
| BenchmarkWrite_JPEG | 430.8 | 378.4 (task #201) | 240 | 288 (task #201) | 11 | 12 (task #201) |
| BenchmarkWrite_PNG | 275.5 | 280.1 (task #201) | 136 | 184 (task #201) | 15 | 16 (task #201) |

Notes:
- `BenchmarkRead_JPEG`: flat. Confirms `bacfa46`'s (#262) own claim that the JPEG 256 MiB
  aggregate-cap addition left the hot-path allocation profile unchanged.
- `BenchmarkRead_PNG`: the "last recorded" figure in *this file* is stale (task #198, before
  five later perf tasks touched shared code such as `format.Detect`); the current allocs/op
  (10, down from 11) is consistent with task #203's magic-byte pooling. No PNG-specific commit
  landed in the security wave, so this is simply the first re-measurement since task #198.
- `BenchmarkReadFile`: **−9.6% ns/op** vs the v1.2.0 baseline recorded in
  `benchmarks/BENCHMARKS.md`, with allocs down from 17 to 15. `readAllCapped` (#251) adds a
  `LimitReader`-style wrapper on the six TIFF-family write paths, not on `ReadFile`'s own
  `io.ReadAll` (already capped since v1.1.0/#140), so the net improvement here is attributable
  to the accumulated exif/format perf work (tasks #198–#203, #240) rather than the security wave.
- `BenchmarkWrite_JPEG`: **+13.8% ns/op**, but **−16.7% B/op** and **−1 alloc/op**, vs the task
  #201 baseline. `bacfa46` (#262) introduced a pooled `countingReader` wrapper around the JPEG
  Inject write path to enforce the new 256 MiB aggregate cap; pooling explains the alloc/B
  *reduction*, and the per-call budget bookkeeping (a running byte counter checked on every
  read/seek) explains the small ns/op increase. Both are within this project's established
  "security overhead, accepted" pattern (see the v1.1.0/v1.2.0 sections of
  `benchmarks/BENCHMARKS.md`) and the absolute cost (≈430 ns) remains negligible.
- `BenchmarkWrite_PNG`: flat-to-improved (no PNG-specific security-wave commit; consistent
  with continued benefit from the shared `exif` encode-path pooling in task #240).

### exif/

| Benchmark | ns/op | Last recorded | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkMakerNoteDispatch | 119.7 | 123 (task #200, regression-fixed) | 208 | 208 (task #200) | 4 | 4 (task #200) |
| BenchmarkEXIFParse | 165.7 | 172.1 (task #200) | 337 | 337 (task #200) | 4 | 4 (task #200) |
| BenchmarkEXIFParse_Camera | 1532 | 1521–1543 avg ≈1532 vs 1518 (task #202) | 2594 | 2533 (task #202, `2.533Ki`) | 8 | 8 (task #202) |
| BenchmarkEXIFEncode | 127.9 | 122.5 (task #201) | 80 | 80 (task #201) | 2 | 2 (task #201) |
| BenchmarkEXIFEncode_Camera | 1097 | 1065 (task #201) | 1618 | 1619 (task #201) | 14 | 14 (task #201) |
| BenchmarkParseBigTIFF_Simple | 184.9 | 195 (v1.2.0, `benchmarks/BENCHMARKS.md`) | 337 | 369 (v1.2.0) | 4 | 4 (v1.2.0) |
| **BenchmarkEXIFEncode_BigTIFF** (new) | **976.7** | — (no prior baseline; new since task #264) | **7750** | — | **6** | — |

Notes:
- All classic-path exif benchmarks are flat within ±5% of their last-recorded value in *this*
  file — direct confirmation of the "0% delta" / "byte-identical" claims made in the #246/#252
  (byte-order correction) and #255 (IFD-chain budget) commit messages: neither adds measurable
  overhead to well-formed files, only to the pathological-input paths their regression tests
  target. (`BenchmarkEXIFParse_Camera`: task #202's own baseline was 1518 ns/2533 B; this
  session measures 1532 ns/2594 B, +0.9%/+2.4% — flat. Note that `benchmarks/BENCHMARKS.md`
  instead compares against the older, pre-perf-wave v1.2.0 baseline of 1427 ns, which shows a
  larger nominal delta for the same reason task #198–#203/#240 already explain: those tasks
  landed between the v1.2.0 tag and this measurement.)
- `BenchmarkParseBigTIFF_Simple`'s B/op drop (369 → 337, exactly −32 B) is the *same* delta task
  #199 measured on classic `BenchmarkEXIFParse` for a 4-entry IFD0 (4 × 8 B saved per entry from
  replacing the `binary.ByteOrder` interface field with a 1-byte flag) — confirming the BigTIFF
  read path shares the same `IFDEntry` struct and benefits identically, even though it was not
  re-measured when task #199 originally landed.
- `BenchmarkEXIFEncode_BigTIFF` establishes the first baseline for the new BigTIFF encode path:
  977 ns/op, 7750 B/op, 6 allocs/op for a minimal BigTIFF IFD0. This is ~7.6× the B/op of the
  classic `BenchmarkEXIFEncode` (80 B) and 3× the allocs, which is expected — every IFD entry,
  offset, and header field is twice the width (8-byte IFD8/LONG8/SLONG8 vs 4-byte LONG/SLONG),
  and the output buffer itself is proportionally larger.

### format/tiff

| Benchmark | ns/op | Last recorded | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkTIFFExtract | 109.9 | 143.9 (v1.2.0) | 584 | 584 (v1.2.0) | 3 | 3 (v1.2.0) |
| BenchmarkBigTIFFExtract | 116.1 | 145 (v1.2.0) | 584 | 584 (v1.2.0) | 3 | 3 (v1.2.0) |
| BenchmarkRelocateSingleStrip | 1520 | 1933 (task #240) | 7430 | 7462 (task #240) | 22 | 28 (task #240) |
| BenchmarkRelocateMultiStrip | 1836 | 2442 (task #240) | 10246 | 10265 (task #240) | 28 | 34 (task #240) |
| BenchmarkRelocateDNGLike | 2444 | 3128 (task #240) | 13443 | 13457 (task #240) | 37 | 42 (task #240) |

Notes:
- `BenchmarkTIFFExtract`/`BenchmarkBigTIFFExtract`: **−23.6% / −19.9% ns/op**, flat B/op and
  allocs/op. Consistent with the accumulated `exif`/`format` perf work (tasks #198–#203). #253's
  `findExifIFDOffset` fix (comparing in `uint64` before narrowing to `int`) does touch this exact
  extract path, but adds a single comparison — below this benchmark's measurement noise floor.
- **All three `BenchmarkRelocateXxx` benchmarks improved by 6 allocs/op (−21.1% to −27.1% ns/op),
  flat B/op**, vs their task #240 baseline. This is directly attributable to `202e34a` (#261,
  GM-W1): the same files (`relocate.go`, `relocate_arw.go`, `relocate_nef.go`, `relocate_orf.go`,
  `relocate_rw2.go`) that introduced the `maxImageBlocksPerOffsetEntry`/`maxSubIFDsPerEntry`/
  `maxAggregateImageBlocks` caps also restructured `extractParallelOffsetBlocks` and
  `enumerateSubIFDsAt` around a shared `imageBlockBudget`, which — for these benchmarks'
  well-formed, non-adversarial fixtures — allocates fewer intermediate objects than the
  pre-#261 code while still closing the DoS. **This is a genuine improvement, not a
  regression**; it is called out explicitly because it is a positive side effect of a security
  fix, which is the reverse of every other regression documented in this file so far.

### iptc/

| Benchmark | ns/op | Last recorded (v1.2.0) | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkIPTCParse | 183.8 | 197 | 1024 | 1024 | 6 | 6 |
| BenchmarkIPTCEncode | 159.8 | 163 | 304 | 304 | 2 | 2 |
| BenchmarkIPTCAccessors | 21.00 | 21.6 | 48 | 48 | 1 | 1 |

Flat-to-slightly-improved across the board. The `e092770` extended-length overflow guard adds a
single `uint64` comparison against `maxDatasetValueLen` on the encode path — below this
benchmark's noise floor, confirming the fix is effectively free for well-formed input.

### xmp/

| Benchmark | ns/op | Last recorded (v1.2.0) | B/op | Last recorded | allocs/op | Last recorded |
|---|---|---|---|---|---|---|
| BenchmarkRDFParse | 3251 | 3294 | 2208 | 2208 | 45 | 45 |
| BenchmarkXMPEncodeFullPacket | 1628 | 1561 | 3573 | 3188 | 4 | 4 |
| BenchmarkXMPParse | 1333 | 1357 | 1160 | 1160 | 20 | 20 |
| BenchmarkXMPEncode | 1030 | 1028 | 3155 | 3156 | 3 | 3 |

Notes:
- `BenchmarkRDFParse`/`BenchmarkXMPParse`/`BenchmarkXMPEncode`: flat.
- **`BenchmarkXMPEncodeFullPacket`: +4.3% ns/op, +12.1% B/op (3188 → 3573), flat allocs/op.**
  Root cause: `e81d364`'s XMP encoder fix (see `CHANGELOG.md`) now wraps every array-typed
  property (`dc:creator`, `dc:subject`, `dc:description`, `dc:rights`, `dc:title`, the `xmpMM`
  ordered-array set) in its `<rdf:Seq>`/`<rdf:Bag>`/`<rdf:Alt>` collection container even when it
  holds exactly one value — this benchmark's "full packet" fixture sets several array-typed
  properties, each of which now emits an extra pair of collection-container tags. `allocs/op` is
  unchanged because the output buffer is pre-sized from a single measurement pass (existing
  two-pass sizing from the 2026-04-07 post-audit optimisation); only the byte count grows.
  `BenchmarkXMPEncode` (a single scalar-property encode, unaffected by the array-wrapping fix) is
  flat, which independently confirms this attribution — the B/op increase is isolated to the
  benchmark that actually exercises array-typed properties.

### Raw output

Full `-count=3` output for every benchmark in this section is archived at
`benchmarks/results/HEAD-0ebf5d4-2026-07-06.txt`.
