package trailer

// scan_task417_test.go — task #417: the finders read the region once. A region
// made of signature candidates (AFCP "AXS", Samsung "QDIOBS", FotoStation
// records) must cost time and reads proportional to its size, and the finders
// must return what the finders of v1.5.2 (a bytes.Index loop over the region in
// memory) returned.

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"testing"
	"time"
)

// countingReader counts the reads and the seeks that a source costs.
type countingReader struct {
	r            io.ReadSeeker
	reads, seeks int
}

func (c *countingReader) Read(p []byte) (int, error) { c.reads++; return c.r.Read(p) }
func (c *countingReader) Seek(off int64, whence int) (int64, error) {
	c.seeks++
	return c.r.Seek(off, whence)
}

// repeatTo returns unit repeated until it is size bytes long.
func repeatTo(unit string, size int) []byte {
	return bytes.Repeat([]byte(unit), size/len(unit))
}

// TestScanRegion_R417_Linear: TRAILER-STREAM-03. A region of candidates that
// are not trailers is planned with a number of reads and seeks proportional to
// its size (never one per candidate), and quickly. The number of reads is the
// measure that holds under the race detector and on a loaded machine; the time
// bound is a generous second, against the 9 to 20 seconds that a read per
// candidate took for 16 MiB.
func TestScanRegion_R417_Linear(t *testing.T) {
	t.Parallel()
	const size = 4 << 20
	units := map[string]string{
		"AXSA":   "AXSA",                                        // AFCP prefix, not a signature
		"AXS!":   "AXS!",                                        // signatures: more than the cap
		"QDIOBS": "QDIOBS",                                      // Samsung signature, no block
		"foto":   "\x00\x01\x00\x00\x00\x0a\xa1\xb2\xc3\xd4",    // a chain of FotoStation records
		"mixed":  "AXSAQDIOBSAXS!\xa1\xb2\xc3\xd4QDIOBSAXSAXSA", // everything at once
	}
	for name, unit := range units {
		file := repeatTo(unit, size)
		cr := &countingReader{r: bytes.NewReader(file)}
		start := time.Now()
		if _, err := PlanFromSource(ReaderSource{R: cr}, 0, int64(len(file)), newIIM); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: planning %d bytes took %v", name, len(file), d)
		}
		// Each pass over the region reads it in buffers of 32 KiB or more; a few
		// passes (AFCP, FotoStation twice, Samsung, and the one read of a chain).
		if limit := 8 * (size/(32<<10) + 8); cr.reads > limit || cr.seeks > limit {
			t.Errorf("%s: %d reads and %d seeks for %d bytes, want at most %d of each", name, cr.reads, cr.seeks, len(file), limit)
		}
	}
}

// refCollectAFCPSigs is collectAFCPSigs of v1.5.2: bytes.Index again from the
// byte after each candidate, over the region in memory.
func refCollectAFCPSigs(region []byte, base int64, limit int) []afcpSig {
	sigs := []afcpSig{}
	end := base + int64(len(region))
	for from := base; from < end; {
		i := bytes.Index(region[from-base:], afcpSigPrefix)
		if i < 0 {
			break
		}
		at := from + int64(i)
		from = at + 1
		if at+4 > end || (region[at-base+3] != '!' && region[at-base+3] != '*') {
			continue
		}
		if len(sigs) == limit {
			return nil
		}
		sigs = append(sigs, afcpSig{at: at, kind: region[at-base+3]})
		from = at + 4
	}
	return sigs
}

// refFotoStation is scanFotoStation of v1.5.2: the first signature at or after
// the end of the previous record, one search per record.
func refFotoStation(region []byte, base, from int64) (recs []FotoRecord, start, end int64) {
	stop := base + int64(len(region))
	cur := from
	for cur >= base && cur <= stop {
		i := bytes.Index(region[cur-base:], fotoSig)
		if i < 0 || i < 6 {
			break
		}
		at := cur + int64(i)
		footer := at - 6
		foot := region[footer-base : footer-base+6]
		size := int64(binary.BigEndian.Uint32(foot[2:]))
		if size < 10 || footer+10-size != cur {
			break
		}
		recs = append(recs, FotoRecord{Tag: binary.BigEndian.Uint16(foot), Data: region[cur-base : footer-base]})
		cur = footer + 10
	}
	if len(recs) == 0 {
		return nil, 0, 0
	}
	return recs, from, cur
}

