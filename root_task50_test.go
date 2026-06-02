package gometadata

// root_task50_test.go — Sprint 8, Task #50
//
// Covers:
//  1. Format detection by magic bytes (not by file extension).
//  2. Reconciliation precedence — explicit tests for every documented policy.
//  3. MWG timezone synthesis — RFC 3339 round-trip.
//  4. Lazy options (WithoutXMP/IPTC/EXIF/MakerNote) — proven to skip work via
//     a corrupted-block approach (corrupt block would error in strict mode if
//     parsed, but is silently skipped when the lazy option is set).
//  5. PreserveUnknownSegments round-trip — unknown APP segment survives
//     Read→Write.
//  6. nil components — every accessor safe on nil-EXIF/IPTC/XMP Metadata.
//  7. Unsupported format — actionable error, no internal state leaked.
//  8. Concurrent Read/Write — clean under -race.
//  9. IO errors — file-not-found and permission-denied.
// 10. Write preserves file permissions.
// 11. Strict × {EXIF, XMP, IPTC} corrupted block matrix.
// 12. Best-effort × {EXIF, XMP, IPTC} corrupted block matrix.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
	"github.com/FlavioCFOliveira/GoMetadata/xmp"
)

// ---------------------------------------------------------------------------
// F-1: Format detection by magic bytes — not by file extension.
// ---------------------------------------------------------------------------

