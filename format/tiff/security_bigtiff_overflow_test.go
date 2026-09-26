package tiff

// security_bigtiff_overflow_test.go — regression guard for a CRITICAL
// security-audit finding (2026-09-26): several bounds checks across the
// copy-and-relocate write path computed `end := off + size` as a raw,
// unchecked uint64 addition, then compared `end > fileLen`. A BigTIFF file
// declaring StripOffsets/StripByteCounts as LONG8 (8-byte) fields can set
// off=MaxUint64-1, size=10 — off+size wraps to 8, which incorrectly passes
// as "in bounds" against any fileLen. The resulting out-of-range imageBlock
// then reaches writeRelocated's wholeFile==true branch and panics on
// `prefix[blk.srcOffset:end]` (srcOffset itself already exceeds cap(prefix)).
//
// Bisected to 0ebf5d4 ("feat(write): enable BigTIFF container writes via
// public API") — pre-existing since standalone BigTIFF write support first
// shipped, not introduced by Sprint 44 Batch G. Fixed by routing every such
// comparison through fits (internal/boundscheck.Fits), which computes the
// same bounds check without the overflow-prone raw addition.
//
// This test proves BOTH of writeRelocated's branches — wholeFile==true
// (slicing prefix directly) and wholeFile==false (streaming from r) — now
// return ErrBlockOutOfBounds instead of panicking, for the exact PoC
// offset/size pair the audit found.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// buildBigTIFFStripOverflowPoC constructs a minimal, otherwise well-formed
// BigTIFF file (BigTIFF spec §2: magic 0x002B, 16-byte header, 8-byte IFD0
// offset, uint64 entry count, 20-byte entries) whose IFD0 declares
// StripOffsets (0x0111) = math.MaxUint64-1 and StripByteCounts (0x0117) = 10,
// both LONG8 (type 16, 8 bytes/element), Count=1 — exactly the BigTIFF
// inline threshold (8 bytes), so both values are stored inline in their own
// 20-byte entry record with no further out-of-line data required.
func buildBigTIFFStripOverflowPoC(order binary.ByteOrder) []byte {
	const (
		hdrSize     = 16
		countSize   = 8
		entrySize   = 20
		nextPtrSize = 8
		nEntries    = 2
	)
	bufLen := hdrSize + countSize + nEntries*entrySize + nextPtrSize
	buf := make([]byte, bufLen)

	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
	order.PutUint16(buf[4:], 8)      // offset bytesize = 8
	order.PutUint16(buf[6:], 0)      // constant = 0
	order.PutUint64(buf[8:], 16)     // IFD0 offset

	pos := 16
	order.PutUint64(buf[pos:], nEntries)
	pos += 8

	// Entry 0: StripOffsets (0x0111), LONG8, count=1, value=MaxUint64-1 (inline).
	order.PutUint16(buf[pos:], 0x0111)
	order.PutUint16(buf[pos+2:], 16) // LONG8
	order.PutUint64(buf[pos+4:], 1)  // count = 1
	order.PutUint64(buf[pos+12:], math.MaxUint64-1)
	pos += entrySize

	// Entry 1: StripByteCounts (0x0117), LONG8, count=1, value=10 (inline).
	order.PutUint16(buf[pos:], 0x0117)
	order.PutUint16(buf[pos+2:], 16) // LONG8
	order.PutUint64(buf[pos+4:], 1)  // count = 1
	order.PutUint64(buf[pos+12:], 10)
	// next-IFD pointer = 0 (already zero-initialised)

	return buf
}

// TestSecurityBigTIFFStripOverflowNoPanic is the permanent regression test
// for the audit finding described in this file's own doc comment. It must
// never be skipped: both sub-tests exercise the exact PoC bytes, asserting
// Write returns ErrBlockOutOfBounds and — critically — does not panic.
func TestSecurityBigTIFFStripOverflowNoPanic(t *testing.T) {
	t.Parallel()
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		raw := buildBigTIFFStripOverflowPoC(order)

		for _, wholeFile := range []bool{true, false} {
			t.Run(map[bool]string{true: "wholeFile", false: "streaming"}[wholeFile], func(t *testing.T) {
				t.Parallel()
				// exif.EXIF is not safe for concurrent mutation (#245,
				// XMPCONC-01) — relocateTIFFFromParsed mutates e.IFD0.Entries
				// in place (upsertIFD0Entry), so each parallel sub-test must
				// parse its OWN independent *exif.EXIF rather than share one
				// across goroutines.
				e, err := exif.Parse(raw)
				if err != nil {
					t.Fatalf("exif.Parse(PoC): %v", err)
				}
				if !e.BigTIFF {
					t.Fatal("exif.Parse did not recognise the PoC as BigTIFF")
				}
				if e.IFD0 == nil || e.IFD0.Get(exif.TagStripOffsets) == nil || e.IFD0.Get(exif.TagStripByteCounts) == nil {
					t.Fatal("exif.Parse did not populate StripOffsets/StripByteCounts on IFD0")
				}

				var out bytes.Buffer
				// r backs both branches: wholeFile==true never touches it
				// (may be nil per InjectWithEXIF's own contract, but a real
				// reader is supplied here so the same PoC bytes exercise
				// both paths uniformly); wholeFile==false streams blocks
				// from it after the same fits() check rejects the PoC pair.
				r := bytes.NewReader(raw)
				err = InjectWithEXIF(r, raw, wholeFile, e, nil, []byte("<x/>"), &out)
				if err == nil {
					t.Fatal("InjectWithEXIF succeeded on a malicious StripOffsets/StripByteCounts pair — the overflow guard regressed")
				}
				if !errors.Is(err, ErrBlockOutOfBounds) {
					t.Fatalf("InjectWithEXIF: got error %v, want ErrBlockOutOfBounds", err)
				}
			})
		}
	}
}
