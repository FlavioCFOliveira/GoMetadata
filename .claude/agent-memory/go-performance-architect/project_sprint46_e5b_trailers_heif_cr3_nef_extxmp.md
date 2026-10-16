---
name: sprint46-e5b-trailers-heif-cr3-nef-extxmp
description: Sprint 46 E5b (#346-#349,#353-#355,#357): internal/trailer, heif meta selection, cr3 no-drop, NEF 0E0E, keep-extended-XMP frame; verified facts, perf traps, unfixed defects
metadata:
  type: project
---

Done 2026-10-16 (uncommitted at hand-off; benchmarks vs `git archive HEAD` in scratchpad base5b).

- **internal/trailer** (AFCP, FotoStation, Samsung QDIO, `FindTail` generic over `Source`, `Plan.WriteRegion`): shared by format/jpeg (region after EOI) and format/tiff (`writeRelocated`, tail scan; trailer must start after every copied block). Only the Samsung QDIO block has absolute offsets; CanonVRD/PhotoMechanic/Vivo/OnePlus/MIE carry lengths or relative offsets (ExifTool sources, study).
- **HEIF**: one `findMetaBoxAbs` (top-level meta; moov direct-child fallback only when no top-level meta; malformed top-level meta does not fall through). Fast-path Extract takes the moov fallback only when the 64 KB window holds the whole file. `parseIinf` now uses headerLen.
- **CR3**: `ErrNoMetadataContainer` (EXIF needs Canon uuid; no moov also matches ErrNoMoovBox). Fuzz found: a size==0 child in the Canon uuid swallowed an appended CMT1 (`closeOpenEnded`).
- **NEF 0E0E**: value = "0100" + u16 count + 12-byte entries (id u32, offset u32, 4 unknown bytes), MakerNote byte order. Corpus values (Nikon.nef, Issue 247-*) match NO structure of their own file (pruned/edited), so the rule is "rewrite only when value == source structure position".
- **#357**: keep frame `\x00XMPKEEP`+GUID+packet crosses the rawXMP channel; `jpeg.prepareInject` resolves it against the real source (link added only if the source has ext segments of that GUID); kept segments pass through in place; oversize packet -> `ErrExtendedXMPConflict`.
- Perf traps: adding a string field to `xmpResult`/`Metadata` costs +3% / a size class (200->216 B moved Metadata to the 224 class): use pointers (`*string`) and copy-inside-branch (`new(string)`), never `&param`. A non-inlined gate call inside `jpeg.Inject` cost +6% on 250 ns micro-benchmarks; moving the gate into `prepareInject` removed it.
- Unfixed defects found (report, not fixed): heif `parseIinf` reads iinf version>=1 entry_count as 16 bits (spec: 32); heif Inject appends boxes after a size==0 child of meta/iinf/iref so readers swallow them; TIFF writes drop bytes no IFD addresses (unknown trailers); SetCaption auto-created EXIF lacks YCbCrPositioning (exiftool -validate warning); trailers after unknown/other trailers are not found by the TIFF tail scan.
- macOS: `sed -i -E` creates `*-E` backup files; zsh `==` in echo needs quotes; fuzz inputs land in testdata/fuzz (kept `FuzzCR3Inject/e2a80445ea50c869` as a regression seed).
