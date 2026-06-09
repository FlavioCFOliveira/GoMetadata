package riff

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// buildChunkHeader constructs a raw 8-byte RIFF chunk header.
func buildChunkHeader(fourcc [4]byte, size uint32) []byte {
	buf := make([]byte, 8)
	copy(buf[:4], fourcc[:])
	binary.LittleEndian.PutUint32(buf[4:], size)
	return buf
}

// TestReadChunkBasic verifies that ReadChunk correctly parses a well-formed
// chunk header and records the data offset.
func TestReadChunkBasic(t *testing.T) {
	t.Parallel()
	fourcc := [4]byte{'R', 'I', 'F', 'F'}
	const dataSize = 1234

	raw := buildChunkHeader(fourcc, dataSize)
	// Append dummy data (not required for header parsing, but realistic).
	raw = append(raw, make([]byte, dataSize)...)

	r := bytes.NewReader(raw)
	c, err := ReadChunk(r)
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}

	if c.FourCC != fourcc {
		t.Errorf("FourCC = %v, want %v", c.FourCC, fourcc)
	}
	if c.Size != dataSize {
		t.Errorf("Size = %d, want %d", c.Size, dataSize)
	}
	// After reading the 8-byte header the data starts at offset 8.
	if c.Offset != 8 {
		t.Errorf("Offset = %d, want 8", c.Offset)
	}
}

// TestFourCCString verifies that FourCCString returns the ASCII representation.
func TestFourCCString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fourcc [4]byte
		want   string
	}{
		{[4]byte{'R', 'I', 'F', 'F'}, "RIFF"},
		{[4]byte{'W', 'E', 'B', 'P'}, "WEBP"},
		{[4]byte{'V', 'P', '8', 'L'}, "VP8L"},
		{[4]byte{0x00, 0x00, 0x00, 0x00}, "\x00\x00\x00\x00"},
	}
	for _, tc := range tests {
		c := &Chunk{FourCC: tc.fourcc}
		if got := c.FourCCString(); got != tc.want {
			t.Errorf("FourCCString() = %q, want %q", got, tc.want)
		}
	}
}

// TestSkipChunkEvenSize verifies that SkipChunk advances the reader to the
// byte immediately after an even-size data region (no padding byte).
func TestSkipChunkEvenSize(t *testing.T) {
	t.Parallel()
	const dataSize = 10 // even
	raw := buildChunkHeader([4]byte{'d', 'a', 't', 'a'}, dataSize)
	raw = append(raw, make([]byte, dataSize)...)

	r := bytes.NewReader(raw)
	c, err := ReadChunk(r)
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}

	if err := SkipChunk(r, c); err != nil {
		t.Fatalf("SkipChunk: %v", err)
	}

	// After skipping we should be at EOF (8 header + 10 data = 18 bytes consumed).
	pos, _ := r.Seek(0, io.SeekCurrent)
	if pos != int64(8+dataSize) {
		t.Errorf("reader position after SkipChunk = %d, want %d", pos, 8+dataSize)
	}
}

// TestSkipChunkOddSize verifies that SkipChunk advances one extra byte when
// the chunk size is odd (RIFF alignment padding).
func TestSkipChunkOddSize(t *testing.T) {
	t.Parallel()
	const dataSize = 7 // odd → 1 byte padding
	raw := buildChunkHeader([4]byte{'o', 'd', 'd', ' '}, dataSize)
	raw = append(raw, make([]byte, dataSize+1)...) // include the padding byte

	r := bytes.NewReader(raw)
	c, err := ReadChunk(r)
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}

	if err := SkipChunk(r, c); err != nil {
		t.Fatalf("SkipChunk: %v", err)
	}

	// Expected position: offset(8) + size(7) + padding(1) = 16.
	pos, _ := r.Seek(0, io.SeekCurrent)
	if pos != int64(8+dataSize+1) {
		t.Errorf("reader position after SkipChunk (odd) = %d, want %d", pos, 8+dataSize+1)
	}
}

