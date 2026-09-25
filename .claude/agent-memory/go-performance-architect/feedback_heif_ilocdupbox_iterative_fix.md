---
name: feedback_heif_ilocdupbox_iterative_fix
description: A prescribed "sum all matching boxes" fix for an arithmetic-size-vs-actual-build mismatch can be locally correct yet still incomplete; strengthening the fuzz invariant that found the bug is what surfaced the remaining gap, twice, in the same session
type: feedback
---

Security audit finding HEIF-ILOC-DUPBOX-01 (2026-09-25, `format/heif/heif.go`) was reported
with a specific root cause (an arithmetic meta-box-size delta counted only the FIRST `iloc`
child box's size, while `buildMetaBox`'s real removal loop drops EVERY `iloc`-typed child)
and a specific prescribed fix ("sum the sizes of ALL iloc boxes, exactly mirroring
buildMetaBox's removal loop"). Applying that fix literally (change `oldIlocLen = size; break`
to `oldIlocLen += size`, no `break`) was correct for the exact scenario in the report
(duplicate `iloc` boxes) — verified via a crafted PoC and a byte-identical comparison against
pre-diff HEAD. But the SAME finding's mandated next step — strengthen `FuzzHEIFInject` with a
"does every recorded extent stay within the output's bounds" self-consistency invariant, then
fuzz for 60s — found a SECOND, structurally different trigger for the SAME underlying disease
within seconds of fuzzing: a meta box whose declared length includes trailing bytes that
`parseHEIFBoxHeader` cannot parse as a box header at all. `buildMetaBox`'s real walk stops and
silently drops those trailing bytes; the literal "sum all iloc boxes" fix still trusted the
meta box's OWN declared length as the baseline to subtract from, which does not account for
bytes `buildMetaBox` never actually copies.

**The generalizable lesson**: when an arithmetic pre-computation must exactly match what a
separate, hand-written "actually build it" function produces (the classic "measure twice by
hand, in two different functions" trap — see also `feedback_png_chunktype_escape.md`'s "escape
analysis" lesson and the exif `EncodedSize`/`writeIFD` exactness trap in
`project_sprint44_batchC_219_227_237.md` for two prior instances of this exact SHAPE of bug in
this codebase), a "mirror the other loop's FILTER condition" fix is necessary but not
sufficient — the other loop's TERMINATION condition (here: "stop the walk, drop the rest, the
instant a box header fails to parse") must ALSO be mirrored, or reproduced via a genuinely
SHARED helper function. The robust fix that closed both variants at once, applied after the
second discovery: extract the traversal itself (`nonIlocBoxesLen`, walking metaContent exactly
as `buildMetaBox` does, stopping identically) into ONE function called by BOTH the size
pre-computation (`newMetaBoxLen`) and the real builder (`buildMetaBox`) — this makes the two
outputs match BY CONSTRUCTION, eliminating the whole class of "two loops drift apart" bugs,
rather than requiring a human to keep noticing every way they could still disagree.

**Practical protocol validated by this incident**: when a coordinator/auditor asks you to (a)
fix a specific reported instance AND (b) strengthen a fuzz invariant that could catch that
CLASS of bug, do both fully and actually RUN the strengthened fuzzer for the full requested
duration before declaring done — do not treat step (b) as "already covered" once step (a)'s
literal fix passes the reported PoC. In this case the fuzzer found a second instance within
the first ~6 seconds of a 60s run, and (after fixing that too) a completely clean 60s run only
appeared on the THIRD attempt. Two independent clean 60-second runs were performed before
reporting closure, specifically because two prior "looks clean" checkpoints (seed-corpus-only
runs) had each been immediately followed by a real fuzz-discovered failure.

**Also worth recording**: the natural first invariant to write for "does Inject's output stay
in bounds" — checking EVERY item's extent in the output — produces false positives against
GENUINELY unrelated, pre-existing behaviour: (1) items with no recognised type (or a type
Inject never rewrites) can carry whatever garbage offset the original malformed input already
declared, verbatim, since Inject/writeHEIFOutput never touches non-pending items; (2) even an
Exif/mime-typed item's extent can be untouched, garbage input data if `buildInjectComponents`
declined to rebuild the file at all (e.g. `ilocInfo.offsetSize == 0`, a pre-existing,
unrelated guard) and the whole call fell through to `writePassThrough` — comparing
`bytes.Equal(output, input)` first is the simplest way to detect that case and skip the
invariant, rather than trying to enumerate every reason Inject might decline to rebuild.
Narrow the invariant to "items I can PROVE Inject computed a new value for" (Exif/mime type
present in the OUTPUT's own iinf AND `output != input`), not "every value the input or output
happens to contain" — the latter tests the INPUT's validity, which is not Inject's job to
enforce, not the CODE's correctness.

See [[project_sprint44_batchD_228_234]] for the original Batch D task list this finding
belongs to, and [[feedback_png_chunktype_escape.md]] for the same-session's other
"looks right until measured against the real other implementation" lesson.
