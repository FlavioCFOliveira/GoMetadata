---
name: feedback-xmp-parser-lenient
description: xmp.Parse is lenient to malformed XML by design; only ErrEmptyInput and ErrXMLNestingDepth (>100 levels) are reliable failure triggers
type: feedback
---

The xmp package uses a hand-written scan-based parser (xmp/rdf.go) that does not use encoding/xml. It is designed to be resilient to malformed XML and will silently skip tags it cannot parse. Two reliable parse failure paths exist:

1. `xmp.ErrEmptyInput`: `xmp.Parse([]byte{})` — but the JPEG extractor only sets rawXMP when len >= 1, so injecting an empty XMP segment in a JPEG doesn't easily trigger this.
2. `xmp.ErrXMLNestingDepth`: building 102 nested open tags (e.g. `<a0><a1>...<a101>`) exceeds the 100-level limit in `parseStartTag` and returns this error reliably.

**Why:** The XMP parser's leniency is intentional — real-world XMP is often slightly malformed. Generic "invalid XML" like `<this is not valid xml <<<` will NOT cause a parse error; the scanner shrugs past bytes it cannot interpret.

**How to apply:** In tests that require a corrupt XMP payload, always use the 102-nested-element approach, not generic garbage or malformed XML syntax. When testing the IPTC strict-mode path, note that `iptc.Parse` never returns an error — it is always lenient by design and the warning/strict code path for IPTC is dead code under current parser behavior.
