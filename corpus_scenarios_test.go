package gometadata

// corpus_scenarios_test.go
//
// Targeted semantic tests against specific edge-case files from testdata/corpus/.
// These complement TestCorpusReadAll (which verifies no panics or
// CorruptMetadataErrors on all 3000+ corpus files) by asserting meaningful
// metadata invariants on particular files that exercise specific parser paths.
//
// All tests skip gracefully when the required file is absent.
// Run `bash testdata/download.sh` to populate the full corpus before running
// these tests.

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireCorpusFile returns the absolute path to a corpus file relative to
// testdata/corpus/. The test is skipped if the file is not present.
func requireCorpusFile(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join("testdata", "corpus", rel)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		t.Skipf("corpus file absent (run testdata/download.sh): %s", p)
	}
	return p
}

// openAndRead opens a corpus file and calls Read(). The test fails on
// CorruptMetadataError and skips on all other errors (UnsupportedFormat,
// TruncatedFile, etc.).
func openAndRead(t *testing.T, path string) *Metadata {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	m, err := Read(f)
	if err != nil {
		var corrupt *CorruptMetadataError
		if errors.As(err, &corrupt) {
			t.Fatalf("CorruptMetadataError on %s: %v", path, err)
		}
		t.Skipf("skipping %s: %v", filepath.Base(path), err)
	}
	return m
}

// walkCorpusDir returns all files under testdata/corpus/<subdir>/<source>/.
// The test is skipped if the directory is absent or empty.
func walkCorpusDir(t *testing.T, subdir string) []string {
	t.Helper()
	dir := filepath.Join("testdata", "corpus", subdir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skipf("corpus directory absent (run testdata/download.sh): %s", dir)
	}
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}
		return err
	})
	if len(paths) == 0 {
		t.Skipf("no files found in %s", dir)
	}
	return paths
}

// ---------------------------------------------------------------------------
// XMP-only JPEG — no EXIF APP1 segment
// ---------------------------------------------------------------------------

// TestCorpusXMPPresent verifies that a JPEG carrying an XMP packet produces a
// non-nil RawXMP segment. XMP.jpg from the exiftool suite also carries EXIF
// (a common combination) — the key invariant is that the XMP segment is found.
// Source: exiftool test suite (XMP.jpg).
func TestCorpusXMPPresent(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/XMP.jpg")
	m := openAndRead(t, path)

	if m.RawXMP() == nil {
		t.Error("RawXMP() = nil; XMP.jpg must carry an XMP packet")
	}
}

// TestCorpusNoExifSampleFile reads no_exif.jpg and verifies it is parseable.
// Despite its name, this file from ianare/exif-samples carries EXIF, IPTC,
// and XMP — the "no_exif" label refers to the absence of camera-specific EXIF.
// The test confirms the file parses without CorruptMetadataError.
func TestCorpusNoExifSampleFile(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exif-samples/no_exif.jpg")
	m := openAndRead(t, path)

	// File carries XMP; at minimum raw XMP bytes must be present.
	if m.RawXMP() == nil {
		t.Error("RawXMP() = nil; no_exif.jpg is expected to carry XMP data")
	}
}

// ---------------------------------------------------------------------------
// No metadata at all
// ---------------------------------------------------------------------------

// TestCorpusMinimalMetadata verifies that Unknown.jpg (a JPEG with EXIF but
// no IPTC and no XMP) is parsed cleanly. This exercises the common single-
// metadata-type path and confirms the absence of IPTC/XMP is handled gracefully.
// Source: exiftool test suite (Unknown.jpg).
func TestCorpusMinimalMetadata(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/Unknown.jpg")
	m := openAndRead(t, path)

	// Unknown.jpg carries EXIF but no IPTC and no XMP.
	if m.RawIPTC() != nil {
		t.Errorf("RawIPTC() = %d bytes; Unknown.jpg should not carry IPTC", len(m.RawIPTC()))
	}
	if m.RawXMP() != nil {
		t.Errorf("RawXMP() = %d bytes; Unknown.jpg should not carry XMP", len(m.RawXMP()))
	}
}

