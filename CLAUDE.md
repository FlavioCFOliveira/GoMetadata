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

### 4. Strict specification compliance — 100% conformance is a hard requirement

**GoMetadata MUST achieve 100% conformance with the official specification of every metadata
format and every container format it supports.** This is a non-negotiable, measurable
requirement, not an aspiration. Every format MUST be covered by an exhaustive, spec-clause-driven
conformance test battery that proves correctness against the standard — coverage of code paths is
not sufficient; the tests must prove the library obeys the specification.

The normative-requirements checklists that define this contract live in
[`docs/conformance/`](docs/conformance/) (one per spec family). Every checklist rule has a stable
ID (e.g. `S-08`, `IIM-BIN-05`, `JPEG-04`, `ROB-03`) that is used verbatim as the corresponding Go
sub-test name, so a failing test points directly at the violated specification clause.

**Authoritative specifications (the compliance targets):**

| Format | Official specification(s) |
|---|---|
| EXIF | CIPA DC-008 (Exif 3.0, 2023/2024) / DC-X008 (Exif 2.32, 2019) / JEITA CP-3451 |
| TIFF | Adobe TIFF Revision 6.0 (1992); BigTIFF (Aware Systems / libtiff) |
| IPTC IIM | IPTC-NAA Information Interchange Model 4.2 (2014) |
| IPTC Core/Ext | IPTC Photo Metadata Standard 2025.1 (Core 1.5 / Extension 1.9) |
| XMP | ISO 16684-1:2019 + ISO 16684-2:2014; Adobe XMP Specification Parts 1–3; MWG Guidelines v2.0 |
| JPEG / JFIF | ITU-T T.81 \| ISO/IEC 10918-1; ITU-T T.871 \| ISO/IEC 10918-5 |
| PNG | W3C PNG Specification 3rd Edition (Rec. 2025-06-24) \| ISO/IEC 15948:2004 |
| WebP | IETF RFC 9649 (2024) + Google WebP Container Specification (RIFF) |
| ISO BMFF | ISO/IEC 14496-12 |
| HEIF / HEIC | ISO/IEC 23008-12 |
| AVIF | AOM "AV1 Image File Format" v1.2.0 (on HEIF + MIAF ISO/IEC 23000-22) |
| DNG | Adobe Digital Negative Specification 1.7.1.0 (2023) |
| TIFF/EP RAW (NEF, ARW, CR2, ORF, RW2) | ISO 12234-2:2001 (TIFF/EP); reverse-engineered refs: ExifTool, LibRaw, lclevy |
| CR3 | Canon CR3 (ISO BMFF / `crx`); reverse-engineered ref: lclevy canon_cr3 |
| Container date strings | RFC 3339 (XMP date subset of ISO 8601) |

When a real-world file deviates from the spec (manufacturer non-compliance), the library must
handle it without crashing, must degrade gracefully, and must document the deviation. Spec-derived
decisions in code must be annotated with a comment citing the standard, section, and page (the
checklists in `docs/conformance/` provide the citations).

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

## Decision Policy

You are NOT authorised to make decisions unilaterally. Whenever instructions are insufficient, unclear, not specific, not concrete, or contain contradictions or ambiguities, you MUST ALWAYS ask the user how to proceed. When asking, provide multiple options (a, b, c, …) and indicate your recommendation. When there are multiple clarification needs, present each question to the user sequentially, one at a time.

---

## Documentation Policy

All project documentation must be written in English — correct, precise, grammatically and orthographically flawless. Use clear, simple, unambiguous technical language aimed at human readers. Documentation must be accurate and faithful to the code.

---

## Development Workflow

Every development cycle must follow these steps in order:

**Specify → Implement → Test → Document**

---

## Self-Contained Development Policy

All development cycles must be self-contained. Never complete only part of a task — each development cycle must produce a working deliverable. When new requirements are discovered during a task, resolve them immediately within the same cycle (add new tasks and implement them as quickly as possible).

All code and development must be **full-fledged** by default. Skipping a test to hide a library bug is forbidden. `t.Skip` is permitted only for the three narrow categories defined in [`docs/TESTING.md §2.1`](docs/TESTING.md): corpus file absent, OS/privilege limitation, or a stale boundary-constant guard. In every other circumstance, use `t.Fatal` so the failure is visible. See [`docs/TESTING.md`](docs/TESTING.md) for the complete testing policy.

Whenever pre-existing bugs are found, fix them immediately and continue the original task.

---

## Production-Oriented

Every stage of the work cycle — analysis, planning, development, testing — must target **production-grade** output. Apply maximum knowledge and care to ensure every deliverable is ready for production use.

---

## Task Planning and Execution

Use the `rmp` CLI (available on the system) for all planning and task coordination. Treat `rmp` as the single source of truth for planning and execution — no other mechanism may be used for this purpose.

Use the **Knowledge Graph** to understand the project, its components, and how they relate, in order to identify the scope and impact of each task.

### Planning

Assess the proposed work and determine whether multiple development phases (sprints) are needed, each delivering a solid, well-defined deliverable.

Every task must have a clear, objective definition of: goals, functional requirements, technical requirements, and acceptance criteria that confirm the task is complete. When a task is closed, include a brief summary of what was done.

Phases are modelled as **sprints** in `rmp`. When multiple sprints are needed, first define all sprints and their scope, then populate each sprint with tasks — one sprint at a time — using `rmp` as the single source of truth throughout.

Use the Knowledge Graph to identify high-value and foundational tasks, and to optimise the execution order.

### Execution

Task execution is the natural continuation of planning. Always use `rmp` to:

1. Check whether any open task is already in progress and resume it if so.
2. Identify the next task.
3. Understand the task objective from its description, functional requirements, and technical requirements.
4. Validate acceptance criteria before closing the task.
5. Close the task with a brief summary of what was done.
6. After closing the task, create a git commit following best practices that describes what was done.
7. Update the Knowledge Graph.

Sprints must always be executed sequentially. Tasks within a sprint may be parallelised when there is clear justification for doing so.

---

## Knowledge Graph

Use the `rmp` graph features (Groadmap) to create, maintain, and query a knowledge graph of the project. The graph **MUST contain everything** that is useful to know about the project — examples: features, where they are specified, where they are implemented, which tests exist and what they test, components and their relationships, dependencies, the git commits in which each feature was specified, implemented, and tested, `rmp` tasks, component tasks, and any other information that may be useful to map.

The graph **MUST always be updated** on every git commit, reflecting changes to graph objects, and identifying the commit hash and date of each update.

**This graph is the absolute source of truth about the project.** Maintain it with rigorous focus so that, before reading files, you can consult the graph and know what you need.

Create whatever nodes and edges make the most sense for the project and for your activity. Use the graph together with tasks and sprints to coordinate project work.

---

## Never Guess

All interactions in the project must be based exclusively on knowledge you already have. Never attempt to guess expected answers. When available information is insufficient, search for answers on the internet from official or authoritative sources, papers, books, or domain experts to determine the best outcome.

---

## Measure to Decide

Whenever it is necessary to evaluate performance, completeness, or correctness, always collect evidence from the project first. Decide empirically.
