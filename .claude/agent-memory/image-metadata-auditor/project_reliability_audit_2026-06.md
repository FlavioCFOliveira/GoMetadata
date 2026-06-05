---
name: project-reliability-audit-2026-06
description: Reliability fragility audit June 2026 — findings from deep code inspection of GoMetadata read/write/reconciliation paths
metadata:
  type: project
---

Reliability fragility audit performed June 2026. Key findings:

**F1 — iobuf pool orphan (jpeg/readSegment reallocation)**
When readSegment grows *scratch via `*scratch = make([]byte, need)`, the original pooled buffer is orphaned. The defer iobuf.Put(scratchPtr) in extractFull puts the NEW large slice (correct behavior for the new buffer), but the original 4096-byte pool entry is lost permanently. Under load with many large JPEG EXIF segments, pool depletes. Location: format/jpeg/jpeg.go:952-953.

**F2 — IPTC Dataset.stringValue concurrent data race**
Dataset.stringValue(*Dataset) writes d.decoded and d.decodedValue without synchronization. Concurrent read of the same *IPTC from multiple goroutines (e.g., calling Keywords() from two goroutines) causes a data race. Location: iptc/encoding.go:43-48.

**F3 — XMP UTF-32 input missing pre-decode size cap**
xmp.Parse calls normaliseToUTF8(b) BEFORE the maxXMPDocumentBytes size check. For UTF-32 input, decodeUTF32 receives the full raw input (potentially 256 MiB from PNG) and iterates all codepoints (up to 64M iterations) even though output is capped at 1 MiB. The UTF-16 path has a guard (maxXMPTranscodeBytes) but UTF-32 does not. Location: xmp/xmp.go:68 vs xmp/encoding.go:119-125.

**F4 — XMP numeric character reference overflow (parseHex/parseDec)**
parseHex and parseDec accumulate into rune (int32) without overflow/range validation. References like &#x110000; or long decimal refs can produce negative or invalid rune values. bld.WriteRune emits RuneError silently — wrong property values, not a panic. Location: xmp/rdf.go:1157-1184.

**F5 — ifdTotalSize uint32 wrap on encode with adversarial IFDEntry.Count**
ifdTotalSize computes sz += uint32(total) where total = uint64(e.Count)*uint64(typeSize). If an IFDEntry has Count=0xFFFFFFFF and typeSize=8, total=~34 GiB, and uint32(total) wraps. This produces wrong offset arithmetic during encode. Only reachable via manually constructed *IFD with extreme Count values. Location: exif/ifd.go:500.

**F6 — HEIF slow-path rawXMP aliases io.ReadAll buffer**
In heif.Extract slow path, parseHEIFMetadata returns rawXMP as a sub-slice of the full-file data buffer (not a copy). This prevents GC of the large file buffer but is not a safety issue. Fast path (readItemPayload) correctly copies. Location: heif/heif.go:697.

**Why:** These findings inform the go-performance-architect on what needs fixing:
- F1: needs a "save original before realloc" pattern in readSegment
- F2: needs sync.RWMutex or read-through immutable design for Dataset cache
- F3: needs pre-decode size guard for UTF-32 matching the UTF-16 guard
- F4: needs codepoint range validation in parseHex/parseDec matching decodeUTF32's guard
- F5: needs uint64 accumulation in ifdTotalSize with overflow check
- F6: should bytes.Clone rawXMP in the slow path for consistency

**How to apply:** Verify these findings are tracked in rmp tasks before implementing fixes. F2 is the only concurrent safety issue.
