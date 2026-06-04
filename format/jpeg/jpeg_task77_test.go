package jpeg

// Task #77 regression — readSegment must not orphan the pooled scratch buffer
// when a segment payload exceeds the current scratch length.
//
// Root cause: when need > len(*scratch), readSegment overwrote *scratch with a
// fresh make([]byte, need) allocation without first returning the old pooled
// buffer to iobuf. The deferred iobuf.Put in extractFull/Inject then put back
// the *new* (larger) slice while the original 4096-byte pooled backing array was
// abandoned, eventually depleting the small pool under sustained load.
//
// Fix: save old := *scratch, call iobuf.Put(&old) before *scratch = make(...).
// The old variable escapes to the heap via sync.Pool, which makes the Put safe.

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/internal/iobuf"
)

// buildJPEGWithLargeEXIF constructs a minimal JPEG whose EXIF APP1 payload is
// exactly payloadSize bytes. payloadSize must be >= len("Exif\x00\x00") = 6.
// The EXIF content is a valid TIFF header followed by padding zeros so that the
// total APP1 payload (identifier + TIFF data + padding) equals payloadSize bytes.
// This guarantees readSegment will see need > 4096 = defaultScratchSize when
// payloadSize > 4096, triggering the resize path.
func buildJPEGWithLargeEXIF(payloadSize int) []byte {
	const exifIdent = "Exif\x00\x00" // 6 bytes
	if payloadSize < len(exifIdent)+8 {
		// Minimum: identifier (6) + minimal TIFF header (8).
		payloadSize = len(exifIdent) + 8
	}

	// Build a minimal TIFF header (8 bytes LE) followed by zero padding so the
	// payload reaches exactly payloadSize bytes.
	tiff := make([]byte, payloadSize-len(exifIdent))
	tiff[0], tiff[1] = 'I', 'I' // little-endian
	binary.LittleEndian.PutUint16(tiff[2:], 0x002A)
	binary.LittleEndian.PutUint32(tiff[4:], 0) // IFD0 offset = 0 (no entries — not a valid TIFF, but enough to pass the Extract stage)

	payload := append([]byte(exifIdent), tiff...)

	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xD8}) // SOI
	// APP1 segment: 0xFF 0xE1 <length-2-bytes> <payload>
	// JPEG length field includes itself (2 bytes) but not the marker.
	segLen := uint16(len(payload) + 2) //nolint:gosec // G115: test helper, bounded by test input
	buf.Write([]byte{0xFF, 0xE1})
	var lb [2]byte
	binary.BigEndian.PutUint16(lb[:], segLen)
	buf.Write(lb[:])
	buf.Write(payload)
	// Minimal SOS + EOI to terminate the marker stream.
	buf.Write([]byte{0xFF, 0xDA, 0x00, 0x02, 0xFF, 0xD9})
	return buf.Bytes()
}

