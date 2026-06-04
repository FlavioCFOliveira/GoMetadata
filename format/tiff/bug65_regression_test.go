package tiff

// bug65_regression_test.go — regression test for task #65:
// upsertIFD0Entry could violate the sorted-unique tag invariant when inserting
// a tag (e.g. XMP = 0x02BC = 700) into an IFD0 that already contains
// higher-value tags (e.g. IPTC = 0x83BB, ExifIFDPointer = 0x8769).
//
// Root cause: upsertIFD0Entry appended the new entry at the slice end without
// re-sorting, breaking the binary-search invariant that filterEntries, hasEntry,
// and IFD.Get all depend on. The encode path then produced duplicate ExifIFDPointer
// entries: the original (with the correct patched offset) plus a stale placeholder.
//
// TIFF 6.0 §7: each tag in an IFD must appear at most once.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// buildTIFFWithExifIFD constructs a minimal little-endian TIFF whose IFD0
// contains four entries in sorted order:
//
//	ImageWidth     (0x0100 = 256)   TypeLong  Count=1  Value=640   inline
//	ImageLength    (0x0101 = 257)   TypeLong  Count=1  Value=480   inline
//	IPTC           (0x83BB = 33723) TypeUndef Count=20 out-of-line
//	ExifIFDPointer (0x8769 = 34665) TypeLong  Count=1  inline → ExifIFD offset
//
// ExifIFD contains one entry (ExifVersion 0x9000) so that exif.Parse populates
// e.ExifIFD. This is required for the bug to manifest: buildIFD0Entries only
// appends an ExifIFDPointer placeholder when e.ExifIFD != nil.
//
// Layout:
//
//	[0..7]   TIFF header
//	[8..9]   IFD0 entry count = 4
//	[10..57] four 12-byte IFD0 entries
//	[58..61] IFD0 next-IFD pointer (0)
//	[62..81] IPTC data (20 bytes)
//	[82..83] ExifIFD entry count = 1
//	[84..95] ExifIFD entry (ExifVersion)
//	[96..99] ExifIFD next-IFD pointer (0)
func buildTIFFWithExifIFD() []byte {
	order := binary.LittleEndian

	const (
		ifd0Off    = 8
		iptcOff    = 62
		iptcLen    = 20
		exifIFDOff = 82
	)

	buf := make([]byte, 100)

	// TIFF header.
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], ifd0Off)

	// IFD0: 4 entries (sorted by tag).
	order.PutUint16(buf[ifd0Off:], 4)

	p := ifd0Off + 2

	// Entry 0: ImageWidth 0x0100 TypeLong Count=1 Value=640 (inline).
	order.PutUint16(buf[p:], 0x0100)
	order.PutUint16(buf[p+2:], 4) // TypeLong
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 640)
	p += 12

	// Entry 1: ImageLength 0x0101 TypeLong Count=1 Value=480 (inline).
	order.PutUint16(buf[p:], 0x0101)
	order.PutUint16(buf[p+2:], 4)
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], 480)
	p += 12

	// Entry 2: IPTC 0x83BB TypeUndefined Count=20 offset=62 (out-of-line).
	order.PutUint16(buf[p:], 0x83BB)
	order.PutUint16(buf[p+2:], 7) // TypeUndefined
	order.PutUint32(buf[p+4:], iptcLen)
	order.PutUint32(buf[p+8:], iptcOff)
	p += 12

	// Entry 3: ExifIFDPointer 0x8769 TypeLong Count=1 Value=exifIFDOff (inline).
	order.PutUint16(buf[p:], 0x8769)
	order.PutUint16(buf[p+2:], 4)
	order.PutUint32(buf[p+4:], 1)
	order.PutUint32(buf[p+8:], exifIFDOff)
	p += 12

	// IFD0 next-IFD pointer = 0.
	order.PutUint32(buf[p:], 0)

	// IPTC data area.
	for i := range iptcLen {
		buf[iptcOff+i] = byte(i + 1)
	}

	// ExifIFD: 1 entry (ExifVersion 0x9000 TypeUndefined Count=4 "0230" inline).
	order.PutUint16(buf[exifIFDOff:], 1)
	q := exifIFDOff + 2
	order.PutUint16(buf[q:], 0x9000) // ExifVersion
	order.PutUint16(buf[q+2:], 7)    // TypeUndefined
	order.PutUint32(buf[q+4:], 4)
	copy(buf[q+8:], "0230")
	// ExifIFD next-IFD pointer = 0.
	order.PutUint32(buf[q+12:], 0)

	return buf
}

