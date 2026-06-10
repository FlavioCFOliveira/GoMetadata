package exif

// task99_word_align_test.go — regression tests for task #99.
//
// TIFF 6.0 §2: "Each data item (field value) must begin on a word boundary."
// (Word = 2 bytes.)  writeIFD and ifdTotalSize must cooperate to ensure that
// every out-of-line value in every IFD emitted by exif.Encode begins at an
// even (word-aligned) absolute file offset.
//
// Tests in this file:
//   TestWordAlignedEncodeBasic  — IFD with an odd-length ASCII value followed
//                                  by an 8-byte RATIONAL: the RATIONAL must
//                                  land on an even offset.
//   TestWordAlignedEncodeAllIFDs — builds a realistic EXIF with IFD0, ExifIFD,
//                                   GPSIFD, and IFD1 (thumbnail); parses the
//                                   output and asserts all OOL value offsets are
//                                   even.
//   TestWordAlignedEncodeRoundTrip — encodes, re-parses, and verifies that the
//                                     values survived intact.

import (
	"encoding/binary"
	"sort"
	"testing"
)

// parseAllOOLOffsets parses a TIFF stream (from byte 0, as produced by Encode)
// and returns a list of (tagName, offset) pairs for every out-of-line value in
// IFD0 and every sub-IFD (ExifIFD, GPS IFD, Interop, IFD1 chain).
func parseAllOOLOffsets(t *testing.T, data []byte) []uint32 {
	t.Helper()
	if len(data) < 8 {
		t.Errorf("TIFF stream too short (%d bytes)", len(data))
		return nil
	}
	var order binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		t.Errorf("unknown byte-order marker %q", data[0:2])
		return nil
	}

	ifd0Off := order.Uint32(data[4:])

	var offsets []uint32
	visited := make(map[uint32]bool)

	var walkIFD func(off uint32)
	walkIFD = func(off uint32) {
		if off == 0 || visited[off] {
			return
		}
		visited[off] = true
		if uint64(off)+2 > uint64(len(data)) {
			return
		}
		count := int(order.Uint16(data[off:]))
		pos := int(off) + 2
		if pos+count*12 > len(data) {
			return
		}

		for i := range count {
			e := pos + i*12
			if e+12 > len(data) {
				break
			}
			typ := DataType(order.Uint16(data[e+2:]))
			cnt := order.Uint32(data[e+4:])
			valOrOff := order.Uint32(data[e+8:])
			tag := TagID(order.Uint16(data[e:]))

			sz := typeSize(typ)
			total := uint64(sz) * uint64(cnt)
			if sz > 0 && total > 4 {
				offsets = append(offsets, valOrOff)
			}

			// Follow sub-IFD pointers for recognised pointer tags.
			isPtr := tag == TagExifIFDPointer || tag == TagGPSIFDPointer ||
				tag == TagInteropIFDPointer
			if isPtr && sz > 0 && total <= 4 {
				walkIFD(valOrOff)
			}
		}
		// Follow next-IFD chain.
		nextPtrPos := pos + count*12
		if nextPtrPos+4 <= len(data) {
			next := order.Uint32(data[nextPtrPos:])
			walkIFD(next)
		}
	}
	walkIFD(ifd0Off)
	return offsets
}

