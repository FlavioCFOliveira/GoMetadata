package jpeg

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"
)

// Extended XMP chunk accumulation (task #394, sprint 50).
//
// A JPEG that carries extended XMP splits it over several APP1 segments, each
// holding a 75-byte header (identifier, GUID, full length, offset) and at most
// maxExtChunkSize bytes of the document (Adobe XMP Specification Part 3
// §1.1.3.1, pp. 14-15: GUID, full length and offset are written in this
// order, the integers big-endian). The reader must copy every chunk out of
// the pooled scratch buffer, because the next segment overwrites it, and must
// assemble the chunks in offset order once the whole file has been scanned.
//
// extAcc stores the chunks of one GUID in a single block sized from the
// declared full length, so a conforming file costs one copy of the extended
// XMP in total, and the assembled document is the block itself (no further
// copy) whenever the chunks were stored in offset order, as every real writer
// emits them. The pre-sizing is bounded by the bytes that the source still
// holds, so a header that declares more than the file contains cannot make
// the reader allocate more than the file is long.

// wireHeadroom is the room that the first block of a large extended XMP
// document reserves in front of the document, for the wire-frame header and
// the standard packet (see xmpWireMagic): the frame [magic][mainLen][main]
// [ext] is then the block itself, with no second copy of the extended XMP.
// The standard packet of a JPEG is one APP1 segment, so it never exceeds
// maxAPP1Payload bytes.
const wireHeadroom = 8 + 4 + maxAPP1Payload

// wireHeadroomMin is the smallest block capacity that reserves wireHeadroom:
// below it the second copy costs less than the reservation.
const wireHeadroomMin = 256 << 10

// maxUnboundedPrealloc bounds the pre-sizing of a chunk block when the number
// of bytes left in the source is unknown (the source cannot seek). Larger
// declared lengths are served block by block as the chunks arrive.
const maxUnboundedPrealloc = 1 << 20

// extGUID holds the chunks of one extended XMP GUID.
type extGUID struct {
	fullLen int64      // total size declared by the first chunk seen (#122)
	size    int64      // bytes accumulated so far, capped by maxExtendedXMPTotal (#40)
	chunks  []extChunk // in arrival order; data aliases block or an earlier block
	block   []byte     // current block: len is the bytes stored in it, cap its room
	blocks  int        // number of blocks allocated; 1 when block holds every chunk
	buf     []byte     // the allocation of the first block, headroom included; nil without headroom
	inPlace bool       // assemble returned the first block itself
}

// extAcc accumulates the extended XMP chunks of one JPEG scan, keyed by the
// uppercase GUID. The zero value is ready to use.
type extAcc struct {
	guids     map[string]*extGUID
	truncated bool      // a GUID was capped or a chunk was dropped (#134)
	src       io.Seeker // when not nil, gives the size of the file (see available)
	err       error     // set when src could not be repositioned; the scan must stop

	availState uint8 // 0: not computed, 1: computed
	avail      int64 // bytes left after the segment being read, or -1 when unknown
}

// available returns an upper bound of the bytes that the source still holds
// after the segment being read, or -1 when the source cannot tell. It seeks
// to the end and back once per scan, and only when an extended XMP chunk is
// found.
func (a *extAcc) available() int64 {
	if a.availState != 0 {
		return a.avail
	}
	a.availState, a.avail = 1, -1
	if a.src == nil {
		return -1
	}
	cur, err := a.src.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1
	}
	end, err := a.src.Seek(0, io.SeekEnd)
	if err != nil {
		// The position is unchanged by a failed Seek on every reader that
		// is in use; restore it anyway so the scan stays valid.
		if _, rerr := a.src.Seek(cur, io.SeekStart); rerr != nil {
			a.err = fmt.Errorf("jpeg: seek: %w", rerr)
		}
		return -1
	}
	if _, err = a.src.Seek(cur, io.SeekStart); err != nil {
		a.err = fmt.Errorf("jpeg: seek: %w", err)
		return -1
	}
	if end >= cur {
		a.avail = end - cur
	}
	return a.avail
}

