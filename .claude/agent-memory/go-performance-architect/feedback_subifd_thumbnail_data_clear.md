---
name: feedback-subifd-thumbnail-data-clear
description: SubIFDs with 0x0201/0x0202 (JPEG-in-TIFF) must have ThumbnailData cleared before block enumeration
metadata:
  type: feedback
---

When enumerateSubIFDsAt parses a SubIFD via ParseIFDAt → parseSingleIFD, the parsed IFD gets ThumbnailData set if it contains both 0x0201 and 0x0202. This causes appendJPEGBlock to skip the JPEG block, treating it as exif.Encode-managed. But SubIFDs are NEVER managed by exif.Encode — they are handled as raw bytes. The fix is to clear ThumbnailData on the SubIFD parsedIFD before calling enumerateIFDBlocks.

**Why:** Discovered in task #102 (NEF write); this caused 715 kB of JpgFromRaw data to be silently dropped from NEF output.

**How to apply:** Any SubIFD (tag 0x014A child) that carries 0x0201/0x0202 must have its ThumbnailData cleared before block enumeration. This is now done in relocate.go enumerateSubIFDsAt. Do not regress this fix.