// TestInjectXMPIntoTIFFWithIPTCAndExifPtrNoTagDuplicate is the acceptance-criteria
// regression gate for task #65.
//
// It builds a TIFF with a sorted IFD0 = [ImageWidth, ImageLength, IPTC, ExifIFDPointer],
// calls Inject with a non-nil XMP payload, parses the result, and asserts:
//  1. Every tag ID in IFD0.Entries appears exactly once (no duplicates).
//  2. IFD0 entry count == 5 (original 4 + XMP tag 0x02BC).
//
// Before the fix: upsertIFD0Entry appended XMP at the end of the slice
// ([…IPTC=0x83BB, ExifIFDPointer=0x8769, XMP=0x02BC]) leaving IFD0 unsorted.
// filterEntries used binary search on the unsorted slice, incorrectly determined
// ExifIFDPointer was absent, and returned a copy that included it.
// buildIFD0Entries then also appended an ExifIFDPointer placeholder → two
// ExifIFDPointer entries in the output (the second with stale offset 0).
func TestInjectXMPIntoTIFFWithIPTCAndExifPtrNoTagDuplicate(t *testing.T) {
	t.Parallel()

	data := buildTIFFWithExifIFD()

	// Sanity: original data must parse cleanly and have ExifIFD populated.
	orig, err := exif.Parse(data)
	if err != nil {
		t.Fatalf("exif.Parse on original data: %v", err)
	}
	if orig.IFD0 == nil {
		t.Fatal("original IFD0 is nil")
	}
	if orig.ExifIFD == nil {
		t.Fatal("original ExifIFD is nil (test setup failure: ExifIFDPointer must be valid)")
	}
	if len(orig.IFD0.Entries) != 4 {
		t.Fatalf("original IFD0 entry count = %d; want 4 (test setup failure)", len(orig.IFD0.Entries))
	}

	// Inject a non-nil XMP payload. This triggers the upsertIFD0Entry path.
	rawXMP := []byte("<?xpacket begin='' id='x'?><xmpmeta/><?xpacket end='r'?>")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP); err != nil {
		t.Fatalf("Inject with XMP: %v", err)
	}

	// Parse the result.
	result, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("exif.Parse after Inject: %v", err)
	}
	if result.IFD0 == nil {
		t.Fatal("IFD0 is nil after Inject")
	}

	entries := result.IFD0.Entries

	// Assert 1: every tag ID appears exactly once (TIFF 6.0 §7).
	seen := make(map[exif.TagID]int)
	for _, e := range entries {
		seen[e.Tag]++
	}
	for tag, count := range seen {
		if count > 1 {
			// Pre-fix: ExifIFDPointer (0x8769) appears twice — once with the
			// correct patched offset, once as a stale placeholder with offset 0.
			t.Errorf("tag 0x%04X appears %d times in IFD0 (want exactly once)", tag, count)
		}
	}

	// Assert 2: IFD0 must have exactly 5 entries (original 4 + XMP).
	if len(entries) != 5 {
		t.Errorf("IFD0 entry count = %d; want 5 (original 4 + XMP tag 0x02BC)", len(entries))
		for i, e := range entries {
			t.Logf("  entries[%d]: tag=0x%04X type=%d count=%d", i, e.Tag, e.Type, e.Count)
		}
	}
}
