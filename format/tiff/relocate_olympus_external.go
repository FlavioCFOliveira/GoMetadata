package tiff

// relocate_olympus_external.go — the Olympus MakerNote offsets that address
// data outside the IFD structures of the file (task #370).
//
// An Olympus MakerNote stores the offset of a preview image (PreviewImageStart
// with PreviewImageLength; see exif.ExternalOffsets for the tags and the byte
// each layout counts from). The copy-and-relocate serializer of this package
// moves the MakerNote with the re-encoded IFDs and moves the image blocks
// (strips, tiles, JPEG) behind them, so such an offset is right after a write
// only when the data it addresses moved by the same distance as the byte it
// counts from. Three cases arise:
//
//   - the data is inside the MakerNote (every ORF of the corpus: the MakerNote of
//     an E-M5 is 1.4 MB and holds the 874318-byte preview): it moves with the
//     MakerNote, so an offset counted from the MakerNote is still right, and one
//     counted from the TIFF header (the "OLYMP\0" layout, the E-1 and E-300)
//     changes by the distance the MakerNote moved;
//   - the data is inside an image block (strip, tile, JPEG, SubIFD block): it
//     moves with that block;
//   - the data follows every image block, and the metadata structures (the tail:
//     relocate_stream.go, planTail): it is copied unchanged after the last block,
//     so it moves by the distance the end of the last block moves;
//   - the data is outside every structure the serializer knows and before the end
//     of the last image block: a preview that sits between the IFDs and the image
//     data. It would be dropped, and its offset be stale; the serializer carries
//     it as a block of its own, as it does for the thumbnail of an "OLYMP\0"
//     MakerNote (relocate_orf.go, olympMNTagThumbnailImage), and points the offset
//     at the copy.
//
// An offset whose data cannot be placed is left as it is: the data lies outside
// the file, or overlaps the MakerNote or a block only partly, or lies inside the
// metadata structures, or is a JPEG stream that does not start with the SOI
// marker (the preview is a JPEG stream; ISO/IEC 10918-1 §B.1.1.3) where the
// bytes can be read. One case is not detected: an AFCP or FotoStation trailer
// that starts inside the metadata structures moves the start of the tail (planTail),
// and the offset of data behind it is then wrong by the difference; no file of
// the corpus has one.
//
// Reference: Nikon NEF has the same problem for its PreviewIFD (relocate_nef.go),
// solved with a standalone image block and a patch after the re-encode; ExifTool
// Olympus.pm marks the tags IsOffset with an OffsetPair and writes them back
// with the data (study only).

