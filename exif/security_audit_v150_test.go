package exif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// Security audit 2026-10-16 (v1.5.0 pre-clearance), findings F-06a and F-06b.
// Sub-test names are the finding IDs.

// auditThumbChain builds a little-endian TIFF stream of nifd chained IFDs (the
// first is IFD0), each holding JPEGInterchangeFormat/Length (0x0201/0x0202).
// thumbAt(k) gives the offset of the thumbnail of IFD k, relative to the end
// of the IFDs; thumbLen is the length of each. The thumbnail area is
// area bytes long.
func auditThumbChain(nifd, area int, thumbAt func(k int) int, thumbLen int) []byte {
	le := binary.LittleEndian
	const ifdSize = 2 + 2*12 + 4
	base := 8 + nifd*ifdSize
	b := make([]byte, base+area)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], 8)
	for k := range nifd {
		p := 8 + k*ifdSize
		le.PutUint16(b[p:], 2)
		e := p + 2
		le.PutUint16(b[e:], 0x0201)
		le.PutUint16(b[e+2:], 4)
		le.PutUint32(b[e+4:], 1)
		le.PutUint32(b[e+8:], uint32(base+thumbAt(k))) //nolint:gosec // G115: test sizes are small
		e += 12
		le.PutUint16(b[e:], 0x0202)
		le.PutUint16(b[e+2:], 4)
		le.PutUint32(b[e+4:], 1)
		le.PutUint32(b[e+8:], uint32(thumbLen)) //nolint:gosec // G115: test sizes are small
		next := uint32(0)
		if k < nifd-1 {
			next = uint32(8 + (k+1)*ifdSize)
		}
		le.PutUint32(b[p+2+24:], next)
	}
	for i := base; i < len(b); i++ {
		b[i] = byte(i & 0xFF)
	}
	return b
}

func TestSecurityAuditV150(t *testing.T) {
	t.Parallel()

	// F-06a: 500 chained IFDs addressing one 1 MiB range made Encode write the
	// range 500 times (a 1 MiB EXIF re-encoded to 499 MiB).
	t.Run("F-06a", func(t *testing.T) {
		t.Parallel()
		const thumb = 1 << 20
		in := auditThumbChain(500, thumb, func(int) int { return 0 }, thumb)
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		kept := 0
		for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
			if ifd.ThumbnailData != nil {
				kept++
			}
		}
		// IFD0 (never written by Encode, not charged) and IFD1 keep theirs.
		if kept != 2 {
			t.Errorf("%d IFDs kept a thumbnail, want 2", kept)
		}
		dropped := 0
		for _, w := range e.Warnings {
			if strings.Contains(w, "thumbnail of 1048576 bytes") {
				dropped++
			}
		}
		if dropped != 498 {
			t.Errorf("%d thumbnail warnings, want 498", dropped)
		}
		out, err := Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if len(out) > len(in)+64<<10 {
			t.Errorf("Encode wrote %d bytes for %d bytes of input (limit input+64 KiB)", len(out), len(in))
		}
		out2, err := EncodeRebased(e, in)
		if err != nil {
			t.Fatalf("EncodeRebased: %v", err)
		}
		if len(out2) > len(in)+64<<10 {
			t.Errorf("EncodeRebased wrote %d bytes for %d bytes of input", len(out2), len(in))
		}
	})

	// F-06a: IFD0 and IFD1 sharing one range (IFD0's thumbnail is never
	// written) must not cost IFD1 its thumbnail: it round-trips byte-exactly,
	// and a later IFD repeating the range is the one dropped.
	t.Run("F-06a-repeat", func(t *testing.T) {
		t.Parallel()
		const thumb = 4096
		in := auditThumbChain(3, thumb, func(int) int { return 0 }, thumb) // IFD0, IFD1, IFD2: one range
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		want := e.IFD0.Next.ThumbnailData
		if len(want) != thumb {
			t.Fatalf("IFD1 thumbnail after Parse: %d bytes, want %d", len(want), thumb)
		}
		out, err := Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		e2, err := Parse(out)
		if err != nil {
			t.Fatalf("Parse(Encode): %v", err)
		}
		if !bytes.Equal(e2.IFD0.Next.ThumbnailData, in[len(in)-thumb:]) {
			t.Errorf("IFD1 thumbnail changed in the round trip (%d bytes)", len(e2.IFD0.Next.ThumbnailData))
		}
		if len(out) > len(in)+64 {
			t.Errorf("Encode wrote %d bytes for %d bytes of input", len(out), len(in))
		}
	})

	// F-06a: valid thumbnails (disjoint ranges) are never dropped, wherever the
	// IFDs are in the chain.
	t.Run("F-06a-valid", func(t *testing.T) {
		t.Parallel()
		const thumb = 4096
		in := auditThumbChain(3, 3*thumb, func(k int) int { return k * thumb }, thumb)
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		n := 0
		for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
			if len(ifd.ThumbnailData) != thumb {
				t.Errorf("IFD %d: thumbnail of %d bytes, want %d", n, len(ifd.ThumbnailData), thumb)
			}
			n++
		}
		for _, w := range e.Warnings {
			if strings.Contains(w, "thumbnail of") {
				t.Errorf("unexpected warning %q", w)
			}
		}
	})

	// F-06b: 40 IFD0 entries of type IFD addressing one private IFD with 1 MiB
	// of values made EncodeRebased carry the values 40 times (15 MiB -> 639 MiB
	// at full size).
	t.Run("F-06b", func(t *testing.T) {
		t.Parallel()
		const dataLen = 1<<20 - 1024
		in := auditPrivateIFDs(40, func(int) (int, int) { return 0, dataLen }, dataLen)
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		out, err := EncodeRebased(e, in)
		if !errors.Is(err, ErrCarriedDataTooLarge) {
			t.Fatalf("EncodeRebased error = %v (%d bytes), want ErrCarriedDataTooLarge", err, len(out))
		}
		// One such entry is a valid stream: it is carried once.
		in1 := auditPrivateIFDs(1, func(int) (int, int) { return 0, dataLen }, dataLen)
		e1, err := Parse(in1)
		if err != nil {
			t.Fatalf("Parse(1): %v", err)
		}
		out1, err := EncodeRebased(e1, in1)
		if err != nil {
			t.Fatalf("EncodeRebased(1): %v", err)
		}
		if len(out1) > 2*len(in1) {
			t.Errorf("EncodeRebased wrote %d bytes for %d bytes of input (limit 2x)", len(out1), len(in1))
		}
	})

	// F-06b: private IFDs with disjoint data are all carried.
	t.Run("F-06b-valid", func(t *testing.T) {
		t.Parallel()
		const each = 2048
		in := auditPrivateIFDs(5, func(i int) (int, int) { return i * each, each }, 5*each)
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		out, err := EncodeRebased(e, in)
		if err != nil {
			t.Fatalf("EncodeRebased: %v", err)
		}
		if len(out) < len(in) {
			t.Errorf("EncodeRebased wrote %d bytes, fewer than the %d of the input: data was lost", len(out), len(in))
		}
	})

	// F-06b: more private IFDs than a real file has are refused.
	t.Run("F-06b-count", func(t *testing.T) {
		t.Parallel()
		in := auditPrivateIFDs(maxPrivateIFDsCarried+1, func(i int) (int, int) { return i * 8, 8 }, (maxPrivateIFDsCarried+1)*8)
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if _, err := EncodeRebased(e, in); !errors.Is(err, ErrCarriedDataTooLarge) {
			t.Errorf("EncodeRebased error = %v, want ErrCarriedDataTooLarge", err)
		}
	})
}

