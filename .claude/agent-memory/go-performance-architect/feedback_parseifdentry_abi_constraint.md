---
name: feedback_parseifdentry_abi_constraint
description: parseIFDEntry must return only (IFDEntry, bool) — adding any struct return causes ARM64 compiler to emit STP zero-init in hot loop, measured +10-18% ns/op regression
metadata:
  type: feedback
---

Never add struct-typed return values to `parseIFDEntry` in `exif/ifd.go`.

**Why**: `parseIFDEntry` is called for every IFD entry in the hot `fillIFD` loop. On ARM64, the Go ABI passes return values in registers when the total fits ≤ 15 register words. However, because `IFDEntry` contains pointer fields (`Value []byte`), the compiler must zero-initialise the return area before each call for GC correctness — even when all values fit in registers. Adding a `parseWarn` struct (even 20 bytes = 5 words) to the return tuple means the compiler emits 3×STP zero-init instructions per loop iteration, causing a measured +10.56% regression on `EXIFParse` and +18.07% on `EXIFParse_Camera` (same-session A/B, benchstat p=0.000 n=10).

**How to apply**: Keep `parseIFDEntry` returning `(IFDEntry, bool)` forever. If new per-entry data needs to reach the caller, use: (a) a bool flag, (b) a single int/uint32, or (c) move the check inline into `fillIFD` after the call. The OOL alias check (`warnOOLAliasIFD`) is an example of option (c) — it was moved inline to `fillIFD` and uses `ifdStart`/`ifdEnd` already held there. See [[project_task200_warn_defer]] for full root-cause analysis.
