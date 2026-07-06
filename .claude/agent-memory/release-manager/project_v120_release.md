---
name: v1.2.0 release record
description: Gate results, security clearance method, benchmark regression analysis, and key decisions for the v1.2.0 release (2026-06-10)
metadata:
  type: project
---

# v1.2.0 Release Record — 2026-06-10

## Version bump
MINOR bump: v1.1.0 → v1.2.0. 94 commits (74 fix/feat) since v1.1.0. New write support for 7 TIFF-based RAW formats, BigTIFF read, 17 conformance test suites. No breaking API changes.

## Gate results

| Gate | Result |
|---|---|
| `go build ./...` | PASS |
| `golangci-lint run` | PASS (0 issues) |
| `govulncheck ./...` | PASS (no vulnerabilities) |
| `go test -race ./...` | PASS (21 packages, 0 failures, 0 races) |
| `go mod tidy` | CLEAN (no changes to go.mod/go.sum) |

## Security clearance method

Security clearance was performed by reading the security-auditor's prior audit memory files (2026-06-09 audit: 7 finding files covering HEIF, XMP, EXIF, IPTC, containers, TIFF/CR2/CR3/DNG, and concurrency) and cross-referencing each finding against the remediation commits. All CRITICAL, HIGH, and MEDIUM findings were confirmed addressed:

- HEIF-INJECT-01/CONFIRMS-#106 (CRITICAL): fixed in commit 1140781
- RACE-01 (HIGH, mutex-guard): fixed in commit f3dc6fa
- OOM-01 (MEDIUM, LimitReader): fixed in commit a8982f7
- XMP-NS-URI-01/XMP-LOCAL-01 (HIGH): fixed in commit c6fe808
- XMP-RDFRESOURCE-01 (MEDIUM): fixed in commit c6fe808
- IPTC duplicate 1:00 header (LOW): fixed in commit 9712915
- CR3-SILENT-001/CR3-DEPTH-001: fixed in commit e184e24

Clearance date: 2026-06-10.

## Notable benchmark regressions (all root-caused, no block)

- `BenchmarkMakerNoteDispatch`: +184% — MakerNote OOL rebasing (#127); one-time per image load, absolute 280 ns
- `BenchmarkIPTCParse`/`BenchmarkIPTCEncode`: +89%/+77% — IIM ascending-order sort pass
- `BenchmarkReadFile`: +51% — LimitReader wrapper adds one allocation per io.ReadAll call
- `BenchmarkTIFFExtract`: +52% — SubIFD bounds + RW2 nextIFD rebasing
- RAW format extracts (ARW/CR2/DNG/NEF): +25–30% — defensive raw-slice copy (#139)
- `BenchmarkJPEGInject`: +45% — 8BIM sibling preservation (#134)
- `BenchmarkXMPEncode`: +29% / `BenchmarkXMPEncodeFullPacket`: +38% — NS-URI XML escape (#112/#113)

## Decisions

- Working tree had untracked agent-memory files at release time. These were correctly excluded from staging using explicit `git add` per file.
- `gh release create --notes-file <(...)` uses process substitution; macOS `head -n -1` (GNU syntax) fails. Worked around by writing notes to `/tmp/v120_notes.md` and editing the release after creation.
- README was already reconciled by commit a1755e5 — no README changes required for this release.
- No `.go` files were modified by the release-manager; security re-audit was not required.

## Artifacts

- Release commit: 1c3b7a6
- Tag: v1.2.0
- Remotes pushed: Github (github.com:FlavioCFOliveira/GoMetadata.git), origin (wg32:/xumiga/img-metadata.git)
- GitHub release: https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.2.0

**Why:** v1.2.0 is the first release with full write support for all 13 container formats, BigTIFF read, and a 17-suite conformance battery.
**How to apply:** Reference for next release cycle patterns, benchmark baseline, and security-clearance method.
