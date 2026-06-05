---
name: exif-audit-findings
description: Re-audit conformance results 2026-06-04 (reliability fragilities) — gaps in EXIF/TIFF subsystem
metadata:
  type: project
---

## Audit 2026-06-04 (reliability fragilities — current sprint)

**CRITICAL**
C1. writeSubIFDs nil pointer: e.IFD0.Next dereferences when e.IFD0 == nil (write.go:121).
    Triggered by Encode(&EXIF{ByteOrder: LE, ExifIFD: &IFD{...}}) with no IFD0.
    Panic at runtime. Guard: `if e.IFD0 != nil` before the for loop.

**HIGH**
H1. Rational() silently accepts TypeSRational (ifd.go:295), returning [2]uint32 with wrong bit
    interpretation for negative numerators/denominators (e.g. ShutterSpeedValue, ExposureBiasValue,
    BrightnessValue). Should reject TypeSRational and require callers to use SRational().
    Currently ExposureTime/FNumber/FocalLength use it on unsigned-rational tags (correct), but
    the open type-check allows silent wrong-value bugs in external callers and future API extensions.

H2. Dead subtree (exif/makernote/) missing Casio: live makernote_parse.go has parseCasioMakerNote
    with 3 Make variants; exif/makernote/dispatch.go Dispatch() has no Casio entry.
    Drift: any code using exif/makernote.Dispatch() for Casio cameras returns nil silently.

**MEDIUM**
M1. (pre-existing) upsertIFD0Entry breaks binary-search invariant — non-exploitable due to TIFF write gate.
M2. (pre-existing) TagIPTC registered as TypeLong; real files use TypeUndefined/TypeByte.
M3. (pre-existing) makernote/dispatch.go Dispatch() has no TrimSpace unlike parseMakerNoteIFD.
M4. MakerNote absolute-offset fragility on rewrite (Nikon Type3, Fujifilm).

**MINOR**
m1. int(total) conversion in dead subtree parsers (nikon, olympus, sony, pentax, fujifilm): could overflow
    on 32-bit systems but dead subtree is not called from live code.
m2. dmsToDecimal silently returns 0.0 for zero denominators.
m3. No depth limit on IFD chain (only cycle detection) in traverse().

**CONFIRMED RESOLVED (prior audit hardening)**
- iobuf pool buffer zeroed (clear(entryBuf) at ifd.go:528).
- TypeUTF8 (13): CORRECT.
- SetGPS GPSVersionID: CORRECT.
- MakerNote TrimSpace dispatch: CORRECT (live path only).
- UserComment charset + XP* tags: CORRECT.
- IFD1 thumbnail preservation: CORRECT.
- TIFF write gate: CORRECT.
