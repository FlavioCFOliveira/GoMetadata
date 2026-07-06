package exif

// bigtiff_write_test.go — task #264: BigTIFF write support for exif.Encode.
//
// Spec references:
//   - BigTIFF spec (Aware Systems / libtiff) §2: 16-byte header, 20-byte IFD
//     entries, 64-bit offsets, 8-byte inline threshold.
//   - EXIF §4.6.3/§4.5.5: sub-IFD and thumbnail pointer tags are fixed LONG
//     (4-byte) fields regardless of container.
//
// Test categories:
//   RT  — Round-trip fidelity (R-14, R-17): Parse -> Encode -> Parse produces
//         byte-identical (Tag, Type, Count, Value) for every entry.
//   G   — Guard tests: R-15 (size ceiling) and R-16 (pointer overflow) fire
//         without panicking or attempting a runaway allocation.
//   V   — Value-level correctness: V-14 unknown-type round-trip; S-38 sub-IFD
//         pointer tags stay TypeLong with the physical field widened.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// buildBigTIFFCameraEXIF builds a *EXIF with EXIF.BigTIFF forced to true and
// a full sub-IFD tree (ExifIFD with a MakerNote, GPSIFD, InteropIFD nested
// inside ExifIFD) attached programmatically. This exercises the BigTIFF
// sub-IFD pointer dispatch (patchPointers via serialiseBigTIFF) end-to-end,
// independent of any specific corpus fixture's structure (none of the
// committed BigTIFF fixtures happen to carry a real ExifIFD/GPSIFD/InteropIFD
// tree — see the dump in the task #264 spec investigation).
func buildBigTIFFCameraEXIF(t *testing.T) *EXIF {
	t.Helper()
	order := binary.LittleEndian
	data := minimalTIFF(order, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1920},
		{uint32(TagImageLength), uint32(TypeLong), 1, 1080},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("buildBigTIFFCameraEXIF: Parse: %v", err)
	}
	// Force BigTIFF provenance so Encode dispatches to serialiseBigTIFF
	// (task #264). This is a valid construction technique: EXIF.BigTIFF is
	// just a bool field, and Encode's behaviour depends only on it plus the
	// IFD trees below — not on how the struct was produced.
	e.BigTIFF = true

	mn := bytes.Repeat([]byte{'N'}, 32) // out-of-line MakerNote (32 > 8 BigTIFF inline threshold)
	e.ExifIFD = &IFD{Entries: []IFDEntry{
		{Tag: 0x9000, Type: TypeUndefined, Count: 4, Value: []byte("0232")},
		{Tag: TagMakerNote, Type: TypeUndefined, Count: uint32(len(mn)), Value: mn}, //nolint:gosec // G115: test data, fixed 32-byte length
	}}
	e.GPSIFD = &IFD{Entries: []IFDEntry{
		{Tag: TagGPSLatitudeRef, Type: TypeASCII, Count: 2, Value: []byte("N\x00")},
	}}
	e.InteropIFD = &IFD{Entries: []IFDEntry{
		{Tag: TagInteroperabilityIndex, Type: TypeASCII, Count: 4, Value: []byte("R98\x00")},
	}}
	return e
}

// ---------------------------------------------------------------------------
// RT — Round-trip fidelity (R-14)
// ---------------------------------------------------------------------------

// TestBigTIFFEncodeRoundTrip_RealFixtures is the primary R-14 gate: it reads
// the two committed real-world BigTIFF fixtures (produced by `tiffcp -8`,
// used throughout bigtiff_test.go), re-encodes them via exif.Encode, re-parses
// the result, and asserts that every IFD0 entry is (Tag, Type, Count, Value)
// identical to the original. Neither fixture is modified before Encode, so
// this is a pure round-trip fidelity check (R-14).
func TestBigTIFFEncodeRoundTrip_RealFixtures(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"testdata/BigTIFF_LE.tif", "testdata/BigTIFF_BE.tif"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !e.BigTIFF {
				t.Fatal("fixture is not BigTIFF-provenanced; test invariant violated")
			}

			encoded, err := Encode(e)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(encoded) < 4 || e.ByteOrder.Uint16(encoded[2:4]) != 0x002B {
				t.Fatalf("Encode: output does not carry BigTIFF magic 0x002B")
			}

			e2, err := Parse(encoded)
			if err != nil {
				t.Fatalf("Parse (round-trip): %v", err)
			}
			if !e2.BigTIFF {
				t.Error("Parse (round-trip): EXIF.BigTIFF = false, want true")
			}

			before := snapshotEntries(e.IFD0.Entries)
			after := snapshotEntries(e2.IFD0.Entries)
			if !entriesEqual(before, after) {
				t.Fatalf("R-14: IFD0 entries changed across round-trip:\nbefore=%d entries\nafter=%d entries", len(before), len(after))
			}
			for i := range before {
				if before[i].Tag != after[i].Tag {
					continue
				}
				if before[i].Type != after[i].Type || before[i].Count != after[i].Count {
					t.Errorf("R-14: tag 0x%04X: Type/Count changed: before={%d,%d} after={%d,%d}",
						before[i].Tag, before[i].Type, before[i].Count, after[i].Type, after[i].Count)
				}
				if !bytes.Equal(before[i].Value, after[i].Value) {
					t.Errorf("R-14: tag 0x%04X: Value changed across round-trip", before[i].Tag)
				}
			}
		})
	}
}

