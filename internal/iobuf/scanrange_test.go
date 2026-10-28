package iobuf

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"
)

// scanEvent is what ScanRange tells fn about one occurrence.
type scanEvent struct {
	at  int64
	win string // "" with ok false: no window
	ok  bool
}

// refScan is ScanRange written with bytes.Index over the whole data: the
// occurrence at or after searchAt, the window, and the next start from next.
func refScan(data []byte, from, to int64, pat []byte, behind, ahead int, next func(at int64, i int) int64) []scanEvent {
	var ev []scanEvent
	if len(pat) == 0 || to-from < int64(len(pat)) {
		return ev
	}
	for searchAt, i := from, 0; searchAt < to && to-searchAt >= int64(len(pat)); i++ {
		j := bytes.Index(data[searchAt:to], pat)
		if j < 0 {
			break
		}
		at := searchAt + int64(j)
		e := scanEvent{at: at}
		if at-int64(behind) >= from && at+int64(ahead) <= to {
			e.win, e.ok = string(data[at-int64(behind):at+int64(ahead)]), true
		}
		ev = append(ev, e)
		n := next(at, i)
		if n < 0 {
			break
		}
		searchAt = max(n, at+1)
	}
	return ev
}

// TestScanRange_MatchesIndexLoop: ScanRange reports the occurrences that
// repeated searches report, with the windows around them, on data longer than
// its buffer so that occurrences and windows fall on the boundaries of reads,
// for every way fn can choose where the search goes on (task #417).
func TestScanRange_MatchesIndexLoop(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(417, 3)) //nolint:gosec // G404: deterministic seed for reproducible data, not a secret
	pats := [][]byte{[]byte("AXS"), []byte("QDIOBS"), {0xA1, 0xB2, 0xC3, 0xD4}, []byte("aa")}
	nexts := map[string]func(at int64, i int) int64{
		"after-start": func(at int64, _ int) int64 { return at + 1 },
		"after-match": func(at int64, _ int) int64 { return at + 4 },
		"skip-far":    func(at int64, i int) int64 { return at + int64(1+(i*7919)%90000) },
		"back":        func(at int64, _ int) int64 { return at - 5 }, // read as at+1
		"stop-at-5":   func(at int64, i int) int64 { return map[bool]int64{true: -1, false: at + 1}[i == 4] },
	}
	for trial := range 60 {
		size := []int{10, 1000, StreamCopyBufSize - 1, StreamCopyBufSize, StreamCopyBufSize + 1, 3*StreamCopyBufSize + 17, 400000}[trial%7]
		data := make([]byte, size)
		for i := range data {
			data[i] = "aAXSQDIOBS\xa1\xb2\xc3\xd4x"[rng.IntN(15)]
		}
		for range 40 { // plant occurrences, some across the boundaries of reads
			p := pats[rng.IntN(len(pats))]
			at := rng.IntN(size + 1)
			if rng.IntN(2) == 0 {
				at = (1+rng.IntN(max(size/StreamCopyBufSize, 1)))*StreamCopyBufSize - rng.IntN(len(p)+1)
			}
			if at >= 0 && at+len(p) <= size {
				copy(data[at:], p)
			}
		}
		pat := pats[trial%len(pats)]
		behind, ahead := rng.IntN(40), len(pat)+rng.IntN(8)
		from := int64(rng.IntN(size/2 + 1))
		to := int64(size) - int64(rng.IntN(size/4+1))
		for name, next := range nexts {
			want := refScan(data, from, to, pat, behind, ahead, next)
			var got []scanEvent
			i := 0
			err := ScanRange(bytes.NewReader(data), from, to, pat, behind, ahead, ScanFunc(func(at int64, win []byte) (int64, error) {
				e := scanEvent{at: at}
				if win != nil {
					e.win, e.ok = string(win), true
					if len(win) != behind+ahead {
						t.Fatalf("window of %d bytes, want %d", len(win), behind+ahead)
					}
				}
				got = append(got, e)
				n := next(at, i)
				i++
				return n, nil
			}))
			if err != nil {
				t.Fatalf("trial %d %s: %v", trial, name, err)
			}
			if len(got) != len(want) {
				t.Fatalf("trial %d %s (size %d, [%d,%d), %q, %d+%d): %d occurrences, want %d", trial, name, size, from, to, pat, behind, ahead, len(got), len(want))
			}
			for k := range got {
				if got[k] != want[k] {
					t.Fatalf("trial %d %s: occurrence %d = %+v, want %+v", trial, name, k, got[k], want[k])
				}
			}
		}
	}
}

