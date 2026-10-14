---
name: project-sprint45-e5-reconcile-tables
description: Task #308 (2026-10-14) internal/reconcile Toolkit mapping tables + MWG read/write policy; MWG section numbers, oracle facts, perf costs, pre-existing write defects found
metadata:
  type: project
---

Task #308 (sprint 45 E5, 2026-10-14, uncommitted at report time): `internal/reconcile` holds the XMP Toolkit tables
(ReconcileTIFF.cpp sPrimary/sExif/sGPSInfo = 25/68/27 rows, IPTC_Support.cpp kKnownDataSets = 56 rows) and a
`fields` table driving the 16 root setters/getters; root glue in `reconcile.go`.

Facts worth keeping (verified, not derivable from code):
- MWG v2.0 PDF real sections: IPTC digest/predicted-IIM rule = §4.2.3.2 (scenario 4); blank (spaces/NUL) = absent
  §4.2.3.3; time-zone handling §4.2.3.4; Artist "; " list + quoting §5.7. Old code cited "§3.3.1"/"§2.2.1" (wrong).
- MWG text and ExifTool -MWG read EXIF > XMP > IIM; the Toolkit's actual Import3WayItems code (haveExif only when no
  XMP and no IIM) is XMP > IIM > EXIF although its comment states the MWG policy. USER DECISION (2026-10-14): MWG
  order EXIF > XMP > IIM, with an edited IIM (stale digest + differs from XMP prediction) winning over EXIF and XMP;
  applied to Caption/Copyright/Creator and DateTimeOriginal (Keywords has no EXIF form). ExifTool differs only there
  (it always prefers EXIF) and on blank XMP. [[project-mwg02-iptc-digest]]
- USER DECISION: the ns-scale getter/setter cost of MWG behaviour is accepted (MWGAccessors ~45 -> ~80 ns).
- ExifTool MWG write also adds OffsetTimeOriginal to Exif < 0231 and its own -validate then warns; same as ours.
- ExifTool MWG does NOT treat blank XMP as absent (only blank EXIF); we follow the MWG MUST.

**Why:** these drove decisions and will recur in any MWG/precedence work.
**How to apply:** cite the §4.2.3.x numbers; keep the MWG order, it was chosen by the user.

Perf: getter cost is dominated by XMP map lookups (namespace URI keys ~32 B); a func-value dispatch table was
slower than a switch; each "update XMP copy if present" check costs ~7-10 ns. MWGAccessors 44->73 ns, setters
bench +16% — accepted by the user.

Pre-existing defects seen in the corpus sweep (not fixed): JPEG write leaves TIFF-relative MakerNotes
(Panasonic/Sony/Olympus/Nikon E950...) unrebased (~106 files, ExifTool "Possibly incorrect maker notes offsets");
IFD1 ExifIFD/GPS pointers not relocated (Olympus mu 800); PNG "Raw profile type exif/xmp" text chunks keep stale
copies; internal/irb never creates a digest and refreshes a stale one, erasing the MWG signal.