// TestReadChunkTruncatedHeader verifies that ReadChunk returns an error when
// fewer than 8 bytes are available.
func TestReadChunkTruncatedHeader(t *testing.T) {
	t.Parallel()
	truncated := []byte{0x52, 0x49, 0x46} // only 3 bytes
	r := bytes.NewReader(truncated)
	_, err := ReadChunk(r)
	if err == nil {
		t.Fatal("expected error for truncated header, got nil")
	}
}

// TestReadChunkEmptyReader verifies behaviour on a completely empty reader.
func TestReadChunkEmptyReader(t *testing.T) {
	t.Parallel()
	r := bytes.NewReader(nil)
	_, err := ReadChunk(r)
	if err == nil {
		t.Fatal("expected error for empty reader, got nil")
	}
}

// TestReadChunkZeroSize verifies that a chunk with size=0 is parsed correctly.
func TestReadChunkZeroSize(t *testing.T) {
	t.Parallel()
	raw := buildChunkHeader([4]byte{'n', 'u', 'l', 'l'}, 0)
	r := bytes.NewReader(raw)
	c, err := ReadChunk(r)
	if err != nil {
		t.Fatalf("ReadChunk with zero size: %v", err)
	}
	if c.Size != 0 {
		t.Errorf("Size = %d, want 0", c.Size)
	}
}

// TestSkipChunkZeroSize verifies that SkipChunk on a zero-size chunk moves
// the reader to offset 8 (immediately after the header).
func TestSkipChunkZeroSize(t *testing.T) {
	t.Parallel()
	raw := buildChunkHeader([4]byte{'n', 'u', 'l', 'l'}, 0)
	r := bytes.NewReader(raw)
	c, err := ReadChunk(r)
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if err := SkipChunk(r, c); err != nil {
		t.Fatalf("SkipChunk: %v", err)
	}
	pos, _ := r.Seek(0, io.SeekCurrent)
	if pos != 8 {
		t.Errorf("reader position after SkipChunk(zero) = %d, want 8", pos)
	}
}

// TestMultipleChunksSequential verifies that back-to-back ReadChunk+SkipChunk
// calls parse a multi-chunk stream in order.
func TestMultipleChunksSequential(t *testing.T) {
	t.Parallel()
	fourccs := [][4]byte{
		{'R', 'I', 'F', 'F'},
		{'V', 'P', '8', ' '},
		{'E', 'X', 'I', 'F'},
	}
	sizes := []uint32{10, 6, 4}

	var stream []byte
	for i, fc := range fourccs {
		stream = append(stream, buildChunkHeader(fc, sizes[i])...)
		dataCap := sizes[i]
		if sizes[i]%2 != 0 {
			dataCap++
		}
		data := make([]byte, 0, dataCap)
		data = append(data, make([]byte, sizes[i])...)
		if sizes[i]%2 != 0 {
			data = append(data, 0x00) // padding
		}
		stream = append(stream, data...)
	}

	r := bytes.NewReader(stream)
	for i, wantFC := range fourccs {
		c, err := ReadChunk(r)
		if err != nil {
			t.Fatalf("chunk %d ReadChunk: %v", i, err)
		}
		if c.FourCC != wantFC {
			t.Errorf("chunk %d FourCC = %v, want %v", i, c.FourCC, wantFC)
		}
		if c.Size != sizes[i] {
			t.Errorf("chunk %d Size = %d, want %d", i, c.Size, sizes[i])
		}
		if err := SkipChunk(r, c); err != nil {
			t.Fatalf("chunk %d SkipChunk: %v", i, err)
		}
	}
}

