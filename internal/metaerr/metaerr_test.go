package metaerr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TruncatedFileError
// ---------------------------------------------------------------------------

func TestTruncatedFileErrorMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		at   string
		want string
	}{
		{"TIFF header", "gometadata: truncated file while reading TIFF header"},
		{"IFD entry", "gometadata: truncated file while reading IFD entry"},
		{"", "gometadata: truncated file while reading "},
	}
	for _, tc := range tests {
		e := &TruncatedFileError{At: tc.at}
		got := e.Error()
		if got != tc.want {
			t.Errorf("TruncatedFileError{At:%q}.Error() = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestTruncatedFileErrorContainsAt(t *testing.T) {
	t.Parallel()
	e := &TruncatedFileError{At: "GPS IFD"}
	if !strings.Contains(e.Error(), "GPS IFD") {
		t.Errorf("error message does not contain the At field: %q", e.Error())
	}
}

func TestTruncatedFileErrorIsPrefix(t *testing.T) {
	t.Parallel()
	e := &TruncatedFileError{At: "anything"}
	if !strings.HasPrefix(e.Error(), "gometadata:") {
		t.Errorf("error message missing 'gometadata:' prefix: %q", e.Error())
	}
}

// TestTruncatedFileErrorAsUnwrap verifies that errors.As correctly identifies
// a wrapped TruncatedFileError.
func TestTruncatedFileErrorAsUnwrap(t *testing.T) {
	t.Parallel()
	inner := &TruncatedFileError{At: "APP1 segment"}
	wrapped := fmt.Errorf("outer: %w", inner)

	var target *TruncatedFileError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As: expected to unwrap *TruncatedFileError, got false")
	}
	if target.At != "APP1 segment" {
		t.Errorf("unwrapped At = %q, want %q", target.At, "APP1 segment")
	}
}

// TestTruncatedFileErrorDirectErrors_As verifies errors.As on a direct (non-
// wrapped) value.
func TestTruncatedFileErrorDirectErrorsAs(t *testing.T) {
	t.Parallel()
	e := &TruncatedFileError{At: "direct"}
	var target *TruncatedFileError
	if !errors.As(e, &target) {
		t.Fatal("errors.As on direct *TruncatedFileError: expected true")
	}
}

// ---------------------------------------------------------------------------
// CorruptMetadataError
// ---------------------------------------------------------------------------

func TestCorruptMetadataErrorMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		format string
		reason string
		want   string
	}{
		{"EXIF", "bad IFD offset 99999", "gometadata: corrupt EXIF metadata: bad IFD offset 99999"},
		{"IPTC", "unexpected end of stream", "gometadata: corrupt IPTC metadata: unexpected end of stream"},
		{"XMP", "malformed RDF", "gometadata: corrupt XMP metadata: malformed RDF"},
		{"", "", "gometadata: corrupt  metadata: "},
	}
	for _, tc := range tests {
		e := &CorruptMetadataError{Format: tc.format, Reason: tc.reason}
		got := e.Error()
		if got != tc.want {
			t.Errorf("CorruptMetadataError{%q,%q}.Error() = %q, want %q",
				tc.format, tc.reason, got, tc.want)
		}
	}
}

func TestCorruptMetadataErrorContainsFormatAndReason(t *testing.T) {
	t.Parallel()
	e := &CorruptMetadataError{Format: "TIFF", Reason: "negative count"}
	msg := e.Error()
	if !strings.Contains(msg, "TIFF") {
		t.Errorf("error message does not contain format: %q", msg)
	}
	if !strings.Contains(msg, "negative count") {
		t.Errorf("error message does not contain reason: %q", msg)
	}
}

// TestCorruptMetadataErrorAsUnwrap verifies errors.As unwrapping for
// CorruptMetadataError.
func TestCorruptMetadataErrorAsUnwrap(t *testing.T) {
	t.Parallel()
	inner := &CorruptMetadataError{Format: "XMP", Reason: "depth limit exceeded"}
	wrapped := fmt.Errorf("parse failed: %w", inner)

	var target *CorruptMetadataError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As: expected to unwrap *CorruptMetadataError, got false")
	}
	if target.Format != "XMP" {
		t.Errorf("unwrapped Format = %q, want %q", target.Format, "XMP")
	}
	if target.Reason != "depth limit exceeded" {
		t.Errorf("unwrapped Reason = %q, want %q", target.Reason, "depth limit exceeded")
	}
}

func TestCorruptMetadataErrorDirectErrorsAs(t *testing.T) {
	t.Parallel()
	e := &CorruptMetadataError{Format: "IPTC", Reason: "direct"}
	var target *CorruptMetadataError
	if !errors.As(e, &target) {
		t.Fatal("errors.As on direct *CorruptMetadataError: expected true")
	}
}

// TestErrorsAreDistinct confirms that a TruncatedFileError cannot satisfy
// errors.As for *CorruptMetadataError, and vice versa.
func TestErrorsAreDistinct(t *testing.T) {
	t.Parallel()
	trunc := &TruncatedFileError{At: "x"}
	var corrupt *CorruptMetadataError
	if errors.As(trunc, &corrupt) {
		t.Error("TruncatedFileError erroneously matched as *CorruptMetadataError")
	}

	corr := &CorruptMetadataError{Format: "EXIF", Reason: "x"}
	var tr *TruncatedFileError
	if errors.As(corr, &tr) {
		t.Error("CorruptMetadataError erroneously matched as *TruncatedFileError")
	}
}
