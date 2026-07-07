---
name: project_task274_xmp_charref_forbidden_chars
description: Task #274 (Clean-GO sprint) — numeric char-ref forbidden-Char sanitization fix + struct-array docs limitation
metadata:
  type: project
---

Task #274 closed the Clean-GO XMP dimension with two items on top of [[project_task273_xmp_container_preservation]].

**FIX 1 (real corruption-on-write bug, LOW severity):** `decodeCharRef` in `xmp/rdf.go`
correctly rejected surrogates and values > U+10FFFF (via `parseHex`/`parseDec`) but did NOT
reject numeric character references whose value is a legal Unicode scalar value yet still
falls in an XML 1.0 §2.2 forbidden range — the C0 control block minus TAB/LF/CR, or
U+FFFE/U+FFFF. A document with `&#x1e;` or `&#30;` inside a Simple/scalar property decoded to
a literal U+001E byte — this library's INTERNAL multi-value sentinel (see write.go's
`strings.IndexByte(val, '\x1e') < 0 && !isColl` dispatch) — which spuriously re-serialised the
scalar property as an `rdf:Bag` on Encode. Confirmed with a live demo before the fix:
`tiff:Make = "Canon" + &#x1e; + "EOS"` encoded to `<tiff:Make><rdf:Bag><rdf:li>Canon</rdf:li><rdf:li>EOS</rdf:li></rdf:Bag></tiff:Make>`.

Fix: new `isForbiddenXMLCharRef(r rune) bool` helper in rdf.go, called from `decodeCharRef`
AFTER `parseHex`/`parseDec` return ok=true. Substitutes U+FFFD (writes it via
`bld.WriteRune`) instead of the raw forbidden code point — mirrors `writeXMLEscaped`'s
existing output-side policy exactly (xmp/write.go), so encode/decode are policy-consistent.
Returns `true` (not `false`) so something is always written — important because
`decodeEntity` DISCARDS `decodeCharRef`'s bool return entirely (a pre-existing, now-corrected
stale doc-comment claimed decodeEntity falls back to emitting `&ref;` literal on `false`; it
actually just drops the reference silently). Fixed the stale parseHex/parseDec doc comments
to say this accurately.

Scope was deliberately narrow per the task's own explicit instruction: "preserve the parse
fast path... not the hot literal-copy path." This means a RESIDUAL vector remains open by
design: a raw, un-encoded literal 0x1E byte embedded directly in XML text content (no
character reference at all) still flows through unescapeXML's fast path (`bytes.IndexByte(b,
'&') < 0 → string(b)`) and the CDATA raw-copy path with zero Char-production validation —
this is the SAME corruption class via a different injection vector, confirmed to exist by
reading collectTextContent/parseCDATA, but intentionally left alone because (a) the task
explicitly forbade touching the fast/literal-copy path for perf reasons, and (b) it's
consistent with this parser's established, memoed leniency philosophy (malformed XML is
accepted, not rejected — see [[feedback_xmp_parser_lenient]]). Flagged for a possible future
task if raw-control-byte XMP is ever seen in the wild.

**Defense-in-depth in write.go: assessed, NOT implemented.** The task asked whether
write.go's dispatch (`strings.IndexByte(val, '\x1e') < 0 && !isColl`) should defer entirely to
`effectiveContainerType` so a stray sentinel can't misclassify. Rejected as unclean: 
`effectiveContainerType` has no "confirmed scalar" signal — `isColl==false` means EITHER "spec
table confirms this is not an array" OR "totally unknown, e.g. a brand-new custom property a
caller Set() with manually-joined `\x1e` values and no spec-table entry" (this second case is
explicitly documented at write.go's `ctype == ""` fallback branch, added by task #273). Making
`isColl==false` force scalar serialisation regardless of `\x1e` presence would silently
downgrade that legitimate custom multi-value case, replacing its `\x1e` separators with
U+FFFD on output and losing the list structure — a NEW corruption, not a fix. Documented this
reasoning directly in write.go as a comment (search "Task #274 defense-in-depth assessment").

**FIX 2 (docs only):** added an "Accepted, spec-bounded limitation" note under RDF-08 in
docs/conformance/xmp.md: array-typed field nested inside a struct value is unsupported
(`atCollection()` requires `!p.inStruct`; `writeStructInListProperty` emits every field as a
bare scalar) but unreachable — none of ResourceEvent/stEvt, ResourceRef/stRef, Version,
Ancestor, Layer, ArtworkOrObjectDetails have array-typed sub-fields per Adobe XMP Spec Part
1/2 or IPTC Photo Metadata Standard 2025.1.

**Tests:** new xmp/task274_test.go — TestTask274CharRefForbiddenCharSanitized (load-bearing,
red-before/green-after proven by temporarily neutering the `isForbiddenXMLCharRef` call site
with `if false && ...`), TestTask274OtherForbiddenCharRefsSanitized (full C0/FFFE/FFFF
matrix), TestTask274LegalCharRefsUnaffected (positive control incl. supplementary-plane +
TAB/LF/CR), TestIsForbiddenXMLCharRefTable (primitive-level boundary table per
[[feedback_width_claims_need_primitive_test]] pattern), TestTask274NoAllocRegressionOnCharRefFreeDocument
(structural pin + AllocsPerRun — needed `//nolint:paralleltest`, see
[[feedback_allocs_per_run_no_parallel]]). New BenchmarkNumericCharRefDecode added to
bench_test.go for the cold path this fix touches.

**Benchmark delta: zero.** BenchmarkRDFParse, BenchmarkEntityDecode,
BenchmarkUnescapeXMLNoEntity, BenchmarkXMPParse all identical ns/op, B/op, allocs/op
before/after (measured via git stash of rdf.go+write.go with count=3) — structurally
guaranteed since decodeCharRef is unreachable from the entity-free fast path. Commit
05e0b73.
