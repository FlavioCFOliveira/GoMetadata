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
