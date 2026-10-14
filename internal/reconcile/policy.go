package reconcile

// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/ReconcileTIFF.cpp:PhotoDataUtils::Import3WayItems, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
// Adapted from Adobe XMP-Toolkit-SDK XMPFiles/source/FormatSupport/ReconcileIPTC.cpp:PhotoDataUtils::Import2WayIPTC,PhotoDataUtils::ImportIPTC_Date,ExportIPTC_Date, Copyright 2006 Adobe, SPDX-License-Identifier: BSD-3-Clause
//
// Changes from the Toolkit: the import policy of Import3WayItems and
// Import2WayIPTC is applied when a value is read (by the root package) instead
// of being merged into the XMP, so this file provides only its predicates;
// the IIM value predicted from the XMP is the value the iptc package writes
// (truncated at a UTF-8 boundary, without CR/LF conversion); the IIM date
// parser keeps the Toolkit's tolerance of separators and of a missing time
// zone but rejects out-of-range fields instead of clamping them. The EXIF
// Artist list functions implement MWG §5.7 and are not adapted. See
// THIRD_PARTY_NOTICES.md for the licence note on these files.

import (
	"strings"
	"time"
	"unicode/utf8"
)

// itemSep is the separator that the xmp package uses between the items of an
// array value in xmp.XMP.Properties (U+001E, see the doc of that field).
const itemSep = "\x1e"

// artistSep is the separator of the items of the EXIF Artist list (MWG §5.7).
const artistSep = "; "

// IsBlank reports whether s holds only spaces and NUL characters. MWG §4.2.3.3:
// "A string property that is comprised of only spaces or only nul characters
// MUST be treated as non-existent." The empty string is blank.
func IsBlank(s string) bool {
	// The first byte decides for almost every real value; the loop runs only
	// for values that start with a space or a NUL.
	return s == "" || (s[0] == ' ' || s[0] == 0) && isBlankTail(s[1:])
}

// isBlankTail is the loop of IsBlank, kept out of it so that IsBlank inlines.
func isBlankTail(s string) bool {
	for i := range len(s) {
		if c := s[i]; c != ' ' && c != 0 {
			return false
		}
	}
	return true
}

// PredictIIM returns the IIM value that a writer derives from the XMP value
// xmp: xmp truncated to maxLen bytes at a UTF-8 boundary, exactly as the iptc
// package truncates a value it writes. The result is a substring of xmp; it
// does not allocate.
func PredictIIM(xmp string, maxLen int) string {
	if maxLen <= 0 || len(xmp) <= maxLen {
		return xmp
	}
	t := xmp[:maxLen]
	for t != "" && !utf8.ValidString(t) {
		t = t[:len(t)-1]
	}
	return t
}

// IIMChanged reports whether the IIM value iim differs from the value
// predicted from the XMP value xmp (PredictIIM). It applies MWG §4.2.3.2,
// scenario (4): when the IPTC digest does not match, "the XMP value is used
// to create a predicted IPTC-IIM value, taking value truncation and character
// encoding into account. If the predicted and actual IPTC-IIM values match,
// then the XMP value is used. Otherwise the IPTC-IIM value is used."
// Toolkit equivalent: PhotoDataUtils::IsValueDifferent against the IIM
// predicted by PhotoDataUtils::ExportIPTC.
func IIMChanged(iim, xmp string, maxLen int) bool {
	return iim != PredictIIM(xmp, maxLen)
}

// IIMListChanged is IIMChanged for a repeatable DataSet: iim holds the IIM
// occurrences in order, and xmpItems the items of the XMP array joined with
// U+001E, the representation of xmp.XMP.Properties. The lists match when they
// have the same length and every IIM item equals the prediction of the XMP
// item at the same position; empty XMP items are ignored. It does not
// allocate.
func IIMListChanged(iim []string, xmpItems string, maxLen int) bool {
	rest := xmpItems
	for _, v := range iim {
		item := ""
		for item == "" && rest != "" {
			// Empty XMP items are skipped, as xmp.XMP.Keywords skips them.
			item, rest, _ = strings.Cut(rest, itemSep)
		}
		if item == "" || v != PredictIIM(item, maxLen) {
			return true
		}
	}
	for rest != "" {
		var item string
		if item, rest, _ = strings.Cut(rest, itemSep); item != "" {
			return true
		}
	}
	return false
}

