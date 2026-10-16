package exif

// thumbnail_strips_test.go — task #368, rule ID S-50 (docs/conformance/
// exif-tiff.md): the strips of an uncompressed thumbnail in the 1st IFD are
// carried by Encode, with StripOffsets rewritten to their new positions.
// Exif 3.1 §4.5.8, pp. 25-26; §4.6.5.2.1 and §4.6.5.2.3, pp. 39-40.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// stripBlob returns a strip of n bytes whose content depends on i, so that a
// strip written at the wrong place is noticed.
func stripBlob(i, n int) []byte {
	return bytes.Repeat([]byte{byte(0x30 + i)}, n) //nolint:gosec // G115: tiny test values
}

// rawStripStream builds a classic TIFF stream: IFD0 (one entry) and IFD1 with
// the entries of an uncompressed thumbnail whose strips are stored at the
// given file positions after the IFDs, in the order of offs (which may differ
// from the strip order). offType and cntType are the types (SHORT or LONG) of
// StripOffsets and StripByteCounts. The strip bytes are filled by stripBlob.
func rawStripStream(order binary.ByteOrder, offType, cntType DataType, lens []int, gaps []int, reverse bool) (stream []byte, want [][]byte) {
	n := len(lens)
	put := func(dst []byte, typ DataType, v uint32) {
		if typ == TypeShort {
			order.PutUint16(dst, uint16(v)) //nolint:gosec // G115: tiny test values
			return
		}
		order.PutUint32(dst, v)
	}
	size := func(typ DataType) int {
		if typ == TypeShort {
			return 2
		}
		return 4
	}
	// Layout: header 8, IFD0 (2+12+4=18), IFD1 (2+12*4+4=54), then the offset
	// array, the count array, then the strips.
	ifd0 := 8
	ifd1 := ifd0 + 18
	if ifd1&1 == 1 {
		ifd1++
	}
	// An array of at most 4 bytes is stored in the entry itself (TIFF 6.0 §2).
	ool := func(typ DataType) int {
		if n*size(typ) <= 4 {
			return 0
		}
		return n * size(typ)
	}
	offArr := ifd1 + 2 + 12*4 + 4
	cntArr := offArr + ool(offType)
	data := cntArr + ool(cntType)
	pos := make([]int, n)
	p := data
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	if reverse {
		for i := range idx {
			idx[i] = n - 1 - i
		}
	}
	for _, i := range idx {
		p += gaps[i%len(gaps)]
		pos[i] = p
		p += lens[i]
	}
	stream = make([]byte, p)
	if order == binary.LittleEndian {
		copy(stream, "II")
	} else {
		copy(stream, "MM")
	}
	order.PutUint16(stream[2:], 42)
	order.PutUint32(stream[4:], uint32(ifd0))
	// IFD0: ImageDescription "abc\0" inline; next = IFD1.
	order.PutUint16(stream[ifd0:], 1)
	e := stream[ifd0+2:]
	order.PutUint16(e[0:], uint16(TagImageDescription))
	order.PutUint16(e[2:], uint16(TypeASCII))
	order.PutUint32(e[4:], 4)
	copy(e[8:], "abc\x00")
	order.PutUint32(stream[ifd0+2+12:], uint32(ifd1)) //nolint:gosec // G115: tiny test values
	// IFD1: Compression, StripOffsets, SamplesPerPixel, StripByteCounts.
	order.PutUint16(stream[ifd1:], 4)
	ent := func(i int, tag TagID, typ DataType, count uint32) []byte {
		b := stream[ifd1+2+12*i:]
		order.PutUint16(b[0:], uint16(tag))
		order.PutUint16(b[2:], uint16(typ))
		order.PutUint32(b[4:], count)
		return b[8:12]
	}
	put(ent(0, TagCompression, TypeShort, 1), TypeShort, 1)
	offField := ent(1, TagStripOffsets, offType, uint32(n)) //nolint:gosec // G115: tiny test values
	put(ent(2, TagSamplesPerPixel, TypeShort, 1), TypeShort, 3)
	cntField := ent(3, TagStripByteCounts, cntType, uint32(n)) //nolint:gosec // G115: tiny test values
	offDst, cntDst := offField, cntField
	if ool(offType) != 0 {
		order.PutUint32(offField, uint32(offArr)) //nolint:gosec // G115: tiny test values
		offDst = stream[offArr:]
	}
	if ool(cntType) != 0 {
		order.PutUint32(cntField, uint32(cntArr)) //nolint:gosec // G115: tiny test values
		cntDst = stream[cntArr:]
	}
	for i := range n {
		put(offDst[i*size(offType):], offType, uint32(pos[i]))  //nolint:gosec // G115: tiny test values
		put(cntDst[i*size(cntType):], cntType, uint32(lens[i])) //nolint:gosec // G115,G602: i < n = len(lens), tiny test values
		blob := stripBlob(i, lens[i])                           //nolint:gosec // G602: i < n = len(lens)
		copy(stream[pos[i]:], blob)
		want = append(want, blob)
	}
	return stream, want
}

