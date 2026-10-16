---
name: sprint46-e6-358-365
description: Sprint 46 E6 (#358-#365) DONE 2026-10-16 (uncommitted at hand-off): design, verified facts, harness, perf, unfixed defects
metadata:
  type: project
---

Base HEAD 7e94eff. All code+tests+docs done; gofmt/vet/build/race/golangci-lint(0); fuzz 60 s x14 PASS; corpus sweep 132 -> 118 files with new warnings (0 regressions, 14 fixed); MakerNote value diffs 6 -> 4 files. Harness in scratchpad/e6: sw_base (base6 = git archive HEAD)/sw_new sweepw, ew_in/ew_base/ew_new + cmpt.py, wr/wr (SetCaption), mn/mn (MN entry count), fo/fo (ExternalOffsets), runbench.sh (interleaved x5), fuzz.sh pkg target.

Design: internal/bmff (OpenEnded/Close/CloseInPlace; cr3 + heif); heif parseIinfEntryCount (v0=16 bit else 32) vs parseIlocItemCount (iloc.go used the same helper: split!); internal/trailer Samsung rigid piece (audioStart..QDIO), spans intersecting it dropped; exif/version.go minExifVersion + requireExifTag; nikonType1 picks byte order from count; jpeg size.go DNL (T.81 B.2.2 p.36, B.2.5 p.45, B.2.1 p.35 from w3.org itu-t81.pdf); exif.ExternalOffsets + jpeg shiftExternalOffsets (gate in prepareInject: bytes.IndexByte('O') -> 0% cost; gate at Inject call site cost +5%); exif/private_ifd.go (IFD0 0x8290 KodakIFD via mnWalker) + parseInteropIFD loop guard.

Verified facts: Olympus PreviewImageStart is MAKERNOTE-relative (not file-absolute): E-M1 MN at file 3632 + 10976976 = 0xa78d00. Kodak ProBack has NO MakerNote: KodakIFD (IFD0 0x8290, no BOM, TIFF-relative values) + ExifIFD InteropOffset -> IFD0 (loop). exiftool -v3 "MakerNotes directory with N entries" is the entry-count oracle (exiftool -MakerNotes:all line counts expand sub-tags). ExifTool does not read ExifVersion table; Exif 2.2/2.21 text not local (Gamma not gated). libjpeg-turbo rejects Y=0 (no DNL code to adapt).

Perf vs 7e94eff: all benches within +-1.2%, no B/op or allocs change. New benches: ExternalOffsets NotOlympus 9 ns / Olympus 417 ns; AppendPrivateIFD 607 ns; ImageSizeDNL 1 MiB (dense FF) 2.55 ms; InjectOlympusPreview 1.25 us.

Traps: zsh no-match on `--include=*.rs` (quote it); BSD sed has no \b (use perl -pi); govet shadow fires on a package-level var used in a later file (token.Pos is global); tparallel needs t.Parallel in wrapped subtests; shiftExternalOffsets once left the reader unsought on the !ok path (test caught it).

Unfixed defects found (report, not fixed): FuzzHEIFInject: source with trailing unboxed bytes after the last top-level box + Inject appended mdat can form a bogus file-level meta header that hides the moov meta (exempted in fuzz oracle); Sony HX5V(2) IFD0 type-0 entry; Samsung Note 4 extra values; Olympus E-1 style CameraSettings (UNDEFINED value) and offsets before the first scan header not rewritten; other private IFDs in IFD0 not carried; ORF (TIFF path) Olympus offsets; Kodak ProBack IFD1 uncompressed-thumbnail strip data (51948 bytes) is not in the encoded EXIF (enc 4.9 KB vs 56.9 KB source) -- check.
