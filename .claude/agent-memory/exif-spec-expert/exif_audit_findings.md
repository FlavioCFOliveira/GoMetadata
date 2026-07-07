---
name: exif-audit-findings
description: EXIF/TIFF conformance & correctness state as of 2026-07-06 (round-4/5 whole-team production-readiness audit) — all prior known bugs fixed; one documented non-corrupting gap (BigTIFF write unsupported)
metadata:
  type: project
---

## Status as of 2026-07-06 (HEAD ~5e76f72, verified against 0d03bf5/6a0bbc6)

Superscedes the 2026-06-04 findings below — every item from that audit has since been
fixed or resolved by dead-code removal. Re-verified by reading current source, not by
re-running the 2026-06-04 notes blindly.

**Checklist coverage** — `docs/conformance/exif-tiff.md` defines 57 rules (S-01..S-33
structural, V-01..V-11 value-level, R-01..R-13 robustness). All 57 have a corresponding
named Go sub-test in `exif/conformance_structural_test.go`, `exif/conformance_value_test.go`,
`exif/conformance_robustness_test.go` (verified via grep, one-by-one). Only 2 `t.Skip`
in that battery, both corpus-directory-absent guards (allowed category per
docs/TESTING.md §2.1). `format/tiff/conformance_test.go` has a separate 51-subtest
container-layer battery (TIFF+BigTIFF structural/type/robustness/write-correctness),
per go-performance-architect's `project_tiff_conformance_battery.md` — all passing, 0
violations found at the time it was written (task #156).

**Byte-level round-trip evidence** (my domain: unmodified-EXIF preservation):
- `roundtrip_test.go::TestRoundTripPreservesExistingEXIF` — when only IPTC is touched
  and `m.EXIF` stays nil, `RawEXIF()` bytes are `bytes.Equal` before/after (exact
  pass-through, not just "parseable").
- `format/tiff/realfile_test.go::TestInjectWithEXIFRealFile_CrampsTIF` — real libtiff
  fixture (cramps.tif, 800x607 stripped grayscale): after SetCopyright + InjectWithEXIF,
  all image-block (strip) bytes are verified byte-identical at their new offsets
  (`verifyImageBlocksIdentical`); `TestInjectWithEXIFRealFile_PassThrough` — nil-modify
  produces `bytes.Equal(original, output)` exactly.
- security-auditor's 2026-07-06 R5 clearance (`audit_findings_20260706_r5_gmw1_jpeg262_clearance.md`):
  142 real RAW corpus files (DNG/ARW/NEF/ORF/RW2/CR2/CR3) round-tripped Read→Write at
  both HEAD and pre-GM-W1-fix commit via git worktree — byte-identical result:
  127 ok / 3 write-errors (same 3 files, same error text, pre-existing truncated
  StripByteCounts fixtures, unrelated to any EXIF logic).

## Prior findings (2026-06-04) — ALL NOW RESOLVED, re-verified 2026-07-06

- **C1** (nil-pointer `e.IFD0.Next` when `e.IFD0==nil`) — FIXED. `exif/write.go:151`
  now guards `if e.IFD0 != nil && e.IFD0.Next != nil`.
- **H1** (`Rational()` silently accepting `TypeSRational`, misreading sign bit) — FIXED.
  `exif/ifd.go` `Rational()` now has `if e.Type != TypeRational { return [2]uint32{} }`
  with a doc comment citing CIPA DC-008-2023 §4.6.3 and directing callers to
  `SRational()`. This is codified as checklist rule **V-02** and has a dedicated
  conformance sub-test.
- **H2** (dead `exif/makernote/` subtree missing Casio dispatch) — RESOLVED BY REMOVAL.
  The dead `exif/makernote/` package no longer exists in the tree; only the live
  `exif/makernote_parse.go` (which does have Casio support, 3 Make variants) remains.
  No more drift risk from a second, unmaintained dispatch table.
- **M1** (`upsertIFD0Entry` breaking binary-search invariant) — FIXED. Both
  `format/tiff/tiff.go:upsertIFD0Entry` and `format/tiff/relocate.go:upsertIFDEntryWithCount`
  now do a correct binary-search insertion that maintains sorted-by-tag order on every
  call (verified by reading the current implementation, not just the doc comment).
- **M2** (`TagIPTC` 0x83BB registered as `TypeLong`; real files use Undefined/Byte) —
  NOT A BUG, intentional documented deviation-handling. Codified as checklist item
  Section 5.6 ("accept all, return raw") and exercised by
  `format/tiff/conformance_test.go`'s IPTC 0x83BB sub-tests (TypeLong write,
  TypeByte/Undefined NOT trimmed = ROBUST-16).
