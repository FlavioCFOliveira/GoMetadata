package gometadata

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/ReconcileTIFF.cpp:ImportSingleTIFF_Short,ImportSingleTIFF_Long,ImportSingleTIFF_Rational,ImportSingleTIFF_SRational,ImportSingleTIFF_ASCII, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// The XMP text of an EXIF value is formatted as the Toolkit imports it: a
// SHORT or LONG as a decimal number, a RATIONAL or SRATIONAL as
// "numerator/denominator", an ASCII value as its text. The Toolkit writes the
// whole native tag into XMP on import; here only an XMP copy that already
// exists is refreshed, after a setter has changed the EXIF tag (see
// reconcile.WriteIfPresent). See THIRD_PARTY_NOTICES.md for the licence note.

import (
	"slices"
	"strconv"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/reconcile"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// exifTextForm and iimTextForm read one form of a text field read with
// reconcile.ReadEXIFThenXMPThenIIM (Description, Copyright, Creator): the
// EXIF tag and the first IIM DataSet occurrence named by the field's
// reconcile.Mapping; xmpText reads the XMP form. TestTask308_TextAccessorsMatchTable
// proves that each form read is the tag, DataSet and property of the table. A
// switch is used rather than a table of function values so that the component
// accessors stay inlinable.
func exifTextForm(e *exif.EXIF, f reconcile.Field) string {
	switch f {
	case reconcile.Description:
		return e.Caption()
	case reconcile.Copyright:
		return e.Copyright()
	default:
		// MWG §5.7: the Artist tag holds the creator list as one string.
		return reconcile.FirstArtist(e.Creator())
	}
}

func iimTextForm(i *iptc.IPTC, f reconcile.Field) string {
	switch f {
	case reconcile.Description:
		return i.Caption()
	case reconcile.Copyright:
		return i.Copyright()
	default:
		return i.Creator()
	}
}

// resolveText returns the value of a text field read with
// reconcile.ReadEXIFThenXMPThenIIM, the order of MWG v2.0 §4.2.3.3 (Toolkit
// equivalent: the policy stated in ReconcileTIFF.cpp at
// PhotoDataUtils::Import3WayItems):
//
//  1. When the IPTC digest does not match the IIM block (iptcTrustElevated),
//     the IIM value wins if it differs from the value predicted from the XMP
//     value (MWG §4.2.3.2, scenario 4; reconcile.IIMChanged). MWG §4.2.3.3:
//     on a digest mismatch "a Consumer [...] SHOULD prefer IPTC-IIM" over
//     Exif. An IIM value that is only the XMP value truncated to the IIM
//     length limit is not a change, so the rule does not fire for it.
//  2. Otherwise the EXIF value wins (MWG §4.2.3.1: "a Consumer SHOULD prefer
//     the values from Exif"; §4.2.3.3: Exif over IPTC-IIM when the digest
//     matches or is absent), then the XMP value (§4.2.3.2, scenario 3), then
//     the IIM value.
//
// A value of only spaces or NULs counts as absent (MWG §4.2.3.3).
func (m *Metadata) resolveText(f reconcile.Field) string {
	if m.iptcTrustElevated() {
		if v := m.iimText(f); v != "" && reconcile.IIMChanged(v, m.xmpText(f), reconcile.IIMMaxLen(f)) {
			return v
		}
	}
	if m.EXIF != nil {
		if v := exifTextForm(m.EXIF, f); !reconcile.IsBlank(v) {
			return v
		}
	}
	if v := m.xmpText(f); v != "" {
		return v
	}
	return m.iimText(f)
}

// xmpText returns the XMP form of a text field (the x-default or first item
// of the XMP property of the field's reconcile.Mapping), or "" when it is
// absent or blank.
func (m *Metadata) xmpText(f reconcile.Field) string {
	if m.XMP == nil {
		return ""
	}
	var v string
	switch f {
	case reconcile.Description:
		v = m.XMP.Caption()
	case reconcile.Copyright:
		v = m.XMP.Copyright()
	default:
		v = m.XMP.Creator()
	}
	if reconcile.IsBlank(v) {
		return ""
	}
	return v
}

// iimText returns the IIM form of a text field, or "" when it is absent or
// blank.
func (m *Metadata) iimText(f reconcile.Field) string {
	if m.IPTC == nil {
		return ""
	}
	if v := iimTextForm(m.IPTC, f); !reconcile.IsBlank(v) {
		return v
	}
	return ""
}

// resolveKeywords returns the keywords read with reconcile.ReadXMPThenIIM: the
// rule of resolveText applied to lists (Toolkit
// PhotoDataUtils::Import2WayIPTC; MWG §4.2.3.2 and §5.1).
func (m *Metadata) resolveKeywords() []string {
	if !m.iptcTrustElevated() || m.IPTC == nil {
		if m.XMP != nil {
			if kw := m.XMP.Keywords(); len(kw) > 0 {
				return kw
			}
		}
		if m.IPTC != nil {
			return m.IPTC.Keywords()
		}
		return nil
	}
	iim := m.IPTC.Keywords()
	if m.XMP == nil {
		return iim
	}
	p := &reconcile.Lookup(reconcile.Keywords).XMP[0]
	if len(iim) > 0 && reconcile.IIMListChanged(iim, m.XMP.Get(p.NS, p.Name), reconcile.IIMMaxLen(reconcile.Keywords)) {
		return iim
	}
	if kw := m.XMP.Keywords(); len(kw) > 0 {
		return kw
	}
	return iim
}

// resolveDateCreated returns the date of the second XMP property of
// reconcile.DateTimeOriginal (photoshop:DateCreated) or of the IIM DataSets
// 2:55 and 2:60, with the digest rule of resolveText: the XMP value wins
// unless the IPTC digest does not match and the IIM date differs from the one
// predicted from the XMP (reconcile.IIMDateMatches). When the winning value
// cannot be parsed, the other one is used.
func (m *Metadata) resolveDateCreated() (time.Time, bool) {
	var x, date, tm string
	if m.XMP != nil {
		p := &reconcile.Lookup(reconcile.DateTimeOriginal).XMP[1]
		x = m.XMP.Get(p.NS, p.Name)
	}
	if m.IPTC != nil {
		date = m.IPTC.DateCreated()
	}
	if date == "" {
		return parseXMPDate(x)
	}
	tm = m.IPTC.TimeCreated()
	if x == "" || m.iptcTrustElevated() && !reconcile.IIMDateMatches(date, tm, x) {
		if t, ok := reconcile.ParseIIMDate(date, tm); ok {
			return t, true
		}
		return parseXMPDate(x)
	}
	if t, ok := parseXMPDate(x); ok {
		return t, true
	}
	return reconcile.ParseIIMDate(date, tm)
}

// editedIIMDate returns the IIM date (2:55 with 2:60) when the IPTC digest
// does not match and the IIM date differs from the one predicted from XMP
// photoshop:DateCreated (reconcile.IIMDateMatches): the first rule of
// resolveText applied to dates (MWG §4.2.3.3 and §5.3).
func (m *Metadata) editedIIMDate() (time.Time, bool) {
	if !m.iptcTrustElevated() || m.IPTC == nil {
		return time.Time{}, false
	}
	date := m.IPTC.DateCreated()
	if date == "" {
		return time.Time{}, false
	}
	tm := m.IPTC.TimeCreated()
	if m.XMP != nil {
		p := &reconcile.Lookup(reconcile.DateTimeOriginal).XMP[1]
		if reconcile.IIMDateMatches(date, tm, m.XMP.Get(p.NS, p.Name)) {
			return time.Time{}, false
		}
	}
	return reconcile.ParseIIMDate(date, tm)
}

// parseXMPDateTimeOriginal parses the value of XMP exif:DateTimeOriginal: the
// three layouts that DateTimeOriginal has always accepted come first, so that
// their results (including the time.Parse choice of location) are unchanged,
// then the other forms of the XMP Date type (date only, minutes only,
// fractional seconds).
func parseXMPDateTimeOriginal(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range xmpDateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return parseXMPDate(v)
}

// parseXMPDate is reconcile.ParseXMPDate without the presence flags.
func parseXMPDate(s string) (time.Time, bool) {
	t, _, _, ok := reconcile.ParseXMPDate(s)
	return t, ok
}

// exifSubSecOriginal adds to t the fraction of a second recorded in ExifIFD
// tag 0x9291 SubSecTimeOriginal (CIPA DC-008-2023 §4.6.5: the leading decimal
// digits are the fraction; trailing spaces pad the value). MWG §5.3: the EXIF
// sub-second tag is handled together with DateTimeOriginal.
func (m *Metadata) exifSubSecOriginal(t time.Time) time.Time {
	e := m.EXIF.ExifIFD.Get(exif.TagSubSecTimeOriginal)
	if e == nil {
		return t
	}
	v := e.Value
	if e.Type != exif.TypeASCII && e.Type != exif.TypeUTF8 {
		return t
	}
	ns, scale := 0, 100_000_000
	for _, c := range v {
		if c < '0' || c > '9' {
			break
		}
		ns += int(c-'0') * scale
		scale /= 10
	}
	return t.Add(time.Duration(ns))
}

// mirrorToXMP refreshes the XMP properties of field f whose write mode is
// reconcile.WriteIfPresent and that the XMP packet already has, from the
// value of the field's EXIF tag (see reconcile.WriteIfPresent). The value is
// formatted for the type of the Toolkit mapping of the tag; a tag whose type
// differs from the mapping, or that the Toolkit does not map, is not copied.
// mirrorToXMP allocates only the strings it stores.
//
// It reports whether it wrote a property, that is, whether the XMP changed
// (task #387).
func (m *Metadata) mirrorToXMP(f reconcile.Field) (wrote bool) {
	if m.XMP == nil || m.EXIF == nil {
		return false
	}
	mp := reconcile.Lookup(f)
	for i := range mp.XMP {
		p := &mp.XMP[i]
		if p.Write != reconcile.WriteIfPresent {
			continue
		}
		if _, ok := m.XMP.Properties[p.NS][p.Name]; !ok {
			continue
		}
		e := exifEntry(m.EXIF, mp.IFD, mp.Tag)
		tm, ok := reconcile.TIFF(mp.IFD, mp.Tag)
		if e == nil || !ok {
			continue
		}
		if v, ok := xmpText(e, tm.Type); ok {
			m.XMP.Set(p.NS, p.Name, v)
			wrote = true
		}
	}
	return wrote
}

// exifEntry returns the entry of tag in the directory ifd of e, or nil.
func exifEntry(e *exif.EXIF, ifd reconcile.IFD, tag uint16) *exif.IFDEntry {
	var d *exif.IFD
	switch ifd {
	case reconcile.IFDPrimary:
		d = e.IFD0
	case reconcile.IFDExif:
		d = e.ExifIFD
	case reconcile.IFDGPS:
		d = e.GPSIFD
	default:
		return nil
	}
	return d.Get(exif.TagID(tag))
}

// xmpText formats the first value of entry as the XMP Toolkit imports a tag
// of type want (ReconcileTIFF.cpp:ImportSingleTIFF_*): "%hu" for SHORT,
// "%lu" for LONG, "%lu/%lu" for RATIONAL, "%ld/%ld" for SRATIONAL, the text
// for ASCII. reconcile.TypeShortOrLong accepts both integer types. ok is
// false when entry has another type or no value.
func xmpText(entry *exif.IFDEntry, want reconcile.TIFFType) (string, bool) {
	if !typeMatches(entry, want) {
		return "", false
	}
	var buf [24]byte
	switch entry.Type {
	case exif.TypeShort:
		return string(strconv.AppendUint(buf[:0], uint64(entry.Uint16()), 10)), true
	case exif.TypeLong:
		return string(strconv.AppendUint(buf[:0], uint64(entry.Uint32()), 10)), true
	case exif.TypeRational:
		r := entry.Rational(0)
		b := append(strconv.AppendUint(buf[:0], uint64(r[0]), 10), '/')
		return string(strconv.AppendUint(b, uint64(r[1]), 10)), true
	case exif.TypeSRational:
		r := entry.SRational(0)
		b := append(strconv.AppendInt(buf[:0], int64(r[0]), 10), '/')
		return string(strconv.AppendInt(b, int64(r[1]), 10)), true
	default: // ASCII or UTF-8, checked by typeMatches
		return entry.String(), true
	}
}

// typeMatches reports whether entry has the Toolkit type want and holds at
// least one value.
func typeMatches(entry *exif.IFDEntry, want reconcile.TIFFType) bool {
	got, size, ok := toolkitType(entry.Type)
	if !ok {
		return false
	}
	if want == reconcile.TypeShortOrLong && (got == reconcile.TypeShort || got == reconcile.TypeLong) {
		want = got
	}
	return got == want && len(entry.Value) >= size
}

// toolkitType returns the Toolkit type of an EXIF field type that xmpText
// formats, and the size of one value; ok is false for any other type. An
// EXIF 3.0 UTF-8 string is formatted as the Toolkit formats ASCII.
func toolkitType(t exif.DataType) (typ reconcile.TIFFType, size int, ok bool) {
	switch t {
	case exif.TypeShort:
		return reconcile.TypeShort, 2, true
	case exif.TypeLong:
		return reconcile.TypeLong, 4, true
	case exif.TypeRational:
		return reconcile.TypeRational, 8, true
	case exif.TypeSRational:
		return reconcile.TypeSRational, 8, true
	case exif.TypeASCII, exif.TypeUTF8:
		return reconcile.TypeASCII, 0, true
	default:
		return 0, 0, false
	}
}

// keepFirstRecord2 removes every occurrence of the repeatable record 2
// DataSet ds but the first, in place and without allocating, so that a
// setter that replaced the first occurrence leaves a one-item list.
func keepFirstRecord2(i *iptc.IPTC, ds uint8) {
	recs := i.Records[2]
	out, seen := 0, false
	for k := range recs {
		if recs[k].DataSet == ds {
			if seen {
				continue
			}
			seen = true
		}
		recs[out] = recs[k]
		out++
	}
	clear(recs[out:])
	i.Records[2] = recs[:out]
}

// removeEXIFTag deletes the entry of tag from d, if present. MWG §4.2.3.1,
// "Writing": "If only one form is updated, an existing value in the other
// form MUST be removed." The entries stay sorted, and the slice is shortened
// in place, never grown.
func removeEXIFTag(d *exif.IFD, tag exif.TagID) {
	if d == nil {
		return
	}
	for i := range d.Entries {
		if d.Entries[i].Tag == tag {
			d.Entries = slices.Delete(d.Entries, i, i+1)
			return
		}
	}
}
