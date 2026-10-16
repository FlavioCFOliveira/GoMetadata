package png

// rawprofile_task314_test.go — regression tests for task #314: an IPTC write
// rewrites an existing "Raw profile type iptc" chunk, so it never keeps the
// old IIM next to the new one; a write without IPTC, or with the IIM the chunk
// already holds, copies the chunk through byte-identical.

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: MD5 is the MWG IPTC digest algorithm, not used for security
	"encoding/binary"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/irb"
	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

func newIIM(t *testing.T, caption string) []byte {
	t.Helper()
	i := &iptc.IPTC{}
	i.SetCaption(caption)
	out, err := iptc.Encode(i)
	if err != nil {
		t.Fatalf("iptc.Encode: %v", err)
	}
	return out
}

// injectPNG writes data through Inject with rawIPTC (and no EXIF/XMP).
func injectPNG(t *testing.T, data, rawIPTC []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, nil, rawIPTC, nil, true); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return out.Bytes()
}

// TestInject_RawProfileIPTCRewritten covers every chunk type and payload
// shape the reader accepts (TestExtract_RawProfileIPTC).
func TestInject_RawProfileIPTCRewritten(t *testing.T) {
	t.Parallel()
	old := sampleIIM(t)
	updated := newIIM(t, "rewritten caption")
	payloads := map[string][]byte{
		"irb":           irbOf(old),
		"photoshop irb": append([]byte("Photoshop 3.0\x00"), irbOf(old)...),
		"bare iim":      old,
	}
	for _, ct := range []string{"tEXt", "zTXt", "iTXt", "iTXtZ"} {
		for shape, p := range payloads {
			t.Run(ct+"/"+shape, func(t *testing.T) {
				t.Parallel()
				in := pngWithChunks([2]any{ct, textChunk(ct, "Raw profile type iptc", rawProfileHexText("IPTC profile", p))})
				out := injectPNG(t, in, updated)
				_, got, _ := extractBytes(t, out)
				if !bytes.Equal(got, updated) {
					t.Fatalf("IIM after write = %x, want %x", got, updated)
				}
				prof := profileOf(t, out)
				if shape != "bare iim" && !bytes.Contains(prof, []byte("8BIM\x04\x0C\x00\x00\x00\x00\x00\x04thmb")) {
					t.Errorf("the 0x040C resource of the IRB was not kept: %x", prof)
				}
				if shape == "photoshop irb" && !bytes.HasPrefix(prof, identPhotoshop) {
					t.Error("the \"Photoshop 3.0\" identifier was not kept")
				}
				if n := bytes.Count(out, []byte("Raw profile type iptc")); n != 1 {
					t.Errorf("%d raw profile chunks after the write, want 1", n)
				}
			})
		}
	}
}

// profileOf returns the decoded profile of the first "Raw profile type iptc"
// chunk of the PNG data.
func profileOf(t *testing.T, data []byte) []byte {
	t.Helper()
	for pos := 8; pos+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[pos:]))
		var ct [4]byte
		copy(ct[:], data[pos+4:pos+8])
		payload := data[pos+8 : pos+8+n]
		if kind, kw := isRawProfileKeyword(payload); kind == rawProfileIPTC {
			text, ok := rawProfileText(ct, payload, kw)
			if !ok {
				t.Fatal("raw profile chunk unreadable")
			}
			return decodeRawProfile(text)
		}
		pos += 12 + n
	}
	t.Fatal("no raw profile chunk")
	return nil
}

