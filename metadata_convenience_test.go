package imgmetadata

// Tests for Metadata convenience methods that were under-covered (P2 gaps).
//
// Each sub-test builds a minimal TIFF payload carrying the relevant tag(s),
// parses it with exif.Parse, wraps the result in a *Metadata, and calls the
// method under test. Nil-safety (zero-value Metadata) is verified in
// TestMetadata_NilMetadata in read_test.go; the tests here focus on the
// "tag present and correctly decoded" path.

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/flaviocfo/img-metadata/exif"
	"github.com/flaviocfo/img-metadata/xmp"
)

// ---------------------------------------------------------------------------
// TIFF builder helpers
// ---------------------------------------------------------------------------

// tiffBuilder assembles a TIFF byte stream in little-endian order from a set
// of typed IFD entries. Each entry is described by tiffEntry; values that do
// not fit inline (>4 bytes) are placed in a trailing value area and the entry
// receives the correct offset.
//
// Layout:
//
//	TIFF header (8 B)
//	IFD0: count(2) + N×entry(12) + next(4)
//	[ExifIFD: count(2) + M×entry(12) + next(4)]   (when exifEntries is non-nil)
//	[GPSIFD:  count(2) + K×entry(12) + next(4)]   (when gpsEntries is non-nil)
//	Value area (all out-of-line values concatenated)
type tiffEntry struct {
	tag   exif.TagID
	typ   exif.DataType
	count uint32
	// inline is used when the total value size is ≤ 4 bytes.
	inline [4]byte
	// outOfLine is used when the value does not fit in 4 bytes; it is placed in
	// the value area and the entry gets the offset.
	outOfLine []byte
}

// typeSize returns the byte size of a single element of the given DataType.
func typeSize(t exif.DataType) int {
	switch t {
	case exif.TypeByte, exif.TypeASCII, exif.TypeSByte, exif.TypeUndefined:
		return 1
	case exif.TypeShort, exif.TypeSShort:
		return 2
	case exif.TypeLong, exif.TypeSLong, exif.TypeFloat:
		return 4
	case exif.TypeRational, exif.TypeSRational, exif.TypeDouble:
		return 8
	default:
		return 1
	}
}

