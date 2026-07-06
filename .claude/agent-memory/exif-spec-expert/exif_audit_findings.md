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

## Current known gap (NOT a corruption risk, but a real scope gap)

**BigTIFF write is not implemented.** `exif.Encode` on an `EXIF` parsed from a BigTIFF
source (`e.BigTIFF == true`) returns `ErrBigTIFFEncodeNotSupported` and emits **zero
bytes** rather than silently downgrading to classic TIFF (which used to truncate all
64-bit offsets to 32 bits — audit finding #107, now gated/fixed). BigTIFF **read** is
fully supported (`project_bigtiff_read.md`, task #54): magic 0x002B, 16-byte header,
20-byte/64-bit IFD entries, LONG8/SLONG8/IFD8 types, all correctly parsed for both
classic-JPEG-with-EXIF and true BigTIFF-container files.

This is documented in `README.md` ("BigTIFF write is not yet supported
(`ErrWriteNotSupported`)") and gate-tested in `exif/bigtiff_encode_guard_test.go`
(`TestEncodeBigTIFFSourceReturnsError`). It is a genuine gap against the literal
"100% ... read AND write" mandate for one specific sub-format (BigTIFF is essentially
never produced by real cameras — it's a large-file/GIS use case), but it does NOT
violate the "never corrupt" guarantee: the library fails loudly and safely (explicit
sentinel error, no output) instead of producing a corrupt file. Classic TIFF (0x002A)
EXIF write — the format every real camera and virtually all RAW files use — is fully
implemented and covered by the round-trip evidence above.

**How to apply:** When asked to certify "100% EXIF/TIFF write compliance," always
name this BigTIFF-write gap explicitly. It is the correct basis for a
GO-WITH-CAVEATS verdict rather than an unqualified GO, but does not by itself
justify a NO-GO given it is documented, safely gated, and out of scope for
virtually all real-world camera-originated files.
