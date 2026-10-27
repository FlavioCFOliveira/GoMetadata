package jpeg

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// keepSegment returns the marker segment (marker, length, payload).
func keepSegment(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xFF, marker, byte(n >> 8), byte(n)}, payload...) //nolint:gosec // G115: tiny test payloads
}

// keepIRB returns an APP13 Photoshop payload: an IPTC resource (0x0404)
// followed by a stale IPTC digest resource (0x0425), both after a non-empty
// resource name so a rebuilt segment could not match it by accident.
func keepIRB(iim []byte) []byte {
	var b bytes.Buffer
	b.WriteString("Photoshop 3.0\x00")
	res := func(id uint16, name string, data []byte) {
		b.WriteString("8BIM")
		b.Write(binary.BigEndian.AppendUint16(nil, id))
		b.WriteByte(byte(len(name))) //nolint:gosec // G115: a two-byte test name
		b.WriteString(name)
		if (1+len(name))%2 != 0 {
			b.WriteByte(0)
		}
		b.Write(binary.BigEndian.AppendUint32(nil, uint32(len(data)))) //nolint:gosec // G115: tiny test data
		b.Write(data)
		if len(data)%2 != 0 {
			b.WriteByte(0)
		}
	}
	res(0x0404, "ab", iim)
	res(0x0425, "", bytes.Repeat([]byte{0xEE}, 16)) // a stale digest
	return b.Bytes()
}

const keepXMPPacket = `<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'></x:xmpmeta><?xpacket end='r'?>`

// keepSource builds a JPEG: SOI, JFIF APP0, EXIF, Photoshop APP13, XMP, APP2,
// SOS and image data.
func keepSource() (file, exifPayload, ps, xmp []byte) {
	tiff := []byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	exifPayload = append([]byte("Exif\x00\x00"), tiff...)
	ps = keepIRB([]byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'})
	xmp = append([]byte("http://ns.adobe.com/xap/1.0/\x00"), keepXMPPacket...)
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	b.Write(keepSegment(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00")))
	b.Write(keepSegment(0xE1, exifPayload))
	b.Write(keepSegment(0xED, ps))
	b.Write(keepSegment(0xE1, xmp))
	b.Write(keepSegment(0xE2, []byte("ICC_PROFILE\x00data")))
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0x01, 0x02, 0xFF, 0x00, 0x03, 0xFF, 0xD9})
	return b.Bytes(), exifPayload, ps, xmp
}

// keepPayloads returns the payloads of the segments of file with the given
// marker, before the first SOS.
func keepPayloads(file []byte, marker byte) [][]byte {
	var out [][]byte
	for i := 2; i+4 <= len(file) && file[i] == 0xFF; {
		if file[i+1] == 0xFF { // a fill byte before the marker (T.81 §B.1.1.2)
			i++
			continue
		}
		m := file[i+1]
		if m == 0xDA || m == 0xD9 {
			break
		}
		n := int(binary.BigEndian.Uint16(file[i+2:]))
		if n < 2 || i+2+n > len(file) {
			break
		}
		if m == marker {
			out = append(out, file[i+4:i+2+n])
		}
		i += 2 + n
	}
	return out
}

// TestInjectKeeping covers task #387: the segments in keep stay in the output
// byte for byte, the others are replaced, and nothing else changes.
func TestInjectKeeping(t *testing.T) {
	t.Parallel()
	src, exifPayload, ps, xmp := keepSource()
	newEXIF := []byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 1, 0, 0x0E, 0x01, 2, 0, 4, 0, 0, 0, 'n', 'e', 'w', 0, 0, 0, 0, 0}
	addIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'n', 'e', 'w'}
	newXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'><!--new--></x:xmpmeta><?xpacket end='r'?>`)

	tests := []struct {
		name string
		keep Keep
	}{
		{"keep nothing", 0},
		{"keep EXIF", KeepEXIF},
		{"keep IPTC", KeepIPTC},
		{"keep XMP", KeepXMP},
		{"keep EXIF and IPTC", KeepEXIF | KeepIPTC},
		{"keep IPTC and XMP", KeepIPTC | KeepXMP},
		{"keep everything", KeepEXIF | KeepIPTC | KeepXMP},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			// The payloads of a kept segment are passed too, and ignored.
			if err := InjectKeeping(bytes.NewReader(src), &out, newEXIF, addIIM, newXMP, true, tc.keep); err != nil {
				t.Fatalf("InjectKeeping: %v", err)
			}
			got := out.Bytes()

			check := func(label string, marker byte, kept bool, orig []byte, prefix string) {
				var found [][]byte
				for _, p := range keepPayloads(got, marker) {
					if bytes.HasPrefix(p, []byte(prefix)) {
						found = append(found, p)
					}
				}
				if len(found) != 1 {
					t.Errorf("%s: %d segments in the output, want 1", label, len(found))
					return
				}
				if kept && !bytes.Equal(found[0], orig) {
					t.Errorf("%s: a kept segment changed", label)
				}
				if !kept && bytes.Equal(found[0], orig) {
					t.Errorf("%s: a replaced segment is unchanged", label)
				}
			}
			check("EXIF", 0xE1, tc.keep&KeepEXIF != 0, exifPayload, "Exif\x00\x00")
			check("APP13", 0xED, tc.keep&KeepIPTC != 0, ps, "Photoshop 3.0\x00")
			check("XMP", 0xE1, tc.keep&KeepXMP != 0, xmp, "http://ns.adobe.com/xap/1.0/\x00")

			// The segments that are not metadata and the image data are intact.
			if got := keepPayloads(got, 0xE2); len(got) != 1 || string(got[0]) != "ICC_PROFILE\x00data" {
				t.Errorf("APP2 changed: %q", got)
			}
			if jf := keepPayloads(got, 0xE0); len(jf) != 1 {
				t.Errorf("APP0: %d segments", len(jf))
			}
			if sos := bytes.Index(src, []byte{0xFF, 0xDA}); sos < 0 || !bytes.HasSuffix(got, src[sos:]) {
				t.Error("the image data changed")
			}
			if tc.keep == KeepEXIF|KeepIPTC|KeepXMP && !bytes.Equal(got, src) {
				t.Error("keeping everything must give back the source byte for byte")
			}
		})
	}
}

