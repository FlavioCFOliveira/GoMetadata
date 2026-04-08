// Package bmff provides tests for the ISO Base Media File Format box reader.
package bmff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// ---- helpers ----------------------------------------------------------------

// buildStandardBox constructs a minimal ISOBMFF box byte stream with an
// 8-byte header.  payload is appended verbatim after the header so that the
// total stream length is 8 + len(payload).  size encodes the value placed in
// the first four header bytes; pass the full box size (header + payload) for
// well-formed boxes.
func buildStandardBox(size uint32, typ [4]byte, payload []byte) []byte {
	buf := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], size)
	copy(buf[4:8], typ[:])
	return append(buf, payload...)
}

// buildExtendedBox constructs a box with size==1 in the 4-byte field and a
// 64-bit extended size in the next 8 bytes (total header: 16 bytes).
// extSize is the value written into the 64-bit field.
func buildExtendedBox(extSize uint64, typ [4]byte, payload []byte) []byte {
	buf := make([]byte, 16, 16+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], 1) // sentinel: extended size follows
	copy(buf[4:8], typ[:])
	binary.BigEndian.PutUint64(buf[8:16], extSize)
	return append(buf, payload...)
}

// buildSizeZeroBox constructs a box whose 4-byte size field is 0, which by
// ISO 14496-12 §4.2 means "the box extends to EOF".
func buildSizeZeroBox(typ [4]byte, payload []byte) []byte {
	return buildStandardBox(0, typ, payload)
}

// currentPos returns the current reader position using Seek(0, SeekCurrent).
func currentPos(t *testing.T, r io.ReadSeeker) int64 {
	t.Helper()
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(0, SeekCurrent): %v", err)
	}
	return pos
}

// ---- TestReadBox_Standard ---------------------------------------------------

// TestReadBox_Standard verifies that a well-formed 8-byte-header box is parsed
// with correct Size, Type, Offset, and DataSize fields.
func TestReadBox_Standard(t *testing.T) {
	t.Parallel()
	ftyp := [4]byte{'f', 't', 'y', 'p'}
	mdat := [4]byte{'m', 'd', 'a', 't'}
	free := [4]byte{'f', 'r', 'e', 'e'}

	tests := []struct {
		name       string
		typ        [4]byte
		payload    []byte
		wantSize   uint64
		wantOffset int64
		wantData   uint64
	}{
		{
			name:       "ftyp with 4-byte payload",
			typ:        ftyp,
			payload:    []byte{0x01, 0x02, 0x03, 0x04},
			wantSize:   12,
			wantOffset: 8,
			wantData:   4,
		},
		{
			name:       "mdat with 16-byte payload",
			typ:        mdat,
			payload:    make([]byte, 16),
			wantSize:   24,
			wantOffset: 8,
			wantData:   16,
		},
		{
			name:       "free box with empty payload (size==8)",
			typ:        free,
			payload:    nil,
			wantSize:   8,
			wantOffset: 8,
			wantData:   0,
		},
		{
			name:       "box with 255-byte payload",
			typ:        [4]byte{'u', 'u', 'i', 'd'},
			payload:    make([]byte, 255),
			wantSize:   263,
			wantOffset: 8,
			wantData:   255,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := buildStandardBox(uint32(tc.wantSize), tc.typ, tc.payload) //nolint:gosec // G115: test helper, intentional type cast
			r := bytes.NewReader(raw)

			box, err := ReadBox(r)
			if err != nil {
				t.Fatalf("ReadBox: unexpected error: %v", err)
			}
			if box.Size != tc.wantSize {
				t.Errorf("Size = %d, want %d", box.Size, tc.wantSize)
			}
			if box.Type != tc.typ {
				t.Errorf("Type = %v, want %v", box.Type, tc.typ)
			}
			if box.Offset != tc.wantOffset {
				t.Errorf("Offset = %d, want %d", box.Offset, tc.wantOffset)
			}
			if box.DataSize != tc.wantData {
				t.Errorf("DataSize = %d, want %d", box.DataSize, tc.wantData)
			}
		})
	}
}

// ---- TestReadBox_ExtendedSize -----------------------------------------------

