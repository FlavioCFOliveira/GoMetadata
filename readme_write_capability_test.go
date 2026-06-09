package gometadata

// readme_write_capability_test.go — regression guard for task #193.
//
// TestREADMEWriteCapabilityMatchesCode enforces the doc↔code invariant:
// every format for which format.SupportsWrite returns true must NOT appear as
// read-only (Write column "No" or in any "ErrWriteNotSupported" enumeration)
// in README.md.
//
// Motivation: this divergence already regressed once (Sprint 9) and was
// discovered only during release hardening. A test that reads the README and
// correlates it with SupportsWrite ensures the divergence cannot silently
// reappear after future un-gates or re-gates.
//
// Design:
//   - The test locates the "Supported formats" table inside README.md by
//     searching for the header row (| Format | Extension(s) | ...).
//   - It parses each data row to extract the format name (column 0) and the
//     Write column (column 3).
//   - For each format whose Write column contains "No" (case-insensitive), it
//     maps the display name back to a FormatID and asserts SupportsWrite==false.
//   - It also scans for any prose line that lists format names alongside the
//     text "ErrWriteNotSupported" and asserts those formats also have
//     SupportsWrite==false in code.
//   - If the "Supported formats" table is not found, the test FAILS rather than
//     skipping — t.Skip is forbidden by project policy.
//
// Robustness:
//   - Anchors on format name tokens, not on line numbers or column counts, so
//     minor reformatting does not break the test.
//   - Footnote markers (¹ ² ³ ⁴ etc.) are stripped before comparison so "Yes²"
//     matches the same as "Yes".

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/FlavioCFOliveira/GoMetadata/format"
)

// readmeFormatDisplayNames maps the human-readable name used in the README
// table to the corresponding FormatID constant.  The keys are the exact
// strings that appear in column 0 of the "Supported formats" table (after
// stripping leading/trailing spaces and any markdown emphasis).
//
//nolint:gochecknoglobals // read-only lookup table; never mutated
var readmeFormatDisplayNames = map[string]format.FormatID{
	"JPEG":          format.FormatJPEG,
	"TIFF":          format.FormatTIFF,
	"PNG":           format.FormatPNG,
	"WebP":          format.FormatWebP,
	"HEIF":          format.FormatHEIF,
	"AVIF":          format.FormatAVIF,
	"Canon CR2":     format.FormatCR2,
	"Canon CR3":     format.FormatCR3,
	"Nikon NEF":     format.FormatNEF,
	"Sony ARW":      format.FormatARW,
	"Adobe DNG":     format.FormatDNG,
	"Olympus ORF":   format.FormatORF,
	"Panasonic RW2": format.FormatRW2,
}

// stripFootnoteMarkers removes superscript footnote characters (¹²³⁴ and
// similar Unicode superscript digits/letters) from s.  This lets "Yes²" and
// "Yes" compare equal.
func stripFootnoteMarkers(s string) string {
	return strings.Map(func(r rune) rune {
		// Unicode superscript digits and common footnote symbols.
		switch r {
		case '¹', '²', '³', '⁴', '⁵', '⁶', '⁷', '⁸', '⁹', '⁰',
			'ⁱ', 'ⁿ', '†', '‡', '§', '¶':
			return -1 // drop
		}
		return r
	}, s)
}

// splitMarkdownRow splits a Markdown table row (| col | col | ...) into its
// cell values, trimmed of whitespace.  The leading and trailing '|' delimiters
// are handled; empty cells are preserved.
func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	result := make([]string, len(parts))
	for i, p := range parts {
		result[i] = strings.TrimSpace(p)
	}
	return result
}

// isTableSeparator returns true for Markdown table separator rows
// (rows whose non-pipe characters are entirely dashes, colons, and spaces).
func isTableSeparator(line string) bool {
	for _, r := range line {
		if r != '|' && r != '-' && r != ':' && !unicode.IsSpace(r) {
			return false
		}
	}
	return strings.Contains(line, "-")
}

