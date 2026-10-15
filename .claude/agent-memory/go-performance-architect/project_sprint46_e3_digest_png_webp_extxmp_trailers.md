---
name: sprint46-e3-digest-png-webp-extxmp-trailers
description: Sprint 46 E3 (#323 #325 #333 #332 #322) — verified spec clauses, design choices, perf cost, traps; no commit made by this agent
metadata:
  type: project
---

Effort E3 of rmp sprint 46 (2026-10-15): IPTC digest policy, PNG raw-profile drop, WebP Exif preamble + `XMP\0`, JPEG XMP PackageForJPEG, IPTC copies in AFCP/FotoStation/Nikon Capture.

**Spec clauses verified from local/official sources (cite these, not the task text):**
- MWG v2.0 digest rules are §4.2.3.2 pp. 26-29 (not "§3.3.1", which many older comments/tests still cite: read.go, metadata_conformance_test.go, tiffscan.go — unfixed). p. 29: Changer MUST create/update the checksum "whether changed or not"; we keep a stale digest stale (user/task policy) — documented deviation RECONCILE-06.
- PNG 3rd Ed eXIf is §11.3.4.5 (task text said §11.3.3 = textual info). XMP Part 3 §1.1.5 p. 16 (one XMP chunk), §1.1.3.1 pp. 14-15, §1.1.3.2 p. 15 (partitioning). RFC 9649 §2.7.1.5 (WebP metadata, at most one chunk of each type).
- MWG PDF saved to docs/specs (gitignored) from web.archive.org 2012 copy of metadataworkinggroup.org/pdf/mwg_guidance.pdf; original offline.

**Design choices:** digest created when IIM+XMP both written (irb.AppendSpliced createDigest arg); PNG exif/APP1/xmp raw profiles are dropped (not rewritten) when the standard chunk is written, nil payload leaves them; 'Raw profile type xmp' now read as XMP fallback (payload may start with UTF-8 BOM); xmp:Thumbnails is MOVED not deleted (Toolkit deletes); PackageForJPEG lives in xmp/jpegpackage.go over the hybrid Properties+retained-nodes model (subset() rebuilds XMP values; compactMarkup post-process removes formatting whitespace; no attribute shorthand); AFCP/FotoStation rewriting in format/jpeg/trailer.go (plan built before the copy pass; counting writer only when a trailer exists; JPEG ending in FFD9 costs one seek+2-byte read); Nikon Capture IPTC (MakerNote 0x0E01, entry 0x9EF5F6E0, LE ints) rewritten in place if it fits (zero padded) else block rebuilt and appended at end of MakerNote with count/offset patched, old bytes cleared.

**Traps:** ExifTool.jpg corpus file has AFCP NOT at EOF (chain: AFCP, FotoStation records, CanonVRD, Samsung...) and a stale IPTC digest; FotoStation IPTC copy included though not named in the task (AC needed it). zsh: quote `--include='*.go'` or grep aborts with "no matches found". Baseline tree for benchmarks via `git archive HEAD | tar -x` into scratchpad (read-only git).

**Perf (benchstat vs HEAD 19dac51+9f3b559):** read paths unchanged (allocs identical); Write_JPEG +4%, Write_TIFF +10% (digest creation, +3 allocs), JPEGInject +5% (~14 ns fixed trailer probe), Write_JPEG_ExtendedXMP 0.41 ms -> ~1.1 ms (now parses + partitions; cold path).

**Unfixed defects found:** byte-splice fallback in buildXMPResult (extended chunk matches but xmp.ParseExtended fails) keeps xmpNote:HasExtendedXMP; wrong MWG §3.3.1 citations repo-wide; NikonCaptureOffsets (MakerNote 0x0E0E) holds outer-TIFF offsets that are not updated by any NEF write.
