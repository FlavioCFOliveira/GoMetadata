---
name: task389-compact-tiff-read
description: Sprint 50 #389 — TIFF/DNG Read without image data via compacted stream (format/tiff/sparse.go); design choices, verified equivalence method, traps
metadata:
  type: project
---

Task #389 (2026-10-27): `tiff.ExtractCompact` builds a compacted TIFF stream (IFD dirs + OOL values + JIF thumbnails side by side, the offsets exif.Parse follows rewritten), root `Read` parses it, then `Sparse.Restore()` writes the file offsets back. `m.rawEXIF` = `Head()` (true prefix), `m.rawEXIFSparse=true`; Write re-reads the metadata prefix via `tiff.Extract` (`sourceMetadataPrefix`), so Write bytes are unchanged. Pentax QS1.dng Read: 22.7 MB -> 0.21 MB/op, 839 -> 18 µs.

**Why not an offset-aware exif.Parse:** exif parse code (ifd.go) is threaded with `b []byte` + ARM64-ABI-tuned hot loops; a sparse view would touch ~15 functions and ~35 test call sites. Compaction + restore keeps exif untouched.

**Safety net (the part to keep):** compaction only when exact: dirs must not overlap each other/values/header, MakerNote must lie in region 0 (offsets identity), no OOL pointer/JIF values, no dup JIF, no BigTIFF, chain <=512. Read falls back to the dense path when exif.Parse errors OR warns on the compacted stream (warnings embed stream offsets) — `finishSparse` + internal `withoutSparse()` option; `denseTIFF` in readConfig.

**Known invisible difference:** unexported `IFDEntry.rawOffset` of entries in relocated regions differs (only observable via MakerNoteOffset, guarded by MN rule). Compare harnesses must skip it.

**Verification method that worked:** throwaway module in scratchpad with `replace` to the repo + `git archive HEAD` copy as baseline (git stash/worktree forbidden); reflect-based dump of m.EXIF/IPTC/XMP + SHA-256 of 4 Write variants over every corpus file; mutation test (skip Restore) proves tests bite. Differential fuzz in FuzzTIFFExtract via `sparseParams{always:true}`.

**Traps:** macOS `sed -i` without '' created a stray `rite_plan.go`; golangci-lint revive/gosec report only a chunk per run (nondeterministic, re-run until 0 in own files); `countingReader` name already used in write_test.go; baseline lint had 29 pre-existing issues (xmp/ revive). Corpus: 15 files compacted of 75 TIFF-family >4 MiB (incl. _2 duplicates).