// TestBigTIFFEncodeRoundTrip_Corpus extends R-14 to the full BigTIFF-tagged
// subset of the shared TIFF corpus (testdata/corpus/tiff/**), covering
// diverse real-world encoders: LONG8 strip offsets, motorola (big-endian)
// byte order, tiled images, and multi-level SubIFD (0x014A) chains. Only
// files that Parse identifies as BigTIFF (magic 0x002B) are round-tripped;
// classic-TIFF corpus files are skipped (already covered by the classic-path
// conformance battery).
func TestBigTIFFEncodeRoundTrip_Corpus(t *testing.T) {
	t.Parallel()
	const corpusDir = "../testdata/corpus/tiff"
	paths := corpusFilesFromDir(t, corpusDir)

	tested := 0
	for _, path := range paths {
		data := mustReadFile(t, path)
		e, err := Parse(data)
		if err != nil || e == nil || !e.BigTIFF {
			continue // not a BigTIFF file (or unparseable); out of scope for this test
		}
		tested++
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mustNotPanic(t, "R-14 corpus round-trip "+path, func() {
				encoded, encErr := Encode(e)
				if encErr != nil {
					t.Fatalf("Encode: %v", encErr)
				}
				e2, reErr := Parse(encoded)
				if reErr != nil {
					t.Fatalf("Parse (round-trip): %v", reErr)
				}
				// Walk the full IFD0->IFD1->... chain, comparing entries level
				// by level (R-14, R-17: IFD1 verbatim-preserve contract applies
				// to BigTIFF the same as classic TIFF).
				a, b := e.IFD0, e2.IFD0
				level := 0
				for a != nil && b != nil {
					if !entriesEqual(snapshotEntries(a.Entries), snapshotEntries(b.Entries)) {
						t.Errorf("IFD level %d: entries differ after round-trip", level)
					}
					if !bytes.Equal(a.ThumbnailData, b.ThumbnailData) {
						t.Errorf("IFD level %d: ThumbnailData differs after round-trip", level)
					}
					a, b = a.Next, b.Next
					level++
				}
				if (a == nil) != (b == nil) {
					t.Errorf("IFD chain length differs after round-trip (level %d)", level)
				}
			})
		})
	}
	if tested == 0 {
		t.Skip("no BigTIFF files found in corpus; run 'make testdata'")
	}
}

