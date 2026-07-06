package tiff

// relocate_imageblock_budget_test.go — regression gate for GM-W1 (rmp task
// #261; CWE-770 uncontrolled resource consumption / CWE-405 asymmetric
// resource consumption).
//
// Confirmed finding: a crafted sub-256 MiB TIFF/RAW file drives
// multi-gigabyte allocation and seconds of CPU through the write path, with
// no metadata modification required, via two unbounded attacker-Count-driven
// allocation sinks in relocate.go:
//
//   - extractParallelOffsetBlocks: one heap-allocated *imageBlock per element
//     of a StripOffsets/StripByteCounts or TileOffsets/TileByteCounts array,
//     with the element count (n) bounded only by "does the declared array
//     fit in the source buffer" — up to tens of millions for a file still
//     well under maxFileSize (256 MiB).
//   - enumerateSubIFDsAt: a map[uint32]bool visited-set pre-allocated with
//     the attacker-controlled 0x014A (SubIFDs) Count as its size hint, plus
//     one *subIFDInfo per accepted pointer.
//
// The fix (relocate.go): maxImageBlocksPerOffsetEntry and maxSubIFDsPerEntry
// reject any single entry that is implausibly large before any per-element
// work is done, and a shared imageBlockBudget (maxAggregateImageBlocks)
// additionally bounds the SUM across every entry, IFD1-chain link, and
// SubIFD-recursion level visited within one relocate call.
//
// Test coverage in this file:
//   - TestWrite_RejectsHugeStripCount        — per-entry cap, strips/tiles.
//   - TestWrite_RejectsHugeSubIFDCount        — per-entry cap, SubIFDs.
//   - TestExtractParallelOffsetBlocksRejectsHugeCountFast — internal
//     function proves rejection happens before the value array is even
//     consulted (Count alone is enough to reject).
//   - TestEnumerateSubIFDsAtRejectsHugeCountFast — same, for SubIFDs.
//   - TestWrite_RejectsAggregateImageBlockBudget — cross-IFD amplification:
//     several entries, each individually within the per-entry cap, chained
//     together to exceed the aggregate budget.
//   - TestWrite_LegitimateMultiStripStillRoundTrips  — positive control.
//   - TestWrite_LegitimateManySubIFDsStillRoundTrips — positive control.
//
// Spec references: TIFF 6.0 §7 (strips), §15 (tiles); TIFF Extension §F /
// Adobe DNG Spec §4 (SubIFDs).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/exif"
)

// ---------------------------------------------------------------------------
// Fixture builders
// ---------------------------------------------------------------------------

// buildTIFFWithHugeStripArrayCount builds a minimal LE classic TIFF whose
// IFD0 declares only a StripOffsets/StripByteCounts pair (TypeShort, 2
// bytes/element) with Count=n. The value arrays are left zero-filled by
// make(); GM-W1's per-entry cap must reject the entry from the Count field
// alone, before any element is ever read, so the array contents are
// irrelevant to what this fixture is testing.
//
// TypeShort (2 bytes/element, TIFF 6.0 §7 permits either SHORT or LONG for
// StripOffsets/StripByteCounts) keeps the fixture at n*4 bytes total instead
// of n*8, so even n=10,000,000 (matching the confirmed PoC scale) stays at a
// manageable ~38 MiB rather than ~76 MiB.
func buildTIFFWithHugeStripArrayCount(n uint32) []byte {
	order := binary.LittleEndian
	const (
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		typeShort          = uint16(3)
		nEntries           = 2
	)
	ifdBodyEnd := uint32(8 + 2 + nEntries*12 + 4) // header + count + 2 entries + next-IFD
	offArrayOff := ifdBodyEnd
	arraySize := n * 2 // TypeShort: 2 bytes/element
	cntArrayOff := offArrayOff + arraySize
	total := int(cntArrayOff) + int(arraySize)

	buf := make([]byte, total) // zero-filled: array contents are never read by the fix
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], uint16(nEntries))

	p := 10
	writeEntry := func(tag, typ uint16, count, valOrOff uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typ)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], valOrOff)
		p += 12
	}
	writeEntry(tagStripOffsets, typeShort, n, offArrayOff)
	writeEntry(tagStripByteCounts, typeShort, n, cntArrayOff)
	// next-IFD = 0 (already zero)
	return buf
}