// JoinArtist returns the EXIF Artist string of the list items (MWG §5.7): the
// items joined with "; ", where an item that contains "; " or begins with a
// quote is enclosed in quotes (U+0022) and its quotes are doubled. "If
// recognized, individual values in the TIFF Artist form MUST be enclosed in
// quotes if they contain a semicolon-space or begin with a quote [...].
// Embedded quotes MUST be doubled when an individual value is enclosed in
// quotes", as MWG §5.7 states.
func JoinArtist(items []string) string {
	quote := false
	for _, it := range items {
		quote = quote || needsArtistQuotes(it)
	}
	if !quote {
		// The common case: the plain "; "-joined list.
		return strings.Join(items, artistSep)
	}
	n := 0
	for _, it := range items {
		n += len(it) + len(artistSep) + 2 + strings.Count(it, `"`)
	}
	var b strings.Builder
	b.Grow(n)
	for i, it := range items {
		if i > 0 {
			b.WriteString(artistSep)
		}
		if !needsArtistQuotes(it) {
			b.WriteString(it)
			continue
		}
		b.WriteByte('"')
		for {
			j := strings.IndexByte(it, '"')
			if j < 0 {
				break
			}
			b.WriteString(it[:j+1])
			b.WriteByte('"')
			it = it[j+1:]
		}
		b.WriteString(it)
		b.WriteByte('"')
	}
	return b.String()
}

// needsArtistQuotes reports whether an Artist list item must be quoted
// (MWG §5.7).
func needsArtistQuotes(it string) bool {
	return strings.HasPrefix(it, `"`) || strings.Contains(it, artistSep)
}

// SplitArtist returns the items of the EXIF Artist list s (MWG §5.7), the
// inverse of JoinArtist: s is split at each "; " that is not inside a quoted
// item, and a quoted item loses its enclosing quotes and has its doubled
// quotes undoubled. An unterminated quoted item extends to the end of s, and
// text between the closing quote and the next separator is kept. SplitArtist
// returns nil for the empty string.
func SplitArtist(s string) []string {
	if s == "" {
		return nil
	}
	items := make([]string, 0, strings.Count(s, artistSep)+1)
	for {
		it, rest, more := nextArtist(s)
		items = append(items, it)
		if !more {
			return items
		}
		s = rest
	}
}

// FirstArtist returns the first item of the EXIF Artist list s (MWG §5.7). It
// allocates only when the first item is quoted and contains doubled quotes.
func FirstArtist(s string) string {
	it, _, _ := nextArtist(s)
	return it
}

// nextArtist returns the first item of the Artist list s, the rest of s after
// its separator, and whether a separator followed the item.
func nextArtist(s string) (item, rest string, more bool) {
	if !strings.HasPrefix(s, `"`) {
		item, rest, more = strings.Cut(s, artistSep)
		return item, rest, more
	}
	// Quoted item: the content runs to the first quote that is not doubled.
	body := s[1:]
	end := -1 // index in body of the closing quote
	doubled := false
	for i := 0; i < len(body); i++ {
		if body[i] != '"' {
			continue
		}
		if i+1 < len(body) && body[i+1] == '"' {
			doubled = true
			i++
			continue
		}
		end = i
		break
	}
	if end < 0 {
		// Unterminated: the item is the remainder of the string.
		return unquoteArtist(body, doubled), "", false
	}
	item = unquoteArtist(body[:end], doubled)
	tail := body[end+1:]
	extra, rest, more := strings.Cut(tail, artistSep)
	if extra != "" {
		item += extra
	}
	return item, rest, more
}

// unquoteArtist undoubles the quotes of a quoted Artist item when doubled is
// true; otherwise it returns s unchanged.
func unquoteArtist(s string, doubled bool) string {
	if !doubled {
		return s
	}
	return strings.ReplaceAll(s, `""`, `"`)
}