// refSamsung is scanSamsungQDIO of v1.5.2 over the region in memory: every
// signature in turn, the last valid block wins.
func refSamsung(region []byte, base int64) (sp Span, audioStart int64, ok bool) {
	stop := base + int64(len(region))
	le := binary.LittleEndian
	for from := base; ; {
		i := bytes.Index(region[from-base:], samsungQDIOSig)
		if i < 0 {
			return sp, audioStart, ok
		}
		q := from + int64(i)
		from = q + 1
		blockStart := q - samsungQDIOBlockLen + 4
		if blockStart < base+12 || q+int64(len(samsungQDIOSig)) > stop {
			continue
		}
		winStart := blockStart - 8
		if winStart < base {
			continue
		}
		win := region[winStart-base : q+int64(len(samsungQDIOSig))-base]
		if string(win[8:12]) != "QDIO" || le.Uint32(win[q-4-winStart:]) != samsungQDIODataLen || !bytes.Equal(win[4:8], samsungSEFT) {
			continue
		}
		dirLen := int64(le.Uint32(win))
		if dirLen < 12 || dirLen > blockStart-base-8 {
			continue
		}
		if !bytes.Equal(region[blockStart-8-dirLen-base:blockStart-8-dirLen-base+4], samsungSEFH) {
			continue
		}
		data := win[8 : 8+samsungQDIODataLen]
		start := int64(le.Uint32(data[samsungOffsetsAt:]))
		end := int64(le.Uint32(data[samsungOffsetsAt+4:]))
		if start < base || end < start || end > blockStart {
			continue
		}
		sp, audioStart, ok = samsungSpan(blockStart+samsungOffsetsAt, start, end), start, true
	}
}

// assertFindersMatchReference runs the three finders over region (the bytes of
// the file from base) through every kind of Source and compares them with the
// references.
func assertFindersMatchReference(tb testing.TB, region []byte, base int64) {
	tb.Helper()
	file := append(make([]byte, base), region...)
	sources := map[string]func() view[Source]{
		"reader": func() view[Source] {
			return view[Source]{src: ReaderSource{R: bytes.NewReader(file)}, base: base, end: int64(len(file))}
		},
		"bytes": func() view[Source] { return view[Source]{src: BytesSource(file), base: base, end: int64(len(file))} },
		"region": func() view[Source] {
			return view[Source]{src: regionSource{b: region, base: base}, base: base, end: int64(len(file))}
		},
	}
	wantSigs := refCollectAFCPSigs(region, base, maxAFCPSignatures)
	wantRecs, wantStart, wantEnd := refFotoStation(region, base, base)
	wantSp, wantAudio, wantOK := refSamsung(region, base)
	for name, mk := range sources {
		v := mk()
		gotSigs, err := collectAFCPSigs(new(afcpScan), v)
		if err != nil {
			tb.Fatalf("%s: collectAFCPSigs: %v", name, err)
		}
		if (gotSigs == nil) != (wantSigs == nil) || len(gotSigs) != len(wantSigs) {
			tb.Fatalf("%s: AFCP signatures %v, want %v", name, gotSigs, wantSigs)
		}
		for i := range gotSigs {
			if gotSigs[i] != wantSigs[i] {
				tb.Fatalf("%s: AFCP signature %d = %v, want %v", name, i, gotSigs[i], wantSigs[i])
			}
		}
		recs, start, end, err := scanFotoStation(v, base)
		if err != nil {
			tb.Fatalf("%s: scanFotoStation: %v", name, err)
		}
		if len(recs) != len(wantRecs) || start != wantStart || end != wantEnd {
			tb.Fatalf("%s: %d FotoStation records in [%d,%d), want %d in [%d,%d)", name, len(recs), start, end, len(wantRecs), wantStart, wantEnd)
		}
		for i := range recs {
			if recs[i].Tag != wantRecs[i].Tag || !bytes.Equal(recs[i].Data, wantRecs[i].Data) {
				tb.Fatalf("%s: FotoStation record %d differs", name, i)
			}
		}
		sp, audio, ok, err := scanSamsungQDIO(v)
		if err != nil {
			tb.Fatalf("%s: scanSamsungQDIO: %v", name, err)
		}
		if ok != wantOK || (ok && (sp.Start != wantSp.Start || sp.End != wantSp.End || audio != wantAudio)) {
			tb.Fatalf("%s: Samsung span %v/%v (%v), want %v/%v (%v)", name, sp.Start, audio, ok, wantSp.Start, wantAudio, wantOK)
		}
	}
}

