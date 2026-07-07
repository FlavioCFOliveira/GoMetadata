---
name: project_task271_bigtiff_public_api_gate
description: Removed the root-package isBigTIFFSource gate (task #271) — gometadata.Write now writes BigTIFF end-to-end; closes the follow-up flagged in project_task270_bigtiff_container_write
type: project
---

Task #271 (Clean-GO sprint) was the trivial, well-defined follow-up flagged
at the end of [[project_task270_bigtiff_container_write]]: with `exif.Encode`
BigTIFF-capable since task #264 ([[project_bigtiff_write]]) and
`format/tiff`'s copy-and-relocate serializer container-width-aware since
task #270, the root package's `write.go` still hardcoded a refusal gate that
predated both fixes. Commit `0ebf5d4`.

**Why this mattered**: `gometadata.Write`/`WriteFile` are the only entry
points most callers ever use. Even though `tiff.Inject`/
`tiff.InjectWithEXIF` (the `format/tiff` package API) fully supported
BigTIFF, and this was proven directly in task #270's own tests bypassing the
top-level gate, the public API itself still returned `ErrWriteNotSupported`
for every BigTIFF source. This was pure integration debt — no new logic was
needed, just wiring removal.

## What was removed
- `isBigTIFFSource(raw []byte, e *exif.EXIF) bool` (write.go) — the two-layer
  BigTIFF-detection helper (checks `e.BigTIFF` first, falls back to raw
  magic-byte inspection when `e` is nil). Confirmed via grep it had exactly
  one call site before removal.
- The `if isBigTIFFSource(...) { return ...ErrWriteNotSupported }`
  short-circuit at the top of `writeTIFF` (write.go). This was the ONLY
  BigTIFF-specific branch anywhere in the root package's write path — no
  other function (`writeTIFFCR2`, `writeTIFFARW`, `writeTIFFORF`,
  `writeTIFFRW2`, `writeTIFFNEF`, `writeTIFFDNG`) ever called it, because
  BigTIFF never occurs in practice for camera-produced RAW formats.
- The now-unused `"encoding/binary"` import from write.go (it had no other
  consumer in that file).

## What was kept unchanged
- `ErrWriteNotSupported` (errors.go) — kept as-is. Its doc comment already
  states it is "retained for future use when a new format is detected but
  its write path is not yet implemented," so it remains the correct sentinel
  for any future format gap; removing BigTIFF's use of it required no
  changes to the sentinel itself.
- `exif.ErrBigTIFFEncodeNotSupported` — already marked `// Deprecated:` by
  task #264; untouched here (out of scope, exif package).

## Test changes
- **Rewrote** `bigtiff_write_guard_test.go`: `TestBigTIFFWriteReturnsError`
  (which pinned the pre-#271 refusal behaviour, asserting
  `errors.Is(err, ErrWriteNotSupported)` and zero output bytes) →
  `TestBigTIFFWriteSucceeds` (asserts `Write` succeeds, output is non-empty,
  re-parses, and the BigTIFF magic `0x002B` is preserved — never silently
  downgraded to classic TIFF `0x002A`, which is the original audit finding
  #107 corruption). This exactly mirrors the `#264`-era rewrite pattern in
  `exif/bigtiff_encode_guard_test.go`
  (`TestEncodeBigTIFFSourceReturnsError` → `TestEncodeBigTIFFSourceSucceeds`).
  `TestBigTIFFWriteClassicPositive` (classic-TIFF regression guard) was kept
  unchanged — it never depended on the gate.
- **New** `bigtiff_write_e2e_test.go` — `TestWriteBigTIFFEndToEnd`, the
  public-API round-trip proof using the two REAL committed fixtures
  (`testdata/fixtures/BigTIFF_{LE,BE}.tif`, already used read-only by
  `TestReadBigTIFF` in `read_test.go`; NOT gitignored — tracked by git, so no
  corpus-absence skip is needed). Flow: `Read` → `m.SetCopyright(...)`
  (exercises the FULL write path: EXIF edit + auto-created IPTC + auto-created
  XMP, not a bare pass-through copy) → `Write` → `Read` again. Asserts:
  magic never downgraded, unmodified fields (Make/Model/ImageWidth/
  ImageLength) survive byte-for-byte, the modified field (Copyright) reflects
  the new value, AND the image strip payload (addressed via IFD0's
  StripOffsets/StripByteCounts, both `TypeLong8` in these fixtures) is
  byte-for-byte identical before/after despite its absolute file offset
  necessarily changing across the copy-and-relocate write. Added a
  `bigTIFFStrip` test helper that decodes `IFDEntry.Value` for a `TypeLong8`,
  `Count=1` entry manually via `e.ByteOrder.Uint64(...)` — there is no
  `IFDEntry.Uint64()` accessor in the `exif` package (only `Uint16`/`Uint32`/
  `Rational`/`SRational`/`Int16`/`Int32`/`Float32`/`Float64`/`Bytes`/`Byte`/
  `Uint8s`), so any future test needing a 64-bit IFD scalar must decode via
  `e.ByteOrder` directly on `entry.Value`, same as this helper does.
- Fixed a stale doc comment in `format/tiff/fuzz_test.go` (`FuzzTIFFInject`)
  that still described BigTIFF seeds as exercising "the unsupported-magic
  rejection path" — no longer true since task #270 (`tiff.Inject` never
  rejected BigTIFF at that layer; only the now-removed root-package gate
  did).

## Documentation updated (not just code)
- `README.md` — "Supported formats" prose row (Write column) and footnote ²
  no longer say BigTIFF write is unsupported; they now describe the native
  BigTIFF encoder + container-width-aware relocator.
- `CHANGELOG.md` — per Keep a Changelog discipline, did NOT retroactively
  edit the `[1.2.0]` historical entries (those are accurate snapshots of
  what was true at that release). Added a new `### Fixed` bullet under
  `[Unreleased]` documenting the #264/#270/#271 combination closing the gap.
- Deliberately did NOT touch `knowledge-model.md` or the `rmp` Knowledge
  Graph — those are the orchestrating agent's responsibility per this
  project's CLAUDE.md Execution workflow (step 8), not part of a delegated
  coding task. `knowledge-model.md:70` still says BigTIFF write returns
  `ErrWriteNotSupported` as of this writing — flag this to the orchestrator
  if asked to audit doc/graph accuracy again.

## Verification
- `TestWriteBigTIFFEndToEnd/BigTIFF_LE` and `/BigTIFF_BE` both pass: full
  Read→SetCopyright→Write→Read round trip, metadata + strip image data
  byte-for-byte preserved, magic never downgraded.
- Full suite: `go build ./...`, `go vet ./...`, `gofmt -l .`,
  `go test -race -count=1 ./...` (all packages, ~31s), and
  `golangci-lint run ./...` (0 issues) all clean after this change.
