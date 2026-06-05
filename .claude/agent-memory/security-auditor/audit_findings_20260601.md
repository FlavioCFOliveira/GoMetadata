---
name: audit-findings-20260601
description: Security audit findings from 2026-06-01 re-audit for production readiness — all prior panics closed; new HIGH DoS found in UTF-16 XMP transcoding
metadata:
  type: project
---

# Re-Audit Date: 2026-06-01 (Round 2 — Post-Hardening)

## Prior Critical/High Findings — CONFIRMED CLOSED

### FINDING-001: IPTC Record OOB Array Index Panic — CLOSED
- Fix: `iptc/iptc.go:95` — guard `if record < 1 || int(record) >= len(i.Records)` present
- Fuzzer replay `a41cba1ae5d3aa76` PASSES

### FINDING-002: HEIF findBox Missing size >= headerLen Guard — CLOSED
- Fix: `parseHEIFBoxHeader` (heif.go:295) now returns ok=false when `size < headerLen`
- Fuzzer replay `9892fae67c7fda83` PASSES

### FINDING-003: CR3 findBox Missing size >= headerLen Guard — CLOSED
- Fix: `parseCR3BoxHeader` (cr3.go:52) now returns ok=false when `size < headerLen`
- Also covers findUUIDBox (cr3.go:366 requires `size >= headerLen+16`)
- Fuzzer replay `a16a01105570bae1` PASSES

### FINDING-004: PNG readChunk Unbounded Allocation — CLOSED
- Fix: `maxPNGChunkSize = 256MB` enforced in `readChunk` before any allocation
- Large chunk path uses `io.ReadAll(io.LimitReader(r, length))` — allocates proportional to actual bytes, not declared length

### FINDING-005: WebP readPaddedChunk Unbounded Allocation — CLOSED
- Fix: `maxWebPChunkSize = 256MB` + stream-availability seek check before `make([]byte, chunk.Size)`

### FINDING-006: WriteFile Cross-Device EXDEV — CLOSED
- Fix: `write.go:122` uses `os.CreateTemp(filepath.Dir(path), ...)` instead of `CreateTemp("", ...)`

### FINDING-007: JPEG FuzzJPEGExtract sub-8-byte rawEXIF — CLOSED
- Fix: `format/jpeg/jpeg.go:140` guard `if payload := data[len(identExif):]; len(payload) >= 8`

## NEW Findings (Round 2)

### FINDING-008: UTF-16 XMP Input Causes 2x Memory Amplification in xmp.Parse — HIGH
- **Location**: `xmp/encoding.go:77-93` (`toUTF8`, `encUTF16BE` and `encUTF16LE` branches)
- **Trigger**: Any format that allows a large rawXMP item + UTF-16 BOM:
  - HEIF: `maxItemPayloadSize = 256MB` → `transform.Bytes` allocates ~512MB
  - PNG: `maxPNGChunkSize = 256MB` → same amplification
- **Root cause**: `transform.Bytes(utf16decoder, b)` allocates a new buffer proportional to `len(b)` (1.5x–2x). No input size cap exists in `toUTF8` or `normaliseToUTF8`.
- **Contrast**: UTF-32 decode (`decodeUTF32`) correctly caps output at `maxUnescapedXMLBytes = 1MB`. UTF-16 has no equivalent cap.
- **Exploitability**: Probable. Attacker crafts a HEIF file with a 256MB XMP item beginning with `\xFE\xFF`. Library allocates 512MB on parse.
- **Fix direction**: Add a size cap before calling `toUTF8`. Reject or limit non-UTF-8 XMP inputs above, e.g., 16MB. Or add `io.LimitReader`-equivalent before `transform.Bytes`.
- **Status**: CLOSED — fixed in Sprint 8 pre-task-51. `maxXMPTranscodeBytes = 16 MiB` constant added at encoding.go:75; guard `if len(b) > maxXMPTranscodeBytes { return nil }` applied at encoding.go:97 (UTF-16BE) and encoding.go:109 (UTF-16LE). Verified 2026-06-03.

