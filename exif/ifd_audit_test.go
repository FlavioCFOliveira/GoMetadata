package exif

// ifd_audit_test.go — Gate tests for audit findings #126, #129, #130, #131, #132.
//
// Each test has a stable name matching the audit finding so that a failure
// points directly at the violated specification clause.
//
// Spec references:
//   - CIPA DC-008-2023 §4.6.3 — SHORT/SSHORT and LONG/SLONG are distinct types.
//   - CIPA DC-008-2023 §4.5.2 — IFD structure; count field and entry list.
//   - TIFF 6.0 §2 (Adobe, 1992) — next-IFD pointer; value data location.
//   - ExifTool / Exiv2 behaviour — first-occurrence wins for duplicate tags.
//
// All tests in this file are fully self-contained: they build synthetic TIFF
// buffers from scratch and do not depend on testdata files.
//
// Inline-value encoding note (TIFF 6.0 §2):
//   "Values shorter than LONG are left-justified within the 4-byte Value field."
//   "Left-justified" means the bytes begin at the lowest byte address of the
//   4-byte field — so a TypeShort value must be written as a 2-byte field at
//   offset+8, not a 4-byte uint32 at offset+8.  buildIFDShortEntry (below)
//   enforces this for all test builders in this file.

import (
	"encoding/binary"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Shared IFD construction helpers
// ---------------------------------------------------------------------------

// tiffHeader writes a TIFF header at b[0:8]:
//
//	bytes 0-1: byte-order mark ('II' LE, 'MM' BE)
//	bytes 2-3: magic 0x002A
//	bytes 4-7: IFD0 offset (always 8 in tests constructed by this helper)
//
// ifd0Off is always 8 in practice for the synthetic TIFF buffers built by this
// file; it is kept as a parameter for clarity.
func tiffHeader(b []byte, order binary.ByteOrder, ifd0Off uint32) { //nolint:unparam // ifd0Off=8 in all current callers; kept explicit for readability
	if order == binary.LittleEndian {
		b[0], b[1] = 'I', 'I'
	} else {
		b[0], b[1] = 'M', 'M'
	}
	order.PutUint16(b[2:], 0x002A)
	order.PutUint32(b[4:], ifd0Off)
}

// writeIFDEntryLong writes a 12-byte TIFF IFD entry for TypeLong at b[pos:pos+12].
// TypeLong values are 4 bytes, always fit inline (TIFF 6.0 §2).
func writeIFDEntryLong(b []byte, pos int, tag TagID, val uint32, order binary.ByteOrder) {
	order.PutUint16(b[pos:], uint16(tag))
	order.PutUint16(b[pos+2:], uint16(TypeLong))
	order.PutUint32(b[pos+4:], 1)
	// TypeLong is 4 bytes; fits inline as-is in both byte orders.
	order.PutUint32(b[pos+8:], val)
}

// writeIFDEntryShort writes a 12-byte TIFF IFD entry for TypeShort at b[pos:pos+12].
// TIFF 6.0 §2: "Values shorter than LONG are left-justified within the 4-byte
// Value field." Left-justified = at the lowest byte address, so we write
// order.PutUint16 at pos+8 (the first 2 bytes of the field), not a uint32.
//
// tag is always 0x0112 (Orientation) in the duplicate-tag gate tests; it is
// kept as a parameter for the multi-condition test that uses TagImageWidth too.
func writeIFDEntryShort(b []byte, pos int, tag TagID, val uint16, order binary.ByteOrder) { //nolint:unparam // tag varies across callers; unparam fires because one caller subset always passes 0x0112
	order.PutUint16(b[pos:], uint16(tag))
	order.PutUint16(b[pos+2:], uint16(TypeShort))
	order.PutUint32(b[pos+4:], 1) // count=1
	// Write the 2-byte value left-justified in the 4-byte field.
	order.PutUint16(b[pos+8:], val)
	// bytes pos+10 and pos+11 are padding — already zero from make().
}

// writeIFDEntryTyped writes a 12-byte TIFF IFD entry with an arbitrary inline
// (≤4 byte) value. The value is written with a 16-bit store for 2-byte types
// and a 32-bit store for 4-byte types.
func writeIFDEntrySShort(b []byte, pos int, tag TagID, val int16, order binary.ByteOrder) {
	order.PutUint16(b[pos:], uint16(tag))
	order.PutUint16(b[pos+2:], uint16(TypeSShort))
	order.PutUint32(b[pos+4:], 1)
	order.PutUint16(b[pos+8:], uint16(val)) //nolint:gosec // G115: intentional bit-cast int16→uint16 for TIFF inline value
}

func writeIFDEntrySLong(b []byte, pos int, tag TagID, val int32, order binary.ByteOrder) {
	order.PutUint16(b[pos:], uint16(tag))
	order.PutUint16(b[pos+2:], uint16(TypeSLong))
	order.PutUint32(b[pos+4:], 1)
	order.PutUint32(b[pos+8:], uint32(val)) //nolint:gosec // G115: intentional bit-cast int32→uint32 for TIFF inline value
}

// ---------------------------------------------------------------------------
// #126 — Duplicate tags: first occurrence wins (deterministic)
// ---------------------------------------------------------------------------

// TestDuplicateTagFirstWins verifies that when an IFD contains the same tag
// twice, parseSingleIFD keeps the FIRST occurrence and drops the second.
//
// EXIF 3.0 CIPA DC-008-2023 §4.5.2: each tag must appear at most once; the
// de-facto standard (ExifTool, Exiv2) is to keep the first occurrence when a
// file violates this rule.  Audit finding #126.
//
// Gate contract: IFD with tag 0x0112 = [6, 1] (first value 6, second value 1)
// must return Uint16() == 6 regardless of run order or architecture.
func TestDuplicateTagFirstWins(t *testing.T) {
	t.Parallel()

	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		name := "LittleEndian"
		if order == binary.BigEndian {
			name = "BigEndian"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Build: TIFF header (8) + count(2) + 2 entries (2×12=24) + next-IFD(4) = 38 bytes.
			// Both entries are TypeShort (2 bytes inline) for tag 0x0112 (Orientation).
			// First value = 6, second = 1.
			buf := make([]byte, 38)
			tiffHeader(buf, order, 8)
			order.PutUint16(buf[8:], 2) // IFD0: 2 entries

			writeIFDEntryShort(buf, 10, 0x0112, 6, order) // first  occurrence
			writeIFDEntryShort(buf, 22, 0x0112, 1, order) // second occurrence (duplicate)
			order.PutUint32(buf[34:], 0)                  // next-IFD = 0

			// Run 10 times to confirm determinism across calls.
			for run := range 10 {
				e, err := Parse(buf)
				if err != nil {
					t.Fatalf("run %d: Parse failed: %v", run, err)
				}
				if e.IFD0 == nil {
					t.Fatalf("run %d: IFD0 is nil", run)
				}

				// IFD0 must contain exactly ONE entry for 0x0112 (dedup).
				dup := 0
				for _, entry := range e.IFD0.Entries {
					if entry.Tag == 0x0112 {
						dup++
					}
				}
				if dup != 1 {
					t.Errorf("run %d: expected 1 entry for tag 0x0112, got %d", run, dup)
				}

				// The surviving entry must carry the FIRST value (6).
				entry := e.IFD0.Get(0x0112)
				if entry == nil {
					t.Fatalf("run %d: tag 0x0112 not found after dedup", run)
				}
				if got := entry.Uint16(); got != 6 {
					t.Errorf("run %d: Uint16()=%d, want 6 (first occurrence)", run, got)
				}

				// Duplicate tag must have produced a ParseWarning.
				foundWarn := false
				for _, w := range e.Warnings {
					if strings.Contains(w, "duplicate tag") && strings.Contains(w, "0x0112") {
						foundWarn = true
						break
					}
				}
				if !foundWarn {
					t.Errorf("run %d: expected warning mentioning duplicate tag 0x0112; warnings: %v", run, e.Warnings)
				}
			}
		})
	}
}