// buildTIFFMultiIFD constructs a TIFF stream (LE) from up to three IFDs.
// ifd0Extra contains IFD0 entries (pointer tags are added automatically).
// exifEntries and gpsEntries are the ExifIFD and GPSIFD entries respectively;
// pass nil to omit either sub-IFD.
func buildTIFFMultiIFD(ifd0Extra, exifEntries, gpsEntries []tiffEntry) []byte {
	order := binary.LittleEndian

	const headerSz = 8

	// Determine whether we need ExifIFD/GPSIFD pointer entries in IFD0.
	needExif := len(exifEntries) > 0
	needGPS := len(gpsEntries) > 0

	// Build the full IFD0 entry list: user entries + pointer entries.
	// Pointer values are placeholders; they are patched below once offsets are known.
	ifd0 := make([]tiffEntry, len(ifd0Extra))
	copy(ifd0, ifd0Extra)
	if needExif {
		ifd0 = append(ifd0, tiffEntry{
			tag: exif.TagExifIFDPointer, typ: exif.TypeLong, count: 1,
		})
	}
	if needGPS {
		ifd0 = append(ifd0, tiffEntry{
			tag: exif.TagGPSIFDPointer, typ: exif.TypeLong, count: 1,
		})
	}
	// IFD0 must have at least one entry for the TIFF parser to accept it.
	// When caller passes nil for all three slices, add a dummy tag.
	if len(ifd0) == 0 {
		ifd0 = []tiffEntry{makeShortEntry(exif.TagImageWidth, 1)}
	}

	// Compute block sizes.
	ifd0Sz := 2 + len(ifd0)*12 + 4
	exifSz := 0
	if needExif {
		exifSz = 2 + len(exifEntries)*12 + 4
	}
	gpsSz := 0
	if needGPS {
		gpsSz = 2 + len(gpsEntries)*12 + 4
	}

	// Absolute offsets within the TIFF blob.
	ifd0Off := uint32(headerSz)
	exifOff := ifd0Off + uint32(ifd0Sz)
	gpsOff := exifOff + uint32(exifSz)
	valueAreaStart := gpsOff + uint32(gpsSz)

	// Patch ExifIFD and GPSIFD pointer values in ifd0 now that offsets are known.
	for i := range ifd0 {
		switch ifd0[i].tag {
		case exif.TagExifIFDPointer:
			order.PutUint32(ifd0[i].inline[:], exifOff)
		case exif.TagGPSIFDPointer:
			order.PutUint32(ifd0[i].inline[:], gpsOff)
		}
	}

	// Pass 1: walk all IFDs and assign out-of-line value offsets in order.
	// Each IFD carries its ifdIdx (0=IFD0, 1=ExifIFD, 2=GPSIFD) so that the
	// write pass can look up the correct placement without pointer comparison.
	type valPlacement struct {
		ifdIdx   int
		entryIdx int
		off      uint32
	}
	var placements []valPlacement
	cursor := valueAreaStart

	placeEntries := func(entries []tiffEntry, ifdIdx int) {
		for i := range entries {
			e := &entries[i]
			totalSize := uint32(typeSize(e.typ)) * e.count
			if totalSize > 4 && len(e.outOfLine) > 0 {
				placements = append(placements, valPlacement{ifdIdx, i, cursor})
				cursor += uint32(len(e.outOfLine))
			}
		}
	}
	placeEntries(ifd0, 0)
	if needExif {
		placeEntries(exifEntries, 1)
	}
	if needGPS {
		placeEntries(gpsEntries, 2)
	}

	buf := make([]byte, int(cursor))

	// TIFF header (TIFF §2).
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// writeIFD serialises one IFD into buf at offset base.
	// ifdIdx is used to match entries against their valPlacement records.
	writeIFD := func(base uint32, entries []tiffEntry, ifdIdx int, nextOff uint32) {
		off := int(base)
		order.PutUint16(buf[off:], uint16(len(entries)))
		off += 2
		for i, e := range entries {
			order.PutUint16(buf[off:], uint16(e.tag))
			order.PutUint16(buf[off+2:], uint16(e.typ))
			order.PutUint32(buf[off+4:], e.count)

			totalSize := uint32(typeSize(e.typ)) * e.count
			placed := false
			if totalSize > 4 && len(e.outOfLine) > 0 {
				for _, p := range placements {
					if p.ifdIdx == ifdIdx && p.entryIdx == i {
						order.PutUint32(buf[off+8:], p.off)
						placed = true
						break
					}
				}
			}
			if !placed {
				// Inline value, left-justified (TIFF §2).
				copy(buf[off+8:off+12], e.inline[:])
			}
			off += 12
		}
		order.PutUint32(buf[off:], nextOff) // next-IFD pointer
	}

	writeIFD(ifd0Off, ifd0, 0, 0)
	if needExif {
		writeIFD(exifOff, exifEntries, 1, 0)
	}
	if needGPS {
		writeIFD(gpsOff, gpsEntries, 2, 0)
	}

	// Pass 2: copy out-of-line values into the value area.
	allEntries := [][]tiffEntry{ifd0, exifEntries, gpsEntries}
	for _, p := range placements {
		entries := allEntries[p.ifdIdx]
		copy(buf[p.off:], entries[p.entryIdx].outOfLine)
	}

	return buf
}

// makeShortEntry builds a tiffEntry for a SHORT (uint16) tag stored inline.
func makeShortEntry(tag exif.TagID, val uint16) tiffEntry {
	var e tiffEntry
	e.tag = tag
	e.typ = exif.TypeShort
	e.count = 1
	binary.LittleEndian.PutUint16(e.inline[:], val)
	return e
}

// makeASCIIEntry builds a tiffEntry for an ASCII tag. Strings ≤ 3 bytes
// (including NUL) are stored inline; longer strings are placed out-of-line.
func makeASCIIEntry(tag exif.TagID, s string) tiffEntry {
	val := []byte(s + "\x00")
	e := tiffEntry{tag: tag, typ: exif.TypeASCII, count: uint32(len(val))}
	if len(val) <= 4 {
		copy(e.inline[:], val)
	} else {
		e.outOfLine = val
	}
	return e
}

// makeRationalEntry builds a tiffEntry for a RATIONAL (num/den) stored
// out-of-line (8 bytes, always > 4).
func makeRationalEntry(tag exif.TagID, num, den uint32) tiffEntry {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b[0:], num)
	binary.LittleEndian.PutUint32(b[4:], den)
	return tiffEntry{tag: tag, typ: exif.TypeRational, count: 1, outOfLine: b}
}

