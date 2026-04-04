---
name: Corpus gap analysis
description: Gaps identified in testdata/corpus as of 2026-04-03 audit, and how each was resolved in download.sh
type: project
---

# Test Corpus Gap Analysis (2026-04-03)

## Corpus state before this audit
1,726 files across jpeg/tiff/png/heif/webp/raw. No xmp/ directory.

## Gaps identified and resolutions

### 1. Progressive JPEG — RESOLVED
**Gap**: No progressive JPEG test files in corpus.
**Resolution**: SOURCE 9 (sindresorhus/is-progressive) added. Provides both progressive and baseline variants with MIT license.
**Files**: kitten-progressive.jpg, progressive.jpg, baseline.jpg, kitten.jpg, curious-exif.jpg
**Destination**: corpus/jpeg/progressive/

### 2. XMP-only JPEG — RESOLVED
**Gap**: No JPEG with XMP but no EXIF was in corpus (needed to test XMP-first parsing path).
**Resolution**: SOURCE 8 (exiftool) XMP.jpg (XMP-only, 10KB). SOURCE 1 (ianare) jpg/xmp/no_exif.jpg.
**Files**: corpus/jpeg/exiftool/XMP.jpg, corpus/jpeg/exif-samples/no_exif.jpg

### 3. EXIF+IPTC+XMP combined — RESOLVED
**Gap**: No single image confirmed to carry all three metadata standards simultaneously.
**Resolution**: SOURCE 8 (exiftool) ExifTool.jpg (confirmed EXIF+IPTC+XMP, 26KB). MWG.jpg also confirmed.
**Files**: corpus/jpeg/exiftool/ExifTool.jpg, corpus/jpeg/exiftool/MWG.jpg

### 4. No-metadata JPEG — RESOLVED
**Gap**: Unknown.jpg from ExifTool test suite was not in corpus.
**Resolution**: SOURCE 8 now includes Unknown.jpg (7.2 KB, no recognized metadata).
**Files**: corpus/jpeg/exiftool/Unknown.jpg

### 5. Extended/multi-packet XMP — RESOLVED
**Gap**: No file testing the case where XMP is split across multiple APP1 segments.
**Resolution**: SOURCE 8 ExtendedXMP.jpg (1.4KB, multi-packet XMP).
**Files**: corpus/jpeg/exiftool/ExtendedXMP.jpg

### 6. BigTIFF big-endian (Motorola byte order) — CONFIRMED PRESENT
**Status**: BigTIFFMotorola.tif and BigTIFFMotorolaLongStrips.tif already in corpus via SOURCE 2.
**Files**: corpus/tiff/metadata-extractor/BigTIFFMotorola.tif

### 7. Multi-page TIFF — CONFIRMED PRESENT
**Status**: multipage.tif already in corpus via SOURCE 2.
**Files**: corpus/tiff/metadata-extractor/multipage.tif

### 8. Fuji RAF — RESOLVED
**Gap**: No RAF files in corpus (copy_ext in old download.sh did not include raf extension).
**Resolution**: SOURCE 2 now copies raf extension; SOURCE 8 includes FujiFilm.raf.
**Files**: corpus/raw/metadata-extractor/*.raf, corpus/raw/exiftool/FujiFilm.raf

### 9. Pentax PEF — RESOLVED
**Gap**: PEF format not collected.
**Resolution**: SOURCE 2 now copies pef extension.
**Files**: corpus/raw/metadata-extractor/Pentax K-1 Mark II.pef

### 10. Sigma X3F — RESOLVED
**Gap**: X3F format not collected.
**Resolution**: SOURCE 2 now copies x3f extension. SOURCE 8 includes Sigma.x3f.
**Files**: corpus/raw/metadata-extractor/*.x3f, corpus/raw/exiftool/Sigma.x3f

### 11. Minolta MRW — RESOLVED
**Gap**: MRW format not collected.
**Resolution**: SOURCE 8 includes Minolta.mrw.
**Files**: corpus/raw/exiftool/Minolta.mrw

### 12. Canon CRW (legacy) — RESOLVED
**Gap**: CRW (pre-CR2 Canon format) not collected.
**Resolution**: SOURCE 2 now copies crw extension. SOURCE 8 includes CanonRaw.crw.
**Files**: corpus/raw/exiftool/CanonRaw.crw

### 13. Phase One IIQ — RESOLVED
**Gap**: No medium-format RAW coverage.
**Resolution**: SOURCE 8 includes PhaseOne.iiq.
**Files**: corpus/raw/exiftool/PhaseOne.iiq

### 14. XMP sidecar files — RESOLVED
**Gap**: No .xmp sidecar files were collected (only embedded XMP).
**Resolution**: xmp/ corpus directory created. SOURCE 2 collects xmp/ directory. SOURCE 6 collects BlueSquare.xmp, StaffPhotographer-Example.xmp. SOURCE 8 collects PLUS.xmp.
**Files**: corpus/xmp/

### 15. PNG with EXIF / PNG with no metadata / PNG with tEXt — RESOLVED
**Gap**: No explicit minimal PNG test cases.
**Resolution**: SOURCE 6 (Exiv2) adds 1343_exif.png, 1343_comment.png, 1343_empty.png.
**Files**: corpus/png/exiv2/*.png

### 16. MakerNote variants (Olympus 5 variants, Pentax 3 variants) — RESOLVED
**Gap**: MakerNote variant testing was limited.
**Resolution**: SOURCE 4 (libexif/libexif test/testdata) adds 9 explicit MakerNote variant JPEGs.
**Files**: corpus/jpeg/libexif-makernotes/

### 17. IPTC backward compatibility (older standard versions) — RESOLVED
**Gap**: Only Std2024.1 and Std2021.1 were downloaded; Core1.3 failed (404).
**Resolution**: Download now attempts Std2023.2, Std2019.1, Std2017.1, Std2016, Std2014 in addition to Core1.3.
**Note**: Core1.3 URL may still 404 — verify manually.

### 18. TIFF structural variants (planar, tiled, multi-page, compressed) — RESOLVED
**Gap**: Existing TIFF corpus was mostly camera-metadata focused, not TIFF-structure focused.
**Resolution**: SOURCE 10 (tlnagy/exampletiffs) adds mri.tif (multi-page), compression variants, planar, tiled.
**Files**: corpus/tiff/exampletiffs/

## Remaining confirmed gaps
- **Hasselblad 3FR/FFF**: No source found with freely licensed files
- **Phase One RAW beyond IIQ**: Limited to ExifTool's single sample
- **Unicode in JPEG metadata body**: 46_UnicodeEncodeError.jpg from ianare exists but tests encoder error, not embedded UTF-8 characters in field values — a hand-crafted test file would be more precise
- **Images with very large metadata blocks (>64KB XMP)**: Not confirmed present in any source; would need synthetic generation
