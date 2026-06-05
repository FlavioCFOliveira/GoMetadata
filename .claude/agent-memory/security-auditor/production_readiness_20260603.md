---
name: production-readiness-20260603
description: Production-readiness security verdict for GoMetadata at HEAD after Sprint 8 — GO-WITH-CONDITIONS (LOW only); all MEDIUM+ closed; fuzz sweep 27 targets 0 crashers; stdlib CVEs informational.
metadata:
  type: project
---

# Production-Readiness Audit — 2026-06-03

**Verdict: GO-WITH-CONDITIONS** (all conditions are LOW or INFO severity)

## Tooling Results (live run, current HEAD)

- `go test -race -count=1 ./...`: PASS — all 35 packages (cached bypassed)
- `go vet ./...`: PASS — 0 findings
- `govulncheck ./...`: 0 symbols affected; 3 stdlib CVEs in go1.26.3 (net/textproto, mime, crypto/x509) — GoMetadata calls NONE of these packages; informational only
- `golangci-lint`: PASS (from Sprint 8 memory)
- No `recover()` anywhere in production code (test-only uses confirmed)

## Fuzz Sweep (all 27 targets, current HEAD, 20-30s each)

| Target | Execs | PASS |
|---|---|---|
| FuzzRead | 7.1M | yes |
| FuzzParseEXIF | 6.3M | yes |
| FuzzParseXMP | 11.2M | yes |
| FuzzParseIPTC | 9.1M | yes |
| FuzzJPEGExtract | 12.3M | yes |
| FuzzHEIFExtract | 284K (I/O bound) | yes |
| FuzzTIFFExtract | 539K (I/O bound) | yes |
| FuzzWebPExtract | 13.2M | yes |
| FuzzPNGExtract | 1.8M (I/O bound) | yes |
| FuzzCR3Extract | 357K (I/O bound) | yes |
| FuzzARWExtract | 574K (I/O bound) | yes |
| FuzzNEFExtract | 404K (I/O bound) | yes |
| FuzzDNGExtract | 514K (I/O bound) | yes |
| FuzzORFExtract | 2.3M | yes |
| FuzzRW2Extract | 7.8M | yes |
| FuzzCR2Extract | 588K (I/O bound) | yes |
| FuzzNikonParse | 13.8M | yes |
| FuzzCanonParse | 809K (I/O bound) | yes |
| FuzzSonyParse | 2.3M (wakeup timeout on 20s; PASS at 30s) | yes |
| FuzzFujifilmParse | 12.0M | yes |
| FuzzOlympusParse | 7.6M | yes |
| FuzzPanasonicParser | 13.1M | yes |
| FuzzSigmaParser | 9.5M | yes |
| FuzzSamsungParser | 1.1M | yes |
| FuzzDJIParser | 1.9M | yes |
| FuzzLeicaParser | 1.1M | yes |
| FuzzPentaxParse | 8.4M | yes |

Zero crashers across all 27 targets.

## Open Conditions (all LOW or INFO)

### CONDITION-01 — LOW: Uncapped io.ReadAll in TIFF/RAW/HEIF slow-path
- Locations: tiff.go:22, orf.go:26, rw2.go:25, cr3.go:72, heif.go:65
- A crafted 4 GB TIFF/ORF/RW2/CR3/HEIF file causes 4 GB allocation.
- Amplification ratio: 1:1 (no multiplier). Process OOM is the sole guard.
- Pre-existing architecture decision. WebP Inject (webp.go:144) adds same for write path.
- Recommendation: expose a `WithMaxInputBytes(n int64)` ReadOption applying `io.LimitReader` at the container level. Not blocking for initial production deployment.

### CONDITION-02 — INFO: stdlib go1.26.3 CVEs (GO-2026-5037/5038/5039)
- GoMetadata does not import net/textproto, mime, or crypto/x509. Not callable.
- Upgrade to go1.26.4 when available for completeness.

### CONDITION-03 — INFO: No configurable max-input-size option in public API
- Operators deploying in a service context cannot currently bound peak RSS per-request.
- Existing per-segment caps (IPTC 256 MiB, XMP 16 MiB, PNG/WebP chunks 256 MiB) provide secondary defence.

## Residual Finding Inventory — ALL HISTORICAL FINDINGS CLOSED

FINDING-001 through FINDING-009 (from Rounds 1-2): CLOSED (verified in code).

## Memory / DoS Model (confirmed current)
See [[sprint8-holistic-clearance]] — model unchanged post-Sprint 8. Dominant allocation for JPEG ≈ 336 MiB worst case (requires 256 MiB+ input). For TIFF/RAW: 1:1 amplification, no multiplier, not bounded by library alone.

## Write Path
- WriteFile: same-dir `os.CreateTemp` → `os.Rename` — atomic. Verified write.go:122-156.
- TIFF-based writes: gated behind `ErrWriteNotSupported` in `isTIFFBased()` — no corruption possible.
- All error paths close temp file before returning; `renamed` flag prevents double-cleanup. Correct.

## Concurrency Contract
- No goroutines spawned in production code.
- `*Metadata` concurrency: caller is responsible for serialising concurrent access (documented).
- All `sync.Pool` patterns use typed pools with correct Put guards (iobuf.Put discards oversized buffers). Race detector: clean.
