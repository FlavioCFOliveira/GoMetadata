---
name: project_cr2_write_ungated
description: CR2/NEF/ARW/ORF/RW2 write un-gated status — tasks #95, #102, #103, #104; all TIFF-based RAW formats now writable
metadata:
  type: project
---

CR2 write is un-gated as of task #95 (commit 561f5d8). CR2 uses standard LE TIFF magic and routes through `writeTIFF` + `relocateTIFFFromParsed`.

**Why:** Canon MakerNotes use blob-relative (self-relative) offsets — verbatim blob copy is safe. Validated against real Canon EOS 350D (7.4 MB CR2): ImageDataHash IN==OUT, all MakerNote tags preserved.

NEF write is un-gated as of task #102 (commit 7d34aa5). See [[project-nef-write-ungated]].

ARW write is un-gated as of task #103. See [[project-arw-write-ungated]].

ORF write is un-gated as of task #104 (commit 2c6c9a0). Both IIRO (byte[3]=0x4F, DSLRs/OM-D) and IIRS (byte[3]=0x53, older compacts) magic variants are handled. Algorithm: patch bytes [2:4] to 0x2A 0x00 before relocation, restore original 4-byte magic in output. `format/tiff/relocate_orf.go` + `tiff.InjectWithEXIFORF` + `writeTIFFORF` in write.go.

RW2 write is un-gated as of task #104 (commit 2c6c9a0). Algorithm: save 16-byte GUID from base[8:24], remove sentinel StripOffsets (0xFFFFFFFF), extract RawDataOffset (0x0118) as standalone imageBlock, run standard relocator, patch 0x0118 inline val_or_off with new offset, insert GUID back at position 8, rebase all OOL pointers by +16, restore "IIU\x00" magic. JpgFromRaw (0x002E) OOL data is preserved automatically by exif.Encode. `format/tiff/relocate_rw2.go` + `tiff.InjectWithEXIFRW2` + `writeTIFFRW2` in write.go.

`isTIFFBased` now always returns false — all RAW formats have dedicated dispatch paths in write.go.

**How to apply:** All TIFF-based RAW formats (CR2, NEF, ARW, DNG, ORF, RW2) are now fully writable. Only ORF/RW2 (and ORF) had non-standard magic; both are handled by their respective relocators.
