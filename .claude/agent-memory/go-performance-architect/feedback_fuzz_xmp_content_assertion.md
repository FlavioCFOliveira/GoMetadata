---
name: feedback-fuzz-xmp-content-assertion
description: FuzzJPEGExtract must not assert XMP content structure — JPEG layer extracts bytes verbatim; XMP validation belongs to the xmp package
type: feedback
---

The original `FuzzJPEGExtract` asserted that any non-nil `rawXMP` must contain `<?xpacket`, `<rdf:`, or `<x:xmpmeta`. This fired when a crafted JPEG embedded arbitrary bytes after the `http://ns.adobe.com/xap/1.0/\x00` namespace prefix.

**Why:** The JPEG parser does not validate XMP content — it extracts whatever bytes follow the namespace prefix verbatim. XMP content validation is the role of the `xmp` package. A fuzz test at the JPEG layer must only assert structural JPEG invariants (e.g. rawEXIF length ≥ 8 if non-nil), not XMP document validity.

**How to apply:** At the JPEG package boundary, fuzz assertions for `rawXMP` must not check for XMP document structure markers. The correct assertion is simply that no panic occurs and `rawEXIF` satisfies the TIFF minimum length invariant.
