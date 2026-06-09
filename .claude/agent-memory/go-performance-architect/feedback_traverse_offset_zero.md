---
name: traverse-offset-zero-bug
description: traverse() for cur!=0 guard caused offset=0 IFDs (Canon/Sony/DJI/Samsung/Casio/Leica) to never parse; fix uses first bool flag
metadata:
  type: feedback
---

`traverse()` in `exif/ifd.go` used `for cur != 0` as its loop guard. When a MakerNote format stores its IFD at file offset 0 (Canon, Sony, DJI, Samsung, Casio, Leica Type 0), the starting `cur=0` caused the loop to never execute — `root` stayed nil, traverse returned an error, and the MakerNote was silently discarded.

**Fix (commit 2d3866e)**: introduced a `first bool` flag — `for cur != 0 || first { first = false; ... }`. This forces one loop iteration regardless of starting offset, while still stopping when the next-IFD chain pointer is 0.

**Why:** The `cur == 0` check conflated two distinct meanings: "IFD starts at file offset zero" vs "end-of-chain marker" (next-IFD pointer = 0 after last IFD). These must be handled separately.

**How to apply:** Any loop over TIFF IFD chains that uses `for offset != 0` as its sole loop guard has this latent bug. Always use a `first` flag or restructure to `do-while` semantics when offset=0 is a valid starting point. Affected formats: all MakerNote types that begin their IFD at relative offset 0 within the MakerNote blob.

**Side effect:** Fixing this made Canon IXUS 40 (and other Canon/Sony/DJI) MakerNotes successfully parse for the first time, changing the `ExampleWrite` output size from 239074 → 239080 bytes (Canon MakerNote IFD round-tripped through encode).