// TestInjectKeepingDigest: replacing the XMP while the IPTC is kept leaves the
// APP13 segment, the stale digest included, as it is; replacing the IPTC while
// the XMP is kept leaves the digest alone too (MWG Guidelines v2.0 §4.2.3.2).
func TestInjectKeepingDigest(t *testing.T) {
	t.Parallel()
	src, _, ps, _ := keepSource()
	newXMP := []byte(`<?xpacket begin='' id='W5M0MpCehiHzreSzNTczkc9d'?><x:xmpmeta xmlns:x='adobe:ns:meta/'><!--new--></x:xmpmeta><?xpacket end='r'?>`)
	addIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'n', 'e', 'w'}

	var out bytes.Buffer
	if err := InjectKeeping(bytes.NewReader(src), &out, nil, nil, newXMP, true, KeepEXIF|KeepIPTC); err != nil {
		t.Fatal(err)
	}
	if p := keepPayloads(out.Bytes(), 0xED); len(p) != 1 || !bytes.Equal(p[0], ps) {
		t.Error("IPTC kept, XMP replaced: the APP13 segment changed")
	}

	out.Reset()
	if err := InjectKeeping(bytes.NewReader(src), &out, nil, addIIM, nil, true, KeepEXIF|KeepXMP); err != nil {
		t.Fatal(err)
	}
	p := keepPayloads(out.Bytes(), 0xED)
	if len(p) != 1 || !bytes.Contains(p[0], bytes.Repeat([]byte{0xEE}, 16)) {
		t.Error("IPTC replaced, XMP kept: the stale digest was rewritten")
	}
	if !bytes.Contains(p[0], addIIM) {
		t.Error("IPTC replaced: the new IIM is not in the APP13 segment")
	}
}

