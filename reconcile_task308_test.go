package gometadata

// reconcile_task308_test.go — task #308: table-driven EXIF, IPTC and XMP
// reconciliation (internal/reconcile).
//
// The expected values are those that ExifTool 13.55 reported in the oracle
// run of task #308 (exiftool -MWG:all and per-group dumps of the files that
// these tests write); the tests do not run ExifTool.

import (
	"bytes"
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/internal/reconcile"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// task308Values are the values that the oracle run wrote with every setter.
var task308Values = struct { //nolint:gochecknoglobals // read-only test values
	caption, copyright, make, model, lens string
	creators, keywords                    []string
	date                                  time.Time
}{
	caption:   "GoMetadata caption — café ✓",
	copyright: "© 2026 GoMetadata Contributors",
	make:      "GoMake",
	model:     "GoModel X1",
	lens:      "GoLens 50mm F1.8",
	creators:  []string{"Alice Example", `"Bob"; the second`, "Carol"},
	keywords:  []string{"alpha", "beta ✓", "gamma"},
	date:      time.Date(2026, 3, 14, 15, 9, 26, 0, time.FixedZone("", 5*3600+30*60)),
}

// applyTask308Setters calls every convenience setter once, as the oracle run
// did. soloCreator selects SetCreator instead of SetCreators.
func applyTask308Setters(m *Metadata, soloCreator string) {
	v := &task308Values
	m.SetCaption(v.caption)
	m.SetCopyright(v.copyright)
	if soloCreator != "" {
		m.SetCreator(soloCreator)
	} else {
		m.SetCreator("Placeholder")
		m.SetCreators(v.creators)
	}
	m.SetKeywords(v.keywords)
	m.SetDateTimeOriginal(v.date)
	m.SetCameraModel(v.model)
	m.SetMake(v.make)
	m.SetLensModel(v.lens)
	m.SetGPS(38.7223, -9.1393)
	m.SetExposureTime(1, 250)
	m.SetFNumber(2.8)
	m.SetISO(400)
	m.SetFocalLength(50)
	m.SetOrientation(6)
	m.SetImageSize(4000, 3000)
}

// TestTask308_EveryFieldConsistentAfterWrite writes every field once into
// four fixtures (JPEG without IPTC and XMP, JPEG with IPTC and XMP, PNG, TIFF
// with an IPTC digest), reads the file back, and checks each form against the
// values of the ExifTool oracle run: the MWG composite values (exiftool
// -MWG:all printed the same Description, Copyright, Creator list, Keywords,
// DateTimeOriginal "2026:03:14 15:09:26+05:30" and Orientation 6 for all
// eight files) and the per-group values.
func TestTask308_EveryFieldConsistentAfterWrite(t *testing.T) {
	t.Parallel()
	fixtures := []string{
		"Canon_40D.jpg", "IPTC-PhotometadataRef-Std2021.1.jpg", "png_raw_profile_exif.png", "tiff_iptc_in_psir_only.tif",
	}
	variants := []struct {
		name, solo string
		creators   []string
		artist     string // IFD0:Artist printed by the oracle
	}{
		{"SetCreators", "", task308Values.creators, `Alice Example; """Bob""; the second"; Carol`},
		{"SetCreator", `Solo "Creator"; Esq.`, []string{`Solo "Creator"; Esq.`}, `"Solo ""Creator""; Esq."`},
	}
	for _, fx := range fixtures {
		for _, vr := range variants {
			t.Run(fx+"/"+vr.name, func(t *testing.T) {
				t.Parallel()
				src, err := os.ReadFile(filepath.Join("testdata", "fixtures", fx))
				if err != nil {
					t.Fatalf("read fixture: %v", err)
				}
				m, err := Read(bytes.NewReader(src))
				if err != nil {
					t.Fatalf("Read: %v", err)
				}
				applyTask308Setters(m, vr.solo)
				var out bytes.Buffer
				if err := Write(bytes.NewReader(src), &out, m); err != nil {
					t.Fatalf("Write: %v", err)
				}
				m2, err := Read(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("Read after Write: %v", err)
				}
				checkTask308Forms(t, m2, vr.creators, vr.artist)
			})
		}
	}
}

func checkTask308Forms(t *testing.T, m *Metadata, creators []string, artist string) {
	t.Helper()
	v := &task308Values
	// MWG composites (oracle: exiftool -MWG:all).
	if got := m.Caption(); got != v.caption {
		t.Errorf("Caption() = %q, want %q", got, v.caption)
	}
	if got := m.Copyright(); got != v.copyright {
		t.Errorf("Copyright() = %q, want %q", got, v.copyright)
	}
	if got := m.Creator(); got != creators[0] {
		t.Errorf("Creator() = %q, want %q", got, creators[0])
	}
	if got := m.Keywords(); !slices.Equal(got, v.keywords) {
		t.Errorf("Keywords() = %q, want %q", got, v.keywords)
	}
	if got, ok := m.DateTimeOriginal(); !ok || got.Format("2006:01:02 15:04:05-07:00") != "2026:03:14 15:09:26+05:30" {
		t.Errorf("DateTimeOriginal() = %v, %v; want 2026:03:14 15:09:26+05:30", got, ok)
	}
	if got, ok := m.Orientation(); !ok || got != 6 {
		t.Errorf("Orientation() = %d, %v; want 6", got, ok)
	}

	// EXIF forms (oracle: IFD0 and ExifIFD groups).
	e := m.EXIF
	if e == nil {
		t.Fatal("EXIF missing")
	}
	for tag, want := range map[exif.TagID]string{
		exif.TagImageDescription: v.caption, exif.TagCopyright: v.copyright, exif.TagArtist: artist,
		exif.TagMake: v.make, exif.TagModel: v.model,
	} {
		if got := e.IFD0.Get(tag); got == nil || got.String() != want {
			t.Errorf("IFD0 tag 0x%04X = %v, want %q", tag, got, want)
		}
	}
	for tag, want := range map[exif.TagID]string{
		exif.TagDateTimeOriginal: "2026:03:14 15:09:26", exif.TagOffsetTimeOriginal: "+05:30", exif.TagLensModel: v.lens,
	} {
		if got := e.ExifIFD.Get(tag); got == nil || got.String() != want {
			t.Errorf("ExifIFD tag 0x%04X = %v, want %q", tag, got, want)
		}
	}
	if e.ExifIFD.Get(exif.TagSubSecTimeOriginal) != nil {
		t.Error("SubSecTimeOriginal survived SetDateTimeOriginal")
	}
	if got := reconcile.SplitArtist(e.Creator()); !slices.Equal(got, creators) {
		t.Errorf("EXIF Artist list = %q, want %q", got, creators)
	}

	// IPTC forms, for the formats that carry IPTC (oracle: IPTC group).
	if m.canCarryIPTC() {
		i := m.IPTC
		if i == nil {
			t.Fatal("IPTC missing")
		}
		if i.Caption() != v.caption || i.Copyright() != v.copyright {
			t.Errorf("IPTC caption/copyright = %q / %q", i.Caption(), i.Copyright())
		}
		if got := i.AllCreators(); !slices.Equal(got, creators) {
			t.Errorf("IPTC By-line = %q, want %q", got, creators)
		}
		if got := i.Keywords(); !slices.Equal(got, v.keywords) {
			t.Errorf("IPTC Keywords = %q, want %q", got, v.keywords)
		}
		if i.DateCreated() != "20260314" || i.TimeCreated() != "150926+0530" {
			t.Errorf("IPTC date/time = %q %q, want 20260314 150926+0530", i.DateCreated(), i.TimeCreated())
		}
	} else if m.IPTC != nil {
		t.Errorf("format %v carries no IPTC, but IPTC was read back", m.Format())
	}

	// XMP forms (oracle: XMP-dc, XMP-photoshop, XMP-exif, XMP-tiff, XMP-aux).
	x := m.XMP
	if x == nil {
		t.Fatal("XMP missing")
	}
	if x.Caption() != v.caption || x.Copyright() != v.copyright {
		t.Errorf("XMP caption/copyright = %q / %q", x.Caption(), x.Copyright())
	}
	if got := x.Creators(); !slices.Equal(got, creators) {
		t.Errorf("XMP dc:creator = %q, want %q", got, creators)
	}
	if got := x.Keywords(); !slices.Equal(got, v.keywords) {
		t.Errorf("XMP dc:subject = %q, want %q", got, v.keywords)
	}
	for _, p := range []struct{ ns, name, want string }{
		{xmp.NSphotoshop, "DateCreated", "2026-03-14T15:09:26+05:30"},
		{xmp.NSexif, "DateTimeOriginal", "2026-03-14T15:09:26+05:30"},
		{xmp.NStiff, "Model", v.model},
		{xmp.NSaux, "Lens", v.lens},
	} {
		if got := x.Get(p.ns, p.name); got != p.want {
			t.Errorf("XMP %s%s = %q, want %q", p.ns, p.name, got, p.want)
		}
	}
	if lat, lon, ok := x.GPS(); !ok || !near(lat, 38.7223) || !near(lon, -9.1393) {
		t.Errorf("XMP GPS = %v, %v, %v", lat, lon, ok)
	}
	if lat, lon, ok := e.GPS(); !ok || !near(lat, 38.7223) || !near(lon, -9.1393) {
		t.Errorf("EXIF GPS = %v, %v, %v", lat, lon, ok)
	}
}

func near(a, b float64) bool { return a-b < 1e-6 && b-a < 1e-6 }

// TestTask308_SettersFollowTable drives every setter from the mapping table:
// the EXIF tag of the field holds the value, every XMP property with
// reconcile.WriteAlways holds it, and every property with
// reconcile.WriteIfPresent is refreshed with the Toolkit text of the EXIF
// value when the packet already has it and is not created otherwise.
func TestTask308_SettersFollowTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		field reconcile.Field
		set   func(*Metadata)
		// xmpText is the XMP text of each XMP property of the field, in table
		// order: the Toolkit text of the EXIF value (ImportSingleTIFF_*).
		xmpText [2]string
	}{
		{reconcile.Make, func(m *Metadata) { m.SetMake("GoMake") }, [2]string{"GoMake"}},
		{reconcile.Model, func(m *Metadata) { m.SetCameraModel("GoModel X1") }, [2]string{"GoModel X1"}},
		{reconcile.LensModel, func(m *Metadata) { m.SetLensModel("GoLens") }, [2]string{"GoLens", "GoLens"}},
		{reconcile.ExposureTime, func(m *Metadata) { m.SetExposureTime(1, 250) }, [2]string{"1/250"}},
		{reconcile.FNumber, func(m *Metadata) { m.SetFNumber(2.8) }, [2]string{"280/100"}},
		{reconcile.ISO, func(m *Metadata) { m.SetISO(400) }, [2]string{"400", "400"}},
		{reconcile.FocalLength, func(m *Metadata) { m.SetFocalLength(50) }, [2]string{"5000/100"}},
		{reconcile.Orientation, func(m *Metadata) { m.SetOrientation(6) }, [2]string{"6"}},
		{reconcile.PixelXDimension, func(m *Metadata) { m.SetImageSize(4000, 3000) }, [2]string{"4000"}},
		{reconcile.PixelYDimension, func(m *Metadata) { m.SetImageSize(4000, 3000) }, [2]string{"3000"}},
	}
	for _, tc := range cases {
		mp := reconcile.Lookup(tc.field)
		for _, preset := range []bool{true, false} {
			name := mp.XMP[0].Name
			if preset {
				name += "/xmp-present"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				m := NewMetadata(format.FormatJPEG)
				m.XMP = &xmp.XMP{Properties: map[string]map[string]string{}}
				if preset {
					for _, p := range mp.XMP {
						if p.Name != "" {
							m.XMP.Set(p.NS, p.Name, "stale")
						}
					}
				}
				tc.set(m)
				if exifEntry(m.EXIF, mp.IFD, mp.Tag) == nil {
					t.Fatalf("EXIF tag 0x%04X not written", mp.Tag)
				}
				for i, p := range mp.XMP {
					if p.Name == "" {
						continue
					}
					got, has := m.XMP.Properties[p.NS][p.Name]
					switch {
					case p.Write == reconcile.WriteAlways || preset:
						if got != tc.xmpText[i] {
							t.Errorf("%s%s = %q, %v; want %q", p.NS, p.Name, got, has, tc.xmpText[i])
						}
					case has:
						t.Errorf("%s%s was created (%q); WriteIfPresent must not create it", p.NS, p.Name, got)
					}
				}
			})
		}
	}
}

