# Changelog

All notable changes to GoMetadata are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are listed in descending order (newest first). GoMetadata adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **CR3 metadata write with `stco`/`co64` offset relocation** (`format/raw/cr3/`): `cr3.Inject` now fully supports CR3 metadata writes. After rebuilding the Canon UUID box with the new CMTx payloads, it computes `delta = len(newMoovBox) - len(oldMoovBox)` and walks every `trak → mdia → minf → stbl → {stco, co64}` table inside the rebuilt `moov`, adding `delta` to each chunk offset whose absolute file position points at or beyond the original `moov` end (`>= oldMoovEnd`). Offsets that reference data before `moov` (e.g., into `ftyp`) are left unchanged. `stco` overflow (relocated value > `MaxUint32`) returns `cr3.ErrStcoOverflow` rather than silently truncating. The pass-through path (all-nil payloads) remains unchanged. `format.SupportsWrite(FormatCR3)` now returns `true`.

### Changed

- **`format.SupportsWrite` for CR3** (`format/format.go`): `SupportsWrite(FormatCR3)` now returns `true`. The task #56 safe gate (`ErrWriteNotSupported`) inside `cr3.Inject` has been removed and replaced with the full stco/co64 relocation pass. README, `doc.go`, and the knowledge-model capability matrix are updated to match.

## [1.1.0] - 2026-06-03

### Added

- **`tiff.ErrUnsupportedMagic`**: new exported sentinel error returned when the TIFF parser encounters a BigTIFF magic number (`0x002B`). Previously the library silently misidentified BigTIFF files as ordinary TIFF; now callers can detect and handle this case explicitly with `errors.Is(err, tiff.ErrUnsupportedMagic)`.
- **`xmp.ErrDocumentTooLarge`**: new exported sentinel error returned when an XMP document exceeds the `maxXMPDocumentBytes` input cap (16 MiB, compile-time constant). Callers can use `errors.Is` to distinguish this condition from malformed-XML errors.
- **`FuzzRead`**: end-to-end fuzz target at the top-level package (`FuzzRead`) that drives the full `Read` orchestrator with arbitrary input. This joins 26 existing fuzz targets for a total of 27.
- **CI fuzz job**: a new `fuzz` CI job runs 6 fuzz targets for 10 seconds each under `-race`, catching regressions on every pull request.
- **FormatCapability knowledge-graph matrix**: the format capability matrix (which combinations of format and operation are supported) is now recorded in the project knowledge graph (the `FormatCapability` matrix mirroring `format.SupportsWrite`).

### Changed

- **JPEG ExtendedXMP GUID cap**: the JPEG parser now caps the number of distinct ExtendedXMP GUIDs at 4 per file during reassembly of multi-segment ExtendedXMP payloads (each GUID is itself capped at 16 MiB, giving a 64 MiB aggregate ceiling). Excess GUIDs beyond the fourth are dropped and the reassembled payload is marked truncated, preventing memory exhaustion from crafted multi-segment JPEGs without aborting the parse.
- **Write-support documentation corrected**: README, CHANGELOG, SECURITY.md, and `doc.go` now precisely state that `Write` is supported for JPEG, PNG, WebP, HEIF/AVIF, and Canon CR3. TIFF-based containers (TIFF, CR2, NEF, ARW, DNG, ORF, RW2) are read-only; `Write` returns `ErrWriteNotSupported` for those formats.
- **`go.mod`**: `golang.org/x/text` reclassified as a direct dependency (the `// indirect` annotation removed by `go mod tidy`).
- **Test coverage**: +207 tests added since v1.0.4. New tests cover EXIF/TIFF adversarial fuzz seeds, the `internal/iobuf` buffer pool (race, contamination, and DoS scenarios), and the top-level read orchestrator.

### Fixed

