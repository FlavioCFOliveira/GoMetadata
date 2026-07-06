---
name: project_task200_warn_defer
description: task #200 — exif: defer fmt.Sprintf in parseSingleIFD using compact parseWarn records; regression-fixed 2026-06-10; eliminates warning-string heap allocs from parse hot path with zero overhead on clean files
metadata:
  type: project
---

Task #200 is COMPLETE with regression fix (2026-06-10).

**What was done**: Eliminated `fmt.Sprintf` from the EXIF parse hot path in `exif/ifd.go` and `exif/exif.go`. All warning-emitting sites now accumulate compact `parseWarn` records (20 bytes: `kind warnKind`, `[3]byte` explicit padding, `val1–val4 uint32`) instead of calling `fmt.Sprintf`. A single `materializeWarnings([]parseWarn) []string` call at the `Parse` boundary converts records to strings using `strconv.AppendUint` + a 256-byte stack buffer. On clean files the records slice is nil — zero overhead.

**Regression fix**: An earlier implementation returned `(IFDEntry, bool, parseWarn)` from `parseIFDEntry` (the per-entry hot loop function). A same-session A/B with benchstat (p=0.000 n=10) proved a +10.56% regression on `EXIFParse` and +18.07% on `EXIFParse_Camera`. Root cause: the compiler emits 3×STP zero-init instructions before each `parseIFDEntry` call in the hot loop when the return tuple contains pointer-containing fields plus extra fields — even if the total fits within 15 ARM64 ABI registers. Fix: `parseIFDEntry` returns only `(IFDEntry, bool)`; the OOL alias check (`warnOOLAliasIFD`) moved inline into `fillIFD` using `ifdStart`/`ifdEnd` already available there.

**Why**: `fmt.Sprintf` in `parseSingleIFD` was ~9% of alloc_objects on Canon files (duplicate-tag dedup warning fires on Canon MakerNote). Most callers never read `EXIF.Warnings`.

**Key results** (same-session A/B, benchstat -count=10, benchtime=3s, commit 6cf3462 baseline):
- `BenchmarkMakerNoteDispatch`: −55.8% ns/op (279→123ns), −39.5% B/op (344→208), −33.3% allocs (6→4)
- `BenchmarkEXIFParse`: ~0% (p=0.269, within noise); 337 B/op, 4 allocs unchanged
- `BenchmarkEXIFParse_Camera`: −0.55% (p=0.001, noise floor); 2482 B/op, 6 allocs unchanged
- pprof: `fmt.Sprintf` absent from top alloc_objects profile on BenchmarkEXIFParse_Camera

**CRITICAL design rule for `parseIFDEntry`**: NEVER add a return value containing struct types (even small ones) to `parseIFDEntry`. The function is in the hot IFD traversal loop; any extra return value that triggers compiler zero-init adds measurable overhead. Return only `(IFDEntry, bool)`. If new per-entry information needs to flow to the caller, use a bool flag or an int, never a struct.

**Message-lock test**: `TestParseWarnMessageLock` in `exif/ifd_audit_test.go` asserts each of 8 warnKind variants renders to a hard-coded literal string. Catches future format drift.

**API invariant**: `EXIF.Warnings []string` type unchanged; message text byte-identical to former fmt.Sprintf output.

**How to apply**: See [[project_task198_arena]] for the preceding arena optimisation. Future warning-path changes must update both `warnString()` switch cases in `exif/ifd.go` AND the literal strings in `TestParseWarnMessageLock`.
