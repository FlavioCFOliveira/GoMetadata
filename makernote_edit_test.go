package gometadata

// makernote_edit_test.go — tasks #398, #399 and #407 at the level of the public
// API: delete, same-size and size-changing edits of MakerNote tags, written
// into every container the library edits, with everything else of the image
// checked to be as it was.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// mneCorpus returns a corpus file, or skips the test when the corpus is absent
// (docs/TESTING.md §2.1: corpus file absent).
func mneCorpus(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "corpus", filepath.FromSlash(rel)))
	if err != nil {
		t.Skipf("corpus file %s absent: %v", rel, err)
	}
	return data
}

// mneTags returns the MakerNote entries of m by tag ID: "type:count:value".
func mneTags(t *testing.T, m *Metadata) map[exif.TagID]string {
	t.Helper()
	if m.EXIF == nil || m.EXIF.MakerNoteIFD == nil {
		t.Fatal("no MakerNote IFD")
	}
	res := map[exif.TagID]string{}
	for _, e := range m.EXIF.MakerNoteIFD.Entries {
		res[e.Tag] += string(rune('0'+e.Type%64)) + ":" + string(rune('0'+e.Count%64)) + ":" + hex.EncodeToString(e.Value) + ";"
	}
	return res
}

// ---- image data of each container -----------------------------------------

// mneJPEGSegments splits a JPEG into its marker segments before the scan data
// ((marker, payload) as strings, EXIF APP1 left out) and returns the scan data
// from the first SOS marker on.
func mneJPEGSegments(t *testing.T, data []byte) (segs []string, scan []byte) {
	t.Helper()
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			t.Fatalf("JPEG: no marker at %d", i)
		}
		marker := data[i+1]
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if marker == 0xDA {
			return segs, data[i:]
		}
		seg := data[i : i+2+n]
		if marker != 0xE1 || !bytes.HasPrefix(seg[4:], []byte("Exif\x00\x00")) {
			segs = append(segs, string(seg))
		}
		i += 2 + n
	}
	t.Fatal("JPEG: no SOS")
	return nil, nil
}

// mnePNGChunks returns the chunks of a PNG other than the ones that carry
// metadata (eXIf, tEXt, zTXt, iTXt: a writer re-encodes an EXIF kept in a text
// chunk as eXIf, task #325), whole (length, type, data and CRC).
func mnePNGChunks(t *testing.T, data []byte) []string {
	t.Helper()
	var res []string
	for i := 8; i+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		end := i + 12 + n
		if end > len(data) {
			t.Fatal("PNG: chunk beyond the end")
		}
		switch string(data[i+4 : i+8]) {
		case "eXIf", "tEXt", "zTXt", "iTXt":
		default:
			res = append(res, string(data[i:end]))
		}
		i = end
	}
	return res
}

// mneRIFFChunks returns the chunks of a WebP other than EXIF, XMP and VP8X, whole
// (a writer corrects the flags of VP8X to the chunks the file holds, and writes
// the XMP chunk with the FourCC "XMP " where the source has "XMP\x00").
func mneRIFFChunks(t *testing.T, data []byte) []string {
	t.Helper()
	var res []string
	for i := 12; i+8 <= len(data); {
		n := int(binary.LittleEndian.Uint32(data[i+4:]))
		end := i + 8 + n + n&1
		if end > len(data) {
			end = len(data)
		}
		if tag := string(data[i : i+4]); tag != "EXIF" && tag != "VP8X" && !strings.HasPrefix(tag, "XMP") {
			res = append(res, string(data[i:end]))
		}
		i = end
	}
	return res
}

