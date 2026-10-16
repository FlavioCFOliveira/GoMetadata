package heif

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"
)

// Security audit 2026-10-16 (v1.5.0 pre-clearance), findings F-01, F-02, F-09.
// Sub-test names are the finding IDs.

// buildAuditFile assembles ftyp + meta{pitm?, iinf, iloc} for the audit PoCs.
// items maps an item id to its infe box; entries are the iloc entries.
func buildAuditFile(infes [][]byte, entries []ilocTestEntry) []byte {
	body := append([]byte{0, 0, 0, 0}, bmffIinf(infes...)...)
	body = append(body, boxWithHeader("iloc", buildIlocBody(0, 8, 8, 0, 0, entries))...)
	out := bmffFtyp("heic", 0, "mif1")
	return append(out, bmffBox("meta", body)...)
}

func TestSecurityAuditV150HEIF(t *testing.T) {
	t.Parallel()

	t.Run("F-01", func(t *testing.T) {
		t.Parallel()
		// Two extents whose lengths sum to 50 modulo 2^64: 100 + (2^64-50).
		// Before the fix itemLength wrapped, allocated 50 bytes and the copy
		// of the first extent panicked with slice bounds out of range.
		f := buildAuditFile([][]byte{bmffInfeV2(1, "Exif")}, []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{0, 100}, {0, ^uint64(0) - 49}}},
		})
		f = append(f, make([]byte, 400)...)
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Extract panicked on a wrapping extent-length sum: %v", r)
			}
		}()
		exif, _, _, _ := Extract(bytes.NewReader(f))
		if exif != nil {
			t.Errorf("Extract returned %d EXIF bytes for an impossible item", len(exif))
		}
		// The Exif item error is swallowed by Extract (best effort); the XMP
		// item path reports it, so check that variant for a specific error.
		g := buildAuditFile([][]byte{bmffInfeV2WithContentType(1, "mime", "application/rdf+xml")}, []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{0, 100}, {0, ^uint64(0) - 49}}},
		})
		g = append(g, make([]byte, 400)...)
		_, _, _, err := Extract(bytes.NewReader(g))
		if !errors.Is(err, errItemPayloadTooLarge) && !errors.Is(err, errItemExtentOutOfFile) {
			t.Errorf("Extract error = %v, want a payload-size or extent-range error", err)
		}
		// A huge single extent must also be rejected, not wrapped.
		h := buildAuditFile([][]byte{bmffInfeV2WithContentType(1, "mime", "application/rdf+xml")}, []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{0, 1 << 62}, {0, 1 << 62}, {0, 1 << 62}, {0, 1 << 62}}},
		})
		if _, _, _, err = Extract(bytes.NewReader(h)); err == nil {
			t.Error("Extract accepted four 2^62-byte extents")
		}
	})

	t.Run("F-09", func(t *testing.T) {
		t.Parallel()
		// A 168-byte file whose Exif and XMP items each claim 250 MiB used to
		// make Extract allocate 500 MiB before the first read failed.
		f := buildAuditFile([][]byte{
			bmffInfeV2(1, "Exif"),
			bmffInfeV2WithContentType(2, "mime", "application/rdf+xml"),
		}, []ilocTestEntry{
			{id: 1, extents: [][2]uint64{{0, 250 << 20}}},
			{id: 2, extents: [][2]uint64{{0, 250 << 20}}},
		})
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		_, _, _, err := Extract(bytes.NewReader(f))
		runtime.ReadMemStats(&m1)
		// TotalAlloc is process-wide and tests run in parallel, so allow slack
		// for neighbours; the unfixed code allocates 500 MiB.
		if d := m1.TotalAlloc - m0.TotalAlloc; d > 32<<20 {
			t.Errorf("Extract allocated %d MiB for a %d-byte file", d>>20, len(f))
		}
		if !errors.Is(err, errItemExtentOutOfFile) {
			t.Errorf("Extract error = %v, want errItemExtentOutOfFile", err)
		}
	})

	t.Run("F-02", func(t *testing.T) {
		t.Parallel()
		// A 12-byte trailer: a box with size 4, then a largesize box whose
		// largesize is 0. patchAncestorSize walked past the meta box into it
		// and never advanced.
		base := buildHEIF([]byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"), nil)
		trailers := [][]byte{
			{0, 0, 0, 4, 0, 0, 0, 1, 'a', 'b', 'c', 'd', 0, 0, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 1, 'a', 'b', 'c', 'd', 0, 0, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 1, 'a', 'b', 'c', 'd', 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF0},
			{0, 0, 0, 3, 'a', 'b', 'c', 'd'},
		}
		bigEXIF := append([]byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...)
		for i, tr := range trailers {
			f := append(bytes.Clone(base), tr...)
			done := make(chan error, 1)
			go func() { done <- Inject(bytes.NewReader(f), io.Discard, bigEXIF, nil, nil, true) }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("trailer %d: Inject error = %v, want success", i, err)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("trailer %d: Inject did not return within 30s (infinite loop)", i)
			}
		}
	})

	t.Run("F-02-unit", func(t *testing.T) {
		t.Parallel()
		// Direct calls: invalid sizes before the meta box are reported, never
		// spun on.
		for name, data := range map[string][]byte{
			"size 4":         {0, 0, 0, 4, 'a', 'b', 'c', 'd', 0, 0, 0, 8, 'm', 'e', 't', 'a'},
			"largesize 0":    {0, 0, 0, 1, 'a', 'b', 'c', 'd', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 8, 'm', 'e', 't', 'a'},
			"largesize wrap": {0, 0, 0, 1, 'a', 'b', 'c', 'd', 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xF8, 0, 0, 0, 8, 'm', 'e', 't', 'a'},
		} {
			done := make(chan error, 1)
			go func() { done <- patchAncestorSize(bytes.Clone(data), len(data)-8, 4) }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("%s: patchAncestorSize did not return", name)
			}
		}
	})
}