// TestScanRegion_R417_MatchesTheInMemoryScan: TRAILER-STREAM-04. On random
// regions built from the tokens the finders look for, longer than the buffer of
// the streaming scan, so that candidates fall on its boundaries, the finders
// return what the v1.5.2 scans return.
func TestScanRegion_R417_MatchesTheInMemoryScan(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(417, 1)) //nolint:gosec // G404: deterministic seed for reproducible regions, not a secret
	audio := bytes.Repeat([]byte("a"), 300)
	tokens := [][]byte{
		[]byte("AXS!"), []byte("AXS*"), []byte("AXSA"), []byte("AXS"), []byte("QDIOBS"), []byte("QDIO"),
		[]byte("\xa1\xb2\xc3\xd4"), []byte("SEFT"), []byte("SEFH"),
		fotoOf(FotoRecord{Tag: 1, Data: oldIIM}), fotoOf(FotoRecord{Tag: 2}), samsungOf(0, audio),
		afcpOf(binary.BigEndian, "AXS!", 0, [][2]string{{"IPTC", string(oldIIM)}}),
		{0}, {0xff}, []byte("xyz"),
	}
	for trial := range 120 {
		size := []int{200, 5000, 70000, 140000, 400000}[trial%5]
		var region []byte
		for len(region) < size {
			if rng.IntN(4) == 0 {
				region = append(region, make([]byte, rng.IntN(300))...)
			}
			region = append(region, tokens[rng.IntN(len(tokens))]...)
		}
		base := int64(rng.IntN(100))
		assertFindersMatchReference(t, region, base)
	}
}

// TestScanRegion_R417_Boundaries puts a candidate of each finder at every offset
// around the end of the first buffer of the streaming scan (and before it, so
// that the look-behind of the Samsung block crosses it).
func TestScanRegion_R417_Boundaries(t *testing.T) {
	t.Parallel()
	const buf = 64 << 10
	block := samsungOf(0, bytes.Repeat([]byte("a"), 40))
	for _, tok := range [][]byte{[]byte("AXS!"), fotoOf(FotoRecord{Tag: 1, Data: oldIIM}), block} {
		for at := buf - len(tok) - 3; at <= buf+3; at++ {
			region := make([]byte, 2*buf)
			copy(region[at:], tok)
			assertFindersMatchReference(t, region, 0)
		}
	}
}

// FuzzFindersMatchReference: for any region and base the finders return what
// the in-memory scans return.
func FuzzFindersMatchReference(f *testing.F) {
	f.Add(repeatTo("AXSA", 600), uint8(0))
	f.Add(repeatTo("QDIOBS", 600), uint8(3))
	f.Add(repeatTo("\x00\x01\x00\x00\x00\x0a\xa1\xb2\xc3\xd4", 600), uint8(0))
	f.Add(append(bytes.Repeat([]byte{7}, 20), samsungOf(20, []byte("audio"))...), uint8(20))
	f.Add(afcpOf(binary.BigEndian, "AXS!", 0, [][2]string{{"IPTC", string(oldIIM)}}), uint8(0))
	f.Fuzz(func(t *testing.T, region []byte, base uint8) {
		assertFindersMatchReference(t, region, int64(base))
	})
}
