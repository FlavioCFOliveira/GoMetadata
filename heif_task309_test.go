package gometadata

// heif_task309_test.go — task #309: a write into a HEIF/AVIF file without
// Exif and XMP items creates both items, so the written metadata is read
// back instead of being silently dropped.

import (
	"bytes"
	"os"
	"testing"
)

func TestHEIF_Task309_CaptionCreatesItems(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"format/heif/testdata/no_metadata.avif", "format/heif/testdata/no_metadata.heic"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			src, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			before, err := Read(bytes.NewReader(src))
			if err != nil {
				t.Fatalf("Read source: %v", err)
			}
			if before.EXIF != nil || before.XMP != nil {
				t.Fatal("fixture unexpectedly carries metadata")
			}
			out := writeCaption(t, src, "task 309")
			assertTopLevelBoxesContiguous(t, out)
			after, err := Read(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("Read output: %v", err)
			}
			if after.EXIF == nil || after.XMP == nil {
				t.Fatalf("output: EXIF=%v XMP=%v, want both", after.EXIF != nil, after.XMP != nil)
			}
			if got := after.EXIF.IFD0.Get(0x010E); got == nil || got.String() != "task 309" {
				t.Errorf("EXIF ImageDescription = %v, want %q", got, "task 309")
			}
			if got := after.XMP.Caption(); got != "task 309" {
				t.Errorf("XMP caption = %q, want %q", got, "task 309")
			}
		})
	}
}
