package rw2

import (
	"bytes"
	"io"
	"testing"
)

func FuzzRW2Extract(f *testing.F) {
	// Seed: minimal RW2 with correct magic.
	f.Add(buildRW2())

	// Seed: empty input.
	f.Add([]byte{})

	// Seed: truncated header.
	f.Add(append(rw2Magic, 0x00))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input.
		_, _, _, _ = Extract(bytes.NewReader(data))
	})
}

// FuzzRW2Inject feeds arbitrary bytes as the source RW2 container and
// asserts that Inject never panics — it must return an error or write valid
// output, never crash. Unlike arw/cr2/dng/nef, RW2.Inject adds real logic on
// top of tiff.Inject: it validates the "IIU\x00" magic, patches bytes [2:4]
// to standard TIFF LE magic (0x2A 0x00) on a private copy, delegates, and
// then restores the RW2 magic in the output. This exact patch-and-restore
// path previously shipped an image-corrupting bug (see project memory:
// "ORF/RW2 write corruption fixed, commit e52dd8f" — recursive IFD GUID
// shift for ALL sub-IFDs), which is precisely the kind of regression a
// write-path fuzz target is meant to catch.
//
// Design: rawEXIF is passed as nil (not the fuzzed bytes). Inside
// tiff.Inject, rawEXIF == nil means "read the base TIFF from r" — i.e. the
// RW2-patched copy of data that THIS package produces — so the fuzzer's
// mutations flow through rw2.Inject's own magic-patch step before reaching
// relocateTIFF. This is also the realistic call shape gometadata.Write
// exercises when only IPTC/XMP change (see project memory:
// "TIFF/DNG write: EXIF must be nil for rawEXIF base to be the full file").
// Passing data as rawEXIF instead (as FuzzTIFFInject does) would bypass
// rw2.Inject's magic patch entirely, since a non-nil rawEXIF short-circuits
// the read of r — that would test tiff.Inject a second time but never touch
// the RW2-specific code this target exists to cover.
//
// preserveUnknownSegments is fixed at true, matching the tiff/webp/heif/png/
// jpeg Inject fuzzers (task #258).
func FuzzRW2Inject(f *testing.F) {
	// Seed 1: minimal RW2 with correct "IIU\x00" magic and a 0-entry IFD0.
	f.Add(buildRW2())

	// Seed 2: empty input — exercises the pre-magic-check read path
	// (io.ReadAll on an empty reader succeeds with 0 bytes, then the
	// bytes.HasPrefix magic check fails).
	f.Add([]byte{})

	// Seed 3: truncated magic (not a full 4-byte prefix).
	f.Add(append(append([]byte{}, rw2Magic...), 0x00))

	// Seed 4: input with no RW2 magic at all — exercises ErrInvalidMagic.
	f.Add([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07})

	// Seed 5: RW2 carrying IPTC and XMP in IFD0 — exercises IFD entry
	// traversal, through the magic-patch, with existing out-of-line values.
	f.Add(buildRW2WithIPTCAndXMP(
		[]byte("fuzz-rw2-iptc-seed"),
		[]byte("<xmpmeta rw2='1'/>"),
	))

	// Fixed metadata payloads for rawIPTC/rawXMP: short and constant so that
	// Inject reaches relocateTIFF on every iteration once the magic and
	// length gates are satisfied by the fuzzer-controlled data.
	rawIPTC := []byte("fuzz-rw2-iptc-data")
	rawXMP := []byte("<xmpmeta/>")

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic regardless of input. Inject must return an error or
		// write valid output — a panic is always a bug in the write path.
		err := Inject(bytes.NewReader(data), io.Discard, nil, rawIPTC, rawXMP, true)
		_ = err
	})
}
