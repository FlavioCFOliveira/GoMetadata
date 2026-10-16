# EXIF + TIFF 6.0 + BigTIFF — Conformance Test Contract

Normative-requirements checklist for the GoMetadata conformance test battery. Rule IDs
(`S-*` structural, `V-*` value-level, `R-*` robustness) are used verbatim as Go sub-test
names. Every rule is testable and cites its spec clause.

## Section 1: Exact Spec Identifiers

### 1.1 EXIF

| Document ID | Title | Org | Version | Year |
|---|---|---|---|---|
| CIPA DC-008-Translation-2026 | Exif Version 3.1 (**compliance target**) | CIPA / JEITA | 3.1 | 2026 |
| CIPA DC-008-Translation-2024 | Exif Version 3.0 (corrected edition) | CIPA | 3.0 (corr.) | 2024 |
| CIPA DC-008-Translation-2023 | Exif Version 3.0 | CIPA | 3.0 | 2023 |
| CIPA DC-X008-Translation-2019 | Exif Version 2.32 | CIPA / JEITA | 2.32 | 2019 |
| CIPA DC-008-Translation-2016 | Exif Version 2.31 | CIPA / JEITA | 2.31 | 2016 |

JEITA companion: **CP-3451** (CP-3451C = 2.3, CP-3451D = 2.31, CP-3451E = 2.32, CP-3451G = 3.0,
CP-3451H = 3.1). CIPA DC-008 and JEITA CP-3451 are technically identical; cite CIPA DC-008 as primary.

**Target.** GoMetadata targets Exif 3.1 (DC-008-2026) and keeps reading and writing 2.x and 3.0 files
(Exif 3.1 §4.2, p. 14: "Players should be able to play back files of versions earlier than the supported
standard"). Page numbers in this checklist are the printed page numbers of the Exif 3.1 document (the PDF
page is 11 higher). The local copy is described in `docs/specs/README.md`.

**EXIF 2.32 vs 3.0 vs 3.1 normatively significant differences**

| Feature | 2.32 | 3.0 | 3.1 |
|---|---|---|---|
| Text encoding | TypeASCII (2) only | ASCII tags may use UTF-8 ("ASCII or UTF-8", Exif 3.1 Table 6, pp. 31-32; Table 9, p. 45) | same |
| TypeUTF8 | undefined | code **129** (not 13), element size 1, NUL-terminated, no BOM, the count includes the NUL (Exif 3.1 §4.6.2, p. 28: "defined independently by this standard, rather than in TIFF6.0") | same |
| Code 13 | not an Exif type | not an Exif type: the TIFF IFD type (TIFF Tech Note 1; libtiff `TIFF_IFD`), 4 bytes | same |
| 2-byte or larger characters in ASCII-or-UTF-8 tags | recorded in the ASCII type | "shall" use UTF-8 as the Type (§4.6.5.4.1, p. 42; ImageDescription, Make, Model, Software, Artist, Copyright, and the Exif IFD tags of Table 9) | same |
| FlashpixVersion | mandatory in ExifIFD | optional (Table 18, p. 95) | optional |
| XResolution, YResolution, ResolutionUnit | mandatory in IFD0 | optional (Table 17, p. 93) | optional |
| ExifVersion value | "0220"/"0230"/"0232" | "0300" | "0300" (§4.6.6.1.1, p. 47; 3.1 has no "0310") |
| GPSVersionID | 2.3.0.0 | 2.4.0.0 | 2.4.0.0 (§4.6.7.1.1, p. 83) |
| Title, Photographer, ImageEditor, CameraFirmware, RAWDevelopingSoftware, ImageEditingSoftware, MetadataEditingSoftware | not defined | tags `0xA436`-`0xA43C` (ASCII or UTF-8) | same |
| LearningOptOutIn `0x9287` (UNDEFINED), DevelopmentType `0xA40D`, DevelopmentTypeDescription `0xA40E` (UTF-8 only), DistortionCorrection `0xA40F`, ChromaticAberrationCorrection `0xA410`, ShadingCorrection `0xA411`, NoiseReduction `0xA412` (SHORT) | not defined | not defined | new, all optional at every support level (§4.6.6.4.3, pp. 49-51; §4.6.6.7.47-52, pp. 72-74; Tables 9 and 18, pp. 45-46 and 95-96) |
| LightSource values 25-34 (daylight, day white, cool white, white, warm white light source; the same five as LED) | not defined | not defined | new (§4.6.6.7.20, pp. 60-61) |
| Annex K (original preservation image) | - | added | revised following the MPF revision of 2025; the tag description levels moved to K.2.13 |

"M" below is "shall be recorded", "N" "not allowed to record", "J" "not allowed (included in JPEG marker)"
(Exif 3.1 Table 17 notation, pp. 93-94).

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
| 7 | UNDEFINED | 1 | | 13 | IFD | 4 |
| 129 | UTF-8 (Exif 3.0/3.1) | 1 | | 16/17/18 | LONG8/SLONG8/IFD8 (BigTIFF only) | 8 |