// makeUndefinedEntry builds a tiffEntry for an UNDEFINED-type tag. Values ≤ 4
// bytes are stored inline; longer values are placed out-of-line.
func makeUndefinedEntry(tag exif.TagID, data []byte) tiffEntry {
	e := tiffEntry{tag: tag, typ: exif.TypeUndefined, count: uint32(len(data))}
	if len(data) <= 4 {
		copy(e.inline[:], data)
	} else {
		e.outOfLine = data
	}
	return e
}

// makeByteEntry builds a tiffEntry for a BYTE tag stored inline (1 byte).
func makeByteEntry(tag exif.TagID, val byte) tiffEntry {
	return tiffEntry{
		tag: tag, typ: exif.TypeByte, count: 1,
		inline: [4]byte{val},
	}
}

// ---------------------------------------------------------------------------
// Software()
// ---------------------------------------------------------------------------

func TestMetadata_Software(t *testing.T) {
	tests := []struct {
		name     string
		software string
		wantOK   bool
	}{
		{"present", "Adobe Lightroom 6.0", true},
		{"short", "X", true},
		{"empty string falls through to XMP", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantOK {
				tiff := buildTIFFMultiIFD(
					[]tiffEntry{makeASCIIEntry(exif.TagSoftware, tc.software)},
					nil, nil,
				)
				parsed, err := exif.Parse(tiff)
				if err != nil {
					t.Fatalf("exif.Parse: %v", err)
				}
				m := &Metadata{EXIF: parsed}
				if got := m.Software(); got != tc.software {
					t.Errorf("Software() = %q, want %q", got, tc.software)
				}
			} else {
				// Empty EXIF — should fall through to XMP.
				m := &Metadata{}
				if got := m.Software(); got != "" {
					t.Errorf("Software() on empty Metadata = %q, want empty", got)
				}
			}
		})
	}

	t.Run("fallback to XMP CreatorTool", func(t *testing.T) {
		// Directly populate the public Properties map — no encoder needed.
		x := &xmp.XMP{Properties: map[string]map[string]string{
			xmp.NSxmp: {"CreatorTool": "Capture One 23"},
		}}
		m := &Metadata{XMP: x}
		if got := m.Software(); got != "Capture One 23" {
			t.Errorf("Software() via XMP = %q, want %q", got, "Capture One 23")
		}
	})

	t.Run("EXIF wins over XMP", func(t *testing.T) {
		tiff := buildTIFFMultiIFD(
			[]tiffEntry{makeASCIIEntry(exif.TagSoftware, "EXIF Software")},
			nil, nil,
		)
		parsed, err := exif.Parse(tiff)
		if err != nil {
			t.Fatalf("exif.Parse: %v", err)
		}
		x := &xmp.XMP{Properties: map[string]map[string]string{
			xmp.NSxmp: {"CreatorTool": "XMP Software"},
		}}
		m := &Metadata{EXIF: parsed, XMP: x}
		if got := m.Software(); got != "EXIF Software" {
			t.Errorf("Software() = %q, want EXIF value %q", got, "EXIF Software")
		}
	})
}

// ---------------------------------------------------------------------------
// DateTime()
// ---------------------------------------------------------------------------

func TestMetadata_DateTime(t *testing.T) {
	const rawDT = "2023:06:15 12:30:00"
	wantTime := time.Date(2023, 6, 15, 12, 30, 0, 0, time.UTC)

	tiff := buildTIFFMultiIFD(
		[]tiffEntry{makeASCIIEntry(exif.TagDateTime, rawDT)},
		nil, nil,
	)
	parsed, err := exif.Parse(tiff)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	m := &Metadata{EXIF: parsed}

	got, ok := m.DateTime()
	if !ok {
		t.Fatal("DateTime() ok = false, want true")
	}
	if !got.Equal(wantTime) {
		t.Errorf("DateTime() = %v, want %v", got, wantTime)
	}

	t.Run("tag absent", func(t *testing.T) {
		tiff2 := buildTIFFMultiIFD(
			[]tiffEntry{makeASCIIEntry(exif.TagSoftware, "dummy")},
			nil, nil,
		)
		parsed2, _ := exif.Parse(tiff2)
		m2 := &Metadata{EXIF: parsed2}
		if _, ok2 := m2.DateTime(); ok2 {
			t.Error("DateTime() ok = true, want false when tag absent")
		}
	})

	t.Run("malformed value", func(t *testing.T) {
		tiff3 := buildTIFFMultiIFD(
			[]tiffEntry{makeASCIIEntry(exif.TagDateTime, "not-a-date")},
			nil, nil,
		)
		parsed3, _ := exif.Parse(tiff3)
		m3 := &Metadata{EXIF: parsed3}
		if _, ok3 := m3.DateTime(); ok3 {
			t.Error("DateTime() ok = true, want false for malformed value")
		}
	})
}

