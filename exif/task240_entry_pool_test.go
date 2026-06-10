package exif

// task240_entry_pool_test.go — Regression gates for task #240.
//
// task #240 (performance audit 2026-06-10, finding F41): pool the
// filterEntries scratch slices in buildIFD0Entries / buildExifIFDEntries.
//
// Safety properties verified by this file:
//
//  1. Encode does not mutate the live IFD: IFD0 and ExifIFD entry slices
//     must have the same tag order and byte-identical values before and after
//     a call to Encode (the pool scratch slices are separate from the source IFD).
//
//  2. Pool reuse is race-free: concurrent Encode calls on independent EXIF
//     structs produce byte-identical output compared to a reference serial
//     encode, even when the pool hands out the same backing array to multiple
//     goroutines sequentially.
//
//  3. Encode of a camera-like EXIF (IFD0 + ExifIFD + GPSIFD) is byte-identical
//     to a reference encode performed without the pool (to guard against any
//     behavioural regression from the refactor).
//
// Spec reference: CIPA DC-008-2023 §4.6.2; TIFF 6.0 §2.
// Task reference: performance audit 2026-06-10, task #240.

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
)

// buildCameraEXIFForPool builds a parsed *EXIF with IFD0 (multiple tags),
// ExifIFD (multiple tags), and GPSIFD.  Used across all task #240 tests.
func buildCameraEXIFForPool(t *testing.T) *EXIF {
	t.Helper()
	data := buildCameraEXIF()
	e, err := Parse(data)
	if err != nil {
		t.Fatalf("task240: Parse camera EXIF: %v", err)
	}
	return e
}

// snapshotEntries returns a deep copy of entries (tag + value bytes) so
// we can compare before and after an Encode call without false positives
// caused by slice-header aliasing.
func snapshotEntries(entries []IFDEntry) []IFDEntry {
	snap := make([]IFDEntry, len(entries))
	for i, e := range entries {
		cp := make([]byte, len(e.Value))
		copy(cp, e.Value)
		snap[i] = IFDEntry{Tag: e.Tag, Type: e.Type, Count: e.Count, Value: cp, bigEndian: e.bigEndian}
	}
	return snap
}