// TestBigTIFFEncodeRoundTrip_SubIFDs verifies R-14/R-17 for a BigTIFF EXIF
// carrying a full sub-IFD tree: ExifIFD (with a MakerNote), GPSIFD, and
// InteropIFD. This exercises serialiseBigTIFF's pointer-patching path
// (computeIFDOffsetsBigTIFF + patchPointers), which the real-world fixtures
// in TestBigTIFFEncodeRoundTrip_RealFixtures do not exercise (they carry no
// ExifIFD/GPSIFD/InteropIFD).
func TestBigTIFFEncodeRoundTrip_SubIFDs(t *testing.T) {
	t.Parallel()
	e := buildBigTIFFCameraEXIF(t)

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := binary.LittleEndian.Uint16(encoded[2:4]); got != 0x002B {
		t.Fatalf("Encode: output magic = 0x%04X, want 0x002B", got)
	}

	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}

	if e2.ExifIFD == nil {
		t.Fatal("R-17: ExifIFD lost across BigTIFF round-trip")
	}
	if !entriesEqual(
		snapshotEntries([]IFDEntry{{Tag: 0x9000, Type: TypeUndefined, Count: 4, Value: []byte("0232")}}),
		snapshotEntries([]IFDEntry{*e2.ExifIFD.Get(0x9000)}),
	) {
		t.Error("R-14: ExifVersion (0x9000) entry changed across round-trip")
	}
	mn := e2.ExifIFD.Get(TagMakerNote)
	if mn == nil {
		t.Fatal("R-17: MakerNote lost across BigTIFF round-trip")
	}
	if !bytes.Equal(mn.Value, e.ExifIFD.Get(TagMakerNote).Value) {
		t.Error("R-17: MakerNote bytes changed across BigTIFF round-trip (verbatim-preserve contract violated)")
	}

	if e2.GPSIFD == nil {
		t.Fatal("R-14: GPSIFD lost across BigTIFF round-trip")
	}
	if latRef := e2.GPSIFD.Get(TagGPSLatitudeRef); latRef == nil || string(latRef.Value) != "N\x00" {
		t.Errorf("R-14: GPSLatitudeRef = %v, want \"N\\x00\"", latRef)
	}

	if e2.InteropIFD == nil {
		t.Fatal("R-14: InteropIFD lost across BigTIFF round-trip")
	}
	if idx := e2.InteropIFD.Get(TagInteroperabilityIndex); idx == nil || string(idx.Value) != "R98\x00" {
		t.Errorf("R-14: InteroperabilityIndex = %v, want \"R98\\x00\"", idx)
	}
}

// TestBigTIFFEncodeRoundTrip_Thumbnail verifies that an IFD1 JPEG thumbnail
// (Compression=6, JPEGInterchangeFormat, JPEGInterchangeFormatLength) survives
// a BigTIFF round-trip with its offset correctly repatched to the new BigTIFF
// layout (EXIF §4.5.5; task #264 §4: these two tags stay TypeLong).
func TestBigTIFFEncodeRoundTrip_Thumbnail(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	data := minimalTIFF(order, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 640},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e.BigTIFF = true

	fakeJPEG := []byte{0xFF, 0xD8, 0xFF, 0xD9, 0x00, 0x01, 0x02, 0x03}
	e.IFD0.Next = &IFD{
		Entries: []IFDEntry{
			{Tag: TagCompression, Type: TypeShort, Count: 1, Value: []byte{6, 0}},
			{Tag: TagJPEGInterchangeFormat, Type: TypeLong, Count: 1, Value: []byte{0, 0, 0, 0}},
			{Tag: TagJPEGInterchangeFormatLength, Type: TypeLong, Count: 1, Value: []byte{0, 0, 0, 0}},
		},
		ThumbnailData: fakeJPEG,
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	if e2.IFD0.Next == nil {
		t.Fatal("IFD1 lost across BigTIFF round-trip")
	}
	if !bytes.Equal(e2.IFD0.Next.ThumbnailData, fakeJPEG) {
		t.Errorf("ThumbnailData = %v, want %v", e2.IFD0.Next.ThumbnailData, fakeJPEG)
	}
}

// ---------------------------------------------------------------------------
// G — Guard tests (R-15, R-16)
// ---------------------------------------------------------------------------

// TestBigTIFFEncode_PointerOverflowGuard is the gate for R-16: when a sub-IFD
// pointer tag's target offset would exceed 32 bits, Encode must return
// ErrBigTIFFPointerOverflow rather than truncating the pointer.
//
// The oversized offset is manufactured purely arithmetically: IFD0 carries
// one entry whose declared Count (close to math.MaxUint32) multiplied by its
// element size pushes ifd0Size — and therefore the ExifIFDPointer target
// offset — past 4 GiB. Computing this size is pure uint64 arithmetic (see
// ifdTotalSizeBigTIFF); no multi-gigabyte buffer is ever allocated because
// the guard fires before writeTIFFHeaderBigTIFF's make() call.
func TestBigTIFFEncode_PointerOverflowGuard(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		BigTIFF:   true,
		IFD0: &IFD{Entries: []IFDEntry{
			// TypeDouble (8 bytes/element) x (2^32-1) elements =~ 34.4 GiB;
			// Value is deliberately short — the guard fires before any padding
			// or copying of this entry's value area would occur.
			{Tag: TagStripOffsets, Type: TypeDouble, Count: 0xFFFFFFFF, Value: []byte{}},
		}},
		ExifIFD: &IFD{Entries: []IFDEntry{
			{Tag: 0x9000, Type: TypeUndefined, Count: 4, Value: []byte("0232")},
		}},
	}

	_, err := Encode(e)
	if err == nil {
		t.Fatal("Encode: expected ErrBigTIFFPointerOverflow, got nil")
	}
	if !errors.Is(err, ErrBigTIFFPointerOverflow) {
		t.Errorf("Encode: error does not wrap ErrBigTIFFPointerOverflow: %v", err)
	}
}

