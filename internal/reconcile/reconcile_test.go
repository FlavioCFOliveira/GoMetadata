package reconcile

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// TestTables_MatchToolkit pins the Toolkit tables: their sizes, a sample of
// rows transcribed by hand from ReconcileTIFF.cpp and IPTC_Support.cpp
// (commit 7093513bd3ca), and their structural invariants.
func TestTables_MatchToolkit(t *testing.T) {
	t.Parallel()
	if n := len(primaryIFDMappings); n != 25 {
		t.Errorf("sPrimaryIFDMappings has %d rows, want 25", n)
	}
	if n := len(exifIFDMappings); n != 68 {
		t.Errorf("sExifIFDMappings has %d rows, want 68", n)
	}
	if n := len(gpsIFDMappings); n != 27 {
		t.Errorf("sGPSInfoIFDMappings has %d rows, want 27", n)
	}
	if n := len(iimDataSets); n != 56 {
		t.Errorf("kKnownDataSets has %d rows, want 56", n)
	}

	tiffRows := []struct {
		ifd  IFD
		want TIFFMapping
	}{
		{IFDPrimary, TIFFMapping{Tag: 274, Type: TypeShort, Count: 1, Export: ExportNoDelete, NS: nsTIFF, Name: "Orientation"}},
		{IFDPrimary, TIFFMapping{Tag: 270, Type: TypeASCII, Export: ExportAlways}},
		{IFDPrimary, TIFFMapping{Tag: 271, Type: TypeASCII, Export: ExportInjectOnly, NS: nsTIFF, Name: "Make"}},
		{IFDPrimary, TIFFMapping{Tag: 301, Type: TypeShort, Count: 768, Export: ExportNever, NS: nsTIFF, Name: "TransferFunction"}},
		{IFDExif, TIFFMapping{Tag: 33434, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "ExposureTime"}},
		{IFDExif, TIFFMapping{Tag: 40962, Type: TypeShortOrLong, Count: 1, Export: ExportInjectOnly, NS: nsEXIF, Name: "PixelXDimension"}},
		{IFDExif, TIFFMapping{Tag: 42036, Type: TypeASCII, Export: ExportInjectOnly, NS: nsExifEX, Name: "LensModel"}},
		{IFDExif, TIFFMapping{Tag: 34855, Type: TypeShort, Count: 1, Export: ExportInjectOnly}},
		{IFDGPS, TIFFMapping{Tag: 6, Type: TypeRational, Count: 1, Export: ExportAlways, NS: nsEXIF, Name: "GPSAltitude"}},
		{IFDGPS, TIFFMapping{Tag: 31, Type: TypeRational, Count: 1, Export: ExportInjectOnly, NS: nsExifEX, Name: "GPSHPositioningError"}},
	}
	for _, r := range tiffRows {
		got, ok := TIFF(r.ifd, r.want.Tag)
		if !ok || got != r.want {
			t.Errorf("TIFF(%d, %d) = %+v, %v; want %+v", r.ifd, r.want.Tag, got, ok, r.want)
		}
	}
	if _, ok := TIFF(IFDNone, 274); ok {
		t.Error("TIFF(IFDNone, 274) found a row")
	}
	if _, ok := TIFF(IFDPrimary, 0x8769); ok {
		t.Error("TIFF(IFDPrimary, 0x8769) found a row; the Toolkit does not map the Exif IFD pointer")
	}

	iimRows := []IIMMapping{
		{DataSet: 3, Form: IIMUnmappedText, MaxLen: 67},
		{DataSet: 5, Form: IIMLangAlt, MaxLen: 64, NS: nsDC, Name: "title"},
		{DataSet: 25, Form: IIMArray, MaxLen: 64, NS: nsDC, Name: "subject"},
		{DataSet: 55, Form: IIMSpecial, MaxLen: 8, NS: nsPhotoshop, Name: "DateCreated"},
		{DataSet: 80, Form: IIM3Way, MaxLen: 32},
		{DataSet: 116, Form: IIM3Way, MaxLen: 128},
		{DataSet: 120, Form: IIM3Way, MaxLen: 2000},
		{DataSet: 202, Form: IIMUnmappedBin, MaxLen: 256000},
	}
	for _, want := range iimRows {
		got, ok := IIM(want.DataSet)
		if !ok || got != want {
			t.Errorf("IIM(%d) = %+v, %v; want %+v", want.DataSet, got, ok, want)
		}
	}
	for _, ds := range []uint8{0, 1, 2, 6, 254, 255} {
		if _, ok := IIM(ds); ok {
			t.Errorf("IIM(%d) found a row for an unknown DataSet", ds)
		}
	}

	// The IIM table is sorted and unique (IIM uses a binary search).
	if !slices.IsSortedFunc(iimDataSets[:], func(a, b IIMMapping) int { return int(a.DataSet) - int(b.DataSet) }) {
		t.Error("kKnownDataSets is not sorted by DataSet")
	}
	for i := 1; i < len(iimDataSets); i++ {
		if iimDataSets[i].DataSet == iimDataSets[i-1].DataSet {
			t.Errorf("DataSet %d appears twice", iimDataSets[i].DataSet)
		}
	}
	// Every IIM mapping that names an XMP property has a mapped form, and
	// every tag appears once per directory.
	for _, m := range iimDataSets {
		if (m.Name != "") != (m.Form <= IIMSpecial) {
			t.Errorf("DataSet %d: form %d with XMP name %q", m.DataSet, m.Form, m.Name)
		}
	}
	for _, ifd := range []IFD{IFDPrimary, IFDExif, IFDGPS} {
		seen := map[uint16]bool{}
		for _, m := range TIFFMappings(ifd) {
			if seen[m.Tag] {
				t.Errorf("IFD %d: tag %d appears twice", ifd, m.Tag)
			}
			seen[m.Tag] = true
			if (m.NS == "") != (m.Name == "") {
				t.Errorf("IFD %d tag %d: namespace %q with name %q", ifd, m.Tag, m.NS, m.Name)
			}
		}
	}
}