// TestChunkEqual exercises the Equal method (0% coverage).
func TestChunkEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fourcc  [4]byte
		compare [4]byte
		want    bool
	}{
		{"matching RIFF", [4]byte{'R', 'I', 'F', 'F'}, [4]byte{'R', 'I', 'F', 'F'}, true},
		{"non-matching", [4]byte{'R', 'I', 'F', 'F'}, [4]byte{'W', 'E', 'B', 'P'}, false},
		{"all zeros match", [4]byte{0, 0, 0, 0}, [4]byte{0, 0, 0, 0}, true},
		{"single byte differs", [4]byte{'V', 'P', '8', ' '}, [4]byte{'V', 'P', '8', 'L'}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &Chunk{FourCC: tc.fourcc}
			if got := c.Equal(tc.compare); got != tc.want {
				t.Errorf("Equal(%v) = %v, want %v", tc.compare, got, tc.want)
			}
		})
	}
}

// BenchmarkReadChunk measures the throughput of reading a chunk header.
func BenchmarkReadChunk(b *testing.B) {
	raw := buildChunkHeader([4]byte{'R', 'I', 'F', 'F'}, 1024)
	raw = append(raw, make([]byte, 1024)...)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(raw)
		_, _ = ReadChunk(r)
	}
}

// ---------------------------------------------------------------------------
// #187 gate: documented no-bounds-check contract is locked
// ---------------------------------------------------------------------------

// TestReadChunk_SizeContract is the regression gate for audit finding #187.
// It verifies the documented no-bounds-check contract of ReadChunk and SkipChunk.
//
// Key behaviors asserted:
//  1. ReadChunk returns Chunk.Size verbatim from the wire bytes — no clamping,
//     no validation. Size = 0xFFFFFFFF on an 8-byte stream is returned unchanged.
//  2. Neither ReadChunk nor SkipChunk panics with an oversized Size.
//  3. After SkipChunk with a huge Size, a subsequent ReadChunk fails (the
//     stream position is past all available data), proving the caller must
//     validate Size before trusting subsequent reads.
//
// Note: io.Seeker's contract (io package docs) explicitly permits seeking past
// EOF without error; only reads after the seek will fail. This is why SkipChunk
// itself does not return an error on a short bytes.NewReader — and why the
// documented contract places validation responsibility on the caller.
func TestReadChunk_SizeContract(t *testing.T) {
	t.Parallel()

	// Build a header that claims 0xFFFFFFFF (4 GiB - 1) bytes of data on a
	// stream that is only 8 bytes long (the header itself, no payload).
	const hugeSize = uint32(0xFFFFFFFF)
	raw := buildChunkHeader([4]byte{'R', 'I', 'F', 'F'}, hugeSize)
	r := bytes.NewReader(raw)

	// Contract gate 1: ReadChunk must succeed and return the oversized Size unchanged.
	c, err := ReadChunk(r)
	if err != nil {
		t.Fatalf("ReadChunk on short stream with huge Size: unexpected error %v", err)
	}
	if c.Size != hugeSize {
		t.Errorf("ReadChunk Size = %d, want %d — contract violation: Size must be returned verbatim", c.Size, hugeSize)
	}
	if c.Offset != 8 {
		t.Errorf("ReadChunk Offset = %d, want 8", c.Offset)
	}

	// Contract gate 2: SkipChunk must not panic (io.Seeker allows seeking past EOF).
	// On bytes.NewReader a seek past the end is a valid no-error seek — the
	// documented contract relies on subsequent read failures, not seek failures.
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Errorf("SkipChunk panicked with huge Size: %v", rec)
			}
		}()
		_ = SkipChunk(r, c)
	}()

	// Contract gate 3: after SkipChunk with a huge Size the stream position is
	// far past the end of data. A subsequent ReadChunk must fail — proving that
	// the caller MUST validate Size before trusting the stream to continue.
	_, readErr := ReadChunk(r)
	if readErr == nil {
		t.Error("ReadChunk after SkipChunk(hugeSize): expected error (EOF/short read), got nil — caller must validate Size")
	}
}

