package cr3

// task286_retention_test.go — regression battery for task #286 (Sprint 44,
// Batch E): Extract must read and retain memory proportional to the metadata
// actually present (ftyp + moov), not to the size of the whole CR3 file. Real
// Canon CR3 files are 12-37 MB, almost entirely raw sensor data in the mdat
// box that Extract never needs; the pre-#286 implementation read and
// retained the ENTIRE file via a single io.ReadAll, so every returned
// rawEXIF/rawXMP slice — no matter how small — pinned the whole buffer alive
// for as long as the caller held it.
//
// This mirrors xmp/xmpbuf_retain01_test.go's heapAlloc/testRetentionBounded
// pattern: a large, unambiguous padding vector (here, an mdat box many times
// bigger than the 64 KiB per-Metadata retention bound) makes "still retaining
// the whole file" and "retaining only the EXIF/XMP payload" trivially
// distinguishable by heap growth, without needing a razor-thin assertion
// against the exact 64 KiB figure (which would be vulnerable to ordinary
// GC/allocator noise).

import (
	"bytes"
	"runtime"
	"testing"
)

// heapAlloc286 forces two full GC cycles (settling any pending
// finalizer/sweep-triggered frees) and returns runtime.MemStats.HeapAlloc.
// Named distinctly from any sibling package's identically-purposed helper
// since this is a different package (cr3, not xmp).
func heapAlloc286() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// buildCR3WithLargeMdat builds a syntactically valid CR3 stream — ftyp, moov
// (Canon uuid > CMT1 > minimal TIFF), and a trailing mdat box padded to
// mdatSize bytes — mimicking a real CR3's shape: a small metadata-carrying
// moov followed by a large raw-sensor-data mdat.
func buildCR3WithLargeMdat(mdatSize int) []byte {
	data := buildMinimalCR3(minimalTIFF(), nil)
	mdat := buildBox("mdat", make([]byte, mdatSize))
	return append(data, mdat...)
}

// TestCR3ExtractRetentionBounded proves that retaining the rawEXIF result of
// N independent Extract calls does not pin N copies of the (large) mdat
// payload alive. Under the pre-#286 bug, each retained rawEXIF slice aliased
// its own full-file io.ReadAll buffer, so total heap growth would be
// approximately numDocs * mdatSize; the fix reads only ftyp+moov (a few dozen
// bytes here) and clones just the CMT1 payload out, so total growth for all
// numDocs objects should be on the order of numDocs small allocations, many
// orders of magnitude below a single mdat's size.
//
//nolint:paralleltest // measures GLOBAL process heap stats (runtime.ReadMemStats); running concurrently with other t.Parallel() tests would let their unrelated allocations pollute the before/after delta
func TestCR3ExtractRetentionBounded(t *testing.T) {
	const mdatSize = 8 << 20 // 8 MiB — far larger than any real EXIF/XMP payload
	const numDocs = 10

	template := buildCR3WithLargeMdat(mdatSize)

	before := heapAlloc286()

	rawEXIFs := make([][]byte, 0, numDocs)
	for i := range numDocs {
		// Each iteration gets its OWN independent copy of the file, exactly
		// like N distinct uploaded files each in their own backing array —
		// input is loop-scoped and referenced nowhere else, so it becomes
		// unreachable at the end of each iteration UNLESS Extract's result
		// still aliases it (the condition this test detects).
		input := append([]byte(nil), template...)
		rawEXIF, _, _, err := Extract(bytes.NewReader(input))
		if err != nil {
			t.Fatalf("Extract(doc %d): %v", i, err)
		}
		rawEXIFs = append(rawEXIFs, rawEXIF)
	}

	after := heapAlloc286()

	var grown uint64
	if after > before {
		grown = after - before
	}

	// The regression gate: total retained growth for ALL numDocs retained
	// rawEXIF results must stay well under the size of a SINGLE mdat. The
	// pre-#286 behaviour would retain roughly numDocs * mdatSize here
	// (80 MiB for these parameters); the fixed behaviour retains only the
	// tiny CMT1 payload per call (14 bytes of TIFF header here, plus small
	// slice-header/allocator overhead) — a few hundred bytes total for all
	// numDocs, many orders of magnitude below mdatSize.
	if grown >= mdatSize {
		t.Errorf("heap grew by %d bytes for %d retained Extract results (mdat=%d bytes each) — "+
			"want growth well under a single mdat's size (%d bytes); this indicates Extract is "+
			"retaining the whole file again instead of just the moov-derived EXIF/XMP payload",
			grown, numDocs, mdatSize, mdatSize)
	}
	// Independently pin the AC's literal "≤ 64 KiB per Metadata" figure, with
	// a generous ×4 safety margin against allocator/GC bookkeeping noise —
	// still two orders of magnitude below mdatSize, so this remains a
	// meaningful, non-vacuous check rather than just restating the guard
	// above with a bigger number.
	const perObjectBound = 64 << 10 // AC: ≤ 64 KiB retained per Metadata
	const safetyFactor = 4
	if bound := uint64(numDocs * perObjectBound * safetyFactor); grown >= bound {
		t.Errorf("heap grew by %d bytes for %d retained Extract results — want growth under %d bytes "+
			"(%d objects × %d KiB × %dx safety margin)",
			grown, numDocs, bound, numDocs, perObjectBound/1024, safetyFactor)
	}
	t.Logf("heap grew by %d bytes for %d retained Extract results (mdat=%d bytes each, %d bytes total mdat)",
		grown, numDocs, mdatSize, numDocs*mdatSize)

	runtime.KeepAlive(rawEXIFs)
}
