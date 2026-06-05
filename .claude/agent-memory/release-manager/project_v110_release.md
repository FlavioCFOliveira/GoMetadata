---
name: v1.1.0-release-record
description: Release record for GoMetadata v1.1.0 (2026-06-03) — version decision, gate results, notable findings
type: project
---

## Release: v1.1.0 — 2026-06-03

**Bump rationale:** MINOR. Two new exported error sentinels (`tiff.ErrUnsupportedMagic`, `xmp.ErrDocumentTooLarge`) added to the public API. Backwards-compatible additions per SemVer 2.0.0.

**Commit:** 98b1635affe38eb37109b49c80d27ae32a2d3d6a (amended after CHANGELOG factual-error corrections)
**Previous commit (before amendment):** ac063454d3856d31429096718f1873d2c57c4d19
**Tag:** v1.1.0 (annotated, recreated to point at amended commit)
**Previous:** v1.0.4 (402a067)

## Gate Results

| Gate | Result |
|---|---|
| go build ./... | PASS |
| go vet ./... | PASS |
| golangci-lint run ./... | 0 issues |
| staticcheck ./... | 0 issues |
| govulncheck ./... | 0 vulnerabilities in called symbols |
| go test ./... | PASS (all 41 packages) |
| go test -race ./... | PASS (0 races) |
| Security clearance | CLEARED — prior auditor decision covers all executable code; 4 post-audit commits are documentation-only |

## go.mod change

`go mod tidy` promoted `golang.org/x/text` from `// indirect` to a direct dependency. This is correct and expected; included in the release commit.

## Notable Benchmark Regressions (all security-driven, accepted)

- `exif/BenchmarkIFDEntryString`: 5.59 ns -> 11.83 ns (+112%); 0 allocs -> 1 alloc (16 B). Bounds-checked string formatting added in Sprint 8 hardening.
- `format/png/BenchmarkPNGExtract`: 231 ns -> 326 ns (+41%). Per-chunk document-size cap for iTXt/tEXt XMP paths.
- `iptc/BenchmarkIPTCEncode`: 70.4 ns -> 91.8 ns (+30%). Receiver-copy to eliminate concurrent-write data race.
- `xmp/BenchmarkXMPEncodeFullPacket`: 969 ns -> 1127 ns (+16%); allocs 1 -> 4. `maxXMPDocumentBytes` cap overhead.
- `xmp/BenchmarkXMPEncode`: 672 ns -> 796 ns (+18%); allocs 1 -> 3. Same cause.

All regressions documented in benchmarks/BENCHMARKS.md with root-cause analysis.

**Why:** v1.1.0 security hardening (Sprint 8) added DoS caps and race fixes at the cost of measurable overhead on benchmarks that exercise those paths. This was intentional.
**How to apply:** When reviewing future benchmark regressions, consult this record to distinguish expected security-overhead patterns from true performance regressions.

## Push and GitHub Release (2026-06-03)

**Pushed main branch:**
- Github (git@github.com:FlavioCFOliveira/GoMetadata.git): 402a067..98b1635 — SUCCESS
- origin (git@wg32:/xumiga/img-metadata.git): 402a067..98b1635 — SUCCESS

**Pushed tag v1.1.0:**
- Github: new tag, refs/tags/v1.1.0 @ c0c8f06c667c073d5d74b70096f2ef83069a20d4 — SUCCESS
- origin: new tag, same SHA — SUCCESS

**Tag verified on both remotes** via `git ls-remote --tags`: SHA c0c8f06c confirmed on both.

**GitHub Release created:**
- URL: https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.1.0
- Title: v1.1.0, draft: false, prerelease: false, latest: true
- Notes: full CHANGELOG v1.1.0 section as published

## CHANGELOG Factual Corrections (post-release, pre-push)

The initial release commit contained five errors in the v1.1.0 CHANGELOG entry that were caught by the user before any push. CHANGELOG was amended and tag recreated locally.

| Item | Original (wrong) | Corrected (ground truth) |
|---|---|---|
| XMP cap | "configurable, default 50 MiB" | `const maxXMPDocumentBytes = 16 << 20` — 16 MiB, compile-time constant (`xmp/xmp.go:49`) |
| XMP cap check point | (not stated) | Post-normalisation, before the RDF scan; checked in `Parse` |
| ExtendedXMP GUID cap | "ceiling of 64 GUIDs … rejected with `ErrSegmentTooLarge`" | Cap is 4 distinct GUIDs (`maxExtendedXMPGUIDs = 4`), each capped at 16 MiB → 64 MiB aggregate. Excess GUIDs are dropped and result marked truncated — no error returned. `ErrSegmentTooLarge` is unrelated (65535-byte APP write limit) |
| `iptc.Encode` fix | "copies the dataset slice before sorting" | Appended 1:90 `CodedCharacterSet` marker to `Records[0]` on the receiver; fix emits it to the encoded output only via `needsUTF8Declaration` path; receiver is now pure/idempotent (no sorting, no slice copy) |
| FormatCapability | "referenced from SECURITY.md" | SECURITY.md does not mention FormatCapability; only the knowledge graph (`knowledge-model.md`) records it |

**How to apply:** Before writing any CHANGELOG entry that describes a cap value, error name, or mechanism, verify the exact constant and code path against `git show` or the source file. Never infer values from design-rationale comments without checking the actual constant declaration.
