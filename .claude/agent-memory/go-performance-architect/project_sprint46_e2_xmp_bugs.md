---
name: sprint46-e2-xmp-bugs
description: Sprint 46 E2 (#326-#331) XMP fixes - no-BOM UTF-16/32, end-tag matching + unparsed-packet carry, verbatim text, alias folding, SetLocalizedText, CameraModel; decisions and traps
metadata:
  type: project
---

Sprint 46 E2 (2026-10-14), files: xmp/{encoding,xmlscan,rdf,parserdf,project,xmp,alias,errors}.go, metadata.go (xmpUnparsed), read.go.

- Alias fold = xmp/alias.go, called at the top of (*rdfParser).finish so Parse and ParseExtended both get it. Toolkit rule: base present => alias deleted (value lost). Extension: tiff:ImageDescription as Alt (Exiv2 form) is merged per language. Alias-using tests (tiff:ImageDescription/Copyright, pdf:*) had to move to non-alias props (exif:UserComment, xmp:Label).
- Go trap: `p.t.method(p.t.add(...))` - receiver slice header may be read before add() grows p.t; always compute the index into a variable first.
- Scanner is now strict: mismatched/stray end tag, malformed start tag, unclosed element => ErrXMLNotWellFormed. Corpus: 61/3055 images newly rejected, all genuinely ill-formed (3 exiv2-bug1026*, 58 fuzzed metadata-extractor TIFFs).
- Metadata.xmpUnparsed: set when xmp.Parse fails; ensureXMP skips, Write carries rawXMP unchanged. WithoutXMP + setter still overwrites raw XMP (pre-existing, unfixed).
- Text is verbatim and EOL-normalised (normaliseEOL); xmlDoc.hasText replaces text()!="" for the resource/empty decisions.
- Tool: corpus before/after dump program lives only in the session scratchpad; recreate with a tiny main that walks testdata/corpus and prints props.
