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
