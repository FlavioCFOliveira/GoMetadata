---
name: project_arw_conformance_battery
description: ARW conformance battery (task #165) — status, test categories, and key implementation facts
metadata:
  type: project
---

ARW conformance battery (task #165): `format/raw/arw/conformance_test.go` is complete and passing.

**Why:** Validates Sony ARW containers.md §8 rules — Make=="SONY" detection, SR2Private 0xC634 inline offset rebase, Sony MakerNote TIFF-absolute rebase, IFD0 preview relocation, write round-trip, and robustness.

**How to apply:** The battery uses `arw.Inject` (delegates to `tiff.Inject`) for all write tests, NOT `tiff.InjectWithEXIFARW`. That is correct — the ARW-specific SR2Private+MakerNote rebasing is tested via round-trip parse assertions, not by routing through the Sony-specific write path.

**Key implementation facts:**
- All `uint32(len(x))` and `uint32(intVar)` conversions in fixture builders need `//nolint:gosec // G115: bounded by test fixture construction`
- `uint32(exifOff)` where `exifOff` is a `const` does NOT trigger G115 (gosec doesn't fire on const-to-uint conversions)
- `gofmt -w` must be run after every edit before linting — trailing alignment spaces in comments cause gci failures
- Test categories: ARW-detect-*, ARW-SR2Private-*, ARW-makernote-*, ARW-IFD0-*, ARW-write-*, ARW-robust-*, ARW-corpus-*
- All tests pass `-race`, 0 lint issues

**Related:** [[project_cr2_conformance_battery]]
