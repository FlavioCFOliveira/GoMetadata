# EXIF + TIFF 6.0 + BigTIFF — Conformance Test Contract

Normative-requirements checklist for the GoMetadata conformance test battery. Rule IDs
(`S-*` structural, `V-*` value-level, `R-*` robustness) are used verbatim as Go sub-test
names. Every rule is testable and cites its spec clause.

## Section 1: Exact Spec Identifiers

### 1.1 EXIF

| Document ID | Title | Org | Version | Year |
|---|---|---|---|---|
| CIPA DC-008-Translation-2024 | Exif Version 3.0 (corrected edition) | CIPA | 3.0 (corr.) | 2024 |
| CIPA DC-008-Translation-2023 | Exif Version 3.0 | CIPA | 3.0 | 2023 |
| CIPA DC-X008-Translation-2019 | Exif Version 2.32 | CIPA / JEITA | 2.32 | 2019 |
| CIPA DC-008-Translation-2016 | Exif Version 2.31 | CIPA / JEITA | 2.31 | 2016 |

JEITA companion: **CP-3451** (CP-3451C = 2.3, CP-3451D = 2.31, CP-3451E = 2.32). CIPA
DC-008 and JEITA CP-3451 are technically identical; cite CIPA DC-008 as primary.

**EXIF 2.32 vs 3.0 normatively significant differences**

| Feature | 2.32 | 3.0 |
|---|---|---|
| Text encoding | TypeASCII (2) only | TypeUTF8 (13) added; ASCII fields may use type 13 |
| TypeUTF8 | undefined | code 13, element size 1, NUL-terminated |
| FlashpixVersion | mandatory in ExifIFD | optional/recommended |
| ExifVersion value | "0220"/"0230"/"0232" | "0300" |

### 1.2 TIFF 6.0
"TIFF Revision 6.0 Final", Adobe Developers Association, 3 June 1992. No ISO number.
Structural foundation of EXIF; all IFD layout rules derive from TIFF 6.0 §2.

### 1.3 BigTIFF
"BigTIFF Design", Aware Systems / libtiff. De-facto spec at
libtiff.gitlab.io/libtiff/specification/bigtiff.html. First stable libtiff impl: 4.0.0 (2011).

### 1.4 EXIF in JPEG (APP1) — CIPA DC-008 §4.5.2 / §4.7.2
- APP1 marker `0xFF 0xE1`; 2-byte big-endian length (counts itself, excludes marker).
- Exif identifier: exactly 6 bytes `45 78 69 66 00 00` ("Exif" + two NUL).
- TIFF header follows the identifier. TIFF-stream offsets are relative to the byte-order
  marker, NOT to SOI or the APP1 marker.
- Max APP1 segment: 65,535 bytes total; EXIF cannot span segments.

## Section 2: Normative Structural Rules

### 2.1 TIFF Header
- **S-01** First two bytes MUST be `49 49` (II, LE) or `4D 4D` (MM, BE). — TIFF 6.0 §2.
  Fixture: header `00 00…` → error, no panic.
- **S-02** Bytes 2–3 of classic TIFF MUST be magic 42 (`2A 00` LE / `00 2A` BE). — TIFF 6.0 §2.
  Fixture: `49 49 2B 00…` (BigTIFF magic in classic context) → reject.
- **S-03** Bytes 4–7 = uint32 IFD0 offset from byte 0; MUST be ≥ 8 and < len. 0 is invalid. — TIFF 6.0 §2.
- **S-04** IFD0 MUST begin on a word boundary (even offset). — TIFF 6.0 §2. Parser must not crash on odd.
- **S-05 (BigTIFF)** 16-byte header: BOM, magic 43 (`2B 00`/`00 2B`), offset-bytesize `08 00`/`00 08` (MUST = 8), reserved `00 00`, uint64 IFD0 offset. Fixture: offset-bytesize 4 → error.
- **S-06 (BigTIFF)** Reserved bytes 6–7 SHOULD be 0; parser SHOULD ignore non-zero (advisory).

