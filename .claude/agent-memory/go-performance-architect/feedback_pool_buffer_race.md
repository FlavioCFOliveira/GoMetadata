---
name: sync.Pool buffer subslice race pattern
description: Pool-buffer subslices must not escape past tiffScanPool.Put/iobuf.Put — discovered in detect.go and heif.go when t.Parallel() exposed the race
type: feedback
---

Never return a subslice of a pooled buffer to callers, and never call Pool.Put before all reads of a subslice derived from that pool buffer are complete.

**Why:** When t.Parallel() was added to corpus tests, two pre-existing bugs were exposed:
1. `format/detect.go refineTIFFVariant`: called `tiffScanPool.Put(bp)` before `mapMakeToFormat(makeRaw)`, where `makeRaw` was a subslice of `*bp`. Another goroutine could Get and overwrite the buffer while `mapMakeToFormat` was reading it.
2. `format/heif/heif.go Extract`: called `iobuf.Put(hdrPtr)` before `extractFromMetaData(r, metaData)`, where `metaData` was a subslice of `*hdrPtr`. Same race pattern.

**How to apply:** Whenever a pool buffer is used via `buf := *pool.Get().(*[]byte)` and a subslice is derived (e.g., `sub = buf[off:end]`), ensure all reads of `sub` complete before calling `pool.Put`. Use defer only when the pool buffer's subslices are not returned or captured by callers.
