---
name: project-task240-entry-pool
description: Pool design for filterEntries scratch slices in exif.Encode (task #240); -71% B/op on BenchmarkEXIFEncode, -11% on TIFF relocate benchmarks
metadata:
  type: project
---

Pool design for `filterEntries` scratch slices in `exif.Encode` — task #240, performance audit 2026-06-10 finding F41.

**Fact**: `buildIFD0Entries` and `buildExifIFDEntries` previously called `filterEntries` (now removed) which did `make([]IFDEntry, n, n+extraCap) + copy` on every `Encode`. On the TIFF relocate profile this was the #2 flat allocator at 9.35% of alloc_space.

**Solution**:
- `entrySlicePool sync.Pool` in `exif/write.go` stores `*[]IFDEntry` (pointer to slice header), `New` allocates 64-entry backing array
- `getEntrySlice()` / `putEntrySlice(p)` helpers — Put zeros elements (`clear(*p)`) before returning to pool to release `IFDEntry.Value` aliases
- `filterEntriesInto(ifd, dst, extraCap, exclude...)` in `ifd.go` — pool-aware variant: reslices `*dst` to 0, bulk-copies (fast path) or filters into it
- `serialise()` gets both scratch slices at top, passes to build functions, `defer putEntrySlice` on both — fires on ALL return paths
- Build functions `*scratch = entries` at end to sync the pooled header so `putEntrySlice` zeros all live elements
- `maxPooledCap = 128` — discard backing arrays grown beyond this (no camera has IFDs this large)

**Safety properties**:
- Gets happen AFTER the BigTIFF early-return check → always paired with deferred Puts on all reachable paths
- No consumer retains a reference after `serialise` returns (verified: `ifdTotalSize`, `computeIFDOffsets`, `patchPointers`, `writeTIFFHeader`, `writeIFD`, `writeSubIFDs` all read-only consumers within same call frame)
- Race-free: pool identity is per-`serialise` call; independent concurrent Encode calls get independent scratch slices from the pool

**Results** (benchstat count=10, benchtime=3s):
- `BenchmarkEXIFEncode`: 336 B/op → 96 B/op (−71%), 6 → 5 allocs/op (−17%), 156.7 → 140.1 ns/op (−10.6%)
- `BenchmarkWrite_JPEG`: 448 B/op → 305 B/op (−32%), 16 → 15 allocs/op (−6.3%)
- `BenchmarkRelocateSingleStrip`: 8412 B/op → 7462 B/op (−11%), 30 → 28 allocs/op (−7%)
- `BenchmarkRelocateDNGLike`: 14326 B/op → 13457 B/op (−6%), 44 → 42 allocs/op (−4.5%)
- pprof: `filterEntriesInto` completely absent from top-20 alloc_space nodes after task #240

**Regression gates**: `exif/task240_entry_pool_test.go` — IFD mutation tests, concurrent race test, Value-alias clearing test.

**Why:** Pool pattern is identical to `visitedPool` (ifd.go) and `iobuf` package — established library convention. MakerNote IFDs are excluded from the pool (different buffer origin, IFD parser is a separate code path).

**How to apply:** When pooling scratch `[]T` slices for serialise-style functions: use pointer-to-slice (`*[]T`) so the backing array survives GC; zero elements before Put when T contains reference types; verify no consumer retains a reference past the outer function's return; use `defer Put` (not explicit) to cover all error paths.