// TestInjectKeepingPreserveUnknownFalse: dropping the unknown APPn segments
// still applies to a write that keeps metadata segments.
func TestInjectKeepingPreserveUnknownFalse(t *testing.T) {
	t.Parallel()
	src, exifPayload, ps, xmp := keepSource()
	var out bytes.Buffer
	if err := InjectKeeping(bytes.NewReader(src), &out, nil, nil, nil, false, KeepEXIF|KeepIPTC|KeepXMP); err != nil {
		t.Fatal(err)
	}
	got := out.Bytes()
	if len(keepPayloads(got, 0xE2)) != 0 || len(keepPayloads(got, 0xE0)) != 0 {
		t.Error("unknown APPn segments were not dropped")
	}
	if e := keepPayloads(got, 0xE1); len(e) != 2 || !bytes.Equal(e[0], exifPayload) || !bytes.Equal(e[1], xmp) {
		t.Error("kept EXIF and XMP changed")
	}
	if p := keepPayloads(got, 0xED); len(p) != 1 || !bytes.Equal(p[0], ps) {
		t.Error("kept APP13 changed")
	}
}

// TestInjectKeepingOlympusEXIF: an EXIF stream with MakerNote offsets to data
// outside it is never kept (the offsets follow the layout of the write); the
// IPTC and XMP still are, and the preview is found where the offset says.
func TestInjectKeepingOlympusEXIF(t *testing.T) {
	t.Parallel()
	file := olympusJPEG(t, nil, func(int64) []byte { return previewJPEG })
	raw, _, _, err := Extract(bytes.NewReader(file))
	if err != nil || raw == nil {
		t.Fatalf("Extract: %v", err)
	}
	iim := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'a', 'b', 'c'}

	var out bytes.Buffer
	// An IPTC is added (not kept): the preview moves, and the offset must follow.
	if err := InjectKeeping(bytes.NewReader(file), &out, raw, iim, nil, true, KeepEXIF|KeepXMP); err != nil {
		t.Fatalf("InjectKeeping: %v", err)
	}
	if got := previewAfter(t, out.Bytes()); !bytes.Equal(got, previewJPEG) {
		t.Error("the MakerNote offset does not address the preview after the write")
	}
	// Everything kept: byte identical to the source.
	out.Reset()
	if err := InjectKeeping(bytes.NewReader(file), &out, raw, nil, nil, true, KeepEXIF|KeepIPTC|KeepXMP); err != nil {
		t.Fatalf("InjectKeeping: %v", err)
	}
	if !bytes.Equal(out.Bytes(), file) {
		t.Error("an unchanged write of an Olympus JPEG is not byte-identical")
	}
}

// FuzzInjectKeeping: any source and any keep set must not panic, and the output
// of a write that keeps a segment must hold it unchanged.
func FuzzInjectKeeping(f *testing.F) {
	src, _, _, _ := keepSource()
	f.Add(src, uint8(0))
	f.Add(src, uint8(7))
	f.Add(src, uint8(5))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xD9}, uint8(3))
	f.Add([]byte{}, uint8(1))
	rawEXIF := []byte{'I', 'I', 0x2A, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	rawIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'f', 'z', '1'}
	rawXMP := []byte(keepXMPPacket)
	f.Fuzz(func(t *testing.T, data []byte, k uint8) {
		keep := Keep(k) & (KeepEXIF | KeepIPTC | KeepXMP)
		var out bytes.Buffer
		if err := InjectKeeping(bytes.NewReader(data), &out, rawEXIF, rawIPTC, rawXMP, true, keep); err != nil {
			return
		}
		if keep == KeepEXIF|KeepIPTC|KeepXMP && len(data) >= 2 {
			// A kept segment is copied from the source: the output has the same
			// payloads of each kind as the input.
			for _, m := range []byte{0xE1, 0xED} {
				a, b := keepPayloads(data, m), keepPayloads(out.Bytes(), m)
				if len(a) != len(b) {
					t.Fatalf("marker %#x: %d segments in the source, %d in the output", m, len(a), len(b))
				}
			}
		}
	})
}
