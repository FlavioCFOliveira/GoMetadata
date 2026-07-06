package iptc

// dataset_value_too_large_test.go — regression test for the Encode
// extended-length overflow guard (ErrDatasetValueTooLarge).
//
// Encode's extended-length branch (IIM 4.2 §1.6.2) writes a Value's length
// into a 4-byte big-endian field, which can represent at most
// maxDatasetValueLen (math.MaxUint32 in production) bytes. Without a bound
// check, a Dataset.Value longer than that would have its length silently
// truncated to its low 32 bits while buf.Write still emitted every byte of
// the value — a corrupt, desynchronised stream whose declared length
// disagrees with its actual content. This is unreachable via Parse -> Encode
// (Parse's aggregate maxIPTCTotalBytes / maxIPTCDatasets caps keep every
// Value far below the limit) but is reachable via direct construction of the
// public Dataset struct.
//
// These tests lower the package-level maxDatasetValueLen — declared as a
// test-overridable var specifically so this guard can be exercised without
// allocating a multi-gigabyte byte slice, mirroring the maxFileSize pattern
// used elsewhere in this project (root package, format/webp, format/jpeg) —
// and restore the production default via t.Cleanup. No multi-gigabyte
// allocation is ever performed.

import (
	"errors"
	"testing"
)

// setMaxDatasetValueLenForTest temporarily replaces the package-level
// maxDatasetValueLen with limit and registers a t.Cleanup to restore the
// production default (math.MaxUint32). It must not be called from parallel
// sub-tests that share the package-level variable.
func setMaxDatasetValueLenForTest(t *testing.T, limit uint64) {
	t.Helper()
	orig := maxDatasetValueLen
	maxDatasetValueLen = limit
	t.Cleanup(func() { maxDatasetValueLen = orig })
}

// TestEncodeDatasetValueTooLarge verifies that Encode returns
// ErrDatasetValueTooLarge — and emits no output — when a Dataset.Value is
// longer than the extended-length field can represent, instead of silently
// wrapping the length field while still writing every value byte (which
// would desynchronise any reader at the next dataset marker).
//
//nolint:paralleltest // sets package-level maxDatasetValueLen; must not run in parallel with sibling tests in this file
func TestEncodeDatasetValueTooLarge(t *testing.T) {
	setMaxDatasetValueLenForTest(t, 64)

	i := &IPTC{}
	i.Records[2] = []Dataset{
		{Record: 2, DataSet: DS2Caption, Value: make([]byte, 65)},
	}

	out, err := Encode(i)
	if !errors.Is(err, ErrDatasetValueTooLarge) {
		t.Fatalf("Encode with oversized Value: err = %v, want ErrDatasetValueTooLarge", err)
	}
	if out != nil {
		t.Errorf("Encode with oversized Value: output = %v, want nil (no corrupt stream emitted)", out)
	}
}

// TestEncodeDatasetValueAtLimitSucceeds is the positive control: a
// Dataset.Value exactly at the (lowered) limit must still encode
// successfully via the extended-length path and round-trip through Parse,
// proving the guard's boundary is "length > limit", not "length >= limit".
//
//nolint:paralleltest // sets package-level maxDatasetValueLen; must not run in parallel with sibling tests in this file
func TestEncodeDatasetValueAtLimitSucceeds(t *testing.T) {
	setMaxDatasetValueLenForTest(t, 64)

	value := make([]byte, 64)
	for j := range value {
		value[j] = byte(j)
	}

	i := &IPTC{}
	i.Records[2] = []Dataset{
		{Record: 2, DataSet: DS2Caption, Value: value},
	}

	encoded, err := Encode(i)
	if err != nil {
		t.Fatalf("Encode with Value at limit: unexpected error: %v", err)
	}

	i2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse (round-trip) after Encode at limit: %v", err)
	}
	datasets := i2.Records[2]
	if len(datasets) == 0 {
		t.Fatal("Records[2] is empty after round-trip")
	}
	got := datasets[len(datasets)-1].Value
	if len(got) != len(value) {
		t.Fatalf("round-trip value length = %d, want %d", len(got), len(value))
	}
	for j, b := range got {
		if b != value[j] {
			t.Fatalf("value mismatch at byte %d: got 0x%02X, want 0x%02X", j, b, value[j])
		}
	}
}
