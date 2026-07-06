---
name: project_cr3_extsize01_fix
description: CR3-EXTSIZE-01 fix (task #256) — write path now handles ISOBMFF extended-size (largesize) moov/uuid box headers instead of hardcoding +8
metadata:
  type: project
---

CR3-EXTSIZE-01 (rmp task #256, HIGH severity data-corruption bug) fixed in
`format/raw/cr3/cr3.go`.

**Bug:** `cr3.Inject`'s write path (`injectIntoMoov`, `rebuildMoovContent`) hardcoded the
normal 8-byte ISOBMFF box header length when slicing `moov`/Canon-`uuid` box content. The
READ path (`findBox`, `findUUIDBox`, `parseCR3BoxHeader`) already correctly resolved
extended-size boxes (ISO 14496-12 §4.2: `size==1` + 8-byte `largesize` → 16-byte header).
On a source file whose `moov` or Canon `uuid` box used extended encoding, the write path
sliced 8 bytes too early, embedding half the `largesize` field as bogus leading content —
silently discarding the new EXIF (moov case) or dropping sibling sub-boxes CMT2/`XMP ` even
when the caller passed `nil` intending to preserve them (uuid case). `Inject` returned NO
error in either case.

**Fix pattern — recompute, don't thread a new return value:** Rather than changing
`findMoovRange`'s return arity (which would have required touching ~12 existing call sites
across `cr3_test.go` and `conformance_test.go`), both `injectIntoMoov` and
`rebuildMoovContent` now call `parseCR3BoxHeader(data, knownStartOffset)` a second time at
the point of use to recover the real `headerLen`. This is correct because `parseCR3BoxHeader`
is pure and side-effect-free, and the position was already validated by the caller
(`findMoovRange` / `flatUUIDBoxRange`) — the second call is guaranteed to succeed (guarded
defensively anyway, never actually taken). This is the SAME technique the original audit
recommendation explicitly endorsed as the alternative for the `uuid` case ("re-derive ...
via parseCR3BoxHeader(moovContent, uuidStart) (or have flatUUIDBoxRange return it)") —
applied symmetrically to `moov` too, for consistency and minimal test-file churn.

**Incidental pre-existing bug fixed in the same edit:** `flatUUIDBoxRange` was missing the
`size >= headerLen+16` guard that `findUUIDBox` already had. Without it, the new
`uuidStart+headerLen+16` slice in `rebuildMoovContent` could underflow (negative-length
slice panic) on a crafted `uuid` box whose declared size is smaller than `headerLen+16`.
Added the identical guard, mirroring `findUUIDBox`.

**Delta/stco accounting:** No extra bookkeeping needed. `buildBox`/`buildUUIDBox` (the
output constructors) ALWAYS emit the normal 8-byte-header form — CR3 write output never
re-emits extended-size encoding. `delta = len(newMoovBox) - oldMoovSize` already compares
whole boxes (header included on both sides), so any header-length shrinkage (16→8 bytes on
extended input) is automatically captured in `delta` with no separate term required.
`relocateChunkOffsets` operates on `newMoovBox[8:]` — safe because `newMoovBox` is always
freshly built in normal form.

**Regression tests** (`format/raw/cr3/extended_size_test.go`, new file):
- `TestCR3InjectExtendedSizeMoovBoxPreservesEXIF` — extended `moov` wrapping normal `uuid`+`CMT1`.
- `TestCR3InjectExtendedSizeUUIDBoxPreservesSiblings` — normal `moov` wrapping extended `uuid` with `CMT1`+`CMT2`+`XMP `; asserts CMT2/XMP survive and rawXMP round-trips via Extract.
- Both confirmed to FAIL against pre-fix code (verified via `git stash` on cr3.go only) and PASS after the fix.
- Reusable helpers `buildExtendedBox` / `buildExtendedUUIDBox` / `ftypBox16` added for a future `FuzzCR3Inject` corpus seed.

[[feedback_relocate_isobmff_recursion]]
[[project_cr3_conformance_battery]]