// TestReadBox_ExtendedSize verifies that when the 4-byte size field equals 1,
// ReadBox reads the subsequent 8 bytes as the actual 64-bit box size, sets
// Offset to 16 (past the 16-byte header), and computes DataSize correctly.
func TestReadBox_ExtendedSize(t *testing.T) {
	t.Parallel()
	typ := [4]byte{'m', 'd', 'a', 't'}

	tests := []struct {
		name       string
		extSize    uint64 // value in the 64-bit field
		payloadLen int
		wantOffset int64
		wantData   uint64
	}{
		{
			name:       "no payload, header only",
			extSize:    16,
			payloadLen: 0,
			wantOffset: 16,
			wantData:   0,
		},
		{
			name:       "small payload",
			extSize:    32,
			payloadLen: 16,
			wantOffset: 16,
			wantData:   16,
		},
		{
			name:       "large 64-bit size",
			extSize:    0x0000_0100_0000_0010, // much larger than the actual stream, header counts
			payloadLen: 0,
			wantOffset: 16,
			wantData:   0x0000_0100_0000_0010 - 16,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := buildExtendedBox(tc.extSize, typ, make([]byte, tc.payloadLen))
			r := bytes.NewReader(raw)

			box, err := ReadBox(r)
			if err != nil {
				t.Fatalf("ReadBox: unexpected error: %v", err)
			}
			if box.Size != tc.extSize {
				t.Errorf("Size = %d, want %d", box.Size, tc.extSize)
			}
			if box.Type != typ {
				t.Errorf("Type = %v, want %v", box.Type, typ)
			}
			if box.Offset != tc.wantOffset {
				t.Errorf("Offset = %d, want %d", box.Offset, tc.wantOffset)
			}
			if box.DataSize != tc.wantData {
				t.Errorf("DataSize = %d, want %d", box.DataSize, tc.wantData)
			}
		})
	}
}

// ---- TestReadBox_SizeZero ---------------------------------------------------

// TestReadBox_SizeZero verifies that a box with size==0 is parsed with
// Size==0 and DataSize==0 (caller is responsible for reading to EOF), and
// that Offset is set to 8 (the byte immediately after the standard header).
//
// ISO 14496-12 §4.2: "If size is 0, then this box is the last one in the
// file, and its contents extend to the end of the file."
func TestReadBox_SizeZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		typ     [4]byte
		payload []byte
	}{
		{
			name:    "zero-size box with no trailing payload",
			typ:     [4]byte{'m', 'd', 'a', 't'},
			payload: nil,
		},
		{
			name:    "zero-size box with trailing image data",
			typ:     [4]byte{'m', 'd', 'a', 't'},
			payload: make([]byte, 1024),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := buildSizeZeroBox(tc.typ, tc.payload)
			r := bytes.NewReader(raw)

			box, err := ReadBox(r)
			if err != nil {
				t.Fatalf("ReadBox: unexpected error: %v", err)
			}
			if box.Size != 0 {
				t.Errorf("Size = %d, want 0", box.Size)
			}
			if box.DataSize != 0 {
				t.Errorf("DataSize = %d, want 0 (extends to EOF)", box.DataSize)
			}
			if box.Offset != 8 {
				t.Errorf("Offset = %d, want 8", box.Offset)
			}
			if box.Type != tc.typ {
				t.Errorf("Type = %v, want %v", box.Type, tc.typ)
			}
		})
	}
}

// ---- TestReadBox_Truncated --------------------------------------------------

// TestReadBox_Truncated verifies that ReadBox propagates an error (io.EOF or
// io.ErrUnexpectedEOF) when the input is shorter than a complete box header.
func TestReadBox_Truncated(t *testing.T) {
	t.Parallel()
	typ := [4]byte{'f', 't', 'y', 'p'}
	fullHeader := buildStandardBox(12, typ, []byte{0x00, 0x00, 0x00, 0x00})

	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "completely empty",
			data: nil,
		},
		{
			name: "one byte",
			data: fullHeader[:1],
		},
		{
			name: "three bytes",
			data: fullHeader[:3],
		},
		{
			name: "seven bytes (one short of a complete header)",
			data: fullHeader[:7],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := bytes.NewReader(tc.data)
			_, err := ReadBox(r)
			if err == nil {
				t.Fatal("ReadBox: expected error for truncated input, got nil")
			}
			// io.ReadFull returns io.EOF when 0 bytes were read and
			// io.ErrUnexpectedEOF when some (but not all) bytes were read.
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("ReadBox error = %v, want io.EOF or io.ErrUnexpectedEOF", err)
			}
		})
	}
}

// ---- TestReadBox_EmptyData --------------------------------------------------

