package png

// rawprofile_task325_test.go — regression tests for task #325: a write that
// emits the standard eXIf chunk or XMP iTXt chunk drops the "Raw profile type
// exif|APP1|xmp" text chunk whose content the standard chunk replaces, so
// exactly one current copy remains (PNG 3rd Edition §11.3.4.5: one eXIf chunk;
// XMP Specification Part 3 §1.1.5, p. 16: no more than one chunk containing
// XMP).

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// countRawProfiles returns how many text chunks of data carry a "Raw profile type
// <suffix>" keyword (ASCII case-insensitive).
func countRawProfiles(data []byte, suffix string) int {
	n := 0
	for pos := 8; pos+12 <= len(data); {
		l := int(binary.BigEndian.Uint32(data[pos:]))
		if pos+12+l > len(data) {
			break
		}
		payload := data[pos+8 : pos+8+l]
		if kind, kw := isRawProfileKeyword(payload); kind != rawProfileNone &&
			bytes.EqualFold(payload[len(rawProfilePrefix):kw], []byte(suffix)) {
			n++
		}
		pos += 12 + l
	}
	return n
}

// countChunks returns the number of chunks of type ct in data.
func countChunks(data []byte, ct string) int {
	n := 0
	for pos := 8; pos+12 <= len(data); {
		l := int(binary.BigEndian.Uint32(data[pos:]))
		if string(data[pos+4:pos+8]) == ct {
			n++
		}
		pos += 12 + l
	}
	return n
}

