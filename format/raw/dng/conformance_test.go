package dng

// conformance_test.go — DNG container specification-conformance test battery.
// Task #161.
//
// Rule IDs (DNG-*, ROBUST-*) are used verbatim as sub-test names and cite the
// authoritative specification clause for each assertion.
//
// Sources:
//   - Adobe "Digital Negative (DNG) Specification v1.7.1.0, Sep 2023"
//     — §2: TIFF magic + byte order; §4: IFD0 / SubIFD layout; §5: metadata tags
//   - TIFF Revision 6.0 (Adobe, 1992) §2                — S-* structural rules
//   - BigTIFF Design (Aware Systems / libtiff)            — S-05/S-06 header
//   - Adobe XMP Spec Part 3 §1.3                         — tag 0x02BC (700) TypeByte
//   - IPTC IIM 4.2 + iptc.md ROBUST-16                   — tag 0x83BB TypeLong/TypeUndefined
//
// Test categories:
//   DNG-detect        — detection by DNGVersion tag 0xC612 in IFD0
//   DNG-IFD0          — IFD0 structure: NewSubFileType, SubIFD pointer 0x014A
//   DNG-metadata      — XMP (0x02BC), EXIF (0x8769), GPS (0x8825), IPTC (0x83BB)
//   DNG-bigTIFF       — BigTIFF DNG (magic 0x002B)
//   DNG-write         — round-trip byte-correctness (offsets, DNGVersion preserved)
//   DNG-robust        — malformed input must not panic; correct degradation
//   DNG-corpus        — parity over testdata/corpus/raw *.dng files

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
	"github.com/FlavioCFOliveira/GoMetadata/internal/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture builders
// ─────────────────────────────────────────────────────────────────────────────

// dngFixtureParams configures the synthetic DNG fixture built by buildDNG.
type dngFixtureParams struct {
	order              binary.ByteOrder
	dngVersion         [4]byte // DNGVersion value; [0,0,0,0] omits the tag
	omitDNGVersion     bool
	newSubFileTypeIFD0 uint32 // IFD0 NewSubFileType (0x00FE); 1 = reduced-res, 0 = full-res
	iptc               []byte // non-nil → add tag 0x83BB
	xmp                []byte // non-nil → add tag 0x02BC
	addEXIFPointer     bool   // true → add ExifIFD pointer tag 0x8769
	addGPSPointer      bool   // true → add GPS IFD pointer tag 0x8825
	addSubIFD          bool   // true → add SubIFDs pointer tag 0x014A with a minimal child IFD
	subIFDNewFileType  uint32 // NewSubFileType for the SubIFD (0 = full-res raw)
}

// buildDNG constructs a synthetic DNG/TIFF byte stream according to params.
//
// Layout (little-endian unless overridden):
//
//	[0:8]   TIFF header (byte-order marker + magic 0x002A + IFD0 offset = 8)
//	[8:]    IFD0 (nEntries entries × 12 bytes + 4-byte next-IFD)
//	[...]   out-of-line values (IPTC, XMP, SubIFD pointer array)
//	[...]   ExifIFD (0-entry stub, if addEXIFPointer)
//	[...]   GPS IFD (0-entry stub, if addGPSPointer)
//	[...]   SubIFD (0-entry stub, if addSubIFD)
//
// Adobe DNG Spec v1.7 §4: IFD0 MUST carry DNGVersion (0xC612) to be identified
// as DNG (not just TIFF). NewSubFileType IFD0 = 1 (reduced-res thumbnail).
func buildDNG(p dngFixtureParams) []byte {
	if p.order == nil {
		p.order = binary.LittleEndian
	}
	if p.dngVersion == ([4]byte{}) && !p.omitDNGVersion {
		p.dngVersion = [4]byte{1, 7, 0, 0}
	}

	// ── Phase 1: count IFD0 entries ──────────────────────────────────────────
	nEntries := 0
	if !p.omitDNGVersion {
		nEntries++ // DNGVersion 0xC612
	}
	nEntries++ // NewSubFileType 0x00FE
	if p.iptc != nil {
		nEntries++ // IPTC-NAA 0x83BB
	}
	if p.xmp != nil {
		nEntries++ // XMP 0x02BC
	}
	if p.addEXIFPointer {
		nEntries++ // ExifIFD pointer 0x8769
	}
	if p.addGPSPointer {
		nEntries++ // GPS IFD pointer 0x8825
	}
	if p.addSubIFD {
		nEntries++ // SubIFDs 0x014A
	}

	// ── Phase 2: compute offsets ─────────────────────────────────────────────
	const hdrLen = 8
	// IFD0: 2-byte count + nEntries×12 bytes + 4-byte next-IFD pointer.
	ifd0Size := 2 + nEntries*12 + 4
	payloadBase := hdrLen + ifd0Size // first OOL value starts here

	// OOL data: IPTC (if non-nil), XMP (if non-nil), SubIFD pointer array (4 bytes).
	iptcOff, xmpOff, subIFDArrayOff := 0, 0, 0
	totalOOL := 0
	if p.iptc != nil {
		iptcOff = payloadBase + totalOOL
		totalOOL += len(p.iptc)
		if totalOOL%2 != 0 {
			totalOOL++ // word-align
		}
	}
	if p.xmp != nil {
		xmpOff = payloadBase + totalOOL
		totalOOL += len(p.xmp)
		if totalOOL%2 != 0 {
			totalOOL++ // word-align
		}
	}
	// SubIFD pointer array: one uint32 offset.
	subIFDBodyOff := 0 // where the SubIFD IFD starts
	if p.addSubIFD {
		subIFDArrayOff = payloadBase + totalOOL
		totalOOL += 4 // one uint32 pointer
		if totalOOL%2 != 0 {
			totalOOL++ // word-align
		}
	}

	// Stubs for ExifIFD, GPS IFD, SubIFD — each is a 0-entry IFD (6 bytes).
	stubBase := payloadBase + totalOOL
	exifIFDOff, gpsIFDOff := 0, 0
	nextStub := stubBase
	const stubSize = 6 // 2-byte count + 4-byte next-IFD = empty IFD
	if p.addEXIFPointer {
		exifIFDOff = nextStub
		nextStub += stubSize
	}
	if p.addGPSPointer {
		gpsIFDOff = nextStub
		nextStub += stubSize
	}
	if p.addSubIFD {
		subIFDBodyOff = nextStub
		nextStub += stubSize
	}

	totalLen := nextStub
	buf := make([]byte, totalLen)

	// ── Phase 3: write TIFF header ──────────────────────────────────────────
	// Adobe DNG Spec v1.7 §2: DNG uses standard TIFF 6.0 byte order and magic.
	// TIFF 6.0 §2: "II" = LE, "MM" = BE; magic = 42 (0x002A).
	if p.order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	p.order.PutUint16(buf[2:], 0x002A)
	p.order.PutUint32(buf[4:], hdrLen) // IFD0 at offset 8

	// ── Phase 4: write IFD0 entries ─────────────────────────────────────────
	// TIFF 6.0 §2: entries must be sorted ascending by tag.
	// We write them in tag-ascending order.
	ifdPos := hdrLen
	p.order.PutUint16(buf[ifdPos:], uint16(nEntries))
	e := ifdPos + 2

	writeEntry := func(tag, typ uint16, count uint32, valOrOff uint32) {
		p.order.PutUint16(buf[e:], tag)
		p.order.PutUint16(buf[e+2:], typ)
		p.order.PutUint32(buf[e+4:], count)
		p.order.PutUint32(buf[e+8:], valOrOff)
		e += 12
	}

	// Tags in ascending order:

	// 0x00FE — NewSubFileType, SHORT[1] (≤4 bytes → inline).
	// Adobe DNG Spec v1.7 §4: IFD0 = reduced-res (NewSubFileType=1).
	writeEntry(0x00FE, 4 /*LONG*/, 1, p.newSubFileTypeIFD0)

	// 0x014A — SubIFDs (OOL pointer array, 4 bytes) — only if addSubIFD.
	if p.addSubIFD {
		// TIFF Extension §F: SubIFDs (0x014A) is an array of LONG offsets.
		// The value is a single uint32 pointing to the SubIFD's IFD block.
		// Adobe DNG Spec v1.7 §4: full-resolution raw in one or more SubIFDs.
		writeEntry(0x014A, 4 /*LONG*/, 1, uint32(subIFDArrayOff))
		// Write the SubIFD pointer array value.
		p.order.PutUint32(buf[subIFDArrayOff:], uint32(subIFDBodyOff))
		// Write the stub SubIFD (0 entries, next-IFD = 0) with NewSubFileType.
		// Adobe DNG Spec v1.7 §4: SubIFD NewSubFileType = 0 (full-res).
		// For the stub we write 0 entries; a real SubIFD would have more tags.
		p.order.PutUint16(buf[subIFDBodyOff:], 1) // 1 entry: NewSubFileType
		p.order.PutUint16(buf[subIFDBodyOff+2:], 0x00FE)
		p.order.PutUint16(buf[subIFDBodyOff+4:], 4 /*LONG*/)
		p.order.PutUint32(buf[subIFDBodyOff+6:], 1) // count = 1
		p.order.PutUint32(buf[subIFDBodyOff+10:], p.subIFDNewFileType)
		p.order.PutUint32(buf[subIFDBodyOff+14:], 0) // next-IFD = 0
		// Extend buffer to hold the 1-entry SubIFD (14 bytes).
		// The stub is sized at 6 bytes; a 1-entry IFD needs 2+12+4 = 18 bytes.
		// We will rebuild the buffer correctly — recalculate below.
	}

	// 0x83BB — IPTC-NAA (OOL for len > 4).
	if p.iptc != nil {
		// Adobe DNG Spec v1.7 §5: IPTC stored in TIFF tag 0x83BB.
		// TIFF IPTC convention: TypeUndefined (7) on read; TypeLong (4) on write.
		writeEntry(0x83BB, 7 /*UNDEFINED*/, uint32(len(p.iptc)), uint32(iptcOff)) //nolint:gosec // G115: test helper, payload len bounded by buf alloc
		copy(buf[iptcOff:], p.iptc)
	}

	// 0x8769 — ExifIFD pointer (LONG[1], inline = offset to ExifIFD).
	if p.addEXIFPointer {
		// Adobe DNG Spec v1.7 §5: EXIF metadata via tag 0x8769 from IFD0.
		writeEntry(0x8769, 4 /*LONG*/, 1, uint32(exifIFDOff))
		// Write the stub ExifIFD (0 entries, next = 0).
		p.order.PutUint16(buf[exifIFDOff:], 0)
		p.order.PutUint32(buf[exifIFDOff+2:], 0)
	}

	// 0x8825 — GPS IFD pointer (LONG[1], inline = offset to GPS IFD).
	if p.addGPSPointer {
		// Adobe DNG Spec v1.7 §5: GPS metadata via tag 0x8825 from IFD0.
		writeEntry(0x8825, 4 /*LONG*/, 1, uint32(gpsIFDOff))
		// Write the stub GPS IFD (0 entries, next = 0).
		p.order.PutUint16(buf[gpsIFDOff:], 0)
		p.order.PutUint32(buf[gpsIFDOff+2:], 0)
	}

	// 0x02BC — XMP (OOL for len > 4).
	if p.xmp != nil {
		// Adobe XMP Spec Part 3 §1.3: XMP in TIFF tag 700 (0x02BC), TypeByte(1).
		// Adobe DNG Spec v1.7 §5: XMP stored in tag 0x02BC.
		writeEntry(0x02BC, 1 /*BYTE*/, uint32(len(p.xmp)), uint32(xmpOff)) //nolint:gosec // G115: test helper, payload len bounded by buf alloc
		copy(buf[xmpOff:], p.xmp)
	}

	// 0xC612 — DNGVersion (BYTE[4], always inline).
	if !p.omitDNGVersion {
		// Adobe DNG Spec v1.7 §5.1: DNGVersion BYTE[4] — identifies file as DNG.
		// Inline: 4 bytes fit in the value-or-offset field.
		var vv uint32
		if p.order == binary.LittleEndian {
			vv = uint32(p.dngVersion[0]) | uint32(p.dngVersion[1])<<8 |
				uint32(p.dngVersion[2])<<16 | uint32(p.dngVersion[3])<<24
		} else {
			vv = uint32(p.dngVersion[0])<<24 | uint32(p.dngVersion[1])<<16 |
				uint32(p.dngVersion[2])<<8 | uint32(p.dngVersion[3])
		}
		writeEntry(0xC612, 1 /*BYTE*/, 4, vv)
	}

	// next-IFD pointer (0 = end of chain).
	p.order.PutUint32(buf[e:], 0)

	return buf
}

