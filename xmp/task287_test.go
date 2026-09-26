package xmp

// task287_test.go — regression battery for task #287 (Sprint 44, Batch E):
// isNameTerminator's compare-chain was replaced with a 256-entry lookup
// table (nameTerminatorLUT) for performance. This file proves the table is
// byte-for-byte equivalent to the original predicate for every possible
// input, independent of any XML fixture, so a future edit to the table
// cannot silently narrow or widen the terminator set without failing here.

import "testing"

// oldIsNameTerminator287 is the pre-#287 compare-chain implementation of
// isNameTerminator, kept here only as the reference oracle for
// TestNameTerminatorLUTMatchesPredicate. It must never be used by production
// code; nameTerminatorLUT (rdf.go) is the single source of truth.
func oldIsNameTerminator287(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
		c == '>' || c == '/' || c == '=' || c == '<'
}

// TestNameTerminatorLUTMatchesPredicate proves nameTerminatorLUT (and, by
// extension, isNameTerminator, which is now a thin wrapper around it) agrees
// with the original eight-way branch predicate for every one of the 256
// possible byte values — not just the eight terminator bytes themselves, so
// a regression that flips an unrelated entry to true (over-matching) is
// caught just as reliably as one that drops a real terminator.
func TestNameTerminatorLUTMatchesPredicate(t *testing.T) {
	t.Parallel()
	for i := range 256 {
		c := byte(i)
		want := oldIsNameTerminator287(c)
		got := isNameTerminator(c)
		if got != want {
			t.Errorf("isNameTerminator(%#02x) = %v, want %v (old predicate)", c, got, want)
		}
		if lut := nameTerminatorLUT[c]; lut != want {
			t.Errorf("nameTerminatorLUT[%#02x] = %v, want %v (old predicate)", c, lut, want)
		}
	}
}

// TestNameTerminatorLUTExactSet documents and pins the exact terminator set
// by name, independent of the exhaustive loop above, so the specific bytes
// XML 1.0 §2.3 and #171's '<'-injection guard require are named explicitly
// in a failure message rather than only by numeric value.
func TestNameTerminatorLUTExactSet(t *testing.T) {
	t.Parallel()
	terminators := map[byte]string{
		' ':  "space",
		'\t': "tab",
		'\n': "line feed",
		'\r': "carriage return",
		'>':  "greater-than",
		'/':  "slash",
		'=':  "equals",
		'<':  "less-than (#171 XML-injection guard)",
	}
	for i := range 256 {
		c := byte(i)
		name, isTerminator := terminators[c]
		got := isNameTerminator(c)
		switch {
		case isTerminator && !got:
			t.Errorf("isNameTerminator(%#02x %s) = false, want true", c, name)
		case !isTerminator && got:
			t.Errorf("isNameTerminator(%#02x) = true, want false (not in the documented terminator set)", c)
		}
	}
}