// TestInject_RawProfileIPTCUnchanged: no IIM, or the IIM the chunk already
// holds, leaves the PNG byte-identical (the original CRC is kept).
func TestInject_RawProfileIPTCUnchanged(t *testing.T) {
	t.Parallel()
	old := sampleIIM(t)
	in := pngWithChunks([2]any{"zTXt", textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", irbOf(old)))})
	if out := injectPNG(t, in, nil); !bytes.Equal(out, in) {
		t.Error("a write without IPTC changed the PNG")
	}
	if out := injectPNG(t, in, old); !bytes.Equal(out, in) {
		t.Error("a write of the same IIM changed the PNG")
	}
	// A malformed profile is preserved, never repaired.
	bad := pngWithChunks([2]any{"tEXt", textChunk("tEXt", "Raw profile type iptc", []byte("\nIPTC profile\n      99\nzz\n"))})
	if out := injectPNG(t, bad, newIIM(t, "x")); !bytes.Equal(out, bad) {
		t.Error("a malformed raw profile chunk was modified")
	}
	// Other text chunks are untouched.
	other := pngWithChunks([2]any{"tEXt", textChunk("tEXt", "Comment", []byte("hello"))})
	if out := injectPNG(t, other, newIIM(t, "x")); !bytes.Equal(out, other) {
		t.Error("a non-profile text chunk was modified")
	}
}

// TestInject_RawProfileIPTCDigest (task #323; MWG Guidelines v2.0 §4.2.3.2,
// p. 29): when the PNG carries XMP as well, a raw profile in the Photoshop IRB
// form gets the IPTC digest of the IIM written, created when absent and
// refreshed when present (a stale one included), also when the IIM is
// unchanged; without XMP the profile is left as it is.
func TestInject_RawProfileIPTCDigest(t *testing.T) {
	t.Parallel()
	old := sampleIIM(t)
	updated := newIIM(t, "rewritten caption")
	sum := func(b []byte) []byte { s := md5.Sum(b); return s[:] } //nolint:gosec // G401: MWG digest
	stale := bytes.Repeat([]byte{0x5A}, 16)
	xmpPacket := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	withDigest := func(digest []byte) []byte {
		var b []byte
		if digest != nil {
			b = irb.AppendBlock(b, irb.ResourceIPTCDigest, digest)
		}
		return irb.AppendBlock(b, irb.ResourceIPTC, old)
	}
	cases := []struct {
		name       string
		digest     []byte
		iim        []byte
		xmp        []byte
		wantDigest []byte
	}{
		{"RECONCILE-06 absent digest is created", nil, updated, xmpPacket, sum(updated)},
		{"RECONCILE-06 matching digest is refreshed", sum(old), updated, xmpPacket, sum(updated)},
		{"RECONCILE-06 stale digest is refreshed", stale, updated, xmpPacket, sum(updated)},
		{"RECONCILE-06 stale digest of an unchanged IIM is refreshed", stale, old, xmpPacket, sum(old)},
		{"RECONCILE-06 no XMP: stale digest kept as it is", stale, updated, nil, stale},
		{"RECONCILE-06 no XMP: no digest created", nil, updated, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := pngWithChunks([2]any{"zTXt", textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", withDigest(tc.digest)))})
			var out bytes.Buffer
			if err := Inject(bytes.NewReader(in), &out, nil, tc.iim, tc.xmp, true); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			iim, digest := irb.IPTCAndDigest(profileOf(t, out.Bytes()))
			if !bytes.Equal(iim, tc.iim) {
				t.Errorf("IIM = %x, want %x", iim, tc.iim)
			}
			if !bytes.Equal(digest, tc.wantDigest) {
				t.Errorf("digest = %x, want %x", digest, tc.wantDigest)
			}
		})
	}
}

// TestAppendRawProfileText pins the ImageMagick text layout the writer emits
// (the layout ExifTool writes: name line, 8-column length, 72 hex digits per
// line), which rawProfileHexText (the reader-side fixture builder) also uses.
func TestAppendRawProfileText(t *testing.T) {
	t.Parallel()
	profile := bytes.Repeat([]byte{0xAB}, 40)
	got := appendRawProfileText(nil, []byte("\nIPTC profile\n       1\n00\n"), profile)
	if want := rawProfileHexText("IPTC profile", profile); !bytes.Equal(got, want) {
		t.Errorf("text =\n%q\nwant\n%q", got, want)
	}
}

// BenchmarkPNGInjectRawProfileIPTC measures a write that rewrites a raw
// profile IPTC chunk.
func BenchmarkPNGInjectRawProfileIPTC(b *testing.B) {
	enc := func(caption string) []byte {
		i := &iptc.IPTC{}
		i.SetCaption(caption)
		out, err := iptc.Encode(i)
		if err != nil {
			b.Fatal(err)
		}
		return out
	}
	old, updated := enc("old caption"), enc("rewritten caption")
	in := pngWithChunks([2]any{"zTXt", textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", irbOf(old)))})
	var out bytes.Buffer
	b.ReportAllocs()
	for range b.N {
		out.Reset()
		if err := Inject(bytes.NewReader(in), &out, nil, updated, nil, true); err != nil {
			b.Fatal(err)
		}
	}
}

// FuzzRewriteRawProfileIPTC (task #314): rewriteRawProfileIPTC consumes the
// untrusted payload of a text chunk. It must never panic, and whenever it
// rewrites a chunk the result must read back, through the same decoder
// Extract uses, as exactly the new IIM.
func FuzzRewriteRawProfileIPTC(f *testing.F) {
	iim := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 'o', 'l', 'd'}
	for _, ct := range []string{"tEXt", "zTXt", "iTXt", "iTXtZ"} {
		for _, p := range [][]byte{irbOf(iim), append([]byte("Photoshop 3.0\x00"), irbOf(iim)...), iim} {
			f.Add(byte(len(ct)+len(p)), textChunk(ct, "Raw profile type iptc", rawProfileHexText("IPTC profile", p))) //nolint:gosec // G115: a seed selector; wrapping is harmless
		}
	}
	f.Add(byte(0), []byte("Raw profile type iptc\x00\nIPTC profile\n 4294967295\n00\n"))
	types := [...][4]byte{chunkTEXt, chunkZTXt, chunkITXt}
	newIIM := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'f', 'u', 'z', 'z', '!'}
	f.Fuzz(func(t *testing.T, sel byte, data []byte) {
		ct := types[int(sel)%len(types)]
		payload, ok, err := rewriteRawProfileIPTC(ct, data, newIIM, sel%2 == 0)
		if err != nil || !ok {
			return
		}
		kind, kw := isRawProfileKeyword(payload)
		if kind != rawProfileIPTC {
			t.Fatalf("rewritten chunk lost its keyword: %q", payload)
		}
		text, tok := rawProfileText(ct, payload, kw)
		if !tok {
			t.Fatal("rewritten chunk is unreadable")
		}
		if got := profileIPTC(decodeRawProfile(text)); !bytes.Equal(got, newIIM) {
			t.Fatalf("rewritten chunk reads %x, want %x", got, newIIM)
		}
	})
}
