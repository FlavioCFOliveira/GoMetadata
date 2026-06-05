---
name: feedback-tiff-word-align
description: TIFF 6.0 §2 word-alignment: ifdTotalSize and writeIFD must cooperate; all IFD blocks have even total size
type: feedback
---

TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary." (Word = 2 bytes.)

## The problem (task #99)

`writeIFD` placed OOL values back-to-back in the value area without padding, so values following an odd-length predecessor (e.g. a 7-byte ASCII string or 2389-byte XMP blob) started at odd file offsets. `exiftool -validate` reported "[minor] Odd offset for tag X".

## The fix

Two invariants must hold together:

1. **`writeIFD` inserts a 1-byte 0x00 pad** before each OOL value that would otherwise start at an odd running offset.

2. **`writeIFD` appends a trailing 1-byte 0x00 pad** if the final IFD block size is odd — ensuring every IFD block has an even total byte count.

3. **`ifdTotalSize` mirrors both invariants** (inter-value padding + trailing round-up) so `computeIFDOffsets` still places each IFD block at the correct offset.

## Key invariant

`ifdTotalSize` always returns an even number. This guarantees:
- header(8) + ifd0_size(even) = exifStart(even)  
- exifStart(even) + exifSize(even) = gpsStart(even)  
- ... every sub-IFD starts at an even absolute offset

Since `startOff` is always even for all IFDs (by induction), the first OOL value in each IFD always starts at `startOff(even) + fixed_size(even) = even`. No padding is needed for the first OOL entry; padding is only needed when a preceding OOL entry has odd byte count.

## SubIFD raw blocks (format/tiff/relocate.go)

Raw SubIFD blocks appended in step 11 also need word-alignment:
- `assignSubIFDOffsets` skips 1 byte (counts it as padding) before any block that would start at an odd offset.
- `computeSubIFDsSize` mirrors this padding.
- The step-11 append loop inserts `0x00` before any block that needs it.

## Evidence

Before fix: cramps.tif.written had 2 odd-offset warnings; pentax.dng.written had 19.
After fix: cramps.tif.written — OK; pentax.dng.written — OK (0 warnings introduced by our library).

**Why:** The one remaining warning in canon_r3.cr3.written (ExifIFD tag 0x9011 in CMT2) is pre-existing in the original Canon CR3 file — it lives in an unmodified raw TIFF box (CMT2 = Canon's ExifIFD box) that our library passes through verbatim.