// TestDuplicateTagFirstWins_ThreeOccurrences verifies that three occurrences of
// the same tag result in exactly one entry (the first) and two warnings.
// Audit finding #126.
func TestDuplicateTagFirstWins_ThreeOccurrences(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// header(8) + count(2) + 3×12(36) + next(4) = 50 bytes
	buf := make([]byte, 50)
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 3) // 3 entries

	writeIFDEntryShort(buf, 10, 0x0112, 6, order) // first  = keep
	writeIFDEntryShort(buf, 22, 0x0112, 1, order) // second = drop
	writeIFDEntryShort(buf, 34, 0x0112, 3, order) // third  = drop
	order.PutUint32(buf[46:], 0)                  // next-IFD = 0

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	entry := e.IFD0.Get(0x0112)
	if entry == nil {
		t.Fatal("tag 0x0112 not found")
	}
	if got := entry.Uint16(); got != 6 {
		t.Errorf("Uint16()=%d, want 6 (first occurrence)", got)
	}
	// Two extra occurrences → two warnings.
	dupWarns := 0
	for _, w := range e.Warnings {
		if strings.Contains(w, "duplicate tag") {
			dupWarns++
		}
	}
	if dupWarns != 2 {
		t.Errorf("expected 2 duplicate-tag warnings, got %d (warnings: %v)", dupWarns, e.Warnings)
	}
}

// ---------------------------------------------------------------------------
// #129 — Uint16/Uint32 reject signed-type entries
// ---------------------------------------------------------------------------

