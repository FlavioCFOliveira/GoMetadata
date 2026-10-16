# Third-Party Code Attribution

This document defines how GoMetadata attributes code and data that it adapts
from other open-source projects. The notice register is
[`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md); the licence texts are in
[`LICENSES/`](../LICENSES/).

## Rules

1. Only permissively licensed code may be copied or adapted: MIT, BSD-2-Clause,
   BSD-3-Clause, Apache-2.0, libtiff, libpng-2.0. Code and tables that are
   GPL, LGPL, or Artistic licensed, or that the source marks as derived from
   ExifTool, are never copied.
2. The root [`LICENSE`](../LICENSE) stays the MIT licence of GoMetadata and is
   never modified, so that pkg.go.dev detects it.
3. Every project that contributes adapted content has one entry in
   `THIRD_PARTY_NOTICES.md` with its name, URL, source commit, licence,
   copyright notice, source files, destination files, and whether content was
   adapted.
4. The full text of every licence in use is stored in `LICENSES/<SPDX-id>.txt`.
5. The copyright and NOTICE requirements of the source licence are retained in
   the notice entry.

## File header

Every `.go` file that contains adapted content starts, after the `package`
clause, with one header line per source:

```go
// Adapted from <project> <file>:<symbol>, Copyright <years> <holder>, SPDX-License-Identifier: <id>
```

Example:

```go
// Adapted from libtiff libtiff/tif_dirinfo.c:tiffFields, Copyright (c) 1988-1997 Sam Leffler, Copyright (c) 1991-1997 Silicon Graphics, Inc., SPDX-License-Identifier: libtiff
```

The header names the modifications when the content was changed more than a
mechanical transcription (Apache-2.0 section 4(b)).

## Enforcement

`third_party_notices_test.go` walks the repository. For every `.go` file with a
line that starts with `// Adapted from`, the test requires that:

- the file path appears in `THIRD_PARTY_NOTICES.md`, and
- the project name of the header appears in `THIRD_PARTY_NOTICES.md`, and
- the SPDX identifier of the header has a matching file in `LICENSES/`.

Local clones of reference projects are working copies only and are never
committed.