- **`iptc.Encode` receiver mutation** (`iptc/iptc.go`): `Encode` previously appended the IPTC 1:90 UTF-8 `CodedCharacterSet` marker directly to the receiver's `Records[0]` slice when emitting non-ASCII content, mutating shared state and creating a data race under concurrent `Write` calls. The fix emits the 1:90 declaration to the encoded output only (via the existing `needsUTF8Declaration` path); the receiver is now pure and idempotent.
- **`internal/iobuf` pool hardening** (`internal/iobuf/iobuf.go`): `Get(n)` now clamps negative `n` to zero instead of passing it to `make`, which would have panicked. `Put` now discards buffers whose capacity exceeds the large-tier canonical cap (`largeSize = 65536`) rather than returning them, preventing unbounded pool growth from adversarially crafted payloads.
- **DoS caps and write determinism** (`exif`, `iptc`, `xmp`, `format/jpeg`): follow-up fixes from the Sprint 8 re-audit — additional byte-count caps on IFD entry aggregation, IPTC dataset aggregation, and XMP attribute accumulation; deterministic output ordering for EXIF and IPTC write paths.
- **Untrusted-input crash sites** (`exif`, `iptc`, `xmp`, `format/*`): elimination of the remaining nil-dereference and out-of-bounds-slice-index paths reachable from attacker-controlled binary data identified in the Sprint 8 audit.

### Security

- **XMP document-level input cap** (`xmp/`): introduced `maxXMPDocumentBytes` (16 MiB, compile-time constant) as a hard ceiling on the total UTF-8 bytes accepted by a single XMP parse (checked post-normalisation, before the RDF scan). Exceeding the cap returns `xmp.ErrDocumentTooLarge` without allocating further memory, preventing memory-exhaustion attacks from crafted XMP payloads (CWE-400).
- **TIFF/BigTIFF discrimination** (`format/tiff/`): the TIFF parser now reads the magic word and immediately returns `tiff.ErrUnsupportedMagic` for BigTIFF (`0x002B`). Previously the parser would misinterpret BigTIFF offsets as TIFF-6 offsets and could seek to arbitrary positions in the file or allocate large intermediate buffers (CWE-125, CWE-400).
- **JPEG ExtendedXMP GUID cap** (`format/jpeg/`): the parser caps distinct ExtendedXMP GUIDs at 4 per file (each GUID itself capped at 16 MiB, giving a 64 MiB aggregate ceiling); excess GUIDs are dropped and the result marked truncated, preventing memory exhaustion from crafted multi-segment JPEGs (CWE-400).
- **`iptc.Encode` data race eliminated** (`iptc/`): the receiver-mutation bug described under Fixed was also a data-race vulnerability under concurrent use; the fix eliminates the race without API change (CWE-362).
- **`internal/iobuf` pool hardening** (`internal/iobuf/`): the `Get(n<0)` panic path and the oversized-buffer pool-retention path are both closed, removing two crash/memory-exhaustion vectors reachable from attacker-controlled input sizes (CWE-400, CWE-476).

## [1.0.4] - 2026-04-08

### Added

- **SECURITY.md**: fuzz target inventory, supported fuzz targets (`FuzzParseEXIF`, `FuzzParseIPTC`, `FuzzParseXMP`), responsible disclosure process, and the library's security model for parser hardening.
- **CONTRIBUTING.md**: full contributor guide covering dev environment setup, build and test commands, linter configuration, fuzz testing workflow, and CI pipeline overview.
- **`examples/copyright-stamp`**: end-to-end example that reads a JPEG, sets copyright and artist metadata via EXIF and XMP, and writes the result back.
- **`examples/gallery-sidecar`**: example that extracts metadata from any supported image format and writes an XMP sidecar file alongside the original.
- **`examples/multi-format-roundtrip`**: example demonstrating a full read–modify–write cycle across JPEG, PNG, WebP, HEIF, and RAW formats.
- **`examples/raw-inspector`**: example that opens RAW files (CR2, CR3, NEF, ARW, DNG, ORF, RW2) and prints all EXIF IFD entries, MakerNote fields, and GPS data.
- **`examples/stream-transcode`**: example that streams metadata from one image format and injects it into another without loading full pixel data.
- **`example_test.go`**: runnable Go example functions in the top-level package covering EXIF, IPTC, and XMP reading and writing across all image formats; these serve as both API documentation and tested usage samples.

### Changed

