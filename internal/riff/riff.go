// Package riff provides a minimal RIFF chunk reader used by the webp container package.
package riff

import (
	"encoding/binary"
	"io"
)

// Chunk represents a RIFF chunk header.
type Chunk struct {
	// FourCC is the four-character chunk identifier.
	FourCC [4]byte
	// Size is the data size in bytes (excluding the 8-byte header).
	// Chunks with odd Size have a 1-byte padding byte that is not counted.
	Size uint32
	// Offset is the position of the first data byte within the stream.
	Offset int64
}

// FourCCString returns the chunk identifier as a string.
func (c *Chunk) FourCCString() string {
	return string(c.FourCC[:])
}

// Equal reports whether the FourCC equals v without allocating.
func (c *Chunk) Equal(v [4]byte) bool {
	return c.FourCC == v
}

// ReadChunk reads the next RIFF chunk header from r.
//
// CONTRACT: ReadChunk performs NO bounds validation on the returned Chunk.Size.
// The caller is responsible for verifying that Chunk.Size does not exceed the
// bytes remaining in the stream before attempting to read or seek past the data
// region. Passing an unchecked Size to SkipChunk on a short stream will cause
// a seek past EOF; the io.ReadSeeker will return an error at that point, but the
// caller must handle it explicitly. This contract is intentional: the RIFF
// package is a thin, zero-copy header decoder; policy decisions (size limits,
// maximum depth) belong in the consumer (e.g. format/webp).
//
// ReadChunk allocates its own 8-byte scratch header on every call (see
// ReadChunkBuf's doc comment for why this is unavoidable for a single,
// stateless call). Callers that read many chunks from the same stream in a
// loop should use ReadChunkBuf with a buffer declared once outside the loop
// to amortise that allocation across the whole scan (task #209).
func ReadChunk(r io.ReadSeeker) (Chunk, error) {
	var hdr [8]byte
	return ReadChunkBuf(r, &hdr)
}

// ReadChunkBuf reads the next RIFF chunk header from r using hdr as scratch
// space, avoiding an internal allocation on every call.
//
// Rationale: io.ReadFull(r, hdr[:]) passes hdr's address through the
// io.Reader interface method call. Because r's concrete type is unknown at
// compile time, the Go compiler cannot prove that the callee does not retain
// the slice beyond the call, so it conservatively heap-allocates hdr — this
// holds regardless of whether hdr is declared inside ReadChunk itself or by
// its caller; the allocation is inherent to passing a stack buffer through an
// interface-typed Read call (verified with `go build -gcflags=-m`).
//
// The one available mitigation is amortisation: a caller that reads N chunks
// from the same stream (e.g. format/webp's readWebPChunks) can declare a
// single [8]byte buffer before the loop and pass its address to ReadChunkBuf
// on every iteration, paying the heap allocation once for the whole scan
// instead of once per chunk. ReadChunk itself cannot do this because it is
// stateless between calls; use ReadChunkBuf directly when scanning a chunk
// list.
//
// hdr must not be nil.
func ReadChunkBuf(r io.ReadSeeker, hdr *[8]byte) (Chunk, error) {
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Chunk{}, err
	}
	var c Chunk
	copy(c.FourCC[:], hdr[:4])
	c.Size = binary.LittleEndian.Uint32(hdr[4:])
	pos, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return Chunk{}, err
	}
	c.Offset = pos
	return c, nil
}

// ReadChunkHeaderAt reads the next RIFF chunk header from r (using hdr as
// scratch space, exactly like ReadChunkBuf) and sets Chunk.Offset to offset
// instead of discovering it via Seek.
//
// Unlike ReadChunkBuf, this variant takes a plain io.Reader and never calls
// Seek: it trusts the caller to already know the byte position of the first
// data byte (immediately after the 8-byte header this call is about to
// consume) because the caller is reading the stream sequentially and tracking
// its own running offset — the exact situation format/webp's readWebPChunks
// is in. This removes the "1 Seek(SeekCurrent) per chunk" cost ReadChunkBuf
// pays purely to populate a field the caller could compute for free (task
// #234).
//
// offset must equal the true byte position of the first data byte for the
// chunk about to be read (i.e. the position immediately after the 8-byte
// header). Passing an incorrect value produces a Chunk with a wrong Offset
// field but does not otherwise affect parsing: FourCC and Size are read
// directly from r regardless.
//
// hdr must not be nil.
func ReadChunkHeaderAt(r io.Reader, hdr *[8]byte, offset int64) (Chunk, error) {
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Chunk{}, err
	}
	var c Chunk
	copy(c.FourCC[:], hdr[:4])
	c.Size = binary.LittleEndian.Uint32(hdr[4:])
	c.Offset = offset
	return c, nil
}

// SkipChunk advances r past the data (and any padding byte) of c.
//
// CONTRACT: SkipChunk performs NO bounds validation on c.Size. The caller must
// have verified that c.Offset + c.Size (with odd-padding adjustment) does not
// exceed the total stream length before calling SkipChunk. On a short stream the
// underlying io.Seeker will return an error; SkipChunk propagates it but makes no
// other attempt to prevent the seek-past-EOF. See ReadChunk for the full bounds
// contract rationale.
func SkipChunk(r io.ReadSeeker, c Chunk) error {
	skip := int64(c.Size)
	if c.Size%2 != 0 {
		skip++ // padding byte
	}
	_, err := r.Seek(c.Offset+skip, io.SeekStart)
	return err
}
