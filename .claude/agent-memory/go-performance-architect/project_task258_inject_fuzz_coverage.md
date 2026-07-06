---
name: task258_inject_fuzz_coverage
description: 9 new FuzzXxxInject write-path targets (jpeg/png/7 RAW); rawEXIF semantics diverge by format family and dictate fuzz-harness design
type: project
---

Task #258 closed the write-path fuzz-coverage gap: every parser package had a
FuzzXxxExtract target but only format/tiff, format/heif, format/webp had a
matching FuzzXxxInject. Added FuzzJPEGInject, FuzzPNGInject, and
FuzzARWInject/FuzzCR2Inject/FuzzCR3Inject/FuzzDNGInject/FuzzNEFInject/
FuzzORFInject/FuzzRW2Inject — all in the existing `fuzz_test.go` per package
(no new `fuzz_inject_test.go` files; kept consistent with the one-file-per-
package convention already used by tiff/webp/heif).

**Key design insight — `rawEXIF` has TWO different semantics depending on
format family, and the fuzz harness must match the right one:**

1. **TIFF-identical formats** (tiff, arw, cr2, dng, nef — no container/EXIF
   distinction, the whole file IS one big IFD tree): inside `tiff.Inject`,
   `rawEXIF != nil` makes `base = rawEXIF` and the reader `r` is **never
   read**. So to let the fuzzer's mutations reach `relocateTIFF`'s IFD
   traversal/cycle-detection/field-width logic, `rawEXIF` must be set to the
   SAME fuzzed `data` passed as the reader (`Inject(bytes.NewReader(data),
   io.Discard, data, rawIPTC, rawXMP, true)`), exactly mirroring
   `FuzzTIFFInject`. Passing a fixed unrelated string as rawEXIF here is a
   dead-end — see below.

2. **Container formats with their own metadata slot** (webp, heif, jpeg, png,
   cr3): rawEXIF is a genuine "new EXIF payload to embed", separate from the
   container. Fix rawEXIF/rawXMP as small constant valid payloads and let the
   fuzzer vary the container bytes via the reader, mirroring
   FuzzWebPInject/FuzzHEIFInject.

3. **ORF/RW2 special case**: these two packages add real logic ON TOP of
   tiff.Inject — validate "IIRO"/"IIRS"/"IIU\x00" magic, patch bytes[2:4] to
   standard TIFF LE (0x2A 0x00) on a private copy of `data` read from `r`,
   delegate to tiff.Inject, then restore the original magic in the output.
   If you pass the fuzzed `data` as `rawEXIF` (style 1), `tiff.Inject` uses
   `base = rawEXIF` (still bearing ORF/RW2 magic, UNPATCHED) and never reads
   the ORF/RW2-patched copy from `r` — this bypasses the exact
   package-specific patch-and-restore code you're trying to fuzz, AND the
   base immediately fails exif.Parse's TIFF-magic check every time (since
   satisfying the outer isORFMagic/HasPrefix gate on `data` guarantees
   failing the inner un-patched magic check). **Correct design: pass
   `rawEXIF = nil`** so tiff.Inject reads its base from `r` — i.e. the
   ORF/RW2-patched copy that the wrapper package itself produced. This is
   also the realistic gometadata.Write call shape (see
   [[feedback_tiff_exif_base_for_write]] / project_dng_write_gated.md-style
   memory: rawEXIF=nil means "preserve original file, only update
   IPTC/XMP").

**CR3 seed reuse for regression lock-in**: FuzzCR3Inject seeds reuse
`buildExtendedBox`, `buildExtendedUUIDBox`, `ftypBox16` from
`format/raw/cr3/extended_size_test.go` (written specifically to be reusable
fuzz seeds for the CR3-EXTSIZE-01 fix — extended/largesize ISOBMFF box
encoding, ISO 14496-12 §4.2). Seeds cover: extended-size moov wrapping a
normal uuid box, normal moov wrapping an extended-size uuid box (with
CMT1+CMT2+"XMP " siblings), and both extended simultaneously.

All 9 targets ran clean for a 20s sweep each (2.6M-7.9M execs/target, 0
crashers). ORF/RW2/CR3 show much lower "new interesting" counts (10-32 vs
70-120 for jpeg/png/arw/cr2/dng/nef) because of the magic-byte gate narrowing
the reachable state space early — this is expected, not a coverage bug.

Files touched (test-only, per CLAUDE.md scope discipline):
format/jpeg/fuzz_test.go, format/png/fuzz_test.go,
format/raw/{arw,cr2,cr3,dng,nef,orf,rw2}/fuzz_test.go. No production .go file
was modified for this task. format/raw/cr3/cr3.go and
format/raw/cr3/extended_size_test.go were already modified/added in the
working tree before this task started (CR3-EXTSIZE-01 fix, pre-existing) —
not touched by this task.