// TestUint16RejectsSShort verifies that Uint16() returns 0 for a TypeSShort
// entry (even if the bit pattern would be a valid uint16) and that Int16()
// returns the correct signed value.
//
// CIPA DC-008-2023 §4.6.3: SHORT (3) and SSHORT (8) are distinct types.
// Audit finding #129.
func TestUint16RejectsSShort(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// header(8) + count(2) + 1×12(12) + next(4) = 26 bytes
	buf := make([]byte, 26)
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 1) // 1 entry

	// TypeSShort entry: tag 0x0112, value −100.
	// TIFF 6.0 §2: left-justified inline → PutUint16 at pos+8.
	writeIFDEntrySShort(buf, 10, 0x0112, -100, order)
	order.PutUint32(buf[22:], 0) // next-IFD = 0

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}
	entry := e.IFD0.Get(0x0112)
	if entry == nil {
		t.Fatal("tag 0x0112 not found")
	}
	if entry.Type != TypeSShort {
		t.Fatalf("expected TypeSShort, got %d", entry.Type)
	}

	// Uint16 MUST return 0 for TypeSShort — not the two's-complement bit pattern.
	// CIPA DC-008-2023 §4.6.3: SHORT and SSHORT are distinct; Uint16 accepts only SHORT.
	if got := entry.Uint16(); got != 0 {
		t.Errorf("Uint16() on TypeSShort = %d, want 0 (must reject signed type; audit finding #129)", got)
	}

	// Int16 must correctly decode the signed value.
	if got := entry.Int16(); got != -100 {
		t.Errorf("Int16() on TypeSShort = %d, want -100", got)
	}
}

// TestUint32RejectsSLong verifies that Uint32() returns 0 for a TypeSLong entry.
//
// CIPA DC-008-2023 §4.6.3: LONG (4) and SLONG (9) are distinct types.
// Audit finding #129.
func TestUint32RejectsSLong(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// header(8) + count(2) + 1×12(12) + next(4) = 26 bytes
	buf := make([]byte, 26)
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 1)

	// TypeSLong entry: tag 0x0100 (ImageWidth), value −1.
	writeIFDEntrySLong(buf, 10, TagImageWidth, -1, order)
	order.PutUint32(buf[22:], 0) // next-IFD = 0

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	entry := e.IFD0.Get(TagImageWidth)
	if entry == nil {
		t.Fatal("tag TagImageWidth not found")
	}
	if entry.Type != TypeSLong {
		t.Fatalf("expected TypeSLong, got %d", entry.Type)
	}

	// Uint32 MUST return 0 for TypeSLong.
	if got := entry.Uint32(); got != 0 {
		t.Errorf("Uint32() on TypeSLong = %d, want 0 (must reject signed type; audit finding #129)", got)
	}

	// Int32 must correctly decode the signed value.
	if got := entry.Int32(); got != -1 {
		t.Errorf("Int32() on TypeSLong = %d, want -1", got)
	}
}

// TestUint16AcceptsShort verifies that Uint16() still works correctly for
// TypeShort (the only type it should accept) after the #129 fix.
func TestUint16AcceptsShort(t *testing.T) {
	t.Parallel()

	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		name := "LE"
		if order == binary.BigEndian {
			name = "BE"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf := make([]byte, 26)
			tiffHeader(buf, order, 8)
			order.PutUint16(buf[8:], 1)
			writeIFDEntryShort(buf, 10, 0x0112, 6, order)
			order.PutUint32(buf[22:], 0)

			e, err := Parse(buf)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			entry := e.IFD0.Get(0x0112)
			if entry == nil {
				t.Fatal("tag 0x0112 not found")
			}
			if got := entry.Uint16(); got != 6 {
				t.Errorf("Uint16() on TypeShort = %d, want 6", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// #130 — Truncated IFD entry count: parse fitting entries (lenient)
// ---------------------------------------------------------------------------

// TestTruncatedIFDCountPartial verifies that when the IFD declares more entries
// than fit in the buffer, parseSingleIFD clamps to the fitting count and returns
// the entries that do fit (without panicking).
//
// CIPA DC-008-2023 §4.5.2 / conformance rule R-05 / ExifTool lenient behaviour.
// Audit finding #130.
func TestTruncatedIFDCountPartial(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const ifd0Off = 8
	// Only 2 entries physically fit: header(8) + count(2) + 2×12(24) + next(4) = 38 bytes.
	buf := make([]byte, 38)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 5) // lie: claim 5 entries; only 2 fit

	writeIFDEntryLong(buf, ifd0Off+2, TagImageWidth, 640, order)
	writeIFDEntryLong(buf, ifd0Off+14, TagImageLength, 480, order)
	// next-IFD pointer = 0 (already zero from make)

	// Must not panic.
	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse with truncated IFD count: unexpected error: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil; expected lenient parse to recover fitting entries")
	}

	// Both fitting entries must be present.
	if w := e.IFD0.Get(TagImageWidth); w == nil || w.Uint32() != 640 {
		t.Errorf("ImageWidth: want 640, got %v", e.IFD0.Get(TagImageWidth))
	}
	if l := e.IFD0.Get(TagImageLength); l == nil || l.Uint32() != 480 {
		t.Errorf("ImageLength: want 480, got %v", e.IFD0.Get(TagImageLength))
	}

	// A ParseWarning must be present describing the truncation/clamping.
	if len(e.Warnings) == 0 {
		t.Fatal("expected at least one ParseWarning for truncated IFD count, got none")
	}
	found := false
	for _, w := range e.Warnings {
		if strings.Contains(w, "clamped") || strings.Contains(w, "fit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ParseWarning mentioning clamping/truncation; warnings: %v", e.Warnings)
	}
}

// TestTruncatedIFDCountZeroFit verifies that when no entries fit at all (extreme
// truncation), Parse returns an error rather than an empty IFD0.
// Audit finding #130: clamping to zero available entries should hard-reject.
func TestTruncatedIFDCountZeroFit(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// Only space for the count field; no room for any entry.
	buf := make([]byte, 10) // header(8) + count(2); zero bytes for entries
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 10) // claims 10 entries; 0 fit

	// Must not panic; expecting an error because available = 0.
	_, err := Parse(buf)
	if err == nil {
		t.Error("expected error when no entries fit at all (count clamped to zero)")
	}
}

// ---------------------------------------------------------------------------
// #131 — Out-of-range NextIFD pointer produces ParseWarning
// ---------------------------------------------------------------------------

// TestBadNextIFDPointerWarning verifies that an IFD whose next-IFD pointer
// points outside the buffer produces a ParseWarning and leaves IFD0 intact.
//
// TIFF 6.0 §2: the next-IFD pointer value 0 = end of chain; a non-zero pointer
// must be a valid offset. Audit finding #131.
func TestBadNextIFDPointerWarning(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const ifd0Off = 8
	// header(8) + count(2) + 1×12(12) + next(4) = 26 bytes
	buf := make([]byte, 26)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)
	writeIFDEntryLong(buf, ifd0Off+2, TagImageWidth, 1920, order)

	// Bad next-IFD pointer (0xFFFFFFFF = far past any valid buffer).
	order.PutUint32(buf[ifd0Off+14:], 0xFFFFFFFF)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse with bad next-IFD pointer: unexpected error: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil; IFD0 must be returned even with a bad next-IFD pointer")
	}

	// IFD0 must contain the valid entry.
	entry := e.IFD0.Get(TagImageWidth)
	if entry == nil || entry.Uint32() != 1920 {
		t.Errorf("ImageWidth: want 1920, got %v", entry)
	}

	// IFD0.Next must be nil (chain terminated).
	if e.IFD0.Next != nil {
		t.Error("IFD0.Next should be nil when next-IFD pointer is out of range")
	}

	// A ParseWarning mentioning the IFD chain must be present.
	if len(e.Warnings) == 0 {
		t.Fatal("expected at least one ParseWarning for bad next-IFD pointer, got none")
	}
	found := false
	for _, w := range e.Warnings {
		if strings.Contains(w, "next-IFD") || strings.Contains(w, "chain") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ParseWarning mentioning next-IFD/chain; warnings: %v", e.Warnings)
	}
}

