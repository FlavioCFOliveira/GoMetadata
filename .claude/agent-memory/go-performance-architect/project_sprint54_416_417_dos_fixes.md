---
name: project-sprint54-416-417-dos-fixes
description: Sprint 54 (v1.6.0 blockers) #416 MakerNote edit index + #417 one-pass trailer scan; design, traps, pre-existing defects found
metadata:
  type: project
---

#416: `exif/makernote_edit_index.go` (range index: scan for <=64 entries, sorted starts/ends + two Fenwick trees above; release on delete/move; `appendFrom` region; <=64 extra ranges), `deleteEntries` one pass, `encodeReplacements` memo per type, `maxMakerNoteGrowth` 16 MiB+2. Corpus HAS duplicate tags (Canon PowerShot tag 0 x2, Samsung WB1100F x7, Sony DSC-P52 x4), so "refuse duplicates" was rejected. Typical-edit cost must stay at parity: an always-built index cost +12-24%; the small-table scan path fixed it.
#417: `iobuf.ScanRange` (ScanVisitor interface, look-behind/ahead window, forward-only) + `Source.IndexEach`; Source.Index removed; finders keep state in pooled structs (closures + stack buf escaped: +11 allocs, 4 KB). FotoStation chain was the same defect (4.9 s/16 MiB from a file) and is fixed too.

**Why:** pre-release security audit blocked v1.6.0 on two HIGH DoS findings.
**How to apply:** use read-count assertions (not time) for streaming regressions; differential-test against a copy of the old algorithm; any per-candidate Seek+Read in a Source finder is the bug class.

Pre-existing, unfixed (reported to coordinator): FuzzMakerNoteEdit finds in seconds a Canon note whose last value overlaps the footer lookalike ("II*\0"+4 bytes): EncodeRebased patches the footer offset over the value's tail (repros kept in scratchpad fuzzrepro/). `TestInjectLargeTrailerNotHeld` is flaky under -race (sync.Pool drops puts; 6/40 on HEAD, 1/40 now).