// buildTIFFWithHugeSubIFDCount builds a minimal LE classic TIFF whose IFD0
// declares only a 0x014A (SubIFDs, TypeLong) entry with Count=n. The value
// array is left zero-filled (every declared SubIFD offset is 0, which the
// off==0 loop guard would skip anyway) — GM-W1's per-entry cap must reject
// the entry from the Count field alone, before the visited-set map is even
// sized.
func buildTIFFWithHugeSubIFDCount(n uint32) []byte {
	order := binary.LittleEndian
	const (
		tagSubIFDs = uint16(0x014A)
		typeLong   = uint16(4)
		nEntries   = 1
	)
	arrayOff := uint32(8 + 2 + nEntries*12 + 4)
	arraySize := n * 4 // TypeLong: 4 bytes/element
	total := int(arrayOff) + int(arraySize)

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], uint16(nEntries))

	order.PutUint16(buf[10:], tagSubIFDs)
	order.PutUint16(buf[12:], typeLong)
	order.PutUint32(buf[14:], n)
	order.PutUint32(buf[18:], arrayOff)
	// next-IFD = 0 (already zero)
	return buf
}

// buildMultiStripTIFFN builds a LE classic TIFF with numStrips strips, each
// holding exactly one distinct byte (strip i == byte(i)). Used to prove that
// maxImageBlocksPerOffsetEntry does not reject legitimate large multi-strip
// files: numStrips is chosen well within the cap but far beyond any strip
// count a real camera, scanner, or software encoder produces.
func buildMultiStripTIFFN(numStrips int) []byte {
	order := binary.LittleEndian
	const (
		tagImageWidth      = uint16(0x0100)
		tagImageLength     = uint16(0x0101)
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		typeLong           = uint16(4)
		nEntries           = 4
	)
	const ifdBodyEnd = 8 + 2 + nEntries*12 + 4
	offArrayOff := uint32(ifdBodyEnd)
	cntArrayOff := offArrayOff + uint32(numStrips*4) //nolint:gosec // G115: test helper
	stripsOff := cntArrayOff + uint32(numStrips*4)   //nolint:gosec // G115: test helper
	total := int(stripsOff) + numStrips

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)
	order.PutUint16(buf[8:], uint16(nEntries))

	p := 10
	// All four IFD0 entries in this fixture are TypeLong, so typ is fixed
	// rather than parameterised (unparam).
	writeEntry := func(tag uint16, count, valOrOff uint32) {
		order.PutUint16(buf[p:], tag)
		order.PutUint16(buf[p+2:], typeLong)
		order.PutUint32(buf[p+4:], count)
		order.PutUint32(buf[p+8:], valOrOff)
		p += 12
	}
	writeEntry(tagImageWidth, 1, 1)
	writeEntry(tagImageLength, 1, uint32(numStrips))               //nolint:gosec // G115: test helper
	writeEntry(tagStripOffsets, uint32(numStrips), offArrayOff)    //nolint:gosec // G115: test helper
	writeEntry(tagStripByteCounts, uint32(numStrips), cntArrayOff) //nolint:gosec // G115: test helper
	// next-IFD = 0

	for i := range numStrips {
		order.PutUint32(buf[int(offArrayOff)+i*4:], uint32(int(stripsOff)+i)) //nolint:gosec // G115: test helper
		order.PutUint32(buf[int(cntArrayOff)+i*4:], 1)
		buf[int(stripsOff)+i] = byte(i)
	}
	return buf
}

