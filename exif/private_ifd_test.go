package exif

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// kodakStream builds a classic little-endian TIFF stream with IFD0 (Make,
// KodakIFD pointer, Exif IFD pointer), an Exif IFD (ExifVersion and an
// Interoperability pointer to interop) and the private IFD of a Kodak image: a
// BYTE entry inline, a text of 10 bytes and a pair of LONGs stored after it.
// kodakAt returns the offset of the private IFD in the stream.
func kodakStream(interop func(ifd0, exifIFD uint32) uint32, kodakType uint16) (stream []byte, kodakAt uint32) {
	le := binary.LittleEndian
	const ifd0At, makeAt, exifAt, privAt = 8, 50, 56, 86
	text, pair := []byte("Kodak text"), []byte{9, 0, 0, 0, 7, 0, 0, 0}
	b := make([]byte, privAt+2+3*12+4+len(text)+len(pair))
	copy(b, "II*\x00")
	le.PutUint32(b[4:], ifd0At)
	put := func(at int, tag, typ uint16, cnt uint32, val []byte, off uint32) {
		le.PutUint16(b[at:], tag)
		le.PutUint16(b[at+2:], typ)
		le.PutUint32(b[at+4:], cnt)
		if val != nil {
			copy(b[at+8:], val)
		} else {
			le.PutUint32(b[at+8:], off)
		}
	}
	le.PutUint16(b[ifd0At:], 3)
	put(ifd0At+2, 0x010F, 2, 6, nil, makeAt)
	put(ifd0At+14, 0x8290, 4, 1, nil, privAt)
	put(ifd0At+26, 0x8769, 4, 1, nil, exifAt)
	copy(b[makeAt:], "Kodak\x00")
	le.PutUint16(b[exifAt:], 2)
	put(exifAt+2, 0x9000, 7, 4, []byte("0210"), 0)
	put(exifAt+14, 0xA005, 4, 1, nil, interop(ifd0At, exifAt))
	le.PutUint16(b[privAt:], 3)
	textAt := privAt + 2 + 3*12 + 4
	put(privAt+2, 0x0000, 1, 4, []byte{1, 0, 0, 0}, 0)
	put(privAt+14, 0x03F1, kodakType, uint32(len(text)), nil, uint32(textAt)) //nolint:gosec // G115: tiny test offsets
	put(privAt+26, 0x0CE7, 4, 2, nil, uint32(textAt+len(text)))               //nolint:gosec // G115: tiny test offsets
	copy(b[textAt:], text)
	copy(b[textAt+len(text):], pair)
	return b, privAt
}

func noInterop(uint32, uint32) uint32 { return 0 }

// privateIFDValues reads the entries of the IFD at offset off of the
// little-endian stream out (offsets count from the TIFF header).
func privateIFDValues(t *testing.T, out []byte, off uint32) map[uint16][]byte {
	t.Helper()
	got := testutil.DecodeMN(out, 0, int(off), binary.LittleEndian, true, 0)
	if got == nil {
		t.Fatalf("no IFD at offset %d of the %d-byte stream", off, len(out))
	}
	return got
}

// TestPrivateIFD_K1_KodakIFDIsCarried (task #362): the IFD that IFD0 tag 0x8290
// points to, and the values it addresses, are in the stream EncodeRebased
// writes, with the pointer set for their new position; Encode alone leaves the
// pointer stale.
func TestPrivateIFD_K1_KodakIFDIsCarried(t *testing.T) {
	t.Parallel()
	t.Run("R-28", func(t *testing.T) {
		t.Parallel()
		src, at := kodakStream(noInterop, 2)
		e, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		want := privateIFDValues(t, src, at)
		if string(want[0x03F1]) != "Kodak text" || len(want) != 3 {
			t.Fatalf("fixture: IFD values %v", want)
		}
		out, err := EncodeRebased(e, src)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatalf("Parse of the encoded stream: %v", err)
		}
		ptr := e2.IFD0.Get(tagKodakIFD)
		if ptr == nil {
			t.Fatal("the KodakIFD entry is gone")
		}
		newAt := binary.LittleEndian.Uint32(ptr.Value)
		if newAt == at {
			t.Fatal("the pointer was not moved: the test proves nothing")
		}
		if got := privateIFDValues(t, out, newAt); !equalValues(got, want) {
			t.Errorf("KodakIFD values after the write = %v, want %v", got, want)
		}
		if newAt&1 == 1 {
			t.Errorf("the carried IFD is at the odd offset %d (TIFF 6.0 §2)", newAt)
		}
		// Encode alone writes the entry as it was: stale (K-1 documents the reason
		// EncodeRebased exists).
		plain, err := Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		e3, _ := Parse(plain)
		if v := e3.IFD0.Get(tagKodakIFD); v == nil || binary.LittleEndian.Uint32(v.Value) != at {
			t.Errorf("Encode changed the KodakIFD entry: %v", v)
		}
	})
}

