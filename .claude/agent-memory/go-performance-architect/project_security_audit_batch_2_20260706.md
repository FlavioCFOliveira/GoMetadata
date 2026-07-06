---
name: project_security_audit_batch_2_20260706
description: Second 2026-07-06 security audit batch (5 findings) — HEIF iloc amplification, ORF/RW2 double-write, root io.ReadAll caps, EXIF-BO-002, 32-bit offset truncation
metadata:
  type: project
---

Second production-readiness security audit on 2026-07-06 (same day as the #244-247 batch)
found and fixed 5 more defects, all delivered in one session with load-bearing regression
tests (fail-before/pass-after verified for every fix except FIX 5, which is provably
32-bit-only — see below).

**FIX 1 — HEIF/AVIF iloc extent-count amplification (CWE-770/834).**
`format/heif/heif.go` `readIlocFullExtents`/`readIlocSimpleExtents`: doc comment claimed
excess extents were "dropped" but the append was unconditional, and when every per-extent
field-size nibble is zero (offsetSize=lengthSize=indexSize=0) NO bounds check ever fires
so the loop ran the full attacker-controlled `extentCount` (up to 65535) doing zero-cost
no-op iterations. Measured: a 262KB crafted file took 2.55s pre-fix vs 310µs post-fix
(~8200x). Fix: cap `loopCount` at `maxIlocExtentsPerItem` (1024) when the combined
field-size is 0; guard the `append` with `len(extents) < maxIlocExtentsPerItem`; added a
new `maxIlocItems` (4096) cap on the outer item-count loop in both `parseIloc` and
`parseIlocFull`, mirroring `format/detect.go`'s `parseBigTIFFIFD0` count-cap idiom.
Regression: `TestHEIFIlocZeroFieldSizeAmplificationBounded` in `format/heif/heif_test.go`;
new fuzz seeds added to `FuzzHEIFExtract`/`FuzzHEIFInject` via
`buildIlocZeroFieldAmplification`. Benchmark parity confirmed byte-identical
(629 B/op 15 allocs Extract, 1816 B/op 35 allocs Inject, before AND after).

**FIX 2 — ORF/RW2 double-write corruption (CWE-664).**
`write.go` `writeTIFFORF`/`writeTIFFRW2` passed `m.EXIF` directly to
`InjectWithEXIFORF`/`InjectWithEXIFRW2` instead of `cloneEXIF(m.EXIF)` — every sibling
(writeTIFF, writeTIFFCR2, writeTIFFARW, writeTIFFNEF) already clones per the #109 fix;
ORF/RW2 were added later (#104) and missed it. A second `Write()` on the same `*Metadata`
silently corrupted image data (confirmed diverging at byte 653532 on
`testdata/corpus/raw/metadata-extractor/Olympus E410.orf`). Fix: pass `cloneEXIF(m.EXIF)`
at both call sites. Regression: extended `TestWriteTwicePreservesMetadata` in
`write_test.go` with real-corpus ORF/RW2 subcases (Olympus E410.orf, Panasonic
DMC-GF1.rw2), using `SetOrientation(3)` as the mutating tag.

**FIX 3 — Unbounded io.ReadAll in the 6 root-package TIFF write paths (CWE-770/400).**
`write.go` writeTIFF/writeTIFFCR2/writeTIFFARW/writeTIFFORF/writeTIFFRW2/writeTIFFNEF
each fell back to bare `io.ReadAll(r)` when `m.rawEXIF == nil` — the
`NewMetadata(fmtID)+Write(...)` pattern. The #140 fix capped every `io.ReadAll` in
`format/*` packages but missed these 6 root-package sites. Fix: new `readAllCapped(r,
tag)` helper in `write.go` wrapping `io.ReadAll(io.LimitReader(r, maxFileSize+1))` +
size check; new root-package `maxFileSize` var (256 MiB, test-overridable) and
`ErrFileTooLarge` sentinel in `errors.go`, mirroring `format/tiff/errors.go`. Regression:
new `write_oom_test.go` with a custom `zeroFillReadSeeker` (presents a real 256 MiB+1 MiB
virtual stream without ever allocating a buffer that large — only io.ReadAll's own
destination buffer, bounded by the cap, allocates real memory) —
`TestWriteTIFFFamilyRejectsOversizedSource` (6 formats) +
`TestWriteTIFFFamilyPositiveControlSmallSource`.

**FIX 4 — EXIF-BO-002: IFD.set's own bigEndian flag ignored ifd0ByteOrder (CWE-198).**
`exif/ifd.go` `IFD.set()` inherited its `bigEndian` decode flag from
`ifd.Entries[0].bigEndian`, defaulting to false (LE) for a freshly-created empty
GPSIFD/ExifIFD regardless of the true BE stream order — a SEPARATE bug from EXIF-BO-001
(#246, which only fixed `ifd0ByteOrder()`'s own value, controlling ENCODE). On a
big-endian source, in-memory accessors (`GPS()`, `ExposureTime()`, `FNumber()`, `ISO()`,
`FocalLength()`, `ImageSize()`) called on the SAME object right after the matching Set*
call returned garbage — Encode() output was never affected. Fix: `IFD.set` signature now
takes an explicit `bigEndian bool` parameter (removed the Entries[0] heuristic entirely);
added `EXIF.ifd0BigEndian()` helper (`e.ifd0ByteOrder() == binary.BigEndian`); updated all
21 production call sites in `exif/exif.go` + 21 test call sites across 5 test files.
Verified pre-fix garbage via a controlled sabotage-and-restore of `IFD.set`'s body (kept
the new signature, reverted only the internal logic) — every accessor returned wrong
values in both empty-IFD0 and non-empty-IFD0-but-fresh-subIFD scenarios. Regression:
extended `TestIFD0ByteOrderEmptyIFD0RoundTrip` in `exif/exif_test.go` with pre-Encode
accessor assertions for all 6 setters (added `SetImageSize` to the exercised set).
Benchmark parity confirmed byte-identical (1656 B/op, 31 allocs, `BenchmarkIFDSet`).

**FIX 5 — 32-bit int(ifd0Off) truncation (CWE-681/190, LOW, 32-bit only).**
4 sites: `format/tiff/tiff.go` `extractTagValues`, `format/raw/orf/orf.go`
`extractTIFFTags`, `format/raw/rw2/rw2.go` `extractTIFFTags`, `format/raw/cr3/cr3.go`
`findExifIFDOffset` — all used `int(ifd0Off)+2 > len(data)` where `ifd0Off` is an
attacker-controlled `uint32`; on GOARCH=386/arm this wraps negative for offsets
>= 2^31, letting the guard pass and `data[ifd0Off:]` panic. Also found (same file, same
root cause, NOT in the original 4-site list but directly adjacent) `cr3.go`'s `mergeCMT`:
`int(exifIFDOffset) < len(cmt1)` has the identical wraparound, silently skipping the
CMT1+CMT2 merge rather than panicking — fixed it too. All 5 sites now compare in
`uint64` before any int conversion, mirroring the pre-existing `#74`
(`format/detect.go` `parseClassicTIFFIFD0`) and `#45` (`format/jpeg` `parseIRBEntry`)
idiom. IMPORTANT: on 64-bit test hardware, `int(uint32)` never wraps, so these specific
regression tests pass BOTH before and after the fix — empirically verified by reverting
each guard and re-running (all green pre-revert-fix too). This is not a test weakness;
it's the correct, expected signature of a 32-bit-only defect, and is exactly what
`format/detect_test.go`'s own `TestDetectTIFFHighIFD0Offset` (for the #74 fix) documents
for the identical pattern. New test files:
`format/tiff/security_fix5_offset_truncation_test.go`,
`format/raw/orf/security_fix5_offset_truncation_test.go`,
`format/raw/rw2/security_fix5_offset_truncation_test.go`,
`format/raw/cr3/security_fix5_offset_truncation_test.go`.

**Full validation gate (2026-07-06):** gofmt clean; `go vet ./...` clean; `go build ./...`
clean; `go test -race -count=1 ./...` all packages PASS; `golangci-lint run ./...` 0
issues (had to fix 2 gocritic + 2 gocyclo + 2 nolintlint issues along the way — see
[[feedback_lint_iteration_after_new_code]]); `govulncheck ./...` no vulnerabilities;
40s fuzz runs on FuzzHEIFExtract/FuzzHEIFInject/FuzzParseEXIF/FuzzRead — 0 crashers.