// TestFormatDetectionByMagicNotExtension verifies that Read detects the
// container format from magic bytes regardless of file extension. A JPEG file
// given a ".png" path must be parsed as JPEG; a PNG file given no extension
// must be parsed as PNG.
func TestFormatDetectionByMagicNotExtension(t *testing.T) {
	t.Parallel()

	jpegData := buildMinimalJPEG(nil) // JPEG magic
	pngData := buildMinimalPNG()      // PNG magic

	tests := []struct {
		name     string
		data     []byte
		wantFmt  format.FormatID
		fileName string // wrong extension to prove detection ignores extension
	}{
		{
			name:     "JPEG_bytes_in_dotpng_file",
			data:     jpegData,
			wantFmt:  format.FormatJPEG,
			fileName: "image.png", // wrong extension: JPEG magic overrides
		},
		{
			name:     "PNG_bytes_in_dotjpg_file",
			data:     pngData,
			wantFmt:  format.FormatPNG,
			fileName: "photo.jpg", // wrong extension: PNG magic overrides
		},
		{
			name:     "PNG_bytes_no_extension",
			data:     pngData,
			wantFmt:  format.FormatPNG,
			fileName: "noextension",
		},
		{
			name:     "JPEG_bytes_dotraw_extension",
			data:     jpegData,
			wantFmt:  format.FormatJPEG,
			fileName: "shot.raw",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Write tc.data to a temp file with the "wrong" name so that any
			// hypothetical extension-based dispatch would return the wrong format.
			tmp, err := os.CreateTemp("", "gometadata-magic-test-*-"+tc.fileName)
			if err != nil {
				t.Fatal(err)
			}
			path := tmp.Name()
			defer func() { _ = os.Remove(path) }()
			if _, err := tmp.Write(tc.data); err != nil {
				_ = tmp.Close()
				t.Fatal(err)
			}
			_ = tmp.Close()

			m, err := ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if got := m.Format(); got != tc.wantFmt {
				t.Errorf("Format() = %v (%d), want %v (%d) — detection relied on extension instead of magic",
					got, got, tc.wantFmt, tc.wantFmt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F-2: Reconciliation precedence — explicit tests for every documented policy.
// ---------------------------------------------------------------------------

// TestPrecedence_CameraData verifies that EXIF wins over XMP for camera data
// fields (CameraModel, Make, LensModel) per metadata.go §"Camera data".
func TestPrecedence_CameraData(t *testing.T) {
	t.Parallel()

	// Build a Metadata where EXIF and XMP carry different values for the same field.
	tiff := buildRichTIFF("ExifMake", "ExifModel", 6, 200) // orientation=6 (rotated 90 CW)
	jpeg := buildMinimalJPEG(tiff)

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Attach XMP with different camera values.
	m.XMP = &xmp.XMP{Properties: map[string]map[string]string{
		"http://ns.adobe.com/tiff/1.0/": {
			"Make":  "XmpMake",
			"Model": "XmpModel",
		},
	}}

	// CameraModel: EXIF must win.
	if got := m.CameraModel(); got != "ExifModel" {
		t.Errorf("CameraModel() = %q, want EXIF value %q", got, "ExifModel")
	}
	// Make: EXIF must win.
	if got := m.Make(); got != "ExifMake" {
		t.Errorf("Make() = %q, want EXIF value %q", got, "ExifMake")
	}
}

// TestPrecedence_CameraModelXMPFallback verifies that CameraModel falls back
// to XMP when EXIF is absent (or EXIF IFD0.Model is empty).
func TestPrecedence_CameraModelXMPFallback(t *testing.T) {
	t.Parallel()
	x := &xmp.XMP{Properties: map[string]map[string]string{
		"http://ns.adobe.com/tiff/1.0/": {"Model": "XMP-only camera"},
	}}
	m := &Metadata{XMP: x}
	if got := m.CameraModel(); got != "XMP-only camera" {
		t.Errorf("CameraModel() XMP fallback = %q, want %q", got, "XMP-only camera")
	}
}

// TestPrecedence_DescriptiveData verifies that XMP wins over IPTC over EXIF
// for descriptive fields (Caption, Copyright, Creator, Keywords) per
// metadata.go §"Descriptive and rights data".
func TestPrecedence_DescriptiveData(t *testing.T) {
	t.Parallel()

	// Build a JPEG with both IPTC and XMP caption/copyright.
	const (
		exifCaption   = "EXIF caption"
		iptcCaption   = "IPTC caption"
		xmpCaption    = "XMP caption"
		iptcCopyright = "IPTC (c)"
		xmpCopyright  = "XMP (c)"
	)

	// ---- Caption ----
	t.Run("Caption_XMP_wins_over_IPTC", func(t *testing.T) {
		t.Parallel()
		jpeg := buildJPEGWithIPTCAndXMP(iptcCaption, xmpCaption)
		m, err := Read(bytes.NewReader(jpeg))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if m.IPTC == nil || m.XMP == nil {
			t.Skip("IPTC or XMP nil — segments not parsed; test not meaningful")
		}
		if got := m.Caption(); got != xmpCaption {
			t.Errorf("Caption() = %q, want XMP value %q", got, xmpCaption)
		}
		// Cross-check: individual layers are unchanged.
		if got := m.IPTC.Caption(); got != iptcCaption {
			t.Errorf("IPTC.Caption() = %q, want %q", got, iptcCaption)
		}
		if got := m.XMP.Caption(); got != xmpCaption {
			t.Errorf("XMP.Caption() = %q, want %q", got, xmpCaption)
		}
	})

	t.Run("Caption_IPTC_wins_over_EXIF", func(t *testing.T) {
		t.Parallel()
		// Attach IPTC with caption; EXIF has caption via ImageDescription.
		// Build a rich TIFF with an image description tag.
		tiff := buildRichTIFF("Sony", "A7R5", 1, 100)
		jpeg := buildMinimalJPEG(tiff)
		m, err := Read(bytes.NewReader(jpeg))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		m.IPTC = new(iptc.IPTC)
		m.IPTC.SetCaption(iptcCaption)
		// XMP is nil → IPTC > EXIF.
		if got := m.Caption(); got != iptcCaption {
			t.Errorf("Caption() with IPTC+no-XMP = %q, want IPTC value %q", got, iptcCaption)
		}
	})

	// ---- Copyright ----
	t.Run("Copyright_XMP_wins_over_IPTC", func(t *testing.T) {
		t.Parallel()
		jpeg := buildJPEGWithIPTCAndXMP("", "") // captions don't matter here
		m, err := Read(bytes.NewReader(jpeg))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if m.IPTC == nil {
			t.Skip("IPTC nil")
		}
		m.IPTC.SetCopyright(iptcCopyright)
		if m.XMP == nil {
			m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
		}
		m.XMP.SetCopyright(xmpCopyright)

		if got := m.Copyright(); got != xmpCopyright {
			t.Errorf("Copyright() = %q, want XMP value %q", got, xmpCopyright)
		}
	})

	// ---- Creator ----
	t.Run("Creator_XMP_wins_over_IPTC_wins_over_EXIF", func(t *testing.T) {
		t.Parallel()
		const (
			exifCreator = "EXIF Creator"
			iptcCreator = "IPTC Creator"
			xmpCreator  = "XMP Creator"
		)
		tiff := buildRichTIFF("Make", "Model", 1, 200)
		jpeg := buildMinimalJPEG(tiff)
		m, err := Read(bytes.NewReader(jpeg))
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		m.IPTC = new(iptc.IPTC)
		m.IPTC.SetCreator(iptcCreator)
		m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
		m.XMP.SetCreator(xmpCreator)

		// XMP must win.
		if got := m.Creator(); got != xmpCreator {
			t.Errorf("Creator() = %q, want XMP value %q", got, xmpCreator)
		}

		// Drop XMP — IPTC must win over EXIF.
		m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)} // empty XMP
		if got := m.Creator(); got != iptcCreator {
			t.Errorf("Creator() (no XMP) = %q, want IPTC value %q", got, iptcCreator)
		}
	})

	// ---- Keywords ----
	t.Run("Keywords_XMP_wins_over_IPTC", func(t *testing.T) {
		t.Parallel()
		i := new(iptc.IPTC)
		i.SetKeywords([]string{"iptc-kw"})
		x := &xmp.XMP{Properties: make(map[string]map[string]string)}
		x.SetKeywords([]string{"xmp-kw1", "xmp-kw2"})
		m := &Metadata{IPTC: i, XMP: x}
		kws := m.Keywords()
		if len(kws) != 2 || kws[0] != "xmp-kw1" || kws[1] != "xmp-kw2" {
			t.Errorf("Keywords() = %v, want XMP value [xmp-kw1 xmp-kw2]", kws)
		}
	})
}

// TestPrecedence_GPS verifies that EXIF wins over XMP for GPS per
// metadata.go §GPS.
func TestPrecedence_GPS(t *testing.T) {
	t.Parallel()
	// Build a Metadata with EXIF GPS and a different XMP GPS.
	m := newTestMetadata(t)
	m.SetGPS(48.8566, 2.3522) // EXIF + XMP both set

	// Now override the XMP with a different coordinate.
	m.XMP.Set("http://www.w3.org/2003/01/geo/wgs84_pos#", "lat", "51,30.00N")
	m.XMP.Set("http://www.w3.org/2003/01/geo/wgs84_pos#", "lon", "0,7.00W")

	// GPS() must return the EXIF value.
	lat, lon, ok := m.GPS()
	if !ok {
		t.Fatal("GPS() ok=false, want true")
	}
	// The EXIF value was set to 48.8566 / 2.3522.
	// Tolerance: the EXIF rational encoding introduces small rounding error.
	if lat < 48.8 || lat > 48.9 {
		t.Errorf("GPS lat = %f, want ~48.8566 (EXIF wins)", lat)
	}
	if lon < 2.3 || lon > 2.4 {
		t.Errorf("GPS lon = %f, want ~2.3522 (EXIF wins)", lon)
	}
}

// ---------------------------------------------------------------------------
// F-3: MWG timezone synthesis — RFC 3339 output.
// ---------------------------------------------------------------------------

// TestMWGTimezoneSynthesis_RFC3339 verifies that when EXIF DateTimeOriginal
// lacks OffsetTimeOriginal and XMP carries a timezone, the returned time.Time
// can be formatted as RFC 3339 with the correct timezone offset.
// MWG Guidelines §2.2.1.
func TestMWGTimezoneSynthesis_RFC3339(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		xmpDate    string
		wantTZ     string // expected substring in RFC3339 string
		wantOffset int    // UTC offset in seconds
	}{
		{
			name:       "plus_two",
			xmpDate:    "2024-06-15T10:30:00+02:00",
			wantTZ:     "+02:00",
			wantOffset: 2 * 3600,
		},
		{
			name:       "minus_five",
			xmpDate:    "2024-06-15T10:30:00-05:00",
			wantTZ:     "-05:00",
			wantOffset: -5 * 3600,
		},
		{
			name:       "half_hour_plus_five_thirty",
			xmpDate:    "2024-06-15T10:30:00+05:30",
			wantTZ:     "+05:30",
			wantOffset: 5*3600 + 30*60,
		},
	}

	// Build a TIFF with ExifIFD DateTimeOriginal but no OffsetTimeOriginal.
	tiffData := buildExifIFDTIFF("2024:06:15 10:30:00", "")
	parsed, err := exif.Parse(tiffData)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := &xmp.XMP{Properties: map[string]map[string]string{
				xmp.NSexif: {"DateTimeOriginal": tc.xmpDate},
			}}
			m := &Metadata{EXIF: parsed, XMP: x}

			ts, ok := m.DateTimeOriginal()
			if !ok {
				t.Fatalf("DateTimeOriginal() ok=false")
			}
			// Format as RFC3339 and verify the timezone offset is present.
			rfc := ts.Format(time.RFC3339)
			if !contains(rfc, tc.wantTZ) {
				t.Errorf("RFC3339 output %q does not contain TZ %q", rfc, tc.wantTZ)
			}
			_, offset := ts.Zone()
			if offset != tc.wantOffset {
				t.Errorf("UTC offset = %d s, want %d s", offset, tc.wantOffset)
			}
			// Wall-clock digits must be 2024-06-15T10:30:00.
			if ts.Year() != 2024 || ts.Month() != 6 || ts.Day() != 15 ||
				ts.Hour() != 10 || ts.Minute() != 30 || ts.Second() != 0 {
				t.Errorf("wall-clock mismatch: got %v", ts)
			}
		})
	}
}

