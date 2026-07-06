---
name: project_task198_arena
description: Task #198 — exif sub-IFD parse-level arena; lazy approach; -25% allocs for Camera; all acceptance criteria met
metadata:
  type: project
---

Task #198 implements a lazy sub-IFD arena in `exif.Parse` to reduce per-IFD allocation.

**Result**: `BenchmarkEXIFParse_Camera` allocs/op: 8 → 6 (-25%); ns/op flat within run-to-run noise (-0.4% to +1.7% across paired -count=10 benchstat runs). Root benchmarks (benchstat p=0.000): BenchmarkRead_JPEG -3.12%, BenchmarkRead_PNG -1.78%; allocs unchanged.

**Why:** `parseSingleIFD` was the #1 allocator (25.9% alloc_objects, 36.5% alloc_space per perf audit). Sub-IFDs each triggered 2 heap allocations (`*IFD` + `[]IFDEntry`); arena co-allocates them.

**How to apply:** When modifying the Parse path, be aware that IFD0 uses the original `traverse` (direct `parseSingleIFD` calls), while sub-IFDs use `traverseWithArena` with the `parseArena` populated by `scanSubIFDs`. The hint order in `scanSubIFDs` MUST match the consumption order in `parseExifSubIFDs` + `parseGPSSubIFD`: ExifIFD(0) → InteropIFD(1, if present) → GPSIFD(last).

**Arena safety invariant**: `entryBatch[lo:lo:lo+count]` — cap-clamped so append beyond count reallocates outside the arena. Tested by `TestArenaNeighbourCorruption_*` in `exif/task198_arena_test.go`.

**MakerNote arena exclusion (documented decision)**: MakerNote IFDs are NOT in the arena. Reasons: (1) they parse from `mn.Value` (different buffer origin from main TIFF buf); (2) Nikon Type 3 / Fujifilm further sub-slice that blob; (3) 18+ manufacturer format-detection heuristics are interleaved with parsing — pre-scanning requires duplicating all that logic; (4) the Make tag isn't available at scan time. Decision documented in `parseMakerNoteIFD` comment and BENCHMARKS.md #198 section. BenchmarkMakerNoteDispatch: unchanged at 6 allocs/op, ~280 ns/op.

**Dead code removed**: `scanClassicIFDChain`, `scanAllClassicIFDs`, `scanVisitedCap` — all superseded.

**B/op note**: Camera benchmark B/op increased from 2818→2994 (+176 B, i.e. 2994−2818=176) — batch rounding; expected trade-off for 2 fewer allocations.
