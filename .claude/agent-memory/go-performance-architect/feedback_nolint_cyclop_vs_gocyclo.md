---
name: feedback-nolint-cyclop-vs-gocyclo
description: cyclop and gocyclo are separate linters; nolint:cyclop does not suppress gocyclo and vice versa — both must be listed when both fire
type: feedback
---

Use `//nolint:cyclop,gocyclo` when a function exceeds the threshold for both linters. Adding only one silences that linter but leaves an "unused directive" error for the other.

**Why:** Discovered during #16 implementation when `parseRDF` and `collectTextContent` triggered `gocyclo` after only `cyclop` was suppressed, causing a second lint run to fail with a `nolintlint` "unused directive" error for the already-silent linter.

**How to apply:** When a function has high cyclomatic complexity and you need to suppress it, check which linters are actually firing (`golangci-lint run`) and list ALL of them in the single directive. Common pair: `//nolint:cyclop,gocyclo`.
