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
	truncated := []byte{0x52, 0x49, 0x46} // only 3 bytes
	r := bytes.NewReader(truncated)
	_, err := ReadChunk(r)
	if err == nil {
		t.Fatal("expected error for truncated header, got nil")
	}
}

// TestReadChunkEmptyReader verifies behaviour on a completely empty reader.
func TestReadChunkEmptyReader(t *testing.T) {
	r := bytes.NewReader(nil)
	_, err := ReadChunk(r)
	if err == nil {
		t.Fatal("expected error for empty reader, got nil")
	}
}

// TestReadChunkZeroSize verifies that a chunk with size=0 is parsed correctly.
func TestReadChunkZeroSize(t *testing.T) {
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
