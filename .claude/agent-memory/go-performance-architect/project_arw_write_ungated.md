---
name: project-arw-write-ungated
description: ARW write un-gated (task #103): Sony MakerNote absolute-offset rebase + SR2Private block preservation
metadata:
  type: project
---

ARW write is fully un-gated as of task #103 (commit 652eda0). A subsequent regression (IFD0 preview dropped) was fixed in commit 0ef9ce6.

**Why:** Sony DSLR-A500.arw write was silently losing all 52 MakerNote OOL entries and corrupting SR2Private because Sony uses TIFF-absolute (not blob-relative) MakerNote offsets, and the SR2Private IFD block holds an encrypted SR2SubIFD blob with 3 levels of absolute-offset pointers.

**How to apply:** `SupportsWrite(FormatARW) = true`. Dispatch in `write.go` routes through `relocateTIFFFromParsedARW` → `arwRelocateWithSR2`. Two post-encode patches:
1. `rebaseSonyMakerNote`: scans MakerNote IFD in finalTIFF, rebases each OOL val_or_off by delta=(newMNAbs−oldMNAbs).
2. `patchSonySR2InFinalTIFF`: patches 0xC634 inline pointer in IFD0, rebases SR2 IFD OOL entries and absolute-offset tags (0x7200/0x7240), then decrypt+rebase+re-encrypt the SR2SubIFD blob (Sony PRNG-XOR stream cipher, `sr2CryptBlob`).

Key details:
- SR2SubIFD uses Sony PRNG-seeded XOR cipher (128-element pad, big-endian uint32 words, ExifTool Sony.pm sub Decrypt).
- `rebaseIFDInBlob` is a 3-level recursive walker: root IFD → TypeLong OOL arrays (sub-IFD pointer arrays like 0x74C0) → SR2DataIFDs.
- SR2 block word-alignment: compute exact sr2ActualSize (not worst-case +1) before `assignNewOffsets`; otherwise StripOffset is off by 1.
- SR2SubIFDKey (tag 0x7221, TypeUndefined Count=4) is always little-endian regardless of TIFF byte order.
- `sr2BlockSize` helper was removed as unused after exact-size computation was inlined into `arwRelocateWithSR2`.

See also: [[project-nef-write-ungated]], [[project-cr2-write-ungated]]
