---
name: audit-findings-20260604-container
description: Security audit findings for container/segment extraction scope (format/*, internal/bmff, internal/riff, internal/iobuf) — 2026-06-04
metadata:
  type: project
---

# Container/Segment Extraction Audit — 2026-06-04

## Scope
format/detect.go, format/format.go, format/jpeg/jpeg.go, format/png/png.go, format/webp/webp.go, format/heif/heif.go, format/raw/{arw,cr2,cr3,dng,nef,orf,rw2}/*.go, internal/bmff/bmff.go, internal/riff/riff.go, internal/iobuf/iobuf.go

## Findings

### FINDING-C001 — WebP Inject VP8X canvas corruption (HIGH, CONFIRMED)
- **Location**: format/webp/webp.go:259 `buildVP8XFlags`
- **Root cause**: `copy(vp8xData, origVP8XData[:10])` where origVP8XData may have len < 10 but cap >= 10 (depending on io.ReadAll buffer allocation).
- **The guard that's wrong**: `collectOriginalChunks` sets origVP8XData when `size >= 10` but dataEnd is clamped to `min(dataStart+size, len(original))`, meaning the slice may have fewer than 10 bytes.
- **Impact**: Silent data corruption. WebP canvas width/height and feature flags are populated from bytes beyond the VP8X data region (adjacent chunk header bytes). Writes corrupted VP8X to output — could make the WebP unrenderable.
- **PoC**: WebP file with VP8X size field = 10, only 5 actual bytes of VP8X data, followed by any chunk. Inject writes the adjacent chunk's header bytes as canvas dimensions.
- **Fix**: Change condition from `if size >= 10 { origVP8XData = original[dataStart:dataEnd] }` to use `dataEnd` length check: `if dataEnd-dataStart >= 10 { origVP8XData = original[dataStart:dataEnd] }` so origVP8XData is only set when it genuinely contains 10+ bytes.
- **Regression test**: `TestInjectVP8XTruncatedChunkPreservesCanvasOrErrors` — feed truncated VP8X, verify either (a) Inject returns an error, or (b) output VP8X canvas bytes match what was in the input (not garbage from adjacent data).

### FINDING-C002 — HEIF Inject iloc extentCount allocation amplification (MEDIUM)
- **Location**: format/heif/heif.go:363 `readIlocFullExtents`
- **Root cause**: `make([]ilocExtent, 0, extentCount)` where extentCount comes from uint16 (max 65535). Called once per item in parseIlocFull (Inject path only; gated by offsetSize != 0 check at line 163).
- **Impact**: For a crafted iloc box with many items, each claiming 65535 extents, memory use amplification is ~6x relative to input data size (each item needs offsetSize*65535 bytes of actual iloc data, allocates 65535*24=1.5MB). 100MB crafted file → ~600MB RSS.
- **This is compounded by the existing uncapped io.ReadAll (known LOW finding)**.
- **Prerequisite**: offsetSize must be non-zero (enforced by guard at line 163); extentCount = 65535 means each item actually has 65535 non-trivial extents in the iloc data, so amplification is bounded by real data.
- **Fix**: Add `const maxExtents = 1024` and cap extentCount before allocation: `if extentCount > maxExtents { extentCount = maxExtents }`. Real HEIF files have 1-3 extents per item.
- **Regression test**: `TestHEIFInjectLargeExtentCountBounded` — craft iloc with extentCount=65535 per item, assert RSS stays bounded (or function returns error if cap is enforced as error).

### FINDING-C003 — HEIF patchAncestorSize uint32 truncation for large boxes (LOW)
- **Location**: format/heif/heif.go:609-611
- **Root cause**: `newSize := int(size) + delta; binary.BigEndian.PutUint32(data[pos:], uint32(newSize))`. If newSize > 0xFFFFFFFF (possible when moov box size was already near 4GB and meta grew), uint32 cast silently truncates, writing a wrong box size.
- **Also**: If ancestor box uses 64-bit largesize (size==1), patchAncestorSize skips patching entirely, leaving a stale size. The meta box grows but its container size is not updated.
- **Impact**: Corrupt HEIF output (wrong box sizes). Only affects files where ancestor box is close to 4GB limit or uses 64-bit sizes. Very rare in practice.
- **Fix**: Check `newSize > 0xFFFFFFFF` before writing (return error or skip); handle largesize update case.

### FINDING-C004 — PNG/WebP/collectOriginalChunks uint32→int on 32-bit platforms (LOW)
- **Locations**: png/png.go:451, webp/webp.go:225, tiff/tiff.go:166, orf/orf.go:106, rw2/rw2.go:109
- **Root cause**: `length := int(binary.BigEndian.Uint32(hdr[:4]))` on 32-bit Go (int=32 bits), values >= 0x80000000 become negative, bypassing maxPNGChunkSize/maxWebPChunkSize guards (positive values) and then treated as zero-length. Wrong but not a crash.
- **Platform**: Only affects 32-bit Go builds. Library currently targets arm64 (64-bit).
- **Fix**: Use explicit uint32 intermediate and check before int conversion: `if u32 > math.MaxInt32 { return error }`.

### FINDING-C005 — No fuzz targets for Inject paths (LOW, coverage gap)
- **Missing fuzz targets**: format/webp, format/heif, format/tiff, format/jpeg all lack FuzzXxxInject targets.
- **Impact**: Inject paths have no fuzz coverage. FINDING-C001 was discovered manually, not by fuzzer.
- **Fix**: Add FuzzWebPInject, FuzzHEIFInject, FuzzTIFFInject (at minimum) with seeds from real camera images.

## Tooling Results
- govulncheck: PASS (0 vulnerabilities in called code)
- go vet: PASS
- go test -race ./format/... ./internal/...: ALL PASS
- Fuzz targets exercised (30s each): FuzzJPEGExtract, FuzzHEIFExtract, FuzzPNGExtract, FuzzWebPExtract, FuzzTIFFExtract, FuzzCR3Extract, FuzzCR2Extract, FuzzNEFExtract, FuzzARWExtract, FuzzDNGExtract, FuzzORFExtract, FuzzRW2Extract — 0 crashers

## Confirmed Safe Patterns
- JPEG readSegment scratch buffer lifecycle: SAFE (all data cloned before next iteration)
- JPEG skipFillBytes: bounded by file size, terminates on EOF
- JPEG extended XMP: GUID count (4) + per-GUID size (16MB) caps verified in place
- PNG decompression bomb: capped at 64MB via io.LimitReader
- PNG readLargeChunk: io.ReadAll + exact length check, stream-proportional allocation
- WebP readPaddedChunk: two-stage guard (size cap + stream-availability seek check)
- HEIF fast path: 64KB header window, no full-file read unless meta beyond window
- HEIF box traversal: depth-limited to 32 for recursive findBox
- HEIF item payload: capped at 256MB (maxItemPayloadSize); offset overflow guard
- CR3/HEIF box header: size < headerLen guard prevents OOB slice
- HEIF iloc: offsetSize==0 guard at buildInjectComponents:163 prevents degenerate all-zero case
- iobuf pool: Put discards buffers with cap > largeSize (65536), preventing pool contamination

## Known Open Condition (from prior audit, CONFIRMED STILL PRESENT)
- Uncapped io.ReadAll in tiff.Extract, heif.Extract (slow path), heif.Inject, cr3.Extract, cr3.Inject, webp.Inject, orf.Extract, orf.Inject, rw2.Extract, rw2.Inject — LOW severity, pre-existing.
