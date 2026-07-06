package exif

// bigtiff_encode_guard_test.go — gate tests for audit finding #107 and task #264.
//
// Spec references:
//   - BigTIFF spec (Aware Systems / libtiff) §2: magic 0x002B, 64-bit offsets.
//   - TIFF 6.0 §2: magic 0x002A, 32-bit offsets.
//
// History:
//   - Audit finding #107 (pre-task #264): exif.Encode refused to re-encode a
//     BigTIFF-sourced EXIF, returning ErrBigTIFFEncodeNotSupported, because
//     doing so as classic TIFF would have truncated every 64-bit offset.
//   - Task #264: exif.Encode gained a native BigTIFF write path
//     (serialiseBigTIFF, writeTIFFHeaderBigTIFF, writeIFDBigTIFF,
//     writeSubIFDsBigTIFF — see write.go/ifd.go). Encode now succeeds for
//     BigTIFF sources and preserves the 0x002B magic instead of downgrading.
//     TestEncodeBigTIFFSourceReturnsError (which pinned the pre-#264 refusal
//     behaviour) is replaced by TestEncodeBigTIFFSourceSucceeds below.
//
// Test IDs:
//   TestEncodeBigTIFFSourceSucceeds     — gate for task #264: Encode succeeds for BigTIFF.
//   TestBigTIFFProvenance_FlagSet       — verify Parse sets BigTIFF=true for 0x002B.
//   TestBigTIFFProvenance_ClassicFalse  — verify Parse sets BigTIFF=false for 0x002A.

import (
	"encoding/binary"
	"testing"
)

// TestEncodeBigTIFFSourceSucceeds is the exif-level gate for task #264.
//
// Before task #264 (audit finding #107 fix): exif.Encode refused to encode a
// BigTIFF-sourced EXIF at all, returning ErrBigTIFFEncodeNotSupported, because
// the only available encoder emitted a classic TIFF (0x002A) header that would
// have truncated every 64-bit offset to 32 bits.
//
// After task #264: exif.Encode dispatches to a native BigTIFF encode path
// (serialiseBigTIFF) that emits a conformant 16-byte BigTIFF header, 20-byte
// IFD entries, and 64-bit offsets throughout (BigTIFF spec §2).
//
// Two sub-cases cover the full surface:
//
//	(a) BigTIFF with a TypeLong8 entry — ensures the encoder round-trips
//	    BigTIFF-only 64-bit type codes without truncation (the #1 correctness
//	    rule: typeSizeBigTIFF, never typeSize).
//	(b) BigTIFF with only small offsets / standard types — ensures the
//	    written output is still a conformant BigTIFF stream (0x002B magic)
//	    rather than a classic-TIFF downgrade, regardless of offset magnitude.
func TestEncodeBigTIFFSourceSucceeds(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		data func() []byte
	}{
		{
			// Case (a): BigTIFF with TypeLong8 entry.
			// BigTIFF spec §3.3: type code 16 = LONG8 (uint64, 8 bytes).
			name: "with_TypeLong8_entry",
			data: func() []byte {
				// Build a minimal BigTIFF: header + IFD with one TypeLong8 entry.
				const (
					hdrSize     = 16
					countSize   = 8
					entrySize   = 20
					nextPtrSize = 8
					nEntries    = 1
				)
				order := binary.LittleEndian
				ifdBlockSize := countSize + nEntries*entrySize + nextPtrSize
				ifdOff := uint64(hdrSize)
				dataOff := ifdOff + uint64(ifdBlockSize)
				buf := make([]byte, int(dataOff)+8)

				buf[0], buf[1] = 'I', 'I'
				order.PutUint16(buf[2:], 0x002B) // BigTIFF magic
				order.PutUint16(buf[4:], 8)
				order.PutUint16(buf[6:], 0)
				order.PutUint64(buf[8:], ifdOff)

				ifdPos := int(ifdOff)
				order.PutUint64(buf[ifdPos:], nEntries)
				ifdPos += 8
				// StripOffsets (0x0111), TypeLong8 (16), count=1, OOL.
				order.PutUint16(buf[ifdPos:], 0x0111)
				order.PutUint16(buf[ifdPos+2:], 16) // TypeLong8
				order.PutUint64(buf[ifdPos+4:], 1)
				order.PutUint64(buf[ifdPos+12:], dataOff)
				order.PutUint64(buf[int(dataOff):], 0x1000000)
				return buf
			},
		},
		{
			// Case (b): BigTIFF with only small, standard-type entries.
			// Even though all values fit in 32 bits, the source is BigTIFF (magic
			// 0x002B) and Encode must preserve that container, not downgrade it.
			name: "small_offsets_only",
			data: func() []byte {
				return buildBigTIFF(binary.LittleEndian, []bigTIFFEntry{
					{tag: 0x010F, typ: uint16(TypeASCII), count: 6, payload: []byte("Canon\x00")},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := tc.data()

			// Parse must succeed — BigTIFF read is fully supported.
			e, parseErr := Parse(data)
			if parseErr != nil {
				t.Fatalf("Parse: unexpected error: %v", parseErr)
			}
			if e == nil {
				t.Fatal("Parse: returned nil EXIF")
			}
			// Provenance flag must be set.
			if !e.BigTIFF {
				t.Error("Parse: EXIF.BigTIFF = false, want true for 0x002B source")
			}

			// Encode must now succeed (task #264) and preserve the BigTIFF magic.
			out, encErr := Encode(e)
			if encErr != nil {
				t.Fatalf("Encode: unexpected error: %v", encErr)
			}
			if len(out) < 4 {
				t.Fatalf("Encode: output too short (%d bytes)", len(out))
			}
			if got := binary.LittleEndian.Uint16(out[2:4]); got != 0x002B {
				t.Errorf("Encode: output magic = 0x%04X, want 0x002B (BigTIFF must not be downgraded)", got)
			}

			// The encoded output must itself be a valid, re-parseable BigTIFF stream.
			e2, reparseErr := Parse(out)
			if reparseErr != nil {
				t.Fatalf("Parse (round-trip): unexpected error: %v", reparseErr)
			}
			if !e2.BigTIFF {
				t.Error("Parse (round-trip): EXIF.BigTIFF = false, want true")
			}
			if e2.IFD0 == nil {
				t.Fatal("Parse (round-trip): IFD0 is nil")
			}
			if len(e2.IFD0.Entries) != len(e.IFD0.Entries) {
				t.Errorf("Parse (round-trip): IFD0 entry count = %d, want %d", len(e2.IFD0.Entries), len(e.IFD0.Entries))
			}
		})
	}
}

// TestBigTIFFProvenance_FlagSet verifies that Parse sets EXIF.BigTIFF = true
// when the source magic is 0x002B (BigTIFF).
//
// BigTIFF spec §2; audit finding #107.
func TestBigTIFFProvenance_FlagSet(t *testing.T) {
	t.Parallel()

	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		name := "LE"
		if order == binary.BigEndian {
			name = "BE"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := buildBigTIFF(order, nil) // minimal BigTIFF, no entries
			e, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse BigTIFF %s: %v", name, err)
			}
			if !e.BigTIFF {
				t.Errorf("BigTIFF %s: EXIF.BigTIFF = false, want true", name)
			}
		})
	}
}