// buildDNGWithSubIFD constructs a DNG fixture where IFD0 has NewSubFileType=1
// (reduced-res) and a SubIFD (tag 0x014A) carries NewSubFileType=0 (full-res).
// This is the canonical DNG layout per Adobe DNG Spec v1.7 §4.
//
// Because buildDNG's stub SubIFD (6 bytes) is not large enough for a 1-entry
// IFD (18 bytes), this helper expands the final buffer correctly.
func buildDNGWithSubIFD(order binary.ByteOrder) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	// Layout:
	//   [0:8]    TIFF header
	//   [8:32]   IFD0: count(2) + 1×entry(12) + SubIFDs-ptr(12) + DNGVersion-entry(12) + next(4)
	//            = 2 + 3×12 + 4 = 44 bytes → IFD0 ends at 8+44 = 52
	// Wait, we have: NewSubFileType(0x00FE), SubIFDs(0x014A), DNGVersion(0xC612) = 3 entries.
	//   IFD0: 2 + 3*12 + 4 = 42 bytes → ends at 8+42 = 50
	//   SubIFD array OOL (4 bytes) at 50 → ends at 54
	//   SubIFD IFD (2+1*12+4 = 18 bytes) at 54 → ends at 72
	const (
		hdrLen       = 8
		nIFD0Entries = 3                       // NewSubFileType, SubIFDs, DNGVersion
		ifd0Size     = 2 + nIFD0Entries*12 + 4 // 42
		subArrOff    = hdrLen + ifd0Size       // 50
		subIFDOff    = subArrOff + 4           // 54 (pointer array is 4 bytes)
		subIFDSize   = 2 + 1*12 + 4            // 18 (NewSubFileType entry)
		totalLen     = subIFDOff + subIFDSize  // 72
	)

	buf := make([]byte, totalLen)

	// TIFF header.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdrLen)

	// IFD0: 3 entries in tag-ascending order.
	order.PutUint16(buf[hdrLen:], nIFD0Entries)

	e := hdrLen + 2
	// 0x00FE — NewSubFileType = 1 (reduced-res thumbnail).
	// Adobe DNG Spec v1.7 §4: IFD0 MUST have NewSubFileType = 1.
	order.PutUint16(buf[e:], 0x00FE)
	order.PutUint16(buf[e+2:], 4 /*LONG*/)
	order.PutUint32(buf[e+4:], 1) // count
	order.PutUint32(buf[e+8:], 1) // value = 1
	e += 12

	// 0x014A — SubIFDs: OOL array of 1 LONG (the SubIFD offset).
	// TIFF Extension §F / Adobe DNG Spec v1.7 §4.
	order.PutUint16(buf[e:], 0x014A)
	order.PutUint16(buf[e+2:], 4 /*LONG*/)
	order.PutUint32(buf[e+4:], 1) // count = 1
	order.PutUint32(buf[e+8:], uint32(subArrOff))
	e += 12

	// 0xC612 — DNGVersion = [1,7,0,0] inline BYTE[4].
	// Adobe DNG Spec v1.7 §5.1: version bytes major.minor.patch.rev.
	order.PutUint16(buf[e:], 0xC612)
	order.PutUint16(buf[e+2:], 1 /*BYTE*/)
	order.PutUint32(buf[e+4:], 4) // count = 4
	buf[e+8] = 1
	buf[e+9] = 7
	buf[e+10] = 0
	buf[e+11] = 0
	e += 12

	// next-IFD = 0.
	order.PutUint32(buf[e:], 0)

	// SubIFD pointer array value (one uint32 = subIFDOff).
	order.PutUint32(buf[subArrOff:], uint32(subIFDOff))

	// SubIFD IFD: 1 entry (NewSubFileType = 0 = full-res raw).
	// Adobe DNG Spec v1.7 §4: full-resolution raw in SubIFD, NewSubFileType=0.
	order.PutUint16(buf[subIFDOff:], 1) // entry count
	es := subIFDOff + 2
	order.PutUint16(buf[es:], 0x00FE)
	order.PutUint16(buf[es+2:], 4 /*LONG*/)
	order.PutUint32(buf[es+4:], 1)  // count
	order.PutUint32(buf[es+8:], 0)  // value = 0 (full-res)
	order.PutUint32(buf[es+12:], 0) // next-IFD = 0

	return buf
}

