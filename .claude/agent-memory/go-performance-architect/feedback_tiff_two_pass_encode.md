---
name: feedback_tiff_two_pass_encode
description: TIFF copy-and-relocate: two-pass exif.Encode pattern for stable IFD size with placeholder entries
type: feedback
---

Two-pass exif.Encode pattern for TIFF copy-and-relocate:

**Rule:** When you need to know the encoded IFD structure size *before* you know the final offset values for image blocks, insert placeholder entries (TypeLong, Count=N, zero-valued byte slices) into the IFDs, call `exif.Encode` once to get `ifdEnd = len(skeleton)`, then write the real offsets into the same byte slices in-place and call `exif.Encode` again. Both encodes produce the same IFD structure size because only value bytes changed.

**Why:** The naïve "remove image-data entries, encode skeleton, re-insert patched entries, re-encode" approach fails because the skeleton is *smaller* than the final TIFF (it's missing the offset/bytecount entries), so `ifdEnd` from the skeleton is wrong — the image blocks end up overlapping the IFD value area.

**How to apply:**
1. `removeImageOffsetEntries(blocks)` — remove stale entries.
2. `offsetValueSlices := insertPlaceholders(blocks, order)` — insert TypeLong Count=N entries with zero value slices; return the slice map.
3. First `exif.Encode(e)` → `ifdEnd = len(skeleton)`.
4. `assignNewOffsets(blocks, ifdEnd)`.
5. `updatePlaceholders(blocks, offsetValueSlices, order)` — write real offsets into the same backing arrays.
6. Second `exif.Encode(e)` → final TIFF (same size as step 3).
7. Append block bytes.

**Critical detail:** `IFDEntry.Count` must be set to the number of TypeLong *elements* (N), not `len(value)` (which would be N×4). Use `upsertIFDEntryWithCount(ifd, tag, N, valueSlice)` not `upsertIFD0Entry` (which infers Count from len(value) and sets it to N×4 for multi-element arrays, producing garbage output when re-parsed).

Related: [[feedback_nolint_cyclop_vs_gocyclo]]
