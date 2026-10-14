---
name: project-makernote-ool-rebasing
description: MakerNote offset-base model (corrected 2026-10-14, task #310/#312) — which layouts are TIFF-relative, where read and write apply it
metadata:
  type: project
---

Corrected 2026-10-14 (sprint 45 effort E2b). The #127 matrix was WRONG for Canon and Panasonic:
both are TIFF-relative (metadata-extractor processMakernote base=tiffHeaderOffset; ExifTool
MakerNotes.pm no Base; corpus Panasonic_DMC-FZ30.jpg tag 0x0021 at TIFF offset 1296).

Single source of truth: `exif/makernote_detect.go` mnLayouts[].base.
- TIFF-relative: Canon, Sony, Panasonic, Casio(+QVC), Samsung, DJI, Kyocera, Sigma, Minolta,
  Nikon Type 2 (no header), Nikon "Nikon\0\x01", Olympus "OLYMP\0", Leica non-v1/4-7, SEMC, Sony CAM/DSC,
  and (write side only) unrecognised plausible plain IFDs (ExifTool MakerNoteUnknown default).
- Move-safe: Nikon Type 3, Olympus "OLYMPUS\0"/"OM SYSTEM", Fujifilm (IFD offset at bytes 8-11!), Pentax, Apple, Ricoh, Sanyo.

Read: exif.Parse resolves TIFF-relative layouts against the whole TIFF (mnView). Write:
`(*exif.EXIF).MakerNoteLayout()` → format/tiff rebaseGenericMakerNote (+ Canon footer fix) and the
CR2 rebaser (+8 inside the MakerNote). JPEG/PNG/WebP/HEIF do NOT rebase (reported, unfixed).

**Why:** tests passed for years with the wrong matrix because the reader used the same wrong base.
**How to apply:** never trust a read-back test of MakerNote values unless it asserts explicit
expected values; verify with `exiftool -validate -warning` ("Adjusted MakerNotes base").
See [[project-sprint45-e2b-subifd-makernote-irb]].