— TIFF 6.0 §2 (1–12), TIFF Tech Note 1 (13), Exif 3.1 §4.6.2 p. 28 (129), BigTIFF (16–18). Unknown type
code MUST be skipped/preserved (treat 4 value bytes as raw, never dereference). 
- **S-19** RATIONAL = two u32 (num/den); SRATIONAL = two i32. Denominator 0 must not divide/crash.
- **S-20** ASCII MUST be NUL-terminated; count includes NUL; parser strips trailing NUL.
- **S-21 (Exif 3.0/3.1, task #320)** UTF-8 is type **129**: NUL-terminated UTF-8, element size 1, the count is the
  bytes including the NUL (Exif 3.1 §4.6.2, p. 28). An entry of type 129 parses as a string and the entry keeps
  its type and bytes. Type 13 is the 4-byte IFD type on every tag (TIFF Tech Note 1), never a string; an Exif-2.x
  parser treats 129 as unknown, no crash. (The constant was 13 before #320: a breaking change, see CHANGELOG.)
- **S-22 (BigTIFF)** types 16/17/18 valid only in BigTIFF; classic parser must not dereference as LONG8.

### 2.5 EXIF IFD Chain
- **S-23** IFD0 from header; ExifIFD from tag `0x8769` (LONG, or LONG8/IFD8 in BigTIFF). Absent → ExifIFD nil, no error.
- **S-24** GPS IFD from tag `0x8825` in IFD0; separate from ExifIFD.
- **S-25** Interop IFD from tag `0xA005` inside ExifIFD (not IFD0).
- **S-26** IFD1 (thumbnail) via IFD0 next-IFD pointer; main chain limited to IFD0+IFD1 for JPEG. — Exif §4.5.1.

### 2.6 Mandatory Tags
- **S-27 (ExifIFD)** ExifVersion `0x9000` UNDEFINED[4] ("0300" for Exif 3.x, e.g. "0232" in 2.x files; no NUL); FlashpixVersion `0xA000` (optional since 3.0); ColorSpace `0xA001` SHORT (1=sRGB, 0xFFFF=Uncalibrated); ComponentsConfiguration `0x9101`. — Exif 3.1 §4.6.6.1-3, pp. 47-49; Table 18, pp. 94-95.
- **S-28 (IFD0)** Make `0x010F`, Model `0x0110`, Orientation `0x0112`, XResolution `0x011A`, YResolution `0x011B`, ResolutionUnit `0x0128`, DateTime `0x0132`, YCbCrPositioning `0x0213`. — Exif 3.1 Table 6, pp. 31-32. Mandatory for a JPEG primary image is only YCbCrPositioning (and the Exif IFD pointer); the others are recommended or optional (Table 17, p. 93; see S-46).
- **S-29** DateTime `0x0132`/`0x9003`/`0x9004` ASCII[20] format `"YYYY:MM:DD HH:MM:SS\0"`; unknown fields → spaces. — Exif §4.6.4/§4.6.5.
- **S-30** SubSecTime `0x9290`/`0x9291`/`0x9292` ASCII decimal digit string.
- **S-31** OffsetTime `0x9010`/`0x9011`/`0x9012` ASCII `"+HH:MM\0"` (Exif 2.31+). — DC-X008-2019 §4.6.5.
- **S-32 (GPS)** GPSVersionID `0x0000` BYTE[4] = {2,4,0,0} (mandatory when any GPS tag is present; 2.3.0.0 in Exif 2.3 files, which are read as they are); GPSLatitudeRef `0x0001` ASCII "N/S"; GPSLatitude `0x0002` RATIONAL[3]; GPSLongitudeRef `0x0003` "E/W"; GPSLongitude `0x0004` RATIONAL[3]. — Exif 3.1 §4.6.7, Table 14, pp. 82-83 (§4.6.7.1.1, p. 83: "The version is given as 2.4.0.0").
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
  IFD (type 13 — TIFF Tech Note 1 / libtiff `TIFF_IFD`; Exif 3.0/3.1 UTF-8 is
  type 129, so the two tables of `exif/type.go` and `format/tiff/tiff.go` agree
  since #320), LONG8 (type 16,
  BigTIFF), or IFD8 (type 18, BigTIFF). The relocator decodes and re-encodes
  the pointer array correctly for all four widths, using format/tiff's own
  generic raw-IFD type table — see `enumerateSubIFDs`/`patchSubIFDPointers` in
  `format/tiff/relocate.go` and `format/tiff/relocate_bigtiff.go`. Fixtures:
  `testdata/corpus/tiff/metadata-extractor/BigTIFFSubIFD4.tif` (type 13),
  `BigTIFFSubIFD8.tif` (type 18).

### 2.9 Exif 3.1 target: text type, mandatory tags, IFD1 pointers, ExifVersion, thumbnail strips — **S-44..S-50** (tasks #320, #321, #319, #363, #368)

- **S-44** (#320) An entry of type 129 survives `Encode` with its type, count and value bytes unchanged, in classic
  TIFF and BigTIFF, in both byte orders. — Exif 3.1 §4.6.2, p. 28. `TestUTF8Type_S21_Code129IsUTF8`.
- **S-45** (#320) A setter of an "ASCII or UTF-8" tag (`SetCaption`, `SetCopyright`, `SetCreator`, `SetCameraModel`,
  `SetMake`, `SetLensModel`) writes TypeASCII for 7-bit text, and for any other text TypeUTF8 (129) with the count
  including the NUL; with UTF-8 it raises an ExifVersion lower than "0300" to "0300" (creating the Exif IFD when it
  is absent), and leaves "0300" or later as it is. ASCII text never touches the ExifVersion. — Exif 3.1 §4.6.5.4.1,
  p. 42 ("2-byte or larger character codes can not be used when ASCII is set as Type. When a 2-byte or larger code
  is necessary, UTF-8 shall be set as Type"); §4.2, p. 14 (the upper part of the version changes when an old reader
  may operate incorrectly because of the new specification). Policy: this is original behaviour, not ExifTool's
  (it writes ASCII with the UTF-8 bytes). `TestUTF8Type_S45_SettersChooseTheType`.
- **S-46** (#321) EXIF created by the library (`exif.New`, `Metadata.ensureEXIF`, a setter that creates an Exif IFD
  or GPS IFD) carries the tags that Exif 3.1 marks mandatory for its primary image: for a JPEG ("Compressed"
  column) IFD0 YCbCrPositioning = 1 (p. 93; §4.6.5.1.13, p. 38: default 1, centered) and the Exif IFD pointer; Exif
  IFD ExifVersion "0300" (p. 94; §4.6.6.1.1, p. 47), ColorSpace 0xFFFF Uncalibrated (p. 95; §4.6.6.2.1, p. 47: the
  colour space of a stream the library does not decode is not known), ComponentsConfiguration 1 2 3 0 (p. 94;
  §4.6.6.3.3, p. 49), PixelXDimension and PixelYDimension from the frame header of the JPEG (p. 95; §4.6.6.3.1-2,
  p. 48), and in a created GPS IFD GPSVersionID 2.4.0.0 (§4.6.7.1.1, p. 83). For any other primary image
  (uncompressed column: PNG, WebP, HEIF/AVIF) only ExifVersion and ColorSpace and the Exif IFD pointer are
  written: YCbCrPositioning, ComponentsConfiguration and PixelX/YDimension are "N" there. Divergence from ExifTool:
  ExifVersion "0300" (ExifTool writes "0232"); XResolution, YResolution and ResolutionUnit are not written (optional
  since Exif 3.0, Table 17, p. 93; ExifTool dropped them in 13.x as well). When the frame header cannot be read, PixelX/YDimension are left out, never guessed; a frame header that declares
  zero lines takes the height from the DNL segment after the first scan (JPEG-DNL-01, as in corpus `beach.jpg`). `TestMandatory_S46_*`, `TestWrite_Task321_*`,
  `TestCorpus_Task321_ExifToolSeesNoMissingTag`.
- **S-47** (#321) An IFD that exists is never completed: the mandatory tags are added only to an IFD the library
  creates. A parsed EXIF whose IFD0 or Exif IFD lacks mandatory tags keeps lacking them after a write.
  `TestMandatory_S47_ExistingIFDsAreNeverCompleted`, `TestWrite_Task321_ExistingEXIFIsNotCompleted`.
- **S-48** (#319) The Exif IFD pointer (`0x8769`) and the GPS Info IFD pointer (`0x8825`) are optional tags of the
  1st IFD (thumbnail), the Interoperability pointer is not defined there (Exif 3.1 Table 21, p. 99; §4.6.3.3.1,
  p. 29: a tag of the Exif IFD). `Encode` writes the pointer entries of an IFD of the chain after IFD0 so that
  each addresses a valid IFD: (1) an IFD of its own (`IFD.Sub`, parsed from an offset that differs from IFD0's) is
  written after the thumbnail and the entry points to it; (2) a pointer whose offset equals the IFD0 pointer of
  the same tag is rewritten to the block written for IFD0; (3) any other pointer, and any Interoperability pointer
  of the chain, is dropped. All offsets are even (TIFF 6.0 §2). Corpus: `Olympus μ Digital 800.JPG` (IFD1 is a
  copy of the primary tags with an Exif IFD of 23 entries and a GPS IFD of its own). `TestChain_S48_*`,
  `TestCorpus_Task319_Olympus800IFD1Pointers`.

- **S-49** (#363) A setter that writes a tag, or a tag type, that a later version of the standard introduced raises the
  ExifVersion of the file to the lowest version that defines it, and never lowers a higher one: `SetDateTimeOriginal`
  (OffsetTimeOriginal, Exif 2.31: "0231"), `SetLensModel` (LensModel, Exif 2.3: "0230"), the text setters with UTF-8
  (type 129, Exif 3.0: "0300", S-45). One table, `exif.minExifVersion`, built from the revision history of Exif 3.1
  (pp. 3-5) and the Exif IFD tag tables (Table 9, pp. 44-47; Table 18, pp. 94-96), checked against Exif 2.3 (DC-008-2012,
  Revision History pp. 3-4, whose tag table lists 0x8830-0x8835 and 0xA430-0xA435 and none of the later tags):
  0230 sensitivity tags `0x8830-0x8835` and camera/lens tags `0xA430-0xA435`; 0231 `0x9010-0x9012` (time offsets) and
  `0x9400-0x9405` (shooting situation); 0232 `0xA460-0xA462` (composite image); 0300 `0xA436-0xA43C` (title,
  photographer, editor, software tags) and the Exif 3.1 tags `0x9287`, `0xA40D-0xA412`. Exif 3.1 keeps "0300" (§4.6.6.1.1,
  p. 47). The rule of the file is §4.2, p. 14: the lower part of the version changes when a reader can skip the new
  data, the upper part when an old reader could misread it. A tag whose version the local texts do not state (Gamma
  `0xA500`, the 2.3 GPS tags; Exif 2.2 and 2.21 are not available locally) is not gated: an unlisted tag never raises
  the version. An ExifVersion that is not four ASCII digits cannot be compared and is replaced. `TestMinExifVersionTable`,
  `TestRequireExifVersionRaisesAndNeverLowers`, `TestVersionRaisedBySetters`,
  `TestWrite_Task363_SetDateTimeOriginalRaisesExifVersion`.

- **S-50** (#368) An uncompressed thumbnail of the 1st IFD keeps its image data on a write. Exif 3.1 §4.5.8, pp. 25-26:
  thumbnails are recorded in the 1st IFD, "when thumbnails are recorded in uncompressed format, they are to be recorded
  in the 1st IFD in conformance with Baseline TIFF Rev. 6.0 RGB Full Color Images or TIFF Rev. 6.0 Extensions YCbCr
  Images", and an uncompressed thumbnail is possible with a compressed primary image (Table 3, p. 26); Table 21,
  pp. 98-99: StripOffsets (`0x0111`), RowsPerStrip and StripByteCounts (`0x0117`) are mandatory for it; §4.6.5.2.1-3,
  pp. 39-40: SHORT or LONG, one value per strip (StripsPerImage, or SamplesPerPixel x StripsPerImage for planar data).
  `Parse` with the option `exif.CarryThumbnailStrips` reads the strips of every IFD after IFD0 into `IFD.Sub.ThumbnailStrips`
  (in strip order, whatever their order in the source), and `Encode` and `EncodeRebased` write them after the IFD, each
  on an even offset (TIFF 6.0 §2), and set StripOffsets as a LONG array of the new positions; StripByteCounts is
  unchanged. `Read` gives the option for every container whose EXIF stream is encoded again on its own (JPEG, PNG, WebP,
  HEIF/AVIF, CR3), and not for a TIFF-based file, whose writer relocates the image data of every IFD itself and would
  write the strips twice. The strips are carried when they lie inside the parsed stream, are at most 4096, and total at
  most 1 MiB; otherwise `Parse` adds the warning "thumbnail image data not carried: ..." and the IFD is written as it
  was. A JPEG APP1 segment is limited to 64 KB (JPEG-EXIF limit): a thumbnail that does not fit makes the write fail
  with `ErrEXIFPayloadTooLarge`, never silently drop it. The Kodak ProBack of the corpus (156x111 RGB, one strip of
  51948 bytes at offset 4944; the EXIF shrank from 56.9 KB to 5 KB and StripOffsets was stale) now keeps the strip byte
  for byte. Reference: Apache Commons Imaging `TiffOutputDirectory.getOutputItems` (lines 757-794) and
  `TiffOutputSummary.updateOffsets` (lines 63-72) rewrite StripOffsets as a LONG array of the final positions (design
  only, Apache-2.0). `TestThumbnailStrips_S50_ParseAndRoundTrip`, `TestThumbnailStrips_S50_BigTIFFAndSubIFDs`,
  `TestThumbnailStrips_S50_NotCarried`, `TestThumbnailStrips_S50_JPEGThumbnailAndDefault`,
  `TestWrite_Task368_KodakProBackThumbnailStrips`, `TestWrite_Task368_StripsOfEveryContainer`, `FuzzParseEXIF`.

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
- **V-16 (Exif 3.1 tags, task #320)** The tags new in Exif 3.1 are registered with the names and types of the standard: LearningOptOutIn `0x9287` UNDEFINED (§4.6.6.4.3, pp. 49-51), DevelopmentType `0xA40D` SHORT, DevelopmentTypeDescription `0xA40E` UTF-8 only, DistortionCorrection `0xA40F`, ChromaticAberrationCorrection `0xA410`, ShadingCorrection `0xA411`, NoiseReduction `0xA412` SHORT (§4.6.6.7.47-52, pp. 72-74). All are optional ("O") in every column of Table 18 (pp. 95-96). Unknown tags are carried as they are. `TestMandatory_V16_Exif31Tags`.
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
- **R-11** Relocating a MakerNote with TIFF-absolute offsets makes them stale: library MUST preserve-in-place, fully rebase, or document the limitation. The TIFF-family writers rebase after `exif.Encode`; `exif.EncodeRebased` (JPEG, PNG, WebP, HEIF/AVIF) rebases and carries data (R-20..R-26).
- **R-12** Truncated after header before IFD0 → error (no panic); truncated mid-IFD → partial IFD.
- **R-13** Classic stream < 8 bytes / BigTIFF < 16 bytes always invalid; check min length first.
- **R-14 (BigTIFF write, task #264)** Round-trip fidelity: encoding an unmodified BigTIFF-provenanced EXIF and re-parsing it MUST decode every (Tag, Type, Count, Value) identically to the original, at every level of the IFD0→IFD1→… chain, for both real-world fixtures and the shared TIFF corpus.
- **R-15 (BigTIFF write)** All BigTIFF encode-side offset/size arithmetic is performed in uint64; because classic TIFF's implicit ceiling (offset fields are 32 bits wide, so ifdTotalSize saturates at math.MaxUint32) does not apply to BigTIFF's 64-bit fields, an explicit, documented, test-overridable sanity ceiling (maxBigTIFFEncodeSize) is checked before any allocation proportional to the computed size — never math.MaxUint32 saturation, and never an unchecked allocation.
- **R-16 (BigTIFF write)** Sub-IFD pointer tags and thumbnail pointer tags (fixed EXIF LONG fields, S-38) never silently receive a value ≥ 2^32 when encoding BigTIFF; Encode fails loudly with ErrBigTIFFPointerOverflow instead of truncating the pointer.
- **R-17 (BigTIFF write)** The MakerNote/IFD1 verbatim-preserve contract (classic-TIFF R-11) applies identically when the source is BigTIFF: MakerNote bytes and IFD1 (thumbnail) entries survive an Encode round-trip unchanged.
- **R-18 (BigTIFF container write, task #270)** Round-trip fidelity for standalone BigTIFF container write: extracting a real-world BigTIFF fixture (including one with SubIFDs and/or LONG8-typed strip/tile arrays), rewriting it via `tiff.Inject`/`tiff.InjectWithEXIF` with a metadata change that forces the copy-and-relocate path, and re-parsing the output MUST preserve every IFD0 tag value and every byte of every strip/tile/SubIFD image block untouched.
- **R-19 (BigTIFF container write)** MakerNote OOL rebasing (the classic-TIFF R-11 contract, implemented by `rebaseGenericMakerNote`) is a documented, tested, fail-safe no-op — never a corruption — when the outer container is BigTIFF (`EXIF.BigTIFF == true`). No known real-world file combines a Sony plain-IFD or Olympus OLYMP-type MakerNote (both predate BigTIFF) with a genuinely BigTIFF outer container; full BigTIFF-aware outer-container MakerNote rebasing is a tracked follow-up, not a corruption risk in the interim.

### 4.1 MakerNote repair for the containers that hold the EXIF stream (tasks #317, #318, #362, #364, #369, #370) — **R-20..R-36**

The MakerNote (tag `0x927C`) is vendor data (Exif 3.1 §4.6.6.4.1, p. 49); `exif.EncodeRebased(e, src)` is the encoder
for JPEG APP1, PNG `eXIf`, WebP `EXIF` and HEIF/AVIF items. Layouts: `EXIF.MakerNoteLayout` (metadata-extractor
`ExifTiffHandler.processMakernote`), which the data corrects (R-24). Reference model: ExifTool `RebuildMakerNotes`
(`WriteExif.pl`) and Exiv2 `TiffIfdMakernote::doWrite` re-serialise the MakerNote at its new position with a fixup
(study only; no code copied).

- **R-20** For every layout (Canon, Sony, Panasonic, Olympus `OLYMP\0`, Nikon Type 2, Olympus `OLYMPUS\0`) every
  out-of-line value of the MakerNote IFD reads the same after the write as before, in JPEG, PNG, WebP and HEIF
  (the 4-byte `exif_tiff_header_offset` prefix of a HEIF item is not part of the TIFF stream the offsets count
  from). `TestMakerNoteRebase_R20_ValuesFollowTheMove`, `TestWrite_Task317_MakerNoteFollowsTheMove`,
  `TestWrite_Task317_HEIFPrefixIsNotPartOfTheStream`, `TestCorpus_Task317_NoNewMakerNoteWarning` (every corpus
  file with a MakerNote: no ExifTool MakerNote warning class that the input did not draw).
- **R-21** (#318) A value that the IFD addresses after the declared end of the MakerNote (Olympus C-720UZ) is
  carried, after the end of the blob, with its pointer set: the 49 values `exiftool -G1 -a -s -MakerNotes:all`
  shows are identical. `TestMakerNoteRebase_R21_CarryDataOutsideTheNote`,
  `TestCorpus_Task318_ExifToolReadsTheSameMakerNote`.
- **R-22** (#318) The Nikon `PreviewIFD` (tag `0x0011`) and `NikonScanIFD` (`0x0E10`), an IFD that crosses the
  declared end of the note, and the preview image they address outside it (Coolpix P7000: 36,892 bytes 17 KB past a
  4.7 KB note) are carried with their offsets; the same sub-IFD rule covers the Olympus sub-IFDs `0x2010`-`0x2050`,
  `0x3000` and the Ricoh `0x2001` sub-directory. `TestMakerNoteRebase_R22_NikonPreviewIFD`,
  `TestCorpus_Task318_NikonPreviewImageIsCarried` (JPEG, WebP; `exiftool -b -PreviewImage` is byte-equal).
- **R-23** Canon's footer (`II*\0` or `MM\0*` and the TIFF offset of the MakerNote) is set to the new position; a
  note whose pointers and footer disagree with its position is repaired from the footer's origin when the last
  value then ends where the footer starts (ExifTool `FixBase`), and the pointers follow.
  `TestMakerNoteRebase_R23_CanonFooter`.
- **R-24** The data decides how a note counts its offsets: Sanyo notes of the corpus hold TIFF offsets and Samsung NX
  notes offsets from the note, against the layout table. `TestMakerNoteRebase_R24_TheDataDecidesTheBase`.
- **R-25** A TIFF-relative note with a value that straddles its end has wrong pointers (ExifTool: "Possibly
  incorrect maker notes offsets"): nothing is carried and every pointer follows the note by the same distance.
  `TestMakerNoteRebase_R25_InconsistentPointers`.
- **R-26** Without a MakerNote, with a note that moves unchanged, or with a source stream that no longer holds the
  note, `EncodeRebased` equals `Encode` or repairs only the offsets inside the note; it never modifies `e` or `src`;
  the data carried is bounded by the source size and 16 MiB. `TestMakerNoteRebase_R26_NoSourceNoMove`,
  `FuzzParseEXIF`.

- **R-27** (#362) The Olympus MakerNotes `OLYMPUS\0` and `OM SYSTEM\0` store, in CameraSettings (sub-IFD `0x2020`),
  PreviewImageStart (`0x0101`) and PreviewImageLength (`0x0102`): the start counts from the first byte of the MakerNote
  and addresses a JPEG preview after the end of the image (corpus `Olympus E-M1`: MakerNote at file offset 3632,
  PreviewImageStart 10976976, preview at 0xa78d00 = 3632 + 10976976; ExifTool `Olympus.pm`, tags `0x101` and `0x102`
  with IsOffset and DataTag PreviewImage: study only). `exif.ExternalOffsets` finds the field, and `jpeg.Inject`
  rewrites it for the position the MakerNote and the preview have in the output: one pass over the segments before the
  first scan header (what the new metadata segments take, what is dropped, where the old EXIF was) gives the movement of
  everything after it, and the rewritten AFCP and FotoStation trailers before the preview add their growth. Only an offset
  that addresses a JPEG SOI after the first scan header and inside the file is rewritten; a value the caller changed, a
  source without EXIF, and a preview inside a rewritten trailer are left as they are. A stream without an Olympus
  MakerNote pays one single-byte search. In the corpus the five bodies E-M1, E-M5, TG-850, SH-1, SP-100EE (ten files with
  their copies) read `exiftool -b -PreviewImage` byte-equal after the write and draw no "PreviewImageStart+PreviewImageLength
  runs past end of file". `TestExternalOffsets_X1`, `TestExternalOffsets_X2`, `TestExternalOffsets_X3_Corpus`,
  `TestExternalOffsets_O1_PreviewFollowsTheWrite`, `TestExternalOffsets_O5_Corpus`,
  `TestWrite_Task362_OlympusPreviewFollowsTheWrite`, `TestCorpus_Task362_ExifToolReadsThePreviewAndTheKodakIFD`,
  `TestCorpus_Task317_NoNewMakerNoteWarning` (class "preview offset"), `FuzzInjectExternalOffsets`.
- **R-28** (#362) IFD0 tag `0x8290` (ExifTool `Exif.pm` "KodakIFD", "used in various types of Kodak images", a SubIFD:
  study only) is a LONG that holds the offset of a standard IFD with vendor tags; the Kodak ProBack of the corpus has
  KodakVersion, TextualInfo (94 bytes) and HostSoftware tags with their values after the other EXIF data. It is not a
  MakerNote, but ExifTool lists it in the `MakerNotes` group. `EncodeRebased` carries the IFD and the values it
  addresses after the end of the stream, points the entry at the copy, clears the next-IFD offset of the copy and keeps
  every offset even (TIFF 6.0 §2). An entry that does not address a plausible IFD of the source (1-255 entries, types
  1-13, inside the source) is written as `Encode` writes it. `exiftool -G1 -a -s -MakerNotes:all` of the ProBack output
  equals the input's. `TestPrivateIFD_K1_KodakIFDIsCarried`, `TestPrivateIFD_K2_NotAnIFD`,
  `TestPrivateIFD_K3_NextIFDPointerIsCleared`, `TestPrivateIFD_K5_Corpus`.
- **R-29** (#362) An Interoperability pointer (`0xA005`) that addresses IFD0 or the Exif IFD is a loop, not an IFD: the
  parser reads no Interoperability IFD from it (ExifTool: "InteropIFD pointer references previous IFD0 directory") and a
  write drops the pointer instead of writing a second copy of IFD0 as the Interoperability IFD. The corpus Kodak
  ProBack points it at IFD0 (offset 8). `TestPrivateIFD_K4_InteropLoop`.
- **R-30** (#364) A headerless Nikon MakerNote (Coolpix 775, E990, E5000, D1: the IFD starts at the first MakerNote byte,
  offsets from the TIFF header) is read in the byte order of its entry count: a count of 1-255 in exactly one of the two
  orders (it is below 256 in an order only when its high byte is 0, so both qualify only for 0, which is refused). The
  note was read as big-endian only, so the little-endian notes of most cameras had no entries. metadata-extractor sends
  the note through `TiffReader.processIfd`, whose byte-swap test is the same idea. Corpus: `Nikon E990` 16, `E5000` 21,
  `Coolpix 775` 18 entries, equal to the `MakerNotes directory with N entries` of `exiftool -v3`.
  `TestNikonHeaderless_N1_EitherByteOrder`, `TestNikonHeaderless_N2_Refused`, `TestNikonHeaderless_N3_ParseOfTheStream`,
  `TestRead_Task364_HeaderlessLittleEndianNikon`.

- **R-31** (#370) The other forms of the Olympus preview offset that `exif.ExternalOffsets` reports, with `Base` (the byte
  the offset counts from), `MNStart`/`MNLen` (the MakerNote that holds the field) and `Target()`: (1) the pair
  `0x1036` PreviewImageStart / `0x1037` PreviewImageLength in the MakerNote IFD itself, and the read-only pair `0x0088` /
  `0x0089` (ExifTool `Olympus.pm`, IsOffset with an OffsetPair; metadata-extractor `OlympusMakernoteDirectory.java`
  lines 216-218 names `0x1035-0x1037`; Exiv2 `olympusmn_int.cpp` line 292: study only); (2) CameraSettings stored as a
  VALUE (tag `0x2020` UNDEFINED, the entries of the IFD are the value, the E-1 and E-300; ExifTool: "Olympus really
  screwed up the format of the following subdirectories (for the E-1 and E-300 anyway)"); (3) the `OLYMP\0` layout,
  whose offsets count from the TIFF header (`Base` 0), where the offset may address bytes inside the MakerNote
  (corpus `OlympusE1.jpg`: PreviewImageStart 2556, PreviewImageLength 26, the text "<Dummy preview image data>" inside a
  MakerNote of 5816 bytes at TIFF offset 1002). Exiv2 `preview.cpp` line 299 reads `Exif.OlympusCs.PreviewImageStart` with
  `Exif.MakerNote.Offset` as the base, as here. A field outside the MakerNote, a length of 0 and a type other than LONG
  are not reported. `jpeg.Inject` rewrites such an offset by where its data lands: (a) inside the MakerNote: it follows
  the MakerNote (an offset counted from the MakerNote does not change, one counted from the TIFF header changes by the
  distance the MakerNote moved); (b) inside a segment before the first scan header that the write copies unchanged:
  it follows the segment (`measureShift` records the output position of each kept segment); (c) a JPEG stream after the
  first scan header and inside the file: as in R-27. `TestExternalOffsets_X4`, `TestExternalOffsets_R31_E1PreviewInsideTheMakerNote`,
  `TestExternalOffsets_R31_SegmentBeforeTheScan`, `TestExternalOffsets_R31_CorpusE1`, `TestWrite_Task370_OlympusJPEGCorpus`,
  `FuzzParseEXIF`.
- **R-32** (#370) Which IFD0 pointers `EncodeRebased` carries as private IFDs (R-28 generalised). The value of an unknown
  LONG is a number, so the rule is closed: only an entry that is certain to address an IFD is carried, that is, an entry of
  type IFD (13: Adobe PageMaker 6.0 TIFF Technical Notes, TIFF Tech Note 1, "IFD = 13"; libtiff `TIFF_IFD`; any tag), or a
  LONG with a tag that ExifTool `Exif.pm` marks `Flags => 'SubIFD'` with a table of its own: `0x014A` SubIFDs (TIFF/EP and
  DNG, "MaxSubdirs => 10"), `0x0190` GlobalParametersIFD, `0x8290` KodakIFD, `0x888A` LeafSubIFD, `0xFE00` KDC_IFD
  (study only). Up to 10 offsets per entry, each carried separately (an array entry is rewritten element by element). The
  Exif IFD, GPS and Interoperability pointers, which `Encode` writes itself, are outside the rule. An entry whose offsets
  do not address a plausible IFD (1-255 entries, types 1-13, inside the source) is written as `Encode` writes it, as is an
  entry of any other tag and type. The corpus has only `0x8290` (the Kodak ProBack); the others are tested with
  synthetic streams. `TestPrivateIFD_R32_Registry`, `TestPrivateIFD_R32_ExifAndGPSPointersAreNotCarried`.
- **R-33** (#370) An Olympus preview offset whose data cannot be placed is left as it is, and the write succeeds: the data is
  in a segment that the write drops or replaces (an XMP APP1 segment), in the EXIF stream outside the MakerNote (the stream
  is encoded again), inside a trailer that the write rewrites (AFCP, FotoStation: the new position of bytes inside a
  rewritten trailer is not known), or outside the file (the truncated corpus `Olympus E-20.jpg`: PreviewImageStart 2419558
  with a file of 2411039 bytes; ExifTool reports "Error reading PreviewImage from file" for the source). Corpus
  `corrupted.jpg` (PreviewImageStart 1899600 from the MakerNote at TIFF offset 2020) keeps its preview.
  `TestExternalOffsets_R33_LeftAsItIs`, `TestWrite_Task370_OlympusJPEGCorpus`.
- **R-34** (#370) The Olympus preview offset in the TIFF family (TIFF, DNG, CR2 and ORF write through
  `relocateTIFFFromParsed`, and the ORF `OLYMP\0` path through `orfRelocateWithOLYMP`). In every ORF of the corpus (E-M5,
  E-M10, E-PM1, E-PL1, E-PL7, E-P3, E-30, E-410, E-600, XZ1, TG-4, TG-6, TG-7, OM-1) the MakerNote is large (1.4 MB for the
  E-M5) and holds the preview (E-M5: MakerNote at 3264, PreviewImageStart 48960, preview at 52224 inside it), so it moves
  with the MakerNote and `exiftool -b -PreviewImage` was already equal after a write. The offset can address three other
  places, each now placed (`relocate_olympus_external.go`): inside an image block (it moves with the block); between the
  IFDs and the end of the last image block and outside every block (the preview is carried as a block of its own, as for
  the `ThumbnailImage` of an `OLYMP\0` MakerNote, and the offset points at the copy; the bytes must start with the JPEG SOI
  marker where they can be read, ISO/IEC 10918-1 §B.1.1.3); after the last block, in the tail (it moves with the end of the
  last block). A value whose data straddles the end of the MakerNote or a block, lies inside the metadata structures or
  outside the file is left. Not detected: an AFCP or FotoStation trailer that starts inside the metadata structures moves the
  start of the tail. `TestRelocateOlympusExternal_R34`, `TestRelocateOlympusExternal_R34_LeftAsItIs`,
  `TestWrite_Task370_OlympusORFCorpus`, `FuzzTIFFInject`, `FuzzORFInject`.
- **R-35** (#369) The Samsung Galaxy Note 4 (corpus `Samsung SM-N910F (Galaxy Note 4).jpg`): the MakerNote (98 bytes, 7
  entries, `MakerNoteVersion` "0100") is byte-identical after a write. `exiftool -G1 -a -MakerNotes:all` lists five more
  lines for the output than for the input, and the cause is in the SOURCE: the camera stores the IFD0 entries out of tag order
  (ExifTool: "Entries in IFD0 are out of order", Make `0x010F` after the Exif IFD that holds the MakerNote), and ExifTool
  decides the format of a MakerNote from the Make it has already read (`MakerNotes.pm`, `MakerNoteSamsung2`: `uc $$self{Make}
  eq 'SAMSUNG'`), so in the source it finds an unknown note ("MakerNoteUnknown", the `Unknown_0x0001...` tags), and in the
  output, where IFD0 is in ascending tag order (TIFF 6.0 §2, p. 15), the same bytes decode as Samsung Type2. The write is
  right and keeps the order that the specification requires; the report cannot be met literally without writing an
  out-of-order IFD0. The oracle is the number of entries that ExifTool finds, `MakerNotes directory with 7 entries` of
  `exiftool -v3`, equal before and after. `TestWrite_Task369_SamsungNote4MakerNote`.
- **R-36** (#370) The pointers inside the sub-IFDs of an `OLYMP\0` MakerNote follow it in the TIFF family: Equipment,
  CameraSettings, RawDevelopment, ImageProcessing and FocusInfo (`0x2010-0x2050`, `0x3000`) stored as UNDEFINED values
  (the E-1 and the compacts) hold entries whose out-of-line values and sub-IFD offsets count from the TIFF header, so they
  change when the MakerNote moves; the pointers of the top IFD were rebased by the distance and the ones inside the
  sub-IFDs were not, which made ExifTool read ColorMatrix2, CoringValues, the settings and CMExposureCompensation of the
  corpus `Olympus SP350.orf`, `SP500UZ.orf` and `C8080.orf` (and their copies) wrongly after a write.
  `exif.(*EXIF).RebaseMakerNoteSubIFDs` sets them from the walk of the MakerNote that `EncodeRebased` uses
  (`makernote_walk.go`); `rebaseGenericMakerNote` and the ORF path call it. `exiftool -G1 -a -s -MakerNotes:all` of those
  files is equal before and after, apart from the tags that hold a position or a size. `TestRebaseMakerNoteSubIFDs_R36`,
  `TestRelocateOlympusSubIFD_R36`, `TestWrite_Task370_OlympusORFSubIFDValues`.

Known limits (not corrected by a write): a MakerNote in a BigTIFF (R-19); an Olympus offset whose data is in a segment the
write drops, in the EXIF stream outside the MakerNote or in a trailer the write rewrites (R-33); an IFD0 LONG of a tag that
is not in the registry of R-32, which is a number to the library; the pointers of the sub-IFDs of the MakerNote layouts of
other makers in the TIFF family (R-36 is for `OLYMP\0`); the Olympus `FE190/X750` TIFF of the corpus
(`ce50e53224a3b62bb821bc294bf6a588.tif`), whose `OLYMP\0` MakerNote counts its offsets from the MakerNote while the layout
table says the TIFF header (ExifTool reads it with "Possibly incorrect maker notes offsets (fix by 369478?)"): the TIFF
writer rebases it as TIFF-relative.

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
- CIPA DC-008-Translation-2026 (Exif 3.1, target); CIPA DC-008-Translation-2024 (Exif 3.0); CIPA DC-X008-Translation-2019 (Exif 2.32)
- TIFF Revision 6.0 (Adobe, 1992); BigTIFF Design (libtiff.gitlab.io)
- ExifTool idiosyncrasies & MakerNotes.pm; Exiv2 makernote.html; LoC FDD000618