// TestREADMEWriteCapabilityMatchesCode is the doc↔code regression guard for
// task #193.  It fails if:
//   - The "Supported formats" table is missing from README.md.
//   - A format whose Write column says "No" has SupportsWrite==true in code.
//   - A format whose Write column says "Yes" (or "Yes²" etc.) has
//     SupportsWrite==false in code.
//
// The test does NOT use t.Skip under any condition — if the table structure
// changes in a way that makes parsing impossible, that is itself a failure
// requiring a fix.
//
// This test must be kept in sync when a new format is added to the library.
// Regression history: divergence was introduced in Sprint 9 (commit 561f5d8)
// and remained undetected until task #193 (Sprint 33).
//
//nolint:paralleltest // uses os.ReadFile; no shared mutable state; serial is fine
func TestREADMEWriteCapabilityMatchesCode(t *testing.T) {
	const readmePath = "README.md"

	f, err := os.Open(readmePath)
	if err != nil {
		t.Fatalf("TestREADMEWriteCapabilityMatchesCode: cannot open %s: %v", readmePath, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("TestREADMEWriteCapabilityMatchesCode: close %s: %v", readmePath, cerr)
		}
	}()

	// ------------------------------------------------------------------ //
	// Phase 1: locate and parse the "Supported formats" table.
	// ------------------------------------------------------------------ //
	//
	// We scan for the header row that contains all five expected column
	// titles.  Once found, we consume rows until the table ends (a line that
	// does not start with '|').

	const tableAnchor = "| Format |"

	type tableRow struct {
		formatName string // column 0
		writeValue string // column 3 (0-indexed)
	}

	var rows []tableRow
	inTable := false
	pastHeader := false
	tableFound := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if !inTable {
			// Detect the table header row.
			if strings.HasPrefix(trimmed, tableAnchor) {
				inTable = true
				tableFound = true
				pastHeader = false
			}
			continue
		}

		// We are inside the table.
		if !strings.HasPrefix(trimmed, "|") {
			// Blank line or non-pipe line: table has ended.
			break
		}

		if isTableSeparator(trimmed) {
			// The separator row (|---|:---:|...) marks end of header.
			pastHeader = true
			continue
		}

		if !pastHeader {
			// Still in the header area (multi-row header).
			continue
		}

		// Data row.
		cells := splitMarkdownRow(trimmed)
		// We need at least 4 columns (Format, Extension(s), Read, Write).
		if len(cells) < 4 {
			continue
		}
		rows = append(rows, tableRow{
			formatName: cells[0],
			writeValue: stripFootnoteMarkers(cells[3]),
		})
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("TestREADMEWriteCapabilityMatchesCode: scanner error: %v", err)
	}

	if !tableFound {
		// The table is structurally required; its absence is itself a failure.
		t.Fatal("TestREADMEWriteCapabilityMatchesCode: \"Supported formats\" table not found in README.md — " +
			"the table header row containing \"| Format |\" is required; " +
			"if the table was renamed or removed, update this test AND the README together")
	}

	if len(rows) == 0 {
		t.Fatal("TestREADMEWriteCapabilityMatchesCode: \"Supported formats\" table found but contains no data rows")
	}

	// ------------------------------------------------------------------ //
	// Phase 2: correlate each table row with format.SupportsWrite.
	// ------------------------------------------------------------------ //

	for _, row := range rows {
		fid, known := readmeFormatDisplayNames[row.formatName]
		if !known {
			// Unknown display name — either a new format was added without
			// updating readmeFormatDisplayNames, or the table row is a
			// footnote/legend line.  Fail loudly so the test stays complete.
			t.Errorf("README format %q is not in readmeFormatDisplayNames map — "+
				"update readmeFormatDisplayNames in readme_write_capability_test.go "+
				"when a new format is added", row.formatName)
			continue
		}

		writeColUpper := strings.ToUpper(row.writeValue)
		readmeClaimsWritable := strings.HasPrefix(writeColUpper, "YES")
		readmeClaimsReadOnly := strings.HasPrefix(writeColUpper, "NO")
		codeSupportsWrite := format.SupportsWrite(fid)

		switch {
		case readmeClaimsReadOnly && codeSupportsWrite:
			// README says read-only but code disagrees — the core regression.
			t.Errorf("README.md \"Supported formats\" table claims %q is read-only "+
				"(Write=%q) but format.SupportsWrite(%v)==true in code; "+
				"update the README Write column to match the code "+
				"(regression: Sprint 9 divergence, task #193)",
				row.formatName, row.writeValue, fid)

		case readmeClaimsWritable && !codeSupportsWrite:
			// README says writable but code disagrees — also a divergence.
			t.Errorf("README.md \"Supported formats\" table claims %q is writable "+
				"(Write=%q) but format.SupportsWrite(%v)==false in code; "+
				"update the README Write column or the code to match",
				row.formatName, row.writeValue, fid)
		}
	}

	// ------------------------------------------------------------------ //
	// Phase 3: verify every known writable FormatID has a table row.
	// ------------------------------------------------------------------ //
	//
	// If a format is supported for write in code but is entirely absent from
	// the README table (not just marked No), that is also a documentation gap.

	seenNames := make(map[string]bool, len(rows))
	for _, row := range rows {
		seenNames[row.formatName] = true
	}

	for displayName, fid := range readmeFormatDisplayNames {
		if format.SupportsWrite(fid) && !seenNames[displayName] {
			t.Errorf("format %q (FormatID=%v) supports write in code but has no row "+
				"in the README \"Supported formats\" table", displayName, fid)
		}
	}
}
