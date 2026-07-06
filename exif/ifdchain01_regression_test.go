package exif

// ifdchain01_regression_test.go — regression gate for audit finding
// EXIF-IFDCHAIN-01: quadratic resource-exhaustion DoS via an overlapping
// IFD next-chain (CWE-400 uncontrolled resource consumption, CWE-405
// asymmetric resource consumption / amplification, CWE-834 excessive
// iteration).
//
// Background: prior to this fix, the only defence against a malicious
// next-IFD chain was a visited-offset cycle detector (a map[uint32]bool
// keyed by exact starting offset). That defence does nothing to bound a
// chain of K IFDs at K DISTINCT, densely-spaced (stride-4) offsets whose
// 12-byte entry regions overlap almost entirely in the buffer — every
// offset in such a chain is unique, so the cycle detector never fires.
// Each of the K IFDs re-parses up to C entries from largely the same
// bytes, so the traversal performed O(K*C) work and retained O(K*C)
// IFDEntry structs from only O(len(b)) input bytes: choosing K ~ C turned a
// linear-size input into quadratic CPU and memory cost. Measured on the
// pre-fix parser: 16KB->174MB/59ms, 32KB->707MB/322ms, 64KB->2.82GB/1.24s,
// 96KB->6.62GB (OOM territory) — and Parse returned success in every case.
//
// The fix (traverseBudget, defined in ifd.go immediately above traverse())
// caps both the cumulative entry count and the chain length for a
// traversal. This file proves that fix holds and that it does not regress
// well-formed multi-IFD files (TIFF 6.0 §2 IFD0->IFD1 thumbnail chains).
//
// Round 2 (2026-07-06, adversarial re-verification): the entries dimension
// of traverseBudget was originally charged with len(ifd.Entries) — the
// RETAINED entry count AFTER fillIFD's duplicate-tag dedup pass (audit
// finding #126). An attacker can fill each IFD with up to 65535 (the
// classic-TIFF uint16 count-field ceiling) entries that all share the SAME
// tag, so dedup collapses each IFD down to about one retained entry — the
// entries budget dimension barely moves, leaving only the looser 512-IFD
// chain-length cap as a backstop. 512 IFDs x 65535 declared/parsed entries
// is bounded (not quadratic) but still a ~4000x amplification: measured via
// the public gometadata.ReadFile path, 258KB -> chain 512 / ~0.83s CPU /
// ~595MB retained, 770KB -> chain 512 / ~5.35s CPU / ~13GB peak heap /
// ~1.74GB retained, plateauing at a constant (512 x 65535-entry) ceiling.
//
// Fix: fillIFD/fillIFDBigTIFF now return parsedCount — the clamped,
// buffer-fit-checked loop bound they actually iterated over, BEFORE dedup —
// and traverse/traverseWithArena/traverseBigTIFF charge the budget with
// THAT value instead of len(ifd.Entries). The dedup compaction itself was
// also hardened to copy down to a right-sized backing array whenever a
// duplicate is actually dropped, so even the handful of IFDs that do fit
// within budget no longer retain an oversized backing array. See
// parseSingleIFD's doc comment in ifd.go for the full rationale.

import (
	"encoding/binary"
	"runtime"
	"testing"
	"time"
)

