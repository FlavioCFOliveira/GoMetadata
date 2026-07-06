---
name: audit-findings-20260706-xmp-root-concurrency-pass2
description: Second-pass fresh audit 2026-07-06 of xmp/, root API (metadata.go/read.go/write.go/write_unix.go/write_windows.go/options.go/doc.go), and the concurrency model, at HEAD 48e6f20 (source unchanged since 6a0bbc6). 2 NEW HIGH findings in write.go's TIFF-family write paths: ORF-DBLWR-01 (confirmed silent image-data corruption on repeated Write() calls) and WRITE-OOM-01 (confirmed unbounded io.ReadAll, bypasses the #140 256 MiB convention). XMPCONC-01 and all 2026-06-09 XMP findings reconfirmed closed.
metadata:
  type: project
---

## Scope and baseline

Fresh independent second-pass audit of xmp/, root API surface, and concurrency
model at HEAD 48e6f20. `git diff --stat 6a0bbc6..HEAD -- xmp/ metadata.go
read.go write.go options.go errors.go doc.go write_unix.go write_windows.go`
is empty — confirmed no source changed since the first 2026-07-06 pass
([[production_readiness_20260706]], [[audit_findings_20260706_xmp_root_concurrency]]).

Tooling: `go vet ./...` clean, `govulncheck ./...` clean, `go test -race
./xmp/... .` PASS (0 races). Fuzz: `FuzzParseXMP` 17.8M execs/45s 0 crashers;
`FuzzRead` 2.27M execs/45s 0 crashers.

## Priors reconfirmed CLOSED (independent re-verification, not just re-read of memory)

- XMPCONC-01: `xmp/xmp.go:23-32`, `exif/exif.go:48`, `iptc/iptc.go:569` all now
  carry the "Thread-safety: not safe for concurrent use" doc comment;
  `metadata.go:43-63` cross-references them explicitly and states the mutex
  guards `Metadata.Set*` only. Verified with a live probe: 16 goroutines ×
  500 iterations calling every `Metadata.Set*` method concurrently on one
  shared `*Metadata` — `go test -race` clean, 0 races. The documented-safe
  surface (`Metadata.Set*`) is genuinely race-free; the sub-struct direct-
  mutation gap remains an explicit, honest documentation contract (not a
  code fix) — this was the deliberate remediation choice (option a) and is
  correctly and consistently applied.
- XMP NS-URI/local-name injection (write.go:92, writeXMLName call sites):
  still present and correct.
- XXE/entity expansion: independently re-verified at the byte level.
  `decodeEntity`/`decodeNamedEntity` (rdf.go:1185-1220) only ever handle the
  5 predefined XML entities + numeric char refs via `decodeCharRef`
  (parseHex/parseDec, both reject >7 digits and validate the Unicode scalar
  range, no overflow). There is no DTD/entity table anywhere in the package,
  so no expansion mechanism exists to abuse regardless of how `skipBang`
  (rdf.go:568) delimits a `<!DOCTYPE ...>` construct. Noted a benign
  parsing quirk: `skipBang` finds the FIRST unescaped `>`, so a DOCTYPE
  with an internal subset containing `<!ENTITY ... >` (which itself
  contains a `>`) will under-skip and leave stray `]>` bytes before the
  real root element; these are inert (not "<", so never re-enter tag
  parsing) and harmless — not filed as a finding, matches 6.8M+17.8M-exec
  fuzz evidence of 0 crashers across two independent runs today.
- DETECT-SHORTREAD-01 fix confirmed present at format/detect.go:87 (`io.ReadFull`).
  The single `r.Read(magic[:])` at `read.go:75` is COSMETIC ONLY — it exists
  solely to populate `UnsupportedFormatError.Magic` for the error string
  (errors.go:26-32, `fmt.Sprintf("%x", e.Magic[:])` on a fixed `[12]byte`
  array) after `format.Detect` has already returned `FormatUnknown`; a short
  read here cannot cause a misdetection (detection already happened) and
  cannot OOB (fixed-size array, formatted whole). Not a finding.
