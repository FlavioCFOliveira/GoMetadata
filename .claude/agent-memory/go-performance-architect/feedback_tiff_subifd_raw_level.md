---
name: feedback_tiff_subifd_raw_level
description: SubIFD relocation must be done at raw-TIFF level, not via exif.Encode model, because exif.Parse/Encode doesn't model 0x014A SubIFDs
type: feedback
---

SubIFD (0x014A) relocation must be implemented at the raw-TIFF byte level within format/tiff/relocate.go, NOT by extending exif.Encode's struct model.

**Why:** exif.EXIF only models IFD0/ExifIFD/GPSIFD/InteropIFD/IFD1. Tag 0x014A is preserved as opaque TypeLong bytes. Extending the exif struct to model arbitrary SubIFDs would require major API changes. The relocate layer already does structural rewriting beyond what exif.Encode handles.

**How to apply:**
- extractRawIFD: capture a verbatim copy of each SubIFD block (2+count×12+4 fixed block + ALL OOL value arrays including strip/tile index arrays).
- patchRawIFDOffsets: patch both element values AND the IFD entry's valOrOff pointer for OOL arrays (new absolute position = si.newOffset + relOff).
- patchSubIFDPointers: after exif.Encode, scan finalTIFF's IFD0 for 0x014A and overwrite its pointer array.
- Append SubIFD rawBytes to finalTIFF BEFORE image blocks, then append image blocks.

Key bug: OOL array patching requires BOTH updating the array elements AND updating the entry's valOrOff field (bytes 8-11 in the 12-byte entry) to the new absolute position. Updating only the elements leaves a stale pointer and the parser reads garbage.

[[feedback_tiff_two_pass_encode]]
