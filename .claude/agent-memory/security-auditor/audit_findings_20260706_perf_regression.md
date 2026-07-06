# Audit 2026-07-06 — Post-v1.2.0 performance-commit regression hunt

Scope: the 7 perf commits landed AFTER the v1.2.0 security clearance (1c3b7a6).
Mandate: hunt for memory-safety / data-race / correctness bugs those commits INTRODUCED.
Auditor: security-researcher. Baseline (build/vet/govulncheck/test-race) already green.

Commits: #198 400dbf8 (lazy sub-IFD arena) | #199 88ee467 (byteOrder 1-byte flag) |
#200 0622090 (deferred parseWarn) | #201 12acaae (AppendByteOrder, kill stack-array escapes) |
#202 b79817b (zero-alloc string([]byte) MakerNote map key) | #203 8fae811 (magicPool in Detect) |
#240 6cf3462 (entrySlicePool in encode).

## VERDICT: SOUND — 0 memory-safety / data-race / correctness regressions. 1 LOW (API-misuse only). PRODUCTION-READY.

---

## Finding PERF-201-LOW — writeTIFFHeader/writeIFD panic on non-standard binary.ByteOrder
- Severity: LOW (CWE-704 improper type conversion; not reachable from untrusted input).
- Location: exif/write.go:214 (writeTIFFHeader), exif/ifd.go ~2065 (writeIFD): `appOrd := order.(binary.AppendByteOrder)` — a NON-comma-ok assertion.
- Regression: pre-#201 used `order.PutUint16(...)`, which works for ANY binary.ByteOrder.
  #201 replaced it with a hard type assertion that PANICS if `order` does not implement
  the (separate) binary.AppendByteOrder interface.
- Reachability: NOT attacker-reachable. e.ByteOrder is only ever binary.LittleEndian/BigEndian
  (set by parseByteOrder; serialise defaults nil→LittleEndian at write.go:118-120). Both singletons
  implement AppendByteOrder. NO custom ByteOrder type exists anywhere in the module (grep-verified:
  no NativeEndian, no user ByteOrder impls; internal/ has no byteorder pkg). The panic requires a Go
  caller to hand-construct `&EXIF{ByteOrder: <custom impl not implementing AppendByteOrder>}` and call
  Encode — pure API-misuse, same class as prior LOW EXIF-A003. Attacker cannot inject a Go type.
- Remediation (defense-in-depth, optional): comma-ok assertion with fallback:
  `if ao, ok := order.(binary.AppendByteOrder); ok { ...Append... } else { PutUint16 into scratch }`,
  or normalise order to a singleton at the serialise boundary.

---

## What was verified CLEAN and HOW

### #198 lazy sub-IFD arena (400dbf8) — SOUND (deepest analysis)
Memory-safe by construction, three invariants:
  1. parseArena.allocEntries advances entryN monotonically and returns `entryBatch[lo:lo:lo+count]`
     (cap-clamped). Two live IFDs can NEVER alias the same entry region (disjoint [lo,lo+count)).
  2. Cap-clamp => any append/slices.Insert/dedup beyond the clamped cap reallocates OUTSIDE the arena;
     writes at index<cap stay in the IFD's OWN reserved region, never a neighbour's.
  3. parseSingleIFDInto RE-READS the real entry count from the buffer; the pre-scan hint only affects
     allocation sizing, never parsed values.
