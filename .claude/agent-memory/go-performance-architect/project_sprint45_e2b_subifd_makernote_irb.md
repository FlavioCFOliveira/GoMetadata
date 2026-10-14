---
name: project-sprint45-e2b-subifd-makernote-irb
description: Sprint 45 effort E2b (2026-10-14) — #310-#314,#316: SubIFD tree model, MakerNote offset base, IRB first-wins; exiftool-verified facts and unfixed findings
metadata:
  type: project
---

Effort E2b at base cfe80d1 (tasks #310 #311 #312 #313 #314 #316).

Non-obvious facts established empirically:
- Corpus f15fca5b's "5 SubIFDs" are ONE 0x014A element (type 13) + a next-IFD CHAIN of SubIFDs;
  ExifTool names them SubIFD1..4. libtiff never links SubIFDs by next pointer, but files do.
- TypeUTF8 = 13 in exif/type.go collides with TIFF IFD type 13; ExifTool says Exif 3.0 UTF-8 is 129.
  Fixed only for IFD-pointer tags (entryTypeSize); the constant itself reported to the user, NOT changed.
- S-43 conformance requires 0x014A type 13 to round-trip unchanged (don't normalize to LONG).
- MakerNote byte order can differ from the TIFF (re-saved files; BE Panasonic/Sigma notes):
  metadata-extractor TiffReader.processIfd swap heuristic (count>0xFF && low byte 0).
- Canon MakerNote footer: last 8 bytes "II*\0"/"MM\0*" + own TIFF offset; must be rewritten on move.
- Other IPTC copies exist: AFCP trailer (AFCP_IPTC) and Nikon Capture NX (MakerNote tag 0x9ef5f6e0) — not updated.

Method that worked: copy new test files into a `git worktree` of the base to prove they fail
there; corpus sweep with tree.py (per-directory image hashes) classifies regressions vs fixes.
See [[project-makernote-ool-rebasing]], [[project-sprint45-e2-irb-alignment-makernote]].
