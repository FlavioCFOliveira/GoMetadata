# Contributing to GoMetadata

Thank you for your interest in contributing to GoMetadata. This document covers everything you need to get started: setting up the development environment, running the test suite, and the standards every pull request must meet.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Getting started](#getting-started)
- [Running tests](#running-tests)
- [Running the linter](#running-the-linter)
- [Running benchmarks](#running-benchmarks)
- [Running the full CI check locally](#running-the-full-ci-check-locally)
- [Fuzz testing](#fuzz-testing)
- [Coverage](#coverage)
- [Coding standards](#coding-standards)
- [Pull request checklist](#pull-request-checklist)
- [Commit message style](#commit-message-style)
- [Reporting issues](#reporting-issues)

---

## Prerequisites

| Tool | Minimum version | Notes |
|---|---|---|
| Go | 1.26 | <https://go.dev/doc/install> |
| golangci-lint | latest stable | `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` |
| GNU Make | any | Optional; all targets have plain `go` equivalents |

No non-stdlib runtime dependencies are used. `go mod download` fetches only the `golang.org/x/text` indirect dependency.

---

## Getting started

```bash
# 1. Fork the repository on GitHub, then clone your fork
git clone https://github.com/<your-username>/GoMetadata.git
cd GoMetadata

# 2. Download dependencies
go mod download

# 3. Verify the build is clean
go build ./...
```

The repository layout:

```
exif/       EXIF/TIFF parser and writer
iptc/       IPTC IIM parser and writer
xmp/        XMP/RDF parser and writer
format/     Container format detection and segment extraction
internal/   Shared byte-order utilities, pool helpers, error types
```

The top-level package (`gometadata`) is the single public entry point. Internal packages are not part of the public API and may change without notice.

---

## Running tests

```bash
# All packages
go test ./...
make test

# With the race detector (required before opening a PR)
go test -race ./...
make test-race

# A single test by name
go test -run TestReadFile ./...
```

Tests are table-driven. Every new feature or bug fix must be accompanied by a test that fails without the change and passes with it.

To download the real-world image corpus used by the integration tests:

```bash
make testdata
```

---

## Running the linter

```bash
golangci-lint run ./...
make lint
```

All lint warnings must be resolved before a PR is merged. If a warning must be suppressed, add a `//nolint:<linter>` comment with a clear explanation on the same line.

---

## Running benchmarks

```bash
make bench
# Equivalent to:
go test -bench=. -benchmem -count=5 ./...
```

Performance is a first-class requirement. PRs that introduce a measurable regression on an existing benchmark will not be merged. If your change touches a hot path, include benchmark output for both the baseline (main branch) and your branch in the PR description.

Every performance-critical function must have a corresponding `BenchmarkXxx` in the same package's `_test.go` file. "Performance-critical" means any function on a parsing or writing path that is called per-field or per-byte.

---

## Running the full CI check locally

```bash
make ci
# Equivalent to:
make lint && make test-race && make bench
```

Run this before opening a pull request to replicate the checks that run in CI.

---

## Fuzz testing

GoMetadata uses Go's built-in fuzzer (`go test -fuzz`) to test all components that consume untrusted bytes. The three primary fuzz targets are:

| Target | Package | Make target |
|---|---|---|
| `FuzzParseEXIF` | `./exif/...` | `make fuzz-exif` |
| `FuzzParseIPTC` | `./iptc/...` | `make fuzz-iptc` |
| `FuzzParseXMP` | `./xmp/...` | `make fuzz-xmp` |

```bash
# Run each target for 60 seconds (the default fuzztime)
make fuzz-exif
make fuzz-iptc
make fuzz-xmp

# Run a single target manually with a custom duration
go test -fuzz=FuzzParseEXIF -fuzztime=5m ./exif/...
```

The `-fuzztime` flag controls how long the fuzzer runs before stopping. Crash-inducing inputs are written to `testdata/fuzz/<Target>/` and are replayed automatically on every future `go test` run.

**Requirement:** any new parser or component that consumes untrusted bytes (file data, network data, user input) must include a `FuzzXxx` function. PRs adding a parser without a fuzz target will not be accepted.

---

## Coverage

```bash
make coverage
# Equivalent to:
go test -coverprofile=cover.out ./...
go tool cover -html=cover.out
```

This generates `cover.out` and opens an HTML report in your browser. There is no enforced minimum threshold, but coverage must not decrease for the packages you touch. New code paths must be reachable by at least one test.

---

## Coding standards

### Specification compliance

GoMetadata targets three standards:

- **EXIF**: CIPA DC-008 / JEITA CP-3451 (EXIF 2.x and 3.0) and TIFF 6.0
- **IPTC**: IPTC IIM 4.2
- **XMP**: ISO 16684-1 and Adobe XMP Specification Parts 1–3

Every decision derived from a specification must be annotated with a comment that cites the standard, section, and page number. Example:

```go
// EXIF 2.32, CIPA DC-008-2019, §4.6.2: the value-or-offset field holds the
// value inline when the byte count is ≤ 4; otherwise it holds an offset to
// the actual value elsewhere in the file.
if byteCount <= 4 {
    return parseInlineValue(entry)
}
return parseOffsetValue(r, entry)
```

When a real-world file deviates from the spec, handle it gracefully (do not panic or return a hard error) and document the deviation in a comment.

### Tests

- All tests must be table-driven using the standard `testing` package.
- Use `t.Parallel()` in unit tests where there is no shared mutable state.
- Tests for concurrent code must be run with `-race`; add a comment noting the concurrency concern.
- Fuzz targets are required for any component that parses bytes from an untrusted source.

### Performance

- Avoid heap allocations on hot parsing paths. Prefer stack-allocated values and pre-allocated slices.
- Use `sync.Pool` for buffers that are allocated frequently and have a short lifetime.
- Do not copy byte slices unnecessarily; use sub-slices where the lifetime allows.
- If you introduce a `sync.Pool`, ensure that pooled buffers are not returned to the pool while a sub-slice derived from them is still in use.
- Back any performance claim in a PR description with `go test -bench` output.

### Error handling

- Return errors explicitly. Do not panic in library code.
- Error messages must be specific and actionable. Do not expose internal parser vocabulary (`IFD`, `APP13`, `rdf:Seq`) in errors returned through the public API.
- Use the sentinel error types in `internal/` for structured error inspection.

### API surface

- The top-level `gometadata` package is the only public API. Internal packages are implementation details.
- Do not add exported symbols to internal packages.
- All exported symbols must have a godoc comment.

---

## Pull request checklist

Before opening a pull request, confirm every item below:

- [ ] `go test -race ./...` passes with no failures or race reports
- [ ] `golangci-lint run ./...` is clean (or all suppressions are explained)
- [ ] No existing benchmark regresses; new hot paths have a `BenchmarkXxx`
- [ ] New parsers or byte-consuming components include a `FuzzXxx` target
- [ ] All spec-derived decisions are annotated with a comment citing the standard and section
- [ ] New code paths are covered by at least one table-driven test
- [ ] Exported symbols have godoc comments
- [ ] `go mod tidy` (`make tidy`) has been run and `go.mod` / `go.sum` are clean

---

## Commit message style

GoMetadata uses [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).

```
<type>(<scope>): <subject>

[optional body]
```

**Types:** `feat`, `fix`, `test`, `docs`, `refactor`, `chore`, `perf`

**Rules:**
- Subject line is imperative mood, lowercase, no trailing period, 72 characters or fewer.
- Scope is optional; when used, it should be the package name (`exif`, `iptc`, `xmp`, `format`).
- Body explains the *why*, not the *what*, when the subject is not self-explanatory.

**Examples:**

```
feat(exif): add MakerNote dispatch for Sony ARW files

fix(iptc): handle datasets with zero-length values without panicking

test(xmp): add fuzz target for RDF alt-tag parsing

perf(exif): eliminate per-tag heap allocation in IFD traversal
```

---

## Reporting issues

Please open a GitHub issue at <https://github.com/FlavioCFOliveira/GoMetadata/issues>.

Include the following where applicable:

- A minimal reproducible example or a sample file (if the file contains private data, a generated minimal file that reproduces the bug is preferred)
- The Go version (`go version`) and OS
- The full error message or unexpected output
- The expected behaviour

For security vulnerabilities, do not open a public issue. See the security policy in the repository for the preferred disclosure channel.