// TestNamespaces_MatchXMPPackage proves that the namespace constants of this
// package equal the ones of the xmp package.
func TestNamespaces_MatchXMPPackage(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{nsTIFF, xmp.NStiff}, {nsEXIF, xmp.NSexif}, {nsAux, xmp.NSaux},
		{nsDC, xmp.NSdc}, {nsPhotoshop, xmp.NSphotoshop}, {nsIPTCCore, xmp.NSiptcCore},
		// CIPA DC-010-2012 §5.1: the namespace of the Exif 2.3 properties.
		{nsExifEX, "http://cipa.jp/exif/1.0/"},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("namespace %q, want %q", p[0], p[1])
		}
	}
}

// TestFields_AgreeWithToolkitTables proves every field of the reconciliation
// table against the Toolkit rows: the EXIF tag is a Toolkit-mapped tag of its
// directory, and the XMP property named by the Toolkit row (when the row is
// not a special mapping) is a property of the field; the IIM DataSet is a
// Toolkit DataSet whose XMP property (when named) is the field's.
func TestFields_AgreeWithToolkitTables(t *testing.T) {
	t.Parallel()
	// Fields whose Toolkit EXIF row is a special mapping (empty name):
	// ImageDescription, Copyright, Artist, DateTimeOriginal,
	// PhotographicSensitivity, GPSLatitude and GPSLongitude.
	special := map[Field]bool{
		Description: true, Copyright: true, Creator: true, DateTimeOriginal: true,
		ISO: true, GPSLatitude: true, GPSLongitude: true,
	}
	for f := range FieldCount {
		mp := Lookup(f)
		if mp.Read == 0 || mp.XMP[0].Name == "" {
			t.Errorf("field %d has no read order or no XMP property", f)
		}
		if mp.IFD != IFDNone {
			row, ok := TIFF(mp.IFD, mp.Tag)
			if !ok {
				t.Errorf("field %d: tag 0x%04X is not in the Toolkit table of IFD %d", f, mp.Tag, mp.IFD)
				continue
			}
			if (row.Name == "") != special[f] {
				t.Errorf("field %d: Toolkit row name %q, special mapping expected %v", f, row.Name, special[f])
			}
			if row.Name != "" && !hasXMP(mp, row.NS, row.Name) {
				t.Errorf("field %d: Toolkit maps tag 0x%04X to %s%s, which the field does not list", f, mp.Tag, row.NS, row.Name)
			}
		}
		if mp.DataSet == 0 && mp.IIMMaxLen != 0 {
			t.Errorf("field %d: IIMMaxLen %d without a DataSet", f, mp.IIMMaxLen)
		}
		if mp.DataSet != 0 {
			row, ok := IIM(mp.DataSet)
			if !ok {
				t.Errorf("field %d: DataSet %d is not in the Toolkit table", f, mp.DataSet)
				continue
			}
			if row.Name != "" && !hasXMP(mp, row.NS, row.Name) {
				t.Errorf("field %d: Toolkit maps DataSet %d to %s%s, which the field does not list", f, mp.DataSet, row.NS, row.Name)
			}
			if uint32(mp.IIMMaxLen) != row.MaxLen {
				t.Errorf("field %d: IIMMaxLen %d, Toolkit length of DataSet %d is %d", f, mp.IIMMaxLen, mp.DataSet, row.MaxLen)
			}
		}
	}
	// IIMMaxLen reads the Toolkit lengths (IIM 4.2 §2.2).
	for f, want := range map[Field]int{Description: 2000, Copyright: 128, Creator: 32, Keywords: 64, DateTimeOriginal: 8, Make: 0} {
		if got := IIMMaxLen(f); got != want {
			t.Errorf("IIMMaxLen(%d) = %d, want %d", f, got, want)
		}
	}
}

