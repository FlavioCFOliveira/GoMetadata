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

---

## Fuzz test coverage

All 26 fuzz targets run continuously in CI using Go's built-in fuzzer. Crash-inducing inputs found during a run are committed to `testdata/fuzz/<Target>/` and are replayed automatically on every subsequent `go test` invocation.

### Core parsers

| Target | Package | What it covers |
|---|---|---|
| `FuzzParseEXIF` | `exif/` | EXIF/TIFF IFD traversal: byte order detection, tag decoding, inline vs. offset values, and IFD chaining |
| `FuzzParseIPTC` | `iptc/` | IPTC IIM dataset decoding: record/dataset markers, extended-length headers, and character-encoding fields |
| `FuzzParseXMP` | `xmp/` | XMP/RDF-XML parsing: namespace resolution, property type dispatch, and packet boundary detection |

### Container extractors

| Target | Package | What it covers |
|---|---|---|
| `FuzzJPEGExtract` | `format/jpeg/` | JPEG APP segment extraction: marker scanning, segment length validation, and APP1/APP13 boundary checks |
| `FuzzPNGExtract` | `format/png/` | PNG chunk extraction: chunk length and CRC validation, iTXt/tEXt/zTXt identification |
| `FuzzTIFFExtract` | `format/tiff/` | Standalone TIFF container parsing: IFH magic, IFD0 offset, and sub-IFD discovery |
| `FuzzWebPExtract` | `format/webp/` | WebP RIFF chunk parsing: four-byte chunk type identification, chunk size bounds, and EXIF/XMP chunk location |
| `FuzzHEIFExtract` | `format/heif/` | HEIF/ISOBMFF box extraction: box size and type validation, nested-box traversal, and iloc/mdat offset resolution |

### RAW extractors

| Target | Package | What it covers |
|---|---|---|
| `FuzzCR2Extract` | `format/raw/cr2/` | Canon CR2: TIFF-based IFD layout with Canon-specific sub-IFD offsets |
| `FuzzCR3Extract` | `format/raw/cr3/` | Canon CR3: ISOBMFF-based container with `CMT1`/`CMT2` box extraction |
| `FuzzNEFExtract` | `format/raw/nef/` | Nikon NEF: little-endian and big-endian TIFF variants with Nikon-specific IFD chaining |
| `FuzzARWExtract` | `format/raw/arw/` | Sony ARW: little-endian TIFF with Sony SR2 private IFD offsets |
| `FuzzDNGExtract` | `format/raw/dng/` | Adobe DNG: little-endian and big-endian TIFF with DNG-specific tag validation |
| `FuzzORFExtract` | `format/raw/orf/` | Olympus ORF: non-standard TIFF magic bytes and Olympus IFD layout |
| `FuzzRW2Extract` | `format/raw/rw2/` | Panasonic RW2: little-endian TIFF variant with Panasonic-specific IFD offsets |

### MakerNote parsers

| Target | Package | What it covers |
|---|---|---|
| `FuzzCanonParse` | `exif/makernote/canon/` | Canon MakerNote IFD decoding and Canon-specific tag type dispatch |
| `FuzzNikonParse` | `exif/makernote/nikon/` | Nikon Type 1 (pre-D100) and Type 3 (embedded TIFF) MakerNote formats |
| `FuzzSonyParse` | `exif/makernote/sony/` | Sony MakerNote IFD decoding and encrypted tag handling |
| `FuzzOlympusParse` | `exif/makernote/olympus/` | Olympus MakerNote with ASCII header and nested equipment/camera settings IFDs |
| `FuzzPanasonicParser` | `exif/makernote/panasonic/` | Panasonic MakerNote IFD decoding with ASCII header validation |
| `FuzzLeicaParser` | `exif/makernote/leica/` | Leica MakerNote variants (LEICA0, plain IFD) and tag dispatch |
| `FuzzSamsungParser` | `exif/makernote/samsung/` | Samsung MakerNote IFD decoding and tag value parsing |
| `FuzzDJIParser` | `exif/makernote/dji/` | DJI MakerNote IFD decoding for aerial-camera metadata fields |
| `FuzzFujifilmParse` | `exif/makernote/fujifilm/` | Fujifilm MakerNote with `FUJIFILM` header and little-endian IFD |
| `FuzzSigmaParser` | `exif/makernote/sigma/` | Sigma MakerNote with `SIGMA` or `FOVEON` header and tag dispatch |
| `FuzzPentaxParse` | `exif/makernote/pentax/` | Pentax MakerNote with `AOC` header and type-3 IFD encoding |

---

## Running fuzz tests locally

Run any fuzz target directly with the Go toolchain. Substitute the target name and package path as needed:

```bash
go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...
go test -fuzz=FuzzJPEGExtract -fuzztime=60s ./format/jpeg/...
go test -fuzz=FuzzCanonParse -fuzztime=60s ./exif/makernote/canon/...
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