// TestScanRange_ReadsOnce: a range of 4 MiB made of occurrences is read in
// buffers, not once per occurrence (task #417).
func TestScanRange_ReadsOnce(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("AXSA"), 1<<20)
	cr := &countingRS{r: bytes.NewReader(data)}
	n := 0
	err := ScanRange(cr, 0, int64(len(data)), []byte("AXS"), 0, 4, ScanFunc(func(at int64, _ []byte) (int64, error) { n++; return at + 1, nil }))
	if err != nil || n != 1<<20 {
		t.Fatalf("occurrences = %d, err = %v", n, err)
	}
	if limit := len(data)/(StreamCopyBufSize/2) + 4; cr.reads > limit || cr.seeks > 2 {
		t.Errorf("%d reads and %d seeks, want at most %d and 2", cr.reads, cr.seeks, limit)
	}
}

type countingRS struct {
	r            io.ReadSeeker
	reads, seeks int
}

func (c *countingRS) Read(p []byte) (int, error) { c.reads++; return c.r.Read(p) }
func (c *countingRS) Seek(off int64, whence int) (int64, error) {
	c.seeks++
	return c.r.Seek(off, whence)
}

// TestScanRange_Errors: a short source, an error of fn and a window larger than
// the buffer are reported.
func TestScanRange_Errors(t *testing.T) {
	t.Parallel()
	data := bytes.Repeat([]byte("xAXS"), 100000)
	pat := []byte("AXS")
	next := func(at int64, _ []byte) (int64, error) { return at + 1, nil }
	if err := ScanRange(bytes.NewReader(data[:1000]), 0, int64(len(data)), pat, 0, 3, ScanFunc(next)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short source: err = %v, want io.ErrUnexpectedEOF", err)
	}
	boom := errors.New("boom")
	if err := ScanRange(bytes.NewReader(data), 0, int64(len(data)), pat, 0, 3, ScanFunc(func(int64, []byte) (int64, error) { return 0, boom })); !errors.Is(err, boom) {
		t.Errorf("error of fn: err = %v, want it returned", err)
	}
	if err := ScanRange(bytes.NewReader(data), 0, int64(len(data)), pat, StreamCopyBufSize, 3, ScanFunc(next)); !errors.Is(err, ErrPatternTooLong) {
		t.Errorf("huge window: err = %v, want ErrPatternTooLong", err)
	}
	if err := ScanRange(bytes.NewReader(data), 5, 6, pat, 0, 3, ScanFunc(next)); err != nil {
		t.Errorf("range shorter than the pattern: err = %v", err)
	}
}

// BenchmarkScanRange: occurrences that are not signatures, 4 MiB of "AXSA", from
// memory and the cost per call of IndexRange for the same occurrences (what
// the finders did before task #417).
func BenchmarkScanRange(b *testing.B) {
	data := bytes.Repeat([]byte("AXSA"), 1<<20)
	pat := []byte("AXS")
	b.Run("ScanRange", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		b.ReportAllocs()
		r := bytes.NewReader(data)
		for range b.N {
			_ = ScanRange(r, 0, int64(len(data)), pat, 0, 4, ScanFunc(func(at int64, _ []byte) (int64, error) { return at + 1, nil }))
		}
	})
	b.Run("IndexRangeLoop-64KiB-region", func(b *testing.B) {
		small := data[:64<<10]
		b.SetBytes(int64(len(small)))
		b.ReportAllocs()
		r := bytes.NewReader(small)
		for range b.N {
			for from := int64(0); ; {
				at, _ := IndexRange(r, from, int64(len(small)), pat)
				if at < 0 {
					break
				}
				from = at + 1
			}
		}
	})
}
