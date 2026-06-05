---
name: relocate-isobmff-recursion
description: ISOBMFF box walker must pass bounded content slices to recursive calls, not the parent buffer with a different start offset
type: feedback
---

When walking ISOBMFF boxes recursively to patch stco/co64 entries, each recursive call MUST receive a slice bounded to the current box's content (`data[contentStart:boxEnd]`), NOT the full parent buffer with just a different `contentStart` parameter.

**Why:** Passing the full parent buffer but a different start causes the scanner loop to run to the end of the full buffer after exhausting the current box's sub-boxes — processing sibling and later boxes multiple times. In a moov with two traks, trak2's co64 was processed once correctly and once again as a side-effect of trak1's stbl recursion, adding delta twice (614 instead of 294 for a delta=80 case).

**How to apply:** In `relocateInContainer`, always recurse as:
```go
content := data[pos+int(headerLen) : pos+int(size)]  // bounded slice
if err := relocateInContainer(content, oldMoovEnd, delta); err != nil { ... }
```
Never pass `(data, contentOffset, ...)` — that lets the loop escape the box boundary.

Related: [[relocate-stco-co64-api-redesign]] — refactoring relocateStco/Co64 to accept the content slice directly (not data+contentOff+boxEnd) also eliminates the risk of passing wrong bounds.