// TestTask308_MirrorSkipsUnexpectedEXIFType proves that an XMP copy is not
// refreshed from an EXIF tag whose type differs from the Toolkit mapping.
func TestTask308_MirrorSkipsUnexpectedEXIFType(t *testing.T) {
	t.Parallel()
	b := buildTIFFMultiIFD([]tiffEntry{makeASCIIEntry(exif.TagOrientation, "6")}, nil, nil)
	e, err := exif.Parse(b)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	m := &Metadata{format: uint8(format.FormatJPEG), EXIF: e, XMP: &xmp.XMP{Properties: map[string]map[string]string{}}}
	m.XMP.Set(xmp.NStiff, "Orientation", "1")
	m.mirrorToXMP(reconcile.Orientation)
	if got := m.XMP.Get(xmp.NStiff, "Orientation"); got != "1" {
		t.Errorf("tiff:Orientation = %q, want it untouched (EXIF tag is ASCII, Toolkit type SHORT)", got)
	}
}

// TestTask308_SetCreatorsRemovesArtist pins MWG §4.2.3.1: clearing the
// creators removes the EXIF Artist tag too (behaviour change: the tag used to
// be left untouched).
func TestTask308_SetCreatorsRemovesArtist(t *testing.T) {
	t.Parallel()
	m := newTestMetadata(t)
	m.format = uint8(format.FormatJPEG)
	m.SetCreators([]string{"Alice", "Bob"})
	if m.EXIF.IFD0.Get(exif.TagArtist) == nil {
		t.Fatal("Artist not written")
	}
	before := len(m.EXIF.IFD0.Entries)
	m.SetCreators(nil)
	if m.EXIF.IFD0.Get(exif.TagArtist) != nil {
		t.Error("Artist survived SetCreators(nil)")
	}
	if len(m.EXIF.IFD0.Entries) != before-1 {
		t.Errorf("IFD0 has %d entries, want %d", len(m.EXIF.IFD0.Entries), before-1)
	}
	if !slices.IsSortedFunc(m.EXIF.IFD0.Entries, func(a, b exif.IFDEntry) int { return int(a.Tag) - int(b.Tag) }) {
		t.Error("IFD0 entries are no longer sorted")
	}
	if m.Creator() != "" {
		t.Errorf("Creator() = %q after clearing", m.Creator())
	}
	m.SetCreators(nil) // no Artist left: a no-op
}

