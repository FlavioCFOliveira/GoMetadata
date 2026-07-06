---
name: security-audit-batch-244-247
description: Four post-HEIF-panic security-audit findings fixed 2026-07-06 — DETECT-SHORTREAD-01, EXIF-BO-001, PERF-201-LOW, XMPCONC-01
metadata:
  type: project
---

Fixed four remaining findings from the 2026-07-06 security audit (rmp tasks #244-#247), the
batch that followed the CRITICAL HEIF iloc panic (see [[project_heif_iloc_offbyone_243]]).

**DETECT-SHORTREAD-01 (task #244, MEDIUM)** — `format/detect.go` `Detect()` used a single
`r.Read(bp[:])` call, which `io.Reader` explicitly permits to return fewer bytes than
requested even mid-stream. A chunking reader (network socket, io.Pipe, small bufio) could
misdetect a valid PNG/JPEG/WebP/ORF/RW2 as `FormatUnknown`. Fixed by switching to
`io.ReadFull`, treating `io.EOF`/`io.ErrUnexpectedEOF` as short-but-valid (still classify on
`bp[:n]`) and any other error as fatal — matching the established convention in
`format/heif/heif.go` (`errors.Is(rerr, io.ErrUnexpectedEOF)` check). Regression test
`TestDetectShortReads` in `format/detect_test.go` uses a `shortReader` (wraps `*bytes.Reader`,
returns ≤1 byte per `Read` call) across PNG/JPEG/WebP/ORF/RW2; confirmed it fails
(`FormatUnknown` for all 5) on the pre-fix code via `git stash` and passes after.

**EXIF-BO-001 (task #246, MEDIUM)** — `exif.EXIF.ifd0ByteOrder()` (introduced by task #199,
see [[project_task200_warn_defer]] era work) inferred byte order from
`e.IFD0.Entries[0].bigEndian`, which requires ≥1 IFD0 entry. A spec-legal big-endian TIFF with
an **empty** IFD0 (count==0) has no `Entries[0]`, so it silently fell back to
`binary.LittleEndian` even on a "MM" stream. All six numeric setters
(SetOrientation/SetGPS/SetExposureTime/SetFNumber/SetISO/SetFocalLength) then pre-encoded LE
value bytes into a structure `Encode` serialises as BE — e.g. a set Orientation of 6 (0x0006 LE
bytes) reads back as 1536 (0x0600 BE) after round-trip. Fixed by preferring the authoritative
`e.ByteOrder` (set unconditionally by every `Parse` path regardless of entry count — confirmed
via `grep -n "ByteOrder:" exif/exif.go`, both classic-TIFF and BigTIFF branches set it) and
falling back to `binary.LittleEndian` only when `e.ByteOrder == nil` (mirrors the identical
fallback already used in `write.go`'s `serialise()`, task #59). Regression test
`TestIFD0ByteOrderEmptyIFD0RoundTrip` in `exif/exif_test.go` (table-driven: empty-IFD0 case +
non-empty-IFD0 control) builds a synthetic BE TIFF via the existing `minimalTIFF` test helper,
exercises all six setters, checks the raw Orientation inline bytes directly with
`binary.BigEndian.Uint16` (pre-Encode proof), then Encodes + re-Parses and asserts every
accessor round-trips. Confirmed load-bearing via `git stash`.

**PERF-201-LOW (task #247, LOW, defense-in-depth)** — see [[feedback_append_byteorder_escape]]
for the full technical writeup. Summary: task #201's direct (non-comma-ok)
`order.(binary.AppendByteOrder)` assertion in `writeTIFFHeader` (write.go) and `writeIFD`
(ifd.go) panicked for any caller-supplied `binary.ByteOrder` that doesn't implement
`AppendByteOrder` — reachable via `&EXIF{ByteOrder: <custom>}` + `Encode`, not
attacker-reachable but a real public-API panic. Fixed with `appendUint16Order`/
`appendUint32Order` helper functions (write.go) using a comma-ok assertion + PutUint16-into-
scratch fallback. `benchstat` confirmed 0% delta on B/op and allocs/op for
`BenchmarkEXIFEncode`/`BenchmarkEXIFEncode_Camera` (80 B/2 allocs, 1618 B/14 allocs, unchanged) —
the CRITICAL constraint ("byte-identical and allocation-free" on the fast path) is satisfied.
There is a ~+3ns (~2-3%) ns/op cost because the wrapper function's inline cost (148-154) exceeds
Go's 80 budget, so it's a real (non-inlined) call frame on every append; tried splitting the
fallback into its own function to shrink the wrapper — made cost worse, reverted. Regression
test `TestEncodeCustomByteOrderWithoutAppend` in new file `exif/write_test.go` defines
`customByteOrderNoAppend` (implements `binary.ByteOrder` by delegating to
`binary.LittleEndian`, deliberately omits `Append*` methods), self-validates via a runtime
`any(...).(binary.AppendByteOrder)` guard that the premise still holds, then calls `Encode` and
asserts no panic. Confirmed load-bearing via `git stash` (pre-fix: panics with
"interface conversion: exif.customByteOrderNoAppend is not binary.AppendByteOrder").

**XMPCONC-01 (task #245, HIGH, documentation-only)** — no logic/ABI change. Two goroutines
calling `m.XMP.SetCaption` concurrently hit `fatal error: concurrent map writes` in
`xmp.XMP.putProp`; `exif.EXIF`'s Set* methods have the analogous unsynchronised-mutation
problem. `iptc.IPTC` already documented "not safe for concurrent use" on its `setUTF8IfNeeded`
method (iptc/iptc.go:569-571) — NOT on the `IPTC` type declaration itself (the task instructions
assumed the type but the actual comment lives on a private method; matched wording, not
location). Added a "Thread-safety: not safe for concurrent use" doc comment directly on the
`XMP` type (xmp/xmp.go) and the `EXIF` type (exif/exif.go), each naming the specific unsafe
write paths (`putProp`'s map writes for XMP; in-place IFD entry slice mutation for EXIF) and
pointing callers needing concurrency at `gometadata.Metadata`'s internal mutex. Strengthened
`Metadata`'s existing concurrency-contract doc comment (metadata.go) to state explicitly that
`m.EXIF`/`m.IPTC`/`m.XMP` are not independently safe for concurrent mutation and must go through
`Metadata.Set*` or caller-provided synchronisation. `doc.go` had no pre-existing concurrency
note, so left untouched per task instructions ("if it has a concurrency note, keep it
consistent; otherwise skip").

**Verification for the whole batch:** `gofmt -l` clean, `go vet ./...` clean, `go build ./...`
clean, `go test -race ./...` full PASS, `golangci-lint run ./...` 0 issues, `govulncheck ./...`
clean, `FuzzParseEXIF` 30s (1.38M execs, 0 crashes), `FuzzRead` 30s (1.40M execs, 0 crashes —
`FuzzRead` is the fuzz target that exercises `format.Detect` end-to-end via `gometadata.Read`;
there is no dedicated `FuzzDetect`). No scratch files; only the intended source/test diffs in
`exif/exif.go`, `exif/exif_test.go`, `exif/ifd.go`, `exif/write.go`, `exif/write_test.go` (new),
`format/detect.go`, `format/detect_test.go`, `metadata.go`, `xmp/xmp.go`.
