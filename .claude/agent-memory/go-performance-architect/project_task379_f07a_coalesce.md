---
name: project-task379-f07a-coalesce
description: F-07a rework (#379) - coalesce overlapping TIFF image-block source ranges instead of rejecting; real files overlap
metadata:
  type: project
---

Overlap-rejection (ErrImageBlocksOverlap) was wrong: real files overlap (Sony ARW strip + 140 tiles, JPEG thumbnail inside a strip). Fix is `planBlocks` (format/tiff/relocate_blocks.go): union of source ranges, placed in first-block slice order, blocks rebased by distance from range start; `disjointAscending` fast path keeps disjoint files byte-identical and alloc-free. Error removed.

**Why:** user decision (binding): copy each source byte once. **How to apply:** any new consumer of imageBlock sizes (relocatedEnd, assignNewOffsets, writer) must go through planBlocks when blocks are not disjointAscending.

Corpus facts: 3364 testdata files never "all write OK" - 651 are pinned malformed/non-image rejects (testdata/corpus_write_rejects.txt, regenerate with GOMETADATA_UPDATE_CORPUS_REJECTS=1). 9b286add...tif output is ~1.8x input already on 3d74 (pre-existing, unrelated). format/png F-08 timing test flakes under `go test -race ./...` contention, passes alone.