// TestSkipChunk_MaxUint32NoPanic verifies that SkipChunk does not panic on
// the maximum uint32 Size even when the stream is empty, hardening the no-panic
// contract for any future refactor.
func TestSkipChunk_MaxUint32NoPanic(t *testing.T) {
	t.Parallel()
	c := Chunk{
		FourCC: [4]byte{'t', 'e', 's', 't'},
		Size:   0xFFFFFFFF,
		Offset: 8,
	}
	r := bytes.NewReader(nil) // zero-length stream
	// Must not panic; the error value is expected but irrelevant to this gate.
	defer func() {
		if rec := recover(); rec != nil {
			t.Errorf("SkipChunk panicked with max Size: %v", rec)
		}
	}()
	_ = SkipChunk(r, c)
}

// ---------------------------------------------------------------------------
// #150 gate: direct fuzz target for the RIFF chunk reader
// ---------------------------------------------------------------------------

// FuzzRIFFRead feeds arbitrary bytes directly into the RIFF chunk reader and
// asserts that neither ReadChunk nor SkipChunk panics, regardless of input.
//
// Seed corpus covers: valid well-formed chunk, truncated header, odd-size chunk,
// zero-size chunk, maximum-size claim, and a multi-chunk stream.
//
// The fuzz target also exercises the chunk walk pattern used by the webp consumer
// so that corpus-guided mutation can discover edge cases in the Size + Offset
// arithmetic without going through the full WebP parser.
func FuzzRIFFRead(f *testing.F) {
	// Seed: valid RIFF header + 4 bytes of payload.
	f.Add(buildChunkHeader([4]byte{'R', 'I', 'F', 'F'}, 4), true)
	// Seed: truncated (3 bytes — fewer than the 8-byte minimum).
	f.Add([]byte{0x52, 0x49, 0x46}, false)
	// Seed: odd-size chunk (7 bytes data).
	f.Add(append(buildChunkHeader([4]byte{'V', 'P', '8', ' '}, 7), make([]byte, 8)...), true)
	// Seed: zero-size chunk.
	f.Add(buildChunkHeader([4]byte{'n', 'u', 'l', 'l'}, 0), true)
	// Seed: maximum uint32 Size on a short stream (no data follows).
	f.Add(buildChunkHeader([4]byte{'h', 'u', 'g', 'e'}, 0xFFFFFFFF), false)
	// Seed: two well-formed chunks back-to-back.
	two := buildChunkHeader([4]byte{'f', 'i', 'r', 's'}, 4)
	two = append(two, make([]byte, 4)...)
	two = append(two, buildChunkHeader([4]byte{'s', 'e', 'c', 'd'}, 0)...)
	f.Add(two, true)

	f.Fuzz(func(t *testing.T, data []byte, _ bool) {
		// The fuzz corpus boolean is unused at runtime; it exists only to widen
		// the seed space for the fuzzer's mutation engine.
		r := bytes.NewReader(data)

		// Walk up to a reasonable number of chunks.  We stop on any error
		// (expected for truncated/corrupt input) and assert only "no panic".
		const maxChunks = 64
		for range maxChunks {
			c, err := ReadChunk(r)
			if err != nil {
				return // EOF or truncation — expected on arbitrary input
			}
			// Clamp the skip to avoid seeking far past a tiny corpus buffer
			// and spending all fuzz time on I/O latency rather than parsing.
			// Cap at 1 MiB of effective skip; anything larger is still valid
			// per the contract (Size is not validated here) but not interesting.
			const skipCap = 1 << 20
			if c.Size > skipCap {
				// Still call SkipChunk to exercise the seek path; it will
				// return an error on the short buffer, which is fine.
				_ = SkipChunk(r, c)
				return
			}
			if err := SkipChunk(r, c); err != nil {
				return // seek past available data — expected on short input
			}
		}
	})
}
