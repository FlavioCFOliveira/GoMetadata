package tiff

// oom_gate_test.go — regression gate for #140: uncapped io.ReadAll OOM.
//
// These tests verify that Extract and Inject reject inputs that exceed
// maxFileSize with ErrFileTooLarge, and that normal-sized inputs continue to
// parse correctly even when maxFileSize is temporarily lowered.
//
// The tests lower maxFileSize to a tiny value (capBytes) for the OOM path and
// restore it via t.Cleanup so the production default (256 MiB) is never changed
// across the test suite.  No 256 MiB allocation is ever performed.

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// capBytes is the small cap used by OOM-gate tests. Any input longer than this
// value will trigger ErrFileTooLarge when maxFileSize is set to capBytes.
// 64 bytes is well below the minimum valid TIFF header (8 bytes) threshold
// while being large enough to build a valid minimal TIFF for positive-control
// tests.
const capBytes = 64

// setMaxFileSizeForTest temporarily replaces the package-level maxFileSize with
// cap and registers a t.Cleanup to restore the original value.  It must not be
// called from parallel sub-tests that share a package-level variable.
func setMaxFileSizeForTest(t *testing.T, cap int64) {
	t.Helper()
	orig := maxFileSize
	maxFileSize = cap
	t.Cleanup(func() { maxFileSize = orig })
}

// oversizedReader returns an io.ReadSeeker that claims to contain n bytes of
// zero-filled content.  Used to feed exactly cap+1 bytes without allocating
// cap+1 bytes in the test process itself.
func oversizedReader(n int) io.ReadSeeker {
	return bytes.NewReader(make([]byte, n))
}

// TestExtractFileTooLarge verifies that Extract returns ErrFileTooLarge when
// the input exceeds maxFileSize.
//
// Gate for #140 (tiff.Extract uncapped io.ReadAll).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytes)

	// Feed capBytes+1 bytes — guaranteed to exceed the cap.
	r := oversizedReader(capBytes + 1)
	_, _, _, err := Extract(r)
	if err == nil {
		t.Fatal("Extract: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Extract: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestInjectFileTooLarge verifies that Inject returns ErrFileTooLarge when
// rawEXIF is nil (forcing the full-file read path) and the reader exceeds
// maxFileSize.
//
// Gate for #140 (tiff.Inject uncapped io.ReadAll when rawEXIF==nil).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytes)

	// Feed capBytes+1 bytes with rawEXIF=nil to trigger the full-file read path.
	r := oversizedReader(capBytes + 1)
	// rawIPTC non-nil so the pass-through branch is not taken.
	err := Inject(r, io.Discard, nil, []byte{0x1C}, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractFileTooLargePositiveControl verifies that a valid TIFF input
// smaller than the (temporarily lowered) maxFileSize still parses correctly.
//
// Gate for #140: normal-path regression guard.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLargePositiveControl(t *testing.T) {
	setMaxFileSizeForTest(t, capBytes)

	// Build a minimal valid little-endian TIFF (14 bytes) — well under capBytes.
	data := make([]byte, 14)
	data[0], data[1] = 'I', 'I'
	data[2], data[3] = 0x2A, 0x00
	data[4], data[5], data[6], data[7] = 8, 0, 0, 0 // IFD0 at offset 8
	// IFD0: count=0, next=0
	_, _, _, err := Extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Extract positive control: unexpected error: %v", err)
	}
}
