---
name: project-makernote-ool-rebasing
description: MakerNote OOL offset convention matrix and #127 fix location (commit 07c7355)
metadata:
  type: project
---

Audit finding #127 fixed in commit 07c7355.

Per-maker OOL offset convention and current status:

| Maker | Convention | Status after #127 |
|-------|-----------|----------------|
| Olympus OLYMP-type ("OLYMP\x00" prefix, 6 bytes) | TIFF-absolute | CORRECT (rebaseGenericMakerNote) |
| Olympus OLYMPUS-type ("OLYMPUS\x00" prefix, 8 bytes) | Blob-relative | CORRECT (verbatim copy safe) |
| Panasonic ("Panasonic\x00\x00\x00" prefix, 12 bytes) | Blob-relative | CORRECT (verbatim copy safe) |
| Sony plain-IFD (no prefix, DSLR-A/ILCE/SLT/Cybershot) | TIFF-absolute | CORRECT (rebaseGenericMakerNote) |
| Nikon Type-3 ("Nikon\x00" prefix, embedded TIFF header at blob[8]) | Embedded-TIFF-relative | CORRECT (self-contained blob) |
| Nikon Type-1 (plain IFD, big-endian) | TIFF-absolute | CORRECT (treated as Sony plain-IFD by isSonyPlainIFDMakerNote) |
| Canon (plain IFD at offset 0) | Blob-relative | CORRECT (verbatim copy safe) |

Key files:
- `format/tiff/relocate_makernote.go`: `rebaseGenericMakerNote`, `isOlympTypeMakerNote`, `isSonyPlainIFDMakerNote`
- `format/tiff/relocate.go` lines ~284 and ~340: rebasing called on both short-circuit and main path
- `format/tiff/relocate_arw.go` `rebaseSonyMakerNote`: upper-bound skip fixed (#127)
- `exif/write.go` `buildExifIFDEntries`: comment corrected

**Why:** exif.Encode copies MakerNote verbatim. Makers with TIFF-absolute OOL pointers get stale pointers after relocation. Only blob-relative and self-contained (Nikon Type-3) blobs are safe to copy verbatim.

**How to apply:** When adding new maker support or modifying MakerNote handling, check the convention first. For TIFF-absolute makers, `rebaseGenericMakerNote` in `relocate_makernote.go` must be updated. For ARW-specific Sony rebasing, also check `rebaseSonyMakerNote` in `relocate_arw.go`.
