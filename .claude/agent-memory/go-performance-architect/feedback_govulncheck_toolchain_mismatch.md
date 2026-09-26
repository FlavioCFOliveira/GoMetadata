---
name: feedback_govulncheck_toolchain_mismatch
description: Homebrew-installed govulncheck can be built against an older Go than the local toolchain and fail to load packages using newer stdlib generics (math/rand/v2); rebuild it with `go install` before trusting a "Loading packages failed" error as a code problem
type: feedback
---

`govulncheck` installed via Homebrew (`/opt/homebrew/bin/govulncheck` at the time of this
finding) failed with "Loading packages failed... method must have no type parameters...
too many arguments in call to Int" when run against this repo (Go 1.27.1 local toolchain).
The error looked like a real code problem in `math/rand/v2` but was actually
`govulncheck` itself having been built against an older Go release than the one on PATH,
so it could not parse `math/rand/v2`'s newer generic method signatures.

**Fix**: `go install golang.org/x/vuln/cmd/govulncheck@latest` (rebuilds it with the
current local toolchain, installed to `~/go/bin/govulncheck`) resolved it immediately —
"No vulnerabilities found" against the exact same repo state.

**Why**: cost time misdiagnosing this as a potential code-generation issue before
recognizing the tool/toolchain version skew.
**How to apply**: whenever `govulncheck` (or any Go-toolchain-coupled static-analysis
binary from a package manager) reports a package-loading/parse error unrelated to your
own code changes, rebuild it with `go install .../cmd/toolname@latest` using the CURRENT
`go` on PATH before spending time investigating it as a real issue. Prefer the
freshly-built binary (`~/go/bin/...`) over the package-manager one for the rest of the
session if the mismatch persists.