// buildChainedStripTIFFs builds a LE classic TIFF whose IFD0 -> IFD1 -> ...
// next-IFD chain holds numIFDs IFDs, each declaring a StripOffsets /
// StripByteCounts pair (TypeShort) with Count=perIFDCount and zero-filled
// value arrays. Used to prove the AGGREGATE budget (maxAggregateImageBlocks)
// rejects a file where every individual entry stays within
// maxImageBlocksPerOffsetEntry but the combined total across the chain does
// not.
func buildChainedStripTIFFs(perIFDCount uint32, numIFDs int) []byte {
	order := binary.LittleEndian
	const (
		tagStripOffsets    = uint16(0x0111)
		tagStripByteCounts = uint16(0x0117)
		typeShort          = uint16(3)
		nEntries           = 2
		ifdFixedSize       = 2 + nEntries*12 + 4 // count + 2 entries + next-IFD
	)
	arraySize := perIFDCount * 2 // TypeShort: 2 bytes/element
	perIFDValueSize := arraySize * 2
	perIFDTotalSize := uint32(ifdFixedSize) + perIFDValueSize

	total := 8 + int(perIFDTotalSize)*numIFDs
	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	ifdOff := uint32(8)
	for i := range numIFDs {
		offArrayOff := ifdOff + uint32(ifdFixedSize)
		cntArrayOff := offArrayOff + arraySize
		nextIFDOff := ifdOff + perIFDTotalSize

		p := int(ifdOff)
		order.PutUint16(buf[p:], uint16(nEntries))
		p += 2
		writeEntry := func(tag, typ uint16, count, valOrOff uint32) {
			order.PutUint16(buf[p:], tag)
			order.PutUint16(buf[p+2:], typ)
			order.PutUint32(buf[p+4:], count)
			order.PutUint32(buf[p+8:], valOrOff)
			p += 12
		}
		writeEntry(tagStripOffsets, typeShort, perIFDCount, offArrayOff)
		writeEntry(tagStripByteCounts, typeShort, perIFDCount, cntArrayOff)

		if i < numIFDs-1 {
			order.PutUint32(buf[p:], nextIFDOff) // chain to the next IFD
		} else {
			order.PutUint32(buf[p:], 0) // end of chain
		}

		ifdOff = nextIFDOff
	}
	return buf
}

// buildTIFFWithNSubIFDs builds a LE classic TIFF whose IFD0 carries a single
// 0x014A (SubIFDs) entry with Count=numSubIFDs, each pointing at a distinct
// minimal, valid child IFD (one inline ImageWidth entry). Used to prove that
// maxSubIFDsPerEntry does not reject legitimate files with more SubIFDs than
// any real DNG carries.
func buildTIFFWithNSubIFDs(numSubIFDs int) []byte {
	order := binary.LittleEndian
	const (
		tagSubIFDs    = uint16(0x014A)
		tagImageWidth = uint16(0x0100)
		typeLong      = uint16(4)
		childIFDSize  = 2 + 1*12 + 4 // count + 1 entry + next-IFD = 18
	)
	arrayOff := uint32(8 + 2 + 1*12 + 4)        // IFD0: count + 1 entry (0x014A) + next-IFD
	arrayEnd := arrayOff + uint32(numSubIFDs*4) //nolint:gosec // G115: test helper
	total := int(arrayEnd) + numSubIFDs*childIFDSize

	buf := make([]byte, total)
	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8)

	order.PutUint16(buf[8:], 1) // IFD0: 1 entry
	order.PutUint16(buf[10:], tagSubIFDs)
	order.PutUint16(buf[12:], typeLong)
	order.PutUint32(buf[14:], uint32(numSubIFDs)) //nolint:gosec // G115: test helper
	order.PutUint32(buf[18:], arrayOff)
	order.PutUint32(buf[22:], 0) // next-IFD = 0

	for i := range numSubIFDs {
		childOff := int(arrayEnd) + i*childIFDSize
		order.PutUint32(buf[int(arrayOff)+i*4:], uint32(childOff)) //nolint:gosec // G115: test helper

		order.PutUint16(buf[childOff:], 1) // child IFD: 1 entry
		order.PutUint16(buf[childOff+2:], tagImageWidth)
		order.PutUint16(buf[childOff+4:], typeLong)
		order.PutUint32(buf[childOff+6:], 1)
		order.PutUint32(buf[childOff+10:], uint32(i+1)) // arbitrary distinct inline value
		order.PutUint32(buf[childOff+14:], 0)           // next-IFD = 0
	}
	return buf
}

