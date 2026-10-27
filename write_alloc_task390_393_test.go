//go:build !race

package gometadata

// write_alloc_task390_393_test.go — the acceptance bounds of tasks #390-#393:
// Write and the container-level Inject allocate memory proportional to the
// metadata, not to the file. Excluded under the race detector, which makes
// sync.Pool drop buffers at random and so allocates more than the library does.

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format/heif"
	"github.com/FlavioCFOliveira/GoMetadata/format/raw/orf"
	"github.com/FlavioCFOliveira/GoMetadata/format/webp"
)

// allocPerOp returns the bytes allocated per call of fn, averaged over n calls
// after one warm-up call (which fills the buffer pools).
func allocPerOp(n int, fn func()) uint64 {
	fn()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range n {
		fn()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(n) //nolint:gosec // G115: n is a small positive constant
}

// boxBytes returns the total size of the top-level boxes of data with the type.
func boxBytes(data []byte, typ string) int {
	total := 0
	for pos := 0; pos+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[pos:]))
		if size == 0 || size > len(data)-pos {
			size = len(data) - pos
		}
		if size < 8 {
			break
		}
		if string(data[pos+4:pos+8]) == typ {
			total += size
		}
		pos += size
	}
	return total
}

// TestWriteAllocationBounds: write allocation is at most the size of the
// metadata structures that are held, plus 64 KiB for the pooled buffers and
// the writer's own bookkeeping. The metadata size M of each case is stated
// where it is computed. Task #393's JPEG bound is the 256 KiB of its criterion.
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestWriteAllocationBounds(t *testing.T) {
	const slack = 64 << 10
	for _, c := range writeBenchCases() {
		data, err := os.ReadFile(c.path)
		if err != nil {
			t.Logf("%s: corpus file absent, skipped", c.name)
			continue
		}
		m, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: Read: %v", c.name, err)
		}
		// M: the structures the write holds. HEIF: the meta and moov boxes and the
		// payloads. WebP and JPEG: the payloads. ORF and RW2: the metadata prefix
		// (which Read already holds in m: Write allocates the relocated header
		// that is built from it).
		payloads := len(m.RawEXIF()) + len(m.RawIPTC()) + len(m.RawXMP())
		bound := uint64(payloads + slack)
		switch c.name {
		case "HEIC-IMG_2928", "HEIC-IMG_2929":
			bound += uint64(boxBytes(data, "meta") + boxBytes(data, "moov")) //nolint:gosec // G115: lengths are non-negative
		case "JPEG-samsung-s9":
			bound = 256 << 10
		}
		m.SetCaption("bench")
		var out bytes.Buffer
		out.Grow(len(data) + 1<<16)
		r := bytes.NewReader(data)
		got := allocPerOp(20, func() {
			out.Reset()
			_, _ = r.Seek(0, io.SeekStart)
			if err := Write(r, &out, m); err != nil {
				t.Fatalf("%s: Write: %v", c.name, err)
			}
		})
		t.Logf("%-18s file %9d B  write %8d B/op  bound %8d", c.name, len(data), got, bound)
		if got > bound {
			t.Errorf("%s: Write allocates %d B/op for a %d-byte file, want at most %d (metadata %d + %d)", c.name, got, len(data), bound, bound-slack, slack)
		}
	}
}

// TestInjectAllocationBounds is TestWriteAllocationBounds for the container
// level Inject. HEIF and WebP hold the same structures as Write; ORF reads the
// metadata prefix of the file (M) and builds the relocated header from it
// (another M), so its bound is 2M.
//
//nolint:paralleltest // measures the heap of the process: no other test may allocate meanwhile
func TestInjectAllocationBounds(t *testing.T) {
	const slack = 64 << 10
	xmp := []byte(`<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="w"?>`)
	cases := writeBenchCases()
	for _, tc := range []struct {
		name   string
		path   string
		inject func(io.ReadSeeker, io.Writer, []byte, []byte, []byte, bool) error
		bound  func(data []byte) uint64
	}{
		{"HEIC-IMG_2928", cases[0].path, heif.Inject, func(d []byte) uint64 {
			return uint64(boxBytes(d, "meta") + boxBytes(d, "moov") + len(xmp) + slack) //nolint:gosec // G115: lengths are non-negative
		}},
		{"WebP-VP8X", cases[2].path, webp.Inject, func([]byte) uint64 { return uint64(len(xmp) + slack) }},
		{"ORF-TG-4", cases[4].path, orf.Inject, func(d []byte) uint64 {
			prefix, _, _, err := orf.Extract(bytes.NewReader(d))
			if err != nil {
				t.Fatal(err)
			}
			return uint64(2*len(prefix) + slack)
		}},
	} {
		data, err := os.ReadFile(tc.path)
		if err != nil {
			t.Logf("%s: corpus file absent, skipped", tc.name)
			continue
		}
		var out bytes.Buffer
		out.Grow(len(data) + 1<<16)
		r := bytes.NewReader(data)
		got := allocPerOp(20, func() {
			out.Reset()
			_, _ = r.Seek(0, io.SeekStart)
			if err := tc.inject(r, &out, nil, nil, xmp, true); err != nil {
				t.Fatalf("%s: Inject: %v", tc.name, err)
			}
		})
		bound := tc.bound(data)
		t.Logf("%-18s file %9d B  inject %8d B/op  bound %8d", tc.name, len(data), got, bound)
		if got > bound {
			t.Errorf("%s: Inject allocates %d B/op for a %d-byte file, want at most %d", tc.name, got, len(data), bound)
		}
	}
}
