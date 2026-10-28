package exif

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// benchCanon returns the Exif stream of a real Canon file, or a synthetic one
// when the corpus is absent.
func benchCanon(b *testing.B) []byte {
	b.Helper()
	if data, err := os.ReadFile(filepath.Join("..", "testdata", "corpus", "jpeg", "metadata-extractor", "Canon EOS 450D.jpg")); err == nil {
		if i := strings.Index(string(data[:min(len(data), 1<<16)]), "Exif\x00\x00"); i >= 0 {
			return data[i+6:]
		}
	}
	tiff, _ := testutil.MakerNoteTIFF(mnCases()[0].spec)
	return tiff
}

// BenchmarkMakerNoteEdit measures an edit of a MakerNote (parse excluded) and
// the encode that follows: the cost of the safe-editing path. The edit works on
// a private copy of the note (one allocation of its size) and checks the result.
func BenchmarkMakerNoteEdit(b *testing.B) {
	tiff := benchCanon(b)
	parse := func(b *testing.B) *EXIF {
		b.Helper()
		e, err := Parse(tiff)
		if err != nil || e.MakerNoteIFD == nil {
			b.Fatalf("Parse: %v", err)
		}
		return e
	}
	tag := TagID(0x0096)
	if parse(b).MakerNoteIFD.Get(tag) == nil {
		tag = 0x0002 // the synthetic note
	}
	grow := strings.Repeat("G", 60)
	b.Run("Delete", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			e := parse(b)
			b.StartTimer()
			if err := e.DeleteMakerNoteTag(tag); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("SameSize", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			e := parse(b)
			cur := string(e.MakerNoteIFD.Get(tag).Value)
			cur = strings.Repeat("S", len(cur)-1)
			b.StartTimer()
			if err := e.SetMakerNoteTag(tag, cur, FixedSize()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Grow", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			e := parse(b)
			b.StartTimer()
			if err := e.SetMakerNoteTag(tag, grow); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("GrowAndEncode", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			b.StopTimer()
			e := parse(b)
			b.StartTimer()
			if err := e.SetMakerNoteTag(tag, grow); err != nil {
				b.Fatal(err)
			}
			if _, err := EncodeRebased(e, tiff); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("EncodeUnedited", func(b *testing.B) { // the baseline the edit adds to
		b.ReportAllocs()
		e := parse(b)
		for range b.N {
			if _, err := EncodeRebased(e, tiff); err != nil {
				b.Fatal(err)
			}
		}
	})
}