- **README.md**: added an Examples section with code excerpts and links to the full example programs; added benchmark reproduction instructions so contributors can verify performance claims locally.
- **Test coverage**: expanded from 68% to 88% across all 25 packages. New tests target previously uncovered branches in `exif/makernote` (Canon, DJI, Fujifilm, Leica, Nikon, Olympus, Panasonic, Pentax, Samsung, Sigma, Sony), `format` (HEIF, JPEG, PNG, TIFF, WebP, all RAW variants), `internal` (bmff, iobuf, riff, testutil), `iptc`, `xmp`, and the top-level API (`metadata_convenience_test.go`, `options_test.go`, `read_test.go`).

## [1.0.3] - 2026-04-07

### Security

- **IPTC extended-length integer overflow** (`iptc/iptc.go`): added an immediate `length < 0` guard after the extended-length accumulation loop to prevent sign-bit overflow on 32-bit platforms (IIM §1.6.2, CWE-190).
- **IPTC unbounded aggregate allocation** (`iptc/iptc.go`): added `maxIPTCTotalBytes = 256 MiB` cap on the total size of all parsed datasets in a single stream, preventing memory exhaustion from crafted files with many large datasets (CWE-400).
- **XMP entity expansion** (`xmp/rdf.go`): `unescapeXML` now returns an empty string and recycles the pooled builder if the decoded output of a single attribute or text node exceeds 1 MiB, preventing unbounded allocation from crafted numeric character references (CWE-776).
- **EXIF IFD entry over-allocation** (`exif/ifd.go`): `parseSingleIFD` caps the pre-allocated `Entries` slice capacity at 1 024, preventing a crafted `count = 0xFFFF` field from forcing a 65 535-entry allocation before the buffer-bounds check fires (CWE-190).
- **HEIF item offset overflow** (`format/heif/heif.go`): `readItemPayload` now validates that `loc.offset` fits in `int64` before the `Seek` conversion, preventing sign-wrapping on the cast; added private `extractItemSlice` helper with the same guard for the in-memory code path.
- **PNG decompression bomb** (`format/png/png.go`): `zlibDecompress` now reads through `io.LimitReader` capped at 64 MiB and returns a sentinel error if the limit is exceeded, preventing zip-bomb-style payloads from exhausting memory.

## [1.0.2] - 2026-04-07

### Performance

- **XMP GPS parse**: `strings.Split` replaced with `strings.Cut`; GPS coordinate parsing is now zero-allocation (`BenchmarkGPSParse`: 0 B/op, 0 allocs/op).
- **XMP `Keywords`**: single-pass `strings.IndexByte` scan with `strings.Count`-pre-sized result slice replaces `strings.Split`; eliminates the intermediate `[]string` allocation per call.
- **XMP `AddKeyword`**: `strings.Builder` with pre-grown capacity replaces string concatenation; one allocation instead of two per keyword append.
- **XMP `SetGPS`**: `strconv.AppendFloat` into a `[32]byte` stack buffer replaces `fmt.Sprintf`; eliminates heap allocation and `fmt` reflection overhead per GPS encode.
- **XMP `writeMultiValuedProperty`**: `strings.IndexByte` loop replaces `strings.Split`; the `[]string` allocation on every multi-valued property encode is eliminated.
- **XMP packet scanner**: `[]byte("?>")` literals extracted to package-level variables; no heap allocation on every `Scan` call.
- **XMP RDF parser**: per-call `[]byte("-->")` and `[]byte("?>")` literals extracted to package-level; `rdf:Alt` item concatenation uses a pooled `strings.Builder`; named-entity comparison uses `switch string(ref)` (compiler-optimised zero-alloc path).
- **IPTC ISO-8859-1 decoder**: per-call `charmap.ISO8859_1.NewDecoder()` replaced with a `sync.Pool`; decoder is `Reset()` before each use.
- **HEIF write path**: `buildIlocBox` and `buildMetaBox` now measure required length in a first pass and allocate a single pre-sized output buffer, eliminating incremental `append` reallocs; `appendUintN` uses `binary.BigEndian.AppendUint16/32/64` instead of `make([]byte, n)` per field.
- **JPEG segment copy**: all four `append([]byte(nil), ...)` call sites replaced with `bytes.Clone`.
- **PNG write path**: `crc32.NewIEEE()` pooled via `sync.Pool` to avoid per-chunk hash allocation; 8-byte chunk header stack-allocated (`[8]byte` instead of `make([]byte, 8)`).
- **PNG read path**: `readChunk` refactored to a callback pattern; the pooled buffer is passed directly to the callback without cloning in the common (non-retained) path, saving one allocation and one copy per pass-through chunk.
- **WebP write path**: `bytes.Buffer` in `buildWebPBody` pooled via `sync.Pool`; 4-byte RIFF chunk size field stack-allocated.
- **ORF/RW2 write path**: only the 4-byte magic header is patched in-place on the `io.ReadAll`-owned slice; the previous full-file copy is eliminated.
- **EXIF `filterEntries`**: accepts an `extraCap` argument to pre-size the result slice, avoiding a realloc when `buildIFD0Entries` appends trailing entries.
- **`internal/bmff`**: `Box.Equal([4]byte) bool` added for zero-alloc box type comparison.
- **`internal/riff`**: `Chunk.Equal([4]byte) bool` added for zero-alloc FourCC comparison.
- **XMP date layouts**: inline `[]string` literal in `metadata.DateTime()` hoisted to a package-level `[3]string` array, eliminating the per-call slice header allocation.