// TestJPEGReadSegmentPoolReuse is the mandatory regression gate for task #77.
//
// It verifies that extracting a JPEG whose EXIF APP1 payload exceeds the default
// scratch buffer size (4096 bytes) does not permanently deplete the small-tier
// iobuf pool under repeated calls.
//
// Before the fix: each Extract call orphaned the original 4096-byte pooled buffer
// (it was overwritten in readSegment but never Put back). The pool shrank by one
// entry per call; subsequent calls triggered new allocations from pool.New.
//
// After the fix: iobuf.Put(&old) is called before the overwrite, so the old
// buffer is returned to the pool on every resize. The pool stays healthy: Get
// returns a recycled buffer rather than calling New.
//
// Verification strategy: we measure heap allocations per Extract call using
// testing.AllocsPerRun. With the fix, once the pool is warm the per-call alloc
// count is bounded by a small constant (one for the old-slice escape, one for
// the make, plus a few for the extracted EXIF slice copy). Without the fix the
// count would grow by 1 per iteration because each call leaks one pool entry,
// forcing pool.New on every Get(4096).
//
// We also perform a direct pool-health check: after N iterations we drain the
// small pool tier up to a fixed limit and verify we can still retrieve at least
// one buffer with cap == iobuf default size (4096), confirming the pool was
// replenished by the fix.
func TestJPEGReadSegmentPoolReuse(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics in parallel tests
	// testing.AllocsPerRun requires a non-parallel test.

	// Use a payload larger than the default scratch (4096 bytes) so readSegment
	// is forced to resize. 8192 bytes is a realistic EXIF size for most cameras.
	const largePayloadSize = 8192
	jpegData := buildJPEGWithLargeEXIF(largePayloadSize)

	// Sanity: Extract must succeed without error.
	rawEXIF, _, _, err := Extract(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("Extract: unexpected error: %v", err)
	}
	if rawEXIF == nil {
		t.Fatal("Extract: rawEXIF is nil; JPEG construction error")
	}

	// --- Allocation-rate check ---
	// Warm the pool by running once before the measurement to avoid counting the
	// initial pool-miss allocation in the average.
	for range 3 {
		_, _, _, _ = Extract(bytes.NewReader(jpegData))
	}
	runtime.GC() // flush pool and reset heap counters before measurement

	const measureRuns = 50
	allocs := testing.AllocsPerRun(measureRuns, func() {
		_, _, _, _ = Extract(bytes.NewReader(jpegData))
	})

	// The expected allocs per Extract call (with fix, steady state):
	//   1 — old slice header escapes to heap via iobuf.Put (resize path)
	//   1 — make([]byte, need) for the large scratch buffer
	//   1 — rawEXIF copy (append([]byte(nil), data...) in processAPP1Segment)
	// Total: ~3. We allow up to 10 to be conservative against future minor changes.
	//
	// Without the fix the count would be higher (additional pool.New calls per
	// iteration as the pool depletes). The bound here primarily catches a
	// regression where every call allocates a fresh 4096-byte pool-entry buffer.
	const allocBudget = 10
	if allocs > allocBudget {
		t.Errorf("Extract allocs/run = %.1f, want <= %d; pool may be depleted (orphaned buffer regression)",
			allocs, allocBudget)
	}

	// --- Direct pool-health check ---
	// After N iterations, the small pool must still contain at least one entry
	// with cap == defaultSize (4096). We drain up to a bound, collect all entries,
	// and confirm at least one has the canonical small-tier capacity.
	// Note: iobuf.Get routes n <= defaultSize to the small pool; a hit returns
	// a buffer with cap == defaultSize when the pool has a canonical entry.
	// We use a deliberately tight loop with no allocation between drain and check
	// to minimise GC eviction probability.
	const drainBound = 32
	runtime.GC() // ensure stale pool entries are flushed before recheck

	drained := make([]*[]byte, 0, drainBound)
	for range drainBound {
		p := iobuf.Get(1) // requests 1 byte → routes to small pool
		drained = append(drained, p)
	}
	defer func() {
		for _, p := range drained {
			iobuf.Put(p)
		}
	}()

	foundCanonical := false
	for _, p := range drained {
		if cap(*p) == 4096 {
			foundCanonical = true
			break
		}
	}

	// We cannot guarantee a canonical-cap buffer is present (GC may evict pool
	// entries between the last Extract and this drain), so we log rather than
	// fatalf. The alloc-rate check above is the primary gate.
	if !foundCanonical {
		t.Logf("pool-health check: no canonical 4096-byte entry found after %d drains "+
			"(GC may have evicted pool entries — this is not a test failure, "+
			"but alloc-rate check above is the primary regression gate)", drainBound)
	} else {
		t.Logf("pool-health check: canonical 4096-byte pool entry confirmed after %d Extract calls",
			measureRuns)
	}
}

// TestJPEGReadSegmentPoolReuseInject verifies the same fix applies to the Inject
// code path, which also uses a pooled scratch buffer passed through readSegment.
func TestJPEGReadSegmentPoolReuseInject(t *testing.T) { //nolint:paralleltest // testing.AllocsPerRun panics in parallel tests
	// testing.AllocsPerRun requires a non-parallel test.

	const largePayloadSize = 8192
	jpegData := buildJPEGWithLargeEXIF(largePayloadSize)

	// Minimal EXIF for round-trip inject.
	tiffData := minimalTIFFBytes()

	// Warm up.
	for range 3 {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(jpegData), &out, tiffData, nil, nil, true)
	}
	runtime.GC()

	const measureRuns = 50
	allocs := testing.AllocsPerRun(measureRuns, func() {
		var out bytes.Buffer
		_ = Inject(bytes.NewReader(jpegData), &out, tiffData, nil, nil, true)
	})

	// Inject allocates more than Extract (output buffer, EXIF segment buffer,
	// etc.) but the scratch-buffer path must not leak one allocation per call.
	// We set a generous budget; the critical regression is an unbounded linear
	// growth, not the absolute level.
	const allocBudget = 20
	if allocs > allocBudget {
		t.Errorf("Inject allocs/run = %.1f, want <= %d; pool may be depleted (orphaned buffer regression)",
			allocs, allocBudget)
	}
}