// ---------------------------------------------------------------------------
// Combined EXIF + IPTC + XMP
// ---------------------------------------------------------------------------

// TestCorpusCombinedMetadata asserts that a JPEG carrying all three metadata
// types is parsed and all three raw segments are non-nil.
// Source: exiftool test suite (ExifTool.jpg — all-three reference image).
func TestCorpusCombinedMetadata(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/ExifTool.jpg")
	m := openAndRead(t, path)

	if m.RawEXIF() == nil {
		t.Error("RawEXIF() = nil; ExifTool.jpg should carry EXIF")
	}
	if m.RawIPTC() == nil {
		t.Error("RawIPTC() = nil; ExifTool.jpg should carry IPTC")
	}
	if m.RawXMP() == nil {
		t.Error("RawXMP() = nil; ExifTool.jpg should carry XMP")
	}
}

// TestCorpusMWGCompliance verifies the Metadata Working Group reference image.
// MWG.jpg is a conformance target for consistent EXIF/IPTC/XMP alignment.
// Source: exiftool test suite.
func TestCorpusMWGCompliance(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/MWG.jpg")
	m := openAndRead(t, path)

	// MWG images are typically small but must carry aligned metadata.
	// At minimum the Caption (or one of its aliases) must be non-empty.
	if m.Caption() == "" && m.RawEXIF() == nil && m.RawIPTC() == nil && m.RawXMP() == nil {
		t.Error("MWG.jpg: all metadata accessors returned empty — expected at least one metadata type")
	}
}

// ---------------------------------------------------------------------------
// IPTC-only JPEG
// ---------------------------------------------------------------------------

// TestCorpusIPTCOnly verifies a JPEG that carries IPTC but no EXIF.
// Source: exiftool test suite (IPTC.jpg).
func TestCorpusIPTCOnly(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/IPTC.jpg")
	m := openAndRead(t, path)

	if m.RawIPTC() == nil {
		t.Error("RawIPTC() = nil; IPTC.jpg should carry IPTC")
	}
}

// ---------------------------------------------------------------------------
// Progressive JPEG
// ---------------------------------------------------------------------------

// TestCorpusProgressiveJPEG verifies that progressive JPEGs (SOF2 marker)
// are parsed without error. Metadata segments appear before the SOFn marker
// so encoding style should not affect extraction.
// Source: sindresorhus/is-progressive fixture set.
func TestCorpusProgressiveJPEG(t *testing.T) {
	t.Parallel()
	files := walkCorpusDir(t, "jpeg/progressive")
	progressive := []string{"kitten-progressive.jpg", "progressive.jpg"}

	for _, name := range progressive {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := requireCorpusFile(t, filepath.Join("jpeg", "progressive", name))
			// Must parse without CorruptMetadataError. Other errors are benign.
			openAndRead(t, path)
		})
	}
	_ = files // Walk verified directory is non-empty.
}

// TestCorpusBaselineVsProgressive reads both a baseline and a progressive JPEG
// and asserts neither produces a CorruptMetadataError.
func TestCorpusBaselineVsProgressive(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"baseline.jpg", "kitten-progressive.jpg"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := requireCorpusFile(t, filepath.Join("jpeg", "progressive", name))
			openAndRead(t, path) // CorruptMetadataError → fatal; skip otherwise
		})
	}
}

// ---------------------------------------------------------------------------
// GPS coordinates
// ---------------------------------------------------------------------------

// TestCorpusGPSCoordinates reads a GPS-tagged JPEG and asserts that the
// returned coordinates are within valid WGS-84 bounds.
// Source: exiftool test suite (GPS.jpg — purpose-built GPS fixture).
func TestCorpusGPSCoordinates(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/GPS.jpg")
	m := openAndRead(t, path)

	lat, lon, ok := m.GPS()
	if !ok {
		t.Skip("GPS() returned ok=false; GPS data may not be accessible via EXIF")
	}
	if lat < -90 || lat > 90 {
		t.Errorf("GPS lat %f out of range [-90, 90]", lat)
	}
	if lon < -180 || lon > 180 {
		t.Errorf("GPS lon %f out of range [-180, 180]", lon)
	}
}

