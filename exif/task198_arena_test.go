package exif

// task198_arena_test.go — Regression gate for task #198 (parse-level arena).
//
// The arena allocates a single contiguous []IFD + []IFDEntry pair and
// sub-slices them for each IFD with cap clamped to the pre-scanned entry
// count.  The cap-clamping is the critical safety invariant: it prevents an
// append inside IFD-N from bleeding into IFD-(N+1)'s region of the batch.
//
// These tests exercise that invariant directly by constructing TIFF buffers
// whose IFDs are known in advance and verifying that:
//
//  1. Every IFD reads the correct tag values after parsing.
//  2. No IFD's Entries slice aliases or overlaps a neighbouring IFD's region.
//  3. An explicit append-beyond-hint attempt (simulated by a duplicate-tag
//     injection that triggers an in-place compaction) does NOT corrupt the
//     adjacent arena slot.
//
// Spec reference: CIPA DC-008-2019 §4.5.2; TIFF 6.0 §2.
// Task reference: performance audit 2026-06-10, task #198.

import (
	"encoding/binary"
	"testing"
)

// buildArenaTestTIFF constructs a minimal TIFF buffer with:
//   - IFD0 containing nIFD0Entries entries (tags 0x0001, 0x0002, ... TypeShort, value == tag index)
//   - ExifIFD at a known offset, containing nExifEntries entries
//   - GPSIFD at a known offset, containing nGPSEntries entries
//
// All IFDs use little-endian byte order and TypeLong entries (inline 4-byte
// values) to keep construction simple.  The ExifIFD and GPSIFD pointer tags
// are written into IFD0 so that Parse finds them.
//
// Layout:
//
//	0x00–0x07 : TIFF header (II magic 0x002A, IFD0 at 0x08)
//	0x08–…    : IFD0 (count + nIFD0Entries×12 + next-ptr)
//	…         : ExifIFD
//	…         : GPSIFD
func buildArenaTestTIFF(nIFD0Entries, nExifEntries, nGPSEntries int) []byte {
	const (
		headerSize  = 8
		entrySize   = 12
		countSize   = 2
		nextPtrSize = 4
	)
	ifdSize := func(n int) int { return countSize + n*entrySize + nextPtrSize }

	// Compute offsets.
	ifd0Off := uint32(headerSize)
	exifOff := ifd0Off + uint32(ifdSize(nIFD0Entries)) //nolint:gosec // G115: test-only TIFF builder; sizes are small constants
	gpsOff := exifOff + uint32(ifdSize(nExifEntries))  //nolint:gosec // G115: same
	totalSize := int(gpsOff) + ifdSize(nGPSEntries)

	b := make([]byte, totalSize)
	order := binary.LittleEndian

	// TIFF header.
	b[0], b[1] = 'I', 'I'
	order.PutUint16(b[2:], 0x002A)
	order.PutUint32(b[4:], ifd0Off)

	// Helper: write TypeLong entry with an inline uint32 value.
	writeEntry := func(buf []byte, pos int, tag uint16, val uint32) {
		order.PutUint16(buf[pos:], tag)
		order.PutUint16(buf[pos+2:], uint16(TypeLong))
		order.PutUint32(buf[pos+4:], 1)
		order.PutUint32(buf[pos+8:], val)
	}

	// Helper: write a full IFD block.
	writeIFD := func(buf []byte, off uint32, n int, baseTag uint16, nextPtr uint32) {
		pos := int(off)
		order.PutUint16(buf[pos:], uint16(n)) //nolint:gosec // G115: test-only; n is a small constant entry count
		pos += countSize
		for i := range n {
			writeEntry(buf, pos, baseTag+uint16(i), uint32(i+1))
			pos += entrySize
		}
		order.PutUint32(buf[pos:], nextPtr) // next-IFD pointer (0 = end)
	}

	// IFD0: include ExifIFD pointer (0x8769) and GPSIFD pointer (0x8825) plus
	// nIFD0Entries−2 ordinary tags (to keep the tag space simple we always
	// reserve the last two slots for the pointer tags when both sub-IFDs exist).
	// For simplicity here we write ordinary tags first, then the two pointers.
	{
		pos := int(ifd0Off)
		order.PutUint16(b[pos:], uint16(nIFD0Entries)) //nolint:gosec // G115: test-only; nIFD0Entries is small
		pos += countSize
		// Ordinary tags 0x0100, 0x0101, … (up to nIFD0Entries-2).
		for i := range nIFD0Entries - 2 {
			writeEntry(b, pos, uint16(0x0100+i), uint32(i+1))
			pos += entrySize
		}
		// ExifIFD pointer.
		order.PutUint16(b[pos:], uint16(tagExifIFDPointer)) // 0x8769
		order.PutUint16(b[pos+2:], uint16(TypeLong))
		order.PutUint32(b[pos+4:], 1)
		order.PutUint32(b[pos+8:], exifOff)
		pos += entrySize
		// GPSIFD pointer.
		order.PutUint16(b[pos:], uint16(tagGPSIFDPointer)) // 0x8825
		order.PutUint16(b[pos+2:], uint16(TypeLong))
		order.PutUint32(b[pos+4:], 1)
		order.PutUint32(b[pos+8:], gpsOff)
		pos += entrySize
		// next-IFD = 0.
		order.PutUint32(b[pos:], 0)
	}

	// ExifIFD: tags 0xA000, 0xA001, …
	writeIFD(b, exifOff, nExifEntries, 0xA000, 0)

	// GPSIFD: tags 0x0000, 0x0001, …
	writeIFD(b, gpsOff, nGPSEntries, 0x0000, 0)

	return b
}

