---
name: v150-preclearance-lessons
description: v1.5.0 pre-release audit (2026-10-16, HEAD 3d74da6) — reusable attack-class lessons for the sprint 45/46 rewrite; verdict was BLOCKED
metadata:
  type: project
---

Verdict at HEAD 3d74da6: BLOCKED (1 CRITICAL, 5 HIGH, 4 MEDIUM). Blind public-API fuzzing (90s/target) and the release manager's evidence found NONE of these; every one was found by reading code plus a hand-built PoC or a structure-aware random generator.

**Why it matters / how to apply:** for this codebase, `go test -fuzz` with corpus seeds plateaus (exec counter stalls in the sandbox) and misses shape-specific bugs. Next audit: start with structure-aware generators (scratchpad module with `replace` to the repo; build HEIF/CR3/TIFF/JPEG from interesting-value tables incl. 2^64-k) and amplification probes (output/alloc ratio, wall time vs size), not blind fuzz.

Attack classes that were NEW in v1.4.0..HEAD (regressions; v1.4.0 behaved fine, verified via `git archive v1.4.0` + replace):
- HEIF iloc `itemLength` sums extent lengths with unchecked uint64 `+=` -> wrap -> `payload[filled:filled+len]` panic (format/heif/iloc.go). Same function allocates `make(total)` before checking file size (168-byte file -> 500 MiB).
- HEIF `patchAncestorSize*` walks the WHOLE file past the meta box; box with size==1/largesize<16 or a size32<8 chain lands on largesize==0 -> infinite loop on Write.
- internal/trailer FindAFCP: O(n^2) (candidate x EOF-record scan); 512 KiB trailer = 15 s.
- xmp/xmlscan.go: `resolve()` linear scope scan (O(decls x attrs)), `extra +=` concat (O(n^2)), and NO cap on element/attr count (15 MiB doc -> 3 GiB alloc).
- exif/private_ifd.go: per-entry walker budget, no global budget (N type-13 IFD0 entries x 16 MiB carry).
- format/png raw profiles: every matching zTXt chunk is zlib-decompressed (64 MiB cap each, no first-wins) on Read AND Write. Breaks the old "<=1 decompression per Extract" invariant.
- format/tiff subIFDWalker.chain: next-IFD chains are not charged to the image-block budget (3 MiB file -> 206 MiB alloc).

Pre-existing (also in v1.4.0): exif chain IFD thumbnails each re-copied on Encode (500x); duplicate/overlapping StripOffsets blocks stream N x data on TIFF Write (2000x).

Checked and CLEAN: XMP entity handling (no DTD expansion), XMP arena aliasing after input overwrite, XMP injection round-trip well-formedness, CR3 write path (40k structured x4 ops), TIFF/JPEG structured write (320k/120k), Olympus ExternalOffsets, MakerNote walker/rebase, Nikon Capture rewrite/offsets, cr2Rebaser, tiffscan PSIR, webp vp8x, irb package, govulncheck 0.

## Re-audit of fix commit f6f5291 (HEAD 3cd3831) — 2026-10-16 — verdict BLOCKED (1 HIGH, new)

All 10 original PoCs re-run: F-01/F-09 (168-byte HEIF: err, 0 MiB alloc), F-02 (no hang), F-03 (512 KiB trailer: 136us), F-04 (320k ns decls 167ms parse), F-05 (3.9M `<a/>`: ErrXMLTooComplex 133 MiB), F-06a/b (ratio 1x / ErrCarriedDataTooLarge), F-07a/b (ErrImageBlocksOverlap / ErrTooManyImageBlocks, 18 MiB), F-08 (ErrRawProfileBudget 15 ms), F-10 (2.4 ms): ALL RESOLVED. go vet clean, govulncheck no called vulns, unit tests pass, iptc fuzz 9.2M execs 0 crashers.

NEW BLOCKER (HIGH): format/tiff/relocate_stream.go checkBlocks (~L303-318), F-07a "sum of blocks <= fileLen" assumes disjoint image blocks. FALSE for real files. Found by differential corpus run (Read->Write unmodified + SetCaption on all 3364 testdata files, 3d74da6 vs f6f5291; harness scratchpad/corp): 16 files (8 unique) that wrote fine before now fail with ErrImageBlocksOverlap, incl. real Sony ILCE-7M4 ARW (SubIFD has StripOffsets=1 strip of 36304000 B AND 140 TileOffsets covering the identical bytes, sum 2x raw) and 7 metadata-extractor TIFFs (old-JPEG: JPEGInterchangeFormat 8/4608 overlaps strip 610/3447; overlapping strips). All other 3348 files byte-identical. Fix: bound total by a multiple (e.g. 4x fileLen, still stops the 2000x PoC) or dedupe/coalesce overlapping ranges; add regression test with strip+tile same-range shape and a corpus-wide Read->Write must-not-error test.

Residuals judged: F-05 attr shape (23x, 322 MiB) ACCEPT: absolute ceiling independent of doc size (~170 MiB live/~450 MiB total alloc worst valid shape, measured), 16 MiB packet cap bounds input; optional: lowering caps would risk valid-file rejection. Gm.Read XMP error->warning ACCEPT: m.XMP nil so raw XMP is passed through unchanged on Write (no loss). Minor LOW: png 32 MiB profile budget could reject a legit PNG with ~16 MiB XMP profile + others.
Lesson: ALWAYS run a differential corpus Read->Write against the previous commit after "reject if" hardening; unit tests with synthetic files missed this.

## Final re-audit — 68357f7 (branch dfcee93) — 2026-10-16 — verdict CLEARED

BLOCKING-1 (checkBlocks rejecting strip+tile/JPEG overlap) is fixed by coalescing (format/tiff/relocate_blocks.go planBlocks/disjointAscending; assignNewOffsets rebases each block at srcOffset-range.start inside its copied range). Verified:
- Corpus differential (3365 files, Read->Write plain + SetCaption) vs 3d74da6: 0 new errors, 0 panics; vs 3cd3831: only 26 entries changed (13 unique: 8 former failures now write incl. Sony A7M4 ARW, 5 coalesced/smaller).
- Validity of those 13 outputs: mini TIFF walker compares bytes at every strip/tile/JPEGIF (offset,count) orig vs output, ARW 144 blocks + all TIFFs: 0 content mismatches, all in range.
- Structure-aware generator (40k random strip/tile/JPEG block sets, overlap/dup/nested/near-2^32 offsets): 4896 writes OK, 0 panic, 0 corrupt; output <= input + ~6 KB (no amplification); OOB blocks still rejected by checkBlocks (ErrBlockOutOfBounds).
- F-07a PoC: N=2000 strips over one range, 1 MiB in -> 1 MiB out (0.5 ms). F-07b PoC: ErrTooManyImageBlocks, 18 MiB. Plan arithmetic uses saturatingAdd, rg.end-rg.start cannot underflow (sorted sweep), dst+offset <= MaxUint32 by the saturation guard; assign/relocatedEnd/write all derive the same plan from the same slice (ORF allBlocks incl. thumbBlock returned and written).
- go vet clean, tiff/raw tests + root corpus tests pass, tiff -race pass.
Note: files with chain-IFD JPEG thumbnails still write ~2x input (e.g. 2b27 10 MB -> 19.6 MB): header carries Encode's thumbnails (bounded by F-06a len(b) budget) plus the union once. Bounded, pre-existing, not a blocker. Lesson: differential corpus + pointer-content walker is the right check for any write-path offset rebasing.