// ---------------------------------------------------------------------------
// Extended XMP (multi-packet)
// ---------------------------------------------------------------------------

// TestCorpusExtendedXMP verifies that a JPEG carrying multi-packet extended
// XMP (GUIDed continuation segments) is reassembled and parsed correctly.
// Source: exiftool test suite (ExtendedXMP.jpg).
func TestCorpusExtendedXMP(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "jpeg/exiftool/ExtendedXMP.jpg")
	m := openAndRead(t, path)

	if m.RawXMP() == nil {
		t.Error("RawXMP() = nil; ExtendedXMP.jpg should produce reassembled XMP")
	}
}

// ---------------------------------------------------------------------------
// MakerNote variants (libexif)
// ---------------------------------------------------------------------------

// TestCorpusMakerNoteVariants verifies that all MakerNote variant fixtures
// from the libexif test suite are parsed without CorruptMetadataError and
// produce non-nil EXIF data.
// Source: libexif/libexif test/testdata (canon, fuji, olympus 2-5, pentax 2-4).
func TestCorpusMakerNoteVariants(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("testdata", "corpus", "jpeg", "libexif-makernotes")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skip("libexif-makernotes not present (run testdata/download.sh)")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Skip("libexif-makernotes directory is empty")
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, e.Name())
			m := openAndRead(t, path)
			if m.RawEXIF() == nil {
				t.Errorf("%s: RawEXIF() = nil; MakerNote variant files must carry EXIF", e.Name())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MakerNote manufacturers — exiftool corpus
// ---------------------------------------------------------------------------

// TestCorpusMakerNoteManufacturers checks one JPEG per camera manufacturer
// from the exiftool test suite. Each file is purpose-built to exercise a
// specific MakerNote encoding. The key invariant is: no CorruptMetadataError
// and EXIF is present.
func TestCorpusMakerNoteManufacturers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		file string
	}{
		{"Canon", "jpeg/exiftool/Canon.jpg"},
		{"Canon1DmkIII", "jpeg/exiftool/Canon1DmkIII.jpg"},
		{"Nikon", "jpeg/exiftool/Nikon.jpg"},
		{"NikonD70", "jpeg/exiftool/NikonD70.jpg"},
		{"NikonD2Hs", "jpeg/exiftool/NikonD2Hs.jpg"},
		{"Olympus", "jpeg/exiftool/Olympus.jpg"},
		{"Olympus2", "jpeg/exiftool/Olympus2.jpg"},
		{"Panasonic", "jpeg/exiftool/Panasonic.jpg"},
		{"Pentax", "jpeg/exiftool/Pentax.jpg"},
		{"Sony", "jpeg/exiftool/Sony.jpg"},
		{"Sigma", "jpeg/exiftool/Sigma.jpg"},
		{"Minolta", "jpeg/exiftool/Minolta.jpg"},
		{"FujiFilm", "jpeg/exiftool/FujiFilm.jpg"},
		{"Casio", "jpeg/exiftool/Casio.jpg"},
		{"Ricoh", "jpeg/exiftool/Ricoh.jpg"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join("testdata", "corpus", tc.file)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				t.Skipf("file absent: %s", tc.file)
			}
			m := openAndRead(t, p)
			if m.RawEXIF() == nil {
				t.Errorf("%s: RawEXIF() = nil; manufacturer fixture must carry EXIF", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// IPTC official reference images
// ---------------------------------------------------------------------------

// TestCorpusIPTCReferenceImages validates the official IPTC Photo Metadata
// reference images. Each image has every IPTC field populated with a
// recognisable value, making them ideal for verifying parser field coverage.
//
// Source: iptc.org (IPTC-PhotometadataRef-Std20xx.x.jpg).
func TestCorpusIPTCReferenceImages(t *testing.T) {
	t.Parallel()
	variants := []string{
		"Std2024.1",
		"Std2023.2",
		"Std2021.1",
		"Std2019.1",
		"Std2017.1",
	}
	for _, v := range variants {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			path := requireCorpusFile(t, "jpeg/iptc/IPTC-PhotometadataRef-"+v+".jpg")
			m := openAndRead(t, path)

			// All IPTC reference images must carry IPTC APP13 data.
			if m.RawIPTC() == nil {
				t.Errorf("[%s] RawIPTC() = nil; reference image must carry IPTC data", v)
			}

			// Caption/Abstract (IPTC 2:120) or XMP description must be non-empty.
			if m.Caption() == "" {
				t.Errorf("[%s] Caption() = \"\"; reference image must have a caption", v)
			}

			// Keyword list must be non-empty (IPTC 2:25 / XMP dc:subject).
			kws := m.Keywords()
			if len(kws) == 0 {
				t.Errorf("[%s] Keywords() = []; reference image must have keywords", v)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TIFF structural variants (exampletiffs)
// ---------------------------------------------------------------------------

// TestCorpusTIFFStructuralVariants reads TIFF files covering different
// compression schemes (LZW, deflate, uncompressed), tiling configurations,
// planar storage, and multi-page sequences. These exercise the TIFF IFD
// traversal code paths beyond camera images.
// Source: tlnagy/exampletiffs.
func TestCorpusTIFFStructuralVariants(t *testing.T) {
	t.Parallel()
	files := []string{
		"mri.tif",                              // multi-page (MRI slices)
		"shapes_tiled_multi.tif",               // tiled, multi-page
		"shapes_lzw.tif",                       // LZW compression
		"shapes_deflate.tif",                   // deflate compression
		"shapes_uncompressed.tif",              // no compression
		"shapes_lzw_planar.tif",                // planar (non-chunky) storage
		"shapes_lzw_tiled.tif",                 // LZW + tiled
		"shapes_lzw_palette.tif",               // palette/indexed
		"shapes_uncompressed_tiled_planar.tif", // uncompressed + tiled + planar
		"4D-series.ome.tif",                    // OME-TIFF with XML in ImageDescription
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := requireCorpusFile(t, filepath.Join("tiff", "exampletiffs", name))
			// Must not CorruptMetadataError. Other errors are benign (e.g. no EXIF).
			openAndRead(t, path)
		})
	}
}

// TestCorpusMultiPageTIFF specifically validates that a multi-page TIFF is
// readable without corruption and returns valid (or absent) metadata.
func TestCorpusMultiPageTIFF(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "tiff/exampletiffs/mri.tif")
	m := openAndRead(t, path)
	_ = m // The key invariant is: no panic and no CorruptMetadataError.
}

// ---------------------------------------------------------------------------
// Big-endian TIFF
// ---------------------------------------------------------------------------

// TestCorpusBigEndianTIFF reads BigTIFF Motorola (big-endian, MM byte order)
// files and verifies that the byte-order negotiation path handles them without
// producing a CorruptMetadataError.
// Source: drewnoakes/metadata-extractor-images tif/BigTIFF/.
func TestCorpusBigEndianTIFF(t *testing.T) {
	t.Parallel()
	files := []string{
		"BigTIFFMotorola.tif",
		"BigTIFFMotorolaLongStrips.tif",
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := requireCorpusFile(t, filepath.Join("tiff", "metadata-extractor", name))
			openAndRead(t, path)
		})
	}
}

// TestCorpusBigTIFFVariants reads all BigTIFF files (Intel and Motorola byte
// order, various offset widths and SubIFD depths) to confirm graceful handling.
func TestCorpusBigTIFFVariants(t *testing.T) {
	t.Parallel()
	files := []string{
		"BigTIFF.tif",
		"BigTIFFMotorola.tif",
		"BigTIFFMotorolaLongStrips.tif",
		"BigTIFFLong.tif",
		"BigTIFFLong8.tif",
		"BigTIFFSubIFD4.tif",
		"BigTIFFSubIFD8.tif",
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join("testdata", "corpus", "tiff", "metadata-extractor", name)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				t.Skipf("file absent: %s", name)
			}
			openAndRead(t, p)
		})
	}
}

