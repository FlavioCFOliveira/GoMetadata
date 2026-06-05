---
name: high-risk-locations
description: Recurring unsafe patterns, confirmed vulnerabilities, and safe reference patterns in GoMetadata — updated Round 2 post-hardening
metadata:
  type: project
---

# High-Risk Code Locations (GoMetadata) — Round 2

## OPEN Vulnerabilities

None confirmed open as of 2026-06-03. FINDING-008 and FINDING-009 both closed (see audit_findings_20260601.md).

None. The multi-GUID aggregate cap was added as `maxExtendedXMPGUIDs = 4` in Task #53 (2026-06-03). Verified CLOSED by per-task audit.

## Closed Vulnerabilities (do not regress)

| Location | What was fixed | Round |
|---|---|---|
| `iptc/iptc.go:95` storeDataset | `if record < 1 \|\| int(record) >= len(i.Records)` guard | 1 |
| `format/heif/heif.go:295` parseHEIFBoxHeader | `if size < headerLen { return false }` guard | 1 |
| `format/raw/cr3/cr3.go:52` parseCR3BoxHeader | same guard + `findUUIDBox` requires `size >= headerLen+16` | 1 |
| `format/png/png.go:457` readChunk | `maxPNGChunkSize = 256MB` + LimitReader in readLargeChunk | 1 |
| `format/webp/webp.go:101` readPaddedChunk | `maxWebPChunkSize = 256MB` + seek-verify against stream | 1 |
| `write.go:122` WriteFile | `CreateTemp(filepath.Dir(path), ...)` avoids EXDEV | 1 |
| `format/jpeg/jpeg.go:140` processAPP1Segment | rawEXIF returned only when payload >= 8 bytes | 1 |
| `xmp/rdf.go:191` onEndElement | `if p.depth <= 0 { return }` depth underflow guard | 1 |

## Safe Reference Patterns

### SAFE: IPTC record bounds guard
```go
if record < 1 || int(record) >= len(i.Records) {
    return
}
```

### SAFE: ISOBMFF box header guard (heif.go, cr3.go)
```go
if size < headerLen {
    return 0, "", 0, false
}
```

### SAFE: Large allocation — read proportional to stream, not declared size
```go
data, err := io.ReadAll(io.LimitReader(r, int64(length)))
if len(data) != length { return truncated error }
```

### SAFE: Sync.Pool buffer — always clone before Put
```go
result := bytes.Clone(buf.Bytes())
encBufPool.Put(buf)
return result
```

### SAFE: IFD cycle detection (exif/ifd.go)
- Uses `visited` map keyed on IFD offset
- Preallocates at most 1024 tags per IFD
- Uses `uint64` for `totalSize` arithmetic to prevent overflow

### SAFE: XMP depth tracking (xmp/rdf.go)
- `p.depth <= 0` guard in `onEndElement` prevents negative depth
- `nsDepth [101]nsDepthEntry` fixed-size stack prevents OOB at depth 0

### SAFE: XMP UTF-32 decode (xmp/encoding.go)
- `maxOut := min(nCodePoints*4, maxUnescapedXMLBytes)` caps allocation at 1MB
- Early break when `len(out) >= maxUnescapedXMLBytes`

### SAFE: XMP UTF-16 decode (xmp/encoding.go) — CLOSED (Task #51 pre-fix)
- `maxXMPTranscodeBytes = 16 MiB` guard at encoding.go:97 and encoding.go:109
- Post-transcode output (up to 24 MiB for 16 MiB UTF-16 BMP content) is caught by `maxXMPDocumentBytes = 16 MiB` at xmp.go:75
- Both caps work together: no gap

## Architecture Constraints That Bound Risk

