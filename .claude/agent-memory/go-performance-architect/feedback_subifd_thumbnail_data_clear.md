---
name: feedback-subifd-thumbnail-data-clear
description: IFD0 and SubIFDs with 0x0201/0x0202 must have ThumbnailData cleared before block enumeration — affects ARW preview + NEF JpgFromRaw
metadata:
  type: feedback
---

`extractJPEGThumbnail` runs on EVERY parsed IFD (not just IFD1), so any IFD containing both 0x0201 (JPEGInterchangeFormat) and 0x0202 (JPEGInterchangeFormatLength) gets `ThumbnailData` set. `enumerateIFDBlocks` checks `if ifd.ThumbnailData == nil` before calling `appendJPEGBlock`, assuming `exif.Encode` handles ThumbnailData — but `exif.Encode` only processes ThumbnailData for `IFD0.Next` (IFD1 chain), never for IFD0 itself or SubIFDs.

Two affected cases:
1. **SubIFDs** (0x014A children): `enumerateSubIFDsAt` must clear `parsedIFD.ThumbnailData` before `enumerateIFDBlocks`. (Fixed in task #102.)
2. **IFD0 preview JPEG** (Sony ARW, any TIFF): IFD0 with 0x0201/0x0202 stores a large preview JPEG. The relocation entry points (`relocateTIFFFromParsed`, `arwRelocateWithSR2`, `nefRelocateWithPreview`) must clear `e.IFD0.ThumbnailData` before `enumerateImageBlocks` so the preview is treated as an imageBlock. (Fixed in task #103 regression, commit 0ef9ce6.)

**Why:** Sony ARW DSLR-A500 carries a 736 KB preview JPEG in IFD0. The missing clear caused a 736 KB file size reduction and a stale PreviewImageStart offset in all ARW write output. The same pattern applies to any TIFF/NEF/DNG with an IFD0 JPEG preview.

**How to apply:** Before calling `enumerateImageBlocks` in any relocation entry point, clear `e.IFD0.ThumbnailData = nil`. For SubIFDs, clear on the parsed SubIFD before passing to `enumerateIFDBlocks`. Do NOT clear ThumbnailData on IFD1 chain entries — exif.Encode handles those.
