package exif

import (
	"encoding/binary"
	"sort"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// TestTagNameIn_StandardIFDs (task #397): the name of a tag depends on the IFD.
func TestTagNameIn_StandardIFDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ctx  TagContext
		tag  TagID
		want string
	}{
		{ContextInterop, 1, "InteropIndex"},
		{ContextInterop, 2, "InteropVersion"},
		{ContextInterop, 0x1000, "RelatedImageFileFormat"},
		{ContextInterop, 0x1001, "RelatedImageWidth"},
		{ContextInterop, 0x1002, "RelatedImageLength"},
		{ContextInterop, 3, ""},
		{ContextInterop, TagMake, ""},
		{ContextGPS, 1, "GPSLatitudeRef"},
		{ContextGPS, 2, "GPSLatitude"},
		{ContextGPS, 0, "GPSVersionID"},
		{ContextGPS, 0x1F, "GPSHPositioningError"},
		{ContextGPS, 0x20, ""},
		{ContextGPS, TagMake, ""},
		{ContextIFD0, TagMake, "Make"},
		{ContextIFD0, TagOrientation, "Orientation"},
		{ContextIFD0, 1, ""},
		{ContextIFD0, 2, ""},
		{ContextIFD0, 0x1001, ""},
		{ContextIFD1, TagJPEGInterchangeFormat, "JPEGInterchangeFormat"},
		{ContextIFD1, 1, ""},
		{ContextExif, TagExposureTime, "ExposureTime"},
		{ContextExif, TagLensModel, "LensModel"},
		{ContextExif, 1, ""},
		{ContextExif, 0xFFFF, ""},
		{0, TagMake, ""},
		{contextEnd, TagMake, ""},
		{255, TagMake, ""},
	} {
		if got := TagNameIn(tc.ctx, tc.tag); got != tc.want {
			t.Errorf("TagNameIn(%d, %#x) = %q, want %q", tc.ctx, uint16(tc.tag), got, tc.want)
		}
	}
}

// TestTagNameIn_TagNameUnchanged (task #397): TagName keeps its behaviour,
// including the GPS name of the IDs that the Interoperability IFD shares.
func TestTagNameIn_TagNameUnchanged(t *testing.T) {
	t.Parallel()
	if got := TagName(1); got != "GPSLatitudeRef" {
		t.Errorf("TagName(1) = %q, want GPSLatitudeRef", got)
	}
	if got := TagName(TagMake); got != "Make" {
		t.Errorf("TagName(Make) = %q", got)
	}
}

// TestTagNameIn_AgreesWithTagName: for every tag of the combined registry, the
// context that owns the tag returns the same name as TagName, so TagNameIn
// never contradicts TagName except for the ID collisions it exists to resolve.
func TestTagNameIn_AgreesWithTagName(t *testing.T) {
	t.Parallel()
	for id, info := range tagRegistry {
		switch {
		case id <= gpsMaxTag:
			if got := TagNameIn(ContextGPS, id); got != info.Name {
				t.Errorf("GPS %#x = %q, TagName %q", uint16(id), got, info.Name)
			}
		case id >= interopRelatedFirst && id <= interopRelatedLast:
			if got := TagNameIn(ContextInterop, id); got != info.Name {
				t.Errorf("Interop %#x = %q, TagName %q", uint16(id), got, info.Name)
			}
		default:
			for _, c := range []TagContext{ContextIFD0, ContextIFD1, ContextExif} {
				if got := TagNameIn(c, id); got != info.Name {
					t.Errorf("ctx %d %#x = %q, TagName %q", c, uint16(id), got, info.Name)
				}
			}
		}
	}
}