// TestTask308_SetCreatorReplacesEveryByline pins the consistency fix of
// SetCreator: every IPTC By-line is replaced, as dc:creator always was
// (behaviour change: the extra By-lines used to survive).
func TestTask308_SetCreatorReplacesEveryByline(t *testing.T) {
	t.Parallel()
	m := newTestMetadata(t)
	m.format = uint8(format.FormatJPEG)
	m.SetCreators([]string{"A", "B", "C"})
	m.SetCreator("Solo; Esq.")
	if got := m.IPTC.AllCreators(); !slices.Equal(got, []string{"Solo; Esq."}) {
		t.Errorf("IPTC By-line = %q, want [Solo; Esq.]", got)
	}
	if got := m.XMP.Creators(); !slices.Equal(got, []string{"Solo; Esq."}) {
		t.Errorf("XMP dc:creator = %q", got)
	}
	// MWG §5.7: a value with "; " is quoted in Artist.
	if got := m.EXIF.Creator(); got != `"Solo; Esq."` {
		t.Errorf("EXIF Artist = %q, want %q", got, `"Solo; Esq."`)
	}
}

// task308ReadCase is one combination of field values for the read policy.
type task308ReadCase struct {
	name                string
	exifV, iimV, xmpV   string
	elevated            bool
	want                string
	behaviourChangeNote string // non-empty when the result differs from release v1.x
}