- WriteFile atomicity (#124/#125): re-read in full; temp+fsync+rename,
  symlink resolution via EvalSymlinks, chmod-before-write, best-effort chown,
  `renamed` flag guarding the deferred cleanup — all correct, matches prior audit.

## NEW FINDINGS (both in write.go, both confirmed via public-API PoC)

### ORF-DBLWR-01 / RW2-DBLWR-01 — HIGH — silent image-data corruption on repeated Write()

- **Location**: `write.go:601` (`writeTIFFORF`) and `write.go:674`
  (`writeTIFFRW2`) pass `m.EXIF` directly to `tiff.InjectWithEXIFORF` /
  `tiff.InjectWithEXIFRW2`. Every sibling TIFF-family write function
  (`writeTIFF:415`, `writeTIFFCR2:466`, `writeTIFFARW:526`,
  `writeTIFFNEF:933`) passes `cloneEXIF(m.EXIF)` instead.
- **Root cause**: `relocateTIFFFromParsed`/`relocateTIFFFromParsedORF`/
  `relocateTIFFFromParsedRW2` (format/tiff/relocate*.go) permanently mutate
  the `*exif.EXIF` they receive in place: `e.IFD0.ThumbnailData = nil`,
  `removeImageOffsetEntries` deletes StripOffsets/StripByteCounts entries,
  `insertPlaceholders`/`upsertIFDEntryWithCount` re-inserts NEW entries whose
  `Value` slices get patched with the FINAL, already-relocated absolute
  offsets from THIS write. This is exactly the mutation class that finding
  `#109` (see the `cloneEXIF` doc comment at write.go:810-839) already fixed
  for TIFF/CR2/ARW/NEF — but ORF and RW2 were never updated to match when
  their dedicated write paths were added (task #104).
- **Confirmed PoC (ORF)**: `Read()` a real fixture
  (`testdata/corpus/raw/metadata-extractor/Olympus E410.orf`), call
  `m.SetOrientation(3)` once, then call `Write()` twice into two separate
  buffers using the SAME `*Metadata`. Both writes succeed with no error and
  produce the SAME LENGTH (10,275,850 bytes) but DIFFERENT CONTENT, first
  diverging at byte offset 653532 deep in the image-data region (not just a
  metadata-header difference) — real corrupted image bytes, sourced from the
  wrong offset in the second pass because the second call reads a
  StripOffsets/MakerNote-pointer value that reflects the FIRST write's
  post-relocation layout, not the original file's layout, then uses that
  stale value as `srcOffset` into the (unmodified) `originalBytes`.
- **RW2**: identical code omission confirmed by source read
  (`write.go:674`), but the two available fixtures
  (`Panasonic DMC-GF1.rw2` with `SetOrientation`/`SetCaption`) did not
  visibly manifest divergence in this session — plausibly because those
  fixtures' RW2-specific relocation path happens to be numerically
  idempotent for the specific tag/size deltas exercised (e.g. IFD size
  unchanged between calls, keeping computed offsets equal by coincidence).
  Classified PROBABLE (not CONFIRMED) for RW2 specifically; the underlying
  code defect is identical and equally real. Recommend the fix be applied
  symmetrically to both and a `TestWriteTwicePreservesMetadata` subcase
  be added for ORF and RW2 (see below — the existing regression test for
  `#109` at write_test.go:1802 only covers TIFF/NEF/ARW subcases; ORF/RW2
  were never added, confirming this is a genuine test-coverage gap that let
  the regression through undetected).
- **Impact**: Silent, undetected corruption of the user's own RAW camera
  image data — no error returned, same output length (defeats naive
  size-based integrity checks) — on any ordinary caller pattern that invokes
  `Write`/`WriteFile` more than once against the same `*Metadata` for an ORF
  or RW2 source (e.g. writing tagged output to two destinations, retry-on-
  transient-error logic, or any batch/pipeline code that reuses one
  `*Metadata` across multiple write targets). Directly violates the
  project's own "Write operations must ... not corrupt the image data"
  guarantee (CLAUDE.md §5) and SECURITY.md's "no data corruption" contract.
- **Reachability**: CONFIRMED via public API (`Read` → `Set*` → `Write` ×2),
  zero malicious/malformed input required — a real, valid, unmodified camera
  file is sufficient.
- **CWE**: closest official mapping is CWE-664 (Improper Control of a
  Resource Through its Lifetime) — a shared mutable object is reused
  destructively across independent logical operations without being reset
  or cloned. Same class as the project's own prior fix for `#109`.
- **Remediation** (for go-performance-architect): change `write.go:601` to
  `tiff.InjectWithEXIFORF(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w)`
  and `write.go:674` to
  `tiff.InjectWithEXIFRW2(originalBytes, cloneEXIF(m.EXIF), rawIPTC, rawXMP, w)`,
  matching every sibling TIFF-family write function. `cloneEXIF` already
  exists and is fully correct (write.go:840-880) — this is a one-line fix at
  each of the two call sites.
- **Regression test**: extend `TestWriteTwicePreservesMetadata`
  (write_test.go:1811) with "ORF" and "RW2" subcases using real corpus
  fixtures (Olympus E410.orf reproduces it directly; for RW2 use a fixture
  and mutation combination that changes IFD0 entry count/size between the
  two calls — e.g. `SetCaption` on a fixture with no pre-existing
  ImageDescription tag — to force a non-idempotent skeleton size and
  guarantee the corruption manifests deterministically in CI).

### WRITE-OOM-01 — HIGH — unbounded io.ReadAll in all 6 TIFF-family write paths

- **Location**: `write.go:377` (`writeTIFF`), `:443` (`writeTIFFCR2`), `:503`
  (`writeTIFFARW`), `:579` (`writeTIFFORF`), `:652` (`writeTIFFRW2`), `:910`
  (`writeTIFFNEF`) — all six call bare `io.ReadAll(r)` with NO
  `io.LimitReader` wrapper, in the fallback branch taken whenever
  `m.rawEXIF == nil`.
- **Root cause**: task `#140` wrapped every `io.ReadAll` call inside
  `format/*` package extractors/injectors (tiff.Extract, tiff.Inject,
  heif.Extract/Inject, webp.Inject, cr3.Extract, orf.Extract, rw2.Extract —
  all confirmed via grep to use `io.ReadAll(io.LimitReader(r,
  maxFileSize+1))` with `maxFileSize = 256 << 20`, 256 MiB) — but the SIX
  bare `io.ReadAll(r)` calls added directly in root `write.go` for the
  TIFF-family dedicated write paths were never covered by that fix. These
  fire whenever `m.rawEXIF` is nil, i.e. whenever `m` was constructed via
  `NewMetadata(fmtID)` (NOT via `Read()`) and then passed to `Write`/
  `WriteFile` — this is the explicitly documented, intended usage pattern
  for `NewMetadata` (see its own doc comment: "assign m.EXIF, m.IPTC, or
  m.XMP directly before passing m to Write or WriteFile").
- **Confirmed PoC**: `m := NewMetadata(format.FormatTIFF)` (a literal no-op
  Metadata — `m.EXIF`/`IPTC`/`XMP` all nil), `Write(r, io.Discard, m)`
  against a synthetic `io.ReadSeeker` presenting a minimal valid classic-TIFF
  header (8-byte header + IFD0 with 0 entries) followed by 300 MiB of zero
  bytes. `Write` returns nil error and the entire 300 MiB (comfortably over
  every sibling package's 256 MiB `maxFileSize` convention) is read into
  memory and passed straight through — the `io.ReadAll(r)` call executes
  UNCONDITIONALLY, even before the later "nothing to do" pass-through short-
  circuit check (`rawIPTC == nil && rawXMP == nil && m.EXIF == nil`), so even
  a completely no-op `Write` call triggers it. There is no upper bound at
  all in this code path (unlike the format/* packages, which at least cap at
  256 MiB and return a clear error above that) — a sufficiently large or
  effectively-infinite stream would run the process out of memory.
- **Impact**: memory-exhaustion Denial of Service, reachable via the primary
  public `Write`/`WriteFile` API with zero malformed/malicious file
  structure required — just a long stream. Realistic threat model: any
  service that accepts an untrusted upload and calls
  `gometadata.Write(upload, out, gometadata.NewMetadata(detectedFormat))` to
  tag it before storage, without first calling `Read()`.
- **Reachability**: CONFIRMED via public API, ordinary/documented usage
  pattern, no adversarial file content needed.
- **CWE**: CWE-770 (Allocation of Resources Without Limits or Throttling) /
  CWE-400 (Uncontrolled Resource Consumption).
- **Remediation** (for go-performance-architect): wrap all six call sites
  with the same `io.LimitReader(r, maxFileSize+1)` + explicit
  `len(originalBytes) > maxFileSize` check + clear error pattern already
  used throughout `format/tiff`, `format/heif`, `format/webp`,
  `format/raw/{orf,rw2,cr3}` (task #140). Since these six sites live in the
  root package (not `format/tiff`), either export a shared constant from
  `format/tiff` (or a new small `internal/` helper) or define an equivalent
  private `maxFileSize` in the root package — recommend reusing
  `format/tiff.maxFileSize`'s value (256 MiB) for consistency across the
  whole TIFF family.
- **Suggested test**: a table-driven test (mirroring the PoC harness above,
  a synthetic zero-allocating `io.ReadSeeker`) asserting that `Write` returns
  a clear, actionable error (not an OOM) when the source stream for a
  `rawEXIF == nil` TIFF-family write exceeds the cap, for each of the six
  affected format IDs (TIFF, DNG, CR2, NEF, ARW, ORF, RW2).

## Fuzz targets exercised

- `xmp.FuzzParseXMP`: 17,828,425 execs / 45s, 0 crashers, 8 new interesting inputs added to corpus (not committed — ephemeral fuzz cache only, confirmed no testdata/fuzz changes left in git status).
- root `FuzzRead`: 2,266,279 execs / 45s (some workers stalled on expensive large-corpus entries, expected/benign), 0 crashers.

## Clearance status for this scope

FINDINGS PRESENT (2 new HIGH). XMPCONC-01 and all prior xmp/root/concurrency
findings remain correctly fixed. Neither new finding requires any change to
`xmp/` itself — both are confined to `write.go`'s TIFF-family dispatch (root
package). Release should hold for MEDIUM+ until ORF-DBLWR-01/RW2-DBLWR-01 and
WRITE-OOM-01 are fixed and re-audited.