// canonicalGUID returns the uppercase form of the 32 hexadecimal digits raw,
// and false when raw holds any other byte.
func canonicalGUID(raw []byte) (key [guidLen]byte, ok bool) {
	if !isAllHex(raw) {
		return key, false
	}
	for i, c := range raw {
		if c >= 'a' && c <= 'f' {
			c -= 'a' - 'A'
		}
		key[i] = c
	}
	return key, true
}

// add adds one extended XMP chunk body to the accumulator, enforcing the
// maxExtendedXMPTotal aggregate cap per GUID (#40).
//
// body is the APP1 payload after the identifier and must be at least 40 bytes
// (32-byte GUID + 4-byte fullLen + 4-byte offset). The wire fullLen field of
// the first chunk of a GUID is validated against maxExtendedXMPTotal; if it
// exceeds the cap the GUID is blacklisted. Subsequent chunks are individually
// bounded by the same cap. Adobe XMP Specification Part 3 §1.1.4 describes the
// wire fields.
func (a *extAcc) add(body []byte) {
	// #135: validate that the GUID is exactly 32 hex characters and
	// canonicalise it to uppercase before keying the map. Adobe XMP
	// Specification Part 3 §1.1.4 defines the GUID as a 32-character uppercase
	// hex string; real-world writers may emit lowercase. A case mismatch
	// between the main packet's HasExtendedXMP attribute and the chunk GUID
	// would silently discard all extended properties without this
	// normalisation.
	key, ok := canonicalGUID(body[:guidLen])
	if !ok {
		return // GUID contains non-hex characters: discard this chunk silently
	}
	fullLen := int64(binary.BigEndian.Uint32(body[32:36]))
	offset := binary.BigEndian.Uint32(body[36:40])
	chunkData := body[40:]

	// First-seen check: validate the declared total against the cap and
	// enforce the per-file GUID count cap.
	//
	// Adobe XMP Specification Part 3 §1.1.3.1: a conforming file uses exactly
	// one GUID. We allow up to maxExtendedXMPGUIDs (4) to tolerate
	// non-conforming files without permitting unbounded memory growth: an
	// adversary crafting a JPEG with many distinct GUIDs (each up to
	// maxExtendedXMPTotal bytes) could otherwise aggregate far beyond 16 MiB
	// of allocations.
	g := a.guids[string(key[:])] // a map lookup by string(bytes) does not allocate
	if g == nil {
		if len(a.guids) >= maxExtendedXMPGUIDs || fullLen > maxExtendedXMPTotal {
			a.truncated = true
			return
		}
		if a.guids == nil {
			a.guids = make(map[string]*extGUID, 1)
		}
		// #122: record the wire-declared total size so buildXMPResult can
		// validate that the assembled chunks cover exactly fullLen bytes with
		// no gaps or overlaps. Adobe XMP Specification Part 3 §1.1.4.
		g = &extGUID{fullLen: fullLen}
		a.guids[string(key[:])] = g
	}

	// Per-chunk running total check: drop this chunk if it would exceed the cap.
	if g.size+int64(len(chunkData)) > maxExtendedXMPTotal {
		a.truncated = true
		return
	}

	// Copy chunk data: body aliases the scratch buffer and must outlive this
	// loop.
	if cap(g.block)-len(g.block) < len(chunkData) {
		room := a.blockCap(g, len(chunkData))
		if g.blocks == 0 && room >= wireHeadroomMin {
			g.buf = make([]byte, wireHeadroom+room)
			g.block = g.buf[wireHeadroom:wireHeadroom:len(g.buf)]
		} else {
			g.block = make([]byte, 0, room)
		}
		g.blocks++
	}
	start := len(g.block)
	g.block = append(g.block, chunkData...)
	g.size += int64(len(chunkData))
	g.chunks = append(g.chunks, extChunk{
		offset: offset,
		data:   g.block[start:len(g.block):len(g.block)],
	})
}