// mneTIFFBlocks returns the sorted SHA-256 of every block of image data of the
// classic TIFF-family file data: the strips and tiles of the IFD chain and of
// the SubIFDs, and the JPEGInterchangeFormat data (Exif 3.1 §4.6.5; TIFF 6.0 §3,
// §15). The blocks keep their bytes when a writer moves them, so the digests
// say whether the image data survived.
func mneTIFFBlocks(t *testing.T, data []byte) []string {
	t.Helper()
	if len(data) < 8 {
		t.Fatal("TIFF: too short")
	}
	var o binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		o = binary.BigEndian
	}
	var sums []string
	seen := map[uint32]bool{}
	u32s := func(ep int) []uint32 {
		typ, cnt := o.Uint16(data[ep+2:]), int(o.Uint32(data[ep+4:]))
		size := map[uint16]int{3: 2, 4: 4}[typ]
		if size == 0 || cnt == 0 || cnt > 1<<20 {
			return nil
		}
		at := ep + 8
		if size*cnt > 4 {
			at = int(o.Uint32(data[ep+8:]))
		}
		if at < 0 || at+size*cnt > len(data) {
			return nil
		}
		res := make([]uint32, cnt)
		for i := range res {
			if size == 2 {
				res[i] = uint32(o.Uint16(data[at+2*i:]))
			} else {
				res[i] = o.Uint32(data[at+4*i:])
			}
		}
		return res
	}
	var walk func(off uint32, depth int)
	walk = func(off uint32, depth int) {
		if off == 0 || int(off)+2 > len(data) || seen[off] || depth > 3 {
			return
		}
		seen[off] = true
		n := int(o.Uint16(data[off:]))
		var offs, lens, subs []uint32
		var jpegOff, jpegLen uint32
		for i := range n {
			ep := int(off) + 2 + 12*i
			if ep+12 > len(data) {
				return
			}
			switch o.Uint16(data[ep:]) {
			case 0x0111, 0x0144:
				offs = u32s(ep)
			case 0x0117, 0x0145:
				lens = u32s(ep)
			case 0x014A:
				subs = u32s(ep)
			case 0x0201:
				if v := u32s(ep); len(v) == 1 {
					jpegOff = v[0]
				}
			case 0x0202:
				if v := u32s(ep); len(v) == 1 {
					jpegLen = v[0]
				}
			}
		}
		add := func(start, n uint32) {
			if int(start) < len(data) && int(start)+int(n) <= len(data) && n > 0 {
				s := sha256.Sum256(data[start : start+n])
				sums = append(sums, hex.EncodeToString(s[:]))
			}
		}
		for i := range offs {
			if i < len(lens) {
				add(offs[i], lens[i])
			}
		}
		add(jpegOff, jpegLen)
		for _, s := range subs {
			walk(s, depth+1)
		}
		if next := int(off) + 2 + 12*n; next+4 <= len(data) {
			walk(o.Uint32(data[next:]), depth)
		}
	}
	walk(o.Uint32(data[4:]), 0)
	sort.Strings(sums)
	return sums
}

// mneUntouched checks that everything of the image other than its MakerNote is as
// it was, with the check each container allows (see the helpers above).
func mneUntouched(t *testing.T, f format.FormatID, in, out []byte) {
	t.Helper()
	switch f {
	case format.FormatJPEG:
		a, sa := mneJPEGSegments(t, in)
		b, sb := mneJPEGSegments(t, out)
		if !mneEqual(a, b) {
			t.Error("a JPEG segment other than the EXIF APP1 changed")
		}
		if !bytes.Equal(sa, sb) {
			t.Error("the JPEG scan data changed")
		}
	case format.FormatPNG:
		if !mneEqual(mnePNGChunks(t, in), mnePNGChunks(t, out)) {
			t.Error("a PNG chunk other than the metadata chunks changed")
		}
	case format.FormatWebP:
		if !mneEqual(mneRIFFChunks(t, in), mneRIFFChunks(t, out)) {
			t.Error("a WebP chunk other than EXIF, XMP and VP8X changed")
		}
	case format.FormatTIFF, format.FormatDNG, format.FormatCR2, format.FormatNEF, format.FormatARW, format.FormatORF:
		a, b := mneTIFFBlocks(t, in), mneTIFFBlocks(t, out)
		if len(a) == 0 {
			t.Fatal("the image has no image data blocks the helper finds")
		}
		if !mneEqual(a, b) {
			t.Errorf("the image data blocks changed: %d before, %d after", len(a), len(b))
		}
	}
}

func mneEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- the edits, container by container --------------------------------------

// mneCase is a corpus file with the tag the edits are made on.
type mneCase struct {
	path string
	tag  string // hexadecimal ID of an out-of-line tag
}

