---
name: task371-nikon-capture-overlap
description: #371 fuzz finding - Nikon Capture 0x0E01 data overlapping the MakerNote IFD; fix in findNikonCaptureEntry
metadata:
  type: project
---

FuzzRewriteNikonMakerNoteIPTC "no tag 0x0E01": the data named by 0x0E01 overlapped the embedded TIFF header/IFD, so `clear(old data)` in the grow path erased the IFD entry itself. Library defect (not oracle). Fix: findNikonCaptureEntry rejects data overlapping [hdr,hdr+8) or [ifd, ifd+2+12*count+4). Regression: TestRewriteNikonMakerNoteIPTC_DataOverlapsIFD + seed.

**Why:** a rewrite of untrusted MakerNote must never clear bytes it does not own.
**How to apply:** any "clear old region, append new" relocation needs an overlap check against the structure that points to it.
Sprint-46 fuzz sweep (15 targets x 300s) found no other failure.