- **M3** (makernote dispatch.go missing TrimSpace) — MOOT, the dead dispatch.go no
  longer exists.
- **M4** (MakerNote absolute-offset fragility on rewrite, Nikon Type3/Fujifilm) —
  resolved by design: `project_nef_write_ungated.md` (go-performance-architect memory)
  documents the Nikon Type-3 fix (dynamic TIFF-header-within-MakerNote scan +
  PreviewIFD offset patch after relocation). Checklist rule **R-11** explicitly permits
  "preserve-in-place, fully rebase, or document" — the codebase does the rebase.
- **m1** (`int(total)` overflow in dead subtree parsers) — MOOT, dead subtree removed.
- **m2** (`dmsToDecimal` returns 0.0 for zero denominators) — NOT A BUG, this is the
  spec-mandated defensive behavior (checklist **V-01**/Section 5.2: "zero-denominator
  rationals as unknown sentinel → no divide-by-zero"). Guard confirmed present at
  `exif/gps.go` (`dms[0][1]==0 || ...` check before any division).
- **m3** (no depth limit on IFD chain, only cycle detection) — FIXED. `exif/ifd.go`
  `maxTraverseChainIFDs = 512` bounds the IFD chain walk (this is EXIF-IFDCHAIN-01,
  a round-3 security-audit fix, CWE-834). GM-W1 (task #261, format/tiff write path)
  independently caps aggregate image-block/SubIFD allocation on the WRITE side with
  `maxAggregateImageBlocks = 262144` — see go-performance-architect's
  `project_gmw1_imageblock_budget.md`.

## BigTIFF write gap — CLOSED 2026-07-06 (was the sole caveat; re-verified same day)

**Superseded.** As of HEAD `3de8d2f` (commits `aa24232` task #264, `ef9041e` task
#270, `0ebf5d4` task #271), BigTIFF write is fully implemented end to end and the
prior GO-WITH-CAVEATS verdict is upgraded to **CLEAN GO**. Verified by direct
reading of `exif/write.go`/`exif/ifd.go` (`serialiseBigTIFF`, `writeIFDBigTIFF`,
`ifdTotalSizeBigTIFF`, `typeSizeBigTIFF`), `format/tiff/relocate.go` +
`relocate_bigtiff.go` (64-bit-aware copy-and-relocate), the root `write.go` gate
removal, and `docs/conformance/exif-tiff.md` §2.7/§2.8 (rules S-34..S-43, V-12..V-15,
R-14..R-19 — all present with spec citations).

Test-to-rule traceability confirmed by exact function-name grep (not executed —
this agent has no Bash/shell tool in this environment, only Read/Grep/Glob/Edit;
static/logical verification only, go-performance-architect or CI must be the
final execution gate):
- exif package: `TestConformance_S34..S39_*` (6), `TestConformance_V12..V14_*` (3),
  `TestConformance_R14..R17_*` (4) — all 13 exif-side rule IDs have exactly one
  dedicated named sub-test, 1:1.
- format/tiff package: `TestConformance_S40..S43_*` (S-42/S-43 each have a
  real-fixture + a synthetic variant), `TestConformance_R18_*` (curated + corpus),
  `TestConformance_R19_*`.
- Minor traceability nit (not a functional/corruption caveat): **V-15** (uint64
  arithmetic throughout `format/tiff`'s relocation offset math) has no test
  literally named after it — the invariant is enforced by the Go type system
  (`imageBlock.srcOffset/newOffset`, `subIFDInfo.srcOffset/newOffset` are `uint64`,
  confirmed by reading the struct defs at `format/tiff/relocate.go:234-282`) and is
  exercised indirectly by S-41/S-42/R-18's real-LONG8-value round trips. Worth
  flagging if asked to audit test-naming hygiene, but does not affect correctness.

Key design facts worth remembering for future BigTIFF questions:
- **S-39 alignment decision**: BigTIFF write uses word (2-byte) alignment for
  OOL value areas, NOT the BigTIFF design doc's literal 8-byte-alignment text —
  deliberately matches libtiff `tif_dirwrite.c`'s actual behavior and this
  project's own tiffcp-generated fixtures. Directly tested
  (`TestConformance_S39_bigtiff_alignment_encode` forces an odd-length ASCII
  value then asserts the next OOL entry's offset is even).
- Sub-IFD/thumbnail pointer tags (0x8769/0x8825/0xA005/0x0201/0x0202) stay
  `TypeLong` even in BigTIFF (never promoted to LONG8/IFD8) — matches EXIF
  §4.6.3/§4.5.5 and the libtiff/tiffcp convention. `ErrBigTIFFPointerOverflow`
  fires instead of truncating if a target offset would exceed 32 bits.
- `ErrBigTIFFEncodeSizeExceeded` + test-overridable `maxBigTIFFEncodeSize` (4 GiB
  default) replace classic TIFF's implicit `MaxUint32`-saturation ceiling, since
  BigTIFF's 64-bit fields have no natural overflow point of their own.
- `format/tiff`'s `assignNewOffsets` still saturates at `math.MaxUint32` even for
  BigTIFF sources — deliberate and harmless, NOT a real limitation: this project's
  own pre-existing, independently-security-audited `maxFileSize` = 256 MiB
  aggregate-read cap (applied on every `io.ReadAll` across every format package,
  predates this task) means no file this library will ever actually process can
  approach the 4 GiB `MaxUint32` threshold regardless of container. This is
  explicitly disclosed in V-15's own doc text in `docs/conformance/exif-tiff.md`,
  not a hidden gap.
- `rebaseGenericMakerNote` explicitly no-ops (`if e.BigTIFF { return }`) for
  BigTIFF sources — a documented, tested (`TestConformance_R19_*`) fail-safe
  deferral, not a corruption risk, because no known real-world file combines a
  Sony-plain-IFD/Olympus-OLYMP MakerNote with a genuinely BigTIFF container (both
  maker conventions predate BigTIFF). Same category as the pre-existing classic-
  TIFF R-11 "preserve-in-place, fully rebase, or document" allowance, extended to
  BigTIFF. Full BigTIFF-aware outer-container MakerNote rebasing remains a tracked
  (non-blocking) follow-up.
- `exif.ErrBigTIFFEncodeNotSupported` is kept, `// Deprecated:`-tagged, for API
  compatibility; the stale guard test (`TestEncodeBigTIFFSourceReturnsError`) was
  properly retired and replaced by `TestEncodeBigTIFFSourceSucceeds` — not left as
  a surprise regression, confirming the go-performance-architect followed the
  test-migration instruction from `bigtiff_write_spec.md` exactly.
- Public API proof: `bigtiff_write_e2e_test.go::TestWriteBigTIFFEndToEnd` drives
  real `testdata/fixtures/BigTIFF_{LE,BE}.tif` (tiffcp -8 fixtures) through
  `Read → SetCopyright → Write → Read`, asserting magic preservation (no silent
  downgrade to 0x002A), unmodified-field byte-fidelity, and byte-for-byte strip
  image-data preservation despite the strip's absolute offset changing.

**How to apply:** The EXIF/TIFF/BigTIFF dimension of GoMetadata is CLEAN GO as of
2026-07-06 (HEAD 3de8d2f). If a future audit is requested, re-verify HEAD has not
regressed (check `exif.ErrBigTIFFEncodeNotSupported` is not being returned again,
check the root `write.go` gate has not been reintroduced) rather than assuming this
finding is still current — it is a snapshot, not a permanent guarantee.
