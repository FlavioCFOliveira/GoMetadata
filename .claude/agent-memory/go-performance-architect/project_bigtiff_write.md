---
name: project_bigtiff_write
description: BigTIFF write support in exif.Encode (task #264, commit aa24232) — dispatch, functions, guards, alignment decision
type: project
---

Task #264 (Clean-GO sprint) implemented native BigTIFF write support in
`exif.Encode`, closing the last EXIF/TIFF production-readiness caveat (audit
finding #107). Commit `aa24232`.

**Why**: `exif/write.go`'s `serialise` previously returned
`ErrBigTIFFEncodeNotSupported` unconditionally for `EXIF.BigTIFF == true`
sources — read was fully compliant but write was blocked. Scope for #264 was
`exif.Encode` (the EXIF-blob encoder) only; standalone BigTIFF *container*
write (image-block relocation in `format/tiff/relocate.go`, currently
uint32-only) is tracked separately as task #270 and was NOT touched.

**How to apply**: if asked about BigTIFF write status, container relocation,
or the design of the BigTIFF encode path, this is the authoritative summary —
verify function names still exist via grep before citing them, since this is
a snapshot as of 2026-07-06.

## Dispatch and new functions
- `serialise` (exif/write.go) builds `ifd0Entries`/`exifIFDEntries` via the
  SAME container-agnostic `buildIFD0Entries`/`buildExifIFDEntries` used by
  classic TIFF (zero changes there — sub-IFD pointer tags stay `TypeLong`
  regardless of container), then dispatches to `serialiseBigTIFF` when
  `e.BigTIFF`.
- New BigTIFF counterparts, each mirroring an existing classic function 1:1
  with widened constants (8-byte fields, 16-byte header, 20-byte entries,
  8-byte inline threshold vs 4):
  - `ifdTotalSizeBigTIFF`, `writeIFDBigTIFF` — exif/ifd.go (end of file)
  - `appendUint64Order`, `writeTIFFHeaderBigTIFF`, `computeIFDOffsetsBigTIFF`,
    `writeSubIFDsBigTIFF`, `serialiseBigTIFF` — exif/write.go
- `patchPointers` and `patchThumbnailEntries` are REUSED unchanged (they still
  write a 4-byte uint32 into a `[4]byte` placeholder); the BigTIFF path just
  range-checks the target uint64 offset against `math.MaxUint32` before
  calling them.

## #1 correctness rule (repeatedly emphasized in code comments)
Every BigTIFF size computation MUST call `typeSizeBigTIFF` (exif/type.go:60),
never `typeSize`. `typeSize` returns 0 for LONG8/SLONG8/IFD8 (16/17/18),
which would treat 8-byte BigTIFF-only values as size-0 → silently inline +
truncated. This was flagged so heavily in the exif-spec-expert's spec doc
(`.claude/agent-memory/exif-spec-expert/bigtiff_write_spec.md`) that every
new function's doc comment cites it explicitly.

## Alignment decision (already made by exif-spec-expert, not re-litigated)
Word (2-byte) alignment, NOT the BigTIFF design-doc's advisory 8-byte text.
Reason: libtiff's actual reference implementation (`tif_dirwrite.c`) only
enforces word alignment even for BigTIFF output, and that's what produced
this project's own committed fixtures (`exif/testdata/BigTIFF_{LE,BE}.tif`,
via `tiffcp -8`). This let `ifdTotalSizeBigTIFF`/`writeIFDBigTIFF` reuse the
exact, already-audited padding arithmetic from `ifdTotalSize`/`writeIFD`
verbatim, just with wider field widths.

## New sentinel errors (exif/errors.go)
- `ErrBigTIFFPointerOverflow` — sub-IFD pointer tags (ExifIFDPointer 0x8769,
  GPSIFDPointer 0x8825, InteropIFDPointer 0xA005) and thumbnail pointer tags
  (JPEGInterchangeFormat 0x0201, JPEGInterchangeFormatLength 0x0202) stay
  fixed EXIF LONG (4-byte) fields even in BigTIFF (EXIF §4.6.3/§4.5.5,
  matches libtiff/tiffcp convention and this package's own
  `readBigTIFFSubIFDOffset` reader). If Encode computes a target offset that
  doesn't fit in 32 bits, it returns this error instead of truncating.
  Guard is gated on the corresponding sub-IFD actually being non-nil (no
  false positives when e.g. `e.ExifIFD == nil` but IFD0 alone is huge).
- `ErrBigTIFFEncodeSizeExceeded` — aggregate encoded-size ceiling
  (`maxBigTIFFEncodeSize`, exif/write.go, default `4 << 30` = 4 GiB). Classic
  TIFF never needed this because its 32-bit offset fields make
  `math.MaxUint32` an implicit ceiling; BigTIFF's 64-bit fields have none, so
  a caller-constructed `IFDEntry` with a pathological `Count` could otherwise
  drive an unbounded `make([]byte, 0, N)` (CWE-400). Declared as a `var` (not
  `const`), mirroring the `maxFileSize`/`maxDatasetValueLen` test-overridable
  idiom — tests lower it via a `setMaxBigTIFFEncodeSizeForTest` helper +
  `t.Cleanup`, exactly copying the pattern in
  `iptc/dataset_value_too_large_test.go`.
- `ErrBigTIFFEncodeNotSupported` is kept for API compat, marked
  `// Deprecated:` (precedent: `format/raw/cr3.ErrWriteNotSupported`). No
  longer returned by Encode under normal conditions.

## Test files added/changed
- `exif/bigtiff_write_test.go` (new) — round-trip fixtures (LE/BE), corpus
  round-trip (scans `testdata/corpus/tiff/**` for any file Parse tags
  `BigTIFF==true` — found 20 real files including a previously-unnoticed
  `testdata/corpus/tiff/exiv2/issue_712_poc*.tif` set), sub-IFD tree
  round-trip, thumbnail round-trip, pointer-overflow guard (+ no-false-positive
  companion), size-ceiling guard, unknown-type round-trip, raw-byte S-38
  inspection, `BenchmarkEXIFEncode_BigTIFF`.
- `exif/bigtiff_encode_guard_test.go` — REWROTE
  `TestEncodeBigTIFFSourceReturnsError` → `TestEncodeBigTIFFSourceSucceeds`
  (old test pinned the pre-#264 refusal behavior). Provenance tests
  (`TestBigTIFFProvenance_FlagSet/_ClassicFalse`) kept unchanged.
- `exif/fuzz_test.go` — `FuzzParseEXIF` extended: after a successful Parse, it
  now also calls `Encode` and (if that succeeds) re-`Parse`s the output,
  asserting `BigTIFF` provenance never flips — crash/downgrade detection
  only, no deep tag-equality assertion (that's the job of the deterministic
  round-trip tests). Added the real `testdata/BigTIFF_LE.tif` fixture as a
  new seed. Ran 45s / ~7.5M execs locally with zero crashes.
- `docs/conformance/exif-tiff.md` + matching Go sub-tests: S-34..S-39 (structural,
  in `conformance_structural_test.go`), V-12..V-14 (value, in
  `conformance_value_test.go`), R-14..R-17 (robustness, in
  `conformance_robustness_test.go`, including a corpus-wide
  `TestConformance_R14_bigtiff_roundtrip_fidelity`).

## Benchmark evidence
`BenchmarkEXIFEncode_BigTIFF` (real 4744-byte fixture, 22 IFD0 entries):
~974 ns/op, 7749 B/op, 6 allocs/op — same order of magnitude as
`BenchmarkEXIFEncode_Camera` (classic path, ~1117 ns/op, 1618 B/op,
14 allocs/op). No pooling was added specifically for the BigTIFF path in this
task (it reuses `entrySlicePool` and `iobuf` already used by the classic
path); allocation count is naturally lower because BigTIFF's 8-byte inline
threshold keeps more values inline vs classic's 4-byte threshold.

## Lint iteration notes (see also [[feedback_lint_iteration_after_new_code]])
- `serialiseBigTIFF`/`writeSubIFDsBigTIFF` needed
  `//nolint:gocyclo,cyclop` (cyclomatic complexity 11 > 10 threshold);
  `cyclop` isn't in this project's enabled linter set but the existing
  convention (`traverseBigTIFF`, `Parse`, etc.) still lists it in the
  directive — golangci-lint's `nolintlint` does not flag a `cyclop` mention
  as "unused" when `cyclop` isn't part of the active linter set at all, so
  this is safe and matches house style.
- `modernize` is excluded entirely on `_test.go` files (`.golangci.yml`
  issues section), but `intrange` is NOT excluded for tests — converted
  three `for i := uint64(0); i < n; i++` test loops to `for i := range n`
  instead of adding nolint.
- `paralleltest`: a subtest that mutates `maxBigTIFFEncodeSize` cannot be
  `t.Parallel()`'d alongside its sibling; restructured into two separate
  top-level test funcs (one parallel, one not, with
  `//nolint:paralleltest` + explanation) rather than nesting — mirrors
  `iptc/dataset_value_too_large_test.go`'s `setMaxDatasetValueLenForTest`
  pattern exactly.
