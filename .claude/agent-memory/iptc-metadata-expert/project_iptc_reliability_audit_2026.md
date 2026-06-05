---
name: project-iptc-reliability-audit-2026
description: Reliability fragility audit of GoMetadata IPTC subsystem, 2026-06-04 — decode-cache race, SetKeywords UTF-8 flag gap, Pascal-string truncation
metadata:
  type: project
---

## Reliability Audit 2026-06-04

Full read-only audit of iptc/iptc.go, iptc/dataset.go, iptc/encoding.go, format/jpeg/jpeg.go with all IPTC tests.

**Why:** go-performance-architect requested IPTC-specific reliability fragility audit for the structured output pipeline.

### FINDING A (HIGH): Decode-cache data race on concurrent reads

`(*Dataset).stringValue()` in iptc/encoding.go:42-48 writes `d.decoded` and `d.decodedValue` fields without any synchronization. Two goroutines calling `Keywords()`, `Caption()`, `AllCreators()`, or `firstRecord2()` concurrently on the same `*IPTC` will race on those fields — detected by `go test -race`. The FINDING-002 fix for `Encode` only addressed the `Records[0]` write race; the accessor read-path cache-fill race is untouched. No test covers concurrent `Keywords()` calls.

### FINDING B (MEDIUM): SetKeywords does not update UTF-8 in-memory flag

`SetKeywords` (iptc/iptc.go:438-455) does not call `hasHighBytes` on added keywords or set `Records[0]`. `AddKeyword` does (iptc/iptc.go:427-430). The gap means: after calling `SetKeywords(["αβγ"])` on a fresh *IPTC, `isUTF8()` returns false. A subsequent `Keywords()` call will decode via ISO-8859-1, producing mojibake. This is only harmless at encode time because `needsUTF8Declaration()` scans all records.

### FINDING C (MEDIUM): decode-cache stale after UTF-8 flag upgrade via AddKeyword/AddCreator

If a caller first reads `Caption()` on a non-UTF-8-declared stream (cache fills with ISO-8859-1 decode), then calls `AddKeyword("αβγ")` (which sets the UTF-8 flag on Records[0]), then reads `Caption()` again — the old decoded value is returned unchanged because the Caption's decode cache was filled on first read and never invalidated by the UTF-8 flag upgrade. The value in `Dataset.Value` did not change but the intended interpretation did.

### FINDING D (MEDIUM): skipPascalString silent truncation drops all IRB blocks that follow a long Pascal-name

`skipPascalString` (jpeg.go:770-781) only guards the initial read of the length byte (`if pos >= len(b)`), then blindly advances `pos += nameLen` without checking whether the name bytes fit in the buffer. It returns `(pos, true)` even when `pos > len(b)`. The caller `parseIRBEntry` subsequently detects this via `if pos+4 > len(b)` and returns `(pos+1, false)`. In `parseIRB`, this matches the "structural failure" branch (newPos != pos), causing an immediate `break`. All 8BIM entries that follow a block with an overly-long Pascal name string are silently discarded. In a crafted or corrupted IRB stream where the 0x0404 IPTC block appears after such an entry, IPTC data is silently lost.

**How to apply:** Any future IPTC changes should reference these findings. The decode-cache race (FINDING A) is the most likely to surface under production concurrent loads.
