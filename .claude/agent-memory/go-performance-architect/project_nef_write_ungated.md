---
name: project-nef-write-ungated
description: NEF write un-gated in task #102; details of Nikon-specific relocation fix (commit 7d34aa5)
metadata:
  type: project
---

NEF write was un-gated in task #102 (commit 7d34aa5, 2026-06-05).

**Why:** Three interrelated root causes made the standard relocateTIFF path corrupt NEF files:
1. Nikon Type-3 MakerNote declares a smaller byte count than its actual data extent (PreviewIFD + NikonScanIFD live beyond the declared boundary)
2. The Nikon TIFF-header-within-MakerNote is at offset +10 (D70/version 0x0200) or +8 (version 0x0210) — must be scanned dynamically, not hardcoded
3. PreviewIFD 0x0201 holds a MakerNote-TIFF-relative offset (not outer-TIFF-absolute) that must be patched after re-encoding to the new position

**Incidental fix also made:** SubIFDs that use 0x0201/0x0202 (Nikon's JpgFromRaw SubIFD) had ThumbnailData set by parseSingleIFD, causing appendJPEGBlock to skip them. Fixed by clearing ThumbnailData on SubIFD parsed IFDs before block enumeration. patchRawIFDOffsets extended to handle 0x0201/0x0202 inline in SubIFD raw bytes.

**How to apply:** When implementing relocation for other RAW formats (ARW, ORF, RW2) that also have MakerNote-relative offsets, use the same pattern: detect the format-specific structure, enumerate extra blocks, patch post-encode via IFD scanning.

**Validation evidence (Nikon D70, real.nef 5.6 MB):**
- ImageDataHash: f6273abe4d04a43a0b95cc664fef6c45 (IN==OUT)
- PreviewIFD (6 entries), NikonScanIFD (0 entries): preserved
- SubIFD0 JpgFromRawLength=715274, SubIFD1 raw=4808449: preserved
- TIFF/DNG/CR2 no regression (ImageDataHash IN==OUT for all three)
