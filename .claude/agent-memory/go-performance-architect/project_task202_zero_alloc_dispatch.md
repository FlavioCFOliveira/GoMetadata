---
name: project_task202_zero_alloc_dispatch
description: Zero-alloc MakerNote dispatch via string([]byte) map key — task #202 evidence and design
metadata:
  type: project
---

# Zero-alloc MakerNote dispatch (task #202)

**Fact:** `makeEntry.String()` at both dispatch sites in `parseExifSubIFDs` and `parseExifSubIFDsBigTIFF` was allocating a heap string on every camera-file parse with a MakerNote. Replaced with `makerNoteDispatch(makeEntry.Value, order, mn.Value)` which uses `string([]byte)` directly as a map key expression.

**Why:** The Go compiler (`cmd/compile mapaccess2_faststr`) elides the heap allocation for `map[string]V` lookups when `string([]byte)` appears directly as the index expression. Verified with `go build -gcflags='-m=2'`: `string(raw) does not escape`.

**Result:** -1 alloc/op on BenchmarkEXIFParse_Camera (9→8, p=0.000, -11.11%); -7 B/op.

**How to apply:** Whenever trimming raw bytes for a map lookup — never create an intermediate `string` variable. The `string([]byte)` must appear directly as the map index expression `m[string(slice)]`, not stored first.

**Trim semantics:** `bytes.TrimRight(v, "\x00")` then `bytes.TrimSpace(...)` before the lookup. This matches `(*IFDEntry).String()` + `strings.TrimSpace` from `parseMakerNoteIFD`. The old `parseMakerNoteIFD(string)` path is preserved for test compatibility.

**Benchmark fixture change:** `buildCameraExifEntries` (exif_test.go) now includes a Canon MakerNote blob (tag 0x927C, 18-byte plain IFD) so `BenchmarkEXIFParse_Camera` exercises the full dispatch path. The baseline for the A/B comparison must use the SAME fixture with the old code path.

**Evidence files:** `/tmp/t202_base.txt`, `/tmp/t202_after.txt` (deleted after session); results recorded in `BENCHMARKS.md` section "[main — perf task #202]".

See also: [[project_task200_warn_defer]] (same pattern of avoiding allocations on hot dispatch paths).
