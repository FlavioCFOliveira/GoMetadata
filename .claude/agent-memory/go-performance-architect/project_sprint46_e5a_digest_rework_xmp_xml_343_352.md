---
name: sprint46-e5a-digest-rework-xmp-xml-343-352
description: Sprint 46 E5a (#323 rework, #343-#345, #350-#352) - MWG digest literal rule, opaque-segment guard, XML attr normalisation, unbound prefix, 65502 limit; verified facts, perf cost, traps
metadata:
  type: project
---

**#323 rework (user decision: MWG v2.0 4.2.3.2 p.29 literally):** with IIM and XMP both in the output, the digest 0x0425 is created or rewritten to MD5(IIM written), stale included, changed or not. No XMP in output, or IIM removed: digests left as is, none created (MWG silent for IIM-only; p.28 Changer guidance is "write both"). Read side untouched. Callers: irb.AppendSpliced(withXMP) (JPEG rawXMP!=nil; PNG raw profile rawXMP!=nil, only IRB form), tiff iptcDigestUpdate(psir, rawIPTC, withXMP) (currentIPTC removed). AppendSpliced no longer scans orig for the digest: -36% ns/op. ExifTool MWG.pm and Toolkit SetIPTCDigest both diverge from MWG; spec wins.

**MWG facts verified in the local PDF:** digest rules are 4.2.3.2 pp.26-29 (def p.26, scenario 3 p.27, scenario 4 + Changer rules p.28, "whether changed or not" + checksum paragraph p.29); TIFF location (PSIR 34377) p.31 sec 4.2.3.4. The all-zero "unknown" digest sentinel is NOT in MWG - library convention (iptc/digest.go), comments say so now.

**#343:** Metadata.xmpUnparsed renamed xmpOpaque; also exifOpaque/iptcOpaque. Set in parseEXIF/IPTC/XMP when raw != nil and the Without* option is on; ensureX skip creation. WithoutIPTC/WithoutEXIF had the identical defect (setter replaced the whole segment) - fixed with the same guard, confirmed by failing tests on the baseline.

**#344/#345:** attribute normalisation in rdf.go unescapeAttrValue (before entity expansion; CRLF = one space). Go encoding/xml does NOT normalise (keeps TAB/LF); Expat does; spec wins. Encode writes TAB/LF in attributes as &#9;/&#10; (writeXMLAttrEscaped). xmlDoc.resolve now returns (uri, bound); unbound prefix of element/attribute -> ErrXMLNotWellFormed naming the prefix. Corpus (639 packets): only 2 newly rejected (fuzz-mutated TIFF packet, xmllint rejects too); 61 already rejected. Two old tests encoded the lenient behaviour (task283 f0:Outside, task278 fragment) and were changed.

**#350:** Toolkit ProcessXMP deletes HasExtendedXMP BEFORE merging and ignores MergeFromJPEG failure, so on unmergeable extended content the packet is returned linkless + truncated=true (ErrExtendedXMPTruncated, text changed); wire frame kept. Byte-splice fallback kept but result must parse via StripExtendedLink. Known unfixed: a later setter write drops the unmerged extended content (m.XMP rewritten from the standard packet); only the unmodified-write path keeps it via the wire frame.

**#351:** Part 3 1.1.3.1 p.14: 65502 (segment could hold 65504 = 65533-29; spec is 2 stricter). maxXMPPayload=65502 is write-only; the reader has no limit. Toolkit has no 65502 constant (kStdXMPLimit=65000 is the partition target).

**Perf:** attr normalisation costs ~+2% on BenchmarkXMPParse even with a Hacker's-Delight "byte < 0x0E" word test (parity when the call is stubbed out); first attempt (3 xor/has-zero tests per word) was +4-6%, a plain byte loop similar. 0 alloc change everywhere.

**Trap:** `sed -i` on macOS needs `-i ''`; a failed sed in an && chain silently skipped the following python import edit.

**Closing run (2026-10-16):** all ACs verified; 8 fuzz targets 60 s green; lint = the 9 pre-existing (#346); benchstat vs `git archive HEAD`: AppendSpliced -36%, XMPParse +4.4%, RDFParse +3.7%, others <=1.5%, allocs unchanged. Added root `read_task350_test.go` (ParseWarning). Residual #350 confirmed: Read + SetCaption + Write of a file with an unmergeable matching-GUID extended chunk drops the extended chunk (m.XMP is rewritten from the standard packet). `go test -fuzz` needs `./pkg` (not `pkg`) paths.
