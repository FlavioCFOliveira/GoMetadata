package gometadata

// write_oom_test.go — regression gate for security audit FIX 3
// (CWE-770/400: unbounded io.ReadAll in the six TIFF-family write paths).
//
// Root cause: writeTIFF, writeTIFFCR2, writeTIFFARW, writeTIFFORF,
// writeTIFFRW2, and writeTIFFNEF each fall back to a bare io.ReadAll(r) when
// m.rawEXIF is nil — the documented NewMetadata(fmtID) + Write(r, w, m)
// pattern for callers who did not first Read an existing file. #140 already
// capped every io.ReadAll in the format/* packages (format/tiff,
// format/heif, ...) with io.LimitReader(r, maxFileSize+1); these six
// root-package call sites were missed by that fix.
//
// These tests exercise the REAL production maxFileSize threshold (256 MiB),
// not a lowered test override, using a synthetic io.ReadSeeker that never
// allocates a buffer proportional to the declared stream length — only the
// small header and a few counters. This proves the guard without the test
// process itself needing to hold 256+ MiB of source data (io.ReadAll's
// destination buffer inside readAllCapped is bounded to maxFileSize+1 bytes
// by construction, which is the exact behaviour under test).

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// zeroFillReadSeeker implements io.ReadSeeker over a virtual stream of
// `total` bytes: the first len(header) bytes come from header verbatim, the
// remainder are zero-filled. No buffer of size `total` is ever allocated —
// Read synthesises zero bytes on demand — so the reader itself is
// zero-allocating regardless of how large `total` is.
type zeroFillReadSeeker struct {
	header []byte
	total  int64
	pos    int64
}

// newZeroFillReadSeeker returns a zeroFillReadSeeker presenting header
// followed by zero-fill bytes up to a virtual length of total. header must be
// no longer than total.
func newZeroFillReadSeeker(header []byte, total int64) *zeroFillReadSeeker {
	return &zeroFillReadSeeker{header: header, total: total}
}

func (z *zeroFillReadSeeker) Read(p []byte) (int, error) {
	if z.pos >= z.total {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && z.pos < z.total {
		if z.pos < int64(len(z.header)) {
			p[n] = z.header[z.pos]
		} else {
			p[n] = 0
		}
		n++
		z.pos++
	}
	return n, nil
}

func (z *zeroFillReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var newPos int64
	switch whence {
	case io.SeekStart:
		newPos = offset
	case io.SeekCurrent:
		newPos = z.pos + offset
	case io.SeekEnd:
		newPos = z.total + offset
	default:
		return 0, fmt.Errorf("zeroFillReadSeeker: invalid whence %d", whence)
	}
	if newPos < 0 {
		return 0, errors.New("zeroFillReadSeeker: negative resulting position")
	}
	z.pos = newPos
	return newPos, nil
}

// buildMinimalMakeTIFF returns a minimal little-endian classic TIFF whose
// IFD0 carries a single out-of-line TagMake (0x010F) entry with the given
// ASCII value. Used to synthesise ARW ("SONY") and NEF ("Nikon") headers
// small enough to stay well within format.Detect's bounded scan window
// (tiffScanSize = 1560 bytes; see format/detect.go) while still being
// classified correctly by refineTIFFVariant/mapMakeToFormat.
func buildMinimalMakeTIFF(makeStr string) []byte {
	order := binary.LittleEndian
	makeBytes := append([]byte(makeStr), 0x00) // NUL-terminated, trimmed by mapMakeToFormat
	const headerFixed = 8 + 2 + 12 + 4         // header + ifd_count + 1 entry + next_ifd
	makeOff := headerFixed
	buf := make([]byte, makeOff+len(makeBytes))

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A)
	order.PutUint32(buf[4:], 8) // IFD0 at offset 8

	order.PutUint16(buf[8:], 1)                       // 1 entry
	order.PutUint16(buf[10:], 0x010F)                 // TagMake
	order.PutUint16(buf[12:], 2)                      // TypeASCII
	order.PutUint32(buf[14:], uint32(len(makeBytes))) //nolint:gosec // G115: test helper, len(makeBytes) is a small constant
	order.PutUint32(buf[18:], uint32(makeOff))        // makeOff is derived from a small compile-time constant
	order.PutUint32(buf[22:], 0)                      // next IFD = 0
	copy(buf[makeOff:], makeBytes)
	return buf
}

