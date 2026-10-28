package gometadata

// write_dng_truncated_task410_test.go — task #410: the corpus files IMG_1361*.dng
// are stripped of their image data (64104 bytes; the first strip starts at the
// end of the file, the tiles lie 5.5 MB to 28.9 MB in). Writing EXIF into them
// cannot keep that data, so Write refuses with the public TruncatedFileError
// instead of an internal block-offset error. ExifTool refuses too ("Error
// reading image data"); Exiv2 writes the file without the strips it cannot find.

import (
	"bytes"
	"errors"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/tiff"
)

func TestWriteDNG_R410_StrippedImageDataIsTruncatedFile(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"IMG_1361.dng", "IMG_1361_2.dng", "IMG_1361_3.dng"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := mneCorpus(t, "raw/exiv2/"+name)
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			m.SetCaption("x")
			var out bytes.Buffer
			err = Write(bytes.NewReader(data), &out, m)
			var trunc *TruncatedFileError
			if !errors.As(err, &trunc) || !errors.Is(err, tiff.ErrBlockOutOfBounds) {
				t.Fatalf("Write error = %v, want a TruncatedFileError that wraps tiff.ErrBlockOutOfBounds", err)
			}
			if out.Len() != 0 {
				t.Errorf("Write wrote %d bytes before it failed", out.Len())
			}
		})
	}
}