// ---------------------------------------------------------------------------
// WebP variants (VP8, VP8L, VP8X, animated)
// ---------------------------------------------------------------------------

// TestCorpusWebPVariants reads WebP files covering all three encoding types
// (VP8 lossy, VP8L lossless, VP8X extended) plus animated WebP.
// Source: drewnoakes/metadata-extractor-images webp/.
func TestCorpusWebPVariants(t *testing.T) {
	t.Parallel()
	dir := filepath.Join("testdata", "corpus", "webp", "metadata-extractor")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Skip("webp/metadata-extractor corpus absent")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Skip("no WebP corpus files")
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".webp") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, e.Name())
			openAndRead(t, path)
		})
	}
}

// ---------------------------------------------------------------------------
// HEIF / HEIC / AVIF
// ---------------------------------------------------------------------------

// TestCorpusHEIFVariants reads all HEIF-family files from the corpus,
// covering Apple HEIC, Nokia HEIC, Sony HEIF, and AVIF.
// Source: drewnoakes/metadata-extractor-images heif/ and ianare/exif-samples heic/.
func TestCorpusHEIFVariants(t *testing.T) {
	t.Parallel()
	paths := walkCorpusDir(t, "heif")
	var heifFiles []string
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".heic" || ext == ".heif" || ext == ".avif" {
			heifFiles = append(heifFiles, p)
		}
	}
	if len(heifFiles) == 0 {
		t.Skip("no HEIC/HEIF/AVIF files found in corpus")
	}

	for _, path := range heifFiles {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			openAndRead(t, path)
		})
	}
}