- HINT-MISALIGNMENT is possible (scanSubIFDs records interop hint only for cnt==1 && Long/Short;
  parseExifSubIFDs traverses interop with no type check; sub-IFD next-chains aren't pre-counted). I
  proved via a hand-built TIFF (ExifIFD carrying a 0xA005 pointer typed SLONG so scanSubIFDs skips it,
  forcing InteropIFD to consume GPS's hint slot) that misalignment degrades ONLY to reallocation/
  fallback — every sub-IFD still decodes its exact bytes. Verified: single + 32g×200 concurrent (-race)
  + post-parse set() growth mutation. No corruption, no race, no panic.

### #199 byteOrder 1-byte flag (88ee467) — SOUND
- `isBig := order == binary.BigEndian` (identity on singletons) — order() returns the matching
  singleton. Behaviour-identical to the old interface for all parse-produced entries.
- Zero-value flag (false=LE): this FIXES prior LOW EXIF-A003 (old nil-interface panic on
  programmatically-built entries). A bool is always valid — strictly a robustness improvement, not a
  regression. Not memory-unsafe. set() byte-order inheritance logic (Entries[0].bigEndian, else false)
  is spirit-identical to pre-#199. TestIFDEntryOrder_ZeroValue + all conformance gates pass.

### #200 deferred parseWarn (0622090) — SOUND
- parseWarn captures ONLY scalar uint32 (offsets/counts/tags/buflen). NO []byte / pointer captured =>
  "deferred stringify reflects mutated/aliased bytes" hypothesis DISPROVEN. warnString renders into a
  256-byte stack buffer (max message << 256; even on realloc, string(b) copies out — no corruption).
  Byte-identical text locked by TestParseWarnMessageLock.

### #201 AppendByteOrder (12acaae) — SOUND except PERF-201-LOW above
- Stack arrays hdr/countB/nextB no longer taken by address across a call boundary; output is built by
  append directly into `out`. No slice of a stack array escapes/is retained. Output copied correctly.

### #202 zero-alloc string([]byte) MakerNote key (b79817b) — SOUND
- `makerNoteParsers[string(raw)]` is compiler mapaccess_faststr (read-only, no alloc, no alias). Map
  VALUE is a func; called with (makerNote, parentOrder) — NOT raw. raw is transient, never retained,
  never mutated during lookup. No unsafe. Trim semantics byte-equivalent (locked by regression gates).

### #203 magicPool in Detect (8fae811) — SOUND
- detectMagic(bp[:n]) respects n; ALL is* predicates guard len(b)>=X on b=bp[:n], so stale bytes
  beyond n are NEVER inspected. FormatID is a scalar (no alias to bp). Each path Puts exactly once,
  no use-after-Put, no double-Put (error path Puts+returns; success Puts before seek/refine).
- Proved: 64g×1000 concurrent Detect across 5 formats of differing magic length => 0 cross-
  contamination; 2000× (WebP-12B then 4B-unknown) => stale WebP residue never leaks past n (-race).
- NOTE (pre-existing, OUT OF SCOPE): Detect does a SINGLE r.Read (not io.ReadFull); a short-reading
  reader (<magic-len bytes in first Read) misdetects. This is UNCHANGED by #203 (old code identical)
  and not a regression. Real callers (os.File/bytes.Reader) fill in one Read.

### #240 entrySlicePool in encode (6cf3462) — SOUND
- getEntrySlice/putEntrySlice paired via defer in serialise (both Gets after BigTIFF early-return =>
  each pooled ptr Put exactly once). ifd0Ptr != exifPtr (two distinct pool Gets). build*Entries sync
  `*scratch = entries` before return => putEntrySlice's clear() zeroes the LIVE elements (releases
  Value aliases into caller data). Backing >128 entries discarded (no unbounded pool growth). Scratch
  never escapes serialise; encode output is a fresh []byte holding no IFDEntry structs. sync.Pool gives
  exclusive Get→Put ownership => no concurrent corruption. Existing TestTask240_ConcurrentEncode
  (20g×50, -race) + full suite pass.

## Empirical evidence
- go build ./... PASS; go vet ./exif/... ./format/... PASS.
- go test -race ./exif/... ./format/  PASS (full suites + all task198/199/240/Detect/MakerNote/
  ParseWarn gates).
- Adversarial probes (written, run under -race, then DELETED — source tree clean):
  arena hint-misalignment (single/32g-concurrent/post-set-growth), Detect pool cross-contamination
  (64g×1000), Detect stale-buffer isolation (2000×). ALL PASS.
- Fuzz: FuzzParseEXIF 61s / 1,274,135 execs / 0 crashers; FuzzTIFFExtract 61s / 362,111 execs /
  0 crashers (exercises Detect→refineTIFFVariant + TIFF parse).
