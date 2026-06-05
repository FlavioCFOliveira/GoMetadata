---
name: project_cr2_write_ungated
description: CR2 write un-gated (task #95, commit 561f5d8); NEF and ARW remain gated with specific failure modes documented
type: project
---

CR2 write is un-gated as of task #95 (commit 561f5d8). CR2 uses standard LE TIFF magic and routes through `writeTIFF` + `relocateTIFFFromParsed`.

**Why:** Canon MakerNotes use blob-relative (self-relative) offsets — verbatim blob copy is safe. Validated against real Canon EOS 350D (7.4 MB CR2): ImageDataHash IN==OUT, all MakerNote tags preserved.

NEF remains gated: SubIFD OOL RATIONAL corruption (XResolution/YResolution: 72→1), PreviewIFD/NikonScanIFD lost, ImageDataHash mismatch (5.6 MB → 4.9 MB).

ARW remains gated: 52 Sony MakerNote tags lost, SR2Private IFD corrupted, SubIFD StripOffsets wrong (917504 → 50979).

ORF/RW2 remain gated: non-standard TIFF magic (IIRS, IIU\0).

**How to apply:** When asked to un-gate NEF or ARW, the failures are at the SubIFD / MakerNote level — the outer TIFF relocation works, but Nikon and Sony have additional IFD structures (PreviewIFD, SR2Private) and OOL values inside SubIFDs that are not correctly relocated. A deeper format-specific investigation is needed before these can be un-gated.
