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

## 2. PNG
**(a)** W3C "PNG Specification (Third Edition)", W3C Recommendation 24 Jun 2025; ISO/IEC
15948:2004; historical RFC 2083. XMP via XMP Part 3 §1.1.5.
**(b)** 8-byte signature `89 50 4E 47 0D 0A 1A 0A` at offset 0 (PNG-3 §5.2).
**(c)** Chunk = Length(u32 BE) + Type(4) + Data + CRC(u32 BE); Length counts only data
(PNG-3 §5.3); Length ≤ 2³¹−1. Type bytes ASCII letters; property bits = bit5 of each byte
(ancillary/private/reserved/safe-to-copy) (§5.4). IHDR first, IEND last; IDATs consecutive;
PLTE before first IDAT (§5.6, §11). CRC-32 poly `0xEDB88320` reflected, init `0xFFFFFFFF`, final
ones-complement, over Type+Data (not Length) (§5.5).
**(d)** EXIF → `eXIf` chunk; raw TIFF stream **with no `Exif\0\0` prefix** (PNG-3 §11.3.4.4).
XMP → `iTXt` keyword `XML:com.adobe.xmp`, compression 0, empty lang/translated keyword. Text →
`tEXt`/`zTXt`. IPTC → via XMP (no native chunk).
**(e)** Length = data length; recompute CRC over Type+Data; insert ancillary chunks between
IHDR and IEND (eXIf/text before IDAT recommended); preserve unknown safe-to-copy chunks.
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

## 5. HEIF / HEIC
**(a)** ISO/IEC 23008-12 (on ISO/IEC 14496-12). Reference: libheif, ExifTool QuickTime.
**(b)** ftyp major_brand ∈ {heic, heix, heim, heis, hevc, mif1, msf1} (or in compatible_brands).
**(c)** Inside `meta`: `hdlr` handler `pict`; `pitm` primary item ID; `iinf` entry_count + `infe`
per item (item_ID, type e.g. hvc1/Exif/mime/grid, name; FullBox v≥2); `iloc` item byte locations
(construction_method, base_offset, extents: extent_offset/extent_length); `iref` typed refs
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
`CMT4`=GPS; not the TIFF item model.
**(e)** Byte order fixed by header (no mixing); patch all dependent offsets when moving
(StripOffsets 0x0111, TileOffsets 0x0144, SubIFDs, ExifIFD, MakerNote internal). Absolute-offset
MakerNotes break if relocated — preserve in place or fix up. ORF/RW2: restore original magic on
write. CR2: preserve `CR 02 00` at offset 8. Even-byte alignment; tag-sorted IFDs.
**(f)** IFD cycles; backward next-IFD; count exceeding file; value offset past EOF; MakerNote
relative-vs-absolute; encrypted Sony tags; truncated MakerNote; ORF magic not in {IIRO,IIRS}
→ degrade to generic TIFF; RW2 0x0055 misread; CR3 missing/extra CMT* boxes, malformed
moov/uuid; BigTIFF RAW (8-byte offsets, 20-byte entries, u64 counts).

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
