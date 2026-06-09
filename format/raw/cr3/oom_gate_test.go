package cr3

// oom_gate_test.go — regression gate for #140: uncapped io.ReadAll OOM.
//
// These tests verify that Extract and Inject reject inputs that exceed
// maxFileSize with ErrFileTooLarge, and that normal-sized inputs continue to
// be handled correctly even when maxFileSize is temporarily lowered.
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

// capBytesOOM is the small cap used by OOM-gate tests. Set to 256 so that:
//   - the OOM test can feed capBytesOOM+1 bytes to trigger ErrFileTooLarge,
//   - the positive-control test can use buildMinimalCR3 (~70 bytes) which fits
//     comfortably under the cap.
const capBytesOOM = 256

// setMaxFileSizeForTest temporarily replaces the package-level maxFileSize with
// cap and registers a t.Cleanup to restore the original value.  It must not be
// called from parallel sub-tests that share a package-level variable.
func setMaxFileSizeForTest(t *testing.T, cap int64) {
	t.Helper()
	orig := maxFileSize
	maxFileSize = cap
	t.Cleanup(func() { maxFileSize = orig })
}

// TestExtractFileTooLarge verifies that Extract returns ErrFileTooLarge when
// the input exceeds maxFileSize.
//
// Gate for #140 (cr3.Extract uncapped io.ReadAll).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	r := bytes.NewReader(make([]byte, capBytesOOM+1))
	_, _, _, err := Extract(r)
	if err == nil {
		t.Fatal("Extract: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Extract: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestInjectFileTooLarge verifies that Inject returns ErrFileTooLarge when
// the reader exceeds maxFileSize.
//
// Gate for #140 (cr3.Inject uncapped io.ReadAll).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	r := bytes.NewReader(make([]byte, capBytesOOM+1))
	// rawEXIF non-nil so the all-nil pass-through branch is not taken.
	err := Inject(r, io.Discard, []byte{0x49, 0x49, 0x2A, 0x00}, nil, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractFileTooLargePositiveControl verifies that a valid CR3 input
// smaller than the (temporarily lowered) maxFileSize is accepted (does not
// return ErrFileTooLarge).
//
// Gate for #140: normal-path regression guard.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLargePositiveControl(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	// Build a minimal CR3 — the buildMinimalCR3 helper is defined in cr3_test.go
	// within the same package (package cr3).
	data := buildMinimalCR3(minimalTIFF(), nil)
	// Sanity: the helper must produce a file smaller than capBytesOOM.
	if int64(len(data)) > capBytesOOM {
		t.Fatalf("test design error: minimal CR3 (%d bytes) exceeds capBytesOOM (%d)", len(data), capBytesOOM)
	}
	_, _, _, err := Extract(bytes.NewReader(data))
	if errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Extract positive control: unexpected ErrFileTooLarge for a small CR3 input")
	}
}
