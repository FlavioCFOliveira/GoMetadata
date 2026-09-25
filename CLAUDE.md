# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## MANDATORY: Code Authorship Rule

> **ALL code creation and modification — including new files, edits, refactors, bug fixes, and test additions — MUST be performed EXCLUSIVELY by the `go-performance-architect` specialist agent via the `Agent` tool.**
>
> The assistant (Claude Code) must NEVER use the `Edit`, `Write`, or equivalent tools to modify or create source files directly. When any code change is required, the assistant must spawn the `go-performance-architect` agent with full context and delegate the change to it. This rule has no exceptions.
>
> All non-code work is likewise delegated to the most suitable specialist subagent. Subagents run one at a time, in series — see the [Subagent Policy](#subagent-policy).

## Project

**GoMetadata** (`github.com/FlavioCFOliveira/GoMetadata`) — a pure Go library for **reading and writing** EXIF, IPTC, and XMP metadata from and to **any image format** (JPEG, TIFF, PNG, HEIF/HEIC, WebP, RAW variants — CR2, CR3, NEF, ARW, DNG, ORF, RW2 — and others). The library is a universal metadata layer: regardless of container format, the caller gets a unified API.

## Non-Negotiable Design Constraints

> **ABSOLUTELY INVIOLABLE REQUIREMENTS**
>
> GoMetadata is bound by three inviolable compliance mandates. These are hard requirements, not
> goals, and they admit no exception:
>
> - **100% EXIF compliant** — the library MUST support the EXIF specification faithfully, across
>   **every version of the standard**, and MUST be able to **read from and write to** image files
>   **without corrupting** the file or any of its metadata.
> - **100% IPTC compliant** — the library MUST support the IPTC specification faithfully, across
>   **every version of the standard**, and MUST be able to **read from and write to** image files
>   **without corrupting** the file or any of its metadata.
> - **100% XMP compliant** — the library MUST support the XMP specification faithfully, across
>   **every version of the standard (including all supported XML serialisations)**, and MUST be
>   able to **read from and write to** image files **without corrupting** the file or any of its
>   metadata.
>
> "Faithful to the specification" means byte-level correctness on both read and write: existing
> metadata that is not explicitly modified is preserved exactly, offsets/lengths/padding stay
> valid, and image data and other embedded structures are never damaged. The authoritative
> specifications and the measurable conformance contract for each of these three formats are
> defined in [§4 below](#4-strict-specification-compliance--100-conformance-is-a-hard-requirement).

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

## Decision Framework

To guide decisions about the expected outcome for the project — whether in evaluations and audits or during development (code implementation) — follow these guidelines, applied in strict priority order (**correct → safe → fast**):

1. **Is it correct?** — Ask whether the outcome (of an evaluation or of a task) meets the objective, conforms to the project specification, and complies with the applicable specifications, RFCs, and authoritative sources.
2. **Is it safe?** — Ask whether the decision to be taken, or the task to be developed, is free of any characteristic or behaviour that could compromise the safe use of the deliverable in question.
3. **Is it fast?** — Ask whether the decision or task is as fast as achievable without compromising correctness (accuracy, precision, assertiveness) or the safety of the requirements, and what can be done to give the deliverable the highest possible performance.

If conflicts arise, or these steps prove difficult to follow, immediately ask the user how to proceed, presenting the available options.

---

## Work Synergy Policy

Motto: **"Maximise the return on effort: deliver the most with the least work."**

This is the default way of working on this project. The user MUST NOT need to ask for it, and it MUST NOT be modelled as an `rmp` task.

- **Merge related tasks.** Whenever tasks — in `rmp` or requested ad hoc by the user — show verifiable functional or technical proximity, merge them into a single development effort.
- **Batch work within and across tasks.** Write all the code for the merged tasks in one pass, then test all the changes in one pass. Do not write small fragments and test each one in isolation.
- **Batch documentation.** Write all the documentation in one pass. When the scope is too large, split it into a few coherent blocks and handle each block in one pass.
- **Apply the principle to all work**, not only to code and documentation.
- **Minimise tasks and iterations.** Reach each objective with the smallest possible number of tasks and iterations.

---

## Work Convergence Policy

- **Always look for convergence** between the individual objectives of each task, and turn that convergence into synergy. Tasks with complementary objectives, functional proximity, or technical proximity MUST always be grouped so that the effort is optimised and the synergies are maximised.
- **Group work of the same type**: write all the code at once, write all the documentation at once, and run the tests for all changed code at once.
- **Quality is not negotiable.** Grouping MUST deliver work that is faster and cheaper than task-by-task development and at least equal in quality. If grouping would reduce quality, do not group.
- **Maximise internal resources** (skills, subagents, the Knowledge Graph, existing tooling) so that deliveries are faster and cost the user less.

---

## Subagent Policy

- **Delegate all work.** ALL work on this project MUST be delegated to the specialist subagent whose expertise best matches the objective of the task. Always choose the most suitable subagent. Go code is always delegated to `go-performance-architect`, per the mandatory Code Authorship Rule above.
- **One subagent at a time.** Run **exactly one** subagent in parallel with the main Claude Code conversation. NEVER run more than one subagent at the same time.
- **Serial, not parallel.** Use as many subagents as the objective requires, but run them one after another.
- **Exceptions are temporary.** When the user explicitly authorises more than one parallel subagent, that authorisation applies only to the current task and is revoked automatically when the task ends.

---

## Language Policy

All writing and all interpretation of instructions on this project MUST be:

- **Explicit** — state clearly what is intended.
- **Objective** — state precisely what must be executed.
- **Closed** — define the scope of the work to be done.
- **Concise** — use as few words as possible.

### Documentation Language

**All documentation** — from the main README to the specifications, including code documentation (comments and doc comments) — MUST be written in flawless English, in a professional tone, with no spelling, grammatical, or syntax errors. It MUST be accurate and faithful to the code, aimed at human readers, and follow the four rules above (explicit, objective, closed, concise).

---

## Scope Discipline (No Unrequested Work)

Act strictly towards the objective of the current work. You are FORBIDDEN from voluntarily starting any task that was not EXPLICITLY requested. Whenever you identify a need outside the scope of the task in execution — including pre-existing bugs, new requirements, improvements, or clean-ups — ask the user how to proceed. NEVER start that work proactively.

---

## Completeness

You are FORBIDDEN from executing any task or piece of work only partially. Every piece of work that is started MUST be executed in full. **DO NOT LEAVE TASKS HALF-DONE OR PARTIALLY DONE.**

---

## Skills

Use the following Claude Code skills for the corresponding operations:

- **`gitflow`** — ALL git write commands (branches, commits, merges, tags, releases, hotfixes) MUST be executed through the `gitflow` skill, following the gitflow branching model adopted by this repository.
- **`roadmap-manager`** — ALL coordination and management of tasks, sprints, and comments, and ALL other `rmp *` CLI operations **except `rmp graph *`**, MUST be executed through the `roadmap-manager` skill.
- **`knowledge-authority`** — The project's Knowledge Graph (`rmp graph *`) MUST be managed EXCLUSIVELY through the `knowledge-authority` skill, for both queries and updates.

---

## Development Workflow

Every development cycle must follow these steps in order:

**Specify → Implement → Test → Document**

Apply the Work Synergy and Work Convergence policies to each step: when a cycle covers several merged tasks, specify them together, implement them together, test them together, and document them together.

---

## Self-Contained Development Policy

All development cycles must be self-contained and must produce a working deliverable, in accordance with the Completeness policy. When a requirement that is necessary to complete the current task is discovered during that task, resolve it within the same cycle. Any need outside the scope of the current task is governed by the Scope Discipline policy: ask the user before acting.

All code and development must be **full-fledged** by default. Skipping a test to hide a library bug is forbidden. `t.Skip` is permitted only for the three narrow categories defined in [`docs/TESTING.md §2.1`](docs/TESTING.md): corpus file absent, OS/privilege limitation, or a stale boundary-constant guard. In every other circumstance, use `t.Fatal` so the failure is visible. See [`docs/TESTING.md`](docs/TESTING.md) for the complete testing policy.

Whenever a pre-existing bug is found, report it to the user and ask how to proceed, per the Scope Discipline policy. Fix it within the current task only when it blocks the current task or when the user authorises the fix.

---

## Production-Oriented

Every stage of the work cycle — analysis, planning, development, testing — must target **production-grade** output. Apply maximum knowledge and care to ensure every deliverable is ready for production use.

---

## Task Planning and Execution

Use the `rmp` CLI, operated exclusively through the `roadmap-manager` skill, for all planning and task coordination. Treat `rmp` as the single source of truth for planning and execution — no other mechanism may be used for this purpose.

Use the **Knowledge Graph**, queried through the `knowledge-authority` skill, to understand the project, its components, and how they relate, in order to identify the scope and impact of each task.

### Planning

Assess the proposed work and determine whether multiple development phases (sprints) are needed, each delivering a solid, well-defined deliverable. Apply the Work Synergy and Work Convergence policies: plan the smallest possible number of sprints and tasks, and merge tasks with functional or technical proximity.

Every task must have a clear, objective definition of: goals, functional requirements, technical requirements, and acceptance criteria that confirm the task is complete. When a task is closed, include a brief summary of what was done.

Phases are modelled as **sprints** in `rmp`. When multiple sprints are needed, first define all sprints and their scope, then populate each sprint with tasks — one sprint at a time — using `rmp` as the single source of truth throughout.

Use the Knowledge Graph to identify high-value and foundational tasks, and to optimise the execution order. Tasks that yield the greatest gain or impact, that unblock other tasks or features, or that are foundational must always take priority: by default, work from the highest-gain tasks toward the least essential ones.

Subdivide a task only when its scope is too large for a single development cycle run by one AI coding agent. Every part must remain a self-contained, working deliverable.

### Execution

Task execution is the natural continuation of planning. Always, through the `roadmap-manager` skill, use `rmp` to:

1. Check whether any open task is already in progress and resume it if so.
2. Identify the next task, together with every other open task that can be merged with it under the Work Synergy and Work Convergence policies.
3. Understand the objective of each selected task from its description, functional requirements, and technical requirements.
4. Determine the most appropriate specialist subagent and delegate the execution to it, per the Subagent Policy (one subagent at a time, in series). Go code is always delegated to `go-performance-architect`, per the mandatory Code Authorship Rule above.
5. Validate the acceptance criteria before closing each task.
6. Close each task with a brief summary of what was done.
7. Create the git commit(s) describing what was done, through the `gitflow` skill.
8. Update the Knowledge Graph through the `knowledge-authority` skill.

Whenever possible, adapt the model and its reasoning-effort level to the requirements of each task's individual operations.

Sprints must always be executed sequentially. Tasks within a sprint may be merged into a single effort under the Work Synergy and Work Convergence policies; subagents always run in series, per the Subagent Policy.

---

## Specialist Agent Team

Treat every available subagent as a member of a single multidisciplinary development team. Each agent carries a distinct area of expertise, and every one of those areas must be exploited to its fullest. Route each piece of work to the agent whose specialism fits it best — code, specifications, security, performance, releases, research, and every other discipline represented — so that no area of intervention is under-used. Collaboration across these specialists is the default mode of work; it always happens in series, one subagent at a time, per the Subagent Policy.

---

## Knowledge Graph

The project Knowledge Graph lives in the `rmp` graph features (Groadmap) and is managed EXCLUSIVELY through the `knowledge-authority` skill. The graph **MUST contain everything** that is useful to know about the project — examples: features, where they are specified, where they are implemented, which tests exist and what they test, components and their relationships, dependencies, the git commits in which each feature was specified, implemented, and tested, `rmp` tasks, component tasks, and any other information that may be useful to map.

The graph **MUST always be updated** on every git commit, reflecting changes to graph objects, and identifying the commit hash and date of each update.

**This graph is the absolute source of truth about the project.** Maintain it with rigorous focus so that, before reading files, you can consult the graph and know what you need.

Create whatever nodes and edges make the most sense for the project and for your activity. Use the graph together with tasks and sprints to coordinate project work.

---

## Never Guess

All interactions in the project must be based exclusively on knowledge you already have. Never attempt to guess expected answers. When available information is insufficient, search for answers on the internet from official or authoritative sources, papers, books, or domain experts to determine the best outcome.

Use the Knowledge Graph as the primary source of information — both to query what is already known and to record the relationships you discover as you work.

---

## Measure to Decide

Whenever it is necessary to evaluate performance, completeness, or correctness, always collect evidence from the project first. Decide empirically.

---

## Regression Prevention

Whenever a bug is fixed, create the regression tests needed to guarantee that the same bug cannot recur as a consequence of future development.

---

## Separation of Responsibilities

Every package, component, and function must follow a strict separation-of-responsibilities pattern in order to maximise code reuse.

---

## Memory

Use the Knowledge Graph, through the `knowledge-authority` skill, as the memory of the project, of the agents, and of the skills. Take maximum advantage of the relational capabilities of the graph database (`rmp graph`) to optimise how you read and write your memories, and use this method to avoid the token cost of reading files whenever the graph already holds the answer.

Whenever project files change, update the Knowledge Graph so that your understanding of the project stays current.