// auditPrivateIFDs builds a little-endian TIFF stream whose IFD0 holds n
// entries of type IFD (tags 0xC000+i), each addressing its own private IFD,
// which holds one UNDEFINED entry whose value is the range of the data area
// that dataOf(i) gives (offset, length). The private IFDs are the same one
// when dataOf returns the same range and IFD for every i is NOT shared: each
// entry has its own IFD at its own offset.
func auditPrivateIFDs(n int, dataOf func(i int) (off, length int), area int) []byte {
	le := binary.LittleEndian
	ifd0Size := 2 + n*12 + 4
	priv := 8 + ifd0Size
	priv += priv & 1
	const privSize = 2 + 12 + 4
	data := priv + n*privSize
	b := make([]byte, data+area)
	copy(b, "II*\x00")
	le.PutUint32(b[4:], 8)
	le.PutUint16(b[8:], uint16(n)) //nolint:gosec // G115: test sizes are small
	for i := range n {
		e := 8 + 2 + i*12
		le.PutUint16(b[e:], uint16(0xC000+i))
		le.PutUint16(b[e+2:], 13)
		le.PutUint32(b[e+4:], 1)
		p := priv + i*privSize
		le.PutUint32(b[e+8:], uint32(p)) //nolint:gosec // G115: test sizes are small
		off, length := dataOf(i)
		le.PutUint16(b[p:], 1)
		le.PutUint16(b[p+2:], 0x0001)
		le.PutUint16(b[p+4:], 7)
		le.PutUint32(b[p+6:], uint32(length))    //nolint:gosec // G115: test sizes are small
		le.PutUint32(b[p+10:], uint32(data+off)) //nolint:gosec // G115: test sizes are small
	}
	for i := data; i < len(b); i++ {
		b[i] = byte(i & 0xFF)
	}
	return b
}

// BenchmarkEncodeRebasedAuditV150 measures the shapes of F-06a and F-06b after
// the fixes; both used to write hundreds of megabytes.
func BenchmarkEncodeRebasedAuditV150(b *testing.B) {
	const thumb = 1 << 20
	chain := auditThumbChain(500, thumb, func(int) int { return 0 }, thumb)
	const each = 2048
	priv := auditPrivateIFDs(5, func(i int) (int, int) { return i * each, each }, 5*each)
	for _, tc := range []struct {
		name string
		in   []byte
	}{{"F-06a-chain-500", chain}, {"F-06b-private-5", priv}} {
		e, err := Parse(tc.in)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, err := EncodeRebased(e, tc.in); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