// mneCases lists, for each container, corpus files whose MakerNote
// the editor accepts, with the manufacturers the corpus has for it. Every case was
// checked against ExifTool 13.55 ("exiftool -a -G1 -s" of the file and of the
// edited copy: the other tags are as they were, no warning is added except the
// minor "Possibly incorrect maker notes offsets" that a gap in front of the
// first value causes) and Exiv2 0.28.9 ("exiv2 -pa": no error); the checks ran
// once and no test runs them.
var mneCases = []mneCase{ //nolint:gochecknoglobals // test table
	{"jpeg/metadata-extractor/Canon EOS 450D.jpg", "0x0096"},
	{"jpeg/exif-samples/11-tests.jpg", "0x0006"}, // Canon, base shifted by -54
	{"jpeg/metadata-extractor/Canon EOS REBEL T3i.jpg", "0x0096"},
	{"jpeg/metadata-extractor/Nikon D300.jpg", "0x001D"},
	{"jpeg/exif-samples/nikon-e950.jpg", "0x0002"},
	{"jpeg/exiftool/FujiFilm.jpg", "0x1000"},
	{"jpeg/exiftool/Olympus.jpg", "0x0207"},
	{"jpeg/exiftool/Sigma.jpg", "0x0002"},
	{"jpeg/exiftool/JVC.jpg", "0x0002"},
	{"jpeg/exif-samples/IMG_5250.jpeg", "0x0002"}, // Apple
	{"png/exiv2/exiv2-bug922.png", "0x0004"},
	{"webp/metadata-extractor/Issue 473 (Java).webp", "0x0002"},
	{"heif/exif-samples/IMG_5195.HEIC", "0x0002"},
	{"heif/metadata-extractor/Issue 649.avif", "0x0006"},
	{"tiff/metadata-extractor/Nikon D300.tif", "0x0004"},
	{"raw/metadata-extractor/Leica T (Typ 701).dng", "0x0303"},
	{"raw/metadata-extractor/Canon EOS 350D.CR2", "0x0006"},
	{"raw/metadata-extractor/Nikon 800E.nef", "0x0004"},
	{"raw/metadata-extractor/Sony ILCE-5000 (A5000).arw", "0x2003"},
	{"raw/metadata-extractor/Olympus E-M10.orf", "0x0100"},
}

// elemSize is the size in bytes of one element of each TIFF type (TIFF 6.0 §2).
var elemSize = map[exif.DataType]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8, 13: 4} //nolint:gochecknoglobals // test table

// mneValue returns the replacement of the tag value cur of an entry for the
// three edits: the same size, larger, and smaller.
func mneValue(text bool, size int, cur []byte, kind string) any {
	n := len(cur)
	if text {
		n-- // the NUL
	}
	switch kind {
	case "same":
		if text {
			return strings.Repeat("S", max(n, 1))
		}
		return bytes.Repeat([]byte{0x53}, len(cur))
	case "grow":
		if text {
			return strings.Repeat("G", n+25)
		}
		// 24 bytes are a whole number of elements of every type.
		return append(bytes.Clone(cur), bytes.Repeat([]byte{0x47}, 24)...)
	default: // "shrink"
		if text {
			return "ab"
		}
		return bytes.Repeat([]byte{0x54}, size)
	}
}