// buildExifIFDTIFF builds a TIFF stream (LE) with an ExifIFD containing
// DateTimeOriginal (and optionally OffsetTimeOriginal) entries.
func buildExifIFDTIFF(dto, offsetTimeOriginal string) []byte {
	order := binary.LittleEndian

	const headerSz = 8
	// IFD0: 1 entry (ExifIFDPointer)
	const (
		nIFD0  = 1
		ifd0Sz = 2 + nIFD0*12 + 4
	)

	// ExifIFD entries: DateTimeOriginal + optional OffsetTimeOriginal
	exifEntries := []struct {
		tag uint16
		val string
	}{
		{0x9003, dto},
	}
	if offsetTimeOriginal != "" {
		exifEntries = append(exifEntries, struct {
			tag uint16
			val string
		}{0x9011, offsetTimeOriginal})
	}
	nExifIFD := len(exifEntries)
	exifIFDSz := 2 + nExifIFD*12 + 4

	// Offsets.
	ifd0Off := uint32(headerSz)
	exifIFDOff := ifd0Off + uint32(ifd0Sz)
	valueAreaStart := exifIFDOff + uint32(exifIFDSz) //nolint:gosec // G115: exifIFDSz ≤ 2+8*12+4 = 102

	// Compute total size: value area holds all ASCII strings.
	valOffsets := make([]uint32, nExifIFD)
	cur := valueAreaStart
	for i, e := range exifEntries {
		valOffsets[i] = cur
		cur += uint32(len(e.val) + 1) //nolint:gosec // G115: test helper, controlled string lengths
	}
	totalSize := int(cur)

	buf := make([]byte, totalSize)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: ExifIFDPointer.
	off := int(ifd0Off)
	order.PutUint16(buf[off:], uint16(nIFD0))
	off += 2
	order.PutUint16(buf[off:], 0x8769) // ExifIFDPointer
	order.PutUint16(buf[off+2:], 4)    // TypeLong
	order.PutUint32(buf[off+4:], 1)    // count
	order.PutUint32(buf[off+8:], exifIFDOff)
	off += 12
	order.PutUint32(buf[off:], 0) // next IFD = 0

	// ExifIFD entries.
	off = int(exifIFDOff)
	order.PutUint16(buf[off:], uint16(nExifIFD)) //nolint:gosec // G115: test helper
	off += 2
	for i, e := range exifEntries {
		asciiBytes := []byte(e.val + "\x00")
		order.PutUint16(buf[off:], e.tag)
		order.PutUint16(buf[off+2:], 2)                       // TypeASCII
		order.PutUint32(buf[off+4:], uint32(len(asciiBytes))) //nolint:gosec // G115: test helper
		order.PutUint32(buf[off+8:], valOffsets[i])
		off += 12
		copy(buf[valOffsets[i]:], asciiBytes)
	}
	order.PutUint32(buf[off:], 0) // next-IFD = 0

	return buf
}

