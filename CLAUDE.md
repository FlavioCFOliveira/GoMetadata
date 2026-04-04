# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## MANDATORY: Code Authorship Rule

> **ALL code creation and modification — including new files, edits, refactors, bug fixes, and test additions — MUST be performed EXCLUSIVELY by the `go-performance-architect` specialist agent via the `Agent` tool.**
>
> The assistant (Claude Code) must NEVER use the `Edit`, `Write`, or equivalent tools to modify or create source files directly. When any code change is required, the assistant must spawn the `go-performance-architect` agent with full context and delegate the change to it. This rule has no exceptions.

## Project

**GoMetadata** (`github.com/FlavioCFOliveira/GoMetadata`) — a pure Go library for **reading and writing** EXIF, IPTC, and XMP metadata from and to **any image format** (JPEG, TIFF, PNG, HEIF/HEIC, WebP, RAW variants — CR2, CR3, NEF, ARW, DNG, ORF, RW2 — and others). The library is a universal metadata layer: regardless of container format, the caller gets a unified API.

## Non-Negotiable Design Constraints

### 1. Universal format support
The library must handle any image format that can carry metadata. Format detection is by magic bytes, never by file extension. Every parser must degrade gracefully on unknown or partially-supported containers.

### 2. Ultra-performance
This library targets performance parity with the fastest native implementations (libexif, Exiv2). Every hot path must be designed with this in mind:
- Zero or near-zero heap allocation in the parsing fast path
- No unnecessary copies — prefer `[]byte` slices over allocations
- Lazy parsing: parse only what the caller asks for
- `sync.Pool` for reusable buffers
- Benchmarks are mandatory for every performance-critical function; claims about performance must be backed by `go test -bench` evidence

### 3. Exhaustive testing
Every feature must be covered by tests that **prove correctness**, not just exercise code paths:
- Table-driven unit tests for all parsers and writers
- Fuzz tests (`FuzzXxx`) for all components that consume untrusted bytes
- Integration tests against a corpus of real-world image files (multiple cameras, software, edge cases)
- Race-condition tests with `-race` for all concurrent code
- A test that fails is a bug in the library, never a bug in the test

### 4. Strict specification compliance
All parsers and writers must comply with the relevant standards:
- EXIF: CIPA DC-008 / JEITA CP-3451 (EXIF 2.x and 3.0) and TIFF 6.0
- IPTC: IPTC IIM 4.2 and IPTC Core/Extension (XMP mapping)
- XMP: ISO 16684-1/2 and Adobe XMP Specification Parts 1–3

When a real-world file deviates from the spec (manufacturer non-compliance), the library must handle it without crashing, and must document the deviation. Spec-derived decisions in code must be annotated with a comment citing the standard, section, and page.

### 6. User-oriented API
The public API must be the simplest possible interface over the internal complexity. A user must be able to read or write metadata in a handful of lines, without knowing anything about IFDs, RDF, APP13, or byte order. Complexity is internal; the surface is clean.

Guiding principles for the API:
- **One entry point** for reading, one for writing — the library detects the format automatically
- **No mandatory configuration** — sane defaults for everything; options only when genuinely needed
- **Errors are specific and actionable** — never expose internal parser state in error messages
- **Zero boilerplate** — the caller should never have to assemble byte buffers, manage offsets, or understand the container structure
- When internal complexity must surface (e.g., a tag exists in both EXIF and XMP with different values), the API resolves it with a documented, predictable policy — it does not push the decision onto the caller

The benchmark for API quality: a developer unfamiliar with image metadata standards should be able to read the camera model, GPS coordinates, and copyright from any image in under 10 lines of Go, and write a caption back in 5 more.

### 5. Read and write support
The library provides both **read** and **write** operations for all three metadata formats in all supported containers. Write operations must:
- Preserve all existing metadata not explicitly modified
- Maintain byte-level correctness (offsets, lengths, padding)
- Not corrupt the image data or other embedded structures

## Common Commands

```bash
# Build
go build ./...

# Run all tests
go test ./...

# Run a single test
go test -run TestName ./...

# Run tests with race detector
go test -race ./...

# Run benchmarks
go test -bench=. -benchmem ./...

# Fuzz a specific target (example)
go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...

# Lint
golangci-lint run
```

## Architecture

The library is organised around three metadata formats, each with a dedicated package, plus a top-level dispatcher:

- **`exif/`** — EXIF/TIFF parser and writer. IFD traversal, tag registry, byte-order handling, MakerNote dispatch, GPS IFD.
- **`iptc/`** — IPTC IIM parser and writer. Record/dataset decoding, APP13/Photoshop IRB extraction, character encoding.
- **`xmp/`** — XMP parser and writer. RDF/XML parsing, namespace registry, packet scanning and injection.
- **Top-level entry point** — accepts `io.ReadSeeker` or file path, detects container format by magic bytes, extracts the relevant metadata segments, and dispatches to the format parsers. Returns a unified `Metadata` struct.

Write operations follow the same dispatch path in reverse: serialise the modified metadata back into the correct container segment without touching image data.