// ---------------------------------------------------------------------------
// End-to-end tests: per-entry caps, via the public Inject entry point
// ---------------------------------------------------------------------------

// allocBudgetForRejection is the ceiling on TotalAlloc bytes permitted while
// Inject rejects a crafted huge-Count file. Before the GM-W1 fix, n=10,000,000
// drove ~2.1 GiB of heap allocation (strip sink) or ~263 MiB (SubIFD sink).
// 64 MiB is two orders of magnitude below that measured regression while
// still comfortably covering this test's own bookkeeping (IFD parsing,
// error formatting) — proving the fix rejects BEFORE allocating anything
// proportional to the attacker-declared count, not merely that an error is
// eventually returned.
const allocBudgetForRejection = 64 << 20

// TestWrite_RejectsHugeStripCount is the regression gate for GM-W1 sink A
// (extractParallelOffsetBlocks).
//
// Deliberately NOT t.Parallel(): this test measures process-global
// runtime.MemStats.TotalAlloc. That counter is a single cumulative value
// shared by the whole test binary, so any sibling test allocating
// concurrently (this package has 100+ t.Parallel() subtests elsewhere —
// conformance, bigtiff, bug111_116_118_149, etc.) inflates the measured
// delta and makes the allocation-budget assertion flaky, especially under
// -race. Running sequentially keeps the TotalAlloc window clean: the Go
// test runner holds already-declared parallel siblings paused at their own
// t.Parallel() call (not allocating) until this test and every other
// sequential test finish. See rmp task #263.
//
//nolint:paralleltest // global TotalAlloc measurement requires a clean (non-parallel) window; see comment above.
func TestWrite_RejectsHugeStripCount(t *testing.T) {
	cases := []struct {
		name string
		n    uint32
	}{
		{"just_above_per_entry_cap", maxImageBlocksPerOffsetEntry + 1},
		{"matches_confirmed_PoC_scale", 10_000_000}, // GM-W1 PoC: ~2.1 GiB TotalAlloc, ~2.2 GiB RSS
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := buildTIFFWithHugeStripArrayCount(tc.n)

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			var out bytes.Buffer
			// rawIPTC non-nil forces the copy-and-relocate path (bypasses the
			// pass-through early return), exactly like the vulnerable write path.
			err := Inject(bytes.NewReader(original), &out, original,
				[]byte("iptc-payload-long-enough-to-force-relocate"), nil, true)

			runtime.ReadMemStats(&after)

			if err == nil {
				t.Fatalf("Inject: expected error for StripOffsets Count=%d, got nil (wrote %d bytes)", tc.n, out.Len())
			}
			if !errors.Is(err, ErrTooManyImageBlocks) {
				t.Errorf("Inject: error does not wrap ErrTooManyImageBlocks: %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("Inject: wrote %d bytes on error, want 0", out.Len())
			}
			if got := after.TotalAlloc - before.TotalAlloc; got > allocBudgetForRejection {
				t.Errorf("Inject: allocated %d bytes rejecting Count=%d, want <= %d (GM-W1 regression)",
					got, tc.n, allocBudgetForRejection)
			}
		})
	}
}