func stripsOfIFD1(e *EXIF) [][]byte {
	if e.IFD0 == nil || e.IFD0.Next == nil {
		return nil
	}
	return e.IFD0.Next.thumbnailStrips()
}

func equalStrips(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestThumbnailStrips_S50_ParseAndRoundTrip: the strips named by StripOffsets
// and StripByteCounts are read in strip order (whatever their position in the
// source), Encode writes them again, every StripOffsets value is even and
// addresses the strip, and a second parse reads the same bytes.
func TestThumbnailStrips_S50_ParseAndRoundTrip(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, tc := range []struct {
			name           string
			offT, cntT     DataType
			lens, gaps     []int
			reverse, alias bool
		}{
			{"one strip long", TypeLong, TypeLong, []int{100}, []int{0}, false, false},
			{"one odd strip short", TypeShort, TypeShort, []int{101}, []int{0}, false, false},
			{"several strips with gaps", TypeLong, TypeLong, []int{50, 51, 0, 7, 64}, []int{0, 3, 1}, false, false},
			{"strips stored in reverse order", TypeLong, TypeLong, []int{9, 10, 11}, []int{1}, true, false},
			{"offsets short counts long", TypeShort, TypeLong, []int{33, 34}, []int{0}, false, false},
			{"aliasing parse", TypeLong, TypeLong, []int{21, 22, 23}, []int{0}, false, true},
		} {
			t.Run(fmt.Sprintf("S-50/%s/%v", tc.name, order), func(t *testing.T) {
				t.Parallel()
				src, want := rawStripStream(order, tc.offT, tc.cntT, tc.lens, tc.gaps, tc.reverse)
				opts := []ParseOption{CarryThumbnailStrips()}
				if tc.alias {
					opts = append(opts, AliasThumbnail())
				}
				e, err := Parse(src, opts...)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if len(e.Warnings) != 0 {
					t.Fatalf("warnings %v", e.Warnings)
				}
				if got := stripsOfIFD1(e); !equalStrips(got, want) {
					t.Fatalf("strips read %d, want %d (contents differ)", len(got), len(want))
				}
				out, err := Encode(e)
				if err != nil {
					t.Fatalf("Encode: %v", err)
				}
				if n, serr := EncodedSize(e); serr != nil || n != len(out) {
					t.Fatalf("EncodedSize = %d, %v; len(Encode) = %d", n, serr, len(out))
				}
				e2, err := Parse(out, CarryThumbnailStrips())
				if err != nil {
					t.Fatalf("Parse(Encode): %v", err)
				}
				if len(e2.Warnings) != 0 {
					t.Fatalf("warnings after the round trip: %v", e2.Warnings)
				}
				if got := stripsOfIFD1(e2); !equalStrips(got, want) {
					t.Fatal("strips read after the round trip differ")
				}
				// Every offset is even and addresses the strip bytes.
				ifd1 := e2.IFD0.Next
				offE := ifd1.Get(TagStripOffsets)
				if offE.Type != TypeLong || int(offE.Count) != len(want) {
					t.Fatalf("StripOffsets type %v count %d, want LONG x %d", offE.Type, offE.Count, len(want))
				}
				for i := range want {
					off, _ := elementUint(offE, i)
					if off&1 == 1 {
						t.Errorf("strip %d at odd offset %d (TIFF 6.0 §2)", i, off)
					}
					if !bytes.Equal(out[off:off+uint64(len(want[i]))], want[i]) {
						t.Errorf("strip %d: bytes at its StripOffsets value differ", i)
					}
				}
			})
		}
	}
}

// TestThumbnailStrips_S50_BigTIFFAndSubIFDs: the same carry in a BigTIFF stream
// and in an IFD1 that also has an Exif IFD of its own, where the sub-IFD block
// follows the strips and the layout agrees with EncodedSize.
func TestThumbnailStrips_S50_BigTIFFAndSubIFDs(t *testing.T) {
	t.Parallel()
	strips := [][]byte{stripBlob(0, 31), stripBlob(1, 8), stripBlob(2, 5)}
	for _, big := range []bool{false, true} {
		t.Run(fmt.Sprintf("S-50/IFD1 with strips and an Exif IFD/bigtiff=%v", big), func(t *testing.T) {
			t.Parallel()
			sub := &SubIFDs{Exif: &IFD{Entries: []IFDEntry{chainEnt(TagColorSpace, TypeShort, 1, []byte{1, 0})}}, ThumbnailStrips: strips}
			ifd1 := &IFD{
				Entries: []IFDEntry{
					chainEnt(TagCompression, TypeShort, 1, []byte{1, 0}),
					chainEnt(TagStripOffsets, TypeShort, 3, []byte{0, 0, 0, 0, 0, 0}),
					chainEnt(TagStripByteCounts, TypeLong, 3, bytes.Join([][]byte{le32(31), le32(8), le32(5)}, nil)),
					chainEnt(TagExifIFDPointer, TypeLong, 1, le32(1)),
				},
				Sub: sub,
			}
			e := &EXIF{
				ByteOrder: binary.LittleEndian,
				BigTIFF:   big,
				IFD0:      &IFD{Entries: []IFDEntry{chainEnt(TagImageDescription, TypeASCII, 4, []byte("abc\x00"))}, Next: ifd1},
			}
			e2, out := reparse(t, e)
			_ = out
			if got := stripsOfIFD1(e2); got != nil {
				t.Fatal("strips read without CarryThumbnailStrips")
			}
			e3, err := Parse(out, CarryThumbnailStrips())
			if err != nil {
				t.Fatal(err)
			}
			if got := stripsOfIFD1(e3); !equalStrips(got, strips) {
				t.Fatal("strips differ after Encode")
			}
			if e3.IFD0.Next.Sub == nil || e3.IFD0.Next.Sub.Exif == nil || e3.IFD0.Next.Sub.Exif.Get(TagColorSpace) == nil {
				t.Fatal("the Exif IFD of IFD1 was lost")
			}
		})
	}
}

