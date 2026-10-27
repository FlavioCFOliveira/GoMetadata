---
name: project-sprint49-385-388-selective-write
description: Sprint 49 (#385-#388, 2026-10-27): SRATIONAL GPS, first-NUL strings, dirty-flag selective Write (Segment/MarkModified, jpeg.InjectKeeping), README IPTC cells; design facts, traps, measured costs
metadata:
  type: project
---

Implemented in one pass on branch feature/49-reading-and-writing-enhancements (uncommitted when written; commits done by the coordinator).

**Traps found**
- A test file named `*_386_test.go` (or any `_<GOARCH>_test.go`) is a build-constrained file: it compiles only for GOARCH=386 and its tests silently never run. `go test -list` showed nothing. Never name files `task385_386_test.go`.
- `sed -i` on macOS needs `-i ''`, and BSD sed has no `\b`; a bad `sed -i 's/x/y/' file` created a stray empty file. Use perl or python for edits.
- zsh does not word-split `$var`; loops over "pkg Name" pairs need a script file.
- `exif.DataType` is the TIFF type (not `exif.Type`).
- Benchmarks on this machine are noisy (other load, +-20%); sequential base/new runs gave false +400% once. Use interleaved A/B with `go test -c` binaries (scratchpad `ab_*.txt` method) and benchstat.

**Design (selective write, #387)**
- `Metadata.modified Segment` (guarded by mu), set by `touch()`/`mirrorAndTouch()` in every Set*; `MarkModified(...Segment)` public. `write_plan.go`: `planWrite` -> encode / carry / bypass.
- Encode set = (modified | not-held) & parsed. "Held" = raw slice non-nil. Carried segments need `sourceHolds` (re-extract raw from r and compare with m.raw*); mismatch means Write onto another image -> encode everything (legacy behaviour, keeps Read(A)+Write(B) copying).
- bypass (nothing modified, source verified) -> `copyThrough` (StreamCopyN). Gives byte identity for JPEG/PNG/WebP/TIFF/HEIF/CR3/RAW on the whole corpus (3 000+ files). A metadata-less Metadata still needs the check: legacy inject of empty metadata is NOT byte-identical for ~170 JPEG and some PNG.
- JPEG: `jpeg.InjectKeeping(..., keep Keep)` leaves kept segments in place; for JPEG carry = all unencoded segments (also APP13 with non-8BIM signature etc.). EXIF with MakerNote external offsets (Olympus) loses KeepEXIF only; measureShift is keep-aware.
- PNG: nil payload = "unchanged"; carried segments are passed as nil (otherwise the IPTC raw-profile chunk is rewritten). JPEG nil = remove; other formats' nil semantics not verified, so they receive raw bytes.
- TIFF family: relocator always re-lays IFDs from m.EXIF once anything is written; unmarked direct EXIF edits are still written there (documented).
- SetGPS touches XMP by design (xmp.SetGPS), so "SetGPS only -> XMP byte-identical" cannot hold; IPTC identical on 279/279 corpus JPEGs.

**Costs measured**: Write of an empty NewMetadata PNG +19% (+26 ns, one extraction pass, inherent); real no-op Write 2-15x faster; IPTC Parse +6-10% from the NUL scan (cutAtNUL, 1.6-3.8 ns/dataset); IFDEntry.String -22%.

**Pre-existing, unfixed**: jpeg.Inject writes metadata before JFIF APP0 (T.81 wants APP0 first); Make/Software/LensModel keep trailing blanks (only CameraModel trims, ExifTool trims all); IPTC Caption in WWL ION230 is "WWL     " (EXIF ImageDescription blanks); RATIONAL GPS coordinate with zero denominator still returns (0,0,true) in dmsToDecimal.