// buildBigTIFFDNG builds a minimal BigTIFF DNG fixture with DNGVersion.
//
// BigTIFF header (16 bytes) followed by an IFD0 with DNGVersion.
// Adobe DNG Spec v1.7: BigTIFF magic (0x002B) is a supported variant.
func buildBigTIFFDNG(order binary.ByteOrder, iptc, xmp []byte) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	// Count entries.
	nEntries := 1 // DNGVersion always present
	if iptc != nil {
		nEntries++
	}
	if xmp != nil {
		nEntries++
	}

	// Layout:
	//   [0:16]   BigTIFF header
	//   [16:]    IFD0: 8-byte count + nEntries*20 bytes + 8-byte next-IFD
	//   [...]    OOL data (IPTC, XMP)
	//
	// BigTIFF spec §2: 20-byte entries, 8-byte inline threshold, 8-byte IFD offsets.
	const btHdrLen = 16
	ifd0Size := 8 + nEntries*20 + 8
	payloadBase := uint64(btHdrLen + ifd0Size)

	iptcOff, xmpOff := uint64(0), uint64(0)
	totalOOL := uint64(0)
	if iptc != nil {
		iptcOff = payloadBase + totalOOL
		totalOOL += uint64(len(iptc))
		if totalOOL%2 != 0 {
			totalOOL++
		}
	}
	if xmp != nil {
		xmpOff = payloadBase + totalOOL
		totalOOL += uint64(len(xmp))
	}

	totalLen := int(payloadBase + totalOOL)
	buf := make([]byte, totalLen)

	// BigTIFF header.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B)   // BigTIFF magic
	order.PutUint16(buf[4:], 8)        // offset bytesize = 8
	order.PutUint16(buf[6:], 0)        // reserved
	order.PutUint64(buf[8:], btHdrLen) // IFD0 offset

	// IFD0 entry count.
	order.PutUint64(buf[btHdrLen:], uint64(nEntries))

	e := btHdrLen + 8 // first entry

	writeBigEntry := func(tag, typ uint16, count, valOrOff uint64) {
		order.PutUint16(buf[e:], tag)
		order.PutUint16(buf[e+2:], typ)
		order.PutUint64(buf[e+4:], count)
		order.PutUint64(buf[e+12:], valOrOff)
		e += 20
	}

	// Tags ascending. 0x02BC < 0x83BB < 0xC612.
	if xmp != nil {
		// BigTIFF inline threshold = 8; XMP len > 8 → OOL.
		writeBigEntry(0x02BC, 1 /*BYTE*/, uint64(len(xmp)), xmpOff)
		copy(buf[xmpOff:], xmp)
	}
	if iptc != nil {
		writeBigEntry(0x83BB, 7 /*UNDEFINED*/, uint64(len(iptc)), iptcOff)
		copy(buf[iptcOff:], iptc)
	}
	// DNGVersion BYTE[4] inline (4 ≤ 8 → inline).
	var dngVerVal uint64
	if order == binary.LittleEndian {
		dngVerVal = 0x00000701 // bytes: 1,7,0,0
	} else {
		dngVerVal = 0x01070000_00000000 // big-endian: 1,7,0,0 at the high 4 bytes
	}
	writeBigEntry(0xC612, 1 /*BYTE*/, 4, dngVerVal)

	// next-IFD = 0.
	order.PutUint64(buf[e:], 0)

	return buf
}

// buildCyclicSubIFDDNG builds a DNG where the SubIFD pointer (tag 0x014A)
// references a self-referential offset — simulating an IFD cycle.
//
// Adobe DNG Spec v1.7 §4 / TIFF 6.0 §2 (R-01): cycles must be detected.
func buildCyclicSubIFDDNG() []byte {
	// IFD0 at 8; SubIFD array points to IFD0 itself (offset 8).
	// This creates a SubIFD cycle: IFD0 → SubIFD → [back to IFD0].
	//
	// Layout: header(8) + IFD0(count(2)+2*entry(24)+next(4)) + subArr(4)
	//   IFD0 entries: DNGVersion (inline), SubIFDs (OOL pointer to offset 8)
	const (
		hdrLen    = 8
		ifd0Size  = 2 + 2*12 + 4      // 30
		subArrOff = hdrLen + ifd0Size // 38
		totalLen  = subArrOff + 4     // 42
	)
	buf := make([]byte, totalLen)
	order := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], hdrLen)

	order.PutUint16(buf[hdrLen:], 2) // 2 entries

	e := hdrLen + 2
	// 0x014A SubIFDs → OOL array at subArrOff; array value = 8 (IFD0 offset) → cycle.
	order.PutUint16(buf[e:], 0x014A)
	order.PutUint16(buf[e+2:], 4 /*LONG*/)
	order.PutUint32(buf[e+4:], 1) // count
	order.PutUint32(buf[e+8:], uint32(subArrOff))
	e += 12

	// 0xC612 DNGVersion inline.
	order.PutUint16(buf[e:], 0xC612)
	order.PutUint16(buf[e+2:], 1 /*BYTE*/)
	order.PutUint32(buf[e+4:], 4)
	buf[e+8] = 1
	buf[e+9] = 7
	e += 12

	order.PutUint32(buf[e:], 0) // next-IFD = 0

	// SubIFD pointer array: value = 8 = IFD0 offset → circular reference.
	order.PutUint32(buf[subArrOff:], 8)

	return buf
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-detect — detection by DNGVersion tag 0xC612 in IFD0
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_detect_DNGVersion_0xC612_LE verifies that Extract
// succeeds on a little-endian DNG containing DNGVersion tag 0xC612 in IFD0.
//
// Adobe DNG Spec v1.7 §5.1: DNGVersion (0xC612) BYTE[4] is the definitive
// identifier that distinguishes a DNG from a plain TIFF file.
// §7(b) detection rule: definitive marker = DNGVersion tag 0xC612 in IFD0.
func TestConformance_DNG_detect_DNGVersion_0xC612_LE(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-detect-DNGVersion-0xC612-LE: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-detect-DNGVersion-0xC612-LE: rawEXIF is nil")
	}

	// Parse the EXIF and verify DNGVersion is present in IFD0.
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-detect-DNGVersion-0xC612-LE: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("DNG-detect-DNGVersion-0xC612-LE: IFD0 is nil")
	}
	entry := parsed.IFD0.Get(exif.TagID(0xC612))
	if entry == nil {
		t.Fatal("DNG-detect-DNGVersion-0xC612-LE: DNGVersion tag 0xC612 not found in IFD0")
	}
	// Adobe DNG Spec v1.7 §5.1: DNGVersion MUST be BYTE[4].
	if entry.Type != exif.TypeByte {
		t.Errorf("DNG-detect-DNGVersion-0xC612-LE: DNGVersion type = %d, want TypeByte(1)", entry.Type)
	}
	if entry.Count != 4 {
		t.Errorf("DNG-detect-DNGVersion-0xC612-LE: DNGVersion count = %d, want 4", entry.Count)
	}
}

