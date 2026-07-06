---
name: audit-findings-20260609-exif
description: 2026-06-09 exif/ package deep reliability + spec-conformance audit findings
metadata:
  type: project
---

## Audit 2026-06-09 — exif/ package

Scope: all *.go in exif/ package. Tools: go vet PASS, govulncheck PASS (no vulns), go test -race PASS, FuzzParseEXIF 30s 6.1M execs 0 crashers.

### FINDING-EXIF-A001 — R-05 Spec Violation: parseSingleIFD rejects entire IFD instead of partial recovery — MEDIUM

**Location**: exif/ifd.go:143-145 (`parseSingleIFD`)

**Finding**: When `pos+int(count)*12 > len(b)`, the whole IFD is rejected (`return nil, 0, false`). R-05 says "count×12 > remaining → read only entries that fit (partial IFD)". Confirmed: a buffer with count=5 but only 1 entry's worth of space returns an error, losing the valid entry.

**Status**: NEW — spec-conformance violation. NOT a crash risk (more conservative). Affects real-world files from Canon 40D and Kodak that write one too many entries per IFD.

### FINDING-EXIF-A002 — parsePentaxPENTAX lacks internal bounds check — LOW

**Location**: exif/makernote_parse.go:311-313 (`parsePentaxPENTAX`)

**Finding**: `parsePentaxPENTAX` reads `b[8]` and `b[9]` without a length guard. Direct call with `len(b)==9` panics with index-out-of-range. NOT reachable via public `Parse()` because `parsePentaxMakerNote` dispatches only when `len(b)>=14`. Defense-in-depth issue only.

**Reproducer**: `parsePentaxPENTAX([]byte("PENTAX \x00I"))` — panics with `index out of range [9] with length 9`.

### FINDING-EXIF-A003 — ifd0ByteOrder() nil byteOrder panic in Set* methods — LOW

**Location**: exif/exif.go:616-622 (`ifd0ByteOrder`)

**Finding**: If a caller manually constructs `IFD0.Entries[0]` without setting `byteOrder`, all Set* methods panic. Not reachable via untrusted parse bytes (parseIFDEntry always sets byteOrder). API-misuse risk.

### CHECKING STATUS
- R-01 IFD cycle detection: CLEAN (iterative + visited set)
- R-06 count×typeSize uint64 overflow: CLEAN (uint64 arithmetic in parseIFDEntry)
- BigTIFF DoS count cap: CLEAN (bigTIFFMaxEntries=65535 + maxBigTIFFCount)
- GPS boundary values (±90/±180, NaN, Inf): CLEAN (validWGS84Coords guard)
- TypeUTF8 (EXIF 3.0) decoding: CLEAN (String() handles TypeUTF8 same as TypeASCII)
- Thumbnail OOB: CLEAN (extractJPEGThumbnail uses uint64 bounds)
- Write-path curOff overflow: SAFE (ifdTotalSize saturates at MaxUint32)
- parsePentaxAOC with short buffer: SAFE (traverse guards internally)
- MakerNote cycle: SAFE (traverse uses visited-set)
- EXIF 3.0 unknown tags with known types: CLEAN (parsed normally)
- FuzzParseEXIF 30s: 0 crashers

**Why:** All confirmed panics are either defense-in-depth (not public-API reachable) or pure spec-conformance issues.

**How to apply:** EXIF-A001 is the only finding worth fixing. parseSingleIFD should be changed to read entries that fit rather than reject the whole IFD.