// TestBigTIFFEncode_PointerOverflowGuard_NoFalsePositive verifies that the
// R-16 guard does NOT fire when the oversized IFD0 has no ExifIFD/GPSIFD/
// InteropIFD attached — there is no pointer field to truncate, so the correct
// failure mode is the aggregate size ceiling (R-15), tested separately in
// TestBigTIFFEncode_SizeCeilingGuard.
func TestBigTIFFEncode_PointerOverflowGuard_NoFalsePositive(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		BigTIFF:   true,
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagStripOffsets, Type: TypeDouble, Count: 0xFFFFFFFF, Value: []byte{}},
		}},
	}

	_, err := Encode(e)
	if errors.Is(err, ErrBigTIFFPointerOverflow) {
		t.Error("Encode: ErrBigTIFFPointerOverflow fired with no sub-IFD pointer present; false positive")
	}
}

// setMaxBigTIFFEncodeSizeForTest temporarily replaces the package-level
// maxBigTIFFEncodeSize with limit and registers a t.Cleanup to restore the
// production default, mirroring the iptc package's
// setMaxDatasetValueLenForTest helper (dataset_value_too_large_test.go). It
// must not be called from parallel sub-tests that share the package-level
// variable.
func setMaxBigTIFFEncodeSizeForTest(t *testing.T, limit uint64) {
	t.Helper()
	orig := maxBigTIFFEncodeSize
	maxBigTIFFEncodeSize = limit
	t.Cleanup(func() { maxBigTIFFEncodeSize = orig })
}

// TestBigTIFFEncode_SizeCeilingGuard is the gate for R-15: the aggregate
// encoded-size ceiling (maxBigTIFFEncodeSize) rejects a pathological
// caller-constructed IFDEntry.Count with ErrBigTIFFEncodeSizeExceeded rather
// than attempting an unbounded allocation. This sub-test reads only the
// package-level maxBigTIFFEncodeSize (does not mutate it), so it is safe to
// run in parallel.
func TestBigTIFFEncode_SizeCeilingGuard(t *testing.T) {
	t.Parallel()
	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		BigTIFF:   true,
		IFD0: &IFD{Entries: []IFDEntry{
			{Tag: TagStripOffsets, Type: TypeDouble, Count: 0xFFFFFFFF, Value: []byte{}},
		}},
	}
	_, err := Encode(e)
	if err == nil {
		t.Fatal("Encode: expected ErrBigTIFFEncodeSizeExceeded, got nil")
	}
	if !errors.Is(err, ErrBigTIFFEncodeSizeExceeded) {
		t.Errorf("Encode: error does not wrap ErrBigTIFFEncodeSizeExceeded: %v", err)
	}
}

