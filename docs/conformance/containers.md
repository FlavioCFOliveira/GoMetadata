# Container & RAW Formats — Conformance Test Contract

Normative-requirements checklist for the GoMetadata conformance test battery, covering every
container/RAW format. For each: (a) spec identity, (b) detection rule, (c) structure
assertions, (d) EXIF/XMP/IPTC embedding, (e) write byte-correctness, (f) robustness cases.
Detection rules were validated against the project's own `format/detect.go`.

## 1. JPEG (JIF / JFIF / Exif-JPEG)
**(a)** ITU-T T.81 | ISO/IEC 10918-1:1994; JFIF ITU-T T.871 | ISO/IEC 10918-5:2013. EXIF via
CIPA DC-008; XMP via Adobe XMP Part 3; IPTC via Photoshop APP13 + IIM 4.2.
**(b)** SOI `FF D8` at offset 0 (T.81 §B.1.1.3).
**(c)** Markers `FF xx` (xx ≠ 00, ≠ FF); `FF 00` = stuffed zero in entropy data, not a marker.
SOI first, EOI `FF D9` last. Standalone markers (SOI/EOI/RSTn/TEM) carry no length. Segment
markers (APPn/COM/DQT/DHT/SOFn/SOS/DRI) followed by 2-byte BE length `Lp` that **counts itself
but not the marker**; payload = Lp−2; Lp ≥ 2. Single segment ≤ 65533 payload bytes. SOS entropy
data has no length — scan to next non-stuffed marker; metadata precedes SOS. Multiple APP1
legal (Exif + XMP + ExtendedXMP).
**(d)** EXIF → APP1, id `45 78 69 66 00 00` ("Exif\0\0") then TIFF header; cannot span segments.
XMP → APP1, id `http://ns.adobe.com/xap/1.0/\0`. ExtendedXMP → APP1, id
`http://ns.adobe.com/xmp/extension/\0` + 32-byte GUID + uint32 BE fullLength + uint32 BE offset.
IPTC → APP13, `Photoshop 3.0\0` + 8BIM IRBs, resource `0x0404`.
**(e)** APPn length = payload+2, ≤ 65535; preserve unmodified segments; new APPn before SOS;
never rewrite entropy/stuffing; ExtendedXMP fullLength/offset/GUID/HasExtendedXMP consistent;
EXIF write must not exceed 65533 (cannot span) — fail rather than truncate.
**(f)** Truncated after SOI; missing EOI; Lp > remaining; Lp < 2; APPn past EOF; multiple
`Exif\0\0` (use first); ExtendedXMP missing/dup/overlapping chunks, GUID mismatch; malformed IRB
length/padding; fill bytes (`FF FF…`).

