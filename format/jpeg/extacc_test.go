package jpeg

// extacc_test.go — task #394 (sprint 50): chunk accumulation and assembly of
// extended XMP (Adobe XMP Specification Part 3 §1.1.3.1, §1.1.4).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

const extAccGUID = "AABBCCDDEEFF00112233445566778899"

// extBody builds the payload of an extended XMP APP1 segment after the
// identifier: GUID, declared full length, offset and chunk data.
func extBody(guid string, fullLen, offset uint32, data []byte) []byte {
	b := make([]byte, 0, 40+len(data))
	b = append(b, guid...)
	b = binary.BigEndian.AppendUint32(b, fullLen)
	b = binary.BigEndian.AppendUint32(b, offset)
	return append(b, data...)
}

type extPiece struct {
	fullLen, offset uint32
	data            string
}

func accFrom(src io.Seeker, pieces ...extPiece) *extAcc {
	acc := &extAcc{src: src}
	for _, p := range pieces {
		acc.add(extBody(extAccGUID, p.fullLen, p.offset, []byte(p.data)))
	}
	return acc
}

func TestExtAcc_Assemble(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		pieces []extPiece
		want   string
		valid  bool
	}{
		{"single", []extPiece{{10, 0, "0123456789"}}, "0123456789", true},
		{"in order", []extPiece{{10, 0, "0123"}, {10, 4, "4567"}, {10, 8, "89"}}, "0123456789", true},
		{"reverse order", []extPiece{{10, 8, "89"}, {10, 4, "4567"}, {10, 0, "0123"}}, "0123456789", true},
		{"shuffled", []extPiece{{10, 4, "4567"}, {10, 0, "0123"}, {10, 8, "89"}}, "0123456789", true},
		{"duplicate chunk", []extPiece{{8, 0, "0123"}, {8, 0, "0123"}}, "", false},
		{"duplicate chunk with declared total of one copy", []extPiece{{4, 0, "0123"}, {4, 0, "0123"}}, "", false},
		{"overlap", []extPiece{{10, 0, "012345"}, {10, 4, "456789"}}, "", false},
		{"gap", []extPiece{{10, 0, "0123"}, {10, 6, "6789"}}, "", false},
		{"first chunk not at zero", []extPiece{{4, 2, "0123"}}, "", false},
		{"declared length larger than the data", []extPiece{{20, 0, "0123"}, {20, 4, "4567"}}, "", false},
		{"declared length smaller than the data", []extPiece{{6, 0, "0123"}, {6, 4, "4567"}}, "", false},
		{"declared length zero", []extPiece{{0, 0, "0123"}}, "", false},
		{"declared length from the first chunk wins", []extPiece{{8, 0, "0123"}, {99, 4, "4567"}}, "01234567", true},
		{"empty chunk first", []extPiece{{4, 0, ""}, {4, 0, "0123"}}, "0123", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Exercise both the bounded (seekable) and the unbounded source.
			for _, src := range []io.Seeker{bytes.NewReader(make([]byte, 4096)), nil} {
				acc := accFrom(src, tc.pieces...)
				g := acc.guids[extAccGUID]
				if g == nil {
					t.Fatal("no chunk was accumulated")
				}
				got, ok := g.assemble()
				if ok != tc.valid || (ok && string(got) != tc.want) {
					t.Fatalf("assemble() = %q, %v; want %q, %v", got, ok, tc.want, tc.valid)
				}
			}
		})
	}
}

// TestExtAcc_GUIDCase covers #135: the chunks of one GUID written with
// different letter case join one document.
func TestExtAcc_GUIDCase(t *testing.T) {
	t.Parallel()
	acc := &extAcc{}
	acc.add(extBody(extAccGUID, 8, 0, []byte("0123")))
	acc.add(extBody(strings.ToLower(extAccGUID), 8, 4, []byte("4567")))
	if len(acc.guids) != 1 {
		t.Fatalf("GUIDs = %d, want 1", len(acc.guids))
	}
	got, ok := acc.guids[extAccGUID].assemble()
	if !ok || string(got) != "01234567" {
		t.Fatalf("assemble() = %q, %v", got, ok)
	}
}