- TIFF-based write path: `isTIFFBased()` check in `write.go:88` returns `ErrWriteNotSupported` before any allocation
- HEIF/PNG XMP max size: 256MB per item/chunk (not 4GB). Still large enough for 2x amplification concern.
- JPEG APP1 segments: 16-bit length field → max 65533 bytes per segment. Extended XMP has no per-GUID aggregate cap.
- decodeXMPWire: only called with data produced by encodeXMPWire (mainLen always ≤ 65533). Not user-controllable.

## Task #48 Guard Verification (2026-06-02)

### tiff/tiff.go magic-number check (lines 26-43)
- `len(data) < 8` at line 26 → `ErrFileTooShort` before any byte access beyond header
- `byteOrder(data)` at line 30 reads only `data[0]` and `data[1]` — safe given len>=8
- `magic := order.Uint16(data[2:])` at line 39 — safe: len>=8 guarantees bytes 2-3 exist
- `if magic != 0x002A` at line 40 rejects BigTIFF (0x002B) and all other non-42 values
- Error wraps `ErrUnsupportedMagic` with `%w` → `errors.Is(err, tiff.ErrUnsupportedMagic)` works through all RAW wrapping layers (arw:/cr2:/dng:/nef: prefix then tiff: prefix)
- ORF/RW2 bypass tiff.Extract entirely (magic-patch + local IFD scan); they only call tiff.Inject, which uses buildUpdatedTIFF → exif.Parse — `exif.Parse` rejects BigTIFF because it also expects classic TIFF header; ORF/RW2 BigTIFF inputs are caught by their own ErrInvalidMagic check before any TIFF code runs

### DoS chain bound (exif/ifd.go:220-238, traverse)
- No numeric depth cap exists; bound is the `visited` map (map[uint32]bool, recycled via visitedPool)
- For an acyclic N-IFD chain: traverse runs N iterations (one per unique offset); all N IFDs parsed; terminates when next==0
- For cyclic input: `visited[cur]` check at line 221 breaks the loop immediately
- Both cases are O(N) in input buffer size — N is bounded by `len(b)/6` (minimum IFD size is 6 bytes)
- A 10,000-IFD chain requires ~60KB buffer; a 500,000-IFD chain requires ~3MB buffer — linear, not exponential
- TestInjectLongIFDChainDoSBound uses 500 IFDs (correct: proves bound works; 10,000 just proves Extract reads only IFD0)
- Note: parseSingleIFD (line 132) rejects an IFD claiming more entries than the buffer can hold; prevents a single IFD from causing O(N²) work

### tiff/tiff.go error propagation confirmed SAFE
- `extractByFormat` in read.go:199-201 wraps extractor errors with `gometadata: %w`; non-nil error returned to `Read` caller without panicking in either strict or best-effort mode
- ORF Extract (orf.go:30) checks orfMagic BEFORE any TIFF parse; returns ErrInvalidMagic on mismatch — BigTIFF header bytes 2-3 (0x2B) do not match orfMagic[2] (0x52), so ErrInvalidMagic is returned, never ErrUnsupportedMagic from tiff layer

## Task #50 Sprint 8 — FuzzRead End-to-End Target (2026-06-03)

### Task #53 FuzzJPEGExtract run (2026-06-03)
- 29.7M execs, 60s, 0 crashers — relaxed assertion confirmed correct-layer; GUID-flood seeds in corpus
- Seeds present: 324ecc33f57fa484, 46d583d332f7645b, a3ef598e7e78544c + 10 named seeds

### FuzzRead coverage and contracts
- Target: `fuzz_test.go:22` in package `gometadata` — calls real `Read()` (not a sub-parser)
- Seed count: 14 inline seeds + 7 on-disk seeds in `testdata/fuzz/FuzzRead/`
- Formats exercised by seeds: JPEG, JPEG+EXIF, JPEG+XMP, JPEG+IPTC, JPEG+all-three, PNG, PNG+EXIF (eXIf), WebP, TIFF LE, TIFF BE, unknown magic, empty, truncated
- Contracts tested per fuzz iteration: best-effort never panics; strict may error but never panics; all 29 accessors nil-safe on parsed result
- 60s run: 16.4M execs, 0 crashers; 45s+race run: 509K execs, 0 crashers