func hasXMP(mp *Mapping, ns, name string) bool {
	for _, p := range mp.XMP {
		if p.NS == ns && p.Name == name {
			return true
		}
	}
	return false
}

func TestIsBlank(t *testing.T) {
	t.Parallel()
	for s, want := range map[string]bool{
		"": true, " ": true, "   ": true, "\x00": true, " \x00 \x00": true,
		"a": false, " a ": false, "\t": false, "\x00x": false,
	} {
		if got := IsBlank(s); got != want {
			t.Errorf("IsBlank(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestPredictIIM(t *testing.T) {
	t.Parallel()
	tests := []struct {
		xmp    string
		maxLen int
		want   string
	}{
		{"", 32, ""},
		{"short", 32, "short"},
		{"abcdef", 3, "abc"},
		{"abcdef", 0, "abcdef"},
		// "é" is two bytes: a cut inside it backs off to the rune start.
		{"aé", 2, "a"},
		{"aé", 3, "aé"},
		{"✓✓", 4, "✓"},
	}
	for _, tc := range tests {
		if got := PredictIIM(tc.xmp, tc.maxLen); got != tc.want {
			t.Errorf("PredictIIM(%q, %d) = %q, want %q", tc.xmp, tc.maxLen, got, tc.want)
		}
	}
	if !IIMChanged("edited", "original", 32) {
		t.Error("IIMChanged reported an edited IIM value as unchanged")
	}
	long := strings.Repeat("x", 40)
	if IIMChanged(long[:32], long, 32) {
		t.Error("IIMChanged reported the truncated XMP value as a change")
	}
}

func TestIIMListChanged(t *testing.T) {
	t.Parallel()
	k70 := strings.Repeat("k", 70)
	tests := []struct {
		name string
		iim  []string
		xmp  string
		want bool
	}{
		{"equal", []string{"a", "b"}, "a\x1eb", false},
		{"truncated item", []string{k70[:64], "short"}, k70 + "\x1eshort", false},
		{"edited item", []string{"a", "c"}, "a\x1eb", true},
		{"fewer IIM items", []string{"a"}, "a\x1eb", true},
		{"more IIM items", []string{"a", "b"}, "a", true},
		{"no XMP", []string{"a"}, "", true},
		{"both empty", nil, "", false},
		{"empty XMP items are skipped", []string{"a", "b"}, "\x1ea\x1e\x1eb\x1e", false},
		{"order matters", []string{"b", "a"}, "a\x1eb", true},
	}
	for _, tc := range tests {
		if got := IIMListChanged(tc.iim, tc.xmp, 64); got != tc.want {
			t.Errorf("%s: IIMListChanged(%q, %q) = %v, want %v", tc.name, tc.iim, tc.xmp, got, tc.want)
		}
	}
}

// mwgExample2 is example 2 of MWG §5.7 (page 44), with the typographic quotes
// of the PDF rendering written as the U+0022 quotes the text prescribes.
const mwgExample2 = `first; with;semicolon; "with; semicolon-space"; "with; semicolon-space and ""quotes"""; ` +
	`non-leading "quotes"; """leading"" and non-leading ""quotes"""; last`

// mwgExample2Items is the list of MWG §5.7 example 2. ExifTool 13.55 with the
// MWG module returns the same seven items for an Artist tag holding
// mwgExample2 (oracle run of task #308).
var mwgExample2Items = []string{ //nolint:gochecknoglobals // read-only test table
	"first", "with;semicolon", "with; semicolon-space", `with; semicolon-space and "quotes"`,
	`non-leading "quotes"`, `"leading" and non-leading "quotes"`, "last",
}

func TestArtist_MWGExamples(t *testing.T) {
	t.Parallel()
	// MWG §5.7 example 1.
	ex1 := "Camera owner, John Smith; Photographer, Michael Brown; Image creator, Ken James"
	want1 := []string{"Camera owner, John Smith", "Photographer, Michael Brown", "Image creator, Ken James"}
	if got := SplitArtist(ex1); !slices.Equal(got, want1) {
		t.Errorf("SplitArtist(example 1) = %q, want %q", got, want1)
	}
	if got := JoinArtist(want1); got != ex1 {
		t.Errorf("JoinArtist(example 1) = %q, want %q", got, ex1)
	}
	// MWG §5.7 example 2.
	if got := SplitArtist(mwgExample2); !slices.Equal(got, mwgExample2Items) {
		t.Errorf("SplitArtist(example 2) = %q, want %q", got, mwgExample2Items)
	}
	if got := JoinArtist(mwgExample2Items); got != mwgExample2 {
		t.Errorf("JoinArtist(example 2) = %q, want %q", got, mwgExample2)
	}
	if got := FirstArtist(mwgExample2); got != "first" {
		t.Errorf("FirstArtist(example 2) = %q", got)
	}
}

func TestArtist_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		artist string
		items  []string
		first  string
	}{
		{"", nil, ""},
		{"solo", []string{"solo"}, "solo"},
		// Oracle run of task #308: ExifTool writes and reads these forms.
		{`"Solo ""Creator""; Esq."`, []string{`Solo "Creator"; Esq.`}, `Solo "Creator"; Esq.`},
		{`Alice Example; """Bob""; the second"; Carol`, []string{"Alice Example", `"Bob"; the second`, "Carol"}, "Alice Example"},
		{"a; ", []string{"a", ""}, "a"},
		{"; b", []string{"", "b"}, ""},
		{`""`, []string{""}, ""},
		{`""""`, []string{`"`}, `"`},
		// An unterminated quoted item runs to the end; text after a closing
		// quote is kept.
		{`"a; b`, []string{"a; b"}, "a; b"},
		{`"a"x; b`, []string{"ax", "b"}, "ax"},
		{"a;; b", []string{"a;", "b"}, "a;"},
	}
	for _, tc := range tests {
		if got := SplitArtist(tc.artist); !slices.Equal(got, tc.items) {
			t.Errorf("SplitArtist(%q) = %q, want %q", tc.artist, got, tc.items)
		}
		if got := FirstArtist(tc.artist); got != tc.first {
			t.Errorf("FirstArtist(%q) = %q, want %q", tc.artist, got, tc.first)
		}
	}
	// JoinArtist quotes exactly the items that need it.
	joins := map[string][]string{
		`Solo "Creator", Esq.`:                        {`Solo "Creator", Esq.`},
		`"Solo ""Creator""; Esq."`:                    {`Solo "Creator"; Esq.`},
		`"""quoted"""; plain`:                         {`"quoted"`, "plain"},
		`Alice Example; """Bob""; the second"; Carol`: {"Alice Example", `"Bob"; the second`, "Carol"},
		"":    nil,
		"; x": {"", "x"},
	}
	for want, items := range joins {
		if got := JoinArtist(items); got != want {
			t.Errorf("JoinArtist(%q) = %q, want %q", items, got, want)
		}
	}
}

func TestParseXMPDate(t *testing.T) {
	t.Parallel()
	plus2 := time.FixedZone("+02:00", 7200)
	tests := []struct {
		in               string
		want             time.Time
		hasTime, hasZone bool
		ok               bool
	}{
		{"2021", time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), false, false, true},
		{"2021-07", time.Date(2021, 7, 1, 0, 0, 0, 0, time.UTC), false, false, true},
		{"2021-07-04", time.Date(2021, 7, 4, 0, 0, 0, 0, time.UTC), false, false, true},
		{"2021-07-04T10:20+02:00", time.Date(2021, 7, 4, 10, 20, 0, 0, plus2), true, true, true},
		{"2021-07-04T10:20:30", time.Date(2021, 7, 4, 10, 20, 30, 0, time.UTC), true, false, true},
		{"2021-07-04T10:20:30Z", time.Date(2021, 7, 4, 10, 20, 30, 0, time.UTC), true, true, true},
		{"2021-07-04T10:20:30.25-05:30", time.Date(2021, 7, 4, 10, 20, 30, 250_000_000, time.FixedZone("-05:30", -19800)), true, true, true},
		{"", time.Time{}, false, false, false},
		{"21-07-04", time.Time{}, false, false, false},
		{"2021-13-01", time.Time{}, false, false, false},
		{"2021-02-30", time.Time{}, false, false, false},
		{"2021-07-04T25:00", time.Time{}, false, false, false},
		{"2021-07-04T10", time.Time{}, false, false, false},
		{"2021T10:20", time.Time{}, false, false, false},
		{"2021-07-04T10:20+0200", time.Time{}, false, false, false},
		{"2021-07-04T10:20:30.", time.Time{}, false, false, false},
		{"2021-07-04 10:20:30", time.Time{}, false, false, false},
	}
	for _, tc := range tests {
		got, hasTime, hasZone, ok := ParseXMPDate(tc.in)
		if ok != tc.ok || hasTime != tc.hasTime || hasZone != tc.hasZone || !got.Equal(tc.want) {
			t.Errorf("ParseXMPDate(%q) = %v, %v, %v, %v; want %v, %v, %v, %v",
				tc.in, got, hasTime, hasZone, ok, tc.want, tc.hasTime, tc.hasZone, tc.ok)
		}
	}
}