// ParseXMPDate parses an XMP Date value: the W3C date and time profile of
// ISO 8601 that the Adobe XMP Specification Part 1 §8.2.1.2 (ISO 16684-1:2019
// §8.2.1.2) prescribes — YYYY, YYYY-MM, YYYY-MM-DD, YYYY-MM-DDThh:mmTZD,
// YYYY-MM-DDThh:mm:ssTZD or YYYY-MM-DDThh:mm:ss.sTZD, where the time zone
// designator TZD (Z, +hh:mm or -hh:mm) is optional (MWG §4.2.3.4, "Time-zone
// handling"). hasTime and hasZone report which parts were present. A value
// without a time zone is returned in UTC; missing month and day parts are 1.
func ParseXMPDate(s string) (t time.Time, hasTime, hasZone, ok bool) {
	year, month, day, p, haveDay, ok := xmpDay(s)
	if !ok {
		return time.Time{}, false, false, false
	}
	var c clock
	loc := time.UTC
	if haveDay && p < len(s) && s[p] == 'T' {
		hasTime = true
		if c, p, ok = xmpClock(s, p+1); !ok {
			return time.Time{}, false, false, false
		}
		if p < len(s) {
			if loc, ok = xmpZone(s[p:]); !ok {
				return time.Time{}, false, false, false
			}
			hasZone, p = true, len(s)
		}
	}
	if p != len(s) || !validDate(year, month, day, c) {
		return time.Time{}, false, false, false
	}
	return time.Date(year, time.Month(month), day, c.hour, c.minute, c.sec, c.nsec, loc), hasTime, hasZone, true
}

// clock is a time of day.
type clock struct {
	hour, minute, sec, nsec int
}

// xmpDay parses the "YYYY[-MM[-DD]]" prefix of an XMP date and returns the
// position after it.
func xmpDay(s string) (year, month, day, end int, haveDay, ok bool) {
	month, day = 1, 1
	year, p, ok := digits(s, 0, 4)
	if !ok || p == len(s) || s[p] != '-' {
		return year, month, day, p, false, ok
	}
	if month, p, ok = digits(s, p+1, 2); !ok || p == len(s) || s[p] != '-' {
		return year, month, day, p, false, ok
	}
	day, p, ok = digits(s, p+1, 2)
	return year, month, day, p, ok, ok
}

// xmpClock parses the "hh:mm[:ss[.s]]" time of an XMP date at s[p:].
func xmpClock(s string, p int) (c clock, end int, ok bool) {
	if c.hour, p, ok = digits(s, p, 2); !ok || p >= len(s) || s[p] != ':' {
		return c, p, false
	}
	if c.minute, p, ok = digits(s, p+1, 2); !ok || p == len(s) || s[p] != ':' {
		return c, p, ok
	}
	if c.sec, p, ok = digits(s, p+1, 2); !ok || p == len(s) || s[p] != '.' {
		return c, p, ok
	}
	c.nsec, p, ok = fraction(s, p+1)
	return c, p, ok
}

// xmpZone parses a whole time zone designator: "Z", "+hh:mm" or "-hh:mm". It
// returns UTC for "Z" and a fixed zone named by the offset otherwise.
func xmpZone(z string) (*time.Location, bool) {
	if z == "Z" {
		return time.UTC, true
	}
	if len(z) != len("+hh:mm") || z[3] != ':' {
		return nil, false
	}
	return offsetZone(z, z[:3], z[4:])
}

// offsetZone returns the fixed zone of the signed offset whose hour digits
// are hh and minute digits mm (z[0] is the sign), named "+hh:mm" like an EXIF
// offset tag.
func offsetZone(z, hh, mm string) (*time.Location, bool) {
	if z[0] != '+' && z[0] != '-' {
		return nil, false
	}
	h, _, okH := digits(hh, len(hh)-2, 2)
	m, _, okM := digits(mm, 0, 2)
	if !okH || !okM || h > 23 || m > 59 {
		return nil, false
	}
	off := h*3600 + m*60
	if z[0] == '-' {
		off = -off
	}
	name := [6]byte{z[0], hh[len(hh)-2], hh[len(hh)-1], ':', mm[0], mm[1]}
	return time.FixedZone(string(name[:]), off), true
}

// ParseIIMDate parses IIM DataSets 2:55 Date Created ("CCYYMMDD") and 2:60
// Time Created ("HHMMSS±HHMM"), IIM 4.2 §2.2.23 and §2.2.24. As the Toolkit
// does (ReconcileIPTC.cpp:ImportIPTC_Date), it also accepts dashes between
// the date parts and colons between the time parts, a time without a time
// zone, and a time zone preceded by nothing else than spaces or NULs is
// treated as absent; the IIM "00" of an unknown month or day reads as 1.
// Other out-of-range fields make the value invalid. A date without a time, or
// a time without a zone, is returned in UTC.
func ParseIIMDate(date, tm string) (time.Time, bool) {
	year, month, day, ok := iimDay(date)
	if !ok {
		return time.Time{}, false
	}
	c, loc, ok := iimClock(strings.TrimRight(tm, " \x00"))
	if !ok || !validDate(year, month, day, c) {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), day, c.hour, c.minute, c.sec, 0, loc), true
}