// TestMakerNoteEdit_R399_Containers: for each container and manufacturer the
// corpus has, a delete, a same-size, a growing and a shrinking edit are written,
// and the output read back holds every other MakerNote tag unchanged, the edit
// itself, and the image as it was (task #398, #399).
func TestMakerNoteEdit_R399_Containers(t *testing.T) {
	t.Parallel()
	for _, tc := range mneCases {
		for _, kind := range []string{"delete", "same", "grow", "shrink"} {
			t.Run(tc.path+"/"+kind, func(t *testing.T) {
				t.Parallel()
				data := mneCorpus(t, tc.path)
				m, err := Read(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("Read: %v", err)
				}
				before := mneTags(t, m)
				n, perr := strconv.ParseUint(tc.tag[2:], 16, 16)
				if perr != nil {
					t.Fatal(perr)
				}
				id := exif.TagID(n)
				var cur []byte
				text, size := false, 1
				for _, e := range m.EXIF.MakerNoteIFD.Entries {
					if e.Tag == id {
						cur, text, size = e.Value, e.Type == exif.TypeASCII, elemSize[e.Type]
					}
				}
				if cur == nil {
					t.Fatalf("tag %s not in the MakerNote", tc.tag)
				}
				if kind == "delete" {
					err = m.DeleteMakerNoteTag(tc.tag)
				} else {
					err = m.SetMakerNoteTag(tc.tag, mneValue(text, size, cur, kind))
				}
				if err != nil {
					t.Fatalf("%s: %v", kind, err)
				}
				var out bytes.Buffer
				if err := Write(bytes.NewReader(data), &out, m); err != nil {
					t.Fatalf("Write: %v", err)
				}
				m2, err := Read(bytes.NewReader(out.Bytes()))
				if err != nil {
					t.Fatalf("Read(output): %v", err)
				}
				after := mneTags(t, m2)
				for tag, v := range before {
					if tag == id {
						continue
					}
					if after[tag] != v {
						t.Errorf("tag %#04x changed: %q -> %q", uint16(tag), v, after[tag])
					}
				}
				for tag := range after {
					if _, ok := before[tag]; !ok {
						t.Errorf("tag %#04x appeared", uint16(tag))
					}
				}
				got, present := after[id]
				switch kind {
				case "delete":
					if present {
						t.Errorf("deleted tag %s is still in the output", tc.tag)
					}
				default:
					want := mneValue(text, size, cur, kind)
					raw, _ := want.([]byte)
					if s, ok := want.(string); ok {
						raw = append([]byte(s), 0)
					}
					if !strings.Contains(got, hex.EncodeToString(raw)) {
						t.Errorf("tag %s = %q, want the value %x", tc.tag, got, raw)
					}
				}
				mneUntouched(t, m.Format(), data, out.Bytes())
				// The input of a write is never modified by it.
				if m3, err := Read(bytes.NewReader(data)); err != nil || len(mneTags(t, m3)) != len(before) {
					t.Error("the source image reads differently after the write")
				}
			})
		}
	}
}

