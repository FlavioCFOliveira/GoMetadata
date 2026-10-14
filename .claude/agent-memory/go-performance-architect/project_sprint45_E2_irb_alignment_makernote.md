---
name: project-sprint45-e2-irb-alignment-makernote
description: Sprint 45 effort E2 (2026-10-14) — #279/#280/#281 TIFF alignment + CR2 rebase, #301/#284 shared internal/irb + TIFF digest, #302 PNG raw profiles, #304 MakerNote signature detection; non-obvious facts verified with exiftool
metadata:
  type: project
---

Effort E2 of sprint 45 (tasks #279 #280 #281 #284 #301 #302 #304), validated 2026-10-14 against HEAD 31b187b.

Non-obvious facts established empirically (exiftool 13.55 oracle):
- TIFF IPTCDigest: ExifTool hashes the tag 0x83BB value WITH its LONG zero padding; the XMP Toolkit
  (TIFF_Handler.cpp ProcessXMP) accepts padded or unpadded. Writer uses irb.Digest(iim, 4); reader
  accepts both via irb.DigestCurrent.
- CR2 header bytes 12-15 are the RAW IFD (IFD3) offset (lclevy); the writer used to zero them.
- "PENTAX \0"+"II/MM" MakerNote: IFD starts at offset 10 (not 12) — Pentax K-1 II/QS1, Ricoh WG20/WG4.
- "SONY DSC"/"SONY CAM" notes were parsed from offset 0 before #304 (header read as entry count →
  hundreds of garbage entries); Sony Make family must be case-sensitive "SONY" (Sony Mobile "Sony").
- Digest policy (JPEG+TIFF): never create; refresh on IIM change; unchanged IIM keeps a stale digest.
- iptc.Parse never stores 2:00/1:00 record-version datasets in Records (by design).

Pre-existing bugs found, NOT fixed (reported): Canon MakerNote OOL offsets not rebased on CR2 write
(exiftool "Adjusted MakerNotes base"); SubIFDs of IFD1+ dropped and SubIFD arrays truncated to 1 on
TIFF write (0c84d07e, f15fca5b); "Issue 151.tif" loses ExifIFD+GPS on write; MakerNote OOL values
with TIFF-relative offsets outside the blob are dropped (counts below exiftool).

**Why:** these took corpus-wide base/new comparisons to discover; tests alone missed them.
**How to apply:** for write-path changes, compare base vs new with a scratchpad oracle module
(replace directive → worktree) + exiftool -v1 directory counts and per-IFD strip hashes; run
golangci-lint with --max-issues-per-linter=0 --max-same-issues=0 or findings are hidden.
See [[feedback-corpuswide-bench-and-cpu-contention]].