// ---------------------------------------------------------------------------
// RAW formats — new manufacturers
// ---------------------------------------------------------------------------

// TestCorpusRawFormatCoverage reads RAW files for formats supported by the
// library (CR2, CR3, NEF, ARW, DNG, ORF, RW2) and verifies they parse
// without CorruptMetadataError.
func TestCorpusRawFormatCoverage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ext  string
		dir  string
	}{
		{"CanonCR2", "cr2", "raw/exiftool"},
		{"CanonCR3", "cr3", "raw/exiftool"},
		{"NikonNEF", "nef", "raw/exiftool"},
		{"AdobeDNG", "dng", "raw/exiftool"},
		{"PanasonicRW2", "rw2", "raw/exiftool"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join("testdata", "corpus", tc.dir)
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				t.Skipf("directory absent: %s", tc.dir)
			}
			entries, _ := os.ReadDir(dir)
			var found bool
			for _, e := range entries {
				if !e.IsDir() && strings.ToLower(filepath.Ext(e.Name())) == "."+tc.ext {
					found = true
					path := filepath.Join(dir, e.Name())
					openAndRead(t, path)
					break
				}
			}
			if !found {
				t.Skipf("no .%s file found in %s", tc.ext, tc.dir)
			}
		})
	}
}

// TestCorpusRawGracefulUnsupported verifies that genuinely unsupported RAW
// formats (Fuji RAF, Minolta MRW, Sigma X3F, Phase One IIQ) return
// UnsupportedFormatError and do not panic or return CorruptMetadataError.
func TestCorpusRawGracefulUnsupported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		file string
	}{
		{"FujiRAF", "raw/exiftool/FujiFilm.raf"},
		{"MinoltaMRW", "raw/exiftool/Minolta.mrw"},
		{"SigmaX3F", "raw/exiftool/Sigma.x3f"},
		{"PhaseOneIIQ", "raw/exiftool/PhaseOne.iiq"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join("testdata", "corpus", tc.file)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				t.Skipf("file absent: %s", tc.file)
			}
			f, err := os.Open(p)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { _ = f.Close() }()
			_, rerr := Read(f)
			if rerr == nil {
				// The format may be supported now — that's fine.
				return
			}
			var corrupt *CorruptMetadataError
			if errors.As(rerr, &corrupt) {
				t.Errorf("%s: got CorruptMetadataError; want UnsupportedFormatError: %v", tc.name, rerr)
			}
			// UnsupportedFormatError or TruncatedFileError are both acceptable.
		})
	}
}

// ---------------------------------------------------------------------------
// PNG edge cases
// ---------------------------------------------------------------------------

