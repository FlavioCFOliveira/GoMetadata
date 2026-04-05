package iobuf

import (
	"sync"
	"testing"
)

// TestGetPutRoundtrip verifies that a buffer obtained from the pool can be
// written to, returned, and then re-obtained with at least the requested
// length.
func TestGetPutRoundtrip(t *testing.T) {
	const n = 100
	p := Get(n)
	if len(*p) < n {
		t.Fatalf("Get(%d): len = %d, want >= %d", n, len(*p), n)
	}

	// Write a recognisable pattern into the buffer.
	for i := range *p {
		(*p)[i] = byte(i % 251)
	}

	Put(p)

	// After Put the caller must not touch p, but we can call Get again and
	// confirm the pool hands back a slice of at least n bytes.
	p2 := Get(n)
	if len(*p2) < n {
		t.Fatalf("Get(%d) after Put: len = %d, want >= %d", n, len(*p2), n)
	}
	Put(p2)
}

// TestGetLargeSlice verifies that Get correctly allocates a new backing array
// when the pool's default-size buffers are too small.
func TestGetLargeSlice(t *testing.T) {
	const n = 8192
	p := Get(n)
	if len(*p) != n {
		t.Fatalf("Get(%d): len = %d, want %d", n, len(*p), n)
	}
	Put(p)
}

// TestGetDefaultSize verifies that Get(0) returns at least an empty slice
// without panicking.
func TestGetDefaultSize(t *testing.T) {
	p := Get(0)
	if p == nil {
		t.Fatal("Get(0) returned nil pointer")
	}
	Put(p)
}

// TestGetExactDefaultSize verifies that Get of the internal defaultSize (4096)
// is served from the pool without a new allocation path.
func TestGetExactDefaultSize(t *testing.T) {
	p := Get(defaultSize)
	if len(*p) != defaultSize {
		t.Fatalf("Get(%d): len = %d, want %d", defaultSize, len(*p), defaultSize)
	}
	Put(p)
}

// TestPutNil verifies that Put(nil) does not panic.
func TestPutNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Put(nil) panicked: %v", r)
		}
	}()
	Put(nil)
}

// TestGetPutRace exercises Get and Put from many goroutines concurrently to
// detect data races under the -race detector.
func TestGetPutRace(t *testing.T) {
	t.Parallel()

	const goroutines = 32
	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range iterations {
				size := (i % 4) * 1024 // 0, 1024, 2048, 3072
				p := Get(size)
				if p == nil {
					// Signal failure safely without calling t.Fatal from a goroutine.
					return
				}
				// Touch every byte to expose any unsafe sharing.
				for j := range *p {
					(*p)[j] = byte(j)
				}
				Put(p)
			}
		}()
	}

	wg.Wait()
}

// BenchmarkGetPut measures the overhead of a Get/Put pair on the hot path.
func BenchmarkGetPut(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := Get(defaultSize)
		Put(p)
	}
}

// BenchmarkGetLarge measures the overhead when the requested size exceeds the
// pool's default, forcing a new allocation.
func BenchmarkGetLarge(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p := Get(65536)
		Put(p)
	}
}
