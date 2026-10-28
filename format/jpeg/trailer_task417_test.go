package jpeg

// trailer_task417_test.go — task #417: Inject of a JPEG whose area after EOI is
// made of trailer signature candidates reads that area once.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// countingSeeker counts the reads and the seeks of a source.
type countingSeeker struct {
	r            io.ReadSeeker
	reads, seeks int
}

func (c *countingSeeker) Read(p []byte) (int, error) { c.reads++; return c.r.Read(p) } //nolint:wrapcheck // a test double: the error is the reader's
func (c *countingSeeker) Seek(off int64, whence int) (int64, error) {
	c.seeks++
	return c.r.Seek(off, whence) //nolint:wrapcheck // a test double: the error is the seeker's
}

// TestInject_R417_TrailerCandidatesAreLinear: TRAILER-STREAM-05. Inject of a
// JPEG with 4 MiB of AFCP prefixes that are not signatures ("AXSA"), of Samsung
// signatures that are not blocks ("QDIOBS"), and of FotoStation records, reads
// the area a bounded number of times, copies it unchanged, and takes well under
// a second (v1.5.2: 58 ms for 16 MiB; the first streaming write: 9 s for
// 16 MiB held in memory and 20 s from a file). The number of reads is the
// measure that is not affected by the machine or the race detector.
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
			start := time.Now()
			if err := Inject(cr, &out, nil, newIIM, nil, true); err != nil {
				t.Fatalf("%s/%s: Inject: %v", name, label, err)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("%s/%s: Inject of %d bytes took %v", name, label, len(src), d)
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