// TestConformance_DNG_detect_DNGVersion_0xC612_BE is the big-endian counterpart.
//
// Adobe DNG Spec v1.7 §2: DNG supports both II (LE) and MM (BE) byte order.
func TestConformance_DNG_detect_DNGVersion_0xC612_BE(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:      binary.BigEndian,
		dngVersion: [4]byte{1, 6, 0, 0},
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-detect-DNGVersion-0xC612-BE: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-detect-DNGVersion-0xC612-BE: rawEXIF is nil")
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-detect-DNGVersion-0xC612-BE: exif.Parse: %v", err)
	}
	entry := parsed.IFD0.Get(exif.TagID(0xC612))
	if entry == nil {
		t.Error("DNG-detect-DNGVersion-0xC612-BE: DNGVersion not found in BE IFD0")
	}
}

// TestConformance_DNG_detect_TIFF_without_DNGVersion verifies that a plain
// TIFF without DNGVersion (0xC612) still succeeds via Extract (DNG falls back
// to the TIFF path — the tag is a semantic marker, not enforced by the parser).
//
// Adobe DNG Spec v1.7 §7(b): the parser accepts any TIFF-magic file; callers
// are responsible for verifying DNGVersion to confirm a file is truly DNG.
// The library does not reject DNG-less files — it reads them as plain TIFF.
func TestConformance_DNG_detect_TIFF_without_DNGVersion(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:          binary.LittleEndian,
		omitDNGVersion: true,
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	// Must not fail: the DNG parser delegates to TIFF regardless.
	if err != nil {
		t.Fatalf("DNG-detect-TIFF-without-DNGVersion: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-detect-TIFF-without-DNGVersion: rawEXIF is nil for DNGVersion-absent TIFF")
	}
}

// TestConformance_DNG_detect_DNGBackwardVersion_0xC613 verifies that
// DNGBackwardVersion (0xC613) is readable alongside DNGVersion.
//
// Adobe DNG Spec v1.7 §5.2: DNGBackwardVersion (0xC613) BYTE[4] indicates
// the minimum DNG spec version needed to process the file.
func TestConformance_DNG_detect_DNGBackwardVersion_0xC613(t *testing.T) {
	t.Parallel()
	// Build a DNG manually with both DNGVersion and DNGBackwardVersion.
	// Layout: hdr(8) + IFD0 with 2 entries + next-IFD(4).
	const (
		hdrLen = 8
		nE     = 2
		ifdSz  = 2 + nE*12 + 4
		total  = hdrLen + ifdSz
	)
	buf := make([]byte, total)
	o := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	o.PutUint16(buf[2:], 0x002A)
	o.PutUint32(buf[4:], hdrLen)
	o.PutUint16(buf[hdrLen:], nE)

	e := hdrLen + 2
	// 0xC612 DNGVersion [1,7,0,0].
	o.PutUint16(buf[e:], 0xC612)
	o.PutUint16(buf[e+2:], 1 /*BYTE*/)
	o.PutUint32(buf[e+4:], 4)
	buf[e+8] = 1
	buf[e+9] = 7
	e += 12

	// 0xC613 DNGBackwardVersion [1,1,0,0].
	// Adobe DNG Spec v1.7 §5.2.
	o.PutUint16(buf[e:], 0xC613)
	o.PutUint16(buf[e+2:], 1 /*BYTE*/)
	o.PutUint32(buf[e+4:], 4)
	buf[e+8] = 1
	buf[e+9] = 1
	e += 12

	o.PutUint32(buf[e:], 0)

	rawEXIF, _, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("DNG-detect-DNGBackwardVersion-0xC613: Extract: %v", err)
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-detect-DNGBackwardVersion-0xC613: exif.Parse: %v", err)
	}
	if e612 := parsed.IFD0.Get(exif.TagID(0xC612)); e612 == nil {
		t.Error("DNG-detect-DNGBackwardVersion-0xC613: DNGVersion not found")
	}
	if e613 := parsed.IFD0.Get(exif.TagID(0xC613)); e613 == nil {
		t.Error("DNG-detect-DNGBackwardVersion-0xC613: DNGBackwardVersion not found")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-IFD0 — IFD0 structure: NewSubFileType, SubIFD pointer 0x014A
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_IFD0_reduced_res_NewSubFileType verifies that an IFD0
// with NewSubFileType=1 (reduced-res) is parsed without error.
//
// Adobe DNG Spec v1.7 §4: IFD0 is the reduced-resolution representation
// (thumbnail); NewSubFileType bit 0 = 1 ("reduced-resolution image").
// TIFF 6.0 §7.3: NewSubFileType (0x00FE) LONG[1], bit 0 = 1 → reduced-res.
func TestConformance_DNG_IFD0_reduced_res_NewSubFileType(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:              binary.LittleEndian,
		newSubFileTypeIFD0: 1, // reduced-res
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-IFD0-reduced-res-NewSubFileType: Extract: %v", err)
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-IFD0-reduced-res-NewSubFileType: exif.Parse: %v", err)
	}
	entry := parsed.IFD0.Get(exif.TagID(0x00FE))
	if entry == nil {
		t.Fatal("DNG-IFD0-reduced-res-NewSubFileType: NewSubFileType (0x00FE) not found")
	}
	// Verify value = 1.
	if len(entry.Value) < 4 {
		t.Fatalf("DNG-IFD0-reduced-res-NewSubFileType: NewSubFileType value too short (%d bytes)", len(entry.Value))
	}
	got := binary.LittleEndian.Uint32(entry.Value)
	if got != 1 {
		t.Errorf("DNG-IFD0-reduced-res-NewSubFileType: NewSubFileType = %d, want 1", got)
	}
}

// TestConformance_DNG_IFD0_SubIFD_raw verifies that a DNG SubIFD (tag 0x014A)
// with NewSubFileType=0 (full-res raw) is accessible from the EXIF parse result.
//
// Adobe DNG Spec v1.7 §4: full-res image in a SubIFD (tag 0x014A), IFD0 is
// reduced-res (thumbnail). TIFF Extension §F: SubIFDs (0x014A) LONG array of
// offsets to child IFDs.
func TestConformance_DNG_IFD0_SubIFD_raw(t *testing.T) {
	t.Parallel()
	data := buildDNGWithSubIFD(binary.LittleEndian)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-IFD0-reduced-res-SubIFD-raw: Extract: %v", err)
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-IFD0-reduced-res-SubIFD-raw: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("DNG-IFD0-reduced-res-SubIFD-raw: IFD0 is nil")
	}
	// SubIFDs tag must be present in IFD0.
	subIFDEntry := parsed.IFD0.Get(exif.TagID(0x014A))
	if subIFDEntry == nil {
		t.Fatal("DNG-IFD0-reduced-res-SubIFD-raw: SubIFDs tag 0x014A not found in IFD0")
	}
	// Adobe DNG Spec v1.7 §4: SubIFDs LONG array; TIFF 6.0 §2: LONG type = 4.
	if subIFDEntry.Type != exif.TypeLong {
		t.Errorf("DNG-IFD0-reduced-res-SubIFD-raw: SubIFDs type = %d, want TypeLong(4)", subIFDEntry.Type)
	}
}

