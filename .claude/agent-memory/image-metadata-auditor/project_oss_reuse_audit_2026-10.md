---
name: oss-reuse-audit-2026-10
description: Permissive-licence reuse audit (2026-10-14, repo HEAD 321704f): confirmed GoMetadata defects found while measuring (HEIF iloc, WebP VP8X, CR3 XMP, XMP nested loss) and which clones fix them
metadata:
  type: project
---

Audit of permissive refs (XMP SDK, metadata-extractor, evanoberholster/imagemeta, libtiff, libavif, libwebp, libpng + bep/imagemeta, little_exif, img-parts, commons-imaging, exif-rs, libjxl). Clones live in the session scratchpad (volatile); re-clone with `git clone --depth 1` if needed.

Defects CONFIRMED empirically in GoMetadata (all pre-existing, reported to user, not fixed):
- HEIF/AVIF: `format/heif/heif.go` parseIlocItemSimple (~L1386) and parseIlocFullItem (~L555) never consume the 2-byte `data_reference_index` (libavif src/read.c:2151, bep/imagemeta imagedecoder_heif.go:352 do). Result: 0/43 corpus HEIF/AVIF files yield EXIF; Write on iPhone HEIC rewrites iloc with item_count 45->2 and corrupts it (exiftool: "No Extents for Exif item"). Existing corpus test only asserts no-panic.
- WebP: `format/webp/webp.go` buildVP8XFlags emits zero canvas (1x1) for simple VP8/VP8L files; libwebp src/dec/webp_dec.c:410 rejects canvas != image size. Fix refs: libwebp src/dec/vp8_dec.c VP8GetInfo, vp8l_dec.c VP8LGetInfo, src/mux/muxedit.c CreateVP8XChunk.
- CR3: `format/raw/cr3/cr3.go` Extract never reads the top-level XMP uuid be7acfcb-97a9-42e8-9c71-999491e3afac (xmp=0 on 11/11 corpus CR3); Inject writes an "XMP " sub-box inside the Canon uuid (non-standard, ExifTool ignores it). CMT3 (MakerNote) and CMT4 (GPS) not read. Ref: imagemeta meta/isobmff/crx.go.
- XMP: flat key model loses nested structs on Parse->Encode (MWG regions, hierarchical keywords, exif:Flash struct): 18/20 tags lost after one SetCaption via Write on a JPEG. Ref: Adobe XMPCore ParseRDF.cpp / XMPMeta-Serialize.cpp (tree model).
- TIFF/RAW: tag 0x8649 (Photoshop IRB, carries IPTC 0x0404 + digest 0x0425) not read in TIFF; only 0x83BB. Ref: XMP SDK TIFF_Handler.cpp L184-218, PSIR_MemoryReader.cpp.
- PNG: no "Raw profile type iptc/exif" reading (bep/imagemeta imagedecoder_png.go:94 as study ref; XMP SDK PNG_Handler does NOT do IPTC despite CLAUDE.md table).

Licence traps: XMP SDK per-file headers (429 files) say "Adobe license agreement accompanying it" (repo LICENSE = BSD-3) — ask legal; imagemeta (evan) Canon/Sony/Nikon tables are generated from ExifTool (GPL/Artistic) per their own comments; mdx Canon descriptor also "converted from ExifTool 10.10". Treat makernote value tables as provenance risk. libwebp ships PATENTS grant; Commons Imaging has NOTICE.txt. Repo has no NOTICE/THIRD_PARTY file today.

Registry measurements: GoMetadata tagRegistry = 152 tags (TagName only consumer); libtiff dirinfo 249 TIFF + 81 EXIF + 32 GPS; 178 libtiff TIFF tags missing (mostly DNG 0xC612+), 26 EXIF-IFD missing (ExifVersion 0x9000, Temperature..CameraElevationAngle 0x9400-9405, CompositeImage 0xA460-62, Gamma 0xA500). mdx ExifDirectoryBase 172 tags, 61 not in registry; mdx makernote Directory tags 947 total vs GoMetadata 0 named. GoMetadata MakerNote dispatch = exact Make string map, 23 keys / 12 vendors (no Apple, Kodak, Minolta, Epson/Agfa, Sanyo, Kyocera, Reconyx); mdx dispatches on prefix signatures (ExifTiffHandler.processMakernote). IPTC: 37 dataset consts vs bep/imagemeta JSON 116 datasets (metadecoder_iptc_fields.json).
