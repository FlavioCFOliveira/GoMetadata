---
name: feedback-makernote-short-circuit
description: relocateTIFFFromParsed short-circuit path skips Step 9.5; always apply MakerNote rebasing on BOTH code paths
metadata:
  type: feedback
---

When adding post-encode steps to `relocateTIFFFromParsed`, the short-circuit path at `len(blocks)==0 && len(subIFDs)==0` also needs the step applied.

The short-circuit exits after `exif.Encode(e)` without going through steps 9–12. A metadata-only TIFF (no strip/tile data) takes this path and still moves the MakerNote blob when new IPTC/XMP tags expand the IFD section.

**Why:** `rebaseGenericMakerNote` was only added to the main path (step 9.5 after line 338) but tests used synthetic TIFFs with no image data — they always hit the short-circuit. The Olympus/Sony gate tests failed with garbage OOL values pointing into the XMP packet bytes.

**How to apply:** Whenever adding a post-encode mutation to `relocateTIFFFromParsed`, add it to BOTH the short-circuit block (around line 284-294) AND the main path (after step 9). See `#127` fix in `format/tiff/relocate.go`. Related: [[feedback-tiff-two-pass-encode]].
