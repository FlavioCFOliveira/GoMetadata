package testutil

import (
	"encoding/binary"
)

// MNEntry is one entry of a synthetic MakerNote IFD (see MakerNoteTIFF).
type MNEntry struct {
	Tag   uint16
	Type  uint16 // TIFF field type code
	Count uint32
	Value []byte // the value bytes, in the byte order of the stream
	// Outside places an out-of-line value after the end of the MakerNote in
	// the TIFF stream, as the Olympus C-720UZ does, instead of inside it.
	Outside bool
	// PtrOutside, when not nil, makes the entry an inline LONG that holds the
	// pointer to these bytes, placed after the end of the MakerNote: the
	// preview image of a Nikon PreviewIFD (tag 0x0201).
	PtrOutside []byte
	// Sub, when not nil, makes the entry a sub-IFD pointer (type LONG, count
	// 1): the IFD is placed inside the MakerNote, or crossing its end when
	// SubCrossesEnd, and the entry holds its offset. Value is ignored.
	Sub *MNSub
}

// MNSub is a sub-IFD of a synthetic MakerNote: the Nikon PreviewIFD.
type MNSub struct {
	Entries []MNEntry
	// CrossesEnd declares the MakerNote 11 bytes shorter than it is, so the
	// sub-IFD, which is its last structure, runs past the declared end (the
	// Nikon Coolpix P7000 does it).
	CrossesEnd bool
}

// MNSpec describes a synthetic classic TIFF stream: IFD0 with Make, an Exif IFD
// with a MakerNote, and the MakerNote blob.
type MNSpec struct {
	Make  string
	Order binary.ByteOrder
	// Prefix is the MakerNote bytes before its IFD: the signature of the
	// layout. The IFD starts at len(Prefix).
	Prefix []byte
	// TIFFRelative makes the value pointers count from the TIFF header; when
	// false they count from the MakerNote byte Base.
	TIFFRelative bool
	Base         int
	Entries      []MNEntry
	// Footer appends Canon's 8-byte TIFF footer ("II*\0" and the TIFF offset of
	// the MakerNote).
	Footer bool
	// FooterAdd is added to the offset the footer holds, to model a note that
	// was moved without updating its pointers (ExifTool: "Adjusted MakerNotes
	// base by"); the pointers are shifted by the same amount.
	FooterAdd int
	// PadBefore inserts bytes between the Exif IFD and the MakerNote, so the
	// MakerNote starts at a position that differs from a minimal layout.
	PadBefore int
}

// MNLayout is where the structures of a stream built by MakerNoteTIFF are.
type MNLayout struct {
	MNOffset int // offset of the MakerNote in the TIFF stream
	MNLen    int // declared length of the MakerNote
	// Values[i] is the offset in the TIFF stream of the value of Entries[i], or
	// -1 for an inline value; sub-IFD entries give the offset of the IFD.
	Values []int
}