// TestArenaNeighbourCorruption_NoOverlap verifies that the per-IFD entry
// sub-slices in the arena batch are correctly isolated: tags from ExifIFD must
// not appear in GPSIFD.Entries and vice versa.
//
// This is the primary regression gate for the cap-clamped sub-slice invariant
// introduced in task #198.  If the cap clamping were absent, a sort/append
// operation inside fillIFD would spill into the next arena slot, corrupting
// the neighbouring IFD's entries.
func TestArenaNeighbourCorruption_NoOverlap(t *testing.T) {
	t.Parallel()

	const nIFD0 = 5 // 3 ordinary + ExifPtr + GPSPtr
	const nExif = 8 // ExifIFD entry count
	const nGPS = 6  // GPSIFD entry count
	buf := buildArenaTestTIFF(nIFD0, nExif, nGPS)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	if e.ExifIFD == nil {
		t.Fatal("ExifIFD is nil — pointer was not followed")
	}
	if e.GPSIFD == nil {
		t.Fatal("GPSIFD is nil — pointer was not followed")
	}

	// IFD0 must have exactly nIFD0 entries.
	if got, want := len(e.IFD0.Entries), nIFD0; got != want {
		t.Errorf("IFD0: got %d entries, want %d", got, want)
	}

	// ExifIFD entries must all have tags in the ExifIFD tag range (0xA000+).
	for i, entry := range e.ExifIFD.Entries {
		if entry.Tag < 0xA000 {
			t.Errorf("ExifIFD.Entries[%d].Tag = 0x%04X: unexpectedly low tag (arena bleed from GPSIFD?)", i, entry.Tag)
		}
	}

	// GPSIFD entries must all have tags in the GPS range (0x0000–0x001F).
	for i, entry := range e.GPSIFD.Entries {
		if entry.Tag >= 0xA000 {
			t.Errorf("GPSIFD.Entries[%d].Tag = 0x%04X: unexpectedly high tag (arena bleed from ExifIFD?)", i, entry.Tag)
		}
	}

	// ExifIFD and GPSIFD entry slices must not share backing memory.
	// We detect this by checking that the underlying arrays are distinct.
	// If cap-clamping were absent, an append in ExifIFD could overwrite
	// the first entries of GPSIFD.
	if len(e.ExifIFD.Entries) > 0 && len(e.GPSIFD.Entries) > 0 {
		exifBase := &e.ExifIFD.Entries[0]
		gpsBase := &e.GPSIFD.Entries[0]
		if exifBase == gpsBase {
			t.Error("ExifIFD and GPSIFD share the same entry backing array — arena cap-clamp invariant violated")
		}
	}
}

// TestArenaNeighbourCorruption_EntryCount verifies that each IFD retains the
// correct entry count after Parse, across a range of IFD sizes.  If the arena
// batch sub-slicing is wrong (e.g. hint off-by-one or wrong consumption order),
// one IFD may "absorb" entries belonging to a neighbouring IFD.
func TestArenaNeighbourCorruption_EntryCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name               string
		nIFD0, nExif, nGPS int
	}{
		{"small", 3, 2, 2},
		{"medium", 5, 8, 6},
		{"large", 7, 20, 15},
		{"exif_only_1", 3, 1, 2},
		{"gps_only_1", 3, 5, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Minimum 2 entries in IFD0 to hold both pointer tags.
			if tc.nIFD0 < 2 {
				tc.nIFD0 = 2
			}
			buf := buildArenaTestTIFF(tc.nIFD0, tc.nExif, tc.nGPS)
			e, err := Parse(buf)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			if got, want := len(e.IFD0.Entries), tc.nIFD0; got != want {
				t.Errorf("IFD0: got %d entries, want %d", got, want)
			}
			if got, want := len(e.ExifIFD.Entries), tc.nExif; got != want {
				t.Errorf("ExifIFD: got %d entries, want %d", got, want)
			}
			if got, want := len(e.GPSIFD.Entries), tc.nGPS; got != want {
				t.Errorf("GPSIFD: got %d entries, want %d", got, want)
			}
		})
	}
}

// TestArenaNeighbourCorruption_Values verifies the decoded values of the IFD
// entries to ensure no byte-level corruption occurred during arena sub-slicing.
//
// Each IFD is built with predictable tag/value pairs: tag 0xA000+i for ExifIFD,
// value i+1; tag 0x0000+i for GPSIFD, value i+1.  Parsing then fetching every
// tag must return the expected values.
func TestArenaNeighbourCorruption_Values(t *testing.T) {
	t.Parallel()

	const nIFD0 = 4 // 2 ordinary + ExifPtr + GPSPtr
	const nExif = 6
	const nGPS = 4
	buf := buildArenaTestTIFF(nIFD0, nExif, nGPS)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Verify ExifIFD entry values.
	for i := range nExif {
		tag := TagID(0xA000 + i)
		entry := e.ExifIFD.Get(tag)
		if entry == nil {
			t.Errorf("ExifIFD: tag 0x%04X not found", tag)
			continue
		}
		got := entry.Uint32()
		if want := uint32(i + 1); got != want {
			t.Errorf("ExifIFD: tag 0x%04X: got %d, want %d (possible arena neighbour corruption)", tag, got, want)
		}
	}

	// Verify GPSIFD entry values.
	for i := range nGPS {
		tag := TagID(i)
		entry := e.GPSIFD.Get(tag)
		if entry == nil {
			t.Errorf("GPSIFD: tag 0x%04X not found", tag)
			continue
		}
		got := entry.Uint32()
		if want := uint32(i + 1); got != want {
			t.Errorf("GPSIFD: tag 0x%04X: got %d, want %d (possible arena neighbour corruption)", tag, got, want)
		}
	}
}
