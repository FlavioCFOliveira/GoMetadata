---
name: Test corpus sources
description: 12 public repos/sources for image metadata test images; confirmed file lists, download URLs, and edge-case coverage per source as of 2026-04-03
type: reference
---

# Image Metadata Test Corpus Sources

All sources are incorporated in testdata/download.sh.

## SOURCE 1: ianare/exif-samples
**URL**: https://github.com/ianare/exif-samples
**License**: Not explicitly stated (public test images)
**Key content**:
- jpg/tests/: 13 edge-case JPEGs (type errors, GPS IFD, Unicode errors, zero-length strings, memory errors, IndexError)
- jpg/invalid/: 7 malformed JPEG files (image00971.jpg through image02206.jpg)
- jpg/gps/: 9 Nikon DSCN GPS-tagged JPEGs
- jpg/orientation/: 16 orientation-tagged JPEGs (landscape_1-8, portrait_1-8)
- jpg/xmp/: no_exif.jpg (XMP-only, no EXIF), BlueSquare.jpg
- tiff/: 8 TIFF files including Crémieux11.tiff (Unicode filename test)
- heic/, heic/mobile/: iPhone 13 Pro Max, Nokia 8.3 5G HEIC/HEIF

## SOURCE 2: drewnoakes/metadata-extractor-images
**URL**: https://github.com/drewnoakes/metadata-extractor-images
**License**: Varies per file (community contributed)
**Key content** (CONFIRMED via API):
- tif/BigTIFF/BigTIFF.tif: BigTIFF Intel/LE
- tif/BigTIFF/BigTIFFMotorola.tif: BigTIFF Motorola/BE (explicit big-endian test)
- tif/BigTIFF/BigTIFFSubIFD4.tif, BigTIFFSubIFD8.tif: SubIFD traversal tests
- tif/multipage.tif: multi-page TIFF (1.5 MB)
- tif/ImageTestSuite/: ~45 hash-named TIFF edge cases
- png/PngSuite/: ~175 official PNG test suite files
- png/photoshop-8x12-rgb24-all-metadata.png: EXIF+IPTC+XMP in PNG
- png/sampleWithExifData.png: EXIF in PNG
- png/photoshop-8x12-rgb24-interlaced.png, rgba32-interlaced.png: interlaced PNG
- png/invalid-iCCP-missing-adler32-checksum.png: corrupt PNG chunk
- webp/: 15 WebP files covering VP8, VP8L, VP8X, lossless, lossy, animated
- raf/: FujiFilm EX-2, FinePix S5500, X-E1
- x3f/: Sigma DP1 Quattro, DP2 Quattro
- pef/: Pentax K-1 Mark II
- xmp/: 4 sidecar XMP files (digiKam, jPhotoTagger, ExifTool 9.74, APhotoManager)