// MakerNoteTIFF builds the TIFF stream of s and reports where its parts are.
// Layout: header, IFD0 (Make, Exif IFD pointer), Make value, Exif IFD
// (MakerNote), MakerNote blob (prefix, IFD, inside values, sub-IFDs, footer),
// then the values that lie outside the MakerNote. Offsets are even.
func MakerNoteTIFF(s MNSpec) ([]byte, MNLayout) { //nolint:cyclop,gocyclo,funlen // one linear builder; splitting it would hide the layout
	o := s.Order
	mk := append([]byte(s.Make), 0)
	if len(mk)%2 == 1 {
		mk = append(mk, 0)
	}
	const ifd0Off = 8
	ifd0Len := 2 + 2*12 + 4
	mkOff := ifd0Off + ifd0Len
	exifOff := mkOff + len(mk)
	exifLen := 2 + 12 + 4
	mnOff := exifOff + exifLen + s.PadBefore
	if mnOff%2 == 1 {
		mnOff++
	}

	// MakerNote blob: prefix, IFD, values.
	ifdAt := len(s.Prefix)
	n := len(s.Entries)
	valAt := ifdAt + 2 + 12*n + 4
	blob := make([]byte, valAt)
	copy(blob, s.Prefix)
	o.PutUint16(blob[ifdAt:], uint16(n)) //nolint:gosec // G115: test sizes are small

	var outside [][]byte // values placed after the blob
	type fix struct{ field, outsideIdx int }
	var fixes []fix
	var topOutside [][2]int // [entry, outside index] of the entries of the top IFD
	lay := MNLayout{MNOffset: mnOff, Values: make([]int, n)}
	var subs []int                    // entries with a sub-IFD
	ptr := func(blobOff int) uint32 { // value of a pointer to the blob offset blobOff
		if s.TIFFRelative {
			return uint32(mnOff + blobOff + s.FooterAdd) //nolint:gosec // G115: test sizes are small
		}
		return uint32(blobOff - s.Base) //nolint:gosec // G115: test sizes are small
	}
	for i, e := range s.Entries {
		ep := ifdAt + 2 + 12*i
		o.PutUint16(blob[ep:], e.Tag)
		o.PutUint16(blob[ep+2:], e.Type)
		o.PutUint32(blob[ep+4:], e.Count)
		lay.Values[i] = -1
		switch {
		case e.Sub != nil:
			o.PutUint16(blob[ep+2:], 4)
			o.PutUint32(blob[ep+4:], 1)
			subs = append(subs, i)
		case e.PtrOutside != nil:
			fixes = append(fixes, fix{ep + 8, len(outside)})
			topOutside = append(topOutside, [2]int{i, len(outside)})
			outside = append(outside, e.PtrOutside)
		case len(e.Value) <= 4:
			copy(blob[ep+8:ep+12], e.Value)
		case e.Outside:
			fixes = append(fixes, fix{ep + 8, len(outside)})
			topOutside = append(topOutside, [2]int{i, len(outside)})
			outside = append(outside, e.Value)
		default:
			if len(blob)%2 == 1 {
				blob = append(blob, 0) //nolint:makezero // the zeroed head of the builder is intended
			}
			o.PutUint32(blob[ep+8:], ptr(len(blob)))
			lay.Values[i] = mnOff + len(blob)
			blob = append(blob, e.Value...) //nolint:makezero // see above
		}
	}
	// Sub-IFDs: placed after the values, inside the blob.
	for _, i := range subs {
		e := s.Entries[i]
		if len(blob)%2 == 1 {
			blob = append(blob, 0) //nolint:makezero // the zeroed head of the builder is intended
		}
		subAt := len(blob)
		m := len(e.Sub.Entries)
		sub := make([]byte, 2+12*m+4)
		o.PutUint16(sub, uint16(m)) //nolint:gosec // G115: test sizes are small
		for j, se := range e.Sub.Entries {
			sp := 2 + 12*j
			o.PutUint16(sub[sp:], se.Tag)
			o.PutUint16(sub[sp+2:], se.Type)
			o.PutUint32(sub[sp+4:], se.Count)
			switch {
			case se.PtrOutside != nil:
				fixes = append(fixes, fix{subAt + sp + 8, len(outside)})
				outside = append(outside, se.PtrOutside)
			case len(se.Value) <= 4:
				copy(sub[sp+8:sp+12], se.Value)
			case se.Outside:
				fixes = append(fixes, fix{subAt + sp + 8, len(outside)})
				outside = append(outside, se.Value)
			default:
				// A value of the sub-IFD stored after it, inside the blob.
				pos := subAt + len(sub)
				if pos%2 == 1 {
					sub = append(sub, 0) //nolint:makezero // the zeroed head of the builder is intended
					pos++
				}
				o.PutUint32(sub[sp+8:], ptr(pos))
				sub = append(sub, se.Value...) //nolint:makezero // see above
			}
		}
		o.PutUint32(blob[ifdAt+2+12*i+8:], ptr(subAt))
		lay.Values[i] = mnOff + subAt
		blob = append(blob, sub...) //nolint:makezero // see above
	}
	declared := len(blob)
	if s.Footer {
		if len(blob)%2 == 1 {
			blob = append(blob, 0) //nolint:makezero // the zeroed head of the builder is intended
		}
		f := make([]byte, 8)
		if o == binary.LittleEndian {
			copy(f, "II*\x00")
		} else {
			copy(f, "MM\x00*")
		}
		o.PutUint32(f[4:], uint32(mnOff+s.FooterAdd)) //nolint:gosec // G115: test sizes are small
		blob = append(blob, f...)                     //nolint:makezero // see above
		declared = len(blob)
	}
	for _, e := range s.Entries {
		if e.Sub != nil && e.Sub.CrossesEnd {
			declared = len(blob) - 11
		}
	}
	lay.MNLen = declared

	// Values outside the MakerNote follow the blob.
	end := mnOff + len(blob)
	if end%2 == 1 {
		end++
	}
	outPos := make([]int, len(outside))
	total := end
	for i, v := range outside {
		outPos[i] = total
		total += len(v)
		if total%2 == 1 {
			total++
		}
	}
	for _, f := range fixes {
		blobOff := outPos[f.outsideIdx] - mnOff
		o.PutUint32(blob[f.field:], ptr(blobOff))
	}
	for _, t := range topOutside {
		lay.Values[t[0]] = outPos[t[1]]
	}

	out := make([]byte, total)
	if o == binary.LittleEndian {
		copy(out, "II*\x00")
	} else {
		copy(out, "MM\x00*")
	}
	o.PutUint32(out[4:], ifd0Off)
	// IFD0: Make (0x010F), ExifIFD pointer (0x8769).
	o.PutUint16(out[ifd0Off:], 2)
	e0 := ifd0Off + 2
	o.PutUint16(out[e0:], 0x010F)
	o.PutUint16(out[e0+2:], 2)
	o.PutUint32(out[e0+4:], uint32(len(s.Make)+1)) //nolint:gosec // G115: test sizes are small
	if len(s.Make)+1 <= 4 {
		copy(out[e0+8:], mk)
	} else {
		o.PutUint32(out[e0+8:], uint32(mkOff))
	}
	o.PutUint16(out[e0+12:], 0x8769)
	o.PutUint16(out[e0+14:], 4)
	o.PutUint32(out[e0+16:], 1)
	o.PutUint32(out[e0+20:], uint32(exifOff)) //nolint:gosec // G115: test sizes are small
	copy(out[mkOff:], mk)
	// Exif IFD: MakerNote (0x927C).
	o.PutUint16(out[exifOff:], 1)
	o.PutUint16(out[exifOff+2:], 0x927C)
	o.PutUint16(out[exifOff+4:], 7)
	o.PutUint32(out[exifOff+6:], uint32(declared)) //nolint:gosec // G115: test sizes are small
	o.PutUint32(out[exifOff+10:], uint32(mnOff))   //nolint:gosec // G115: test sizes are small
	copy(out[mnOff:], blob)
	for i, v := range outside {
		copy(out[outPos[i]:], v)
	}
	return out, lay
}

