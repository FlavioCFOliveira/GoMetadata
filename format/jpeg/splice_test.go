package jpeg

// splice_test.go — tasks #400 and #403: Inject copies the source and puts the
// new metadata segments in the place the splice rules give them (splice.go).
// Rule IDs are the sub-test names; docs/conformance/containers.md lists them.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// spSeg is a marker segment: FF marker, length, payload.
func spSeg(marker byte, payload string) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...) //nolint:gosec // G115: the fixtures are short
}

// Segments of the fixtures. The EXIF payloads are tagged so that the tests can
// tell the old from the new.
var (
	spJFIF  = spSeg(0xE0, "JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00") //nolint:gochecknoglobals // immutable test fixture
	spJFXX  = spSeg(0xE0, "JFXX\x00\x10thumb")                            //nolint:gochecknoglobals // immutable test fixture
	spAdobe = spSeg(0xEE, "Adobe\x00\x64\x00\x00\x00\x00\x01")            //nolint:gochecknoglobals // immutable test fixture
	spICC   = spSeg(0xE2, "ICC_PROFILE\x00\x01\x01profile")               //nolint:gochecknoglobals // immutable test fixture
	spCOM   = spSeg(0xFE, "a comment")                                    //nolint:gochecknoglobals // immutable test fixture
	spDQT   = spSeg(0xDB, "\x00quantisation-table")                       //nolint:gochecknoglobals // immutable test fixture
	spSOF   = spSeg(0xC0, "\x08\x00\x01\x00\x01\x01\x01\x11\x00")         //nolint:gochecknoglobals // immutable test fixture
	spSOS   = spSeg(0xDA, "\x01\x01\x00\x00\x3f\x00")                     //nolint:gochecknoglobals // immutable test fixture
	spScan  = []byte{0x12, 0x34, 0xFF, 0x00, 0x56, 0xFF, 0xD9}            //nolint:gochecknoglobals // immutable test fixture: entropy data, EOI
	spSOI   = []byte{0xFF, 0xD8}                                          //nolint:gochecknoglobals // immutable test fixture
)

// spTIFF returns a minimal little-endian TIFF stream of 8+ bytes, tagged by
// the Make of IFD0 so that two streams differ.
func spTIFF(tag string) []byte {
	b := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x0F, 0x01, 2, 0}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(tag)+1)) //nolint:gosec // G115: the tags are short
	if len(tag)+1 <= 4 {
		b = append(b, tag...)
		b = append(b, make([]byte, 4-len(tag))...)
		return append(b, 0, 0, 0, 0)
	}
	b = binary.LittleEndian.AppendUint32(b, 26)
	b = append(b, 0, 0, 0, 0)
	return append(b, append([]byte(tag), 0)...)
}

func spExif(tag string) []byte { return spSeg(0xE1, "Exif\x00\x00"+string(spTIFF(tag))) }

// spXMPSeg is an XMP APP1 segment holding spPacket.
func spXMPSeg() []byte { return spSeg(0xE1, "http://ns.adobe.com/xap/1.0/\x00"+spPacket) }

// spPS is a Photoshop APP13 segment holding an old IPTC resource.
func spPS() []byte {
	const iim = "\x1c\x02\x78\x00\x03old"
	irb := []byte("8BIM\x04\x04\x00\x00")
	irb = binary.BigEndian.AppendUint32(irb, uint32(len(iim)))
	irb = append(irb, iim...)
	if len(iim)%2 == 1 {
		irb = append(irb, 0)
	}
	return spSeg(0xED, "Photoshop 3.0\x00"+string(irb))
}

// spFile joins SOI, the segments, SOS, the scan data and EOI.
func spFile(segs ...[]byte) []byte {
	b := append([]byte(nil), spSOI...)
	for _, s := range segs {
		b = append(b, s...)
	}
	b = append(b, spSOS...)
	return append(b, spScan...)
}