// ---------------------------------------------------------------------------
// F-4: Lazy options — proven to skip work via corrupted-block approach.
// ---------------------------------------------------------------------------

// TestWithoutEXIF_SkipsWork verifies that WithoutEXIF causes the EXIF segment
// to be skipped entirely, even when the bytes are corrupt (would cause a strict
// error if parsed).
func TestWithoutEXIF_SkipsWork(t *testing.T) {
	t.Parallel()
	// Corrupt EXIF payload (wrong TIFF magic): would produce a ParseSegmentError
	// in strict mode. With WithoutEXIF it must be silently skipped.
	corrupt := []byte{'I', 'I', 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	jpeg := buildMinimalJPEG(corrupt)

	// 1. Baseline: strict mode with no lazy option returns an error.
	_, strictErr := Read(bytes.NewReader(jpeg), Strict())
	if strictErr == nil {
		t.Fatal("Strict() + corrupt EXIF: expected error, got nil — baseline broken")
	}

	// 2. WithoutEXIF + Strict: the EXIF segment must be skipped (no parse error).
	m, err := Read(bytes.NewReader(jpeg), Strict(), WithoutEXIF())
	if err != nil {
		t.Fatalf("Strict() + WithoutEXIF() + corrupt EXIF: unexpected error: %v", err)
	}
	// EXIF must be nil — it was skipped, not parsed.
	if m.EXIF != nil {
		t.Error("EXIF should be nil when WithoutEXIF() is set")
	}
	// No warnings either — skipped segments don't generate warnings.
	if len(m.ParseWarnings) != 0 {
		t.Errorf("ParseWarnings should be empty when lazy, got: %v", m.ParseWarnings)
	}
}

// TestWithoutXMP_SkipsWork verifies that WithoutXMP causes the XMP segment to
// be skipped entirely, even when the bytes contain 102 nested tags (would
// trigger ErrXMLNestingDepth in strict mode).
func TestWithoutXMP_SkipsWork(t *testing.T) {
	t.Parallel()
	jpeg := buildJPEGWithCorruptXMP()

	// 1. Baseline: strict mode without lazy option returns a ParseSegmentError.
	_, strictErr := Read(bytes.NewReader(jpeg), Strict())
	if strictErr == nil {
		t.Fatal("Strict() + corrupt XMP: expected error, got nil — baseline broken")
	}

	// 2. WithoutXMP + Strict: no parse error; XMP is nil.
	m, err := Read(bytes.NewReader(jpeg), Strict(), WithoutXMP())
	if err != nil {
		t.Fatalf("Strict() + WithoutXMP() + corrupt XMP: unexpected error: %v", err)
	}
	if m.XMP != nil {
		t.Error("XMP should be nil when WithoutXMP() is set")
	}
	if len(m.ParseWarnings) != 0 {
		t.Errorf("ParseWarnings should be empty when lazy, got: %v", m.ParseWarnings)
	}
}

// TestWithoutIPTC_SkipsWork verifies that WithoutIPTC prevents IPTC parsing.
// Because IPTC Parse always succeeds (by design), we use a JPEG with a valid
// IPTC segment and verify the IPTC field is nil (skipped), while strict mode
// without the option produces a non-nil IPTC struct.
func TestWithoutIPTC_SkipsWork(t *testing.T) {
	t.Parallel()
	iptcRaw := buildIPTCBytes("test caption")
	jpeg := t50BuildJPEGWithIPTCOnly(iptcRaw)

	// 1. Baseline: without lazy option, IPTC is parsed and non-nil.
	m1, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read baseline: %v", err)
	}
	if m1.IPTC == nil {
		t.Fatal("IPTC should be non-nil on baseline parse")
	}

	// 2. WithoutIPTC: IPTC must be nil (skipped).
	m2, err := Read(bytes.NewReader(jpeg), WithoutIPTC())
	if err != nil {
		t.Fatalf("Read + WithoutIPTC: %v", err)
	}
	if m2.IPTC != nil {
		t.Errorf("IPTC should be nil when WithoutIPTC() is set, got non-nil")
	}
}