// TestMakerNoteEdit_R399_OnlyTheEXIFSegmentChanges (task #387): a JPEG whose
// MakerNote is edited keeps its IPTC and XMP segments byte for byte.
func TestMakerNoteEdit_R399_OnlyTheEXIFSegmentChanges(t *testing.T) {
	t.Parallel()
	data := mneCorpus(t, "jpeg/metadata-extractor/Canon EOS 450D.jpg")
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteMakerNoteTag("0x000C"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Write(bytes.NewReader(data), &out, m); err != nil {
		t.Fatal(err)
	}
	a, sa := mneJPEGSegments(t, data)
	b, sb := mneJPEGSegments(t, out.Bytes())
	if len(a) == 0 || !mneEqual(a, b) || !bytes.Equal(sa, sb) {
		t.Error("a segment other than the EXIF APP1, or the scan data, changed")
	}
	// An unedited write stays byte-identical.
	m2, _ := Read(bytes.NewReader(data))
	var same bytes.Buffer
	if err := Write(bytes.NewReader(data), &same, m2); err != nil || !bytes.Equal(same.Bytes(), data) {
		t.Error("a write without edits is not byte-identical")
	}
}

// TestMakerNoteEdit_R398_SerialNumbers: the serial number of Canon, Nikon and
// Sony is deleted and is gone from the output (ExifTool 13.55 reports Canon
// SerialNumber 0x000C and InternalSerialNumber 0x0096, Nikon SerialNumber 0x001D,
// and for Sony the InternalSerialNumber that lives in the encrypted block of tag
// 0x9400, captured once; no test runs it).
func TestMakerNoteEdit_R398_SerialNumbers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path, tag string
		secret    []byte // the bytes that must not be left in the file
	}{
		// Canon SerialNumber is a LONG (ExifTool prints it as 10 digits).
		{"jpeg/metadata-extractor/Canon EOS 450D.jpg", "CameraSerialNumber", binary.LittleEndian.AppendUint32(nil, 330103043)},
		{"jpeg/metadata-extractor/Canon EOS 450D.jpg", "0x0096", []byte("KFFFFFFF")},
		{"jpeg/metadata-extractor/Nikon D300.jpg", "0x001D", []byte("3064687")},
		{"jpeg/metadata-extractor/Nikon D7000.jpg", "0x001D", []byte("3003516")},
		{"jpeg/metadata-extractor/Canon EOS 5D Mark II.jpg", "0x000C", binary.LittleEndian.AppendUint32(nil, 230106520)},
	}
	for _, tc := range tests {
		t.Run(tc.path+"/"+tc.tag, func(t *testing.T) {
			t.Parallel()
			data := mneCorpus(t, tc.path)
			m, err := Read(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, tc.secret) {
				t.Fatalf("the fixture does not hold %q", tc.secret)
			}
			if err := m.DeleteMakerNoteTag(tc.tag); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := Write(bytes.NewReader(data), &out, m); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(out.Bytes(), tc.secret) {
				t.Errorf("%q is still in the output", tc.secret)
			}
			m2, err := Read(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			ids, err := m.EXIF.MakerNoteTagIDs(tc.tag)
			if err == nil {
				t.Fatalf("the edited Metadata still has the tag %s", tc.tag)
			}
			_ = ids
			ids2, err := m2.EXIF.MakerNoteTagIDs(tc.tag)
			if err == nil {
				t.Errorf("the output still has the tag %s: %v", tc.tag, ids2)
			}
		})
	}
	t.Run("Sony encrypted block", func(t *testing.T) {
		t.Parallel()
		data := mneCorpus(t, "raw/metadata-extractor/Sony ILCE-7M3 (A7M3).arw")
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.DeleteMakerNoteTag("0x9400"); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Write(bytes.NewReader(data), &out, m); err != nil {
			t.Fatal(err)
		}
		m2, err := Read(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m2.EXIF.MakerNoteTagIDs("0x9400"); !errors.Is(err, ErrMakerNoteTagNotFound) {
			t.Errorf("tag 0x9400 is still in the output (%v)", err)
		}
		if !mneEqual(mneTIFFBlocks(t, data), mneTIFFBlocks(t, out.Bytes())) {
			t.Error("the image data blocks changed")
		}
	})
}

// TestMakerNoteEdit_R407_ShiftedCanonEdit (task #407): a Canon note whose base
// is shifted reads its lens, and an edit of it is written with the corrected
// base: every other tag of the output reads as it did.
func TestMakerNoteEdit_R407_ShiftedCanonEdit(t *testing.T) {
	t.Parallel()
	const path = "jpeg/metadata-extractor/Canon EOS REBEL T3i.jpg"
	data := mneCorpus(t, path)
	for _, kind := range []string{"delete", "grow"} {
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		before := mneTags(t, m)
		if kind == "delete" {
			err = m.DeleteMakerNoteTag("0x0096")
		} else {
			err = m.SetMakerNoteTag("0x0096", strings.Repeat("Z", 60))
		}
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		var out bytes.Buffer
		if err := Write(bytes.NewReader(data), &out, m); err != nil {
			t.Fatal(err)
		}
		m2, err := Read(bytes.NewReader(out.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		after := mneTags(t, m2)
		for tag, v := range before {
			if tag != 0x0096 && after[tag] != v {
				t.Errorf("%s: tag %#04x changed", kind, uint16(tag))
			}
		}
		if got := m2.LensModel(); got != "EF-S18-55mm f/3.5-5.6 IS II" {
			t.Errorf("%s: LensModel = %q", kind, got)
		}
	}
}

// TestMakerNoteEdit_R398_Errors: the errors of the API, and the Metadata
// unchanged on each.
func TestMakerNoteEdit_R398_Errors(t *testing.T) {
	t.Parallel()
	canon := mneCorpus(t, "jpeg/metadata-extractor/Canon EOS 450D.jpg")
	read := func(t *testing.T, data []byte) *Metadata {
		t.Helper()
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tests := []struct {
		name string
		do   func(m *Metadata) error
		want error
	}{
		{"unknown tag", func(m *Metadata) error { return m.DeleteMakerNoteTag("NoSuchTag") }, ErrMakerNoteTagNotFound},
		{"bad hex", func(m *Metadata) error { return m.DeleteMakerNoteTag("0xZZ") }, ErrMakerNoteTagNotFound},
		{"set unknown tag", func(m *Metadata) error { return m.SetMakerNoteTag("NoSuchTag", "x") }, ErrMakerNoteTagNotFound},
		{"wrong value type", func(m *Metadata) error { return m.SetMakerNoteTag("0x0096", 5) }, ErrMakerNoteValueType},
		{"NUL in text", func(m *Metadata) error { return m.SetMakerNoteTag("0x0096", "a\x00b") }, ErrMakerNoteValueType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := read(t, canon)
			before := mneTags(t, m)
			if err := tc.do(m); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			after := mneTags(t, m)
			if len(before) != len(after) {
				t.Error("the Metadata changed although the edit failed")
			}
			var out bytes.Buffer
			if err := Write(bytes.NewReader(canon), &out, m); err != nil || !bytes.Equal(out.Bytes(), canon) {
				t.Error("a failed edit marked the EXIF as modified")
			}
		})
	}
	t.Run("ambiguous name", func(t *testing.T) {
		t.Parallel()
		m := read(t, mneCorpus(t, "jpeg/exif-samples/87_OSError.jpg"))
		ids, err := m.EXIF.MakerNoteTagIDs("ISO")
		if err != nil || len(ids) < 2 {
			t.Fatalf("the Nikon file must hold two tags named ISO: %v, %v", ids, err)
		}
		if err := m.SetMakerNoteTag("ISO", 100); !errors.Is(err, ErrMakerNoteAmbiguousTag) {
			t.Errorf("error = %v, want ErrMakerNoteAmbiguousTag", err)
		}
	})
	t.Run("no EXIF", func(t *testing.T) {
		t.Parallel()
		m := NewMetadata(format.FormatJPEG)
		if err := m.DeleteMakerNoteTag("0x0001"); !errors.Is(err, ErrNoMakerNote) {
			t.Errorf("error = %v, want ErrNoMakerNote", err)
		}
		if err := m.SetMakerNoteTag("0x0001", 1); !errors.Is(err, ErrNoMakerNote) {
			t.Errorf("error = %v, want ErrNoMakerNote", err)
		}
	})
	t.Run("MakerNote that refers to data outside itself", func(t *testing.T) {
		t.Parallel()
		m := read(t, mneCorpus(t, "jpeg/exiv2/exiv2-bug876.jpg"))
		if err := m.DeleteMakerNoteTag("0x0006"); !errors.Is(err, ErrMakerNoteNotEditable) {
			t.Errorf("error = %v, want ErrMakerNoteNotEditable", err)
		}
	})
	t.Run("CR3", func(t *testing.T) {
		t.Parallel()
		m := read(t, mneCorpus(t, "raw/metadata-extractor/Canon EOS R3.cr3"))
		if err := m.DeleteMakerNoteTag("0x0006"); !errors.Is(err, ErrMakerNoteNotEditable) {
			t.Errorf("error = %v, want ErrMakerNoteNotEditable", err)
		}
	})
	t.Run("WithoutMakerNote", func(t *testing.T) {
		t.Parallel()
		m, err := Read(bytes.NewReader(canon), WithoutMakerNote())
		if err != nil {
			t.Fatal(err)
		}
		if err := m.DeleteMakerNoteTag("0x000C"); !errors.Is(err, ErrNoMakerNote) {
			t.Errorf("error = %v, want ErrNoMakerNote", err)
		}
	})
}

// TestMakerNoteEdit_R398_Concurrent: the edit methods and the Set* methods of one
// Metadata, called from several goroutines, are free of races (go test -race);
// like the Set* methods they are serialised, and the getters are not part of
// that contract (doc.go, "Concurrency").
func TestMakerNoteEdit_R398_Concurrent(t *testing.T) {
	t.Parallel()
	data := mneCorpus(t, "jpeg/metadata-extractor/Canon EOS 450D.jpg")
	m, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				_ = m.SetMakerNoteTag("0x0006", strings.Repeat("a", 10+i))
			case 1:
				_ = m.DeleteMakerNoteTag("0x0006") // fails once another goroutine deleted it
			default:
				m.SetCaption("x")
			}
		}()
	}
	wg.Wait()
	var out bytes.Buffer
	if err := Write(bytes.NewReader(data), &out, m); err != nil {
		t.Fatal(err)
	}
}