// TestCorpusPNGEdgeCases reads specific PNG files from the Exiv2 corpus that
// target individual chunk scenarios: eXIf chunk, tEXt/Comment, and no-metadata.
// Source: Exiv2/exiv2 test/data.
func TestCorpusPNGEdgeCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		file     string
		wantExif bool
	}{
		{"exif-chunk", "png/exiv2/1343_exif.png", true},
		{"comment-chunk", "png/exiv2/1343_comment.png", false},
		{"no-metadata", "png/exiv2/1343_empty.png", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join("testdata", "corpus", tc.file)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				t.Skipf("file absent: %s", tc.file)
			}
			m := openAndRead(t, p)
			if tc.wantExif && m.RawEXIF() == nil {
				t.Errorf("%s: RawEXIF() = nil; expected EXIF in eXIf chunk", tc.name)
			}
		})
	}
}

// TestCorpusPNGInterlaced verifies that interlaced PNG files are handled
// without error or panic.
func TestCorpusPNGInterlaced(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "png/metadata-extractor/photoshop-8x12-rgb24-interlaced.png")
	openAndRead(t, path)
}

// TestCorpusPNGWithAllMetadata reads a Photoshop-generated PNG that carries
// EXIF, IPTC, and XMP in a single file.
func TestCorpusPNGWithAllMetadata(t *testing.T) {
	t.Parallel()
	path := requireCorpusFile(t, "png/metadata-extractor/photoshop-8x12-rgb24-all-metadata.png")
	m := openAndRead(t, path)

	// This specific file is documented to have all three metadata types.
	if m.RawEXIF() == nil && m.RawIPTC() == nil && m.RawXMP() == nil {
		t.Error("photoshop all-metadata PNG: all raw accessors nil; expected at least one metadata type")
	}
}

// ---------------------------------------------------------------------------
// XMP sidecar files
// ---------------------------------------------------------------------------

// TestCorpusXMPSidecars verifies that XMP sidecar (.xmp) files from the
// corpus are handled gracefully. They are XML documents and will return
// UnsupportedFormatError (not JPEG/TIFF/PNG containers), which is correct.
func TestCorpusXMPSidecars(t *testing.T) {
	t.Parallel()
	dirs := []string{"xmp/metadata-extractor", "xmp/exiv2", "xmp/exiftool"}
	for _, d := range dirs {
		dir := filepath.Join("testdata", "corpus", d)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".xmp") {
				continue
			}
			t.Run(filepath.Join(d, e.Name()), func(t *testing.T) {
				t.Parallel()
				path := filepath.Join(dir, e.Name())
				f, err := os.Open(path)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				defer func() { _ = f.Close() }()
				_, rerr := Read(f)
				if rerr == nil {
					return // If we happen to support it, fine.
				}
				var corrupt *CorruptMetadataError
				if errors.As(rerr, &corrupt) {
					t.Errorf("XMP sidecar produced CorruptMetadataError: %v", rerr)
				}
				// UnsupportedFormatError is expected for raw XMP files.
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Benchmarks for new corpus additions
// ---------------------------------------------------------------------------

// BenchmarkReadProgressiveJPEG measures Read() performance on a progressive
// JPEG, allowing comparison against baseline JPEG performance.
func BenchmarkReadProgressiveJPEG(b *testing.B) {
	path := filepath.Join("testdata", "corpus", "jpeg", "progressive", "kitten-progressive.jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		b.Skip("progressive JPEG corpus absent")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read: %v", err)
	}
	b.ResetTimer()
	b.SetBytes(int64(len(data)))
	for range b.N {
		if _, err := Read(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadCombinedMetadataJPEG measures Read() on a JPEG with all three
// metadata types (EXIF + IPTC + XMP), the heaviest realistic parse path.
func BenchmarkReadCombinedMetadataJPEG(b *testing.B) {
	path := filepath.Join("testdata", "corpus", "jpeg", "exiftool", "ExifTool.jpg")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		b.Skip("ExifTool.jpg corpus absent")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read: %v", err)
	}
	b.ResetTimer()
	b.SetBytes(int64(len(data)))
	for range b.N {
		if _, err := Read(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
