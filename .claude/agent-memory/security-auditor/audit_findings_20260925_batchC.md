---
name: audit-20260925-batchC
description: Sprint-44 Batch C (#219-#227/#237) security pass — CLEARED after BATCHC-01 re-audit; pre-existing CR2/odd-thumb/odd-ifdEnd corruptions tracked as #279-#281
metadata:
  type: project
---
Batch C audit 2026-09-25 (relocate pre-size, iobuf.ReadAll/MagicWriter, tiffscan, exif EncodedSize/EncodeInto).

NEW regression (HIGH, CPU DoS): format/tiff/relocate.go blockIndex.find falls back to an O(n) scan on
every miss; patchRawIFDOffsets calls it per raw array element. SubIFD with 65536 strips + a raw
TileOffsets SHORT entry (no TileByteCounts) of count C -> O(C*n). PoC 2.5 MB file: 35 s vs 17 ms at HEAD
(map). Fix: return nil on fast-path miss (blocks are contiguous by construction) or build map once.

Pre-existing (identical at HEAD, confirmed with exiftool):
- CR2 write: rebaseAllIFDsAfterCR2Marker never shifts IFD0 next-IFD ptr / IFD1 chain / inline strip
  offsets by +8 -> every corpus CR2 (9/9) loses IFD1-IFD3 (raw image IFD) after Write. HIGH.
- exif writeSubIFDs: IFD following an odd-length ThumbnailData starts odd; ifdTotalSize assumes even
  start -> that IFD's next ptr / thumbnail ptr off by 1 (2b27b742...tif 23->5 IFDs). HIGH.
- relocate computeSubIFDsSize assumes even ifdEnd -> odd trailing thumbnail + SubIFDs shifts ALL image
  offsets by -1. MEDIUM-HIGH. Len(Value)>Count*size misplacement only via caller-built IFDEntry (LOW).
Golden 126 failures all expected (102 strips past EOF, 17 malformed types/magic, 5 bad BigTIFF hdr, 2...).
govulncheck not runnable (tool built with go1.26 vs go1.27 toolchain).

Re-audit 2026-09-25: BATCHC-01 CLOSED (find returns nil on miss; contiguity holds on all 6 relocate variants since si.blocks only built in enumerateSubIFDsAt, never reordered). poc1 2.5MB 35s->3.5ms; golden 452/126 identical; EncodedSize==len(Encode)==EncodeInto on corpus. CLEARED.