func spCat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// spOrder returns the markers of the segments of b before the first scan
// header, and for APP1 the kind (E = Exif, X = XMP) and for APP13 P.
func spOrder(t *testing.T, b []byte) string {
	t.Helper()
	var out []byte
	pos := 2
	for pos+4 <= len(b) {
		if b[pos] != 0xFF {
			t.Fatalf("no marker at %d", pos)
		}
		m := b[pos+1]
		n := int(binary.BigEndian.Uint16(b[pos+2:]))
		switch {
		case m == 0xDA:
			return string(out) + "S"
		case m == 0xE1 && bytes.HasPrefix(b[pos+4:], identExif):
			out = append(out, 'E')
		case m == 0xE1 && (bytes.HasPrefix(b[pos+4:], identXMP) || bytes.HasPrefix(b[pos+4:], identXMPNote)):
			out = append(out, 'X')
		case m == 0xED && bytes.HasPrefix(b[pos+4:], identPS):
			out = append(out, 'P')
		case m == 0xE0 && bytes.HasPrefix(b[pos+4:], identJFIF):
			out = append(out, 'J')
		case m == 0xE0 && bytes.HasPrefix(b[pos+4:], identJFXX):
			out = append(out, 'F')
		case m == 0xEE:
			out = append(out, 'A')
		case m == 0xE2:
			out = append(out, 'C')
		case m == 0xFE:
			out = append(out, 'M')
		case m == 0xDB:
			out = append(out, 'Q')
		case m == 0xC0:
			out = append(out, 'B')
		default:
			out = append(out, '?')
		}
		pos += 2 + n
	}
	return string(out)
}

// spStrip returns b without its Exif, XMP and Photoshop APP segments before the
// first scan header. It is written from JPEG ISO/IEC 10918-1 §B.1.1 alone, not
// from splice.go, so that it is an independent reading of the grammar: a
// segment that cannot be read ends the walk and the rest is kept.
func spStrip(b []byte) []byte {
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		return b
	}
	out := append([]byte(nil), b[:2]...)
	pos := 2
	for {
		start := pos
		p := pos
		if p+2 > len(b) || b[p] != 0xFF {
			return append(out, b[start:]...)
		}
		for p+2 < len(b) && b[p+1] == 0xFF {
			p++
		}
		m := b[p+1]
		hdrEnd := p + 2
		switch {
		case m == 0 || m == 0xD9:
			return append(out, b[start:]...)
		case m == 0xD8 || m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			out = append(out, b[start:hdrEnd]...)
			pos = hdrEnd
			continue
		}
		if hdrEnd+2 > len(b) {
			return append(out, b[start:]...)
		}
		n := int(binary.BigEndian.Uint16(b[hdrEnd:]))
		end := hdrEnd + n
		if n < 2 || end > len(b) || m == 0xDA {
			return append(out, b[start:]...)
		}
		payload := b[hdrEnd+2 : end]
		drop := m == 0xE1 && (bytes.HasPrefix(payload, identExif) || bytes.HasPrefix(payload, identXMP) || bytes.HasPrefix(payload, identXMPNote)) ||
			m == 0xED && bytes.HasPrefix(payload, identPS)
		if !drop {
			out = append(out, b[start:end]...)
		}
		pos = end
	}
}

