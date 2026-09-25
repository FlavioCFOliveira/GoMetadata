---
name: audit-findings-20260925-jpeg-sprint44-batchb
description: Focused security pass on uncommitted sprint-44 Batch B (#214-#218,#238,#239) touching format/jpeg/jpeg.go, read.go, metadata.go
metadata:
  type: project
---

Audit date 2026-09-25, base commit f6d5907 (uncommitted diff). Scope: format/jpeg/jpeg.go
(readSegment scratch via iobuf #214, single IPTC clone-elision in
extractIPTCAndDigestFromIRBPayloads #215, writeHeaderedBuf/stampSegmentHeader/writeSegmentCopy
#216, probeIRBHasSiblings/resolveOrigIRB pre-scan skip #217, appendSplicedIRB/appendIRBBlock
in-place append #218, ExtractFullSelective #238), read.go/metadata.go (Metadata.RawSegments
read-only accessor #239).

**Verdict: CLEARED.**

Evidence:
- go vet ./... clean; go build ./... clean.
- go test -race ./format/jpeg/... PASS (full suite, includes all pre-existing #174/#217
  sibling-preservation and #122/#134 extended-XMP validation tests).
- FuzzJPEGExtract 60s: 20,064,838 execs, 0 crashers, 101 new corpus entries.
- FuzzJPEGInject 60s: 17,603,930 execs, 0 crashers, 8 new corpus entries.
- **Differential round-trip**: wrote a harness (ExtractFull -> Inject with rawXMPWire
  preferred) over the full 1724-file real-image corpus under testdata/ (1507 valid JPEGs,
  217 non-JPEG/errors skipped identically both runs). Ran it against the working tree
  (post-diff) and against `git stash` of the diff (pre-diff, same commit f6d5907). SHA-256 of
  every one of the 1507 Inject() outputs was byte-identical pre/post-diff — zero output
  regression from #214-#218/#238 across a large real-world corpus. Working tree cleanly
  restored via stash pop afterward (`git status` matched original before/after).

Findings against the 6 specific checks:
1. **No retained-result aliasing of pooled/iobuf/caller memory.** rawEXIF/rawXMP/rawIPTC in
   processAPP1Segment/processAPP13Segment/appendExtendedXMPChunk are always `bytes.Clone`'d off
   scratch before storage. #215's clone-elision is narrowly scoped: `payloads[0]` (the sole
   accumulated APP13 buffer) is itself always a `bytes.Clone` of scratch taken at accumulation
   time in scanMetadataSegmentsWithWire — an independently-owned heap slice, never iobuf/pooled
   memory and never scratch itself — so returning a sub-slice of it without a second clone is
   safe. Confirmed no code path returns a sub-slice of the *scratch buffer itself.
2. **iobuf Get/Put balance.** Traced every Get/Put pair in writeEXIFSegment, writeXMPSegments,
   writeRawXMPSegment, writeExtendedChunks, writeIPTCSegmentRaw, writeIPTCSegment, writeMarker,
   writeSegmentCopy, writeHeaderedBuf, readSegment's grow-path, and Inject's unified scratch
   (#216 merged preScratch + injectScratch into one buffer reused across pre-scan/SOI/main-copy
   phases, each phase fully consuming `data` before the next readSegment call — safe reuse, no
   new UAF). All paths balanced 1:1; early-return branches in writeIPTCSegmentRaw/writeIPTCSegment
   correctly Put before returning on the size-guard error path. No double-Put, no use-after-Put.
3. **#217 sibling preservation.** probeIRBHasSiblings/resolveOrigIRB correctly gate the
   preserving clone pass: rawIPTC==nil always takes the full extractOriginalIRB path (needed to
   know what remains after stripping); rawIPTC!=nil only skips the clone pass when
   irbHasSibling() is false for every APP13 segment, which is exactly the condition under which
   a splice would reproduce bare buildIRB(rawIPTC) bytes. writeIPTCOrSiblings's call to
   writeIPTCSegmentRaw was correctly updated in lockstep with writeIPTCSegmentRaw's semantic
   change (it now strips 0x0404 internally via appendSplicedIRB(b, origIRB, nil) instead of
   receiving a pre-stripped buffer) — verified only one caller exists, no stale-caller mismatch.
   Confirmed via the full corpus differential (many files have multi-APP13 / sibling 0x0425
   resources) and via existing task-174/217 test files (jpeg_task62_test.go etc.), all still
   passing.
4. **65535 length guards / even-padding intact, output identical.** stampSegmentHeader/
   writeHeaderedBuf reproduce the exact same `length := bodyLen+2; if length>65535` guard as the
   old writeSegment. appendIRBBlock/appendSplicedIRB reproduce byte-identical padding logic to
   the pre-#218 buildIRB/spliceIPTCIntoIRB (confirmed line-by-line against the diff and via the
   corpus SHA-256 match).
5. **#238 never drops a wanted segment or the MWG-02 digest.** wantDigest=false (WithoutIPTC)
   forces iptcDigest nil, which forces computeIPTCTrustElevated to its default-false branch —
   but Copyright/Caption/Keywords/Creator all gate on `m.IPTC != nil` first in both the elevated
   and non-elevated branches, and m.IPTC is itself nil whenever WithoutIPTC is set (parseIPTC's
   own lazy check) — so the flag is provably a no-op exactly as documented, confirmed by reading
   all four accessor bodies. wantXMP=false skips only the reassembly parse/merge/encode; rawXMP
   degrades to the main packet and rawXMPWire still carries the full main+extended payload, so
   Write() stays byte-stable (also confirmed by the corpus differential, which always prefers
   rawXMPWire when present). extTruncated is still computed/returned regardless of wantXMP.
6. **#239 RawSegments() relocation-base risk — documented, not memory-unsafe, but a genuine new
   data-race surface.** `RawSegments()` returns m.rawEXIF/rawIPTC/rawXMP directly (no
   bytes.Clone), unlike RawEXIF()/RawIPTC()/RawXMP() (#139). The doc comment explicitly warns
   the caller must not mutate/retain across a subsequent Set*/Write call. Since m.rawEXIF etc.
   are set once at construction and never reassigned (verified: only write site is the Read()
   struct literal), Write() reading them concurrently is safe *only if* no caller mutates the
   returned view. If a caller does write into the returned slice while another goroutine calls
   Write(), that is a genuine, `-race`-detectable data race (not present before #239, since every
   prior raw accessor cloned). This requires deliberate API misuse by the embedding program, not
   attacker-controlled image bytes alone — LOW/INFO, accepted zero-copy performance trade-off
   given the explicit documentation, consistent with the project's ultra-performance mandate.
   Recommend (not blocking): strengthen the doc comment to name the concurrent-Write data race
   explicitly (today it says "may corrupt... a concurrent Write call" without using the word
   "race"), so callers with concurrent access patterns are not surprised by `-race` failures
   that are entirely their own doing.

No CRITICAL/HIGH/MEDIUM findings. One LOW/INFO (RawSegments doc wording, see above) — informational
only, not a blocker.