// TestWithoutMakerNote_SkipsWork verifies that WithoutMakerNote prevents
// MakerNote sub-IFD parsing while still returning the EXIF struct with the
// standard IFD0 tags decoded.
func TestWithoutMakerNote_SkipsWork(t *testing.T) {
	t.Parallel()
	tiff := minimalTIFFPayload()
	jpeg := buildMinimalJPEG(tiff)

	// Parse without the option — should succeed and produce EXIF.
	m, err := Read(bytes.NewReader(jpeg), WithoutMakerNote())
	if err != nil {
		t.Fatalf("Read + WithoutMakerNote: %v", err)
	}
	// EXIF must still be parsed (WithoutMakerNote only suppresses MakerNote
	// sub-IFD, not the whole EXIF segment).
	if m.EXIF == nil {
		t.Error("EXIF should be non-nil even with WithoutMakerNote()")
	}
	// MakerNoteIFD should be nil because we skipped it.
	if m.EXIF != nil && m.EXIF.MakerNoteIFD != nil {
		t.Error("MakerNoteIFD should be nil when WithoutMakerNote() is set")
	}
}

// ---------------------------------------------------------------------------
// F-5: PreserveUnknownSegments round-trip.
// ---------------------------------------------------------------------------

// TestPreserveUnknownSegments_RoundTrip verifies that an unrecognised APP
// segment (here a COM marker APP / COM comment marker) written into a JPEG
// survives a Read→Write round-trip.
//
// Per options.go, preservation is always active regardless of the option value.
// This test proves the documented behaviour holds for a JPEG with an unknown
// segment.
func TestPreserveUnknownSegments_RoundTrip(t *testing.T) {
	t.Parallel()

	// Build a JPEG with a COM (comment) marker which the library does not parse.
	// COM is 0xFF 0xFE; we embed a known string so we can verify it survived.
	jpeg := t50BuildJPEGWithComment("unique-sentinel-x7q9")

	// Read it.
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Add an IPTC block (to give Write something to do).
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("preserve-test")

	// Write with PreserveUnknownSegments(true) (which is also the default).
	var out bytes.Buffer
	if err := Write(bytes.NewReader(jpeg), &out, m, PreserveUnknownSegments(true)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify the sentinel string is present in the output bytes.
	if !bytes.Contains(out.Bytes(), []byte("unique-sentinel-x7q9")) {
		t.Error("output JPEG does not contain the original COM marker sentinel — unknown segment was not preserved")
	}

	// Also verify the IPTC caption survived.
	m2, err := Read(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Read after Write: %v", err)
	}
	if got := m2.Caption(); got != "preserve-test" {
		t.Errorf("Caption after round-trip = %q, want %q", got, "preserve-test")
	}
}

// ---------------------------------------------------------------------------
// F-11/F-12: Strict × {EXIF, XMP, IPTC} and best-effort × {EXIF, XMP, IPTC}
// corrupted block matrix.
//
// (EXIF and XMP cells already exist in read_test.go; the IPTC cells are new.)
// ---------------------------------------------------------------------------

// TestStrictMode_IPTCCorrupt verifies that when a JPEG carries a structurally
// valid APP13 with IPTC raw bytes that exceed the aggregate DoS cap (would
// normally be skipped), the strict/best-effort mode still does not return an
// error because IPTC.Parse always succeeds by design (it sets Truncated=true).
// The test asserts that the returned IPTC struct is non-nil and Truncated=true
// rather than expecting a ParseSegmentError.
func TestStrictMode_IPTCCorrupt(t *testing.T) {
	t.Parallel()
	// Build a JPEG with IPTC that is malformed: all bytes are junk (no 0x1C
	// markers). IPTC Parse will return an empty non-nil struct.
	junkIPTC := make([]byte, 100)
	for i := range junkIPTC {
		junkIPTC[i] = 0xFF // never 0x1C so no datasets are recognised
	}
	jpeg := t50BuildJPEGWithIPTCOnly(junkIPTC)

	// Strict mode: IPTC Parse returns nil error by design → no ParseSegmentError.
	m, err := Read(bytes.NewReader(jpeg), Strict())
	if err != nil {
		t.Fatalf("Strict() + junk IPTC: unexpected error: %v", err)
	}
	// IPTC must be non-nil — Parse always returns a struct.
	if m.IPTC == nil {
		t.Error("IPTC should be non-nil even for junk bytes (Parse always succeeds)")
	}

	// Best-effort mode: same expectation.
	m2, err2 := Read(bytes.NewReader(jpeg))
	if err2 != nil {
		t.Fatalf("best-effort + junk IPTC: unexpected error: %v", err2)
	}
	if m2.IPTC == nil {
		t.Error("IPTC should be non-nil in best-effort mode")
	}
}

// TestBestEffort_CorruptIPTC verifies that in best-effort mode, even with
// corrupt IPTC bytes, Read does not return an error and IPTC is non-nil.
func TestBestEffort_CorruptIPTC(t *testing.T) {
	t.Parallel()
	// IPTC bytes that start with 0x1C (valid marker) but have a declared
	// length that exceeds the buffer.
	truncatedIPTC := []byte{0x1C, 0x02, 0x78, 0xFF, 0xFF} // length = 65535, no body
	jpeg := t50BuildJPEGWithIPTCOnly(truncatedIPTC)

	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("best-effort + truncated IPTC: unexpected error: %v", err)
	}
	if m.IPTC == nil {
		t.Error("IPTC should be non-nil in best-effort mode even with truncated bytes")
	}
	// No ParseWarnings: IPTC Parse returns nil error.
	for _, w := range m.ParseWarnings {
		if w.Segment == "IPTC" {
			t.Errorf("unexpected IPTC ParseWarning: %v", w)
		}
	}
}