// TestThumbnailStrips_S50_NotCarried: a thumbnail that cannot be carried is
// reported, the IFD is left as parsed, and Encode still succeeds.
func TestThumbnailStrips_S50_NotCarried(t *testing.T) {
	t.Parallel()
	order := binary.LittleEndian
	base, _ := rawStripStream(order, TypeLong, TypeLong, []int{10, 10}, []int{0}, false)
	for _, tc := range []struct {
		name   string
		mutate func(b []byte, ifd1 int)
	}{
		{"a strip outside the stream", func(b []byte, ifd1 int) {
			// StripByteCounts array is at cntArr; make its first count huge.
			cnt := int(order.Uint32(b[ifd1+2+12*3+8:]))
			order.PutUint32(b[cnt:], uint32(len(b))) //nolint:gosec // G115: tiny test values
		}},
		{"counts and offsets differ in number", func(b []byte, ifd1 int) {
			order.PutUint32(b[ifd1+2+12*3+4:], 1)
		}},
		{"more bytes than the carry limit", func(b []byte, ifd1 int) {
			cnt := int(order.Uint32(b[ifd1+2+12*3+8:]))
			order.PutUint32(b[cnt:], maxThumbnailStripBytes+1)
		}},
		{"StripByteCounts missing", func(b []byte, ifd1 int) {
			order.PutUint16(b[ifd1+2+12*3:], uint16(TagTileByteCounts))
		}},
	} {
		t.Run("S-50/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := bytes.Clone(base)
			tc.mutate(src, 8+18)
			e, err := Parse(src, CarryThumbnailStrips())
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if stripsOfIFD1(e) != nil {
				t.Error("strips were carried")
			}
			found := false
			for _, w := range e.Warnings {
				if strings.Contains(w, "thumbnail image data not carried") {
					found = true
				}
			}
			if !found {
				t.Errorf("no warning for the thumbnail, warnings = %v", e.Warnings)
			}
			if _, err := Encode(e); err != nil {
				t.Errorf("Encode: %v", err)
			}
		})
	}
}

// TestThumbnailStrips_S50_JPEGThumbnailAndDefault: an IFD1 with a JPEG thumbnail
// is not touched by the option, and without the option strips are not read.
func TestThumbnailStrips_S50_JPEGThumbnailAndDefault(t *testing.T) {
	t.Parallel()
	t.Run("S-50/default parse reads no strips", func(t *testing.T) {
		t.Parallel()
		src, _ := rawStripStream(binary.LittleEndian, TypeLong, TypeLong, []int{10}, []int{0}, false)
		e, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if stripsOfIFD1(e) != nil || len(e.Warnings) != 0 {
			t.Errorf("strips %v warnings %v", stripsOfIFD1(e), e.Warnings)
		}
	})
	t.Run("S-50/JPEG thumbnail", func(t *testing.T) {
		t.Parallel()
		e := chainEXIF(0, 0, nil, false)
		e2, out := reparse(t, e)
		_ = e2
		e3, err := Parse(out, CarryThumbnailStrips())
		if err != nil {
			t.Fatal(err)
		}
		if stripsOfIFD1(e3) != nil || !bytes.Equal(e3.IFD0.Next.ThumbnailData, thumbnailJPEG) {
			t.Error("the JPEG thumbnail was not kept, or strips appeared")
		}
	})
}

func BenchmarkThumbnailStripsEncode(b *testing.B) {
	src, _ := rawStripStream(binary.BigEndian, TypeLong, TypeLong, []int{20000, 20000, 11948}, []int{0}, false)
	e, err := Parse(src, CarryThumbnailStrips(), AliasThumbnail())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		if _, err := Encode(e); err != nil {
			b.Fatal(err)
		}
	}
}