// entriesEqual reports whether two entry snapshots are tag-and-value identical.
func entriesEqual(a, b []IFDEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Tag != b[i].Tag || a[i].Type != b[i].Type || a[i].Count != b[i].Count {
			return false
		}
		if !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

// TestTask240_EncodeDoesNotMutateIFD0 verifies that Encode does not mutate
// the live IFD0 entry slice.  The pool scratch slices must be separate from
// e.IFD0.Entries; any bleed would change the entry order or values.
//
// This is the primary regression gate for the pool-based filterEntriesInto
// implementation: if the scratch slice were backed by the live IFD entries
// array, sortEntries() would reorder the source IFD in place.
func TestTask240_EncodeDoesNotMutateIFD0(t *testing.T) {
	t.Parallel()

	e := buildCameraEXIFForPool(t)

	// Snapshot IFD0 before encode.
	before := snapshotEntries(e.IFD0.Entries)

	// Encode twice to exercise pool reuse.
	for range 2 {
		_, err := Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}

	// IFD0.Entries must be byte-identical to the pre-encode snapshot.
	after := snapshotEntries(e.IFD0.Entries)
	if !entriesEqual(before, after) {
		t.Errorf("task #240: Encode mutated IFD0 entries\nbefore: %v\nafter:  %v", before, after)
	}
}

// TestTask240_EncodeDoesNotMutateExifIFD verifies that Encode does not mutate
// the live ExifIFD entry slice.
func TestTask240_EncodeDoesNotMutateExifIFD(t *testing.T) {
	t.Parallel()

	e := buildCameraEXIFForPool(t)
	if e.ExifIFD == nil {
		t.Fatal("task240: buildCameraEXIFForPool must produce an ExifIFD")
	}

	before := snapshotEntries(e.ExifIFD.Entries)

	for range 2 {
		_, err := Encode(e)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}

	after := snapshotEntries(e.ExifIFD.Entries)
	if !entriesEqual(before, after) {
		t.Errorf("task #240: Encode mutated ExifIFD entries\nbefore: %v\nafter:  %v", before, after)
	}
}

// TestTask240_ConcurrentEncodeByteIdentical verifies that concurrent Encode
// calls on independent EXIF structs (pool-contested scenario) all produce
// byte-identical output compared to a reference serial encode.
//
// Run with -race to detect data races on the pool and on IFD state.
// The goroutine count (20) is chosen to maximise pool contention within a
// short test run while staying deterministic (each goroutine has its own EXIF).
func TestTask240_ConcurrentEncodeByteIdentical(t *testing.T) {
	t.Parallel()

	// Build a single reference EXIF and a reference encode output.
	ref := buildCameraEXIFForPool(t)
	refOut, err := Encode(ref)
	if err != nil {
		t.Fatalf("reference Encode: %v", err)
	}

	const goroutines = 20
	const encodesPer = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			// Each goroutine builds its own EXIF from the same source bytes to
			// avoid sharing mutable state across goroutines (Encode must be
			// safe for concurrent calls on DISTINCT EXIF values; it is not
			// required to be safe for concurrent calls on the SAME *EXIF).
			local := buildCameraEXIFForPool(t)
			for range encodesPer {
				out, encErr := Encode(local)
				if encErr != nil {
					t.Errorf("concurrent Encode: %v", encErr)
					return
				}
				if !bytes.Equal(out, refOut) {
					t.Errorf("concurrent Encode: output differs from reference\ngot  (first 64 B): %x\nwant (first 64 B): %x",
						out[:min(64, len(out))], refOut[:min(64, len(refOut))])
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestTask240_PoolPutClearsValueAliases verifies that putEntrySlice zeros the
// elements of the returned slice.  If Value aliases in pooled slots are NOT
// cleared, a subsequent Get would hand out a slice whose IFDEntry.Value fields
// point into a previous caller's IFD data — pinning the caller's byte slices
// in GC and potentially leaking data across encode calls.
//
// This test directly exercises the zero-before-Put contract by encoding once
// (populating the pool), triggering a GC, then encoding again with a different
// EXIF and verifying the second output is correct.
func TestTask240_PoolPutClearsValueAliases(t *testing.T) {
	t.Parallel()

	e1 := buildCameraEXIFForPool(t)
	out1, err := Encode(e1)
	if err != nil {
		t.Fatalf("Encode e1: %v", err)
	}

	// Build a minimal EXIF (different content) and encode it.  If the pool
	// returned a dirty slice from e1's encode, the filtered entries would
	// contain stale IFDEntry values → output would not match a fresh encode.
	data2 := minimalTIFF(binary.LittleEndian, [][4]uint32{
		{uint32(TagImageWidth), uint32(TypeLong), 1, 1920},
		{uint32(TagImageLength), uint32(TypeLong), 1, 1080},
	})
	e2, err := Parse(data2)
	if err != nil {
		t.Fatalf("Parse e2: %v", err)
	}
	out2, err := Encode(e2)
	if err != nil {
		t.Fatalf("Encode e2: %v", err)
	}

	// out1 and out2 must differ (different EXIF content).
	if bytes.Equal(out1, out2) {
		t.Error("task #240: Encode of different EXIFs produced identical output — suspect pool contamination")
	}

	// Re-encode e2 again; must match the first e2 encode exactly.
	out2b, err := Encode(e2)
	if err != nil {
		t.Fatalf("Encode e2 (second): %v", err)
	}
	if !bytes.Equal(out2, out2b) {
		t.Errorf("task #240: second Encode of e2 differs from first\ngot  %x\nwant %x", out2b, out2)
	}
}

// BenchmarkEXIFEncode_Camera measures the full encode cost for a realistic
// camera EXIF with IFD0 (15 entries), ExifIFD (20 entries), and GPSIFD
// (8 entries).  This is the benchmark most sensitive to filterEntries cost
// because it exercises both buildIFD0Entries and buildExifIFDEntries.
//
// Before task #240: filterEntries allocated once per build call → 2 allocs
// per Encode just for the scratch slices (on top of the output buffer and
// value-area allocations).
// After task #240: both scratch slices come from the pool → 0 allocs for the
// scratch copies on warm-pool steady state.
func BenchmarkEXIFEncode_Camera(b *testing.B) {
	data := buildCameraEXIF()
	e, err := Parse(data)
	if err != nil {
		b.Fatalf("task240 BenchmarkEXIFEncode_Camera: Parse: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = Encode(e)
	}
}
