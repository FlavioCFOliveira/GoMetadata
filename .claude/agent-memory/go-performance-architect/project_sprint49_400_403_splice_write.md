---
name: sprint49-400-403-splice-write
description: Sprint 49 #400-#403 (2026-10-27): JPEG/PNG splice-and-copy write (format/jpeg/splice.go), GPS zero denominator, blank-trimmed EXIF text accessors; perf traps and A/B method
metadata:
  type: project
---

JPEG Inject is now scan-then-copy (format/jpeg/splice.go): `scanSplice` reads segment headers up to the first SOS, `splicePlan.ops` orders drops/emits, `writeSplice` copies ranges. Replaced segments keep their place; new ones go after the JFIF/JFXX/Adobe-APP14 leading run; EXIF < XMP < IPTC. Damaged sources copy verbatim from the damaged byte. PNG: `scanOldMetadata` pre-scan decides in-place vs after-IHDR; damaged chunk after IHDR copies the rest; ErrNoImageHeader when no IHDR.

**Why:** user decision on #403 ("splice and copy the rest"); T.871 6.3 JFIF first beats Exif 3.1 4.5.4 APP1-after-SOI.

**How to apply / traps found:**
- A test-only `func min` in format/jpeg/fuzz_test.go shadowed the builtin (removed in this sprint); check for shadowing before using min/max in a package.
- `iobuf.StreamCopyN` into a `*bytes.Buffer` grows 512 B per call (ReadFrom) - for short ranges read into the scratch buffer instead (copyRange).
- Local arrays passed to io.Reader escape: reuse a struct field buffer (PNG hdr in injectState).
- `splicePlan` must come from a sync.Pool (self-referential dropsArr slice moves it to heap otherwise).
- Corpus: 8 "raw/*.nef" files in metadata-extractor are really JPEGs; 6 "jpeg/*.jpg" are HEIF; 10 mutated PNGs have a corrupt IHDR type and now fail with ErrNoImageHeader (before: silent no-op).
- Pre-existing bug NOT fixed (reported): png.Inject drops the old eXIf chunk when rawEXIF is nil, but write.go passes nil for a carried (unmodified) EXIF, so SetKeywords on a PNG with eXIf loses the EXIF.
- A/B method: `git archive HEAD` into scratch dir + symlink testdata/corpus; build old/new `go test -c` binaries, interleave 8 runs, benchstat. Walk of a symlinked corpus root needs the real path.
- zsh: `set -- $var` does not word-split; write explicit commands in the fuzz runner script.
