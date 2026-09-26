package tiff

// relocate_extend.go — task #291 follow-up: on-demand extension of a
// metadata-prefix `base` buffer for the four manufacturer-specific write-path
// preprocessing steps (Sony SR2Private, Nikon PreviewIFD, Olympus
// ThumbnailImage-pointer, Panasonic RawDataOffset) whose own small internal
// structures are NOT captured by #289/#293's extent.go scanner: each lives
// one level deeper than a generic IFD/SubIFD/out-of-line-array walk reaches
// (an inline pointer into a manufacturer-private blob, or a pointer nested
// inside a MakerNote's own internal IFD). Fetching a few extra KB directly
// from the source, exactly when and where a relocator discovers it needs
// them, is simpler and safer than teaching the generic scanner every
// manufacturer's private conventions.

import (
	"fmt"
	"io"
)

// extendBase grows base to cover at least targetLen bytes, fetching ONLY the
// delta [len(base), targetLen) from r in a single Seek+ReadFull — never
// re-reading bytes base already holds. Returns base UNCHANGED (same slice,
// zero allocation) when it already covers targetLen; this is the common case
// once #289/#293's own scanner already captured enough, or the whole file is
// already resident (m.rawEXIFIsWholeFile), or a prior extendBase call in the
// same relocate pass already grew far enough.
//
// targetLen is clamped to fileLen (the true total source length): requesting
// past EOF grows to fileLen instead of failing, so the caller's own existing
// bounds checks (already present in every manufacturer-specific extraction
// function, written for the base-is-whole-file era) degrade gracefully on a
// malformed or unusually-shaped file exactly as they always have — extendBase
// only ever WIDENS what is available, never changes how a caller reacts to
// data that still turns out to be insufficient or absent.
//
// r must be seekable; its position after a successful call that actually grows
// base is unspecified — callers must not rely on r's position afterward. When
// base already covers targetLen (the common case), r is never touched, so a
// nil r is safe there.
func extendBase(base []byte, r io.ReadSeeker, fileLen uint64, targetLen uint64) ([]byte, error) {
	if targetLen > fileLen {
		targetLen = fileLen
	}
	if uint64(len(base)) >= targetLen {
		return base, nil
	}
	grown := make([]byte, targetLen)
	copy(grown, base)
	if _, err := r.Seek(int64(len(base)), io.SeekStart); err != nil {
		return nil, fmt.Errorf("tiff: seek to extend metadata range: %w", err)
	}
	if _, err := io.ReadFull(r, grown[len(base):]); err != nil {
		return nil, fmt.Errorf("tiff: read extended metadata range: %w", err)
	}
	return grown, nil
}
