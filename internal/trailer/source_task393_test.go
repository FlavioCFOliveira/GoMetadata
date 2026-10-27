package trailer

// source_task393_test.go — task #393: PlanFromSource, which reads the region of
// a file as it needs it, must find the trailers PlanFromRegion finds, plan the
// same rewrites and write the same bytes, for every input.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

// samsungOf builds a Samsung trailer that starts at absolute offset start (the
// layout of the package comment): the audio file, a SEFH/SEFT directory block,
// and the QDIO block with "BS".
func samsungOf(start int64, audio []byte) []byte {
	le := binary.LittleEndian
	out := append([]byte(nil), audio...)
	dir := []byte("SEFH")
	dir = le.AppendUint32(dir, 101)
	dir = le.AppendUint32(dir, 0)
	out = append(out, dir...)
	out = le.AppendUint32(out, uint32(len(dir))) //nolint:gosec // G115: test sizes are small
	out = append(out, "SEFT"...)
	out = append(out, "QDIO"...)
	out = le.AppendUint32(out, 101)
	out = le.AppendUint32(out, 1)
	out = le.AppendUint32(out, uint32(start))                   //nolint:gosec // G115: test sizes are small
	out = le.AppendUint32(out, uint32(start+int64(len(audio)))) //nolint:gosec // G115: test sizes are small
	out = le.AppendUint32(out, 20)
	out = append(out, "QDIO"...)
	return append(out, "BS"...)
}

// planSnapshot is what a plan decides: its spans and what they build at a few
// output positions, and the bytes WriteRegion writes at one.
type planSnapshot struct {
	spans   [][2]int64
	builds  []string
	written []byte
}