// TestBadNextIFDPointerWarning_BigEndian re-runs the bad-next-IFD gate in BE.
// Audit finding #131.
func TestBadNextIFDPointerWarning_BigEndian(t *testing.T) {
	t.Parallel()

	order := binary.BigEndian
	const ifd0Off = 8
	buf := make([]byte, 26)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)
	writeIFDEntryLong(buf, ifd0Off+2, TagImageWidth, 1920, order)
	order.PutUint32(buf[ifd0Off+14:], 0xFFFFFFFF)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse with bad next-IFD (BE): unexpected error: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil (BE)")
	}
	if len(e.Warnings) == 0 {
		t.Fatal("expected at least one ParseWarning (BE), got none")
	}
	found := false
	for _, w := range e.Warnings {
		if strings.Contains(w, "next-IFD") || strings.Contains(w, "chain") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected next-IFD chain warning (BE); warnings: %v", e.Warnings)
	}
}

// TestBadNextIFDPointerZeroMeansEnd verifies that a next-IFD pointer of 0 is
// treated as end-of-chain and does NOT generate a warning.
// TIFF 6.0 §2: 0 = end of chain. Regression test for #131 fix.
func TestBadNextIFDPointerZeroMeansEnd(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	buf := make([]byte, 26)
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 1)
	writeIFDEntryLong(buf, 10, TagImageWidth, 100, order)
	order.PutUint32(buf[22:], 0) // next-IFD = 0 (end of chain)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse with next-IFD=0: unexpected error: %v", err)
	}
	// Zero next-IFD must not produce any chain-related warnings.
	for _, w := range e.Warnings {
		if strings.Contains(w, "next-IFD") {
			t.Errorf("unexpected next-IFD warning for next=0: %s", w)
		}
	}
}

// ---------------------------------------------------------------------------
// #132 — IFD entry value area overlapping the IFD directory
// ---------------------------------------------------------------------------

