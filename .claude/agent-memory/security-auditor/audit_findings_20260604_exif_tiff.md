---
name: audit-findings-20260604-exif-tiff
description: EXIF/TIFF core robustness audit findings from 2026-06-04 — 4 confirmed findings
metadata:
  type: project
---

# EXIF/TIFF Robustness Audit — 2026-06-04

## Scope
exif/exif.go, exif/ifd.go, exif/tag.go, exif/type.go, exif/gps.go, exif/charset.go, exif/makernote_parse.go, exif/errors.go, exif/write.go, format/tiff/tiff.go, format/tiff/errors.go, internal/byteorder/byteorder.go

## Tooling
- govulncheck: PASS (0 symbol-level findings)
- go vet: PASS
- go test -race: PASS (all packages)
- FuzzParseEXIF (30s): 3.2M execs, 0 crashers
- FuzzTIFFExtract (30s): 324K execs, 0 crashers

## Confirmed Findings

### FINDING-EXIF-01 — HIGH
**exif.Encode panics with nil ByteOrder**
- Location: exif/write.go:304 (patchPointers) and exif/write.go:67 (writeTIFFHeader)
- Trigger: construct `&exif.EXIF{IFD0: &exif.IFD{}}`, call any setter (SetGPS, SetCameraModel, etc.), then call exif.Encode
- Root cause: serialise() uses e.ByteOrder directly without nil guard; nil interface method call panics
- Fix: In serialise(), if e.ByteOrder == nil, default to binary.LittleEndian
- Status: OPEN

### FINDING-EXIF-02 — HIGH
**exif.Encode panics with nil IFD0**
- Location: exif/write.go:121 (writeSubIFDs — `for ifd := e.IFD0.Next`)
- Trigger: construct `&exif.EXIF{ByteOrder: binary.LittleEndian}` with nil IFD0, call Encode
- Root cause: writeSubIFDs dereferences e.IFD0 without nil check (writeTIFFHeader has the check but writeSubIFDs does not)
- Fix: guard `if e.IFD0 != nil {` around the IFD1 chain loop in writeSubIFDs
- Status: OPEN

### FINDING-TIFF-01 — MEDIUM
**tiff.upsertIFD0Entry breaks IFD sort invariant -> duplicate IFD pointer tags**
- Location: format/tiff/tiff.go:133-147 (upsertIFD0Entry) + exif/ifd.go:443-469 (filterEntries anyPresent fast-path)
- Trigger: tiff.Inject on a TIFF with IFD0 = [ImageWidth(256), ImageLength(272), IPTC(33723), ExifIFDPointer(34665)] plus rawXMP payload. upsertIFD0Entry appends XMP(700) at end of sorted slice. filterEntries binary search for 34665 probes index 4 (value 700) which is < 34665, making lo=5 (out of bounds). anyPresent=false -> fast path returns full copy WITH existing ExifIFDPointer. buildIFD0Entries then appends a new ExifIFDPointer placeholder -> duplicate.
- Impact: Encoded TIFF has two ExifIFDPointer (0x8769) entries. First has correct offset (patched by patchPointers via binary search). Second has 0 (stale placeholder). TIFF 6.0 §7 spec violation. Strict TIFF readers may reject the file.
- Confirmed by test: go run /tmp/test_iptc_width_length.go produced 6 IFD0 entries with DUPLICATE TAG: 0x8769 x2
- Fix: Replace upsertIFD0Entry with a call to IFD.set() which maintains sort invariant via binary search insertion
- Status: OPEN

### FINDING-EXIF-03 — LOW
**SetGPS(NaN, NaN) silently stores (0,0) and GPS() returns ok=true**
- Location: exif/exif.go:664 (decimalToDMSBytes) and exif/gps.go:66 (parseGPS)
- Trigger: call e.SetGPS(math.NaN(), math.NaN()); e.GPS() returns (0, 0, true)
- Root cause: Go converts NaN to 0 for uint32 conversion (undefined but deterministic on arm64). The zero DMS coordinates pass the valid WGS-84 range check in parseGPS (-90 <= 0 <= 90, -180 <= 0 <= 180).
- Impact: Silently wrong GPS data stored in EXIF with no indication of error. SetGPS has no error return.
- Fix: Add NaN/Inf input validation at the top of decimalToDMSBytes (or SetGPS), returning early if coord is NaN or Inf.
- Status: OPEN

## Areas Confirmed Safe
- IFD cycle detection: traverse() visited-set approach correctly prevents infinite loops and cyclic chains
- Integer overflow in offset math: uint64 guards prevent overflow for all sizes up to buffer bounds
- MakerNote bounds: all sub-parsers validate minimum length; traverse guards all offset accesses
- iobuf pool: no aliasing, defer cleanup, stale-byte prevention via clear()
- GPS denominator-zero: dmsToDecimal guards all three denominators
- visitedPool concurrency: sync.Pool is goroutine-safe; map cleared before return
- MakerNote recursion: fixed 2-level depth (Parse -> parseExifSubIFDs -> traverse); no unbounded recursion
- writeIFD value area: entries[i].Value written verbatim after pool buffer is appended to out (copy happens before Put)

**Why:** Root cause of each finding is confirmed by PoC test execution.
**How to apply:** Report these as new findings if asked to audit; do not re-surface as novel if already fixed.