### Changed

- All packages now define package-level sentinel error variables (`ErrXxx`) for every error previously constructed inline with `errors.New` or `fmt.Errorf`; callers can now use `errors.Is` for reliable error identity checks. Affected packages: `exif`, `format/heif`, `format/jpeg`, `format/png`, `format/tiff`, `format/webp`, `format/raw/cr3`, `format/raw/orf`, `format/raw/rw2`, `xmp`, and the top-level package.
- Import ordering enforced across all files (`gci` linter, stdlib → external → internal grouping).
- `t.Parallel()` added to all table-driven tests and `t.Run` callbacks across all 43 test files; the entire test suite now runs with maximum parallelism under `go test -race ./...`.
- Linter suite expanded by five additional rules: `err113` (no inline error construction), `godot` (comment punctuation), `nestif` (nesting depth ≤ 4), `godox` (no TODO/FIXME/HACK comments), `gci` (import ordering), `paralleltest`/`tparallel` (parallel test enforcement), and `funlen` (function length ≤ 80 lines / 60 statements).
- `metadata.DateTime()` refactored from four levels of nesting to guard clauses (cyclomatic complexity reduced from 6 to 1; behaviour unchanged).

### Fixed

- **`sync.Pool` use-after-put race in `format/detect.go`**: `mapMakeToFormat` was called after `tiffScanPool.Put(buf)` despite `makeRaw` being a subslice of the pooled buffer; reordered to call `mapMakeToFormat` before `Put`.
- **`sync.Pool` use-after-put race in `format/heif/heif.go`**: `extractFromMetaData` was called after `iobuf.Put(hdrPtr)` despite `metaData` being a subslice of the pooled buffer; reordered likewise.
- **PNG data lifetime bug**: `eXIf`, `tEXt`, and `iTXt` chunk handlers in `readChunk` were retaining references to a pooled buffer slice without cloning; the callback-pattern refactor ensures retained data is always copied from the pool before the buffer is returned.

## [1.0.1] - 2026-04-06

### Changed

- Linter suite expanded from 25 to 46 checked rules; contributors now benefit from stricter automated enforcement including `nilnesserr`, `wastedassign`, `recvcheck`, `inamedparam`, `nolintlint` strict mode, `intrange`, `mirror`, `modernize`, and 13 additional linters.
- All `interface{}` occurrences replaced with the `any` type alias throughout the codebase, in line with the Go 1.18+ convention.
- All functions refactored to cyclomatic complexity ≤ 10, making the codebase easier to extend and audit.
- CI pipeline hardened: golangci-lint v2.11.4 pinned to a specific version, all GitHub Actions runners updated to their latest major versions, `gofmt -s` simplification enforced on every commit, and Codecov coverage reporting integrated.
- MIT licence file added to the repository.

### Fixed

