---
name: Project identity
description: Core identity facts about GoMetadata — module path, package count, Go version
type: project
---

GoMetadata — pure Go image metadata library (EXIF/IPTC/XMP).

- Module: `github.com/FlavioCFOliveira/GoMetadata`
- Top-level package: `gometadata`
- 25 packages total (25 pass `go test -race ./...`)
- Go version: 1.26.1 (darwin/arm64)

**Why:** Module was renamed from an earlier name on 2026-04-04; all imports use the new path.

**How to apply:** Always use the full module path in import statements and error messages.