// TestExtAcc_DeclaredLengthDoesNotDriveAllocation covers the DoS guard of the
// pre-sizing: a header that declares the largest accepted document must not
// make the reader allocate more than the file can hold, or than
// maxUnboundedPrealloc when the size of the source is unknown.
func TestExtAcc_DeclaredLengthDoesNotDriveAllocation(t *testing.T) {
	t.Parallel()
	const chunk = "0123456789"
	t.Run("seekable", func(t *testing.T) {
		t.Parallel()
		acc := accFrom(bytes.NewReader(make([]byte, 1000)), extPiece{maxExtendedXMPTotal, 0, chunk})
		g := acc.guids[extAccGUID]
		if got := cap(g.block); got > 1000+len(chunk) {
			t.Fatalf("block capacity = %d for a 1000-byte source, want <= %d", got, 1000+len(chunk))
		}
		if _, ok := g.assemble(); ok {
			t.Fatal("a document shorter than its declared length was accepted")
		}
	})
	t.Run("unknown size", func(t *testing.T) {
		t.Parallel()
		acc := accFrom(nil, extPiece{maxExtendedXMPTotal, 0, chunk})
		if got := cap(acc.guids[extAccGUID].block); got > maxUnboundedPrealloc {
			t.Fatalf("block capacity = %d, want <= %d", got, maxUnboundedPrealloc)
		}
	})
	t.Run("over the cap", func(t *testing.T) {
		t.Parallel()
		acc := accFrom(nil, extPiece{maxExtendedXMPTotal + 1, 0, chunk})
		if len(acc.guids) != 0 || !acc.truncated {
			t.Fatalf("GUIDs = %d, truncated = %v; want 0, true", len(acc.guids), acc.truncated)
		}
	})
	t.Run("data larger than declared", func(t *testing.T) {
		t.Parallel()
		// Declared 5 bytes, 60 delivered in 6 chunks: every chunk lands in a
		// block of its own size, nothing is lost and nothing panics.
		pieces := make([]extPiece, 0, 6)
		for i := range 6 {
			pieces = append(pieces, extPiece{5, uint32(i * 10), chunk})
		}
		g := accFrom(bytes.NewReader(make([]byte, 1000)), pieces...).guids[extAccGUID]
		if g.size != 60 || len(g.chunks) != 6 {
			t.Fatalf("size = %d, chunks = %d; want 60, 6", g.size, len(g.chunks))
		}
		for i, c := range g.chunks {
			if string(c.data) != chunk {
				t.Fatalf("chunk %d = %q, want %q", i, c.data, chunk)
			}
		}
	})
}

// seekFail is a seekable source whose n-th Seek call fails.
type seekFail struct {
	*bytes.Reader
	calls, failAt int
}

var errSeekFail = errors.New("seek failed")

func (s *seekFail) Seek(off int64, whence int) (int64, error) {
	s.calls++
	if s.calls == s.failAt {
		return 0, errSeekFail
	}
	return s.Reader.Seek(off, whence) //nolint:wrapcheck // test double
}

// largeExtendedXMP returns a standard packet that links to GUID guid and an
// extended document of about n bytes: one rdf:Description with a long value.
func largeExtendedXMP(guid string, n int) (std, ext []byte) {
	std = []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmpNote="http://ns.adobe.com/xmp/note/"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/" xmpNote:HasExtendedXMP="` + guid + `" dc:format="image/jpeg"/>` +
		`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`)
	ext = []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/">` +
		`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:ex="http://example.com/ns/" ex:blob="` +
		strings.Repeat("QUJD", n/4) + `"/></rdf:RDF></x:xmpmeta>`)
	return std, ext
}

func chunkDescriptors(ext []byte, size int) []struct {
	fullLen uint32
	offset  uint32
	data    []byte
} {
	var out []struct {
		fullLen uint32
		offset  uint32
		data    []byte
	}
	for off := 0; off < len(ext); off += size {
		end := min(off+size, len(ext))
		out = append(out, struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}{uint32(len(ext)), uint32(off), ext[off:end]}) //nolint:gosec // G115: test sizes
	}
	return out
}