// TestTask308_ReadPolicy_Text drives the getters of the three text fields
// (reconcile.ReadEXIFThenXMPThenIIM, MWG §4.2.3.3) through the same cases.
// The cases marked "oracle" reproduce files of the oracle run and expect what
// exiftool -MWG:all returned for them. ExifTool (MWG.pm) agrees with every
// case except two, where the MWG text is followed: it returns a blank XMP
// value, and it prefers EXIF over an IIM value edited behind a stale digest,
// where MWG §4.2.3.3 prefers the IIM value.
func TestTask308_ReadPolicy_Text(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 1990) + "ABCDEFGHIJKLMNOP" // 2006 bytes, as in the oracle run
	cases := []task308ReadCase{
		{name: "oracle c1: no digest, XMP wins over IIM", iimV: "IPTC caption", xmpV: "XMP caption", want: "XMP caption"},
		{name: "oracle c2: digest mismatch, edited IIM wins", iimV: "IPTC caption", xmpV: "XMP caption", elevated: true, want: "IPTC caption"},
		{name: "oracle c3: digest mismatch, IIM is the truncated XMP", iimV: long, xmpV: long, elevated: true, want: long,
			behaviourChangeNote: "returned the truncated IIM value"},
		{name: "EXIF wins over XMP (MWG §4.2.3.1)", exifV: "EXIF", xmpV: "XMP", want: "EXIF",
			behaviourChangeNote: "returned the XMP value"},
		{name: "EXIF wins over IIM, no digest (MWG §4.2.3.3)", exifV: "EXIF", iimV: "IIM", want: "EXIF",
			behaviourChangeNote: "returned the IIM value"},
		{name: "EXIF wins over XMP and IIM", exifV: "EXIF", iimV: "IIM", xmpV: "XMP", want: "EXIF",
			behaviourChangeNote: "returned the XMP value"},
		{name: "EXIF alone", exifV: "EXIF", want: "EXIF"},
		{name: "digest mismatch, IIM without XMP wins over EXIF", exifV: "EXIF", iimV: "IIM", elevated: true, want: "IIM"},
		{name: "digest mismatch, edited IIM wins over EXIF and XMP (MWG §4.2.3.3)", exifV: "EXIF", iimV: "IIM", xmpV: "XMP",
			elevated: true, want: "IIM"},
		{name: "digest mismatch, unedited IIM: EXIF wins", exifV: "EXIF", iimV: "same", xmpV: "same", elevated: true, want: "EXIF",
			behaviourChangeNote: "returned the IIM value"},
		{name: "digest mismatch, unedited IIM, no EXIF: XMP", iimV: long, xmpV: long, elevated: true, want: long},
		{name: "blank EXIF falls back to XMP", exifV: "   ", xmpV: "XMP", iimV: "IIM", want: "XMP"},
		{name: "blank XMP is absent (MWG §4.2.3.3)", iimV: "IIM", xmpV: "   ", want: "IIM",
			behaviourChangeNote: "returned the blank XMP value"},
		{name: "blank IIM is absent", exifV: "EXIF", iimV: "  ", want: "EXIF"},
		{name: "blank EXIF is absent", exifV: "  ", want: "",
			behaviourChangeNote: "returned the blank EXIF value"},
		{name: "nothing", want: ""},
	}
	type getter struct {
		field reconcile.Field
		get   func(*Metadata) string
		set   func(e *exif.EXIF, i *iptc.IPTC, x *xmp.XMP, ev, iv, xv string)
	}
	getters := []getter{
		{reconcile.Description, (*Metadata).Caption, func(e *exif.EXIF, i *iptc.IPTC, x *xmp.XMP, ev, iv, xv string) {
			setIf(ev, e.SetCaption)
			setIf(iv, i.SetCaption)
			setIf(xv, x.SetCaption)
		}},
		{reconcile.Copyright, (*Metadata).Copyright, func(e *exif.EXIF, i *iptc.IPTC, x *xmp.XMP, ev, iv, xv string) {
			setIf(ev, e.SetCopyright)
			setIf(iv, i.SetCopyright)
			setIf(xv, x.SetCopyright)
		}},
		{reconcile.Creator, (*Metadata).Creator, func(e *exif.EXIF, i *iptc.IPTC, x *xmp.XMP, ev, iv, xv string) {
			setIf(ev, e.SetCreator)
			setIf(iv, i.SetCreator)
			setIf(xv, x.SetCreator)
		}},
	}
	for _, g := range getters {
		if reconcile.Lookup(g.field).Read != reconcile.ReadEXIFThenXMPThenIIM {
			t.Fatalf("field %d is not read EXIF, XMP, IIM", g.field)
		}
		maxLen := reconcile.IIMMaxLen(g.field)
		for _, tc := range cases {
			t.Run(reconcile.Lookup(g.field).MWG+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				m := newTestMetadata(t)
				m.iptcTrustElev = tc.elevated
				g.set(m.EXIF, m.IPTC, m.XMP, tc.exifV, tc.iimV, tc.xmpV)
				want := tc.want
				if want == tc.iimV && tc.iimV != tc.xmpV {
					want = reconcile.PredictIIM(want, maxLen) // the IIM form holds the truncated value
				}
				if got := g.get(m); got != want {
					t.Errorf("got %.40q (len %d), want %.40q (len %d); release 1.x %s",
						got, len(got), want, len(want), cmp.Or(tc.behaviourChangeNote, "returned the same"))
				}
			})
		}
	}
}

