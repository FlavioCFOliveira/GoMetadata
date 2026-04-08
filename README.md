<div align="center">

  # GoMetadata

<img src="assets/GoMetadata-logo.png" alt="GoMetadata" width="200" height="200" />

[![Go Reference](https://pkg.go.dev/badge/github.com/FlavioCFOliveira/GoMetadata.svg)](https://pkg.go.dev/github.com/FlavioCFOliveira/GoMetadata) [![Go Report Card](https://goreportcard.com/badge/github.com/FlavioCFOliveira/GoMetadata)](https://goreportcard.com/report/github.com/FlavioCFOliveira/GoMetadata) [![CI](https://github.com/FlavioCFOliveira/GoMetadata/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/FlavioCFOliveira/GoMetadata/actions/workflows/ci.yml) [![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg?style=flat)](LICENSE) [![Go: 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8.svg?style=flat)](https://go.dev/doc/install) [![codecov](https://codecov.io/gh/FlavioCFOliveira/GoMetadata/branch/main/graph/badge.svg)](https://codecov.io/gh/FlavioCFOliveira/GoMetadata) [![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/FlavioCFOliveira/GoMetadata/badge)](https://securityscorecards.dev/viewer/?uri=github.com/FlavioCFOliveira/GoMetadata) [![Release](https://img.shields.io/github/v/release/FlavioCFOliveira/GoMetadata?style=flat)](https://github.com/FlavioCFOliveira/GoMetadata/releases)

</div>

A pure Go library for reading and writing EXIF, IPTC, and XMP metadata from any image format. `GoMetadata` provides a single, unified API over all three metadata standards — EXIF 3.0 (CIPA DC-008 / TIFF 6.0), IPTC IIM 4.2, and XMP (ISO 16684-1) — across 13 container formats including JPEG, TIFF, PNG, WebP, HEIF/AVIF, and the major RAW formats (CR2, CR3, NEF, ARW, DNG, ORF, RW2).

Developers searching for a Go EXIF library, a Go IPTC parser, or a way to read and write XMP metadata in Go will find that `GoMetadata` handles all three in a single import. Format detection is by magic bytes, not file extension. All parsers are fuzz-tested and race-clean.

## Installation

```
go get github.com/FlavioCFOliveira/GoMetadata
```

Requires Go 1.26 or later. No non-stdlib runtime dependencies.

## Usage

### Reading common fields

```go
package main

import (
	"fmt"
	"log"

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	m, err := gometadata.ReadFile("photo.jpg")
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

	gometadata "github.com/FlavioCFOliveira/GoMetadata"
)

func main() {
	m, err := gometadata.ReadFile("photo.jpg")
	if err != nil {
		log.Fatal(err)
	}

	m.SetCaption("Grand Canyon, South Rim")
	m.SetCopyright("2024 Jane Smith")
	m.SetCreator("Jane Smith")
	m.SetKeywords([]string{"landscape", "canyon", "arizona"})
	m.SetGPS(36.0544, -112.1401)
	m.SetDateTimeOriginal(time.Date(2024, 9, 14, 7, 32, 0, 0, time.UTC))

	if err := gometadata.WriteFile("photo.jpg", m); err != nil {
		log.Fatal(err)
	}
}
```

### Skipping segments for faster reads

Use `ReadOption` helpers to skip segments you do not need. Skipping the MakerNote is the single biggest speed win for cameras with large proprietary blocks.

```go
m, err := gometadata.ReadFile("photo.jpg",
	gometadata.WithoutMakerNote(),
	gometadata.WithoutIPTC(),
	gometadata.WithoutXMP(),
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
import "github.com/FlavioCFOliveira/GoMetadata/format"

blank := gometadata.NewMetadata(format.JPEG)
blank.SetCameraModel("Custom Device")
blank.SetCopyright("2024 Example Corp")
```

## Examples

### Reading RAW file metadata

[`examples/raw-inspector`](examples/raw-inspector/main.go) — extract camera identification, shooting parameters, GPS, and descriptive fields from any RAW format (CR2, CR3, NEF, ARW, DNG, ORF, RW2). Format is detected automatically from magic bytes.

```go
// WithoutMakerNote skips the costliest part of EXIF parsing —
// the manufacturer-specific IFD — when only standard tags are needed.
m, err := gometadata.ReadFile(path, gometadata.WithoutMakerNote())

fmt.Printf("Format:      %s\n",     m.Format())
fmt.Printf("Make/Model:  %s %s\n",  m.Make(), m.CameraModel())
fmt.Printf("Lens:        %s\n",     m.LensModel())

if num, den, ok := m.ExposureTime(); ok { fmt.Printf("Shutter: 1/%d s\n", den/num) }
if f, ok := m.FNumber();             ok { fmt.Printf("Aperture: f/%.1f\n", f) }
if iso, ok := m.ISO();               ok { fmt.Printf("ISO: %d\n", iso) }
if fl, ok := m.FocalLength();        ok { fmt.Printf("Focal: %.0f mm\n", fl) }
if wb, ok := m.WhiteBalance();       ok { fmt.Printf("WB: %d\n", wb) }   // 0=auto 1=manual
if fl, ok := m.Flash();              ok { fmt.Printf("Flash fired: %v\n", fl&0x01 != 0) }
if lat, lon, ok := m.GPS();          ok { fmt.Printf("GPS: %.6f, %.6f\n", lat, lon) }
if alt, ok := m.Altitude();          ok { fmt.Printf("Altitude: %.1f m\n", alt) }
```

---

### Batch copyright stamping

[`examples/copyright-stamp`](examples/copyright-stamp/main.go) — walk a directory tree and embed copyright, creator, caption, and keywords into every image. Setters write to all non-nil metadata components (EXIF, IPTC, XMP) in one call.

```go
m, err := gometadata.ReadFile(path)
// Distinguish corrupt / truncated files from hard I/O errors.
var corrupt *gometadata.CorruptMetadataError
var truncated *gometadata.TruncatedFileError
switch {
case errors.As(err, &corrupt):   /* skip */
case errors.As(err, &truncated): /* skip */
}

// Each setter writes to all non-nil metadata layers simultaneously:
// SetCopyright → EXIF tag 0x8298 + IPTC dataset 2:116 + XMP dc:rights
m.SetCopyright("© 2025 Jane Smith. All rights reserved.")
m.SetCreator("Jane Smith")
m.SetCaption("Grand Canyon at sunset")
m.SetKeywords([]string{"landscape", "canyon", "arizona"})

gometadata.WriteFile(path, m) // atomic: temp file + rename
```

---

### Stream pipeline (no disk I/O)

[`examples/stream-transcode`](examples/stream-transcode/main.go) — read metadata from `stdin`, update fields, write to `stdout`. No temporary files. Works with any `io.ReadSeeker` and `io.Writer` — `net/http`, `bytes.Buffer`, object-store streams.

```go
m, err := gometadata.Read(os.Stdin)  // *os.File implements io.ReadSeeker

m.SetCaption("...")
m.SetGPS(48.8566, 2.3522) // Paris

// Seek back so Write can re-read the original image bytes from the same handle.
os.Stdin.Seek(0, io.SeekStart)

// PreserveUnknownSegments passes APP/chunk segments the library
// does not recognise through byte-for-byte (e.g. ICC profiles).
gometadata.Write(os.Stdin, os.Stdout, m, gometadata.PreserveUnknownSegments(true))
```

```sh
stream-transcode -caption "Night shot" -copyright "2025 J. Smith" < input.jpg > output.jpg
```

---

### JSON metadata export

[`examples/gallery-sidecar`](examples/gallery-sidecar/main.go) — parse images and emit a JSON array for static site generators, search indexes, or API responses. Optional fields use Go pointer types so absent values serialise as `null`.

```go
type imageRecord struct {
    File        string   `json:"file"`
    Format      string   `json:"format"`
    Model       *string  `json:"model,omitempty"`
    CapturedAt  *string  `json:"captured_at,omitempty"` // RFC3339
    Latitude    *float64 `json:"latitude,omitempty"`
    Longitude   *float64 `json:"longitude,omitempty"`
    ISO         *uint    `json:"iso,omitempty"`
    // ...
}

m, _ := gometadata.ReadFile(path, gometadata.WithoutMakerNote())
rec := imageRecord{File: path, Format: m.Format().String()}
if t, ok := m.DateTimeOriginal(); ok { s := t.Format(time.RFC3339); rec.CapturedAt = &s }
if lat, lon, ok := m.GPS();       ok { rec.Latitude = &lat; rec.Longitude = &lon }
if iso, ok := m.ISO();            ok { rec.ISO = &iso }
```

```sh
gallery-sidecar -pretty photo1.jpg photo2.nef photo3.heic
```

---

### Multi-format round-trip smoke test

[`examples/multi-format-roundtrip`](examples/multi-format-roundtrip/main.go) — read, modify, write, and re-read across all supported formats. Exits non-zero on any mismatch. Useful as a pre-release integration test.

```go
m, _ := gometadata.ReadFile(path)

// Check format before writing — not all container variants support write.
if !format.SupportsWrite(m.Format()) { /* skip */ }

m.SetCaption("roundtrip-test")
m.SetGPS(51.5074, -0.1278)

tmp, _ := os.CreateTemp(filepath.Dir(path), "roundtrip-*"+ext)
defer os.Remove(tmp.Name())

gometadata.WriteFile(tmp.Name(), m)

m2, _ := gometadata.ReadFile(tmp.Name())
fmt.Printf("PASS/FAIL %s (%s): caption=%v\n",
    path, m.Format(), m2.Caption() == "roundtrip-test")
```

---

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

## Performance

Benchmarks run with `go test -bench=. -benchmem -benchtime=2s ./...` (Go 1.26, macOS, `GOMAXPROCS=10`).  
All figures are the mean of multiple runs; allocation counts are stable across runs.

### End-to-end read

| Scenario | Time/op | Memory/op | Allocs/op |
|---|---:|---:|---:|
| Progressive JPEG (no metadata) | 163 ns | 176 B | 4 |
| JPEG — EXIF + IPTC + XMP combined | 10.6 µs | 22.8 kB | 24 |
| Real-world JPEG corpus file | 1.55 µs | 4.7 kB | 14 |
| Concurrent reads (parallel goroutines) | 11.4 µs | 544 B | 11 |

### Write

| Operation | Time/op | Memory/op | Allocs/op |
|---|---:|---:|---:|
| JPEG — metadata update | 282 ns | 264 B | 15 |
| PNG — pass-through | 188 ns | 168 B | 17 |

### Metadata format parsers

| Format | Operation | Time/op | Memory/op | Allocs/op |
|---|---|---:|---:|---:|
| EXIF | Parse — minimal TIFF (width, height, orientation) | 121 ns | 257 B | 4 |
| EXIF | Parse — camera tags | 997 ns | 2.4 kB | 8 |
| EXIF | Encode | 121 ns | 240 B | 6 |
| EXIF | IFD tag lookup — 100-entry set (binary search) | 3.8 ns | 0 B | 0 |
| IPTC | Parse | 102 ns | 944 B | 2 |
| IPTC | Encode | 68 ns | 96 B | 1 |
| IPTC | Field accessor | 26 ns | 64 B | 1 |
| XMP | Parse | 1.06 µs | 968 B | 12 |
| XMP | Encode | 650 ns | 3.1 kB | 2 |

### Container format parsers

| Format | Operation | Time/op |
|---|---|---:|
| JPEG | Segment extraction | 102 ns |
| JPEG | Segment injection | 206 ns |
| JPEG | Real corpus file (full parse) | 2.02 µs |
| PNG | Extraction | 192 ns |
| PNG | Extraction — compressed XMP (`zlib`) | 810 ns |
| TIFF | Extraction | 98 ns |
| WebP | Extraction | 98 ns |
| HEIF / AVIF | Extraction | 271 ns |
| Sony ARW | Extraction | 81 ns |
| Canon CR2 | Extraction | 82 ns |
| Adobe DNG | Extraction | 79 ns |
| Nikon NEF | Extraction | 80 ns |

> Canon CR3 and Olympus ORF/Panasonic RW2 benchmarks are covered by the TIFF and BMFF
> primitive benchmarks; their combined overhead falls within the same 80–100 ns range.

### Internal primitives

| Component | Operation | Time/op |
|---|---|---:|
| `sync.Pool` buffer | Get + Put (≤4 kB) | 7.0 ns |
| `sync.Pool` buffer | Get + Put (>64 kB) | 7.2 ns |
| Byte-order | `Uint16` little-endian | 0.26 ns |
| Byte-order | `Uint32` little-endian | 0.27 ns |
| BMFF | Read box header | 24.8 ns |
| BMFF | Skip box | 27.5 ns |
| RIFF | Read chunk header | 24.4 ns |

### Design choices behind the numbers

| Technique | Effect |
|---|---|
| `sync.Pool`-backed buffers (`internal/iobuf`) | Amortises heap allocation to zero after warm-up |
| Lazy parsing (`WithoutEXIF`, `WithoutIPTC`, `WithoutXMP`, `WithoutMakerNote`) | Skips unwanted segments entirely; MakerNote skip is the largest win on RAW files |
| Binary search in IFD entry set | Tag lookup in a 100-entry IFD costs **3.8 ns** and **0 allocations** |
| Lazy map init for extended XMP | Map is only allocated when extended XMP is actually present |
| Magic-byte format detection | Dispatch adds no measurable overhead; no string allocation |

## API reference

Full documentation is available at [pkg.go.dev/github.com/FlavioCFOliveira/GoMetadata](https://pkg.go.dev/github.com/FlavioCFOliveira/GoMetadata).

## License

MIT
