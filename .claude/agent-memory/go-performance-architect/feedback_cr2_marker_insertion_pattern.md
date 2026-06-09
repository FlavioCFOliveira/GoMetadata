---
name: feedback_cr2_marker_insertion_pattern
description: CR2 write: insert marker+shift-offsets pattern (not overwrite); mirrors RW2 GUID insertion
metadata:
  type: feedback
---

Never overwrite IFD0 bytes with a proprietary marker when `exif.Encode` places IFD0 at offset 8. Instead, **insert** the marker bytes at position 8 and **rebase** all IFD absolute offsets by the insertion length.

**Why:** `exif.Encode` hardcodes IFD0 at offset 8 (`headerSize = 8` in `exif/write.go`). Overwriting bytes [8:12] with a proprietary marker corrupts IFD0's entry-count field, making `exif.Parse` read a nonsensical entry count and fail.

**How to apply:**
- For any format with a fixed-length proprietary header after byte 8 (e.g., CR2 marker = 8 bytes, RW2 GUID = 16 bytes): use the insert-and-rebase pattern.
- The reference implementation is `insertRW2GUIDAndShiftOffsets` + `rebaseAllIFDsAfterGUID` in `format/tiff/relocate_rw2.go`.
- For CR2: `insertCR2MarkerAndShiftOffsets` + `rebaseAllIFDsAfterCR2Marker` in `format/tiff/tiff.go` (delta=8).
- After insertion, IFD0 offset in the TIFF header bytes [4:8] must be updated from 8 to `8 + insertionLen`.
- All OOL val_or_off fields where oldVOO >= 8 (the insertion point) must be shifted by +insertionLen.
- Inline sub-IFD pointer tags (0x8769 ExifIFD, 0x8825 GPS, 0xA005 Interop) must also be shifted AND recursively rebased.
