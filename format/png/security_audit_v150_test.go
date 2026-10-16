package png

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"testing"
	"time"

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

	// F-08: 40 chunks that each inflate to 63 MiB cost 2.1 s of Read.
	t.Run("F-08", func(t *testing.T) {
		t.Parallel()
		bomb := auditProfileBomb(63)
		chunks := make([][2]any, 40)
		for i := range chunks {
			chunks[i] = [2]any{"zTXt", bomb}
		}
		in := pngWithChunks(chunks...)
		start := time.Now()
		_, _, _, err := Extract(bytes.NewReader(in))
		d := time.Since(start)
		t.Logf("Extract of 40 bombs: %v", d)
		if !errors.Is(err, ErrRawProfileBudget) {
			t.Errorf("Extract error = %v, want ErrRawProfileBudget", err)
		}
		// One inflate of at most the budget (about 30 ms); the unfixed code
		// inflated all 40 chunks (2.1 s). The bound allows -race and loaded hosts.
		if d > time.Second {
			t.Errorf("Extract took %v for 40 raw profile bombs", d)
		}
	})

	// F-08: the same file through Inject (superseded and rewrite checks).
	t.Run("F-08-inject", func(t *testing.T) {
		t.Parallel()
		bomb := auditProfileBomb(63)
		chunks := make([][2]any, 40)
		for i := range chunks {
			chunks[i] = [2]any{"zTXt", bomb}
		}
		in := pngWithChunks(chunks...)
		start := time.Now()
		err := Inject(bytes.NewReader(in), io.Discard, tinyEXIF(t), sampleIIM(t), nil, true)
		d := time.Since(start)
		if !errors.Is(err, ErrRawProfileBudget) {
			t.Errorf("Inject error = %v, want ErrRawProfileBudget", err)
		}
		if d > time.Second {
			t.Errorf("Inject took %v for 40 raw profile bombs", d)
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
