---
name: project-gometadata
description: GoMetadata — pure Go EXIF/IPTC/XMP library; module github.com/FlavioCFOliveira/GoMetadata
metadata:
  type: project
---

Project is "GoMetadata"; module `github.com/FlavioCFOliveira/GoMetadata`; package `gometadata`. Pure Go library for reading and writing EXIF, IPTC, and XMP metadata from/to JPEG, TIFF, PNG, HEIF/HEIC, WebP, and RAW formats (CR2, CR3, NEF, ARW, DNG, ORF, RW2).

**Why:** Performance parity with libexif/Exiv2; zero/near-zero allocation on hot path; lazy parsing; universal format API.

**How to apply:** All spec answers must be immediately actionable as Go code. Performance implications must be surfaced. All findings should cite spec section and file:line.
