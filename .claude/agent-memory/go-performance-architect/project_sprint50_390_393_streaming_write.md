---
name: sprint50-390-393-streaming-write
description: Sprint 50 #390-#393 (2026-10-27) — HEIF/WebP/ORF/RW2/JPEG-trailer writes stream from the io.ReadSeeker; shared iobuf helpers, oracle+differential test method, traps
metadata:
  type: project
---

HEIF (#390), WebP (#391), ORF/RW2 Inject (#392) and the JPEG post-EOI trailer (#393) now copy unchanged ranges from the source instead of buffering it. Bytes written unchanged (hash-compared vs a baseline tree: heif 459 rows, webp 297, orf 324, rw2 171, jpeg 17010, 0 mismatches).

**Shared helpers** (internal/iobuf/rangecopy.go): `CopyRange(r,w,off,n)` (Seek+StreamCopyN), `Window` (4 KiB pooled header window, `Peek`), `IndexRange` (streaming signature search). `internal/testutil/sparse.go` has `SparseFile`/`CountingWriter` for >256 MiB tests without memory.

**Method that worked:** keep the old whole-file implementation as a test-only oracle (`oracle_test.go`, `oracleInject`) and add a differential test (corpus + synthetic layouts + every header truncation + byte flips) plus a `FuzzInjectMatchesWholeFile` target. It found no divergence but is the proof of "bytes unchanged". Baseline binary built from `git archive HEAD` copy with `ln -s` to the untracked `testdata/corpus` (the corpus is gitignored, absent from the archive).

**Design facts:**
- heif/stream.go: W1 `scanTopLevel` (meta, first moov, boxesEnd, open-ended last box), plan (meta read, `planInject`, shift iloc, dry-run moov stco shift so errors come before output), emit = box walk with `cursor` copy. Old `patchAncestorSize`/`closeOpenEndedBox`/`shiftChunkOffsets` live in oracle_test.go (existing tests call them).
- webp: two passes over chunk headers (RIFF size needs the total); `stillImage` accumulator replaced `newVP8XPayload(chunks)`; `vp8Info(data, chunkSize)`.
- tiff.InjectWithMagic (new exported): rawEXIF!=nil keeps the old base semantic; nil reads the metadata prefix (cap only on the PREFIX, file limited to 4 GiB classic offsets), restores std magic on the owned prefix.
- trailer: finders are generic over `Source` (Range/Index/CopyRange), absolute offsets; `PlanFromRegion` = wrapper over `PlanFromSource(regionSource)`. Go methods cannot be generic, so checkDirectory etc. are functions.

**Traps:** public `Write` for ORF/RW2 already streamed (#291/#293) — the whole-file read was only in `orf.Inject`/`rw2.Inject`; with `NewMetadata` (rawEXIF nil) root `write.go` still buffers (readAllCapped, gated by write_oom_test). Direct `rw2.Inject` of a real RW2 fails before and after ("image block offset=4294967295", RawDataOffset). Direct ORF Inject allocates 2x prefix (scan buffer + relocated header) so its bound is 2M+64 KiB, not M+64 KiB. gocritic sloppyReassign vs govet shadow: use `err = f()` + separate `if err != nil`, or distinct names. A test `const` > MaxUint32 passed to `uint32()` breaks 386; use int64 params.

**Why:** sprint goal is memory proportional to metadata. **How to apply:** reuse CopyRange/Window/IndexRange and the oracle+differential pattern for any further streaming write (PNG already uses StreamCopyN; CR3/TIFF predate this).
