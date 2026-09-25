---
name: audit-20260925-batchD-228-234
description: Sprint-44 Batch D (#228-#234) security pass — CLEARED after both HEIF-ILOC-DUPBOX-01 (HIGH) and PNG-BYTESREADERPOOL-RETENTION-01 (MEDIUM) fixed and independently re-verified; webp/riff offset-tracking and PNG writeChunk pooling verified sound throughout
metadata:
  type: project
---

**RE-AUDIT 2026-09-25 (same day, post-fix): CLEARED.** Both findings below fixed in the working
tree (still uncommitted at re-audit time) and independently re-verified — not just by reading the
fix, but by re-running my own from-scratch PoCs against the patched code.

- HEIF-ILOC-DUPBOX-01 fix: `nonIlocBoxesLen(metaContent)` is now the ONE traversal function called
  by both `newMetaBoxLen` (the arithmetic pre-estimate, replacing the old `ilocSizeDelta`) and
  `buildMetaBox`'s own body-size precompute AND its real copy loop. Because both the estimate and
  the real builder now literally call the identical function with the identical input, the
  equivalence holds BY CONSTRUCTION for any metaContent — 0, 1, 2, or N iloc boxes, with or without
  trailing bytes that fail to parse as a box header — closing the whole class of drift, not just
  the two instances found. Independently re-verified: (a) extended the maintainer's 2-duplicate-iloc
  PoC to 3 duplicate iloc boxes from scratch — still holds (extent stays in bounds, bytes match
  injected payload exactly); (b) ran both new regression tests
  (TestInjectDuplicateIlocBoxesOffsetInBounds, TestInjectMetaWithTrailingUnparseableBytesOffsetInBounds)
  — pass; (c) read `assertHEIFExtentsWithinBounds`'s fuzz-invariant scoping (Exif/mime-typed items
  only) and confirmed it is sound, not narrowing coverage: `mapPendingItems` in this fuzz target
  (fixed non-nil rawEXIF/rawXMP) adds EVERY Exif/mime-typed item to pendingByID unconditionally, so
  "items Inject computed a new offset for" and "items the invariant checks" are exactly the same
  set — non-pending items are copied verbatim from input and any pre-existing OOB offset there is
  the input's own malformation, correctly excluded; (d) ran `FuzzHEIFInject` for 75s (24.2M execs,
  10 workers) — PASS, 0 crashers, 5 new coverage-only "interesting" entries (no persisted new
  corpus files beyond the 3 the implementer had already added — not a regression, just fuzzer
  churn); (e) grepped for the old `ilocSizeDelta` name — fully removed, no dead references; (f)
  993-file real corpus (919 PNG + 30 WebP + 44 HEIF) SHA-256 byte-identical to both the pre-fix
  Batch-D state and original HEAD — zero behavioral change on legitimate files.
- PNG-BYTESREADERPOOL-RETENTION-01 fix: `putBytesReader(br)` calls `br.Reset(nil)` before every
  `bytesReaderPool.Put(br)`, at all 3 call sites (both error paths + success path) — single choke
  point, no direct `bytesReaderPool.Put` call bypasses it anywhere in the file. Independently
  re-verified via my own reflection+unsafe PoC (peeking the unexported `bytes.Reader.s` field
  after Put) reproducing the ORIGINAL retention finding methodology against the FIXED code: 0/64
  drained pooled readers retain a non-empty slice, vs. 100% retention before the fix. Also ran the
  maintainer's `TestZlibDecompressDoesNotRetainInput` (Size()/Len()-based, no reflection needed) —
  passes. -race clean on all 4 touched packages.

Both fixes are surgical (no unrelated behavior change), address the ROOT CAUSE (shared traversal /
explicit pool-clear) rather than special-casing the specific PoC, and carry regression tests +
(for the HEIF one) a strengthened fuzz invariant that would have caught this class of bug earlier.
Verdict: CLEARED for commit.


