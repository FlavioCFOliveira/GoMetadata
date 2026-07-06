---
name: audit-findings-20260609-tiff-cr2-cr3-dng
description: 2026-06-09 audit findings for format/tiff/, format/raw/cr2/, format/raw/cr3/, format/raw/dng/ — 3 findings (1 MEDIUM, 1 LOW, 1 INFO)
metadata:
  type: project
---

Audit date: 2026-06-09
Scope: format/tiff/, format/raw/cr2/, format/raw/cr3/, format/raw/dng/
Auditor: security-auditor agent

## Summary

3 findings: 1 MEDIUM confirmed (CR3 silent EXIF data loss), 1 LOW theoretical (uint32 overflow in
imageStart calculation in two locations), 1 INFO (missing explicit depth guard in CR3 recursive
box walker, bounded by slice shrinking so not exploitable).

## FINDING 1 — MEDIUM CONFIRMED
Title: CR3 Inject silently discards rawEXIF when UUID box has no CMT1 sub-box
Location: format/raw/cr3/cr3.go:173 rebuildUUIDContent
Class: Silent data loss / spec violation
Scratch test: TestCR3InjectSilentDataLossWhenNoCMT1 (written, run, DELETED)
Observed: "VULNERABILITY CONFIRMED: Inject with rawEXIF != nil silently succeeded but CMT1 was NOT written"
Remediation: When rawEXIF != nil and no CMT1 found in UUID box, insert a new CMT1 box.
Reference: containers.md §8(e) — CMT1 is the required EXIF IFD0 carrier in CR3
Status: NEW, not in existing 46 issues

## FINDING 2 — LOW
Title: Unchecked uint32 addition for imageStart in relocate.go and relocate_arw.go
Locations:
  - format/tiff/relocate.go:300 — imageStart := ifdEnd + subIFDsSize
  - format/tiff/relocate_arw.go:1071 — imageStart := ifdEnd + subIFDsSize + sr2ActualSize
Class: Integer overflow (uint32 wrap)
Impact: Overflow wraps imageStart to near-zero, assignNewOffsets assigns wrong newOffset to all
image blocks, written StripOffsets/TileOffsets corrupt. Only reachable for inputs where IFD
skeleton + SubIFD data approaches 4GB.
Remediation: Use overflow-safe addition (detect carry or upcast to uint64 then range-check).
Status: NEW

## FINDING 3 — INFO
Title: relocateInContainer has no explicit recursion depth guard
Location: format/raw/cr3/cr3.go:260 relocateInContainer
Class: Missing defensive depth guard (inconsistency with findBox which has depth > 32)
Impact: Not exploitable — each recursive call operates on a strictly smaller sub-slice, so depth
is naturally bounded by log2(fileSize/8). No stack exhaustion possible.
Remediation: Add explicit depth > 32 guard for consistency with findBox.
Status: NEW

## CONFIRMED SAFE PATTERNS
- byteOrder bounds: all callers pre-check len >= 8; safe
- extractParallelOffsetBlocks allocation bomb: bounds check fires before make(); safe on 64-bit
- patchRawIFDOffsets large entryCount DoS: bounds check fires before inner loop; safe
- CR3 CMT1+CMT2 merge round-trip: merged buffer written as new CMT1, read path handles correctly
- SubIFD next-IFD pointer not patched: DNG doesn't use next-IFD within SubIFDs; correct
- BigTIFF DNG write downgrade: typeSize(TypeLong8=16)=0 → ErrUnsupportedOffsetType before corrupt
- rebaseAllIFDsAfterCR2Marker overflow (line 365): theoretical only, only +8 delta on files < 4GB
- relocateStco overflow: correctly checks relocated < 0 || relocated > math.MaxUint32

## TOOLING RESULTS
- govulncheck: PASS
- go vet: PASS
- go test -race ./format/tiff/... ./format/raw/cr2/... ./format/raw/cr3/... ./format/raw/dng/...: PASS
- Fuzz targets exercised: FuzzParseEXIF 30s 0 crashers, FuzzParseIPTC 30s 0 crashers,
  FuzzScanPacket 30s 0 crashers, FuzzInjectJPEG 30s 0 crashers, FuzzInjectPNG 30s 0 crashers
