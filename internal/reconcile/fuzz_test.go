package reconcile

import (
	"slices"
	"testing"
)

// FuzzArtist checks the EXIF Artist list functions on arbitrary strings:
// they never panic, FirstArtist is the first item of SplitArtist, and the
// list survives a JoinArtist/SplitArtist round trip.
func FuzzArtist(f *testing.F) {
	for _, s := range []string{
		"", "a", "a; b", mwgExample2, `"`, `""`, `"a""b"; c`, `"a; b`, `"a"x; b`, "a; ", "; ; ",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		items := SplitArtist(s)
		if s == "" {
			if items != nil {
				t.Fatalf("SplitArtist(\"\") = %q, want nil", items)
			}
			return
		}
		if len(items) == 0 || FirstArtist(s) != items[0] {
			t.Fatalf("FirstArtist(%q) = %q, SplitArtist = %q", s, FirstArtist(s), items)
		}
		joined := JoinArtist(items)
		again := SplitArtist(joined)
		if joined == "" {
			// A single empty item joins to the empty string.
			if !slices.Equal(items, []string{""}) {
				t.Fatalf("JoinArtist(%q) = \"\"", items)
			}
			return
		}
		if !slices.Equal(again, items) {
			t.Fatalf("round trip of %q: SplitArtist(%q) = %q, want %q", s, joined, again, items)
		}
	})
}

// FuzzParseDates checks the date parsers on arbitrary strings: they never
// panic, a parsed XMP date is predicted by IIMDateMatches from its own
// export, and a parsed IIM date has valid fields.
func FuzzParseDates(f *testing.F) {
	for _, s := range []string{
		"2021-07-04T10:20:30.25-05:30", "2021", "2021-07-04T10:20Z", "20210704", "102030+0200", "2021-02-30",
	} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if tm, hasTime, hasZone, ok := ParseXMPDate(a); ok {
			if !hasTime && hasZone {
				t.Fatalf("ParseXMPDate(%q): zone without time", a)
			}
			date := tm.Format("20060102")
			clock := ""
			switch {
			case hasZone:
				clock = tm.Format("150405-0700")
			case hasTime:
				clock = tm.Format("150405")
			}
			if tm.Year() <= 9999 && !IIMDateMatches(date, clock, a) {
				t.Fatalf("IIMDateMatches(%q, %q, %q) = false for the export of the same date", date, clock, a)
			}
		}
		if tm, ok := ParseIIMDate(a, b); ok && (tm.Year() > 9999 || tm.Month() < 1) {
			t.Fatalf("ParseIIMDate(%q, %q) = %v", a, b, tm)
		}
	})
}
