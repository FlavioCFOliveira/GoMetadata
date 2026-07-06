# Changelog

All notable changes to GoMetadata are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions are listed in descending order (newest first). GoMetadata adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **BigTIFF write support, end to end** (`exif/write.go`, `format/tiff/relocate_bigtiff.go`, `write.go`, tasks #264/#270/#271): `exif.Encode` now natively encodes BigTIFF-sourced EXIF (`EXIF.BigTIFF == true`) with a 16-byte header, 20-byte IFD entries, and 64-bit offsets throughout (BigTIFF spec §2, Aware Systems/libtiff); `format/tiff`'s copy-and-relocate serialiser is now container-width-aware end to end (image-block enumeration, `SubIFDs` 0x014A relocation, and every raw-offset scan performed after encoding), so a standalone BigTIFF file relocates correctly on write; and the top-level `Write`/`WriteFile` no longer refuse BigTIFF sources with `ErrWriteNotSupported` — the root-package guard has been removed now that both layers are verified end-to-end against real BigTIFF corpus fixtures (LONG8/SLONG8/IFD8 strip/tile arrays, multi-level SubIFD chains, and the committed `BigTIFF_LE.tif`/`BigTIFF_BE.tif`/`big_cramps_{be,le}.tif` fixtures). Two new sentinel errors guard the encode path: `exif.ErrBigTIFFPointerOverflow` (a sub-IFD or thumbnail pointer — fixed by spec to a 4-byte EXIF LONG field regardless of container — would need an absolute offset that does not fit in 32 bits) and `exif.ErrBigTIFFEncodeSizeExceeded` (the encoded payload would exceed a documented, test-overridable 4 GiB sanity ceiling, since BigTIFF's 64-bit offsets have no implicit `MaxUint32` saturation the way classic TIFF's do). `exif.ErrBigTIFFEncodeNotSupported` is no longer returned by `Encode` under normal conditions and its doc comment now marks it **Deprecated**; it is retained only for API compatibility and may be removed in a future major release. `ErrWriteNotSupported` remains exported for any future format without a write path.
- **IPTC↔XMP↔EXIF write-side bridging for dates and creators** (`iptc/`, `xmp/`, `metadata.go`, #265): `iptc.IPTC.SetDateCreated` (dataset 2:55 Date Created + 2:60 Time Created, IIM §2.2.23) and `iptc.IPTC.SetCreators` (full-replace dataset 2:80 By-line, order-preserving); `xmp.XMP.SetDateCreated` (`photoshop:DateCreated`, RFC 3339), `xmp.XMP.Creators`, and `xmp.XMP.SetCreators` (ordered `dc:creator`); `Metadata.SetDateTimeOriginal` now also writes IPTC 2:55/2:60 and XMP `photoshop:DateCreated` alongside its existing EXIF and `exif:DateTimeOriginal` writes, and a new `Metadata.SetCreators([]string)` fans an ordered creator list out to IPTC 2:80 (repeatable, order preserved), XMP `dc:creator` (ordered `rdf:Seq`), and EXIF `0x013B Artist` (flattened, `"; "`-joined per MWG list-handling convention). `IPTC.SetDateCreated` guards against `t.Year()` outside `[0, 9999]` by no-op'ing the IPTC write only; EXIF and XMP proceed normally. `xmp:CreateDate` (digitization date) is a distinct field and is never written by this bridge.
- **XMP `geo:` namespace prefix** (`xmp/namespace.go`): `xmp.Encode` now emits the conventional `geo:` prefix for the W3C Basic Geo namespace (`http://www.w3.org/2003/01/geo/wgs84_pos#`) instead of a generated `nsN:` placeholder, matching the prefix that `xmp.GPS()`'s W3C-Geo fallback (added in v1.2.0) already expects when reading it back.
- **Write-path (`*Inject`) fuzz targets for JPEG, PNG, and all seven RAW formats** (#258): `FuzzJPEGInject`, `FuzzPNGInject`, `FuzzARWInject`, `FuzzCR2Inject`, `FuzzCR3Inject`, `FuzzDNGInject`, `FuzzNEFInject`, `FuzzORFInject`, and `FuzzRW2Inject` each round-trip Extract → mutate → Inject → Extract and assert the library never panics on untrusted input; `FuzzCR3Inject` is seeded with the extended-size `moov`/`uuid` byte sequences that exposed the fix described below. This closes a gap where every write path except TIFF, HEIF, and WebP had read-only fuzz coverage; the library now ships exactly 29 fuzz targets (see `SECURITY.md` for the full, verified inventory).
- **`iptc.ErrDatasetValueTooLarge`**: new exported sentinel returned by `iptc.Encode` when a `Dataset.Value` is longer than the extended-length header can represent (4 GiB − 1, IIM §1.6.2 extended-length form). Callers can use `errors.Is` to distinguish this from other encode failures.
- **`format/tiff.ErrTooManyImageBlocks`**: new exported sentinel returned by the TIFF-family write/relocate path (TIFF, DNG, CR2, NEF, ARW, ORF, RW2) when a single `StripOffsets`/`TileOffsets`/SubIFD-pointer entry, or the aggregate across all such entries in one write, declares more image blocks than the new spec-justified caps allow.
- **Root `ErrFileTooLarge`**: new exported sentinel returned by `Write`/`WriteFile` when the source stream exceeds a 256 MiB aggregate read cap on the TIFF-family write paths, matching the cap every format package's own extractor already enforced.
- **`format/jpeg.ErrFileTooLarge`**: JPEG gains the same 256 MiB aggregate-size cap already enforced by every other container package (`webp`, `heif`, `orf`, `rw2`, `cr3`, `tiff`).

### Changed

- **Performance: EXIF parse and encode hot paths** (tasks #198–#203, #240; see `BENCHMARKS.md` for full benchstat detail): `exif.Parse` uses a lazy sub-IFD arena that co-allocates all sub-IFD structures in a single batch when `ExifIFD`/`GPSIFD` are present (measured −25% allocs/op on camera-file EXIF at the time the task landed); `IFDEntry`'s byte-order field is now a 1-byte flag instead of a 16-byte `binary.ByteOrder` interface (`unsafe.Sizeof(IFDEntry{})` 56 B → 48 B); warning-string construction is deferred from the IFD-traversal hot path to the `Parse` boundary, removing `fmt.Sprintf` from the common (warning-free) case; the fixed-array staging buffers in `writeTIFFHeader`/`writeIFD` no longer escape to the heap; MakerNote dispatch no longer allocates a string via `(*IFDEntry).String()` to build its map key; and the EXIF encode path pools its `filterEntries` scratch slices via a package-level `sync.Pool`. Net effect at HEAD: `BenchmarkEXIFEncode` 2 allocs/op (down from 6 at the v1.2.0 baseline).
- **`exif.EXIF` and `xmp.XMP` concurrency contract documented** (#245, XMPCONC-01): both types' doc comments now explicitly state they are not safe for concurrent mutation from multiple goroutines without external synchronisation, mirroring the existing `iptc.IPTC` note. `Metadata`'s own concurrency contract (an internal mutex guarding every `Set*` call) is unaffected; this clarifies the boundary for callers who mutate `m.EXIF`/`m.XMP` directly instead of through `Metadata.Set*`.
- **`format.Detect` now uses `io.ReadFull`** (#244) instead of a single `Read` call, so a chunking reader (a socket, `io.Pipe`, a decompressing stream, or a small `bufio.Reader`) that satisfies a short read mid-stream is no longer misdetected as `FormatUnknown`.
- **EXIF byte-order resolution corrected** (#246, #252): `ifd0ByteOrder()` now prefers the authoritative `EXIF.ByteOrder` field instead of inferring order from `IFD0.Entries[0].bigEndian`, and `IFD.set()` now takes an explicit big-endian flag instead of inheriting it from `Entries[0]`. Previously, a spec-legal big-endian file with an empty IFD0, or a freshly created `ExifIFD`/`GPSIFD`, could cause the numeric `Set*` methods and the `GPS`/`ExposureTime`/`FNumber`/`ISO`/`FocalLength`/`ImageSize` accessors to silently read or write little-endian bytes into a big-endian structure.

### Fixed

- **HEIF `iloc` 5-byte body out-of-bounds panic** (#243): `parseIloc` and `parseIlocFull` guarded the `iloc` body length with `len < 5`, but both read a second size-nibble byte at index 5; a minimal legal 5-byte `iloc` body (box size 13, ISO 14496-12 §8.11.3) passed the guard and then panicked. Both guards are now `len < 6`.
- **HEIF `iloc` extent-count memory/CPU amplification** (#249): `readIlocFullExtents`/`readIlocSimpleExtents` iterated the full attacker-controlled `extent_count` (up to 65 535) even in the degenerate case where every field-size nibble is zero and each iteration consumes zero input bytes. The extent count is now capped at `maxIlocExtentsPerItem` (1024) per item, the zero-field-size loop is bounded to the same cap, and the outer item count is capped at `maxIlocItems` (4096), mirroring the existing BigTIFF IFD0 item cap.
- **HEIF 32-bit offset-arithmetic slice panic** (#257): `extractExifFromData` and `parseIinfItemCount` narrowed an attacker-controlled `uint32` offset to `int` before bounds-checking; on 32-bit platforms (`GOARCH=386/arm/mips`) an offset ≥ `0x80000000` wrapped negative and bypassed the guard, producing a negative-index slice panic. Both sites now compare in `uint64` before narrowing.
- **CR3 extended-size box headers mishandled in `Inject`** (#256): `cr3.Inject` hard-coded an 8-byte box header when locating the `moov`/Canon `uuid` boxes, silently mis-slicing (and dropping metadata siblings from) any source using the ISO/IEC 14496-12 §4.2 extended 16-byte header form (`size == 1` followed by an 8-byte `largesize`). The header length is now re-derived via `parseCR3BoxHeader` at both slice sites, and `flatUUIDBoxRange` gained a missing bounds guard against an undersized `uuid` box.
- **ORF/RW2 double-write corruption** (#250): `writeTIFFORF`/`writeTIFFRW2` passed `m.EXIF` directly to the mutating relocator instead of a clone, so calling `Write` twice on the same `*Metadata` silently corrupted image data on the second call. Both now clone the EXIF struct first, matching the fix already applied to every other format in v1.2.0 (#109).
- **EXIF IFD-chain traversal unbounded** (#255, EXIF-IFDCHAIN-01): a crafted EXIF/TIFF payload with a chain of overlapping IFD offsets, each declaring a large entry count, could force multi-gigabyte retained heap and multi-second CPU from a sub-megabyte input. `Parse` now shares a `traverseBudget` across the IFD0 chain and all sub-IFD traversals: chain length is capped at `maxTraverseChainIFDs` (512 IFDs), and cumulative entries parsed are capped at `max(len(b)/6, 64)` and charged using the pre-deduplication entry count so a duplicate-tag IFD cannot evade the budget.
- **TIFF-family write/relocate path unbounded allocation, GM-W1** (#261): `extractParallelOffsetBlocks` and `enumerateSubIFDsAt` allocated one heap object per declared `StripOffsets`/`TileOffsets`/SubIFD-count element with no upper bound, so a crafted sub-256 MiB TIFF/DNG/CR2/NEF/ARW/ORF/RW2 file could drive multi-gigabyte allocation through a plain read-then-write round trip with no metadata modification required. New per-entry (`maxImageBlocksPerOffsetEntry` = 65536; `maxSubIFDsPerEntry` = 1024) and aggregate (`maxAggregateImageBlocks` = 262144) caps now reject oversized declarations with `format/tiff.ErrTooManyImageBlocks` before any allocation.
- **`WriteFile` propagated `setuid`/`setgid`/sticky bits to rewritten output** (#259): the atomic-replace path preserved the source file's full mode via `Chmod`, including any `setuid`/`setgid`/sticky bits already present. These are now masked off the replacement file while ordinary permission bits are still preserved.
- **TIFF/ORF/RW2/CR3 32-bit offset truncation in IFD0 lookup** (#253): `findExifIFDOffset` (in `format/tiff`, `format/raw/orf`, `format/raw/rw2`) and CR3's `mergeCMT` compared an offset after narrowing it to `int`; on 32-bit platforms an offset ≥ 2³¹ narrowed to a negative `int`, passed the length guard, and then panicked on slicing. The comparison is now performed in `uint64` before narrowing.
- **`writeTIFFHeader`/`writeIFD` panicked on a non-standard `binary.ByteOrder`** (#247, PERF-201-LOW): a non-comma-ok type assertion to `binary.AppendByteOrder` (introduced by the byte-order refactor in v1.2.0) panicked for any `binary.ByteOrder` implementation other than the two standard-library singletons. The assertion is now comma-ok with a `PutUint16`/`PutUint32` fallback; the little-/big-endian fast path is unchanged and remains zero-allocation.
- **IPTC `Encode` extended-length overflow**: a `Dataset.Value` longer than the 4-byte extended-length field can represent was silently truncated to its low 32 bits while every value byte was still written, desynchronising the encoded stream. `Encode` now rejects such values with `iptc.ErrDatasetValueTooLarge` (see Added) instead of emitting a wrapped length field.
- **TIFF SubIFD type-13 (`IFD`) misparsed as UTF8** (part of #270): `format/tiff`'s relocator trusted `exif.Parse`'s type-13 decoding for tag `0x014A` (`SubIFDs`), but CIPA DC-008-2023's EXIF-3.0 assignment of `TypeUTF8` to the same numeric code as the legacy TIFF-extension `IFD` type meant a SubIFD pointer array declared as type 13 was silently corrupted or dropped on write. The relocator now re-scans the raw bytes for this tag directly instead of trusting the parsed struct.
- **BigTIFF write is now fully supported through the public API** (`write.go`, `exif/write.go`, `format/tiff/relocate.go`, `format/tiff/relocate_bigtiff.go`, #264/#270/#271) — see Added for the full description.
- **XMP encoder now always wraps array-typed properties in their RDF collection container** (`xmp/write.go`, `xmp/namespace.go`): `dc:creator`, `dc:subject`, `dc:description`, `dc:rights`, `dc:title`, and the `xmpMM` ordered-array set (`History`, `Ingredients`, `Pantry`) are now serialised as `<rdf:Seq>`/`<rdf:Bag>`/`<rdf:Alt>` even when they hold exactly one value, per ISO 16684-1 §7.5. Previously, a single-item array-typed property (e.g. the common case of `SetCaption`/`SetCreator` writing exactly one value) was serialised as a bare `<prefix:local>value</prefix:local>` element, which is only correct for Simple (Text) properties. Round-trip value correctness (via this library's own lenient parser) was unaffected, but the produced wire format did not conform to the formal Lang-Alt/array schema that other XMP consumers expect.

### Security

- **HEIF CRITICAL: `iloc` 5-byte body OOB panic closed** (#243): a slice-bounds-out-of-range panic reachable from untrusted HEIF/AVIF input via the public `heif.Extract` and `heif.Inject` paths (and therefore the top-level `Read`/`Write`) is now closed (CWE-125, CWE-787).
- **HEIF HIGH: `iloc` extent-count amplification closed** (#249): an attacker-controlled extent count could amplify a small HEIF/AVIF file into multi-gigabyte allocation on `Inject` or unbounded CPU on `Extract`; both are now bounded (CWE-770, CWE-834).
- **HIGH: OOM guard added to the TIFF-family root `Write` paths** (#251): the six TIFF-family write paths (`TIFF`, `DNG`, `CR2`, `NEF`, `ARW`, `ORF`, `RW2`) now cap the total bytes read from an untrusted `io.Reader` at 256 MiB via the new root `ErrFileTooLarge`, closing an OOM vector that the v1.1.0 `io.ReadAll` guard (#140) missed at these specific root-package call sites (CWE-770).
- **HIGH: TIFF-family write/relocate allocation DoS closed, GM-W1** (#261): a crafted sub-256 MiB file could force multi-gigabyte allocation through a plain read-then-write round trip with no metadata modification; per-entry and aggregate caps now reject the request before allocation (CWE-770, CWE-405).
- **HIGH: ORF/RW2 double-write corruption closed** (#250): calling `Write` twice on the same `*Metadata` for an ORF or RW2 source silently corrupted image data on the second call, with no error returned (CWE-664).
- **MEDIUM: `format.Detect` short-read misdetection closed** (#244): a chunking reader (socket, `io.Pipe`, decompressing stream, small `bufio.Reader`) could cause a valid image to be misdetected as `FormatUnknown`, failing every subsequent `Read`/`Write` call for that caller — a soft denial of service (CWE-20).
- **MEDIUM: EXIF byte-order inference corrected** (#246, #252): a spec-legal big-endian EXIF file with an empty IFD0, or a freshly created sub-IFD, could cause numeric setters and accessors to silently read or write little-endian bytes into a big-endian structure, corrupting output with no error surfaced (CWE-198).
- **CR3 extended-size box mishandling closed** (#256): a CR3 source using the ISO/IEC 14496-12 extended 16-byte box header form could have its EXIF and/or pre-existing metadata siblings silently dropped by `Inject`, with no error returned (CWE-682, CWE-390).
- **HEIF/TIFF/ORF/RW2/CR3 32-bit offset-arithmetic panics closed** (#253, #257): several offset comparisons narrowed an attacker-controlled 32-bit or 64-bit file offset to a platform `int` before bounds-checking, producing a negative-index slice panic on 32-bit builds (`GOARCH=386/arm/mips`); all affected sites now compare in `uint64` before narrowing (CWE-681, CWE-190, CWE-129).
- **EXIF IFD-chain traversal resource-exhaustion DoS closed** (#255): a crafted chain of overlapping IFD offsets could force gigabytes of retained heap and multi-second CPU from a sub-megabyte input; traversal is now bounded by both chain length and cumulative entry count (CWE-400, CWE-405, CWE-834).
- **LOW: `writeTIFFHeader`/`writeIFD` panic on non-standard `binary.ByteOrder` closed** (#247): a non-comma-ok type assertion could panic when the library is used with a caller-supplied `binary.ByteOrder` implementation instead of the two standard-library singletons (CWE-704).
- **`WriteFile` no longer propagates `setuid`/`setgid`/sticky bits to rewritten output** (#259): a metadata rewrite can no longer (re-)create a privilege-escalation surface, even when only preserving bits already present on the source file. The write trust boundary — `WriteFile`'s symlink-following behaviour and the streaming `Write`'s partial-output-on-error behaviour — is now documented in `SECURITY.md`.
- **IPTC extended-length overflow closed** (`iptc.Encode`): a `Dataset.Value` too long for the 4-byte extended-length field to represent was previously truncated silently rather than rejected, which could desynchronise the encoded stream; `Encode` now returns `iptc.ErrDatasetValueTooLarge` (CWE-190).
- **BigTIFF encode size ceiling added** (`exif.ErrBigTIFFEncodeSizeExceeded`): BigTIFF's 64-bit offset fields have no natural saturation ceiling the way classic TIFF's 32-bit fields do; `Encode` now enforces an explicit, documented, test-overridable 4 GiB sanity ceiling on the total encoded size before allocating the output buffer, preventing a pathological `IFDEntry.Count` from directing an unbounded allocation (CWE-400).
- **INFO: `format/jpeg` 256 MiB aggregate size cap added** (#262): JPEG now enforces the same project-wide aggregate input cap as every other container format, closing a defense-in-depth gap — JPEG was the only container package without one. Per-APP-segment lengths were already 16-bit bounded, so this is a normalisation rather than a fix for a known amplification bug.

## [1.2.0] - 2026-06-10

### Added

- **Write support for all 13 container formats** (`write.go`, `format/format.go`): `format.SupportsWrite` now returns `true` for every supported format. The only remaining write limitation is BigTIFF: `Write` and `WriteFile` return `ErrWriteNotSupported` when the source is a BigTIFF file (magic `0x002B`).

- **TIFF copy-and-relocate metadata write** (`format/tiff/`, #92/#93): `tiff.Inject` uses a copy-and-relocate serialiser (`format/tiff/relocate.go`) that enumerates every image-data block referenced by `StripOffsets` (0x0111), `TileOffsets` (0x0144), and `JPEGInterchangeFormat` (0x0201 non-thumbnail) across IFD0 and the IFD1 chain, appends each block at a fresh absolute offset in the rebuilt TIFF stream, and patches the offset entries accordingly. Both scalar (single-strip) and array (multi-strip, multi-tile, COUNT > 1) offset entries are handled. New sentinel errors `tiff.ErrBlockOutOfBounds`, `tiff.ErrUnsupportedOffsetType`, `tiff.ErrTruncatedOffsetArray`, and `tiff.ErrUnsupportedElemSize` are exported for diagnostic use.

- **DNG SubIFD recursive relocation and write support** (`format/tiff/relocate.go`, #94 + #98): the copy-and-relocate serialiser recursively follows `SubIFDs` (tag 0x014A) from IFD0, enumerates their strip/tile image blocks, and relocates both the SubIFD structures and their image blocks. The fix for #98 ensures that all out-of-line value areas (RATIONAL, SRATIONAL, DOUBLE, long ASCII, etc.) within each SubIFD have their `valOrOff` pointers updated to the new absolute positions, preventing `XResolution`/`YResolution` and similar fields from becoming undefined after write. Validated against a real Pentax QS1 DNG corpus file: `ImageDataHash` IN==OUT. Sentinel errors `tiff.ErrSubIFDPointerArrayOOB` and `tiff.ErrSubIFDEntryNotFound` are exported for diagnostic use.

- **CR2 metadata write via copy-and-relocate** (#95): Canon CR2 uses standard LE TIFF magic (`II*\0`) and routes through the same `writeTIFF` copy-and-relocate path as TIFF and DNG. Canon MakerNote blobs are copied verbatim (blob-relative offsets; move-safe). Validated against a real Canon EOS 350D corpus file: `ImageDataHash` IN==OUT, all MakerNote and SubIFD tags preserved.

- **NEF metadata write** (#102): the NEF-specific write path extends the Nikon Type-3 MakerNote blob to cover `PreviewIFD` and `NikonScanIFD` (which live beyond the declared byte count in the outer TIFF entry), enumerates the PreviewIFD image block, and patches the MakerNote-relative `0x0201` offset after re-encoding. Validated against a real Nikon D70 NEF corpus file: `ImageDataHash` IN==OUT, all metadata preserved.

- **ARW metadata write** (#103): the ARW-specific write path rebases all Sony MakerNote out-of-line offsets (Sony uses TIFF-absolute offsets, not blob-relative), extracts the SR2Private (0xC634) block verbatim, appends it to the output, and patches both the SR2 internal pointers and the IFD0 tag to the new position. Validated against a real Sony DSLR-A500 ARW corpus file: `ImageDataHash` IN==OUT, all 52 MakerNote tags and SR2Private preserved.

- **ORF metadata write** (#104): the ORF-specific write path patches the non-standard IIRO/IIRS magic bytes to standard LE TIFF before the copy-and-relocate pass and restores the original magic in the output. Both IIRO (Olympus DSLRs) and IIRS (older compacts) are supported. Validated against real corpus files (Olympus E-M10 and C5050Z).

- **RW2 metadata write** (#104): the RW2-specific write path preserves the Panasonic 16-byte device GUID header (bytes [8:24]) and rebases all absolute IFD0 offsets by +16 after GUID insertion. Validated against a real Panasonic DMC-GF1 RW2 corpus file: `ImageDataHash` IN==OUT.

- **CR3 metadata write with `stco`/`co64` offset relocation** (`format/raw/cr3/`, #91): `cr3.Inject` rebuilds the Canon UUID box with the new CMTx payloads, then walks every `trak → mdia → minf → stbl → {stco, co64}` table inside the rebuilt `moov`, adding `delta` to each chunk offset pointing at or beyond the original `moov` end. `stco` overflow (relocated value > `MaxUint32`) returns `cr3.ErrStcoOverflow`.

- **BigTIFF read support** (`format/tiff/`, `exif/`, #54): the TIFF package now recognises BigTIFF magic (`0x002B`), reads the 16-byte BigTIFF header (version, offset size, constant), and traverses IFDs using 8-byte offsets and 8-byte value areas. A parameterised IFD traversal in the EXIF package handles both 32-bit (TIFF 6.0) and 64-bit (BigTIFF) IFD layouts. BigTIFF is supported for read only; `Write` and `WriteFile` return `ErrWriteNotSupported` for BigTIFF sources (`exif.ErrBigTIFFEncodeNotSupported`).

- **Conformance test batteries** (`docs/conformance/`, sprints CF-1 to CF-4, #152–#168): 17 exhaustive conformance test suites covering all supported formats and metadata standards. Each suite maps directly to a normative specification clause (e.g. `S-08`, `IIM-BIN-05`, `JPEG-04`, `ROB-03`), so a failing test points directly at the violated specification requirement. Contracts are documented in `docs/conformance/` (one file per spec family: `exif-tiff.md`, `iptc.md`, `xmp.md`, `containers.md`).

- **`cr3.ErrNoCMT1Box`**: new exported sentinel returned by `cr3.Extract` when the Canon UUID structure is present but contains no CMT1 sub-box. The top-level `Read` converts this to a non-fatal parse warning so that XMP and other metadata remain accessible.

- **`cr3.ErrFileTooLarge`**: new exported sentinel returned when the CR3 input exceeds the 256 MiB read cap.

- **Embedded CI test fixtures** (`testdata/fixtures/`, #195): a curated set of real camera images is now embedded directly in the module, enabling the full test suite to run without downloading a separate corpus. Coverage is maintained at 82.7% with embedded fixtures alone.

- **`docs/TESTING.md`**: new document describing the testing policy, corpus structure, fuzz target inventory, and coverage requirements.

### Changed

- **`format.SupportsWrite` for TIFF, DNG, CR2, NEF, ARW, ORF, RW2, CR3**: all now return `true`. Each format has a dedicated write path with real-corpus validation. The only remaining write limitation is BigTIFF (returns `ErrWriteNotSupported`).
- **BigTIFF write**: `Write` and `WriteFile` return `ErrWriteNotSupported` when the source is a BigTIFF file (magic `0x002B`). Use `format.SupportsWrite(id)` to check write capability programmatically.
- **`Write` cross-format mismatch detection** (#108): `Write` now returns `ErrFormatMismatch` when the `*Metadata` was read from a different container format than the write target, preventing silent data loss from mismatched roundtrips.
- **`Write` idempotency** (#109): calling `Write` on an already-written byte slice now produces identical output. The fix eliminates a class of bugs where repeated writes caused progressive format divergence.
- **`WriteFile` atomicity and safety** (#124/#125): `WriteFile` now fsyncs the temporary file before rename, preserves the original file's ownership (uid/gid on Unix), and follows symlinks — writing through to the symlink target rather than replacing the link.
- **MakerNote out-of-line offset rebasing on write** (#127): the TIFF write path now rebases all Olympus, Panasonic, Sony, and Nikon Type-3 MakerNote out-of-line offsets when the MakerNote blob is relocated. Previously, relocated MakerNotes had stale absolute file offsets, corrupting all MakerNote values after write.
- **IPTC dataset ascending-order enforcement** (#146/#179): `iptc.Encode` now emits datasets in ascending record-and-dataset-number order as required by IIM §7. Duplicate Dataset 1:00 `EnvelopeRecordVersion` headers are suppressed when the envelope record is written alongside application-record datasets.
- **XMP GPS decoding via W3C Geo namespace** (#195): `xmp.GPS()` now recognises the W3C Geo namespace (`http://www.w3.org/2003/01/geo/wgs84_pos#`) in addition to the standard EXIF `GPS` namespace, allowing GPS coordinates stored by W3C-Geo-aware XMP producers to be decoded correctly.
- **`PreserveUnknownSegments` option honoured** (#85): the `PreserveUnknownSegments(true)` option is now applied consistently across all format writers. Previously, the option was parsed but silently ignored in several code paths.

### Fixed

- **HEIF panic on malformed `infe`/`meta` boxes** (#106/#169/#177): `parseInfeV0V1` now bounds-checks the item protection index read before advancing the position pointer. `buildInjectComponents` validates `metaContentOff <= metaAbsEnd` before slicing, closing the `panic: slice bounds out of range` paths that were confirmed by the fuzzer. AVIF brand detection hardened.
- **HEIF `iloc` construction method misresolution** (#133/#137): `parseIlocItemSimple` now reads and checks `construction_method`; items with `construction_method != 0` (idat-relative or item-relative extents) are skipped with a diagnostic rather than silently returning garbage bytes from wrong file offsets (ISO 14496-12 §8.11.3).
- **BigTIFF offset truncation** (#141/#142/#143): thumbnail offsets, MakerNote offsets, and sub-IFD offsets in BigTIFF files are now read as 64-bit values. Previously, these were read as 32-bit values, causing corrupt seeks and wrong data for BigTIFF files where offsets exceed 4 GiB.
- **EXIF partial-IFD recovery** (#126): the IFD parser now recovers usable entries from a truncated IFD rather than discarding all entries when the declared entry count exceeds the available buffer. This brings the parser into conformance with CIPA DC-008 robustness clause R-05.
- **EXIF duplicate tag deduplication** (#129): when an IFD contains duplicate tag numbers (malformed file), only the first occurrence is retained. Previously, duplicate tags could cause incorrect value reads depending on which occurrence was returned.
- **EXIF value-overlap detection** (#131/#132): the IFD parser now warns (via `metaerr`) when two entries declare overlapping value regions, and rejects NextIFD pointers that refer to an already-visited offset.
- **EXIF signed-type rejection in `Uint` accessors** (#130): `IFDEntry.Uint()` and `UintN()` now return an error rather than silently interpreting a signed EXIF type (SBYTE, SSHORT, SLONG, SRATIONAL) as unsigned.
- **EXIF Nikon Type-3 MakerNote header scan** (#114): the Nikon Type-3 parser now scans for the `Nikon\0` signature anywhere in the first 32 bytes of the MakerNote blob, accommodating the D70 and other bodies that prepend a camera-model string before the standard header.
- **EXIF Casio QVC MakerNote parsing** (#119): Casio QV-series MakerNotes (using compact type codes without the standard Casio header) are now parsed without panicking.
- **EXIF Pentax MakerNote length guard and nil byte-order** (#144/#188/#189): the Pentax MakerNote parser checks the declared length against the available buffer before slicing. A nil byte-order in the outer EXIF is now detected before any IFD traversal rather than panicking later.
- **EXIF SRational rejection in GPS IFD** (#119): GPS latitude, longitude, and altitude are RATIONAL (unsigned); entries with SRATIONAL type are now rejected with a typed error rather than silently returning a sign-wrapped coordinate value.
- **JPEG extended-XMP reassembly validation** (#122/#123): the extended-XMP parser now validates that all chunks for a given GUID share the same declared total length, surfaces truncation when the assembled payload is shorter than declared, and rejects multi-chunk payloads that disagree on total length.
- **JPEG multi-APP13 8BIM sibling preservation** (#134/#135): when rewriting the APP13 segment, the JPEG writer now preserves all non-IPTC 8BIM resource blocks (e.g. Photoshop layer settings, ICC profile references) alongside the updated IPTC block. Previously, non-IPTC 8BIM resources were silently dropped.
- **JPEG IRB Pascal-name bounds** (#151/#174): the IRB (Image Resource Block) parser now clamps the Pascal-name length to the remaining buffer size before reading, preventing silent IPTC block drops on files with a malformed Pascal name in the IRB header.
- **PNG oversized XMP chunk guard** (#147/#181): PNG Inject now returns an error if the serialised XMP payload would exceed the 2 GiB PNG chunk size limit. The input PNG signature is validated before any write begins (#181). IEND passthrough is always preserved in the output even when no metadata is present (#182).
- **TIFF RW2 `nextIFD` rebasing and SubIFD patch bounds** (#111/#116): the RW2 write path now rebases the chain `nextIFD` pointer after the GUID shift. `patchSubIFDPointers` validates the patched entry count against the IFD entry slice length to prevent out-of-bounds writes.
- **TIFF cross-container XMP wire-frame rejection** (#118): the TIFF writer now refuses to embed a JPEG-ExtendedXMP wire-frame (which is only valid inside a JPEG APP2 segment) into a TIFF container, returning an error rather than writing a malformed TIFF file.
- **TIFF integer overflow and recursion depth guards** (#115/#172/#176/#183/#184): the TIFF write and relocation paths now guard against 32-bit overflow in image-start calculations, cap IFD chain depth, and cap SubIFD recursion depth.
- **CR3 missing CMT1 insertion on write** (#138/#175): `cr3.Inject` now inserts a CMT1 sub-box when writing to a CR3 file that did not originally contain one. Previously, writing to such a file produced a structurally valid but EXIF-less CR3.
- **CR3 box-walker recursion depth cap** (#191): the CR3 box walker now limits nesting depth to 64 levels, preventing stack exhaustion from pathologically nested ISO BMFF containers.
- **ORF/RW2 original magic preservation in RawEXIF** (#117/#190): `orf.Extract` and `rw2.Extract` now preserve the original container-specific magic bytes (IIRO/IIRS/RW2 GUID) in the returned `rawEXIF` byte slice. Previously, re-encoding the returned bytes would produce an EXIF stream that the respective parsers could not re-read.
- **CR2 detection validation** (#136): `cr2.Extract` now validates the Canon CR2 marker (0x2D/0x2D in bytes 8–9) before treating the file as CR2, rather than relying solely on the TIFF magic bytes shared with other LE-TIFF formats.
- **Zero-length IPTC upsert skip** (#190): `iptc.Upsert` now skips datasets with a zero-length value silently rather than writing a malformed dataset with a zero-byte body.
- **`io.ReadAll` OOM guard** (#140): all `io.ReadAll` call sites in TIFF, HEIF, ORF, RW2, and CR3 are now wrapped with `io.LimitReader` at a format-appropriate cap (256 MiB for CR3/HEIF; 512 MiB for TIFF/ORF/RW2). Exceeding the cap returns a sentinel error without retaining the partial allocation.
- **`internal/iobuf` undersized-buffer pool return** (#186/#187): `iobuf.Put` now correctly returns buffers whose capacity exceeds the small-tier threshold but is within the large-tier cap to the large pool, rather than discarding them. This halves allocations on large-buffer cache-miss paths.
- **RIFF bounds contract enforcement** (#192): the internal `riff` package now enforces the invariant that a chunk body slice cannot extend beyond the parent list boundary, preventing cross-chunk reads in WebP and other RIFF-based containers.
- **XMP W3C Geo GPS namespace decoding** (#195): `xmp.GPS()` previously returned zero coordinates for files using the W3C Geo namespace (`http://www.w3.org/2003/01/geo/wgs84_pos#`) for GPS properties. The namespace is now recognised alongside the standard EXIF `GPS` namespace.
- **`metadata.Set*` mutex guard** (#128/#148/#185): all `Set*` methods on `*Metadata` are now protected by an internal `sync.Mutex`, making concurrent calls to `SetCopyright`, `SetCaption`, `SetKeywords`, and similar methods safe. The concurrency contract is documented in `doc.go`.
- **`Write` defensive raw-slice copy** (#139): `Write` now copies the raw EXIF, IPTC, and XMP byte slices from the source `*Metadata` before passing them to the format writer, preventing a caller that holds a reference to the original slices from observing in-flight writes.
- **IPTC auto-create for TIFF-based RAW** (#110): calling `Write` on a TIFF-based RAW file (NEF, ARW, DNG, ORF, RW2) now correctly auto-creates an IPTC block when none is present in the source, matching the behaviour already provided for JPEG and PNG.
- **`canCarryIPTC` and `Read` documentation corrected** (#178): the exported `canCarryIPTC` function and the `Read` doc comment now accurately describe which formats carry IPTC in an APP13/Photoshop IRB and which embed it directly in a TIFF IFD.
- **EXIF encode nil-IFD0 and nil-ByteOrder safety** (#58/#59): `exif.Encode` now returns a typed error instead of panicking when called with a nil IFD0 or a nil ByteOrder. These cases arise when `Write` is called on a `*Metadata` that was constructed programmatically rather than parsed from a file.
- **JPEG orphaned pooled buffer on segment resize** (#77): the JPEG writer no longer leaks the segment buffer back to the pool before the new-segment slice is written, closing a use-after-pool-return path.
- **`iptc.SetKeywords` UTF-8 flag** (#63): `SetKeywords` now sets the IPTC `CodedCharacterSet` (Dataset 1:90) flag to UTF-8 when any keyword contains non-ASCII characters, preventing downstream consumers from misinterpreting the encoding.
- **IPTC dataset count alloc bomb** (#71): `iptc.Parse` now caps the maximum total parsed dataset count at 65 535, preventing a crafted stream with a very large dataset-count field from forcing a proportional allocation before the bounds check fires.

### Security

- **HEIF CRITICAL: `infe`/`buildInjectComponents` OOB panic closed** (#106/#133/#137/#169/#177): two independent slice-bounds-out-of-range panic paths in the HEIF parser and injector were confirmed by the fuzzer and are now closed. Both were reachable from untrusted input via `Read` or `Write` (CWE-125, CWE-787).
- **Mutex guard on `Set*` methods eliminates data race** (#128/#148/#185): concurrent calls to `Set*` methods on the same `*Metadata` could produce a fatal Go runtime map-concurrent-write or pointer-assignment race. The internal `sync.Mutex` guard closes this race (CWE-362).
- **XMP namespace-URI and local-name injection closed** (#112/#113): the XMP encoder now applies XML attribute-value escaping to namespace URIs and validates local names against the XML NCName production before serialising. A crafted XMP file with a `"` in a namespace URI or `<` in a local name could inject arbitrary XML into the encoded output (CWE-91).
- **`io.ReadAll` OOM guard across TIFF/HEIF/RAW** (#140): format-appropriate `io.LimitReader` caps prevent a streaming or adversarially crafted file from exhausting heap memory during read (CWE-400).
- **XMP C0 control character filtering** (#170/#171): the XMP encoder now strips Unicode C0 control characters (U+0001–U+001F, excluding TAB, LF, CR) from property values and namespace URIs before serialisation. XML 1.0 prohibits these characters; their presence in encoded output would produce invalid XML (CWE-91).
- **CR3 box-walker recursion cap** (#175/#191): the CR3 box walker is now depth-capped at 64 levels, preventing stack exhaustion from pathologically nested ISO BMFF input (CWE-674).
- **PNG oversized XMP chunk guard** (#147/#181): the PNG writer now rejects payloads that would exceed the 2 GiB PNG chunk size limit before writing, preventing a 32-bit length overflow that would produce a corrupt file (CWE-190).
- **JPEG IRB Pascal-name bounds** (#151/#174): the IRB parser clamps the Pascal-name length to the remaining buffer, closing a read-past-end path reachable from a malformed APP13 segment (CWE-125).
- **TIFF integer overflow in image-start arithmetic** (#115/#172/#176/#183/#184): `uint32` arithmetic in the relocation path is now promoted to `uint64` before comparison, preventing silent overflow when image blocks are positioned near the 4 GiB boundary (CWE-190).

## [1.1.0] - 2026-06-03

### Added

- **`tiff.ErrUnsupportedMagic`**: new exported sentinel error returned when the TIFF parser encounters a BigTIFF magic number (`0x002B`). Previously the library silently misidentified BigTIFF files as ordinary TIFF; now callers can detect and handle this case explicitly with `errors.Is(err, tiff.ErrUnsupportedMagic)`.
- **`xmp.ErrDocumentTooLarge`**: new exported sentinel error returned when an XMP document exceeds the `maxXMPDocumentBytes` input cap (16 MiB, compile-time constant). Callers can use `errors.Is` to distinguish this condition from malformed-XML errors.
- **`FuzzRead`**: end-to-end fuzz target at the top-level package (`FuzzRead`) that drives the full `Read` orchestrator with arbitrary input. This joins 26 existing fuzz targets for a total of 27.
- **CI fuzz job**: a new `fuzz` CI job runs 6 fuzz targets for 10 seconds each under `-race`, catching regressions on every pull request.
- **FormatCapability knowledge-graph matrix**: the format capability matrix (which combinations of format and operation are supported) is now recorded in the project knowledge graph (the `FormatCapability` matrix mirroring `format.SupportsWrite`).

### Changed

- **JPEG ExtendedXMP GUID cap**: the JPEG parser now caps the number of distinct ExtendedXMP GUIDs at 4 per file during reassembly of multi-segment ExtendedXMP payloads (each GUID is itself capped at 16 MiB, giving a 64 MiB aggregate ceiling). Excess GUIDs beyond the fourth are dropped and the reassembled payload is marked truncated, preventing memory exhaustion from crafted multi-segment JPEGs without aborting the parse.
- **Write-support documentation corrected**: README, CHANGELOG, SECURITY.md, and `doc.go` now precisely state that `Write` is supported for JPEG, PNG, WebP, HEIF/AVIF, and Canon CR3. TIFF-based containers (TIFF, CR2, NEF, ARW, DNG, ORF, RW2) are read-only; `Write` returns `ErrWriteNotSupported` for those formats.
- **`go.mod`**: `golang.org/x/text` reclassified as a direct dependency (the `// indirect` annotation removed by `go mod tidy`).
- **Test coverage**: +207 tests added since v1.0.4. New tests cover EXIF/TIFF adversarial fuzz seeds, the `internal/iobuf` buffer pool (race, contamination, and DoS scenarios), and the top-level read orchestrator.

### Fixed

- **`iptc.Encode` receiver mutation** (`iptc/iptc.go`): `Encode` previously appended the IPTC 1:90 UTF-8 `CodedCharacterSet` marker directly to the receiver's `Records[0]` slice when emitting non-ASCII content, mutating shared state and creating a data race under concurrent `Write` calls. The fix emits the 1:90 declaration to the encoded output only (via the existing `needsUTF8Declaration` path); the receiver is now pure and idempotent.
- **`internal/iobuf` pool hardening** (`internal/iobuf/iobuf.go`): `Get(n)` now clamps negative `n` to zero instead of passing it to `make`, which would have panicked. `Put` now discards buffers whose capacity exceeds the large-tier canonical cap (`largeSize = 65536`) rather than returning them, preventing unbounded pool growth from adversarially crafted payloads.
- **DoS caps and write determinism** (`exif`, `iptc`, `xmp`, `format/jpeg`): follow-up fixes from the Sprint 8 re-audit — additional byte-count caps on IFD entry aggregation, IPTC dataset aggregation, and XMP attribute accumulation; deterministic output ordering for EXIF and IPTC write paths.
- **Untrusted-input crash sites** (`exif`, `iptc`, `xmp`, `format/*`): elimination of the remaining nil-dereference and out-of-bounds-slice-index paths reachable from attacker-controlled binary data identified in the Sprint 8 audit.

### Security

- **XMP document-level input cap** (`xmp/`): introduced `maxXMPDocumentBytes` (16 MiB, compile-time constant) as a hard ceiling on the total UTF-8 bytes accepted by a single XMP parse (checked post-normalisation, before the RDF scan). Exceeding the cap returns `xmp.ErrDocumentTooLarge` without allocating further memory, preventing memory-exhaustion attacks from crafted XMP payloads (CWE-400).
- **TIFF/BigTIFF discrimination** (`format/tiff/`): the TIFF parser now reads the magic word and immediately returns `tiff.ErrUnsupportedMagic` for BigTIFF (`0x002B`). Previously the parser would misinterpret BigTIFF offsets as TIFF-6 offsets and could seek to arbitrary positions in the file or allocate large intermediate buffers (CWE-125, CWE-400).
- **JPEG ExtendedXMP GUID cap** (`format/jpeg/`): the parser caps distinct ExtendedXMP GUIDs at 4 per file (each GUID itself capped at 16 MiB, giving a 64 MiB aggregate ceiling); excess GUIDs are dropped and the result marked truncated, preventing memory exhaustion from crafted multi-segment JPEGs (CWE-400).
- **`iptc.Encode` data race eliminated** (`iptc/`): the receiver-mutation bug described under Fixed was also a data-race vulnerability under concurrent use; the fix eliminates the race without API change (CWE-362).
- **`internal/iobuf` pool hardening** (`internal/iobuf/`): the `Get(n<0)` panic path and the oversized-buffer pool-retention path are both closed, removing two crash/memory-exhaustion vectors reachable from attacker-controlled input sizes (CWE-400, CWE-476).

## [1.0.4] - 2026-04-08

### Added

- **SECURITY.md**: fuzz target inventory, supported fuzz targets (`FuzzParseEXIF`, `FuzzParseIPTC`, `FuzzParseXMP`), responsible disclosure process, and the library's security model for parser hardening.
- **CONTRIBUTING.md**: full contributor guide covering dev environment setup, build and test commands, linter configuration, fuzz testing workflow, and CI pipeline overview.
- **`examples/copyright-stamp`**: end-to-end example that reads a JPEG, sets copyright and artist metadata via EXIF and XMP, and writes the result back.
- **`examples/gallery-sidecar`**: example that extracts metadata from any supported image format and writes an XMP sidecar file alongside the original.
- **`examples/multi-format-roundtrip`**: example demonstrating a full read–modify–write cycle across JPEG, PNG, WebP, HEIF, and RAW formats.
- **`examples/raw-inspector`**: example that opens RAW files (CR2, CR3, NEF, ARW, DNG, ORF, RW2) and prints all EXIF IFD entries, MakerNote fields, and GPS data.
- **`examples/stream-transcode`**: example that streams metadata from one image format and injects it into another without loading full pixel data.
- **`example_test.go`**: runnable Go example functions in the top-level package covering EXIF, IPTC, and XMP reading and writing across all image formats; these serve as both API documentation and tested usage samples.

### Changed

- **README.md**: added an Examples section with code excerpts and links to the full example programs; added benchmark reproduction instructions so contributors can verify performance claims locally.
- **Test coverage**: expanded from 68% to 88% across all 25 packages. New tests target previously uncovered branches in `exif/makernote` (Canon, DJI, Fujifilm, Leica, Nikon, Olympus, Panasonic, Pentax, Samsung, Sigma, Sony), `format` (HEIF, JPEG, PNG, TIFF, WebP, all RAW variants), `internal` (bmff, iobuf, riff, testutil), `iptc`, `xmp`, and the top-level API (`metadata_convenience_test.go`, `options_test.go`, `read_test.go`).

## [1.0.3] - 2026-04-07

### Security

- **IPTC extended-length integer overflow** (`iptc/iptc.go`): added an immediate `length < 0` guard after the extended-length accumulation loop to prevent sign-bit overflow on 32-bit platforms (IIM §1.6.2, CWE-190).
- **IPTC unbounded aggregate allocation** (`iptc/iptc.go`): added `maxIPTCTotalBytes = 256 MiB` cap on the total size of all parsed datasets in a single stream, preventing memory exhaustion from crafted files with many large datasets (CWE-400).
- **XMP entity expansion** (`xmp/rdf.go`): `unescapeXML` now returns an empty string and recycles the pooled builder if the decoded output of a single attribute or text node exceeds 1 MiB, preventing unbounded allocation from crafted numeric character references (CWE-776).
- **EXIF IFD entry over-allocation** (`exif/ifd.go`): `parseSingleIFD` caps the pre-allocated `Entries` slice capacity at 1 024, preventing a crafted `count = 0xFFFF` field from forcing a 65 535-entry allocation before the buffer-bounds check fires (CWE-190).
- **HEIF item offset overflow** (`format/heif/heif.go`): `readItemPayload` now validates that `loc.offset` fits in `int64` before the `Seek` conversion, preventing sign-wrapping on the cast; added private `extractItemSlice` helper with the same guard for the in-memory code path.
- **PNG decompression bomb** (`format/png/png.go`): `zlibDecompress` now reads through `io.LimitReader` capped at 64 MiB and returns a sentinel error if the limit is exceeded, preventing zip-bomb-style payloads from exhausting memory.

## [1.0.2] - 2026-04-07

### Performance

- **XMP GPS parse**: `strings.Split` replaced with `strings.Cut`; GPS coordinate parsing is now zero-allocation (`BenchmarkGPSParse`: 0 B/op, 0 allocs/op).
- **XMP `Keywords`**: single-pass `strings.IndexByte` scan with `strings.Count`-pre-sized result slice replaces `strings.Split`; eliminates the intermediate `[]string` allocation per call.
- **XMP `AddKeyword`**: `strings.Builder` with pre-grown capacity replaces string concatenation; one allocation instead of two per keyword append.
- **XMP `SetGPS`**: `strconv.AppendFloat` into a `[32]byte` stack buffer replaces `fmt.Sprintf`; eliminates heap allocation and `fmt` reflection overhead per GPS encode.
- **XMP `writeMultiValuedProperty`**: `strings.IndexByte` loop replaces `strings.Split`; the `[]string` allocation on every multi-valued property encode is eliminated.
- **XMP packet scanner**: `[]byte("?>")` literals extracted to package-level variables; no heap allocation on every `Scan` call.
- **XMP RDF parser**: per-call `[]byte("-->")` and `[]byte("?>")` literals extracted to package-level; `rdf:Alt` item concatenation uses a pooled `strings.Builder`; named-entity comparison uses `switch string(ref)` (compiler-optimised zero-alloc path).
- **IPTC ISO-8859-1 decoder**: per-call `charmap.ISO8859_1.NewDecoder()` replaced with a `sync.Pool`; decoder is `Reset()` before each use.
- **HEIF write path**: `buildIlocBox` and `buildMetaBox` now measure required length in a first pass and allocate a single pre-sized output buffer, eliminating incremental `append` reallocs; `appendUintN` uses `binary.BigEndian.AppendUint16/32/64` instead of `make([]byte, n)` per field.
- **JPEG segment copy**: all four `append([]byte(nil), ...)` call sites replaced with `bytes.Clone`.
- **PNG write path**: `crc32.NewIEEE()` pooled via `sync.Pool` to avoid per-chunk hash allocation; 8-byte chunk header stack-allocated (`[8]byte` instead of `make([]byte, 8)`).
- **PNG read path**: `readChunk` refactored to a callback pattern; the pooled buffer is passed directly to the callback without cloning in the common (non-retained) path, saving one allocation and one copy per pass-through chunk.
- **WebP write path**: `bytes.Buffer` in `buildWebPBody` pooled via `sync.Pool`; 4-byte RIFF chunk size field stack-allocated.
- **ORF/RW2 write path**: only the 4-byte magic header is patched in-place on the `io.ReadAll`-owned slice; the previous full-file copy is eliminated.
- **EXIF `filterEntries`**: accepts an `extraCap` argument to pre-size the result slice, avoiding a realloc when `buildIFD0Entries` appends trailing entries.
- **`internal/bmff`**: `Box.Equal([4]byte) bool` added for zero-alloc box type comparison.
- **`internal/riff`**: `Chunk.Equal([4]byte) bool` added for zero-alloc FourCC comparison.
- **XMP date layouts**: inline `[]string` literal in `metadata.DateTime()` hoisted to a package-level `[3]string` array, eliminating the per-call slice header allocation.

### Changed

- All packages now define package-level sentinel error variables (`ErrXxx`) for every error previously constructed inline with `errors.New` or `fmt.Errorf`; callers can now use `errors.Is` for reliable error identity checks. Affected packages: `exif`, `format/heif`, `format/jpeg`, `format/png`, `format/tiff`, `format/webp`, `format/raw/cr3`, `format/raw/orf`, `format/raw/rw2`, `xmp`, and the top-level package.
- Import ordering enforced across all files (`gci` linter, stdlib → external → internal grouping).
- `t.Parallel()` added to all table-driven tests and `t.Run` callbacks across all 43 test files; the entire test suite now runs with maximum parallelism under `go test -race ./...`.
- Linter suite expanded by five additional rules: `err113` (no inline error construction), `godot` (comment punctuation), `nestif` (nesting depth ≤ 4), `godox` (no TODO/FIXME/HACK comments), `gci` (import ordering), `paralleltest`/`tparallel` (parallel test enforcement), and `funlen` (function length ≤ 80 lines / 60 statements).
- `metadata.DateTime()` refactored from four levels of nesting to guard clauses (cyclomatic complexity reduced from 6 to 1; behaviour unchanged).

### Fixed

- **`sync.Pool` use-after-put race in `format/detect.go`**: `mapMakeToFormat` was called after `tiffScanPool.Put(buf)` despite `makeRaw` being a subslice of the pooled buffer; reordered to call `mapMakeToFormat` before `Put`.
- **`sync.Pool` use-after-put race in `format/heif/heif.go`**: `extractFromMetaData` was called after `iobuf.Put(hdrPtr)` despite `metaData` being a subslice of the pooled buffer; reordered likewise.
- **PNG data lifetime bug**: `eXIf`, `tEXt`, and `iTXt` chunk handlers in `readChunk` were retaining references to a pooled buffer slice without cloning; the callback-pattern refactor ensures retained data is always copied from the pool before the buffer is returned.

## [1.0.1] - 2026-04-06

### Changed

- Linter suite expanded from 25 to 46 checked rules; contributors now benefit from stricter automated enforcement including `nilnesserr`, `wastedassign`, `recvcheck`, `inamedparam`, `nolintlint` strict mode, `intrange`, `mirror`, `modernize`, and 13 additional linters.
- All `interface{}` occurrences replaced with the `any` type alias throughout the codebase, in line with the Go 1.18+ convention.
- All functions refactored to cyclomatic complexity ≤ 10, making the codebase easier to extend and audit.
- CI pipeline hardened: golangci-lint v2.11.4 pinned to a specific version, all GitHub Actions runners updated to their latest major versions, `gofmt -s` simplification enforced on every commit, and Codecov coverage reporting integrated.
- MIT licence file added to the repository.

### Fixed

- Variable shadowing in several parser functions: inner error variables were silently shadowing outer ones in chained binary-read paths; renamed to eliminate ambiguity (`govet shadow`).
- Missing `t.Helper()` calls in test helper functions corrected; failure line numbers now point to the actual test case rather than the helper body.
- Redundant `strings.X(string(b), ...)` patterns replaced with `bytes.X(b, ...)` throughout the XMP and IPTC packages, eliminating a transient allocation per call in those hot paths.
- Several counter loops modernised to the `for i := range n` idiom (Go 1.22+).
- Superfluous `else` blocks after early returns removed throughout the parser code.
- Inconsistent receiver variable names within types corrected.

## [1.0.0] - 2026-04-04

### Added

- **Unified read/write API**: `Read(r io.ReadSeeker) (*Metadata, error)` and `Write(r io.ReadSeeker, md *Metadata) ([]byte, error)` — the container format is detected automatically from magic bytes; no configuration required.
- **`Metadata` struct** with typed accessor and setter methods for all common fields: camera make and model, lens, capture date/time, GPS coordinates (decoded to decimal degrees), copyright, artist, caption/description, keywords, rating, orientation, exposure, focal length, ISO, aperture, and shutter speed.
- **EXIF parser and writer**: full IFD traversal (IFD0, IFD1, SubIFD, GPS IFD, Interop IFD), tag registry covering approximately 200 standard CIPA DC-008/JEITA CP-3451 tags, both big-endian and little-endian byte order, and all TIFF 6.0 data types.
- **MakerNote dispatch** for 11 manufacturers — Canon, DJI, Fujifilm, Leica, Nikon, Olympus, Panasonic, Pentax, Samsung, Sigma, and Sony — each with a brand-specific tag registry.
- **IPTC IIM parser and writer**: all standard records (Records 1–9), dataset decoding, APP13/Photoshop IRB extraction, and UTF-8 character encoding via the IPTC envelope (Dataset 1:90).
- **XMP parser and writer**: full RDF/XML parsing, all RDF collection types (`rdf:Seq`, `rdf:Bag`, `rdf:Alt`), packet scanning and in-place injection, and a namespace registry covering all standard schemas (`dc`, `xmp`, `xmpRights`, `photoshop`, `Iptc4xmpCore`, `Iptc4xmpExt`, `exif`, `tiff`, `aux`, `GPS`).
- **Container support** — read for all formats: JPEG (APP1/APP13 segments), TIFF, PNG (`iTXt`/`tEXt` chunks), WebP (RIFF VP8X/EXIF/XMP chunks), HEIF/HEIC (ISO BMFF box traversal), and the RAW variants Canon CR2, Canon CR3, Nikon NEF, Sony ARW, Adobe DNG, Olympus ORF, and Panasonic RW2. Metadata write (injection) is supported for JPEG, PNG, WebP, HEIF/AVIF, and Canon CR3; TIFF-based containers (TIFF, CR2, NEF, ARW, DNG, ORF, RW2) are read-only in this release (`Write` returns `ErrWriteNotSupported`).
- Format detection by magic bytes — never by file extension.
- GPS decimal-degree decoding and encoding: degrees/minutes/seconds rational values are decoded to `float64` and re-encoded on write.
- Convenience CLI example programs shipped under `examples/`: `read-metadata`, `write-metadata`, and `batch-keywords`.
- Zero-allocation parsing fast path across all format parsers: `sync.Pool` for reusable buffers, `[]byte` slice references over copies, and lazy field parsing — only fields the caller accesses are decoded.

### Fixed

- Panic-free handling of malformed, truncated, and corrupted image files across all parsers.
- Integer overflow protection in HEIF box-length arithmetic and TIFF IFD entry counts.
- IPTC IIM record boundary compliance (IPTC IIM 4.2, §2).
- HEIF/HEIC container rewritten for correct nested-box traversal (ISO 14496-12).
- WebP RIFF chunk four-byte alignment padding applied correctly on write.
- RAW format IFD pointer resolution for manufacturer-specific sub-IFDs.

### Security

- All parsers harden against attacker-controlled offsets, lengths, and counts in binary data; no out-of-bounds read is possible on any `io.ReadSeeker` consumer.
- Fuzz targets `FuzzParseEXIF`, `FuzzParseIPTC`, and `FuzzParseXMP` ship as part of the test suite so regressions are caught automatically.

---

[Unreleased]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.4...v1.1.0
[1.0.4]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.3...v1.0.4
[1.0.3]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/FlavioCFOliveira/GoMetadata/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/FlavioCFOliveira/GoMetadata/releases/tag/v1.0.0
