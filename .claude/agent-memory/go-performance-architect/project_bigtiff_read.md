---
name: project_bigtiff_read
description: BigTIFF read support fully end-to-end — tiff.Extract (803070e) + public-API routing fix (8f5752d)
metadata:
  type: project
---

Task #54 is fully closed across two commits:

**Commit 803070e** added BigTIFF read support to `format/tiff/tiff.Extract` (internal only).

**Commit 8f5752d** fixed the public-API gap: `format.Detect` now recognises BigTIFF magic (0x002B) and routes it to `tiff.Extract` through the same `FormatTIFF` → `extractors[FormatTIFF]` dispatch path used by classic TIFF.

**Internal implementation (803070e):**
- `extractBigTIFF(data, order)` in `format/tiff/tiff.go` handles the 16-byte BigTIFF header:
  - bytes 4-5: offset-bytesize must == 8, else `ErrUnsupportedMagic`
  - bytes 8-15: IFD0 offset as uint64
- `extractTagValuesBigTIFF(data, ifd0Off, order)` scans IFD0 with 64-bit field widths:
  - 8-byte entry count (capped at `bigTIFFMaxIFDEntries=65535`)
  - 20-byte entries: 2(tag)+2(type)+8(count)+8(value-or-offset)
  - inline threshold: 8 bytes (classic is 4)
  - overflow guard: `cnt > maxUint64/sz` before computing `total = sz*cnt`
- `typeSizeBigTIFF(t uint16) uint64` extends classic typeSize with LONG8(16)=8, SLONG8(17)=8, IFD8(18)=8

**Key difference from classic path:** the BigTIFF path does NOT call `exif.Parse` (which only accepts magic 0x002A). `rawEXIF` = full data bytes; the write path for BigTIFF is explicitly unsupported.

**Fixtures committed:**
- `format/tiff/testdata/big_cramps_le.tif` — tiffcp -8 -L cramps.tif (LE BigTIFF, ~194KB)
- `format/tiff/testdata/big_cramps_be.tif` — tiffcp -8 -B cramps.tif (BE BigTIFF, ~194KB)

**Tests updated in other packages:**
- `format/raw/{arw,cr2,dng,nef}` each had a `TestXxxBigTIFFReturnsError` that expected `ErrUnsupportedMagic`. These were renamed to `TestXxxBigTIFFSucceeds` and updated to expect success. The `errors` and `format/tiff` imports were removed from those test files as they were no longer needed.

**Public-API routing fix (8f5752d):**
- `isBigTIFFLittleEndian` / `isBigTIFFBigEndian` predicates added to `format/detect.go`
- `detectMagic` returns `FormatTIFF` for BigTIFF magic so it routes to `tiff.Extract`
- `parseTIFFScanHeader` auto-detects BigTIFF vs classic from magic byte [2:4]; dispatches to `parseBigTIFFIFD0` (uint64 header, 8-byte IFD count) or `parseClassicTIFFIFD0`
- `findMakeTagInIFDBigTIFF` scans 20-byte entries for DNG (0xC612) and Make (0x010F); inline threshold 8 bytes
- `refineTIFFVariant` dispatches to the BigTIFF tag scanner when `bigTIFF=true`
- `tiffScanSize` enlarged from 1034 → 1560 (covers 64 BigTIFF entries)
- New public-API test: `TestReadBigTIFF` in root `read_test.go` exercises `gometadata.ReadFile` on both LE and BE fixtures — the test that was missing before
- New FuzzRead seed: `testdata/fuzz/FuzzRead/seed_bigtiff_le_magic`

**Why:** BigTIFF header validation fires `ErrUnsupportedMagic` only for invalid offset-bytesize (not 8) or unknown magic (not 0x002A or 0x002B). The old "BigTIFF unsupported" rejection was replaced by actual parsing.

**How to apply:** When adding new TIFF-based format packages, their delegation tests should not expect BigTIFF to return an error — it succeeds now. Public-API tests must exercise `gometadata.Read`/`ReadFile`, not internal extract functions, to catch routing gaps like this one.
