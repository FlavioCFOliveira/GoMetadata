---
name: feedback_webfetch_html_tables_unreliable
description: WebFetch's AI summarization drops/garbles rows in large HTML reference tables — use curl + deterministic parsing instead for spec-table verification
type: feedback
---

When a task requires transcribing a large HTML reference table verbatim
(e.g. exiv2.org's XMP/EXIF/IPTC tag tables, or any similar spec-table
mirror), do NOT rely on the `WebFetch` tool's AI-summarized output as the
ground truth, even when explicitly asked to "transcribe verbatim." Observed
during task #273 (XMP container-type table build): fetching the exact same
`exiv2.org/tags-xmp-xmpMM.html` page twice with slightly different prompts
produced self-contradictory results for the SAME properties — one fetch
said "Ingredients: XmpBag, Pantry: XmpText, Versions: XmpText" (with the
model itself flagging its own answer as "inconsistent"), while the
underlying raw table was actually perfectly consistent once parsed
directly. A separate fetch of `tags-xmp-iptcExt.html` silently dropped a row
("EventId") that was present in the raw HTML and present in an earlier fetch
of the same page.

**Fix that worked reliably:** `curl -s -A "Mozilla/5.0" <url> -o page.html`
then a small Python script using `re.findall(r'<tr>(.*?)</tr>', html, re.S)`
+ per-cell `<td>` extraction with tags stripped, printed as pipe-delimited
rows. This is deterministic (same output every time) and complete (every
row, not just the ones a summarizer chose to include).

**Why:** the report format explicitly instructed to "cross-check... do not
guess" for a spec-derived table used directly in production code
(namespace.go's `arrayProperties`). An AI-summarized WebFetch that silently
drops or contradicts rows is itself a form of guessing, even though it
looks like a citation.
**How to apply:** whenever a task needs a large, exact reference table from
a web page (tag tables, opcode tables, any tabular spec mirror), reach for
`curl` + `Bash` + a short parsing script BEFORE trusting `WebFetch`'s
summary, especially when the table has more than ~10 rows or when two
independent fetches of the same page might be compared. Small, single-fact
lookups (a handful of named rows) are fine via WebFetch; anything
approaching "the whole table" is not.
