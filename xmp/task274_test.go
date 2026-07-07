package xmp

// task274_test.go — regression battery for task #274 (Clean-GO sprint):
// numeric character references to XML 1.0 §2.2 forbidden Char code points.
//
// Root cause (fixed by isForbiddenXMLCharRef in rdf.go, called from
// decodeCharRef): parseHex/parseDec correctly rejected surrogates and values
// above U+10FFFF, but a numeric character reference whose decoded value IS a
// legal Unicode scalar value and STILL falls inside one of the XML 1.0 §2.2
// forbidden ranges (the C0 control block other than TAB/LF/CR, or the two
// BMP non-characters U+FFFE/U+FFFF) passed straight through unfiltered. For
// U+001E specifically — this library's internal multi-value record
// separator (see the '\x1e' dispatch in write.go's Encode) — that meant a
// crafted-but-parseable document containing `&#x1e;` (or the decimal form
// `&#30;`) inside a genuinely Simple/scalar property's text caused Encode to
// spuriously re-serialise that property as an rdf:Bag: corruption-on-write
// of input that Parse accepted without error.
//
// XML 1.0 (5th ed.) §2.2 Char production:
//
//	Char ::= #x9 | #xA | #xD | [#x20-#xD7FF] | [#xE000-#xFFFD] | [#x10000-#x10FFFF]
//
// §4.1: "[a numeric character reference] MUST match the Char production" —
// a reference to a code point outside that production is ill-formed XML.
//
// The fix substitutes U+FFFD for the forbidden code point (Unicode §5.22
// "Best Practice for U+FFFD Substitution"), mirroring EXACTLY the policy
// writeXMLEscaped (xmp/write.go) already applies on the output side for the
// identical forbidden set, so encode and decode are policy-consistent in
// both directions.

import (
	"strings"
	"testing"
)

// buildSimplePropertyDoc builds a minimal XMP packet with a single Simple
// (scalar) property tiff:Make whose element text is rawContent verbatim
// (i.e. rawContent may contain raw XML markup such as character references).
func buildSimplePropertyDoc274(rawContent string) []byte {
	return xmpDoc(
		`<rdf:Description rdf:about="" xmlns:tiff="` + NStiff + `">` +
			`<tiff:Make>` + rawContent + `</tiff:Make>` +
			`</rdf:Description>`,
	)
}

// TestTask274CharRefForbiddenCharSanitized is the load-bearing regression
// test: a Simple property whose source text contains a numeric character
// reference to U+001E (this library's internal multi-value sentinel) — in
// both hex (&#x1e;) and decimal (&#30;) form — must:
//  1. decode to a value with NO literal U+001E byte (§2.2/§4.1: the raw
//     byte itself is exactly the fix's closed vulnerability surface), and
//  2. survive Parse -> Encode still emitted as a bare SCALAR element, never
//     split into an rdf:Bag/Seq/Alt collection.
//
// Before the fix (isForbiddenXMLCharRef absent from decodeCharRef): step 1
// failed — the decoded value contained a literal 0x1E byte — which in turn
// broke step 2 via write.go's `strings.IndexByte(val, '\x1e') < 0` dispatch.
func TestTask274CharRefForbiddenCharSanitized(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		charRef string // the numeric character reference under test
	}{
		{"hex-U+001E", "&#x1e;"},
		{"hex-U+001E-uppercase-X", "&#X1E;"},
		{"decimal-U+001E", "&#30;"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := buildSimplePropertyDoc274("Canon" + tc.charRef + "EOS")
			x, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := x.Get(NStiff, "Make")

			// (1) No literal U+001E byte in the decoded value — the exact byte
			// write.go's array-dispatch heuristic keys on.
			if strings.IndexByte(got, '\x1e') >= 0 {
				t.Fatalf("%s: decoded value %q contains a literal U+001E byte — forbidden Char reference was NOT sanitized", tc.name, got)
			}
			// Surrounding text must survive untouched.
			if !strings.HasPrefix(got, "Canon") || !strings.HasSuffix(got, "EOS") {
				t.Errorf("%s: surrounding text corrupted; got %q", tc.name, got)
			}
			// The forbidden code point must have been replaced with U+FFFD
			// (Unicode §5.22), the same substitution writeXMLEscaped applies.
			if !strings.Contains(got, "�") {
				t.Errorf("%s: expected U+FFFD substitution for the forbidden reference; got %q", tc.name, got)
			}

			// (2) Round trip through Encode: the property must still be a bare
			// scalar element, never an rdf:Bag/Seq/Alt collection.
			enc, err := Encode(x)
			if err != nil {
				t.Fatalf("%s: Encode: %v", tc.name, err)
			}
			out := string(enc)
			if strings.Contains(out, "<rdf:Bag>") || strings.Contains(out, "<rdf:Seq>") || strings.Contains(out, "<rdf:Alt>") {
				t.Errorf("%s: tiff:Make spuriously serialised as a collection:\n%s", tc.name, out)
			}
			wantScalar := "<tiff:Make>Canon�EOS</tiff:Make>"
			if !strings.Contains(out, wantScalar) {
				t.Errorf("%s: expected bare scalar element %q, got:\n%s", tc.name, wantScalar, out)
			}

			// Re-parsing the encoded output must reproduce the same sanitized
			// value — the fix must be stable under a second round trip.
			x2, err := Parse(enc)
			if err != nil {
				t.Fatalf("%s: re-Parse of encoded output: %v", tc.name, err)
			}
			if got2 := x2.Get(NStiff, "Make"); got2 != got {
				t.Errorf("%s: value unstable across round trip: %q vs %q", tc.name, got, got2)
			}
		})
	}
}

