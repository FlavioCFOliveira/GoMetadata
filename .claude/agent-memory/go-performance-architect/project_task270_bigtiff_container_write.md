---
name: project_task270_bigtiff_container_write
description: BigTIFF standalone CONTAINER write / 64-bit relocation in format/tiff (task #270) — full support, files, discovered bugs, remaining gap
type: project
---

Task #270 (Clean-GO sprint) closed the SECOND half of the "100% BigTIFF
write" mandate — task #264 (see [[project_bigtiff_write]]) made
`exif.Encode` produce valid BigTIFF EXIF blobs; #270 made `format/tiff`'s
copy-and-relocate serializer (`relocateTIFFFromParsed`, behind
`tiff.Inject`/`tiff.InjectWithEXIF`) correctly rebuild the surrounding
BigTIFF *container* — image blocks, SubIFDs, and every outer-container raw
byte scan performed after `exif.Encode`. **Full support achieved**, not just
fail-safe — proven against 16 real corpus fixtures (BigTIFFLong/Long8/
Long8Tiles/SubIFD4/SubIFD8/Motorola/MotorolaLongStrips + `_2` variants,
exiftool BigTIFF_LE/BE.tif) plus 194 KB big_cramps_{be,le}.tif (51 LONG8
strips, committed fixture) and synthetic hand-built fixtures.

**Why this mattered**: `format/tiff/relocate.go`'s enumeration/patching
functions (`extractParallelOffsetBlocks`, `enumerateSubIFDs`,
`patchSubIFDPointers`, `extractRawIFD`, `patchRawIFDOffsets`,
`rebaseGenericMakerNote`'s outer scan) all hardcoded classic-TIFF widths
(8-byte header, 12-byte entries, 4-byte fields, 4-byte inline threshold).
Even though `exif.Encode` (task #264) could already SERIALISE a valid
BigTIFF IFD skeleton, the surrounding relocation layer would silently
misinterpret the resulting BigTIFF byte stream when scanning it post-encode
(patchSubIFDPointers reading `finalTIFF[4:]` as a classic IFD0 offset would
read BigTIFF's bytesize/reserved fields instead) — a real corruption risk for
any BigTIFF file with SubIFDs (i.e. DNG-style full-res raw data), which is
NOT a rare edge case for BigTIFF.

## Files
- **NEW** `format/tiff/relocate_bigtiff.go` — container-width-aware raw IFD
  scanning primitives, all parameterised on a single `bigTIFF bool`:
  `ifdWidths`, `inlineThreshold`, `elemSizeFor`, `readIFD0Offset`,
  `rawIFDEntry` struct, `ifdEntryTable`, `readRawEntryAt`, `findEntryInIFD`,
  `fieldAsU64`, `putFieldU64`, `decodeOffsetArray`, `parseIFDAtBigTIFF`.
- **NEW** `format/tiff/conformance_bigtiff_write_test.go` — S-40..S-43/R-18/
  R-19 battery; raw-buffer-based recursive comparator
  (`compareIFDRoundTrip`/`compareOffsetBlockContent`/`dereferenceEntryValue`)
  deliberately does NOT use `exif.Parse`'s `*exif.IFDEntry.Value` for
  structural comparison (see "type-13 bug" below) — it re-derives everything
  from raw bytes via the SAME primitives the production code uses, keeping
  the test an independent check of the WRITE OUTPUT bytes while still being
  correct for the type-13 collision case.
- `format/tiff/relocate.go` — `imageBlock`/`subIFDInfo` struct fields widened
  uint32→uint64 (`offElemSz`/`cntElemSz uint8` added to `imageBlock` to
  preserve the SOURCE entry's SHORT/LONG/LONG8 width on write — S-42);
  `readUint` now supports elemSz=8 and returns uint64;
  `extractParallelOffsetBlocks`/`enumerateIFDBlocks`/`enumerateImageBlocks`/
  `appendJPEGBlock` all gained a `bigTIFF bool` param, dispatching
  `typeSizeBigTIFF` vs `typeSize` via the new `elemSizeFor`;
  `insertPlaceholders`/`updatePlaceholders`/`upsertIFDEntryWithCount`/
  `widthToType`/`putWidth` rewritten to preserve element width instead of
  hardcoding TypeLong; `enumerateSubIFDs`/`enumerateSubIFDsAt` COMPLETELY
  rewritten to raw-rescan `base` via `findEntryInIFD`/`decodeOffsetArray`
  instead of trusting `ifd0.Get(exif.TagSubIFDs)` (see type-13 bug);
  `extractRawIFD`/`patchRawIFDOffsets`/`patchSubIFDPointers`/
  `computeSubIFDsSize`/`assignSubIFDOffsets`/`patchSubIFDImageOffsets`/
  `assignNewOffsets` all widened to uint64 and/or made `bigTIFF`-aware.
- `format/tiff/tiff.go` — `typeSize`/`typeSizeBigTIFF` (format/tiff's OWN
  local copy, distinct from `exif/type.go`) gained `case 13: return 4`.
- `format/tiff/relocate_makernote.go` — `rebaseGenericMakerNote` gained an
  explicit `if e.BigTIFF { return }` documented fail-safe deferral (see
  "remaining gap" below).
- `format/tiff/relocate_arw.go`/`relocate_nef.go`/`relocate_orf.go`/
  `relocate_rw2.go` — mechanical call-site updates only (pass `false`/
  `e`/widened locals) for the shared function signature changes; these RAW
  formats are ALWAYS classic TIFF in practice (never BigTIFF), zero
  behavioural change, proven by full existing test suite passing unchanged.
- `docs/conformance/exif-tiff.md` §2.8 — new rules S-40..S-43, V-15, R-18,
  R-19.

## Discovered pre-existing bug: EXIF-3.0/TIFF-Extension type-13 collision
CIPA DC-008-2023 §4.6.3 assigns type code 13 to `TypeUTF8` (1 byte/element,
EXIF-registry). Adobe TIFF 6.0 Extensions (libtiff `TIFF_IFD`) assigns the
SAME code 13 to `IFD` (4-byte child-IFD pointer), predating EXIF 3.0 by
decades. `exif/type.go` correctly follows CIPA for its own EXIF-registry
tags — but tag 0x014A (SubIFDs) is a TIFF-Extension tag, and real files
legitimately declare it type 13 (proven fixture:
`testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif`). `exif.Parse`
mis-sizes such an entry's inline value to 1 byte instead of 4, silently
truncating the SubIFD pointer — this ALREADY existed for classic TIFF too
(not BigTIFF-specific), and would have caused `enumerateSubIFDs`'s OLD
`ifd0.Get(exif.TagSubIFDs)`-based lookup to see `elemSz==0` (format/tiff's
OWN pre-#270 local `typeSize` also lacked case 13) and silently skip the
SubIFD entirely on write — total loss of the SubIFD's image data on any
metadata write to such a file. Fixed by (a) adding case 13→4 to format/tiff's
local `typeSize`/`typeSizeBigTIFF`, and (b) `enumerateSubIFDs`/
`enumerateSubIFDsAt` raw-rescanning `base` directly instead of trusting
`exif.Parse`'s struct. Did NOT touch `exif/type.go` (out of scope, and its
CIPA DC-008-2023 interpretation is correct for EXIF-registry tags).

## Policy decisions made
- **Preserve source element width on write** (S-42): StripOffsets/
  StripByteCounts/TileOffsets/TileByteCounts keep whatever width
  (SHORT/LONG/LONG8) the source declared, rather than always forcing LONG.
  Justified because real fixtures legitimately use either (BigTIFFLong.tif
  vs BigTIFFLong8.tif), and TIFF/BigTIFF spec never mandates one width for
  these tags.
- **maxFileSize (256 MiB) makes the classic math.MaxUint32 saturation
  ceiling in `assignNewOffsets`/`computeSubIFDsSize`/`assignSubIFDOffsets`
  harmless to reuse UNCHANGED for BigTIFF** — deliberately did NOT
  re-litigate this ceiling's behaviour for BigTIFF; it's now purely
  defence-in-depth for BigTIFF (real BigTIFF format ceiling is 2^64) since
  this package can never actually produce output anywhere near 4 GiB. Only
  the field TYPES were widened to uint64 (to avoid precision loss on
  wire-format values from LONG8/IFD8 sources), not the ceiling logic itself.
- **MakerNote + BigTIFF = documented fail-safe no-op, not full support**
  (R-19): `rebaseGenericMakerNote`'s OUTER-container scan (IFD0/ExifIFD
  lookup) is classic-TIFF-only; its MakerNote-INTERNAL IFD walk is ALWAYS
  classic-shaped regardless of outer container (Sony/Olympus MakerNote
  conventions predate BigTIFF). Rather than leave this "accidentally safe by
  guard cascade" (the classic-only scan on a BigTIFF buffer happens to find
  nothing and return early), added an EXPLICIT `if e.BigTIFF { return }`
  guard with a test proving byte-for-byte no-op
  (`TestConformance_R19_makernote_bigtiff_failsafe_noop`). No known
  real-world file combines a Sony-plain-IFD/Olympus-OLYMP-type MakerNote
  with a genuinely BigTIFF outer container (BigTIFF is scientific/GIS/aerial
  imagery territory, never camera MakerNotes) — full outer-container-aware
  MakerNote rebasing is the one deferred piece, flagged as a follow-up, not
  attempted.
- Did NOT touch the RAW-specific relocators' (arw/nef/orf/rw2) OWN
  MakerNote-rebasing/thumbnail logic — only mechanical call-site updates for
  the shared-function signature changes. These formats are never BigTIFF in
  practice (camera-produced, always classic TIFF magic).

## Remaining integration gap (NOT fixed — deliberately out of scope)
`write.go:416` (root package) has a HARDCODED gate:
`isBigTIFFSource(...) → return ErrWriteNotSupported` inside `writeTIFF`,
predating this task (task #264 era, when the underlying layers genuinely
weren't ready). This means **`gometadata.Write`'s top-level API still
refuses to write BigTIFF sources**, even though `tiff.Inject`/
`tiff.InjectWithEXIF` (the format/tiff package API) now fully supports it —
proven directly via `tiff.Inject` in this task's tests, bypassing the
top-level gate. Removing this 3-line gate is a trivial, well-defined
follow-up, but requires touching root-package `write.go`, which was
EXPLICITLY out of scope for #270 ("stay strictly within format/tiff/** + docs
+ tests; do not touch metadata.go" — write.go is the same root package).
**Recommend a fast follow-up task** (call it #271) to flip this gate once
confirmed; do not assume it's already done without re-checking `write.go`.

## Test evidence
- `TestConformance_S40_bigtiff_outer_container_widths` — proves classic vs
  BigTIFF scans of readIFD0Offset/findEntryInIFD never conflate layouts.
- `TestConformance_S41_bigtiff_long8_strip_arrays` — real fixtures with
  LONG8 strip/tile arrays round-trip (was previously
  `ErrUnsupportedOffsetType`).
- `TestConformance_S42_element_width_preserved` (+`_synthetic`) — LONG8
  StripOffsets/StripByteCounts stay LONG8 after relocation.
- `TestConformance_S43_subifd_pointer_type_variants` (+`_synthetic`, all 4
  type codes LONG/IFD/LONG8/IFD8) — 0x014A type preserved.
- `TestConformance_R18_bigtiff_roundtrip_fidelity_curated`/`_corpus` — full
  recursive structural diff (every non-relocatable tag byte-identical,
  every strip/tile/SubIFD content byte-identical) across 16 real fixtures +
  2 committed big_cramps fixtures; corpus-based sub-tests correctly SKIP
  when `testdata/corpus` absent (gitignored, matches existing house
  convention — verified all 16 fixtures PASS when corpus IS present via a
  temporary local symlink, removed before commit).
- `TestConformance_R19_makernote_bigtiff_failsafe_noop`.
- `FuzzTIFFInject`: added 3 new BigTIFF seeds via `buildSyntheticBigTIFF`
  (shared between the conformance test and fuzz_test.go); 60s run,
  ~1,019,840 execs, 0 crashers. Fixed a stale seed-7 comment that claimed
  "Inject still rejects BigTIFF" (no longer true after this task).

## Lint iteration notes (see also [[feedback_lint_iteration_after_new_code]])
- `enumerateSubIFDsAt` needed `funlen` added to its existing
  `//nolint:cyclop,gocyclo` (now handles a BigTIFF-vs-classic child-IFD
  parse dispatch in addition to recursion/cycle detection).
- gosec G115 fired inconsistently on `int→uint64` conversions of the SAME
  kind of value (`entryWidth`/`countWidth`, compile-time constants 12/20/2/8
  from `ifdWidths`) depending on how directly the conversion appeared in an
  expression — assigning to a named `xWidth64 := uint64(xWidth)` variable
  first, THEN using that variable, got the `//nolint:gosec` directive to
  register as "used" consistently; inlining `uint64(x)` directly inside a
  larger expression sometimes did and sometimes didn't trigger G115 at all
  (matches [[feedback_gosec_g115_inconsistent]] — always verify the exact
  line golangci-lint flags before adding nolint, never assume symmetry).
- `unconvert` fired on `uint64(x)` casts of variables that were ALREADY
  uint64 as a knock-on effect of widening `imageBlock`/`stripRecord` fields
  and `readUint`'s return type — had to sweep ALL pre-existing test files
  using these fields (`realfile_test.go`, `relocate_test.go`,
  `relocate_subifd_test.go`), not just the ones I wrote new code in.