// makerNoteSamples are tag IDs and names of metadata-extractor
// (makernotes/*MakernoteDirectory.java at the commit of THIRD_PARTY_NOTICES.md),
// each checked against the Java source by hand: the TAG_* constant and the
// _tagNameMap entry of the directory class. Three per class where it has
// them: the first, one in the middle and the last tag.
var makerNoteSamples = []struct { //nolint:gochecknoglobals // test table
	ctx   TagContext
	class string
	tag   TagID
	name  string
}{
	{ContextMakerNoteCanon, "CanonMakernoteDirectory", 0x0006, "ImageType"},
	{ContextMakerNoteCanon, "CanonMakernoteDirectory", 0x0095, "LensModel"},
	{ContextMakerNoteCanon, "CanonMakernoteDirectory", 0x0097, "DustRemovalData"},
	{ContextMakerNoteCasio, "CasioType1MakernoteDirectory", 0x0001, "RecordingMode"},
	{ContextMakerNoteCasio, "CasioType1MakernoteDirectory", 0x0014, "CCDSensitivity"},
	{ContextMakerNoteCasioType2, "CasioType2MakernoteDirectory", 0x0002, "ThumbnailDimensions"},
	{ContextMakerNoteCasioType2, "CasioType2MakernoteDirectory", 0x3017, "Filter"},
	{ContextMakerNoteFujifilm, "FujifilmMakernoteDirectory", 0x0000, "MakernoteVersion"},
	{ContextMakerNoteFujifilm, "FujifilmMakernoteDirectory", 0xB211, "Parallax"},
	{ContextMakerNoteKyocera, "KyoceraMakernoteDirectory", 0x0001, "ProprietaryThumbnailFormatData"},
	{ContextMakerNoteKyocera, "KyoceraMakernoteDirectory", 0x0E00, "PrintImageMatchingPIMInfo"},
	{ContextMakerNoteLeica, "LeicaMakernoteDirectory", 0x0300, "Quality"},
	{ContextMakerNoteLeica, "LeicaMakernoteDirectory", 0x0340, "ImageIDNumber"},
	{ContextMakerNoteNikonType1, "NikonType1MakernoteDirectory", 0x0007, "WhiteBalance"},
	{ContextMakerNoteNikon, "NikonType2MakernoteDirectory", 0x0084, "Lens"},
	{ContextMakerNoteNikon, "NikonType2MakernoteDirectory", 0x008B, "LensStops"},
	{ContextMakerNoteOlympus, "OlympusMakernoteDirectory", 0x0000, "MakernoteVersion"},
	{ContextMakerNoteOlympus, "OlympusMakernoteDirectory", 0x4000, "MainInfo"},
	{ContextMakerNotePanasonic, "PanasonicMakernoteDirectory", 0x0001, "QualityMode"},
	{ContextMakerNotePanasonic, "PanasonicMakernoteDirectory", 0x8012, "Transform1"},
	{ContextMakerNotePentax, "PentaxMakernoteDirectory", 0x0001, "CaptureMode"},
	{ContextMakerNotePentax, "PentaxMakernoteDirectory", 0x1001, "DaylightSavings"},
	{ContextMakerNoteRicoh, "RicohMakernoteDirectory", 0x0002, "Version"},
	{ContextMakerNoteSanyo, "SanyoMakernoteDirectory", 0x0F00, "DataDump"},
	{ContextMakerNoteSigma, "SigmaMakernoteDirectory", 0x0019, "AutoBracket"},
	{ContextMakerNoteSony, "SonyType1MakernoteDirectory", 0x0010, "CameraInfo"},
	{ContextMakerNoteSony, "SonyType1MakernoteDirectory", 0xFFFF, "NoPrint"},
	{ContextMakerNoteSonyEricsson, "SonyType6MakernoteDirectory", 0x2000, "MakernoteThumbVersion"},
}

// TestTagNameIn_MakerNoteSamples (task #395): a sample of IDs per
// manufacturer, against the values of metadata-extractor.
func TestTagNameIn_MakerNoteSamples(t *testing.T) {
	t.Parallel()
	seen := map[TagContext]bool{}
	for _, s := range makerNoteSamples {
		seen[s.ctx] = true
		if got := TagNameIn(s.ctx, s.tag); got != s.name {
			t.Errorf("%s: TagNameIn(%d, %#x) = %q, want %q", s.class, s.ctx, uint16(s.tag), got, s.name)
		}
	}
	for c := ContextMakerNoteCanon; c < contextEnd; c++ {
		if !seen[c] {
			t.Errorf("context %d has no sample", c)
		}
	}
}

// TestMakerNoteTables_Wellformed (task #395): every MakerNote context has a
// table, sorted by ID without duplicates (the binary search needs it), with
// no empty name; an ID that is not in the table has no name.
func TestMakerNoteTables_Wellformed(t *testing.T) {
	t.Parallel()
	if got, want := len(makerNoteTables), int(contextEnd-ContextMakerNoteCanon); got != want {
		t.Fatalf("%d tables for %d MakerNote contexts", got, want)
	}
	total := 0
	for i, tbl := range makerNoteTables {
		ctx := ContextMakerNoteCanon + TagContext(i)
		if len(tbl) == 0 {
			t.Errorf("context %d has an empty table", ctx)
		}
		if !sort.SliceIsSorted(tbl, func(a, b int) bool { return tbl[a].id < tbl[b].id }) {
			t.Errorf("context %d: table is not sorted", ctx)
		}
		for j, e := range tbl {
			if e.name == "" {
				t.Errorf("context %d: tag %#x has no name", ctx, e.id)
			}
			if j > 0 && tbl[j-1].id == e.id {
				t.Errorf("context %d: duplicate tag %#x", ctx, e.id)
			}
			if got := TagNameIn(ctx, TagID(e.id)); got != e.name {
				t.Errorf("context %d: TagNameIn(%#x) = %q, want %q", ctx, e.id, got, e.name)
			}
		}
		total += len(tbl)
	}
	t.Logf("%d MakerNote contexts, %d tags", len(makerNoteTables), total)
}

