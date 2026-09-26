---
name: project_sprint44_batchB_214_218_238_239
description: Sprint 44 Batch B (jpeg #214-218, root #238-239) — probe-before-clone Inject pattern, WithoutXMP extraction-level skip, discovered pre-existing extractGUIDFromMain element-content bug
metadata:
  type: project
---

Sprint 44 "Performance and Efficiency Laboratory" Batch B, 2026-09-25. Files:
format/jpeg/jpeg.go, format/jpeg/jpeg_test.go, read.go, metadata.go, bench_test.go,
read_test.go, BENCHMARKS.md.

**#214** readSegment growth: `*scratch = *iobuf.Get(need)` replaces bare `make`.
Isolated one-line change; did NOT touch the data-offset convention (see #216 note).

**#215+#238 combined** in `extractIPTCAndDigestFromIRBPayloads`: single-payload
fast path no longer clones the 0x0404 sub-slice (payloads[0] is already an
independently-owned clone from scanMetadataSegmentsWithWire — safe to alias).
Added `wantDigest bool` param: when false, digest is never cloned, which
transitively makes `computeIPTCTrustElevated` take its free nil-digest branch
in read.go with zero extra code there (no explicit `if !cfg.lazyIPTC` gate
needed — proved via reading Copyright/Caption/Keywords/Creator: when m.IPTC is
nil, iptcTrustElev's value never changes the observable branch outcome).

**#216 design decision — do NOT unify writeSegment's signature.** `writeSegment`
(w, marker, data-no-headroom) is directly unit-tested by conformance_test.go
and jpeg_test.go (TestWriteSegmentTooLarge) with a plain payload buffer — it
must stay a correct, independent, two-write function. Built THREE new helpers
instead: `stampSegmentHeader(buf, marker)` (stamps header into buf[0:4], buf
must reserve segHeaderSize=4 bytes upfront), `writeHeaderedBuf(w, marker,
*bufPtr)` (stamp + one w.Write + guaranteed single iobuf.Put), and
`writeSegmentCopy(w, marker, data)` (Get+copy+writeHeaderedBuf, for
pass-through data that has no reserved headroom — used by writeSOS and
writePassThroughSegment). Every "we already build a pooled buffer" call site
(writeEXIFSegment, writeXMPSegments fast path, writeRawXMPSegment,
writeExtendedChunks, writeExtendedXMP main stub, writeIPTCSegment,
writeIPTCSegmentRaw) now does `iobuf.Get(segHeaderSize + ...)` and writes body
starting at offset 4, one writeHeaderedBuf call. writeMarker keeps its
2-argument signature (no scratch threading needed) — just swapped the stack
composite literal for iobuf.Get(2)/Put internally. This kept the diff's test
blast radius to zero: no existing direct-call test signature changed.
Considered and REJECTED: giving readSegment's scratch buffer a 4-byte
head-room (payload at scratch[4:]) so pass-through segments could reuse it —
would have broken TestWriteSOSCopyError's manual `writeSOS(reader, ew, data)`
call with a bare `data:=[]byte{}` (no headroom to borrow). Not worth it once
writeSegmentCopy already achieves the same allocation-free single-write goal
via its own Get+copy.

**#217 design** — `probeIRBHasSiblings` (zero-copy scan, reuses parseIRBEntry)
decides whether the full cloning `extractOriginalIRB` pass is needed at all.
Rule, extracted into `resolveOrigIRB(r, scratch, rawIPTC)` (needed to dodge a
gocyclo/nestif violation in Inject): rawIPTC==nil (remove/strip mode) always
needs the clone pass (can't know in advance if anything survives stripping);
rawIPTC!=nil (replace mode) only clones when a sibling resource is actually
present — otherwise splicing == buildIRB(rawIPTC) with a bare stub, so origIRB
stays nil and Inject just writes a bare 0x0404 IRB. Do NOT attempt a per-segment
"skip clone unless this segment has a sibling" decision across MULTIPLE APP13
segments (multi-segment/IRB-APP13-09 case) — the decision can't be undone
once scratch is overwritten by the next readSegment call; a segment declared
"no sibling, skippable" can't be un-skipped if a LATER segment turns out to
have one. The safe granularity is "hasSiblings across the whole scan", decided
in one full pre-pass, with a full second pass only when true.

**#218** `appendIRBBlock`/`appendSplicedIRB` are append-based twins of
`buildIRB`/`spliceIPTCIntoIRB`, written to build directly into a pooled
segment buffer (dst arg). `buildIRB`/`spliceIPTCIntoIRB` kept as thin
freshly-allocating wrappers ONLY because existing tests
(jpeg_task62_test.go, jpeg_irb_task52_test.go, conformance_test.go) call them
directly by name.

**Bug discovered, NOT fixed (out of scope, reported to user)**: real exiftool
corpus fixture `testdata/corpus/jpeg/exiftool/ExtendedXMP.jpg` uses
ELEMENT-CONTENT form `<xmpNote:HasExtendedXMP>GUID</xmpNote:HasExtendedXMP>`,
not attribute form `HasExtendedXMP="GUID"`. `extractGUIDFromMain` only
supports attribute form (`bytes.IndexAny(rest, "\"'")` within 5 bytes) — GUID
lookup silently fails, `TestCorpusExtendedXMP`'s `RawXMP() != nil` assertion
passes anyway because it degrades to the main-only packet, masking the bug.
Discovered while building a benchmark for #238 (had to write a synthetic
`buildJPEGWithExtendedXMP` fixture in bench_test.go using attribute form to
get a benchmark that actually exercises reassembleExtendedXMPByParse).

**Verification protocol used**: `git stash push -- format/jpeg/jpeg.go read.go`
to get a true pre-change baseline for a *newly added* benchmark (one that
didn't exist before this task) against the *old* implementation, without
losing the new benchmark code itself (which lives in a different, untouched
file). Pop after capturing baseline.