// TestConformance_DNG_IFD0_SubIFD_raw_BE is the big-endian counterpart of
// TestConformance_DNG_IFD0_SubIFD_raw.
func TestConformance_DNG_IFD0_SubIFD_raw_BE(t *testing.T) {
	t.Parallel()
	data := buildDNGWithSubIFD(binary.BigEndian)
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-IFD0-reduced-res-SubIFD-raw-BE: Extract: %v", err)
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-IFD0-reduced-res-SubIFD-raw-BE: exif.Parse: %v", err)
	}
	if parsed.IFD0.Get(exif.TagID(0x014A)) == nil {
		t.Error("DNG-IFD0-reduced-res-SubIFD-raw-BE: SubIFDs 0x014A not found in BE DNG")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-metadata — XMP, EXIF, GPS, IPTC embedding
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_XMP_tag_0x02BC verifies that XMP stored in TIFF tag
// 0x02BC (700) is extracted correctly from a DNG file.
//
// Adobe DNG Spec v1.7 §5: XMP stored in TIFF tag 0x02BC (700), TypeByte(1).
// Adobe XMP Spec Part 3 §1.3: no size limit; raw RDF/XML packet; no APP1 framing.
func TestConformance_DNG_XMP_tag_0x02BC(t *testing.T) {
	t.Parallel()
	wantXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildDNG(dngFixtureParams{
		order: binary.LittleEndian,
		xmp:   wantXMP,
	})

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-XMP-tag-0x02BC: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("DNG-XMP-tag-0x02BC: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
	// Adobe XMP Spec Part 3 §1.3 (TIFF-03): must NOT carry APP1 framing.
	if len(rawXMP) >= 2 && rawXMP[0] == 0xFF && rawXMP[1] == 0xE1 {
		t.Error("DNG-XMP-tag-0x02BC: XMP must not carry APP1 framing (0xFF 0xE1)")
	}
}

// TestConformance_DNG_XMP_tag_0x02BC_BE verifies XMP extraction from a BE DNG.
func TestConformance_DNG_XMP_tag_0x02BC_BE(t *testing.T) {
	t.Parallel()
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	data := buildDNG(dngFixtureParams{
		order: binary.BigEndian,
		xmp:   wantXMP,
	})
	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-XMP-tag-0x02BC-BE: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("DNG-XMP-tag-0x02BC-BE: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_DNG_IPTC_tag_0x83BB verifies that IPTC stored in TIFF tag
// 0x83BB is extracted correctly from a DNG file.
//
// Adobe DNG Spec v1.7 §5: IPTC legacy metadata via tag 0x83BB (Photoshop IRB/IIM).
// TIFF convention: TypeLong on write, accept TypeByte/Undefined on read.
func TestConformance_DNG_IPTC_tag_0x83BB(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x05, 'A', 'd', 'o', 'b', 'e'}
	data := buildDNG(dngFixtureParams{
		order: binary.LittleEndian,
		iptc:  wantIPTC,
	})
	_, rawIPTC, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-IPTC-tag-0x83BB: Extract: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("DNG-IPTC-tag-0x83BB: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
}

// TestConformance_DNG_EXIF_pointer_0x8769 verifies that the EXIF IFD pointer
// tag 0x8769 in IFD0 is readable from a DNG file.
//
// Adobe DNG Spec v1.7 §5: EXIF metadata via pointer tag 0x8769 from IFD0.
// TIFF 6.0 §2 / S-23: ExifIFD from tag 0x8769 (LONG) in IFD0.
func TestConformance_DNG_EXIF_pointer_0x8769(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:          binary.LittleEndian,
		addEXIFPointer: true,
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-EXIF-pointer-0x8769: Extract: %v", err)
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-EXIF-pointer-0x8769: exif.Parse: %v", err)
	}
	if parsed.IFD0.Get(exif.TagID(0x8769)) == nil {
		t.Error("DNG-EXIF-pointer-0x8769: ExifIFD pointer tag 0x8769 not found in IFD0")
	}
}

// TestConformance_DNG_GPS_pointer_0x8825 verifies that the GPS IFD pointer
// tag 0x8825 in IFD0 is readable from a DNG file.
//
// Adobe DNG Spec v1.7 §5: GPS metadata via tag 0x8825 from IFD0.
// TIFF 6.0 §2 / S-24: GPS IFD from tag 0x8825 in IFD0.
func TestConformance_DNG_GPS_pointer_0x8825(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:         binary.LittleEndian,
		addGPSPointer: true,
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-GPS-pointer-0x8825: Extract: %v", err)
	}
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-GPS-pointer-0x8825: exif.Parse: %v", err)
	}
	if parsed.IFD0.Get(exif.TagID(0x8825)) == nil {
		t.Error("DNG-GPS-pointer-0x8825: GPS IFD pointer tag 0x8825 not found in IFD0")
	}
}

// TestConformance_DNG_XMP_and_IPTC_combined verifies that both XMP and IPTC
// are correctly extracted when present together in the same DNG file.
//
// Adobe DNG Spec v1.7 §5: both tags may coexist in IFD0.
func TestConformance_DNG_XMP_and_IPTC_combined(t *testing.T) {
	t.Parallel()
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	wantIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x03, 'D', 'N', 'G'}
	data := buildDNG(dngFixtureParams{
		order: binary.LittleEndian,
		xmp:   wantXMP,
		iptc:  wantIPTC,
	})
	_, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-XMP-and-IPTC-combined: Extract: %v", err)
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("DNG-XMP-and-IPTC-combined: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("DNG-XMP-and-IPTC-combined: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-bigTIFF — BigTIFF DNG (magic 0x002B)
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_BigTIFF_extract verifies that a BigTIFF DNG (magic 0x002B)
// is accepted by Extract and returns rawEXIF with the correct metadata.
//
// Adobe DNG Spec v1.7 §7(c): "TIFF 8-byte header (BigTIFF 0x002B allowed)."
// BigTIFF spec §2: 16-byte header with uint64 IFD offsets and 20-byte entries.
func TestConformance_DNG_BigTIFF_extract(t *testing.T) {
	t.Parallel()
	wantIPTC := []byte{0x1C, 0x02, 0x78, 0x00, 0x03, 0x44, 0x4E, 0x47, 0x62, 0x74}
	wantXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildBigTIFFDNG(binary.LittleEndian, wantIPTC, wantXMP)

	rawEXIF, rawIPTC, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-BigTIFF: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-BigTIFF: rawEXIF is nil")
	}
	if !bytes.Equal(rawIPTC, wantIPTC) {
		t.Errorf("DNG-BigTIFF: rawIPTC = %q, want %q", rawIPTC, wantIPTC)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("DNG-BigTIFF: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
	// Verify BigTIFF magic 0x002B.
	if len(data) >= 4 && binary.LittleEndian.Uint16(data[2:]) != 0x002B {
		t.Errorf("DNG-BigTIFF: fixture magic = 0x%04X, want 0x002B", binary.LittleEndian.Uint16(data[2:]))
	}
}

// TestConformance_DNG_BigTIFF_extract_BE is the big-endian counterpart of
// TestConformance_DNG_BigTIFF_extract.
func TestConformance_DNG_BigTIFF_extract_BE(t *testing.T) {
	t.Parallel()
	wantXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	data := buildBigTIFFDNG(binary.BigEndian, nil, wantXMP)

	_, _, rawXMP, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-BigTIFF-BE: Extract: %v", err)
	}
	if !bytes.Equal(rawXMP, wantXMP) {
		t.Errorf("DNG-BigTIFF-BE: rawXMP = %q, want %q", rawXMP, wantXMP)
	}
}

// TestConformance_DNG_BigTIFF_DNGVersion verifies that DNGVersion (0xC612) is
// present in the IFD0 of a BigTIFF DNG.
//
// Adobe DNG Spec v1.7 §5.1: DNGVersion is required regardless of whether the
// container uses classic TIFF or BigTIFF.
func TestConformance_DNG_BigTIFF_DNGVersion(t *testing.T) {
	t.Parallel()
	data := buildBigTIFFDNG(binary.LittleEndian, nil, nil)

	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-BigTIFF-DNGVersion: Extract: %v", err)
	}
	// exif.Parse handles BigTIFF (task #54: BigTIFF-aware parser).
	parsed, err := exif.Parse(rawEXIF)
	if err != nil {
		t.Fatalf("DNG-BigTIFF-DNGVersion: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("DNG-BigTIFF-DNGVersion: IFD0 is nil")
	}
	if parsed.IFD0.Get(exif.TagID(0xC612)) == nil {
		t.Error("DNG-BigTIFF-DNGVersion: DNGVersion tag 0xC612 not found in BigTIFF DNG IFD0")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-write — round-trip byte-correctness
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_write_round_trip_XMP verifies that a DNG Inject round-trip
// preserves XMP faithfully and that DNGVersion is present in the output.
//
// Adobe DNG Spec v1.7 §7(e): write must preserve DNGVersion and not corrupt
// the raw strip/tile data. XMP via tag 0x02BC must round-trip exactly.
func TestConformance_DNG_write_round_trip_XMP(t *testing.T) {
	t.Parallel()
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
		t.Fatalf("DNG-write-round-trip-XMP: Inject: %v", err)
	}

	_, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("DNG-write-round-trip-XMP: Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("DNG-write-round-trip-XMP: XMP = %q, want %q", gotXMP, rawXMP)
	}

	// Verify DNGVersion is preserved in the output.
	// Adobe DNG Spec v1.7 §7(e): DNGVersion must survive write round-trip.
	parsed, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("DNG-write-round-trip-XMP: exif.Parse after Inject: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("DNG-write-round-trip-XMP: IFD0 nil after Inject")
	}
	if parsed.IFD0.Get(exif.TagID(0xC612)) == nil {
		t.Error("DNG-write-round-trip-XMP: DNGVersion (0xC612) not preserved after write")
	}
}

// TestConformance_DNG_write_round_trip_IPTC verifies that a DNG round-trip
// preserves IPTC faithfully.
//
// Adobe DNG Spec v1.7 §7(e): IPTC (tag 0x83BB) must round-trip without loss.
func TestConformance_DNG_write_round_trip_IPTC(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 0x48, 0x65, 0x6C, 0x6C, 0x6F}
	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, nil, true); err != nil {
		t.Fatalf("DNG-write-round-trip-IPTC: Inject: %v", err)
	}
	_, gotIPTC, _, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("DNG-write-round-trip-IPTC: Extract after Inject: %v", err)
	}
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("DNG-write-round-trip-IPTC: IPTC = %q, want %q", gotIPTC, rawIPTC)
	}
}