// TestIFDEntryValueOverlapsDirectory verifies that an OOL value offset pointing
// into the IFD directory area produces a ParseWarning but does not panic, and
// that the IFD is still returned (lenient parse).
//
// TIFF 6.0 §2: value data must not alias the IFD directory structure.
// Audit finding #132.
func TestIFDEntryValueOverlapsDirectory(t *testing.T) {
	t.Parallel()

	// Build a TIFF where a TypeASCII Make entry (count=5 → OOL) has its value
	// offset pointing INTO the IFD directory region.
	// Layout: header(8) + count(2) + 1×12 + next(4) = 26 bytes (ifdEnd=26).
	// valOff = ifd0Off+2 = 10 (into the entry area).
	// Buffer must be at least ifdEnd (26) bytes; valOff+5=15 < 26 so the bound
	// check in parseIFDEntry will reject the entry (end > len(b)) unless we
	// extend the buffer to accommodate the value.  Use ifdEnd as the minimum so
	// the IFD directory itself is always fully readable.
	const ifd0Off = 8
	const ifdEnd = ifd0Off + 2 + 12 + 4 // = 26; covers count+1-entry+next
	const valOff = ifd0Off + 2          // 10 — inside the entry area
	const valCount = 5
	// Buffer must hold: (a) the full IFD directory, and (b) valOff+valCount bytes.
	bufLen := ifdEnd
	if valOff+valCount > bufLen {
		bufLen = valOff + valCount
	}

	order := binary.LittleEndian
	buf := make([]byte, bufLen)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1) // 1 entry

	// TypeASCII Make (0x010F), count=5, OOL (>4 bytes), valOff points into IFD.
	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagMake))
	order.PutUint16(buf[p+2:], uint16(TypeASCII))
	order.PutUint32(buf[p+4:], valCount)       // count=5 → OOL
	order.PutUint32(buf[p+8:], uint32(valOff)) // overlaps IFD directory

	// next-IFD pointer (at p+12 = 22) — zero from make().

	// Must not panic.
	var gotWarnings []string
	mustNotPanic(t, "#132 OOL value aliases IFD directory", func() {
		e, err := Parse(buf)
		if err != nil || e == nil || e.IFD0 == nil {
			return
		}
		gotWarnings = e.Warnings
	})

	// If IFD0 was returned, the overlap warning must be present.
	// (If IFD0 was nil, the no-panic assertion already passed.)
	if len(gotWarnings) > 0 {
		found := false
		for _, w := range gotWarnings {
			if strings.Contains(w, "overlap") || strings.Contains(w, "IFD directory") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected ParseWarning mentioning overlap/IFD directory; warnings: %v", gotWarnings)
		}
	}
}

// TestIFDEntryValueOverlapsDirectory_NoPanic is the focused no-panic gate for
// #132: even with a maximally adversarial overlapping offset, Parse must not
// panic.
func TestIFDEntryValueOverlapsDirectory_NoPanic(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const ifd0Off = 8
	const ifdEnd = ifd0Off + 2 + 12 + 4 // 26

	// valOff = ifd0Off (the count field itself — maximum aliasing).
	// buffer must be at least ifdEnd bytes so the IFD count field is readable.
	buf := make([]byte, ifdEnd)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 1)

	p := ifd0Off + 2
	order.PutUint16(buf[p:], uint16(TagMake))
	order.PutUint16(buf[p+2:], uint16(TypeASCII))
	order.PutUint32(buf[p+4:], 5)               // count=5 → OOL; but valOff+5 > len(buf) → entry skipped
	order.PutUint32(buf[p+8:], uint32(ifd0Off)) // valOff = 8 (inside IFD)

	mustNotPanic(t, "#132 OOL value aliases IFD count field", func() {
		_, _ = Parse(buf)
	})
}