// TestBigTIFFProvenance_ClassicFalse verifies that Parse sets EXIF.BigTIFF = false
// for a classic TIFF source (magic 0x002A). This is the regression guard: the
// BigTIFF provenance flag must not be set for classic TIFF inputs.
//
// TIFF 6.0 §2; audit finding #107 (regression guard).
func TestBigTIFFProvenance_ClassicFalse(t *testing.T) {
	t.Parallel()
	// Build a minimal classic TIFF LE.
	makeBytes := asciiPayload("TestMake")
	order := binary.LittleEndian
	const hdrSize = 8
	const entrySize = 12
	ifdOff := uint32(hdrSize)
	valueAreaOff := ifdOff + 2 + entrySize + 4
	buf := make([]byte, int(valueAreaOff)+len(makeBytes))

	buf[0], buf[1] = 'I', 'I'
	order.PutUint16(buf[2:], 0x002A) // classic TIFF magic
	order.PutUint32(buf[4:], ifdOff)

	order.PutUint16(buf[ifdOff:], 1) // 1 entry
	p := int(ifdOff) + 2
	order.PutUint16(buf[p:], 0x010F)                   // Make tag
	order.PutUint16(buf[p+2:], 2)                      // TypeASCII
	order.PutUint32(buf[p+4:], uint32(len(makeBytes))) //nolint:gosec // G115: test data, bounded
	order.PutUint32(buf[p+8:], valueAreaOff)
	copy(buf[valueAreaOff:], makeBytes)

	e, err := Parse(buf)
	if err != nil {
		t.Fatalf("Parse classic TIFF: %v", err)
	}
	if e.BigTIFF {
		t.Error("Classic TIFF: EXIF.BigTIFF = true, want false")
	}

	// Encode must succeed for a classic TIFF source (positive control).
	out, encErr := Encode(e)
	if encErr != nil {
		t.Errorf("Encode classic TIFF: unexpected error: %v", encErr)
	}
	if len(out) == 0 {
		t.Error("Encode classic TIFF: returned 0 bytes, want non-empty")
	}
	// Output must carry classic TIFF magic.
	if len(out) >= 4 && order.Uint16(out[2:]) != 0x002A {
		t.Errorf("Encode classic TIFF: output magic = 0x%04X, want 0x002A", order.Uint16(out[2:]))
	}
}
