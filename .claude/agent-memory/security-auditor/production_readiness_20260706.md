---
name: production-readiness-20260706
description: Production-readiness security verdict for GoMetadata at HEAD 6a0bbc6 — GO (production-ready). Fresh full-module audit (post-v1.2.0); 5 findings (1 CRIT, 1 HIGH, 2 MED, 1 LOW) all fixed+gated in commits 89dac1e & 6a0bbc6; the 7 post-v1.2.0 perf commits certified SOUND (0 regressions); fuzz sweep 20 targets 0 crashers.
metadata:
  type: project
---

# Production-Readiness Security Audit — 2026-07-06

**Verdict: GO — production-ready.**

All findings from this audit are fixed, gated, and committed. No open CRITICAL,
HIGH, or MEDIUM findings remain. The library is safe to deploy on untrusted image
input.

## Scope & method

Fresh full-module audit (post-v1.2.0), executed by 4 parallel specialists:
- 1 × security-researcher: deep-dive regression hunt on the 7 performance commits
  (#198–#203, #240) that landed **after** the v1.2.0 security clearance and were
  therefore unaudited.
- 3 × security-auditor: full re-sweep of (a) `exif/`+`iptc/`, (b) `format/` containers
  + `internal/`, (c) `xmp/` + root dispatch/API + concurrency model.
- Extended fuzzing (~60 s/target) + adversarial verification of every candidate finding.

Prior audit (2026-06-09, [[reliability-2026-06-09-pass2]]): all 67 findings confirmed
fixed & shipped in v1.2.0. Spot-checked closed and confirmed no regressions.

## Findings (5 new) — ALL FIXED

| ID | Sev | Location | Root cause | Fix commit |
|---|---|---|---|---|
| HEIF-ILOC-OFFBYONE-01 | CRITICAL | `format/heif/heif.go` parseIloc/parseIlocFull | `len<5` guard then read `ilocData[5]` → unrecovered panic on 5-byte iloc body (min-legal iloc box). Fuzzer-found. Twin bug in the Inject write path. | 89dac1e |
| XMPCONC-01 | HIGH | `xmp/xmp.go` putProp (via exported `m.XMP`/`m.EXIF`) | Metadata mutex (#128) guards `Metadata.Set*` only; direct concurrent sub-struct mutation → `fatal error: concurrent map writes` (uncatchable DoS, CWE-362). | 6a0bbc6 |
| DETECT-SHORTREAD-01 | MEDIUM | `format/detect.go` Detect | Single `r.Read` instead of `io.ReadFull`; chunking readers misdetect valid files as Unknown (CWE-20 soft DoS). | 6a0bbc6 |
| EXIF-BO-001 | MEDIUM | `exif/exif.go` ifd0ByteOrder | Inferred byte order from `Entries[0].bigEndian` (consequence of #199); empty-IFD0 big-endian file → LE fallback → numeric `Set*` corrupt output (CWE-198). | 6a0bbc6 |
| PERF-201-LOW | LOW | `exif/write.go`, `exif/ifd.go` | Non-comma-ok `order.(binary.AppendByteOrder)` (#201) panics on a custom `binary.ByteOrder` (CWE-704, API-misuse only, not attacker-reachable). | 6a0bbc6 |

Each fix ships a load-bearing regression test (verified to fail without the fix); the
HEIF fix also commits fuzz seeds for `FuzzHEIFExtract` and `FuzzHEIFInject`.

## Post-v1.2.0 performance commits — CERTIFIED SOUND

The 7 commits (#198 lazy sub-IFD arena, #199 byteOrder 1-byte flag, #200 deferred
warnings, #201 stack-array escape elimination, #202 zero-alloc `string([]byte)`
MakerNote dispatch, #203 magic-byte pool, #240 filterEntries pool) introduced **0
memory-safety / data-race / correctness regressions**. Every hypothesis
(pool contamination, aliasing, use-after-Put, arena pointer invalidation, unsafe
key mutation, byteOrder zero-value confusion) was disproven with static reasoning +
concrete `-race` probes (incl. 64-goroutine concurrent Detect and 32-goroutine
concurrent arena parse) + fuzzing. Notes:
- #199's zero-value bool flag *fixes* the prior LOW EXIF-A003 (nil-interface panic).
- EXIF-BO-001 (empty-IFD0 LE default) is behaviour-preserved from before #199; not a
  #199 regression, but fixed here anyway.
- DETECT-SHORTREAD-01 predates and is unchanged by #203; fixed here.
- The only perf-introduced defect is PERF-201-LOW (fixed); the zero-alloc fast path is
  preserved byte-identical (benchstat 0 % B/op & allocs/op delta).

## Final verification (committed HEAD 6a0bbc6)

- `go build ./...` — clean
- `go vet ./...` — clean
- `golangci-lint run ./...` — 0 issues
- `govulncheck ./...` — **No vulnerabilities found** (Go 1.26.4; the 3 stdlib CVEs
  flagged informationally in the 2026-06-03 verdict are resolved on 1.26.4)
- `go test -race -count=1 ./...` — **PASS, all 21 packages, 0 races**
- Fuzz sweep — **20/20 targets, ~67M execs, 0 crashers**: FuzzHEIFExtract/Inject,
  FuzzParseEXIF, FuzzRead, FuzzParseXMP, FuzzParseIPTC (fixed paths, 45/30 s each) +
  FuzzJPEG/PNG/WebP(Extract+Inject)/TIFF(Extract+Inject)/RIFF/CR2/CR3/DNG/NEF/ARW/ORF/RW2
  (regression pass, 20 s each).
- No `recover()` anywhere in production code (re-confirmed) — the no-panic guarantee is
  structural.

## Threat-model status (SECURITY.md guarantees — all upheld)

- Safe on untrusted input: yes — the sole fuzzer-reachable panic (HEIF-ILOC) is fixed.
- No panic on malformed input: yes — HEIF-ILOC closed; fuzz sweep clean.
- No unbounded allocation: yes — `io.ReadAll` capped via `io.LimitReader` (#140);
  per-segment caps intact (IPTC 256 MiB, XMP 16 MiB, PNG/WebP chunks, HEIF item/iloc).
- No network / no FS side-effects: yes; WriteFile atomic (temp+fsync+rename, symlink &
  ownership preserved, #124/#125).
- Concurrency contract: `*Metadata` and its sub-structs (`EXIF`/`IPTC`/`XMP`) are
  documented not-safe-for-concurrent-mutation; `Metadata.Set*` serialise on an internal
  mutex. Now documented consistently across all three sub-structs (XMPCONC-01).

## Non-security observations (NOT blocking; not fixed here — need product decision)

- INFO: `iptc.Encode` extended-length path would truncate a `Dataset.Value` > 4 GiB —
  unreachable via Parse→Encode (256 MiB/1 MiB caps); only via direct caller construction.
- Cosmetic conformance: `xmp.NSgeo` (added 2026-06-10) is absent from `write.go`'s
  `prefixMap`, so round-tripping emits a generated `nsN:` prefix instead of `geo:`. No
  security impact (namespace correctness is by URI, not prefix spelling). Candidate for a
  future conformance task.

## Sign-off

GoMetadata at commit **6a0bbc6** is **cleared for production use**. Recommend keeping the
27→20 fuzz corpus running continuously in CI (P0 on any crasher) and re-running this
security gate before each release tag, per SECURITY.md.