// TestWriteTIFFFamilyRejectsOversizedSource is the regression gate for
// security audit FIX 3 (CWE-770/400).
//
// For each of the six TIFF-family write paths, a *Metadata is constructed via
// NewMetadata(fmtID) — so m.rawEXIF is nil, forcing the vulnerable full-file
// read branch — and Write is called with a source stream that presents a
// minimal valid header for that format followed by well over maxFileSize
// (256 MiB) of filler. Before the fix, Write would buffer the entire stream
// (or hang indefinitely against a truly infinite reader) instead of returning
// promptly with ErrFileTooLarge.
func TestWriteTIFFFamilyRejectsOversizedSource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fmtID  format.FormatID
		header func() []byte
	}{
		{"TIFF", format.FormatTIFF, minimalTIFFPayload},
		{"CR2", format.FormatCR2, func() []byte {
			return buildTIFFWithStrip(binary.LittleEndian, false, nil, true /* canonMarker */)
		}},
		{"ARW", format.FormatARW, func() []byte { return buildMinimalMakeTIFF("SONY") }},
		{"ORF", format.FormatORF, buildMinimalORF},
		{"RW2", format.FormatRW2, buildMinimalRW2},
		{"NEF", format.FormatNEF, func() []byte { return buildMinimalMakeTIFF("Nikon") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header := tc.header()

			// Sanity: confirm format.Detect classifies the crafted header as
			// expected before wrapping it in the oversized stream — otherwise
			// this test would silently exercise the wrong write path and prove
			// nothing about the intended function.
			gotFmt, err := format.Detect(bytes.NewReader(header))
			if err != nil {
				t.Fatalf("format.Detect on header: %v", err)
			}
			if gotFmt != tc.fmtID {
				t.Fatalf("format.Detect(header) = %v, want %v (test setup invalid)", gotFmt, tc.fmtID)
			}

			m := NewMetadata(tc.fmtID)

			// Present the real production maxFileSize (256 MiB) plus 1 MiB of
			// filler — strictly larger than the cap, without lowering it for
			// the test.
			r := newZeroFillReadSeeker(header, maxFileSize+1<<20)

			writeErr := Write(r, io.Discard, m)
			if writeErr == nil {
				t.Fatalf("%s: Write: expected an error for a %d-byte source, got nil", tc.name, maxFileSize+1<<20)
			}
			if !errors.Is(writeErr, ErrFileTooLarge) {
				t.Errorf("%s: Write: expected errors.Is(err, ErrFileTooLarge), got: %v", tc.name, writeErr)
			}
		})
	}
}

// TestWriteTIFFFamilyPositiveControlSmallSource verifies that a source stream
// well under maxFileSize continues to Write successfully for each TIFF-family
// format, using the same zero-allocating reader machinery as the OOM gate
// above. This is the normal-path regression guard for security audit FIX 3:
// it proves readAllCapped's cap check does not reject legitimate small files.
func TestWriteTIFFFamilyPositiveControlSmallSource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fmtID  format.FormatID
		header func() []byte
	}{
		{"TIFF", format.FormatTIFF, minimalTIFFPayload},
		{"CR2", format.FormatCR2, func() []byte {
			return buildTIFFWithStrip(binary.LittleEndian, false, nil, true /* canonMarker */)
		}},
		{"ARW", format.FormatARW, func() []byte { return buildMinimalMakeTIFF("SONY") }},
		{"ORF", format.FormatORF, buildMinimalORF},
		{"RW2", format.FormatRW2, buildMinimalRW2},
		{"NEF", format.FormatNEF, func() []byte { return buildMinimalMakeTIFF("Nikon") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			header := tc.header()
			m := NewMetadata(tc.fmtID)

			var out bytes.Buffer
			if err := Write(bytes.NewReader(header), &out, m); err != nil {
				t.Fatalf("%s: Write: unexpected error for a %d-byte source well under maxFileSize: %v",
					tc.name, len(header), err)
			}
			if out.Len() == 0 {
				t.Fatalf("%s: Write produced no output for a valid small source", tc.name)
			}
		})
	}
}
