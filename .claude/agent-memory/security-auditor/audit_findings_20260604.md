---
name: audit-findings-20260604-xmp-iptc
description: XMP and IPTC package security audit findings — 2026-06-04. Scope: xmp/* and iptc/* focused on hostile input robustness.
metadata:
  type: project
---

# Security Audit 2026-06-04 — XMP + IPTC Robustness

**Date**: 2026-06-04
**Packages**: xmp/*, iptc/*

## Tooling Results
- go vet: PASS
- go test -race -count=1: PASS (both packages)
- govulncheck: PASS (0 symbol-level vulnerabilities)
- FuzzParseXMP 30s: 0 crashers
- FuzzParseIPTC 30s: 0 crashers

## Findings Summary

### FINDING-XMP-001 — unsafe.String aliasing: parse buffer mutation corrupts XMP properties (MEDIUM)

**Location**: xmp/rdf.go:1044 (unescapeXML fast path)
**Status**: OPEN

The `unescapeXML` fast path returns `unsafe.String(unsafe.SliceData(b), len(b))` — a zero-copy string backed by the caller's `b` slice. This string is stored in `x.Properties` (returned to caller). If the caller mutates `b` after `Parse` returns, all property values without XML entities are silently corrupted. PoC confirmed: modifying `b` post-Parse changes `x.CameraModel()` output.

**Why it matters**: API contract does not document that `b` must be kept immutable for `*XMP` lifetime. Buffer-reuse patterns (pool, file-mmap) can trigger this silently.

**Fix**: Replace `unsafe.String(unsafe.SliceData(b), len(b))` with `string(b)` (heap-copies) in the unescapeXML fast path.

### FINDING-XMP-002 — parseHex/parseDec: unchecked integer overflow in numeric char refs (LOW)

**Location**: xmp/rdf.go:1157-1184 (parseHex, parseDec)
**Status**: OPEN

`parseHex` and `parseDec` accumulate into a `rune` (int32) without bounds checking. References like `&#x80000000;` or `&#2147483648;` overflow to negative rune values. `bld.WriteRune(r)` handles negative runes by writing U+FFFD (replacement char), so no crash, but the decoded value is silently wrong.

**Fix**: Add `if v > unicode.MaxRune { return 0, false }` guard after accumulation in both functions.

### FINDING-IPTC-001 — SetKeywords: missing UTF-8 flag update causes in-memory mojibake (HIGH)

**Location**: iptc/iptc.go:438-455 (SetKeywords)
**Status**: OPEN — CONFIRMED

`SetKeywords` stores `[]byte(kw)` values but does NOT update `i.Records[0]` (the internal UTF-8 flag). Reading back keywords via `Keywords()` before encoding calls `stringValue(isUTF8=false)` → `decodeString(bytes, false)` → ISO-8859-1 decode of UTF-8 bytes → mojibake.

PoC: `i.SetKeywords([]string{"café"}); i.Keywords()` returns `["cafÃ©"]`.

Note: `Encode` correctly catches this via `needsUTF8Declaration()` (checks bytes), so the wire format is correct. The bug is in the in-memory read path between SetKeywords and Encode.

**Fix**: Add `hasHighBytes` check + Records[0] update at end of SetKeywords loop, matching AddKeyword behavior.

### FINDING-IPTC-002 — IPTC zero-length dataset allocation bomb (MEDIUM)

**Location**: iptc/iptc.go:138-202 (Parse loop)
**Status**: OPEN — CONFIRMED

The `maxIPTCTotalBytes` aggregate cap bounds VALUE bytes only. A stream of N zero-length datasets (each 5 bytes on wire) passes the cap entirely (contributes 0 to totalBytes) but allocates a Dataset struct (~67 bytes) per entry. 5 MiB input → ~65 MiB heap (13.6× amplification).

For JPEG, APP13 is capped at ~65 KB → ~870 KB max allocation (acceptable). For TIFF (IPTC-NAA tag), the IPTC data can be larger, making this a real DoS vector.

**Fix**: Add a per-parse dataset count cap (e.g., `maxIPTCDatasets = 65536`) to bound the number of Dataset struct allocations.

### FINDING-XMP-003 — Write path: namespace URI and local name not XML-escaped (LOW)

**Location**: xmp/write.go:69,71,134,140 (serialise, writeSimpleProperty)
**Status**: OPEN

Namespace URIs and property local names are written to XMP output without XML escaping. If a caller sets adversarial namespace URIs or local names via `x.Set()`, the output XML is malformed or contains injected attributes. Values ARE correctly escaped via `writeXMLEscaped`.

Scope: affects only callers using `x.Set()` with attacker-controlled inputs, not the parsing path.

**Fix**: XML-escape ns URIs in attribute value position; validate that local names are valid XML NCNames before writing.

## What is NOT a finding

- The 100-level nesting cap: correctly enforced at parseStartTag
- The 16 MiB document cap: confirmed working
- The 1 MiB per-attribute cap: confirmed working  
- The 16 MiB transcode cap: confirmed working
- liPool / builderPool pool safety: confirmed no double-take, correct defer return
- IPTC decodeDatasetLength integer overflow: correctly guarded
- IPTC filter-in-place in SetKeywords: safe (range captures slice header before filter)
- parseHex/parseDec with zero-length ref: handled by decodeCharRef len==0 check
- nsTable/attrBuf overflow: silent drop with bounds guard (not a crash)
