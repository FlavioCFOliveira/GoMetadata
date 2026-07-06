# Security Policy

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Use one of the following private channels:

- **GitHub private security advisory** (preferred): <https://github.com/FlavioCFOliveira/GoMetadata/security/advisories/new>
- **Email**: Include a minimal reproducer, the Go version, and the OS. If attaching a sample file, a synthetically crafted minimal file is preferred over a real photo that may contain personal data.

You will receive an acknowledgement within 72 hours. A fix will be prepared and released before public disclosure. Credit will be given in the release notes unless you prefer to remain anonymous.

---

## Supported versions

Only the latest release receives security fixes. If you are on an older release, upgrade before filing a report.

| Version | Supported |
|---|---|
| Latest release | Yes |
| Older releases | No |

---

## Security model

GoMetadata is a parsing library. Its primary attack surface is **untrusted bytes supplied as image input**. The threat model and the guarantees the library provides are described below.

### Threat model

| Threat | Description |
|---|---|
| Crafted image files | A malicious file could carry a metadata segment designed to trigger out-of-bounds reads, integer overflows, infinite loops, or excessive memory allocation. |
| Denial of service | A deeply nested IFD chain, an extremely large value count, or a circular offset reference could cause the parser to consume unbounded CPU or memory. |
| Panic | Any `nil` dereference, slice bounds violation, or unrecovered `panic` inside the library exposed to a caller processing untrusted files constitutes a vulnerability. |
| Information disclosure | A bug that causes the library to return bytes from outside the intended metadata segment (e.g., from image pixel data or an adjacent memory region). |

### Guarantees

- **The library is safe to use on untrusted input.** Every parser that consumes bytes from an external source has a corresponding fuzz target that is run continuously in CI. Crashes found by the fuzzer are treated as P0 bugs.
- **The library does not panic on malformed input.** Parsers return errors instead of panicking. Spec deviations are handled gracefully and documented in code comments.
- **No unbounded allocations.** All parsers enforce size limits before allocating. Value counts and string lengths derived from file data are validated before use.
- **No external network access.** The library reads only from the `io.ReadSeeker` or file path the caller provides. It never initiates network connections.
- **No file-system side effects.** Read operations are read-only. Write operations are scoped strictly to the output path provided by the caller.

### Write trust boundary

Write operations have a narrower, explicitly documented trust boundary that is distinct from the read-path guarantees above:

- **Symlink following is intentional.** `WriteFile` resolves the target path with `filepath.EvalSymlinks` and rewrites the *resolved* real file, not the symlink itself (decision #125). This is correct for the common case — rewriting metadata on a file reached through a symlink — but it means `WriteFile` follows a symlink to wherever it points, including outside any directory tree the caller may have intended to restrict writes to. `WriteFile` performs no path-safety validation of its own. **Path validation is the caller's responsibility**: an application that accepts a path from an untrusted source (user input, a web request parameter, an archive entry name, etc.) must validate or reject symlinks before calling `WriteFile`, exactly as it would before calling `os.OpenFile` or any other path-based standard library function.
- **The streaming `Write(r, w, m)` API may emit partial output on error.** `Write` writes directly to the caller's `io.Writer` as it encodes; it does not buffer the complete result first. If encoding fails partway through, `w` may already contain a truncated or inconsistent byte sequence. Callers that require all-or-nothing semantics — the destination is either the complete, correct result or is left untouched — must use `WriteFile`, which isolates all output in a temporary file created in the target directory and removed on any error, only replacing the original file via an atomic `os.Rename` after a full, synced write succeeds.
- **Privilege bits are never propagated to rewritten output.** `WriteFile` preserves the original file's ordinary permission bits (e.g. `0644`, `0755`) but always clears `setuid`, `setgid`, and the sticky bit on the replacement file, even if the original file carried them (#259). A metadata rewrite must never (re)create a privilege-escalation surface, even preservation-only.

---

## Fuzz test coverage

All 29 fuzz targets run continuously in CI using Go's built-in fuzzer. Crash-inducing inputs found during a run are committed to `testdata/fuzz/<Target>/` and are replayed automatically on every subsequent `go test` invocation. MakerNote parsing (Canon, Nikon, Sony, Olympus, Panasonic, Pentax, DJI, FujiFilm, Leica, Samsung, Sigma, Minolta, Casio) is consolidated into a single file, `exif/makernote_parse.go`, dispatched from and covered by `FuzzParseEXIF` — there are no separate per-vendor MakerNote fuzz targets or packages.

This inventory is verified directly against the source tree (`grep -rn '^func Fuzz' --include='*.go' .`); if you add or rename a fuzz target, update this table in the same change.

### End-to-end

| Target | Package | What it covers |
|---|---|---|
| `FuzzRead` | `.` (root) | End-to-end fuzzing of the public `Read` entry point: magic-byte format detection, container dispatch, and EXIF/IPTC/XMP reconciliation across all supported container formats; strict mode surfaces all parser errors, best-effort mode guarantees no panic |

### Core parsers

| Target | Package | What it covers |
|---|---|---|
| `FuzzParseEXIF` | `exif/` | EXIF/TIFF IFD traversal: byte order detection, tag decoding, inline vs. offset values, IFD chaining (bounded by the IFD-chain traversal budget, EXIF-IFDCHAIN-01), and MakerNote dispatch/decoding for all supported manufacturers |
| `FuzzParseIPTC` | `iptc/` | IPTC IIM dataset decoding: record/dataset markers, extended-length headers, and character-encoding fields |
| `FuzzParseXMP` | `xmp/` | XMP/RDF-XML parsing: namespace resolution, property type dispatch, and packet boundary detection |

### Container extractors and injectors

Every container format with a write path has both an `Extract` (read) and an `Inject` (write) fuzz target, each round-tripping Extract → mutate → Inject → Extract and asserting the library never panics on untrusted input.

| Target | Package | What it covers |
|---|---|---|
| `FuzzJPEGExtract` | `format/jpeg/` | JPEG APP segment extraction: marker scanning, segment length validation, and APP1/APP13 boundary checks |
| `FuzzJPEGInject` | `format/jpeg/` | JPEG write path: APP1(EXIF)/APP1(XMP)/APP13(IPTC) segment rebuild, extended-XMP GUID generation and chunk splitting, and Photoshop IRB sibling-resource preservation on a malformed or truncated original APP13 |
| `FuzzPNGExtract` | `format/png/` | PNG chunk extraction: chunk length and CRC validation, iTXt/tEXt/zTXt identification |
| `FuzzPNGInject` | `format/png/` | PNG write path: chunk rebuild and metadata upsert on structurally varied container bytes |
| `FuzzTIFFExtract` | `format/tiff/` | Standalone TIFF container parsing: IFH magic, IFD0 offset, and sub-IFD discovery |
| `FuzzTIFFInject` | `format/tiff/` | TIFF copy-and-relocate write path: image-block enumeration/relocation, SubIFD relocation, and the image-block budget caps (GM-W1) |
| `FuzzWebPExtract` | `format/webp/` | WebP RIFF chunk parsing: four-byte chunk type identification, chunk size bounds, and EXIF/XMP chunk location |
| `FuzzWebPInject` | `format/webp/` | WebP write path: RIFF chunk rebuild and VP8X canvas-dimension preservation on structurally varied container bytes |
| `FuzzHEIFExtract` | `format/heif/` | HEIF/ISOBMFF box extraction: box size and type validation, nested-box traversal, and `iloc`/`mdat` offset resolution |
| `FuzzHEIFInject` | `format/heif/` | HEIF/ISOBMFF write path: `iloc`/`meta` box rebuild on structurally varied container bytes |

### RAW extractors and injectors

| Target | Package | What it covers |
|---|---|---|
| `FuzzCR2Extract` | `format/raw/cr2/` | Canon CR2: TIFF-based IFD layout with Canon-specific sub-IFD offsets |
| `FuzzCR2Inject` | `format/raw/cr2/` | Canon CR2 write path: copy-and-relocate serialisation with Canon MakerNote blob preservation |
| `FuzzCR3Extract` | `format/raw/cr3/` | Canon CR3: ISOBMFF-based container with `CMT1`/`CMT2` box extraction |
| `FuzzCR3Inject` | `format/raw/cr3/` | Canon CR3 write path: `moov`/Canon `uuid` box rebuild and `stco`/`co64` chunk-offset relocation, including the classic and ISO/IEC 14496-12 §4.2 extended-size box header forms |
| `FuzzNEFExtract` | `format/raw/nef/` | Nikon NEF: little-endian and big-endian TIFF variants with Nikon-specific IFD chaining |
| `FuzzNEFInject` | `format/raw/nef/` | Nikon NEF write path: copy-and-relocate serialisation extending the Nikon Type-3 MakerNote blob over PreviewIFD/NikonScanIFD |
| `FuzzARWExtract` | `format/raw/arw/` | Sony ARW: little-endian TIFF with Sony SR2 private IFD offsets |
| `FuzzARWInject` | `format/raw/arw/` | Sony ARW write path: Sony MakerNote TIFF-absolute offset rebasing and SR2Private block relocation |
| `FuzzDNGExtract` | `format/raw/dng/` | Adobe DNG: little-endian and big-endian TIFF with DNG-specific tag validation |
| `FuzzDNGInject` | `format/raw/dng/` | Adobe DNG write path: recursive SubIFD relocation and out-of-line value-area patching |
| `FuzzORFExtract` | `format/raw/orf/` | Olympus ORF: non-standard TIFF magic bytes and Olympus IFD layout |
| `FuzzORFInject` | `format/raw/orf/` | Olympus ORF write path: IIRO/IIRS magic-byte patching around the copy-and-relocate pass |
| `FuzzRW2Extract` | `format/raw/rw2/` | Panasonic RW2: little-endian TIFF variant with Panasonic-specific IFD offsets |
| `FuzzRW2Inject` | `format/raw/rw2/` | Panasonic RW2 write path: 16-byte device GUID header preservation and IFD0 offset rebasing |

### Internal

| Target | Package | What it covers |
|---|---|---|
| `FuzzRIFFRead` | `internal/riff/` | RIFF chunk-header parsing (used by WebP): chunk `Size` and `Offset` arithmetic, exercised independently of the WebP-specific dispatch layer |

---

## Running fuzz tests locally

Run any fuzz target directly with the Go toolchain. Substitute the target name and package path as needed:

```bash
go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...
go test -fuzz=FuzzJPEGExtract -fuzztime=60s ./format/jpeg/...
go test -fuzz=FuzzJPEGInject -fuzztime=60s ./format/jpeg/...
go test -fuzz=FuzzCR3Inject -fuzztime=60s ./format/raw/cr3/...
```

The `-fuzztime` flag controls how long the fuzzer runs. Crash-inducing inputs are written to `testdata/fuzz/<Target>/` within the relevant package directory and replayed automatically on all subsequent `go test` runs.

---

## Vulnerability classification

| Severity | Examples in this library |
|---|---|
| **Critical** | Remote code execution or memory corruption reachable from untrusted image input |
| **High** | Panic or out-of-bounds read/write reachable from a crafted image file; unbounded memory allocation that enables denial of service from a single request |
| **Medium** | Information disclosure (returning bytes outside the intended metadata segment); incorrect parsing that silently produces wrong values for security-sensitive fields (e.g., GPS coordinates, copyright, authorship) |
| **Low** | Minor spec non-compliance with no security consequence; incorrect handling of a rare or deprecated field that does not affect data integrity |

Vulnerabilities that are reachable only from trusted, locally-provided input (e.g., a developer calling an internal API with hand-crafted data) will be treated as bugs rather than security issues unless there is a realistic exploit path.