// TestWrite_RejectsHugeSubIFDCount is the regression gate for GM-W1 sink B
// (enumerateSubIFDsAt).
//
// Deliberately NOT t.Parallel(): same rationale as
// TestWrite_RejectsHugeStripCount above — this test measures process-global
// runtime.MemStats.TotalAlloc, which is unreliable while sibling tests
// allocate concurrently. See rmp task #263.
//
//nolint:paralleltest // global TotalAlloc measurement requires a clean (non-parallel) window; see comment above.
func TestWrite_RejectsHugeSubIFDCount(t *testing.T) {
	cases := []struct {
		name string
		n    uint32
	}{
		{"just_above_per_entry_cap", maxSubIFDsPerEntry + 1},
		{"matches_confirmed_PoC_scale", 10_000_000}, // GM-W1 PoC: ~263 MiB TotalAlloc, ~324 MiB RSS
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := buildTIFFWithHugeSubIFDCount(tc.n)

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			var out bytes.Buffer
			err := Inject(bytes.NewReader(original), &out, original,
				[]byte("iptc-payload-long-enough-to-force-relocate"), nil, true)

			runtime.ReadMemStats(&after)

			if err == nil {
				t.Fatalf("Inject: expected error for 0x014A SubIFDs Count=%d, got nil (wrote %d bytes)", tc.n, out.Len())
			}
			if !errors.Is(err, ErrTooManyImageBlocks) {
				t.Errorf("Inject: error does not wrap ErrTooManyImageBlocks: %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("Inject: wrote %d bytes on error, want 0", out.Len())
			}
			if got := after.TotalAlloc - before.TotalAlloc; got > allocBudgetForRejection {
				t.Errorf("Inject: allocated %d bytes rejecting SubIFDs Count=%d, want <= %d (GM-W1 regression)",
					got, tc.n, allocBudgetForRejection)
			}
		})
	}
}

// allocBudgetForAggregateRejection is the ceiling on TotalAlloc bytes
// permitted for TestWrite_RejectsAggregateImageBlockBudget.
//
// Unlike the per-entry-cap tests above (which reject in O(1), before any
// per-element loop even starts), this test's fixture is DESIGNED to let the
// first (numIFDs-1) IFDs do real, legitimate per-element work up to the
// aggregate ceiling (maxAggregateImageBlocks = 262144 *imageBlock
// allocations) before the final IFD's charge is rejected — that bounded
// "low tens-of-MiB" cost is the intended, documented ceiling of
// maxAggregateImageBlocks itself, not a regression. Under `go test -race`,
// per-allocation shadow-memory instrumentation inflates that same object
// count substantially. 256 MiB comfortably covers both the race and
// non-race cases while still being two orders of magnitude below the
// multi-gigabyte scale this fix eliminates.
const allocBudgetForAggregateRejection = 256 << 20