// TestReadBox_EmptyData verifies that a box whose total size equals exactly 8
// (header only, zero data bytes) is correctly parsed with DataSize==0.
func TestReadBox_EmptyData(t *testing.T) {
	t.Parallel()
	typ := [4]byte{'f', 'r', 'e', 'e'}
	// size field = 8, no payload bytes.
	raw := buildStandardBox(8, typ, nil)
	r := bytes.NewReader(raw)

	box, err := ReadBox(r)
	if err != nil {
		t.Fatalf("ReadBox: %v", err)
	}
	if box.Size != 8 {
		t.Errorf("Size = %d, want 8", box.Size)
	}
	if box.DataSize != 0 {
		t.Errorf("DataSize = %d, want 0", box.DataSize)
	}
	if box.Offset != 8 {
		t.Errorf("Offset = %d, want 8", box.Offset)
	}
}

// ---- TestSkipBox ------------------------------------------------------------

// TestSkipBox verifies that SkipBox advances the reader to exactly the first
// byte of the next box (i.e., box.Offset + box.DataSize).
func TestSkipBox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		typ         [4]byte
		payloadSize int
		wantPos     int64 // expected stream position after SkipBox
	}{
		{
			name:        "header-only box",
			typ:         [4]byte{'f', 'r', 'e', 'e'},
			payloadSize: 0,
			wantPos:     8,
		},
		{
			name:        "10-byte payload",
			typ:         [4]byte{'m', 'd', 'a', 't'},
			payloadSize: 10,
			wantPos:     18,
		},
		{
			name:        "large payload",
			typ:         [4]byte{'u', 'u', 'i', 'd'},
			payloadSize: 1024,
			wantPos:     1032,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			totalSize := 8 + tc.payloadSize
			raw := buildStandardBox(uint32(totalSize), tc.typ, make([]byte, tc.payloadSize)) //nolint:gosec // G115: test helper, intentional type cast
			r := bytes.NewReader(raw)

			box, err := ReadBox(r)
			if err != nil {
				t.Fatalf("ReadBox: %v", err)
			}

			if err := SkipBox(r, box); err != nil {
				t.Fatalf("SkipBox: %v", err)
			}

			pos := currentPos(t, r)
			if pos != tc.wantPos {
				t.Errorf("reader position after SkipBox = %d, want %d", pos, tc.wantPos)
			}
		})
	}
}

// ---- TestSkipBox_SizeZero --------------------------------------------------

// TestSkipBox_SizeZero verifies that SkipBox on a size==0 box seeks to EOF,
// leaving the reader at the end of the stream.
func TestSkipBox_SizeZero(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		typ     [4]byte
		payload []byte
	}{
		{
			name:    "zero-size box, no trailing data",
			typ:     [4]byte{'m', 'd', 'a', 't'},
			payload: nil,
		},
		{
			name:    "zero-size box, 512 bytes of trailing data",
			typ:     [4]byte{'m', 'd', 'a', 't'},
			payload: make([]byte, 512),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := buildSizeZeroBox(tc.typ, tc.payload)
			totalLen := int64(len(raw))
			r := bytes.NewReader(raw)

			box, err := ReadBox(r)
			if err != nil {
				t.Fatalf("ReadBox: %v", err)
			}
			if box.Size != 0 {
				t.Fatalf("pre-condition: Size = %d, want 0", box.Size)
			}

			if err := SkipBox(r, box); err != nil {
				t.Fatalf("SkipBox: %v", err)
			}

			pos := currentPos(t, r)
			if pos != totalLen {
				t.Errorf("reader position after SkipBox(size==0) = %d, want %d (EOF)", pos, totalLen)
			}
		})
	}
}

// ---- TestTypeString ---------------------------------------------------------