func spInject(t *testing.T, src, exifTIFF, xmp []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(src), &out, exifTIFF, nil, xmp, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

const spPacket = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`

// TestSplice_Placement covers where a new or replaced metadata segment goes
// (ITU-T T.871 | ISO/IEC 10918-5 §6.3, p. 3: the JFIF APP0 segment immediately
// follows SOI; §10.2, p. 7: JFIF extension APP0 segments follow it; Exif
// APP1 first among the APP1 segments, CIPA DC-008-2026 §4.5.4).
func TestSplice_Placement(t *testing.T) {
	t.Parallel()
	newEXIF, newIPTC, newXMP := spTIFF("NEW"), []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'n', 'e', 'w'}, []byte(spPacket)
	tests := []struct {
		name               string
		src                []byte
		exif, iptc, xmp    []byte
		want               string
		keepOld, noPreserv bool
	}{
		{"JFIF-01 new EXIF follows JFIF APP0", spFile(spJFIF, spICC, spDQT), newEXIF, nil, nil, "JECQS", false, false},
		{"JFIF-02 new metadata follows JFIF and JFXX", spFile(spJFIF, spJFXX, spICC, spDQT), newEXIF, newIPTC, newXMP, "JFEXPCQS", false, false},
		{"JFIF-03 new metadata follows a leading Adobe APP14", spFile(spAdobe, spDQT), newEXIF, nil, newXMP, "AEXQS", false, false},
		{"JFIF-04 no JFIF: new metadata follows SOI", spFile(spDQT, spSOF), newEXIF, newIPTC, newXMP, "EXPQBS", false, false},
		{"JFIF-05 ICC before JFIF is not a leading segment", spFile(spICC, spJFIF, spDQT), newEXIF, nil, nil, "ECJQS", false, false},
		{"JFIF-06 JFIF after another segment is not leading", spFile(spJFIF, spCOM, spJFXX), newEXIF, nil, nil, "JEMFS", false, false},
		{"PLACE-01 old EXIF is replaced in place", spFile(spJFIF, spCOM, spExif("OLD"), spICC, spDQT), newEXIF, nil, nil, "JMECQS", false, false},
		{"PLACE-02 second old EXIF is dropped", spFile(spJFIF, spExif("OLD"), spCOM, spExif("TWO"), spDQT), newEXIF, nil, nil, "JEMQS", false, false},
		{"PLACE-03 new XMP follows the old EXIF", spFile(spJFIF, spCOM, spExif("OLD"), spICC, spDQT), newEXIF, nil, newXMP, "JMEXCQS", false, false},
		{"PLACE-04 new EXIF precedes the old XMP", spFile(spJFIF, spCOM, spXMPSeg(), spDQT), newEXIF, nil, newXMP, "JEMXQS", false, false},
		{"PLACE-05 new IPTC follows the old XMP", spFile(spJFIF, spXMPSeg(), spCOM, spDQT), nil, newIPTC, newXMP, "JXPMQS", false, false},
		{"PLACE-06 new IPTC follows the old EXIF when there is no XMP", spFile(spJFIF, spExif("OLD"), spCOM), newEXIF, newIPTC, nil, "JEPMS", false, false},
		{"PLACE-07 old IPTC is replaced in place, after the COM", spFile(spJFIF, spCOM, spPS(), spDQT), nil, newIPTC, nil, "JMPQS", false, false},
		{"PLACE-08 old XMP is replaced in place", spFile(spJFIF, spXMPSeg(), spCOM), nil, nil, newXMP, "JXMS", false, false},
		{"PLACE-09 an old segment is removed when its payload is nil", spFile(spJFIF, spExif("OLD"), spXMPSeg(), spCOM), nil, nil, nil, "JMS", false, false},
		{"PLACE-10 adjacent old EXIF and XMP are replaced where they were", spFile(spJFIF, spXMPSeg(), spExif("OLD"), spCOM), newEXIF, nil, newXMP, "JXEMS", false, false},
		{"PLACE-11 the IPTC replaces the old one that follows the XMP it is placed after", spFile(spJFIF, spPS(), spExif("OLD")), newEXIF, newIPTC, newXMP, "JPEXS", false, false},
		{"PLACE-12 nothing to write: the source is copied", spFile(spJFIF, spICC, spCOM, spDQT), nil, nil, nil, "JCMQS", false, false},
		{"PLACE-13 every other APPn segment, JFIF included, is dropped when asked", spFile(spJFIF, spICC, spCOM, spDQT), newEXIF, nil, nil, "EMQS", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(tc.src), &out, tc.exif, tc.iptc, tc.xmp, !tc.noPreserv); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if got := spOrder(t, out.Bytes()); got != tc.want {
				t.Errorf("segment order = %q, want %q", got, tc.want)
			}
			if !tc.noPreserv {
				// Everything but the metadata segments is the source, byte for byte.
				if got, want := spStrip(out.Bytes()), spStrip(tc.src); !bytes.Equal(got, want) {
					t.Errorf("output without metadata segments differs from the source without them")
				}
			}
			if !bytes.HasSuffix(out.Bytes(), spCat(spSOS, spScan)) {
				t.Error("the scan header and the image data are not copied unchanged")
			}
		})
	}
}

// TestSplice_ByteIdentity covers JPEG-IDENT: with nothing to change the output
// is the source, whatever its structure (fill bytes, unknown markers, bytes
// after EOI), and adding a segment changes nothing else.
func TestSplice_ByteIdentity(t *testing.T) {
	t.Parallel()
	fill := spCat(spSOI, []byte{0xFF, 0xFF, 0xFF}, spJFIF[1:], []byte{0xFF, 0xFF}, spCOM[1:], spDQT, spSOS, spScan, []byte("trailing bytes"))
	tests := []struct {
		name string
		src  []byte
	}{
		{"IDENT-01 fill bytes before markers", fill},
		{"IDENT-02 bytes after EOI", spCat(spFile(spJFIF, spDQT), []byte("tail after EOI"))},
		{"IDENT-03 EOI before any scan", spCat(spSOI, spJFIF, spCOM, []byte{0xFF, 0xD9}, []byte("after"))},
		{"IDENT-04 SOI only", spSOI},
		{"IDENT-05 restart and TEM markers before the scan", spCat(spSOI, spJFIF, []byte{0xFF, 0xD0, 0xFF, 0x01}, spDQT, spSOS, spScan)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := spInject(t, tc.src, nil, nil); !bytes.Equal(got, tc.src) {
				t.Errorf("an empty write changed the file:\n got %x\nwant %x", got, tc.src)
			}
			withEXIF := spInject(t, tc.src, spTIFF("NEW"), nil)
			if !bytes.Equal(spStrip(withEXIF), spStrip(tc.src)) {
				t.Error("adding an EXIF changed something besides the segment")
			}
			if len(withEXIF) != len(tc.src)+len(spSeg(0xE1, "Exif\x00\x00"+string(spTIFF("NEW")))) {
				t.Errorf("output is %d bytes, want the source plus the EXIF segment", len(withEXIF))
			}
		})
	}
	t.Run("IDENT-06 the fill bytes of a dropped segment go with it, those of the others stay", func(t *testing.T) {
		t.Parallel()
		src := spCat(spSOI, []byte{0xFF}, spExif("OLD"), []byte{0xFF, 0xFF}, spCOM[1:], spSOS, spScan)
		got := spInject(t, src, spTIFF("NEW"), nil)
		want := spCat(spSOI, spExif("NEW"), []byte{0xFF, 0xFF}, spCOM[1:], spSOS, spScan)
		if !bytes.Equal(got, want) {
			t.Errorf("got %x\nwant %x", got, want)
		}
	})
}

// TestSplice_DamagedSource covers task #403: a source whose marker stream is
// damaged before the scan header is copied from the damage on, and the new
// metadata is added before it.
func TestSplice_DamagedSource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  []byte
		want []byte // output of a write of an EXIF
	}{
		{"DAMAGE-01 a segment runs past the end of the file",
			spCat(spSOI, spJFIF, []byte{0xFF, 0xE2, 0x01, 0x00, 'x', 'y'}),
			spCat(spSOI, spJFIF, spExif("NEW"), []byte{0xFF, 0xE2, 0x01, 0x00, 'x', 'y'})},
		{"DAMAGE-02 no marker prefix",
			spCat(spSOI, spJFIF, []byte("garbage that is not a segment")),
			spCat(spSOI, spJFIF, spExif("NEW"), []byte("garbage that is not a segment"))},
		{"DAMAGE-03 a length below 2",
			spCat(spSOI, []byte{0xFF, 0xE2, 0x00, 0x01, 1, 2, 3}),
			spCat(spSOI, spExif("NEW"), []byte{0xFF, 0xE2, 0x00, 0x01, 1, 2, 3})},
		{"DAMAGE-04 a marker cut after its first byte",
			spCat(spSOI, spJFIF, []byte{0xFF}),
			spCat(spSOI, spJFIF, spExif("NEW"), []byte{0xFF})},
		{"DAMAGE-05 a length field cut short",
			spCat(spSOI, spJFIF, []byte{0xFF, 0xE2, 0x00}),
			spCat(spSOI, spJFIF, spExif("NEW"), []byte{0xFF, 0xE2, 0x00})},
		{"DAMAGE-06 the first scan header runs past the end",
			spCat(spSOI, spJFIF, []byte{0xFF, 0xDA, 0x00, 0x20, 1}),
			spCat(spSOI, spJFIF, spExif("NEW"), []byte{0xFF, 0xDA, 0x00, 0x20, 1})},
		{"DAMAGE-07 an old EXIF before the damage is replaced",
			spCat(spSOI, spExif("OLD"), []byte{0xFF, 0xE2, 0x01, 0x00, 'x'}),
			spCat(spSOI, spExif("NEW"), []byte{0xFF, 0xE2, 0x01, 0x00, 'x'})},
		{"DAMAGE-08 only the SOI marker",
			spSOI,
			spCat(spSOI, spExif("NEW"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := spInject(t, tc.src, spTIFF("NEW"), nil); !bytes.Equal(got, tc.want) {
				t.Errorf("got %x\nwant %x", got, tc.want)
			}
		})
	}
	t.Run("DAMAGE-09 a source that does not start with SOI is refused and nothing is written", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		err := Inject(bytes.NewReader([]byte("not a jpeg at all")), &out, spTIFF("NEW"), nil, nil, true)
		if !errors.Is(err, ErrNotJPEG) {
			t.Errorf("error = %v, want ErrNotJPEG", err)
		}
		if out.Len() != 0 {
			t.Errorf("%d bytes written for a source that is not a JPEG", out.Len())
		}
		err = Inject(bytes.NewReader(nil), &out, spTIFF("NEW"), nil, nil, true)
		if err == nil || out.Len() != 0 {
			t.Errorf("an empty source: error %v, %d bytes written", err, out.Len())
		}
	})
}

// TestSplice_KeepInPlace covers InjectKeeping: a kept segment stays where it
// is, and a new segment of a kind that is not kept is placed around it.
func TestSplice_KeepInPlace(t *testing.T) {
	t.Parallel()
	src := spFile(spJFIF, spCOM, spExif("KEEP"), spICC, spPS(), spDQT)
	var out bytes.Buffer
	if err := InjectKeeping(bytes.NewReader(src), &out, nil, nil, []byte(spPacket), true, KeepEXIF|KeepIPTC); err != nil {
		t.Fatal(err)
	}
	if got, want := spOrder(t, out.Bytes()), "JMEXCPQS"; got != want {
		t.Errorf("segment order = %q, want %q: a new XMP follows the kept EXIF", got, want)
	}
	if !bytes.Contains(out.Bytes(), spExif("KEEP")) || !bytes.Contains(out.Bytes(), spPS()) {
		t.Error("a kept segment is not in the output byte for byte")
	}
}

// TestSplice_ReaderKinds checks that the write is the same for a reader that
// is not a *bytes.Reader and for a writer that is a *bytes.Buffer, and for one
// that is neither.
func TestSplice_ReaderKinds(t *testing.T) {
	t.Parallel()
	src := spFile(spJFIF, spCOM, spExif("OLD"), spICC, spDQT)
	want := spInject(t, src, spTIFF("NEW"), []byte(spPacket))
	var w1 bytes.Buffer
	if err := Inject(onlyReadSeeker{bytes.NewReader(src)}, onlyWriter{&w1}, spTIFF("NEW"), nil, []byte(spPacket), true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w1.Bytes(), want) {
		t.Error("output differs for a reader and a writer that expose only Read, Seek and Write")
	}
}

type onlyReadSeeker struct{ r io.ReadSeeker }

func (o onlyReadSeeker) Read(p []byte) (int, error)                { return o.r.Read(p) }           //nolint:wrapcheck // test double: passes the reader through
func (o onlyReadSeeker) Seek(off int64, whence int) (int64, error) { return o.r.Seek(off, whence) } //nolint:wrapcheck // test double: passes the reader through

type onlyWriter struct{ w io.Writer }

func (o onlyWriter) Write(p []byte) (int, error) { return o.w.Write(p) } //nolint:wrapcheck // test double: passes the writer through

// TestSplice_TooManyRuns covers ErrTooManySegments: a source whose every
// other segment is an old metadata segment is refused, not planned.
func TestSplice_TooManyRuns(t *testing.T) {
	t.Parallel()
	src := append([]byte(nil), spSOI...)
	exif := spExif("x")
	for range maxSpliceDrops + 1 {
		src = append(src, exif...)
		src = append(src, spCOM...)
	}
	src = append(src, spSOS...)
	src = append(src, spScan...)
	var out bytes.Buffer
	err := Inject(bytes.NewReader(src), &out, spTIFF("NEW"), nil, nil, true)
	if !errors.Is(err, ErrTooManySegments) {
		t.Errorf("error = %v, want ErrTooManySegments", err)
	}
	if out.Len() != 0 {
		t.Errorf("%d bytes written before the refusal", out.Len())
	}
}

// olympusJPEGSegs is olympusJPEG with the segments head before the EXIF segment
// and tail after it; the MakerNote offset addresses the preview, which ends the
// file.
func olympusJPEGSegs(t *testing.T, head, tail [][]byte) []byte {
	t.Helper()
	build := func(previewAt int64) []byte {
		b := append([]byte(nil), spSOI...)
		for _, s := range head {
			b = append(b, s...)
		}
		// The MakerNote of the source sits at pad 6; its value counts from it.
		mn := int64(len(b)) + 10 + mnOffsetOf(t, olympusEXIF(0, previewLen, 6))
		stream := olympusEXIF(uint32(previewAt-mn), previewLen, 6) //nolint:gosec // G115: small test offsets
		b = append(b, spSeg(0xE1, "Exif\x00\x00"+string(stream))...)
		for _, s := range tail {
			b = append(b, s...)
		}
		b = append(b, spSOS...)
		b = append(b, spScan...)
		return append(b, previewJPEG...)
	}
	i := bytes.Index(build(0), previewJPEG)
	file := build(int64(i))
	if j := bytes.Index(file, previewJPEG); i < 0 || j != i {
		t.Fatalf("fixture: the preview is at %d, expected %d", j, i)
	}
	return file
}

// TestSplice_ExternalOffsetsLayout covers the Olympus offsets (task #362) with
// the new layout: the EXIF follows a JFIF segment, and an old EXIF is replaced
// where it is, so the TIFF header of the new stream is not at offset 12.
func TestSplice_ExternalOffsetsLayout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		head [][]byte // segments before the old EXIF
		tail [][]byte // segments after it
	}{
		{"JFIF-before-EXIF", [][]byte{spJFIF}, nil},
		{"JFIF-and-segments-around-EXIF", [][]byte{spJFIF, spCOM}, [][]byte{spICC}},
		{"JFXX-then-EXIF", [][]byte{spJFIF, spJFXX}, [][]byte{spDQT}},
	} {
		t.Run("R-27/"+tc.name, func(t *testing.T) {
			t.Parallel()
			src := olympusJPEGSegs(t, tc.head, tc.tail)
			if got := previewAfter(t, src); !bytes.Equal(got, previewJPEG) {
				t.Fatalf("fixture: the source offset addresses %q, not the preview", got)
			}
			rawSrc, _, _, err := Extract(bytes.NewReader(src))
			if err != nil || rawSrc == nil {
				t.Fatalf("Extract: %v", err)
			}
			offs := exif.ExternalOffsets(rawSrc)
			if len(offs) != 1 {
				t.Fatalf("ExternalOffsets = %v", offs)
			}
			for _, pad := range []int{6, 40, 0} { // the MakerNote stays, moves forward, moves back
				stream := olympusEXIF(offs[0].Value, previewLen, pad)
				var out bytes.Buffer
				if err := Inject(bytes.NewReader(src), &out, stream, newIIM, nil, true); err != nil {
					t.Fatalf("Inject: %v", err)
				}
				if got := previewAfter(t, out.Bytes()); !bytes.Equal(got, previewJPEG) {
					t.Errorf("pad %d: after the write the offset addresses %q, want the preview", pad, got)
				}
			}
			// A new IPTC goes after the EXIF (nothing to follow it): the segments
			// before the EXIF are the source's, so the EXIF keeps its place.
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(src), &out, rawSrc, newIIM, nil, true); err != nil {
				t.Fatal(err)
			}
			if i := bytes.Index(out.Bytes(), []byte("Exif\x00\x00")); i != bytes.Index(src, []byte("Exif\x00\x00")) {
				t.Errorf("the EXIF moved from %d to %d: a replaced segment keeps its place", bytes.Index(src, []byte("Exif\x00\x00")), i)
			}
		})
	}
}

// FuzzJPEGSplice feeds arbitrary bytes as the source and checks, for each write
// that succeeds, that the source is copied: the output without its Exif, XMP
// and Photoshop segments is the source without them (spStrip reads the grammar
// independently of splice.go), and a write of nothing is the source without
// them.
func FuzzJPEGSplice(f *testing.F) {
	f.Add(spFile(spJFIF, spCOM, spExif("OLD"), spICC, spDQT))
	f.Add(spCat(spSOI, []byte{0xFF, 0xFF}, spJFIF[1:], spXMPSeg(), spPS(), spSOS, spScan, []byte("tail")))
	f.Add(spCat(spSOI, spJFIF, []byte{0xFF, 0xE2, 0x01, 0x00, 'x', 'y'}))
	f.Add(spCat(spSOI, spAdobe, spExif("OLD"), spExif("TWO"), []byte{0xFF, 0xD9, 'z'}))
	f.Add([]byte{0xFF, 0xD8})
	f.Fuzz(func(t *testing.T, src []byte) {
		var nop bytes.Buffer
		if err := Inject(bytes.NewReader(src), &nop, nil, nil, nil, true); err == nil {
			// A write of nothing removes the old metadata segments and copies
			// the rest. (A Photoshop segment is re-encoded without its IPTC
			// resource, so only a source without one is compared.)
			if !bytes.Contains(src, identPS) && !bytes.Equal(nop.Bytes(), spStrip(src)) {
				t.Fatalf("a write of nothing is not the source without its metadata:\n got %x\nwant %x", nop.Bytes(), spStrip(src))
			}
		}
		var out bytes.Buffer
		if err := Inject(bytes.NewReader(src), &out, spTIFF("NEW"), nil, []byte(spPacket), true); err != nil {
			return
		}
		if !bytes.Contains(src, identPS) && !bytes.Equal(spStrip(out.Bytes()), spStrip(src)) {
			t.Fatalf("output without metadata segments differs from the source without them:\n out %x\n src %x", out.Bytes(), src)
		}
		if !bytes.Contains(out.Bytes(), spExif("NEW")) {
			t.Fatal("the new EXIF is not in the output")
		}
	})
}