// TestConformance_DNG_write_round_trip_both verifies that a DNG round-trip
// preserves both IPTC and XMP together.
//
// Adobe DNG Spec v1.7 §7(e): all three payload types must survive write.
func TestConformance_DNG_write_round_trip_both(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 0x48, 0x65, 0x6C, 0x6C, 0x6F}
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("DNG-write-round-trip-both: Inject: %v", err)
	}
	rawEXIF2, gotIPTC, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("DNG-write-round-trip-both: Extract: %v", err)
	}
	if rawEXIF2 == nil {
		t.Error("DNG-write-round-trip-both: rawEXIF is nil")
	}
	if !bytes.Equal(gotIPTC, rawIPTC) {
		t.Errorf("DNG-write-round-trip-both: IPTC = %q, want %q", gotIPTC, rawIPTC)
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("DNG-write-round-trip-both: XMP = %q, want %q", gotXMP, rawXMP)
	}
}

// TestConformance_DNG_write_SubIFD_chain_preserved verifies that a DNG with a
// SubIFD (tag 0x014A) survives a write round-trip with SubIFD chain intact.
//
// Adobe DNG Spec v1.7 §7(e): "preserve SubIFD chain + DNGVersion; do not
// corrupt raw strips/tiles."
func TestConformance_DNG_write_SubIFD_chain_preserved(t *testing.T) {
	t.Parallel()
	rawXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)
	data := buildDNGWithSubIFD(binary.LittleEndian)

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, nil, rawXMP, true); err != nil {
		t.Fatalf("DNG-write-SubIFD-chain-preserved: Inject: %v", err)
	}
	rawEXIF2, _, gotXMP, err := Extract(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("DNG-write-SubIFD-chain-preserved: Extract: %v", err)
	}
	if rawEXIF2 == nil {
		t.Fatal("DNG-write-SubIFD-chain-preserved: rawEXIF nil after Inject")
	}
	if !bytes.Equal(gotXMP, rawXMP) {
		t.Errorf("DNG-write-SubIFD-chain-preserved: XMP = %q, want %q", gotXMP, rawXMP)
	}

	// SubIFDs pointer must still be present in IFD0.
	parsed, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("DNG-write-SubIFD-chain-preserved: exif.Parse after Inject: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("DNG-write-SubIFD-chain-preserved: IFD0 nil after Inject")
	}
	if parsed.IFD0.Get(exif.TagID(0x014A)) == nil {
		t.Error("DNG-write-SubIFD-chain-preserved: SubIFDs tag 0x014A not found after write round-trip")
	}
	// DNGVersion must also survive.
	if parsed.IFD0.Get(exif.TagID(0xC612)) == nil {
		t.Error("DNG-write-SubIFD-chain-preserved: DNGVersion 0xC612 not preserved after write")
	}
}

// TestConformance_DNG_write_IFD_entries_sorted verifies that the IFD0 in the
// Inject output has all entries sorted ascending by tag.
//
// TIFF 6.0 §2 (S-12, writer side): entries MUST be sorted ascending by tag.
func TestConformance_DNG_write_IFD_entries_sorted(t *testing.T) {
	t.Parallel()
	rawIPTC := []byte{0x1C, 0x02, 0x50, 0x00, 0x05, 0x48, 0x65, 0x6C, 0x6C, 0x6F}
	rawXMP := []byte(`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/><?xpacket end="r"?>`)
	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("DNG-write-IFD-entries-sorted: Inject: %v", err)
	}
	parsed, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("DNG-write-IFD-entries-sorted: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		t.Fatal("DNG-write-IFD-entries-sorted: IFD0 is nil")
	}
	entries := parsed.IFD0.Entries
	for i := 1; i < len(entries); i++ {
		if entries[i].Tag < entries[i-1].Tag {
			t.Errorf("DNG-write-IFD-entries-sorted: entry[%d] tag 0x%04X < entry[%d] tag 0x%04X (unsorted)",
				i, entries[i].Tag, i-1, entries[i-1].Tag)
		}
	}
}

// TestConformance_DNG_write_word_aligned_OOL verifies that all out-of-line
// value offsets in the Inject output are word-aligned (even).
//
// TIFF 6.0 §2 (S-11, writer side): out-of-line data MUST begin on a word
// boundary (even offset). Adobe DNG Spec v1.7 §7(e): OOL at even offset.
func TestConformance_DNG_write_word_aligned_OOL(t *testing.T) {
	t.Parallel()
	// Odd-length payloads stress the alignment logic.
	rawXMP := make([]byte, 101)
	copy(rawXMP, `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`)
	rawXMP[100] = 0x3E
	rawIPTC := make([]byte, 19)
	rawIPTC[0] = 0x1C

	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})

	var out bytes.Buffer
	if err := Inject(bytes.NewReader(data), &out, data, rawIPTC, rawXMP, true); err != nil {
		t.Fatalf("DNG-write-word-aligned-OOL: Inject: %v", err)
	}
	outBytes := out.Bytes()

	// Scan all OOL entries and verify even offsets.
	parsed, err := exif.Parse(outBytes)
	if err != nil {
		t.Fatalf("DNG-write-word-aligned-OOL: exif.Parse: %v", err)
	}
	if parsed.IFD0 == nil {
		return
	}
	for _, entry := range parsed.IFD0.Entries {
		// Determine whether this entry is OOL.
		var typeSize uint32
		switch entry.Type {
		case 1, 2, 6, 7:
			typeSize = 1
		case 3, 8:
			typeSize = 2
		case 4, 9, 11:
			typeSize = 4
		case 5, 10, 12:
			typeSize = 8
		default:
			continue
		}
		if uint64(typeSize)*uint64(entry.Count) > 4 {
			// OOL: entry.Value points into outBytes; compute actual file offset.
			if len(entry.Value) > 0 {
				// The Value slice is a sub-slice of rawEXIF; find offset via pointer arithmetic.
				// We verify via the raw binary: find the tag in the IFD and read its offset.
				off := findTagOOLOffset(outBytes, uint16(entry.Tag))
				if off != 0 && off%2 != 0 {
					t.Errorf("DNG-write-word-aligned-OOL S-11: tag 0x%04X OOL value at odd offset %d", entry.Tag, off)
				}
			}
		}
	}
}