// DecodeMN reads the entries of the MakerNote IFD that starts ifdAt bytes into
// the note at offset mnAbs of the TIFF stream out, without the parser of the
// library: the offsets count from the TIFF header (tiff) or from the note byte
// base. It returns the value of every entry by tag; an entry whose pointer
// lies outside the stream is left out.
func DecodeMN(out []byte, mnAbs, ifdAt int, order binary.ByteOrder, tiff bool, base int) map[uint16][]byte {
	if mnAbs < 0 || mnAbs+ifdAt+2 > len(out) {
		return nil
	}
	n := int(order.Uint16(out[mnAbs+ifdAt:]))
	res := make(map[uint16][]byte, n)
	for i := range n {
		ep := mnAbs + ifdAt + 2 + 12*i
		if ep+12 > len(out) {
			break
		}
		tag := order.Uint16(out[ep:])
		sz := map[uint16]int{1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 7: 1}[order.Uint16(out[ep+2:])]
		total := sz * int(order.Uint32(out[ep+4:]))
		if total <= 4 {
			res[tag] = append([]byte(nil), out[ep+8:ep+8+total]...)
			continue
		}
		p := int(order.Uint32(out[ep+8:]))
		if !tiff {
			p += mnAbs + base
		}
		if p < 0 || p+total > len(out) {
			continue
		}
		res[tag] = append([]byte(nil), out[p:p+total]...)
	}
	return res
}