// blockCap returns the capacity of the next block of g, which must hold at
// least n bytes: the part of the declared full length that is still to come,
// bounded by what the source can still deliver (a header cannot make the
// reader allocate more than the file holds) and by maxUnboundedPrealloc when
// the size of the source is unknown.
func (a *extAcc) blockCap(g *extGUID, n int) int {
	want := max(int64(n), g.fullLen-g.size)
	limit := int64(maxUnboundedPrealloc)
	if avail := a.available(); avail >= 0 {
		limit = avail + int64(n) // the chunk being stored is already consumed
	}
	if want > limit {
		want = max(limit, int64(n))
	}
	return int(want)
}

// assemble sorts the chunks of g by offset, validates that they form a
// contiguous, non-overlapping sequence starting at offset 0 and covering
// exactly the declared full length, and returns the assembled document and a
// validity flag.
//
// Validation rules (Adobe XMP Specification Part 3 §1.1.4, #122):
//   - First chunk must have offset == 0.
//   - Each subsequent chunk must start exactly where the previous one ended
//     (chunk[i].offset == chunk[i-1].offset + len(chunk[i-1].data)).
//   - The assembled length must equal the wire-declared full length.
//
// On any violation it returns (nil, false): buildXMPResult must degrade to
// the main packet only rather than returning corrupted or doubled reassembled
// bytes.
//
// When one block holds every chunk and the chunks were already in offset
// order, the assembled document is that block and nothing is copied. The
// result then aliases g and must not be modified.
func (g *extGUID) assemble() ([]byte, bool) {
	chunks := g.chunks
	inOrder := slices.IsSortedFunc(chunks, compareChunkOffset)
	if !inOrder {
		slices.SortFunc(chunks, compareChunkOffset)
	}
	totalLen, ok := contiguousLen(chunks)
	// The wire-declared total is always known. Adobe XMP Specification Part 3
	// §1.1.4: fullLength is the total size of the extended XMP document; every
	// byte must be accounted for.
	if !ok || int64(totalLen) != g.fullLen {
		return nil, false
	}

	if inOrder && g.blocks == 1 {
		g.inPlace = true
		return g.block[:totalLen:totalLen], true
	}
	extBytes := make([]byte, 0, totalLen)
	for _, c := range chunks {
		extBytes = append(extBytes, c.data...)
	}
	return extBytes, true
}

// compareChunkOffset orders chunks by their offset.
func compareChunkOffset(a, b extChunk) int {
	switch {
	case a.offset < b.offset:
		return -1
	case a.offset > b.offset:
		return 1
	}
	return 0
}

// contiguousLen reports whether chunks, sorted by offset, start at offset 0
// and each starts where the previous one ends, and returns their total size.
func contiguousLen(chunks []extChunk) (total int, ok bool) {
	// First chunk must start at offset 0.
	if len(chunks) == 0 || chunks[0].offset != 0 {
		return 0, false
	}
	for i, c := range chunks {
		// Each subsequent chunk must be contiguous with the previous one.
		if i > 0 && c.offset != chunks[i-1].offset+uint32(len(chunks[i-1].data)) { //nolint:gosec // G115: chunk offset/size bounded by maxExtendedXMPTotal (16 MiB)
			// Gap or overlap detected: reject the assembled payload.
			return 0, false
		}
		total += len(c.data)
	}
	return total, true
}

// wireFrame returns the wire-frame (see xmpWireMagic) of the standard packet
// main and the document ext that g.assemble returned. It is built in front of
// the block when assemble left the document in place and the block has the
// room, and otherwise copied. The frame then shares memory with ext, and
// both are read-only.
func (g *extGUID) wireFrame(main, ext []byte) []byte {
	if !g.inPlace || g.buf == nil || len(main) > maxAPP1Payload {
		return encodeXMPWire(main, ext)
	}
	start := wireHeadroom - (len(xmpWireMagic) + 4 + len(main))
	b := g.buf[start : wireHeadroom+len(ext) : wireHeadroom+len(ext)]
	n := copy(b, xmpWireMagic)
	binary.BigEndian.PutUint32(b[n:], uint32(len(main))) //nolint:gosec // G115: len(main) <= maxAPP1Payload
	copy(b[n+4:], main)
	return b
}