// layoutContexts is the expected context of every MakerNote layout, and
// layoutNoTable the reason why a layout has none (task #395). The test below
// enumerates mnLayouts: a layout in neither list fails, so a new manufacturer
// cannot be added to the dispatch without a table or a documented reason.
var layoutContexts = map[mnLayout]TagContext{ //nolint:gochecknoglobals // test table
	mnOlympusOld: ContextMakerNoteOlympus, mnOlympus: ContextMakerNoteOlympus,
	mnOMSystem: ContextMakerNoteOlympus, mnOlympusMake: ContextMakerNoteOlympus,
	mnMinolta: ContextMakerNoteOlympus,
	mnNikon1:  ContextMakerNoteNikonType1, mnNikon2: ContextMakerNoteNikon, mnNikon3: ContextMakerNoteNikon,
	mnSonyCam: ContextMakerNoteSony, mnSony: ContextMakerNoteSony,
	mnSonyEricsson: ContextMakerNoteSonyEricsson,
	mnSigma:        ContextMakerNoteSigma,
	mnCasio2:       ContextMakerNoteCasioType2, mnPentaxAOC: ContextMakerNoteCasioType2,
	mnCasio:     ContextMakerNoteCasio,
	mnFujifilm:  ContextMakerNoteFujifilm,
	mnKyocera:   ContextMakerNoteKyocera,
	mnPanasonic: ContextMakerNotePanasonic,
	mnPentax:    ContextMakerNotePentax, mnPentaxPlain: ContextMakerNotePentax,
	mnSanyo: ContextMakerNoteSanyo,
	mnRicoh: ContextMakerNoteRicoh,
	mnCanon: ContextMakerNoteCanon,
	// mnLeicaTIFF and mnLeicaMake depend on the Make: see TestMakerNoteContext_Leica.
}

var layoutNoTable = map[mnLayout]string{ //nolint:gochecknoglobals // test table
	mnKodak:     "binary layout (\"KDK\" blocks): its keys are byte offsets, not IFD tag IDs",
	mnReconyx:   "binary layout (fixed records): its keys are byte offsets, not IFD tag IDs",
	mnApple:     "metadata-extractor's AppleMakernoteDirectory takes its tag reference from the ExifTool documentation; ExifTool is oracle-only",
	mnDJI:       "metadata-extractor's DjiMakernoteDirectory takes its tag reference from the ExifTool documentation; ExifTool is oracle-only",
	mnSamsung:   "metadata-extractor's SamsungType2MakernoteDirectory takes its tag reference from the ExifTool documentation; ExifTool is oracle-only",
	mnLeica:     "Leica Type 5: metadata-extractor's LeicaType5MakernoteDirectory takes its tag reference from the ExifTool documentation; ExifTool is oracle-only",
	mnJVC:       "metadata-extractor has no JVC MakerNote directory",
	mnRicohMake: "no metadata-extractor directory: processMakernote reads a Ricoh note only behind the \"Ricoh\" signature (mnRicoh)",
}

// TestMakerNoteContext_EveryLayoutAccountedFor (task #395): every manufacturer
// layout of the dispatch has a tag table, or a documented reason why not.
func TestMakerNoteContext_EveryLayoutAccountedFor(t *testing.T) {
	t.Parallel()
	covered := map[TagContext]bool{}
	for l := mnLayout(1); int(l) < len(mnLayouts); l++ {
		name := mnLayouts[l].name
		want, hasTable := layoutContexts[l]
		reason, noTable := layoutNoTable[l]
		leicaByMake := l == mnLeicaTIFF || l == mnLeicaMake
		switch {
		case leicaByMake:
			continue
		case hasTable == noTable:
			t.Errorf("layout %d (%s): must be in exactly one of layoutContexts and layoutNoTable", l, name)
			continue
		}
		got, ok := makerNoteContextOf(l, []byte("X"))
		if noTable {
			if ok || reason == "" {
				t.Errorf("layout %d (%s): ok=%v, reason %q", l, name, ok, reason)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("layout %d (%s): context %d ok=%v, want %d", l, name, got, ok, want)
		}
		covered[got] = true
	}
	// Every context is reached by at least one layout (Leica is reached by Make).
	for c := ContextMakerNoteCanon; c < contextEnd; c++ {
		if !covered[c] && c != ContextMakerNoteLeica {
			t.Errorf("context %d is reached by no layout", c)
		}
	}
}

// TestMakerNoteContext_Leica (task #395): processMakernote selects the
// directory of a Leica note without a Type 5 version by the Make.
func TestMakerNoteContext_Leica(t *testing.T) {
	t.Parallel()
	for _, l := range []mnLayout{mnLeicaTIFF, mnLeicaMake} {
		for _, tc := range []struct {
			make string
			want TagContext
			ok   bool
		}{
			{"Leica Camera AG", ContextMakerNoteLeica, true},
			{"LEICA", ContextMakerNotePanasonic, true},
			{"LEICA CAMERA AG", 0, false},
			{"", 0, false},
		} {
			got, ok := makerNoteContextOf(l, []byte(tc.make))
			if got != tc.want || ok != tc.ok {
				t.Errorf("layout %d, Make %q: (%d, %v), want (%d, %v)", l, tc.make, got, ok, tc.want, tc.ok)
			}
		}
	}
}

// parsedMakerNote parses a synthetic stream of Make mk and MakerNote prefix
// and entries.
func parsedMakerNote(t *testing.T, mk, prefix string, rel bool, entries []testutil.MNEntry) *EXIF {
	t.Helper()
	tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: mk, Order: binary.LittleEndian, Prefix: []byte(prefix), TIFFRelative: rel, Entries: entries,
	})
	e, err := Parse(tiff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return e
}