// ---------------------------------------------------------------------------
// WhiteBalance()
// ---------------------------------------------------------------------------

func TestMetadata_WhiteBalance(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
	}{
		{"auto", 0},
		{"manual", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeShortEntry(exif.TagWhiteBalance, tc.val)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.WhiteBalance()
			if !ok {
				t.Fatal("WhiteBalance() ok = false, want true")
			}
			if got != tc.val {
				t.Errorf("WhiteBalance() = %d, want %d", got, tc.val)
			}
		})
	}

	t.Run("tag absent", func(t *testing.T) {
		tiff := buildTIFFMultiIFD(
			nil,
			[]tiffEntry{makeShortEntry(exif.TagFlash, 0)},
			nil,
		)
		parsed, _ := exif.Parse(tiff)
		m := &Metadata{EXIF: parsed}
		if _, ok := m.WhiteBalance(); ok {
			t.Error("WhiteBalance() ok = true, want false when tag absent")
		}
	})
}

// ---------------------------------------------------------------------------
// Flash()
// ---------------------------------------------------------------------------

func TestMetadata_Flash(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
	}{
		{"not fired", 0x00},
		{"fired", 0x01},
		{"fired no return detected", 0x05},
		{"auto fired", 0x19},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeShortEntry(exif.TagFlash, tc.val)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.Flash()
			if !ok {
				t.Fatalf("Flash() ok = false, want true")
			}
			if got != tc.val {
				t.Errorf("Flash() = 0x%02X, want 0x%02X", got, tc.val)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExposureMode()
// ---------------------------------------------------------------------------

func TestMetadata_ExposureMode(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
		desc string
	}{
		{"auto", 0, "auto exposure"},
		{"manual", 1, "manual exposure"},
		{"auto bracket", 2, "auto bracket"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeShortEntry(exif.TagExposureMode, tc.val)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.ExposureMode()
			if !ok {
				t.Fatalf("ExposureMode() ok = false, want true")
			}
			if got != tc.val {
				t.Errorf("ExposureMode() = %d, want %d (%s)", got, tc.val, tc.desc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Altitude()
// ---------------------------------------------------------------------------

func TestMetadata_Altitude(t *testing.T) {
	tests := []struct {
		name    string
		num     uint32
		den     uint32
		ref     byte // 0 = above, 1 = below
		wantAlt float64
	}{
		{"above sea level", 15000, 100, 0, 150.0},
		{"below sea level", 5000, 100, 1, -50.0},
		{"zero", 0, 1, 0, 0.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil, nil,
				[]tiffEntry{
					makeRationalEntry(exif.TagGPSAltitude, tc.num, tc.den),
					makeByteEntry(exif.TagGPSAltitudeRef, tc.ref),
				},
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.Altitude()
			if !ok {
				t.Fatalf("Altitude() ok = false, want true")
			}
			if math.Abs(got-tc.wantAlt) > 1e-9 {
				t.Errorf("Altitude() = %f, want %f", got, tc.wantAlt)
			}
		})
	}

	t.Run("zero denominator", func(t *testing.T) {
		tiff := buildTIFFMultiIFD(
			nil, nil,
			[]tiffEntry{
				makeRationalEntry(exif.TagGPSAltitude, 100, 0), // den=0 → invalid
			},
		)
		parsed, _ := exif.Parse(tiff)
		m := &Metadata{EXIF: parsed}
		if _, ok := m.Altitude(); ok {
			t.Error("Altitude() ok = true, want false for zero denominator")
		}
	})

	t.Run("absent GPSIFD", func(t *testing.T) {
		tiff := buildTIFFMultiIFD(nil, nil, nil)
		parsed, _ := exif.Parse(tiff)
		m := &Metadata{EXIF: parsed}
		if _, ok := m.Altitude(); ok {
			t.Error("Altitude() ok = true, want false when GPSIFD absent")
		}
	})
}

// ---------------------------------------------------------------------------
// SubjectDistance()
// ---------------------------------------------------------------------------

func TestMetadata_SubjectDistance(t *testing.T) {
	tests := []struct {
		name string
		num  uint32
		den  uint32
		want float64
	}{
		{"one metre", 100, 100, 1.0},
		{"two and half metres", 250, 100, 2.5},
		{"infinity sentinel (large)", 0xFFFFFFFF, 1, float64(0xFFFFFFFF)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeRationalEntry(exif.TagSubjectDistance, tc.num, tc.den)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.SubjectDistance()
			if !ok {
				t.Fatalf("SubjectDistance() ok = false, want true")
			}
			want := float64(tc.num) / float64(tc.den)
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("SubjectDistance() = %f, want %f", got, want)
			}
		})
	}

	t.Run("zero denominator", func(t *testing.T) {
		tiff := buildTIFFMultiIFD(
			nil,
			[]tiffEntry{makeRationalEntry(exif.TagSubjectDistance, 100, 0)},
			nil,
		)
		parsed, _ := exif.Parse(tiff)
		m := &Metadata{EXIF: parsed}
		if _, ok := m.SubjectDistance(); ok {
			t.Error("SubjectDistance() ok = true, want false for zero denominator")
		}
	})
}

// ---------------------------------------------------------------------------
// DigitalZoomRatio()
// ---------------------------------------------------------------------------

func TestMetadata_DigitalZoomRatio(t *testing.T) {
	tests := []struct {
		name string
		num  uint32
		den  uint32
		want float64
	}{
		{"not used (0/0 would be zero den — use 0/1)", 0, 1, 0.0},
		{"1x", 1, 1, 1.0},
		{"2.5x", 5, 2, 2.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeRationalEntry(exif.TagDigitalZoomRatio, tc.num, tc.den)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.DigitalZoomRatio()
			if !ok {
				t.Fatalf("DigitalZoomRatio() ok = false, want true")
			}
			want := float64(tc.num) / float64(tc.den)
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("DigitalZoomRatio() = %f, want %f", got, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SceneType()
// ---------------------------------------------------------------------------

func TestMetadata_SceneType(t *testing.T) {
	tests := []struct {
		name string
		val  byte
	}{
		{"directly photographed", 0x00},
		{"non-zero", 0x01},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeUndefinedEntry(exif.TagSceneType, []byte{tc.val})},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.SceneType()
			if !ok {
				t.Fatalf("SceneType() ok = false, want true")
			}
			if got != tc.val {
				t.Errorf("SceneType() = %d, want %d", got, tc.val)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ColorSpace()
// ---------------------------------------------------------------------------

func TestMetadata_ColorSpace(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
		desc string
	}{
		{"sRGB", 0x0001, "sRGB"},
		{"uncalibrated", 0xFFFF, "uncalibrated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeShortEntry(exif.TagColorSpace, tc.val)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.ColorSpace()
			if !ok {
				t.Fatalf("ColorSpace() ok = false, want true")
			}
			if got != tc.val {
				t.Errorf("ColorSpace() = 0x%04X, want 0x%04X (%s)", got, tc.val, tc.desc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MeteringMode()
// ---------------------------------------------------------------------------

func TestMetadata_MeteringMode(t *testing.T) {
	tests := []struct {
		name string
		val  uint16
		desc string
	}{
		{"unknown", 0, "unknown"},
		{"average", 1, "average"},
		{"center-weighted", 2, "center-weighted average"},
		{"spot", 3, "spot"},
		{"multi-spot", 4, "multi-spot"},
		{"pattern", 5, "pattern"},
		{"partial", 6, "partial"},
		{"other", 255, "other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tiff := buildTIFFMultiIFD(
				nil,
				[]tiffEntry{makeShortEntry(exif.TagMeteringMode, tc.val)},
				nil,
			)
			parsed, err := exif.Parse(tiff)
			if err != nil {
				t.Fatalf("exif.Parse: %v", err)
			}
			m := &Metadata{EXIF: parsed}
			got, ok := m.MeteringMode()
			if !ok {
				t.Fatalf("MeteringMode() ok = false, want true")
			}
			if got != tc.val {
				t.Errorf("MeteringMode() = %d, want %d (%s)", got, tc.val, tc.desc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Combined ExifIFD methods: test multiple tags in a single ExifIFD.
// ---------------------------------------------------------------------------

// TestMetadata_ExifIFDMultipleTagsCoexist verifies that when an ExifIFD
// contains several of the tested tags simultaneously, each accessor returns
// the correct value independently.
func TestMetadata_ExifIFDMultipleTagsCoexist(t *testing.T) {
	tiff := buildTIFFMultiIFD(
		nil,
		[]tiffEntry{
			makeShortEntry(exif.TagWhiteBalance, 1),
			makeShortEntry(exif.TagFlash, 0x01),
			makeShortEntry(exif.TagExposureMode, 1),
			makeShortEntry(exif.TagColorSpace, 0x0001),
			makeShortEntry(exif.TagMeteringMode, 5),
			makeUndefinedEntry(exif.TagSceneType, []byte{0x00}),
			makeRationalEntry(exif.TagSubjectDistance, 300, 100),
			makeRationalEntry(exif.TagDigitalZoomRatio, 3, 2),
		},
		nil,
	)
	parsed, err := exif.Parse(tiff)
	if err != nil {
		t.Fatalf("exif.Parse: %v", err)
	}
	m := &Metadata{EXIF: parsed}

	if v, ok := m.WhiteBalance(); !ok || v != 1 {
		t.Errorf("WhiteBalance() = (%d, %v), want (1, true)", v, ok)
	}
	if v, ok := m.Flash(); !ok || v != 0x01 {
		t.Errorf("Flash() = (0x%02X, %v), want (0x01, true)", v, ok)
	}
	if v, ok := m.ExposureMode(); !ok || v != 1 {
		t.Errorf("ExposureMode() = (%d, %v), want (1, true)", v, ok)
	}
	if v, ok := m.ColorSpace(); !ok || v != 0x0001 {
		t.Errorf("ColorSpace() = (0x%04X, %v), want (0x0001, true)", v, ok)
	}
	if v, ok := m.MeteringMode(); !ok || v != 5 {
		t.Errorf("MeteringMode() = (%d, %v), want (5, true)", v, ok)
	}
	if v, ok := m.SceneType(); !ok || v != 0x00 {
		t.Errorf("SceneType() = (%d, %v), want (0, true)", v, ok)
	}
	if v, ok := m.SubjectDistance(); !ok || math.Abs(v-3.0) > 1e-9 {
		t.Errorf("SubjectDistance() = (%f, %v), want (3.0, true)", v, ok)
	}
	if v, ok := m.DigitalZoomRatio(); !ok || math.Abs(v-1.5) > 1e-9 {
		t.Errorf("DigitalZoomRatio() = (%f, %v), want (1.5, true)", v, ok)
	}
}

// TestMetadata_AltitudeAboveAndBelow verifies both positive and negative
// altitude resolution in a single TIFF with both GPS tags.
func TestMetadata_AltitudeAboveAndBelow(t *testing.T) {
	// Above sea level: 200.0 m.
	tiffAbove := buildTIFFMultiIFD(
		nil, nil,
		[]tiffEntry{
			makeRationalEntry(exif.TagGPSAltitude, 20000, 100),
			makeByteEntry(exif.TagGPSAltitudeRef, 0),
		},
	)
	parsedAbove, err := exif.Parse(tiffAbove)
	if err != nil {
		t.Fatalf("exif.Parse above: %v", err)
	}
	mAbove := &Metadata{EXIF: parsedAbove}
	if alt, ok := mAbove.Altitude(); !ok || math.Abs(alt-200.0) > 1e-9 {
		t.Errorf("Altitude (above) = (%f, %v), want (200.0, true)", alt, ok)
	}

	// Below sea level: -75.5 m.
	tiffBelow := buildTIFFMultiIFD(
		nil, nil,
		[]tiffEntry{
			makeRationalEntry(exif.TagGPSAltitude, 7550, 100),
			makeByteEntry(exif.TagGPSAltitudeRef, 1),
		},
	)
	parsedBelow, err := exif.Parse(tiffBelow)
	if err != nil {
		t.Fatalf("exif.Parse below: %v", err)
	}
	mBelow := &Metadata{EXIF: parsedBelow}
	if alt, ok := mBelow.Altitude(); !ok || math.Abs(alt-(-75.5)) > 1e-9 {
		t.Errorf("Altitude (below) = (%f, %v), want (-75.5, true)", alt, ok)
	}
}
