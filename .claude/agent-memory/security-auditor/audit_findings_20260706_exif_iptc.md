---
name: audit-findings-20260706-exif-iptc
description: 2026-07-06 fresh production-readiness audit of exif/ and iptc/ — 1 new confirmed finding (EXIF-BO-001, big-endian value-encoding corruption on empty-IFD0 write-back), all previously-fixed findings spot-checked CLOSED, fuzzers clean
metadata:
  type: project
---

## Audit 2026-07-06 — exif/ and iptc/ fresh sweep (post v1.2.0, post tasks #198-#240 perf work)

**Scope**: all `exif/*.go` and `iptc/*.go` (non-test), focused on untrusted-input
parser robustness and writer byte-correctness. Baseline (`go build`, `go vet`,
`govulncheck`, `go test -race ./...`) confirmed green before this session per
orchestrator; re-confirmed independently for exif/+iptc/ specifically:
`go build ./...` PASS, `go vet ./exif/... ./iptc/...` PASS, `govulncheck
./exif/... ./iptc/...` = no vulnerabilities, `go test -race ./exif/... ./iptc/...`
PASS.

### Spot-check of prior findings (all CONFIRMED CLOSED, not re-reported)
- EXIF-A001 (R-05 partial-IFD-recovery, was MEDIUM 2026-06-09): confirmed FIXED
  — `parseSingleIFD`/`parseSingleIFDInto` (exif/ifd.go) now clamp count to
  `available` and parse the entries that fit (`warnCountClamp`), matching
  ExifTool/libexif lenient behaviour. Shipped in commit 99bf5fc (#126/#129-132).
- IPTC-DUPL-1:00 (duplicate 1:00 EnvelopeRecordVersion, was LOW 2026-06-09):
  confirmed FIXED — `iptc/iptc.go` `Encode()` now tracks `emittedR1Version`
  and gates the main-loop Record-1 version injection on `!emittedR1Version`.
  Shipped in commit 9712915 (#146 #179).
- EXIF-A003 (ifd0ByteOrder nil-panic on manually-constructed entries, was LOW):
  confirmed FIXED differently than expected — task #199 replaced the
  `binary.ByteOrder` interface field with a `bool bigEndian` flag, so the
  zero value is well-defined (LE) instead of a nil-panic. Regression test
  `TestSetMakeOnManuallyConstructedEXIF` (exif_test.go:3731) locks this.
  NOTE: this same code path has a DIFFERENT, previously-unreported bug for a
  DIFFERENT trigger condition — see FINDING EXIF-BO-001 below.

### Fuzzing (this session)
- `FuzzParseEXIF` — 75s, 921,850 execs, 0 crashers, 12 new coverage-interesting
  corpus entries added by go test (harmless — coverage growth, not crashes).
- `FuzzParseIPTC` — 75s, 8,678,040 execs, 0 crashers, 2 new coverage-interesting
  corpus entries.
- No other `FuzzXxx` targets exist inside `exif/` or `iptc/` (confirmed via
  `grep -rn "^func Fuzz"`); MakerNote-specific fuzzing is exercised indirectly
  through `FuzzParseEXIF` (Make tag + MakerNote bytes reach every manufacturer
  dispatch branch via `makerNoteDispatch`).

### Perf-refactor review (tasks #198 arena, #199 byteorder-flag, #200 deferred
warnings, #201 stack-escape fix, #202 zero-alloc MakerNote dispatch, #240
entry pool) — reviewed for regressions introduced after the 2026-06-09
clearance; empirically probed two adversarial hypotheses, both DISPROVEN safe:

1. **Arena hint/slot skew** (task #198): `scanSubIFDs`'s `goto gpsSection`
   path can skip recording a hint for ExifIFD (when the buffer has no room
   for even one entry), causing `traverseWithArena`'s hint index to desync
   from `ifdBatch`/`entryBatch` slot allocation for later sub-IFDs (GPS/
   Interop). Built a hand-crafted classic-TIFF probe
   (`TestZZProbeArenaHintSkew`, deleted after use) forcing exactly this skew.
   Result: **safe** — the `arena.ifdN--`/entry-exhaustion fallback paths in
   `traverseWithArena` correctly detect the mismatch and fall back to
   individual (non-arena) allocation; final parsed IFD contents were fully
   correct (ExifIFD nil as expected from truncation, GPSIFD correctly
   populated). Cap-clamped sub-slices (`s[:0:count]`) guarantee any
   append-beyond-cap reallocates outside the arena, so a hint mismatch can
   only cost an extra allocation, never corrupt a sibling IFD's entries.
   **Not a finding** — arena design is memory-safe by construction regardless
   of hint accuracy. (Note: a second, concurrently-running agent — visible via
   a stray `exif/zzz_probe_test.go` left in the working tree, not authored by
   this audit — was independently probing the same class of bug via an
   InteropIFD/SLONG-type hint-skip variant; consistent with this conclusion.)
2. **MakerNote dispatch trim-order equivalence** (task #202): verified
   `makerNoteDispatch`'s `bytes.TrimRight(NUL)` → `bytes.TrimSpace` byte-for-byte
   matches the old `entry.String()` → `strings.TrimSpace(cameraMake)` path,
   including the interior-NUL-then-space corner case (identical on both old
   and new paths — pre-existing behaviour, not a regression).
3. `writeIFD`/`writeTIFFHeader` (task #201) stack-array-to-append rewrite and
   `entrySlicePool`/`iobuf` pooling (task #240): traced buffer lifetimes —
   `clear()` before every `Put`, single-owner `defer put...` discipline,
   cap-based discard thresholds. No use-after-Put, no stale-byte leak across
   Encode calls, no aliasing between the two pooled entry slices (`ifd0Ptr`,
   `exifPtr`) obtained per `serialise()` call.

### NEW FINDING

#### FINDING EXIF-BO-001 — Big-endian numeric values silently mis-encoded as little-endian when IFD0 has zero entries at Set*-call time — MEDIUM

**Location**: `exif/exif.go:768-773` (`ifd0ByteOrder`), consumed by
`SetOrientation` (:822), `SetGPS`→`decimalToDMSBytes` (:1023,:1041-1042),
`SetExposureTime` (:870), `SetFNumber` (:884), `SetISO` (:899),
`SetFocalLength` (:915), `SetImageSize` (:941). Root helper:

```go
func (e *EXIF) ifd0ByteOrder() binary.ByteOrder {
    if len(e.IFD0.Entries) > 0 && e.IFD0.Entries[0].bigEndian {
        return binary.BigEndian
    }
    return binary.LittleEndian
}
```

**Vulnerability class**: CWE-198 (Use of Incorrect Byte Ordering) / write-path
data-integrity violation (CLAUDE.md constraint #5: "Write operations must...
Maintain byte-level correctness").

**Root cause**: `ifd0ByteOrder()` infers the stream's byte order from
`IFD0.Entries[0].bigEndian` instead of using `e.ByteOrder` (which `Parse`
*always* sets correctly from the TIFF header's "II"/"MM" marker, regardless
of how many entries IFD0 has). When `e.IFD0.Entries` is empty — a rare but
**spec-legal** degenerate IFD (TIFF §2 permits an IFD with `count == 0`) —
the `len(...) > 0` guard is false and the function unconditionally returns
`binary.LittleEndian`, even for a stream whose header declared "MM"
(big-endian). Any of the six numeric `Set*` methods listed above then
pre-encodes its multi-byte value using this wrong (LE) order into
`IFDEntry.Value`, while `Encode`/`serialise` (write.go) correctly uses
`e.ByteOrder` (BE) for the TIFF header, every entry's tag/type/count fields,
and all pointer-patch operations (`patchPointers`) — but never re-encodes
`IFDEntry.Value` bytes, since `writeIFD` copies them verbatim
(`copy(entryBuf[p+8:p+12], e.Value)` / `valueArea = append(valueArea,
e.Value...)`). The result is a **self-inconsistent output stream**: the
header and structural fields say big-endian, but the just-written numeric
value bytes are little-endian.

This is distinct from the earlier EXIF-A003/#189 finding, which covered a
*manually constructed* EXIF struct with a byteOrder-less entry (fixed by
task #199's bool-flag zero-value redesign, and locked by
`TestSetMakeOnManuallyConstructedEXIF`). That regression test only exercises
`SetMake` (ASCII, byte-order-independent) and `SetOrientation` on a
manually-built struct, and does not cover a **Parse-produced** EXIF from a
real big-endian stream with a legitimately empty IFD0 — the trigger
identified here.

**Trigger condition**: (1) input TIFF/EXIF stream declares big-endian
("MM") in its 8-byte header, AND (2) IFD0 at the declared offset has
`count == 0` entries (a valid but unusual/degenerate IFD), AND (3) caller
code calls any of `SetOrientation`, `SetGPS`, `SetExposureTime`,
`SetFNumber`, `SetISO`, `SetFocalLength`, or `SetImageSize` on the parsed
`*EXIF`, then calls `Encode`. (ASCII-only setters — `SetCameraModel`,
`SetCaption`, `SetCopyright`, `SetCreator`, `SetMake`, `SetDateTimeOriginal`
— are unaffected; ASCII bytes have no endianness.) Calling `ensureExifIFD()`
first (as `SetExposureTime`/`SetFNumber`/`SetISO`/`SetFocalLength`/
`SetImageSize` do) does **not** avoid the bug: the placeholder
`TagExifIFDPointer` entry it inserts into the still-empty `IFD0.Entries` is
itself created via `IFD.set()`, which computes its own `isBig` flag from
`ifd.Entries[0].bigEndian` *before* insertion (i.e., from the still-empty
slice) — so the freshly-inserted entry also gets `bigEndian=false`, and the
very next `ifd0ByteOrder()` call still returns LE.

**PoC** (constructed and run as a temporary Go test, then deleted — not
committed): an 10-byte classic BigTIFF-free "MM" TIFF header with IFD0 at
offset 8 declaring `count=0`:

```
4D 4D 00 2A 00 00 00 08   // "MM", magic 0x002A, IFD0 offset = 8
00 00                     // IFD0 count = 0
```//nolint
```go
e, _ := exif.Parse(buf)      // e.ByteOrder == binary.BigEndian, e.IFD0.Entries == []
e.SetOrientation(6)
out, _ := exif.Encode(e)
e2, _ := exif.Parse(out)     // e2.ByteOrder == binary.BigEndian (header preserved)
v := e2.IFD0.Get(exif.TagOrientation).Value   // == []byte{6, 0}
binary.BigEndian.Uint16(v)     // == 1536  (WRONG — spec-compliant BE reader sees garbage)
binary.LittleEndian.Uint16(v)  // == 6     (the value we actually set)
```
Empirically confirmed: `BE-decoded = 1536, LE-decoded = 6 (we set
Orientation=6)` — `CORRUPTION CONFIRMED`. A control test with the same
header but IFD0 carrying one real entry (typical real-world file) round-trips
correctly (`BE-decoded = 6`), confirming the bug is scoped exactly to the
empty-IFD0 edge case and is not a general regression.

**Impact**: Silent metadata corruption on write-back for big-endian sources
with an empty IFD0. Concretely: `SetGPS` would write geotag coordinates
whose RATIONAL numerator/denominator bytes are byte-swapped relative to the
file's declared order — a compliant reader (ExifTool, libexif, Exiv2, or any
other DC-008-conformant decoder) would compute wildly wrong or nonsensical
GPS coordinates from the corrupted rationals; `SetOrientation` would
misrender image rotation; `SetExposureTime`/`SetFNumber`/`SetISO`/
`SetFocalLength`/`SetImageSize` would all read back as wrong numeric values.
No crash, no memory-safety impact — this is a data-integrity violation, not
a memory-corruption bug. Real-world exploitability is limited by how often a
genuinely empty-IFD0 big-endian file is encountered (uncommon but spec-legal;
trivially craftable by an adversary who specifically wants to sabotage a
downstream metadata-editing pipeline built on this library, e.g. to inject
false GPS coordinates that silently survive a "read → add caption/geotag →
write" workflow).

**Exploitability**: Confirmed (concrete reproducer above; probe test passed
under `go test -race`, no panic — purely a silent correctness violation).

**Remediation** (implementation to be delegated to `go-performance-architect`):
Change `ifd0ByteOrder()` to prefer `e.ByteOrder` (which `Parse` always sets
correctly, independent of entry count) and fall back to
`binary.LittleEndian` only when `e.ByteOrder` itself is nil (the
freshly-constructed-by-caller case, which is the only scenario the current
entries-based heuristic actually needs to serve):

```go
func (e *EXIF) ifd0ByteOrder() binary.ByteOrder {
    if e.ByteOrder != nil {
        return e.ByteOrder
    }
    return binary.LittleEndian
}
```
This is a minimal, behaviour-preserving fix for every existing passing test
(freshly-constructed `&EXIF{}` still defaults to LE; any real `Parse` result
now always reflects the true stream order regardless of IFD0 entry count)
and eliminates the divergence between `ifd0ByteOrder()` and the `order`
parameter that `serialise`/`writeIFD`/`patchPointers` already use correctly.
No change to `IFD.set()`'s own `isBig`-inheritance logic is needed once this
fix lands, because `ensureExifIFD`'s placeholder insertion will no longer
matter for byte-order purposes (the numeric setters compute their value
bytes using the corrected `ifd0ByteOrder()`, not by inspecting `IFD0.Entries`).

**Suggested regression test**: a table-driven test parsing a synthetic
big-endian TIFF with `IFD0` `count == 0` (as in the PoC), calling each of the
six affected `Set*` methods in turn, re-encoding, and asserting that
`binary.BigEndian.Uint16/Uint32` decodes the written value bytes to the
exact value that was set (not the byte-swapped value). Add alongside the
existing `TestSetMakeOnManuallyConstructedEXIF` (exif_test.go) under the
"Audit finding #189" section, cross-referencing this finding so the two
scenarios (manually-constructed vs. Parse-produced-with-empty-IFD0) are both
covered going forward.

### Other areas reviewed and confirmed CLEAN (no new findings)
- `exif/gps.go`: `decodeCoordinate`/`dmsToDecimal`/`parseGPS` — TypeSRational
  rejection (#119), div-by-zero guard, WGS-84 range validation all intact.
- `exif/charset.go`: `decodeUserComment`/`decodeUTF16`/`sanitiseUTF8` — no
  OOB, no allocation bombs, UTF-8 sanitisation always produces valid output.
- `exif/type.go`, `exif/tag.go`: static registries, no untrusted-input
  interaction.
- `exif/ifd.go` accessor methods (`Rational`, `SRational`, `Uint16`, `Uint32`,
  `Int16`, `Int32`, `Float32`, `Float64`) — negative-index guards (#87) intact;
  all bounds-checked against `len(e.Value)`, which is always exactly
  `Count*typeSize` for successfully-parsed entries (parseIFDEntry rejects any
  entry whose declared size would exceed the buffer before storing it), so
  index overflow via a crafted huge `Count` is not reachable.
- BigTIFF path (`parseIFDEntryBigTIFF`, `parseSingleIFDBigTIFF`,
  `fillIFDBigTIFF`, `traverseBigTIFF`): count caps (`bigTIFFMaxEntries`,
  `maxBigTIFFCount`), overflow-safe `count*sz` guard, cycle detection via
  pooled `map[uint64]bool` — all intact, no regressions from the perf tasks
  (BigTIFF path was untouched by #198/#199/#201/#202/#240 except the shared
  `bigEndian` flag rename, which is mechanically equivalent to the old
  interface field for this path since BigTIFF has no `ifd0ByteOrder()`
  dependency in its read-only traversal).
- `iptc/iptc.go` `Parse`/`storeDataset`/`decodeDatasetLength`: aggregate byte
  cap (256 MiB), per-dataset cap (1 MiB), dataset-count cap (65536, #71) all
  intact; extended-length decode rejects `nBytes` outside `[1,4]` and
  sign-bit overflow. `Encode`'s ascending-sort + single-1:00-emission logic
  (#146/#179) verified correct by direct code read (see spot-check above).
- `iptc/encoding.go`, `iptc/digest.go`: ISO-8859-1 pool reuse (`Reset()`
  before every use — no cross-call state leak), MD5 digest is spec-mandated
  (MWG §3.3.1) and not a security control (no timing-attack relevance for a
  content-integrity checksum, not an authentication token).
- `iptc/dataset.go`: static length table, no untrusted-input interaction.

### Noted but NOT reported as a finding (informational, out of practical reach)
`iptc/iptc.go` `Encode()`'s extended-length path
(`buf.WriteByte(byte(n >> 24))` etc., :398-401) truncates silently if a
single `Dataset.Value` exceeds 4 GiB (`math.MaxUint32`). This is unreachable
via `Parse`→`Encode` round-trips (Parse's 256 MiB aggregate / 1 MiB
per-dataset caps make it impossible to produce such a `Dataset` from
untrusted bytes) and would require a caller to directly construct a
multi-gigabyte `Dataset.Value` via the public struct fields — a scenario
requiring the caller to already hold gigabytes of attacker data in memory
outside this library's control. Documented here for completeness per the
audit brief's writer-path-integrity mandate, but not rated as an actionable
finding (INFO only, no realistic trigger from the stated threat model of
"untrusted image bytes").