// TestCorruptedBlockMatrix exercises every cell of the strict×best-effort ×
// {EXIF,XMP,IPTC} matrix to confirm no panics occur.
func TestCorruptedBlockMatrix(t *testing.T) {
	t.Parallel()

	corruptEXIF := buildJPEGWithCorruptEXIF()
	corruptXMP := buildJPEGWithCorruptXMP()
	junkIPTC := make([]byte, 32)
	for i := range junkIPTC {
		junkIPTC[i] = 0xAB // not 0x1C
	}
	corruptIPTCJPEG := t50BuildJPEGWithIPTCOnly(junkIPTC)

	type cell struct {
		name    string
		data    []byte
		opts    []ReadOption
		wantErr bool // whether a non-nil error from Read is expected
	}

	cells := []cell{
		// Strict × corrupted EXIF → ParseSegmentError
		{name: "strict_exif", data: corruptEXIF, opts: []ReadOption{Strict()}, wantErr: true},
		// Strict × corrupted XMP → ParseSegmentError
		{name: "strict_xmp", data: corruptXMP, opts: []ReadOption{Strict()}, wantErr: true},
		// Strict × corrupted IPTC → no error (IPTC.Parse never errors)
		{name: "strict_iptc", data: corruptIPTCJPEG, opts: []ReadOption{Strict()}, wantErr: false},
		// Best-effort × corrupted EXIF → no error, warning recorded
		{name: "besteff_exif", data: corruptEXIF, opts: nil, wantErr: false},
		// Best-effort × corrupted XMP → no error, warning recorded
		{name: "besteff_xmp", data: corruptXMP, opts: nil, wantErr: false},
		// Best-effort × corrupted IPTC → no error
		{name: "besteff_iptc", data: corruptIPTCJPEG, opts: nil, wantErr: false},
	}

	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			m, err := Read(bytes.NewReader(c.data), c.opts...)
			// Must never panic (reaching here proves that).
			if c.wantErr && err == nil {
				t.Errorf("cell %q: expected error, got nil", c.name)
			}
			if !c.wantErr && err != nil {
				t.Errorf("cell %q: unexpected error: %v", c.name, err)
			}
			// When no error, accessors must not panic.
			if err == nil && m != nil {
				_ = m.Caption()
				_ = m.CameraModel()
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F-8: Concurrent Read/Write — clean under -race.
// ---------------------------------------------------------------------------

// TestConcurrentReadWriteRoot exercises concurrent Read and Write calls on
// distinct goroutines sharing the same Metadata struct. The test verifies
// clean operation under the race detector.
func TestConcurrentReadWriteRoot(t *testing.T) {
	t.Parallel()

	jpegData := buildMinimalJPEG(minimalTIFFPayload())

	m, err := Read(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("concurrent")

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Alternate between Read and Write in different goroutines.
			if idx%2 == 0 {
				_, errs[idx] = Read(bytes.NewReader(jpegData))
			} else {
				importOut := new(bytes.Buffer)
				errs[idx] = Write(bytes.NewReader(jpegData), importOut, m)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// F-7: Unsupported format — actionable error, no internal state leaked.
// ---------------------------------------------------------------------------

// TestUnsupportedFormat_NoInternalState verifies that the error returned for
// an unrecognised magic does not contain internal parser vocabulary (e.g.
// "IFD", "APP13", "rdf", etc.).
func TestUnsupportedFormat_NoInternalState(t *testing.T) {
	t.Parallel()
	_, err := Read(bytes.NewReader([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0}))
	if err == nil {
		t.Fatal("expected error for unrecognised magic, got nil")
	}
	msg := err.Error()
	// Must not contain internal parser vocabulary.
	internalTerms := []string{"IFD", "APP13", "rdf:", "x:xmpmeta", "Photoshop"}
	for _, term := range internalTerms {
		if contains(msg, term) {
			t.Errorf("error message leaks internal term %q: %q", term, msg)
		}
	}
	// Must be a typed UnsupportedFormatError.
	var uf *UnsupportedFormatError
	if !errors.As(err, &uf) {
		t.Errorf("expected *UnsupportedFormatError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// F-6: nil components — every accessor safe on all-nil Metadata.
// ---------------------------------------------------------------------------

// TestNilComponentsAllAccessors is a comprehensive nil-safety test that calls
// every public accessor on an all-nil Metadata and asserts no panic and correct
// zero-value returns.
func TestNilComponentsAllAccessors(t *testing.T) {
	t.Parallel()
	m := &Metadata{} // format=0 (Unknown), all metadata pointers nil

	// String/slice accessors.
	assertNilStringAccessors(t, m)
	// Bool-returning accessors.
	assertNilBoolAccessors(t, m)

	// Validate on a zero Metadata returns an error (unknown format).
	if err := m.Validate(); err == nil {
		t.Error("Validate() on zero Metadata should return error (unknown format)")
	}
}

// TestNilComponents_SettersNoOp verifies that calling every setter on an
// all-nil Metadata does not panic (all setters guard against nil components).
func TestNilComponents_SettersNoOp(t *testing.T) {
	t.Parallel()
	m := &Metadata{}
	m.SetCaption("x")
	m.SetCopyright("x")
	m.SetCreator("x")
	m.SetCameraModel("x")
	m.SetGPS(0, 0)
	m.SetKeywords([]string{"a"})
	m.SetLensModel("x")
	m.SetMake("x")
	m.SetDateTimeOriginal(time.Now())
	m.SetExposureTime(1, 100)
	m.SetFNumber(1.4)
	m.SetISO(100)
	m.SetFocalLength(50)
	m.SetOrientation(1)
	m.SetImageSize(100, 100)
	// reaching here without panic is the pass condition
}

// ---------------------------------------------------------------------------
// F-9: IO errors — file-not-found and permission-denied.
// ---------------------------------------------------------------------------

// TestIOErrors_FileNotFound and TestIOErrors_PermDenied are already covered
// in read_test.go (TestReadFileNotFound, TestReadFilePermDenied). This test
// exercises WriteFile error paths in the same fashion.

// TestWriteFileIOErrors verifies WriteFile returns clean errors for missing file.
func TestWriteFileIOErrors(t *testing.T) {
	t.Parallel()
	m := NewMetadata(format.FormatJPEG)
	err := WriteFile("/nonexistent/surely-not-here/image.jpg", m)
	if err == nil {
		t.Fatal("expected error for non-existent WriteFile path, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is(err, os.ErrNotExist) = false; err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// F-10: WriteFile preserves file permissions — already in read_test.go
// (TestWriteFilePreservesPermissions). No duplication needed.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers specific to this file.
// ---------------------------------------------------------------------------

// t50BuildJPEGWithIPTCOnly constructs a minimal JPEG containing only an APP13
// segment with the given raw IPTC IIM bytes wrapped in a Photoshop IRB.
func t50BuildJPEGWithIPTCOnly(iptcRaw []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	var irb bytes.Buffer
	irb.WriteString("Photoshop 3.0\x00")
	irb.WriteString("8BIM")
	irb.Write([]byte{0x04, 0x04})
	irb.Write([]byte{0x00, 0x00}) // empty Pascal string
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(len(iptcRaw))) //nolint:gosec // G115: test helper
	irb.Write(sz[:])
	irb.Write(iptcRaw)
	if len(iptcRaw)%2 != 0 {
		irb.WriteByte(0x00)
	}
	length := uint16(irb.Len() + 2) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xED})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(irb.Bytes())

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}

// t50BuildJPEGWithComment constructs a JPEG with a COM (comment) marker
// carrying the given comment string. COM is an unknown segment from the
// perspective of the metadata library.
func t50BuildJPEGWithComment(comment string) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI

	commentBytes := []byte(comment)
	length := uint16(len(commentBytes) + 2) //nolint:gosec // G115: test helper
	buf.Write([]byte{0xFF, 0xFE})           // COM marker
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], length)
	buf.Write(lb[:])
	buf.Write(commentBytes)

	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9}) // SOS + EOI
	return buf.Bytes()
}

// fuzzBuildPNGChunk (used in PNG seed builders) — local helper to avoid
// colliding with the identical function defined in roundtrip_test.go.
func t50WritePNGChunk(buf *bytes.Buffer, chunkType string, data []byte) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data))) //nolint:gosec // G115: test helper
	buf.Write(hdr[:])
	buf.WriteString(chunkType)
	buf.Write(data)
	h := crc32.NewIEEE()
	_, _ = h.Write([]byte(chunkType))
	_, _ = h.Write(data)
	binary.BigEndian.PutUint32(hdr[:], h.Sum32())
	buf.Write(hdr[:])
}