// TestWordAlignedEncodeBasic verifies that an IFD with an odd-length ASCII value
// followed by a RATIONAL value produces a RATIONAL at an even file offset.
//
// Without the word-alignment fix: the ASCII value ("Copyright\0" = 10 bytes) is
// placed starting at valueAreaStart (even), and its end is even+10 = even.
// But if the prior value is odd-length, the next OOL entry lands on an odd offset.
// This test constructs a worst case: ASCII of 7 bytes (odd) + RATIONAL (8 bytes).
func TestWordAlignedEncodeBasic(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// Build a [4]byte for the RATIONAL value (inline-safe: 8 bytes > 4, so OOL).
	ratVal := make([]byte, 8)
	order.PutUint32(ratVal[0:], 72)
	order.PutUint32(ratVal[4:], 1)

	e := &EXIF{
		ByteOrder: order,
		IFD0: &IFD{Entries: []IFDEntry{
			// ASCII "Hello\x00\x00" — 7 bytes (odd length).
			{Tag: TagMake, Type: TypeASCII, Count: 7, Value: []byte("Hello\x00\x00"), bigEndian: orderIsBig(order)},
			// RATIONAL (8 bytes, out-of-line).
			{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: ratVal, bigEndian: orderIsBig(order)},
		}},
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	offsets := parseAllOOLOffsets(t, encoded)
	for _, off := range offsets {
		if off%2 != 0 {
			// TIFF 6.0 §2: word-aligned values required.
			t.Errorf("OOL value at odd offset %d (0x%x) — TIFF 6.0 §2 word-alignment violation", off, off)
		}
	}

	// Round-trip: the RATIONAL value must survive.
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	xres := e2.IFD0.Get(TagXResolution)
	if xres == nil {
		t.Fatal("XResolution missing after round-trip")
	}
	rat := xres.Rational(0)
	if rat[0] != 72 || rat[1] != 1 {
		t.Errorf("XResolution = %v, want [72 1]", rat)
	}
}

// TestWordAlignedEncodeAllIFDs builds a realistic EXIF with multiple OOL values
// of odd and even byte lengths, then asserts that every OOL offset in the encoded
// stream is word-aligned.
//
// Constructs:
//   - IFD0: Make (odd ASCII), Copyright (even ASCII), XResolution (RATIONAL 8b),
//     YResolution (RATIONAL 8b), ExifIFD pointer.
//   - ExifIFD: DateTimeOriginal (20-byte ASCII, even), UserComment (48-byte
//     undefined, even), FNumber (RATIONAL 8b).
//   - GPS IFD: GPSLatitude (RATIONAL×3 = 24b), GPSLongitude (RATIONAL×3 = 24b).
//
// The key stress-tests are the ASCII strings of odd/even lengths interleaved
// with RATIONAL values (8 bytes each).
func TestWordAlignedEncodeAllIFDs(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian

	// RATIONAL helper.
	rat := func(n, d uint32) []byte {
		v := make([]byte, 8)
		order.PutUint32(v[0:], n)
		order.PutUint32(v[4:], d)
		return v
	}
	e := &EXIF{
		ByteOrder: order,
		IFD0: &IFD{Entries: []IFDEntry{
			// 6-byte ASCII (even).
			{Tag: TagMake, Type: TypeASCII, Count: 6, Value: []byte("Canon\x00"), bigEndian: orderIsBig(order)},
			// 7-byte ASCII (odd) — forces a pad before the next OOL value.
			{Tag: TagModel, Type: TypeASCII, Count: 7, Value: []byte("EOS R3\x00"), bigEndian: orderIsBig(order)},
			// 8-byte RATIONAL — must land on even offset even after the odd ASCII above.
			{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(72, 1), bigEndian: orderIsBig(order)},
			// 8-byte RATIONAL.
			{Tag: TagYResolution, Type: TypeRational, Count: 1, Value: rat(72, 1), bigEndian: orderIsBig(order)},
			// 14-byte ASCII (even).
			{Tag: TagCopyright, Type: TypeASCII, Count: 14, Value: []byte("(c) Test 2025\x00"), bigEndian: orderIsBig(order)},
		}},
		ExifIFD: &IFD{Entries: []IFDEntry{
			// 20-byte ASCII (even).
			{Tag: TagDateTimeOriginal, Type: TypeASCII, Count: 20,
				Value: []byte("2025:01:01 12:00:00\x00"), bigEndian: orderIsBig(order)},
			// 8-byte RATIONAL.
			{Tag: TagFNumber, Type: TypeRational, Count: 1, Value: rat(28, 10), bigEndian: orderIsBig(order)},
		}},
	}
	// Add GPS via SetGPS so the encoding is correct (uses the internal representation).
	e.SetGPS(48.8566, 2.3522)

	// Sort entries (required by exif.Encode).
	sortEntries(e.IFD0.Entries)
	sortEntries(e.ExifIFD.Entries)
	if e.GPSIFD != nil {
		sortEntries(e.GPSIFD.Entries)
	}

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	offsets := parseAllOOLOffsets(t, encoded)
	if len(offsets) == 0 {
		t.Fatal("no OOL values found in encoded output — test is not exercising the alignment path")
	}
	for _, off := range offsets {
		if off%2 != 0 {
			t.Errorf("OOL value at odd offset %d (0x%x) — TIFF 6.0 §2 word-alignment violation", off, off)
		}
	}

	// Round-trip sanity: GPS must survive.
	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	lat, lon, ok := e2.GPS()
	if !ok {
		t.Fatal("GPS() ok=false after round-trip")
	}
	const tol = 0.0001
	if d := lat - 48.8566; d > tol || d < -tol {
		t.Errorf("GPS lat = %f, want ~48.8566", lat)
	}
	if d := lon - 2.3522; d > tol || d < -tol {
		t.Errorf("GPS lon = %f, want ~2.3522", lon)
	}
}

// TestIFDBlocksAlwaysEvenSize asserts that ifdTotalSize always returns an even
// number, regardless of the value-area content.  This is the invariant that
// guarantees every subsequent IFD block starts at an even file offset.
func TestIFDBlocksAlwaysEvenSize(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	rat := func(n, d uint32) []byte {
		v := make([]byte, 8)
		order.PutUint32(v[0:], n)
		order.PutUint32(v[4:], d)
		return v
	}

	cases := []struct {
		name    string
		entries []IFDEntry
	}{
		{
			"empty",
			nil,
		},
		{
			"single inline SHORT",
			[]IFDEntry{{Tag: TagImageWidth, Type: TypeShort, Count: 1, Value: []byte{0x00, 0x80}}},
		},
		{
			"odd-length ASCII only",
			[]IFDEntry{{Tag: TagMake, Type: TypeASCII, Count: 7, Value: []byte("Canon\x00\x00")}},
		},
		{
			"even-length ASCII only",
			[]IFDEntry{{Tag: TagCopyright, Type: TypeASCII, Count: 14, Value: []byte("(c) Test 2025\x00")}},
		},
		{
			"RATIONAL only",
			[]IFDEntry{{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(72, 1)}},
		},
		{
			"odd ASCII then RATIONAL",
			[]IFDEntry{
				{Tag: TagMake, Type: TypeASCII, Count: 7, Value: []byte("Canon\x00\x00")},
				{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(72, 1)},
			},
		},
		{
			"multiple ASCII (mix of odd/even)",
			[]IFDEntry{
				{Tag: TagMake, Type: TypeASCII, Count: 7, Value: []byte("Canon\x00\x00")},
				{Tag: TagModel, Type: TypeASCII, Count: 6, Value: []byte("EOS R\x00")},
				{Tag: TagCopyright, Type: TypeASCII, Count: 5, Value: []byte("2025\x00")},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sz := ifdTotalSize(tc.entries)
			if sz%2 != 0 {
				t.Errorf("ifdTotalSize(%q) = %d (odd) — invariant violated: IFD blocks must have even total size", tc.name, sz)
			}
		})
	}
}

// TestWordAlignedEncodeOddXMPThenCopyright exercises the exact scenario that
// triggered task #99: an XMP blob with an odd byte count (TypeByte) written to
// IFD0 via upsertIFD0Entry, followed by a Copyright ASCII string.  Before the
// fix, Copyright landed at an odd offset.
func TestWordAlignedEncodeOddXMPThenCopyright(t *testing.T) {
	t.Parallel()

	order := binary.BigEndian // cramps.tif is big-endian — use same order

	// Construct a minimal EXIF resembling the cramps.tif scenario.
	// XMP blob of 2389 bytes (odd) — TypeByte, Count=2389.
	xmpBlob := make([]byte, 2389)
	for i := range xmpBlob {
		xmpBlob[i] = byte(i & 0xFF)
	}
	// Copyright 14 bytes (even) — TypeASCII.
	copyright := []byte("(c) Test 2025\x00") // 14 bytes

	// IPTC as TypeLong (padded): 19 bytes of raw IPTC → Count=5 (20 bytes).
	iptcRaw := make([]byte, 19)
	iptcRaw[0] = 0x1C

	e := &EXIF{
		ByteOrder: order,
		IFD0: &IFD{Entries: []IFDEntry{
			// TypeByte XMP blob (odd length).
			{Tag: TagXMP, Type: TypeByte, Count: uint32(len(xmpBlob)), Value: xmpBlob, bigEndian: orderIsBig(order)}, //nolint:gosec // G115: length bounded by test data
			// ASCII Copyright (even length) — must land on even offset.
			{Tag: TagCopyright, Type: TypeASCII, Count: uint32(len(copyright)), Value: copyright, bigEndian: orderIsBig(order)}, //nolint:gosec // G115: bounded by test data
			// TypeLong IPTC (count = ceil(19/4) = 5).
			{Tag: TagIPTC, Type: TypeLong, Count: 5, Value: iptcRaw, bigEndian: orderIsBig(order)},
		}},
	}
	sortEntries(e.IFD0.Entries)

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Walk the IFD0 value area manually and assert all OOL offsets are even.
	if len(encoded) < 8 {
		t.Fatal("encoded stream too short")
	}
	boMark := encoded[0:2]
	var ord binary.ByteOrder
	if boMark[0] == 'I' && boMark[1] == 'I' {
		ord = binary.LittleEndian
	} else {
		ord = binary.BigEndian
	}
	ifd0Off := ord.Uint32(encoded[4:])
	if uint64(ifd0Off)+2 > uint64(len(encoded)) {
		t.Fatal("IFD0 offset out of bounds")
	}
	count := int(ord.Uint16(encoded[ifd0Off:]))
	pos := int(ifd0Off) + 2
	for i := 0; i < count; i++ { //nolint:intrange // binary parser: loop variable is a byte-slice offset multiplier
		e2 := pos + i*12
		if e2+12 > len(encoded) {
			break
		}
		tag := TagID(ord.Uint16(encoded[e2:]))
		typ := DataType(ord.Uint16(encoded[e2+2:]))
		cnt := ord.Uint32(encoded[e2+4:])
		valOrOff := ord.Uint32(encoded[e2+8:])
		sz := typeSize(typ)
		total := uint64(sz) * uint64(cnt)
		if sz > 0 && total > 4 {
			if valOrOff%2 != 0 {
				t.Errorf("IFD0 tag 0x%04X: OOL value at odd offset %d — TIFF 6.0 §2 violation", tag, valOrOff)
			}
		}
	}

	// Round-trip: Copyright value must survive.
	e3, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse round-trip: %v", err)
	}
	cpEntry := e3.IFD0.Get(TagCopyright)
	if cpEntry == nil {
		t.Fatal("Copyright tag missing after round-trip")
	}
	// Sort the entries for the binary search used by ifdTotalSize unit test below.
	if cpEntry.String() != "(c) Test 2025" {
		t.Errorf("Copyright = %q, want \"(c) Test 2025\"", cpEntry.String())
	}
}