### 2.2 IFD Structure — Classic
- **S-07** IFD = uint16 entry count + count×12-byte entries + 4-byte next-IFD offset (0 = none). — TIFF 6.0 §2.
- **S-08** Each entry is 12 bytes: tag(u16), type(u16), count(u32), value-or-offset(u32). — TIFF 6.0 §2.
- **S-09** If typeSize×count ≤ 4, value is inline left-justified; trailing bytes are padding. — TIFF 6.0 §2.
- **S-10** If total > 4, bytes 8–11 = uint32 offset; offset+total MUST be ≤ len. — TIFF 6.0 §2.
- **S-11** Out-of-line value offset MUST be even (word boundary). Parser must not crash on odd. — TIFF 6.0 §2.
- **S-12** Entries MUST be sorted ascending by tag (writer); parser MUST handle unsorted. — TIFF 6.0 §2.
- **S-13** next-IFD pointer is uint32; 0 = end; out-of-bounds value → treat as end, no crash. — TIFF 6.0 §2.
- **S-14** Entry count 0 is a valid empty IFD; next-IFD pointer follows the count field. — TIFF 6.0 §2.

### 2.3 IFD Structure — BigTIFF
- **S-15** BigTIFF IFD = uint64 entry count + 20-byte entries + uint64 next-IFD pointer. Entry = tag(u16), type(u16), count(u64), value-or-offset(u64).
- **S-16** BigTIFF inline threshold is 8 bytes; total > 8 → uint64 offset.
- **S-17** Entry count is uint64; counts > 65535 MUST be treated as corrupt (DoS guard / no OOM alloc).

### 2.4 Field Type Codes — **S-18**
| Code | Name | Size | | Code | Name | Size |
|---|---|---|---|---|---|---|
| 1 | BYTE | 1 | | 8 | SSHORT | 2 |
| 2 | ASCII | 1 | | 9 | SLONG | 4 |
| 3 | SHORT | 2 | | 10 | SRATIONAL | 8 |
| 4 | LONG | 4 | | 11 | FLOAT | 4 |
| 5 | RATIONAL | 8 | | 12 | DOUBLE | 8 |
| 6 | SBYTE | 1 | | 13 | UTF8 (Exif 3.0) | 1 |
| 7 | UNDEFINED | 1 | | 16/17/18 | LONG8/SLONG8/IFD8 (BigTIFF only) | 8 |

— TIFF 6.0 §2 (1–12), Exif 2.32/DC-008-2023 §4.6.3 (13), BigTIFF (16–18). Unknown type
code MUST be skipped/preserved (treat 4 value bytes as raw, never dereference). 
- **S-19** RATIONAL = two u32 (num/den); SRATIONAL = two i32. Denominator 0 must not divide/crash.
- **S-20** ASCII MUST be NUL-terminated; count includes NUL; parser strips trailing NUL.
- **S-21 (Exif 3.0)** UTF8 (13) NUL-terminated UTF-8; Exif-2.x parser must treat 13 as unknown, no crash.
- **S-22 (BigTIFF)** types 16/17/18 valid only in BigTIFF; classic parser must not dereference as LONG8.

### 2.5 EXIF IFD Chain
- **S-23** IFD0 from header; ExifIFD from tag `0x8769` (LONG, or LONG8/IFD8 in BigTIFF). Absent → ExifIFD nil, no error.
- **S-24** GPS IFD from tag `0x8825` in IFD0; separate from ExifIFD.
- **S-25** Interop IFD from tag `0xA005` inside ExifIFD (not IFD0).
- **S-26** IFD1 (thumbnail) via IFD0 next-IFD pointer; main chain limited to IFD0+IFD1 for JPEG. — Exif §4.5.1.