### iptc.Encode mutation under concurrent Write — CLOSED (Task #52, 2026-06-03)
- FINDING-002 (this project's numbering): `Encode` previously appended to `i.Records[0]` when non-ASCII content required a 1:90 declaration, causing a data race on concurrent Encode calls.
- Fix: `Encode` now computes `emitUTF8Decl := i.isUTF8() || i.needsUTF8Declaration()` and writes `{0x1C,0x01,0x5A,0x00,0x03,0x1B,0x25,0x47}` to the output buffer only — `i.Records` is NEVER mutated.
- The 1:90 declaration was NOT solely produced by the removed mutation; it is now written independently in the output path (iptc.go:269). Declaration emission is fully preserved.
- Race confirmed gone: `TestConcurrentEncodeNonASCII` (16 goroutines, -race) PASSES. Receiver unchanged before and after all calls.
- Round-trip parse-after-encode still detects UTF-8: Parse reads the 1:90 declaration from the byte stream and sets the internal flag — no dependency on the removed side effect.

## Task #51 Sprint 8 XMP Document Cap (2026-06-03)

- Document cap `maxXMPDocumentBytes = 16 MiB` added to `Parse` at xmp.go:75 (post-transcode)
- Applied BEFORE `Scan` + `parseRDF` — first check after normalisation
- `parseRDF` is unexported — no bypass via `Scan` possible
- `FuzzParseXMP` 60s: 28.7M execs, 0 crashers — verified clean
- All 35 packages `go test -race ./...` PASS

## Task #53 Sprint 8 — format/jpeg hardening + fuzz assertion relaxation (2026-06-03)

### maxExtendedXMPGUIDs cap (appendExtendedXMPChunk, jpeg.go:184-193)
- `maxExtendedXMPGUIDs = 4` constant at jpeg.go:83. Cap enforced at the FIRST-SEEN branch: `if len(extSizes) >= maxExtendedXMPGUIDs { return extended, extSizes, true }`.
- Once a GUID is in extSizes it is FULLY accumulated (no early exit bypassed). New GUIDs above the cap are silently dropped and extTruncated is set.
- Memory ceiling: 4 GUIDs × 16 MiB = 64 MiB worst-case. Confirmed correct by code read and TestExtendedXMPGUIDCapBoundary.
- Single-GUID normal files: cap only triggers on NEW guid insertion. Normal files with exactly 1 GUID never hit the `len(extSizes) >= 4` check on that GUID's first chunk. Confirmed by TestExtendedXMPMultiSegmentGUIDReassembly and TestInjectExtendedXMP round-trip.

### FuzzJPEGExtract assertion relaxation (fuzz_test.go, task #53)
- REMOVED assertion: `bytes.Contains(rawXMP, "<?xpacket")` / `bytes.Contains(rawXMP, "<rdf:")` / `bytes.Contains(rawXMP, "<x:xmpmeta")`.
- Reason confirmed correct: processAPP1Segment (jpeg.go:237-239) copies data[len(identXMP):] VERBATIM — zero XML validation at the JPEG layer. The identXMP prefix is 29 bytes (`http://ns.adobe.com/xap/1.0/\x00`); anything after it is rawXMP. The crasher 46d583d332f7645b was a JPEG with a standard XMP APP1 carrying 0-digit payload (all zeros) — structurally a valid APP1 XMP segment, no XML content. Assertion was wrong-layer: XMP content validation is the xmp package's responsibility.
- REMAINING assertions: `len(rawEXIF) == 0 || len(rawEXIF) >= 8` at fuzz_test.go:57-59 (JPEG-layer invariant, still present). The `_ = rawXMP` no-op is the correct JPEG-layer stance.
- Fuzzer is NOT toothless: the no-panic contract (implicit via test harness) remains; rawEXIF length invariant remains. 60-second run at ~490K exec/s, 29.7M total executions, zero crashers.
- ASSESSMENT: relaxation is correct and layer-appropriate. No JPEG-layer assertion was lost.

## Fuzz Coverage Status (as of 2026-06-02 Sprint 8 Task #47 audit)

All 26 fuzz targets: 0 crashers across 25s+ runs. Key high-coverage targets:
- FuzzParseIPTC: 15.9M execs — closed OOB verified
- FuzzParseXMP: 12.3M execs — depth underflow fix verified
- FuzzHEIFExtract: 50K execs (I/O bound by readItemPayload seek calls) — box parse fix verified
- FuzzCR3Extract: 14.5M execs — box parse fix verified
- FuzzParseEXIF: 16.6M execs (45s, 2026-06-02) — IFD traversal, cycle detection, count overflow all clean
- FuzzCanonParse, FuzzSonyParse, FuzzNikonParse: 20s runs clean (2026-06-02)

## Task #48 Sprint 8 Fuzz Runs (2026-06-02)
FuzzTIFFExtract: 4.6M execs, 40s, 0 crashers — BigTIFF+truncation+cyclic seeds all in corpus
FuzzORFExtract: 4.5M execs, 21s, 0 crashers
FuzzRW2Extract: 2.9M execs, 21s, 0 crashers

## Task #47 Guard Verification (2026-06-02)

### maxIFDEntryPrealloc=1024 (exif/ifd.go:139-141)
- Applied in `parseSingleIFD` only, which is the SINGLE entry point for all IFD parsing
- ALL IFD types (IFD0, IFD1, ExifIFD, GPSIFD, InteropIFD, MakerNote sub-IFDs) reach this via `traverse()` → `parseSingleIFD()`
- Cap bounds only the INITIAL pre-allocation; final slice length can reach int(count) if all entries are valid
- For type=0 (unknown) entries, ALL entries pass parseIFDEntry unconditionally → up to 65535 entries appended
- 65535 entries × 48 bytes = ~3MB per IFD: linear in input size, not an allocation bomb
- TIFF spec: count is uint16, max 65535; buffer must contain count×12 bytes → ~786KB minimum input for max entries

### Cycle detection (exif/ifd.go:209-214, 220-224)
- `visited` map keyed on IFD offset, obtained from `visitedPool`
- Covers: IFD0→IFD1→IFD2 next-IFD chain (all in same traverse() call and visited map)
- ExifIFD, GPSIFD, InteropIFD: each traversed by separate traverse() call with separate visited map
- A cycle where ExifIFDPointer and GPSIFDPointer point to the same offset causes two independent traversals (no shared state): SAFE
- MakerNote sub-IFDs: traverse() called on MakerNote SLICE (not full TIFF); offsets can't escape the slice

### count×typeSize overflow (exif/ifd.go:81)
- `totalSize := uint64(sz) * uint64(cnt)` — promotion to uint64 before multiplication
- Guards ALL entry types including RATIONAL (sz=8, max cnt=0xFFFFFFFF would give totalSize=0xFFFFFFF8)
- Applied on every parseIFDEntry call — covers all IFD levels and MakerNote sub-IFDs

### Value offset bounds check (exif/ifd.go:91-96)
- `end := uint64(valOff) + totalSize; if end > uint64(len(b)) { return IFDEntry{}, false }`
- Covers all out-of-line value reads; inline values (totalSize≤4) are within already-validated buffer range

### TestMakeTrailingSpaceFullParsePath — assertion note
- Hard asserts only: `MakerNote != nil` (raw bytes retained regardless of dispatch)
- The TrimSpace dispatch is confirmed working by t.Logf output: MakerNoteIFD non-nil in practice
- The UNIT test in makernote_parse_test.go (TestParseMakerNoteIFDMakeWithTrailingSpace) provides the
  hard non-nil assertion for TrimSpace behavior — that test directly asserts `ifd != nil` for "NIKON CORPORATION "
