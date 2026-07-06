---
name: project_xmp_geo_namespace
description: xmp.GPS() now supports W3C Basic Geo fallback (geo:lat/geo:long/geo:lon) in addition to NSexif primary
metadata:
  type: project
---

`xmp.GPS()` in `xmp/xmp.go` was extended to support the W3C Basic Geo vocabulary (`http://www.w3.org/2003/01/geo/wgs84_pos#`) as a fallback when NSexif GPS properties are absent.

Priority: NSexif GPSLatitude/GPSLongitude first; W3C geo:lat + geo:long (and geo:lon alias) second.

The namespace constant `NSgeo` was added to `xmp/namespace.go`.

The `parseXMPGPS` function already supports both the EXIF coordinate format ("DDD,MM.mmmR") and plain decimal degrees — W3C Geo uses decimal degrees, so the existing parser handles it.

This fixed a real library gap that had been masked by a `t.Skip` in `read_test.go`.

**Why:** W3C Geo vocabulary is commonly used in consumer mapping apps and social media platforms to embed GPS in XMP.