// snapshot writes the plan with every span built at base+delta and records it.
func snapshot(t *testing.T, plan Plan, file []byte, base int64, delta int64) planSnapshot {
	t.Helper()
	var snap planSnapshot
	for _, sp := range plan.Spans {
		snap.spans = append(snap.spans, [2]int64{sp.Start, sp.End})
		for _, pos := range []int64{sp.Start, sp.Start + delta, 0} {
			b, changed, err := sp.Build(pos)
			snap.builds = append(snap.builds, fmt.Sprintf("%x|%v|%s", b, changed, errText(err)))
		}
	}
	var buf bytes.Buffer
	out := &OutWriter{W: &buf}
	_, _ = out.Write(file[:base])
	out.N += delta
	if err := plan.WriteRegion(out, out); err != nil {
		snap.written = []byte("ERR " + err.Error())
	} else {
		snap.written = buf.Bytes()
	}
	return snap
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// assertSameAsRegion plans the region [base, len(file)) of file with both
// functions and compares the plans.
func assertSameAsRegion(t *testing.T, name string, file []byte, base int64, newIPTC []byte) {
	t.Helper()
	if base < 0 || base > int64(len(file)) {
		return
	}
	want := PlanFromRegion(file[base:], base, newIPTC)
	got, err := PlanFromSource(ReaderSource{R: bytes.NewReader(file)}, base, int64(len(file))-base, newIPTC)
	if err != nil {
		t.Fatalf("%s: PlanFromSource: %v", name, err)
	}
	if got.Region != nil || got.Base != base {
		t.Fatalf("%s: streaming plan holds a region (%d bytes) or has base %d, want none and %d", name, len(got.Region), got.Base, base)
	}
	for _, delta := range []int64{0, 7, 123} {
		w, g := snapshot(t, want, file, base, delta), snapshot(t, got, file, base, delta)
		if len(w.spans) != len(g.spans) {
			t.Fatalf("%s: %d spans, want %d", name, len(g.spans), len(w.spans))
		}
		for i := range w.spans {
			if w.spans[i] != g.spans[i] || w.builds[3*i] != g.builds[3*i] || w.builds[3*i+1] != g.builds[3*i+1] || w.builds[3*i+2] != g.builds[3*i+2] {
				t.Fatalf("%s: span %d differs: %v vs %v", name, i, g.spans[i], w.spans[i])
			}
		}
		if !bytes.Equal(w.written, g.written) {
			t.Fatalf("%s (delta %d): WriteRegion differs: %d vs %d bytes", name, delta, len(g.written), len(w.written))
		}
	}
}

// TestPlanFromSourceMatchesPlanFromRegion: TRAILER-STREAM-01. Every layout of
// trailers, at several region starts, with and without a new IPTC.
func TestPlanFromSourceMatchesPlanFromRegion(t *testing.T) {
	t.Parallel()
	image := bytes.Repeat([]byte{0x55}, 100)
	afcpBE := func(at int64) []byte {
		return afcpOf(binary.BigEndian, "AXS!", at, [][2]string{{"IPTC", string(oldIIM)}, {"TEXT", "caption"}})
	}
	afcpLE := func(at int64) []byte {
		return afcpOf(binary.LittleEndian, "AXS*", at, [][2]string{{"IPTC", string(oldIIM)}})
	}
	foto := fotoOf(FotoRecord{Tag: 3, Data: []byte("thumb")}, FotoRecord{Tag: 1, Data: oldIIM})
	audio := bytes.Repeat([]byte("audio"), 1000)
	files := map[string][]byte{
		"nothing":       image,
		"afcp-be":       append(bytes.Clone(image), afcpBE(100)...),
		"afcp-le":       append(bytes.Clone(image), afcpLE(100)...),
		"afcp+gap":      append(append(bytes.Clone(image), "gap"...), afcpBE(103)...),
		"foto":          append(bytes.Clone(image), foto...),
		"foto+after":    append(append(bytes.Clone(image), foto...), "after"...),
		"afcp+foto":     append(append(bytes.Clone(image), afcpBE(100)...), foto...),
		"foto+afcp":     append(append(bytes.Clone(image), foto...), afcpBE(100+int64(len(foto)))...),
		"samsung":       append(bytes.Clone(image), samsungOf(100, audio)...),
		"samsung+after": append(append(bytes.Clone(image), samsungOf(100, audio)...), "vivo-after"...),
		"afcp+samsung":  append(append(bytes.Clone(image), afcpBE(100)...), samsungOf(100+int64(len(afcpBE(100))), audio)...),
		"samsung-wrong": append(bytes.Clone(image), samsungOf(5000, audio)...), // audio offsets outside the region
		"bomb":          afcpBomb(1 << 14),
		"bomb-at-cap":   afcpBomb(maxAFCPSignatures * 12),
		"signature-end": append(bytes.Clone(image), "AXS!"...),
		"chance":        append(bytes.Clone(image), "AXS!junkAXS*QDIOBSxxQDIOBS"...),
	}
	for name, file := range files {
		for _, base := range []int64{0, 50, 100, 103, int64(len(file)) - 1} {
			for _, iptc := range [][]byte{nil, newIIM} {
				assertSameAsRegion(t, name, file, base, iptc)
			}
		}
	}
}

// FuzzPlanFromSource: for any file and region start the two functions agree
// (task #393).
func FuzzPlanFromSource(f *testing.F) {
	f.Add(append(bytes.Repeat([]byte{7}, 20), afcpOf(binary.BigEndian, "AXS!", 20, [][2]string{{"IPTC", string(oldIIM)}})...), uint16(20))
	f.Add(fotoOf(FotoRecord{Tag: 1, Data: oldIIM}), uint16(0))
	f.Add(append([]byte("xx"), samsungOf(2, []byte("audio"))...), uint16(2))
	f.Add([]byte("AXS!\x00\x00\x00\x00AXS!\x00\x00\x00\x00"), uint16(0))
	f.Add(afcpBomb(4096), uint16(100))
	f.Fuzz(func(t *testing.T, file []byte, base uint16) {
		assertSameAsRegion(t, "fuzz", file, int64(base), newIIM)
	})
}

// TestPlanFromSourceReadError: a source that fails is reported, never
// swallowed into a plan, and a source that is shorter than it was planned for
// ends WriteRegion with an error instead of hanging or panicking. TRAILER-STREAM-02.
func TestPlanFromSourceReadError(t *testing.T) {
	t.Parallel()
	file := append(bytes.Repeat([]byte{1}, 10), samsungOf(10, bytes.Repeat([]byte("a"), 100000))...)
	failing := ReaderSource{R: &failAfter{r: bytes.NewReader(file), left: 3}}
	if _, err := PlanFromSource(failing, 10, int64(len(file))-10, nil); err == nil {
		t.Error("PlanFromSource: a failing source was not reported")
	}

	r := bytes.NewReader(file)
	plan, err := PlanFromSource(ReaderSource{R: r}, 10, int64(len(file))-10, newIIM)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Spans) == 0 {
		t.Fatal("fixture: no span planned")
	}
	for _, keep := range []int{10, 11, 5000, len(file) - 20, len(file) - 1} {
		r.Reset(file[:keep]) // the file shrank after it was planned
		var buf bytes.Buffer
		out := &OutWriter{W: &buf}
		_, _ = out.Write(file[:10])
		if err := plan.WriteRegion(out, out); err == nil {
			t.Errorf("source cut at %d of %d: WriteRegion succeeded", keep, len(file))
		} else if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			t.Errorf("source cut at %d of %d: err = %v, want an unexpected EOF", keep, len(file), err)
		}
	}
}

// failAfter fails every read after left of them.
type failAfter struct {
	r    io.ReadSeeker
	left int
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.left--; f.left < 0 {
		return 0, errors.New("disk on fire")
	}
	return f.r.Read(p)
}

func (f *failAfter) Seek(off int64, whence int) (int64, error) { return f.r.Seek(off, whence) }

// BenchmarkPlanFromSourceSamsung measures the plan of a Samsung trailer with a
// 4 MiB audio file, from a region in memory and through a source: the source
// does not hold the region (task #393).
func BenchmarkPlanFromSourceSamsung(b *testing.B) {
	file := append(bytes.Repeat([]byte{1}, 100), samsungOf(100, bytes.Repeat([]byte("audio"), 800000))...)
	b.Run("region", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = PlanFromRegion(file[100:], 100, newIIM)
		}
	})
	b.Run("source", func(b *testing.B) {
		r := bytes.NewReader(file)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := PlanFromSource(ReaderSource{R: r}, 100, int64(len(file))-100, newIIM); err != nil {
				b.Fatal(err)
			}
		}
	})
}