// TestTypeString verifies that TypeString returns the exact four bytes of the
// box type as a string, including non-ASCII and null bytes, since ISOBMFF
// type codes are binary identifiers, not text strings.
func TestTypeString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		typ  [4]byte
		want string
	}{
		{"ftyp", [4]byte{'f', 't', 'y', 'p'}, "ftyp"},
		{"mdat", [4]byte{'m', 'd', 'a', 't'}, "mdat"},
		{"moov", [4]byte{'m', 'o', 'o', 'v'}, "moov"},
		{"uuid", [4]byte{'u', 'u', 'i', 'd'}, "uuid"},
		{"all zeros", [4]byte{0x00, 0x00, 0x00, 0x00}, "\x00\x00\x00\x00"},
		{"non-ASCII bytes", [4]byte{0xFF, 0xFE, 0x00, 0x01}, "\xff\xfe\x00\x01"},
		{"mixed", [4]byte{'i', 'l', 'o', 'c'}, "iloc"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			box := &Box{Type: tc.typ}
			got := box.TypeString()
			if got != tc.want {
				t.Errorf("TypeString() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---- TestReadBox_HeaderTruncated --------------------------------------------

// TestReadBox_HeaderTruncated directly tests the boundary condition where the
// input is shorter than the minimum 8-byte header, focusing specifically on
// each byte boundary from 0 to 7 bytes.
func TestReadBox_HeaderTruncated(t *testing.T) {
	t.Parallel()
	// A valid 8-byte header for reference.
	valid := [8]byte{
		0x00, 0x00, 0x00, 0x10, // size = 16
		'f', 't', 'y', 'p', // type
	}

	for n := range 8 {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			r := bytes.NewReader(valid[:n])
			_, err := ReadBox(r)
			if err == nil {
				t.Fatalf("ReadBox with %d-byte input: expected error, got nil", n)
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("ReadBox(%d bytes) error = %v; want io.EOF or io.ErrUnexpectedEOF", n, err)
			}
		})
	}
}

// ---- TestReadBox_ExtendedSize_Truncated ------------------------------------

// TestReadBox_ExtendedSize_Truncated verifies that ReadBox returns an error
// when the 4-byte size field is 1 (signalling extended size) but the
// subsequent 8-byte extended size field is missing or truncated.
func TestReadBox_ExtendedSize_Truncated(t *testing.T) {
	t.Parallel()
	typ := [4]byte{'m', 'd', 'a', 't'}

	tests := []struct {
		name  string
		extra int // bytes appended after the sentinel header (0..7, must be <8)
	}{
		{"no extended field at all", 0},
		{"1 byte of extended field", 1},
		{"4 bytes of extended field", 4},
		{"7 bytes of extended field", 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Build a sentinel header (size==1) followed by a partial extended field.
			buf := make([]byte, 8+tc.extra)
			binary.BigEndian.PutUint32(buf[0:4], 1) // sentinel
			copy(buf[4:8], typ[:])
			// The remaining tc.extra bytes are already zero from make.

			r := bytes.NewReader(buf)
			_, err := ReadBox(r)
			if err == nil {
				t.Fatalf("ReadBox: expected error for truncated extended field, got nil")
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("error = %v; want io.EOF or io.ErrUnexpectedEOF", err)
			}
		})
	}
}

// ---- TestSkipBox_PositionAfterMultipleBoxes --------------------------------

// TestSkipBox_PositionAfterMultipleBoxes verifies that sequential ReadBox +
// SkipBox calls correctly traverse a concatenated multi-box stream, leaving
// the reader at the expected byte offset after each hop.
func TestSkipBox_PositionAfterMultipleBoxes(t *testing.T) {
	t.Parallel()
	// Build a stream of three consecutive boxes.
	boxes := []struct {
		typ         [4]byte
		payloadSize int
	}{
		{[4]byte{'f', 't', 'y', 'p'}, 4},
		{[4]byte{'f', 'r', 'e', 'e'}, 0},
		{[4]byte{'m', 'd', 'a', 't'}, 20},
	}

	stream := make([]byte, 0, func() int {
		total := 0
		for _, b := range boxes {
			total += 8 + b.payloadSize
		}
		return total
	}())
	expectedEnd := make([]int64, 0, len(boxes))
	var pos int64
	for _, b := range boxes {
		total := 8 + b.payloadSize
		stream = append(stream, buildStandardBox(uint32(total), b.typ, make([]byte, b.payloadSize))...) //nolint:gosec // G115: test helper, intentional type cast
		pos += int64(total)
		expectedEnd = append(expectedEnd, pos)
	}

	r := bytes.NewReader(stream)
	for i, b := range boxes {
		box, err := ReadBox(r)
		if err != nil {
			t.Fatalf("box %d ReadBox: %v", i, err)
		}
		if box.Type != b.typ {
			t.Errorf("box %d Type = %v, want %v", i, box.Type, b.typ)
		}
		if err := SkipBox(r, box); err != nil {
			t.Fatalf("box %d SkipBox: %v", i, err)
		}
		pos := currentPos(t, r)
		if pos != expectedEnd[i] {
			t.Errorf("box %d: position after SkipBox = %d, want %d", i, pos, expectedEnd[i])
		}
	}

	// Confirm we are at EOF.
	_, err := ReadBox(r)
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF after last box, got %v", err)
	}
}

// ---- TestReadBox_ExtendedSize_Offset ---------------------------------------

