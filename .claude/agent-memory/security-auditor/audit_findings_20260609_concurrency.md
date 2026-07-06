---
name: audit-findings-20260609-concurrency
description: Cross-cutting concurrency/DoS/fuzz audit findings from 2026-06-09
metadata:
  type: project
---

# Audit 2026-06-09 — Concurrency, DoS, Fuzz Robustness

**Date:** 2026-06-09
**Scope:** Full module cross-cutting: concurrency, resource exhaustion, fuzz robustness

## Race Signal

`go test -race ./...` — PASSED (all cached, no new failures). Scratch concurrent test confirmed two categories of data race on `*Metadata`:
1. Check-then-act race in `ensureEXIF/ensureIPTC/ensureXMP` (pointer nil check -> assign) - multiple goroutines can race on the nil check and both allocate/assign.
2. Concurrent map write race in `xmp.(*XMP).putProp` via concurrent `Set*` calls mutating `Properties` map simultaneously.

## Fuzz Signal

19 fuzz targets run, 8-10s each. One crasher found: **FuzzHEIFInject** found `panic: slice bounds out of range [12:8]` in `buildInjectComponents` at `heif.go:188` after ~1s. Input: `[]byte("\x00\x00\x00\x00meta")` (8 bytes).

All other targets: PASS with 0 crashers.

## Findings

### HEIF-INJECT-01 — CRITICAL
- **Location:** `format/heif/heif.go:188` (`buildInjectComponents`)
- **Input:** `[]byte("\x00\x00\x00\x00meta")` — 8 bytes
- **Root cause:** `findMetaBoxAbs` returns `metaContentOff = absStart + 8 + 4 = 12` even when `data` is only 8 bytes. The guard `metaAbsEnd > len(data)` (8 > 8 = false) does NOT catch the case where `metaContentOff > len(data)`. Then `data[metaAbsStart+8 : metaContentOff]` = `data[8:12]` panics.
- **Fix:** In `Inject`, after `findMetaBoxAbs`, add `|| metaContentOff > len(data)` to the pass-through guard. Or validate inside `buildInjectComponents` before the slice.
- **Fuzz seed:** `format/heif/testdata/fuzz/FuzzHEIFInject/360cc77903c2bfd4` (deleted as instructed; reproducer is `[]byte("\x00\x00\x00\x00meta")`).

### RACE-01 — HIGH
- **Location:** `metadata.go:694-721` (`ensureEXIF`, `ensureIPTC`, `ensureXMP`), `xmp/xmp.go:342-345` (`putProp`)
- **Race type 1:** `ensureEXIF` reads `m.EXIF` (line 694) and writes `m.EXIF` (line 697) without synchronisation. Multiple goroutines calling `Set*` concurrently on the same `*Metadata` can both observe nil, both allocate, and race on the write.
- **Race type 2:** `xmp.(*XMP).putProp` writes `x.Properties[ns][local]` (map writes) with no mutex; concurrent `Set*` calls from different goroutines produce a map concurrent write — Go runtime fatal if triggered in production.
- **No concurrency contract documented** in `doc.go` or `metadata.go`.
- **Fix:** Either (a) document `*Metadata` as not safe for concurrent use (matches known issue #128), or (b) add a `sync.Mutex` to `Metadata` and lock it in all `Set*` and accessor methods.

### OOM-01 — MEDIUM (CONFIRMS #140)
- **Location:** `format/tiff/tiff.go:23` and `tiff.go:104`; `format/heif/heif.go:79` and `heif.go:245`; `format/raw/orf/orf.go:42,83`; `format/raw/rw2/rw2.go:25,69`; `format/raw/cr3/cr3.go:73,414`
- **All are uncapped `io.ReadAll(r)`** with no `io.LimitReader` guard.
- **Impact:** An adversary feeding a multi-gigabyte stream causes OOM in the parsing process.
- **Status:** Confirms known issue #140. Still open as of this audit.

## Cap Inventory (what IS bounded)
- PNG zlib decompression: capped at 64 MiB (`maxZlibDecompressSize`)
- HEIF item payload: capped at 256 MiB (`maxItemPayloadSize`)
- HEIF iloc extents per item: capped at 1024 (`maxIlocExtentsPerItem`)
- EXIF IFD entry prealloc: capped at 1024 (`maxIFDEntryPrealloc`)
- BigTIFF IFD entry count: capped at 65535 (`bigTIFFMaxEntries`)
- HEIF findBox recursion: capped at depth 32
- IFD chain traversal: cycle detection via `visitedPool` map (no count cap)

**Why:** concurrency safety and the HEIF-INJECT-01 panic are the new HIGH/CRIT items. io.ReadAll OOM is known #140 (MED, deferred).