### FINDING-009: JPEG Extended XMP Has No Aggregate Size Cap — MEDIUM
- **Location**: `format/jpeg/jpeg.go:164` (`processAPP1Segment` extended XMP accumulation)
- **Trigger**: A crafted JPEG with many extended XMP APP1 segments (each up to 65458 bytes). ~4096 segments = ~268MB of accumulated chunk data from a ~268MB file.
- **Root cause**: `extended[guid] = append(extended[guid], extChunk{...})` has no total size limit. `mergeExtendedChunks` then allocates `totalLen = sum(chunk.data)` without cap.
- **Note**: `fullLen` field (4 bytes BE from the wire) is read but discarded (`_ = fullLen`) — it could be used to cap accumulation early.
- **Impact**: OOM/DoS; also the reassembled XMP is passed to `xmp.Parse` without size limit.
- **Exploitability**: Probable. Requires a large (268MB+) crafted JPEG file.
- **Fix direction**: Track aggregate extended XMP bytes; reject if total exceeds a cap (e.g., 16MB). Can also use `fullLen` from the first chunk as an early-out guard.
- **Status**: CLOSED — fixed in Sprint 8 pre-task-53. `maxExtendedXMPTotal = 16 MiB` constant added at jpeg.go:74; `appendExtendedXMPChunk` function at jpeg.go:147 implements per-GUID first-seen fullLen validation (line 170) and running accumulated+chunkSize check (line 179); both reject excess with extTruncated=true. Verified 2026-06-03.

## Confirmed-Safe Areas (Round 2)

- IPTC Parse: all panics closed, 15.9M fuzz execs clean
- HEIF findBox: size < headerLen guard in parseHEIFBoxHeader — clean
- CR3 findBox + findUUIDBox: size guards in parseCR3BoxHeader — clean
- PNG readChunk + readLargeChunk: 256MB cap + LimitReader — clean
- WebP readPaddedChunk: 256MB cap + seek-verify — clean
- WriteFile: same-dir CreateTemp — EXDEV fixed
- JPEG rawEXIF < 8 bytes: guarded — clean
- xmp/encoding.go decodeUTF32: output capped at 1MB via maxUnescapedXMLBytes — clean
- appendUTF8Rune: invalid codepoints (>0x10FFFF) produce invalid UTF-8 bytes, not panic — LOW/INFO
- xmp/rdf.go onEndElement: depth underflow guard `if p.depth <= 0 { return }` — clean
- decodeXMPWire: mainLen from unexported internal field only — not user-controllable
- TIFF/RAW write path: blocked by ErrWriteNotSupported gate before any allocation
- WriteFile temp cleanup: deferred cleanup with `renamed` flag — correct
- Strict() mode: does not expose exploitable state; parse failures return nil EXIF/XMP
- govulncheck: clean (no CVEs in golang.org/x/text v0.35.0)
- go test -race: all 25 packages PASS
- go vet: clean

## Sprint 8 Task #51 Audit — 2026-06-03 (xmp document-level cap, security_test.go, fuzz seeds)

**Verdict: CLEARED**

### Scope: xmp/xmp.go, xmp/errors.go, xmp/rdf.go, xmp/encoding.go, xmp/packet.go, xmp/security_test.go, xmp/testdata/fuzz/FuzzParseXMP/

### Cap architecture verified:

1. **Document cap location and order**: `Parse` (xmp.go:60) calls `normaliseToUTF8` FIRST (line 68), then checks `len(b) > maxXMPDocumentBytes` at line 75 BEFORE any call to `Scan` or `parseRDF`. Cap is applied to POST-transcode UTF-8 bytes. Correct placement.

2. **Pre-transcode vs post-transcode**: The cap is checked on the post-transcode byte slice. A UTF-16 input of exactly 16 MiB (at the transcode cap boundary) produces up to 24 MiB of UTF-8 after `transform.Bytes` (1.5× BMP amplification). This 24 MiB EXCEEDS the 16 MiB document cap → caught. No gap. The transcode cap (`maxXMPTranscodeBytes = 16 MiB`) acts as the first gate; the document cap (`maxXMPDocumentBytes = 16 MiB`) acts as the second gate on the output. Both work together correctly.

3. **Bypass via Scan**: `Scan` (packet.go:25) calls `normaliseToUTF8` but applies NO document cap — it returns raw bytes. However, `parseRDF` is unexported (`func parseRDF(b []byte, x *XMP) error`). An external caller cannot reach `parseRDF` without going through `Parse`. The only exported entry point to parsing is `Parse`. No bypass path exists.

4. **Depth cap correctness**: `parseStartTag` (rdf.go:626): `p.depth++` THEN `if p.depth > 100 { return ErrXMLNestingDepth }` THEN `p.nsDepth[p.depth] = ...`. At depth 101, the check fires and returns BEFORE the `nsDepth[101]` write. `nsDepth` is `[101]nsDepthEntry` (valid indices 0–100). No OOB write possible. `popNSScope` is guarded `if p.depth > 0 && p.depth <= 100` — further OOB protection. `onEndElement` depth underflow guard `if p.depth <= 0 { return }` prevents negative depth.

5. **Entity expansion cap**: `unescapeXML` checks `bld.Len() > maxUnescapedXMLBytes` after each byte written AND after each entity decoded. Both paths in the loop covered. The no-closing-semicolon path (`bld.Write(b[i:])`) is only reachable when `bld.Len()` is still within cap, and `b` is bounded by the document cap. No unbounded write.

