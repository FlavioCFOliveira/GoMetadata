package webp

// oom_gate_test.go — regression gate for #140: uncapped io.ReadAll OOM.
//
// These tests verify that Inject rejects inputs that exceed maxFileSize with
// ErrFileTooLarge.  The Extract path is not affected because it reads chunks
// individually (each capped at maxWebPChunkSize).
//
// The tests lower maxFileSize to a tiny value (capBytesOOM) for the OOM path
// and restore it via t.Cleanup so the production default (256 MiB) is never
// changed across the test suite.  No 256 MiB allocation is ever performed.

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// capBytesOOM is the small cap used by OOM-gate tests.
const capBytesOOM = 64

// setMaxFileSizeForTest temporarily replaces the package-level maxFileSize with
// cap and registers a t.Cleanup to restore the original value.  It must not be
// called from parallel sub-tests that share a package-level variable.
func setMaxFileSizeForTest(t *testing.T, cap int64) {
	t.Helper()
	orig := maxFileSize
	maxFileSize = cap
	t.Cleanup(func() { maxFileSize = orig })
}

// TestInjectFileTooLarge verifies that Inject returns ErrFileTooLarge when
// the reader exceeds maxFileSize.
//
// Gate for #140 (webp.Inject uncapped io.ReadAll).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	r := bytes.NewReader(make([]byte, capBytesOOM+1))
	// rawEXIF non-nil so the pass-through branch is not taken before the read.
	err := Inject(r, io.Discard, []byte{0x00}, nil, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// onlyReader hides every method except Read, mirroring
// internal/iobuf.onlyReader.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) } //nolint:wrapcheck // test helper: deliberate pass-through, mirrors internal/iobuf's onlyReader

// seekStartOnlyReader allows Seek(0, io.SeekStart) — the rewind Inject itself
// performs before delegating to iobuf.ReadAll — but fails every other Seek
// call. This forces iobuf.ReadAll's non-seekable fallback path (task #230)
// without failing Inject's own unconditional leading rewind.
type seekStartOnlyReader struct{ onlyReader }

func (seekStartOnlyReader) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekStart {
		return 0, nil
	}
	return 0, errors.New("seek not supported beyond initial rewind")
}

// TestInjectFileTooLargeNonSeekable is the non-seekable counterpart of
// TestInjectFileTooLarge (task #230): webp.Inject now reads its input via
// iobuf.ReadAll, which falls back to a bounded io.ReadAll(io.LimitReader(...))
// when Seek fails. This proves that fallback still enforces maxFileSize
// (ErrFileTooLarge) for a reader that cannot report its size up front.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLargeNonSeekable(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	r := seekStartOnlyReader{onlyReader{bytes.NewReader(make([]byte, capBytesOOM+1))}}
	err := Inject(r, io.Discard, []byte{0x00}, nil, nil, true)
	if err == nil {
		t.Fatal("Inject (non-seekable): expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject (non-seekable): expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestInjectFileTooLargeNonSeekableUnderLimit is the positive control for
// TestInjectFileTooLargeNonSeekable: a non-seekable input under the size cap
// must not be rejected by the size guard itself.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLargeNonSeekableUnderLimit(t *testing.T) {
	data := buildWebP(nil, nil, 0, 0, 0)
	setMaxFileSizeForTest(t, int64(len(data)+1))

	r := seekStartOnlyReader{onlyReader{bytes.NewReader(data)}}
	err := Inject(r, io.Discard, []byte{0x49, 0x49, 0x2A, 0x00}, nil, nil, true)
	if errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Inject (non-seekable, under limit): unexpected ErrFileTooLarge")
	}
}
