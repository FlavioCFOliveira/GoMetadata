---
name: project-robustness-audit-2026-06
description: Re-audit of GoMetadata after hardening round — six areas verified vs ExifTool/Exiv2/libexif; gap status updated June 2026
metadata:
  type: project
---

Re-audit conducted 2026-06-01 against current source. All six original blocker areas revisited.

**Why:** Verify that the hardening round resolved the blockers identified in the initial audit (makernote relocation on write, IFD1 thumbnail corruption, encoding edge cases, MWG reconciliation).

**How to apply:** Use as baseline for next planning cycle; resolved items should not be re-opened unless regression is detected.

## Status per original gap:

1. **MakerNote relocation on write — PARTIALLY RESOLVED (scoped to JPEG)**
   TIFF/RAW write is gated by isTIFFBased() → ErrWriteNotSupported. For JPEG, MakerNote bytes are preserved verbatim (exif/write.go buildExifIFDEntries). Offset adjustment for Nikon Type 3 / Fujifilm / Olympus / Panasonic / Pentax / Leica still NOT performed — the comment in write.go §216-218 explicitly acknowledges this. Risk is real for JPEG writes that touch EXIF on those cameras. Canon/Sony/DJI/Samsung are safe (plain IFD at offset 0).

2. **IFD1 thumbnail — RESOLVED**
   exif/ifd.go extractJPEGThumbnail() copies thumbnail bytes to ifd.ThumbnailData at parse time. exif/write.go writeSubIFDs() uses patchThumbnailEntries() to compute new offset for the re-encoded stream. Confirmed correct for round-trips.

3. **UserComment / XP* encoding — RESOLVED**
   exif/charset.go: full decodeUserComment() with ASCII/UNICODE/JIS/Undefined dispatch. decodeUTF16() for UNICODE prefix and decodeUTF16LE() for XP* tags. EXIF.UserComment(), XPTitle(), XPComment(), XPAuthor(), XPKeywords(), XPSubject() all implemented in exif/exif.go.

4. **XMP UTF-16/32 packet encoding — RESOLVED**
   xmp/encoding.go: detectEncoding() with BOM-based dispatch for UTF-32BE/LE, UTF-16BE/LE. normaliseToUTF8() called in both Scan() and Parse(). decodeUTF32() implemented manually (golang.org/x/text has no UTF-32 codec).

5. **xmpMM:History struct-in-list — RESOLVED**
   xmp/rdf.go: full inStructInList tracking with liItemIndex. xmp/write.go writeStructInListProperty() emits correct rdf:Seq with rdf:li rdf:parseType="Resource". Tests in xmp/xmp_test.go #485+. Round-trip confirmed.

6. **MWG DateTimeOriginal reconciliation — RESOLVED (with documented deviation)**
   metadata.go DateTimeOriginal(): EXIF > XMP priority. applyXMPTimezone() synthesises timezone from XMP when EXIF lacks OffsetTimeOriginal (0x9011). This follows MWG §2.2.1 timezone synthesis. Priority still EXIF > XMP for the timestamp value itself, which diverges from MWG's "most specific wins" rule for Photoshop files. Documented but not changed — minor acceptable deviation.

## Remaining gaps (OPEN):

- **MakerNote offset adjustment on JPEG write** — Nikon Type 3, Fujifilm, Olympus, Panasonic, Pentax, Leica have internal offsets relative to MakerNote base. Verbatim preservation is safe for read-only but breaks MakerNote tag access if the MakerNote moves in the JPEG EXIF stream. MEDIUM severity for write paths.
- **DNG SubIFDs / NewSubfileType not traversed** — DNG uses tag 0x014A SubIFDs for embedded images; not parsed on extraction.
- **ICC profile (tag 0x8773) not extracted** — absent from TIFF/DNG extraction path.
- **Out-of-scope formats** — CRW/RAF/MRW/IIQ/X3F/SRW/PEF/RWL documented in doc.go as unsupported, return UnsupportedFormatError.
- **TIFF/RAW write** — blocked by isTIFFBased() gate, ErrWriteNotSupported. Roadmap Option A (epic #33).
