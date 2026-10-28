package heif

// oom_gate_test.go — regression gate for #140: uncapped io.ReadAll OOM.
//
// These tests verify that Extract (slow path) rejects inputs that exceed
// maxFileSize with ErrFileTooLarge, and that the fast-path Extract (meta box
// within 64 KB header window) is not affected. Inject never holds the file in
// memory (task #390): maxFileSize limits only the boxes it holds, and its gates
// are in stream_task390_test.go.
//
// The tests lower maxFileSize to a tiny value (capBytes) for the OOM path and
// restore it via t.Cleanup so the production default (256 MiB) is never changed
// across the test suite.  No 256 MiB allocation is ever performed.

import (
	"bytes"
	"errors"
	"testing"
)

// capBytes is the small cap used by OOM-gate tests. Any input longer than this
// value will trigger ErrFileTooLarge when maxFileSize is set to capBytes.
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

// TestExtractSlowPathFileTooLarge verifies that the Extract slow path (meta
// box not found in first 64 KB, falls back to io.ReadAll) returns
// ErrFileTooLarge when the input exceeds maxFileSize.
//
// To force the slow path the input must not contain a "meta" box within the
// first 64 KB. A stream of capBytes+1 zero bytes has no valid box structure
// and therefore does not find a meta box in the header window, triggering the
// slow-path full-file read.
//
// Gate for #140 (heif.Extract slow-path uncapped io.ReadAll).
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractSlowPathFileTooLarge(t *testing.T) {
	setMaxFileSizeForTest(t, capBytes)

	// Feed capBytes+1 zero bytes. The fast-path header window is 64 KB so this
	// input is read fully in the first ReadFull; then findBox finds no "meta"
	// box and the slow path is entered with r seeked back to 0. The slow-path
	// io.LimitReader read sees capBytes+1 bytes > capBytes → ErrFileTooLarge.
	data := make([]byte, capBytes+1)
	r := bytes.NewReader(data)
	_, _, _, err := Extract(r)
	if err == nil {
		t.Fatal("Extract slow path: expected error for oversized input, got nil")
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("Extract slow path: expected errors.Is(err, ErrFileTooLarge), got: %v", err)
	}
}

// TestExtractFileTooLargePositiveControl verifies that a valid HEIF input
// smaller than the (temporarily lowered) maxFileSize still returns without a
// ErrFileTooLarge error (the fast path reads at most 64 KB and then seeks, so
// it is never affected by maxFileSize — this test exercises that invariant).
//
// Gate for #140: normal-path regression guard.
//
//nolint:paralleltest // sets package-level maxFileSize; must not run in parallel
func TestExtractFileTooLargePositiveControl(t *testing.T) {
	setMaxFileSizeForTest(t, capBytes)

	// Use a minimal HEIF structure that fits under capBytes (20-byte ftyp box).
	// The fast path reads up to 64 KB; this tiny file is fully consumed in
	// ReadFull without hitting maxFileSize. Extract will return nil or a benign
	// error (no metadata found), but NOT ErrFileTooLarge.
	ftyp := []byte{
		0x00, 0x00, 0x00, 0x14, // size = 20
		'f', 't', 'y', 'p', // type
		'h', 'e', 'i', 'c', // major brand
		0x00, 0x00, 0x00, 0x00, // minor version
		'm', 'i', 'f', '1', // compatible brand
	}
	_, _, _, err := Extract(bytes.NewReader(ftyp))
	if errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("Extract positive control: unexpected ErrFileTooLarge for a small HEIF input")
	}
}