## SOURCE 3: libexif/libexif-testsuite
**URL**: https://github.com/libexif/libexif-testsuite
**Images/**: 4 files: canon-eos-m6mark2.jpg, canon-powershot-g2.jpg, nikon-z6.jpg, samsung-s9.jpg

## SOURCE 4: libexif/libexif test/testdata
**URL**: https://github.com/libexif/libexif (test/testdata path)
**Key content**: 9 MakerNote variant JPEGs with .parsed golden files
- canon_makernote_variant_1.jpg
- fuji_makernote_variant_1.jpg
- olympus_makernote_variant_2.jpg through _5.jpg (4 variants)
- pentax_makernote_variant_2.jpg through _4.jpg (3 variants)
**Download pattern**: https://raw.githubusercontent.com/libexif/libexif/master/test/testdata/{filename}

## SOURCE 5: IPTC reference images (iptc.org)
**URL**: https://iptc.org/standards/photo-metadata/reference-images/
**License**: Official IPTC standard reference images (freely distributable for testing)
**Available versions** (CONFIRMED via web):
- Std2024.1: https://iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2024.1.jpg
- Std2023.2: https://iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2023.2.jpg
- Std2021.1: https://www.iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2021.1.jpg
- Std2019.1: https://www.iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2019.1.jpg
- Std2017.1: https://www.iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2017.1.jpg
- Core1.3: https://www.iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Core1.3.jpg
- Std2016: https://iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2016_large.jpg
- Std2014: https://iptc.org/std/photometadata/examples/IPTC-PhotometadataRef-Std2014_large.jpg
**Note**: Core1.3 failed to download in prior runs (404 possible). Each image has every field populated with its own field name as value.

## SOURCE 6: Exiv2/exiv2 test/data
**URL**: https://github.com/Exiv2/exiv2 (test/data path)
**Key content** (CONFIRMED via API):
- HEIC: Stonehenge.heic, IMG_3578.heic, 2021-02-13-1929.heic
- PNG: 1343_comment.png (tEXt chunk), 1343_exif.png (EXIF in PNG), 1343_empty.png (no metadata)
- XMP sidecars: BlueSquare.xmp, StaffPhotographer-Example.xmp
- TIFF: Reagan.tiff, ReaganLargeTiff.tiff
- Multiple *_poc files: fuzzing/security inputs
- exiv2-fujifilm-finepix-s2pro.jpg: Fuji MakerNote
- exiv2-sigma-d10.jpg: Sigma MakerNote
- Canon-R6-pruned.CR3: Canon CR3

## SOURCE 7: adobe/XMP-Toolkit-SDK (samples/testfiles)
**URL**: https://github.com/adobe/XMP-Toolkit-SDK
**Key content**: XMP-heavy JPEGs and TIFFs from Adobe's reference XMP implementation
**Note**: Only 6 JPEGs and 2 TIFFs at time of audit — small but authoritative

## SOURCE 8: exiftool/exiftool (t/images)
**URL**: https://github.com/exiftool/exiftool (t/images path)
**License**: Artistic License (Perl standard)
**Key files** (all CONFIRMED via API, sizes in bytes):
- ExifTool.jpg (26,106): EXIF + IPTC + XMP combined
- MWG.jpg (2,255): Metadata Working Group compliance
- GPS.jpg (2,133): GPS IFD with coordinates
- IPTC.jpg (9,851): IPTC-focused JPEG
- XMP.jpg (10,314): XMP-only JPEG (key gap filler — no EXIF)
- ExtendedXMP.jpg (1,380): multi-packet extended XMP
- Unknown.jpg (7,199): JPEG with no recognized metadata
- ExifTool.tif (4,864): TIFF with EXIF+IPTC+XMP
- GeoTiff.tif (2,660): GeoTIFF with projection metadata
- BigTIFF.btf (384): BigTIFF format (minimal)
- FujiFilm.raf (38,452): Fuji RAF raw
- PhaseOne.iiq (3,364): Phase One IIQ medium format
- CanonRaw.crw (not sized): Canon CRW legacy format
- Minolta.mrw (2,596): Minolta MRW raw
- Sigma.x3f (1,464): Sigma X3F raw
- PLUS.xmp (4,763): PLUS licensing XMP sidecar

## SOURCE 9: sindresorhus/is-progressive
**URL**: https://github.com/sindresorhus/is-progressive
**License**: MIT (confirmed — license file present in repo)
**Key files** (CONFIRMED via API):
- kitten-progressive.jpg: progressive JPEG encoding
- progressive.jpg: progressive JPEG encoding
- baseline.jpg: baseline JPEG encoding
- kitten.jpg: baseline JPEG encoding
- curious-exif.jpg: EXIF data in baseline JPEG
**Download base**: https://raw.githubusercontent.com/sindresorhus/is-progressive/main/fixture/

## SOURCE 10: tlnagy/exampletiffs
**URL**: https://github.com/tlnagy/exampletiffs
**Key files** (CONFIRMED via API, sizes):
- mri.tif (230,578): multi-page TIFF (MRI slices)
- shapes_tiled_multi.tif (37,604): tiled multi-page TIFF
- shapes_lzw.tif (11,530): LZW compressed TIFF
- shapes_deflate.tif: deflate compressed TIFF
- shapes_uncompressed.tif: uncompressed TIFF
- shapes_lzw_planar.tif: planar storage TIFF
- shapes_lzw_palette.tif: palette TIFF
- 4D-series.ome.tif: OME-TIFF with XML in ImageDescription
**Download base**: https://raw.githubusercontent.com/tlnagy/exampletiffs/master/
