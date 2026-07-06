---
name: audit-findings-20260609-heif
description: format/heif audit 2026-06-09 — 3 findings: #106 CONFIRMED LIVE (parseInfeV0V1 OOB panic), HEIF-NEW-01 buildInjectComponents OOB slice (same fuzz crasher as HEIF-INJECT-01), HEIF-NEW-02 construction_method read-path silently misresolves idat/item extents
metadata:
  type: project
---

## Scope
`format/heif/*.go` — full read of heif.go + all test files + conformance doc §4-6.
Date: 2026-06-09. All probe scratch tests deleted before exit.

## FINDING: CONFIRMS-#106 — parseInfeV0V1 OOB panic (CRITICAL, CONFIRMED LIVE)

- **Location**: `format/heif/heif.go:1013` — `bytes.IndexByte(data[pos:], 0x00)`
- **Root cause**: `parseInfeV0V1` at line 1011 does `pos += 2` (skip item_protection_index) with no bounds check. After reading item_ID (2 bytes from pos=4), pos=8. For an infe box with body=6 bytes (size=14), len(data)=6. `data[8:]` with len=6 → **panic: slice bounds out of range [8:6]**.
- **Trigger**: Any infe v0 or v1 box with size = 14 (body = 6 bytes = version+flags(4)+item_ID(2), no room for protection_index).
- **PoC**: `ftyp(16) + meta(with iinf containing infe(size=14, body=[0x00,0x00,0x00,0x00, 0x00,0x01]))` — Extract panics.
- **Observed**: `panic: runtime error: slice bounds out of range [8:6]` at `parseInfeV0V1` → `parseInfe` → `parseIinf` → `extractFromMetaData` → `Extract`.
- **Fix**: Add bounds check after the second `pos += 2` in parseInfeV0V1: `if pos > len(data) { return id, "" }`.
- **Status**: #106 is LIVE and unpatched. The existing TestHEIFRobustInfeOOB does NOT cover size=14 infe boxes — it only covers truncation at box header level.

## FINDING: CONFIRMS-HEIF-INJECT-01 — buildInjectComponents OOB slice (CRITICAL, FUZZER CONFIRMED)

- **Location**: `format/heif/heif.go:173` — `metaContent := data[metaContentOff:metaAbsEnd]`
- **Root cause**: `findMetaBoxAbs` returns `metaContentOff = metaAbsStart + 8 + 4 = metaAbsStart + 12`. But if the meta box has size < 12 (e.g. size=8 header-only, or size=11), then `metaAbsEnd < metaContentOff`. The slice `data[metaContentOff:metaAbsEnd]` with `metaContentOff > metaAbsEnd` panics.
- **Trigger**: A `meta` box with size 8–11 (passes parseHEIFBoxHeader size >= 8 check, but < 12 needed for FullBox version+flags).
- **PoC**: `ftyp(16) + meta([0x00,0x00,0x00,0x0b, 'm','e','t','a','0','0','0'])` — size=11, Inject panics.
- **Observed**: Fuzzer found `7f95ba61145c377b` corpus entry; `panic: slice bounds out of range [12:11]` at `buildInjectComponents` → `Inject`.
- **Fix**: In `Inject` (or `findMetaBoxAbs`), validate `metaContentOff <= metaAbsEnd` before calling `buildInjectComponents`. If not, treat as not-found and pass through.
- **Note**: Extract path is SAFE (findBox returns empty/nil slice for tiny meta boxes, doesn't produce OOB). Only Inject is affected.
- **Status**: Same root cause as HEIF-INJECT-01 from 2026-06-09 concurrency audit; fuzz crasher saved then deleted.

## FINDING: HEIF-NEW-01 — construction_method silently misresolved in read path (MEDIUM, spec conformance gap)

- **Location**: `format/heif/heif.go:1155-1156` (parseIlocItemSimple), also `readIlocSimpleExtents`
- **Root cause**: `parseIlocItemSimple` skips `construction_method` (pos += 2) but does not use it. Extents are resolved as file-absolute (construction_method==0) regardless of declared value. Items with construction_method==1 (idat-relative) or ==2 (item-relative) will produce wrong offsets in the read path — either returning garbage bytes as EXIF/XMP, or silently returning nothing.
- **Spec clause**: ISO 14496-12 §8.11.3; HEIF §5(c); conformance doc ROB §5(f) "construction_method variants".
- **Impact**: Silent data-integrity failure (wrong or no metadata for idat/item-relative extents). Not a panic.
- **Exploitability**: Probable — a crafted file with construction_method=1 can make the library read from wrong file positions. Actual data returned depends on what bytes happen to be at that offset.
- **Fix**: In parseIlocItemSimple, parse and check construction_method; return zero loc (skip item) for methods != 0. (Note: the write path already handles this correctly via updateIlocItems.)
- **Status**: NEW finding. Conformance battery has no test for construction_method != 0 in read path.

## Analysis: ErrMaxNestingDepth silently swallowed (INFO)

`findBox` at depth > 32 returns `(nil, ErrMaxNestingDepth)`. Callers check `if err == nil && inner != nil` — so the error is silently dropped. The test at conformance line 1115-1120 confirms this: "findBox returned nil error on >32 nesting (may have gracefully stopped)". This means overly-nested HEIF files fail silently (metadata not found) rather than surfacing the error. Safe (no crash), but suboptimal for diagnostics. Not filed as new finding since it matches existing INFO-level behavior.

## Tools and coverage

- `go test -v -count=1 ./format/heif/...`: ALL PASS (100 tests)
- `go test -race ./format/heif/...`: ALL PASS
- `go test -fuzz=FuzzHEIFExtract -fuzztime=20s`: 0 crashers (248K execs)
- `go test -fuzz=FuzzHEIFInject -fuzztime=20s`: 1 crasher found (HEIF-INJECT-01 root cause, cleaned up)
- Scratch tests: all deleted; git status format/heif/ clean