**Rule IDs (JPEG splice write).** (tasks #400, #403) `Inject` copies the source and changes only the metadata
segments; the scan (`format/jpeg/splice.go`) reads the segment headers before the first scan header and nothing after.
- **JFIF-01** A new metadata segment of a kind the source has none of goes after the JFIF APP0 segment (ITU-T T.871 | ISO/IEC
  10918-5 §6.3, p. 3: it "shall immediately follow the SOI marker"), after the JFIF extension APP0 segments that follow it
  (§10.2, p. 7) and after a leading Adobe APP14 segment; EXIF, then XMP, then IPTC. A source with none of them gets the segment
  right after SOI. A JFIF segment that is not first, or that follows another segment, is not moved or passed over.
  `TestSplice_Placement/JFIF-*`, `TestTask400_ModifiedWriteKeepsJFIFFirst` (Canon_40D after `SetISO`: APP0, APP1, APP2, DQT),
  `TestTask400_Corpus_JFIFStaysFirst`.
- **PLACE-01** A new segment replaces the first old segment of its kind where it is, and the other old segments of the kind
  are dropped; a new XMP follows the EXIF of the source and a new IPTC follows the XMP, else the EXIF (CIPA DC-008-2026
  §4.5.4, p. 20, and Table 4, p. 22: APP1 immediately after SOI, which a file with a JFIF APP0 cannot satisfy together with
  T.871 §6.3; JFIF wins, as in ExifTool, and the Exif APP1 stays the first APP1); a nil payload removes the old segments. `TestSplice_Placement/PLACE-*`,
  `TestSplice_KeepInPlace`.
- **JPEG-IDENT** Every other byte of the source is copied as it is: segment order, fill bytes (ISO/IEC 10918-1 §B.1.1.2), other
  APPn segments, tables, the scan header, the image data (never parsed) and what follows EOI; the fill bytes before a dropped
  segment go with it. `TestSplice_ByteIdentity`, `TestTask403_Corpus_JPEGSpliceMetadataless` (377 metadata-less corpus
  JPEGs: no error, output without the metadata segments equals the source without them, a rewritten AFCP/FotoStation trailer
  excepted), `FuzzJPEGSplice`.
- **JPEG-DAMAGE** A source whose segments cannot be followed before the first scan header (no marker prefix, a length below 2, a
  segment past the end of the file, a cut header) is copied from the first byte that cannot be read; the metadata is added before
  it. A source that does not start with SOI is refused and nothing is written (`ErrNotJPEG`); more than 65536 separate runs of
  segments to leave out are refused (`ErrTooManySegments`). `TestSplice_DamagedSource`, `TestSplice_TooManyRuns`.
- **R-27 (layout)** The Olympus preview offset follows the new layout: the position of the new EXIF stream and of the segments
  before the scan header is computed from the steps of the write. `TestSplice_ExternalOffsetsLayout`, `TestExternalOffsets_*`.

**Rule IDs (JPEG trailers; sub-test names).**
- **TRAILER-SAMSUNG-01** (task #353) The Samsung "Sound & Shot" trailer ends with a QDIO block (`QDIO`,
  101, 1, the absolute offsets of the start and the end of its audio file, all little-endian, then the block
  length 20, `QDIO` and `BS`) that follows a `SEFH`/`SEFT` directory block. When a write moves the trailer, the
  two offsets are rewritten for its position in the output, so they name the audio file again; the directory's
  offsets are relative to it and need no rewrite. Whatever precedes or follows the trailer (an AFCP trailer, a
  Vivo trailer, unknown bytes) is found in the region after EOI and does not matter. Layout from the format notes
  of ExifTool `Samsung.pm` `ProcessSamsung` (study only; written independently).
  `TestTrailers_SamsungQDIO`, `FuzzTrailers`.
- **TRAILER-SAMSUNG-02** A write that does not move the trailer keeps the file byte for byte; a QDIO block that is
  not understood (offsets outside the region after the other trailers and before the block, no directory block) is
  never touched. `TestTrailers_SamsungUnchanged`.
- **TRAILER-SAMSUNG-03** `testdata/corpus/jpeg/exiftool/ExifTool.jpg` (trailer chain: AFCP, FotoStation x2, CanonVRD,
  PhotoMechanic, MIE, Samsung, Vivo): after a caption write the two audio offsets name the audio file again
  (`exiftool -a -G1 -Samsung:all` identical before and after), exactly 4 bytes of the file differ from a write
  that does not rewrite them, and every trailer after the AFCP and FotoStation trailers is carried unchanged.
  `TestTrailers_CorpusSamsung`.
- **TRAILER-TAIL-01** (task #354) `FindTail` finds AFCP and FotoStation trailers from the end of a file (a
  FotoStation record by its footer, an AFCP trailer by its EOF record), in either order, from a buffer or a
  stream; a file that does not end with one has none. `TestFindTail` (internal/trailer), `FuzzTrailerTail`.
- **TRAILER-PLAN-01** A plan writes its region with the IPTC copies replaced and the offsets set for the position it
  is written at; the bytes between and after the trailers are copied. `TestPlanWriteRegion`.
- **TRAILER-SAMSUNG-04** (task #361) The Samsung audio file, the blocks after it and the QDIO block move as one rigid
  piece, so the two offsets are shifted by the movement of the QDIO block and name the same audio bytes: an AFCP or
  FotoStation trailer after the Samsung trailer (a file edited by software that appends AFCP) no longer stops the
  rewrite, as it did when the offsets had to lie after every other trailer. An AFCP or FotoStation trailer that
  intersects the piece (found among the audio bytes, between the audio and the QDIO block) is part of the audio file as
  far as the Samsung trailer is concerned: it is copied unchanged, not rewritten, because rewriting it could change the
  size of the piece; no producer known to the format notes (ExifTool `Samsung.pm` `ProcessSamsung`, which derives the
  audio position from the directory of blocks, study only) writes such a layout, and a write has no warning channel
  (`ParseWarnings` is read-only). An audio start before the region after the image is not understood and the QDIO block is
  left alone. `TestTrailers_SamsungWithOtherSpans`, `FuzzTrailers` (a Samsung trailer valid in the source is valid and
  names the same bytes in the output).
- Trailers that need no rewrite (checked against the ExifTool sources, study only): CanonVRD (header and footer hold
  the length of the data), PhotoMechanic (the footer holds the trailer length), Vivo and OnePlus (offsets counted back
  from the end of the trailer), MIE (relative lengths). They are copied byte for byte.

**Rule IDs (JPEG frame size).**
- **JPEG-DNL-01** (task #365) A frame header with Y = 0 defers the number of lines to the DNL segment (marker `FF DC`)
  that follows the first scan. ITU-T T.81 §B.2.2, p. 36 (Y: "Value 0 indicates that the number of lines shall be defined
  by the DNL marker and parameters at the end of the first scan"); §B.2.5, p. 45 (the segment is mandatory when Y is 0;
  Ld, NL; Table B.10: Ld 4-65535, NL 1-65535); §B.2.1, p. 35 (it "shall immediately follow the first scan"). The text
  was read in the W3C copy of the Recommendation (itu-t81.pdf). `jpeg.ImageSize` reads the segments to the first SOS,
  skips the scan header, and searches the entropy-coded data of the first scan for the next marker that is not a stuffed
  zero, a restart marker or a fill byte (§B.1.1.5), reading at most 64 MiB; the marker must be DNL with Ld >= 4 and
  NL >= 1, else the height is unknown (`ErrNoFrameHeader`). Nothing is decoded. libjpeg-turbo (`jdmarker.c` `get_sof`)
  rejects Y = 0 ("DNL not supported") and has no search to adapt; metadata-extractor `JpegDnlReader` takes NL as the
  first unsigned 16-bit value of the segment and applies it only when the frame header's height is 0. Corpus
  `beach.jpg` (SOF1, Y 0, X 200, NL 200): a caption write creates PixelXDimension and PixelYDimension 200 x 200.
  `TestImageSizeDNL`, `TestScanForDNLLimit`, `TestImageSizeDNLCorpus`, `TestWrite_Task365_JPEGHeightFromDNL`,
  `FuzzImageSize`.

## 2. PNG
**(a)** W3C "PNG Specification (Third Edition)", W3C Recommendation 24 Jun 2025; ISO/IEC
15948:2004; historical RFC 2083. XMP via XMP Part 3 §1.1.5.
**(b)** 8-byte signature `89 50 4E 47 0D 0A 1A 0A` at offset 0 (PNG-3 §5.2).
**(c)** Chunk = Length(u32 BE) + Type(4) + Data + CRC(u32 BE); Length counts only data
(PNG-3 §5.3); Length ≤ 2³¹−1. Type bytes ASCII letters; property bits = bit5 of each byte
(ancillary/private/reserved/safe-to-copy) (§5.4). IHDR first, IEND last; IDATs consecutive;
PLTE before first IDAT (§5.6, §11). CRC-32 poly `0xEDB88320` reflected, init `0xFFFFFFFF`, final
ones-complement, over Type+Data (not Length) (§5.5).
**(d)** EXIF → `eXIf` chunk; raw TIFF stream **with no `Exif\0\0` prefix** (PNG-3 §11.3.4.5, eXIf: "Only one eXIf chunk is allowed").
XMP → `iTXt` keyword `XML:com.adobe.xmp`, compression 0, empty lang/translated keyword. Text →
`tEXt`/`zTXt`. IPTC → via XMP (no native chunk).
**(e)** Length = data length; recompute CRC over Type+Data; insert ancillary chunks between
IHDR and IEND (eXIf/text before IDAT recommended); preserve unknown safe-to-copy chunks.
**PNG-EXIF-01** (task #325) A write that emits the eXIf chunk drops a `Raw profile type exif` or `APP1` text chunk (tEXt, zTXt or iTXt) whose decoded profile is the EXIF it replaces (PNG-3 §11.3.4.5: one eXIf chunk), so one current copy remains; an undecodable profile, a profile of another kind and a nil EXIF payload leave the chunk byte-identical. `TestInject_RawProfileEXIFDropped`, `TestInject_RawProfileKeptWhenNotSuperseded`, `TestInject_RawProfilesAllKinds`. (`Raw profile type iptc` is rewritten in place: task #314.)
**Rule IDs (PNG splice write).** (tasks #400, #403)
- **PNG-PLACE** A new eXIf or XMP iTXt chunk replaces the first old chunk of its kind where it is and the other old chunks of
  the kind are dropped; a kind the file has none of goes right after IHDR (eXIf and iTXt may appear anywhere between IHDR and
  IEND, PNG-3 §11.3.4.5 and §11.3.4.4). A nil payload drops the old chunk in `Inject`, as before; `InjectKeeping` keeps it (PNG-KEEP). `TestInjectPlacement`, `TestScanOldMetadata`.
- **PNG-KEEP** (task #405) `InjectKeeping` copies the chunks of the segments in its `Keep` (every eXIf and EXIF raw profile chunk,
  every XMP chunk, every IPTC raw profile chunk) as they are, CRC included, and writes no chunk for them; the others are replaced or
  removed as `Inject` does. `Write` uses it for every segment it does not modify, so one modified segment never removes another.
  `TestInjectKeeping`, `TestTask405_PNGKeepsUnmodifiedChunks`.
- **PNG-IDENT** Every other byte is copied as it is: a chunk with a wrong CRC, unknown chunks, and the bytes after IEND.
  `TestInjectVerbatim`, `TestTask403_Corpus_PNGSpliceMetadataless` (667 metadata-less corpus PNGs), `FuzzPNGSplice`.
- **PNG-DAMAGE** After IHDR a chunk that cannot be followed (header cut short, declared length past the end of the file or above
  2^31-1) is copied with the rest of the file, the metadata is added, and no IEND is invented; before IHDR the same is refused,
  as is an IEND with no IHDR before it (`ErrNoImageHeader`): a file whose first chunk is not IHDR has no place for metadata (the
  26 such corpus files, for example `m1-593d4b1a0b5d13b539c6c098dc5797ca.png`, first chunk `vHDR`). `TestInjectDamaged`.
- **PNG-IEND** A file that ends at a chunk boundary without IEND receives one (PNG-3 §5.6: IEND is last): this is the 12-byte
  growth of `m6-1f97f040d0b6b26faeb0a1a7f1499590.png` (856 bytes; last chunk `IFND`). `TestInjectIEND`, `TestTask403_PNG_M6GrowsByIEND`.
**(f)** Bad signature; truncated chunk; Length past EOF; CRC mismatch (detect, no crash);
Length > 2³¹−1; zero-length data (legal); duplicate IHDR/IEND; chunks after IEND; bad iTXt
zlib/separators; non-UTF-8 XMP; bad eXIf TIFF header.

## 3. WebP (RIFF)
**(a)** Google "WebP Container Specification"; IETF RFC 9649 (Nov 2024, normative).
**(b)** `RIFF` at [0:4], u32 LE size at [4:8], `WEBP` at [8:12] (RFC 9649 §2.4).
**(c)** RIFF header `'RIFF'` + u32 LE File-Size + `'WEBP'`; File-Size = bytes after the size
field (i.e. includes the 4-byte `WEBP`), excludes the leading `RIFF`+size (§2.4). Generic chunk
= FourCC + u32 LE Chunk-Size + payload; Chunk-Size excludes FourCC/size/padding (§2.3). Odd
Chunk-Size → exactly 1 padding byte `0x00`, not counted (§2.3). VP8X flags byte (MSB-first):
Rsv,Rsv,ICC,Alpha,EXIF,XMP,Anim,Rsv (§2.5.1). EXIF chunk ⇒ flag E set; XMP chunk ⇒ flag X set.
Reconstruction chunks ordered; metadata/unknown may be out of order (after image data).
**(d)** EXIF → `EXIF` chunk (`45 58 49 46`), raw TIFF, **no `Exif\0\0` prefix**. XMP → `XMP `
chunk (`58 4D 50 20`, 4th byte is space `0x20`), raw packet. IPTC → via XMP. Metadata requires
VP8X (extended) format.
**(e)** Adding EXIF/XMP requires VP8X + set flag; Chunk-Size = exact payload; pad byte iff odd;
update RIFF File-Size; reserved bits 0; ICCP before image data; do not reorder reconstruction chunks.
A VP8X added to a simple-format file takes its canvas (u24 LE width−1, height−1) from the VP8/VP8L
bitstream header and sets Alpha when the image has alpha (VP8L `alpha_is_used`, or an `ALPH` chunk);
libwebp rejects a canvas that differs from the image size (`VP8_STATUS_BITSTREAM_ERROR`).
**WEBP-01** (task #333) An `EXIF` chunk that starts with `Exif\0\0` is read as the TIFF stream after the identifier (metadata-extractor `WebpRiffHandler`); a write emits the TIFF stream without the identifier, as checklist item (d) of this section requires. `TestExtract_ExifPreambleAndXMPNul`.
**WEBP-02** A chunk with the non-standard FourCC `XMP\0` is read as XMP; the standard `XMP ` chunk wins when both exist. `TestExtract_ExifPreambleAndXMPNul`.
**WEBP-03** A write emits exactly one `EXIF` and one `XMP ` chunk (RFC 9649 §2.7.1.5: at most one of each) and no `XMP\0` chunk, with the VP8X flags to match; a nil XMP payload removes the `XMP\0` chunk too. `TestInject_OneStandardChunkEach`, `TestCorpus_WebPExifPreambleXMPNul`.
**(f)** File-Size mismatch; Chunk-Size past EOF; odd size missing/non-zero pad; chunk present but
flag unset (read lenient, write correct); truncated VP8X; `XMP` without trailing space; duplicate
metadata chunks; metadata before image data.

## 4. ISO Base Media File Format (ISO BMFF)
**(a)** ISO/IEC 14496-12.
**(b)** No fixed offset-0 signature; `ftyp` box near start: `66 74 79 70` at [4:8], brand at [8:12].
**(c)** Box = size(u32 BE) + type(4cc) + body (§4.2). size==1 → 8-byte largesize(u64) follows
type; size==0 → box to EOF (last box only); size 2–7 invalid. `uuid` type → 16-byte UUID follows.
FullBox adds version(1)+flags(3). `ftyp` = major_brand + minor_version(u32) + compatible_brands[]
(§4.3). `meta` is a FullBox (§8.11.1) nesting hdlr/iinf/iloc/iref/pitm/idat/dinf. Bound child
iteration by parent size.
**(d)** Mechanism only (meta box + items); concrete EXIF/XMP mapping from HEIF/MIAF. IPTC via XMP.
**(e)** Box size = total incl. header (or largesize/size==0); recompute ancestor sizes when inner
content changes; patch iloc/stco/co64 offsets when boxes move; 4cc exactly 4 bytes.
**(f)** size 2–7; size past EOF; largesize overflow; deep nesting (recursion guard); child larger
than parent; box-vs-FullBox confusion; duplicate ftyp.

**Rule IDs (ISO BMFF boxes).**
- **BMFF-OPENENDED-01** (task #359) A box whose 32-bit size field is 0 extends to the end of its container (ISO/IEC
  14496-12 §4.2); a box appended after it would be read as part of it. `internal/bmff` is the one helper of the HEIF/AVIF
  and CR3 writers: `OpenEnded` (size 0, or a 64-bit form with largesize 0, which the readers of this module also take
  as extending to the end), `Close` (a copy with the explicit size, 32-bit or, above 4 GiB, the 64-bit header) and
  `CloseInPlace`. Reference: libavif `src/stream.c` `avifRWStreamFinishBox` writes the size once the payload is complete
  and refuses a box above UINT32_MAX (BSD-2; the logic here is original). `TestOpenEndedAndClose`,
  `TestCloseAbove32Bits`.

## 5. HEIF / HEIC
**(a)** ISO/IEC 23008-12 (on ISO/IEC 14496-12). Reference: libheif, ExifTool QuickTime.
**(b)** ftyp major_brand ∈ {heic, heix, heim, heis, hevc, mif1, msf1} (or in compatible_brands).
**(c)** Inside `meta`: `hdlr` handler `pict`; `pitm` primary item ID; `iinf` entry_count + `infe`
per item (item_ID, type e.g. hvc1/Exif/mime/grid, name; FullBox v≥2); `iloc` item byte locations
(construction_method, data_reference_index — present in every iloc version, ISO/IEC 14496-12
§8.11.3.2 — base_offset, extents: extent_offset/extent_length); `iref` typed refs
(dimg/thmb/cdsc); `iprp`/`ipco`/`ipma` item properties (ispe/colr/hvcC/pixi/irot).
**(d)** EXIF item: `infe` item_type `Exif`; payload via iloc is an ExifDataBlock = **4-byte u32 BE
`exif_tiff_header_offset`** then EXIF (`Exif\0\0`+TIFF or directly TIFF). XMP item: `infe`
item_type `mime`, content_type `application/rdf+xml\0`; payload = raw packet. EXIF/XMP linked to
primary image via `cdsc` iref. IPTC via XMP.
**(e)** Add item = new infe (inc entry_count) + iloc extent + cdsc iref; EXIF payload carries the
4-byte prefix; when item bytes move, patch ALL iloc offsets + ancestor box sizes (canonical
corruption risk); content_type exactly `application/rdf+xml\0`.
**(f)** iloc extent past EOF; construction_method variants; zero extents; EXIF item missing
4-byte prefix or bad offset; iinf entry_count mismatch; missing cdsc; multiple EXIF items;
truncated meta (CRITICAL HEIF panic — fuzz the meta/iloc/iinf walk).

**Rule IDs (HEIF / AVIF write path; sub-test names).**
- **HEIF-BOX-LARGESIZE-01** The `meta` box, and the `moov` box that may wrap it, may use the 64-bit
  largesize header (size field 1, then the type, then a 64-bit size: 16 bytes). The meta content starts
  after the header plus the 4 FullBox bytes; a rewrite keeps the header form of the source and patches
  the size of an enclosing box in its own form. A meta box smaller than its header + 4 is not a FullBox.
  ISO/IEC 14496-12 §4.2 (box header), §8.11.1 (`meta` FullBox).
- **HEIF-BOX-LARGESIZE-02** (task #348) Every box content offset in the HEIF reader and writer comes from the box
  header length, 8 or 16 bytes, never from a constant: the `infe` boxes of an `iinf` box may use the 64-bit
  largesize header too, and are read identically to their 8-byte form. Reference: libavif `src/read.c`
  `avifParseItemInfoBox` (each `infe` is read through its box header), `src/stream.c`
  `avifROStreamReadBoxHeaderPartial`; ISO/IEC 14496-12 §4.2. `TestTask348ParseIinfInfeHeaderForms`,
  `TestTask348ExtractAndInjectLargesizeInfe`, and the audit gate `TestTask348NoHardCodedBoxHeaderLength`, which
  fails on any `pos+8` content offset in the package outside the box header parser.
- **HEIF-META-SELECT-01** (task #349) `Extract` and `Inject` use one rule to select the metadata container, in
  `findMetaBoxAbs`: the first file-level `meta` box in file order; only when the file has no file-level `meta` box,
  the first `meta` box that is a direct child of the first `moov` box. A file-level `meta` box that is present but too
  small to be a FullBox is the selected one and is not skipped in favour of a nested one, so both paths always agree
  (`Extract` finds nothing and `Inject` returns `ErrNoMetadataContainer`). A `meta` box nested deeper (`trak`,
  `udta`), which `Extract` used to find by a depth-first search while `Inject` never wrote to it, is not selected.
  Rationale: libavif (`src/read.c` `avifParse`: the file-level `meta` box is the item container, `moov` is parsed for
  tracks), libheif (`libheif/file.cc` `HeifFile::parse_heif_file`: `m_meta_box` is the file-level box) and heic-rs
  (`src/meta/mod.rs`) read only the file-level box; ISO/IEC 14496-12 §8.11.1 also allows a `meta` box in `moov`, which
  is kept as the fallback so that a movie-level-only file reads and writes as before. Corpus: all 41 HEIF/AVIF files have
  a file-level `meta` box and none has one in `moov`, so no corpus result changes. Clause status: §4.2 and §8.11.1 of
  ISO/IEC 14496-12 are cited as in the existing checklist; the ISO/IEC 23008-12 clause that requires the file-level
  `MetaBox` for image items is NOT verified (the ISO texts are not available locally).
  `TestTask349ExtractInjectSelectSameMeta` (both placements in both orders, and beyond the 64 KB `Extract` window),
  `TestTask349MalformedTopLevelMetaIsNotSkipped`, `TestTask349NestedDeeperThanMoovIsNotSelected`,
  `TestTask349CorpusSelectsTopLevel`.
- **HEIF-ILOC-WIDTH0-01** An `iloc` `offset_size` or `length_size` of 0 is legal (the sizes are taken from
  {0, 4, 8}; the absent field reads as 0). A write widens a zero width to 4 bytes, keeping every existing
  extent value, and stores the new item locations. ISO/IEC 14496-12 §8.11.3.3. Reference: libavif
  `src/write.c` writes both sizes as 4.
- **HEIF-WRITE-NODROP-01** `Inject` never returns nil without storing a requested EXIF or XMP payload: it
  writes it or returns `ErrNoMetadataContainer` (no valid `meta` box) or `ErrUnreadableItemLocations`
  (`iloc` missing, unsupported version or field size, truncated, or too large to rewrite without dropping
  items). Only a request with no payload copies the file through unchanged.
- **HEIF-IINF-COUNT-01** (task #358) The `entry_count` of an `iinf` box is 16 bits for FullBox version 0 and 32 bits for
  any other version (ISO/IEC 14496-12 §8.11.6, the ItemInfoBox; the sub-clause is not cited because the text of the
  standard is not available locally). Reference: libavif `src/read.c` `avifParseItemInfoBox` (version 0: 16 bits;
  version 1: 32 bits; others rejected) and heic-rs `src/meta/iinf.rs`. `parseIinf` read 16 bits for every version below 2,
  so it could not read what `buildIinfBox` writes for a version 1 box. The `iloc` `item_count` keeps its own rule (16 bits
  below version 2, §8.11.3.2) in `parseIlocItemCount`. `TestTask358IinfEntryCountWidth`,
  `TestTask358ParseIinfItemCountTable`, `TestTask358IlocItemCountWidth`, `TestTask358RoundTripIinfVersion1`.
- **HEIF-BOX-OPENENDED-01** (task #359) `Inject` closes a child of `meta`, `iinf` or `iref` whose size field is 0 (or a
  largesize of 0) before it appends boxes after it, in the 32-bit and the 64-bit forms, through
  `bmff.Close` (BMFF-OPENENDED-01): the appended `iinf`, `iref`, `iloc` and `infe` boxes are read as boxes of their own
  and every item re-reads. `FuzzHEIFInject` no longer exempts such sources, `iinf` version 1, or (task #367) a source whose
  top-level boxes end in bytes that do not parse as a box. `TestTask359InjectClosesOpenEndedChild`,
  `TestTask359OpenEndedChildContentKept`, `FuzzHEIFInject`.
- **HEIF-TRAILING-BYTES-01** (task #367) Bytes after the last top-level box that do not parse as a box (a header whose size
  exceeds what follows or is below its own length, a 64-bit size that does not fit, fewer than 8 bytes) are kept byte for
  byte at the very end of the file, and `Inject` writes the new `mdat` box where the last real box ends, before them
  (ISO/IEC 14496-12 §4.2: a file is a sequence of boxes, so a new box starts on a box boundary). Appended after them, the
  new box header completed a bogus file-level `meta` header (for example a trailing `00 00 00 20 'meta'`) that reached
  into the new box and hid the `meta` box inside `moov`: `Extract` read nothing. An item extent that points into the
  trailing bytes lies outside every box and is not shifted. `Extract` keeps its tolerance: it stops at the first bytes that
  are not a box and uses what it found. Other readers: libavif `src/read.c` `avifParse` (line 4912: a top-level header that
  does not parse is `AVIF_RESULT_BMFF_PARSE_FAILED`, after the earlier boxes were handled), libheif `libheif/file_layout.cc`
  (lines 105-125: once `meta`, `mini` or `moov` was found, a short tail is accepted; a header that does not parse is an
  error) and heic-rs `src/boxes.rs` `BoxIter` (yields the earlier boxes, then an error); all three are stricter, none
  reads a `meta` box out of the trailing bytes. `TestTask367TrailingBytesKept`, `TestTask367NoTrailingBytesUnchanged`,
  `FuzzHEIFInject`.

## 6. AVIF
**(a)** AOM "AV1 Image File Format (AVIF)" v1.2.0 (on HEIF + MIAF ISO/IEC 23000-22). Ref: libavif.
**(b)** ftyp major_brand `avif` (still) or `avis` (sequence); often also mif1/miaf/MA1B.
**(c)** Same meta/iinf/infe/iloc/iref/iprp mechanism as HEIF; coded image item type `av01`;
required properties ispe/pixi/colr/av1C; primary via pitm; MIAF brand constraints.
**(d)** Same as HEIF: EXIF `infe` item_type `Exif` + 4-byte prefix; XMP `infe` `mime`
`application/rdf+xml`; linked via cdsc. IPTC via XMP.
**(e)** Same as HEIF §5e + maintain MIAF brands in ftyp.
**(f)** Same as HEIF §5f + `avis` sequences; `av01` with malformed `av1C`; missing required props.

## 7. DNG (Adobe Digital Negative)
**(a)** Adobe "Digital Negative (DNG) Specification, v1.7.1.0, Sep 2023" (on TIFF 6.0 + TIFF/EP).
**(b)** Standard TIFF magic (II*\0 / MM\0*) — NOT distinguishable by header alone; definitive
marker = `DNGVersion` tag `0xC612` in IFD0.
**(c)** TIFF 8-byte header (BigTIFF `0x002B` allowed). `DNGVersion` (0xC612) 4 bytes (e.g.
1,7,0,0); `DNGBackwardVersion` (0xC613). IFD0 = reduced-res ("IFD with thumbnail",
NewSubFileType `0x00FE`==1); full-res raw in a SubIFD (tag `SubIFDs` 0x014A, NewSubFileType==0).
`UniqueCameraModel` (0xC614) ASCII required.
**(d)** EXIF via pointer tag `0x8769` from IFD0; GPS via `0x8825`. XMP via TIFF tag `0x02BC`
(700), type BYTE/UNDEFINED. IPTC legacy via tag `0x83BB` (Photoshop IRB/IIM) and/or in XMP.
**(e)** File-absolute u32 (or u64 BigTIFF) offsets; moving IFDs/values requires patching all
offsets + StripOffsets/TileOffsets; entries sorted by tag; out-of-line > 4 bytes (TIFF) / > 8
(BigTIFF) at even offset; preserve SubIFD chain + DNGVersion; do not corrupt raw strips/tiles.
**(f)** IFD offset cycles/self-referential SubIFDs; count exceeding bound; offset past EOF;
DNGVersion malformed/absent; BigTIFF DNG; consistent byte order across pointers; truncated tag 700.

## 8. TIFF/EP and Proprietary RAW (NEF, ARW, CR2, CR3, ORF, RW2)
**(a)** TIFF/EP: ISO 12234-2:2001; TIFF 6.0 baseline; BigTIFF (Aware Systems/libtiff). Proprietary
RAW reverse-engineered via ExifTool (Canon.pm/Nikon.pm/Sony.pm/Olympus.pm/PanasonicRaw.pm),
LibRaw/dcraw, lclevy CR2 (lclevy.free.fr/cr2/) and CR3 (github.com/lclevy/canon_cr3).
**(b)** Detection (matches `format/detect.go`):
- TIFF/EP base: `49 49 2A 00` (II*\0) / `4D 4D 00 2A` (MM\0*).
- NEF: standard TIFF; `Make`==`NIKON CORPORATION`/`Nikon` in IFD0.
- ARW: standard TIFF (LE); `Make`==`SONY` in IFD0.
- CR2: TIFF LE + marker `CR` (`43 52`) at byte 8, then `02 00` (offsets 10–11).
- ORF: non-standard magic `IIRO` (`49 49 52 4F`) or `IIRS` (`49 49 52 53`); also MMOR/MMRO.
- RW2: non-standard magic `IIU\0` (`49 49 55 00`) — magic value `0x0055` not `0x002A`.
- CR3: ISO BMFF; ftyp major_brand `crx ` (`63 72 78 20`).
**(c)** TIFF/EP uses SubIFDs: full-res raw in a SubIFD, IFD0 = reduced-res (inverse of plain
TIFF). IFD layout shared with TIFF 6.0 §2 (sorted entries; ≤4 inline; else even-aligned offset).
TIFF/EP adds raw tags (CFAPattern 0x828E, CFARepeatPatternDim 0x828D, …) + EXIF pointer 0x8769.
Proprietary deviations to tolerate: Nikon Type-3 TIFF-in-TIFF (`Nikon\0`+header), Sony
encrypted/obfuscated tags, ORF/RW2 non-standard magic (patch to `2A 00` for the walk, restore on write).
**(d)** EXIF via 0x8769; GPS via 0x8825; MakerNote 0x927C. XMP via tag `0x02BC` (700). IPTC via
tag `0x83BB` and/or XMP. CR3 (BMFF): Canon `CMT1`=IFD0, `CMT2`=EXIF IFD, `CMT3`=MakerNote,
`CMT4`=GPS, each a complete TIFF stream; XMP in the top-level `uuid` box
`be7acfcb-97a9-42e8-9c71-999491e3afac`; not the TIFF item model.
**(e)** Byte order fixed by header (no mixing); patch all dependent offsets when moving
(StripOffsets 0x0111, TileOffsets 0x0144, SubIFDs, ExifIFD, MakerNote internal). Absolute-offset
MakerNotes break if relocated — preserve in place or fix up. ORF/RW2: restore original magic on
write. CR2: preserve `CR 02 00` at offset 8. Even-byte alignment; tag-sorted IFDs.
**(f)** IFD cycles; backward next-IFD; count exceeding file; value offset past EOF; MakerNote
relative-vs-absolute; encrypted Sony tags; truncated MakerNote; ORF magic not in {IIRO,IIRS}
→ degrade to generic TIFF; RW2 0x0055 misread; CR3 missing/extra CMT* boxes, malformed
moov/uuid; BigTIFF RAW (8-byte offsets, 20-byte entries, u64 counts).

**Rule IDs (CR3; sub-test names).**
- **CR3-CMT2-READ-01** CMT2 is a stand-alone TIFF stream whose first IFD is the Exif IFD (with the
  Interoperability IFD behind its `0xA005` pointer); it is parsed into the EXIF model's Exif IFD, in the
  byte order of CMT1. CMT1 holds no `ExifIFD` pointer. Reference: lclevy canon_cr3 (CMT2 = Exif IFD);
  imagemeta `readCrxMoovBox`; ExifTool (`ExifIFD directory with N entries` read from CMT2).
- **CR3-CMT2-WRITE-01** A write stores the Exif IFD in CMT2 (replaced when changed, kept byte for byte
  when unchanged, created after CMT1 when missing) and never as a second Exif IFD inside CMT1.
- **CR3-CMT4-DELETE-01** Deleting the GPS IFD removes the CMT4 box (as ExifTool's `-GPS:all=` does; Canon
  firmware writes a CMT4 holding only `GPSVersionID` when there is no fix, so an empty IFD would still read
  as GPS present). An unchanged GPS IFD keeps CMT4 byte for byte. A CMT4 that the reader does not expose
  (no CMT1, unreadable, other byte order) is kept. The same rule removes CMT2 when the Exif IFD is deleted.

- **CR3-WRITE-NODROP-01** (task #347) `Inject` never returns nil without storing a requested EXIF or XMP payload:
  it writes it or returns `ErrNoMetadataContainer` (no `moov` box, which also matches `ErrNoMoovBox`; or, for EXIF, no
  Canon uuid box in `moov`, the only carrier of CMT1-CMT4) and writes nothing. XMP needs no Canon box: it goes to the
  top-level XMP uuid box. Only a request with no EXIF and no XMP copies the file through (IPTC is never stored in a
  CR3). Creating the Canon uuid box is not attempted. ExifTool `WriteQuickTime.pl` also reports an error in these
  cases (study). A Canon uuid box whose last child has a size field of 0 ("to the end of the container", ISO/IEC
  14496-12 §4.2) gets an explicit size before a CMT box is appended, so the appended box is not read as part of it
  (found by `FuzzCR3Inject`). `TestTask347InjectNeverReturnsNilWithoutWriting`,
  `TestTask347NothingRequestedIsVerbatim`, `TestTask347OpenEndedChildKeepsAppendedCMT1`, `TestCR3RobustNoMoovRefused`,
  `TestTask347CR3WriteWithoutCanonBoxIsRefused` (root, corpus file without its Canon uuid box), `FuzzCR3Inject`.

**Rule IDs (NEF; sub-test names).**
- **NEF-CAPTURE-OFFSETS-01** (task #355) MakerNote tag `0x0E0E` ("NikonCaptureOffsets", written by Nikon Capture) holds
  `"0100"`, an entry count (2) and 12-byte entries (identifier (4), offset (4), 4 bytes of unknown meaning) in the
  byte order of the MakerNote. Identifiers 1, 2 and 3 are copies of the offsets of IFD0, the Nikon PreviewIFD and the
  first SubIFD. A NEF write moves them, so each is rewritten to the new position of the structure it names, when its
  value equals the structure's position in the source; unknown identifiers and the 4 unknown bytes are carried
  unchanged. The PreviewIFD moves with the MakerNote value (it lies inside the extended value the writer builds).
  ExifTool does not update these offsets (`Nikon.pm`, study); this is original policy.
  `TestTask355PatchNikonCaptureOffsets` (all four byte-order combinations).
- **NEF-CAPTURE-OFFSETS-02** A value that does not start with `0100`, is truncated, names no structure of the source,
  or sits in a MakerNote that is not Nikon Type 3 is carried unchanged. `TestTask355PatchLeavesWhatItDoesNotUnderstand`.
- **NEF-CAPTURE-OFFSETS-03** Corpus finding: in all 9 NEF corpus files that carry the tag
  (`raw/exiftool/Nikon.nef`, `raw/metadata-extractor/Issue 247-*.nef`) the values name no structure of their own
  file (the files are pruned, or were edited after Nikon Capture wrote them), so a write carries them unchanged; a
  copy of `Nikon.nef` whose values are made consistent follows the structures (`exiftool -NikonCaptureOffsets`
  shows the new offsets). `TestTask355CorpusNEF`.

**Rule IDs (TIFF-family write: bytes after the data; sub-test names).**
- **TIFF-TAIL-01** (task #360) A TIFF-family write carries the tail of the source: the bytes after the last byte that
  any IFD addresses (the image blocks, and the IFDs, sub-IFDs and out-of-line values of the metadata, found by the
  extent scanner of `Extract`) are written after the last block, unchanged. It covers a trailer that nothing
  addresses (unknown or private data appended by another writer) through the whole-file and the streaming path.
  ExifTool `ExifTool.pm` (trailer handling, study) and Apache Commons Imaging `TiffImageWriterLossless` (keeps
  unknown bytes; Apache-2.0, study) do the same in spirit. `TestTask360UnknownTrailerIsCarried`.
- **TIFF-TAIL-02** An AFCP trailer that another trailer follows is found (the tail is scanned forward, not only
  from the end of the file) and rewritten as in TIFF-AFCP-01; the bytes after it are carried unchanged. A malformed
  trailer is carried byte for byte, never rewritten. `TestTask360AFCPFollowedByAnotherTrailer`,
  `TestTask354TIFFTrailerEdgeCases`.
- **TIFF-TAIL-03** Bytes between addressed regions, and the IFDs and values of the source that the write
  regenerates (a source whose IFD follows the image data, as libtiff writes it), are not carried: they are exactly
  what the write replaces, and carrying them would keep stale metadata. A tail larger than 16 MiB is carried
  without the trailer rewrite. `TestTask360StaleStructuresAreNotCarried`.
- **TIFF-TAIL-04** A file that ends with its image data, or with its structures, is written as before: nothing is
  appended, and the scan is skipped when the last image block ends the file. `TestTask360NoTailIsNoChange`.
- **JPEG-SIZE-01** (task #321) `jpeg.ImageSize` returns the dimensions of the first frame header (SOF0-SOF15 but not
  DHT/JPG/DAC; ITU-T T.81 §B.2.2), skipping each segment by its length, and `ErrNoFrameHeader` at SOS/EOI, at the end
  of the stream, for a zero height, or for a damaged stream. `TestImageSize`, `FuzzImageSize`.

## Cross-cutting: EXIF payload-prefix matrix (high-value test)
| Container | EXIF location | Prefix before TIFF header |
|---|---|---|
| JPEG | APP1 `FFE1` | `Exif\0\0` (6 bytes) |
| PNG | `eXIf` chunk | none |
| WebP | `EXIF` chunk | none |
| HEIF/AVIF | `Exif` item via iloc | 4-byte u32 `exif_tiff_header_offset` then typically `Exif\0\0`+TIFF |
| DNG/TIFF/EP RAW | EXIF IFD via tag 0x8769 | n/a (in-file pointer) |
| CR3 | `CMT2` box | TIFF IFD directly |

XMP is always the raw RDF/XML packet, located by: APP1 `http://ns.adobe.com/xap/1.0/\0` (JPEG),
iTXt `XML:com.adobe.xmp` (PNG), `XMP ` chunk (WebP), `mime`/`application/rdf+xml` item
(HEIF/AVIF), TIFF tag 0x02BC (TIFF/DNG/RAW). IPTC is native only in JPEG APP13 (8BIM 0x0404) and
TIFF tag 0x83BB; elsewhere it rides inside XMP.

## Sources
- ITU-T T.81 / T.871; W3C PNG 3rd ed. (2025); RFC 9649 + Google WebP Container Spec
- ISO/IEC 14496-12; ISO/IEC 23008-12; AOM AVIF v1.2.0; ISO/IEC 23000-22 (MIAF)
- Adobe DNG 1.7.1.0; ISO 12234-2 (TIFF/EP); TIFF 6.0; BigTIFF (Aware Systems)
- ExifTool; LibRaw/dcraw; lclevy CR2 & CR3; libheif; libavif; Exiv2