6. **Boundary exactness in tests**: All three caps use strictly-greater-than (`>`), so exactly-at-cap passes. Tests `TestNestingDepth100Succeeds` (100 → passes), `TestNestingDepth101Fails` (101 → fails), `TestDocumentLevelCapAllowsAtBoundary` (exactly 16 MiB → passes), `TestDocumentLevelCapRejectsOversized` (16 MiB + 1 → fails). All match code. The unescapeXML cap tests correctly use `n = maxUnescapedXMLBytes + 1` entities to trigger the check (fires after writing byte #1,048,577 > 1,048,576).

7. **Real-world regression**: `TestDocumentLevelCapAllowsRealAdobePacket` parses `simpleXMP` (< 1 KiB) against the live cap — confirms no legitimate packet is rejected.

8. **Fuzz result (Task #51 audit)**: `FuzzParseXMP -fuzztime=60s` — 28,677,446 executions, 0 crashers, 24 new interesting inputs found. Corpus: 5 named seeds + 1 legacy file + 2 inline seeds.

9. **Full suite race**: `go test -race ./...` — all 35 packages PASS. `go vet ./xmp/... ` PASS. `govulncheck ./...` — 0 symbols affected.

**No issues found.**

## Sprint 8 Task #50 Audit — 2026-06-03 (FuzzRead + CI fuzz job + test battery)

### Audit scope: fuzz_test.go, root_task50_test.go, .github/workflows/ci.yml

**Verdict: CLEARED**

- `go test -race ./...` (NO skip flag): all 25 packages PASS — Examples run clean locally
- `-skip ^Example` in CI is justified: corpus images are gitignored; confirmed by `ci: skip Example tests` commit (e987672)
- `go test -fuzz=FuzzRead -fuzztime=60s .`: 16.4M execs, 0 crashers — no-panic contract holds
- `go test -fuzz=FuzzRead -fuzztime=45s -race .`: 509K execs (race overhead), 0 crashers — no-panic contract holds under race
- `go vet ./...`: clean
- `govulncheck ./...`: 0 symbols affected

**No `recover()` in production code.** The 4 `recover()` calls are in test files only (byteorder_test.go, iobuf_test.go) — correctly testing that their respective functions panic on programming errors. The no-panic contract for `Read` is satisfied by proper error returns, not by deferred recovery.

**FuzzRead drives the real public `Read()` path.** `fuzz_test.go` is in `package gometadata` (not _test), so `Read()` at line 118 is the exact same symbol as `read.go:48` — no narrowing.

**Unsupported-format error**: message is `gometadata: unsupported format (magic bytes: deadbeef...)` — no IFD/APP13/rdf/Photoshop leakage. Confirmed by live execution.

**`iptc.Encode` mutation concern — CLOSED (Task #52, 2026-06-03)**: Previously flagged as INFORMATIONAL in Task #50 audit. The `Encode` function no longer mutates `i.Records[0]` under any path. The 1:90 ESC%G declaration is now written directly to the output buffer (iptc.go:269) without touching the receiver. `TestConcurrentEncodeNonASCII` with `-race` (16 goroutines, non-ASCII caption) PASSES.

**CI fuzz YAML**: well-formed, bounded (-fuzztime=10s), runs `FuzzRead` at the root (`.`) and 5 sub-targets with `-race`. Non-zero exit from fuzz propagates as job failure. No issues.

**Seed corpus**: 7 files on disk cover empty, SOI+EOI, SOI-only, TIFF LE/BE magic, PNG sig, unknown magic — all structurally valid for the Go fuzzing harness format.

## Fuzz Coverage (Round 2, 25s each, 0 crashers)

FuzzParseIPTC: 15.9M execs | FuzzHEIFExtract: 50K execs (I/O bound) | FuzzCR3Extract: 14.5M
FuzzParseXMP: 12.3M | FuzzParseEXIF: 11.1M | FuzzJPEGExtract: 12.4M
FuzzPNGExtract: 2.1M | FuzzWebPExtract: 19.7M | FuzzTIFFExtract: 13.4M
FuzzNEFExtract: 8.7M | FuzzCanonParse: 1.7M | FuzzNikonParse: 16.9M
FuzzSonyParse: 810K | FuzzARWExtract: 14.6M | FuzzDNGExtract: 14.6M
FuzzORFExtract: 1.5M | FuzzRW2Extract: 2.4M | FuzzCR2Extract: 4.4M
FuzzOlympusParse: 6.8M | FuzzPanasonicParser: 9.0M | FuzzFujifilmParse: 15.7M
FuzzPentaxParse: 6.6M | FuzzDJIParser: 1.2M | FuzzLeicaParser: 1.1M
FuzzSigmaParser: 7.6M | FuzzSamsungParser: 744K