### 2.6 Mandatory Tags
- **S-27 (ExifIFD)** ExifVersion `0x9000` UNDEFINED[4] (e.g. "0232", no NUL); FlashpixVersion `0xA000`; ColorSpace `0xA001` SHORT (1=sRGB, 0xFFFF=Uncalibrated); ComponentsConfiguration `0x9101`. — Exif §4.6.5 Table 4.
- **S-28 (IFD0)** Make `0x010F`, Model `0x0110`, Orientation `0x0112`, XResolution `0x011A`, YResolution `0x011B`, ResolutionUnit `0x0128`, DateTime `0x0132`, YCbCrPositioning `0x0213`. — Exif §4.6.4 Table 3.
- **S-29** DateTime `0x0132`/`0x9003`/`0x9004` ASCII[20] format `"YYYY:MM:DD HH:MM:SS\0"`; unknown fields → spaces. — Exif §4.6.4/§4.6.5.
- **S-30** SubSecTime `0x9290`/`0x9291`/`0x9292` ASCII decimal digit string.
- **S-31** OffsetTime `0x9010`/`0x9011`/`0x9012` ASCII `"+HH:MM\0"` (Exif 2.31+). — DC-X008-2019 §4.6.5.
- **S-32 (GPS)** GPSVersionID `0x0000` BYTE[4] = {2,3,0,0}; GPSLatitudeRef `0x0001` ASCII "N/S"; GPSLatitude `0x0002` RATIONAL[3]; GPSLongitudeRef `0x0003` "E/W"; GPSLongitude `0x0004` RATIONAL[3]. — Exif §4.6.6 Table 15.
- **S-33 (Interop)** InteroperabilityIndex `0x0001` ASCII "R98"/"THM"/"R03"; InteroperabilityVersion `0x0002` UNDEFINED "0100". — Exif Annex A.

### 2.7 BigTIFF Write (Encode) — **S-34..S-39** (task #264)
- **S-34** Encode of a BigTIFF-provenanced EXIF (`EXIF.BigTIFF == true`) emits a 16-byte header: BOM (II/MM) + magic 43 (`2B 00`/`00 2B`) + bytesize-of-offsets `08 00`/`00 08` + reserved `00 00` + uint64 IFD0 offset = 16, for both byte orders. — BigTIFF spec §2.
- **S-35** Encoded BigTIFF IFD = uint64 entry count + count×20-byte entries (tag u16 @+0, type u16 @+2, count u64 @+4, value-or-offset u64 @+12) + uint64 next-IFD pointer; entries sorted ascending by tag. — BigTIFF spec §2.
- **S-36** Encoded inline/out-of-line threshold is 8 bytes, computed as `typeSizeBigTIFF(Type) * Count`: totals ≤ 8 are written inline, left-justified, zero-padded; totals > 8 are written out-of-line with a uint64 absolute offset in the value field. — BigTIFF spec §2.
- **S-37** Encoded next-IFD pointer is a uint64; 0 marks the end of the chain. — BigTIFF spec §2.
- **S-38** Sub-IFD pointer tags (ExifIFDPointer `0x8769`, GPSIFDPointer `0x8825`, InteropIFDPointer `0xA005`) and thumbnail pointer tags (JPEGInterchangeFormat `0x0201`, JPEGInterchangeFormatLength `0x0202`) are encoded with type field = TypeLong (never promoted to LONG8/IFD8); the 4-byte value is left-justified in the 8-byte field with the upper 4 bytes zero. — EXIF §4.6.3, §4.5.5; BigTIFF spec §2.
- **S-39** Value-area alignment is word (2-byte), matching the classic-TIFF padding strategy (writeIFD/ifdTotalSize) reused with wider field constants — a deliberate deviation from the BigTIFF design-doc's advisory 8-byte-alignment text, chosen for interoperability with the reference implementation (libtiff `tif_dirwrite.c`, which enforces only word alignment) and with this project's own committed fixtures. — BigTIFF spec §2 (see exif/ifd.go `ifdTotalSizeBigTIFF` doc comment for the full rationale).

### 2.8 BigTIFF Container Write / Relocation (task #270) — **S-40..S-43**