Batch D audit 2026-09-25 (uncommitted diff, base 9ea3e64): heif iloc arithmetic-delta single-pass
Inject (#229) + [4]byte box/item types (#228), heif/webp iobuf.ReadAll (#230), riff
ReadChunkHeaderAt + webp offset-tracked chunk scan (#234), png pooled writeChunk buffers (#231) +
pooled bytes.Reader in zlibDecompress (#233) + [4]byte chunk types (#232).

**HEIF-ILOC-DUPBOX-01 (HIGH, confirmed via PoC, BLOCKS commit).** `ilocSizeDelta` (heif.go) finds
only the FIRST `iloc` child box's size to compute `oldIlocLen`, but `buildMetaBox`'s copy loop
drops EVERY child box whose type == `iloc` (`if typ != boxTypeIloc { append }`). A meta box with
TWO `iloc` children (ISO 14496-12 §8.11.1 says "at most one", but nothing stops a crafted file
from having two well-formed ones) makes the real `buildMetaBox` output smaller than the arithmetic
`newMetaLen` estimate by exactly the second iloc box's size. `newItemBaseOffset` (computed from the
wrong estimate) is then baked into the final iloc box's item offset BEFORE writeHEIFOutput ever
runs — so the recorded EXIF extent offset points past the true end of the output file (or into
unrelated bytes in a larger real file). No panic; silent, spec-violating, embedded-metadata-lands-
outside-the-file corruption on Inject's public API from a single crafted, non-crashing input.
Confirmed with a from-scratch 123-byte crafted file -> 136-byte output, recorded extent [123,164)
vs actual output length 136. Full pre-existing heif test suite (incl. TestHEIFWriteIlocOffsetsPatched,
TestHEIFWriteRoundTrip, both #229-added multi-extent tests) still 100% green — this is a genuinely
NEW single-iloc-assumption regression from #229's move away from build-twice-and-measure, not a
pre-existing bug. Root cause: `ilocSizeDelta`'s box-removal accounting must mirror
`buildMetaBox`'s "sum ALL non-iloc boxes" loop exactly (sum ALL iloc-typed boxes' sizes, not just
the first found by `findInnerBox`) — or, simpler/more robust, reject a meta box with more than one
iloc child in `parseIlocFull`/`buildInjectComponents` (spec says at most one; treating 2+ as
unparseable -> writePassThrough is the ISO-conformant response anyway). 44-file real HEIF/HEIC/AVIF
corpus round-trip: 0/44 output byte differs pre-diff vs post-diff (git-stash SHA-256 compare) —
confirms mainline path unaffected, bug is isolated to the malformed-duplicate-iloc edge case.
Existing FuzzHEIFInject (15.4M execs/45s, 0 crashers) cannot catch this class of bug: its only
invariant is "must not panic", with no offset-vs-actual-output-bounds self-consistency check.
Recommend adding that invariant to FuzzHEIFInject/PoC-derived regression test once fixed.

**PNG-BYTESREADERPOOL-RETENTION-01 (MEDIUM, confirmed via reflection PoC).** #233's
`bytesReaderPool` (`format/png/png.go` zlibDecompress) pools `*bytes.Reader` values across calls.
`bytes.Reader.Reset(data)` sets an unexported `s []byte` field that is NEVER cleared before
`Put` — so after `zlibDecompress` returns, the pooled reader keeps the WHOLE compressed-chunk
input slice reachable (proven via `reflect`+`unsafe` peek at the unexported `s` field immediately
after `Put`: retained slice's `&s[0]` matched the caller's input pointer exactly). Before #233,
`bytes.NewReader(data)` was allocated fresh per call and GC-eligible the instant the caller's own
references dropped; #233 is a genuine new regression that extends that slice's lifetime by up to
Go's sync.Pool 2-generation victim-cache window (survives until the second following GC), times
up to GOMAXPROCS concurrent pool slots. Chunks up to maxPNGChunkSize (256 MiB) can reach
zlibDecompress via the compressed-tEXt/zTXt/iTXt path, so worst case is up to ~256 MiB pinned per
core beyond its natural lifetime — a genuine (if GC-bounded, not unbounded) memory-retention
regression under sustained large-metadata-PNG throughput. Not a crash, not a data-integrity bug
(Reset always overwrites `s` before the pooled reader is read again, so no stale-byte-read risk).
Remediation options: (a) `br.Reset(nil)` before `Put` to drop the reference immediately (~free,
trivial); (b) skip pooling `*bytes.Reader` for this call site per the same measure-first discipline
already applied in #232 (see [[../go-performance-architect/feedback_png_chunktype_escape.md]] — a
tiny fixed-size scratch object was NOT worth pooling there; `*bytes.Reader` holding a large slice
reference is the opposite case and pooling IS worth it for the Get/Reset/allocation-avoidance win,
just needs the nil-out before Put).

**Verified SOUND, no findings:**
- riff.ReadChunkHeaderAt (#234): trivial, caller-supplied offset used as-is; no internal validation
  needed since it's a private cross-package contract, not attacker-facing.
- webp readWebPChunks's locally-tracked `offset` accumulator (#234): proven self-consistent with
  SkipChunk's absolute `Seek(c.Offset+skip, SeekStart)` (both derive the same next-chunk-start
  value arithmetically) and with readPaddedChunk's direct sequential reads (no Seek). Traced every
  divergence scenario (odd-chunk EOF-truncated padding byte, huge/adversarial chunk.Size, duplicate
  giant skip past EOF) — all terminate cleanly via the next ReadChunkHeaderAt hitting io.EOF, same
  behavior as pre-diff Seek-based Offset discovery. No overflow: chunk.Size (uint32) + int64
  offset accumulation bounded by real bytes actually consumed (SkipChunk's absolute seek can jump
  once past EOF but the very next read terminates the loop — cannot chain).
- iobuf.ReadAll (#230, shared by heif+webp Inject): Stage-1 hard cap + Seek-based single-shot sizing
  verified; a "lying" Seek is a caller-ReadSeeker-implementation trust issue (not attacker-file-byte
  reachable — GoMetadata's threat model is untrusted bytes through a trustworthy Reader/File/
  bytes.Reader, not an untrusted Reader implementation) and even if Seek under/over-reports, the
  io.ReadFull error handling degrades to a safe partial-read-or-error, never OOB alloc beyond
  `limit`. Sub-slices of the returned buffer (webp's `original[dataStart:dataEnd]`,
  heif's `data[metaAbsEnd:]` suffix) are safe: iobuf.ReadAll's buffer is a fresh
  per-call `make([]byte, size)`, never itself pool-backed, so no cross-call aliasing hazard.
- png writeChunk pooled iobuf.Get(8)/Get(4) buffers (#231): traced every return path (2 early
  error returns + success path) — exactly one Put per Get, no use-after-Put (all reads of hdr/crcB
  happen strictly before their respective Put call).
- png buildXMPChunk pre-sized slice, [4]byte chunk-type constants (#228/#232) throughout heif/png/
  webp: all byte values verified to match original ASCII string literals exactly; no dispatch
  mismatches found.
- ilocItemSize/appendIlocItem and ilocBoxSize/buildIlocBox (#229): structurally mirror each other
  field-for-field (verified by reading both side by side) — the arithmetic-vs-actual-length
  equivalence holds for the STRUCTURE (item/extent counts, field widths), which is the part #229
  correctly identified as value-independent. The bug is specifically in the BOX-REMOVAL accounting
  (ilocSizeDelta vs buildMetaBox), not in the per-item size arithmetic.

Tooling: go build/vet clean; go test ./... all green; go test -race on heif/png/webp/riff clean;
govulncheck not runnable (go1.26 toolchain vs go1.27 `go list` mismatch — same pre-existing
environment issue recorded in [[../go-performance-architect/feedback_govulncheck_toolchain_mismatch.md]]
and prior audits, not a new gap). Fuzz: FuzzHEIFInject 15.4M/45s, FuzzHEIFExtract (baseline only),
FuzzWebPInject 11.3M/31s, FuzzWebPExtract 4.9M/25s, FuzzPNGExtract 1.5M/25s (slower — zlib-bomb-cap
path), FuzzPNGInject 1.7M/26s, FuzzRIFFRead 9.1M/25s — 0 crashers anywhere. Real-corpus SHA-256
diff (919 PNG + 30 WebP + 44 injectable HEIF files, git-stash before/after): byte-identical output,
0 regressions on legitimate files for either finding above.

Verdict: BLOCKED — CRITICAL-class (rated HIGH: silent corruption, not a memory-safety panic) until
HEIF-ILOC-DUPBOX-01 is fixed; PNG-BYTESREADERPOOL-RETENTION-01 (MEDIUM) should also be fixed before
merge per this project's "no MEDIUM+ open" release-gate norm (see [[production_readiness_20260706.md]]
and [[sprint8_clearance.md]] for that norm's provenance). No other Batch D area needs code changes.
