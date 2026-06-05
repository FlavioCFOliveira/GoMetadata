---
name: project-iptc-audit-2026
description: Conformance re-audit of GoMetadata IPTC subsystem after hardening round, conducted 2026-06-01
metadata:
  type: project
---

## Re-audit after hardening round (2026-06-01)

Full read-only re-audit of iptc/iptc.go, iptc/dataset.go, iptc/encoding.go, iptc/iptc_compliance_test.go, format/jpeg/jpeg.go.

**Why:** go-performance-architect requested confirmation that four specific hardening fixes are correct and complete, and that no new issues were introduced.

### Fix 1: Field-length enforcement (datasetMaxLen + truncateToLimit)
- CONFIRMED CORRECT. datasetMaxLen table values match IIM §2.2 exactly (verified against exiv2.org and ExifTool TagNames).
- truncateToLimit is UTF-8-safe: walks backwards from maxLen dropping bytes until utf8.Valid passes. Handles 1-byte, 2-byte, 3-byte, 4-byte runes correctly.
- Edge cases tested and correct: limit=0 means no truncation; limit < rune boundary returns shorter valid string (not empty unless boundary forces it); 3-byte rune cut to 2 bytes yields empty (correct).
- All setters (SetCaption, SetCopyright, SetCreator, AddCreator, AddKeyword, SetKeywords) call truncateToLimit. CONFIRMED.

### Fix 2: Recoverable malformed-dataset parsing (break→continue + IPTC.Truncated)
- CONFIRMED CORRECT. Two distinct skip paths:
  - Extended-length block malformed (nBytes out of range or truncated): pos++ then continue (re-scans next byte for 0x1C).
  - Individual dataset too large (>1MiB) or declared length exceeds buffer: pos=newPos then continue (advances past header).
- No infinite loop risk: both paths advance pos by at least 1 byte.
- IPTC.Truncated set in all skip paths. Aggregate DoS cap (256 MiB) uses break, sets Truncated, returns partial data.
- err=nil contract preserved: Parse always returns (non-nil *IPTC, nil).

### Fix 3: Auto-insertion of 1:90 UTF-8 declaration
- CONFIRMED CORRECT. Byte sequence: 0x1C 0x01 0x5A 0x00 0x03 0x1B 0x25 0x47 — verified.
  - 0x1C = tag marker; 0x01 = record 1; 0x5A = dataset 90; 0x00 0x03 = length 3; 0x1B 0x25 0x47 = ESC % G.
- Encode checks: emitUTF8Decl = i.isUTF8() || i.needsUTF8Declaration(). Covers both the "stream already declared UTF-8" and "new non-ASCII write" cases.
- Duplicate injection guard: if !i.isUTF8() before setting Records[0]. CONFIRMED no duplicate on round-trip.
- MINOR NIT: isUTF8Declaration checks len(b)==3 strictly. IIM §1.5.1 says field is up to 32 bytes; exotic multi-sequence values (e.g., NUL-padded to 4 bytes or value "UTF8" as ASCII string from Adobe Bridge) would not be recognised. Low real-world risk.
- setRecord2 and AddCreator also set internal UTF-8 flag when non-ASCII bytes present.

### Fix 4: By-line repeatable AllCreators()/AddCreator() + DateCreated()/TimeCreated()
- CONFIRMED CORRECT. AllCreators() collects all DS2Byline entries. Creator() returns first (via firstRecord2).
- AddCreator appends a new Dataset — correct repeatable semantics per IIM §2.2.25.
- SetCreator calls setRecord2 which replaces only the FIRST occurrence — correct for update-first semantics; additional entries preserved.
- DateCreated() maps to DS2DateCreated (2:55), TimeCreated() to DS2TimeCreated (2:60). Both via firstRecord2 returning raw ASCII string. Format not validated (CCYYMMDD, HHMMSS±HHMM) — by design, the library is a storage layer.

### Residual issues found

**MINOR: isUTF8Declaration strict 3-byte check** (iptc/encoding.go:53)
Some real-world tools (Adobe Bridge, older Photoshop) write "UTF8" as an ASCII literal (4 bytes: 0x55 0x54 0x46 0x38) or pad ESC % G to even length. These are not recognised as UTF-8. In practice, files written by ExifTool or compliant tools always use exactly 3 bytes, so this is low-risk. Not a blocker.

**MINOR: dataSize integer overflow on 32-bit platforms** (format/jpeg/jpeg.go:737)
`dataSize := int(binary.BigEndian.Uint32(b[pos:]))` — on a 32-bit platform, a uint32 value > 2^31-1 would cast to a negative int, causing `pos+dataSize > len(b)` to trivially pass, returning an empty slice rather than an error. On all target 64-bit platforms this is safe. Acceptable risk.

**NIT: SetKeywords does not set UTF-8 flag** (iptc/iptc.go:432-449)
SetKeywords calls truncateToLimit and appends but does not call `hasHighBytes` / set Records[0] for non-ASCII keywords. AddKeyword does. Low risk — Encode's needsUTF8Declaration scan of all records still catches it at encode time.

**NIT: no validation of date/time format** for DS2DateCreated (2:55) and DS2TimeCreated (2:60). Callers can write "garbage" as a date. By-design for a storage library, but worth noting for future strict-mode API.

### Overall verdict (re-audit)
**Production-ready for both READ and WRITE.** All four previously-identified blockers are correctly fixed. No new blockers introduced. Two MINOR and two NIT residuals remain, none of which cause data loss, crashes, or incorrect output on compliant input. See [[reference-iim-field-constraints]] for field-constraint table.

**How to apply:** This is the current baseline. Any future IPTC changes should be validated against these findings.