Task #264 (S-34..S-39) covers `exif.Encode`'s BigTIFF-provenanced EXIF-blob
serialisation. These rules cover the layer above it: `format/tiff`'s
copy-and-relocate serializer (`relocateTIFFFromParsed`, the implementation
behind `tiff.Inject`/`tiff.InjectWithEXIF`), which additionally has to rebuild
the surrounding TIFF/BigTIFF *container* — image-data blocks (strips, tiles),
SubIFDs, and any outer-container byte scans performed after `exif.Encode`
produces the IFD skeleton.

- **S-40** When the source container is BigTIFF (`EXIF.BigTIFF == true`), every
  outer-container structural lookup the relocator performs after
  `exif.Encode` (locating IFD0's offset field, scanning IFD0 for the 0x014A
  SubIFDs entry, scanning IFD0/ExifIFD for MakerNote OOL rebasing) uses
  BigTIFF's 16-byte header, 20-byte entries, and 8-byte value-or-offset
  fields — never the classic 8-byte header / 12-byte entries / 4-byte fields.
  — BigTIFF spec §2.
- **S-41** StripOffsets/StripByteCounts/TileOffsets/TileByteCounts declared as
  LONG8 (type 16) in a BigTIFF source are read using 8-byte elements (not
  misread as 4-byte LONG or silently rejected); an unrecognised element type
  returns `ErrUnsupportedOffsetType` rather than reading a wrong-width value.
  — BigTIFF spec §3.3; fixtures: `testdata/corpus/tiff/metadata-extractor/
  BigTIFFLong8.tif`, `BigTIFFLong8Tiles.tif`, `format/tiff/testdata/
  big_cramps_{be,le}.tif`.
- **S-42** On relocation, the offset/bytecount placeholder entry re-inserted
  for StripOffsets/TileOffsets/StripByteCounts/TileByteCounts preserves the
  SOURCE entry's declared element width (SHORT/LONG/LONG8) rather than always
  forcing LONG — a BigTIFF file that legitimately used LONG8 for these arrays
  is not silently downgraded on write. — TIFF 6.0 §2 / BigTIFF spec §2 (no
  single mandated width for these tags; readers must accept whichever the
  writer used).
- **S-43** SubIFDs (tag 0x014A) may legitimately be declared as LONG (type 4),
  IFD (type 13 — TIFF 6.0 Extensions / libtiff `TIFF_IFD`; NOT the same
  meaning as EXIF 3.0's TypeUTF8 assignment of the same numeric code, see
  `typeSize`'s doc comment in `format/tiff/tiff.go`), LONG8 (type 16,
  BigTIFF), or IFD8 (type 18, BigTIFF). The relocator decodes and re-encodes
  the pointer array correctly for all four widths, using format/tiff's own
  generic raw-IFD type table (which is NOT `exif/type.go`'s EXIF-registry
  table) — see `enumerateSubIFDs`/`patchSubIFDPointers` in
  `format/tiff/relocate.go` and `format/tiff/relocate_bigtiff.go`. Fixtures:
  `testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif` (type 13),
  `BigTIFFSubIFD8.tif` (type 18).

