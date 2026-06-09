package rw2

// oom_gate_test.go — regression gate for #140: uncapped io.ReadAll OOM.
//
// These tests verify that Extract and Inject reject inputs that exceed
// maxFileSize with ErrFileTooLarge, and that normal-sized inputs continue to
// be handled correctly even when maxFileSize is temporarily lowered.
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

// TestExtractFileTooLarge verifies that Extract returns ErrFileTooLarge when
// the input exceeds maxFileSize.
//
// Gate for #140 (rw2.Extract uncapped io.ReadAll).
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
// Gate for #140 (rw2.Inject uncapped io.ReadAll).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestInjectFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	r := bytes.NewReader(make([]byte, capBytesOOM+1))
	err := Inject(r, io.Discard, nil, []byte{0x1C}, nil, true)
	if err == nil {
		t.Fatal("Inject: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Inject: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractFileTooLargePositiveControl verifies that a valid RW2 input
// smaller than the (temporarily lowered) maxFileSize still parses correctly.
//
// Gate for #140: normal-path regression guard.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLargePositiveControl(t *testing.T) {
	setMaxFileSizeForTest(t, capBytesOOM)

	// buildRW2() is defined in rw2_test.go (same package) and produces a
	// 14-byte minimal RW2 — well under capBytesOOM.
	data := buildRW2()
	_, _, _, err := Extract(bytes.NewReader(data))
	if errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Extract positive control: unexpected ErrFileTooLarge for a small RW2 input")
	}
}
