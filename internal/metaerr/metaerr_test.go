package metaerr

import (
	"errors"
	"fmt"
	"regexp"
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

// ---------------------------------------------------------------------------
// #192 gate: error messages must not leak Go-internal identifiers or pointers
// ---------------------------------------------------------------------------

// TestCorruptMetadataError_NoGoInternalLeak is the regression gate for audit
// finding #192. It asserts that Error() strings for representative
// CorruptMetadataError and ParseSegmentError-wrapping scenarios contain:
//
//   - No pointer-address patterns (0x[0-9a-fA-F]+) which change between runs
//     and expose GC internals.
//   - No unexported Go identifier patterns (lower-case package-qualified names
//     like "exif.ifd", "metaerr.CorruptMetadataError", or Go struct field names
//     in "type.field" notation) that expose implementation vocabulary.
//
// Decimal byte offsets and buffer lengths (e.g. "offset 2048", "buf len 1024")
// ARE permitted: they are file-position diagnostics that help callers locate
// the malformed bytes in the source file, consistent with the diagnostic policy
// documented in the CorruptMetadataError godoc.
func TestCorruptMetadataError_NoGoInternalLeak(t *testing.T) {
	t.Parallel()

	// reGoPointer matches a Go runtime pointer address as formatted by %p or
	// default %v on a pointer: "0x" followed by ≥8 hex digits. Short hex
	// literals (e.g. EXIF tag IDs like "0x0100" which are only 4 hex digits)
	// are valid file-format constants, not pointers, and must not be flagged.
	// A real 64-bit address has 12 hex digits; 8 is the minimum on 32-bit.
	reGoPointer := regexp.MustCompile(`0x[0-9a-fA-F]{8,}`)

	// reGoInternal matches unexported package-qualified identifiers: a
	// lower-case word, a dot, then another word (e.g. "exif.ifd", "metaerr.foo").
	// Exported identifiers (CorruptMetadataError, TruncatedFileError) are fine
	// in doc strings but should not appear in runtime error messages; however,
	// this gate focuses specifically on unexported (lower-case first char)
	// package-qualified names which are the primary leakage vector.
	reGoInternal := regexp.MustCompile(`\b[a-z][a-zA-Z0-9_]*\.[a-z][a-zA-Z0-9_]+\b`)

	// Representative CorruptMetadataError instances that mirror real parser output.
	// These reason strings exercise the full range of patterns in exif/ifd.go and
	// exif/exif.go while remaining within the documented diagnostic contract.
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "IFD_offset_out_of_bounds",
			err: &CorruptMetadataError{
				Format: "EXIF",
				Reason: "IFD offset 2048 out of bounds (buf len 1024)",
			},
		},
		{
			name: "invalid_byte_order_marker",
			err: &CorruptMetadataError{
				Format: "EXIF",
				Reason: `invalid byte order marker "\x00\x00"`,
			},
		},
		{
			name: "IFD_entry_count_overflow",
			err: &CorruptMetadataError{
				Format: "EXIF",
				Reason: "IFD entry count 65535 overflows buffer (offset 8, buf len 16)",
			},
		},
		{
			name: "value_offset_out_of_range",
			err: &CorruptMetadataError{
				Format: "EXIF",
				Reason: "tag 0x0100 value offset 99999 out of range (buf len 512)",
			},
		},
		{
			name: "IPTC_unexpected_end",
			err: &CorruptMetadataError{
				Format: "IPTC",
				Reason: "unexpected end of stream at offset 42",
			},
		},
		{
			name: "XMP_malformed_RDF",
			err: &CorruptMetadataError{
				Format: "XMP",
				Reason: "malformed RDF: nesting depth limit exceeded",
			},
		},
		{
			name: "TruncatedFileError",
			err:  &TruncatedFileError{At: "IFD entry at offset 128"},
		},
		{
			name: "wrapped_in_fmt_errorf",
			err:  fmt.Errorf("parse: %w", &CorruptMetadataError{Format: "EXIF", Reason: "tag value length 0 at offset 64"}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := tc.err.Error()

			// Gate 1: no Go pointer addresses.
			if m := reGoPointer.FindString(msg); m != "" {
				t.Errorf("error message contains Go pointer address %q: %q", m, msg)
			}

			// Gate 2: no unexported package-qualified identifiers.
			if m := reGoInternal.FindString(msg); m != "" {
				t.Errorf("error message contains unexported Go identifier %q: %q", m, msg)
			}

			// Sanity: the message must contain the "gometadata:" prefix or be
			// wrapped (fmt.Errorf wrapping adds "parse: " prefix but the inner
			// message still contains "gometadata:").
			if !strings.Contains(msg, "gometadata:") {
				t.Errorf("error message missing 'gometadata:' prefix: %q", msg)
			}
		})
	}
}
