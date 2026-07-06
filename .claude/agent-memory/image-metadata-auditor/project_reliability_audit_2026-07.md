---
name: project-reliability-audit-2026-07
description: Verification pass (2026-07-06) confirming all 6 findings from the 2026-06 reliability audit are fixed, and correcting a stale claim about TIFF/RAW write support.
metadata:
  type: project
---

Re-verified against source on 2026-07-06 as part of the final whole-team
production-readiness determination. All six F1-F6 findings from
[[project-reliability-audit-2026-06]] are now CONFIRMED RESOLVED:

- **F1 (iobuf pool orphan, JPEG readSegment)** — RESOLVED. format/jpeg/jpeg.go
  readSegment (~line 1508-1520) now explicitly saves the old pooled buffer to
  a local (`old := *scratch`) before reassigning `*scratch = make(...)`, with
  a comment citing the orphan risk directly.
- **F2 (IPTC Dataset race)** — RESOLVED by design change, not synchronization.
  iptc/encoding.go Dataset.setDecodedValue is called once at Parse time (or
  once per write-path constructor); the doc comment states "decodedValue is
  stable and never written again by any read accessor, which makes concurrent
  reads race-free without any additional synchronisation." Confirmed via
  `go test -race -count=1 ./iptc/...` passing.
- **F3 (XMP UTF-32 no pre-decode size cap)** — EFFECTIVELY RESOLVED. Parse
  still calls normaliseToUTF8(b) before the maxXMPDocumentBytes check (xmp/xmp.go
  line ~79 vs ~86), so the check-ordering issue is technically unchanged, BUT
  decodeUTF32 (xmp/encoding.go ~150-179) now breaks out of its loop as soon as
  `len(out)+4 > maxUnescapedXMLBytes` (added for finding #81), which bounds the
  loop to ~262K iterations regardless of input size. The original CPU-cost
  concern is mitigated as a side effect. Residual: the check-order itself is
  still not textbook-safest pattern; low priority, non-blocking.
- **F4 (XMP numeric char-ref overflow)** — RESOLVED. xmp/rdf.go parseHex/parseDec
  (~line 1237-1290) now reject inputs >7 digits early and validate
  `v > unicode.MaxRune || (v >= 0xD800 && v <= 0xDFFF)` before returning.
- **F5 (ifdTotalSize uint32 wrap)** — RESOLVED. exif/ifd.go ifdTotalSize
  (~line 2326-2372) now accumulates in uint64 and saturates at math.MaxUint32
  instead of wrapping, with an explicit doc comment and #201-era rationale.
- **F6 (HEIF rawXMP aliasing)** — RESOLVED. format/heif/heif.go parseHEIFMetadata
  (~line 853) now calls `bytes.Clone(extractItemSlice(data, loc))` for rawXMP in
  the slow path, matching the fast path's copy behavior.

## Correction to prior memory: TIFF/RAW write is NOT blocked

[[project-robustness-audit-2026-06]] and older `project_corpus_gaps` notes
stated "TIFF/RAW write — blocked by isTIFFBased() gate, ErrWriteNotSupported."
**This is now stale/wrong.** As of 2026-07-06, `write.go` isTIFFBasedFormat
gates classic-TIFF/DNG/CR2/NEF/ARW/ORF/RW2 into DEDICATED relocation-based
write paths (writeTIFF, writeTIFFNEF, writeTIFFARW, writeTIFFCR2, writeTIFFORF,
writeTIFFRW2) that preserve the original image-data blocks via byte-offset
relocation (epic #33 Option A — appears completed). ErrWriteNotSupported is
now returned ONLY for BigTIFF (write.go ~line 412-416: "BigTIFF write requires
a native 64-bit encoder"), not for classic TIFF-based formats in general.

Field/byte-level correctness for these paths is verified by:
- `format/tiff/bug111_116_118_149_test.go` TestTIFFWritePathPreservesIPTCOnXMPOnlyUpdate
  — byte-exact IPTC preservation when only XMP is updated (#149 contract).
- `format/tiff/tiff_test.go` TestInjectIPTCRoundTrip / TestInjectXMPRoundTrip —
  byte-exact value round-trip for both IPTC and XMP independently.
- `format/tiff/relocate_makernote_test.go` — Sony/Nikon/Panasonic MakerNote OOL
  rebase round-trips.

The root `corpus_test.go` TestCorpusRoundTrip skips *byte-level whole-file*
comparison specifically for TIFF-family files that have BOTH RawIPTC and RawXMP
embedded simultaneously (comment: "Write() re-encodes the TIFF (known
limitation)... skip byte comparison... only verify output is readable"). This
is a narrower caveat than "write is blocked" — it means the combined-IPTC+XMP
TIFF case is verified for readability at the corpus-scale test but relies on
the targeted unit tests above (which do assert value-level correctness) rather
than a corpus-scale byte-diff. Not a correctness gap by evidence gathered, but
narrower corpus-scale proof than the JPEG path has.

**Why:** Prevents future audits from repeating the "TIFF write unsupported"
claim, which would misinform architecture decisions (e.g. someone building a
new format around the false assumption that RAW containers are read-only).

**How to apply:** When asked about RAW/TIFF write capability, check write.go
isTIFFBasedFormat and the dedicated writeTIFF* functions directly rather than
trusting older memory notes. Only BigTIFF write is genuinely unsupported.

## MakerNote rebase coverage — Leica Type-0 is an open question (UNCERTAIN)

format/tiff/relocate_makernote.go documents blob-relative-safe makers (Canon,
Panasonic, Nikon Type-3, Olympus OLYMPUS\0, Pentax AOC/PENTAX) and
TIFF-absolute makers needing rebase (Sony plain-IFD, Olympus OLYMP\0). Leica
Type-0 (M8/M9/X1/X2 — plain IFD at offset 0, no magic prefix, per
exif/makernote_parse.go parseLeicaMakerNote) is NOT mentioned in either bucket.
Because Type-0 has no distinguishing prefix, isSonyPlainIFDMakerNote's
exclusion list (which excludes "LEICA\x00" — the Type 1-5 prefix, not Type-0)
would NOT prevent a Type-0 blob from being heuristically treated as
Sony-plain-IFD and rebased using the TIFF-absolute algorithm. Whether this is
correct (if Leica Type-0 truly uses TIFF-absolute OOL offsets, matching Sony)
or incorrect (if Leica Type-0 uses blob-relative offsets, meaning the generic
rebaser would wrongly move already-correct pointers) has NOT been verified
against ExifTool's Leica.pm source or real Leica M8/M9 sample files with OOL
MakerNote sub-structures. Fujifilm is similarly absent from write.go's explicit
"blob-relative safe" list, but exif/makernote_parse.go's own doc comment
confirms Fujifilm offsets are "relative to b[0]" (blob-relative) — so Fujifilm
IS safe, just under-documented in write.go's comment (documentation
completeness gap only, not a functional gap).

**Why:** This is the one open MakerNote-write correctness question remaining
after the #127 hardening round; it is narrow (JPEG write path only, requires
an OOL entry in a Leica Type-0 MakerNote specifically) and consistent in shape
with the already-accepted Nikon Type-1 documented limitation.

**How to apply:** Flag as non-blocking in reliability audits unless a
Leica-Type-0-with-OOL-MakerNote corpus file surfaces a concrete failure (none
found in the current 3307-file corpus as of 2026-07-06 — TestCorpusRoundTrip
and TestCorpusReadAll both pass with 0 failures). If go-performance-architect
wants to close this definitively, direct them to verify against ExifTool
Leica.pm's %leicaLensTypes / ProcessLeica base-offset convention for Type 0.