// TestBigTIFFEncode_SizeCeilingGuard_LoweredCeiling is the R-15 companion
// test proving the ceiling is live and load-bearing (not just theoretically
// present): with maxBigTIFFEncodeSize temporarily lowered, even an ordinary,
// small BigTIFF EXIF trips ErrBigTIFFEncodeSizeExceeded.
//
//nolint:paralleltest // sets package-level maxBigTIFFEncodeSize; must not run in parallel with sibling tests in this file
func TestBigTIFFEncode_SizeCeilingGuard_LoweredCeiling(t *testing.T) {
	setMaxBigTIFFEncodeSizeForTest(t, 32) // bytes; smaller than even a bare 16-byte header + empty IFD0

	data := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1920},
		{uint32(TagImageLength), uint32(TypeLong), 1, 1080},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e.BigTIFF = true

	_, err = Encode(e)
	if !errors.Is(err, ErrBigTIFFEncodeSizeExceeded) {
		t.Errorf("Encode with lowered ceiling: error = %v, want ErrBigTIFFEncodeSizeExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// V — Value-level correctness (V-14, S-38)
// ---------------------------------------------------------------------------

// TestBigTIFFEncode_UnknownTypeRoundTrip is the gate for V-14: an IFD entry
// whose type code is not defined by TIFF 6.0/EXIF/BigTIFF (e.g. 250) is
// parsed as an opaque 8-byte raw field (parseIFDEntryBigTIFF, ifd.go) and must
// round-trip through Encode byte-for-byte as an inline 8-byte field.
func TestBigTIFFEncode_UnknownTypeRoundTrip(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	want := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x01, 0x02, 0x03, 0x04}
	data := buildBigTIFF(order, []bigTIFFEntry{
		{tag: uint16(TagImageWidth), typ: 250, count: 1, payload: want},
	})

	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	entry := e.IFD0.Get(TagImageWidth)
	if entry == nil {
		t.Fatal("unknown-type entry missing after Parse")
	}
	if !bytes.Equal(entry.Value, want) {
		t.Fatalf("Parse: unknown-type Value = %v, want %v", entry.Value, want)
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip): %v", err)
	}
	entry2 := e2.IFD0.Get(TagImageWidth)
	if entry2 == nil {
		t.Fatal("unknown-type entry lost across round-trip")
	}
	if !bytes.Equal(entry2.Value, want) {
		t.Errorf("V-14: unknown-type Value after round-trip = %v, want %v", entry2.Value, want)
	}
}

// TestBigTIFFEncode_SubIFDPointerStaysTypeLong is the gate for S-38: the
// ExifIFDPointer entry (0x8769) must be encoded with type field = TypeLong(4)
// — never promoted to TypeIFD8/TypeLong8 — and its 8-byte value-or-offset
// field must carry the 4-byte offset left-justified with the upper 4 bytes
// zero (BigTIFF spec §2; task #264 §4). This is checked by decoding the raw
// output bytes directly (not via exif.Parse, which would also accept an
// IFD8-typed pointer via readBigTIFFSubIFDOffset and could mask a regression).
func TestBigTIFFEncode_SubIFDPointerStaysTypeLong(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	data := minimalTIFF(order, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1920},
	})
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e.BigTIFF = true
	e.ExifIFD = &IFD{Entries: []IFDEntry{
		{Tag: 0x9000, Type: TypeUndefined, Count: 4, Value: []byte("0232")},
	}}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Manually walk the raw BigTIFF IFD0 to find the ExifIFDPointer entry.
	const entrySize = 20
	ifd0Off := order.Uint64(encoded[8:16])
	n := order.Uint64(encoded[ifd0Off:])
	found := false
	for i := range n {
		p := ifd0Off + 8 + i*entrySize
		tag := order.Uint16(encoded[p:])
		if TagID(tag) != TagExifIFDPointer {
			continue
		}
		found = true
		typ := order.Uint16(encoded[p+2:])
		if DataType(typ) != TypeLong {
			t.Errorf("S-38: ExifIFDPointer type = %d, want TypeLong (%d)", typ, TypeLong)
		}
		// Upper 4 bytes of the 8-byte value-or-offset field must be zero
		// (left-justified 4-byte LONG value, BigTIFF spec §2).
		upper := encoded[p+16 : p+20]
		for _, b := range upper {
			if b != 0 {
				t.Errorf("S-38: ExifIFDPointer value field upper 4 bytes not zero: %v", upper)
				break
			}
		}
		// Lower 4 bytes must decode to a plausible in-stream offset (non-zero,
		// within the encoded buffer).
		off := order.Uint32(encoded[p+12:])
		if off == 0 || uint64(off) >= uint64(len(encoded)) {
			t.Errorf("S-38: ExifIFDPointer offset = %d, out of range for %d-byte buffer", off, len(encoded))
		}
	}
	if !found {
		t.Fatal("S-38: ExifIFDPointer entry not found in encoded IFD0")
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

// BenchmarkEXIFEncode_BigTIFF measures the BigTIFF Encode path using the
// committed real-world fixture, for comparison against BenchmarkEXIFEncode
// (classic-TIFF) and BenchmarkEXIFEncode_Camera.
func BenchmarkEXIFEncode_BigTIFF(b *testing.B) {
	data, err := os.ReadFile("testdata/BigTIFF_LE.tif")
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	e, err := Parse(data)
	if err != nil {
		b.Fatalf("Parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Encode(e)
	}
}
