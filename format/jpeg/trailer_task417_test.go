package jpeg

// trailer_task417_test.go — task #417: Inject of a JPEG whose area after EOI is
// made of trailer signature candidates reads that area once.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// countingSeeker counts the reads, the bytes read and the seeks of a source.
type countingSeeker struct {
	r            io.ReadSeeker
	reads, seeks int
	bytes        int64
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	c.reads++
	n, err := c.r.Read(p)
	c.bytes += int64(n)
	return n, err //nolint:wrapcheck // a test double: the error is the reader's
}
func (c *countingSeeker) Seek(off int64, whence int) (int64, error) {
	c.seeks++
	return c.r.Seek(off, whence) //nolint:wrapcheck // a test double: the error is the seeker's
}

// TestInject_R417_TrailerCandidatesAreLinear: TRAILER-STREAM-05. Inject of a
// JPEG with 4 MiB of AFCP prefixes that are not signatures ("AXSA"), of Samsung
// signatures that are not blocks ("QDIOBS"), and of FotoStation records, reads
// the area a bounded number of times, and copies it unchanged (v1.5.2: 58 ms for
// 16 MiB; the first streaming write: 9 s for 16 MiB held in memory and 20 s from
// a file). The test counts reads, seeks and bytes read, which do not depend on
// the speed of the machine, its load or the race detector; a wall-clock bound
// failed under CPU contention with -race (task #419). A quadratic scan shows as
// a count of reads and bytes that grows with the square of the area.
func TestInject_R417_TrailerCandidatesAreLinear(t *testing.T) {
	t.Parallel()
	const size = 4 << 20
	units := map[string]string{
		"AXSA":   "AXSA",
		"QDIOBS": "QDIOBS",
		"foto":   "\x00\x01\x00\x00\x00\x0a\xa1\xb2\xc3\xd4",
	}
	for name, unit := range units {
		tail := bytes.Repeat([]byte(unit), size/len(unit))
		src := jpegWithTail(tail)
		path := filepath.Join(t.TempDir(), name+".jpg")
		if err := os.WriteFile(path, src, 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		for label, r := range map[string]io.ReadSeeker{"file": f, "memory": bytes.NewReader(src)} {
			cr := &countingSeeker{r: r}
			var out bytes.Buffer
			if err := Inject(cr, &out, nil, newIIM, nil, true); err != nil {
				t.Fatalf("%s/%s: Inject: %v", name, label, err)
			}
			// Measured: 4.01x (the scan passes and the copy); a quadratic scan reads orders of magnitude more.
			if cr.bytes > 8*int64(len(src)) {
				t.Errorf("%s/%s: %d bytes read for a source of %d, want at most 8x", name, label, cr.bytes, len(src))
			}
			if limit := 16 * (size/(32<<10) + 16); cr.reads > limit || cr.seeks > limit {
				t.Errorf("%s/%s: %d reads and %d seeks, want at most %d of each", name, label, cr.reads, cr.seeks, limit)
			}
			if name != "foto" && !bytes.HasSuffix(out.Bytes(), tail) {
				t.Errorf("%s/%s: the area after EOI was not copied unchanged", name, label)
			}
		}
	}
}
