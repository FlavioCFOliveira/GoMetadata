package exif

// chain_test.go — task #319: the pointer tags of an IFD that follows IFD0 (the
// thumbnail IFD) are relocated or dropped, and the IFDs it addresses itself
// are carried. Exif 3.1 §4.6.9.2, Table 21, p. 99; rule IDs S-48 (docs/
// conformance/exif-tiff.md).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// chainEnt builds an IFD entry of the given little-endian stream.
func chainEnt(tag TagID, typ DataType, count uint32, v []byte) IFDEntry {
	return IFDEntry{Tag: tag, Type: typ, Count: count, Value: v}
}

func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// thumbnailJPEG is an odd-length stand-in for a thumbnail, so that the padding
// before the sub-IFDs that follow it is exercised.
var thumbnailJPEG = []byte{0xFF, 0xD8, 0xFF, 0xD9, 0x00} //nolint:gochecknoglobals // immutable test fixture

// chainEXIF returns an EXIF whose IFD1 holds a thumbnail and the pointers
// exifPtr and gpsPtr (source offsets, meaningless in the output stream); sub
// attaches the IFDs it addresses itself.
func chainEXIF(exifPtr, gpsPtr uint32, sub *SubIFDs, big bool) *EXIF {
	ifd1 := &IFD{
		Entries: []IFDEntry{
			chainEnt(TagCompression, TypeShort, 1, []byte{6, 0}),
			chainEnt(TagJPEGInterchangeFormat, TypeLong, 1, le32(0)),
			chainEnt(TagJPEGInterchangeFormatLength, TypeLong, 1, le32(uint32(len(thumbnailJPEG)))), //nolint:gosec // G115: tiny
		},
		ThumbnailData: thumbnailJPEG,
		Sub:           sub,
	}
	if exifPtr != 0 {
		ifd1.Entries = append(ifd1.Entries, chainEnt(TagExifIFDPointer, TypeLong, 1, le32(exifPtr)))
	}
	if gpsPtr != 0 {
		ifd1.Entries = append(ifd1.Entries, chainEnt(TagGPSIFDPointer, TypeLong, 1, le32(gpsPtr)))
	}
	sortEntries(ifd1.Entries)
	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		BigTIFF:   big,
		IFD0:      &IFD{Entries: []IFDEntry{chainEnt(TagImageDescription, TypeASCII, 4, []byte("abc\x00"))}, Next: ifd1},
	}
	return e
}

// reparse encodes e (EncodedSize must agree with the length) and parses the result.
func reparse(t *testing.T, e *EXIF) (*EXIF, []byte) {
	t.Helper()
	out, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if n, err := EncodedSize(e); err != nil || n != len(out) {
		t.Fatalf("EncodedSize = %d, %v; len(Encode) = %d", n, err, len(out))
	}
	e2, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(Encode): %v", err)
	}
	return e2, out
}

// TestChain_S48_OwnSubIFDsAreCarried: an IFD1 whose Exif and GPS pointers
// address IFDs of their own (the Olympus μ Digital 800 of the corpus) keeps
// them: the entries point at valid IFDs holding the same tags.
func TestChain_S48_OwnSubIFDsAreCarried(t *testing.T) {
	t.Parallel()
	for _, big := range []bool{false, true} {
		name := "classic"
		if big {
			name = "BigTIFF"
		}
		t.Run("S-48/own IFDs/"+name, func(t *testing.T) {
			t.Parallel()
			exifIFD := &IFD{Entries: []IFDEntry{
				chainEnt(TagExposureTime, TypeRational, 1, []byte{1, 0, 0, 0, 100, 0, 0, 0}),
				chainEnt(TagInteropIFDPointer, TypeLong, 1, le32(77)),
			}}
			interop := &IFD{Entries: []IFDEntry{chainEnt(0x0001, TypeASCII, 4, []byte("R98\x00"))}}
			gps := &IFD{Entries: []IFDEntry{chainEnt(TagGPSVersionID, TypeByte, 4, []byte{2, 4, 0, 0})}}
			e := chainEXIF(9999, 8888, &SubIFDs{Exif: exifIFD, GPS: gps, Interop: interop}, big)
			got, _ := reparse(t, e)
			ifd1 := got.IFD0.Next
			if ifd1 == nil || ifd1.Sub == nil || ifd1.Sub.Exif == nil || ifd1.Sub.GPS == nil || ifd1.Sub.Interop == nil {
				t.Fatalf("IFD1 sub-IFDs after the round trip: %+v", ifd1)
			}
			if v := ifd1.Sub.Exif.Get(TagExposureTime); v == nil || !bytes.Equal(v.Value, []byte{1, 0, 0, 0, 100, 0, 0, 0}) {
				t.Errorf("IFD1 Exif IFD ExposureTime = %v", v)
			}
			if v := ifd1.Sub.GPS.Get(TagGPSVersionID); v == nil || !bytes.Equal(v.Value, []byte{2, 4, 0, 0}) {
				t.Errorf("IFD1 GPS IFD GPSVersionID = %v", v)
			}
			if v := ifd1.Sub.Interop.Get(0x0001); v == nil || v.String() != "R98" {
				t.Errorf("IFD1 Interoperability IFD index = %v", v)
			}
			if !bytes.Equal(ifd1.ThumbnailData, thumbnailJPEG) {
				t.Errorf("thumbnail = %x, want %x", ifd1.ThumbnailData, thumbnailJPEG)
			}
			if got.ExifIFD != nil || got.GPSIFD != nil {
				t.Error("IFD0 gained an Exif or GPS IFD from the IFDs of IFD1")
			}
		})
	}
}