// TestMakerNoteContext_Parsed (task #397): EXIF.MakerNoteContext selects the
// context of a parsed file by signature or Make.
func TestMakerNoteContext_Parsed(t *testing.T) {
	t.Parallel()
	one := []testutil.MNEntry{{Tag: 0x0001, Type: 3, Count: 1, Value: []byte{7, 0}}}
	for _, tc := range []struct {
		name, mk, prefix string
		rel              bool
		want             TagContext
		ok               bool
	}{
		{"canon", "Canon", "", true, ContextMakerNoteCanon, true},
		{"nikon headerless", "NIKON", "", true, ContextMakerNoteNikon, true},
		{"olympus signature", "OLYMPUS CORPORATION", "OLYMP\x00\x01\x00", true, ContextMakerNoteOlympus, true},
		{"panasonic", "Panasonic", "Panasonic\x00\x00\x00", true, ContextMakerNotePanasonic, true},
		{"fujifilm make", "FUJIFILM", "", false, ContextMakerNoteFujifilm, true},
		{"apple has no table", "Apple", "Apple iOS\x00\x00\x01MM", false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := parsedMakerNote(t, tc.mk, tc.prefix, tc.rel, one)
			got, ok := e.MakerNoteContext()
			if got != tc.want || ok != tc.ok {
				t.Errorf("MakerNoteContext = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
	var nilEXIF *EXIF
	if _, ok := nilEXIF.MakerNoteContext(); ok {
		t.Error("nil EXIF has a MakerNote context")
	}
	if _, ok := (&EXIF{}).MakerNoteContext(); ok {
		t.Error("EXIF without MakerNote has a MakerNote context")
	}
}

// TestTagNameIn_ZeroAllocs (task #397): the lookup does not allocate.
func TestTagNameIn_ZeroAllocs(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics when tests run in parallel
	var sink string
	n := testing.AllocsPerRun(200, func() {
		sink = TagNameIn(ContextMakerNoteNikon, 0x0084)
		sink = TagNameIn(ContextGPS, 1)
		sink = TagNameIn(ContextInterop, 1)
		sink = TagNameIn(ContextExif, TagLensModel)
	})
	if n != 0 {
		t.Errorf("TagNameIn allocates %v times per run", n)
	}
	_ = sink
}

// BenchmarkTagNameIn measures the context-aware lookup.
func BenchmarkTagNameIn(b *testing.B) {
	for _, bc := range []struct {
		name string
		ctx  TagContext
		tag  TagID
	}{
		{"Exif", ContextExif, TagLensModel},
		{"GPS", ContextGPS, 1},
		{"Interop", ContextInterop, 1},
		{"MakerNoteNikon", ContextMakerNoteNikon, 0x0084},
		{"MakerNoteCanonLast", ContextMakerNoteCanon, 0x4024},
		{"MakerNoteMiss", ContextMakerNoteCanon, 0x0005},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			var sink string
			for range b.N {
				sink = TagNameIn(bc.ctx, bc.tag)
			}
			_ = sink
		})
	}
}

// BenchmarkMakerNoteContext measures the detection of the context of a parsed
// MakerNote.
func BenchmarkMakerNoteContext(b *testing.B) {
	tiff, _ := testutil.MakerNoteTIFF(testutil.MNSpec{
		Make: "Canon", Order: binary.LittleEndian, TIFFRelative: true,
		Entries: []testutil.MNEntry{{Tag: 0x0001, Type: 3, Count: 1, Value: []byte{7, 0}}},
	})
	e, err := Parse(tiff)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok := e.MakerNoteContext(); !ok {
			b.Fatal("no context")
		}
	}
}
