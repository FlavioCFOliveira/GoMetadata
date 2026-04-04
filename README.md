# img-metadata

A pure Go library for reading and writing EXIF, IPTC, and XMP metadata from any image format. `img-metadata` provides a single, unified API over all three metadata standards — EXIF 3.0 (CIPA DC-008 / TIFF 6.0), IPTC IIM 4.2, and XMP (ISO 16684-1) — across 13 container formats including JPEG, TIFF, PNG, WebP, HEIF/AVIF, and the major RAW formats (CR2, CR3, NEF, ARW, DNG, ORF, RW2).

Developers searching for a Go EXIF library, a Go IPTC parser, or a way to read and write XMP metadata in Go will find that `img-metadata` handles all three in a single import. Format detection is by magic bytes, not file extension. All parsers are fuzz-tested and race-clean.

## Installation

```
go get github.com/FlavioCFOliveira/img-metadata
```

Requires Go 1.21 or later. No non-stdlib runtime dependencies.

## Usage

### Reading common fields

```go
package main

import (
	"fmt"
	"log"

	imgmetadata "github.com/FlavioCFOliveira/img-metadata"
)

func main() {
	m, err := imgmetadata.ReadFile("photo.jpg")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Camera:", m.CameraModel())
	fmt.Println("Make:  ", m.Make())
	fmt.Println("Lens:  ", m.LensModel())

	if lat, lon, ok := m.GPS(); ok {
		fmt.Printf("GPS: %.6f, %.6f\n", lat, lon)
	}
	if t, ok := m.DateTimeOriginal(); ok {
		fmt.Println("Shot:", t)
	}
	if num, den, ok := m.ExposureTime(); ok {
		fmt.Printf("Exposure: %d/%d s\n", num, den)
	}
	if f, ok := m.FNumber(); ok {
		fmt.Printf("Aperture: f/%.1f\n", f)
	}
	if iso, ok := m.ISO(); ok {
		fmt.Println("ISO:", iso)
	}

	fmt.Println("Caption:  ", m.Caption())
	fmt.Println("Copyright:", m.Copyright())
	fmt.Println("Keywords: ", m.Keywords())
}
```

### Writing and modifying metadata

`Write` and `WriteFile` preserve all image data and all metadata not explicitly changed. `WriteFile` performs an atomic in-place update via a temporary file and rename.

```go
package main

import (
	"log"
	"time"

	imgmetadata "github.com/FlavioCFOliveira/img-metadata"
)

func main() {
	m, err := imgmetadata.ReadFile("photo.jpg")
	if err != nil {
		log.Fatal(err)
	}

	m.SetCaption("Grand Canyon, South Rim")
	m.SetCopyright("2024 Jane Smith")
	m.SetCreator("Jane Smith")
	m.SetKeywords([]string{"landscape", "canyon", "arizona"})
	m.SetGPS(36.0544, -112.1401)
	m.SetDateTimeOriginal(time.Date(2024, 9, 14, 7, 32, 0, 0, time.UTC))

	if err := imgmetadata.WriteFile("photo.jpg", m); err != nil {
		log.Fatal(err)
	}
}
```

### Skipping segments for faster reads

Use `ReadOption` helpers to skip segments you do not need. Skipping the MakerNote is the single biggest speed win for cameras with large proprietary blocks.

```go
m, err := imgmetadata.ReadFile("photo.jpg",
	imgmetadata.WithoutMakerNote(),
	imgmetadata.WithoutIPTC(),
	imgmetadata.WithoutXMP(),
)
```

### Raw segment access and building from scratch

When you need direct access to the raw bytes of a segment, or want to construct a `Metadata` value to embed in a new file:

```go
// Raw segment bytes — useful for forwarding to another library or logging.
exifBytes := m.RawEXIF()
xmpBytes  := m.RawXMP()
iptcBytes := m.RawIPTC()

// Build a Metadata value from scratch (no source image required).
import "github.com/FlavioCFOliveira/img-metadata/format"

blank := imgmetadata.NewMetadata(format.JPEG)
blank.SetCameraModel("Custom Device")
blank.SetCopyright("2024 Example Corp")
```

## Supported features

| Feature | Details |
|---|---|
| Metadata standards | EXIF 3.0 (CIPA DC-008 / TIFF 6.0), IPTC IIM 4.2, XMP (ISO 16684-1) |
| Read | All three standards across all 13 container formats |
| Write | All three standards; preserves unmodified metadata byte-for-byte |
| Atomic writes | `WriteFile` uses temp file + rename — no partial writes |
| Format detection | Magic bytes only; file extension is never consulted |
| MakerNote (read) | Canon, Nikon, Sony, Olympus, Panasonic, Pentax, DJI, FujiFilm, Leica, Samsung, Sigma, Minolta, Casio |
| Convenience getters | 30+ typed getters with explicit source-priority resolution |
| Convenience setters | 15+ setters that write to all applicable non-nil components simultaneously |
| Priority resolution | Each getter documents its source order (e.g., EXIF > XMP); the caller always gets one answer |
| Lazy parsing | `WithoutEXIF()`, `WithoutIPTC()`, `WithoutXMP()`, `WithoutMakerNote()` skip unwanted work |
| Allocation budget | Zero/near-zero heap allocation in parsing fast paths; `sync.Pool` for reusable buffers |
| Fuzz testing | 18 fuzz targets covering all parsers |
| Race safety | Clean under `go test -race ./...` |
| Corpus coverage | 3,000+ real-world images tested, 0 failures |

## Supported formats

| Format | Extension(s) | Read | Write | EXIF | IPTC | XMP |
|---|---|:---:|:---:|:---:|:---:|:---:|
| JPEG | .jpg, .jpeg | Yes | Yes | Yes | Yes | Yes |
| TIFF | .tif, .tiff | Yes | Yes | Yes | Yes | Yes |
| PNG | .png | Yes | Yes | Yes | No | Yes |
| WebP | .webp | Yes | Yes | Yes | No | Yes |
| HEIF | .heif, .heic | Yes | Yes | Yes | No | Yes |
| AVIF | .avif | Yes | Yes | Yes | No | Yes |
| Canon CR2 | .cr2 | Yes | Yes | Yes | No | Yes |
| Canon CR3 | .cr3 | Yes | Yes | Yes | No | Yes |
| Nikon NEF | .nef | Yes | Yes | Yes | No | Yes |
| Sony ARW | .arw | Yes | Yes | Yes | No | Yes |
| Adobe DNG | .dng | Yes | Yes | Yes | No | Yes |
| Olympus ORF | .orf | Yes | Yes | Yes | No | Yes |
| Panasonic RW2 | .rw2 | Yes | Yes | Yes | No | Yes |

## API reference

Full documentation is available at [pkg.go.dev/github.com/FlavioCFOliveira/img-metadata](https://pkg.go.dev/github.com/FlavioCFOliveira/img-metadata).

## License

MIT
