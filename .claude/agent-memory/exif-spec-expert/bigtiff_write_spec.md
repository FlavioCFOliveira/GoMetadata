---
name: bigtiff-write-spec
description: BigTIFF write encode contract delivered to go-performance-architect 2026-07-06 — header/IFD byte layout, alignment spec-vs-libtiff conflict, function-mapping for exif/write.go, proposed rule IDs S-34..39/V-12..14/R-14..17
metadata:
  type: project
---

Delivered a full BigTIFF-write implementation-ready spec sheet to `go-performance-architect`
on 2026-07-06 (in response to their request to implement BigTIFF write; see their
`project_bigtiff_read.md` for the read-side counterpart). This memory captures the durable,
non-obvious findings so they don't need to be re-derived.

## Key architectural insight (saves significant redesign effort)

`exif.IFDEntry.Value` already stores the LOGICAL value bytes (`typeSize(Type)*Count`), not the
physical container field. This means `buildIFD0Entries`, `buildExifIFDEntries`, `patchPointers`,
and `patchThumbnailEntries` (all in `exif/write.go`) need **zero changes** for BigTIFF — they
already write/patch 4-byte `TypeLong` values into 4-byte scratch buffers regardless of container.
Only the *physical-layout* functions differ between classic and BigTIFF: `writeTIFFHeader`,
`writeIFD`, `ifdTotalSize`, `computeIFDOffsets`, `writeSubIFDs`, and `serialise`'s dispatch.
Sub-IFD pointer tags (ExifIFDPointer 0x8769, GPSIFDPointer 0x8825, InteropIFDPointer 0xA005) MUST
stay `TypeLong` in BigTIFF too (matches EXIF §4.6.3/§4.6.5 tag-type definitions and matches
tiffcp/libtiff real-world convention already documented in `readBigTIFFSubIFDOffset`'s doc
comment) — only the entry's physical value-or-offset FIELD widens to 8 bytes (4-byte value
left-justified + 4 zero-pad bytes). Do not be tempted to promote these tags to `TypeIFD8`.

## Critical spec-vs-reference-implementation conflict (must be escalated, not silently resolved)

The BigTIFF design doc (libtiff.gitlab.io/libtiff/specification/bigtiff.html, verified via 3
independent mirrors) states verbatim: **"All values must begin at an 8-byte-aligned address."**
and explains the widened 8-byte entry-count field exists "to keep IFD entries 8-byte-aligned."

However, the actual reference implementation (`libtiff/tif_dirwrite.c`, inspected directly via
GitHub `vadz/libtiff` master) does **NOT** implement 8-byte alignment for BigTIFF — it applies
the SAME word (2-byte) alignment as classic TIFF in both cases:
- `TIFFWriteDirectoryTagData`: `if (tif->tif_dataoff&1) tif->tif_dataoff++;` (checks bit 0 only)
- `TIFFLinkDirectory`: `(TIFFSeekFile(tif,0,SEEK_END)+1) & (~((toff_t)1))` (clears bit 0 only,
  i.e. rounds to even — `~1`, not `~7`)

This is a genuine, confirmed discrepancy between the design document's stated intent and the
de-facto reference implementation's actual behavior. Per CLAUDE.md's Decision Policy (ambiguous/
contradictory instructions must be escalated with options, not silently resolved), I presented
this to go-performance-architect as a flagged decision point with two options:
(a) literal spec text → 8-byte alignment; (b) match libtiff/tiffcp → 2-byte/word alignment
(same as the already-tested classic-TIFF padding code). **My recommendation was (b)**: matches
the tool (tiffcp) that generated this project's own committed BigTIFF fixtures
(`exif/testdata/corpus/tiff/exiftool/BigTIFF_{LE,BE}.tif`), reuses already-audited padding
arithmetic, and alignment is a performance/mmap convention rather than a correctness
requirement (no reader treats misalignment as invalid).

**How to apply:** if asked again about BigTIFF OOL-value or IFD-block alignment, cite this
conflict explicitly and do not assume the literal 8-byte spec text is what any real file
actually does. See [[project_bigtiff_read]] for the read-side context this write spec extends.

## Proposed conformance-checklist rule IDs (docs/conformance/exif-tiff.md)

