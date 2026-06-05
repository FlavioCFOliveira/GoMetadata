---
name: feedback_tiff_exif_base_for_write
description: TIFF/DNG write path uses writeTIFF()+InjectWithEXIF() to pass original bytes + modified EXIF struct; task #97 fix
type: feedback
---

FIXED in task #97 (commit 20a7317). gometadata.Write for TIFF/DNG now takes a dedicated `writeTIFF()` path that correctly separates image-data source from the IFD model.

**Root cause (was):** encodeEXIF returned exif.Encode(m.EXIF) — an IFD skeleton without image blocks — as rawEXIF. relocateTIFF got this skeleton as `base`, then enumerateImageBlocks saw original strip/tile offsets > len(skeleton) → ErrBlockOutOfBounds.

**How the fix works:**
- `write.go`: for FormatTIFF/FormatDNG, `Write()` calls `writeTIFF(r, w, m)` instead of the generic `encodeMetadata + injectByFormat` pipeline.
- `writeTIFF`: uses `m.rawEXIF` (the ORIGINAL full TIFF bytes) as the image-data source; passes `m.EXIF` (already mutated by Set* calls) to `tiff.InjectWithEXIF`.
- `tiff.InjectWithEXIF(originalBytes, modifiedEXIF, rawIPTC, rawXMP, w)`: new exported function that calls `relocateTIFFFromParsed(base=originalBytes, e=modifiedEXIF, ...)` directly.
- `relocateTIFF` is now a thin wrapper; `relocateTIFFFromParsed` is the implementation.

**Why:** relocateTIFFFromParsed mutates the *exif.EXIF in-place (insertPlaceholders / updatePlaceholders rewrite strip offset entries). Callers must snapshot original strip offsets BEFORE calling InjectWithEXIF if they need to verify the originals afterward (see realfile_test.go).

**How to apply:**
- No longer need to set m.EXIF=nil before Write for TIFF/DNG — the write path handles it correctly.
- Tests that snapshot strip offsets must do so BEFORE calling InjectWithEXIF/writeTIFF.
- The old workaround (m.EXIF=nil) still works for pass-through but is unnecessary.

See: format/tiff/tiff.go InjectWithEXIF, format/tiff/relocate.go relocateTIFFFromParsed, write.go writeTIFF.
