package exif

import (
	"encoding/binary"
	"math/rand/v2"
	"testing"
)

// orderIsBig returns true when order is binary.BigEndian.
// It accepts both concrete types (binary.LittleEndian, binary.BigEndian) and
// the binary.ByteOrder interface, so it can be used inline in IFDEntry struct
// literals without triggering the unconvert linter.
//
// task #199: replaces direct bigEndian field comparisons in test helpers.
func orderIsBig(order binary.ByteOrder) bool {
	return order == binary.BigEndian
}

// buildBenchIFD returns a pre-built IFD with n entries whose tags are
// consecutive uint16 values starting at 0x0100. Used by benchmarks that
// need a realistic sorted IFD.
func buildBenchIFD(n int) *IFD {
	ifd := &IFD{Entries: make([]IFDEntry, 0, n)}
	for i := range n {
		tag := TagID(0x0100 + i)
		var v [4]byte
		binary.LittleEndian.PutUint32(v[:], uint32(i))
		ifd.set(tag, TypeLong, 1, v[:], false) // LE fixture
	}
	return ifd
}

// buildGPSIFD returns a minimal GPS IFD suitable for BenchmarkParseGPS.
func buildGPSIFD() *IFD {
	order := binary.LittleEndian
	ifd := &IFD{}

	// GPSLatitudeRef: "N\x00"
	ifd.set(TagGPSLatitudeRef, TypeASCII, 2, []byte("N\x00"), orderIsBig(order))
	// GPSLatitude: 51°30'26.496" N  (London approximate)
	latDMS := decimalToDMSBytes(51.507360, order)
	ifd.set(TagGPSLatitude, TypeRational, 3, latDMS[:], orderIsBig(order))
	// GPSLongitudeRef: "W\x00"
	ifd.set(TagGPSLongitudeRef, TypeASCII, 2, []byte("W\x00"), orderIsBig(order))
	// GPSLongitude: 0°07'39.60" W
	lonDMS := decimalToDMSBytes(-0.127700, order)
	ifd.set(TagGPSLongitude, TypeRational, 3, lonDMS[:], orderIsBig(order))

	return ifd
}

// BenchmarkIFDGet measures a binary-search lookup in a 20-entry IFD.
func BenchmarkIFDGet(b *testing.B) {
	b.ReportAllocs()
	ifd := buildBenchIFD(20)
	b.ResetTimer()
	for range b.N {
		_ = ifd.Get(TagID(0x0100 + 10))
	}
}

// BenchmarkIFDSet measures building an IFD by inserting N tags in random
// order. The insertion uses sort.Search + slices.Insert (O(n) per insert)
// instead of append + full sort (O(n log n) per insert).
func BenchmarkIFDSet(b *testing.B) {
	b.ReportAllocs()
	const n = 30
	// Build a fixed random permutation of tag IDs so every run exercises the
	// same worst-case insertion pattern.
	tags := make([]TagID, n)
	for i := range tags {
		tags[i] = TagID(0x0100 + i)
	}
	rand.Shuffle(len(tags), func(i, j int) { tags[i], tags[j] = tags[j], tags[i] })

	b.ResetTimer()
	for range b.N {
		ifd := &IFD{Entries: make([]IFDEntry, 0, n)}
		for _, tag := range tags {
			var v [4]byte
			binary.LittleEndian.PutUint32(v[:], uint32(tag))
			ifd.set(tag, TypeLong, 1, v[:], false) // LE fixture
		}
	}
}

// BenchmarkIFDEntryString measures String() on a TypeASCII entry that has
// trailing NUL bytes (the common real-world case for camera model strings).
func BenchmarkIFDEntryString(b *testing.B) {
	b.ReportAllocs()
	entry := IFDEntry{
		Tag:   TagModel,
		Type:  TypeASCII,
		Count: 20,
		Value: []byte("Canon EOS R5\x00\x00\x00\x00\x00\x00\x00\x00"),
	}
	b.ResetTimer()
	for range b.N {
		_ = entry.String()
	}
}

// BenchmarkParseGPS measures GPS coordinate extraction from a pre-built GPS IFD.
func BenchmarkParseGPS(b *testing.B) {
	b.ReportAllocs()
	gpsIFD := buildGPSIFD()
	b.ResetTimer()
	for range b.N {
		_, _, _ = parseGPS(gpsIFD)
	}
}

// BenchmarkMakerNoteDispatch measures the map lookup and parsing dispatch for
// a known camera make. Uses a Canon-style plain-IFD MakerNote (no prefix).
func BenchmarkMakerNoteDispatch(b *testing.B) {
	b.ReportAllocs()
	// Minimal valid TIFF IFD usable as a Canon MakerNote: 1 SHORT entry.
	mn := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{0x0001, uint32(TypeShort), 1, 3},
	})
	b.ResetTimer()
	for range b.N {
		_ = parseMakerNoteIFD(mn, "Canon", binary.LittleEndian)
	}
}
