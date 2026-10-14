---
name: project-sprint45-e3-heif-cr3-webp
description: Sprint 45 effort E3 (2026-10-14) — #282/#254/#309 HEIF iloc+item creation, #299 WebP VP8X, #300 CR3 XMP uuid/CMT3/CMT4; non-obvious facts, fuzz finds, perf traps, replay method
metadata:
  type: project
---

Effort E3 at base 62c6d0f (tasks #282 #254 #309 #299 #300); five commits replayed A→E.

Non-obvious facts established:
- iloc data_reference_index (16-bit) exists in EVERY iloc version; the old parser and ALL test fixtures
  omitted it, so tests passed while 0/43 real HEIF files yielded EXIF. Fixture-matches-bug trap: fix fixtures too.
- #254 was purely the #282 symptom (every corpus Exif item is construction_method 0; only grid items use idat).
- Raw payload bytes appended after the last top-level box => ExifTool "Unknown trailer"; wrap in a new mdat.
- A 'mime' infe is XMP only when content_type == "application/rdf+xml" (libavif rule); Galaxy/Pixel files
  carry other mime items.
- patchAncestorSize on a truncated prefix copy never patches a wrapping moov (end lies beyond the copy).
- CR3: CMT1 IFD0 has NO 0x8769, so CMT2 (ExifIFD) is never read (pre-existing, reported, not fixed).
  XMP lives in top-level uuid be7acfcb…; Canon pads it to 64 KiB. GPS read from CMT4 must be split back out
  on write or exif.Encode puts a GPS IFD inside CMT1.
- exif.Encode copies entry Values raw (no byte-order conversion) — attach foreign-order IFDs only when orders match.

Defects found by my own validation (all fixed in-task): chunkShift compared the partially shifted offset with
later thresholds (corpus chunk check caught it); FuzzHEIFInject found truncated infe revived by appended boxes,
and closeOpenEndedBox mutating data before a pass-through decision.

Perf traps: returning a 176-byte struct from extractByFormat cost +5% Read_JPEG — use original multi-return
plus a small out-pointer; keep the CR3 hook inlinable. `&comp` passed with an io.Writer escapes to heap.
Map iteration (random start, chacha8) costs ~20-40 ns — avoid redundant ranges in write paths.

Replay method: python stage builder writing stage files into a `git worktree` (corpus via `cp -cR` APFS clone;
a symlinked corpus breaks exif's WalkDir test), run gofmt/vet/test/lint per stage, keep cumulative patches.
See [[project-sprint45-e2b-subifd-makernote-irb]], [[feedback-corpuswide-bench-and-cpu-contention]].
