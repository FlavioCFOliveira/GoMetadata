---
name: project_orf_rw2_write_ungated
description: ORF and RW2 write un-gated (task #104, commit 2c6c9a0) — non-standard magic handling and RW2 GUID insertion
metadata:
  type: project
---

ORF and RW2 write support fully un-gated as of task #104 (commit 2c6c9a0, 2026-06-05).

**ORF (Olympus):** Two magic variants — IIRO (byte[3]=0x4F, E-series/OM-D DSLRs) and IIRS (byte[3]=0x53, C-series/SP-series compacts). Algorithm is trivial: patch bytes [2:4] to 0x2A 0x00, run standard `relocateTIFFFromParsed`, restore original 4 magic bytes. Olympus MakerNote uses blob-relative offsets (safe to copy verbatim). Format-specific file: `format/tiff/relocate_orf.go`.

**RW2 (Panasonic):** Four non-standard features require dedicated handling:
1. Magic "IIU\x00" (patch to 0x2A 0x00 for parsing, restore in output).
2. 16-byte device GUID at bytes [8:24]; IFD0 at offset 24 (not 8). After standard encode produces IFD0@8, re-insert GUID and shift all OOL offsets by +16.
3. Sentinel StripOffsets = 0xFFFFFFFF (must be removed before enumerateImageBlocks).
4. RawDataOffset (tag 0x0118, TypeLong Count=1 inline) = absolute offset to raw sensor data (standalone imageBlock, ifdPtr=nil). After assign, patch the inline val_or_off, then add +16 for GUID shift.
5. JpgFromRaw (tag 0x002E) OOL data preserved automatically by exif.Encode (entry.Value holds the JPEG bytes from Parse; Encode writes them to OOL and sets val_or_off; +16 shift applied in GUID insertion step).

Format-specific file: `format/tiff/relocate_rw2.go`.

**Key sentinels exported:**
- `tiff.ErrORFInvalidMagic`, `tiff.ErrORFOutputTooShort`
- `tiff.ErrRW2InvalidMagic`, `tiff.ErrRW2RawDataOffsetOverflow`, `tiff.ErrRW2OutputTooShort`, `tiff.ErrRW2IFD0OutOfBounds`

**Corpus validation:**
- ORF: Olympus E-M10 (IIRO), E410 (IIRO), C5050Z (IIRS, 122 strips)
- RW2: Panasonic DMC-GF1 (14.7 MB, JpgFromRaw 665600 bytes), DMC-GF7

**Why:** Non-standard TIFF magic prevented the existing write path from processing these files. ORF needed a magic-patch-and-restore wrapper. RW2 needed a full dedicated relocator for the GUID header and inline pointer tags.

**How to apply:** When implementing format-specific write support for any future format with non-standard headers, use the ORF (simple magic swap) or RW2 (header insertion with offset rebasing) pattern as templates.
