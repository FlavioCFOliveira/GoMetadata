package png

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/iptc"
)

// Security audit 2026-10-16 (v1.5.0 pre-clearance), finding F-08.
// Sub-test names are the finding IDs.

// auditProfileBomb returns the payload of a zTXt "Raw profile type iptc" chunk
// whose zlib stream inflates to mib MiB of 'A': it is no valid profile, so
// nothing is ever recorded from it and first-wins cannot skip it.
func auditProfileBomb(mib int) []byte {
	var zb bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&zb, zlib.BestCompression)
	blk := bytes.Repeat([]byte{'A'}, 1<<20)
	for range mib {
		_, _ = zw.Write(blk)
	}
	_ = zw.Close()
	return append([]byte("Raw profile type iptc\x00\x00"), zb.Bytes()...)
}

func TestSecurityAuditV150(t *testing.T) {
	t.Parallel()

	// F-08: 40 chunks that each inflate to 63 MiB cost 2.1 s of Read. The proof
	// is a count, not a wall-clock bound: 40 chunks of 1 MiB of text that is no
	// profile (so first-wins never skips them) exhaust the 32 MiB budget at
	// exactly the 33rd chunk, which is rejected before any further chunk is
	// inflated.
	t.Run("F-08", func(t *testing.T) {
		t.Parallel()
		bomb := auditProfileBomb(1)
		var st extractState
		for i := range 40 {
			err := st.addRawProfile(chunkZTXt, bomb)
			if i < 32 {
				if err != nil {
					t.Fatalf("chunk %d: %v, want it inflated within the budget", i, err)
				}
				if want := int64(i+1) << 20; st.budget.used != want {
					t.Fatalf("after chunk %d: budget used %d, want %d", i, st.budget.used, want)
				}
				continue
			}
			if !errors.Is(err, ErrRawProfileBudget) {
				t.Fatalf("chunk %d: error = %v, want ErrRawProfileBudget", i, err)
			}
			if i != 32 {
				t.Fatalf("budget exceeded at chunk %d, want 32", i)
			}
			if st.budget.used != maxRawProfileBudget {
				t.Errorf("budget used %d after the rejected chunk, want %d (the rejected chunk is charged nothing)", st.budget.used, maxRawProfileBudget)
			}
			break
		}
		chunks := make([][2]any, 40)
		for i := range chunks {
			chunks[i] = [2]any{"zTXt", bomb}
		}
		if _, _, _, err := Extract(bytes.NewReader(pngWithChunks(chunks...))); !errors.Is(err, ErrRawProfileBudget) {
			t.Errorf("Extract error = %v, want ErrRawProfileBudget", err)
		}
	})

	// F-08: the same file through Inject (superseded and rewrite checks).
	t.Run("F-08-inject", func(t *testing.T) {
		t.Parallel()
		bomb := auditProfileBomb(1)
		chunks := make([][2]any, 40)
		for i := range chunks {
			chunks[i] = [2]any{"zTXt", bomb}
		}
		in := pngWithChunks(chunks...)
		err := Inject(bytes.NewReader(in), io.Discard, tinyEXIF(t), sampleIIM(t), nil, true)
		if !errors.Is(err, ErrRawProfileBudget) {
			t.Errorf("Inject error = %v, want ErrRawProfileBudget", err)
		}
	})

	// F-08: after the first profile of a kind, later chunks of that kind are
	// not inflated at all.
	t.Run("F-08-first-wins", func(t *testing.T) {
		t.Parallel()
		prof := textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", sampleIIM(t)))
		var st extractState
		if err := st.addRawProfile(chunkZTXt, prof); err != nil {
			t.Fatal(err)
		}
		used := st.budget.used
		if used == 0 || st.iptc == nil {
			t.Fatalf("first profile not read: used=%d iptc=%d bytes", used, len(st.iptc))
		}
		for range 100 {
			if err := st.addRawProfile(chunkZTXt, prof); err != nil {
				t.Fatal(err)
			}
		}
		if st.budget.used != used {
			t.Errorf("later chunks of the same kind were inflated: budget used %d -> %d", used, st.budget.used)
		}
	})

	// F-08: ordinary profiles of every kind and form are unaffected.
	t.Run("F-08-valid", func(t *testing.T) {
		t.Parallel()
		in := pngWithChunks(
			[2]any{"zTXt", textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", sampleIIM(t)))},
			[2]any{"iTXt", textChunk("iTXtZ", "Raw profile type exif", rawProfileHexText("exif", append([]byte("Exif\x00\x00"), tinyEXIF(t)...)))},
		)
		e, i, _, err := Extract(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if e == nil || !bytes.Equal(i, sampleIIM(t)) {
			t.Errorf("profiles not read: exif=%d iptc=%d bytes", len(e), len(i))
		}
	})
}

// TestSecurityAuditV150Alloc proves, by allocation, that 40 chunks that each
// inflate to 63 MiB are not inflated 40 times: the unfixed code allocated at
// least 40 x 63 MiB (2.1 s of CPU); the budget allows one inflate of at most
// 32 MiB. It measures process-wide allocation, so it is NOT parallel: a
// sequential test never overlaps the parallel ones.
func TestSecurityAuditV150Alloc(t *testing.T) { //nolint:paralleltest // measures process-wide allocation; it must not overlap the parallel tests
	bomb := auditProfileBomb(63)
	chunks := make([][2]any, 40)
	for i := range chunks {
		chunks[i] = [2]any{"zTXt", bomb}
	}
	in := pngWithChunks(chunks...)
	exif, iim := tinyEXIF(t), sampleIIM(t)
	// 8 budgets: room for the growth of one inflate buffer, far below the
	// 2.5 GiB that 40 full inflates allocate.
	const limit = 8 * uint64(maxRawProfileBudget)

	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"F-08", func() error { _, _, _, err := Extract(bytes.NewReader(in)); return err }},
		{"F-08-inject", func() error { return Inject(bytes.NewReader(in), io.Discard, exif, iim, nil, true) }},
	} {
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		err := tc.run()
		runtime.ReadMemStats(&m1)
		if !errors.Is(err, ErrRawProfileBudget) {
			t.Errorf("%s: error = %v, want ErrRawProfileBudget", tc.name, err)
		}
		got := m1.TotalAlloc - m0.TotalAlloc
		t.Logf("%s: %d MiB allocated", tc.name, got>>20)
		if got > limit {
			t.Errorf("%s: allocated %d MiB for 40 raw profile bombs (limit %d MiB)", tc.name, got>>20, limit>>20)
		}
	}
}

// BenchmarkExtractRawProfilesAuditV150 measures Extract of a PNG that repeats a
// valid IPTC raw profile 40 times (every chunk after the first is skipped).
func BenchmarkExtractRawProfilesAuditV150(b *testing.B) {
	i := &iptc.IPTC{}
	i.SetCaption("Raw profile caption")
	iim, err := iptc.Encode(i)
	if err != nil {
		b.Fatal(err)
	}
	prof := textChunk("zTXt", "Raw profile type iptc", rawProfileHexText("IPTC profile", iim))
	chunks := make([][2]any, 40)
	for i := range chunks {
		chunks[i] = [2]any{"zTXt", prof}
	}
	in := pngWithChunks(chunks...)
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	for range b.N {
		if _, _, _, err := Extract(bytes.NewReader(in)); err != nil {
			b.Fatal(err)
		}
	}
}