// TestWrite_RejectsAggregateImageBlockBudget proves the AGGREGATE budget
// (maxAggregateImageBlocks / imageBlockBudget) closes the residual
// amplification where several entries, each individually within
// maxImageBlocksPerOffsetEntry, are chained together (via the IFD1 next-IFD
// chain) to exceed the combined cap.
//
// 5 IFDs x 65536 (== maxImageBlocksPerOffsetEntry, so no single entry trips
// the per-entry cap) = 327680 > maxAggregateImageBlocks (262144): the 5th
// IFD's charge must be rejected by the shared budget.
//
// Deliberately NOT t.Parallel(): same rationale as the two tests above —
// this test measures process-global runtime.MemStats.TotalAlloc, which is
// unreliable while sibling tests allocate concurrently. This is the test
// that was observed to flake under -race with a reported 4.4 GiB delta
// entirely attributable to concurrent siblings. See rmp task #263.
//
//nolint:paralleltest // global TotalAlloc measurement requires a clean (non-parallel) window; see comment above.
func TestWrite_RejectsAggregateImageBlockBudget(t *testing.T) {
	const numIFDs = 5
	original := buildChainedStripTIFFs(maxImageBlocksPerOffsetEntry, numIFDs)

	if perIFD, total := maxImageBlocksPerOffsetEntry, numIFDs*maxImageBlocksPerOffsetEntry; total <= maxAggregateImageBlocks {
		t.Fatalf("test invariant broken: %d IFDs x %d per IFD = %d must exceed maxAggregateImageBlocks (%d)",
			numIFDs, perIFD, total, maxAggregateImageBlocks)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	var out bytes.Buffer
	err := Inject(bytes.NewReader(original), &out, original,
		[]byte("iptc-payload-long-enough-to-force-relocate"), nil, true)

	runtime.ReadMemStats(&after)

	if err == nil {
		t.Fatalf("Inject: expected error for chained IFDs exceeding the aggregate budget, got nil (wrote %d bytes)", out.Len())
	}
	if !errors.Is(err, ErrTooManyImageBlocks) {
		t.Errorf("Inject: error does not wrap ErrTooManyImageBlocks: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("Inject: wrote %d bytes on error, want 0", out.Len())
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > allocBudgetForAggregateRejection {
		t.Errorf("Inject: allocated %d bytes rejecting the aggregate-budget file, want <= %d (GM-W1 regression)",
			got, allocBudgetForAggregateRejection)
	}
}

// ---------------------------------------------------------------------------
// Internal-function tests: prove rejection happens from the Count field
// alone, before the value array is consulted (fast-fail, no dependency on
// exif.Parse's own OOL bounds checking).
// ---------------------------------------------------------------------------

// TestExtractParallelOffsetBlocksRejectsHugeCountFast constructs an
// *exif.IFDEntry pair directly (bypassing exif.Parse) with an implausibly
// large Count but a deliberately tiny Value slice — proving
// extractParallelOffsetBlocks rejects on Count alone, before ever touching
// n*offElemSz bytes of "value" data that does not even exist here.
func TestExtractParallelOffsetBlocksRejectsHugeCountFast(t *testing.T) {
	t.Parallel()

	const hugeCount = 100_000_000 // no backing buffer could plausibly hold this
	offsetEntry := &exif.IFDEntry{
		Tag:   exif.TagStripOffsets,
		Type:  exif.TypeLong,
		Count: hugeCount,
		Value: make([]byte, 4), // deliberately far shorter than hugeCount*4
	}
	countEntry := &exif.IFDEntry{
		Tag:   exif.TagStripByteCounts,
		Type:  exif.TypeLong,
		Count: hugeCount,
		Value: make([]byte, 4),
	}
	ifd := &exif.IFD{}

	blocks, err := extractParallelOffsetBlocks(
		make([]byte, 16), ifd, exif.TagStripOffsets, offsetEntry, countEntry,
		binary.LittleEndian, newImageBlockBudget(),
	)
	if err == nil {
		t.Fatalf("extractParallelOffsetBlocks: expected error for Count=%d, got nil (blocks=%d)", hugeCount, len(blocks))
	}
	if !errors.Is(err, ErrTooManyImageBlocks) {
		t.Errorf("extractParallelOffsetBlocks: error does not wrap ErrTooManyImageBlocks: %v", err)
	}
	if blocks != nil {
		t.Errorf("extractParallelOffsetBlocks: expected nil blocks on error, got %d", len(blocks))
	}
}

// TestEnumerateSubIFDsAtRejectsHugeCountFast is the SubIFD analogue of
// TestExtractParallelOffsetBlocksRejectsHugeCountFast.
func TestEnumerateSubIFDsAtRejectsHugeCountFast(t *testing.T) {
	t.Parallel()

	const hugeCount = 100_000_000
	subEntry := &exif.IFDEntry{
		Tag:   exif.TagSubIFDs,
		Type:  exif.TypeLong,
		Count: hugeCount,
		Value: make([]byte, 4), // deliberately far shorter than hugeCount*4
	}

	subIFDs, blocks, err := enumerateSubIFDsAt(
		make([]byte, 16), subEntry, hugeCount, 4,
		binary.LittleEndian, 0, maxSubIFDDepth, newImageBlockBudget(),
	)
	if err == nil {
		t.Fatalf("enumerateSubIFDsAt: expected error for Count=%d, got nil (subIFDs=%d blocks=%d)",
			hugeCount, len(subIFDs), len(blocks))
	}
	if !errors.Is(err, ErrTooManyImageBlocks) {
		t.Errorf("enumerateSubIFDsAt: error does not wrap ErrTooManyImageBlocks: %v", err)
	}
	if subIFDs != nil || blocks != nil {
		t.Errorf("enumerateSubIFDsAt: expected nil results on error, got subIFDs=%d blocks=%d", len(subIFDs), len(blocks))
	}
}

// ---------------------------------------------------------------------------
// Positive controls: legitimate large-but-realistic files must not regress.
// ---------------------------------------------------------------------------

// TestWrite_LegitimateMultiStripStillRoundTrips proves that
// maxImageBlocksPerOffsetEntry does not reject a legitimate TIFF with far
// more strips than any real camera, scanner, or software encoder produces,
// yet still comfortably within the cap.
func TestWrite_LegitimateMultiStripStillRoundTrips(t *testing.T) {
	t.Parallel()

	const numStrips = 5000 // << maxImageBlocksPerOffsetEntry (65536)
	original := buildMultiStripTIFFN(numStrips)

	newIPTC := []byte("iptc-legit-multi-strip-regression-payload")
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, newIPTC, nil, true); err != nil {
		t.Fatalf("Inject legitimate %d-strip TIFF: unexpected error: %v", numStrips, err)
	}

	output := out.Bytes()
	e, err := exif.Parse(output)
	if err != nil {
		t.Fatalf("exif.Parse output: %v", err)
	}
	sOff := e.IFD0.Get(exif.TagStripOffsets)
	if sOff == nil || sOff.Count != numStrips || len(sOff.Value) < numStrips*4 {
		t.Fatalf("StripOffsets entry missing or malformed after relocation: %+v", sOff)
	}

	order := binary.ByteOrder(binary.LittleEndian)
	if e.ByteOrder == binary.BigEndian {
		order = binary.BigEndian
	}
	for i := range numStrips {
		off := order.Uint32(sOff.Value[i*4:])
		if int(off) >= len(output) || output[off] != byte(i) {
			t.Fatalf("strip %d: byte mismatch at relocated offset %d", i, off)
		}
	}
}

// TestWrite_LegitimateManySubIFDsStillRoundTrips proves that
// maxSubIFDsPerEntry does not reject a legitimate file with far more SubIFDs
// than any real DNG carries, yet still comfortably within the cap.
func TestWrite_LegitimateManySubIFDsStillRoundTrips(t *testing.T) {
	t.Parallel()

	const numSubIFDs = 50 // << maxSubIFDsPerEntry (1024); no real DNG approaches this
	original := buildTIFFWithNSubIFDs(numSubIFDs)

	newXMP := []byte(`<xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF/></xmpmeta>`)
	var out bytes.Buffer
	if err := Inject(bytes.NewReader(original), &out, original, nil, newXMP, true); err != nil {
		t.Fatalf("Inject legitimate %d-SubIFD TIFF: unexpected error: %v", numSubIFDs, err)
	}

	e, err := exif.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("exif.Parse output: %v", err)
	}
	subEntry := e.IFD0.Get(exif.TagSubIFDs)
	if subEntry == nil || subEntry.Count != numSubIFDs {
		t.Fatalf("0x014A SubIFDs entry missing or Count mismatch after relocation: %+v", subEntry)
	}
}
