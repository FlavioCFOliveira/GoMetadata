---
name: iptc-xmp-tiff-types
description: IPTC 0x83BB must be TypeLong and XMP 0x02BC must be TypeByte in TIFF IFD0; writeIFD pads OOL value area to Count*typeSize; extractTagValues trims trailing zero padding
type: feedback
---

IPTC tag 0x83BB must be written as TypeLong (type code 4) and XMP tag 0x02BC must be written as TypeByte (type code 1) in TIFF IFD0. Using TypeUndefined for either tag causes `exiftool -validate` to report "Non-standard format (undef)".

**Why:** Adobe XMP Spec (TIFF Technical Note 3) specifies XMP as TypeByte. ExifTool convention (widely adopted) uses TypeLong for IPTC-NAA. Using TypeUndefined is a non-standard encoding that fails exiftool validation.

**How to apply:**
- `upsertIFD0Entry` in `format/tiff/tiff.go`: when `typ == exif.TypeLong`, set `Count = ceil(len(value)/4)` and keep the original (unpadded) Value bytes.
- `writeIFD` in `exif/ifd.go`: when `len(e.Value) < total` (OOL), zero-fill the value area gap to `total` bytes so the TIFF layout is correct.
- `extractTagValues` in `format/tiff/tiff.go` (and the copies in `orf/orf.go`, `rw2/rw2.go`): use `bytes.TrimRight(v, "\x00")` for IPTC 0x83BB values to strip alignment padding added for TypeLong. This is safe because IPTC IIM is self-framing (IIM §1.6: only 0x1C is a valid dataset start marker; trailing zeros are skipped by the IIM scanner).
- Four call sites in `format/tiff/relocate.go` and `format/tiff/relocate_nef.go` must pass `exif.TypeLong` for IPTC and `exif.TypeByte` for XMP.
- The `extractTagValues` / `extractTIFFTags` functions gain one complexity point from the trim branch; add `//nolint:gocyclo` to their declarations.

See [[tiff-two-pass-encode]] for the related TIFF write pattern.