// TestReadBox_ExtendedSize_Offset confirms that Offset for an extended-size
// box is always 16 (after the 16-byte header: 4+4+8), regardless of payload.
func TestReadBox_ExtendedSize_Offset(t *testing.T) {
	t.Parallel()
	typ := [4]byte{'m', 'd', 'a', 't'}
	payloads := []int{0, 1, 8, 100}

	for _, plen := range payloads {
		extSize := uint64(16 + plen) //nolint:gosec // G115: test helper, intentional type cast
		raw := buildExtendedBox(extSize, typ, make([]byte, plen))
		r := bytes.NewReader(raw)

		box, err := ReadBox(r)
		if err != nil {
			t.Fatalf("payload %d: ReadBox: %v", plen, err)
		}
		if box.Offset != 16 {
			t.Errorf("payload %d: Offset = %d, want 16", plen, box.Offset)
		}
		if box.DataSize != uint64(plen) { //nolint:gosec // G115: test helper, intentional type cast
			t.Errorf("payload %d: DataSize = %d, want %d", plen, box.DataSize, plen)
		}
	}
}

// ---- TestReadBox_TypePreservation ------------------------------------------

// TestReadBox_TypePreservation ensures all 256 possible byte values in each
// of the four type positions are preserved verbatim.  ISOBMFF type codes are
// opaque 4-byte identifiers; no normalisation is applied.
func TestReadBox_TypePreservation(t *testing.T) {
	t.Parallel()
	// Test all 256 values in each byte position independently.
	for pos := range 4 {
		for v := range 256 {
			var typ [4]byte
			// Set a stable, recognisable baseline.
			copy(typ[:], "aaaa")
			typ[pos] = byte(v)

			raw := buildStandardBox(8, typ, nil)
			r := bytes.NewReader(raw)

			box, err := ReadBox(r)
			if err != nil {
				t.Fatalf("pos=%d v=%d: ReadBox: %v", pos, v, err)
			}
			if box.Type != typ {
				t.Errorf("pos=%d v=%d: Type = %v, want %v", pos, v, box.Type, typ)
			}
		}
	}
}

// ---- TestBoxEqual -----------------------------------------------------------

// TestBoxEqual exercises the Equal method (0% coverage).
func TestBoxEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		typ     [4]byte
		compare [4]byte
		want    bool
	}{
		{"matching ftyp", [4]byte{'f', 't', 'y', 'p'}, [4]byte{'f', 't', 'y', 'p'}, true},
		{"non-matching", [4]byte{'m', 'd', 'a', 't'}, [4]byte{'f', 't', 'y', 'p'}, false},
		{"all zeros match", [4]byte{0, 0, 0, 0}, [4]byte{0, 0, 0, 0}, true},
		{"single byte differs", [4]byte{'m', 'o', 'o', 'f'}, [4]byte{'m', 'o', 'o', 'v'}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			box := &Box{Type: tc.typ}
			if got := box.Equal(tc.compare); got != tc.want {
				t.Errorf("Equal(%v) = %v, want %v", tc.compare, got, tc.want)
			}
		})
	}
}

// ---- BenchmarkReadBox -------------------------------------------------------

// BenchmarkReadBox measures the throughput of reading a standard 8-byte box
// header.  This is the hot path for any ISOBMFF container traversal.
func BenchmarkReadBox(b *testing.B) {
	typ := [4]byte{'m', 'd', 'a', 't'}
	const payloadSize = 1024
	raw := buildStandardBox(8+payloadSize, typ, make([]byte, payloadSize))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(raw)
		_, _ = ReadBox(r)
	}
}

// BenchmarkReadBoxExtended measures the cost of the extended-size path, which
// requires one additional io.ReadFull call.
func BenchmarkReadBoxExtended(b *testing.B) {
	typ := [4]byte{'m', 'd', 'a', 't'}
	const payloadSize = 1024
	raw := buildExtendedBox(16+payloadSize, typ, make([]byte, payloadSize))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(raw)
		_, _ = ReadBox(r)
	}
}

// BenchmarkSkipBox measures the cost of seeking past a box's data region.
func BenchmarkSkipBox(b *testing.B) {
	typ := [4]byte{'m', 'd', 'a', 't'}
	const payloadSize = 4096
	raw := buildStandardBox(8+payloadSize, typ, make([]byte, payloadSize))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r := bytes.NewReader(raw)
		box, err := ReadBox(r)
		if err != nil {
			b.Fatal(err)
		}
		_ = SkipBox(r, box)
	}
}
