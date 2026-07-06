---
name: project_task203_magic_pool
description: format.Detect magic-byte buffer pooled via magicPool; -11% allocs/op BenchmarkRead_JPEG; -8.4% B/op
metadata:
  type: project
---

Task #203: pool the 36-byte `[magicLen]byte` magic scan buffer in `format.Detect`.

**Why:** `var buf [magicLen]byte` + `r.Read(buf[:])` through io.Reader interface causes heap escape on every `Detect` call (confirmed by `-gcflags="-m=2"`). Detect sits on BOTH Read and Write paths of every format — highest-breadth allocator in the library (~10% alloc_objects, ~30.4 M objects in audit).

**Fix:** Added `magicPool sync.Pool` of `*[magicLen]byte` in `format/detect.go`, mirroring the existing `tiffScanPool` pattern. `Detect` gets from pool, passes pointer to `r.Read`, calls `detectMagic` synchronously, returns to pool before ALL return paths (incl. seek-back error path).

**Results (same-session A/B, -count=10, benchstat p=0.000):**
- `BenchmarkDetect`: 0 allocs/op, 0 B/op, ~9.85 ns/op (new benchmark)
- `BenchmarkRead_JPEG`: 9→8 allocs/op (-11.1%), 569→521 B/op (-8.4%), -2.8% ns/op
- `BenchmarkRead_JPEG_WithXMP`: 24→23 allocs/op (-4.2%), 2503→2456 B/op (-1.9%)

**Acceptance gate:** `TestDetect_ZeroAllocs` asserts `AllocsPerRun(100, ...) == 0`.

**Safety:** buffer consumed synchronously; `detectMagic` takes `bp[:n]` (a slice), TIFF refinement does its own independent seek+read from `tiffScanPool` and never touches this buffer.

**Key pattern:** `bp := magicPool.Get().(*[magicLen]byte)` — pass pointer directly to `r.Read(bp[:])` so only ONE heap object (the pooled array) is involved; no intermediate slice header escapes.

See [[project_task240_entry_pool]] and [[project_task198_arena]] for similar pool patterns.
