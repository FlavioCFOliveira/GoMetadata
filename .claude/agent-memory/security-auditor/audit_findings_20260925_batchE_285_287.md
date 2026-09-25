---
name: audit-20260925-batchE-285-287
description: Sprint-44 Batch E (#285-#287) security pass — CLEARED. write.go m.rawEXIF aliasing exhaustively verified safe (0 mutations/races across 571 real TIFF/RAW files x 8 concurrent writers); CR3 box-walk streaming rewrite sound; exif.AcceptRAWMagic/ORF-RW2 no-clone-parse verified; 1 INFO-level benign output-byte-difference found on a malformed POC fixture (correctness improvement, not a bug)
metadata:
  type: project
---

Batch E audit 2026-09-25 (uncommitted diff on HEAD ce1dc82): write.go/relocate*.go m.rawEXIF
aliasing (#285), exif.AcceptRAWMagic + read.go no-clone ORF/RW2 parse + cr3.go streaming
box-walk Extract rewrite (#286), xmp nameTerminatorLUT (#287).

**#285 write.go aliasing — VERIFIED SAFE.** Exhaustively traced every write into `base`/
`finalTIFF`/`rawBytes`/`out`/`blob`/`buf` across relocate.go, relocate_arw.go (incl. Sony SR2
crypt/patch chain), relocate_nef.go, relocate_orf.go, relocate_rw2.go, tiff.go
(insertCR2MarkerAndShiftOffsets): every one of these originates from `exif.EncodeInto`/
`exif.Encode` (a brand-new buffer) or an explicit `make+copy` clone (extractRawIFD's SubIFD
rawBytes, ARW's sr2RawBytes/sr2BytesForOutput double-clone) — `base` itself is read-only
throughout every relocator, in every RAW-family variant. The CR2-marker/RW2-GUID in-place
overlapping shifts (`out = finalTIFF[:len(finalTIFF)+N]; copy(out[N:], finalTIFF[...])`) operate
on `finalTIFF`'s OWN backing array (never `base`), and rely on Go's spec-guaranteed overlap-safe
`copy()` (memmove semantics) — correct regardless of shift direction. ORF/RW2's removed
`base[2]=0x2A;base[3]=0x00` in-place patches are confirmed fully gone, replaced by
`exif.Parse(base, exif.AcceptRAWMagic(...))`.

Empirical confirmation (real corpus, not just static reading): 571 real TIFF+RAW files with EXIF
(testdata/corpus/tiff + testdata/corpus/raw) — SetCaption + Write, then Write AGAIN from the
same *Metadata: 0 double-write mismatches, 0 RawEXIF()-snapshot changes, 0 input-byte mutations.
Same corpus x 8 concurrent goroutines writing from ONE shared *Metadata: `go test -race` clean,
0 divergent outputs across all 571 files. This directly validates the coordinator's specific
concern ("Write can't mutate Metadata state visible to later calls") end-to-end across every
format-specific writeTIFF* path, not just the ones read statically.

INFO-level note (not a vulnerability, not blocking): `exif.Parse`'s OOL `IFDEntry.Value` fields
have ALWAYS aliased the parse input's backing array (`ifd.go: Value: b[valOff:valOff+totalSize]`,
unchanged by this diff) — this is a pre-existing, zero-copy-by-design characteristic, not
introduced by #285/#286. Since `Metadata.EXIF` is a public, directly-mutable field, a caller that
pokes `m.EXIF.IFD0.Entries[i].Value[j]` in place COULD already (pre-#285 too, for TIFF/CR2/ARW/NEF/
DNG, whose magic was always standard so Parse was already unlonced for them) corrupt `m.rawEXIF`'s
backing array before calling Write — the OLD `bytes.Clone(m.rawEXIF)` in write.go never protected
against this specific scenario either (cloning AFTER external corruption just clones the corrupted
bytes). Not attacker-file-bytes reachable (requires the CALLING APPLICATION's own code to misuse a
public field, not something a malicious image file can trigger) — documented here for completeness
per the "nothing may compromise the code" mandate, not raised as a finding requiring a fix.

**#286 exif.AcceptRAWMagic — sound.** Public but explicitly guarded (`cfg.extraMagic != 0 &&`)
so a caller passing the zero value is always a no-op; classic-TIFF/BigTIFF default rejection is
unchanged for every caller that doesn't opt in. Only theoretical footgun: a caller could pass
`AcceptRAWMagic(0x002B)` and have a genuine BigTIFF file's magic wrongly dispatch through the
classic-TIFF case (switch evaluates in order, classic-TIFF case is listed first) — not exploitable
via untrusted file bytes since it requires the CALLER to deliberately pass that exact value; no
internal call site in this codebase does.

**#286 cr3.go streaming box-walk — sound.** `cr3BoxHeaderAt`/`readTopLevelBox` mirror the
pre-existing, unchanged in-memory `parseCR3BoxHeader` bounds logic exactly (64-bit largesize,
size==0-to-EOF, size<headerLen rejection, size>remaining rejection) — including the same
"size==1 resolves a 64-bit value that could itself be 0, then separately falls into the size==0
extends-to-EOF branch" sequential-if quirk, which is a faithful port of pre-existing, presumably
already-audited behavior, not a new defect. Loop bounded by `pos` strictly advancing by >=8 per
iteration AND a 4096-scan cap — no infinite loop. `moov` payload allocation is bounded by
`fileLen <= maxFileSize` (checked before the scan begins), so a crafted file that makes `moov`
span nearly the whole file allocates at most what the OLD whole-file-read implementation already
allocated — not a new DoS amplification. `originalBytes`/`base` in cr3.go's own functions are only
ever read, never written.

**#287 xmp nameTerminatorLUT — sound.** `[256]bool` array indexed by a `byte` value cannot go
out of bounds (compiler can elide the check); exact same 8 terminator characters as the replaced
branch chain; FuzzParseXMP (8.2M execs/25s) and the existing XMP test suite pass unchanged.

**One confirmed, non-blocking output-byte difference (INFO):** `testdata/corpus/raw/exiv2/
issue_839_poc.rw2` (and its two duplicate-content fixtures) produce a Write() output that differs
from pre-Batch-E HEAD by exactly 1 byte (same length, 2786 bytes). Root-caused via a purpose-built
Go dump of `m.EXIF`: this file (an exiv2 issue-839 proof-of-concept, i.e. a deliberately malformed
fixture) has an IFD0 tag 0x0148 (ASCII, count=48) whose out-of-line value offset self-referentially
overlaps the file's OWN TIFF header — `IFDEntry.Value` for this tag literally aliases `raw[0:48]`.
Before #286, RW2 parsing ran on a magic-patched CLONE (bytes[2:4] forced to 0x2A 0x00 for
exif.Parse's sake), so this self-aliasing tag's captured value contained that ARTIFICIAL patched
magic. After #286, parsing runs on the real, unmodified `raw`, so the same tag now captures the
file's TRUE magic (0x55 0x00, RW2's actual byte). exif.Encode reproduces IFDEntry.Value verbatim,
so the 1-byte difference propagates to the write output. This is judged a correctness IMPROVEMENT
(the new behavior no longer leaks an internal, purely-implementation-driven magic-patch artifact
into round-tripped EXIF data) rather than a regression, is provably confined to this one
self-overlapping-OOL-entry malformation (no other file in the 571-file corpus showed any diff),
and has no security implication (no crash, no OOB, no amplification). Verified via `cmp -l` byte
diff + a custom `exif.EXIF` dumper program; not present for any other corpus file.

Tooling: go build/vet clean; go test ./... all green; go test -race on root, exif, format/tiff,
format/raw/{arw,cr2,cr3,dng,nef,orf,rw2}, xmp all clean; govulncheck not runnable (pre-existing
go1.26/go1.27 toolchain mismatch, same as prior audits, unrelated to this diff). Fuzz (all PASS,
0 crashers): FuzzCR3Extract 8.67M/45s, FuzzCR3Inject 8.2M/45s, FuzzORFInject/FuzzRW2Inject/
FuzzCR2Inject/FuzzARWInject/FuzzNEFInject ~40s each, FuzzParseEXIF 40s, FuzzRead 6.4M/45s,
FuzzParseXMP 8.2M/25s. Real-corpus regression check (git-stash before/after, SHA-256): CR3 12/12
files byte-identical (Extract + Inject); TIFF 451/451 byte-identical; RAW corpus 142/142 except
the 3 issue_839_poc.rw2 duplicates explained above (same length, 1 differing byte, root-caused
and judged benign).

Verdict: CLEARED for commit.
