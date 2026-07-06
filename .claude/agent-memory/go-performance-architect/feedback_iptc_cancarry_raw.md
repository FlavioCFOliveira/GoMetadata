---
name: feedback_iptc_cancarry_raw
description: canCarryIPTC must return true for all 6 TIFF-based RAW formats (DNG/CR2/NEF/ARW/ORF/RW2); CR3/PNG/WebP/HEIF/AVIF return false
metadata:
  type: feedback
---

canCarryIPTC must return true for: JPEG, TIFF, DNG, CR2, NEF, ARW, ORF, RW2.
It must return false for: CR3, PNG, WebP, HEIF, AVIF, Unknown.

**Why:** TIFF-based RAW formats all embed IPTC via IFD0 tag 0x83BB on write (via relocate.go / relocate_*.go). CR3 uses ISO BMFF with CMT UUID boxes — no IPTC pathway. PNG/WebP/HEIF/AVIF have no IPTC pathway either.

**How to apply:** Any time canCarryIPTC is touched, verify the full truth table. The gate test TestCanCarryIPTC_TIFFBasedRAW in metadata_audit_test.go locks this in. Prior to audit finding #110, only JPEG and TIFF returned true, silently dropping IPTC for all 6 RAW formats.

See also: [[feedback_iptc_xmp_tiff_types]] (IPTC TypeLong in TIFF IFD0).