func injectAll(t *testing.T, in, rawEXIF, rawIPTC, rawXMP []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(in), &out, rawEXIF, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

// TestInject_RawProfileEXIFDropped (PNG-EXIF-01): every chunk type and payload
// shape the reader accepts for an EXIF raw profile is dropped when eXIf is
// written, and the written eXIf is what Extract reads back.
func TestInject_RawProfileEXIFDropped(t *testing.T) {
	t.Parallel()
	t.Run("PNG-EXIF-01", func(t *testing.T) {
		t.Parallel()
		old := tinyEXIF(t)
		updated := append(bytes.Clone(old), 0, 0, 0, 0) // distinct, still a TIFF stream
		payloads := map[string]struct{ keyword, name string }{
			"exif": {"Raw profile type exif", "exif"},
			"APP1": {"Raw profile type APP1", "APP1"},
		}
		forms := map[string][]byte{
			"Exif prefix": append([]byte("Exif\x00\x00"), old...),
			"bare TIFF":   old,
		}
		for _, ct := range []string{"tEXt", "zTXt", "iTXt", "iTXtZ"} {
			for pk, pv := range payloads {
				for fk, form := range forms {
					t.Run(ct+"/"+pk+"/"+fk, func(t *testing.T) {
						t.Parallel()
						in := pngWithChunks([2]any{ct, textChunk(ct, pv.keyword, rawProfileHexText(pv.name, form))})
						out := injectAll(t, in, updated, nil, nil)
						if n := countRawProfiles(out, pv.name); n != 0 {
							t.Errorf("%d %q raw profile chunk(s) left after the write, want 0", n, pv.keyword)
						}
						if n := countChunks(out, "eXIf"); n != 1 {
							t.Errorf("%d eXIf chunks, want exactly 1", n)
						}
						gotEXIF, _, _ := extractBytes(t, out)
						if !bytes.Equal(gotEXIF, updated) {
							t.Errorf("EXIF after write = %x, want %x", gotEXIF, updated)
						}
					})
				}
			}
		}
	})
}

// TestInject_RawProfileXMPDropped (PNG-06): an "xmp" raw profile, and an APP1
// raw profile that carries an XMP packet, are dropped when the XMP iTXt chunk
// is written.
func TestInject_RawProfileXMPDropped(t *testing.T) {
	t.Parallel()
	t.Run("PNG-06", func(t *testing.T) {
		t.Parallel()
		oldXMP := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><old/></x:xmpmeta>")
		newXMP := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><new/></x:xmpmeta>")
		cases := []struct {
			name, keyword, profile string
			payload                []byte
		}{
			{"xmp bare", "Raw profile type xmp", "xmp", oldXMP},
			{"xmp with BOM", "Raw profile type xmp", "xmp", append([]byte("\xEF\xBB\xBF"), oldXMP...)},
			{"APP1 XMP", "Raw profile type APP1", "APP1", append(bytes.Clone(identXMPAPP1), oldXMP...)},
		}
		for _, ct := range []string{"tEXt", "zTXt", "iTXt", "iTXtZ"} {
			for _, tc := range cases {
				t.Run(ct+"/"+tc.name, func(t *testing.T) {
					t.Parallel()
					in := pngWithChunks([2]any{ct, textChunk(ct, tc.keyword, rawProfileHexText(tc.profile, tc.payload))})
					out := injectAll(t, in, nil, nil, newXMP)
					if n := countRawProfiles(out, tc.profile); n != 0 {
						t.Errorf("%d %q raw profile chunk(s) left after the write, want 0", n, tc.keyword)
					}
					if n := countChunks(out, "iTXt"); n != 1 {
						t.Errorf("%d iTXt chunks, want exactly 1 (the XMP chunk)", n)
					}
					_, _, gotXMP := extractBytes(t, out)
					if !bytes.Equal(gotXMP, newXMP) {
						t.Errorf("XMP after write = %q, want %q", gotXMP, newXMP)
					}
				})
			}
		}
	})
}

// TestExtract_RawProfileXMP: an "xmp" raw profile is the XMP fallback when no
// iTXt carries XMP, so dropping it on a write loses nothing; a native iTXt
// takes precedence.
func TestExtract_RawProfileXMP(t *testing.T) {
	t.Parallel()
	profXMP := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><prof/></x:xmpmeta>")
	prof := [2]any{"zTXt", textChunk("zTXt", "Raw profile type xmp", rawProfileHexText("xmp", append([]byte("\xEF\xBB\xBF"), profXMP...)))}
	in := pngWithChunks(prof)
	if _, _, got := extractBytes(t, in); !bytes.Equal(got, profXMP) {
		t.Errorf("XMP from the raw profile = %q, want %q (without the byte order mark)", got, profXMP)
	}
	native := []byte("<x:xmpmeta xmlns:x=\"adobe:ns:meta/\"><native/></x:xmpmeta>")
	in = pngWithChunks(prof, [2]any{"iTXt", buildXMPChunk(native)})
	if _, _, got := extractBytes(t, in); !bytes.Equal(got, native) {
		t.Errorf("XMP with both = %q, want the native %q", got, native)
	}
	// A payload that is not XMP is not read as XMP.
	junk := pngWithChunks([2]any{"tEXt", textChunk("tEXt", "Raw profile type xmp", rawProfileHexText("xmp", []byte("not xmp")))})
	if _, _, got := extractBytes(t, junk); got != nil {
		t.Errorf("non-XMP profile read as XMP: %q", got)
	}
}

// TestInject_RawProfileKeptWhenNotSuperseded: a raw profile is dropped only
// when its content is replaced. A nil payload, an undecodable profile, an
// unreadable payload form, a profile of another kind and ordinary text chunks
// are copied through byte-identical (the original CRC included).
func TestInject_RawProfileKeptWhenNotSuperseded(t *testing.T) {
	t.Parallel()
	exifProf := [2]any{"zTXt", textChunk("zTXt", "Raw profile type exif", rawProfileHexText("exif", append([]byte("Exif\x00\x00"), tinyEXIF(t)...)))}
	xmpProf := [2]any{"tEXt", textChunk("tEXt", "Raw profile type xmp", rawProfileHexText("xmp", []byte("<x:xmpmeta/>")))}
	bad := [2]any{"tEXt", textChunk("tEXt", "Raw profile type exif", []byte("\nexif\n      99\nzz\n"))}
	notTIFF := [2]any{"tEXt", textChunk("tEXt", "Raw profile type exif", rawProfileHexText("exif", []byte("garbage!")))}
	other := [2]any{"tEXt", textChunk("tEXt", "Comment", []byte("hello"))}

	both := pngWithChunks(exifProf, xmpProf)
	// No standard chunk is written: both profiles stay.
	if out := injectAll(t, both, nil, nil, nil); !bytes.Equal(out, both) {
		t.Error("a write without EXIF and XMP changed the PNG")
	}
	// Only XMP is written: the EXIF profile stays.
	out := injectAll(t, both, nil, nil, []byte("<x:xmpmeta/>"))
	if countRawProfiles(out, "exif") != 1 || countRawProfiles(out, "xmp") != 0 {
		t.Errorf("XMP-only write: %d exif, %d xmp profiles, want 1 and 0", countRawProfiles(out, "exif"), countRawProfiles(out, "xmp"))
	}
	// Only EXIF is written: the XMP profile stays.
	out = injectAll(t, both, tinyEXIF(t), nil, nil)
	if countRawProfiles(out, "exif") != 0 || countRawProfiles(out, "xmp") != 1 {
		t.Errorf("EXIF-only write: %d exif, %d xmp profiles, want 0 and 1", countRawProfiles(out, "exif"), countRawProfiles(out, "xmp"))
	}
	for name, c := range map[string][2]any{"undecodable": bad, "not a TIFF stream": notTIFF, "ordinary text": other} {
		in := pngWithChunks(c)
		out := injectAll(t, in, tinyEXIF(t), nil, []byte("<x:xmpmeta/>"))
		// Everything but the new eXIf and iTXt chunks is the original chunk, byte for byte.
		if !bytes.Contains(out, in[33:len(in)-12]) {
			t.Errorf("%s: the chunk was not copied through byte-identical", name)
		}
	}
}

// TestInject_RawProfilesAllKinds (task #325 end to end): EXIF, XMP and IPTC
// raw profiles side by side. EXIF and XMP end up only in the standard chunks;
// the IPTC profile is rewritten in place (task #314).
func TestInject_RawProfilesAllKinds(t *testing.T) {
	t.Parallel()
	old := sampleIIM(t)
	updated := newIIM(t, "after")
	in := pngWithChunks(
		[2]any{"zTXt", textChunk("zTXt", "Raw profile type exif", rawProfileHexText("exif", append([]byte("Exif\x00\x00"), tinyEXIF(t)...)))},
		[2]any{"zTXt", textChunk("zTXt", "Raw profile type xmp", rawProfileHexText("xmp", []byte("<x:xmpmeta/>")))},
		[2]any{"zTXt", textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", irbOf(old)))},
	)
	// Read the profiles the way a caller does, then write them back.
	rawEXIF, _, rawXMP := extractBytes(t, in)
	out := injectAll(t, in, rawEXIF, updated, rawXMP)
	if countRawProfiles(out, "exif") != 0 || countRawProfiles(out, "xmp") != 0 || countRawProfiles(out, "iptc") != 1 {
		t.Errorf("profiles after the write: exif %d, xmp %d, iptc %d; want 0, 0, 1",
			countRawProfiles(out, "exif"), countRawProfiles(out, "xmp"), countRawProfiles(out, "iptc"))
	}
	gotEXIF, gotIPTC, gotXMP := extractBytes(t, out)
	if !bytes.Equal(gotEXIF, rawEXIF) || !bytes.Equal(gotXMP, rawXMP) || !bytes.Equal(gotIPTC, updated) {
		t.Error("content changed by the round trip")
	}
}

// FuzzRawProfileSuperseded: rawProfileSuperseded consumes the untrusted
// payload of a text chunk; it must never panic, and a chunk it drops must be
// a profile Extract reads (so its content is carried by the standard chunk).
func FuzzRawProfileSuperseded(f *testing.F) {
	for _, ct := range []string{"tEXt", "zTXt", "iTXt", "iTXtZ"} {
		f.Add(byte(len(ct)), textChunk(ct, "Raw profile type exif", rawProfileHexText("exif", append([]byte("Exif\x00\x00"), []byte("II*\x00\x08\x00\x00\x00")...)))) //nolint:gosec // G115: a seed selector of at most four
		f.Add(byte(len(ct)), textChunk(ct, "Raw profile type xmp", rawProfileHexText("xmp", []byte("<x/>"))))                                                         //nolint:gosec // G115: a seed selector of at most four
	}
	types := [...][4]byte{chunkTEXt, chunkZTXt, chunkITXt}
	f.Fuzz(func(t *testing.T, sel byte, data []byte) {
		ct := types[int(sel)%len(types)]
		if !rawProfileSuperseded(ct, data, true, true) {
			return
		}
		var st extractState
		_ = st.addRawProfile(ct, data)
		if st.profEXIF == nil && st.profXMP == nil {
			t.Fatalf("dropped a raw profile that Extract does not read: %q", data)
		}
	})
}