func TestParseIIMDate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		date, tm string
		want     time.Time
		ok       bool
	}{
		{"20210704", "102030+0200", time.Date(2021, 7, 4, 10, 20, 30, 0, time.FixedZone("+02:00", 7200)), true},
		{"20210704", "102030-0530", time.Date(2021, 7, 4, 10, 20, 30, 0, time.FixedZone("-05:30", -19800)), true},
		{"20210704", "", time.Date(2021, 7, 4, 0, 0, 0, 0, time.UTC), true},
		{"20210704", "102030", time.Date(2021, 7, 4, 10, 20, 30, 0, time.UTC), true},
		{"20210704", "102030  ", time.Date(2021, 7, 4, 10, 20, 30, 0, time.UTC), true},
		// Toolkit tolerance: separators.
		{"2021-07-04", "10:20:30+02:00", time.Date(2021, 7, 4, 10, 20, 30, 0, time.FixedZone("+02:00", 7200)), true},
		// IIM 4.2 §2.2.23: "00" for an unknown month or day.
		{"20210000", "", time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"", "", time.Time{}, false},
		{"2021070", "", time.Time{}, false},
		{"202107041", "", time.Time{}, false},
		{"20211304", "", time.Time{}, false},
		{"20210231", "", time.Time{}, false},
		{"20210704", "246000", time.Time{}, false},
		{"20210704", "102030+02", time.Time{}, false},
		{"20210704", "102030x0200", time.Time{}, false},
	}
	for _, tc := range tests {
		got, ok := ParseIIMDate(tc.date, tc.tm)
		if ok != tc.ok || !got.Equal(tc.want) {
			t.Errorf("ParseIIMDate(%q, %q) = %v, %v; want %v, %v", tc.date, tc.tm, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIIMDateMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		date, tm, xmp string
		want          bool
	}{
		{"20210704", "102030+0200", "2021-07-04T10:20:30+02:00", true},
		{"20210704", "102030", "2021-07-04T10:20:30", true},
		{"20210704", "", "2021-07-04", true},
		{"20210704", "102030+0000", "2021-07-04T10:20:30Z", true},
		{"20210704", "102030+0200", "2021-07-04T10:20:30+03:00", false},
		{"20200101", "000000+0000", "2021-07-04T10:20:30+02:00", false},
		{"20210704", "102030+0200", "not a date", false},
		{"20210704", "102030", "2021-07-04", false},
	}
	for _, tc := range tests {
		if got := IIMDateMatches(tc.date, tc.tm, tc.xmp); got != tc.want {
			t.Errorf("IIMDateMatches(%q, %q, %q) = %v, want %v", tc.date, tc.tm, tc.xmp, got, tc.want)
		}
	}
}

// TestLookups_ZeroAllocs proves that the lookups and the read-path predicates
// do not allocate. No t.Parallel: testing.AllocsPerRun panics in parallel
// tests.
//
//nolint:paralleltest // testing.AllocsPerRun panics if the test (or a parent) is parallel
func TestLookups_ZeroAllocs(t *testing.T) {
	long := strings.Repeat("x", 3000)
	allocs := testing.AllocsPerRun(100, func() {
		_, _ = TIFF(IFDExif, 0xA434)
		_, _ = IIM(120)
		_ = IIMMaxLen(Description)
		_ = Lookup(Orientation)
		_ = IsBlank("   x")
		_ = IIMChanged(long[:2000], long, 2000)
		_ = IIMListChanged([]string{"a", "b"}, "a\x1eb", 64)
		_ = FirstArtist("Alice; Bob")
		_ = FirstArtist(`"Alice; Bob"; Carol`)
	})
	if allocs != 0 {
		t.Errorf("lookups allocate %v times per run, want 0", allocs)
	}
}

func BenchmarkTIFFLookup(b *testing.B) {
	for b.Loop() {
		_, _ = TIFF(IFDExif, 0xA434) // last-but-one third of the Exif table
	}
}

func BenchmarkIIMLookup(b *testing.B) {
	for b.Loop() {
		_, _ = IIM(120)
	}
}

func BenchmarkIIMChanged(b *testing.B) {
	long := strings.Repeat("x", 3000)
	for b.Loop() {
		_ = IIMChanged(long[:2000], long, 2000)
	}
}

func BenchmarkFirstArtist(b *testing.B) {
	for b.Loop() {
		_ = FirstArtist(mwgExample2)
	}
}

func BenchmarkJoinArtist(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = JoinArtist(mwgExample2Items)
	}
}