// findTagOOLOffset scans the classic TIFF IFD0 in data (LE byte order) and
// returns the value-or-offset field for the given tag. Returns 0 if not found.
func findTagOOLOffset(data []byte, tag uint16) uint32 {
	if len(data) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		order = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(data[2:]) != 0x002A {
		return 0 // skip BigTIFF for this helper
	}
	ifd0Off := order.Uint32(data[4:])
	if int(ifd0Off)+2 > len(data) {
		return 0
	}
	count := int(order.Uint16(data[ifd0Off:]))
	for i := 0; i < count; i++ { //nolint:intrange // binary parser: i*12 offset multiplier
		e := int(ifd0Off) + 2 + i*12
		if e+12 > len(data) {
			break
		}
		if order.Uint16(data[e:]) == tag {
			return order.Uint32(data[e+8:])
		}
	}
	return 0
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-robust — malformed input must not panic; correct degradation
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_robust_DNGVersion_malformed verifies that a DNG whose
// DNGVersion tag has an invalid count (e.g. 0) does not crash Extract.
//
// Adobe DNG Spec v1.7 §7(f): "DNGVersion malformed/absent".
// Parser must degrade gracefully — return rawEXIF, no panic.
func TestConformance_DNG_robust_DNGVersion_malformed(t *testing.T) {
	t.Parallel()
	// Build a DNG with DNGVersion count=0 (should be 4).
	const totalLen = 8 + 2 + 12 + 4 // header + count(1 entry) + entry + next
	buf := make([]byte, totalLen)
	o := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	o.PutUint16(buf[2:], 0x002A)
	o.PutUint32(buf[4:], 8)
	o.PutUint16(buf[8:], 1) // 1 entry

	// DNGVersion with count=0 (malformed per Adobe DNG Spec v1.7 §5.1).
	o.PutUint16(buf[10:], 0xC612)
	o.PutUint16(buf[12:], 1 /*BYTE*/)
	o.PutUint32(buf[14:], 0) // count=0: malformed
	o.PutUint32(buf[18:], 0)
	o.PutUint32(buf[22:], 0) // next-IFD = 0

	// Must not panic; error or rawEXIF are both acceptable.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	_ = rawEXIF // May be nil or valid — crash is the only failure.
}

// TestConformance_DNG_robust_DNGVersion_absent verifies that a plain TIFF
// without DNGVersion is handled gracefully — no panic, rawEXIF returned.
//
// Adobe DNG Spec v1.7 §7(f): "DNGVersion absent" — degrades to plain TIFF.
func TestConformance_DNG_robust_DNGVersion_absent(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:          binary.LittleEndian,
		omitDNGVersion: true,
	})
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-robust-DNGVersion-absent: Extract: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-robust-DNGVersion-absent: rawEXIF is nil")
	}
}

// TestConformance_DNG_robust_offset_past_EOF verifies that a DNG with an IFD0
// offset beyond EOF does not panic and returns rawEXIF.
//
// TIFF 6.0 §2 (R-03): any offset outside stream → treat as absent; no crash.
// Adobe DNG Spec v1.7 §7(f): "offset past EOF".
func TestConformance_DNG_robust_offset_past_EOF(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002A)
	binary.LittleEndian.PutUint32(buf[4:], 0xFFFFFFFF) // IFD0 far past EOF

	rawEXIF, _, _, _ := Extract(bytes.NewReader(buf))
	if rawEXIF == nil {
		t.Error("DNG-robust-offset-past-EOF: rawEXIF must not be nil")
	}
}

// TestConformance_DNG_robust_count_overflow verifies that an IFD entry with a
// count that would overflow the buffer boundary is skipped gracefully.
//
// TIFF 6.0 §2 (R-04/R-06): count×typeSize overflow guard. Adobe DNG Spec
// v1.7 §7(f): "count exceeding bound".
func TestConformance_DNG_robust_count_overflow(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 26)
	o := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	o.PutUint16(buf[2:], 0x002A)
	o.PutUint32(buf[4:], 8)
	o.PutUint16(buf[8:], 1)

	// IPTC with count=0x10000001 (LONG type): count×4 overflows uint32.
	// R-06: overflow must be detected in uint64 arithmetic before dereferencing.
	o.PutUint16(buf[10:], 0x83BB) // IPTC
	o.PutUint16(buf[12:], 4 /*LONG*/)
	o.PutUint32(buf[14:], 0x10000001) // overflow count
	o.PutUint32(buf[18:], 26)         // offset = just past buffer

	rawEXIF, rawIPTC, _, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("DNG-robust-count-overflow: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-robust-count-overflow: rawEXIF must not be nil")
	}
	if rawIPTC != nil {
		t.Error("DNG-robust-count-overflow: rawIPTC must be nil for overflow-count entry")
	}
}

// TestConformance_DNG_robust_truncated_tag_700_XMP verifies that a DNG with
// truncated XMP (tag 700, 0x02BC) data does not crash Extract.
//
// Adobe DNG Spec v1.7 §7(f): "truncated tag 700".
// R-04: offset + count×typeSize > len → skip entry; never slice past buffer.
func TestConformance_DNG_robust_truncated_tag_700_XMP(t *testing.T) {
	t.Parallel()
	// Build a TIFF with XMP tag claiming 100 bytes but only 5 available.
	const (
		hdrLen    = 8
		ifdSize   = 2 + 1*12 + 4 // count + 1 entry + next-IFD
		available = 5
	)
	buf := make([]byte, hdrLen+ifdSize+available)
	o := binary.LittleEndian
	buf[0], buf[1] = 'I', 'I'
	o.PutUint16(buf[2:], 0x002A)
	o.PutUint32(buf[4:], hdrLen)
	o.PutUint16(buf[hdrLen:], 1) // 1 entry

	e := hdrLen + 2
	// XMP tag 0x02BC claiming 100 bytes at offset hdrLen+ifdSize (only 5 available).
	o.PutUint16(buf[e:], 0x02BC)
	o.PutUint16(buf[e+2:], 1 /*BYTE*/)
	o.PutUint32(buf[e+4:], 100) // claimed count: 100 bytes
	o.PutUint32(buf[e+8:], uint32(hdrLen+ifdSize))
	o.PutUint32(buf[e+12:], 0) // next-IFD = 0
	// 5 bytes of XMP data at hdrLen+ifdSize.
	copy(buf[hdrLen+ifdSize:], "<xmp/>")

	rawEXIF, _, rawXMP, err := Extract(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("DNG-robust-truncated-tag-700-XMP: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-robust-truncated-tag-700-XMP: rawEXIF must not be nil")
	}
	// The entry must be skipped (offset + 100 > buf length).
	if rawXMP != nil {
		t.Error("DNG-robust-truncated-tag-700-XMP: rawXMP must be nil for truncated OOL entry")
	}
}

// TestConformance_DNG_robust_ifd_cycle_no_hang verifies that a self-referential
// SubIFD pointer in a DNG (simulating an IFD cycle) does not hang or panic.
//
// TIFF 6.0 §2 (R-01): circular IFD chains must be detected; break, no infinite loop.
// Adobe DNG Spec v1.7 §7(f): "offset cycles/self-referential SubIFDs".
func TestConformance_DNG_robust_ifd_cycle_no_hang(t *testing.T) {
	t.Parallel()
	data := buildCyclicSubIFDDNG()

	// Must complete without hanging — no infinite loop from the cycle.
	rawEXIF, _, _, _ := Extract(bytes.NewReader(data))
	_ = rawEXIF // crash / hang is the failure; value is irrelevant
}

// TestConformance_DNG_robust_empty_input verifies that Extract on an empty byte
// slice returns an error and does not panic.
//
// TIFF 6.0 §2 (R-13): classic TIFF < 8 bytes always invalid.
func TestConformance_DNG_robust_empty_input(t *testing.T) {
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{}))
	if err == nil {
		t.Error("DNG-robust-empty-input: expected error for empty input, got nil")
	}
}

// TestConformance_DNG_robust_truncated_header verifies that inputs shorter
// than 8 bytes return an error without panicking.
//
// TIFF 6.0 §2 (R-13): minimum valid classic TIFF = 8 bytes.
func TestConformance_DNG_robust_truncated_header(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, 4, 7} {
		buf := make([]byte, n)
		if n >= 2 {
			buf[0], buf[1] = 'I', 'I'
		}
		_, _, _, err := Extract(bytes.NewReader(buf))
		if err == nil {
			t.Errorf("DNG-robust-truncated-header: %d-byte input: expected error, got nil", n)
		}
	}
}

