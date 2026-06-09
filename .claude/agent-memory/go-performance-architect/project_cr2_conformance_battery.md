---
name: project_cr2_conformance_battery
description: CR2 conformance battery (task #162) — status, root cause fix, and key implementation facts
metadata:
  type: project
---

CR2 conformance battery (task #162): `format/raw/cr2/conformance_test.go` is complete and passing.

**Why:** Validates Canon CR2 containers.md §8 rules — CR marker preservation at bytes [8:12], IFD0 at offset 16, MakerNote TIFF-absolute offsets (R-10/R-11), write round-trip, and robustness.

**Root cause of 2 test failures:** `InjectWithEXIFCR2` in `format/tiff/tiff.go` had a design flaw — it called `relocateTIFFFromParsed` (which places IFD0 at offset 8 via `exif.Encode`), then `copy(updated[8:12], originalBytes[8:12])` overwrote the IFD0 count field (first 2 bytes of IFD0) with CR marker bytes `43 52`, making `exif.Parse` read 21059 entries at offset 8 → parse failure.

**Fix (commit pending):** `InjectWithEXIFCR2` now calls `insertCR2MarkerAndShiftOffsets` which:
1. Inserts 8 bytes at position 8 (CR marker `43 52 02 00` + 4 zero-pad bytes)
2. Updates IFD0 offset in TIFF header from 8 to 16 (`cr2IFD0Offset = 16`)
3. Rebases all OOL IFD pointers and inline sub-IFD pointers by +8 (`rebaseAllIFDsAfterCR2Marker`)

This mirrors `insertRW2GUIDAndShiftOffsets` (delta=16 for RW2) but with delta=8 for CR2. New sentinel errors `ErrCR2OutputTooShort` and `ErrCR2IFD0OutOfBounds` added to `format/tiff/errors.go`.

**How to apply:**
- `buildCR2Header` takes no parameters (always uses `cr2IFD0Off = 16`)
- `buildCR2WithMakerNote` returns just `[]byte` (no return values for internal offsets — they were always ignored by callers)
- All `const ifd0Off = 16` in individual fixture functions are still present (still used in `buf := make(...)` calculations)
- 0 lint issues; all tests pass `-race`

**Related:** [[project_arw_conformance_battery]]