## Section 3: Value-Level Conformance
- **V-01** RATIONAL [num@0..3, den@4..7] in stream byte order; guard den==0 before float. SRATIONAL = two i32.
- **V-02** Signed tags (ShutterSpeedValue `0x9201`, BrightnessValue `0x9203`, ExposureBiasValue `0x9204`) MUST use SRational(); using Rational() misreads the sign bit.
- **V-03** Orientation `0x0112` valid 1–8 (1=normal, 3=180°, 6=90°CW, 8=270°CW, 2/4/5/7=mirrored). Values 0/9+ must not crash. — Exif §4.6.4.
- **V-04** ResolutionUnit `0x0128`: 1=none, 2=inch (default), 3=cm. Default when absent = 2.
- **V-05** GPSLatitude/Longitude RATIONAL[3] = deg/min/sec; deg ≤ 90 (lat) / ≤ 180 (lon); min,sec < 60; den ≠ 0.
- **V-06** GPSLatitudeRef "N"/"S"; GPSLongitudeRef "E"/"W"; coordinate rationals always non-negative.
- **V-07** GPSVersionID BYTE[4]; type MUST be BYTE not ASCII; count = 4.
- **V-08** GPSAltitudeRef `0x0005` BYTE (0=above, 1=below sea level); GPSAltitude `0x0006` RATIONAL non-negative.
- **V-09** GPSTimeStamp `0x0007` RATIONAL[3] UTC; GPSDateStamp `0x001D` ASCII[11] `"YYYY:MM:DD\0"`.
- **V-10** UserComment `0x9286` UNDEFINED: 8-byte charset prefix ("ASCII\0\0\0", "UNICODE\0", "JIS\0\0\0\0\0", or all-NUL) + payload; payload < 8 bytes → empty, no panic.
- **V-11** IFD1 JPEG thumbnail: Compression `0x0103`=6; JPEGInterchangeFormat `0x0201` offset + `0x0202` length within stream; out-of-range → nil thumbnail, no panic.
- **V-12 (BigTIFF write, task #264)** Every BigTIFF encode-side size computation (ifdTotalSizeBigTIFF, writeIFDBigTIFF) MUST call typeSizeBigTIFF, never typeSize — a regression guard against silently treating LONG8/SLONG8/IFD8 (16/17/18) as size-0.
- **V-13 (BigTIFF write)** A total of 5–8 bytes (e.g. RATIONAL count=1 = 8 bytes) is placed inline under the BigTIFF threshold, though the same total would be out-of-line under the classic 4-byte threshold; placement is a deterministic function of (Type, Count), never of how the source stored it.
- **V-14 (BigTIFF write)** An entry with an unrecognised type code round-trips as an opaque 8-byte raw inline field (mirrors parseIFDEntryBigTIFF's `sz == 0` handling) — the exact 8 bytes present at parse time are reproduced verbatim on re-encode.
- **V-15 (BigTIFF container write, task #270)** All image-block and SubIFD relocation offset arithmetic in `format/tiff`'s copy-and-relocate serializer (`imageBlock.srcOffset/size/newOffset`, `subIFDInfo.srcOffset/newOffset`, `readUint`) is carried in `uint64`, so that a value legitimately sourced from a LONG8/IFD8 (8-byte) field is never pre-truncated to `uint32` at any intermediate step before the final classic-4-GiB defence-in-depth ceiling check (see R-15's rationale: this package's own `maxFileSize` cap of 256 MiB means the ceiling itself is never realistically reached for either container, but the arithmetic leading up to it must still be widened to avoid silent precision loss on the wire-format values).

## Section 4: Robustness / Graceful Degradation
- **R-01** Circular IFD chains MUST be detected (visited-offset set); break, no infinite loop.
- **R-02** Circular sub-IFD references (Interop→IFD0) must not crash; return nil for back-pointer.
- **R-03** Any offset (header/next-IFD/value) outside stream → treat as absent; skip & continue; no crash.
- **R-04** offset + count×typeSize > len → skip entry; never slice past buffer.
- **R-05** Classic entry count is ≤ 65535; count×12 > remaining → read only entries that fit (partial IFD).
- **R-06** count×typeSize overflow MUST be checked with uint64 arithmetic, never uint32.
- **R-07** Overlapping IFDs must not crash (may produce duplicate/incorrect values, never corruption/panic).
- **R-08** Nikon Type 3 MakerNote: embedded TIFF header at MakerNote+10; internal offsets relative to that embedded base.
- **R-09** Fujifilm MakerNote starts "FUJIFILM"; offsets relative to MakerNote payload start.
- **R-10** Canon MakerNote: no signature; TIFF-absolute internal offsets.
- **R-11** Relocating a MakerNote with TIFF-absolute offsets makes them stale: library MUST preserve-in-place, fully rebase, or document the limitation.
- **R-12** Truncated after header before IFD0 → error (no panic); truncated mid-IFD → partial IFD.
- **R-13** Classic stream < 8 bytes / BigTIFF < 16 bytes always invalid; check min length first.
- **R-14 (BigTIFF write, task #264)** Round-trip fidelity: encoding an unmodified BigTIFF-provenanced EXIF and re-parsing it MUST decode every (Tag, Type, Count, Value) identically to the original, at every level of the IFD0→IFD1→… chain, for both real-world fixtures and the shared TIFF corpus.
- **R-15 (BigTIFF write)** All BigTIFF encode-side offset/size arithmetic is performed in uint64; because classic TIFF's implicit ceiling (offset fields are 32 bits wide, so ifdTotalSize saturates at math.MaxUint32) does not apply to BigTIFF's 64-bit fields, an explicit, documented, test-overridable sanity ceiling (maxBigTIFFEncodeSize) is checked before any allocation proportional to the computed size — never math.MaxUint32 saturation, and never an unchecked allocation.
- **R-16 (BigTIFF write)** Sub-IFD pointer tags and thumbnail pointer tags (fixed EXIF LONG fields, S-38) never silently receive a value ≥ 2^32 when encoding BigTIFF; Encode fails loudly with ErrBigTIFFPointerOverflow instead of truncating the pointer.
- **R-17 (BigTIFF write)** The MakerNote/IFD1 verbatim-preserve contract (classic-TIFF R-11) applies identically when the source is BigTIFF: MakerNote bytes and IFD1 (thumbnail) entries survive an Encode round-trip unchanged.
- **R-18 (BigTIFF container write, task #270)** Round-trip fidelity for standalone BigTIFF container write: extracting a real-world BigTIFF fixture (including one with SubIFDs and/or LONG8-typed strip/tile arrays), rewriting it via `tiff.Inject`/`tiff.InjectWithEXIF` with a metadata change that forces the copy-and-relocate path, and re-parsing the output MUST preserve every IFD0 tag value and every byte of every strip/tile/SubIFD image block untouched.
- **R-19 (BigTIFF container write)** MakerNote OOL rebasing (the classic-TIFF R-11 contract, implemented by `rebaseGenericMakerNote`) is a documented, tested, fail-safe no-op — never a corruption — when the outer container is BigTIFF (`EXIF.BigTIFF == true`). No known real-world file combines a Sony plain-IFD or Olympus OLYMP-type MakerNote (both predate BigTIFF) with a genuinely BigTIFF outer container; full BigTIFF-aware outer-container MakerNote rebasing is a tracked follow-up, not a corruption risk in the interim.

## Section 5: Real-World Deviations (handle gracefully)
1. Unsorted IFD entries (MUST sort/linear-search, not binary-search-on-unsorted).
2. Zero-denominator rationals as "unknown" sentinel → no divide-by-zero.
3. DateTime strings with spaces / partial fields → no panic, return ok=false.
4. ASCII tags with wrong type (ComponentsConfiguration as ASCII) → read raw bytes.
5. IFD entry count one too high (Canon 40D, Kodak) → skip phantom entry.
6. TIFF IPTC tag `0x83BB` declared LONG but written UNDEFINED/BYTE → accept all, return raw.
7. Out-of-spec ExifVersion count=8 → read min(4,count), return "0220".
8. Odd-byte-offset value data → read at declared offset regardless.
9. Ricoh 2-byte IFD padding → must not crash (parses garbage acceptable).
10. Make trailing spaces ("NIKON CORPORATION ") → TrimSpace before MakerNote dispatch.
11. Nikon Type 3 base = outerTIFFBase + makerNoteOffset + 10.
12. ExifVersion absent (early cameras/scanners) → ExifIFD non-nil, no parse failure.

## Sources
- CIPA DC-008-Translation-2023 (Exif 3.0); CIPA DC-X008-Translation-2019 (Exif 2.32)
- TIFF Revision 6.0 (Adobe, 1992); BigTIFF Design (libtiff.gitlab.io)
- ExifTool idiosyncrasies & MakerNotes.pm; Exiv2 makernote.html; LoC FDD000618
