---
name: feedback_orf_rw2_rawexif_original_magic
description: ORF/RW2 Extract returns rawEXIF with original magic; patchRawEXIFForParse must be used before exif.Parse
metadata:
  type: feedback
---

ORF/RW2 `Extract` functions return rawEXIF with the ORIGINAL non-standard magic bytes (IIRO=0x52 0x4F, IIRS=0x52 0x53, RW2=0x55 0x00). They do NOT patch bytes[2:4] to 0x2A 0x00 before returning.

**Why:** #117 fix (commit 929ec97): callers who write back rawEXIF to disk must get a valid ORF/RW2 file; patching in-place was corrupting the output file format. The write path already reads magic from `r` directly.

**How to apply:**
- Any code that calls `exif.Parse(rawEXIF)` on an ORF/RW2 rawEXIF bytes MUST first call `patchRawEXIFForParse(rawEXIF)` (defined in `read.go`). Without this, `exif.Parse` returns `ErrUnsupportedMagic` and parsing silently fails.
- Test helpers in `write_orf_rw2_corpus_test.go` have `patchNonStandardMagicForParse` (local equivalent) for the same reason.
- The internal `parseEXIF` in `read.go` already calls `patchRawEXIFForParse`; do NOT add a second patch there.
- `relocateTIFFFromParsedORF` in `format/tiff/relocate_orf.go` also handles this correctly: it checks `isORFMagic(base)` then patches bytes[2:4] internally on a working copy.
- CR2/NEF/ARW/DNG Extract always return standard TIFF magic — no patch needed for those formats.
