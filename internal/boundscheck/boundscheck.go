// Package boundscheck provides a single, shared overflow-safe bounds check
// used by every package that scans untrusted binary offsets/lengths read
// directly from image bytes (currently exif and format/tiff).
//
// Security audit finding (2026-09-26): several call sites across both
// packages computed `end := off + width` and compared `end > n` as a raw,
// unchecked uint64 addition. When off and width are both read directly from
// untrusted file bytes — in particular a BigTIFF LONG8 (8-byte) field, which
// can hold any value up to MaxUint64 — a crafted pair (e.g. off=MaxUint64-1,
// width=10) wraps the addition to a small value (8), which then incorrectly
// compares as "in bounds" against any n. The resulting out-of-range offset
// then panics on a subsequent slice expression (`b[off:end]`) with a Go
// runtime "slice bounds out of range" error — a crash reachable from
// untrusted input via the public Read/Write API. Package boundscheck exists
// so this check is written, tested, and audited exactly once.
package boundscheck

// Fits reports whether a width-byte value starting at off lies entirely
// within a buffer of length n, computed without any risk of overflow: off
// and width may each independently be as large as MaxUint64 when derived
// from untrusted file content (e.g. a BigTIFF LONG8 offset or count field),
// so a naive `off+width > n` check can itself overflow and wrap around to a
// small value, incorrectly reporting a huge, out-of-bounds offset as
// "fits".
func Fits(off, width, n uint64) bool {
	if off > n {
		return false
	}
	return width <= n-off
}
