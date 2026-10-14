# Third-Party Notices

GoMetadata is licensed under the MIT License (see [`LICENSE`](LICENSE)). Some
source files adapt data or code from the permissively licensed open-source
projects listed below. Each adapted file carries a header comment that names
its source (see [`docs/THIRD_PARTY.md`](docs/THIRD_PARTY.md)), and each project
has one entry here. The full licence texts are in [`LICENSES/`](LICENSES/),
named by SPDX identifier.

`go test` fails when a `.go` file contains an `Adapted from` header that has no
matching entry in this file (see `third_party_notices_test.go`).

Entry fields: project, URL, source commit, licence, copyright notice, source
files, destination files, adapted (yes: content copied or transcribed; no: used
for cross-checking only).

---

## libtiff

- URL: https://gitlab.com/libtiff/libtiff
- Source commit: `d23249978a7a99ea2e0752a476611914501577de`
- Licence: libtiff (see `LICENSES/libtiff.txt`), SPDX-License-Identifier: `libtiff`
- Copyright: Copyright (c) 1988-1997 Sam Leffler; Copyright (c) 1991-1997 Silicon Graphics, Inc.
- Source files: `libtiff/tif_dirinfo.c` (`tiffFields[]`, `exifFields[]`, `gpsFields[]`)
- Destination files: `exif/tag_extended.go`, `exif/tag_extended_test.go`
- Adapted: yes (tag IDs, names, types and counts, transcribed and renamed to Go types)
- Notes: the tag names of the TIFF/EP entries keep libtiff's `EP` prefix. The libtiff
  pseudo-tag `PerSample` (0x1001B) is not transcribed because it is outside the 16-bit tag space.

## metadata-extractor

- URL: https://github.com/drewnoakes/metadata-extractor
- Source commit: `6f037943c0811799a59b0ea19896cb2b07da17be`
- Licence: Apache-2.0 (see `LICENSES/Apache-2.0.txt`), SPDX-License-Identifier: `Apache-2.0`
- Copyright: Copyright 2002-2019 Drew Noakes and contributors
- Source files: `Source/com/drew/metadata/exif/ExifDirectoryBase.java` (`TAG_*` constants);
  `Source/com/drew/metadata/exif/ExifTiffHandler.java` (`processMakernote`)
- Destination files: `exif/tag_extended.go`, `exif/tag_extended_test.go`; `exif/makernote_detect.go`
- Adapted: yes (tag IDs and names of the tags that libtiff does not list; the rest of the table is used for cross-checking.
  MakerNote detection: the order of the signature and Make tests, the signatures, and the IFD offset and offset base of
  each layout, re-implemented as allocation-free Go; MakerNote tag tables are not copied)
- Notes: the repository has no `NOTICE` file. The changes made are stated in the
  header of each destination file. No descriptor marked as converted from ExifTool is used.

## bep/imagemeta

- URL: https://github.com/bep/imagemeta
- Source commit: `fbefe9cdc214365d79ba42fff69549ee7058db51`
- Licence: MIT (see `LICENSES/MIT.txt`), SPDX-License-Identifier: `MIT`
- Copyright: Copyright (c) 2022 Bjørn Erik Pedersen
- Source files: `metadecoder_iptc_fields.json` (dataset list and repeatable flags); `imagedecoder_png.go`
  (`decodePNG` "Raw profile type iptc" branch, `decompressZTXt`)
- Destination files: `iptc/dataset_table.go` (cross-check only), `iptc/dataset_table_test.go` (reference list of 116 datasets);
  `format/png/rawprofile.go`
- Adapted: yes for the test reference list and for the raw profile chunk decoding (keyword match, header skip, hex
  decode), which was extended to tEXt and iTXt chunks, the "exif" and "APP1" keywords, and declared-length validation;
  no for the production dataset table
- Notes: the production table is transcribed from the IPTC-NAA Information Interchange
  Model 4.2. The upstream JSON file is itself generated from ExifTool documentation, so no
  name or note text from it is copied.

## Adobe XMP Toolkit SDK

- URL: https://github.com/adobe/XMP-Toolkit-SDK
- Source commit: `7093513bd3caaad29da01db0f275d88a39d6bcc2`
- Licence: BSD-3-Clause (see `LICENSES/BSD-3-Clause.txt`), SPDX-License-Identifier: `BSD-3-Clause`
- Copyright: Copyright (c) 2020, Adobe (repository `LICENSE`); Copyright 2002-2003 Adobe and Copyright 2006 Adobe (source file headers)
- Source files: `public/include/XMP_Const.h` (`kXMP_NS_*`), `XMPCore/source/XMPMeta.cpp` (`XMPMeta::Initialize`, `RegisterStandardAliases`);
  `XMPFiles/source/FormatSupport/PSIR_MemoryReader.cpp` (`PSIR_MemoryReader::ParseMemoryResources`),
  `XMPFiles/source/FormatSupport/PSIR_FileWriter.cpp` (`PSIR_FileWriter::ParseMemoryResources`, duplicate-resource
  policy), `XMPFiles/source/FormatSupport/ReconcileIPTC.cpp` (`ComputeIPTCDigest`, `SetIPTCDigest`),
  `XMPFiles/source/FileHandlers/TIFF_Handler.cpp` (`TIFF_MetaHandler::ProcessXMP`)
- Destination files: `xmp/namespace_toolkit.go`, `xmp/namespace_toolkit_test.go`; `internal/irb/irb.go`,
  `internal/tiffscan/tiffscan.go`
- Adapted: yes (namespace URIs, prefixes and alias definitions, transcribed to Go tables; the Photoshop image resource
  walk, the MD5 IPTC digest computation and check, and the TIFF IPTC source precedence of tag 0x83BB over tag 0x8649,
  re-implemented in Go; the duplicate-resource policy — first resource of an ID wins, a later non-empty one replaces
  an empty first one, one resource per ID on write — applied to every reader and to the splice of `internal/irb`)
- Notes: the source files carry the header "Adobe permits you to use, modify, and distribute
  this file in accordance with the terms of the Adobe license agreement accompanying it",
  while the repository root `LICENSE` is the BSD 3-Clause licence. The accompanying licence
  is the root BSD 3-Clause licence, which is the one recorded here. For the namespace
  tables only facts (URIs, prefixes, alias pairs) are transcribed. The image resource
  and IPTC digest logic is re-implemented from the Toolkit's algorithm, with these
  changes: bounds checks in uint64, resources returned as sub-slices instead of copies,
  a digest refreshed only when one already exists, and the stale-digest signal kept when
  the IPTC is not modified (see the header of `internal/irb/irb.go`).
