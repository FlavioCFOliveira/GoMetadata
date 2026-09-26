---
name: audit-20260925-batchF-288-289
description: Sprint-44 Batch F (#288-#289) security pass — CLEARED. New format/tiff/extent.go metadata-prefix scanner proven cycle-safe/overflow-safe/allocation-bounded/convergence-bounded (empirically: 39ms worst-case for a 256MiB adversarial file); write.go originalTIFFBytes correctly falls back to full re-read whenever the prefix isn't the whole file (593-file real-corpus byte-identical regression, including 70 files >4MiB where the prefix path is genuinely engaged); png.go chunk-skip/streaming-copy changes verified truncation-safe and ordering-preserving
metadata:
  type: project
---

Batch F audit 2026-09-25/26 (uncommitted diff on HEAD 84722ee): format/tiff/extent.go (new, task
#289) metadata-prefix scanner for TIFF/CR2/NEF/ARW/DNG Extract; format/tiff/tiff.go dispatch
changes; write.go/read.go/metadata.go rawEXIFIsWholeFile flag + originalTIFFBytes; format/png/png.go
(task #288) bounds-checked chunk-skip via Seek + pooled streaming verbatim-copy for Inject.

**extent.go — VERIFIED SOUND, both statically and empirically.**
- Cycle safety: `seenIFD` is a single scan-wide map shared across IFD0's chain AND every
  ExifIFD/GPSIFD/InteropIFD/SubIFD branch (marked-visited-before-recursing in `walkChain`), so a
  cross-branch cycle (e.g. a SubIFD pointing back to IFD0) is caught, not just same-branch loops.
  PoC: a 14-byte file whose IFD0 next-pointer points back to itself parses in 1.4 microseconds,
  no hang.
- Overflow safety: `fits(off, width, n)` computes `off > n` then `width <= n-off` — never a raw
  `off+width` addition — used consistently for every untrusted-offset bounds check
  (walkIFD/resolveEntryValue/walkEntry/walkRecursiveTag/walkSubIFDArray/growForJIFThumbnail all
  route through it or an equivalent `maxU64-x` pre-check before adding). `resolveEntryValue`
  guards `count*sz` against overflow before multiplying. Confirmed via the file's own doc comment
  that this fits() design was itself introduced BECAUSE FuzzTIFFExtract caught the naive
  `off+width>n` overflow during development — i.e. the class of bug the coordinator asked about
  was already found and fixed pre-audit; re-verified fixed by reading every call site.
- The "325-byte file allocated 268MB" regression: confirmed FIXED via direct PoC — a 5 MiB file
  and a 250 MiB file, both with IFD0 offset = MaxUint32 (worst-case adversarial), allocate exactly
  ~fileSize (5.06 MiB / 250.07 MiB), never anywhere near maxFileSize/268MB, via clampNeed's
  `if need > fileSize { need = fileSize }` safety clamp. Separately confirmed: in the actual call
  graph, `scanMetadataExtent` is NEVER invoked with fileSize==0/unknown (tiff.go's `seekFileSize`
  redirects a non-seekable-to-end reader to `extractWholeFile` instead, and `extractPrefixed` is
  only reached after `fileSize` is validated `> smallFileWholeReadThreshold` and `<= maxFileSize`)
  — so clampNeed's literal "unconditional clamp to fileSize first" (which would zero `need` if
  fileSize really were 0) is dead code in the current wiring, not a live bug; noted as an
  INFO-level doc/implementation mismatch (comment says "maxFileSize fallback if unknown", code
  doesn't implement that fallback), not a security finding.
- Quadratic-work / malicious-offset-forcing-many-grows: PROVEN bounded by construction, not just
  observed — `nextGrowthTarget` always takes `max(clampedNeed, 2*bufLen)`, so each pass's target
  buffer size at least DOUBLES regardless of how an attacker shapes the IFD chain; within the
  maxFileSize (256 MiB) cap this guarantees convergence in ~12-13 real passes (log2(256MiB/64KiB)),
  never the full maxExtentGrowthPasses=64 ceiling (which exists purely as generous defence-in-depth
  headroom, never the active bound). Empirically confirmed: a purpose-built "worst-case chain" file
  (IFDs placed at each doubling checkpoint, sized to maxFileSize, 256 MiB) parses in 39ms.
- BigTIFF 8-byte-offset paths use correct field widths (8-byte count, 20-byte entries, 8-byte
  val-or-off/next) throughout, mirrored consistently from parseIFDAtBigTIFF's established layout.
- SubIFD depth tracking (`depth+1` on entry into ExifIFD/GPSIFD/InteropIFD/SubIFDs, shared `depth`
  across SubIFD-array siblings) matches the standard recursive-depth pattern and is bounded by the
  pre-existing `maxSubIFDDepth` constant.

**write.go originalTIFFBytes / rawEXIFIsWholeFile — VERIFIED SOUND.**
- `m.rawEXIF` and `m.rawEXIFIsWholeFile` are set ONLY ONCE, together, in read.go's `Read()`
  constructor (`grep`-confirmed: no other assignment to `m.rawEXIF` anywhere in the codebase, and
  no SetXxx method touches either field) — so the two fields can never desynchronise for a given
  Metadata object; shallow copies of `*Metadata` preserve both consistently; repeated/concurrent
  Write calls from the same Metadata cannot desync them either (nothing mutates post-construction).
- The one residual scenario — calling `Write(differentReader, w, m)` where `m` came from `Read()`
  with a DIFFERENT original reader — is a PRE-EXISTING API-contract characteristic (the OLD,
  pre-#285/#289 code ALSO unconditionally reused `m.rawEXIF` whenever non-nil, ignoring whatever
  `r` was passed to Write); #289 does not make this worse — for the common case (flag=false, most
  real RAW files), Write now ACTUALLY RE-READS from the current `r` where the old code would have
  silently ignored it, which is a correctness IMPROVEMENT, not a regression. Not attacker-reachable
  from untrusted file bytes (requires the calling application to pass mismatched arguments).
- Empirically confirmed via a 593-file real TIFF+RAW corpus (SetCaption+Write, git-stash pre/post
  Batch F SHA-256 compare): 0 byte differences anywhere, including 70 real files >4 MiB (up to 25
  MB) where `rawEXIFIsWholeFile` is provably false (RawEXIF() length checked directly: e.g. a 24.8
  MB ARW file's prefix is only 477 KB, 1.9% of the file) — i.e. the prefix/full-reread branch is
  genuinely exercised on real files, and image data is never truncated or corrupted.

**png.go (#288) — VERIFIED SOUND.** `checkChunkTruncation`'s `pos+length+4 > total` check runs
before every skip-via-Seek AND every read, for both Extract and Inject, rejecting a chunk whose
declared length would overrun the file with an explicit "truncated chunk" error rather than
tolerating a Seek-past-EOF (confirmed via PoC: a 1MB-declared IDAT with only 10 real bytes is
rejected by both Extract and Inject with the exact documented error). 2^31-1 spec-max length is
still rejected via the pre-existing maxPNGChunkSize cap. Zero-length chunks (tested: 100,000
zero-length tEXt chunks) always advance `pos` by at least 4 (the CRC), no infinite loop, sub-second
processing for both Extract and Inject. Chunk ordering preserved: eXIf/XMP still inserted
immediately after IHDR is copied through; the synthetic-IEND fallback for a source missing IEND
entirely is still present and confirmed via PoC. streamCopyN's pooled 64 KiB buffer has exactly one
Put per call on every return path (2 early-error exits + 1 success), no use-after-Put; its
per-chunk-transient retention matches the SAME already-audited iobuf contract used everywhere else
in this codebase (Get does not zero; not a new retention pattern). The "never repair" CRC policy
(Inject preserves a chunk's original CRC verbatim even if invalid, no verification during Inject)
is a confirmed INTENTIONAL, documented, tested decision (TestInjectPreservesOriginalCRCEvenWhenInvalid/
TestInjectPreservesOriginalCRCOnUnchangedIHDR both exist and pass) — not a Batch F regression to
flag; Extract's own CRC verification policy for eXIf/iTXt/tEXt/zTXt/IHDR is unchanged.

Tooling: go build/vet clean; go test ./... all green; go test -race on root, format/tiff,
format/png clean; govulncheck not runnable (pre-existing go1.26/go1.27 toolchain mismatch, same as
every prior audit). Fuzz (all PASS, 0 crashers): FuzzTIFFExtract 60s, FuzzTIFFInject 45s,
FuzzPNGExtract 40s (6.9M execs), FuzzPNGInject 40s (6.8M execs), FuzzRead 45s (3.2M execs),
FuzzCR2Inject/FuzzARWInject/FuzzNEFInject/FuzzRW2Inject/FuzzORFInject/FuzzDNGInject 35s each.
(FuzzTIFFExtract/FuzzTIFFInject/FuzzRead/RAW-family execs-per-sec plateaued at 0/sec mid-run on
several targets — a known, previously-documented sandbox/reporting quirk, not a hang: every run
still completed and reported PASS at its full requested duration.)

Verdict: CLEARED for commit.
