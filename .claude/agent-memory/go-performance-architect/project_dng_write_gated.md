---
name: project-dng-write-gated
description: DNG write was re-gated (task #101) due to bug #98; bug #98 is now fixed and DNG write is re-enabled (commit 9ff26ac)
metadata:
  type: project
---

DNG write was disabled as a fail-safe in task #101 (commit 1aaec61) pending bug #98 fix.

**Bug #98 root cause:** In `patchRawIFDOffsets` (format/tiff/relocate.go), only
strip/tile image-data offset tags had their `valOrOff` pointer updated after SubIFD
relocation. All other OOL entries — RATIONAL XResolution/YResolution (8 bytes > 4),
SRATIONAL, DOUBLE, long ASCII, etc. — had their value bytes captured verbatim in
`rawBytes` by `extractRawIFD` but their 4-byte `valOrOff` fields left pointing at the
original file offset (stale). Readers followed the stale pointer → "undef".

**Fix (task #98, commit 9ff26ac):** `patchRawIFDOffsets` now updates the `valOrOff`
field for EVERY OOL IFD entry (total > 4), not just strip/tile arrays. The new
absolute position is `newSubIFDOff + (origFileOff − srcOff)`.

**Current state (re-enabled):**
- FormatDNG removed from `isTIFFBased()` in write.go; DNG routes through `writeTIFF`.
- `format.SupportsWrite(FormatDNG)` returns true.
- Tests: `TestSubIFDRationalValuesPreservedOnRelocation` (regression, format/tiff),
  `TestDNGWriteRoundTrip` (write_test.go), `TestSupportsWrite` (both packages).
- Validated on real Pentax QS1.dng: SubIFD:XResolution/YResolution=300 preserved,
  ImageDataHash IN==OUT, no "Undefined value for SubIFD:*" warnings, tiffcp OK.

**Why:** TIFF (FormatTIFF) and DNG (FormatDNG) both share the `writeTIFF` path.
CR2/NEF/ARW/ORF/RW2 remain gated (task #95 manufacturer-specific offset handling).

Related: [[feedback-tiff-exif-base-for-write]], [[feedback-tiff-subifd-raw-level]]
