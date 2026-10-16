package xmp

// task330_test.go — regression battery for task #330: SetCaption and
// SetCopyright replaced the whole language alternative with one x-default
// item (XMP Part 1 §8.2.2.4; Toolkit XMPMeta::SetLocalizedText).

import (
	"strings"
	"testing"
)

// TestTask330SetDefaultText pins the x-default case of SetLocalizedText on
// the flat form of a language alternative (checklist RDF-11).
func TestTask330SetDefaultText(t *testing.T) {
	t.Parallel()
	const sep = "\x1e"
	tests := []struct {
		name, old, want string
	}{
		{"absent", "", "new"},
		{"x-default only", "old", "new"},
		{"x-default first, one language", "old" + sep + "fr|a", "new" + sep + "fr|a"},
		{"x-default last, one language", "fr|a" + sep + "old", "new" + sep + "fr|a"},
		{"x-default first, two languages", "old" + sep + "fr|a" + sep + "de|b", "new" + sep + "fr|a" + sep + "de|b"},
		{"x-default in the middle, two languages", "fr|a" + sep + "old" + sep + "de|b", "new" + sep + "fr|a" + sep + "de|b"},
		{"x-default last, two languages", "fr|a" + sep + "de|b" + sep + "old", "new" + sep + "de|b" + sep + "fr|a"},
		{"x-default absent, one language", "fr|a", "new" + sep + "fr|a"},
		{"x-default absent, two languages", "fr|a" + sep + "de|b", "new" + sep + "fr|a" + sep + "de|b"},
		{"translations that repeat the old default follow it", "old" + sep + "fr|old" + sep + "de|b", "new" + sep + "fr|new" + sep + "de|b"},
		{"every item repeats the old default, x-default last", "fr|old" + sep + "de|old" + sep + "old", "new" + sep + "de|new" + sep + "fr|new"},
		{"explicit x-default tag is x-default", "x-default|old" + sep + "fr|a", "new" + sep + "fr|a"},
		{"default text containing a bar is not a language tag", "a | b", "new"},
		{"bar in the new text is stored as is", "old" + sep + "fr|a", ""},
	}
	for _, tc := range tests {
		for _, setter := range []struct {
			name  string
			local string
			set   func(*XMP, string)
		}{
			{"SetCaption", "description", (*XMP).SetCaption},
			{"SetCopyright", "rights", (*XMP).SetCopyright},
		} {
			t.Run("RDF-11/"+setter.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				x := &XMP{Properties: map[string]map[string]string{}}
				if tc.old != "" {
					x.Set(NSdc, setter.local, tc.old)
				}
				value, want := "new", tc.want
				if tc.want == "" { // the bar case
					value, want = "new | text", "new | text"+sepAndRest(tc.old)
				}
				setter.set(x, value)
				if got := x.Get(NSdc, setter.local); got != want {
					t.Errorf("after %s(%q) on %q: %q, want %q", setter.name, value, tc.old, got, want)
				}
			})
		}
	}
}

// sepAndRest returns the items of old after its first, each preceded by the
// item separator.
func sepAndRest(old string) string {
	_, rest, _ := strings.Cut(old, "\x1e")
	return "\x1e" + rest
}

// TestTask330ParsedAlternativeKeepsLanguages is the end-to-end scenario: a
// parsed language alternative keeps its other languages through SetCaption,
// Encode and a second Parse.
func TestTask330ParsedAlternativeKeepsLanguages(t *testing.T) {
	t.Parallel()
	x := mustParse(t, aliasDoc(
		`<dc:description><rdf:Alt><rdf:li xml:lang="fr">Bonjour</rdf:li><rdf:li xml:lang="x-default">Hello</rdf:li><rdf:li xml:lang="de">Hallo</rdf:li></rdf:Alt></dc:description>`+
			`<dc:rights><rdf:Alt><rdf:li xml:lang="x-default">(c) A</rdf:li><rdf:li xml:lang="fr">(c) A fr</rdf:li></rdf:Alt></dc:rights>`))
	x.SetCaption("Hi")
	x.SetCopyright("(c) B")
	out := mustEncode(t, x)
	again := mustParse(t, out)
	if got := again.Caption(); got != "Hi" {
		t.Errorf("Caption = %q, want Hi", got)
	}
	if got := again.Get(NSdc, "description"); got != "Hi\x1efr|Bonjour\x1ede|Hallo" {
		t.Errorf("description = %q", got)
	}
	if got := again.Copyright(); got != "(c) B" {
		t.Errorf("Copyright = %q, want (c) B", got)
	}
	if got := again.Get(NSdc, "rights"); got != "(c) B\x1efr|(c) A fr" {
		t.Errorf("rights = %q", got)
	}
	if s := string(out); !strings.Contains(s, `xml:lang="fr">Bonjour<`) || !strings.Contains(s, `xml:lang="de">Hallo<`) {
		t.Errorf("languages missing from the packet:\n%s", s)
	}
}

// TestTask330OtherSettersReplace checks that the setters of non-alternative
// properties still replace the whole value.
func TestTask330OtherSettersReplace(t *testing.T) {
	t.Parallel()
	x := &XMP{Properties: map[string]map[string]string{}}
	x.SetCreator("a")
	x.SetCreator("b")
	if got := x.Get(NSdc, "creator"); got != "b" {
		t.Errorf("creator = %q, want b", got)
	}
}