// TestChain_S48_SharedPointersAreRelocated: a pointer of IFD1 that holds the
// same offset as the IFD0 pointer addresses the same IFD; it is rewritten to
// the block written for IFD0, not left at the source offset.
func TestChain_S48_SharedPointersAreRelocated(t *testing.T) {
	t.Parallel()
	e := chainEXIF(300, 400, nil, false)
	e.IFD0.Entries = append(e.IFD0.Entries,
		chainEnt(TagExifIFDPointer, TypeLong, 1, le32(300)),
		chainEnt(TagGPSIFDPointer, TypeLong, 1, le32(400)))
	sortEntries(e.IFD0.Entries)
	e.ExifIFD = &IFD{Entries: []IFDEntry{chainEnt(TagExposureTime, TypeRational, 1, []byte{1, 0, 0, 0, 50, 0, 0, 0})}}
	e.GPSIFD = &IFD{Entries: []IFDEntry{chainEnt(TagGPSVersionID, TypeByte, 4, []byte{2, 4, 0, 0})}}
	got, _ := reparse(t, e)
	ifd1 := got.IFD0.Next
	for _, tag := range []TagID{TagExifIFDPointer, TagGPSIFDPointer} {
		p0, p1 := got.IFD0.Get(tag), ifd1.Get(tag)
		if p0 == nil || p1 == nil {
			t.Fatalf("tag %#04x: IFD0 entry %v, IFD1 entry %v", tag, p0, p1)
		}
		if !bytes.Equal(p0.Value, p1.Value) {
			t.Errorf("tag %#04x: IFD1 pointer %x differs from the IFD0 pointer %x", tag, p1.Value, p0.Value)
		}
	}
	if got.ExifIFD == nil || got.ExifIFD.Get(TagExposureTime) == nil || got.GPSIFD == nil {
		t.Error("the Exif and GPS IFDs of IFD0 were lost")
	}
	if ifd1.Sub != nil {
		t.Errorf("a shared pointer was attached as an IFD of its own: %+v", ifd1.Sub)
	}
}

// TestChain_S48_StalePointersAreDropped: a pointer of IFD1 that addresses no
// IFD that is written (no IFD0 IFD of the same offset, nothing attached), and
// an Interoperability pointer, are dropped rather than left stale.
func TestChain_S48_StalePointersAreDropped(t *testing.T) {
	t.Parallel()
	e := chainEXIF(300, 400, nil, false)
	e.IFD0.Next.Entries = append(e.IFD0.Next.Entries, chainEnt(TagInteropIFDPointer, TypeLong, 1, le32(500)))
	sortEntries(e.IFD0.Next.Entries)
	got, out := reparse(t, e)
	ifd1 := got.IFD0.Next
	for _, tag := range []TagID{TagExifIFDPointer, TagGPSIFDPointer, TagInteropIFDPointer} {
		if v := ifd1.Get(tag); v != nil {
			t.Errorf("tag %#04x kept in IFD1 with value %x", tag, v.Value)
		}
	}
	if !bytes.Equal(ifd1.ThumbnailData, thumbnailJPEG) || ifd1.Get(TagCompression) == nil {
		t.Error("the thumbnail IFD lost its own tags")
	}
	// Every pointer that remains lies inside the stream and on a word boundary.
	for _, ifd := range []*IFD{got.IFD0, ifd1} {
		for _, ent := range ifd.Entries {
			if isIFDPointerTag(ent.Tag) {
				if off := binary.LittleEndian.Uint32(ent.Value); int(off) >= len(out) || off%2 == 1 {
					t.Errorf("tag %#04x points at %d in a stream of %d bytes", ent.Tag, off, len(out))
				}
			}
		}
	}
}

// TestChain_S48_ParseAttachesOnlyDistinctIFDs: Parse attaches the IFDs of IFD1
// that differ from IFD0's, and not those that are the same.
func TestChain_S48_ParseAttachesOnlyDistinctIFDs(t *testing.T) {
	t.Parallel()
	own := &SubIFDs{
		Exif: &IFD{Entries: []IFDEntry{chainEnt(TagExposureTime, TypeRational, 1, []byte{1, 0, 0, 0, 9, 0, 0, 0})}},
	}
	e := chainEXIF(1, 0, own, false)
	e.ExifIFD = &IFD{Entries: []IFDEntry{chainEnt(TagFNumber, TypeRational, 1, []byte{2, 0, 0, 0, 1, 0, 0, 0})}}
	_, out := reparse(t, e)
	e2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if e2.ExifIFD == nil || e2.ExifIFD.Get(TagFNumber) == nil {
		t.Fatal("the Exif IFD of IFD0 was lost")
	}
	sub := e2.IFD0.Next.Sub
	if sub == nil || sub.Exif == nil || sub.Exif.Get(TagExposureTime) == nil || sub.Exif.Get(TagFNumber) != nil {
		t.Fatalf("IFD1 sub-IFD = %+v, want its own Exif IFD with ExposureTime only", sub)
	}
	if sub.GPS != nil {
		t.Error("a GPS IFD appeared in IFD1")
	}
}

// TestChain_S48_NoPointerNoAllocation: an IFD1 without pointer tags is
// measured and written without a copy of its entries (the common thumbnail
// IFD costs nothing extra).
func TestChain_S48_NoPointerNoAllocation(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics under t.Parallel
	// not parallel: AllocsPerRun
	e := chainEXIF(0, 0, nil, false)
	e.IFD0.Next.ThumbnailData = nil
	e.IFD0.Next.Entries = e.IFD0.Next.Entries[:1] // Compression only
	if n := testing.AllocsPerRun(50, func() { _ = chainLen(e, e.IFD0.Next, 100, false) }); n != 0 {
		t.Errorf("measuring a chained IFD without pointers allocates %.0f times per run, want 0", n)
	}
}
