---
name: audit_findings_20260706_reaudit_5fixes
description: Mandatory pre-clearance re-audit of the 5 fixes for HEIF-ILOC-OFFBYONE-01/XMPCONC-01/DETECT-SHORTREAD-01/EXIF-BO-001/PERF-201 successors (ORF-DBLWR-01, WRITE-OOM-01, EXIF-BO-002, HEIF-ILOC-EXTENT-AMPLIFICATION, TIFF-OVF-001) at uncommitted working tree (base 48e6f20)
metadata:
  type: project
---

Re-audit verdict: ALL 5 FIXES CLOSED, ZERO REGRESSIONS. GO for production once committed.
Scope: uncommitted working-tree diff on top of 48e6f20 (write.go, errors.go, exif/ifd.go,
exif/exif.go, format/heif/heif.go, format/tiff/tiff.go, format/raw/{orf,rw2,cr3}/*.go + new
security_fix5_offset_truncation_test.go x4 + write_oom_test.go).

**FIX 1 (HEIF iloc extent amplification, format/heif/heif.go)**: CLOSED. maxIlocItems=4096 +
maxIlocExtentsPerItem=1024 + zero-field-size loop-bound guard in readIlocFullExtents/
readIlocSimpleExtents. Independently re-attacked with a WORSE-than-spec PoC (itemCount=0xFFFFFFFF
for iloc v2, extent_count=0xFFFF, all field nibbles zero) via a throwaway package-internal test
(added+removed, not committed) — Extract/Inject both sub-millisecond. Regression check: wrote an
external ISOBMFF box-walker (scratch, no source touched) against all 44 real testdata/corpus/heif
fixtures — max real item_count observed = 65 (HMD_Nokia_8.3_5G.heif), max ilocLen 1048 bytes ->
real files are 60x+ under the 4096 cap and single-digit extents/item -> zero data-loss risk.
Ancillary (NOT a regression, NOT in scope of these 5 fixes): heif.Extract/top-level Read return
zero-length EXIF for several real fixtures that DO carry EXIF per exiftool (e.g.
HMD_Nokia_8.3_5G.heif — exiftool shows GPS/ISO/etc). Confirmed via `git stash` of heif.go that this
is IDENTICAL pre-fix and post-fix -> pre-existing HEIF item-type/infe-matching gap, unrelated to
FIX 1. Worth a future dedicated HEIF EXIF-extraction conformance audit; does not block this
clearance.

**FIX 2 (ORF/RW2 double-write corruption, write.go writeTIFFORF/writeTIFFRW2 now pass
cloneEXIF(m.EXIF))**: CLOSED. Reproduced the ORIGINAL bug first (git stash write.go): Olympus
E410.orf double-write diverges at byte offset 653530 (matches the reported ~653532). With the fix
applied: ran double-write byte-identical + matches-clean-single-write-baseline check via the public
API against ALL 51 real ORF/RW2 fixtures in testdata/corpus/raw/{metadata-extractor,exiftool,exiv2}
(Olympus C5050Z through OM System TG-7, Panasonic DMC-GF1/GF3/GF7/LX7, issue_839_poc x3) — 51/51
pass. All 6 TIFF-family write paths (TIFF/CR2/ARW/ORF/RW2/NEF) now uniformly call
cloneEXIF(m.EXIF); cloneEXIF/cloneIFD (pre-existing, from #109) deep-copies Entries slice +
bytes.Clone(ThumbnailData) + recursive Next chain; MakerNote raw bytes shared verbatim by design
(documented never-mutated-in-place). No regression: TestWriteTwicePreservesMetadata extended with
real ORF/RW2 subcases, passes.

**FIX 3 (unbounded io.ReadAll in 6 root-package TIFF-family write paths, write.go
readAllCapped + errors.go maxFileSize/ErrFileTooLarge)**: CLOSED. Built an independent
truly-infinite io.ReadSeeker (never returns EOF) presenting a valid minimal TIFF header — real
production Write() call (NewMetadata+Write, not a lowered test threshold) returns
ErrFileTooLarge in ~185ms with 0MB heap delta (runtime.MemStats before/after). Existing
write_oom_test.go's TestWriteTIFFFamilyRejectsOversizedSource (all 6 formats, real 256MiB
threshold, zero-alloc synthetic reader) and TestWriteTIFFFamilyPositiveControlSmallSource
(normal-size still works) both pass. Regression risk (maxFileSize var mutation): grepped whole
repo — root package's `maxFileSize` (errors.go) is read-only outside errors.go/write.go; the
format/{tiff,heif,webp,raw/orf,raw/rw2,raw/cr3}/oom_gate_test.go files mutate their OWN
package-scoped `maxFileSize` var (separate symbol per package, pre-existing #140 pattern), always
under `//nolint:paralleltest` + `t.Cleanup` restore — no race, no cross-package leakage. New
write_oom_test.go tests only READ the root maxFileSize (under t.Parallel()) — confirmed no
mutation added anywhere in this diff.

**FIX 4 (EXIF-BO-002, exif/ifd.go IFD.set(..., bigEndian bool) + exif/exif.go
ifd0BigEndian() threaded through all 21 call sites)**: CLOSED. Verified by inspection: all 21
`.set(` call sites in exif/exif.go use either `e.ifd0BigEndian()` (ASCII/placeholder setters) or
`order == binary.BigEndian` / `isBig` derived from `order := e.ifd0ByteOrder()` (numeric
setters) — none pass a hardcoded/wrong flag. Ran the project's own
TestIFD0ByteOrderEmptyIFD0RoundTrip (both empty-IFD0 and non-empty-IFD0/fresh-subIFD subcases) —
pass. Used a git worktree at pre-fix HEAD to run an identical Encode()-output comparison program
(SetOrientation/SetGPS/SetExposureTime/SetFNumber/SetISO/SetFocalLength/SetImageSize/
SetCameraModel/SetMake) for both an LE and a BE minimal empty-IFD0 TIFF: **Encode() output SHA-256
byte-identical pre-fix vs post-fix in BOTH endiannesses**, proving the fix changes only the
in-memory decode flag and never the persisted bytes, exactly as claimed. go test -race ./exif/...
clean.

**FIX 5 (32-bit int(ifd0Off) truncation, format/tiff/tiff.go + format/raw/{orf,rw2,cr3}/*.go,
uint64 comparison before int() conversion)**: CLOSED. All 4 sites (tiff.extractTagValues,
orf.extractTIFFTags, rw2.extractTIFFTags, cr3.findExifIFDOffset/mergeCMT) now compare
`uint64(ifd0Off)+2 > uint64(len(buf))` before ever computing `int(ifd0Off)`; the guarded
invariant (ifd0Off <= len(buf)-2 before the int() cast) makes the conversion safe on ANY
platform by construction, not just 64-bit. `GOOS=linux GOARCH=386 go build ./...` and `go vet`
both clean (proves compile-correctness on the actually-vulnerable 32-bit target; darwin doesn't
support GOARCH=386 so execution-level 32-bit testing wasn't possible on this machine — the new
security_fix5_offset_truncation_test.go files in format/{tiff,raw/orf,raw/rw2,raw/cr3} honestly
document this 64-bit test-machine limitation in their own comments, matching the precedent set by
format/detect_test.go's TestDetectTIFFHighIFD0Offset for the identical #74 pattern). All 4 new
test files pass.

**Cross-cutting**: go build ./... clean; go vet ./... clean; gofmt -l clean; golangci-lint run
./... = 0 issues; govulncheck ./... = no vulnerabilities; go test -race ./... = all 21 packages
PASS (39.9s root pkg). Fuzz (0 crashers, no new corpus artifacts persisted in any case):
FuzzHEIFExtract 1.45M execs/31s, FuzzHEIFInject 359K execs/31s (23 new coverage-interesting seeds
absorbed, still 0 crashers), FuzzParseEXIF 1.78M execs/16s, FuzzRead 743K execs/35s,
FuzzTIFFExtract 307K execs/26s, FuzzORFExtract 9.8M execs/26s, FuzzRW2Extract 242K execs/26s,
FuzzCR3Extract 452K execs/26s. Noted and verified as a general `go test -fuzz` reporting artifact
(NOT a hang): execs-per-3s plateaus to "0/sec" near the fuzztime deadline across ALL targets
including unrelated ones (FuzzParseEXIF) — a worker-coordination/corpus-minimization quirk of the
Go fuzzing engine, not evidence of a slow-input DoS in HEIF code.

Test-file diffs reviewed line-by-line for gaming (weakened assertions, deleted coverage): none
found — all changes are either genuine strengthening (new pre-Encode accessor assertions in
TestIFD0ByteOrderEmptyIFD0RoundTrip, new real-fixture ORF/RW2 subcases in
TestWriteTwicePreservesMetadata) or mechanical signature updates (`ifd.set(...)` call sites gaining
the new bool arg, using a correct `orderIsBig(order)` helper correlated with each fixture's actual
byte order).

See also: [[production_readiness_20260706]] (the audit that raised these 5 findings),
[[audit_findings_20260706_containers]] (HEIF-ILOC-OFFBYONE-01 original, distinct off-by-one bug,
already fixed in 89dac1e — this session's FIX 1 is a SEPARATE amplification issue in the same
file), [[audit_findings_20260706_xmp_root_concurrency_pass2]] (ORF-DBLWR-01/WRITE-OOM-01 originals).
