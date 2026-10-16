// Package bmff holds small ISO base media file format (ISO/IEC 14496-12) box
// helpers shared by the ISO BMFF containers (HEIF, AVIF, CR3).
package bmff

// Adapted from libavif src/stream.c:avifRWStreamFinishBox, Copyright 2019 Joe Drago, SPDX-License-Identifier: BSD-2-Clause
// Changes: libavif writes the 32-bit size of a box it is building once the payload is complete and refuses a box above UINT32_MAX; Close does the same for a box that was read (an open-ended one), keeps the largesize form of a 64-bit box and switches to the 64-bit header above 4 GiB (task #359).

import (
	"encoding/binary"
	"math"
)

// OpenEnded reports whether the box that starts at box[0] declares that it
// extends to the end of its container.
//
// ISO/IEC 14496-12 §4.2 (Object structure, Box): "if size is 0, then this box
// shall be in a top-level container [sic: the last box], and extends to the
// end of the containing structure" (in the file, the last box). The 64-bit
// form has no such value; a largesize of 0 is malformed, but the box readers
// of this module (heif parseHEIFBoxHeader, cr3 parseCR3BoxHeader) take it as
// extending to the end as well, so it is treated alike here: a reader and the
// writer that appends boxes must agree on the extent of every child.
func OpenEnded(box []byte) bool {
	if len(box) < 8 {
		return false
	}
	switch binary.BigEndian.Uint32(box) {
	case 0:
		return true
	case 1:
		return len(box) >= 16 && binary.BigEndian.Uint64(box[8:]) == 0
	}
	return false
}

// Close returns box with an explicit size when it is open-ended (OpenEnded),
// and box itself otherwise. box must be exactly one complete box: its extent
// is len(box), the bytes up to the end of the container.
//
// ISO/IEC 14496-12 §4.2: size 0 stops being true when a box is appended after
// it, because a reader then takes the appended box as part of the open-ended
// one (the appended metadata is lost, found by FuzzCR3Inject and
// FuzzHEIFInject, tasks #347 and #359). The box is copied, never edited in
// place: it usually aliases the caller's file buffer. A box too long for the
// 32-bit size field gets the 64-bit header (size 1 + largesize), 8 bytes
// longer than the input.
//
// Reference: libavif src/stream.c avifRWStreamFinishBox, lines 429-441
// (BSD-2, ab37e294) writes the 32-bit size of a box once its payload is
// complete and refuses a box above UINT32_MAX; this helper does the same for a
// box that was read and, above 4 GiB, switches to the 64-bit header instead.
func Close(box []byte) []byte { return closeWithLimit(box, math.MaxUint32) }

// closeWithLimit is Close with the largest value of the 32-bit size field as
// a parameter, so the 64-bit conversion is testable without a 4 GiB buffer.
func closeWithLimit(box []byte, limit uint64) []byte {
	if !OpenEnded(box) {
		return box
	}
	if binary.BigEndian.Uint32(box) == 1 {
		out := append([]byte(nil), box...)
		binary.BigEndian.PutUint64(out[8:], uint64(len(out)))
		return out
	}
	if uint64(len(box)) <= limit {
		out := append([]byte(nil), box...)
		binary.BigEndian.PutUint32(out, uint32(len(out))) //nolint:gosec // G115: len(out) <= limit <= MaxUint32
		return out
	}
	// The 32-bit field cannot hold the length: switch to the 64-bit header.
	out := make([]byte, 16, len(box)+8)
	binary.BigEndian.PutUint32(out, 1)
	copy(out[4:8], box[4:8])
	out = append(out, box[8:]...)
	binary.BigEndian.PutUint64(out[8:], uint64(len(out)))
	return out
}

// CloseInPlace gives box an explicit size, editing it in place, when it is
// open-ended and the size fits its header (32 bits, or the 64-bit field). It
// reports whether the box is explicit afterwards (true when it already was).
// Use it on a buffer the caller owns; use Close on a view of a shared buffer.
func CloseInPlace(box []byte) bool {
	if !OpenEnded(box) {
		return true
	}
	if binary.BigEndian.Uint32(box) == 1 {
		binary.BigEndian.PutUint64(box[8:], uint64(len(box)))
		return true
	}
	if uint64(len(box)) > math.MaxUint32 {
		return false
	}
	binary.BigEndian.PutUint32(box, uint32(len(box))) //nolint:gosec // G115: len(box) <= MaxUint32 checked above
	return true
}