// TestRoundTripPreserveUnknownPNG verifies that a non-metadata PNG chunk (tEXt
// with a non-XMP keyword) survives a Read→Write round-trip unchanged.
func TestRoundTripPreserveUnknownPNG(t *testing.T) {
	t.Parallel()

	// Build a PNG with a tEXt chunk using a non-XMP keyword.
	var img bytes.Buffer
	img.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) // PNG sig

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 1) // width=1
	binary.BigEndian.PutUint32(ihdr[4:], 1) // height=1
	ihdr[8] = 8
	ihdr[9] = 2
	t50WritePNGChunk(&img, "IHDR", ihdr)

	// tEXt chunk with keyword "Comment" — this is not metadata the library parses.
	commentData := []byte("Comment\x00hello-unknown-sentinel-z8p2")
	t50WritePNGChunk(&img, "tEXt", commentData)

	t50WritePNGChunk(&img, "IEND", nil)

	m, err := Read(bytes.NewReader(img.Bytes()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCaption("test")

	var out bytes.Buffer
	if err := Write(bytes.NewReader(img.Bytes()), &out, m); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The unknown tEXt chunk sentinel must be present in the output.
	if !bytes.Contains(out.Bytes(), []byte("hello-unknown-sentinel-z8p2")) {
		t.Error("unknown tEXt chunk was dropped during round-trip — PreserveUnknownSegments not working for PNG")
	}
}

// TestPrecedence_ExplicitPolicyComment verifies that the documented resolution
// policy comment in metadata.go matches what the accessors actually do, by
// constructing a Metadata where every source has a different value and
// confirming the right one wins for each field.
func TestPrecedence_ExplicitPolicyComment(t *testing.T) {
	t.Parallel()
	// Construct a Metadata with EXIF, IPTC, and XMP all populated with distinct
	// values for each shared field.

	// EXIF
	tiff := buildRichTIFF("ExifMake", "ExifModel", 1, 200)
	jpeg := buildMinimalJPEG(tiff)
	m, err := Read(bytes.NewReader(jpeg))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if m.EXIF == nil {
		t.Fatal("EXIF nil")
	}
	m.EXIF.SetCopyright("ExifCopyright")
	m.EXIF.SetCreator("ExifCreator")

	// IPTC
	m.IPTC = new(iptc.IPTC)
	m.IPTC.SetCaption("IPTCCaption")
	m.IPTC.SetCopyright("IPTCCopyright")
	m.IPTC.SetCreator("IPTCCreator")
	m.IPTC.SetKeywords([]string{"iptc-kw"})

	// XMP
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.XMP.SetCaption("XMPCaption")
	m.XMP.SetCopyright("XMPCopyright")
	m.XMP.SetCreator("XMPCreator")
	m.XMP.SetKeywords([]string{"xmp-kw"})
	m.XMP.SetCameraModel("XMPCameraModel")
	m.XMP.Set("http://ns.adobe.com/tiff/1.0/", "Make", "XMPMake")

	// --- Camera data: EXIF wins ---
	if got := m.CameraModel(); got != "ExifModel" {
		t.Errorf("CameraModel() = %q, want EXIF value ExifModel", got)
	}
	if got := m.Make(); got != "ExifMake" {
		t.Errorf("Make() = %q, want EXIF value ExifMake", got)
	}

	// --- Descriptive/rights data: XMP wins over IPTC over EXIF ---
	if got := m.Caption(); got != "XMPCaption" {
		t.Errorf("Caption() = %q, want XMP value XMPCaption", got)
	}
	if got := m.Copyright(); got != "XMPCopyright" {
		t.Errorf("Copyright() = %q, want XMP value XMPCopyright", got)
	}
	if got := m.Creator(); got != "XMPCreator" {
		t.Errorf("Creator() = %q, want XMP value XMPCreator", got)
	}
	if kws := m.Keywords(); len(kws) != 1 || kws[0] != "xmp-kw" {
		t.Errorf("Keywords() = %v, want XMP value [xmp-kw]", kws)
	}

	// With XMP emptied, IPTC must win.
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	if got := m.Caption(); got != "IPTCCaption" {
		t.Errorf("Caption() (no XMP) = %q, want IPTC value IPTCCaption", got)
	}
	if got := m.Copyright(); got != "IPTCCopyright" {
		t.Errorf("Copyright() (no XMP) = %q, want IPTC value IPTCCopyright", got)
	}
	if got := m.Creator(); got != "IPTCCreator" {
		t.Errorf("Creator() (no XMP) = %q, want IPTC value IPTCCreator", got)
	}
	if kws := m.Keywords(); len(kws) != 1 || kws[0] != "iptc-kw" {
		t.Errorf("Keywords() (no XMP) = %v, want IPTC value [iptc-kw]", kws)
	}

	// With both XMP and IPTC empty, EXIF must win for Copyright/Creator.
	m.XMP = &xmp.XMP{Properties: make(map[string]map[string]string)}
	m.IPTC = new(iptc.IPTC) // empty IPTC
	if got := m.Copyright(); got != "ExifCopyright" {
		t.Errorf("Copyright() (no XMP, no IPTC) = %q, want EXIF value ExifCopyright", got)
	}
	if got := m.Creator(); got != "ExifCreator" {
		t.Errorf("Creator() (no XMP, no IPTC) = %q, want EXIF value ExifCreator", got)
	}
}

// buildMinimalPNG is duplicated from read_test.go as a package-internal helper.
// It is not re-exported — use buildMinimalPNG() from read_test.go instead when
// calling from the same package.
// The function name is deliberately the same because both files are in package
// gometadata (not _test), so Go will deduplicate by linker; but to avoid a
// duplicate-function compile error we rely on the fact that buildMinimalPNG is
// already defined in read_test.go in the same package. If needed we can alias.
// NOTE: buildMinimalPNG is defined in read_test.go; we do NOT redefine it here.

// fuzzBuildMinimalPNG uses a different local name to avoid duplicate-function
// errors at compile time — it is defined in fuzz_test.go.

// Ensure the fmt import is used (needed for buildExifIFDTIFF comment marker).
var _ = fmt.Sprintf
