---
name: audit-findings-20260706-exif-iptc-followup
description: 2026-07-06 follow-up fresh audit of exif/+iptc/ at HEAD 48e6f20 (unchanged since 6a0bbc6) — 1 new MEDIUM finding EXIF-BO-002 (in-memory accessor byte-order corruption on fresh sub-IFDs, broader than the fixed EXIF-BO-001), all prior findings re-confirmed CLOSED, fuzzers clean
metadata:
  type: project
---

## Follow-up audit 2026-07-06 (same day, second pass) — exif/ and iptc/

HEAD 48e6f20, code in exif/ and iptc/ UNCHANGED since 6a0bbc6 (verified via
`git diff --stat 6a0bbc6..HEAD -- exif/ iptc/` = empty). This session
independently re-verified the two priority regressions and hunted for new
defects with fresh eyes; found ONE new MEDIUM finding that the original
EXIF-BO-001 fix (task #246) did not fully close.

### Priority regression checks — BOTH CONFIRMED STILL FIXED
- EXIF-BO-001: `exif/exif.go:790-795` `ifd0ByteOrder()` correctly prefers
  `e.ByteOrder`, falls back to LE only when nil. Confirmed via source read.
- PERF-201-LOW: `exif/write.go:181-199` `appendUint16Order`/`appendUint32Order`
  use the comma-ok `order.(binary.AppendByteOrder)` form with a `PutUint16`/
  `PutUint32`+append fallback — never panics. Confirmed via source read.

### NEW FINDING — EXIF-BO-002 (MEDIUM) — in-memory accessor byte-order
corruption on freshly-created sub-IFDs (GPSIFD/ExifIFD), broader than
EXIF-BO-001

**Root cause**: `IFD.set()` (exif/ifd.go:1825-1833) computes the new entry's
`bigEndian` flag by inheriting from `ifd.Entries[0].bigEndian` — but when the
target IFD is freshly created and empty (`len(ifd.Entries)==0`), this
defaults to `false` (LE) regardless of the true stream byte order. This is a
DIFFERENT code path from `ifd0ByteOrder()` (which the #246 fix corrected):
`ifd0ByteOrder()` only controls what byte order is used to **encode the
VALUE BYTES** passed into `.set()`; `IFD.set()`'s own `isBig` computation
independently controls the **entry's own `bigEndian` flag**, which is what
every public read accessor (`entry.Uint16()`, `.Uint32()`, `.Rational()`,
`.SRational()`, `.Int16()`, `.Int32()`, `.Float32()`, `.Float64()` — all via
`entry.order()`) uses to decode that entry's `Value` bytes back.

Consequence: the VALUE BYTES are now correctly BE-encoded (thanks to
`ifd0ByteOrder()`), but the entry's `bigEndian` FLAG is wrongly `false`,
so any **immediate in-memory read via the entry accessor or any public
`*EXIF` accessor method (`e.GPS()`, `e.ExposureTime()`, `e.FNumber()`,
`e.ISO()`, `e.FocalLength()`, `e.Orientation()`, `e.ImageSize()`) called on
the SAME struct BEFORE an intervening `Encode()`/`Parse()` round trip**
misdecodes the just-written value.

**This is broader than EXIF-BO-001**: EXIF-BO-001 only manifested when IFD0
itself was empty at Set-time (rare/degenerate). This finding manifests
whenever `e.GPSIFD` or `e.ExifIFD` is **freshly created** by
`ensureExifIFD()`/`SetGPS`'s nil-check — which is the ordinary, common case
(any BE-encoded photo that doesn't already carry GPS data, or that the
caller is adding exposure/GPS metadata to for the first time). `SetOrientation`
is unaffected in the common case because it targets IFD0 directly, which
usually already has real Parse-produced entries with the correct flag — only
degenerate empty-IFD0 BE files hit it (the original narrow case).

**PoC (empirically confirmed, three variants, run as scratch tests then
deleted — not committed)**:
1. Hand-built empty-IFD0 BE `*EXIF`, `SetOrientation(6)`, then
   `entry.Uint16()` (not `Encode`) → returns 1536, not 6.
2. Hand-built BE `*EXIF` with a REALISTIC non-empty, correctly-flagged IFD0
   (Make/Model entries, `bigEndian:true`), no pre-existing GPSIFD/ExifIFD:
   `SetGPS(38.7223,-9.1393)` → `gpsEntry.Rational(0)` returns
   `637534208/16777216` instead of `38/1`; `SetExposureTime(1,200)` →
   `expEntry.Rational(0)` returns `16777216/3355443200` instead of `1/200`.
3. **Public-API-only reproduction** (the most important one): real
   `exif.Parse(bigEndianTIFFBytes)` (non-empty IFD0, via `minimalTIFF`
   helper) → `e.SetGPS(38.7223, -9.1393)` → `e.GPS()` (same object, no
   `Encode()` call) returns `(38.7175, -9.1336, true)` — silently wrong
   coordinates (~0.7 km error), not `(38.7223, -9.1393, true)`.
   `e.SetExposureTime(1,200)` → `e.ExposureTime()` returns `(16777216,
   3355443200, true)` instead of `(1, 200, true)`.
   Encoding the struct via `Encode()` and re-`Parse()`-ing DOES produce the
   correct value (confirmed) — the corruption is confined to the in-memory,
   pre-Encode read path.

**Attacker reachability**: YES, via the public API, using an entirely
spec-compliant (not even malformed) big-endian EXIF/TIFF file. Trigger:
`exif.Parse()` a BE source → call any of `SetGPS`/`SetExposureTime`/
`SetFNumber`/`SetISO`/`SetFocalLength`/`SetImageSize` (ExifIFD/GPSIFD
targets) → read back via the corresponding public accessor without calling
`Encode()` first. A very plausible real-world pattern (e.g. "add geotag,
immediately display/confirm to user, then save"). `Encode()`'s persisted
output is unaffected (confirmed correct) — this is a read-before-persist
data-integrity bug, not a write-path/on-disk corruption bug.

**CWE**: CWE-198 (Use of Incorrect Byte Ordering), same class as
EXIF-BO-001. **Confidence**: CONFIRMED (three independent PoCs, including
one that exercises only public `exif.Parse`/`Set*`/accessor API).

**Severity rationale (MEDIUM)**: No memory-safety impact, no crash, no DoS.
Purely a silent-corruption / data-integrity issue, scoped to BE-encoded
sources and to reads performed between a `Set*` call and `Encode()`. Real
persisted files are unaffected. Rated MEDIUM (same tier as the original
EXIF-BO-001) because it is easier to trigger (no degenerate empty-IFD0
needed) but the practical blast radius (in-memory-only, pre-persist) is
narrower than a genuine on-disk corruption would be.

**Remediation direction** (for go-performance-architect, not implemented
here): `IFD.set()` cannot itself know the true stream order — it only has
`*IFD`, not `*EXIF`. Two viable approaches: (a) thread the correct
`bigEndian bool` (derived from `e.ifd0ByteOrder()` at each call site) as an
explicit parameter into `set()`, replacing the `Entries[0]`-inheritance
heuristic entirely — this is the more surgical fix and matches how
`buildIFD0Entries`/`buildExifIFDEntries` in write.go already compute
`isBig := order == binary.BigEndian` locally; or (b) store the byte order at
the `*IFD` level (a new field, e.g. `IFD.bigEndian bool`, set once when the
IFD is created/first populated from `e.ifd0ByteOrder()`) and have `set()`
read from `ifd.bigEndian` instead of `Entries[0]`. Approach (a) requires
updating all ~19 `.set()` call sites in exif.go to pass the order explicitly
(most already compute `order := e.ifd0ByteOrder()` for the value-byte
encoding, so this is mostly plumbing an existing local variable one
parameter further). Approach (b) is more centralized but requires deciding
where GPSIFD/ExifIFD's `bigEndian` gets initialized (at
`ensureExifIFD()`/`SetGPS`'s nil-check, from `e.ifd0ByteOrder()`).

**Suggested regression test**: extend
`TestIFD0ByteOrderEmptyIFD0RoundTrip` (exif_test.go) — or add a sibling
test — that calls the public accessor (`e.GPS()`, `e.ExposureTime()`,
`e.FNumber()`, `e.ISO()`, `e.FocalLength()`, `e.Orientation()`) on the
SAME `*EXIF` object immediately after the corresponding `Set*` call,
BEFORE any `Encode()`/`Parse()` round trip, for both the empty-IFD0 case
and — critically — the realistic non-empty-IFD0-but-fresh-GPSIFD/ExifIFD
case (variant 2/3 above), asserting the accessor returns the value that was
just set. The existing test only checks post-round-trip values and raw
`binary.BigEndian.Uint16(ori.Value)` byte inspection, which is why this gap
survived the #246 fix.

### Other spot-checks this session (all CONFIRMED CLOSED, re-verified by
direct source read, not just memory recall)
- IPTC duplicate 1:00 (`iptc/iptc.go` `Encode()` `emittedR1Version` gate,
  lines 315-343 / 370-381): confirmed present and correctly gates the
  main-loop Record-1 version injection.
- IPTC-002 zero-length dataset bomb: confirmed the `maxIPTCDatasets` (65536)
  cap in `storeDataset` (iptc/iptc.go:121-128) still bounds the struct-count
  amplification attack regardless of per-dataset value length.
- `decodeDatasetLength` (iptc/iptc.go:62-88): extended-length bounds
  (`nBytes` in [1,4], `newPos+nBytes<=len(b)`, sign-bit rejection) all intact.
- `ifdTotalSize` (exif/ifd.go:1988+) uint64-accumulation + MaxUint32
  saturation guard against manually-constructed extreme `Count` values:
  confirmed intact, not reachable via Parse (Parse-bounded entries can never
  produce an overflowing total since OOL end-offset is bounds-checked
  against the input buffer at parse time).
- `IFDEntry.Rational(i)`/`SRational(i)` negative-index guard (task #87):
  confirmed intact (`if i < 0 { return zero }` before `off := i*8`).
  Positive-huge-`i` int-overflow theoretically possible on `off := i*8` but
  requires `i` near `math.MaxInt/8` — not attacker-reachable (only 3
  internal call sites, all with constant small `i` in {0,1,2}; `i` is a
  caller-supplied Go int, not derived from parsed bytes) — same
  API-misuse-only class as the already-triaged EXIF-A003/PERF-201-LOW.
- MakerNote dispatch (`exif/makernote_parse.go`, all 19 manufacturer
  parsers): every path bottoms out in `traverse()`, which has full
  bounds-checking, cycle detection (pooled `visited` map), and lenient
  count-clamping identical to the main IFD traversal. No new OOB found.
- `traverse`/`traverseWithArena` cycle detection and arena fallback paths
  (exif/ifd.go): re-read in full; safe by construction (as previously
  certified in the 2026-07-06 perf-regression audit — no changes since).
- `write.go` `serialise`/`writeIFD`/`patchPointers`/`computeIFDOffsets`:
  re-read in full; `writeIFD` always uses the `order` PARAMETER (never
  `entry.bigEndian`) to write TIFF structural fields — this is why
  EXIF-BO-001's persisted-output fix is genuinely complete for `Encode()`
  even though EXIF-BO-002 (this finding) affects the separate accessor
  read path.

### Tooling / fuzzing this session
- `go build ./exif/... ./iptc/...` — clean.
- `go vet ./exif/... ./iptc/...` — clean.
- `govulncheck ./exif/... ./iptc/...` — no vulnerabilities found.
- `go test -race ./exif/... ./iptc/...` — PASS.
- `FuzzParseEXIF` 91s / 2,222,634 execs / 0 crashers / 0 new corpus files
  persisted (working tree clean after run).
- `FuzzParseIPTC` 91s / 16,700,699 execs / 0 crashers / 0 new corpus files
  persisted.
- All scratch probe test files were written under
  `/private/tmp/.../scratchpad/` and briefly copied into `exif/` to compile
  against unexported internals, then deleted before concluding — working
  tree verified clean (`git status --short exif/ iptc/` empty) and
  `go build`/`go test` re-confirmed green on the real (unmodified) tree.

### Verdict
FINDINGS PRESENT: 1 new MEDIUM (EXIF-BO-002). No CRITICAL/HIGH. Both
priority regressions (EXIF-BO-001, PERF-201-LOW) reconfirmed fixed. Not a
blocking condition for the two originally-audited regressions, but the
EXIF-BO-001 fix (task #246) should be considered incomplete until
EXIF-BO-002 is also addressed, since they share the same conceptual defect
(byte-order flag desynchronised from `e.ByteOrder` on fresh/empty IFDs) and
a reasonable engineer fixing #246 would expect the accessor path to be
covered too.