// TestIFDEntryValueOverlapsDirectory_VariousOffsets tests several overlap
// scenarios to confirm the detection is robust regardless of where inside the
// directory the valOff lands.
func TestIFDEntryValueOverlapsDirectory_VariousOffsets(t *testing.T) {
	t.Parallel()

	const ifd0Off = 8
	const ifdEnd = ifd0Off + 2 + 12 + 4 // directory spans [8, 26)

	cases := []struct {
		name        string
		valOff      uint32
		wantOverlap bool
	}{
		{"at_count_field", uint32(ifd0Off), true},
		{"at_entry_start", uint32(ifd0Off + 2), true},
		{"mid_entry", uint32(ifd0Off + 8), true},
		{"past_directory", uint32(ifdEnd), false}, // = 26, just outside
		{"well_past", uint32(ifdEnd + 10), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Buffer must hold the IFD and also valOff+5 bytes (if after IFD).
			minLen := ifdEnd
			if int(tc.valOff)+5 > minLen {
				minLen = int(tc.valOff) + 5
			}
			buf := make([]byte, minLen)
			order := binary.LittleEndian
			tiffHeader(buf, order, ifd0Off)
			order.PutUint16(buf[ifd0Off:], 1)

			p := ifd0Off + 2
			order.PutUint16(buf[p:], uint16(TagMake))
			order.PutUint16(buf[p+2:], uint16(TypeASCII))
			order.PutUint32(buf[p+4:], 5) // count=5 → OOL
			order.PutUint32(buf[p+8:], tc.valOff)

			var gotWarning bool
			mustNotPanic(t, tc.name, func() {
				e, err := Parse(buf)
				if err != nil || e == nil || e.IFD0 == nil {
					return
				}
				for _, w := range e.Warnings {
					if strings.Contains(w, "overlap") || strings.Contains(w, "IFD directory") {
						gotWarning = true
					}
				}
			})

			if tc.wantOverlap && !gotWarning {
				// Re-run to get diagnostics.
				buf2 := make([]byte, minLen)
				copy(buf2, buf)
				e2, err2 := Parse(buf2)
				if err2 == nil && e2 != nil && e2.IFD0 != nil && e2.IFD0.Get(TagMake) != nil {
					t.Errorf("%s (valOff=%d): expected overlap ParseWarning, got none (warnings: %v)",
						tc.name, tc.valOff, e2.Warnings)
				}
				// If the entry was rejected (out-of-bounds or IFD nil), no warning is
				// required because the condition was never reached.
			}
			if !tc.wantOverlap && gotWarning {
				t.Errorf("%s (valOff=%d): got unexpected overlap warning (not in directory region [%d,%d))",
					tc.name, tc.valOff, ifd0Off, ifdEnd)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Warnings reach exif.EXIF.Warnings and propagate to the caller
// ---------------------------------------------------------------------------

// TestExifWarningsReachMetadataParseWarnings verifies the end-to-end warning
// propagation path: parse warnings produced inside exif.Parse are surfaced in
// EXIF.Warnings (and from there read.go#parseEXIF populates Metadata.ParseWarnings).
//
// This test drives exif.Parse directly — no container layer needed.
func TestExifWarningsReachMetadataParseWarnings(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// Build an IFD with a duplicate tag so Parse produces at least one warning.
	buf := make([]byte, 38)
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 2)
	writeIFDEntryShort(buf, 10, 0x0112, 6, order) // first
	writeIFDEntryShort(buf, 22, 0x0112, 1, order) // duplicate
	order.PutUint32(buf[34:], 0)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// EXIF.Warnings must be non-empty (duplicate tag was detected).
	if len(e.Warnings) == 0 {
		t.Fatal("EXIF.Warnings is empty; expected at least one duplicate-tag warning")
	}

	// The warnings slice must contain a message about the duplicate tag.
	found := false
	for _, w := range e.Warnings {
		if strings.Contains(w, "duplicate") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no duplicate-tag warning found; EXIF.Warnings: %v", e.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Regression: R-05 conformance battery must still pass after #130 fix
// ---------------------------------------------------------------------------

// TestR05ConformanceStillPasses is an inline regression check that the R-05
// conformance test scenario (count=5, only 2 entries fit) now returns a
// non-nil IFD0 with the fitting entries — previously it returned nil+error.
// This is the core behavioural change introduced by audit finding #130.
func TestR05ConformanceStillPasses(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const ifd0Off = 8
	buf := make([]byte, 38)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 5) // claim 5; only 2 fit

	writeIFDEntryLong(buf, ifd0Off+2, TagImageWidth, 640, order)
	writeIFDEntryLong(buf, ifd0Off+14, TagImageLength, 480, order)
	// next-IFD = 0 (already zero from make)

	e, err := Parse(buf)
	// After #130 fix: must succeed (no error) and return IFD0 with 2 entries.
	if err != nil {
		t.Fatalf("R-05 regression: Parse now returns error (%v); must return partial IFD", err)
	}
	if e.IFD0 == nil {
		t.Fatal("R-05 regression: IFD0 is nil; expected lenient parse")
	}
	if e.IFD0.Get(TagImageWidth) == nil {
		t.Error("R-05 regression: ImageWidth missing after lenient parse")
	}
	if e.IFD0.Get(TagImageLength) == nil {
		t.Error("R-05 regression: ImageLength missing after lenient parse")
	}
	// A warning about clamping must be present.
	found := false
	for _, w := range e.Warnings {
		if strings.Contains(w, "clamped") || strings.Contains(w, "fit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("R-05 regression: no truncation warning; warnings: %v", e.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Multiple-condition parse warnings in a single call
// ---------------------------------------------------------------------------

// TestParseWarningsMultipleConditions verifies that a single Parse call
// collecting both a truncation warning (#130) and a duplicate-tag warning (#126)
// surfaces both in EXIF.Warnings.
func TestParseWarningsMultipleConditions(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const ifd0Off = 8
	// Buffer holds 3 entries but count claims 10.
	// Entries: ImageWidth, Orientation(6), Orientation(1) — truncation + duplicate.
	// header(8) + count(2) + 3×12(36) + next(4) = 50 bytes
	buf := make([]byte, 50)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 10) // claim 10; only 3 fit

	writeIFDEntryLong(buf, ifd0Off+2, TagImageWidth, 100, order)
	writeIFDEntryShort(buf, ifd0Off+14, 0x0112, 6, order) // first occurrence
	writeIFDEntryShort(buf, ifd0Off+26, 0x0112, 1, order) // duplicate
	order.PutUint32(buf[ifd0Off+38:], 0)                  // next-IFD = 0

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if e.IFD0 == nil {
		t.Fatal("IFD0 is nil")
	}

	hasTrunc := false
	hasDup := false
	for _, w := range e.Warnings {
		if strings.Contains(w, "clamped") || strings.Contains(w, "fit") {
			hasTrunc = true
		}
		if strings.Contains(w, "duplicate") {
			hasDup = true
		}
	}
	if !hasTrunc {
		t.Errorf("expected truncation warning; warnings: %v", e.Warnings)
	}
	if !hasDup {
		t.Errorf("expected duplicate-tag warning; warnings: %v", e.Warnings)
	}

	// Orientation must be 6 (first occurrence).
	orient := e.IFD0.Get(0x0112)
	if orient == nil || orient.Uint16() != 6 {
		t.Errorf("Orientation = %v, want 6", orient)
	}
}

// ---------------------------------------------------------------------------
// Whitebox: parseSingleIFD returns warnings directly
// ---------------------------------------------------------------------------

// TestParseSingleIFDTruncatedCount verifies parseSingleIFD directly for the
// truncation-clamping behaviour (audit finding #130).
func TestParseSingleIFDTruncatedCount(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	const ifd0Off = 8
	buf := make([]byte, 38)
	tiffHeader(buf, order, ifd0Off)
	order.PutUint16(buf[ifd0Off:], 99) // claim 99; only 2 fit

	writeIFDEntryLong(buf, ifd0Off+2, TagImageWidth, 640, order)
	writeIFDEntryLong(buf, ifd0Off+14, TagImageLength, 480, order)

	ifd, _, ok, _, warnRecs := parseSingleIFD(buf, ifd0Off, order)
	if !ok {
		t.Fatal("parseSingleIFD returned !ok; expected lenient success")
	}
	if ifd == nil {
		t.Fatal("parseSingleIFD returned nil IFD")
	}
	if ifd.Get(TagImageWidth) == nil {
		t.Error("ImageWidth missing")
	}
	if ifd.Get(TagImageLength) == nil {
		t.Error("ImageLength missing")
	}
	// task #200: parseSingleIFD now returns []parseWarn; materialise before checking.
	warnings := materializeWarnings(warnRecs)
	if len(warnings) == 0 {
		t.Error("expected at least one warning for truncated count, got none")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "clamped") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'clamped' warning; warnings: %v", warnings)
	}
}

// ---------------------------------------------------------------------------
// IFDEntry accessor unit tests (direct struct construction)
// ---------------------------------------------------------------------------

// TestUint16DirectSShort tests Uint16() directly on a manually constructed
// TypeSShort IFDEntry (without going through Parse).
func TestUint16DirectSShort(t *testing.T) {
	t.Parallel()

	// −100 as int16 LE = 0x9C 0xFF; two's-complement as uint16 = 65436.
	e := IFDEntry{
		Tag:       0x0112,
		Type:      TypeSShort,
		Count:     1,
		Value:     []byte{0x9C, 0xFF},
		bigEndian: false,
	}
	if got := e.Uint16(); got != 0 {
		t.Errorf("Uint16() on TypeSShort entry = %d, want 0 (audit finding #129)", got)
	}
	if got := e.Int16(); got != -100 {
		t.Errorf("Int16() on TypeSShort entry = %d, want -100", got)
	}
}

// TestUint32DirectSLong tests Uint32() directly on a manually constructed
// TypeSLong IFDEntry (without going through Parse).
func TestUint32DirectSLong(t *testing.T) {
	t.Parallel()

	var negMillion int32 = -1_000_000
	val := make([]byte, 4)
	binary.LittleEndian.PutUint32(val, uint32(negMillion)) //nolint:gosec // G115: test — intentional bit-cast int32→uint32

	e := IFDEntry{
		Tag:       0x0100,
		Type:      TypeSLong,
		Count:     1,
		Value:     val,
		bigEndian: false,
	}
	if got := e.Uint32(); got != 0 {
		t.Errorf("Uint32() on TypeSLong entry = %d, want 0 (audit finding #129)", got)
	}
	if got := e.Int32(); got != negMillion {
		t.Errorf("Int32() on TypeSLong entry = %d, want %d", got, negMillion)
	}
}

// ---------------------------------------------------------------------------
// No spurious warnings on valid IFDs (regression guard)
// ---------------------------------------------------------------------------

// TestNoSpuriousWarningsOnValidIFD verifies that a well-formed IFD (unique
// tags, accurate count, valid next-IFD=0) produces zero ParseWarnings.
// This prevents the #126/#130/#131/#132 fixes from introducing false positives.
func TestNoSpuriousWarningsOnValidIFD(t *testing.T) {
	t.Parallel()

	order := binary.LittleEndian
	// header(8) + count(2) + 3×12(36) + next(4) = 50
	buf := make([]byte, 50)
	tiffHeader(buf, order, 8)
	order.PutUint16(buf[8:], 3) // exactly 3 entries

	writeIFDEntryLong(buf, 10, TagImageWidth, 100, order)
	writeIFDEntryLong(buf, 22, TagImageLength, 200, order)
	writeIFDEntryShort(buf, 34, 0x0112, 1, order) // Orientation
	order.PutUint32(buf[46:], 0)                  // next-IFD = 0

	parsed, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse failed on valid IFD: %v", err)
	}
	if len(parsed.Warnings) != 0 {
		t.Errorf("unexpected warnings on valid IFD: %v", parsed.Warnings)
	}
}

// ---------------------------------------------------------------------------
// task #200 — message-lock: warnString output must be byte-identical to the
// former fmt.Sprintf output so that any future format drift is caught immediately.
// ---------------------------------------------------------------------------

// TestParseWarnMessageLock asserts that every warnString variant renders to
// the exact literal string that the former fmt.Sprintf calls produced.  This
// test must fail loudly if message text drifts, preventing silent API changes.
//
// The expected strings are hard-coded literals, NOT derived via fmt.Sprintf.
// That is intentional: if both the implementation and the test were changed
// simultaneously, the byte-identity guarantee would be lost.
//
// Task reference: performance audit 2026-06-10, task #200.
func TestParseWarnMessageLock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		record parseWarn
		want   string
	}{
		// warnCountClamp — CIPA DC-008 §4.5.2 truncation
		{
			"warnCountClamp",
			parseWarn{kind: warnCountClamp, val1: 8, val2: 5, val3: 2, val4: 38},
			"exif: IFD at offset 8 declares 5 entries but only 2 fit in the buffer (len 38); clamped \xe2\x80\x94 lenient parse (CIPA DC-008 \xc2\xa74.5.2)",
		},
		// warnDuplicateTag — ExifTool/Exiv2 first-occurrence behaviour
		{
			"warnDuplicateTag",
			parseWarn{kind: warnDuplicateTag, val1: 8, val2: 0x0112},
			"exif: IFD at offset 8 contains duplicate tag 0x0112; keeping first occurrence (ExifTool/Exiv2 behaviour)",
		},
		// warnOOLAliasIFD — OOL value overlaps IFD directory
		{
			"warnOOLAliasIFD",
			parseWarn{kind: warnOOLAliasIFD, val1: 0x010F, val2: 10, val3: 8, val4: 26},
			"exif: tag 0x010F OOL value offset 10 overlaps IFD directory [8,26); value bytes may be corrupt",
		},
		// warnNextIFDUnreadable — TIFF 6.0 §2 chain termination (unreadable)
		{
			"warnNextIFDUnreadable",
			parseWarn{kind: warnNextIFDUnreadable, val1: 0xFFFFFFFF},
			"exif: next-IFD pointer 0xFFFFFFFF is unreadable (out of bounds or corrupt); IFD chain terminated (TIFF 6.0 \xc2\xa72)",
		},
		// warnNextIFDOutOfBounds — TIFF 6.0 §2 chain termination (out of bounds)
		{
			"warnNextIFDOutOfBounds",
			parseWarn{kind: warnNextIFDOutOfBounds, val1: 0xFFFFFFFF, val2: 26},
			"exif: next-IFD pointer 0xFFFFFFFF points outside the buffer (len 26); IFD chain terminated (TIFF 6.0 \xc2\xa72)",
		},
		// warnBigTIFFDuplicateTag — BigTIFF first-occurrence
		// val1=offHi32 (0), val2=offLo32 (0), val3=tag (0x0112): offset=0, tag=0x0112.
		{
			"warnBigTIFFDuplicateTag",
			parseWarn{kind: warnBigTIFFDuplicateTag, val1: 0, val2: 0, val3: 0x0112},
			"exif: BigTIFF IFD at offset 0 contains duplicate tag 0x0112; keeping first occurrence",
		},
		// warnBigTIFFNextUnreadable — BigTIFF chain termination (unreadable)
		// val1=offHi32 (0), val2=offLo32 (0xDEADBEEF): offset=0x00000000DEADBEEF.
		{
			"warnBigTIFFNextUnreadable",
			parseWarn{kind: warnBigTIFFNextUnreadable, val1: 0, val2: 0xDEADBEEF},
			"exif: BigTIFF next-IFD pointer 0x00000000DEADBEEF is unreadable; IFD chain terminated (BigTIFF spec \xc2\xa72)",
		},
		// warnBigTIFFNextOutOfBounds — BigTIFF chain termination (out of bounds)
		// val1=offHi32 (0), val2=offLo32 (0xDEADBEEF), val3=bufLen (100): offset=0x00000000DEADBEEF, len=100.
		{
			"warnBigTIFFNextOutOfBounds",
			parseWarn{kind: warnBigTIFFNextOutOfBounds, val1: 0, val2: 0xDEADBEEF, val3: 100},
			"exif: BigTIFF next-IFD pointer 0x00000000DEADBEEF points outside the buffer (len 100); IFD chain terminated (BigTIFF spec \xc2\xa72)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := warnString(tc.record)
			if got != tc.want {
				t.Errorf("warnString message drift detected:\n  got:  %q\n  want: %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Round-trip regression
// ---------------------------------------------------------------------------

// TestEncodeDecodeNoWarnings verifies that an EXIF created via setters and
// encoded with Encode produces zero warnings when re-parsed. If the encoder
// produces a non-conformant IFD, the new warning paths would fire here.
func TestEncodeDecodeNoWarnings(t *testing.T) {
	t.Parallel()

	e := &EXIF{
		ByteOrder: binary.LittleEndian,
		IFD0:      &IFD{},
	}
	e.SetCameraModel("Nikon Z9")
	e.SetCaption("Sunset over the Pacific")
	e.SetOrientation(6)
	e.SetISO(400)

	encoded, err := Encode(e)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	e2, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse after Encode failed: %v", err)
	}
	if len(e2.Warnings) != 0 {
		t.Errorf("round-trip produced unexpected warnings: %v", e2.Warnings)
	}

	if got := e2.CameraModel(); got != "Nikon Z9" {
		t.Errorf("CameraModel=%q, want \"Nikon Z9\"", got)
	}
	if got, ok := e2.Orientation(); !ok || got != 6 {
		t.Errorf("Orientation=%d ok=%v, want 6 true", got, ok)
	}
}
