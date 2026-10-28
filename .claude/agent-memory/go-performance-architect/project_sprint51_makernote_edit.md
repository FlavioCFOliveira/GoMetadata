---
name: sprint51-makernote-edit
description: Sprint 51 effort B (#407 Canon shifted base read, #398/#399 MakerNote delete/set/grow) — design, evidence method, traps, unfixed pre-existing defects
metadata:
  type: project
---

Effort B of sprint 51 (2026-10-28): `exif/makernote_canon_base.go` (#407), `exif/makernote_edit.go` + root `makernote_edit.go` (#398/#399).

**Design.** Edit the MakerNote as BYTES on a private copy (never write into the Parse buffer): delete = close the 12-byte slot (shift entries, move next-IFD offset unless a value sits there), zero the value; replace in place when it fits; grow = append at the end in front of Canon's footer, pointer set in the SOURCE frame (`origin + pos` for TIFF-relative, `pos - base` otherwise). The writers (`EncodeRebased`, format/tiff relocators) then rebase as for an unedited note. Every edit is verified (`verify`: re-read entries == promised, `resolveMakerNote` layout/base unchanged, walker `lost==0`, origin unchanged) and refused with `ErrMakerNoteNotEditable` otherwise; e untouched on error. Refusals: unknown layout, data outside the blob (`mnWalker.lost`), `noCarry` (value ends past the declared length), value on the tag table, BigTIFF, CR3 (MakerNote in CMT3, no 0x927C entry).

**Why:** `holdsMakerNote(src, off, mn)` makes EncodeRebased stop carrying outside data once the blob differs from src, so non-self-contained notes cannot be edited. The data-driven base (`offsetsCountFromTIFF`) is a count heuristic: an appended pointer can flip it (Samsung NX3000, Kyocera) -> verify refuses; same-size edits never flip.

**Traps found.**
- `topEntries(m.buf)` returns slices into buf; inline values alias the table, which delete shifts -> take the "want" snapshot from `m.orig` (immutable).
- `Parse`'s `MakerNoteIFD` can read wrong values where the data-driven base differs from the layout table (Pentax AOC, Kyocera FC-S3, Samsung NX): tests must compare with the editor's own reading (`newMakerNoteEdit` + `topEntries`), not `MakerNoteIFD` before/after.
- ExifTool (`MakerNotes.pm FixBase`) warns "Possibly incorrect maker notes offsets" when the first value is >16 bytes (or odd) after the IFD end: deleting/moving the FIRST value triggers a minor warning; tags still read right. Nikon encrypts blocks with a key from serial+shutter count: deleting/changing SerialNumber garbles ExifTool's decoded Nikon values (bytes untouched). Sony serials live inside the encrypted 0x9400 block, not a top-level tag.
- Exiv2's non-intrusive writer does NOT append: it writes in place when the value fits and goes intrusive (setDirty) otherwise. Our append model = Adobe XMP Toolkit UpdateMemByAppend.
- `uint` vs `math.MaxInt64` fails on 386: use `uint64(x) <= math.MaxInt64`; check with `GOOS=linux GOARCH=386 go vet`.
- Root test helper names collide (corpusBytes, pngChunks, jpegSegments exist): prefix `mne`. macOS sed has no `\b`: use perl.
- Metadata getters are not covered by the setter mutex: a race test mixing `LensModel()` with Set* races by contract.
- Corpus has 18 shifted Canon files (not 4): exiftool `-v3` "Adjusted MakerNotes base" over all Canon files.

**Evidence method.** Temporary harness test wrote edited copies of corpus files into the scratchpad; python script diffed `exiftool -a -G1 -s -H` (excluding offset-like names), compared `-ImageDataHash` (MD5) and warnings, and `exiv2 -pa` errors. Exiv2 0.28.9 installed with brew (dev oracle only).

**Unfixed pre-existing defects seen.** Any EXIF write of `Samsung NX3000.srw` loses its Samsung MakerNote tags; any write of the Sony A7M3 ARW makes ExifTool warn "Suspicious SR2DataIFD offset"; `raw/exiv2/IMG_1361.dng` write fails (image block out of bounds); exiv2 reads no EXIF from our AVIF writes (baseline, not edit related).