// iimDay parses an IIM date "CCYYMMDD" (or "CCYY-MM-DD"); "00" reads as 1.
func iimDay(date string) (year, month, day int, ok bool) {
	year, p, ok := digits(date, 0, 4)
	if !ok {
		return 0, 0, 0, false
	}
	if month, p, ok = digits(date, skipByte(date, p, '-'), 2); !ok {
		return 0, 0, 0, false
	}
	if day, p, ok = digits(date, skipByte(date, p, '-'), 2); !ok || p != len(date) {
		return 0, 0, 0, false
	}
	return year, max(month, 1), max(day, 1), true
}

// iimClock parses an IIM time "HHMMSS[±HHMM]" (or with colons); the empty
// string is midnight UTC.
func iimClock(tm string) (c clock, loc *time.Location, ok bool) {
	loc = time.UTC
	if tm == "" {
		return c, loc, true
	}
	p := 0
	if c.hour, p, ok = digits(tm, p, 2); ok {
		c.minute, p, ok = digits(tm, skipByte(tm, p, ':'), 2)
	}
	if ok {
		c.sec, p, ok = digits(tm, skipByte(tm, p, ':'), 2)
	}
	if !ok || p == len(tm) {
		return c, loc, ok
	}
	// The zone: "±HHMM" or "±HH:MM".
	z := tm[p:]
	switch {
	case len(z) == len("+hhmm"):
		loc, ok = offsetZone(z, z[:3], z[3:])
	case len(z) == len("+hh:mm") && z[3] == ':':
		loc, ok = offsetZone(z, z[:3], z[4:])
	default:
		ok = false
	}
	return c, loc, ok
}

// IIMDateMatches reports whether the IIM date and time strings equal the
// values predicted from the XMP date xmp, as written by the iptc package and
// by the Toolkit (ReconcileIPTC.cpp:ExportIPTC_Date): "CCYYMMDD", and
// "HHMMSS±HHMM" when the XMP value has a time zone, "HHMMSS" when it has a
// time without a zone, nothing when it has no time. It returns false when xmp
// is not a valid XMP date.
func IIMDateMatches(date, tm, xmp string) bool {
	t, hasTime, hasZone, ok := ParseXMPDate(xmp)
	if !ok {
		return false
	}
	var buf [16]byte
	if string(t.AppendFormat(buf[:0], "20060102")) != date {
		return false
	}
	switch {
	case hasZone:
		return string(t.AppendFormat(buf[:0], "150405-0700")) == tm
	case hasTime:
		return string(t.AppendFormat(buf[:0], "150405")) == tm
	default:
		return tm == ""
	}
}

// digits parses exactly n decimal digits of s starting at p and returns their
// value and the position after them.
func digits(s string, p, n int) (v, end int, ok bool) {
	if p < 0 || len(s)-p < n {
		return 0, p, false
	}
	for i := p; i < p+n; i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, p, false
		}
		v = v*10 + int(c-'0')
	}
	return v, p + n, true
}

// fraction parses one or more decimal digits of a fractional second at s[p:]
// and returns the nanoseconds (digits beyond the ninth are ignored).
func fraction(s string, p int) (nsec, end int, ok bool) {
	start := p
	scale := 100_000_000
	for p < len(s) && s[p] >= '0' && s[p] <= '9' {
		nsec += int(s[p]-'0') * scale
		scale /= 10
		p++
	}
	return nsec, p, p > start
}

// skipByte returns p+1 when s[p] is c, p otherwise.
func skipByte(s string, p int, c byte) int {
	if p < len(s) && s[p] == c {
		return p + 1
	}
	return p
}

// validDate reports whether the fields name an existing date and a time of
// day (no leap second).
func validDate(year, month, day int, c clock) bool {
	if month < 1 || month > 12 || day < 1 || c.hour > 23 || c.minute > 59 || c.sec > 59 {
		return false
	}
	return day <= time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
