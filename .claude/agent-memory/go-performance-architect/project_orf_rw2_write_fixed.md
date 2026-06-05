---
name: project-orf-rw2-write-fixed
description: ORF/RW2 write corruption fixed (task #104, commit e52dd8f); OLYMP MakerNote rebase + RW2 recursive IFD GUID shift
metadata:
  type: project
---

ORF and RW2 write corruption introduced by the initial task #104 implementation (commit 2c6c9a0) was fixed in commit e52dd8f.

**ORF fix:**
- Older Olympus compacts (C5050Z, C8080, SP350, SP500UZ) use "OLYMP\x00" MakerNote format with TIFF-file-ABSOLUTE OOL offsets (not blob-relative like newer "OLYMPUS\x00" cameras).
- The external MakerNote ThumbnailImage JPEG (file-absolute, outside the MakerNote blob) must be registered as a standalone imageBlock (ifdPtr=nil, like NEF preview) so it is not dropped.
- After re-encode, all MakerNote OOL val_or_off entries are delta-rebased: in-blob values by (new_mn_abs - old_mn_abs); external ThumbnailImage pointer set to thumbBlock.newOffset.
- Architecture: `extractOlympMakerNoteInfo` → `orfRelocateWithOLYMP` → `rebaseOlympMakerNote` + `rebaseOlympMNEntry` (analogous to ARW's rebaseSonyMakerNote).

**RW2 fix:**
- After GUID insertion (+16 bytes at file offset 8), ALL IFDs reachable from IFD0 must be rebased, not just IFD0.
- Sub-IFD pointer tags (0x8769 ExifIFD, 0x8825 GPS, 0xA005 Interop) are TypeLong Count=1 = 4 bytes = "inline" but their values are absolute file offsets.
- `rebaseAllIFDsAfterGUID` recursively walks IFD0 + all sub-IFDs, shifting both OOL val_or_off fields AND inline sub-IFD pointer values by +rw2GUIDLen (16).

**Why:** Previous impl (2c6c9a0) assumed OLYMP MakerNote offsets were blob-relative (they are not for OLYMP-type), and only shifted IFD0 OOL entries after GUID insertion (missing ExifIFD pointer and all ExifIFD-internal OOL pointers).

**How to apply:** When adding MakerNote support for any new RAW format, verify whether the MakerNote uses file-absolute or blob-relative offsets before deciding whether rebasing is needed. When inserting bytes at the start of a TIFF, ALL IFDs reachable from IFD0 need offset adjustment, not just IFD0.

Commit: e52dd8f — `fix(orf,rw2): correct byte-faithful relocation for OLYMP MakerNote and RW2 GUID offset shift (#104)`

Related: [[project-orf-rw2-write-ungated]], [[project-arw-write-ungated]]