// TestExtAcc_WireFrameAndOrder checks, end to end, that the wire-frame and
// the reassembled packet do not depend on how the chunks were stored: in
// order (assembled in place, wire-frame built in the block), reversed and
// shuffled (copied), for a document under and over the size from which the
// block reserves the headroom of the frame.
func TestExtAcc_WireFrameAndOrder(t *testing.T) {
	t.Parallel()
	for _, size := range []int{50_000, wireHeadroomMin + 100_000} {
		std, ext := largeExtendedXMP(extAccGUID, size)
		descs := chunkDescriptors(ext, 16_000)
		if len(descs) < 2 {
			t.Fatalf("size %d: fixture has %d chunks, want several", size, len(descs))
		}
		reversed := make([]struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}, len(descs))
		for i, d := range descs {
			reversed[len(descs)-1-i] = d
		}
		wantWire := encodeXMPWire(std, ext)
		var wantXMP []byte
		for name, order := range map[string][]struct {
			fullLen uint32
			offset  uint32
			data    []byte
		}{"in order": descs, "reversed": reversed} {
			j := buildExtChunkJPEG(std, extAccGUID, order)
			_, _, rawXMP, wire, err := ExtractWithWire(bytes.NewReader(j))
			if err != nil {
				t.Fatalf("size %d %s: ExtractWithWire: %v", size, name, err)
			}
			if !bytes.Equal(wire, wantWire) {
				t.Errorf("size %d %s: wire-frame differs from encodeXMPWire (len %d, want %d)", size, name, len(wire), len(wantWire))
			}
			if main, got, ok := decodeXMPWire(wire); !ok || !bytes.Equal(main, std) || !bytes.Equal(got, ext) {
				t.Errorf("size %d %s: wire-frame does not decode to the standard packet and the extended document", size, name)
			}
			if !bytes.Contains(rawXMP, []byte(`blob`)) || bytes.Contains(rawXMP, []byte(`HasExtendedXMP`)) {
				t.Errorf("size %d %s: reassembled packet lacks the extended property or keeps the link", size, name)
			}
			if wantXMP == nil {
				wantXMP = rawXMP
			} else if !bytes.Equal(rawXMP, wantXMP) {
				t.Errorf("size %d %s: reassembled packet depends on the chunk order", size, name)
			}
		}
	}
}

// TestExtAcc_SeekFailure covers the source that cannot report its size: a
// failure of the size query is tolerated, a failure to restore the position
// is reported, because the scan could no longer continue from the right byte.
func TestExtAcc_SeekFailure(t *testing.T) {
	t.Parallel()
	std, ext := largeExtendedXMP(extAccGUID, 20_000)
	j := buildExtChunkJPEG(std, extAccGUID, chunkDescriptors(ext, 8_000))
	// Seek calls: 1 = rewind, 2 = current position, 3 = end, 4 = restore.
	t.Run("size query fails", func(t *testing.T) {
		t.Parallel()
		_, _, rawXMP, _, err := ExtractWithWire(&seekFail{Reader: bytes.NewReader(j), failAt: 3})
		if err != nil {
			t.Fatalf("ExtractWithWire: %v", err)
		}
		if !bytes.Contains(rawXMP, []byte(`blob`)) {
			t.Error("extended XMP was lost")
		}
	})
	t.Run("restore fails", func(t *testing.T) {
		t.Parallel()
		_, _, _, _, err := ExtractWithWire(&seekFail{Reader: bytes.NewReader(j), failAt: 4})
		if !errors.Is(err, errSeekFail) {
			t.Fatalf("ExtractWithWire error = %v, want %v", err, errSeekFail)
		}
	})
}

// BenchmarkExtractExtendedXMP measures the extraction of a 1 MiB extended XMP
// document in 16 chunks, in order. The extended document must be copied once
// (B/op close to its size, plus the reassembly).
func BenchmarkExtractExtendedXMP(b *testing.B) {
	std, ext := largeExtendedXMP(extAccGUID, 1<<20)
	j := buildExtChunkJPEG(std, extAccGUID, chunkDescriptors(ext, 65_000))
	r := bytes.NewReader(j)
	b.ReportAllocs()
	b.SetBytes(int64(len(j)))
	for b.Loop() {
		_, _ = r.Seek(0, io.SeekStart)
		if _, _, _, _, err := ExtractWithWire(r); err != nil {
			b.Fatal(err)
		}
	}
}