// buildOverlappingIFDChain constructs a synthetic classic-TIFF buffer whose
// IFD0 next-chain visits `hops` IFDs at stride-4-spaced offsets, each
// declaring `perHop` entries via its count field. Because the offset
// stride (4 bytes) is far smaller than one IFD's entry region
// (perHop*12 bytes), each hop's declared entry region overlaps almost
// entirely with its neighbours' — this is the exact "overlapping IFDs at
// distinct offsets" shape described in EXIF-IFDCHAIN-01.
//
// Entry bytes are left at their zero value (Go's make zero-initialises the
// buffer): tag=0, type=0 (an unrecognised TIFF data type — TIFF 6.0 §2
// defines types 1-13; BigTIFF spec §3.3 adds 16-18; 0 is never valid), so
// typeSize returns 0 and parseIFDEntry always stores the raw 4-byte field
// inline without attempting an out-of-line fetch (see the "All entries are
// zero-valued" precedent in TestDoSCapHugeIFDCount). This guarantees every
// entry decode is safe regardless of how neighbouring hops' count-field and
// next-pointer writes happen to alias a given hop's entry region — the
// exact retained-entry count per hop is intentionally not pinned down here
// (it depends on the duplicate-tag dedup pass colliding on the incidental
// byte content), only that it is bounded, which is what this test asserts.
//
// The caller must choose hops and perHop so that 3*perHop > hops. That
// keeps the (2-byte count-field, 4-byte next-pointer) write lattice
// (spaced `stride` apart) from exactly aliasing a next-pointer write with a
// later count-field write, which would otherwise corrupt the deliberately
// constructed chain topology (the corrupted next-pointer would gain a
// stray high bit and point out of bounds, terminating the chain early —
// still "bounded", but not exercising the traverseBudget caps this test
// targets). This is a test-construction concern only; it has no bearing on
// the safety of the parser, which never assumes non-overlapping regions.
func buildOverlappingIFDChain(order binary.ByteOrder, hops, perHop int) []byte {
	const (
		base   = 8
		stride = 4
	)
	regionSize := 2 + perHop*12 + 4 // count field + entries + next-IFD pointer
	n := base + (hops-1)*stride + regionSize
	buf := make([]byte, n)

	// TIFF header (TIFF 6.0 §2): byte-order mark, magic 0x002A, IFD0 offset.
	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], base)

	for i := range hops {
		off := base + i*stride
		pos := off + 2

		// Count field: this hop declares perHop entries.
		order.PutUint16(buf[off:], uint16(perHop)) //nolint:gosec // test helper; perHop is a small caller-chosen constant

		// Next-IFD pointer, immediately after the (uninitialised, all-zero)
		// entry region: chain to the next hop (stride bytes further into the
		// buffer), or terminate the chain (0) at the very last hop.
		nextPos := pos + perHop*12
		if i < hops-1 {
			order.PutUint32(buf[nextPos:], uint32(off+stride))
		} else {
			order.PutUint32(buf[nextPos:], 0)
		}
	}
	return buf
}

// buildIdenticalTagIFDChain constructs a synthetic classic-TIFF buffer whose
// IFD0 next-chain visits `hops` IFDs at stride-4-spaced offsets, each
// declaring `perHop` entries that ALL share the same tag/type/count/value —
// deliberately defeating the duplicate-tag dedup pass (audit finding #126)
// down to a handful of retained IFDEntry structs per hop.
//
// This is the round-2 EXIF-IFDCHAIN-01 residual shape: charging the
// traversal budget with the RETAINED (post-dedup) entry count is not
// sufficient, because an attacker can make the retained count arbitrarily
// small while the PARSED count (and thus the CPU cost and the transient
// backing-array growth) stays large. The fix charges the budget with the
// pre-dedup parsed count instead — see parseSingleIFD's doc comment in
// ifd.go.
//
// Every entry is written as TypeUndefined (an EXIF-extension type with
// element size 1, CIPA DC-008-2023 §4.6.3) with count=1, so every decode is
// inline and never attempts an out-of-line fetch — safe regardless of any
// incidental corruption from neighbouring hops' structural writes.
//
// Construction is two-pass to guarantee the deliberately-repeated
// identical-tag entries survive intact:
//
//  1. Write every hop's count field and its full identical-tag entry
//     region, in ascending offset order.
//  2. Write every hop's next-IFD pointer LAST. Because each hop's entry
//     region (perHop*12 bytes) is far larger than the offset stride
//     (stride bytes) between hops, an entries-region write for a LATER hop
//     would otherwise land on top of an EARLIER hop's next-IFD pointer
//     (the same aliasing arithmetic as buildOverlappingIFDChain's
//     count-field/next-pointer collision, scaled up to the whole entry
//     region). Writing every pointer only after all entries have been
//     written removes that risk entirely.
//
// As with buildOverlappingIFDChain, the caller must choose hops and perHop
// so that 3*perHop > hops, which keeps pass 2's next-pointer writes from
// ever landing inside any hop's entries region (the same margin, derived
// from the same 12-byte-entry / stride-4-offset relationship).
func buildIdenticalTagIFDChain(order binary.ByteOrder, hops, perHop int) []byte {
	const (
		base   = 8
		stride = 4
		// sameTag is an arbitrary fixed tag shared by every entry in every
		// hop; the specific value is not meaningful, only that it is
		// identical across the whole construction.
		sameTag = 0x1234
	)
	regionSize := 2 + perHop*12 + 4
	n := base + (hops-1)*stride + regionSize
	buf := make([]byte, n)

	if order == binary.LittleEndian {
		buf[0], buf[1] = 'I', 'I'
	} else {
		buf[0], buf[1] = 'M', 'M'
	}
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], base)

	// Pass 1: count field + identical-tag entries for every hop.
	for i := range hops {
		off := base + i*stride
		pos := off + 2

		order.PutUint16(buf[off:], uint16(perHop)) //nolint:gosec // test helper; perHop is a small caller-chosen constant

		for k := range perHop {
			e := pos + k*12
			order.PutUint16(buf[e:], sameTag)
			order.PutUint16(buf[e+2:], uint16(TypeUndefined))
			order.PutUint32(buf[e+4:], 1) // TIFF value count = 1 element
			buf[e+8] = 0xAA               // inline value byte; content is not meaningful
		}
	}

	// Pass 2: next-IFD pointers, written after every hop's entries so no
	// entries-region write (pass 1) can clobber a pointer set here.
	for i := range hops {
		off := base + i*stride
		pos := off + 2
		nextPos := pos + perHop*12
		if i < hops-1 {
			order.PutUint32(buf[nextPos:], uint32(off+stride))
		} else {
			order.PutUint32(buf[nextPos:], 0)
		}
	}
	return buf
}