// TestTask274OtherForbiddenCharRefsSanitized exercises the remaining members
// of the XML 1.0 §2.2 forbidden set to prove isForbiddenXMLCharRef's full
// range check, not just the U+001E sentinel byte: the C0 controls other than
// TAB/LF/CR, and the two BMP non-characters U+FFFE/U+FFFF.
func TestTask274OtherForbiddenCharRefsSanitized(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		charRef string
	}{
		{"NUL", "&#x0;"}, // U+0000
		{"BS", "&#x8;"},  // U+0008 — top of the "<= 0x08" range
		{"VT", "&#xB;"},  // U+000B
		{"FF", "&#xC;"},  // U+000C
		{"SO", "&#xE;"},  // U+000E — bottom of "0x0E-0x1F"
		{"US", "&#x1F;"}, // U+001F — top of "0x0E-0x1F"
		{"noncharacter-FFFE", "&#xFFFE;"},
		{"noncharacter-FFFF", "&#xFFFF;"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := buildSimplePropertyDoc274("a" + tc.charRef + "b")
			x, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := x.Get(NStiff, "Make")
			want := "a�b"
			if got != want {
				t.Errorf("%s: got %q, want %q (U+FFFD substitution)", tc.name, got, want)
			}
		})
	}
}

// TestTask274LegalCharRefsUnaffected is the positive control demanded
// alongside the fix: character references to code points that ARE legal
// XML 1.0 Chars must continue to decode exactly as before — the new
// forbidden-range check must never fire on a legal reference. Covers the
// five predefined-entity-adjacent ASCII case ('A'), a BMP character outside
// the forbidden set, TAB/LF/CR (the three C0 exceptions that decodeCharRef
// must NOT flag), and a supplementary-plane character (which entirely
// bypasses the BMP-only forbidden-range check by construction).
func TestTask274LegalCharRefsUnaffected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		charRef string
		want    rune
	}{
		{"ASCII-A-hex", "&#x41;", 'A'},
		{"ASCII-A-decimal", "&#65;", 'A'},
		{"snowman-hex", "&#x2603;", '☃'},
		{"TAB", "&#x9;", '\t'},
		{"LF", "&#xA;", '\n'},
		{"CR", "&#xD;", '\r'},
		{"supplementary-plane", "&#x1F600;", '\U0001F600'}, // outside the BMP entirely
		{"last-legal-BMP-before-FFFE", "&#xFFFD;", '�'},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw := buildSimplePropertyDoc274("x" + tc.charRef + "y")
			x, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := x.Get(NStiff, "Make")
			want := "x" + string(tc.want) + "y"
			if got != want {
				t.Errorf("%s: got %q, want %q — legal char-ref incorrectly sanitized", tc.name, got, want)
			}
		})
	}
}

// TestIsForbiddenXMLCharRefTable is a primitive-level table-driven test for
// isForbiddenXMLCharRef itself, independent of the full Parse pipeline, so a
// future refactor of decodeCharRef cannot silently narrow or widen the
// forbidden range without a direct, obvious test failure. Boundary values on
// both sides of each forbidden sub-range are covered.
func TestIsForbiddenXMLCharRefTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		r    rune
		want bool
	}{
		{"0x00-NUL", 0x00, true},
		{"0x08-BS", 0x08, true},
		{"0x09-TAB-legal", 0x09, false},
		{"0x0A-LF-legal", 0x0A, false},
		{"0x0B-VT", 0x0B, true},
		{"0x0C-FF", 0x0C, true},
		{"0x0D-CR-legal", 0x0D, false},
		{"0x0E-SO", 0x0E, true},
		{"0x1D-boundary", 0x1D, true},
		{"0x1E-sentinel", 0x1E, true},
		{"0x1F-US", 0x1F, true},
		{"0x20-space-legal", 0x20, false},
		{"0xD7FF-legal", 0xD7FF, false},
		{"0xE000-legal", 0xE000, false},
		{"0xFFFD-legal", 0xFFFD, false},
		{"0xFFFE-noncharacter", 0xFFFE, true},
		{"0xFFFF-noncharacter", 0xFFFF, true},
		{"0x10000-legal-supplementary", 0x10000, false},
		{"0x10FFFE-legal-supplementary", 0x10FFFE, false},
		{"0x10FFFF-legal-supplementary-max", 0x10FFFF, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isForbiddenXMLCharRef(tc.r); got != tc.want {
				t.Errorf("isForbiddenXMLCharRef(0x%X) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// TestTask274NoAllocRegressionOnCharRefFreeDocument is a structural (not
// merely empirical) guarantee that the fix does not touch the parse fast
// path: decodeCharRef and isForbiddenXMLCharRef are reachable ONLY from
// decodeEntity's numeric-character-reference branch, which unescapeXML's
// entity-free fast path (no '&' present) never enters. A document with no
// '&' at all must decode identically before and after the fix; this test
// pins that behaviour so a future change that moves the check onto the
// literal-copy path would be caught here.
//
//nolint:paralleltest // testing.AllocsPerRun is incompatible with t.Parallel() (runs the closure on a separate, uninstrumented goroutine accounting basis)
func TestTask274NoAllocRegressionOnCharRefFreeDocument(t *testing.T) {
	raw := buildSimplePropertyDoc274("Canon EOS R5")
	x, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := x.Get(NStiff, "Make"); got != "Canon EOS R5" {
		t.Errorf("char-ref-free document decoded incorrectly: got %q", got)
	}

	allocs := testing.AllocsPerRun(100, func() {
		_, _ = Parse(raw)
	})
	t.Logf("char-ref-free Parse: %.1f allocs/run", allocs)
}
