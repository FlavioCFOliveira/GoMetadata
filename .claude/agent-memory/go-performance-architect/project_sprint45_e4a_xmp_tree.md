---
name: project_sprint45_e4a_xmp_tree
description: Sprint 45 E4a (2026-10-14) — xmp/ flat-key model replaced by a Toolkit-ported data-model tree (#303), uncapped scanner lists (#283), composed-key budget (#277); design, perf traps, fuzz finds, unfixed pre-existing defects
metadata:
  type: project
---

Uncommitted at report time on feature/45-open-source-contributions (base 7600933);
coordinator commits A(#303 rewrite) → B(#283 tests) → C(#277 budget + tests).

**Design that survived (and why):**
- `xmlscan.go` builds a transient pooled element tree (DOM-lite) so the port of
  Toolkit `ParseRDF.cpp` (`parserdf.go`) gets look-ahead; then `project.go` fills
  `Properties` (unchanged key shapes) and RETAINS in `XMP.nodes` only top-level
  properties the flat view can't reproduce (`representable`). Plain docs keep
  `nodes == nil` → base-level cost. Retained schema/prop names: Encode merges
  edits on a pooled scratch COPY (Encode must never mutate x — [[feedback_encode_no_receiver_mutation]]).
- Properties is an exported map, so "edited?" is detected at Encode by comparing
  owner keys (`nodeOwner` flag) — no write hooks possible.
- #277: keys "P[N].F" can't share bytes in a Go map → only a budget on the FLAT
  view (4×doc+4KiB, whole entries skipped, never truncated) keeps memory O(input);
  the tree keeps everything so round-trip is lossless. 359× → 6.97× retained heap.

**User decisions (2026-10-14):** a simple value of a schema-array property
(e.g. `dc:rights="…"` attribute) is CONVERTED to the schema container like base
(not retained; exception: Alt value with '|'); rdf:value qualified values are
`"P" = value` only; the #277 budget stays and is documented on Properties and
the package doc. Result: every ./xmp benchmark within 10% of 7600933.

**Perf traps found (measured, idle machine, interleaved):**
- CPU profiles are useless on this Mac (runtime.kevent/madvise dominate even with
  GOMAXPROCS=1) → use phase micro-benchmarks + experiments instead.
- An encoder struct holding `*prefixer` or a method `defer e.release()` that passes
  e.sc to sync.Pool.Put makes the whole encoder (and its maps) escape → +3 allocs.
  Embed by value; release via free function taking the pooled pointer.
- Per-node map lookups (dup index, schema map, nsIntern) cost ~10% on small
  packets → linear scan ≤16 then map, flags instead of an "indexed" map.
- Lazy element text (decode only for property elements) and IndexByte for quoted
  attribute values bought the last ~5%.
- Arena: size once from an estimate accumulated during tree build; regrowth keeps
  superseded arrays alive via interior pointers (2-4× retained).

**Fuzz finds (seeds committed in xmp/testdata/fuzz/FuzzParseXMP):** typed-node
rdf:type values (ns+local) copied per item → intern each distinct type URI once
(c114f1e1801a4aa2, NS-REINTERN class); repeated
attributes → duplicate xml:lang written back; empty/illegal local names written
as different names. New invariant: Encode fixpoint after one round trip.

**Unfixed pre-existing defects reported:** jpeg writer uses wrong xmpNote URI
(`http://ns.adobe.com/xap/1.0/se/Note/` vs `http://ns.adobe.com/xmp/note/`; also
NSxmpNote constant) → ExifTool shows HasExtendedXMP as tmp0; UTF-16 XMP without
leading BOM not detected (ExifTool.jpg, XMP.jpg); ill-formed element names end
the Description early (exiv2-bug1026); extended XMP not merged by Read
(ExtendedXMP.jpg); value whitespace trimming (TextLayer values).