import (
	"bytes"
	"encoding/binary"
	"math"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// extKind says where the data of an external offset is.
type extKind uint8

const (
	extInMakerNote extKind = iota // moves with the MakerNote
	extInBlock                    // moves with an image block
	extOwnBlock                   // is carried as a block of its own
	extInTail                     // is in the tail, after the last block
)

// externalOffset is an exif.ExternalOffset with where its data goes.
type externalOffset struct {
	exif.ExternalOffset
	kind extKind
	blk  *imageBlock // the block that holds the data (extInBlock, extOwnBlock)
}

// olympusExternalPlan is the plan for the external offsets of one relocation.
// The methods accept a nil plan, which stands for "no such offsets".
type olympusExternalPlan struct {
	offs      []externalOffset
	newBlocks []*imageBlock
	tailStart uint64 // source offset where the tail starts (planTail)
}

// planOlympusExternal returns the plan for the external offsets of the Olympus
// MakerNote of e, a stream parsed from base whose image blocks are blocks (the
// blocks of every IFD and SubIFD), and whose file has fileLen bytes. It returns
// nil when e has no such offsets, which is nearly always: one prefix test of the
// MakerNote.
func planOlympusExternal(base []byte, e *exif.EXIF, blocks []*imageBlock, fileLen uint64) *olympusExternalPlan {
	if e == nil || !hasOlympusSignature(e.MakerNote) {
		return nil
	}
	offs := e.ExternalOffsets(base)
	if len(offs) == 0 {
		return nil
	}
	p := &olympusExternalPlan{}
	blocksEnd := endOfBlocks(blocks)
	p.tailStart = min(max(blocksEnd, metadataEnd(base)), fileLen)
	for _, o := range offs {
		if x, ok := p.place(o, base, blocks, blocksEnd, fileLen); ok {
			p.offs = append(p.offs, x)
		}
	}
	if len(p.offs) == 0 {
		return nil
	}
	return p
}

// hasOlympusSignature reports whether the MakerNote mn starts as an Olympus
// layout that stores a preview offset: "OLYMP" or "OM SYSTEM".
func hasOlympusSignature(mn []byte) bool {
	return bytes.HasPrefix(mn, []byte("OLYMP")) || bytes.HasPrefix(mn, []byte("OM SYSTEM"))
}

// place decides where the data of the external offset o goes (see the top of
// this file), appending a block of its own to p.newBlocks when it needs one. It
// returns false when the data cannot be placed.
func (p *olympusExternalPlan) place(o exif.ExternalOffset, base []byte, blocks []*imageBlock, blocksEnd, fileLen uint64) (externalOffset, bool) {
	t, n := o.Target(), int64(o.Length)
	if t < 8 || n <= 0 || uint64(t)+uint64(n) > fileLen {
		return externalOffset{}, false
	}
	x := externalOffset{ExternalOffset: o}
	switch {
	case t >= int64(o.MNStart) && t+n <= int64(o.MNStart)+int64(o.MNLen):
		x.kind = extInMakerNote
	case t < int64(o.MNStart)+int64(o.MNLen) && t+n > int64(o.MNStart):
		return externalOffset{}, false // straddles the end of the MakerNote
	case uint64(t) >= blocksEnd:
		if uint64(t) < p.tailStart {
			return externalOffset{}, false // inside the metadata structures
		}
		x.kind = extInTail
	default:
		return p.placeBeforeTheTail(x, base, blocks, blocksEnd)
	}
	return x, true
}

// placeBeforeTheTail places the data of x, which lies outside the MakerNote and
// before the end of the last image block: inside a block, or as a block of its
// own.
func (p *olympusExternalPlan) placeBeforeTheTail(x externalOffset, base []byte, blocks []*imageBlock, blocksEnd uint64) (externalOffset, bool) {
	t, n := x.Target(), int64(x.Length)
	ut, un := uint64(t), uint64(n) //nolint:gosec // G115: the caller checked t >= 8 and n > 0
	blk, overlap := blockAround(blocks, p.newBlocks, ut, un)
	switch {
	case overlap || ut+un > blocksEnd:
		return externalOffset{}, false
	case blk != nil:
		x.kind, x.blk = extInBlock, blk
	case int64(len(base)) >= t+2 && (base[t] != 0xFF || base[t+1] != 0xD8):
		return externalOffset{}, false // readable here and not a JPEG stream
	default:
		blk = &imageBlock{srcOffset: ut, size: un}
		p.newBlocks = append(p.newBlocks, blk)
		x.kind, x.blk = extOwnBlock, blk
	}
	return x, true
}

// blockAround returns the block of the lists that holds the n bytes at off
// whole, or overlap true when one of them overlaps the range without holding it.
func blockAround(a, b []*imageBlock, off, n uint64) (blk *imageBlock, overlap bool) {
	for _, list := range [...][]*imageBlock{a, b} {
		for _, k := range list {
			switch {
			case k.srcOffset <= off && off+n <= k.srcOffset+k.size:
				return k, false
			case off < k.srcOffset+k.size && k.srcOffset < off+n:
				return nil, true
			}
		}
	}
	return nil, false
}

// blocksToAdd returns the blocks the plan carries on their own.
func (p *olympusExternalPlan) blocksToAdd() []*imageBlock {
	if p == nil {
		return nil
	}
	return p.newBlocks
}

// relocatedEnd returns the position in the output of the end of the last image
// block, where the tail begins: the header length plus the bytes copied for the
// blocks (each source byte once, see assignNewOffsets).
func relocatedEnd(headerLen uint64, blocks []*imageBlock) uint64 {
	if disjointAscending(blocks) {
		for _, b := range blocks {
			headerLen = saturatingAdd(headerLen, b.size)
		}
		return headerLen
	}
	for _, rg := range planBlocks(blocks).ranges {
		headerLen = saturatingAdd(headerLen, rg.end-rg.start)
	}
	return headerLen
}

// patch rewrites the offsets in finalTIFF, the classic TIFF stream the
// relocation encoded, once the blocks have their new offsets
// (assignNewOffsets). The MakerNote of finalTIFF is the one of the source,
// moved. outEnd is the position in the output where the tail begins
// (relocatedEnd).
func (p *olympusExternalPlan) patch(finalTIFF []byte, order binary.ByteOrder, outEnd uint64) {
	if p == nil || len(finalTIFF) < 8 {
		return
	}
	newMN, found := encodedMakerNoteAbs(finalTIFF, order)
	if !found {
		return
	}
	for _, x := range p.offs {
		if nv, ok := p.newValue(x, newMN, outEnd); ok {
			if field := newMN + (x.Field - x.MNStart); field >= 0 && field+4 <= len(finalTIFF) {
				x.Order.PutUint32(finalTIFF[field:], nv)
			}
		}
	}
}

// newValue returns the value that the field of x must hold in the output, whose
// MakerNote is at newMN and whose tail starts at outEnd.
func (p *olympusExternalPlan) newValue(x externalOffset, newMN int, outEnd uint64) (uint32, bool) {
	newT, ok := p.newTarget(x, newMN, outEnd)
	newBase := int64(0) // the TIFF header
	if x.Base != 0 {
		newBase = int64(newMN) + int64(x.Base-x.MNStart)
	}
	nv := newT - newBase
	return uint32(nv), ok && nv >= 0 && nv <= math.MaxUint32 //nolint:gosec // G115: range checked in the same expression
}

// newTarget returns the position in the output of the data of x, whose
// MakerNote is at newMN and whose tail starts at outEnd, or false when the new
// position of its block is not usable: assignNewOffsets saturates an offset to
// MaxUint32 when the output would pass 4 GiB.
func (p *olympusExternalPlan) newTarget(x externalOffset, newMN int, outEnd uint64) (int64, bool) {
	t := x.Target()
	switch x.kind {
	case extInMakerNote:
		return int64(newMN) + (t - int64(x.MNStart)), true
	case extInTail:
		return int64(outEnd) + (t - int64(p.tailStart)), outEnd < math.MaxUint32 //nolint:gosec // G115: bounded by maxFileSize
	case extInBlock:
		return int64(x.blk.newOffset) + (t - int64(x.blk.srcOffset)), x.blk.newOffset < math.MaxUint32 //nolint:gosec // G115: bounded by maxFileSize
	case extOwnBlock:
		return int64(x.blk.newOffset), x.blk.newOffset < math.MaxUint32 //nolint:gosec // G115: bounded by maxFileSize
	}
	return 0, false
}