// equalValues reports whether two entry maps hold the same bytes.
func equalValues(a, b map[uint16][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !bytes.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

// TestPrivateIFD_K2_NotAnIFD: an entry that does not address a plausible IFD
// of the source, and a source that is not the stream of the EXIF, leave the
// stream as Encode writes it.
func TestPrivateIFD_K2_NotAnIFD(t *testing.T) {
	t.Parallel()
	good, _ := kodakStream(noInterop, 2)
	badType, _ := kodakStream(noInterop, 99)
	for _, tc := range []struct {
		name string
		src  []byte
		pass []byte // the src handed to EncodeRebased (nil: tc.src)
	}{
		{"unknown entry type", badType, nil},
		{"no source stream", good, []byte{}},
	} {
		t.Run("R-28/"+tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			src := tc.src
			if tc.pass != nil {
				src = nil
			}
			plain, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			out, err := EncodeRebased(e, src)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, plain) {
				t.Errorf("EncodeRebased appended %d bytes to what Encode writes", len(out)-len(plain))
			}
		})
	}
}

// TestPrivateIFD_K3_NextIFDPointerIsCleared: the next-IFD offset of the carried
// IFD addresses a chain that is not carried, so it is 0.
func TestPrivateIFD_K3_NextIFDPointerIsCleared(t *testing.T) {
	t.Parallel()
	t.Run("R-28", func(t *testing.T) {
		t.Parallel()
		src, at := kodakStream(noInterop, 2)
		binary.LittleEndian.PutUint32(src[at+2+3*12:], 0x1234) // a stale next-IFD offset
		e, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeRebased(e, src)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		newAt := binary.LittleEndian.Uint32(e2.IFD0.Get(tagKodakIFD).Value)
		if next := binary.LittleEndian.Uint32(out[newAt+2+3*12:]); next != 0 {
			t.Errorf("next-IFD offset = %d, want 0", next)
		}
	})
}

// TestPrivateIFD_K4_InteropLoop (task #362): an Interoperability pointer that
// addresses IFD0 or the Exif IFD is ignored, as ExifTool does; a write does not
// copy IFD0 as the Interoperability IFD and drops the pointer.
func TestPrivateIFD_K4_InteropLoop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		interop func(ifd0, exifIFD uint32) uint32
	}{
		{"IFD0", func(ifd0, _ uint32) uint32 { return ifd0 }},
		{"the Exif IFD", func(_, exifIFD uint32) uint32 { return exifIFD }},
	} {
		t.Run("R-29/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src, _ := kodakStream(tc.interop, 2)
			e, err := Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			if e.InteropIFD != nil {
				t.Fatalf("InteropIFD = %v, want nil for a pointer that loops", e.InteropIFD.Entries)
			}
			out, err := EncodeRebased(e, src)
			if err != nil {
				t.Fatal(err)
			}
			e2, err := Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			if e2.InteropIFD != nil || e2.ExifIFD.Get(TagInteropIFDPointer) != nil {
				t.Error("the written stream still has an Interoperability IFD or pointer")
			}
			if e2.ExifIFD.Get(TagExifVersion) == nil || e2.IFD0.Get(TagMake) == nil {
				t.Error("IFD0 or the Exif IFD lost an entry")
			}
		})
	}
}

// TestPrivateIFD_K5_Corpus: the Kodak ProBack of the corpus keeps its KodakIFD
// (TextualInfo, HostSoftware) through EncodeRebased, and has no Interoperability
// IFD (its pointer addresses IFD0).
func TestPrivateIFD_K5_Corpus(t *testing.T) {
	t.Parallel()
	t.Run("R-28", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join("..", "testdata", "corpus", "jpeg", "metadata-extractor", "Kodak ProBack.jpg")
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			t.Skipf("corpus file absent (run testdata/download.sh): %s", path)
		}
		if err != nil {
			t.Fatal(err)
		}
		src := b[bytes.Index(b, []byte("Exif\x00\x00"))+6:]
		be := binary.BigEndian
		e, err := Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if e.InteropIFD != nil {
			t.Error("InteropIFD of a pointer that loops to IFD0 must be nil")
		}
		out, err := EncodeRebased(e, src)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatal(err)
		}
		read := func(stream []byte, e *EXIF) map[uint16][]byte {
			off := be.Uint32(e.IFD0.Get(tagKodakIFD).Value)
			return testutil.DecodeMN(stream, 0, int(off), be, true, 0)
		}
		want, got := read(src, e), read(out, e2)
		if len(want) != 4 || !equalValues(got, want) {
			t.Errorf("KodakIFD after the write = %v, want %v", got, want)
		}
	})
}