// TestEXIFIFDCHAIN01_TraversalBudget is the named regression gate for audit
// finding EXIF-IFDCHAIN-01.
//
// Its subtests deliberately do NOT call t.Parallel(): the first subtest
// asserts a wall-clock ceiling and cumulative-allocation bounds, both of
// which would become flaky under contention from other parallel tests
// sharing the machine's CPUs. Running serially keeps those measurements
// reliable.
//
//nolint:paralleltest // timing/allocation-sensitive; measured subtests must run serially, not in parallel with other tests
func TestEXIFIFDCHAIN01_TraversalBudget(t *testing.T) {
	t.Run("EXIF-IFDCHAIN-01_overlapping_chain_bounded", func(t *testing.T) {
		// hops=600 declared IFDs, 250 entries each, stride 4. 3*perHop=750 >
		// hops=600, so the chain topology survives construction uncorrupted
		// (see buildOverlappingIFDChain doc). If traverseBudget did not exist,
		// this chain would legitimately walk the full 600 hops (each next
		// pointer is well-formed and in-bounds), giving an unbounded chain
		// length proportional to K=600 — this is precisely the shape the
		// audit measured amplifying to gigabytes of retained memory at
		// larger K and C.
		const (
			hops   = 600
			perHop = 250
		)
		order := binary.LittleEndian
		buf := buildOverlappingIFDChain(order, hops, perHop)

		start := time.Now()
		e, err := Parse(buf)
		elapsed := time.Since(start)

		// CWE-834: must complete promptly. The pre-fix parser took over a
		// second on a 64KB crafted input; this buffer is a few KB, so a
		// correctly bounded parse completes in well under a second even on
		// a heavily loaded CI machine. A regression that reintroduces
		// unbounded traversal would make this assertion the first to fail.
		const maxElapsed = 3 * time.Second
		if elapsed > maxElapsed {
			t.Fatalf("EXIF-IFDCHAIN-01: Parse took %v (want <= %v) — traversal budget is not bounding the chain", elapsed, maxElapsed)
		}
		if err != nil {
			t.Fatalf("EXIF-IFDCHAIN-01: Parse returned an unexpected error: %v", err)
		}
		if e == nil || e.IFD0 == nil {
			t.Fatal("EXIF-IFDCHAIN-01: Parse returned a nil EXIF/IFD0 for a well-formed (if malicious) first IFD")
		}

		// Walk the resulting chain. The parser's own visited-offset cycle
		// detector guarantees this list is finite even without the fix
		// under test, but a generous iteration cap keeps this test loop
		// itself from ever hanging if some future change breaks that
		// invariant (CWE-834 defence-in-depth in the test itself).
		const iterationSafetyCap = 10_000
		chainLen := 0
		totalEntries := 0
		for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
			chainLen++
			totalEntries += len(ifd.Entries)
			if chainLen > iterationSafetyCap {
				t.Fatalf("EXIF-IFDCHAIN-01: IFD chain exceeded the test's safety cap of %d — traversal is not terminating", iterationSafetyCap)
			}
		}

		// Primary assertion: chain length must never exceed the documented,
		// fixed ceiling (maxTraverseChainIFDs), regardless of how the file
		// shapes its next-IFD chain. The buffer above declares 600
		// well-formed, in-bounds hops — proof that this bound comes from
		// the budget, not from the file happening to terminate early.
		if chainLen > maxTraverseChainIFDs {
			t.Fatalf("EXIF-IFDCHAIN-01: chain length = %d, want <= maxTraverseChainIFDs (%d)", chainLen, maxTraverseChainIFDs)
		}
		if chainLen < 1 {
			t.Fatal("EXIF-IFDCHAIN-01: chain length = 0; IFD0 itself must always be retained")
		}

		// Secondary assertion: cumulative retained entries must stay within
		// the documented budget formula, plus the one-hop overshoot the
		// budget-checked-before-parse design explicitly allows (see the
		// "IFD chain traversal budget" comment above traverse() in ifd.go).
		// This directly ties the test to newTraverseBudget's own contract
		// rather than to a magic number.
		budget := newTraverseBudget(len(buf))
		maxAllowedEntries := budget.entries + perHop
		if totalEntries > maxAllowedEntries {
			t.Fatalf("EXIF-IFDCHAIN-01: cumulative retained entries = %d, want <= %d (budget.entries=%d + one-hop overshoot perHop=%d); K*perHop would have been %d",
				totalEntries, maxAllowedEntries, budget.entries, perHop, hops*perHop)
		}

		t.Logf("EXIF-IFDCHAIN-01: bounded to chainLen=%d totalEntries=%d in %v (unbounded worst case: chainLen=%d entries~%d)",
			chainLen, totalEntries, elapsed, hops, hops*perHop)
	})

	// EXIF-IFDCHAIN-01_duplicate_tag_dedup_undercount_bounded is the
	// dedicated regression gate for the round-2 residual: charging the
	// budget with the RETAINED (post-dedup) entry count instead of the
	// PARSED (pre-dedup) count let an attacker fill each of up to 512
	// overlapping IFDs with a large count of identical-tag entries that
	// dedup collapses to ~1 retained entry each, so the entries budget
	// dimension barely moved and only the 512-IFD chain cap remained as a
	// backstop — a bounded but severe ~4000x amplification (measured via
	// the public gometadata.ReadFile path: 258KB->~595MB retained,
	// 770KB->~13GB peak heap). This asserts both the parsed-entry-count
	// bound (via the same budget-formula check as the subtest above) and a
	// live-heap retention bound (via a HeapAlloc delta across the Parse
	// call), which is the metric the audit actually measured.
	t.Run("EXIF-IFDCHAIN-01_duplicate_tag_dedup_undercount_bounded", func(t *testing.T) {
		// hops=512 matches the audit's own PoC chain length exactly; perHop
		// is a large declared/parsed-per-IFD entry count (3*perHop=6000 >
		// hops=512, keeping construction uncorrupted per
		// buildIdenticalTagIFDChain's doc). Every entry in every hop shares
		// the same tag, so — pre-fix — len(ifd.Entries) would collapse to
		// about 1 per hop regardless of perHop, defeating an entries-only
		// budget charge and leaving the chain to run all the way to the
		// 512-IFD cap.
		const (
			hops   = 512
			perHop = 20000
		)
		order := binary.LittleEndian
		buf := buildIdenticalTagIFDChain(order, hops, perHop)

		// Force a clean baseline before measuring: collect any garbage from
		// buffer construction above so it does not bleed into the delta.
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		start := time.Now()
		e, err := Parse(buf)
		elapsed := time.Since(start)

		// Collect transient parse-time garbage (sort scratch, discarded
		// arena fallbacks, etc.) while e/the retained chain is still
		// referenced below, so the delta reflects LIVE retained bytes
		// rather than the parse's total transient allocation traffic —
		// the same "retained" metric the audit measured in GB.
		runtime.GC()
		runtime.ReadMemStats(&after)

		const maxElapsed = 3 * time.Second
		if elapsed > maxElapsed {
			t.Fatalf("EXIF-IFDCHAIN-01: Parse took %v (want <= %v) — traversal budget is not bounding the chain", elapsed, maxElapsed)
		}
		if err != nil {
			t.Fatalf("EXIF-IFDCHAIN-01: Parse returned an unexpected error: %v", err)
		}
		if e == nil || e.IFD0 == nil {
			t.Fatal("EXIF-IFDCHAIN-01: Parse returned a nil EXIF/IFD0 for a well-formed (if malicious) first IFD")
		}

		const iterationSafetyCap = 10_000
		chainLen := 0
		totalEntries := 0
		for ifd := e.IFD0; ifd != nil; ifd = ifd.Next {
			chainLen++
			totalEntries += len(ifd.Entries)
			if chainLen > iterationSafetyCap {
				t.Fatalf("EXIF-IFDCHAIN-01: IFD chain exceeded the test's safety cap of %d — traversal is not terminating", iterationSafetyCap)
			}
		}

		// Primary assertion: chain length bounded by the fixed ceiling,
		// exactly as in the distinct-offset subtest above. The buffer
		// declares 512 well-formed, in-bounds hops — proof this bound
		// comes from the budget, not from the file terminating on its own.
		if chainLen > maxTraverseChainIFDs {
			t.Fatalf("EXIF-IFDCHAIN-01: chain length = %d, want <= maxTraverseChainIFDs (%d)", chainLen, maxTraverseChainIFDs)
		}
		if chainLen < 1 {
			t.Fatal("EXIF-IFDCHAIN-01: chain length = 0; IFD0 itself must always be retained")
		}

		// Secondary assertion: even though every hop's RETAINED entry count
		// collapses to a handful via dedup, the chain must still stop well
		// short of 512 hops — proof that the budget is charged with the
		// PARSED (pre-dedup) count, not len(ifd.Entries). If the round-2 fix
		// were reverted, chainLen would run to exactly hops (512), since
		// each hop's collapsed retained count barely dents an
		// entries-only budget.
		if chainLen >= hops {
			t.Fatalf("EXIF-IFDCHAIN-01: chain length = %d reached the full declared hop count (%d) — the entries budget is not being charged with the pre-dedup parsed count (dedup-undercount regression)", chainLen, hops)
		}

		// Tertiary assertion: live retained heap growth attributable to
		// this Parse call must stay far below what an unbounded 512-hop
		// chain of perHop-entry IFDs would require. This buffer's declared
		// (pre-dedup) work is hops*perHop = 1,024,000 entries; at ~48
		// bytes/IFDEntry that is ~49MB if every declared entry were
		// retained without dedup or budget bounding at all, and the
		// pre-round-2-fix behaviour (oversized backing arrays surviving
		// dedup) pushed real-world measurements into the hundreds of MB to
		// low GB range at this scale. A generous 32MB ceiling is far above
		// the fixed code's actual retention (a handful of IFDEntry structs
		// per bounded hop) while remaining far below any plausible
		// unbounded or under-charged outcome.
		const maxRetainedBytes = 32 << 20 // 32 MiB
		var retainedBytes int64
		if after.HeapAlloc > before.HeapAlloc {
			retainedBytes = int64(after.HeapAlloc - before.HeapAlloc) //nolint:gosec // G115: HeapAlloc delta guarded non-negative by the comparison above; test-only measurement
		}
		if retainedBytes > maxRetainedBytes {
			t.Fatalf("EXIF-IFDCHAIN-01: retained heap delta = %d bytes (%.1f MiB), want <= %d bytes (%d MiB) — dedup-undercount amplification regression",
				retainedBytes, float64(retainedBytes)/(1<<20), maxRetainedBytes, maxRetainedBytes>>20)
		}

		t.Logf("EXIF-IFDCHAIN-01: bounded to chainLen=%d totalEntries=%d retainedBytes=%d (%.2f MiB) in %v (unbounded worst case: chainLen=%d parsedEntries~%d)",
			chainLen, totalEntries, retainedBytes, float64(retainedBytes)/(1<<20), elapsed, hops, hops*perHop)
	})

	// EXIF-IFDCHAIN-01_wellformed_multi_ifd_unaffected proves the fix does
	// not regress the common, entirely legitimate case: a small TIFF/EXIF
	// file whose IFD0 chains to a single IFD1 (the classic JPEG thumbnail
	// IFD, TIFF 6.0 §2 / EXIF §4.5.1). Both IFDs, and every tag in them,
	// must still be reachable exactly as before this change.
	t.Run("EXIF-IFDCHAIN-01_wellformed_multi_ifd_unaffected", func(t *testing.T) {
		order := binary.LittleEndian

		// Layout mirrors TestIFD1ThumbnailChain (exif_test.go):
		//   offset 0-7:  TIFF header
		//   offset 8:    IFD0  (1 entry + next ptr -> IFD1)
		//   offset 26:   IFD1  (1 entry + next ptr = 0)
		const ifd0Off = 8
		const ifd1Off = ifd0Off + 2 + 12 + 4 // = 26

		buf := make([]byte, ifd1Off+2+12+4)

		buf[0], buf[1] = 'I', 'I'
		order.PutUint16(buf[2:], 0x002A)
		order.PutUint32(buf[4:], ifd0Off)

		// IFD0: 1 entry (ImageWidth=1920), next -> IFD1.
		order.PutUint16(buf[ifd0Off:], 1)
		order.PutUint16(buf[ifd0Off+2:], uint16(TagImageWidth))
		order.PutUint16(buf[ifd0Off+4:], uint16(TypeLong))
		order.PutUint32(buf[ifd0Off+6:], 1)
		order.PutUint32(buf[ifd0Off+10:], 1920)
		order.PutUint32(buf[ifd0Off+14:], ifd1Off)

		// IFD1: 1 entry (ImageWidth=160, the thumbnail width), next = 0.
		order.PutUint16(buf[ifd1Off:], 1)
		order.PutUint16(buf[ifd1Off+2:], uint16(TagImageWidth))
		order.PutUint16(buf[ifd1Off+4:], uint16(TypeLong))
		order.PutUint32(buf[ifd1Off+6:], 1)
		order.PutUint32(buf[ifd1Off+10:], 160)
		order.PutUint32(buf[ifd1Off+14:], 0)

		e, err := Parse(buf)
		if err != nil {
			t.Fatalf("EXIF-IFDCHAIN-01: Parse of a well-formed IFD0->IFD1 file failed: %v", err)
		}
		if e.IFD0 == nil {
			t.Fatal("EXIF-IFDCHAIN-01: IFD0 is nil for a well-formed file")
		}
		ifd0Entry := e.IFD0.Get(TagImageWidth)
		if ifd0Entry == nil {
			t.Fatal("EXIF-IFDCHAIN-01: IFD0 TagImageWidth not found")
		}
		if got := ifd0Entry.Uint32(); got != 1920 {
			t.Errorf("EXIF-IFDCHAIN-01: IFD0 ImageWidth = %d, want 1920", got)
		}
		if e.IFD0.Next == nil {
			t.Fatal("EXIF-IFDCHAIN-01: IFD0.Next (IFD1) is nil — the traversal budget must not cut short a two-IFD chain")
		}
		ifd1Entry := e.IFD0.Next.Get(TagImageWidth)
		if ifd1Entry == nil {
			t.Fatal("EXIF-IFDCHAIN-01: IFD1 TagImageWidth not found")
		}
		if got := ifd1Entry.Uint32(); got != 160 {
			t.Errorf("EXIF-IFDCHAIN-01: IFD1 ImageWidth = %d, want 160", got)
		}
		if e.IFD0.Next.Next != nil {
			t.Error("EXIF-IFDCHAIN-01: IFD1.Next is non-nil; expected the chain to terminate after IFD1")
		}
	})
}
