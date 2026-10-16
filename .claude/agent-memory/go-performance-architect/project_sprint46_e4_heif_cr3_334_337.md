---
name: sprint46-e4-heif-cr3-334-337
description: Sprint 46 E4 (#334-#337, 2026-10-15) HEIF largesize/iloc-width-0/no-silent-drop and CR3 CMT2 read+write, CMT4 removal; evidence, design choices, traps
metadata:
  type: project
---

HEIF: findMetaBoxAbs uses headerLen (flatBoxRangeHdr); rebuilt meta keeps 8/16-byte header form; iloc width 0 widened to 4 (widenIlocFields); every former Inject pass-through now errors (ErrNoMetadataContainer, ErrUnreadableItemLocations); parseIlocFull is strict (truncated list or >1024 extents = unreadable, was silent item loss).
CR3: CMT2 = stand-alone TIFF stream (Segments.ExifIFD, cr3.ParseExifStream, root attachCR3ExifIFD, CMT1-own ExifIFD unioned, CMT1 wins); splitEXIF returns cmtStreams (cmt1/cmt2/cmt4 + parsed IFDs for sameIFD); CMT2/CMT4 removed when rawEXIF lacks the IFD AND reader could expose it (CMT1 present, same byte order, parses).

**Why:** ExifTool `-GPS:all=` deletes CMT4 entirely; all 12 corpus CR3 carry a CMT4 with only GPSVersionID (Canon "no fix"), so an empty IFD would still read as GPS present.
**How to apply:** `exiftool -ExifIFD:all` counts 40 on 11 corpus CR3 because the 40th is from CTMD (Doc2); use `-G3:1` for the main document (39). SetDateTimeOriginal removes SubSecTimeOriginal by design (CMT2 38 entries after). Re-encoding comparison (2 parses + 2 encodes) cost +11% on CR3Inject; positional sameIFD compare removed it. Interop IFD in CMT2 is encoded via stub IFD0 + header offset patch (encodeExifStream).
Unfixed out-of-scope: heif parseIinf uses +8 for infe (ignores largesize); cr3.Inject silently passes through when no moov / no Canon uuid; Extract (findBox, file order, descends moov) vs Inject (top-level meta first) can pick different meta boxes.
