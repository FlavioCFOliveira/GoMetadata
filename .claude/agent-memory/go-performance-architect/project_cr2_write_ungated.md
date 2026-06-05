---
name: project_cr2_write_ungated
description: CR2/NEF/ARW write un-gated status — task #95 (CR2), #102 (NEF), #103 (ARW); ORF/RW2 remain gated
metadata:
  type: project
---

CR2 write is un-gated as of task #95 (commit 561f5d8). CR2 uses standard LE TIFF magic and routes through `writeTIFF` + `relocateTIFFFromParsed`.

**Why:** Canon MakerNotes use blob-relative (self-relative) offsets — verbatim blob copy is safe. Validated against real Canon EOS 350D (7.4 MB CR2): ImageDataHash IN==OUT, all MakerNote tags preserved.

NEF write is un-gated as of task #102 (commit 7d34aa5). See [[project-nef-write-ungated]].

ARW write is un-gated as of task #103. See [[project-arw-write-ungated]].

ORF/RW2 remain gated: non-standard TIFF magic (IIRS, IIU\0).

**How to apply:** ORF/RW2 require format-specific outer-framing work before the TIFF relocator can process them. All other TIFF-based RAW formats (CR2, NEF, ARW, DNG) are now writable.