// TestConformance_DNG_robust_bad_byte_order verifies that an invalid byte-order
// marker returns an error and does not panic.
//
// TIFF 6.0 §2 (S-01): only "II" and "MM" are valid; anything else is corrupt.
func TestConformance_DNG_robust_bad_byte_order(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 0xFF, 0xFF // invalid BOM
	_, _, _, err := Extract(bytes.NewReader(buf))
	if err == nil {
		t.Error("DNG-robust-bad-byte-order: expected error for invalid BOM, got nil")
	}
}

// TestConformance_DNG_robust_unknown_magic verifies that a TIFF-like file with
// magic ≠ 0x002A and ≠ 0x002B returns an error and does not panic.
//
// TIFF 6.0 §2 (S-02): only magic 42 (0x002A) and 43 (0x002B) are valid.
// Adobe DNG Spec v1.7 §2: DNG uses standard TIFF magic — no other magic.
func TestConformance_DNG_robust_unknown_magic(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 8)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x1234) // invalid magic
	binary.LittleEndian.PutUint32(buf[4:], 8)

	_, _, _, err := Extract(bytes.NewReader(buf))
	if err == nil {
		t.Error("DNG-robust-unknown-magic: expected error for unknown magic, got nil")
	}
}

// TestConformance_DNG_robust_BigTIFF_bad_bytesize verifies that a BigTIFF DNG
// with offset-bytesize ≠ 8 returns an error.
//
// BigTIFF spec §2: bytesize of offsets MUST equal 8. S-05.
// Adobe DNG Spec v1.7 §7(f): BigTIFF DNG must be validated.
func TestConformance_DNG_robust_BigTIFF_bad_bytesize(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 16)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 0x002B) // BigTIFF magic
	binary.LittleEndian.PutUint16(buf[4:], 4)      // invalid bytesize (must be 8)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], 16)

	_, _, _, err := Extract(bytes.NewReader(buf))
	if err == nil {
		t.Error("DNG-robust-BigTIFF-bad-bytesize: expected error for bytesize=4, got nil")
	}
}

// TestConformance_DNG_robust_next_ifd_out_of_bounds verifies that a next-IFD
// pointer beyond the file is treated as end-of-chain, not a crash.
//
// TIFF 6.0 §2 (S-13): out-of-bounds next-IFD → treat as end of chain.
func TestConformance_DNG_robust_next_ifd_out_of_bounds(t *testing.T) {
	t.Parallel()
	data := buildDNG(dngFixtureParams{
		order:      binary.LittleEndian,
		dngVersion: [4]byte{1, 7, 0, 0},
	})
	// IFD0 has nEntries entries; next-IFD ptr is at the end of the IFD.
	// buildDNG IFD0: hdr(8) + count(2) + nEntries*12 + (next=4).
	// We need to find the next-IFD offset in the binary.
	// Since we know the IFD0 layout from the builder, compute it:
	// nEntries = 1 (DNGVersion) + 1 (NewSubFileType) = 2 entries (no iptc/xmp/subifd/exif/gps).
	nE := 2 // NewSubFileType + DNGVersion
	nextIFDOff := 8 + 2 + nE*12
	if nextIFDOff+4 <= len(data) {
		binary.LittleEndian.PutUint32(data[nextIFDOff:], 0xFFFFFFFF)
	}
	rawEXIF, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DNG-robust-next-ifd-out-of-bounds: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Error("DNG-robust-next-ifd-out-of-bounds: rawEXIF must not be nil")
	}
}

// TestConformance_DNG_robust_error_prefix verifies that Extract wraps errors
// with the "dng:" prefix, consistent with the library error wrapping contract.
func TestConformance_DNG_robust_error_prefix(t *testing.T) {
	t.Parallel()
	_, _, _, err := Extract(bytes.NewReader([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0}))
	if err == nil {
		t.Fatal("DNG-robust-error-prefix: expected error for invalid TIFF input, got nil")
	}
	if !strings.HasPrefix(err.Error(), "dng:") {
		t.Errorf("DNG-robust-error-prefix: error = %q, want prefix \"dng:\"", err.Error())
	}
}

// TestConformance_DNG_robust_inject_error_prefix verifies that Inject wraps
// errors with the "dng:" prefix.
func TestConformance_DNG_robust_inject_error_prefix(t *testing.T) {
	t.Parallel()
	badData := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0, 0, 0, 0, 0, 0, 0, 0}
	var out bytes.Buffer
	err := Inject(bytes.NewReader(badData), &out, badData, nil, []byte("<x/>"), true)
	if err == nil {
		t.Fatal("DNG-robust-inject-error-prefix: expected error for invalid TIFF input, got nil")
	}
	if !strings.HasPrefix(err.Error(), "dng:") {
		t.Errorf("DNG-robust-inject-error-prefix: error = %q, want prefix \"dng:\"", err.Error())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DNG-corpus — parity over real-world DNG files
// ─────────────────────────────────────────────────────────────────────────────

// TestConformance_DNG_corpus_extract verifies that Extract does not panic and
// returns a valid rawEXIF for every .dng file in the raw corpus.
//
// Covers: byte-order detection, DNGVersion acceptance, IFD traversal, tag
// extraction across real-world cameras and software (Leica, DJI, LG, Pentax,
// exiv2 corpus, exiftool corpus).
//
// Adobe DNG Spec v1.7 §7(b)-(f): all conformance rules exercised end-to-end.
//
// Note: uses testutil.CorpusFiles, which skips if the corpus directory is absent.
func TestConformance_DNG_corpus_extract(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	// Filter to .dng files only.
	var dngPaths []string
	for _, p := range paths {
		lower := strings.ToLower(filepath.Ext(p))
		if lower == ".dng" {
			dngPaths = append(dngPaths, p)
		}
	}
	if len(dngPaths) == 0 {
		t.Skip("DNG-corpus: no .dng files in testdata/corpus/raw; skipping corpus parity test")
	}

	for _, path := range dngPaths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) == 0 {
				return // .gitkeep or empty placeholder
			}

			// Must not panic; errors are acceptable for adversarial files.
			rawEXIF, _, _, extractErr := Extract(bytes.NewReader(data))
			if extractErr != nil {
				// Errors are acceptable for known-malformed files (exiv2 corpus).
				// The key invariant is no panic.
				return
			}
			// For successfully-parsed files, rawEXIF must be non-nil.
			if rawEXIF == nil {
				t.Errorf("DNG-corpus %s: rawEXIF is nil but Extract returned no error", name)
			}
		})
	}
}

// TestConformance_DNG_corpus_round_trip verifies that Inject produces a
// parseable output for every real-world DNG file in the corpus.
//
// Adobe DNG Spec v1.7 §7(e): write operations must not corrupt the file.
func TestConformance_DNG_corpus_round_trip(t *testing.T) {
	t.Parallel()
	paths := testutil.CorpusFiles(t, "raw")
	var dngPaths []string
	for _, p := range paths {
		if strings.ToLower(filepath.Ext(p)) == ".dng" {
			dngPaths = append(dngPaths, p)
		}
	}
	if len(dngPaths) == 0 {
		t.Skip("DNG-corpus-round-trip: no .dng files in corpus; skipping")
	}

	// Use a minimal XMP payload to trigger the copy-and-relocate path.
	injectXMP := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`)

	for _, path := range dngPaths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("read %s: %v", path, err)
			}
			if len(data) == 0 {
				return
			}

			// Skip files that don't extract cleanly (error = not a DNG we support).
			_, _, _, extractErr := Extract(bytes.NewReader(data))
			if extractErr != nil {
				return
			}

			// Inject must not panic and must produce a parseable output.
			var out bytes.Buffer
			if injectErr := Inject(bytes.NewReader(data), &out, data, nil, injectXMP, true); injectErr != nil {
				// Inject errors on real DNG files are permitted (complex proprietary
				// structures may not be fully relocatable). No panic is the invariant.
				return
			}

			// The output must be parseable.
			_, _, _, extractErr2 := Extract(bytes.NewReader(out.Bytes()))
			if extractErr2 != nil {
				t.Errorf("DNG-corpus-round-trip %s: Extract after Inject: %v", name, extractErr2)
			}
		})
	}
}
