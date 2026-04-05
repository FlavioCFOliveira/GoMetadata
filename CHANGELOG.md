# Changelog

All notable changes to GoMetadata are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are listed in descending order (newest first). GoMetadata adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.1] - 2026-04-05

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
- **Container support** (read and write) for: JPEG (APP1/APP13 segments), TIFF, PNG (`iTXt`/`tEXt` chunks), WebP (RIFF VP8X/EXIF/XMP chunks), HEIF/HEIC (ISO BMFF box traversal), and the RAW variants Canon CR2, Canon CR3, Nikon NEF, Sony ARW, Adobe DNG, Olympus ORF, and Panasonic RW2.
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

[Unreleased]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.0.0