Continuing from S-33/V-11/R-13 (BigTIFF read rules already occupy S-05/S-06/S-15/S-16/S-17/S-18/
S-22 — these are READ rules; the new ones below are WRITE rules):

- **S-34** BigTIFF write header: 16 bytes, BOM+43+8+0+u64 IFD0-offset, both endiannesses.
- **S-35** BigTIFF write IFD: u64 count + 20-byte entries (tag u16/type u16/count u64/val u64) + u64 next-ptr, sorted ascending by tag.
- **S-36** BigTIFF write inline/OOL threshold = 8 bytes via `typeSizeBigTIFF`, left-justified + zero-pad when inline.
- **S-37** BigTIFF write next-IFD pointer is u64; 0 = end.
- **S-38** Sub-IFD pointer tags (0x8769/0x8825/0xA005) keep `TypeLong`; only the entry FIELD widens to 8 bytes — do not promote to TypeIFD8.
- **S-39 (decision-flagged)** OOL value alignment strategy: word (2-byte, matches libtiff) vs literal-spec 8-byte — must be confirmed before implementation.
- **V-12** Both the sizing pass and the serialising pass MUST use `typeSizeBigTIFF`, not `typeSize` — the latter returns 0 for LONG8/SLONG8/IFD8 (types 16/17/18), silently dropping their value area and truncating via Go `copy()`'s min-length semantics if fed into the classic inline path.
- **V-13** Totals of 5–8 bytes (e.g. a single Count=1 RATIONAL) are inline under BigTIFF's 8-byte threshold but were OOL under classic's 4-byte threshold; placement is deterministic from (Type,Count), never "whatever the source file did."
- **V-14** Unknown-type entries (`typeSizeBigTIFF`==0) round-trip as an 8-byte raw inline field, mirroring `parseIFDEntryBigTIFF`'s read-side "sz==0 → store raw 8 bytes verbatim" handling (widened from the classic 4-byte analogue).
- **R-14** Round-trip fidelity: unmodified BigTIFF entries decode back identically (Tag/Type/Count/Value); image-data blocks untouched (cross-references `format/tiff` — see scope note below).
- **R-15** All BigTIFF offset/size arithmetic in `ifdTotalSizeBigTIFF`/`computeIFDOffsetsBigTIFF` is uint64 with an explicit, documented saturation ceiling (recommend a named sanity-cap constant, e.g. 1 TiB, consistent with existing `bigTIFFMaxEntries`/`maxBigTIFFCount` anti-DoS conventions) — never silent 64-bit wraparound. NOT math.MaxUint32 (that would defeat BigTIFF's purpose).
- **R-16** `TypeLong`-typed pointer/offset tags (ExifIFDPointer/GPSIFDPointer/InteropIFDPointer, JPEGInterchangeFormat 0x0201) must never receive a value ≥2^32 in BigTIFF write; guard before calling `patchPointers`/`patchThumbnailEntries` and fail loudly with a new sentinel error rather than truncate.
- **R-17** MakerNote/IFD1 verbatim-preservation contract identical to classic TIFF's R-11 policy, extended explicitly to BigTIFF provenance.

Also flagged (not a new checklist ID, a required test-suite migration): `exif/bigtiff_encode_guard_test.go`'s `TestEncodeBigTIFFSourceReturnsError` asserts `Encode` returns `ErrBigTIFFEncodeNotSupported` for `e.BigTIFF==true` — this test must be deliberately retired/rewritten as part of the same change, not left to fail as a surprise regression.

## Scope boundary flagged to go-performance-architect

`exif.Encode`/`exif/write.go` is the JPEG-APP1/generic-EXIF-blob encoder. `format/tiff`'s
`relocateTIFFFromParsed`/`InjectWithEXIF` (the path used for standalone TIFF/DNG/RAW container
writes) calls `exif.Encode` internally but does its own 32-bit-offset image-block relocation
(`assignNewOffsets`, `ErrOffsetOverflow`, `ErrImageBlockOverflow` are all uint32-only). Fixing
`exif.Encode` alone does NOT give BigTIFF write support for standalone BigTIFF container files
(large GeoTIFF-style files, `-8`/`.btf` outputs) — that needs a parallel follow-up in
`format/tiff`. This is out of scope for the current task (which is explicitly `exif/write.go`)
but is a real gap against the literal "100% TIFF/BigTIFF write" mandate that should become its
own tracked task.
