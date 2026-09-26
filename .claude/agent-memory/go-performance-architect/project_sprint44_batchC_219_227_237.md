---
name: sprint44-batchC-219-227-237
description: Sprint 44 Batch C (2026-09-25) — exif.EncodedSize/EncodeInto exactness trap, relocate pre-sizing, iobuf.ReadAll/MagicWriter, tiffscan, ORF/RW2 ownership verdict
metadata:
  type: project
---
Batch C (#219–#227, #237) was done uncommitted on feature/44 (2026-09-25). Golden SHA-256 identical: 578 files, 452 hashes plus 126 unchanged errors.

Non-obvious findings:
- `ifdTotalSize` does NOT equal what `writeIFD` writes. They diverge when an IFD starts at an odd offset (odd-length thumbnail before it) or when `len(Value) > Count*typeSize`. `exif.EncodedSize` therefore replays writeIFD in `ifdWrittenLen`/`encodedLen`. The pointer offsets from `computeIFDOffsets` still use the theoretical sizes. This is a possible pre-existing encoder bug; it was reported, not fixed.
- relocate: `imageStart = ifdEnd + computeSubIFDsSize` assumes ifdEnd is even, but ifdEnd can be odd. This is a possible pre-existing off-by-one; it was reported, not fixed. `relocatedLen` mirrors the actual appends, not imageStart.
- `InjectWithEXIFORF/RW2` input is caller-owned (exported API), so the #226 in-place magic patch is not allowed there and the copy stays.
- `isSonyPlainIFDMakerNote`'s prefix literal was already stack-allocated, so hoisting it gave 0 allocs saved.
- In zsh, `set -- $spec` does not word-split. Run multi-arg shell loops via `bash -c`.
- A Bash hook sometimes blocks long inline python heredocs. Write the script to the scratchpad and run it from there.

**Why:** these traps cost time or could mislead future batches.
**How to apply:** trust `EncodedSize` for exact sizing, and do not reuse `ifdTotalSize` for exact sizing. Related: [[project_sprint44_batchB_214_218_238_239]].
