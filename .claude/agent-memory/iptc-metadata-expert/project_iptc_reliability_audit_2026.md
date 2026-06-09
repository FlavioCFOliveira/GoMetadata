---
name: project-iptc-reliability-audit-2026
description: Reliability fragility audit of GoMetadata IPTC subsystem, 2026-06-04 and 2026-06-09 — findings from both rounds
metadata:
  type: project
---

## Reliability Audit 2026-06-04 (prior round — now fixed)

Full read-only audit of iptc/iptc.go, iptc/dataset.go, iptc/encoding.go, format/jpeg/jpeg.go with all IPTC tests.

**Why:** go-performance-architect requested IPTC-specific reliability fragility audit for the structured output pipeline.

All four findings from the 2026-06-04 round are confirmed fixed in the codebase as of 2026-06-09:
- FINDING A (HIGH): Decode-cache data race — fixed by task #60 (eager pre-decode in Parse)
- FINDING B (MEDIUM): SetKeywords UTF-8 flag — fixed by task #63
- FINDING C (MEDIUM): Stale cache on UTF-8 upgrade — moot after task #60 (no lazy cache), confirmed by task #78 pinning test
- FINDING D (MEDIUM): skipPascalString OOB — fixed with bounds check added to skipPascalString

## Reliability Audit 2026-06-09 (current round — new findings)

Second read-only audit of the same files plus format/tiff/tiff.go after all prior fixes landed.

### FINDING 1 (MEDIUM): parseIRB padding-byte advance unguarded — can advance pos beyond len(b) by 1

`parseIRB` (jpeg.go:1060-1063): after a successful non-0x0404 block is found and `pos = newPos`, if `len(data)%2 != 0` the code does `pos++` without checking whether `pos` is still within bounds. This cannot cause an OOB *read* (the for-loop guard `pos < len(b)` prevents the next iteration from reading), but it can produce `pos == len(b)+1` on exit — a logically correct loop but an off-by-one exit state. The actual bug is in `spliceIPTCIntoIRB` where the same `blockEnd` computation (jpeg.go:619-621) IS bounded by `if blockEnd > len(origIRB)`. So the main `parseIRB` call is safe; the fragility is documented for anyone who reuses the pattern.

### FINDING 2 (MEDIUM): TIFF IPTC tag 0x83BB — TypeByte/TypeUndefined trailing-zero trimming removes valid IPTC content

`extractTagValues` (tiff.go:416-418): `bytes.TrimRight(v, "\x00")` removes ALL trailing zero bytes. For TypeLong-encoded IPTC this is correct (zero padding). But for TypeByte or TypeUndefined encodings, a valid IPTC IIM dataset whose value genuinely ends in 0x00 (e.g. a NUL-terminated string per IIM convention, or an urgency field value 0x00) will have its content silently truncated. This is LOW severity in practice (most real IPTC-in-TIFF uses TypeLong) but is a spec compliance gap.

### FINDING 3 (LOW): No mandatory Record 2 version dataset (2:00) on write

`Encode` (iptc.go:293) does not emit dataset 2:00 (Record Version, IIM §2.2.1, mandatory 2-byte value = 4 for IIM 4.x). Real-world senders must include this. Absence is tolerated by all known readers but violates IIM §2.2.1 mandatory field requirement.

### FINDING 4 (LOW): Multiple APP13 segments — only the last one wins silently

`scanMetadataSegmentsWithWire` (jpeg.go:349): if a JPEG contains multiple APP13 segments with the Photoshop 3.0 prefix (non-standard but observed from some batch processors), each call to `processAPP13Segment` overwrites `rawIPTC`. The last segment wins silently. No `Truncated`-equivalent flag is set on the metadata struct. Low severity because the scenario is rare and the last-wins policy is consistent with ExifTool's behaviour, but it is not documented.

**How to apply:** Reference these findings in future IPTC work. FINDING 2 (TypeByte/TypeUndefined trim) is the most actionable new finding.