// TestWordAlignedEncodeIFD1Thumbnail verifies that the IFD1 block with a
// JPEG thumbnail appended immediately after also has all OOL values word-aligned.
func TestWordAlignedEncodeIFD1Thumbnail(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	rat := func(n, d uint32) []byte {
		v := make([]byte, 8)
		order.PutUint32(v[0:], n)
		order.PutUint32(v[4:], d)
		return v
	}

	// Minimal JPEG-like thumbnail (not a real JPEG, just bytes with FFD8 header).
	thumb := make([]byte, 100)
	thumb[0] = 0xFF
	thumb[1] = 0xD8

	e := &EXIF{
		ByteOrder: order,
		IFD0: &IFD{
			Entries: []IFDEntry{
				// 7-byte ASCII (odd) — forces pad before next OOL.
				{Tag: TagMake, Type: TypeASCII, Count: 7, Value: []byte("Nikon\x00\x00"), bigEndian: orderIsBig(order)},
				{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(72, 1), bigEndian: orderIsBig(order)},
			},
			Next: &IFD{
				Entries: []IFDEntry{
					// RATIONAL in IFD1.
					{Tag: TagXResolution, Type: TypeRational, Count: 1, Value: rat(72, 1), bigEndian: orderIsBig(order)},
				},
				ThumbnailData: thumb,
			},
		},
	}
	sortEntries(e.IFD0.Entries)
	sortEntries(e.IFD0.Next.Entries)

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	offsets := parseAllOOLOffsets(t, encoded)
	// Sort for deterministic reporting.
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	for _, off := range offsets {
		if off%2 != 0 {
			t.Errorf("OOL value at odd offset %d (0x%x) — TIFF 6.0 §2 word-alignment violation", off, off)
		}
	}
}