- Variable shadowing in several parser functions: inner error variables were silently shadowing outer ones in chained binary-read paths; renamed to eliminate ambiguity (`govet shadow`).
- Missing `t.Helper()` calls in test helper functions corrected; failure line numbers now point to the actual test case rather than the helper body.
- Redundant `strings.X(string(b), ...)` patterns replaced with `bytes.X(b, ...)` throughout the XMP and IPTC packages, eliminating a transient allocation per call in those hot paths.
- Several counter loops modernised to the `for i := range n` idiom (Go 1.22+).
- Superfluous `else` blocks after early returns removed throughout the parser code.
- Inconsistent receiver variable names within types corrected.

## [1.0.0] - 2026-04-04

### Added

- **Unified read/write API**: `Read(r io.ReadSeeker) (*Metadata, error)` and `Write(r io.ReadSeeker, md *Metadata) ([]byte, error)` — the container format is detected automatically from magic bytes; no configuration required.
- **`Metadata` struct** with typed accessor and setter methods for all common fields: camera make and model, lens, capture date/time, GPS coordinates (decoded to decimal degrees), copyright, artist, caption/description, keywords, rating, orientation, exposure, focal length, ISO, aperture, and shutter speed.
- **EXIF parser and writer**: full IFD traversal (IFD0, IFD1, SubIFD, GPS IFD, Interop IFD), tag registry covering approximately 200 standard CIPA DC-008/JEITA CP-3451 tags, both big-endian and little-endian byte order, and all TIFF 6.0 data types.
- **MakerNote dispatch** for 11 manufacturers — Canon, DJI, Fujifilm, Leica, Nikon, Olympus, Panasonic, Pentax, Samsung, Sigma, and Sony — each with a brand-specific tag registry.
- **IPTC IIM parser and writer**: all standard records (Records 1–9), dataset decoding, APP13/Photoshop IRB extraction, and UTF-8 character encoding via the IPTC envelope (Dataset 1:90).
- **XMP parser and writer**: full RDF/XML parsing, all RDF collection types (`rdf:Seq`, `rdf:Bag`, `rdf:Alt`), packet scanning and in-place injection, and a namespace registry covering all standard schemas (`dc`, `xmp`, `xmpRights`, `photoshop`, `Iptc4xmpCore`, `Iptc4xmpExt`, `exif`, `tiff`, `aux`, `GPS`).
- **Container support** — read for all formats: JPEG (APP1/APP13 segments), TIFF, PNG (`iTXt`/`tEXt` chunks), WebP (RIFF VP8X/EXIF/XMP chunks), HEIF/HEIC (ISO BMFF box traversal), and the RAW variants Canon CR2, Canon CR3, Nikon NEF, Sony ARW, Adobe DNG, Olympus ORF, and Panasonic RW2. Metadata write (injection) is supported for JPEG, PNG, WebP, HEIF/AVIF, and Canon CR3; TIFF-based containers (TIFF, CR2, NEF, ARW, DNG, ORF, RW2) are read-only in this release (`Write` returns `ErrWriteNotSupported`).
- Format detection by magic bytes — never by file extension.
- GPS decimal-degree decoding and encoding: degrees/minutes/seconds rational values are decoded to `float64` and re-encoded on write.
- Convenience CLI example programs shipped under `examples/`: `read-metadata`, `write-metadata`, and `batch-keywords`.
- Zero-allocation parsing fast path across all format parsers: `sync.Pool` for reusable buffers, `[]byte` slice references over copies, and lazy field parsing — only fields the caller accesses are decoded.

### Fixed

- Panic-free handling of malformed, truncated, and corrupted image files across all parsers.
- Integer overflow protection in HEIF box-length arithmetic and TIFF IFD entry counts.
- IPTC IIM record boundary compliance (IPTC IIM 4.2, §2).
- HEIF/HEIC container rewritten for correct nested-box traversal (ISO 14496-12).
- WebP RIFF chunk four-byte alignment padding applied correctly on write.
- RAW format IFD pointer resolution for manufacturer-specific sub-IFDs.

### Security

- All parsers harden against attacker-controlled offsets, lengths, and counts in binary data; no out-of-bounds read is possible on any `io.ReadSeeker` consumer.
- Fuzz targets `FuzzParseEXIF`, `FuzzParseIPTC`, and `FuzzParseXMP` ship as part of the test suite so regressions are caught automatically.

---

[Unreleased]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.4...v1.1.0
[1.0.4]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.3...v1.0.4
[1.0.3]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.0.0