func setIf(v string, set func(string)) {
	if v != "" {
		set(v)
	}
}

// TestTask308_CreatorReadsMWGArtistList pins MWG §5.7 on read: the EXIF
// Artist tag is a list, and Creator returns its first item (oracle c4:
// exiftool -MWG:Creator returned the seven items of MWG example 2 for this
// Artist value). Behaviour change: the whole Artist string used to be
// returned.
func TestTask308_CreatorReadsMWGArtistList(t *testing.T) {
	t.Parallel()
	for artist, want := range map[string]string{
		`first; with;semicolon; "with; semicolon-space"; last`: "first",
		`"with; semicolon-space"; last`:                        "with; semicolon-space",
		`"Solo ""Creator""; Esq."`:                             `Solo "Creator"; Esq.`,
		"single creator":                                       "single creator",
	} {
		m := newTestMetadata(t)
		m.EXIF.SetCreator(artist)
		if got := m.Creator(); got != want {
			t.Errorf("Artist %q: Creator() = %q, want %q", artist, got, want)
		}
	}
}

// TestTask308_ReadPolicy_Keywords applies the digest rule to lists (oracle
// c6: exiftool -MWG:Keywords returned the 70-byte XMP keyword and "short"
// when the IIM held the 64-byte truncation and a stale digest).
func TestTask308_ReadPolicy_Keywords(t *testing.T) {
	t.Parallel()
	k70 := strings.Repeat("k", 70)
	cases := []struct {
		name      string
		iim, xmpK []string
		elevated  bool
		want      []string
	}{
		{"oracle c6: IIM is the truncated XMP", []string{k70, "short"}, []string{k70, "short"}, true, []string{k70, "short"}},
		{"digest mismatch, edited IIM", []string{"edited"}, []string{"a", "b"}, true, []string{"edited"}},
		{"digest mismatch, no XMP", []string{"a"}, nil, true, []string{"a"}},
		{"digest mismatch, no IIM", nil, []string{"x"}, true, []string{"x"}},
		{"no digest, XMP wins", []string{"iim"}, []string{"xmp"}, false, []string{"xmp"}},
		{"no digest, IIM fallback", []string{"iim"}, nil, false, []string{"iim"}},
	}
	for _, tc := range cases {
		m := newTestMetadata(t)
		m.iptcTrustElev = tc.elevated
		if tc.iim != nil {
			m.IPTC.SetKeywords(tc.iim)
		}
		if tc.xmpK != nil {
			m.XMP.SetKeywords(tc.xmpK)
		}
		if got := m.Keywords(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: Keywords() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestTask308_DateTimeOriginalSources pins the date sources added by the
// table (reconcile.ReadDate). The oracle cases expect what exiftool
// -MWG:DateTimeOriginal returned: c7 "2021:07:04 10:20:30+02:00" (IIM only),
// c8 the same (photoshop:DateCreated over IIM), c9 "2020:01:01
// 00:00:00+00:00" (stale digest, edited IIM). Behaviour change: without an
// EXIF or XMP exif:DateTimeOriginal value, DateTimeOriginal used to report
// ok=false.
func TestTask308_DateTimeOriginalSources(t *testing.T) {
	t.Parallel()
	const layout = "2006:01:02 15:04:05-07:00"
	cases := []struct {
		name               string
		iimDate, iimTime   string
		photoshop, exifXMP string
		elevated           bool
		want               string
	}{
		{name: "oracle c7: IIM only", iimDate: "20210704", iimTime: "102030+0200", want: "2021:07:04 10:20:30+02:00"},
		{name: "oracle c8: photoshop:DateCreated over IIM", iimDate: "20200101", iimTime: "000000+0000",
			photoshop: "2021-07-04T10:20:30+02:00", want: "2021:07:04 10:20:30+02:00"},
		{name: "oracle c9: stale digest, edited IIM", iimDate: "20200101", iimTime: "000000+0000",
			photoshop: "2021-07-04T10:20:30+02:00", elevated: true, want: "2020:01:01 00:00:00+00:00"},
		{name: "stale digest, IIM is the XMP export", iimDate: "20210704", iimTime: "102030+0200",
			photoshop: "2021-07-04T10:20:30.5+02:00", elevated: true, want: "2021:07:04 10:20:30+02:00"},
		{name: "unparsable XMP falls back to IIM", iimDate: "20210704", iimTime: "102030+0200",
			photoshop: "July 4th", want: "2021:07:04 10:20:30+02:00"},
		{name: "exif:DateTimeOriginal with fraction", exifXMP: "2021-07-04T10:20:30.5Z", want: "2021:07:04 10:20:30+00:00"},
		{name: "exif:DateTimeOriginal over photoshop:DateCreated", exifXMP: "2022-01-01T00:00:00Z",
			photoshop: "2021-07-04T10:20:30+02:00", want: "2022:01:01 00:00:00+00:00"},
	}
	for _, tc := range cases {
		m := &Metadata{IPTC: new(iptc.IPTC), XMP: &xmp.XMP{Properties: map[string]map[string]string{}}, iptcTrustElev: tc.elevated}
		if tc.iimDate != "" {
			b := append(buildIPTCIIMBytesField(55, tc.iimDate), buildIPTCIIMBytesField(60, tc.iimTime)...)
			i, err := iptc.Parse(b)
			if err != nil {
				t.Fatalf("iptc.Parse: %v", err)
			}
			m.IPTC = i
		}
		if tc.photoshop != "" {
			m.XMP.Set(xmp.NSphotoshop, "DateCreated", tc.photoshop)
		}
		if tc.exifXMP != "" {
			m.XMP.Set(xmp.NSexif, "DateTimeOriginal", tc.exifXMP)
		}
		got, ok := m.DateTimeOriginal()
		if !ok || got.Format(layout) != tc.want {
			t.Errorf("%s: DateTimeOriginal() = %v, %v; want %s", tc.name, got, ok, tc.want)
		}
	}
	// An empty Metadata still reports no date.
	if _, ok := (&Metadata{}).DateTimeOriginal(); ok {
		t.Error("empty Metadata reported a date")
	}
}

// TestTask308_DateTimeOriginalEditedIIMWinsOverEXIF pins the digest rule of
// MWG §4.2.3.3 for dates: with a stale digest, an IIM date that differs from
// the one predicted from photoshop:DateCreated wins over the EXIF date; an
// IIM date that matches the prediction does not. Behaviour change: the EXIF
// date used to win in both cases.
func TestTask308_DateTimeOriginalEditedIIMWinsOverEXIF(t *testing.T) {
	t.Parallel()
	const layout = "2006:01:02 15:04:05-07:00"
	for _, tc := range []struct {
		name, photoshop string
		want            string
	}{
		{"edited IIM", "2021-07-04T10:20:30+02:00", "2020:01:01 00:00:00+00:00"},
		{"edited IIM without XMP", "", "2020:01:01 00:00:00+00:00"},
		{"unedited IIM", "2020-01-01T00:00:00Z", "2019:05:06 07:08:09+00:00"},
	} {
		b := buildTIFFMultiIFD(nil, []tiffEntry{
			makeASCIIEntry(exif.TagDateTimeOriginal, "2019:05:06 07:08:09"),
			makeASCIIEntry(exif.TagOffsetTimeOriginal, "+00:00"),
		}, nil)
		e, err := exif.Parse(b)
		if err != nil {
			t.Fatalf("exif.Parse: %v", err)
		}
		i, err := iptc.Parse(append(buildIPTCIIMBytesField(55, "20200101"), buildIPTCIIMBytesField(60, "000000+0000")...))
		if err != nil {
			t.Fatalf("iptc.Parse: %v", err)
		}
		x := &xmp.XMP{Properties: map[string]map[string]string{}}
		if tc.photoshop != "" {
			x.Set(xmp.NSphotoshop, "DateCreated", tc.photoshop)
		}
		m := &Metadata{EXIF: e, IPTC: i, XMP: x, iptcTrustElev: true}
		if got, ok := m.DateTimeOriginal(); !ok || got.Format(layout) != tc.want {
			t.Errorf("%s: DateTimeOriginal() = %v, %v; want %s", tc.name, got, ok, tc.want)
		}
		m.iptcTrustElev = false // digest matches: EXIF wins
		if got, ok := m.DateTimeOriginal(); !ok || got.Format(layout) != "2019:05:06 07:08:09+00:00" {
			t.Errorf("%s, digest match: DateTimeOriginal() = %v, %v", tc.name, got, ok)
		}
	}
}

// TestTask308_DateTimeOriginalSubSec pins MWG §5.3 on read: the EXIF
// SubSecTimeOriginal fraction is part of the EXIF date. Behaviour change: the
// fraction used to be ignored.
func TestTask308_DateTimeOriginalSubSec(t *testing.T) {
	t.Parallel()
	for subsec, wantNS := range map[string]int{"25": 250_000_000, "5  ": 500_000_000, "123456789123": 123_456_789, "x": 0} {
		b := buildTIFFMultiIFD(nil, []tiffEntry{
			makeASCIIEntry(exif.TagDateTimeOriginal, "2021:07:04 10:20:30"),
			makeASCIIEntry(exif.TagOffsetTimeOriginal, "+02:00"),
			makeASCIIEntry(exif.TagSubSecTimeOriginal, subsec),
		}, nil)
		e, err := exif.Parse(b)
		if err != nil {
			t.Fatalf("exif.Parse: %v", err)
		}
		got, ok := (&Metadata{EXIF: e}).DateTimeOriginal()
		want := time.Date(2021, 7, 4, 10, 20, 30, wantNS, time.FixedZone("", 7200))
		if !ok || !got.Equal(want) {
			t.Errorf("SubSecTimeOriginal %q: DateTimeOriginal() = %v, %v; want %v", subsec, got, ok, want)
		}
	}
}

// TestTask308_LensModelExifEX pins the Toolkit mapping of tag 0xA434 on read:
// exifEX:LensModel is used when neither EXIF nor aux:Lens has a value.
func TestTask308_LensModelExifEX(t *testing.T) {
	t.Parallel()
	x := &xmp.XMP{Properties: map[string]map[string]string{}}
	x.Set("http://cipa.jp/exif/1.0/", "LensModel", "EF 50mm")
	if got := (&Metadata{XMP: x}).LensModel(); got != "EF 50mm" {
		t.Errorf("LensModel() = %q, want %q", got, "EF 50mm")
	}
	x.Set(xmp.NSaux, "Lens", "aux lens")
	if got := (&Metadata{XMP: x}).LensModel(); got != "aux lens" {
		t.Errorf("LensModel() = %q, want aux:Lens first", got)
	}
}

// TestTask308_TextAccessorsMatchTable proves that each text accessor reads
// the EXIF tag, IIM DataSet and XMP property named by the mapping table.
func TestTask308_TextAccessorsMatchTable(t *testing.T) {
	t.Parallel()
	for _, f := range []reconcile.Field{reconcile.Description, reconcile.Copyright, reconcile.Creator} {
		mp := reconcile.Lookup(f)
		m := newTestMetadata(t)
		m.format = uint8(format.FormatJPEG)
		switch f {
		case reconcile.Description:
			m.SetCaption("v")
		case reconcile.Copyright:
			m.SetCopyright("v")
		case reconcile.Creator:
			m.SetCreator("v")
		}
		if got := exifEntry(m.EXIF, mp.IFD, mp.Tag); got == nil || got.String() != "v" || exifTextForm(m.EXIF, f) != "v" {
			t.Errorf("field %s: EXIF tag 0x%04X = %v", mp.MWG, mp.Tag, got)
		}
		n := 0
		for _, d := range m.IPTC.Records[2] {
			if d.DataSet == mp.DataSet && string(d.Value) == "v" {
				n++
			}
		}
		if n != 1 || iimTextForm(m.IPTC, f) != "v" {
			t.Errorf("field %s: IIM DataSet %d occurs %d times with the value", mp.MWG, mp.DataSet, n)
		}
		if got := m.XMP.Get(mp.XMP[0].NS, mp.XMP[0].Name); got != "v" {
			t.Errorf("field %s: XMP %s = %q", mp.MWG, mp.XMP[0].Name, got)
		}
		// The getter reads the XMP form alone.
		only := &Metadata{XMP: m.XMP}
		get := map[reconcile.Field]func() string{
			reconcile.Description: only.Caption, reconcile.Copyright: only.Copyright, reconcile.Creator: only.Creator,
		}[f]
		if got := get(); got != "v" {
			t.Errorf("field %s: getter on XMP alone = %q", mp.MWG, got)
		}
	}
}

// TestTask308_GettersZeroAllocs proves that the table-driven text getters do
// not allocate on the XMP path. No t.Parallel: testing.AllocsPerRun panics
// in parallel tests.
//
//nolint:paralleltest // testing.AllocsPerRun panics if the test (or a parent) is parallel
func TestTask308_GettersZeroAllocs(t *testing.T) {
	m := newTestMetadata(t)
	m.XMP.SetCaption("caption")
	m.XMP.SetCopyright("copyright")
	m.XMP.SetCreator("creator")
	m.iptcTrustElev = true
	m.IPTC.SetCaption("caption")
	if a := testing.AllocsPerRun(100, func() {
		_ = m.Caption()
		_ = m.Copyright()
		_ = m.Creator()
	}); a != 0 {
		t.Errorf("text getters allocate %v times per run, want 0", a)
	}
}
