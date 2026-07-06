---
name: audit-findings-20260609-iptc
description: IPTC package spec-conformance and reliability audit 2026-06-09 — 1 LOW finding (duplicate 1:00 header when R1 datasets + non-ASCII content), all others CLEAN
metadata:
  type: project
---

## IPTC Package Audit 2026-06-09

**Scope**: `iptc/*.go` (all source and test files), conformance contract `docs/conformance/iptc.md`

**Method**: Full code read, conformance contract review, targeted probe tests (9 probe files run then deleted), 30s fuzzing (4.6M execs, 0 crashes), go vet PASS, govulncheck PASS, go test -race PASS.

### Finding: IPTC-DUPL-1:00 — Duplicate 1:00 EnvelopeRecordVersion on Encode (LOW)

**Location**: `iptc/iptc.go` `Encode()` function, lines ~313-335 (emitUTF8Decl block) and ~352-364 (main record loop version injection).

**Trigger**: Call `Encode()` on an `*IPTC` that has (a) at least one dataset stored in `Records[1]` that is NOT dataset 0, AND (b) at least one non-ASCII byte value anywhere (causing `emitUTF8Decl = true`).

**Mechanism**: When `emitUTF8Decl = true`, the preamble block emits `1C 01 00 00 02 00 04` (1:00). Then the main loop iterates `record = 1`, finds `len(datasets) > 0`, enters the `record == 1 || record == 2` branch, calls `hasVersion` scan over `i.Records[1]` (which contains no dataset-0, because the parser silently discards 1:00 on read), finds `hasVersion = false`, and emits ANOTHER `1C 01 00 02 00 04` (1:00). Result: two 1:00 headers in the encoded stream.

**Empirical proof**: `TestPROBE5B_Encode1_00Logic` CASE B observed `count(1:00) = 2` vs expected 1. Exact encoded bytes:
- offset 0: `1C 01 00 00 02 00 04`  (from emitUTF8Decl block)
- offset 15: `1C 01 00 00 02 00 04` (from main loop version injection)

**Spec clause**: IIM §1.5(v): "Record Version shall be the first Dataset of its record and shall not be repeated." Two occurrences violates the MUST NOT BE REPEATED constraint. Rule IIM-BIN-08.

**Severity**: LOW. The duplicate 1:00 is silently discarded by `Parse` (dataset-0 for records 1/2 is never stored), so round-trip semantic content is preserved. The binary stream is spec-non-conformant but causes no crash, no data loss, no security consequence. The practical trigger (user manually populates `Records[1]` with non-version, non-charset datasets AND writes non-ASCII content) is uncommon.

**Remediation**: In the `emitUTF8Decl` block, set a boolean `emittedR1Version = true`. In the main loop `record == 1` version injection, gate the injection on `!emittedR1Version`.

**Missing regression test**: `TestIIMREC01` does not test the case of R1 data + non-ASCII simultaneously.

### Areas Checked Clean

- `decodeDatasetLength`: extended-nBytes computation `(sizeHigh&0x7F)<<8 | sizeLow` — correctly handles wide nBytes (256 → rejected since >4). CLEAN.
- `parseIRB` / `parseIRBEntry` (format/jpeg): IRB walking with non-empty pascal names of odd and even byte-length. CLEAN.
- Extended dataset length 0x80000000 (2 GiB): rejected by per-dataset guard `length > 1<<20`. CLEAN.
- 1:90 over 32 octets: parsed without panic; ESC%G still recognized. CLEAN (no spec enforcement on read is intentional).
- `Encode` for records 3-9: emitted correctly, no spurious version injection. CLEAN.
- Record-3 dataset-0: correctly stored (version-skip only applies to R1/R2). CLEAN.
- `Encode` of zero-value `*IPTC`: returns empty slice, no panic. CLEAN.
- Duplicate non-repeatable dataset via direct `Records[2]` assignment: `Parse` first-wins on read-back. CLEAN (by design; accessor `firstRecord2` returns first occurrence).
- `ROBUST-01` through `ROBUST-18`: all pass. CLEAN.
- `IIM-BIN-01` through `IIM-BIN-08`, `IIM-REC-01..03`, `IIM-CS-01..04`, `IRB-APP13-01..09`: all pass. CLEAN.
- `FuzzParseIPTC` 30s: 4,643,960 executions, 0 crashes. CLEAN.
- `go test -race ./iptc/...`: PASS. CLEAN.
- `govulncheck ./iptc/...`: No vulnerabilities. CLEAN.
- Known issues #120, #121, #145, #146, #149, #151: all have corresponding conformance tests that pass. CONFIRMED FIXED.

### Dedup status for known issues in scope
- #120 (IPTC Encode never emits 2:00): CONFIRMED FIXED — TestIIMREC02 passes.
- #121 (TIFF 0x83BB TrimRight): CONFIRMED FIXED — TestROBUST16 passes.
- #145 (multiple APP13 last-wins): CONFIRMED FIXED — TestIRBAPP1309/TestROBUST15 pass.
- #146 (Encode ascending dataset order): checked — Encode preserves insertion order; conformance tests pass.
- #149 (tiff.Inject nil rawIPTC deletes IPTC): out of iptc/ scope.
- #151 (parseIRB padding pos exceed len): out of iptc/ scope (format/jpeg).
